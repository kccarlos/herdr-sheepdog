package flow

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
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
	PlanReviewRounds           int `json:"plan_review_rounds"`             // J16, N10 (design.md 10.1, 11.7)
	RetroAfterRoundsWithMajors int `json:"retro_after_rounds_with_majors"` // L11, L12 (design.md 10.1)
	RetroFromRound             int `json:"retro_from_round"`               // L11, L12 (design.md 10.1)
	RetrosPerTask              int `json:"retros_per_task"`                // L11 (design.md 10.1)
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
	DiskFullPercent            int `json:"disk_full_percent"`              // 6, 7.3: volume used percent raising the disk alert (design.md 10.1)
	// SecondReviewer reviews the second passing round (L8): opus in a
	// fresh session, or codex (world.CodexArgv's model).
	SecondReviewer string `json:"second_reviewer"`
	// DispatchWorkflow is the one workflow file under .github/workflows
	// that N35 may dispatch; empty never dispatches (design.md 9.4).
	DispatchWorkflow string `json:"dispatch_workflow,omitempty"`
	// Models maps each role to its ordered candidate list (design.md 9.1, 10.1).
	Models map[string][]Candidate `json:"models,omitempty"`
	// DesignGate gates design tasks (kind: design) at N30/N31 when set to "owner" (design.md 10.1, 11.4).
	DesignGate string `json:"design_gate,omitempty"`
}

