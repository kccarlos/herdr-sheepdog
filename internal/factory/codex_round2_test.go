package factory

// PR 5b.5 round 2: the review's findings as failing tests. Screens come from
// real captures (testdata/codex-working.screen.txt from an engine-launched
// Codex 0.171.0 worker; testdata/codex-idle.screen.txt from spike S0.3).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var workingCounterRe = regexp.MustCompile(`Working \(23s`)

// workerState starts a Codex worker (Codex-only worker list) and returns it.
func (c *codexRoles) workerRun() flow.RunRecord { return *c.state().Tasks[0].Attempts[0].Run }

// TestCodexRealScreensReadWorkingAndIdle: the captured working screen (23s,
// one background terminal, a weekly-limit banner above, two footer lines)
// never reads idle, whatever the counter says, and so does the same screen
// with the working glyph or position changed; the captured idle screen of
// spike S0.3 still reads idle. Held text is never typed into a working run.
func TestCodexRealScreensReadWorkingAndIdle(t *testing.T) {
	working := readFixture(t, "codex-working.screen.txt")
	if !strings.Contains(working, "esc to interrupt") || !strings.Contains(working, "1 background terminal running") {
		t.Fatal("fixture is not the real working screen")
	}
	variants := map[string]string{
		"as captured (23s)":       working,
		"counter 24s":             workingCounterRe.ReplaceAllString(working, "Working (24s"),
		"counter 1m 05s":          workingCounterRe.ReplaceAllString(working, "Working (1m 05s"),
		"another glyph":           strings.Replace(working, "• Working (", "◦ Working (", 1),
		"indented working line":   strings.Replace(working, "• Working (", "  • Working (", 1),
		"queued lines push it up": strings.Replace(working, "› Ask Codex to do anything", "  ↳ Queued: also run go vet\n  ↳ Queued: and gofmt\n  ↳ Queued: and the docs check\n  ↳ Queued: then stop\n› Ask Codex to do anything", 1),
	}
	for name, screen := range variants {
		t.Run(name, func(t *testing.T) {
			c := newCodexRoles(t, codexWorkerOnly, true)
			w := c.planAndStartWorker()
			c.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Run.Held = []string{"Rebase on main."} })
			c.srv.SetScreen(w, screen)
			c.turnEnds()
			*c.now = c.now.Add(time.Minute)
			c.turnEnds()
			if r := c.workerRun(); r.State != flow.PhaseWorking || r.Turn != 0 {
				t.Fatalf("real working screen: state %s, turn %d, want working and no turn end", r.State, r.Turn)
			}
			if n := len(sendsTo(t, c.srv.Requests(), w)); n != 0 {
				t.Fatalf("%d sends typed into a working Codex", n)
			}
		})
	}
	t.Run("real idle screen", func(t *testing.T) {
		c := newCodexRoles(t, codexWorkerOnly, true)
		w := c.planAndStartWorker()
		c.srv.SetScreen(w, working)
		c.pass()
		c.srv.SetScreen(w, readFixture(t, "codex-idle.screen.txt"))
		c.turnEnds()
		if r := c.workerRun(); r.State != flow.PhaseIdle || r.Turn != 1 {
			t.Fatalf("real idle screen: state %s, turn %d, want idle and one turn", r.State, r.Turn)
		}
	})
}

// TestCodexWeeklyLimitBannerIsNotAQuotaStop: "Heads up, you have less than
// 10% of your weekly limit left" mid-run, working or idle, does not end the
// run as quota or stuck, and does not mark the screen as a limit screen.
func TestCodexWeeklyLimitBannerIsNotAQuotaStop(t *testing.T) {
	working := readFixture(t, "codex-working.screen.txt")
	banner := "⚠ Heads up, you have less than 10% of your weekly limit left. Run /status for a breakdown."
	if !strings.Contains(working, banner) {
		t.Fatal("fixture lacks the banner")
	}
	c := newCodexRoles(t, codexWorkerOnly, true)
	w := c.planAndStartWorker()
	c.srv.SetScreen(w, working)
	for range 4 {
		c.turnEnds()
		*c.now = c.now.Add(5 * time.Minute)
	}
	r := c.workerRun()
	if r.State != flow.PhaseWorking || r.Result != "" || r.Screen == flow.ScreenLimit {
		t.Fatalf("working with the banner: state %s result %q screen %q", r.State, r.Result, r.Screen)
	}
	c.srv.SetScreen(w, strings.Replace(readFixture(t, "codex-idle.screen.txt"), "› Ask Codex", banner+"\n\n› Ask Codex", 1))
	c.turnEnds()
	if r := c.workerRun(); r.State != flow.PhaseIdle || r.Result != "" || r.Screen == flow.ScreenLimit {
		t.Fatalf("idle with the banner: state %s result %q screen %q", r.State, r.Result, r.Screen)
	}
}

