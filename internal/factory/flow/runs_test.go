package flow

import (
	"slices"
	"testing"
	"time"
)

func TestRunRowsCheckAndCompleteness(t *testing.T) {
	if err := CheckTable(RunRows); err != nil {
		t.Fatalf("CheckTable(RunRows): %v", err)
	}
	for _, r := range RunRows {
		if r.Stage != "" {
			t.Errorf("RunRows[%s].Stage = %q, want empty", r.ID, r.Stage)
		}
	}
	s := DefaultSettings()
	events := []RunEvent{
		{Kind: EvLaunched, Address: "t1", RunID: "run-1"},
		{Kind: EvLaunchFailed},
		{Kind: EvScreen, Screen: ScreenNone},
		{Kind: EvScreen, Screen: ScreenTrust},
		{Kind: EvStatus, Status: "working"},
		{Kind: EvStatus, Status: "idle"},
		{Kind: EvStatus, Status: "done"},
		{Kind: EvStatus, Status: "error"},
		{Kind: EvStatus, Status: "blocked", StatusKind: "permission"},
		{Kind: EvTick},
		{Kind: EvActivity, ScreenHash: "h2"},
		{Kind: EvExit, Reason: "exited"},
		{Kind: EvLost},
		{Kind: EvStop},
	}
	for _, st := range []string{PhaseStarting, PhaseWorking, PhaseIdle, PhaseBlocked} {
		for _, ev := range events {
			for _, role := range []string{RoleWorker, RoleReviewer, RoleCoordinator} {
				_, row, _ := StepRun(RunRecord{State: st, Role: role, StartedAt: now}, ev, s, now)
				if row.ID == "R37" {
					t.Errorf("live state %q role %q on %+v fell through to catch-all R37", st, role, ev)
				}
			}
		}
	}
	for _, ev := range events {
		rec, row, _ := StepRun(RunRecord{State: PhaseEnded, Result: EndExited}, ev, s, now)
		if row.ID != "R37" || rec.State != PhaseEnded || rec.Result != EndExited {
			t.Errorf("ended on %q = (%s, %s/%s), want (R37, ended/exited)", ev.Kind, row.ID, rec.State, rec.Result)
		}
	}
}

