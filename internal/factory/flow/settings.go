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
	ReviewRounds               int    `json:"review_rounds"`
	MaxRounds                  int    `json:"max_rounds"`
	RetroAfterRoundsWithMajors int    `json:"retro_after_rounds_with_majors"`
	RetroFromRound             int    `json:"retro_from_round"`
	ChecksGraceMinutes         int    `json:"checks_grace_minutes"`
	CIStalledMinutes           int    `json:"ci_stalled_minutes"`
	MainRedMinutes             int    `json:"main_red_minutes"`
	CIFailsPerHour             int    `json:"ci_fails_per_hour"`
	FlakeReruns                int    `json:"flake_reruns"`
	StartMinutes               int    `json:"start_minutes"`
	IdleGraceSeconds           int    `json:"idle_grace_seconds"`
	QuietMinutes               int    `json:"quiet_minutes"`
	InterruptSeconds           int    `json:"interrupt_seconds"`
	BlockedMinutes             int    `json:"blocked_minutes"`
	StuckMinutes               int    `json:"stuck_minutes"`
	ContinuesPerTask           int    `json:"continues_per_task"`
	ContinueLines              int    `json:"continue_lines"`
	TaskSizeLimit              int    `json:"task_size_limit"`
	SizeCapFactor              int    `json:"size_cap_factor"`
	MaxRunning                 int    `json:"max_running"`
	SecondReviewer             string `json:"second_reviewer"`
	DispatchWorkflow           string `json:"dispatch_workflow,omitempty"`
}

// Range limits for each setting.
const (
	ReviewRoundsMin, ReviewRoundsMax, MaxRoundsMax                     = 1, 5, 10
	RetroMajorsMin, RetroMajorsMax, RetroFromMin, RetroFromMax         = 2, 4, 2, 6
	ChecksGraceMin, ChecksGraceMax, CIStalledMin, CIStalledMax         = 1, 60, 10, 600
	MainRedMin, MainRedMax, CIFailsPerHourMin, CIFailsPerHourMax       = 5, 600, 1, 10
	FlakeRerunsMax, StuckMin, StuckMax, ContinuesMax, ContinueLinesMax = 3, 5, 240, 3, 500
	StartMin, StartMax, IdleGraceMin, IdleGraceMax, QuietMin, QuietMax = 1, 15, 5, 120, 5, 120
	InterruptMin, InterruptMax, BlockedMin, BlockedMax                 = 10, 600, 1, 120
	SizeLimitMin, SizeLimitMax, SizeFactorMin, SizeFactorMax           = 50, 2000, 1, 5
	MaxRunningMin, MaxRunningMax                                       = 1, 20
)

// DefaultSettings are slice 2's defaults: two passing rounds on the final head (design.md 10.1, D12).
func DefaultSettings() Settings {
	return Settings{
		ReviewRounds: 2, MaxRounds: 5, RetroAfterRoundsWithMajors: 2, RetroFromRound: 3,
		ChecksGraceMinutes: 5, CIStalledMinutes: 90, MainRedMinutes: 60, CIFailsPerHour: 3, FlakeReruns: 1,
		StartMinutes: 2, IdleGraceSeconds: 20, QuietMinutes: 20, InterruptSeconds: 90, BlockedMinutes: 10, StuckMinutes: 30,
		ContinuesPerTask: 1, ContinueLines: 60, TaskSizeLimit: 400, SizeCapFactor: 2, MaxRunning: 3, SecondReviewer: "opus",
	}
}

type intSetting struct {
	name                string
	got, def, low, high int
}

func (s Settings) ints() []intSetting {
	d := DefaultSettings()
	return []intSetting{
		{"blocked_minutes", s.BlockedMinutes, d.BlockedMinutes, BlockedMin, BlockedMax}, {"checks_grace_minutes", s.ChecksGraceMinutes, d.ChecksGraceMinutes, ChecksGraceMin, ChecksGraceMax},
		{"ci_fails_per_hour", s.CIFailsPerHour, d.CIFailsPerHour, CIFailsPerHourMin, CIFailsPerHourMax}, {"ci_stalled_minutes", s.CIStalledMinutes, d.CIStalledMinutes, CIStalledMin, CIStalledMax},
		{"continue_lines", s.ContinueLines, d.ContinueLines, 0, ContinueLinesMax}, {"continues_per_task", s.ContinuesPerTask, d.ContinuesPerTask, 0, ContinuesMax},
		{"flake_reruns", s.FlakeReruns, d.FlakeReruns, 0, FlakeRerunsMax}, {"idle_grace_seconds", s.IdleGraceSeconds, d.IdleGraceSeconds, IdleGraceMin, IdleGraceMax},
		{"interrupt_seconds", s.InterruptSeconds, d.InterruptSeconds, InterruptMin, InterruptMax}, {"main_red_minutes", s.MainRedMinutes, d.MainRedMinutes, MainRedMin, MainRedMax},
		{"max_rounds", s.MaxRounds, d.MaxRounds, s.ReviewRounds, MaxRoundsMax}, {"max_running", s.MaxRunning, d.MaxRunning, MaxRunningMin, MaxRunningMax},
		{"quiet_minutes", s.QuietMinutes, d.QuietMinutes, QuietMin, QuietMax}, {"retro_after_rounds_with_majors", s.RetroAfterRoundsWithMajors, d.RetroAfterRoundsWithMajors, RetroMajorsMin, RetroMajorsMax},
		{"retro_from_round", s.RetroFromRound, d.RetroFromRound, RetroFromMin, RetroFromMax}, {"review_rounds", s.ReviewRounds, d.ReviewRounds, ReviewRoundsMin, ReviewRoundsMax},
		{"size_cap_factor", s.SizeCapFactor, d.SizeCapFactor, SizeFactorMin, SizeFactorMax}, {"start_minutes", s.StartMinutes, d.StartMinutes, StartMin, StartMax},
		{"stuck_minutes", s.StuckMinutes, d.StuckMinutes, StuckMin, StuckMax}, {"task_size_limit", s.TaskSizeLimit, d.TaskSizeLimit, SizeLimitMin, SizeLimitMax},
	}
}

// Changed lists every setting that differs from its default as "name value", sorted (design.md 10).
func (s Settings) Changed() []string {
	var out []string
	for _, c := range s.ints() {
		if c.got != c.def {
			out = append(out, fmt.Sprintf("%s %d", c.name, c.got))
		}
	}
	if s.SecondReviewer != DefaultSettings().SecondReviewer {
		out = append(out, "second_reviewer "+s.SecondReviewer)
	}
	if s.DispatchWorkflow != "" {
		out = append(out, "dispatch_workflow "+s.DispatchWorkflow)
	}
	return out
}

// Validate refuses a value outside its range, naming the range.
func (s Settings) Validate() error {
	for _, r := range s.ints() {
		if r.got < r.low || r.got > r.high {
			return fmt.Errorf("%s %d: allowed %d–%d", r.name, r.got, r.low, r.high)
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
