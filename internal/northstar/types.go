// Package northstar computes the eval suite's north-star block
// (docs/dev/handover-2026-10-04.md "Evals first", step 2) and the
// programmatic checks under it, from recorded runs and event logs:
//
//   - said vs done: every claim in what sheepdog said that something was
//     sent, stopped, verified, done, started, closed, approved or declined,
//     matched to an action event and its outcome within the turn; and every
//     action the user was never told about;
//   - missed news: worker finishes, blocks, results and failed spawns that
//     no later speech conveyed;
//   - user effort: turns and time to the goal, repeats and rephrasings;
//   - the four north-star rates (Understood, Done, Low effort, Safe) with
//     95 % confidence intervals, and an A/B comparison of two result sets.
//
// It is measurement only: the claim phrases here are matched against what
// sheepdog said after the fact and never steer the product. The package is
// a leaf (no imports from the rest of sheepdog); internal/eval,
// internal/bench, internal/bench/tier3 and internal/scorecard turn their
// records into a Run.
package northstar

// Side is who produced a line of speech: the orchestrator (the orchestrator's
// spoken text: tool results and pushes) or the voice model (what the phone
// played). At tier 1 there is no voice model and the user hears the
// orchestrator.
type Side string

// The sides.
const (
	SideVoice        Side = "voice"
	SideOrchestrator Side = "orchestrator"
)

// Speech is one line sheepdog said.
type Speech struct {
	AtMS int64  `json:"at_ms"`
	Side Side   `json:"side"`
	Text string `json:"text"`
	// Push marks a line sheepdog said on its own (not a reply).
	Push bool `json:"push,omitempty"`
}

// UserLine is one thing the user said.
type UserLine struct {
	AtMS int64  `json:"at_ms"`
	Text string `json:"text"`
}

// ActionKind is what an action did.
type ActionKind string

// The action kinds. Check is a read whose result can back a "verified"
// claim (run_cli, a finished investigation, a screen read); CheckStarted is
// one that only started (an investigation job).
const (
	ActPrompt       ActionKind = "prompt"
	ActKeys         ActionKind = "keys"
	ActInterrupt    ActionKind = "interrupt"
	ActSpawn        ActionKind = "spawn"
	ActClose        ActionKind = "close"
	ActCheck        ActionKind = "check"
	ActCheckStarted ActionKind = "check_started"
)

// Action outcomes.
const (
	OutcomeOK      = "ok"
	OutcomeError   = "error"
	OutcomeTimeout = "timeout"
	OutcomeBlocked = "blocked"
)

// Action is one thing sheepdog did to the world.
type Action struct {
	AtMS    int64      `json:"at_ms"`
	Kind    ActionKind `json:"kind"`
	Agent   string     `json:"agent,omitempty"`
	Detail  string     `json:"detail,omitempty"`
	Outcome string     `json:"outcome"`
	// Answer is yes or no for keys that answered a prompt, when known.
	Answer string `json:"answer,omitempty"`
	// Tool is the orchestrator tool behind a check (run_cli, investigate,
	// read_screen).
	Tool string `json:"tool,omitempty"`
}

// visible reports whether the user should be told about the action: it
// changed a worker (checks are reads).
func (a Action) visible() bool {
	switch a.Kind {
	case ActPrompt, ActKeys, ActInterrupt, ActSpawn, ActClose:
		return a.Outcome == OutcomeOK
	}
	return false
}

// NewsKind is a kind of event the user should hear about.
type NewsKind string

// The news kinds.
const (
	NewsFinished    NewsKind = "finished"
	NewsBlocked     NewsKind = "blocked"
	NewsResult      NewsKind = "result"
	NewsSpawnFailed NewsKind = "spawn_failed"
)

// News is one event the user should hear about.
type News struct {
	AtMS   int64    `json:"at_ms"`
	Kind   NewsKind `json:"kind"`
	Agent  string   `json:"agent,omitempty"`
	Detail string   `json:"detail,omitempty"`
}

