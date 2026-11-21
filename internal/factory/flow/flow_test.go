package flow

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func row(id string, when func(int) bool) Row[int] {
	return Row[int]{ID: id, WaitsOn: "engine", Stage: "working", When: when, Then: func(int) Decision { return Decision{Why: id} }}
}

var catchAll = row("none", nil)

func TestFirstMatch(t *testing.T) {
	rows := []Row[int]{
		row("big", func(f int) bool { return f > 10 }),
		row("pos", func(f int) bool { return f > 0 }),
		catchAll,
	}
	if err := CheckTable(rows); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		f    int
		want string
	}{{20, "big"}, {5, "pos"}, {-1, "none"}} {
		r, d := FirstMatch(rows, c.f)
		if r.ID != c.want || d.Why != c.want {
			t.Errorf("FirstMatch(%d) = %q/%q, want %q", c.f, r.ID, d.Why, c.want)
		}
	}
}

func TestCheckTable(t *testing.T) {
	pos := row("pos", func(f int) bool { return f > 0 })
	bad := map[string][]Row[int]{
		"empty":           nil,
		"no catch-all":    {pos},
		"catch-all early": {catchAll, pos, catchAll},
		"no id":           {row("", func(int) bool { return true }), catchAll},
		"no waits on":     {{ID: "x", Stage: "working", When: pos.When, Then: pos.Then}, catchAll},
	}
	for name, rows := range bad {
		if CheckTable(rows) == nil {
			t.Errorf("%s: CheckTable passed", name)
		}
	}
	// Stage is optional in generic CheckTable (only task rows need one).
	noStage := []Row[int]{{ID: "x", WaitsOn: "none", When: pos.When, Then: pos.Then}, {ID: "end", WaitsOn: "none", Then: pos.Then}}
	if err := CheckTable(noStage); err != nil {
		t.Errorf("CheckTable rejected non-task table without Stage: %v", err)
	}
	for _, r := range MergeOnlyRows {
		if r.Stage != "" {
			t.Errorf("MergeOnlyRows[%s].Stage = %q, want empty", r.ID, r.Stage)
		}
	}
	for _, r := range PRAdoptRows {
		if r.Stage != "" {
			t.Errorf("PRAdoptRows[%s].Stage = %q, want empty", r.ID, r.Stage)
		}
	}
}

func TestActs(t *testing.T) {
	at := time.Unix(0, 0)
	acts := []Act{{Key: "merge@3f9c", Result: "failed", At: at}, {Key: "merge@3f9c", Result: "done", At: at}, {Key: "push", Result: "failed", At: at}}
	if !Done(acts, "merge@3f9c") || Done(acts, "push") || Done(acts, "x") {
		t.Error("Done")
	}
	if Count(acts, "merge@3f9c") != 2 || Count(acts, "push") != 1 || Count(acts, "x") != 0 {
		t.Error("Count")
	}
}

