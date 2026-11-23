package factory

// PR 5b.7a round 3, TDD step 1: failing tests for Carbon's round-2 majors and agy Gemini pool gating.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

func newRound3QuotaHistory(t *testing.T, models map[string][]flow.Candidate) *quotaHistory {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	return newQuotaHistory(t, models)
}

func setClaudeIdleAt(t *testing.T, q *quotaHistory, address string, at time.Time) {
	t.Helper()
	q.status(address, "idle")
	term, err := q.cl.Get(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	ms := uint64(at.UnixMilli())
	term.StatusMS = &ms
	q.srv.Add(term)
}

// assertProbeHeldUntilReset schedules T2 via the coordinator's token and checks
// that the recorded quota cooldown holds T2 one second before wantReset (even
// with a fresh healthy pool reading and across a restart) and launches T2 at
// wantReset.
func assertProbeHeldUntilReset(t *testing.T, q *quotaHistory, req string, wantReset time.Time) {
	t.Helper()
	tok := (*q.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	if err := q.eng.Report(tok, "plan amend", []byte(`{"why":"probe recorded cooldown","tasks":[`+poolTask("T2", req)+`]}`)); err != nil {
		t.Fatalf("plan amend probe task: %v", err)
	}

	*q.now = wantReset.Add(-time.Second)
	q.pools.healthy(*q.now)
	q.pass()
	if probe := q.state().Tasks[1]; len(probe.Attempts) != 0 || !strings.Contains(strings.ToLower(probe.HoldWhy), "quota") || !reasonHasTime(probe.HoldWhy, wantReset) {
		t.Errorf("one second before %s: probe attempts=%d hold=%q, want held on quota until %s",
			wantReset.Format(time.RFC3339), len(probe.Attempts), probe.HoldWhy, wantReset.Format(time.RFC3339))
	}

	q.restart()
	q.pools.healthy(*q.now)
	q.pass()
	if probe := q.state().Tasks[1]; len(probe.Attempts) != 0 || !reasonHasTime(probe.HoldWhy, wantReset) {
		t.Errorf("after restart one second before %s: probe attempts=%d hold=%q, want held until %s",
			wantReset.Format(time.RFC3339), len(probe.Attempts), probe.HoldWhy, wantReset.Format(time.RFC3339))
	}

	*q.now = wantReset
	q.pools.healthy(*q.now)
	q.pass()
	q.pass()
	if probe := q.state().Tasks[1]; len(probe.Attempts) != 1 || probe.HoldWhy != "" {
		t.Errorf("at reset %s: probe attempts=%d hold=%q, want probe started",
			wantReset.Format(time.RFC3339), len(probe.Attempts), probe.HoldWhy)
	}
}

// Major 1: a working Codex worker and a working Codex coordinator that hit the
// usage limit mid-turn (the limit message and idle composer drawn directly,
// with no clean idle screen first; also the footer variant without " · ") end
// with quota after idle_grace_seconds through R19, record the printed reset as
// the cooldown, and requeue the coordinator's in-flight wake.
func TestQuotaCodexMidTurnLimitEndsAfterGraceAndRequeuesWake(t *testing.T) {
	const poolMinutes = 3
	const codexReq = `,"request":{"worker":[{"cli":"codex","model":"` + codexModel + `","effort":"medium"}]}`

	reset2PM := time.Date(2026, 11, 20, 22, 0, 0, 0, time.UTC)    // 2:00 PM PST on codexRolesT0 (2026-11-20)
	reset230PM := time.Date(2026, 11, 20, 22, 30, 0, 0, time.UTC) // 2:30 PM PST on codexRolesT0

	variants := []struct {
		name      string
		screen    string
		wantReset time.Time
	}{
		{
			name:      "warning above composer",
			screen:    quotaCodexScreen("You've hit your usage limit · try again at 2:00 PM (America/Los_Angeles)"),
			wantReset: reset2PM,
		},
		{
			name: "footer without middle dot",
			screen: "› Continue the current turn\n\n" +
				"› Ask Codex to do anything\n\n" +
				"  You've hit your usage limit, try again at 2:30 PM (America/Los_Angeles)\n",
			wantReset: reset230PM,
		},
	}

	for _, v := range variants {
		t.Run(v.name+"/worker", func(t *testing.T) {
			models := map[string][]flow.Candidate{
				flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "medium"}},
				flow.RoleWorker:      {{CLI: "codex", Model: codexModel, Effort: "medium"}},
			}
			q := newRound3QuotaHistory(t, models)
			q.edit(func(s *flow.State) { s.Settings.PoolMinutes = poolMinutes })
			q.startWorkers(poolTask("T1", codexReq))
			w := q.attempt(0).RunID

			q.srv.SetScreen(w, codexWorkingScreen("Implement task T1"))
			q.pass()
			if r := q.attempt(0).Run; r == nil || r.State != flow.PhaseWorking || r.Status != "working" {
				t.Fatalf("fixture Codex worker = %+v, want working", r)
			}

			// Mid-turn: Codex draws the limit message and idle composer directly,
			// with no unbannered codexIdleScreen in between.
			*q.now = q.now.Add(10 * time.Second)
			limitAt := *q.now
			q.srv.SetScreen(w, v.screen)
			q.pass()

			// Before idle_grace_seconds (20s), the worker must still be live.
			*q.now = limitAt.Add(19 * time.Second)
			q.pass()
			q.assertLive(w)
			if a := q.attempt(0); a.Run.State != flow.PhaseWorking {
				t.Fatalf("before idle_grace_seconds: worker state=%q result=%q, want working", a.Run.State, a.Run.Result)
			}

			// At idle_grace_seconds, R19 stops the worker with quota and records
			// the printed reset time.
			*q.now = limitAt.Add(20 * time.Second)
			q.pools.healthy(*q.now)
			q.pass()
			a := q.attempt(0)
			if a.Run.State != flow.PhaseEnded || a.Run.Result != flow.EndQuota || !q.stopped(w) || len(logRules(t, q.paths.Log(q.id), "R19")) != 1 {
				t.Errorf("after idle_grace_seconds on mid-turn Codex limit: state=%q result=%q stopped=%v R19=%d, want ended(quota) via R19",
					a.Run.State, a.Run.Result, q.stopped(w), len(logRules(t, q.paths.Log(q.id), "R19")))
			}

			assertProbeHeldUntilReset(t, q, codexReq, v.wantReset)
		})

		t.Run(v.name+"/coordinator", func(t *testing.T) {
			q := newRound3QuotaHistory(t, codexCoordOnly)
			q.edit(func(s *flow.State) {
				s.Settings.CoordinatorGapMinutes = 1
				s.Settings.PoolMinutes = poolMinutes
			})
			q.pass()
			coord := q.state().Project.RunID
			if coord == "" {
				t.Fatal("fixture did not launch Codex coordinator")
			}

			const key = "worker_failed"
			q.edit(func(s *flow.State) {
				s.Project.Wakes = []flow.Wake{{Task: "T1", Key: key, Why: "worker needs coordinator decision", At: *q.now}}
			})
			q.srv.SetScreen(coord, codexIdleScreen("Initial goal", 15))
			q.turnEnds()
			if p := q.state().Project; len(p.CoordWakesInFlight) != 1 || len(p.Wakes) != 0 {
				t.Fatalf("fixture wake did not start a turn: queued=%+v inFlight=%+v", p.Wakes, p.CoordWakesInFlight)
			}

			q.srv.SetScreen(coord, codexWorkingScreen("Handle wake"))
			*q.now = q.now.Add(10 * time.Second)
			q.pass()
			if r := q.state().Project.Run; r == nil || r.State != flow.PhaseWorking || r.Status != "working" {
				t.Fatalf("fixture Codex coordinator = %+v, want working", r)
			}

			// Mid-turn: coordinator hits the usage limit directly with no clean
			// idle screen in between.
			*q.now = q.now.Add(10 * time.Second)
			limitAt := *q.now
			q.srv.SetScreen(coord, v.screen)
			q.pass()

			*q.now = limitAt.Add(19 * time.Second)
			q.pass()
			q.assertLive(coord)

			*q.now = limitAt.Add(20 * time.Second)
			q.pools.healthy(*q.now)
			q.pass()
			if !q.stopped(coord) || len(logRules(t, q.paths.Log(q.id), "R19")) != 1 {
				t.Errorf("after idle_grace_seconds on mid-turn Codex coordinator limit: stopped=%v R19=%d, want stopped via R19",
					q.stopped(coord), len(logRules(t, q.paths.Log(q.id), "R19")))
			}

			p := q.state().Project
			if p.Run == nil || p.Run.Result != flow.EndQuota {
				t.Errorf("coordinator run after R19 = %+v, want Result=%q", p.Run, flow.EndQuota)
			}
			if len(p.Wakes) != 1 || p.Wakes[0].Key != key || len(p.CoordWakesInFlight) != 0 {
				t.Errorf("coordinator wakes after mid-turn R19: queued=%+v inFlight=%+v, want %q requeued once",
					p.Wakes, p.CoordWakesInFlight, key)
			}
			if !q.stopped(coord) {
				return
			}

			// Cooldown from the printed reset must gate J7 until wantReset, even
			// past CoordinatorGapMinutes and PoolMinutes and across a restart.
			*q.now = v.wantReset.Add(-time.Second)
			q.pools.healthy(*q.now)
			q.pass()
			q.restart()
			q.pools.healthy(*q.now)
			q.pass()
			if n := roleRuns(*q.runs, flow.RoleCoordinator); n != 1 {
				t.Fatalf("coordinator launches one second before printed reset %s = %d, want 1", v.wantReset.Format(time.RFC3339), n)
			}

			*q.now = v.wantReset
			q.pools.healthy(*q.now)
			q.pass()
			rep := q.state().Project.RunID
			if rep == "" || rep == coord {
				t.Fatalf("at printed reset %s: coordinator RunID=%q, want replacement launched", v.wantReset.Format(time.RFC3339), rep)
			}
			q.srv.SetScreen(rep, codexIdleScreen("Initial goal after quota", 18))
			q.turnEnds()
			sends := sendsTo(t, q.srv.Requests(), rep)
			if len(sends) != 1 || !strings.Contains(sends[0], key) {
				t.Errorf("replacement coordinator sends = %q, want requeued wake %q once", sends, key)
			}
		})
	}
}