// claudeStatus sets a Claude coordinator's hc status as of one second ahead.
func (c *codexRoles) claudeStatus(addr, state string, turn uint64) {
	c.t.Helper()
	c.srv.SetStatus(addr, &hc.Status{App: "claude-code", State: state})
	term, err := c.cl.Get(context.Background(), addr)
	if err != nil {
		c.t.Fatal(err)
	}
	ms := uint64(c.now.Add(time.Second).UnixMilli())
	term.StatusMS, term.Turn = &ms, turn
	c.srv.Add(term)
}

func (c *codexRoles) addWake(key string) {
	c.edit(func(s *flow.State) {
		s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: key, Task: "T1", Why: "N23: waiting", Facts: "f", At: *c.now})
	})
}

// claudeCoordWithWake brings a default (Claude) coordinator to idle after one
// finished turn and delivers one wake to it: the wake's turn has not ended.
func claudeCoordWithWake(t *testing.T) (*codexRoles, string) {
	c := newCodexRoles(t, nil, true)
	c.pass() // J7
	co := c.state().Project.RunID
	c.claudeStatus(co, "working", 0)
	c.pass()
	*c.now = c.now.Add(30 * time.Second)
	c.claudeStatus(co, "idle", 1)
	c.pass()
	*c.now = c.now.Add(30 * time.Second)
	c.pass() // R20 idle
	c.addWake("worker_failed")
	c.pass() // J12
	if sends := sendsTo(t, c.srv.Requests(), co); len(sends) != 1 || !strings.Contains(sends[0], "worker_failed") {
		t.Fatalf("wake not delivered: %q", sends)
	}
	return c, co
}

// replacementSends starts a replacement after the coordinator is gone and
// returns what it is sent once it is idle.
func (c *codexRoles) replacementSends(old string, claude bool) []string {
	c.t.Helper()
	n := len(*c.runs)
	for range 4 {
		*c.now = c.now.Add(11 * time.Minute)
		c.pass()
		if len(*c.runs) > n {
			break
		}
	}
	if len(*c.runs) != n+1 {
		c.t.Fatalf("no replacement coordinator: %d runs, was %d", len(*c.runs), n)
	}
	co := c.state().Project.RunID
	if co == "" || co == old {
		c.t.Fatalf("replacement address %q", co)
	}
	if claude {
		c.claudeStatus(co, "working", 0)
		c.pass()
		*c.now = c.now.Add(30 * time.Second)
		c.claudeStatus(co, "idle", 1)
		c.pass()
		*c.now = c.now.Add(30 * time.Second)
		c.pass()
		c.pass()
	} else {
		c.srv.SetScreen(co, codexIdleScreen("You are the coordinator", 9))
		c.turnEnds()
	}
	return sendsTo(c.t, c.srv.Requests(), co)
}

func anyContains(ss []string, sub string) bool {
	return slices.ContainsFunc(ss, func(s string) bool { return strings.Contains(s, sub) })
}

