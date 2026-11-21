// Package flow holds the engine's records, settings and rule tables. It is
// pure: no I/O and no clock; the time comes in as an argument
// (docs/dev/engine-v2/design.md sections 2.1 and 13).
package flow

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// Roles of Slice 1 (design.md 9.1).
const (
	RoleCoordinator = "coordinator"
	RoleWorker      = "worker"
	RoleReviewer    = "reviewer"
)

// Round kinds of slice 2 (design.md 3, 4.6).
const (
	RoundFull     = "full"
	RoundDelta    = "delta"
	RoundSendBack = "send_back" // the coordinator's "changes", with a why (design.md 4.9)
)

// State is one project's state.json. Seq counts the inputs committed to it.
type State struct {
	Seq      int64    `json:"seq"`
	Project  Project  `json:"project"`
	Settings Settings `json:"settings"`
	Tasks    []Task   `json:"tasks"`
}

// BaseState is the persisted base-branch red spell (design.md 7.3, N38).
type BaseState struct {
	Red     bool      `json:"red,omitempty"`
	Since   time.Time `json:"since,omitempty"`
	SpellID string    `json:"spell_id,omitempty"`
}

// Project is the project record of the skeleton.
type Project struct {
	ID           string     `json:"id"`
	Folder       string     `json:"folder,omitempty"`
	Goal         string     `json:"goal,omitempty"`
	Started      bool       `json:"started,omitempty"`
	RunID        string     `json:"run_id,omitempty"`           // coordinator terminal identity
	RunTokenHash string     `json:"run_token_sha256,omitempty"` // SHA-256 hex of the coordinator's SHEEPDOG_RUN_TOKEN
	Run          *RunRecord `json:"run,omitempty"`
	Acts         []Act      `json:"acts,omitempty"`
	Wakes        []Wake     `json:"wakes,omitempty"` // held until J12 delivers them
	Base         *BaseState `json:"base,omitempty"`
}

// Wake is a held wake for the coordinator (design.md 10, J12): queued once
// per task and key, delivered in one batch when the coordinator is idle.
// Facts are the task's facts when it was queued, built in code.
type Wake struct {
	Key   string    `json:"key"`
	Task  string    `json:"task,omitempty"`
	Why   string    `json:"why"`
	Facts string    `json:"facts"`
	At    time.Time `json:"at"`
}

// Outcome is one verifiable result of a task (design.md 3, 11.7; prompts.md 3.1):
// what is true when done and how it will be proved (cmd:, test:, or manual:).
type Outcome struct {
	Text     string `json:"text"`
	ProvedBy string `json:"proved_by"`
}

// UnmarshalJSON accepts both {text, proved_by} and legacy plain-string
// outcomes from stored state written before PR 3.2.
func (o *Outcome) UnmarshalJSON(data []byte) error {
	*o = Outcome{}
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &o.Text)
	}
	type alias Outcome
	return json.Unmarshal(data, (*alias)(o))
}

// PriorityUrgent marks a task for priority scheduling and the extra urgent slot (design.md 4.10).
const PriorityUrgent = "urgent"

// Task is a planned task. It has no phase field: rows derive where it is.
type Task struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Brief    string    `json:"brief"`
	Outcomes []Outcome `json:"outcomes"`
	After    []string  `json:"after,omitempty"`
	Priority string    `json:"priority,omitempty"`
	// Scope fields of design.md 3, 11.7 (prompts.md 3.1).
	ForWhom   string `json:"for_whom,omitempty"`
	IfSkipped string `json:"if_skipped,omitempty"`
	Smallest  string `json:"smallest,omitempty"`
	NotDoing  string `json:"not_doing,omitempty"`
	// EstimateLines is the plan's estimate of changed lines (design.md 11.5).
	EstimateLines int                    `json:"estimate_lines"`
	Request       map[string][]Candidate `json:"request,omitempty"`
	SplitFrom     string                 `json:"split_from,omitempty"`
	Done          *Merged                `json:"done,omitempty"`
	Closed        *Closed                `json:"closed,omitempty"`
	Dropped       *Dropped               `json:"dropped,omitempty"`
	Split         *Split                 `json:"split,omitempty"`
	Parked        bool                   `json:"parked,omitempty"`
	LastRow       string                 `json:"last_row,omitempty"`
	Recaps        []Recap                `json:"recaps,omitempty"`
	Acts          []Act                  `json:"acts,omitempty"`
	Attempts      []Attempt              `json:"attempts"`
}

