// Package eval implements `sheepdog eval` (docs/roles.md, phase 1b): a
// conversation eval set for the assistant prompt (prompts/assistant.md)
// and the supervisor brain (internal/brain), built from the CEO's real
// voice sessions (generalised) plus the plan's §10 scenarios.
//
// The hand-written sets are testdata/eval/assistant.jsonl (one JSON
// object per line, an AssistantCase) and testdata/eval/svp.jsonl (an
// SVPCase). The L0 corpus of docs/iteration-2.md WP0b adds the five
// logged failures (regressions*.jsonl), cases extracted from the event
// log (extracted/, Extract) and generated variants (expanded*.jsonl,
// internal/eval/gen). Offline (the default) validates the files and runs
// every SVP case through the brain with a scripted fake model, so the
// harness (the rail, provenance wrapping, tool calls) is exercised without
// any network call; the checks about phrasing and judgement are live only
// and reported as skipped, and a case that carries its logged behaviour is
// graded against it. The assistant cases are not runnable without a real
// model, so offline they are only validated. --live calls
// gemini-3.5-flash in plain text mode (never Live audio).
package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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
	// ToolAnyOf: the tool must be one of these (to_supervisor, stop,
	// none), when non-empty — for a case where more than one choice is
	// right but one is wrong ("Oh, never mind." may be relayed or let go,
	// never a stop: docs/iteration-2.md J6).
	ToolAnyOf []string `json:"tool_any_of,omitempty"`
}

// AssistantObserved is what the voice model actually did in the logged
// session a case came from: the baseline the case's expectations are
// graded against offline (see Meta.BaselineFails).
type AssistantObserved struct {
	Tool string `json:"tool"`
	Text string `json:"text,omitempty"`
}

// CaseSource is where a case came from in the event log, so it can be
// traced back: the day file, the event's time and its id
// (docs/iteration-2.md rule 1: every change cites a failure from a real
// log).
type CaseSource struct {
	Day     string `json:"day"`
	TS      string `json:"ts"`
	EventID int64  `json:"event_id"`
	// Heard is what the transcript recorded the shepherd saying, when
	// it differs from what the voice relayed (the relay is the request's
	// question): the GPT-Live partial relay of 2026-09-23 23:53:26.
	Heard string `json:"heard,omitempty"`
}

// Meta is the bookkeeping every case of the L0 corpus carries
// (docs/iteration-2.md WP0b): which job it exercises, where it came from,
// what the harness cannot check yet, and how it was generated.
type Meta struct {
	// Job is the product job (J1…J6 of docs/iteration-2.md, with a
	// sub-intent such as J2-yes or J6-nevermind); the paraphrase
	// generator substitutes utterances within the same job only.
	Job string `json:"job,omitempty"`
	// Note says why the case exists (for a regression: the log time).
	Note string `json:"note,omitempty"`
	// PendingCheck names what the case should assert but the harness
	// cannot yet (e.g. that the supervisor actually speaks a line).
	PendingCheck string      `json:"pending_check,omitempty"`
	Source       *CaseSource `json:"source,omitempty"`
	// Generator and Seed record how an expanded case was made
	// (internal/eval/gen): "paraphrase", "noise:<op>", or both joined by
	// "+"; the seed makes it reproducible.
	Generator string `json:"generator,omitempty"`
	Seed      int64  `json:"seed,omitempty"`
	// Parent is the id of the case an expanded case was generated from.
	Parent string `json:"parent,omitempty"`
	// BaselineFails, when set, says whether the case's expectations must
	// fail (true) or pass (false) against the logged behaviour in
	// Observed: a regression case proves it catches the logged failure
	// by failing on it, deterministically and offline. Unset, the
	// baseline grade is reported but never fails the run.
	BaselineFails *bool `json:"baseline_fails,omitempty"`
}

// AssistantCase is one line of testdata/eval/assistant.jsonl.
type AssistantCase struct {
	Meta
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
	Aliases  []string           `json:"aliases,omitempty"`
	Expect   AssistantExpect    `json:"expect"`
	Observed *AssistantObserved `json:"observed,omitempty"`
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
	// PaneID is the herdr pane (extracted cases keep the logged one);
	// empty gets a synthetic id.
	PaneID  string      `json:"pane_id,omitempty"`
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
	// Kind is utterance (empty means utterance) or announce: the event
	// turn the supervisor runs on a worker's block or finish, so a
	// finish-delivery case (docs/iteration-2.md J4) exercises the turn
	// that decides the line.
	Kind     string `json:"kind"`
	Question string `json:"question"`
	Agent    string `json:"agent"`
	// Briefing is the roster sentence composeBriefing puts in a live
	// request, so a status case can be answered without a tool call.
	Briefing string `json:"briefing,omitempty"`
	// Event, Template, Owed and Screen are an announce turn's
	// (brain.Request): the worker event (done, blocked, idle), the code
	// sentence the brain's line replaces, whether the shepherd is waiting
	// on it, and the screen the supervisor read for it (inline text).
	Event    string `json:"event,omitempty"`
	Template string `json:"template,omitempty"`
	Owed     bool   `json:"owed,omitempty"`
	Screen   string `json:"screen,omitempty"`
}

