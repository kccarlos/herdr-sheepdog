package flow

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func findRow(t *testing.T, rows []SettingRow, name string) SettingRow {
	t.Helper()
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("setting row %q not found", name)
	return SettingRow{}
}

// TestSettingsEveryJSONFieldIsRowOrModels checks every json field of Settings
// (including slice 11's plan_review_rounds) is "models" or a SettingRow.Name,
// labels hold no underscores, max_running is Top, and RuleOption/RuleQuestion match SE1.
func TestSettingsEveryJSONFieldIsRowOrModels(t *testing.T) {
	rows := DefaultSettings().Rows()
	byName := make(map[string]SettingRow, len(rows))
	for _, r := range rows {
		if r.Name == "" || r.Label == "" || strings.Contains(r.Label, "_") {
			t.Errorf("row %q has bad label %q", r.Name, r.Label)
		}
		if _, dup := byName[r.Name]; dup {
			t.Errorf("duplicate row %q", r.Name)
		}
		byName[r.Name] = r
	}
	rt := reflect.TypeFor[Settings]()
	for i := range rt.NumField() {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" && tag != "models" {
			if _, ok := byName[tag]; !ok {
				t.Errorf("Settings json field %q missing from Settings.Rows()", tag)
			}
		}
	}
	if !findRow(t, rows, "max_running").Top {
		t.Error("max_running row must have Top=true")
	}
	if RuleOptionSave != "Save" || RuleOptionKeep != "Keep current" {
		t.Errorf("RuleOption = (%q, %q), want (Save, Keep current)", RuleOptionSave, RuleOptionKeep)
	}
	if q, want := RuleQuestion(SettingChange{Name: "review_rounds", Value: "3", Was: "2"}), "This project: review rounds 3 (now 2). Save?"; q != want {
		t.Errorf("RuleQuestion = %q, want %q", q, want)
	}
}

// TestSettingsRowsAtMinAndMax verifies Rows and With at each min/max,
// out-of-range bounds, choice rows, and empty->default (SE6).
func TestSettingsRowsAtMinAndMax(t *testing.T) {
	def := DefaultSettings()
	for _, r := range def.Rows() {
		if r.Problem != "" {
			t.Errorf("default row %q has Problem %q", r.Name, r.Problem)
		}
		if r.Max <= 0 {
			continue
		}
		for _, v := range []int{r.Min, r.Max} {
			vs := strconv.Itoa(v)
			if s2, err := def.With(r.Name, vs); err != nil || s2.Validate() != nil {
				t.Errorf("With(%q, %q): err=%v validate=%v", r.Name, vs, err, s2.Validate())
			} else if got := findRow(t, s2.Rows(), r.Name); got.Value != vs || got.Problem != "" {
				t.Errorf("row %q after With(%q) = %+v", r.Name, vs, got)
			}
		}
		for _, bad := range []int{r.Min - 1, r.Max + 1} {
			if _, err := def.With(r.Name, strconv.Itoa(bad)); err == nil {
				t.Errorf("With(%q, %d) succeeded, want refusal", r.Name, bad)
			}
		}
	}
	if s2, err := def.With("review_rounds", "3"); err != nil || s2.ReviewRounds != 3 {
		t.Fatalf("With review_rounds=3: (%d, %v)", s2.ReviewRounds, err)
	} else if s3, err := s2.With("review_rounds", ""); err != nil || s3.ReviewRounds != def.ReviewRounds {
		t.Fatalf("With review_rounds=\"\" (SE6): (%d, %v)", s3.ReviewRounds, err)
	}
	for _, c := range [][2]string{{"second_reviewer", "codex"}, {"design_gate", "owner"}, {"dispatch_workflow", "ci.yml"}} {
		if s2, err := def.With(c[0], c[1]); err != nil || findRow(t, s2.Rows(), c[0]).Value != c[1] {
			t.Errorf("With(%q, %q) = (%+v, %v)", c[0], c[1], s2, err)
		}
	}
}

