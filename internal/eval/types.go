// Package eval implements `sheepdog eval` (docs/roles.md, phase 1b): a
// conversation eval set for the assistant prompt (prompts/assistant.md)
// and the supervisor brain (internal/brain), built from the CEO's real
// voice sessions (generalised) plus the plan's §10 scenarios.
//
// Two sets, two files: testdata/eval/assistant.jsonl (one JSON object per
// line, an AssistantCase) and testdata/eval/svp.jsonl (an SVPCase).
// Offline (the default) validates the files and, for the SVP set, runs
// them through the brain with a scripted fake model so the harness itself
// (the rail, provenance wrapping) is exercised without any network call;
// the assistant set is not runnable without a real model, so offline it
// is only validated. --live calls gemini-3.5-flash in plain text mode
// (never Live audio) for both sets.
package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// AssistantExpect is what one assistant case requires of the model's tool
// choice. Since D1-revised the voice has two tools and decides nothing,
// so most cases are "relayed verbatim": Tool to_supervisor with Verbatim.
type AssistantExpect struct {
	// Tool is to_supervisor, stop, or none (no function call: small
	// talk, or a follow-up the last reply already answers).
	Tool string `json:"tool"`
	// Verbatim requires the tool's text argument to be exactly the
	// case's utterance, trimmed: the voice relays, it never paraphrases
	// and never adds a guess about the agent.
	Verbatim bool `json:"verbatim,omitempty"`
	// Agent, when non-nil, is the exact agent/alias argument expected
	// ("" means the model must leave it empty rather than guess). The
	// two tools take no agent argument, so this is only for a case that
	// checks the model does not invent one.
	Agent *string `json:"agent,omitempty"`
	// QuestionContains: the tool's text argument must contain at least
	// one of these substrings (case-insensitive), when non-empty.
	QuestionContains []string `json:"question_contains,omitempty"`
}

// AssistantCase is one line of testdata/eval/assistant.jsonl.
type AssistantCase struct {
	ID        string `json:"id"`
	Utterance string `json:"utterance"`
	// Context is prior transcript lines, oldest first. A line starting
	// "Dog: " is the assistant's own prior turn (model role); anything
	// else — including a CEO utterance verbatim, or a push with its
	// "[system event, not spoken by the user] " / "[system context, not
	// spoken by the user] " prefix exactly as internal/voice/gemini's
	// pushTurn renders it — is a user-role turn, unchanged.
	Context []string `json:"context,omitempty"`
	// Aliases are the {aliases} substitution for this case's system
	// prompt; defaults to ["auth", "docs"] when empty.
	Aliases []string        `json:"aliases,omitempty"`
	Expect  AssistantExpect `json:"expect"`
}

// SVPOption is one option of a pending approval prompt.
type SVPOption struct {
	Label string   `json:"label"`
	Keys  []string `json:"keys"`
}

// SVPPending sets up a recorded approval prompt on an agent (for the one
// rail's cases).
type SVPPending struct {
	Question string      `json:"question"`
	Options  []SVPOption `json:"options,omitempty"`
	YesKeys  []string    `json:"yes_keys,omitempty"`
	NoKeys   []string    `json:"no_keys,omitempty"`
}

// SVPAgent is one roster row the case sets up (screen and, optionally, a
// pending prompt come from the case's own fields for the primary agent;
// Agents lets a case add more agents, e.g. for an ambiguity case).
type SVPAgent struct {
	Alias   string      `json:"alias"`
	Kind    string      `json:"kind,omitempty"`
	State   string      `json:"state,omitempty"`
	Screen  string      `json:"screen,omitempty"` // fixture name under testdata/herdr, or inline text
	Pending *SVPPending `json:"pending,omitempty"`
}

// SVPRequest is the request the brain receives: an utterance, the
// shepherd's words verbatim, and the brain decides what they are. Agent
// is an optional alias hint the live daemon never sends; the cases
// converted from the removed ask/propose kinds keep theirs, so they test
// the same loop behaviour as before.
type SVPRequest struct {
	Kind     string `json:"kind"` // utterance (the only kind; empty means utterance)
	Question string `json:"question"`
	Agent    string `json:"agent"`
	// Briefing is the roster sentence composeBriefing puts in a live
	// request, so a status case can be answered without a tool call.
	Briefing string `json:"briefing,omitempty"`
}

// SVPExpect is what one SVP case requires.
type SVPExpect struct {
	// ToolsUsedAnyOf: at least one of these tool names must have been
	// called, when non-empty (live: any brain.tool_call; offline: the
	// scripted call is derived from this list, so it is a sanity check).
	ToolsUsedAnyOf []string `json:"tools_used_any_of,omitempty"`
	// SpokenContainsAnyOf: the reply's spoken text must contain at least
	// one of these substrings, case-insensitive (live only).
	SpokenContainsAnyOf []string `json:"spoken_contains_any_of,omitempty"`
	// SpokenMaxSentences: the reply's spoken text may have at most this
	// many sentences, when positive (live only): a finding is the gist in
	// two sentences plus an offer (RULES.md answer contract).
	SpokenMaxSentences int `json:"spoken_max_sentences,omitempty"`
	// MustNotCall: none of these tools may complete successfully (a
	// rail-blocked attempt is fine — that is the rail working).
	MustNotCall []string `json:"must_not_call,omitempty"`
	// RailBlocked, when true, requires a brain.rail_blocked event.
	RailBlocked bool `json:"rail_blocked,omitempty"`
	// NoTool requires the turn to answer without calling anything: a
	// status question is answered from the roster and ledger already in
	// the prompt (docs/roles.md "One decision-maker").
	NoTool bool `json:"no_tool,omitempty"`
}

// SVPCase is one line of testdata/eval/svp.jsonl.
type SVPCase struct {
	ID string `json:"id"`
	// Transcript is prior transcript lines, oldest first: "CEO: …", "Dog:
	// …", or "PUSH <alias>: …"; anything else is treated as a CEO
	// utterance.
	Transcript []string `json:"transcript,omitempty"`
	// ScreenFixture is the primary agent's screen: a file name under
	// testdata/herdr, or a path.
	ScreenFixture string `json:"screen_fixture"`
	// Agents adds roster rows beyond the primary one (Request.Agent, or
	// "auth" when Request.Agent is empty); rarely needed (ambiguity
	// cases).
	Agents  []SVPAgent  `json:"agents,omitempty"`
	Pending *SVPPending `json:"pending,omitempty"`
	// Notes seeds the Host's CEO notes (recall/USER.md ledger cases).
	Notes   []string   `json:"notes,omitempty"`
	Request SVPRequest `json:"request"`
	Expect  SVPExpect  `json:"expect"`
}

// LoadAssistantCases reads one AssistantCase per line.
func LoadAssistantCases(path string) ([]AssistantCase, error) {
	var out []AssistantCase
	if err := loadJSONL(path, func(line []byte) error {
		var c AssistantCase
		if err := json.Unmarshal(line, &c); err != nil {
			return err
		}
		out = append(out, c)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// LoadSVPCases reads one SVPCase per line.
func LoadSVPCases(path string) ([]SVPCase, error) {
	var out []SVPCase
	if err := loadJSONL(path, func(line []byte) error {
		var c SVPCase
		if err := json.Unmarshal(line, &c); err != nil {
			return err
		}
		out = append(out, c)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func loadJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Bytes()
		trimmed := trimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if err := fn(trimmed); err != nil {
			return fmt.Errorf("%s:%d: %w", path, n, err)
		}
	}
	return sc.Err()
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}