// Agent is a worker and the names it may be called by.
type Agent struct {
	Alias  string `json:"alias"`
	Kind   string `json:"kind,omitempty"`
	Spoken string `json:"spoken,omitempty"`
}

// Verdict is a judge item's answer (yes, no, or err).
type Verdict struct {
	Name   string `json:"name"`
	Answer string `json:"answer"`
}

// Failure is a labelled failure of a run (eval's taxonomy labels).
type Failure struct {
	Type   string `json:"type"`
	Detail string `json:"detail"`
}

// EndCheck is one end-state check's outcome (tier 3: against the world
// after the call).
type EndCheck struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// Run is one recorded conversation, in the form the checks read.
type Run struct {
	Tier     string `json:"tier"`
	Scenario string `json:"scenario"`
	Repeat   int    `json:"repeat"`
	// Status is pass, fail, error or not_run (eval's run statuses).
	Status    string `json:"status"`
	EndReason string `json:"end_reason,omitempty"`
	// GoalMetSelf is the simulated user's own opinion at end_call.
	GoalMetSelf *bool      `json:"goal_met_self,omitempty"`
	User        []UserLine `json:"user,omitempty"`
	Speech      []Speech   `json:"speech,omitempty"`
	Actions     []Action   `json:"actions,omitempty"`
	News        []News     `json:"news,omitempty"`
	// NewsOutside counts news a real log had while nobody was talking
	// (not graded).
	NewsOutside int `json:"news_outside,omitempty"`
	// Initial are the workers' states when the call started (done or
	// blocked): evidence for a "done" claim, never news to convey.
	Initial []News  `json:"initial,omitempty"`
	Agents  []Agent `json:"agents,omitempty"`
	// EndMS is when the call ended (the last line or event).
	EndMS    int64     `json:"end_ms"`
	Verdicts []Verdict `json:"verdicts,omitempty"`
	// Segments are the segment judge's verdicts (eval.JudgeSegments),
	// when the run was judged per segment.
	Segments []SegmentVerdicts `json:"segments,omitempty"`
	// NotExercised are the beat items (clarify_N, coherence_N) no
	// segment scored: the probe never happened, so neither pass nor fail.
	NotExercised []string  `json:"not_exercised,omitempty"`
	Failures     []Failure `json:"failures,omitempty"`
	// EndState are the declared end-state checks' outcomes (tier 3).
	EndState []EndCheck `json:"end_state,omitempty"`
	// ExpectsActions: the scenario requires actions on the workers, so
	// the run has an intended end state in the (mock) world.
	ExpectsActions bool    `json:"expects_actions,omitempty"`
	Turns          int     `json:"turns"`
	DurationMS     int64   `json:"duration_ms"`
	CostUSD        float64 `json:"cost_usd"`
	// LatencyMS are per-turn total latencies (turn.latency total_ms at
	// tier 2, brain.turn dur_ms of the user's turns otherwise).
	LatencyMS []float64 `json:"latency_ms,omitempty"`
	// Heard is the side the user hears: voice when a voice model was in
	// the loop, else orchestrator.
	Heard Side `json:"heard"`
	// HasRecord: the transcript and actions are known (false for a run
	// rebuilt from a report summary only).
	HasRecord bool `json:"has_record"`
	// HasEvents: the daemon's event log was read (checks, spawns, closes,
	// investigations, the orchestrator's text at tier 2).
	HasEvents bool `json:"has_events"`
}

// graded reports whether the run ran to a verdict.
func (r Run) graded() bool { return r.Status == "pass" || r.Status == "fail" }

// hasFailure reports whether the run carries a failure of one of types.
func (r Run) hasFailure(types ...string) bool {
	for _, f := range r.Failures {
		for _, t := range types {
			if f.Type == t {
				return true
			}
		}
	}
	return false
}

// verdict returns the named verdict's answer, "" when not asked.
func (r Run) verdict(name string) string {
	for _, v := range r.Verdicts {
		if v.Name == name {
			return v.Answer
		}
	}
	return ""
}