// TestSettingsChangedAndValidateByteIdentical pins Changed() and Validate()'s
// existing output and declaration-order first failure.
func TestSettingsChangedAndValidateByteIdentical(t *testing.T) {
	s := DefaultSettings()
	s.BlockedMinutes, s.BuildHours, s.ChecksGraceMinutes, s.CIFailsPerHour, s.CIStalledMinutes = 11, 5, 6, 4, 91
	s.ContinueLines, s.ContinuesPerTask, s.CoordinatorGapMinutes, s.CoordinatorSilentMinutes, s.CoordinatorStartsPerHour = 61, 2, 11, 31, 4
	s.DiskFullPercent, s.EnsureBackoffMinutes, s.EnsureFailures, s.FlakeReruns, s.HandoffMinutes = 91, 2, 4, 2, 6
	s.HeartbeatMinutes, s.IdleGraceSeconds, s.InterruptSeconds, s.MainRedMinutes, s.MaxRounds = 61, 21, 91, 61, 6
	s.MaxRunning, s.PoolFloorPercent, s.PoolMinutes, s.QuietMinutes, s.RetroAfterRoundsWithMajors = 4, 6, 6, 21, 3
	s.RetroFromRound, s.RetrosPerTask, s.ReviewMinutes, s.ReviewRounds, s.ReviewerRetries = 4, 2, 46, 3, 3
	s.SizeCapFactor, s.StartMinutes, s.SweepMinutes, s.TaskSizeLimit, s.WorkerRestarts = 3, 3, 6, 401, 4
	s.SecondReviewer, s.DispatchWorkflow, s.DesignGate = "codex", "ci.yml", "owner"
	s.Models = map[string][]Candidate{RoleWorker: {{CLI: "claude", Model: "sonnet", Effort: "medium"}}}
	want := []string{
		"blocked_minutes 11", "build_hours 5", "checks_grace_minutes 6", "ci_fails_per_hour 4", "ci_stalled_minutes 91",
		"continue_lines 61", "continues_per_task 2", "coordinator_gap_minutes 11", "coordinator_silent_minutes 31", "coordinator_starts_per_hour 4",
		"disk_full_percent 91", "ensure_backoff_minutes 2", "ensure_failures 4", "flake_reruns 2", "handoff_minutes 6",
		"heartbeat_minutes 61", "idle_grace_seconds 21", "interrupt_seconds 91", "main_red_minutes 61", "max_rounds 6",
		"max_running 4", "pool_floor_percent 6", "pool_minutes 6", "quiet_minutes 21", "retro_after_rounds_with_majors 3",
		"retro_from_round 4", "retros_per_task 2", "review_minutes 46", "review_rounds 3", "reviewer_retries 3",
		"size_cap_factor 3", "start_minutes 3", "sweep_minutes 6", "task_size_limit 401", "worker_restarts 4",
		"second_reviewer codex", "dispatch_workflow ci.yml", "models worker", "design_gate owner",
	}
	if got := s.Changed(); !slices.Equal(got, want) {
		t.Fatalf("Changed() mismatch:\n got: %q\nwant: %q", got, want)
	}
	for _, tc := range []struct {
		edit func(*Settings)
		want string
	}{
		{func(x *Settings) { x.ReviewRounds, x.MaxRounds = 6, 5 }, "review_rounds 6: allowed 1–5"},
		{func(x *Settings) { x.ReviewRounds, x.MaxRounds = 4, 3 }, "max_rounds 3: allowed 4–10"},
		{func(x *Settings) { x.SecondReviewer = "Codex" }, `second_reviewer "Codex": allowed opus or codex`},
	} {
		x := DefaultSettings()
		tc.edit(&x)
		if err := x.Validate(); err == nil || err.Error() != tc.want {
			t.Errorf("Validate = %v, want %q", err, tc.want)
		}
	}
}