func TestSettingsRange(t *testing.T) {
	def := DefaultSettings()
	if def.ReviewRounds != 2 {
		t.Fatalf("DefaultSettings().ReviewRounds = %d, want 2", def.ReviewRounds)
	}
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	set := map[string]func(*Settings, int){
		"review_rounds":                  func(s *Settings, n int) { s.ReviewRounds = n },
		"max_rounds":                     func(s *Settings, n int) { s.MaxRounds = n },
		"retro_after_rounds_with_majors": func(s *Settings, n int) { s.RetroAfterRoundsWithMajors = n },
		"retro_from_round":               func(s *Settings, n int) { s.RetroFromRound = n },
		"checks_grace_minutes":           func(s *Settings, n int) { s.ChecksGraceMinutes = n },
		"ci_stalled_minutes":             func(s *Settings, n int) { s.CIStalledMinutes = n },
		"main_red_minutes":               func(s *Settings, n int) { s.MainRedMinutes = n },
		"ci_fails_per_hour":              func(s *Settings, n int) { s.CIFailsPerHour = n },
		"flake_reruns":                   func(s *Settings, n int) { s.FlakeReruns = n },
		"start_minutes":                  func(s *Settings, n int) { s.StartMinutes = n },
		"idle_grace_seconds":             func(s *Settings, n int) { s.IdleGraceSeconds = n },
		"quiet_minutes":                  func(s *Settings, n int) { s.QuietMinutes = n },
		"interrupt_seconds":              func(s *Settings, n int) { s.InterruptSeconds = n },
		"blocked_minutes":                func(s *Settings, n int) { s.BlockedMinutes = n },
		"worker_restarts":                func(s *Settings, n int) { s.WorkerRestarts = n },
		"reviewer_retries":               func(s *Settings, n int) { s.ReviewerRetries = n },
		"ensure_failures":                func(s *Settings, n int) { s.EnsureFailures = n },
		"ensure_backoff_minutes":         func(s *Settings, n int) { s.EnsureBackoffMinutes = n },
		"stuck_minutes":                  func(s *Settings, n int) { s.StuckMinutes = n },
		"coordinator_starts_per_hour":    func(s *Settings, n int) { s.CoordinatorStartsPerHour = n },
		"coordinator_gap_minutes":        func(s *Settings, n int) { s.CoordinatorGapMinutes = n },
		"owner_typing_minutes":           func(s *Settings, n int) { s.OwnerTypingMinutes = n },
		"coordinator_silent_minutes":     func(s *Settings, n int) { s.CoordinatorSilentMinutes = n },
		"heartbeat_minutes":              func(s *Settings, n int) { s.HeartbeatMinutes = n },
		"max_running":                    func(s *Settings, n int) { s.MaxRunning = n },
	}
	limits := map[string][2]int{
		"review_rounds":                  {ReviewRoundsMin, ReviewRoundsMax},
		"max_rounds":                     {def.ReviewRounds, MaxRoundsMax},
		"retro_after_rounds_with_majors": {RetroMajorsMin, RetroMajorsMax},
		"retro_from_round":               {RetroFromMin, RetroFromMax},
		"checks_grace_minutes":           {ChecksGraceMin, ChecksGraceMax},
		"ci_stalled_minutes":             {CIStalledMin, CIStalledMax},
		"main_red_minutes":               {MainRedMin, MainRedMax},
		"ci_fails_per_hour":              {CIFailsPerHourMin, CIFailsPerHourMax},
		"flake_reruns":                   {0, FlakeRerunsMax},
		"start_minutes":                  {StartMin, StartMax},
		"idle_grace_seconds":             {IdleGraceMin, IdleGraceMax},
		"quiet_minutes":                  {QuietMin, QuietMax},
		"interrupt_seconds":              {InterruptMin, InterruptMax},
		"blocked_minutes":                {BlockedMin, BlockedMax},
		"worker_restarts":                {0, WorkerRestartsMax},
		"reviewer_retries":               {0, ReviewerRetriesMax},
		"ensure_failures":                {EnsureFailuresMin, EnsureFailuresMax},
		"ensure_backoff_minutes":         {EnsureBackoffMin, EnsureBackoffMax},
		"stuck_minutes":                  {StuckMin, StuckMax},
		"coordinator_starts_per_hour":    {CoordStartsMin, CoordStartsMax},
		"coordinator_gap_minutes":        {CoordGapMin, CoordGapMax},
		"owner_typing_minutes":           {OwnerTypingMin, OwnerTypingMax},
		"coordinator_silent_minutes":     {CoordSilentMin, CoordSilentMax},
		"heartbeat_minutes":              {HeartbeatMin, HeartbeatMax},
		"max_running":                    {MaxRunningMin, MaxRunningMax},
	}
	for name, lim := range limits {
		for _, c := range []struct {
			n  int
			ok bool
		}{{lim[0], true}, {lim[1], true}, {lim[0] - 1, false}, {lim[1] + 1, false}} {
			s := DefaultSettings()
			set[name](&s, c.n)
			if err := s.Validate(); (err == nil) != c.ok {
				t.Errorf("%s %d: err %v, want ok=%v", name, c.n, err, c.ok)
			}
		}
	}
	// max_rounds lower bound tracks review_rounds (design.md 10.1), and
	// Validate checks fields in declaration order (review_rounds before max_rounds).
	s := DefaultSettings()
	s.ReviewRounds, s.MaxRounds = 4, 3
	if err := s.Validate(); err == nil {
		t.Error("max_rounds < review_rounds validated")
	}
	sOrder := DefaultSettings()
	sOrder.ReviewRounds, sOrder.MaxRounds = 6, 5
	if err := sOrder.Validate(); err == nil || !strings.Contains(err.Error(), "review_rounds 6: allowed 1–5") {
		t.Errorf("Validate(review_rounds=6, max_rounds=5) = %v, want review_rounds error first", err)
	}
	// dispatch_workflow is a plain file name under .github/workflows.
	for w, ok := range map[string]bool{"": true, "ci.yml": true, "Build_2.yaml": true, "../ci.yml": false, ".github/workflows/ci.yml": false, `a\b.yml`: false, "..": false,
		"12345": false, "12345.txt": false, "ci": false, "-x.yml": false, "--repo.yml": false, ".yml": false} {
		s := DefaultSettings()
		s.DispatchWorkflow = w
		if err := s.Validate(); (err == nil) != ok {
			t.Errorf("dispatch_workflow %q: err %v, want ok=%v", w, err, ok)
		}
	}
	if s := (Settings{DispatchWorkflow: "ci.yml"}); !slices.Contains(s.Changed(), "dispatch_workflow ci.yml") {
		t.Errorf("Changed() = %q, want dispatch_workflow ci.yml", s.Changed())
	}
	if def.CIStalledMinutes != 90 {
		t.Fatalf("DefaultSettings().CIStalledMinutes = %d, want 90", def.CIStalledMinutes)
	}
	s2 := DefaultSettings()
	s2.CIStalledMinutes = 10
	if !slices.Contains(s2.Changed(), "ci_stalled_minutes 10") {
		t.Errorf("Changed() = %q, want ci_stalled_minutes 10", s2.Changed())
	}
}
