package factory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// The captured Codex work screens do not contain a quota banner. The Codex
// fixture uses their composer and footer with a synthetic CLI warning. The
// Claude fixture follows the limit banner already used in world_test.go.
func quotaCodexScreen(banner string) string {
	return "⚠ " + banner + "\n\n› Ask Codex to do anything\n\n  gpt-6.2-sol medium · 96% left\n"
}

func quotaClaudeScreen(banner string) string {
	return "⎿  " + banner + "\n\n────────────────────────────────────────\n❯ \n────────────────────────────────────────\n  ⏵⏵ bypass permissions on\n"
}

func TestQuotaReviewerEndKeepsRoundAndTry(t *testing.T) {
	isolateQuotaFixtureGit(t)
	ctx := context.Background()
	srv, eng, paths, id, head, _, runs, _ := codexSetup(t)
	now := time.Now().Add(time.Second).Truncate(time.Second)
	eng.Now = func() time.Time { return now }
	pools := newPoolFake(t)
	pools.healthy(now)
	attachQuotaPools(eng, pools)
	t.Cleanup(func() {
		if pools.underLock != 0 {
			t.Errorf("CodexBar was called %d times under the engine lock", pools.underLock)
		}
	})
	state := func() flow.State {
		t.Helper()
		s, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := state()
	s.Settings.PoolMinutes = 2
	s.Settings.StartMinutes = 5
	if err := SaveState(paths.State(id), s); err != nil {
		t.Fatal(err)
	}
	pass := func() { eng.passProject(ctx, id, false) }
	pass() // N26
	pass() // N29 starts Codex reviewer
	rev := state().Tasks[0].Attempts[0].Review
	if rev == nil || rev.Round != 2 || rev.Head != head || rev.RunID == "" || rev.Run.State != flow.PhaseStarting || rev.Reviewer.CLI != "codex" {
		t.Fatalf("fixture did not start Codex round 2 at %s: %+v", head, rev)
	}
	before := roleRuns(*runs, flow.RoleReviewer)
	reviewer := rev.RunID
	now = now.Add(2 * time.Minute)
	pools.set("codex", poolOut("codex", now, nil, &poolWin{5, 3 * time.Hour}, nil))
	pass()
	if sends := sendsTo(t, srv.Requests(), reviewer); len(sends) != 0 {
		t.Errorf("reviewer got %d handoff sends from a pool reading, want zero (R32)", len(sends))
	}
	if term, err := hc.NewClient(srv.Path).Get(ctx, reviewer); err != nil || term.State != "live" {
		t.Fatalf("floor reading stopped the reviewer before a real quota end: state %q, error %v", term.State, err)
	}
	srv.SetScreen(reviewer, quotaCodexScreen("You've hit your usage limit."))
	now = now.Add(2 * time.Minute)
	// The backup pool resets later than the new Codex cooldown. CodexBar
	// still reports Codex healthy, so only a recorded R5 can hold it.
	pools.healthy(now)
	pools.set("claude", poolOut("claude", now, &poolWin{5, 3 * time.Hour}, &poolWin{60, 72 * time.Hour}, nil))
	reset := now.Add(2 * time.Minute)
	pass()
	s = state()
	r := s.Tasks[0].Attempts[0].Review
	if r == nil || r.Run == nil || r.Run.Result != flow.EndQuota {
		t.Fatalf("starting reviewer did not take the real R5 quota end: %+v", r)
	}
	if term, err := hc.NewClient(srv.Path).Get(ctx, reviewer); err == nil && term.State == "live" {
		t.Fatal("reviewer terminal still live after R5")
	}
	for range 3 {
		pass()
	}
	s = state()
	r = s.Tasks[0].Attempts[0].Review
	if r == nil || r.Round != rev.Round || r.Head != head || r.Retries != rev.Retries || failedWake(s) || roleRuns(*runs, flow.RoleReviewer) != before {
		t.Errorf("quota wait must preserve the review, spend no try and send no failure wake: review %+v, reviewer launches %d (was %d), failed wake %v", r, roleRuns(*runs, flow.RoleReviewer), before, failedWake(s))
	}
	why := s.Tasks[0].HoldWhy
	if !strings.HasPrefix(why, "All reviewers are out of quota; retries at ") || !reasonHasTime(why, reset) {
		t.Errorf("reviewer hold reason = %q, want KB-QTA-8 message naming %s", why, reset.Format(time.RFC3339))
	}
	waits := 0
	for _, line := range logRules(t, paths.Log(id), "N29") {
		if line.Decision == "waiting" && strings.Contains(line.Why, "out of quota") {
			waits++
		}
	}
	if waits != 1 {
		t.Errorf("reviewer quota wait logged %d times, want once", waits)
	}
	now = reset
	pools.set("codex", poolOut("codex", now, nil, &poolWin{70, 80 * time.Hour}, nil))
	pass()
	r = state().Tasks[0].Attempts[0].Review
	if r == nil || r.Round != rev.Round || r.Head != head || r.Retries != rev.Retries || roleRuns(*runs, flow.RoleReviewer) != before+1 {
		t.Errorf("at reset: review %+v, reviewer launches %d, want the pinned round retried with no try spent", r, roleRuns(*runs, flow.RoleReviewer))
	}
}

func TestQuotaCoordinatorEndRequeuesWakeUntilCooldownEnds(t *testing.T) {
	q := newQuotaHistory(t, codexCoordOnly)
	q.edit(func(s *flow.State) { s.Settings.CoordinatorGapMinutes = 1 })
	q.pass()
	coord := q.state().Project.RunID
	const key = "worker_failed"
	q.edit(func(s *flow.State) {
		s.Project.Wakes = []flow.Wake{{Task: "T1", Key: key, Why: "worker needs help", At: *q.now}}
	})
	q.srv.SetScreen(coord, codexIdleScreen("Initial goal", 15))
	q.turnEnds()
	if s := q.state().Project; len(s.CoordWakesInFlight) != 1 || len(s.Wakes) != 0 {
		t.Fatalf("fixture wake did not start a turn: queued %d, in flight %d", len(s.Wakes), len(s.CoordWakesInFlight))
	}

	q.srv.SetScreen(coord, codexWorkingScreen("Wake"))
	*q.now = q.now.Add(time.Minute)
	q.atFloor("codex", q.now.Add(2*time.Hour))
	q.pass()
	if q.handoffs(coord) != 0 || len(sendsTo(t, q.srv.Requests(), coord)) != 1 {
		t.Error("R32 sent a handoff to the coordinator at the floor")
	}
	q.assertLive(coord)

	*q.now = q.now.Add(time.Minute)
	q.pools.healthy(*q.now)
	q.srv.SetScreen(coord, codexIdleScreen("Wake", 17))
	q.pass() // idle observed, still in grace
	q.srv.SetScreen(coord, quotaCodexScreen("You've hit your usage limit."))
	*q.now = q.now.Add(25 * time.Second)
	reset := q.now.Add(time.Minute)
	q.pass() // R19, then J7 gated by the cooldown
	if !q.stopped(coord) || len(logRules(t, q.paths.Log(q.id), "R19")) != 1 {
		t.Fatal("coordinator did not take the real R19 quota end")
	}
	p := q.state().Project
	if len(p.Wakes) != 1 || p.Wakes[0].Key != key || len(p.CoordWakesInFlight) != 0 {
		t.Errorf("quota-ended coordinator wake: queued %+v, in flight %+v, want original wake queued once", p.Wakes, p.CoordWakesInFlight)
	}
	q.restart()
	if n := roleRuns(*q.runs, flow.RoleCoordinator); n != 1 {
		t.Errorf("coordinator launches during cooldown = %d, want only the original", n)
	}
	*q.now = reset
	q.pass()
	replacement := q.state().Project.RunID
	if replacement == "" || replacement == coord {
		t.Fatalf("no replacement coordinator at reset: address %q", replacement)
	}
	q.srv.SetScreen(replacement, codexIdleScreen("Initial goal after quota", 18))
	q.turnEnds()
	sends := sendsTo(t, q.srv.Requests(), replacement)
	if len(sends) != 1 || !strings.Contains(sends[0], key) {
		t.Errorf("replacement coordinator received %q, want the original wake once", sends)
	}
}

func TestQuotaCooldownUsesOnlyTheRealLimitBanner(t *testing.T) {
	const noReset = "You've hit your limit"
	const realReset = "You've hit your limit · resets 11am (America/Los_Angeles)"
	const quoted = "⏺ You've hit your limit · resets 10am (America/Los_Angeles)\n"
	for _, tc := range []struct {
		name, screen, row string
		printed           bool
	}{
		{"printed reset from R5", quotaClaudeScreen(realReset), "R5", true},
		{"unprinted reset from R19", quotaClaudeScreen(noReset), "R19", false},
		{"quoted reset above the real banner", quoted + strings.Repeat("  ok  internal/workerlog 0.01s\n", 15) + quotaClaudeScreen(noReset), "R19", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := newQuotaHistory(t, codexCoordOnly)
			const poolMinutes = 3
			q.edit(func(s *flow.State) { s.Settings.PoolMinutes = poolMinutes })
			q.startWorkers(poolTask("T1", ""))
			w := q.attempt(0).RunID
			if tc.row == "R19" {
				q.status(w, "working")
				q.pass()
				q.status(w, "idle")
				q.pass()
				*q.now = q.now.Add(25 * time.Second)
			}
			q.srv.SetScreen(w, tc.screen)
			q.pass()
			if a := q.attempt(0); a.Run.Result != flow.EndQuota || len(logRules(t, q.paths.Log(q.id), tc.row)) != 1 {
				t.Fatalf("fixture did not reach real %s quota end: state %q, result %q", tc.row, a.Run.State, a.Run.Result)
			}
			reset := q.now.Add(poolMinutes * time.Minute)
			if tc.printed {
				// 11 AM Pacific (standard time) on the fixture's November day.
				reset = time.Date(2026, 11, 20, 19, 0, 0, 0, time.UTC)
			}

			// Schedule a different task on the same pool. This proves the
			// cooldown gates launches even independently of T1's recovery.
			token := (*q.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
			if err := q.eng.Report(token, "plan amend", []byte(`{"why":"Measure the launch gate after a quota end.","tasks":[`+poolTask("T2", "")+`]}`)); err != nil {
				t.Fatalf("queue probe task: %v", err)
			}
			*q.now = reset.Add(-time.Second)
			// This successful reading is newer than the quota end. It has
			// its own (different) future reset and must leave the hold in place.
			q.pools.healthy(*q.now)
			checkHold := func(step string) {
				t.Helper()
				probe := q.state().Tasks[1]
				runs := roleRuns(*q.runs, flow.RoleWorker)
				if len(probe.Attempts) != 0 || !strings.Contains(strings.ToLower(probe.HoldWhy), "quota") || !reasonHasTime(probe.HoldWhy, reset) || runs != 1 {
					t.Errorf("%s, one second before %s: probe attempts = %d, worker launches = %d, hold = %q, want only the original launch and a hold until that time", step, reset.Format(time.RFC3339), len(probe.Attempts), runs, probe.HoldWhy)
				}
			}
			reads := q.pools.claudeReads()
			q.pass()
			if q.pools.claudeReads() <= reads {
				t.Fatal("fixture did not refresh Claude with the newer usable reading")
			}
			checkHold("after the usable reading")
			q.restart()
			q.pass()
			checkHold("after restart")
			*q.now = reset
			q.pass()
			q.pass()
			if probe := q.state().Tasks[1]; len(probe.Attempts) != 1 || probe.HoldWhy != "" {
				t.Errorf("at reset %s: probe attempts = %d, hold = %q, want the probe started", reset.Format(time.RFC3339), len(probe.Attempts), probe.HoldWhy)
			}
		})
	}
}