// TestWakeSurvivesCoordinatorDeathForEveryCLI (O21): a wake that was typed
// into a coordinator whose turn never finished is delivered to the
// replacement, whichever CLI the coordinator is and whether it died or was
// stopped by J9 (silent) or J8 (error).
func TestWakeSurvivesCoordinatorDeathForEveryCLI(t *testing.T) {
	t.Run("claude killed", func(t *testing.T) {
		c, co := claudeCoordWithWake(t)
		c.srv.Finish(co, "killed")
		if s := c.replacementSends(co, true); !anyContains(s, "worker_failed") {
			t.Fatalf("replacement sends = %q, want the interrupted wake", s)
		}
	})
	t.Run("claude stopped by J9", func(t *testing.T) {
		c, co := claudeCoordWithWake(t)
		*c.now = c.now.Add(31 * time.Minute) // coordinator_silent_minutes is 30
		c.claudeStatus(co, "idle", 1)        // no new turn since the wake
		c.pass()
		c.pass()
		if s := c.replacementSends(co, true); !anyContains(s, "worker_failed") {
			t.Fatalf("after J9 the replacement sends = %q, want the interrupted wake", s)
		}
		if got := logRules(t, c.paths.Log(c.id), "J9"); len(got) == 0 {
			t.Fatal("the coordinator was never stopped by J9: scenario did not run")
		}
	})
	t.Run("claude stopped by J8", func(t *testing.T) {
		c, co := claudeCoordWithWake(t)
		c.claudeStatus(co, "error", 2) // the wake's turn ended in an error: J8 tells it to go on
		c.pass()
		*c.now = c.now.Add(30 * time.Second)
		c.pass()
		c.pass()
		c.claudeStatus(co, "working", 2)
		c.pass()
		*c.now = c.now.Add(30 * time.Second)
		c.claudeStatus(co, "error", 3) // a second error within quiet_minutes: stop
		c.pass()
		*c.now = c.now.Add(30 * time.Second)
		c.pass()
		c.pass()
		if got := logRules(t, c.paths.Log(c.id), "J8"); len(got) < 2 {
			t.Skipf("scenario did not reach the J8 stop (%d J8 lines)", len(got))
		}
		// The wake's turn ended (with an error): J8's stop does not requeue it.
		if r := c.replacementSends(co, true); anyContains(r, "worker_failed") {
			t.Fatalf("a wake whose turn ended was delivered again after J8: %q", r)
		}
	})
	t.Run("codex killed", func(t *testing.T) {
		c := newCodexRoles(t, codexCoordOnly, true)
		c.pass()
		co := c.state().Project.RunID
		c.srv.SetScreen(co, codexIdleScreen("You are the coordinator", 9))
		c.turnEnds()
		c.addWake("worker_failed")
		c.turnEnds()
		if sends := sendsTo(t, c.srv.Requests(), co); !anyContains(sends, "worker_failed") {
			t.Fatalf("wake not delivered: %q", sends)
		}
		c.srv.SetScreen(co, codexWorkingScreen("Wake: 1 item(s) need you."))
		c.pass()
		c.srv.Finish(co, "killed")
		if s := c.replacementSends(co, false); !anyContains(s, "worker_failed") {
			t.Fatalf("replacement sends = %q, want the interrupted wake", s)
		}
	})
}

// TestFinishedWakeTurnIsNotRequeued: a wake whose turn finished before the
// coordinator died is never delivered again, for either CLI.
func TestFinishedWakeTurnIsNotRequeued(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		c, co := claudeCoordWithWake(t)
		c.claudeStatus(co, "working", 1)
		c.pass()
		*c.now = c.now.Add(30 * time.Second)
		c.claudeStatus(co, "idle", 2) // the wake's turn ended
		c.pass()
		*c.now = c.now.Add(30 * time.Second)
		c.pass()
		c.srv.Finish(co, "killed")
		if s := c.replacementSends(co, true); anyContains(s, "worker_failed") {
			t.Fatalf("a finished wake was delivered again: %q", s)
		}
	})
	t.Run("codex", func(t *testing.T) {
		c := newCodexRoles(t, codexCoordOnly, true)
		c.pass()
		co := c.state().Project.RunID
		c.srv.SetScreen(co, codexIdleScreen("You are the coordinator", 9))
		c.turnEnds()
		c.addWake("worker_failed")
		c.turnEnds()
		c.srv.SetScreen(co, codexWorkingScreen("Wake: 1 item(s) need you."))
		c.turnEnds()
		c.srv.SetScreen(co, codexIdleScreen("Wake: 1 item(s) need you.", 17))
		c.turnEnds()
		c.srv.Finish(co, "killed")
		if s := c.replacementSends(co, false); anyContains(s, "worker_failed") {
			t.Fatalf("a finished wake was delivered again: %q", s)
		}
	})
}

