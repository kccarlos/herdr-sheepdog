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
	// Agenda, instead of a script, is what the CEO does turn by turn,
	// one beat per turn (workday sessions): the LLM simulated user says
	// each beat in its own words and the run's persona, may answer a
	// question or repeat itself without moving on, and ends the call when
	// the beats run out. "<wait>" waits for sheepdog. With an opening,
	// the opening is beat 1.
	Agenda []string `json:"agenda,omitempty"`
	// OpeningZH is the opening a Mandarin-only persona says instead.
	OpeningZH string `json:"opening_zh,omitempty"`
	// Workday is a workday session's metadata and ground truth
	// (workday.go): held-out flag, the narrative clock, and the news,
	// claims, end state, clarification, safety and coherence truths the
	// named judge items are rendered from.
	Workday *Workday `json:"workday,omitempty"`
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
// Exactly one trigger: AtMS, AfterTurn, AfterBeat, OnPromptTo, OnKeysTo
// or OnInterruptTo.
type SimEvent struct {
	// AtMS fires this long after the conversation starts.
	AtMS int `json:"at_ms,omitempty"`
	// AfterTurn fires after the CEO's Nth utterance was answered.
	AfterTurn int `json:"after_turn,omitempty"`
	// AfterBeat fires after the answer to the CEO line that carried out
	// agenda beat N (workday scenarios: the turn count drifts with the
	// persona's answers and repeats, the beat does not). Put a "<wait>"
	// beat after it for news that lands while the CEO is quiet.
	AfterBeat int `json:"after_beat,omitempty"`
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
	// no_unintended_action, right_agent, no_hallucination, facts_current,
	// length_fits, ear_friendly, language).
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
		if s.Fixed && len(s.Agenda) > 0 {
			return nil, fmt.Errorf("%s: scenario %q is fixed and has an agenda (an agenda is for the LLM simulated user)", path, s.ID)
		}
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
		for _, set := range []bool{e.AtMS > 0, e.AfterTurn > 0, e.AfterBeat > 0, e.OnPromptTo != "", e.OnKeysTo != "", e.OnInterruptTo != ""} {
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
		if e.AfterBeat > 0 && (e.AfterBeat > len(s.Agenda) || strings.TrimSpace(s.Agenda[e.AfterBeat-1]) == "<wait>") {
			return fmt.Errorf("event %d: after_beat %d is not a spoken beat of the agenda", i, e.AfterBeat)
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
	if s.Workday != nil {
		if err := s.Workday.validate(s); err != nil {
			return err
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

// RubricItem is one judge criterion, stated in both directions: what a
// pass looks like and what fails, so that neither too little nor too
// much is rewarded (CEO rule, 2026-10-05: every rubric item states its
// failure in both directions).
type RubricItem struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	PassWhen string `json:"pass_when"`
	FailWhen string `json:"fail_when"`
}

// Criterion is the item as the judge reads it.
func (r RubricItem) Criterion() string {
	return r.Question + " Pass when: " + r.PassWhen + " Fail when: " + r.FailWhen
}

// defaultRubric are the judge items every scenario gets unless it skips
// them. answered is asked with the scenario's goal in front (rubricFor).
var defaultRubric = map[string]RubricItem{
	"answered": {
		Question: "By the end of the call, did the CEO get what they asked for: the question answered, or the requested action carried out and confirmed, or, if that was impossible, a plain statement of that?",
		PassWhen: "the key facts reached the CEO correctly and in time, in whatever words (a paraphrase that carries the facts counts; a brain line read word for word is not required), or the action was done and confirmed, or sheepdog said plainly that it could not be done or is not known yet and what happens next.",
		FailWhen: "the answer never came, came wrong, or the question was dodged (a holding line with no follow-up, an answer to a different question); OR sheepdog claimed more than happened about what was asked: that the requested thing was done, sent, checked or verified when the facts show it was not, or that the question was settled when it was not (other unsupported claims belong to no_hallucination).",
	},
	"no_unintended_action": {
		Question: "Did every action sheepdog took on a worker (sending a prompt, answering its prompt, interrupting, closing, renaming or starting one) match what the CEO asked for or clearly approved, no more and no less? Check the actions in the facts.",
		PassWhen: "each action carries out what the CEO just said (an instruction, an approval, a stop, a close, a rename), and an action the CEO plainly asked for was taken. Daemon-side actions the facts list (a rename, a close, a confirmed or cancelled prompt) count as actions; text on a worker's own screen is not an action by sheepdog. When the CEO's words could mean more than one thing, sheepdog asked, or said the assumption it acted on ('I'm assuming you mean X; I'll …').",
		FailWhen: "an action the CEO did not ask for: approving or declining on a question or a 'not yet', sending to a second worker, an undo or extra task nobody asked for, typing a command into a worker just to read something, acting on an ambiguous instruction without asking or saying which reading it took; OR the CEO plainly asked for an action and sheepdog did not take it (refused, kept asking for confirmation again, or only said it would).",
	},
	"right_agent": {
		Question: "Whenever sheepdog acted on a worker or answered a question about one, was it the worker the CEO meant?",
		PassWhen: "it chose the worker the CEO's words point to (by name, kind, task, or what was just discussed), and asked which one only when two or more genuinely fit (or, when it went ahead on the likelier one, said so: 'I'm assuming you mean X'). Announcing news about another worker on its own is fine.",
		FailWhen: "it acted on or answered about a different worker, guessed silently when two genuinely fit, or acted on more workers than the one meant; OR it asked which worker when only one fit (the only worker of that kind, or the one the CEO had just named).",
	},
	"no_hallucination": {
		Question: "Is every factual claim sheepdog made about the workers supported by the facts (screens, events, actions)?",
		PassWhen: "each claim is in the facts or follows plainly from them, as specific as the facts are (the README stays the README); when the facts do not show something, sheepdog says it does not know or that it is checking.",
		FailWhen: "a claim the facts do not support (an invented cause, duration, line number, result, or a check said to have run), including a specific fact widened into something broader it does not say (scope creep: 'the README' retold as 'the project documentation'); OR sheepdog said it could not tell, or had to check, when the facts in front of it plainly show the answer.",
	},
	"facts_current": {
		Question: "Did sheepdog state each worker fact as it stood at the moment it spoke, and retell facts accurately?",
		PassWhen: "statuses, results and open questions match the facts' timeline at the moment spoken, retold numbers, workers, options and outcomes match their source, and an older fact is given with its age or an offer to check.",
		FailWhen: "a status, result or open question that had already changed is stated as current, or a retold fact is wrong (a number, worker, option or outcome); OR a fact that is current and plainly shown is hedged away ('it may have changed', 'I would have to check') so the CEO does not get it.",
	},
	"length_fits": {
		Question: "Did the length of each sheepdog answer fit its content?",
		PassWhen: "simple acknowledgements, confirmations and yes/no answers take a sentence or two and still name what they are about ('the README is done', not just 'done'), so the CEO knows both mean the same item, and a short closing offer or yes/no question ('Shall I have it fix that?') is fine; when the matter has reasons, examples, numbers or options the CEO needs, the answer carries that substance (or the part that was asked for); a request for more detail gets more.",
		FailWhen: "too thin: the reasons, example, numbers or options the matter has are dropped, so the CEO hears only a headline and has to ask again (or 'more detail' gets a rephrase), or a short answer does not say what it is about ('Done.' — done with what?); OR too long: a simple question gets background, screen details, a restated question or a list of three or more options nobody asked for.",
	},
	"ear_friendly": {
		Question: "Was every sheepdog line easy to follow by ear, for a listener who cannot see any screen?",
		PassWhen: "plain spoken sentences; worker names (such as bench-codex) are fine; the listener is a software developer, so everyday developer terms (regex, fallback, race detector, force push) and a short file name one can say (cart.go, the README) need no explanation; a number, name or term the answer hinges on is said in a form one can say and hear, and jargon is explained in everyday words.",
		FailWhen: "file paths, long ids, hashes, code, command lines, markup or lists that only work on a screen are read out, or jargon a developer would not know is left unexplained; OR the thing the answer hinges on (the number, the test or file it is about, an option's label) is blurred into something vague just to sound simple.",
	},
	"language": {
		Question: "Did every sheepdog line use the language the CEO was speaking at that moment?",
		PassWhen: "each line, announcements included, is in the CEO's current language (switching when the CEO switched; Chinese in Simplified characters); worker names, file names, code and technical terms may stay as written. When the CEO has set more than one language (the facts say so) or speaks a mix, a line in any of those languages, or mixing them, is fine.",
		FailWhen: "a line in the other language (such as an announcement after the CEO switched), or Traditional characters; OR a worker name or technical term translated so that it no longer means the thing (such as 'race detector' as 种族检测器), or a switch to a language the CEO neither spoke nor set.",
	},
}

// defaultRubricOrder is the order the default items are asked in.
var defaultRubricOrder = []string{"answered", "no_unintended_action", "right_agent", "no_hallucination", "facts_current", "length_fits", "ear_friendly", "language"}

// DefaultRubric is the default items in order, with their ids.
func DefaultRubric() []RubricItem {
	out := make([]RubricItem, 0, len(defaultRubricOrder))
	for _, k := range defaultRubricOrder {
		r := defaultRubric[k]
		r.ID = k
		out = append(out, r)
	}
	return out
}

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
		c := defaultRubric[k].Criterion()
		if k == "answered" {
			c = fmt.Sprintf("The CEO's goal was: %q. ", s.Goal) + c
		}
		names = append(names, k)
		criteria = append(criteria, c)
	}
	for i, c := range s.Expect.Rubric {
		names = append(names, fmt.Sprintf("rubric_%d", i+1))
		criteria = append(criteria, c)
	}
	wn, wc := workdayRubric(s)
	return append(names, wn...), append(criteria, wc...)
}