// Major 2: when a worker shows its CLI's own usage-limit message while its pool
// is also at the floor, R19 wins. Nothing is sent into the limit screen (no
// interrupt, no handoff text), the run ends with quota at once, and the printed
// reset is recorded. The handoff deadline may be set, but it never delays R19.
func TestQuotaLimitScreenAtPoolFloorEndsViaR19WithoutHandoffSend(t *testing.T) {
	reset2PM := time.Date(2026, 11, 20, 22, 0, 0, 0, time.UTC) // 2:00 PM PST on codexRolesT0 (2026-11-20)
	const handoffMinutes = 5

	assertNothingSent := func(t *testing.T, q *quotaHistory, offset int, address string) {
		t.Helper()
		methods := quotaTerminalCalls(q.srv.Requests()[offset:], address)
		for _, bad := range []string{"terminal.interrupt", "terminal.send", "terminal.keys"} {
			if slices.Contains(methods, bad) {
				t.Errorf("worker showing limit screen received %s at pool floor (calls=%v)", bad, methods)
			}
		}
		if got := q.handoffs(address); got != 0 {
			t.Errorf("worker showing limit screen received %d floor handoffs, want 0", got)
		}
	}

	t.Run("claude past idle grace ends at once on floor pass", func(t *testing.T) {
		q := newRound3QuotaHistory(t, codexCoordOnly)
		q.setHandoffMinutes(handoffMinutes)
		q.startWorkers(poolTask("T1", ""))
		w := q.attempt(0).RunID
		q.status(w, "working")
		q.pass()

		// Worker hits its CLI limit and enters idle while the cached pool reading
		// is still within PoolMinutes (1m).
		t1 := q.now.Add(15 * time.Second)
		*q.now = t1
		q.srv.SetScreen(w, quotaClaudeScreen("You've hit your limit · resets 2pm (America/Los_Angeles)"))
		setClaudeIdleAt(t, q, w, t1)
		q.pass()

		// Past idle_grace_seconds and past PoolMinutes: CodexBar now reports the
		// pool at the floor with a short 10m window reset, while the limit screen
		// prints 2:00 PM PST. R19 must win at once on this pass without waiting
		// handoff_minutes.
		t2 := t1.Add(time.Minute)
		*q.now = t2
		q.atFloor("claude", t2.Add(10*time.Minute))
		offset := len(q.srv.Requests())
		q.pass()

		assertNothingSent(t, q, offset, w)
		a := q.attempt(0)
		if a.Run.State != flow.PhaseEnded || a.Run.Result != flow.EndQuota || !a.Run.EndedAt.Equal(t2) || !q.stopped(w) || len(logRules(t, q.paths.Log(q.id), "R19")) != 1 {
			t.Errorf("at floor pass t2: state=%q result=%q endedAt=%v (want %v) stopped=%v R19=%d, want ended(quota) at once via R19",
				a.Run.State, a.Run.Result, a.Run.EndedAt, t2, q.stopped(w), len(logRules(t, q.paths.Log(q.id), "R19")))
		}

		assertProbeHeldUntilReset(t, q, "", reset2PM)
	})

	t.Run("claude limit screen and floor observed together before grace elapses", func(t *testing.T) {
		q := newRound3QuotaHistory(t, codexCoordOnly)
		q.setHandoffMinutes(handoffMinutes)
		q.startWorkers(poolTask("T1", ""))
		q.srv.SetScreen(q.state().Project.RunID, codexWorkingScreen("Coordinate"))
		w := q.attempt(0).RunID
		q.status(w, "working")
		q.pass()

		// Past PoolMinutes (1m): limit screen, idle status, and pool floor all
		// arrive on the same pass.
		t1 := q.now.Add(65 * time.Second)
		*q.now = t1
		q.atFloor("claude", t1.Add(10*time.Minute))
		q.srv.SetScreen(w, quotaClaudeScreen("You've hit your limit · resets 2pm (America/Los_Angeles)"))
		setClaudeIdleAt(t, q, w, t1)
		offset := len(q.srv.Requests())
		q.pass()
		assertNothingSent(t, q, offset, w)

		// As soon as idle_grace_seconds (20s) elapses — well before the 5-minute
		// handoff deadline — R19 ends the run with quota and records 2:00 PM.
		t2 := t1.Add(20 * time.Second)
		*q.now = t2
		q.pass()
		assertNothingSent(t, q, offset, w)

		a := q.attempt(0)
		if a.Run.State != flow.PhaseEnded || a.Run.Result != flow.EndQuota || !a.Run.EndedAt.Equal(t2) || !q.stopped(w) || len(logRules(t, q.paths.Log(q.id), "R19")) != 1 {
			t.Errorf("after idle_grace_seconds with floor deadline pending: state=%q result=%q endedAt=%v (want %v) stopped=%v R19=%d, want ended(quota) via R19",
				a.Run.State, a.Run.Result, a.Run.EndedAt, t2, q.stopped(w), len(logRules(t, q.paths.Log(q.id), "R19")))
		}

		assertProbeHeldUntilReset(t, q, "", reset2PM)
	})

	t.Run("codex past idle grace ends at once on floor pass", func(t *testing.T) {
		const codexReq = `,"request":{"worker":[{"cli":"codex","model":"` + codexModel + `","effort":"medium"}]}`
		models := map[string][]flow.Candidate{
			flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "medium"}},
			flow.RoleWorker:      {{CLI: "codex", Model: codexModel, Effort: "medium"}},
		}
		q := newRound3QuotaHistory(t, models)
		q.setHandoffMinutes(handoffMinutes)
		q.startWorkers(poolTask("T1", codexReq))
		w := q.attempt(0).RunID
		q.srv.SetScreen(w, codexWorkingScreen("Implement task T1"))
		q.pass()

		t1 := q.now.Add(10 * time.Second)
		*q.now = t1
		q.srv.SetScreen(w, codexIdleScreen("Implement task T1", 10))
		q.pass()
		q.srv.SetScreen(w, quotaCodexScreen("You've hit your usage limit · try again at 2:00 PM (America/Los_Angeles)"))

		t2 := t1.Add(time.Minute)
		*q.now = t2
		q.atFloor("codex", t2.Add(10*time.Minute))
		offset := len(q.srv.Requests())
		q.pass()

		assertNothingSent(t, q, offset, w)
		a := q.attempt(0)
		if a.Run.State != flow.PhaseEnded || a.Run.Result != flow.EndQuota || !a.Run.EndedAt.Equal(t2) || !q.stopped(w) || len(logRules(t, q.paths.Log(q.id), "R19")) != 1 {
			t.Errorf("codex at floor pass t2: state=%q result=%q endedAt=%v (want %v) stopped=%v R19=%d, want ended(quota) at once via R19",
				a.Run.State, a.Run.Result, a.Run.EndedAt, t2, q.stopped(w), len(logRules(t, q.paths.Log(q.id), "R19")))
		}

		assertProbeHeldUntilReset(t, q, codexReq, reset2PM)
	})
}