// CIFail records the first time a task commit was observed with a failing check run (N13).
type CIFail struct {
	Head string    `json:"head"`
	At   time.Time `json:"at"`
}

// Merged is the done mark: the task's PR merged at this commit.
type Merged struct {
	MergedCommit string `json:"merged_commit"`
}

// Closed marks a task closed without a merge.
type Closed struct {
	Why string `json:"why"`
}

// Dropped marks a task dropped by the coordinator (design.md 3, 4.9).
type Dropped struct {
	Why string `json:"why"`
}

// Split marks a task split into child tasks (design.md 3, 4.9).
type Split struct {
	Children []string `json:"children"`
}

// Recap is a non-convergence or limit stop waiting on the coordinator (design.md 3).
type Recap struct {
	Trigger string         `json:"trigger"`
	Allowed []string       `json:"allowed"`
	Decided *RecapDecision `json:"decided,omitempty"`
	Amended bool           `json:"amended,omitempty"` // a plan amend changed the brief or outcomes since it opened
}

// RecapDecision is the coordinator's decision closing a recap.
type RecapDecision struct {
	Choice string    `json:"choice"`
	Why    string    `json:"why"`
	At     time.Time `json:"at,omitempty"`
}

// Recap choices and triggers (design.md 4.3, 4.9).
const (
	ChoiceContinue = "continue"
	TriggerReview  = "review"
	TriggerSize    = "size"
	TriggerCI      = "ci"
	TriggerTime    = "time"
)

// SizeRecapAllowed is a size recap's choices: continue only while the task
// has used fewer than continues_per_task (design.md 4.3).
func SizeRecapAllowed(t Task, s Settings) []string {
	used := 0
	for _, r := range t.Recaps {
		if r.Decided != nil && r.Decided.Choice == ChoiceContinue {
			used++
		}
	}
	if used < s.ContinuesPerTask {
		return []string{ChoiceContinue, "split", "new", "drop"}
	}
	return []string{"split", "new", "drop"}
}

// Ended reports whether t carries a terminal mark (design.md 4.1, N1).
func Ended(t Task) bool {
	return t.Done != nil || t.Closed != nil || t.Dropped != nil || t.Split != nil
}

// OpenRecap returns t's undecided recap, or nil when none is open.
func OpenRecap(t Task) *Recap {
	for i := len(t.Recaps) - 1; i >= 0; i-- {
		if t.Recaps[i].Decided == nil {
			return &t.Recaps[i]
		}
	}
	return nil
}

// Attempt is one try at a task, from its base commit.
type Attempt struct {
	BaseCommit   string     `json:"base_commit"`
	Branch       string     `json:"branch"`
	Worktree     string     `json:"worktree"`
	PR           int        `json:"pr"`
	RunID        string     `json:"run_id"`                     // the run's terminal identity
	RunTokenHash string     `json:"run_token_sha256,omitempty"` // SHA-256 hex of the run's SHEEPDOG_RUN_TOKEN
	Run          *RunRecord `json:"run,omitempty"`
	Review       *ReviewRun `json:"review,omitempty"`
	Ready        []Ready    `json:"ready"`
	Rejects      []Reject   `json:"rejects,omitempty"`
	Rounds       []Round    `json:"rounds"`
	Accepts      []Accept   `json:"accepts"`
	Acts         []Act      `json:"acts"`
	Restarts     int        `json:"restarts,omitempty"`
	Held         []string   `json:"held,omitempty"`
	// NothingToChange is the worker's reason for "nothing to change"; a later
	// ready or send-back clears it (design.md 3.2).
	NothingToChange string `json:"nothing_to_change,omitempty"`
	// Boundary is how many rounds were recorded when a plan amend last
	// changed the brief or outcomes: those rounds no longer cover (design.md 3.1).
	Boundary int    `json:"boundary,omitempty"`
	Closed   string `json:"closed,omitempty"` // the note a recap's "new" closed this attempt with
	// SizeCap is N12's limit on changed lines at the tip (design.md 3, 11.5).
	SizeCap int `json:"size_cap"`
	// MergeOnly are H's moves to a tip or PR head that was ahead of it by
	// merges of the base only (N20, N40, design.md 3.1), in order.
	MergeOnly []Advance `json:"merge_only,omitempty"`
	// CIFails records distinct commits of this attempt seen with a failing check run (N13).
	CIFails []CIFail `json:"ci_fails,omitempty"`
	// Working is working-state time accrued by earlier worker runs of this attempt since the last recap decision (N14).
	Working time.Duration `json:"working,omitempty"`
}