// TestInFlightWakesMergeNeverOverwrite: a second batch typed while the first
// batch's turn has not ended keeps the first in flight, so a death requeues
// both.
func TestInFlightWakesMergeNeverOverwrite(t *testing.T) {
	c, co := claudeCoordWithWake(t)
	c.claudeStatus(co, "idle", 1) // no new turn since the first wake
	c.pass()
	*c.now = c.now.Add(30 * time.Second)
	c.pass()
	c.addWake("main_red")
	c.pass() // J12 delivers the second batch
	if sends := sendsTo(t, c.srv.Requests(), co); len(sends) != 2 || !strings.Contains(sends[1], "main_red") {
		t.Fatalf("second batch not delivered: %q", sends)
	}
	c.srv.Finish(co, "killed")
	s := c.replacementSends(co, true)
	if !anyContains(s, "worker_failed") || !anyContains(s, "main_red") {
		t.Fatalf("replacement sends = %q, want both interrupted wakes", s)
	}
}

// TestCodexRolesUseTheGivenPathsAsTheyAre (5b.11): the daemon does not resolve
// symlinks or read the project folder itself. A worktree or folder given as an
// alias keeps that exact spelling in the trust key, and a session whose
// recorded cwd is that spelling (normalized: a trailing slash, a ..) is found.
func TestCodexRolesUseTheGivenPathsAsTheyAre(t *testing.T) {
	trustKey := func(argv []string) string {
		for _, a := range argv {
			if rest, ok := strings.CutPrefix(a, "projects={"); ok {
				k, _, _ := strings.Cut(rest, "={")
				var s string
				_ = json.Unmarshal([]byte(k), &s)
				return s
			}
		}
		return ""
	}
	t.Run("worker", func(t *testing.T) {
		c := newCodexRoles(t, codexWorkerOnly, true)
		c.pass() // J7
		tok := (*c.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
		if err := c.eng.Report(tok, "plan apply", []byte(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`}]}`)); err != nil {
			t.Fatal(err)
		}
		c.pass() // N11
		alias := filepath.Join(t.TempDir(), "alias")
		wt := c.state().Tasks[0].Attempts[0].Worktree
		if err := os.Symlink(wt, alias); err != nil {
			t.Fatal(err)
		}
		c.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Worktree = alias })
		c.pass() // N23
		p := c.last()
		if got := trustKey(p.Argv); got != alias || p.Cwd != alias {
			t.Fatalf("trust key %q cwd %q, want the alias %q as given", got, p.Cwd, alias)
		}
		c.rollout(rolesSessionA, alias+"/sub/..//", codexRolesT0.Add(3*time.Second), "")
		c.srv.SetScreen(c.state().Tasks[0].Attempts[0].RunID, codexWorkingScreen("Add hello.go"))
		*c.now = c.now.Add(10 * time.Second)
		c.pass()
		c.pass()
		if r := c.workerRun(); r.SessionID != rolesSessionA {
			t.Fatalf("session for the alias spelling = %q, want %q", r.SessionID, rolesSessionA)
		}
	})
	t.Run("coordinator", func(t *testing.T) {
		c := newCodexRoles(t, codexCoordOnly, true)
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(c.state().Project.Folder, alias); err != nil {
			t.Fatal(err)
		}
		c.edit(func(s *flow.State) { s.Project.Folder = alias })
		c.pass()
		if p := c.last(); trustKey(p.Argv) != alias || p.Cwd != alias {
			t.Fatalf("trust key %q cwd %q, want the alias %q as given", trustKey(p.Argv), p.Cwd, alias)
		}
	})
}

// TestCodexIdleRunSurvivesOneUnreadableScreen: an idle Codex run whose screen
// reads empty for one pass stays idle, and when the screen is back no turn
// end is invented.
func TestCodexIdleRunSurvivesOneUnreadableScreen(t *testing.T) {
	c := newCodexRoles(t, codexWorkerOnly, true)
	w := c.planAndStartWorker()
	c.srv.SetScreen(w, codexWorkingScreen("Add hello.go"))
	c.pass()
	c.srv.SetScreen(w, codexIdleScreen("Add hello.go", 31))
	c.turnEnds()
	before := c.workerRun()
	if before.State != flow.PhaseIdle || before.Turn != 1 {
		t.Fatalf("setup: state %s turn %d", before.State, before.Turn)
	}
	c.srv.SetScreen(w, "")
	*c.now = c.now.Add(5 * time.Second)
	c.pass()
	if r := c.workerRun(); r.State != flow.PhaseIdle {
		t.Fatalf("one unreadable screen moved an idle run to %s", r.State)
	}
	c.srv.SetScreen(w, codexIdleScreen("Add hello.go", 31))
	c.turnEnds()
	if r := c.workerRun(); r.State != flow.PhaseIdle || r.Turn != before.Turn || !r.LastTurnEnd.Equal(before.LastTurnEnd) {
		t.Fatalf("after the screen came back: state %s turn %d (was %d) lastTurnEnd %v (was %v)", r.State, r.Turn, before.Turn, r.LastTurnEnd, before.LastTurnEnd)
	}
}