// Major 3: agent output at the bottom of the screen — including indented
// continuation lines of a reply and a screen where the input box is not in the
// last lines — never counts as the CLI's limit message (no quota, no cooldown),
// while a genuine limit message in the CLI's own area still counts.
func TestQuotaAgentOutputAtScreenBottomNeverCountsAsLimit(t *testing.T) {
	const codexReq = `,"request":{"worker":[{"cli":"codex","model":"` + codexModel + `","effort":"medium"}]}`

	noLimitScreens := []struct {
		name, cli, screen string
	}{
		{
			name: "claude indented reply continuation at bottom",
			cli:  "claude",
			screen: "❯ Show the limit examples from workerlog.\n\n" +
				"⏺ Here is the example banner from workerlog:\n" +
				"  You've hit your limit · resets 11pm (America/Los_Angeles)\n" +
				"  Claude AI usage limit reached · resets 11pm (America/Los_Angeles)\n",
		},
		{
			name: "claude input box and reply marker pushed above last lines",
			cli:  "claude",
			screen: "────────────────────────────────────────\n" +
				"❯ Print every fixture line in workerlog.\n" +
				"────────────────────────────────────────\n\n" +
				"⏺ The workerlog lines are:\n" +
				strings.Repeat("  ordinary agent continuation line\n", 14) +
				"  You've hit your limit · resets 11pm (America/Los_Angeles)\n",
		},
		{
			name: "claude tool output at bottom below earlier input box",
			cli:  "claude",
			screen: "────────────────────────────────────────\n" +
				"❯ Read internal/workerlog/errors.go\n" +
				"────────────────────────────────────────\n" +
				"● Read(internal/workerlog/errors.go) (ctrl+o to expand)\n" +
				"  ⎿  const sampleBanner =\n" +
				"       You've hit your limit · resets 11pm (America/Los_Angeles)\n",
		},
		{
			name: "codex indented reply continuation at bottom",
			cli:  "codex",
			screen: "› Show the limit examples from workerlog.\n\n" +
				"• The CLI banner looks like:\n" +
				"  You've hit your usage limit · try again at 11:00 PM (America/Los_Angeles)\n",
		},
		{
			name: "codex composer and reply marker pushed above last lines",
			cli:  "codex",
			screen: "› Print every fixture line in workerlog.\n\n" +
				"• The workerlog lines are:\n" +
				strings.Repeat("  ordinary agent continuation line\n", 14) +
				"  You've hit your usage limit · try again at 11:00 PM (America/Los_Angeles)\n",
		},
		{
			name: "codex tool output continuation at bottom below earlier composer",
			cli:  "codex",
			screen: "› Run cat internal/workerlog/errors.go\n\n" +
				"• Ran cat internal/workerlog/errors.go\n" +
				"  └ const sampleBanner =\n" +
				"    You've hit your usage limit · try again at 11:00 PM (America/Los_Angeles)\n",
		},
	}

	for _, tc := range noLimitScreens {
		for _, phase := range []string{"R5 starting", "R19 idle"} {
			t.Run("no_limit/"+tc.name+"/"+phase, func(t *testing.T) {
				models := codexCoordOnly
				req := ""
				if tc.cli == "codex" {
					models = map[string][]flow.Candidate{
						flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "medium"}},
						flow.RoleWorker:      {{CLI: "codex", Model: codexModel, Effort: "medium"}},
					}
					req = codexReq
				}
				q := newRound3QuotaHistory(t, models)
				q.startWorkers(poolTask("T1", req))
				w := q.attempt(0).RunID

				if phase == "R19 idle" {
					if tc.cli == "claude" {
						q.status(w, "working")
						q.pass()
						q.srv.SetScreen(w, tc.screen)
						q.status(w, "idle")
						q.turnEnds()
					} else {
						q.srv.SetScreen(w, codexWorkingScreen("Check workerlog"))
						q.pass()
						q.srv.SetScreen(w, codexIdleScreen("Check workerlog", 12))
						q.pass()
						q.srv.SetScreen(w, tc.screen)
						*q.now = q.now.Add(25 * time.Second)
						q.pass()
						q.pass()
					}
				} else {
					q.srv.SetScreen(w, tc.screen)
					q.pass()
				}

				a := q.attempt(0)
				if a.Run.State == flow.PhaseEnded || a.Run.Result == flow.EndQuota || q.stopped(w) {
					t.Errorf("bottom agent output ended %s (%s): state=%q result=%q, want live run",
						tc.cli, phase, a.Run.State, a.Run.Result)
				}

				tok := (*q.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
				if err := q.eng.Report(tok, "plan amend", []byte(`{"why":"probe false cooldown","tasks":[`+poolTask("T2", req)+`]}`)); err != nil {
					t.Fatal(err)
				}
				q.pass()
				q.pass()
				if probe := q.state().Tasks[1]; len(probe.Attempts) != 1 || probe.HoldWhy != "" {
					t.Errorf("bottom agent output set a pool cooldown on %s (%s): attempts=%d hold=%q, want T2 started with no hold",
						tc.cli, phase, len(probe.Attempts), probe.HoldWhy)
				}
			})
		}
	}

	const poolMinutes = 3
	reset2PM := time.Date(2026, 11, 20, 22, 0, 0, 0, time.UTC) // 2:00 PM PST on codexRolesT0 (2026-11-20)

	genuineScreens := []struct {
		name, cli, phase, screen string
	}{
		{
			name:  "claude genuine UI banner below indented reply quote",
			cli:   "claude",
			phase: "R19",
			screen: "❯ Show the limit examples.\n\n" +
				"⏺ Here is the example:\n" +
				"  You've hit your limit · resets 9pm (America/Los_Angeles)\n\n" +
				quotaClaudeScreen("You've hit your limit · resets 2pm (America/Los_Angeles)"),
		},
		{
			name:   "claude genuine bare startup limit",
			cli:    "claude",
			phase:  "R5",
			screen: "You've hit your limit · resets 2pm (America/Los_Angeles)\n",
		},
		{
			name:  "codex genuine UI banner below indented reply quote",
			cli:   "codex",
			phase: "R19",
			screen: "› Show the limit examples.\n\n" +
				"• The CLI banner looks like:\n" +
				"  You've hit your usage limit · try again at 9:00 PM (America/Los_Angeles)\n\n" +
				quotaCodexScreen("You've hit your usage limit · try again at 2:00 PM (America/Los_Angeles)"),
		},
		{
			name:   "codex genuine bare startup limit",
			cli:    "codex",
			phase:  "R5",
			screen: "You've hit your usage limit · try again at 2:00 PM (America/Los_Angeles)\n",
		},
	}

	for _, tc := range genuineScreens {
		t.Run("genuine/"+tc.name, func(t *testing.T) {
			models := codexCoordOnly
			req := ""
			if tc.cli == "codex" {
				models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "medium"}},
					flow.RoleWorker:      {{CLI: "codex", Model: codexModel, Effort: "medium"}},
				}
				req = codexReq
			}
			q := newRound3QuotaHistory(t, models)
			q.edit(func(s *flow.State) { s.Settings.PoolMinutes = poolMinutes })
			q.startWorkers(poolTask("T1", req))
			w := q.attempt(0).RunID

			switch {
			case tc.phase == "R5":
				q.srv.SetScreen(w, tc.screen)
				q.pass()
			case tc.cli == "claude":
				q.status(w, "working")
				q.pass()
				q.status(w, "idle")
				q.pass()
				*q.now = q.now.Add(25 * time.Second)
				q.srv.SetScreen(w, tc.screen)
				q.pass()
			default:
				q.srv.SetScreen(w, codexWorkingScreen("Work"))
				q.pass()
				q.srv.SetScreen(w, codexIdleScreen("Work", 12))
				q.pass()
				q.srv.SetScreen(w, tc.screen)
				*q.now = q.now.Add(25 * time.Second)
				q.pass()
			}

			a := q.attempt(0)
			if a.Run.State != flow.PhaseEnded || a.Run.Result != flow.EndQuota || !q.stopped(w) || len(logRules(t, q.paths.Log(q.id), tc.phase)) != 1 {
				t.Fatalf("genuine %s %s limit did not end with quota: state=%q result=%q stopped=%v %s=%d",
					tc.cli, tc.phase, a.Run.State, a.Run.Result, q.stopped(w), tc.phase, len(logRules(t, q.paths.Log(q.id), tc.phase)))
			}

			assertProbeHeldUntilReset(t, q, req, reset2PM)
		})
	}
}

