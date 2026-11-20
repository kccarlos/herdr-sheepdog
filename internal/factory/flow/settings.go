package flow

import (
	"fmt"
	"regexp"
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
	FlakeReruns                int `json:"flake_reruns"`                   // N33, N34 (design.md 10.1)
	StartMinutes               int `json:"start_minutes"`                  // R11, R14 (design.md 10.1)
	IdleGraceSeconds           int `json:"idle_grace_seconds"`             // R18–R20 (design.md 10.1)
	QuietMinutes               int `json:"quiet_minutes"`                  // R21 (design.md 10.1)
	InterruptSeconds           int `json:"interrupt_seconds"`              // R21, R22 (design.md 10.1)
	BlockedMinutes             int `json:"blocked_minutes"`                // R29 (design.md 10.1)
	StuckMinutes               int `json:"stuck_minutes"`                  // S2, until slice 5a (design.md 10.1)
	ContinuesPerTask           int `json:"continues_per_task"`             // a recap's continue (design.md 4.3, 10.1)
	ContinueLines              int `json:"continue_lines"`                 // the cap after a continue (design.md 4.9)
	TaskSizeLimit              int `json:"task_size_limit"`                // plan checks (design.md 11.5)
	SizeCapFactor              int `json:"size_cap_factor"`                // N12: size_cap = factor × estimate_lines
	MaxRunning                 int `json:"max_running"`                    // N10: concurrent tasks (design.md 4.10, 10.1)
	// SecondReviewer reviews the second passing round (L8): opus in a
	// fresh session, or codex (world.CodexArgv's model).
	SecondReviewer string `json:"second_reviewer"`
	// DispatchWorkflow is the one workflow file under .github/workflows
	// that N35 may dispatch; empty never dispatches (design.md 9.4).
	DispatchWorkflow string `json:"dispatch_workflow,omitempty"`
}

// Range limits for each setting.
const (
	ReviewRoundsMin   = 1
	ReviewRoundsMax   = 5
	MaxRoundsMax      = 10
	RetroMajorsMin    = 2
	RetroMajorsMax    = 4
	RetroFromMin      = 2
	RetroFromMax      = 6
	ChecksGraceMin    = 1
	ChecksGraceMax    = 60
	CIStalledMin      = 10
	CIStalledMax      = 600
	MainRedMin        = 5
	MainRedMax        = 600
	CIFailsPerHourMin = 1
	CIFailsPerHourMax = 10
	FlakeRerunsMax    = 3
	StartMin          = 1
	StartMax          = 15
	IdleGraceMin      = 5
	IdleGraceMax      = 120
	QuietMin          = 5
	QuietMax          = 120
	InterruptMin      = 10
	InterruptMax      = 600
	BlockedMin        = 1
	BlockedMax        = 120
	StuckMin          = 5
	StuckMax          = 240
	ContinuesMax      = 3
	ContinueLinesMax  = 500
	SizeLimitMin      = 50
	SizeLimitMax      = 2000
	SizeFactorMin     = 1
	SizeFactorMax     = 5
	MaxRunningMin     = 1
	MaxRunningMax     = 20
)

// DefaultSettings are slice 2's defaults: two passing rounds on the final head
// (design.md 10.1, D12).
func DefaultSettings() Settings {
	return Settings{
		ReviewRounds: 2, MaxRounds: 5,
		RetroAfterRoundsWithMajors: 2, RetroFromRound: 3,
		ChecksGraceMinutes: 5, CIStalledMinutes: 90, MainRedMinutes: 60, CIFailsPerHour: 3, FlakeReruns: 1,
		StartMinutes: 2, IdleGraceSeconds: 20, QuietMinutes: 20, InterruptSeconds: 90, BlockedMinutes: 10, StuckMinutes: 30,
		ContinuesPerTask: 1, ContinueLines: 60, TaskSizeLimit: 400, SizeCapFactor: 2, MaxRunning: 3,
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
		{"checks_grace_minutes", s.ChecksGraceMinutes, d.ChecksGraceMinutes},
		{"ci_fails_per_hour", s.CIFailsPerHour, d.CIFailsPerHour},
		{"ci_stalled_minutes", s.CIStalledMinutes, d.CIStalledMinutes},
		{"continue_lines", s.ContinueLines, d.ContinueLines},
		{"continues_per_task", s.ContinuesPerTask, d.ContinuesPerTask},
		{"flake_reruns", s.FlakeReruns, d.FlakeReruns},
		{"idle_grace_seconds", s.IdleGraceSeconds, d.IdleGraceSeconds},
		{"interrupt_seconds", s.InterruptSeconds, d.InterruptSeconds},
		{"main_red_minutes", s.MainRedMinutes, d.MainRedMinutes},
		{"max_rounds", s.MaxRounds, d.MaxRounds},
		{"max_running", s.MaxRunning, d.MaxRunning},
		{"quiet_minutes", s.QuietMinutes, d.QuietMinutes},
		{"retro_after_rounds_with_majors", s.RetroAfterRoundsWithMajors, d.RetroAfterRoundsWithMajors},
		{"retro_from_round", s.RetroFromRound, d.RetroFromRound},
		{"review_rounds", s.ReviewRounds, d.ReviewRounds},
		{"size_cap_factor", s.SizeCapFactor, d.SizeCapFactor},
		{"start_minutes", s.StartMinutes, d.StartMinutes},
		{"stuck_minutes", s.StuckMinutes, d.StuckMinutes},
		{"task_size_limit", s.TaskSizeLimit, d.TaskSizeLimit},
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
		{"flake_reruns", s.FlakeReruns, 0, FlakeRerunsMax},
		{"start_minutes", s.StartMinutes, StartMin, StartMax},
		{"idle_grace_seconds", s.IdleGraceSeconds, IdleGraceMin, IdleGraceMax},
		{"quiet_minutes", s.QuietMinutes, QuietMin, QuietMax},
		{"interrupt_seconds", s.InterruptSeconds, InterruptMin, InterruptMax},
		{"blocked_minutes", s.BlockedMinutes, BlockedMin, BlockedMax},
		{"stuck_minutes", s.StuckMinutes, StuckMin, StuckMax},
		{"continues_per_task", s.ContinuesPerTask, 0, ContinuesMax},
		{"continue_lines", s.ContinueLines, 0, ContinueLinesMax},
		{"task_size_limit", s.TaskSizeLimit, SizeLimitMin, SizeLimitMax},
		{"size_cap_factor", s.SizeCapFactor, SizeFactorMin, SizeFactorMax},
		{"max_running", s.MaxRunning, MaxRunningMin, MaxRunningMax},
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
	return nil
}