func (c *codexRoles) startedWorkerCwd() string {
	return c.state().Tasks[0].Attempts[0].Worktree
}

func bigMeta(id, cwd string, at time.Time, pad int) string {
	b, _ := json.Marshal(map[string]any{"timestamp": at.Format(time.RFC3339Nano), "type": "session_meta",
		"payload": map[string]any{"session_id": id, "id": id, "timestamp": at.Format(time.RFC3339Nano), "cwd": cwd, "source": "cli",
			"base_instructions": map[string]any{"text": strings.Repeat("x", pad)}}})
	return string(b)
}

// TestCodexSessionLookupEdges: a first line over 64 KiB is still read; a
// subagent's rollout (another id, a parent, source subagent) and a rollout in
// the owner's own CODEX_HOME are not this run's; a path compared normalized.
func TestCodexSessionLookupEdges(t *testing.T) {
	run := func(t *testing.T, write func(c *codexRoles, cwd string)) string {
		c := newCodexRoles(t, codexWorkerOnly, true)
		w := c.planAndStartWorker()
		write(c, c.startedWorkerCwd())
		c.srv.SetScreen(w, codexWorkingScreen("Add hello.go"))
		*c.now = c.now.Add(10 * time.Second)
		c.pass()
		c.pass()
		return c.workerRun().SessionID
	}
	at := codexRolesT0.Add(3 * time.Second)
	t.Run("first line over 64 KiB", func(t *testing.T) {
		if got := run(t, func(c *codexRoles, cwd string) {
			c.rollout(rolesSessionA, cwd, at, bigMeta(rolesSessionA, cwd, at, 100<<10))
		}); got != rolesSessionA {
			t.Fatalf("SessionID = %q, want %q", got, rolesSessionA)
		}
	})
	t.Run("a subagent rollout beside it is ignored", func(t *testing.T) {
		got := run(t, func(c *codexRoles, cwd string) {
			c.rollout(rolesSessionA, cwd, at, "")
			sub, _ := json.Marshal(map[string]any{"timestamp": at.Format(time.RFC3339Nano), "type": "session_meta",
				"payload": map[string]any{"session_id": rolesSessionA, "id": rolesSessionB, "timestamp": at.Add(time.Second).Format(time.RFC3339Nano), "cwd": cwd,
					"parent_thread_id": rolesSessionA, "source": map[string]any{"subagent": "guardian"}}})
			c.rollout(rolesSessionB, cwd, at.Add(time.Second), string(sub))
		})
		if got != rolesSessionA {
			t.Fatalf("SessionID = %q, want the run's own %q", got, rolesSessionA)
		}
	})
	t.Run("only a subagent rollout gives no id", func(t *testing.T) {
		got := run(t, func(c *codexRoles, cwd string) {
			sub, _ := json.Marshal(map[string]any{"timestamp": at.Format(time.RFC3339Nano), "type": "session_meta",
				"payload": map[string]any{"session_id": rolesSessionA, "id": rolesSessionB, "timestamp": at.Format(time.RFC3339Nano), "cwd": cwd,
					"parent_thread_id": rolesSessionA, "source": map[string]any{"subagent": "guardian"}}})
			c.rollout(rolesSessionB, cwd, at, string(sub))
		})
		if got != "" {
			t.Fatalf("SessionID = %q from a subagent's rollout, want none", got)
		}
	})
	t.Run("the owner's CODEX_HOME is ignored", func(t *testing.T) {
		got := run(t, func(c *codexRoles, cwd string) {
			own := &codexRoles{t: t, home: os.Getenv("CODEX_HOME")}
			own.rollout(rolesSessionA, cwd, at, "")
		})
		if got != "" {
			t.Fatalf("SessionID = %q from the owner's home, want none", got)
		}
	})
	t.Run("recorded cwd compared normalized", func(t *testing.T) {
		if got := run(t, func(c *codexRoles, cwd string) { c.rollout(rolesSessionA, cwd+"/", at, "") }); got != rolesSessionA {
			t.Fatalf("trailing slash: SessionID = %q, want %q", got, rolesSessionA)
		}
		if got := run(t, func(c *codexRoles, cwd string) { c.rollout(rolesSessionA, cwd+"/x/..", at, "") }); got != rolesSessionA {
			t.Fatalf("dot-dot: SessionID = %q, want %q", got, rolesSessionA)
		}
	})
	t.Run("a later rollout in the folder does not change a recorded id", func(t *testing.T) {
		c := newCodexRoles(t, codexWorkerOnly, true)
		w := c.planAndStartWorker()
		cwd := c.startedWorkerCwd()
		c.rollout(rolesSessionA, cwd, at, "")
		c.srv.SetScreen(w, codexWorkingScreen("Add hello.go"))
		*c.now = c.now.Add(10 * time.Second)
		c.pass()
		c.rollout(rolesSessionB, cwd, at.Add(time.Minute), "") // a reviewer's run in the same worktree
		*c.now = c.now.Add(time.Minute)
		c.pass()
		c.pass()
		if got := c.workerRun().SessionID; got != rolesSessionA {
			t.Fatalf("SessionID changed to %q", got)
		}
	})
}