func antigravityPoolOut(at time.Time, fiveLeft int, fiveResetIn time.Duration, weekLeft int) CommandOutput {
	fiveReset := at.Add(fiveResetIn).UTC().Format(time.RFC3339)
	weekReset := at.Add(72 * time.Hour).UTC().Format(time.RFC3339)
	return CommandOutput{Stdout: fmt.Sprintf(
		`[{"provider":"antigravity","rateWindowLabels":{"primary":"Gemini Models","secondary":"Claude and GPT"},"source":"cli","usage":{"updatedAt":%q,"extraRateWindows":[{"id":"antigravity-quota-summary-gemini-5h","title":"Gemini 5-hour","window":{"windowMinutes":300,"resetsAt":%q,"usedPercent":%d}},{"id":"antigravity-quota-summary-gemini-weekly","title":"Gemini weekly","window":{"windowMinutes":10080,"resetsAt":%q,"usedPercent":%d}},{"id":"antigravity-quota-summary-3p-5h","title":"Claude/GPT 5-hour","window":{"windowMinutes":300,"resetsAt":%q,"usedPercent":20}},{"id":"antigravity-quota-summary-3p-weekly","title":"Claude/GPT weekly","window":{"windowMinutes":10080,"resetsAt":%q,"usedPercent":20}}],"primary":{"usedPercent":%d,"windowMinutes":300,"resetsAt":%q},"secondary":{"usedPercent":20,"windowMinutes":10080,"resetsAt":%q},"tertiary":null,"identity":{"providerID":"antigravity"}}}]`,
		at.UTC().Format(time.RFC3339),
		fiveReset, 100-fiveLeft,
		weekReset, 100-weekLeft,
		fiveReset, weekReset,
		100-fiveLeft, fiveReset,
		weekReset,
	)}
}