func TestRunRowsTable(t *testing.T) {
	def := DefaultSettings()
	start := RunRecord{State: PhaseStarting, Role: RoleWorker, StartedAt: now, LastActivity: now, PromptViaArgv: true}
	work := RunRecord{State: PhaseWorking, Role: RoleWorker, StartedAt: now, LastActivity: now, Status: "working", StatusSince: now, StatusReported: true}
	cases := []struct {
		name      string
		rec       RunRecord
		ev        RunEvent
		dt        time.Duration
		wantRow   string
		wantState string
		wantRes   string
		wantAsked []string
		check     func(*testing.T, RunRecord)
	}{
		{"R1 launched", RunRecord{State: PhaseStarting}, RunEvent{Kind: EvLaunched, Address: "t1", RunID: "r1", CreatedMS: 42}, 0,
			"R1", PhaseStarting, "", nil, func(t *testing.T, r RunRecord) {
				if r.Address != "t1" || r.ID != "r1" || r.CreatedMS != 42 || !r.StartedAt.Equal(now) {
					t.Errorf("R1 record = %+v", r)
				}
			}},
		{"R2 launch_failed", start, RunEvent{Kind: EvLaunchFailed}, 0, "R2", PhaseEnded, EndFailed, nil, nil},
		{"R3 trust dialog", start, RunEvent{Kind: EvScreen, Screen: ScreenTrust}, 0,
			"R3", PhaseStarting, "", []string{RunActKeys}, func(t *testing.T, r RunRecord) {
				if !r.ScreenAnswered || r.Screen != ScreenTrust {
					t.Errorf("R3 record = %+v", r)
				}
			}},
		{"R4 signed_out screen", start, RunEvent{Kind: EvScreen, Screen: ScreenSignedOut}, 0, "R4", PhaseEnded, EndSignedOut, []string{RunActStop}, nil},
		{"R5 limit screen", start, RunEvent{Kind: EvScreen, Screen: ScreenLimit}, 0, "R5", PhaseEnded, EndQuota, []string{RunActStop}, nil},
		{"R6 blank screen", start, RunEvent{Kind: EvScreen, Screen: ScreenNone}, 0, "R6", PhaseStarting, "", nil, nil},
		{"R7 idle sends first prompt", RunRecord{State: PhaseStarting, StartedAt: now}, RunEvent{Kind: EvStatus, Status: "idle"}, 0,
			"R7", PhaseWorking, "", []string{RunActSendFirst}, func(t *testing.T, r RunRecord) {
				if !r.PromptSent || r.Status != "working" {
					t.Errorf("R7 record = %+v", r)
				}
			}},
		{"R8 status working", start, RunEvent{Kind: EvStatus, Status: "working"}, 0, "R8", PhaseWorking, "", nil, nil},
		{"R9 argv done", start, RunEvent{Kind: EvStatus, Status: "done"}, 0, "R9", PhaseWorking, "", nil, nil},
		{"R10 worker error during start", start, RunEvent{Kind: EvStatus, Status: "error"}, 0, "R10", PhaseStarting, "", nil, nil},
		{"R11 blank start timeout", start, RunEvent{Kind: EvTick}, 2 * time.Minute, "R11", PhaseEnded, EndBlank, []string{RunActStop}, nil},
		{"R12 tick before start timeout", start, RunEvent{Kind: EvTick}, time.Minute, "R12", PhaseStarting, "", nil, nil},
		{"R12 activity updates clock", start, RunEvent{Kind: EvActivity, ScreenHash: "abc"}, 30 * time.Second,
			"R12", PhaseStarting, "", nil, func(t *testing.T, r RunRecord) {
				if !r.LastActivity.Equal(now.Add(30*time.Second)) || r.ScreenHash != "abc" {
					t.Errorf("R12 activity = %+v", r)
				}
			}},
		{"R13 resume failed early exit", RunRecord{State: PhaseWorking, StartedAt: now, Resume: true}, RunEvent{Kind: EvExit, Reason: "exited"}, 30 * time.Second,
			"R13", PhaseEnded, EndResumeFailed, []string{RunActRemove}, func(t *testing.T, r RunRecord) {
				if !r.NotResumable {
					t.Error("R13 did not set NotResumable")
				}
			}},
		{"R14 late auth block", work, RunEvent{Kind: EvStatus, Status: "blocked", StatusKind: "auth"}, 2 * time.Minute, "R14", PhaseEnded, EndSignedOut, []string{RunActStop}, nil},
		{"R15 permission block", start, RunEvent{Kind: EvStatus, Status: "blocked", StatusKind: "permission", Msg: "Trust this folder?"}, 0,
			"R15", PhaseBlocked, "", nil, func(t *testing.T, r RunRecord) {
				if r.BlockedKind != "permission" || r.Question != "Trust this folder?" || r.BlockedSince.IsZero() {
					t.Errorf("R15 record = %+v", r)
				}
			}},
		{"R16 reviewer error", RunRecord{State: PhaseWorking, Role: RoleReviewer, StartedAt: now}, RunEvent{Kind: EvStatus, Status: "error"}, 0,
			"R16", PhaseEnded, EndError, []string{RunActStop}, nil},
		{"R17 working status update", work, RunEvent{Kind: EvStatus, Status: "idle", Turn: 2}, 5 * time.Second,
			"R17", PhaseWorking, "", nil, func(t *testing.T, r RunRecord) {
				if r.Status != "idle" || r.Turn != 2 || !r.StatusSince.Equal(now.Add(5*time.Second)) {
					t.Errorf("R17 record = %+v", r)
				}
			}},
		{"R18 idle sends held message", RunRecord{State: PhaseWorking, Role: RoleWorker, Status: "idle", StatusSince: now, Held: []string{"m1", "m2"}}, RunEvent{Kind: EvTick}, 20 * time.Second,
			"R18", PhaseWorking, "", []string{RunActSendHeld}, func(t *testing.T, r RunRecord) {
				if !slices.Equal(r.Held, []string{"m2"}) {
					t.Errorf("R18 held = %v, want [m2]", r.Held)
				}
			}},
		{"R19 idle with limit screen", RunRecord{State: PhaseWorking, Status: "done", StatusSince: now}, RunEvent{Kind: EvTick, Screen: ScreenLimit}, 20 * time.Second,
			"R19", PhaseEnded, EndQuota, []string{RunActStop}, nil},
		{"R20 worker idle past grace", RunRecord{State: PhaseWorking, Role: RoleWorker, Status: "idle", StatusSince: now}, RunEvent{Kind: EvTick}, 20 * time.Second,
			"R20", PhaseIdle, "", nil, func(t *testing.T, r RunRecord) {
				if r.TaskRun().State != RunIdle || !r.LastTurnEnd.Equal(now.Add(20*time.Second)) {
					t.Errorf("R20 worker = %+v", r)
				}
			}},
		{"R20 reviewer idle reads report before held", RunRecord{State: PhaseWorking, Role: RoleReviewer, Status: "done", StatusSince: now, Held: []string{"followup"}}, RunEvent{Kind: EvTick}, 20 * time.Second,
			"R20", PhaseIdle, "", []string{RunActRead}, func(t *testing.T, r RunRecord) {
				if !r.ReportRead || len(r.Held) != 1 {
					t.Errorf("R20 reviewer = %+v", r)
				}
			}},
		{"R21 quiet working sends interrupt", work, RunEvent{Kind: EvTick}, 20 * time.Minute,
			"R21", PhaseWorking, "", []string{RunActInterrupt}, func(t *testing.T, r RunRecord) {
				if !r.InterruptedAt.Equal(now.Add(20 * time.Minute)) {
					t.Errorf("R21 InterruptedAt = %v", r.InterruptedAt)
				}
			}},
		{"R22 still quiet after interrupt stops as stuck", RunRecord{State: PhaseWorking, LastActivity: now, InterruptedAt: now.Add(20 * time.Minute)}, RunEvent{Kind: EvTick}, 20*time.Minute + 90*time.Second,
			"R22", PhaseEnded, EndStuck, []string{RunActStop}, nil},
		{"R23 activity after interrupt clears interrupt", RunRecord{State: PhaseWorking, LastActivity: now, InterruptedAt: now.Add(20 * time.Minute)}, RunEvent{Kind: EvActivity, ScreenHash: "h9"}, 20*time.Minute + 10*time.Second,
			"R23", PhaseWorking, "", nil, func(t *testing.T, r RunRecord) {
				if !r.InterruptedAt.IsZero() {
					t.Errorf("R23 did not clear InterruptedAt: %v", r.InterruptedAt)
				}
			}},
		{"R27 idle wakes on working", RunRecord{State: PhaseIdle, Role: RoleReviewer, ReportRead: true}, RunEvent{Kind: EvStatus, Status: "working"}, 0,
			"R27", PhaseWorking, "", nil, func(t *testing.T, r RunRecord) {
				if r.ReportRead {
					t.Error("R27 did not reset ReportRead")
				}
			}},
		{"R28 blocked to idle goes through working grace", RunRecord{State: PhaseBlocked, BlockedSince: now, BlockedKind: "question"}, RunEvent{Kind: EvStatus, Status: "idle"}, 5 * time.Second,
			"R28", PhaseWorking, "", nil, func(t *testing.T, r RunRecord) {
				if !r.BlockedSince.IsZero() || r.BlockedKind != "" {
					t.Errorf("R28 record = %+v", r)
				}
			}},
		{"R29 blocked past limit stops as stuck", RunRecord{State: PhaseBlocked, BlockedSince: now, BlockedKind: "permission"}, RunEvent{Kind: EvTick}, 10 * time.Minute,
			"R29", PhaseEnded, EndStuck, []string{RunActStop}, nil},
		{"R29 auth blocked past limit stops as signed_out", RunRecord{State: PhaseBlocked, BlockedSince: now, BlockedKind: "auth"}, RunEvent{Kind: EvTick}, 10 * time.Minute,
			"R29", PhaseEnded, EndSignedOut, []string{RunActStop}, nil},
		{"R30 idle tick stays idle", RunRecord{State: PhaseIdle}, RunEvent{Kind: EvTick}, 0, "R30", PhaseIdle, "", nil, nil},
		{"R33 exit removes terminal", work, RunEvent{Kind: EvExit, Reason: "killed"}, 0,
			"R33", PhaseEnded, EndExited, []string{RunActRemove}, func(t *testing.T, r RunRecord) {
				if r.Reason != "killed" || r.TaskRun().State != RunGone {
					t.Errorf("R33 exit = %+v", r)
				}
			}},
		{"R33 lost ends as lost", work, RunEvent{Kind: EvLost}, 0, "R33", PhaseEnded, EndLost, nil, nil},
		{"R34 stop ends as stopped", work, RunEvent{Kind: EvStop}, 0, "R34", PhaseEnded, EndStopped, []string{RunActStop}, nil},
		{"R35 late screen while working ignored", work, RunEvent{Kind: EvScreen, Screen: ScreenTrust}, 0, "R35", PhaseWorking, "", nil, nil},
		{"R37 ended ignores events", RunRecord{State: PhaseEnded, Result: EndStopped}, RunEvent{Kind: EvTick}, 0, "R37", PhaseEnded, EndStopped, nil, nil},
	}

	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, row, dec := StepRun(tc.rec, tc.ev, def, now.Add(tc.dt))
			seen[row.ID] = true
			if row.ID != tc.wantRow || got.State != tc.wantState || got.Result != tc.wantRes || !slices.Equal(dec.Asked, tc.wantAsked) {
				t.Fatalf("got row=%s state=%s(%s) asked=%v, want row=%s state=%s(%s) asked=%v",
					row.ID, got.State, got.Result, dec.Asked, tc.wantRow, tc.wantState, tc.wantRes, tc.wantAsked)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
	for _, r := range RunRows {
		if !seen[r.ID] {
			t.Errorf("row %s in RunRows was not exercised by TestRunRowsTable", r.ID)
		}
	}
}

// TestRunStartTrustVsBlank verifies KB-RUN-28 and KB-RUN-43: a start stuck at a
// trust/confirm/onboarding dialog is answered once (R3) and, if still unstarted
// at start_minutes, ends as stuck rather than blank.
func TestRunStartTrustVsBlank(t *testing.T) {
	def := DefaultSettings()
	blank := RunRecord{State: PhaseStarting, StartedAt: now, PromptViaArgv: true}
	blank, r, d := StepRun(blank, RunEvent{Kind: EvScreen, Screen: ScreenNone}, def, now)
	if r.ID != "R6" || len(d.Asked) != 0 {
		t.Fatalf("blank screen = %s %v, want R6 []", r.ID, d.Asked)
	}
	blank, r, _ = StepRun(blank, RunEvent{Kind: EvTick}, def, now.Add(time.Duration(def.StartMinutes)*time.Minute))
	if r.ID != "R11" || blank.State != PhaseEnded || blank.Result != EndBlank {
		t.Fatalf("blank timeout = %s %s(%s), want R11 ended(blank)", r.ID, blank.State, blank.Result)
	}

	trust := RunRecord{State: PhaseStarting, StartedAt: now, PromptViaArgv: true}
	trust, r, d = StepRun(trust, RunEvent{Kind: EvScreen, Screen: ScreenTrust}, def, now)
	if r.ID != "R3" || !slices.Equal(d.Asked, []string{RunActKeys}) {
		t.Fatalf("first trust screen = %s %v, want R3 [press_keys]", r.ID, d.Asked)
	}
	trust, r, d = StepRun(trust, RunEvent{Kind: EvScreen, Screen: ScreenTrust}, def, now.Add(15*time.Second))
	if r.ID != "R6" || len(d.Asked) != 0 {
		t.Fatalf("second trust screen = %s %v, want R6 [] (answered once)", r.ID, d.Asked)
	}
	trust, r, _ = StepRun(trust, RunEvent{Kind: EvTick}, def, now.Add(time.Duration(def.StartMinutes)*time.Minute))
	if r.ID != "R11" || trust.State != PhaseEnded || trust.Result != EndStuck {
		t.Fatalf("stuck trust timeout = %s %s(%s), want R11 ended(stuck)", r.ID, trust.State, trust.Result)
	}
}