// Range limits for each setting.
const (
	ReviewRoundsMin                       = 1
	ReviewRoundsMax                       = 5
	MaxRoundsMax                          = 10
	PlanReviewRoundsMax                   = 1
	RetroMajorsMin                        = 2
	RetroMajorsMax                        = 4
	RetroFromMin                          = 2
	RetroFromMax                          = 6
	RetrosPerTaskMax                      = 2
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
const DiskFullMin, DiskFullMax = 50, 99

// DefaultSettings are the engine's defaults: two passing rounds on the final head
// (design.md 10.1, D12).
func DefaultSettings() Settings {
	return Settings{
		ReviewRounds: 2, MaxRounds: 5, PlanReviewRounds: 1,
		RetroAfterRoundsWithMajors: 2, RetroFromRound: 3, RetrosPerTask: 1,
		ChecksGraceMinutes: 5, CIStalledMinutes: 90, MainRedMinutes: 60, CIFailsPerHour: 3, BuildHours: 4, FlakeReruns: 1,
		StartMinutes: 2, IdleGraceSeconds: 20, QuietMinutes: 20, InterruptSeconds: 90, ReviewMinutes: 45, BlockedMinutes: 10,
		WorkerRestarts: 3, ReviewerRetries: 2, EnsureFailures: 3, EnsureBackoffMinutes: 1,
		CoordinatorStartsPerHour: 3, CoordinatorGapMinutes: 10, CoordinatorSilentMinutes: 30, HandoffMinutes: 5, HeartbeatMinutes: 60,
		ContinuesPerTask: 1, ContinueLines: 60, TaskSizeLimit: 400, SizeCapFactor: 2, MaxRunning: 3,
		PoolFloorPercent: 5, PoolMinutes: 5, SweepMinutes: 5, DiskFullPercent: 90,
		SecondReviewer: "opus",
	}
}

// SettingRow is one setting in the settings view (design.md 10.1, plan.md 14.1, 14.3).
type SettingRow struct {
	Name    string              `json:"name"`
	Label   string              `json:"label"`
	Value   string              `json:"value"`
	Default string              `json:"default"`
	Min     int                 `json:"min"`
	Max     int                 `json:"max"`
	Allowed []string            `json:"allowed,omitempty"`
	CLIs    []string            `json:"clis,omitempty"`
	Models  map[string][]string `json:"models,omitempty"`
	Problem string              `json:"problem,omitempty"`
	Note    string              `json:"note,omitempty"`
	Top     bool                `json:"top,omitempty"`
}

type numSetting struct {
	name      string
	field     *int
	low, high int
}

func settingLabel(name string) string {
	if role, ok := strings.CutPrefix(name, "models."); ok {
		return strings.ReplaceAll(role, "_", " ") + " models"
	}
	return strings.ReplaceAll(name, "_", " ")
}

func (s *Settings) numSettings() []numSetting {
	return []numSetting{
		{"review_rounds", &s.ReviewRounds, ReviewRoundsMin, ReviewRoundsMax}, {"max_rounds", &s.MaxRounds, min(MaxRoundsMax, max(ReviewRoundsMin, s.ReviewRounds)), MaxRoundsMax},
		{"retro_after_rounds_with_majors", &s.RetroAfterRoundsWithMajors, RetroMajorsMin, RetroMajorsMax}, {"retro_from_round", &s.RetroFromRound, RetroFromMin, RetroFromMax},
		{"retros_per_task", &s.RetrosPerTask, 0, RetrosPerTaskMax}, {"plan_review_rounds", &s.PlanReviewRounds, 0, PlanReviewRoundsMax},
		{"checks_grace_minutes", &s.ChecksGraceMinutes, ChecksGraceMin, ChecksGraceMax}, {"ci_stalled_minutes", &s.CIStalledMinutes, CIStalledMin, CIStalledMax},
		{"main_red_minutes", &s.MainRedMinutes, MainRedMin, MainRedMax}, {"ci_fails_per_hour", &s.CIFailsPerHour, CIFailsPerHourMin, CIFailsPerHourMax},
		{"build_hours", &s.BuildHours, BuildHoursMin, BuildHoursMax}, {"flake_reruns", &s.FlakeReruns, 0, FlakeRerunsMax},
		{"start_minutes", &s.StartMinutes, StartMin, StartMax}, {"idle_grace_seconds", &s.IdleGraceSeconds, IdleGraceMin, IdleGraceMax},
		{"quiet_minutes", &s.QuietMinutes, QuietMin, QuietMax}, {"interrupt_seconds", &s.InterruptSeconds, InterruptMin, InterruptMax},
		{"review_minutes", &s.ReviewMinutes, ReviewMin, ReviewMax}, {"blocked_minutes", &s.BlockedMinutes, BlockedMin, BlockedMax},
		{"worker_restarts", &s.WorkerRestarts, 0, WorkerRestartsMax}, {"reviewer_retries", &s.ReviewerRetries, 0, ReviewerRetriesMax},
		{"ensure_failures", &s.EnsureFailures, EnsureFailuresMin, EnsureFailuresMax}, {"ensure_backoff_minutes", &s.EnsureBackoffMinutes, EnsureBackoffMin, EnsureBackoffMax},
		{"coordinator_starts_per_hour", &s.CoordinatorStartsPerHour, CoordStartsMin, CoordStartsMax}, {"coordinator_gap_minutes", &s.CoordinatorGapMinutes, CoordGapMin, CoordGapMax},
		{"coordinator_silent_minutes", &s.CoordinatorSilentMinutes, CoordSilentMin, CoordSilentMax}, {"handoff_minutes", &s.HandoffMinutes, HandoffMin, HandoffMax},
		{"heartbeat_minutes", &s.HeartbeatMinutes, HeartbeatMin, HeartbeatMax}, {"continues_per_task", &s.ContinuesPerTask, 0, ContinuesMax},
		{"continue_lines", &s.ContinueLines, 0, ContinueLinesMax}, {"task_size_limit", &s.TaskSizeLimit, SizeLimitMin, SizeLimitMax},
		{"size_cap_factor", &s.SizeCapFactor, SizeFactorMin, SizeFactorMax}, {"max_running", &s.MaxRunning, MaxRunningMin, MaxRunningMax},
		{"sweep_minutes", &s.SweepMinutes, SweepMin, SweepMax}, {"pool_floor_percent", &s.PoolFloorPercent, 0, PoolFloorMax},
		{"pool_minutes", &s.PoolMinutes, PoolMin, PoolMax}, {"disk_full_percent", &s.DiskFullPercent, DiskFullMin, DiskFullMax},
	}
}

// Changed lists every setting that differs from its default as "name value", sorted, for the coordinator's facts block (design.md 10).
func (s Settings) Changed() []string {
	d := DefaultSettings()
	dn, out := d.numSettings(), []string(nil)
	for i, c := range s.numSettings() {
		if *c.field != *dn[i].field {
			out = append(out, fmt.Sprintf("%s %d", c.name, *c.field))
		}
	}
	slices.Sort(out)
	if s.SecondReviewer != d.SecondReviewer {
		out = append(out, "second_reviewer "+s.SecondReviewer)
	}
	if s.DispatchWorkflow != "" {
		out = append(out, "dispatch_workflow "+s.DispatchWorkflow)
	}
	if len(s.Models) > 0 {
		out = append(out, "models "+strings.Join(slices.Sorted(maps.Keys(s.Models)), ","))
	}
	if s.DesignGate != "" {
		out = append(out, "design_gate "+s.DesignGate)
	}
	return out
}

// Validate refuses a value outside its range, naming the range.
func (s Settings) Validate() error {
	for _, r := range s.numSettings() {
		if *r.field < r.low || *r.field > r.high {
			return fmt.Errorf("%s %d: allowed %d–%d", r.name, *r.field, r.low, r.high)
		}
	}
	if s.SecondReviewer != "opus" && s.SecondReviewer != "codex" {
		return fmt.Errorf("second_reviewer %q: allowed opus or codex", s.SecondReviewer)
	}
	if w := s.DispatchWorkflow; w != "" && !WorkflowFile.MatchString(w) {
		return fmt.Errorf("dispatch_workflow %q: a .yml or .yaml file name under .github/workflows (letters, digits, . _ -; no path)", w)
	}
	if s.DesignGate != "" && s.DesignGate != GateOwner && s.DesignGate != "review" {
		return fmt.Errorf("design_gate %q: allowed owner, review, or empty", s.DesignGate)
	}
	return ValidateCandidates("models", s.Models)
}

// Rows returns every setting row in table order with its label, default, range and problem.
func (s Settings) Rows() []SettingRow {
	d, dm := DefaultSettings(), DefaultModels(s)
	dn, sn := d.numSettings(), s.numSettings()
	var rows []SettingRow
	for _, role := range []string{RoleCoordinator, RoleWorker, RoleReviewer, RoleRetro, RolePlanReviewer} {
		name, lbl, def := "models."+role, settingLabel("models."+role), FormatCandidates(dm[role])
		val, prob := def, ""
		if cands, ok := s.Models[role]; ok {
			val = FormatCandidates(cands)
			if err := ValidateCandidates("models", map[string][]Candidate{role: slices.Clone(cands)}); err != nil {
				_, why, _ := strings.Cut(err.Error(), ": ")
				prob = lbl + ": " + why
			}
		}
		rows = append(rows, SettingRow{Name: name, Label: lbl, Value: val, Default: def, Problem: prob, Top: true})
	}
	for i, r := range sn {
		lbl, prob := settingLabel(r.name), ""
		if *r.field < r.low || *r.field > r.high {
			prob = fmt.Sprintf("%s %d: allowed %d–%d", lbl, *r.field, r.low, r.high)
		}
		rows = append(rows, SettingRow{Name: r.name, Label: lbl, Value: strconv.Itoa(*r.field), Default: strconv.Itoa(*dn[i].field), Min: r.low, Max: r.high, Problem: prob, Top: r.name == "max_running"})
	}
	srProb := map[bool]string{true: fmt.Sprintf("second reviewer %q: allowed opus or codex", s.SecondReviewer)}[s.SecondReviewer != "opus" && s.SecondReviewer != "codex"]
	srNote := map[bool]string{true: "not used while reviewer models are set"}[len(s.Models[RoleReviewer]) > 0]
	dgProb := map[bool]string{true: fmt.Sprintf("design gate %q: allowed owner, review, or empty", s.DesignGate)}[s.DesignGate != "" && s.DesignGate != GateOwner && s.DesignGate != "review"]
	dwProb := map[bool]string{true: fmt.Sprintf("dispatch workflow %q: a .yml or .yaml file name under .github/workflows (letters, digits, . _ -; no path)", s.DispatchWorkflow)}[s.DispatchWorkflow != "" && !WorkflowFile.MatchString(s.DispatchWorkflow)]
	return append(rows,
		SettingRow{Name: "second_reviewer", Label: "second reviewer", Value: s.SecondReviewer, Default: d.SecondReviewer, Allowed: []string{"opus", "codex"}, Problem: srProb, Note: srNote},
		SettingRow{Name: "design_gate", Label: "design gate", Value: s.DesignGate, Default: d.DesignGate, Allowed: []string{"", GateOwner, "review"}, Problem: dgProb},
		SettingRow{Name: "dispatch_workflow", Label: "dispatch workflow", Value: s.DispatchWorkflow, Default: d.DispatchWorkflow, Problem: dwProb},
	)
}

// With returns s with one setting changed, checking row by row so unrelated bad rows do not block repair (SE4, SE6).
func (s Settings) With(name, value string) (Settings, error) {
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	before := s.Rows()
	idx := slices.IndexFunc(before, func(r SettingRow) bool { return r.Name == name })
	if idx < 0 {
		return Settings{}, fmt.Errorf("unknown setting %q", name)
	}
	s.Models = maps.Clone(s.Models)
	if role, ok := strings.CutPrefix(name, "models."); ok {
		delete(s.Models, role)
		if cands, err := ParseCandidates(value); value != "" && err != nil {
			if strings.Contains(err.Error(), "comma") {
				err = fmt.Errorf("%s: %w", before[idx].Label, err)
			}
			return Settings{}, err
		} else if value != "" && FormatCandidates(cands) != before[idx].Default {
			if s.Models == nil {
				s.Models = map[string][]Candidate{}
			}
			s.Models[role] = cands
		}
		if len(s.Models) == 0 {
			s.Models = nil
		}
		return s, nil
	}
	if value == "" {
		value = before[idx].Default
	}
	switch name {
	case "second_reviewer":
		s.SecondReviewer = value
	case "design_gate":
		s.DesignGate = value
	case "dispatch_workflow":
		s.DispatchWorkflow = value
	default:
		n, err := strconv.Atoi(value)
		if err != nil {
			return Settings{}, fmt.Errorf("%s %q: allowed %d–%d", before[idx].Label, value, before[idx].Min, before[idx].Max)
		}
		ns := s.numSettings()
		*ns[slices.IndexFunc(ns, func(r numSetting) bool { return r.name == name })].field = n
	}
	after := s.Rows()
	if after[idx].Problem != "" {
		return Settings{}, errors.New(after[idx].Problem)
	}
	for i, r := range after {
		if r.Problem != "" && before[i].Problem == "" {
			return Settings{}, fmt.Errorf("%s %s does not fit: %s is %s (allowed %d–%d). Raise %s first.", after[idx].Label, after[idx].Value, r.Label, r.Value, r.Min, r.Max, r.Label) //nolint:staticcheck // ST1005: an owner-facing sentence shown as written on the page (wording table, plan 14.0)
		}
	}
	return s, nil
}