// Item 4: an agy candidate with a Gemini model (gemini-*) reads the CodexBar
// provider "antigravity" (KB-QTA-4's antigravity-gemini pool): poolProvider
// maps it, the pool gate holds it at or under the floor with the reason naming
// the reset, and an agy candidate with an unpooled model stays unknown (never
// blocks).
func TestQuotaAgyGeminiCandidateUsesAntigravityPoolGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	rawFixture, err := os.ReadFile(filepath.Join("testdata", "codexbar", "antigravity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(rawFixture); strings.Contains(s, "@") && !strings.Contains(s, "account@example.invalid") {
		t.Errorf("fixture testdata/codexbar/antigravity.json holds an unredacted account address")
	}

	for _, model := range []string{"gemini-4-carbon-medium", "gemini-4-neon-medium", "gemini-3.8-flash-low"} {
		if got := poolProvider(flow.Candidate{CLI: "agy", Model: model, Effort: "medium"}); got != "antigravity" {
			t.Errorf("poolProvider(agy/%s) = %q, want %q", model, got, "antigravity")
		}
	}
	for _, model := range []string{"custom-agy-model", ""} {
		if got := poolProvider(flow.Candidate{CLI: "agy", Model: model, Effort: "medium"}); got != "" {
			t.Errorf("poolProvider(agy/%q) = %q, want empty (unknown)", model, got)
		}
	}

	ctx := context.Background()
	paths := testPaths(t)
	eng := newGitTestEngine(t, paths, nil)
	now := time.Date(2026, 11, 22, 19, 47, 32, 0, time.UTC)
	eng.Now = func() time.Time { return now }
	fake := newPoolFake(t)
	attachQuotaPools(eng, fake)
	t.Cleanup(func() {
		if fake.underLock != 0 {
			t.Errorf("CodexBar was called %d times while the engine lock was held", fake.underLock)
		}
	})

	antigravityCalls := func() int {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.calls["antigravity"]
	}

	geminiCand := flow.Candidate{CLI: "agy", Model: "gemini-4-neon-medium", Effort: "medium"}
	nonGeminiCand := flow.Candidate{CLI: "agy", Model: "custom-agy-model", Effort: "medium"}

	settings := flow.DefaultSettings()
	settings.PoolMinutes = 1
	settings.PoolFloorPercent = 5

	// 1. A project with only unpooled agy models makes no antigravity pool read
	// and never blocks.
	settings.Models = map[string][]flow.Candidate{
		flow.RoleCoordinator: {nonGeminiCand},
		flow.RoleWorker:      {nonGeminiCand},
		flow.RoleReviewer:    {{CLI: "agy", Model: "custom-agy-reviewer", Effort: "high"}},
	}
	if err := SaveState(paths.State("p1"), flow.State{Seq: 1, Settings: settings, Project: flow.Project{ID: "p1", Started: true}}); err != nil {
		t.Fatal(err)
	}
	fake.set("antigravity", antigravityPoolOut(now, 0, 2*time.Hour+30*time.Minute, 0))
	pools := eng.refreshPools(ctx, "p1")
	if got := antigravityCalls(); got != 0 {
		t.Errorf("non-Gemini agy project triggered %d antigravity CodexBar reads, want 0", got)
	}
	gate := poolCheck(pools, paths.poolCooldowns(), settings.PoolFloorPercent, now)
	if in, r := flow.ResolveWithReason(flow.RoleWorker, flow.Task{}, settings, flow.ResolveFacts{Pool: gate}); in.CLI != "agy" || in.Model != nonGeminiCand.Model || r.Why != "" {
		t.Errorf("non-Gemini agy resolve = (%+v, %+v), want resolved without block", in, r)
	}

	// 2. Adding a gemini-* agy candidate reads `codexbar usage --provider antigravity --json`
	// and allows the candidate when above the floor (89% 5h, 92% weekly).
	settings.Models[flow.RoleWorker] = []flow.Candidate{geminiCand}
	if err := SaveState(paths.State("p1"), flow.State{Seq: 2, Settings: settings, Project: flow.Project{ID: "p1", Started: true}}); err != nil {
		t.Fatal(err)
	}
	fake.set("antigravity", CommandOutput{Stdout: string(rawFixture)})
	pools = eng.refreshPools(ctx, "p1")
	if got := antigravityCalls(); got != 1 {
		t.Errorf("gemini-* agy project triggered %d antigravity CodexBar reads, want 1", got)
	}
	gate = poolCheck(pools, paths.poolCooldowns(), settings.PoolFloorPercent, now)
	if in, r := flow.ResolveWithReason(flow.RoleWorker, flow.Task{}, settings, flow.ResolveFacts{Pool: gate}); in.CLI != "agy" || in.Model != geminiCand.Model || r.Why != "" {
		t.Errorf("healthy antigravity resolve = (%+v, %+v), want %s allowed", in, r, geminiCand.Model)
	}

	// 3. Equal to the floor (5% left on Gemini 5-hour) and under the floor (2%
	// left) both hold gemini-*, naming quota and the reset, while a non-Gemini
	// agy candidate stays unknown and never blocks.
	for i, left := range []int{5, 2} {
		now = now.Add(2 * time.Minute)
		reset := now.Add(2*time.Hour + 30*time.Minute)
		fake.set("antigravity", antigravityPoolOut(now, left, 2*time.Hour+30*time.Minute, 92))
		pools = eng.refreshPools(ctx, "p1")
		if got, want := antigravityCalls(), i+2; got != want {
			t.Errorf("at %d%% left: antigravity CodexBar reads = %d, want %d", left, got, want)
		}
		gate = poolCheck(pools, paths.poolCooldowns(), settings.PoolFloorPercent, now)

		in, r := flow.ResolveWithReason(flow.RoleWorker, flow.Task{}, settings, flow.ResolveFacts{Pool: gate})
		if in != (flow.Intent{}) || r.Kind != flow.ReasonPool || !strings.Contains(strings.ToLower(r.Why), "quota") || !reasonHasTime(r.Why, reset) {
			t.Errorf("gemini-* at %d%% left (floor %d%%): intent=%+v reason=%+v, want held on quota naming reset %s",
				left, settings.PoolFloorPercent, in, r, reset.Format(time.RFC3339))
		}

		// Non-Gemini agy candidate against the same exhausted pools snapshot never blocks.
		nonGeminiTask := flow.Task{Request: map[string][]flow.Candidate{flow.RoleWorker: {nonGeminiCand}}}
		if in2, r2 := flow.ResolveWithReason(flow.RoleWorker, nonGeminiTask, flow.DefaultSettings(), flow.ResolveFacts{Pool: gate}); in2.CLI != "agy" || in2.Model != nonGeminiCand.Model || r2.Why != "" {
			t.Errorf("non-Gemini agy at %d%% antigravity reading: intent=%+v reason=%+v, want allowed (unknown never blocks)", left, in2, r2)
		}

		// Candidate list [gemini-*, non-Gemini agy] skips the floor-held Gemini
		// candidate and selects the non-Gemini fallback.
		fallbackTask := flow.Task{Request: map[string][]flow.Candidate{flow.RoleWorker: {geminiCand, nonGeminiCand}}}
		if in3, r3 := flow.ResolveWithReason(flow.RoleWorker, fallbackTask, flow.DefaultSettings(), flow.ResolveFacts{Pool: gate}); in3.CLI != "agy" || in3.Model != nonGeminiCand.Model || r3.Why != "" {
			t.Errorf("[gemini, non-Gemini] fallback at %d%% left: intent=%+v reason=%+v, want %s", left, in3, r3, nonGeminiCand.Model)
		}
	}

	// 4. After the reset passes and CodexBar reports healthy again, gemini-* unblocks.
	now = now.Add(2*time.Hour + 31*time.Minute)
	fake.set("antigravity", antigravityPoolOut(now, 89, 2*time.Hour+30*time.Minute, 92))
	pools = eng.refreshPools(ctx, "p1")
	gate = poolCheck(pools, paths.poolCooldowns(), settings.PoolFloorPercent, now)
	if in, r := flow.ResolveWithReason(flow.RoleWorker, flow.Task{}, settings, flow.ResolveFacts{Pool: gate}); in.CLI != "agy" || in.Model != geminiCand.Model || r.Why != "" {
		t.Errorf("after reset with healthy reading: intent=%+v reason=%+v, want %s unblocked", in, r, geminiCand.Model)
	}
}
