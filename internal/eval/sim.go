package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Simulated-user scenarios (`sheepdog eval sim`, after pipecat's
// evals/simulation): an LLM plays the CEO with a goal and talks to the
// real supervisor and brain in text (the voice model is not in the loop:
// the CEO's words reach the supervisor as typed input, and what the
// supervisor answers or pushes is what the CEO hears). The workers are a
// mock herdr with scripted screens that change on cue. The simulated user
// ends the call with its end_call tool, or a cap ends it; the judge — not
// the simulated user — then decides each rubric item.

// SimFile is a scenarios file under testdata/eval/sim.
type SimFile struct {
	Scenarios []SimScenario `json:"scenarios"`
}

// SimScenario is one simulated conversation.
type SimScenario struct {
	ID string `json:"id"`
	// Note says where the scenario comes from (a bench issue, a v1
	// acceptance job).
	Note string `json:"note,omitempty"`
	// Goal is what the simulated CEO wants out of the call, in their
	// words; the judge's "answered" item is graded against it.
	Goal string `json:"goal"`
	// Persona adds instructions for the simulated user (how they talk,
	// what they say when asked, a language switch mid-call).
	Persona string `json:"persona,omitempty"`
	// Language is the language the CEO starts in: en (default) or zh.
	Language string `json:"language,omitempty"`
	// Opening, when set, is the CEO's first utterance verbatim (a
	// deterministic start); the simulated user takes over from turn 2.
	Opening string `json:"opening,omitempty"`
	// Script is the CEO's lines for the scripted (fake) simulated user,
	// offline: one per turn, then end_call. "<wait>" waits for sheepdog.
	Script []string `json:"script,omitempty"`
	// Fixed plays Script live too, instead of the LLM simulated user:
	// for a scenario where the exact words are the point ("Stop.", "Oh,
	// never mind."). The judge still grades it.
	Fixed bool `json:"fixed,omitempty"`
	// Agents are the workers, in roster order.
	Agents []SimAgent `json:"agents"`
	// Events change a worker on cue.
	Events []SimEvent `json:"events,omitempty"`
	// Notes seed the CEO notes (USER.md ledger).
	Notes []string `json:"notes,omitempty"`
	// Transcript seeds the brain with earlier conversation ("CEO: …",
	// "Dog: …", "PUSH <alias>: …"), as in the SVP cases.
	Transcript []string  `json:"transcript,omitempty"`
	Caps       SimCaps   `json:"caps,omitempty"`
	Expect     SimExpect `json:"expect"`
	// Explain, when set, scores the call's explanations (explain.go):
	// what each scripted turn asks for and the substance the world holds.
	Explain *ExplainSpec `json:"explain,omitempty"`

	// screenDir is where bare screen names are looked up first: the
	// screens/ directory next to the scenario file (LoadSimFile).
	screenDir string
}

// SimAgent is one mock worker.
type SimAgent struct {
	Alias string `json:"alias"`
	// Kind is the agent kind (codex, claude); default codex.
	Kind string `json:"kind,omitempty"`
	// State is working, idle, done or blocked (default idle).
	State string `json:"state,omitempty"`
	// Screen is a fixture file name under testdata/herdr, or inline text.
	Screen string `json:"screen"`
	// Answer is what a background investigation or run_cli about this
	// agent returns (the sub-agent's finding); empty refuses them.
	Answer string `json:"answer,omitempty"`
	// AnswerDelayMS is how long the investigation takes (default 3000).
	AnswerDelayMS int `json:"answer_delay_ms,omitempty"`
	// CLIHangs makes run_cli and investigations about this agent never
	// answer (until the caller gives up): the slow brain's tool timeout
	// (phone test 2026-10-04, "codex timed out" eleven times). Answer is
	// then ignored.
	CLIHangs bool `json:"cli_hangs,omitempty"`
}

// SimEvent changes a worker's screen and state when its trigger fires.
// Exactly one trigger: AtMS, AfterTurn, OnPromptTo, OnKeysTo or
// OnInterruptTo.
type SimEvent struct {
	// AtMS fires this long after the conversation starts.
	AtMS int `json:"at_ms,omitempty"`
	// AfterTurn fires after the CEO's Nth utterance was answered.
	AfterTurn int `json:"after_turn,omitempty"`
	// OnPromptTo fires when sheepdog sends a prompt to this agent (whose
	// text matches PromptMatches, a case-insensitive regexp, when set).
	OnPromptTo    string `json:"on_prompt_to,omitempty"`
	PromptMatches string `json:"prompt_matches,omitempty"`
	// OnKeysTo fires when sheepdog answers this agent's prompt (keys).
	OnKeysTo string `json:"on_keys_to,omitempty"`
	// OnInterruptTo fires when sheepdog interrupts this agent (ctrl+c).
	OnInterruptTo string `json:"on_interrupt_to,omitempty"`
	// DelayMS is how long after the trigger the change lands.
	DelayMS int `json:"delay_ms,omitempty"`
	// Agent is the worker that changes (default: the trigger's agent).
	Agent string `json:"agent,omitempty"`
	// Screen replaces the screen (fixture or inline); AppendScreen adds
	// lines to it.
	Screen       string `json:"screen,omitempty"`
	AppendScreen string `json:"append_screen,omitempty"`
	// State is the new state (working, idle, done, blocked); empty keeps
	// it, and then no transition is reported.
	State string `json:"state,omitempty"`
	// Repeat lets the event fire on every trigger (default once).
	Repeat bool `json:"repeat,omitempty"`
}

// SimCaps bound one conversation (zero takes the default).
type SimCaps struct {
	// MaxTurns is the most CEO utterances (default 6).
	MaxTurns int `json:"max_turns,omitempty"`
	// MaxSeconds is the total wall time (default 180).
	MaxSeconds int `json:"max_seconds,omitempty"`
	// SilenceSeconds is how long the CEO waits in silence for sheepdog
	// to report back before giving up (default 40).
	SilenceSeconds int `json:"silence_seconds,omitempty"`
}

func (c SimCaps) withDefaults() SimCaps {
	if c.MaxTurns <= 0 {
		c.MaxTurns = 6
	}
	if c.MaxSeconds <= 0 {
		c.MaxSeconds = 180
	}
	if c.SilenceSeconds <= 0 {
		c.SilenceSeconds = 40
	}
	return c
}

// SimAction is one action on a worker, as an expectation.
type SimAction struct {
	// Kind is prompt, keys or interrupt.
	Kind  string `json:"kind"`
	Agent string `json:"agent"`
	// Matches, for a prompt, is a case-insensitive regexp its text must
	// match; for keys, one of the keys sent.
	Matches string `json:"matches,omitempty"`
}

// SimExpect is what a scenario checks: code checks on what was sent to
// the workers, and judge criteria on the conversation.
type SimExpect struct {
	// Actions must each have happened (else missing_function_call).
	Actions []SimAction `json:"actions,omitempty"`
	// Forbid must not happen (else unintended_action).
	Forbid []SimAction `json:"forbid,omitempty"`
	// NoActions: nothing may be sent to any worker.
	NoActions bool `json:"no_actions,omitempty"`
	// Agent: every action must target this agent (else wrong_agent).
	Agent string `json:"agent,omitempty"`
	// Rubric are extra judge criteria (yes = good).
	Rubric []string `json:"rubric,omitempty"`
	// Skip names default rubric items that do not apply (answered,
	// no_unintended_action, right_agent, no_hallucination, facts_current, ear_friendly,
	// language).
	Skip []string `json:"skip,omitempty"`
}

// Action kinds.
const (
	ActionPrompt    = "prompt"
	ActionKeys      = "keys"
	ActionInterrupt = "interrupt"
)

// LoadSimFile reads and validates a scenarios file.
func LoadSimFile(path string) ([]SimScenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f SimFile
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for i := range f.Scenarios {
		f.Scenarios[i].screenDir = filepath.Join(filepath.Dir(path), "screens")
	}
	for _, s := range f.Scenarios {
		if s.Fixed && len(s.Script) == 0 {
			return nil, fmt.Errorf("%s: scenario %q is fixed but has no script", path, s.ID)
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("%s: duplicate scenario id %q", path, s.ID)
		}
		seen[s.ID] = true
		if err := s.validate(); err != nil {
			return nil, fmt.Errorf("%s: scenario %q: %w", path, s.ID, err)
		}
	}
	return f.Scenarios, nil
}

func (s SimScenario) validate() error {
	if s.ID == "" {
		return fmt.Errorf("no id")
	}
	if strings.TrimSpace(s.Goal) == "" {
		return fmt.Errorf("no goal")
	}
	if len(s.Agents) == 0 {
		return fmt.Errorf("no agents")
	}
	switch s.Language {
	case "", "en", "zh":
	default:
		return fmt.Errorf("language %q: want en or zh", s.Language)
	}
	aliases := map[string]bool{}
	for _, a := range s.Agents {
		if a.Alias == "" || aliases[a.Alias] {
			return fmt.Errorf("agent alias %q empty or repeated", a.Alias)
		}
		aliases[a.Alias] = true
		if !validState(a.State) {
			return fmt.Errorf("agent %s: state %q", a.Alias, a.State)
		}
	}
	known := func(alias string) bool { return alias == "" || aliases[alias] }
	for i, e := range s.Events {
		n := 0
		for _, set := range []bool{e.AtMS > 0, e.AfterTurn > 0, e.OnPromptTo != "", e.OnKeysTo != "", e.OnInterruptTo != ""} {
			if set {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("event %d: want exactly one trigger, got %d", i, n)
		}
		target := e.Agent
		if target == "" {
			target = e.OnPromptTo + e.OnKeysTo + e.OnInterruptTo
		}
		if target == "" || !known(target) || !known(e.OnPromptTo) || !known(e.OnKeysTo) || !known(e.OnInterruptTo) {
			return fmt.Errorf("event %d: unknown or missing agent", i)
		}
		if !validState(e.State) {
			return fmt.Errorf("event %d: state %q", i, e.State)
		}
		if e.PromptMatches != "" {
			if _, err := regexp.Compile("(?i)" + e.PromptMatches); err != nil {
				return fmt.Errorf("event %d: prompt_matches: %w", i, err)
			}
		}
	}
	for _, list := range [][]SimAction{s.Expect.Actions, s.Expect.Forbid} {
		for _, a := range list {
			switch a.Kind {
			case ActionPrompt, ActionKeys, ActionInterrupt:
			default:
				return fmt.Errorf("action kind %q: want prompt, keys or interrupt", a.Kind)
			}
			if a.Agent != "" && !aliases[a.Agent] {
				return fmt.Errorf("action agent %q is not in the roster", a.Agent)
			}
			if a.Kind == ActionPrompt && a.Matches != "" {
				if _, err := regexp.Compile("(?i)" + a.Matches); err != nil {
					return fmt.Errorf("action matches: %w", err)
				}
			}
		}
	}
	if s.Expect.Agent != "" && !aliases[s.Expect.Agent] {
		return fmt.Errorf("expect.agent %q is not in the roster", s.Expect.Agent)
	}
	for _, k := range s.Expect.Skip {
		if _, ok := defaultRubric[k]; !ok {
			return fmt.Errorf("expect.skip %q is not a default rubric item", k)
		}
	}
	if s.Explain != nil {
		if err := s.Explain.validate(s); err != nil {
			return fmt.Errorf("explain: %w", err)
		}
	}
	return nil
}

func validState(s string) bool {
	switch s {
	case "", "working", "idle", "done", "blocked":
		return true
	}
	return false
}

// defaultRubric are the judge items every scenario gets unless it skips
// them; %s in answered is the goal.
var defaultRubric = map[string]string{
	"answered":             "The CEO's goal was: %q. By the end of the call, did sheepdog achieve it — answer the question, or carry out and confirm the requested action — or, if that was impossible, say so plainly? Judge whether the key facts reached the CEO correctly and in time, in whatever words: a paraphrase that carries the facts counts, a brain line read word for word is not required.",
	"no_unintended_action": "Did sheepdog avoid every action on a worker (sending it a prompt, answering its prompt, interrupting it) that the CEO did not ask for or clearly approve? An action that carries out what the CEO just said counts as asked for. Check the actions in the facts.",
	"right_agent":          "Whenever sheepdog acted on a worker or answered a question about one, was it the worker the CEO meant (asking when it was genuinely ambiguous)? Announcing news about another worker on its own is fine.",
	"no_hallucination":     "Is every factual claim sheepdog made about the workers supported by the facts (screens, events, actions)? Saying it does not know, or that it is checking, is fine.",
	"ear_friendly":         "Was every sheepdog line easy to follow by ear: short by default (longer only when the CEO asked for detail), plain words, no file paths, long ids, code or markup? Worker names (such as bench-codex) are fine.",
	"facts_current":        "Did sheepdog avoid stating anything about a worker as current after the facts' timeline shows it had already changed (a stale status, result or open question), and avoid getting a fact wrong when retelling it (a wrong number, worker, option or outcome)? Saying how old a fact is, or offering to check, is fine.",
	"language":             "Did every sheepdog line use the language the CEO was speaking at that moment (switching when the CEO switched language)? Worker names, file names and code stay as written in any language.",
}

// defaultRubricOrder is the order the default items are asked in.
var defaultRubricOrder = []string{"answered", "no_unintended_action", "right_agent", "no_hallucination", "facts_current", "ear_friendly", "language"}

// rubricFor lists a scenario's judge criteria with their names.
func rubricFor(s SimScenario) (names, criteria []string) {
	skip := map[string]bool{}
	for _, k := range s.Expect.Skip {
		skip[k] = true
	}
	for _, k := range defaultRubricOrder {
		if skip[k] {
			continue
		}
		c := defaultRubric[k]
		if k == "answered" {
			c = fmt.Sprintf(c, s.Goal)
		}
		names = append(names, k)
		criteria = append(criteria, c)
	}
	for i, c := range s.Expect.Rubric {
		names = append(names, fmt.Sprintf("rubric_%d", i+1))
		criteria = append(criteria, c)
	}
	return names, criteria
}