// Advance is one merge_only move of H, observed with git when it was made.
type Advance struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// OpenAttempt is t's current attempt, or nil before the first or once a
// recap's "new" closed it.
func OpenAttempt(t Task) *Attempt {
	if n := len(t.Attempts); n > 0 && t.Attempts[n-1].Closed == "" {
		return &t.Attempts[n-1]
	}
	return nil
}

// Counted is a's rounds after its boundary: the only ones that can cover.
func Counted(a Attempt) []Round { return a.Rounds[min(a.Boundary, len(a.Rounds)):] }

// ReviewRun is the active reviewer run started for the attempt's next round.
type ReviewRun struct {
	RunID        string     `json:"run_id,omitempty"`
	RunTokenHash string     `json:"run_token_sha256,omitempty"`
	Run          *RunRecord `json:"run,omitempty"`
	Round        int        `json:"round"`
	Kind         string     `json:"kind,omitempty"`
	Head         string     `json:"head"`
	Code         string     `json:"code"`
	Reviewer     Reviewer   `json:"reviewer"`
	Retries      int        `json:"retries,omitempty"`
	FailedModels []string   `json:"failed_models,omitempty"`
	RefusalSent  bool       `json:"refusal_sent,omitempty"`
}

// Proof is one outcome's evidence attached to a ready report (design.md 3, 7.2):
// either a command run by the report client or a manual artifact path.
type Proof struct {
	Outcome    string `json:"outcome"`
	Command    string `json:"command,omitempty"`
	Exit       int    `json:"exit"`
	OutputPath string `json:"output_path,omitempty"`
	Artifact   string `json:"artifact,omitempty"`
}

// Ready is the worker's "done at this commit".
type Ready struct {
	Head    string  `json:"head"`
	Proofs  []Proof `json:"proofs,omitempty"`
	Touched string  `json:"touched,omitempty"`
	Skipped *string `json:"skipped,omitempty"`
}

// Reject is the worker's rejection of every finding of a reviewer's round at
// Head with no new commit (design.md 3: "A reject is not a round").
type Reject struct {
	Head  string `json:"head"`
	Round int    `json:"round"`
	Why   string `json:"why"`
}