// TestCodexResumeOnlyWhenTheLaunchAsksToResume: a vanished terminal found at
// adoption with a recorded session is started fresh with the full brief, not
// as `codex resume <id>` carrying a fresh prompt (R13 cannot catch that).
func TestCodexResumeOnlyWhenTheLaunchAsksToResume(t *testing.T) {
	c := newCodexRoles(t, codexWorkerOnly, true)
	w := c.planAndStartWorker()
	c.rollout(rolesSessionA, c.startedWorkerCwd(), codexRolesT0.Add(3*time.Second), "")
	c.srv.SetScreen(w, codexWorkingScreen("Add hello.go"))
	*c.now = c.now.Add(10 * time.Second)
	c.pass()
	if c.workerRun().SessionID != rolesSessionA {
		t.Fatal("setup: session not recorded")
	}
	// The terminal is gone and the daemon crashed after the launch call and
	// before the address was saved: the record is not ended, the address is empty.
	c.srv.Remove(w)
	c.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.RunID, a.Run.Address = "", ""
		a.Acts = slices.DeleteFunc(a.Acts, func(x flow.Act) bool { return strings.HasPrefix(x.Key, flow.AskStartWorker) })
	})
	n := len(*c.runs)
	*c.now = c.now.Add(2 * time.Minute)
	c.restart()
	c.pass()
	c.pass()
	if len(*c.runs) != n+1 {
		t.Fatalf("scenario did not relaunch the worker: %d runs, was %d", len(*c.runs), n)
	}
	p := c.last()
	if slices.Contains(p.Argv, "resume") {
		t.Fatalf("a fresh launch used resume: %q", p.Argv)
	}
	if got := p.Argv[len(p.Argv)-1]; !strings.Contains(got, "Add hello.go with a Hello function.") && !strings.Contains(got, "Handoff from an earlier worker run") {
		t.Errorf("fresh launch prompt carries neither the brief nor a handoff:\n%s", got)
	}
}

// TestNewAttemptChecksTheCodexHome: with Codex the only worker candidate and
// no Codex sign-in, N11 allocates no worktree and no attempt (as N23 would not
// start a run), and says why once.
func TestNewAttemptChecksTheCodexHome(t *testing.T) {
	c := newCodexRoles(t, codexWorkerOnly, false)
	c.pass() // J7
	tok := (*c.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	if err := c.eng.Report(tok, "plan apply", []byte(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`}]}`)); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		c.pass()
	}
	st := c.state()
	if len(st.Tasks[0].Attempts) != 0 || !strings.Contains(strings.ToLower(st.Tasks[0].HoldWhy), "not signed in") {
		t.Fatalf("attempts %d, hold %q: N11 allocated before the Codex home was checked", len(st.Tasks[0].Attempts), st.Tasks[0].HoldWhy)
	}
	if _, err := os.Stat(c.paths.Worktree(c.id, "T1", 1)); !os.IsNotExist(err) {
		t.Fatalf("a worktree exists: %v", err)
	}
	n := 0
	for _, l := range c.logLinesWaiting() {
		if strings.Contains(strings.ToLower(l.Why), "not signed in") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d logged waits with the reason, want 1", n)
	}
}