// ObservedCall is one tool call the logged brain made (brain.tool_call),
// or, for a stop the voice carried out, the interrupt it amounted to.
type ObservedCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// SVPObserved is what the logged system actually did for the case's
// request: the baseline its expectations are graded against offline.
type SVPObserved struct {
	ToolCalls []ObservedCall `json:"tool_calls,omitempty"`
	Spoken    string         `json:"spoken,omitempty"`
	NeedsUser *bool          `json:"needs_user,omitempty"`
	Urgency   string         `json:"urgency,omitempty"`
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
	// PromptMustNotContain: no prompt the brain sends a worker may
	// contain any of these substrings — e.g. "[" for text the brain
	// invented to fill a partial relay (regression
	// partial-relay-no-fabrication, 2026-09-23 23:53:26).
	PromptMustNotContain []string `json:"prompt_must_not_contain,omitempty"`
	// SpokenMaxWords: the spoken reply may have at most this many words,
	// when positive (live only; J1/J3 ≤ 20, WP1's ear contract ≤ 30).
	SpokenMaxWords int `json:"spoken_max_words,omitempty"`
	// SpokenNoJargon: the spoken reply may use none of the jargon terms
	// (live only; J5 "simpler than the previous line").
	SpokenNoJargon bool `json:"spoken_no_jargon,omitempty"`
	// FirstSentenceContainsAnyOf: the spoken reply's first sentence must
	// contain one of these, case-insensitive (live only; J1 "the item
	// that needs the shepherd comes first").
	FirstSentenceContainsAnyOf []string `json:"first_sentence_contains_any_of,omitempty"`
	// NotStopWord requires the request's words not to be a bare stop
	// word (supervisor.BareStopWords, which internal/inject's Router for
	// GPT-Live reads too): a bare stop word never reaches the brain, the
	// daemon runs the stop procedure, Ctrl-C included. Checked offline
	// and live, since it is code, not judgement (regression
	// never-mind-is-not-stop, iteration 2 WP1).
	NotStopWord bool `json:"not_stop_word,omitempty"`
	// NeedsUser, when set, is the reply's required needs_user (live
	// only: the fake model cannot judge it).
	NeedsUser *bool `json:"needs_user,omitempty"`
	// UrgencyNot: an announce reply's urgency may be none of these (live
	// only); "skip" also rejects an empty urgency with an empty spoken,
	// which the supervisor treats as a skip.
	UrgencyNot []string `json:"urgency_not,omitempty"`
}

// SVPCase is one line of testdata/eval/svp.jsonl (and of the
// regressions, extracted and expanded sets).
type SVPCase struct {
	Meta
	ID string `json:"id"`
	// Transcript is prior transcript lines, oldest first: "CEO: …", "Dog:
	// …", or "PUSH <alias>: …"; anything else is treated as a CEO
	// utterance.
	Transcript []string `json:"transcript,omitempty"`
	// ScreenFixture is the primary agent's screen: a file name under
	// testdata/herdr, or a path.
	ScreenFixture string `json:"screen_fixture,omitempty"`
	// Agents adds roster rows beyond the primary one (Request.Agent, or
	// "auth" when Request.Agent is empty); rarely needed (ambiguity
	// cases).
	Agents []SVPAgent `json:"agents,omitempty"`
	// RosterOnly makes Agents the whole roster: no synthetic primary
	// agent is added (extracted cases carry the logged roster, where an
	// utterance names no agent and there is no "auth").
	RosterOnly bool        `json:"roster_only,omitempty"`
	Pending    *SVPPending `json:"pending,omitempty"`
	// Notes seeds the Host's CEO notes (recall/USER.md ledger cases).
	Notes    []string     `json:"notes,omitempty"`
	Request  SVPRequest   `json:"request"`
	Expect   SVPExpect    `json:"expect"`
	Observed *SVPObserved `json:"observed,omitempty"`
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

// WriteJSONL writes one JSON object per line: the case files' format.
// HTML escaping is off so a transcript's "<", ">" and "&" stay readable
// in review.
func WriteJSONL[T any](w io.Writer, items []T) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return err
		}
	}
	return nil
}