// Reviewer names who reviewed a round.
type Reviewer struct {
	CLI    string `json:"cli"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// Round is one review round (kind: full, delta, retro or send_back).
type Round struct {
	N               int      `json:"n"`
	Kind            string   `json:"kind"`
	Head            string   `json:"head"`
	Reviewer        Reviewer `json:"reviewer"`
	Verdict         string   `json:"verdict"`
	Majors          int      `json:"majors"`
	SelfCheckMisses *int     `json:"self_check_misses,omitempty"`
	Class           string   `json:"class,omitempty"`
	Deletable       string   `json:"deletable,omitempty"`
	NotRun          string   `json:"not_run,omitempty"`
	ReportPath      string   `json:"report_path"`
	Why             string   `json:"why,omitempty"` // a send-back's reason
}

// FirstLookPassRate returns how many tasks passed their first reviewed round
// and how many tasks have at least one reviewed round (design.md 11.2,
// prompts.md 1.2). Coordinator send-backs are not reviewed rounds.
func FirstLookPassRate(tasks []Task) (passed, reviewed int) {
	for _, t := range tasks {
	attempts:
		for _, a := range t.Attempts {
			for _, r := range a.Rounds {
				if !isReviewerRound(r) {
					continue
				}
				reviewed++
				if r.Verdict == VerdictPass {
					passed++
				}
				break attempts
			}
		}
	}
	return passed, reviewed
}

// Accept records that the bar (or an override) accepted a commit.
type Accept struct {
	Head   string `json:"head"`
	By     string `json:"by"` // bar or override
	Rounds []int  `json:"rounds"`
}

// Failure classes and retry marker of section 8 (design.md 4.9, 8).
const (
	ClassTransient, ClassSettled, ClassTimeout, AskRetry = "transient", "settled", "timeout", "retry"
	MaxActErrRunes                                       = 500
)

// Act is one entry of the engine's own ledger: a thing it did or tried.
type Act struct {
	Key       string    `json:"key"`
	Result    string    `json:"result"`
	Class     string    `json:"class,omitempty"`
	Err       string    `json:"err,omitempty"`
	Uncounted bool      `json:"uncounted,omitempty"`
	Tries     int       `json:"tries,omitempty"`
	At        time.Time `json:"at"`
}

// CleanText collapses whitespace and control/format characters to single spaces.
func CleanText(raw string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, raw)), " ")
}

// CapRunes truncates s to at most limit runes.
func CapRunes(s string, limit int) string {
	if r := []rune(s); len(r) > limit {
		return string(r[:limit])
	}
	return s
}

// CleanErr cleans and caps an act's error text for storing and wakes.
func CleanErr(raw string) string { return CapRunes(CleanText(raw), MaxActErrRunes) }

// SinceRetry returns the acts recorded after the latest retry act (or all acts).
func SinceRetry(acts []Act) []Act {
	for i := len(acts) - 1; i >= 0; i-- {
		if acts[i].Key == AskRetry {
			return acts[i+1:]
		}
	}
	return acts
}

func actFailures(acts []Act, key string) (last, cause *Act, tries, counted int, settled bool) {
	s := SinceRetry(acts)
	for i := len(s) - 1; i >= 0; i-- {
		switch {
		case s[i].Key != key:
		case s[i].Result != "failed":
			return &s[i], nil, 0, 0, false
		default:
			if last == nil {
				last = &s[i]
			}
			tries += max(1, s[i].Tries)
			if !s[i].Uncounted {
				if cause == nil {
					cause = &s[i]
				}
				counted, settled = counted+1, settled || s[i].Class == ClassSettled
			}
		}
	}
	return last, cause, tries, counted, settled
}

func doneActs(acts []Act, key string) []Act {
	if strings.HasPrefix(key, AskMerge+"@") || strings.HasPrefix(key, AskUpdateBranch+"@") || strings.HasPrefix(key, AskTellDiverged+"@") ||
		strings.HasPrefix(key, AskNudgeReview+"@") || strings.HasPrefix(key, AskStopReview+"@") {
		return SinceRetry(acts)
	}
	return acts
}

// Exhausted reports whether key has hit ensure_failures in a row or a settled
// failure since the last retry, with no done act (design.md 8, N5).
func Exhausted(acts []Act, key string, s Settings) bool {
	_, _, _, n, settled := actFailures(acts, key)
	return !Done(doneActs(acts, key), key) && (settled || n > 0 && n >= s.EnsureFailures)
}

// HasExhausted reports whether any key in acts is currently exhausted
// (design.md 8, N5).
func HasExhausted(acts []Act, s Settings) bool {
	seen := map[string]bool{}
	for i := len(acts) - 1; i >= 0 && acts[i].Key != AskRetry; i-- {
		k := acts[i].Key
		if acts[i].Result == "failed" && !seen[k] {
			seen[k] = true
			if Exhausted(acts, k, s) {
				return true
			}
		}
	}
	return false
}

// Done reports whether an act with key succeeded ("done").
func Done(acts []Act, key string) bool {
	for _, a := range acts {
		if a.Key == key && a.Result == "done" {
			return true
		}
	}
	return false
}

// Count is how many times key was tried, whatever the result.
func Count(acts []Act, key string) int {
	n := 0
	for _, a := range acts {
		if a.Key == key {
			n++
		}
	}
	return n
}

// LastAct returns the newest act with key, or nil if none exists.
func LastAct(acts []Act, key string) *Act {
	for i := len(acts) - 1; i >= 0; i-- {
		if acts[i].Key == key {
			return &acts[i]
		}
	}
	return nil
}
