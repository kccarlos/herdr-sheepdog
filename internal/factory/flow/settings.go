package flow

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

// WorkflowFile is a dispatch_workflow value: exactly one file name, never
// a path, a flag, or a number or name gh would resolve to another workflow.
var WorkflowFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.ya?ml$`)

// PassInterval is the loop's tick. It is a constant, not a setting
// (design.md 10.1: the engine's own machinery is not tunable).
const PassInterval = 15 * time.Second

// Settings holds the numbers the rows use. A version ships only the
// settings its own rows use (design.md 10.1).
type Settings struct {
	ReviewRounds               int `json:"review_rounds"`
	MaxRounds                  int `json:"max_rounds"`                     // L12 (design.md 10.1)
	RetroAfterRoundsWithMajors int `json:"retro_after_rounds_with_majors"` // L12 (design.md 10.1)
	RetroFromRound             int `json:"retro_from_round"`               // L12 (design.md 10.1)
	ChecksGraceMinutes         int `json:"checks_grace_minutes"`           // N35, N36 (design.md 10.1)
	CIStalledMinutes           int `json:"ci_stalled_minutes"`             // N36 (design.md 10.1)
	MainRedMinutes             int `json:"main_red_minutes"`               // N38 (design.md 10.1)
	CIFailsPerHour             int `json:"ci_fails_per_hour"`              // N13 (design.md 10.1)
	BuildHours                 int `json:"build_hours"`                    // N14 (design.md 10.1)
	FlakeReruns                int `json:"flake_reruns"`                   // N33, N34 (design.md 10.1)
	StartMinutes               int `json:"start_minutes"`                  // R11, R14 (design.md 10.1)
	IdleGraceSeconds           int `json:"idle_grace_seconds"`             // R18–R20 (design.md 10.1)
	QuietMinutes               int `json:"quiet_minutes"`                  // R21 (design.md 10.1)
	InterruptSeconds           int `json:"interrupt_seconds"`              // R21, R22 (design.md 10.1)
	ReviewMinutes              int `json:"review_minutes"`                 // N29 (design.md 10.1)
	BlockedMinutes             int `json:"blocked_minutes"`                // R29 (design.md 10.1)
	WorkerRestarts             int `json:"worker_restarts"`                // 4.8 (design.md 10.1)
	ReviewerRetries            int `json:"reviewer_retries"`               // 4.8 (design.md 10.1)
	EnsureFailures             int `json:"ensure_failures"`                // N5, section 8 (design.md 10.1)
	EnsureBackoffMinutes       int `json:"ensure_backoff_minutes"`         // 4.8, section 8 (design.md 10.1)
	CoordinatorStartsPerHour   int `json:"coordinator_starts_per_hour"`    // J6 (design.md 10.1)
	CoordinatorGapMinutes      int `json:"coordinator_gap_minutes"`        // J6 (design.md 10.1)
	CoordinatorSilentMinutes   int `json:"coordinator_silent_minutes"`     // J9 (design.md 10.1)
	HandoffMinutes             int `json:"handoff_minutes"`                // R31, J11 (design.md 10.1)
	HeartbeatMinutes           int `json:"heartbeat_minutes"`              // J13 (design.md 10.1)
	ContinuesPerTask           int `json:"continues_per_task"`             // a recap's continue (design.md 4.3, 10.1)
	ContinueLines              int `json:"continue_lines"`                 // the cap after a continue (design.md 4.9)
	TaskSizeLimit              int `json:"task_size_limit"`                // plan checks (design.md 11.5)
	SizeCapFactor              int `json:"size_cap_factor"`                // N12: size_cap = factor × estimate_lines
	MaxRunning                 int `json:"max_running"`                    // N10: concurrent tasks (design.md 4.10, 10.1)
	SweepMinutes               int `json:"sweep_minutes"`                  // sweep interval (design.md 7.3, 10.1)
	PoolFloorPercent           int `json:"pool_floor_percent"`             // 7.3: minimum percent left to start on a pool (design.md 10.1)
	PoolMinutes                int `json:"pool_minutes"`                   // 7.3: minutes a shared pool reading stays fresh (design.md 10.1)
	// SecondReviewer reviews the second passing round (L8): opus in a
	// fresh session, or codex (world.CodexArgv's model).
	SecondReviewer string `json:"second_reviewer"`
	// DispatchWorkflow is the one workflow file under .github/workflows
	// that N35 may dispatch; empty never dispatches (design.md 9.4).
	DispatchWorkflow string `json:"dispatch_workflow,omitempty"`
	// Models maps each role to its ordered candidate list (design.md 9.1, 10.1).
	Models map[string][]Candidate `json:"models,omitempty"`
}

// Range limits for each setting.
const (
	ReviewRoundsMin                       = 1
	ReviewRoundsMax                       = 5
	MaxRoundsMax                          = 10
	RetroMajorsMin                        = 2
	RetroMajorsMax                        = 4
	RetroFromMin                          = 2
	RetroFromMax                          = 6
	ChecksGraceMin                        = 1
	ChecksGraceMax                        = 60
	CIStalledMin                          = 10
	CIStalledMax                          = 600
	MainRedMin                            = 5
	MainRedMax                            = 600
	CIFailsPerHourMin                     = 1
	CIFailsPerHourMax                     = 10
	BuildHoursMin, BuildHoursMax          = 1, 48
	FlakeRerunsMax                        = 3
	StartMin                              = 1
	StartMax                              = 15
	IdleGraceMin                          = 5
	IdleGraceMax                          = 120
	QuietMin                              = 5
	QuietMax                              = 120
	InterruptMin                          = 10
	InterruptMax                          = 600
	ReviewMin, ReviewMax                  = 10, 240
	BlockedMin                            = 1
	BlockedMax                            = 120
	WorkerRestartsMax, ReviewerRetriesMax = 10, 5
	EnsureFailuresMin, EnsureFailuresMax  = 1, 10
	EnsureBackoffMin, EnsureBackoffMax    = 1, 60
	ContinuesMax                          = 3
	ContinueLinesMax                      = 500
	SizeLimitMin                          = 50
	SizeLimitMax                          = 2000
	SizeFactorMin                         = 1
	SizeFactorMax                         = 5
	MaxRunningMin                         = 1
	MaxRunningMax                         = 20
	SweepMin, SweepMax                    = 1, 60
)

const CoordStartsMin, CoordStartsMax, CoordGapMin, CoordGapMax = 1, 10, 1, 60
const CoordSilentMin, CoordSilentMax, HandoffMin, HandoffMax, HeartbeatMin, HeartbeatMax = 5, 240, 1, 30, 10, 600
const PoolFloorMax, PoolMin, PoolMax = 50, 1, 60

// DefaultSettings are the engine's defaults: two passing rounds on the final head
// (design.md 10.1, D12).
func DefaultSettings() Settings {
	return Settings{
		ReviewRounds: 2, MaxRounds: 5,
		RetroAfterRoundsWithMajors: 2, RetroFromRound: 3,
		ChecksGraceMinutes: 5, CIStalledMinutes: 90, MainRedMinutes: 60, CIFailsPerHour: 3, BuildHours: 4, FlakeReruns: 1,
		StartMinutes: 2, IdleGraceSeconds: 20, QuietMinutes: 20, InterruptSeconds: 90, ReviewMinutes: 45, BlockedMinutes: 10,
		WorkerRestarts: 3, ReviewerRetries: 2, EnsureFailures: 3, EnsureBackoffMinutes: 1,
		CoordinatorStartsPerHour: 3, CoordinatorGapMinutes: 10, CoordinatorSilentMinutes: 30, HandoffMinutes: 5, HeartbeatMinutes: 60,
		ContinuesPerTask: 1, ContinueLines: 60, TaskSizeLimit: 400, SizeCapFactor: 2, MaxRunning: 3,
		PoolFloorPercent: 5, PoolMinutes: 5, SweepMinutes: 5,
		SecondReviewer: "opus",
	}
}

// Changed lists every setting that differs from its default as "name
// value", sorted, for the coordinator's facts block (design.md 10). A new
// setting adds its line here.
func (s Settings) Changed() []string {
	d := DefaultSettings()
	var out []string
	for _, c := range []struct {
		name     string
		got, def int
	}{
		{"blocked_minutes", s.BlockedMinutes, d.BlockedMinutes},
		{"build_hours", s.BuildHours, d.BuildHours},
		{"checks_grace_minutes", s.ChecksGraceMinutes, d.ChecksGraceMinutes},
		{"ci_fails_per_hour", s.CIFailsPerHour, d.CIFailsPerHour},
		{"ci_stalled_minutes", s.CIStalledMinutes, d.CIStalledMinutes},
		{"continue_lines", s.ContinueLines, d.ContinueLines},
		{"continues_per_task", s.ContinuesPerTask, d.ContinuesPerTask},
		{"coordinator_gap_minutes", s.CoordinatorGapMinutes, d.CoordinatorGapMinutes},
		{"coordinator_silent_minutes", s.CoordinatorSilentMinutes, d.CoordinatorSilentMinutes},
		{"coordinator_starts_per_hour", s.CoordinatorStartsPerHour, d.CoordinatorStartsPerHour},
		{"ensure_backoff_minutes", s.EnsureBackoffMinutes, d.EnsureBackoffMinutes},
		{"ensure_failures", s.EnsureFailures, d.EnsureFailures},
		{"flake_reruns", s.FlakeReruns, d.FlakeReruns},
		{"handoff_minutes", s.HandoffMinutes, d.HandoffMinutes},
		{"heartbeat_minutes", s.HeartbeatMinutes, d.HeartbeatMinutes},
		{"idle_grace_seconds", s.IdleGraceSeconds, d.IdleGraceSeconds},
		{"interrupt_seconds", s.InterruptSeconds, d.InterruptSeconds},
		{"main_red_minutes", s.MainRedMinutes, d.MainRedMinutes},
		{"max_rounds", s.MaxRounds, d.MaxRounds},
		{"max_running", s.MaxRunning, d.MaxRunning},
		{"pool_floor_percent", s.PoolFloorPercent, d.PoolFloorPercent},
		{"pool_minutes", s.PoolMinutes, d.PoolMinutes},
		{"quiet_minutes", s.QuietMinutes, d.QuietMinutes},
		{"retro_after_rounds_with_majors", s.RetroAfterRoundsWithMajors, d.RetroAfterRoundsWithMajors},
		{"retro_from_round", s.RetroFromRound, d.RetroFromRound},
		{"review_minutes", s.ReviewMinutes, d.ReviewMinutes},
		{"review_rounds", s.ReviewRounds, d.ReviewRounds},
		{"reviewer_retries", s.ReviewerRetries, d.ReviewerRetries},
		{"size_cap_factor", s.SizeCapFactor, d.SizeCapFactor},
		{"start_minutes", s.StartMinutes, d.StartMinutes},
		{"sweep_minutes", s.SweepMinutes, d.SweepMinutes},
		{"task_size_limit", s.TaskSizeLimit, d.TaskSizeLimit},
		{"worker_restarts", s.WorkerRestarts, d.WorkerRestarts},
	} {
		if c.got != c.def {
			out = append(out, fmt.Sprintf("%s %d", c.name, c.got))
		}
	}
	if s.SecondReviewer != d.SecondReviewer {
		out = append(out, "second_reviewer "+s.SecondReviewer)
	}
	if s.DispatchWorkflow != "" {
		out = append(out, "dispatch_workflow "+s.DispatchWorkflow)
	}
	if len(s.Models) > 0 {
		out = append(out, "models "+strings.Join(slices.Sorted(maps.Keys(s.Models)), ","))
	}
	return out
}

// Validate refuses a value outside its range, naming the range.
func (s Settings) Validate() error {
	for _, r := range []struct {
		name         string
		v, low, high int
	}{
		{"review_rounds", s.ReviewRounds, ReviewRoundsMin, ReviewRoundsMax},
		{"max_rounds", s.MaxRounds, s.ReviewRounds, MaxRoundsMax},
		{"retro_after_rounds_with_majors", s.RetroAfterRoundsWithMajors, RetroMajorsMin, RetroMajorsMax},
		{"retro_from_round", s.RetroFromRound, RetroFromMin, RetroFromMax},
		{"checks_grace_minutes", s.ChecksGraceMinutes, ChecksGraceMin, ChecksGraceMax},
		{"ci_stalled_minutes", s.CIStalledMinutes, CIStalledMin, CIStalledMax},
		{"main_red_minutes", s.MainRedMinutes, MainRedMin, MainRedMax},
		{"ci_fails_per_hour", s.CIFailsPerHour, CIFailsPerHourMin, CIFailsPerHourMax},
		{"build_hours", s.BuildHours, BuildHoursMin, BuildHoursMax},
		{"flake_reruns", s.FlakeReruns, 0, FlakeRerunsMax},
		{"start_minutes", s.StartMinutes, StartMin, StartMax},
		{"idle_grace_seconds", s.IdleGraceSeconds, IdleGraceMin, IdleGraceMax},
		{"quiet_minutes", s.QuietMinutes, QuietMin, QuietMax},
		{"interrupt_seconds", s.InterruptSeconds, InterruptMin, InterruptMax},
		{"review_minutes", s.ReviewMinutes, ReviewMin, ReviewMax},
		{"blocked_minutes", s.BlockedMinutes, BlockedMin, BlockedMax},
		{"worker_restarts", s.WorkerRestarts, 0, WorkerRestartsMax},
		{"reviewer_retries", s.ReviewerRetries, 0, ReviewerRetriesMax},
		{"ensure_failures", s.EnsureFailures, EnsureFailuresMin, EnsureFailuresMax},
		{"ensure_backoff_minutes", s.EnsureBackoffMinutes, EnsureBackoffMin, EnsureBackoffMax},
		{"coordinator_starts_per_hour", s.CoordinatorStartsPerHour, CoordStartsMin, CoordStartsMax},
		{"coordinator_gap_minutes", s.CoordinatorGapMinutes, CoordGapMin, CoordGapMax},
		{"coordinator_silent_minutes", s.CoordinatorSilentMinutes, CoordSilentMin, CoordSilentMax},
		{"handoff_minutes", s.HandoffMinutes, HandoffMin, HandoffMax},
		{"heartbeat_minutes", s.HeartbeatMinutes, HeartbeatMin, HeartbeatMax},
		{"continues_per_task", s.ContinuesPerTask, 0, ContinuesMax},
		{"continue_lines", s.ContinueLines, 0, ContinueLinesMax},
		{"task_size_limit", s.TaskSizeLimit, SizeLimitMin, SizeLimitMax},
		{"size_cap_factor", s.SizeCapFactor, SizeFactorMin, SizeFactorMax},
		{"max_running", s.MaxRunning, MaxRunningMin, MaxRunningMax},
		{"sweep_minutes", s.SweepMinutes, SweepMin, SweepMax},
		{"pool_floor_percent", s.PoolFloorPercent, 0, PoolFloorMax},
		{"pool_minutes", s.PoolMinutes, PoolMin, PoolMax},
	} {
		if r.v < r.low || r.v > r.high {
			return fmt.Errorf("%s %d: allowed %d–%d", r.name, r.v, r.low, r.high)
		}
	}
	if s.SecondReviewer != "opus" && s.SecondReviewer != "codex" {
		return fmt.Errorf("second_reviewer %q: allowed opus or codex", s.SecondReviewer)
	}
	if w := s.DispatchWorkflow; w != "" && !WorkflowFile.MatchString(w) {
		return fmt.Errorf("dispatch_workflow %q: a .yml or .yaml file name under .github/workflows (letters, digits, . _ -; no path)", w)
	}
	return ValidateCandidates("models", s.Models)
}