// TestSettingsWithRefusalsRepairProblemAndNote covers SE4, SE7, KB-REV-18 and UX line 3.
func TestSettingsWithRefusalsRepairProblemAndNote(t *testing.T) {
	def := DefaultSettings()
	for _, tc := range []struct {
		key, val string
		want     []string
	}{
		{"review_rounds", "0", []string{"review rounds", "1–5"}},
		{"review_rounds", "eleven", []string{"review rounds"}},
		{"no_such_setting", "1", []string{"no_such_setting"}},
		{"second_reviewer", "Codex", []string{"second reviewer", "opus", "codex"}},
	} {
		err := error(nil)
		if _, err = def.With(tc.key, tc.val); err == nil {
			t.Errorf("With(%q, %q) succeeded, want refusal", tc.key, tc.val)
			continue
		}
		for _, sub := range tc.want {
			if !strings.Contains(err.Error(), sub) {
				t.Errorf("With(%q, %q) err %q missing %q", tc.key, tc.val, err.Error(), sub)
			}
		}
		if tc.key != "no_such_setting" && strings.Contains(err.Error(), tc.key) {
			t.Errorf("With(%q, %q) err %q leaked key", tc.key, tc.val, err.Error())
		}
	}
	sLowMax := DefaultSettings()
	sLowMax.MaxRounds = 3
	if _, err := sLowMax.With("review_rounds", "4"); err == nil ||
		!strings.Contains(err.Error(), "review rounds") || !strings.Contains(err.Error(), "max rounds") {
		t.Fatalf("With(review_rounds=4, max_rounds=3) = %v, want refusal naming review rounds and max rounds", err)
	}
	broken := DefaultSettings()
	broken.ReviewRounds, broken.SecondReviewer = 0, "Codex"
	if findRow(t, broken.Rows(), "second_reviewer").Problem == "" {
		t.Error("stored SecondReviewer=\"Codex\" must set Problem")
	}
	if r1, err := broken.With("review_rounds", "3"); err != nil || r1.ReviewRounds != 3 || r1.SecondReviewer != "Codex" {
		t.Fatalf("single-row repair failed: (%+v, %v)", r1, err)
	} else if r2, err := r1.With("second_reviewer", "codex"); err != nil || r2.Validate() != nil {
		t.Fatalf("second repair failed: (%+v, %v)", r2, err)
	}
	withRev := DefaultSettings()
	withRev.Models = map[string][]Candidate{RoleReviewer: {{CLI: "claude", Model: "opus", Effort: "medium"}}}
	if findRow(t, def.Rows(), "second_reviewer").Note != "" || findRow(t, withRev.Rows(), "second_reviewer").Note == "" {
		t.Error("second_reviewer Note mismatch: empty when unset, non-empty when models.reviewer is set")
	}
}

func TestSettingsMaxRoundsLowerBoundWhenReviewRoundsInvalid(t *testing.T) {
	// 1. Hand-edited review_rounds=0 must not allow setting max_rounds=0 or negative (SE4).
	sBadRR := DefaultSettings()
	sBadRR.ReviewRounds = 0
	if _, err := sBadRR.With("max_rounds", "0"); err == nil {
		t.Error("With(max_rounds=0) succeeded when stored review_rounds=0; want refusal")
	}

	// 2. Stored (review_rounds=0, max_rounds=0) must mark max_rounds with a Problem (SE7)
	//    and must not block repairing review_rounds first (plan 14.1).
	sBothZero := DefaultSettings()
	sBothZero.ReviewRounds, sBothZero.MaxRounds = 0, 0
	if mr := findRow(t, sBothZero.Rows(), "max_rounds"); mr.Min < ReviewRoundsMin || mr.Problem == "" {
		t.Errorf("max_rounds row = %+v, want Min>=%d and non-empty Problem", mr, ReviewRoundsMin)
	}
	if _, err := sBothZero.With("review_rounds", "2"); err != nil {
		t.Errorf("repairing review_rounds first from (0,0) failed: %v", err)
	}

	// 3. Hand-edited review_rounds=12 must not produce inverted Min > Max on max_rounds.
	sHighRR := DefaultSettings()
	sHighRR.ReviewRounds = 12
	if mr := findRow(t, sHighRR.Rows(), "max_rounds"); mr.Min > mr.Max {
		t.Errorf("max_rounds row has inverted range Min=%d > Max=%d", mr.Min, mr.Max)
	}
}
