package factory

// PR 5b.6, TDD step 1: the pool observer and the launch gate. Every test
// drives the engine through passProject, Report and the fake herdr-core
// server, as the other engine tests do, and states what the owner sees: a
// task at its pool floor stays queued with no worktree, no slot and no
// terminal, with one logged reason that names the reset time; it starts on
// the first pass after the reset with no restart; a pool nobody can read
// never blocks.
//
// Names these tests expect (design.md 7.3, 10.1):
//   - flow.Settings.PoolFloorPercent and PoolMinutes (see flow/pool_test.go);
//   - Engine.poolRun, a CommandFunc the engine uses to run the provider
//     reader; nil means the hc runner (hcExecRunner) on the engine's own
//     hc socket, as the CLI probes already use;
//   - parsePool(provider, CommandOutput) poolReading, with poolReading.Known
//     and poolReading.Windows ([]poolWindow{Minutes, Left, Reset}).
//
// The reader is `codexbar usage --provider <p> --json` (JSON has the
// absolute reset time and the window length; the text form only has
// "Resets in 1h 13m"). Real output is in testdata/codexbar with the account
// replaced by a placeholder.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

var poolT0 = codexRolesT0

// poolWin is one window of a fake reading: percent left and time to reset.
type poolWin struct {
	left    int
	resetIn time.Duration
}

// poolOut builds the JSON `codexbar usage --provider <p> --json` prints.
// five is the provider's 5-hour window (nil: the provider has none, as for
// Codex), week the weekly one, scoped a model-scoped extra weekly window.
func poolOut(provider string, at time.Time, five, week, scoped *poolWin) CommandOutput {
	win := func(w *poolWin, minutes int) string {
		if w == nil {
			return "null"
		}
		return fmt.Sprintf(`{"usedPercent":%d,"windowMinutes":%d,"resetsAt":%q}`, 100-w.left, minutes, at.Add(w.resetIn).UTC().Format(time.RFC3339))
	}
	extra := ""
	if scoped != nil {
		extra = fmt.Sprintf(`,"extraRateWindows":[{"title":"Fable only","id":"claude-weekly-scoped-fable","window":%s}]`, win(scoped, 10080))
	}
	return CommandOutput{Stdout: fmt.Sprintf(`[{"provider":%q,"usage":{"updatedAt":%q,"primary":%s,"secondary":%s,"tertiary":null%s}}]`,
		provider, at.UTC().Format(time.RFC3339), win(five, 300), win(week, 10080), extra)}
}

// poolFake stands in for the provider reader. It records every call.
type poolFake struct {
	t         *testing.T
	mu        sync.Mutex
	out       map[string]CommandOutput
	calls     map[string]int
	argvs     [][]string
	remaining []time.Duration // the time limit each call was given
	noLimit   int             // calls whose context had no deadline
	eng       *Engine         // when set, a call made while eng.mu is held is a violation
	underLock int
}

func newPoolFake(t *testing.T) *poolFake {
	return &poolFake{t: t, out: map[string]CommandOutput{}, calls: map[string]int{}}
}

func (f *poolFake) set(provider string, out CommandOutput) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out[provider] = out
}

func (f *poolFake) claudeReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls["claude"]
}

func (f *poolFake) run(ctx context.Context, _ string, _ []string, argv ...string) CommandOutput {
	f.t.Helper()
	if f.eng != nil {
		if f.eng.mu.TryLock() {
			f.eng.mu.Unlock()
		} else {
			f.mu.Lock()
			f.underLock++
			f.mu.Unlock()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.argvs = append(f.argvs, slices.Clone(argv))
	provider := argValue(argv, "--provider")
	if len(argv) < 2 || argv[0] != "codexbar" || argv[1] != "usage" || provider == "" || !slices.Contains(argv, "--json") {
		f.t.Errorf("pool reader argv = %q, want codexbar usage --provider <p> --json", argv)
		return CommandOutput{Exit: 2}
	}
	if dl, ok := ctx.Deadline(); ok {
		f.remaining = append(f.remaining, time.Until(dl))
	} else {
		f.noLimit++
	}
	f.calls[provider]++
	if out, ok := f.out[provider]; ok {
		return out
	}
	return CommandOutput{Exit: 127, Stderr: "sh: codexbar: not found"}
}

// healthyPools fills the fake with pools far above any floor.
func (f *poolFake) healthy(at time.Time) {
	f.set("claude", poolOut("claude", at, &poolWin{80, 3 * time.Hour}, &poolWin{60, 72 * time.Hour}, nil))
	f.set("codex", poolOut("codex", at, nil, &poolWin{60, 72 * time.Hour}, nil))
}

func poolTask(id, request string) string {
	return `{"id":"` + id + `","estimate_lines":50,"title":"feat: ` + id + `",` + testPlanJSON + request + `}`
}

// poolPlan starts the coordinator (J7, so the pools are healthy at this
// point) and applies a plan of the given tasks through its own token.
func poolPlan(c *codexRoles, tasks ...string) {
	c.t.Helper()
	c.pass() // J7
	if len(*c.runs) != 1 {
		c.t.Fatalf("coordinator not started: %d runs", len(*c.runs))
	}
	tok := (*c.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	if err := c.eng.Report(tok, "plan apply", []byte(`{"tasks":[`+strings.Join(tasks, ",")+`]}`)); err != nil {
		c.t.Fatalf("plan apply: %v", err)
	}
}

// poolNext moves the clock past pool_minutes, so the next pass reads the pools
// again: a reading taken at J7 is shared until then (TestPoolReadingIsShared...).
func poolNext(c *codexRoles) { *c.now = c.now.Add(6 * time.Minute) }

// reasonHasTime reports whether a wait reason names the reset time as the
// owner reads it on the clock ("until 11:30 AM"), in local time or UTC.
func reasonHasTime(why string, reset time.Time) bool {
	return strings.Contains(why, reset.Local().Format("3:04 PM")) || strings.Contains(why, reset.UTC().Format("3:04 PM"))
}

// roleRuns counts the started runs of one role (the coordinator may start
// again on its own pool; these tests count the role they are about).
func roleRuns(runs []hc.RunParams, role string) int {
	n := 0
	for _, p := range runs {
		if p.Labels["sheepdog.role"] == role {
			n++
		}
	}
	return n
}

// failedWake reports a reviewer_failed wake: what a spent-out reviewer would send.
func failedWake(st flow.State) bool {
	return slices.ContainsFunc(st.Project.Wakes, func(w flow.Wake) bool { return strings.HasPrefix(w.Key, "reviewer_failed") })
}

// quotaWaits returns the logged "waiting" lines for task T1 whose reason is
// about quota (the coordinator's own J7 waits are not counted).
func (c *codexRoles) quotaWaits() []LogLine {
	const task = "T1"
	var out []LogLine
	for _, l := range c.logLinesWaiting() {
		if l.Task == task && strings.Contains(strings.ToLower(l.Why), "quota") {
			out = append(out, l)
		}
	}
	return out
}

// noWorktreeNoSlot checks the two things a held task must not have used.
func (c *codexRoles) noWorktreeNoSlot() {
	const task = "T1"
	c.t.Helper()
	st := c.state()
	for _, tk := range st.Tasks {
		if tk.ID == task && len(tk.Attempts) != 0 {
			c.t.Fatalf("task %s has %d attempt(s) although its pool is at the floor (hold %q)", task, len(tk.Attempts), tk.HoldWhy)
		}
	}
	if _, err := os.Stat(c.paths.Worktree(c.id, task, 1)); !os.IsNotExist(err) {
		c.t.Fatalf("a worktree was created for %s although its pool is at the floor (stat err %v)", task, err)
	}
}

// ---- the reading ----

// TestParsePoolReadsRealCodexBarOutput: the saved real output of CodexBar
// 0.84.0 parses into windows with percent left, length and the absolute reset
// time. Claude has a 5-hour and a weekly window plus a model-scoped extra
// weekly window ("Fable only") that never counts as the pool's weekly one
// (KB-QTA-9); Codex has a weekly window only.
func TestParsePoolReadsRealCodexBarOutput(t *testing.T) {
	read := func(name string) CommandOutput {
		raw, err := os.ReadFile(filepath.Join("testdata", "codexbar", name))
		if err != nil {
			t.Fatal(err)
		}
		return CommandOutput{Stdout: string(raw)}
	}
	for _, name := range []string{"claude.json", "codex.json", "claude.txt", "codex.txt"} {
		if raw := read(name).Stdout; strings.Contains(raw, "@") && !strings.Contains(raw, "account@example.invalid") {
			t.Errorf("fixture %s holds an account address", name)
		}
	}
	cl := parsePool("claude", read("claude.json"))
	want := []poolWindow{
		{Minutes: 300, Left: 75, Reset: time.Date(2026, 11, 22, 0, 59, 59, 0, time.UTC)},
		{Minutes: 10080, Left: 11, Reset: time.Date(2026, 11, 23, 5, 0, 0, 0, time.UTC)},
	}
	if !cl.Known || len(cl.Windows) != 2 {
		t.Fatalf("claude = %+v, want 2 known windows (the Fable-only window does not count)", cl)
	}
	for i, w := range want {
		g := cl.Windows[i]
		if g.Minutes != w.Minutes || g.Left != w.Left || !g.Reset.Equal(w.Reset) {
			t.Errorf("claude window %d = %+v, want %+v", i, g, w)
		}
	}
	cx := parsePool("codex", read("codex.json"))
	if !cx.Known || len(cx.Windows) != 1 || cx.Windows[0].Minutes != 10080 || cx.Windows[0].Left != 9 ||
		!cx.Windows[0].Reset.Equal(time.Date(2026, 11, 25, 5, 24, 1, 0, time.UTC)) {
		t.Fatalf("codex = %+v, want one weekly window, 9%% left, reset 2026-11-25T05:24:01Z", cx)
	}
}

// TestParsePoolUnknownIsNeverZero: a missing tool, an error, a timeout,
// garbage and an empty reading are unknown, with no windows; unknown is not
// 0% and not 100% (KB-QTA-1, KB-QTA-9).
func TestParsePoolUnknownIsNeverZero(t *testing.T) {
	for name, out := range map[string]CommandOutput{
		"tool missing":      {Exit: 127, Stderr: "sh: codexbar: not found"},
		"run error":         {Exit: -1, Err: errors.New("hc: connection refused")},
		"timeout":           {Exit: -1, TimedOut: true},
		"garbage":           {Stdout: "not json at all"},
		"non-zero no data":  {Exit: 1, Stderr: "not signed in"},
		"empty list":        {Stdout: `[]`},
		"no windows":        {Stdout: `[{"provider":"claude","usage":{"primary":null,"secondary":null}}]`},
		"no usage":          {Stdout: `[{"provider":"claude"}]`},
		"window w/o number": {Stdout: `[{"provider":"claude","usage":{"secondary":{"windowMinutes":10080,"resetsAt":"2026-11-23T05:00:00Z"}}}]`},
		"other provider":    poolOut("codex", poolT0, nil, &poolWin{50, time.Hour}, nil),
	} {
		got := parsePool("claude", out)
		if got.Known || len(got.Windows) != 0 {
			t.Errorf("%s: %+v, want unknown with no windows", name, got)
		}
	}
}

// ---- the gate ----

// TestPoolFloorHoldsQueuedTaskThenStartsAfterReset is the row's demo
// (plan.md 5b.6): at its floor a queued task keeps no worktree, attempt or
// terminal, logs one reason that names the reset time, and on the first pass
// after the scripted reset (fake clock, new reading) it starts with no
// manual restart.
func TestPoolFloorHoldsQueuedTaskThenStartsAfterReset(t *testing.T) {
	c := newCodexRoles(t, nil, true)
	fake := newPoolFake(t)
	c.eng.poolRun = fake.run
	fake.healthy(*c.now)
	poolPlan(c, poolTask("T1", ""))

	poolNext(c)
	reset := c.now.Add(90 * time.Minute)
	fake.set("claude", poolOut("claude", *c.now, &poolWin{5, 90 * time.Minute}, &poolWin{60, 72 * time.Hour}, nil)) // equal to the floor of 5
	for range 5 {
		c.pass()
		*c.now = c.now.Add(2 * time.Minute) // crosses pool_minutes, so the reading is read again
	}
	if n := roleRuns(*c.runs, flow.RoleWorker); n != 0 {
		t.Fatalf("a worker started at its pool floor: %d worker run(s), last argv[0:3] %q", n, c.last().Argv[:3])
	}
	c.noWorktreeNoSlot()
	st := c.state()
	if why := st.Tasks[0].HoldWhy; !strings.Contains(strings.ToLower(why), "quota") || !strings.Contains(why, "claude") || !reasonHasTime(why, reset) {
		t.Fatalf("hold reason %q, want quota, the pool and the reset time %s", why, reset.Format("3:04 PM"))
	}
	if w := c.quotaWaits(); len(w) != 1 || w[0].Task != "T1" || !reasonHasTime(w[0].Why, reset) {
		t.Fatalf("logged waits %+v, want exactly one for T1 naming the reset time", w)
	}

	// The scripted reset: the clock passes it and the reader now says full.
	*c.now = reset.Add(time.Minute)
	fake.healthy(*c.now)
	c.pass()
	if got := len(c.state().Tasks[0].Attempts); got != 1 {
		t.Fatalf("attempts on the first pass after the reset = %d, want 1 (N11 ran)", got)
	}
	c.pass()
	if n := roleRuns(*c.runs, flow.RoleWorker); n != 1 {
		t.Fatalf("worker not started after the reset: %d worker run(s)", n)
	}
	if why := c.state().Tasks[0].HoldWhy; why != "" {
		t.Errorf("hold reason %q stayed after the task started", why)
	}
	if len(c.quotaWaits()) != 1 {
		t.Errorf("waits logged %d times in all, want 1", len(c.quotaWaits()))
	}
}

// TestPoolWaitSurvivesRestart: the reason and the hold are kept in state, so
// a restart neither starts the task early nor logs the same wait again.
func TestPoolWaitSurvivesRestart(t *testing.T) {
	c := newCodexRoles(t, nil, true)
	fake := newPoolFake(t)
	c.eng.poolRun = fake.run
	fake.healthy(*c.now)
	poolPlan(c, poolTask("T1", ""))
	poolNext(c)
	reset := c.now.Add(90 * time.Minute)
	fake.set("claude", poolOut("claude", *c.now, &poolWin{2, 90 * time.Minute}, &poolWin{60, 72 * time.Hour}, nil))
	c.pass()
	c.pass()
	c.restart()
	c.eng.poolRun = fake.run
	for range 3 {
		*c.now = c.now.Add(time.Minute)
		c.pass()
	}
	c.noWorktreeNoSlot()
	if w := c.quotaWaits(); len(w) != 1 {
		t.Fatalf("waits logged %d times across a restart, want 1: %+v", len(w), w)
	}
	*c.now = reset.Add(time.Minute)
	fake.healthy(*c.now)
	c.pass()
	if len(c.state().Tasks[0].Attempts) != 1 {
		t.Fatal("task did not start on the first pass after the reset on the restarted engine")
	}
}

// TestPoolFloorEdges: a pool is usable only when strictly above the floor,
// so equal blocks; every provider-supported window counts (Codex has the
// weekly one only); the reason carries the earliest reset across the
// blocking windows; a model-scoped extra window never counts.
func TestPoolFloorEdges(t *testing.T) {
	hold := func(d time.Duration) *time.Duration { return &d }
	for _, tc := range []struct {
		name     string
		provider string // the pool the task's worker is on
		floor    int
		five     *poolWin
		week     *poolWin
		scoped   *poolWin
		held     bool
		reasonIn *time.Duration // the reset the reason must name
	}{
		{name: "equal to the floor blocks", provider: "claude", floor: 5, five: &poolWin{5, 90 * time.Minute}, week: &poolWin{60, 72 * time.Hour}, held: true, reasonIn: hold(90 * time.Minute)},
		{name: "one above the floor starts", provider: "claude", floor: 5, five: &poolWin{6, 90 * time.Minute}, week: &poolWin{60, 72 * time.Hour}},
		{name: "floor 0 blocks at 0", provider: "claude", floor: 0, five: &poolWin{0, 90 * time.Minute}, week: &poolWin{60, 72 * time.Hour}, held: true, reasonIn: hold(90 * time.Minute)},
		{name: "floor 0 starts at 1", provider: "claude", floor: 0, five: &poolWin{1, 90 * time.Minute}, week: &poolWin{60, 72 * time.Hour}},
		{name: "weekly window alone blocks", provider: "claude", floor: 5, five: &poolWin{80, 2 * time.Hour}, week: &poolWin{4, 48 * time.Hour}, held: true, reasonIn: hold(48 * time.Hour)},
		{name: "5-hour window alone blocks", provider: "claude", floor: 5, five: &poolWin{3, 90 * time.Minute}, week: &poolWin{70, 48 * time.Hour}, held: true, reasonIn: hold(90 * time.Minute)},
		{name: "both block: the earliest reset is the reason", provider: "claude", floor: 5, five: &poolWin{2, 90 * time.Minute}, week: &poolWin{1, 30 * time.Hour}, held: true, reasonIn: hold(90 * time.Minute)},
		{name: "a scoped extra window does not block", provider: "claude", floor: 5, five: &poolWin{80, 2 * time.Hour}, week: &poolWin{60, 72 * time.Hour}, scoped: &poolWin{0, time.Hour}},
		{name: "codex weekly only blocks", provider: "codex", floor: 5, week: &poolWin{5, 20 * time.Hour}, held: true, reasonIn: hold(20 * time.Hour)},
		{name: "codex weekly only starts", provider: "codex", floor: 5, week: &poolWin{6, 20 * time.Hour}},
		{name: "a higher floor blocks what the default lets through", provider: "claude", floor: 40, five: &poolWin{30, 90 * time.Minute}, week: &poolWin{60, 72 * time.Hour}, held: true, reasonIn: hold(90 * time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var models map[string][]flow.Candidate
			if tc.provider == "codex" {
				models = codexWorkerOnly
			}
			c := newCodexRoles(t, models, true)
			fake := newPoolFake(t)
			c.eng.poolRun = fake.run
			fake.healthy(*c.now)
			poolPlan(c, poolTask("T1", ""))
			c.edit(func(s *flow.State) { s.Settings.PoolFloorPercent = tc.floor })
			poolNext(c)
			fake.set(tc.provider, poolOut(tc.provider, *c.now, tc.five, tc.week, tc.scoped))
			for range 3 {
				c.pass()
			}
			st := c.state()
			if tc.held {
				c.noWorktreeNoSlot()
				if tc.reasonIn != nil && !reasonHasTime(st.Tasks[0].HoldWhy, c.now.Add(*tc.reasonIn)) {
					t.Errorf("reason %q does not name the reset at %s", st.Tasks[0].HoldWhy, c.now.Add(*tc.reasonIn).Format("3:04 PM"))
				}
				return
			}
			if len(st.Tasks[0].Attempts) != 1 || st.Tasks[0].HoldWhy != "" {
				t.Fatalf("task did not start: attempts %d, hold %q", len(st.Tasks[0].Attempts), st.Tasks[0].HoldWhy)
			}
		})
	}
}

// TestPoolHealthyCandidateIsUsedAndNoSlotIsSpent: Resolve takes the first
// candidate whose pool is usable, so a worker list [claude, codex] with
// claude at its floor starts on Codex; and a task held on its pool does not
// keep a max_running slot from a task that can start.
func TestPoolHealthyCandidateIsUsedAndNoSlotIsSpent(t *testing.T) {
	t.Run("next candidate", func(t *testing.T) {
		models := map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "claude", Model: "sonnet", Effort: "medium"}, {CLI: "codex", Model: codexModel, Effort: "high"}}}
		c := newCodexRoles(t, models, true)
		fake := newPoolFake(t)
		c.eng.poolRun = fake.run
		fake.healthy(*c.now)
		poolPlan(c, poolTask("T1", ""))
		poolNext(c)
		fake.set("claude", poolOut("claude", *c.now, &poolWin{3, 90 * time.Minute}, &poolWin{60, 72 * time.Hour}, nil))
		for range 3 {
			c.pass()
		}
		st := c.state()
		if len(*c.runs) != 2 || filepath.Base(c.last().Argv[0]) != "codex" || len(st.Tasks[0].Attempts) != 1 || st.Tasks[0].Attempts[0].Run == nil || st.Tasks[0].Attempts[0].Run.CLI != "codex" {
			t.Fatalf("worker not started on codex: %d runs, last argv[0:3] %q, attempts %d", len(*c.runs), c.last().Argv[:3], len(st.Tasks[0].Attempts))
		}
		if st.Tasks[0].HoldWhy != "" || len(c.quotaWaits()) != 0 {
			t.Errorf("a task that found a candidate was held: %q, waits %+v", st.Tasks[0].HoldWhy, c.quotaWaits())
		}
	})
	t.Run("held task keeps no slot", func(t *testing.T) {
		c := newCodexRoles(t, nil, true) // workers: claude only
		fake := newPoolFake(t)
		c.eng.poolRun = fake.run
		fake.healthy(*c.now)
		poolPlan(c,
			poolTask("T1", ""), // oldest, claude only
			poolTask("T2", `,"request":{"worker":[{"cli":"codex","model":"`+codexModel+`","effort":"high"}]}`))
		c.edit(func(s *flow.State) { s.Settings.MaxRunning = 1 })
		poolNext(c)
		fake.set("claude", poolOut("claude", *c.now, &poolWin{3, 90 * time.Minute}, &poolWin{60, 72 * time.Hour}, nil))
		for range 4 {
			c.pass()
		}
		st := c.state()
		if len(st.Tasks[0].Attempts) != 0 {
			t.Fatalf("T1 started on an exhausted pool: %d attempt(s)", len(st.Tasks[0].Attempts))
		}
		c.noWorktreeNoSlot()
		if len(st.Tasks[1].Attempts) != 1 || st.Tasks[1].Attempts[0].Run == nil || st.Tasks[1].Attempts[0].Run.CLI != "codex" {
			t.Fatalf("T2 (codex, healthy) did not take the only slot while T1 waits on quota: %d attempt(s), hold %q", len(st.Tasks[1].Attempts), st.Tasks[1].HoldWhy)
		}
	})
}

// TestUnknownPoolNeverBlocks: a pool that cannot be read (tool missing, hc or
// tool error, timeout, unparsable, empty) is unknown, not zero and not full,
// and a task on it starts and is never held (KB-QTA-1, design.md 7.3).
func TestUnknownPoolNeverBlocks(t *testing.T) {
	for name, out := range map[string]CommandOutput{
		"tool missing":  {Exit: 127, Stderr: "sh: codexbar: not found"},
		"run error":     {Exit: -1, Err: errors.New("hc: connection refused")},
		"timeout":       {Exit: -1, TimedOut: true},
		"unparsable":    {Stdout: "<html>sign in</html>"},
		"error exit":    {Exit: 1, Stderr: "Error: not signed in"},
		"empty reading": {Stdout: `[]`},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCodexRoles(t, nil, true)
			fake := newPoolFake(t)
			c.eng.poolRun = fake.run
			fake.set("claude", out)
			c.pass() // J7: a coordinator on an unreadable pool starts
			if len(*c.runs) != 1 {
				t.Fatalf("coordinator did not start on an unreadable pool: %d runs", len(*c.runs))
			}
			tok := (*c.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
			if err := c.eng.Report(tok, "plan apply", []byte(`{"tasks":[`+poolTask("T1", "")+`]}`)); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				c.pass()
			}
			st := c.state()
			if len(*c.runs) != 2 || len(st.Tasks[0].Attempts) != 1 || st.Tasks[0].HoldWhy != "" || len(c.quotaWaits()) != 0 {
				t.Fatalf("task on an unreadable pool: %d runs, attempts %d, hold %q, waits %+v", len(*c.runs), len(st.Tasks[0].Attempts), st.Tasks[0].HoldWhy, c.quotaWaits())
			}
			if fake.claudeReads() == 0 {
				t.Fatal("the pool was never read")
			}
		})
	}
	t.Run("refresh fails after the reset", func(t *testing.T) {
		// A known wait ends at its reset even when the fresh reading is
		// unknown: the old reading is not evidence after the reset.
		c := newCodexRoles(t, nil, true)
		fake := newPoolFake(t)
		c.eng.poolRun = fake.run
		fake.healthy(*c.now)
		poolPlan(c, poolTask("T1", ""))
		poolNext(c)
		reset := c.now.Add(90 * time.Minute)
		fake.set("claude", poolOut("claude", *c.now, &poolWin{2, 90 * time.Minute}, &poolWin{60, 72 * time.Hour}, nil))
		c.pass()
		c.noWorktreeNoSlot()
		*c.now = reset.Add(time.Minute)
		fake.set("claude", CommandOutput{Exit: -1, TimedOut: true})
		c.pass()
		if len(c.state().Tasks[0].Attempts) != 1 {
			t.Fatal("task stayed held after its reset because the refresh was unreadable")
		}
	})
}

// TestReviewerPoolExhaustedWaitsAndSpendsNoRetry (KB-QTA-8): when every
// reviewer pool is at its floor the round does not start, no reviewer retry is
// spent, the coordinator is not woken as for a failed reviewer, and the first
// pass after the reset starts the reviewer.
func TestReviewerPoolExhaustedWaitsAndSpendsNoRetry(t *testing.T) {
	_, eng, paths, id, c1, _, runs, logs := codexSetup(t) // round 1 passed by opus; round 2 is codex's
	now := time.Now().Add(10 * time.Minute)               // past any reading codexSetup's passes took
	eng.Now = func() time.Time { return now }
	fake := newPoolFake(t)
	eng.poolRun = fake.run
	fake.healthy(now)
	reset := now.Add(2 * time.Hour)
	exhaust := func() {
		fake.set("claude", poolOut("claude", now, &poolWin{3, 2 * time.Hour}, &poolWin{60, 72 * time.Hour}, nil))
		fake.set("codex", poolOut("codex", now, nil, &poolWin{2, 2 * time.Hour}, nil))
	}
	state := func() flow.State {
		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	pass := func(n int) {
		for range n {
			eng.passProject(context.Background(), id, false)
			now = now.Add(time.Minute)
		}
	}
	before := roleRuns(*runs, flow.RoleReviewer)
	exhaust()
	pass(6) // N26, then N29 asked again and again
	st := state()
	a := &st.Tasks[0].Attempts[0]
	if n := roleRuns(*runs, flow.RoleReviewer); n != before || a.Review != nil {
		t.Fatalf("round 2 reviewer started with every pool at its floor: %d new reviewer run(s), review %+v", n-before, a.Review)
	}
	if failedWake(st) || !strings.Contains(strings.ToLower(st.Tasks[0].HoldWhy), "quota") || !reasonHasTime(st.Tasks[0].HoldWhy, reset) {
		t.Fatalf("wakes %+v, hold %q: want no wake and a quota reason naming %s", st.Project.Wakes, st.Tasks[0].HoldWhy, reset.Format("3:04 PM"))
	}
	n := 0
	for _, l := range logRules(t, paths.Log(id), "N29") {
		if l.Decision == "waiting" && strings.Contains(strings.ToLower(l.Why), "quota") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("N29 logged the quota wait %d times, want once (log %s)", n, logs.String())
	}

	// The reset: the first pass after it starts the reviewer, still round 2.
	now = reset.Add(time.Minute)
	fake.healthy(now)
	pass(1)
	st = state()
	if rev := st.Tasks[0].Attempts[0].Review; roleRuns(*runs, flow.RoleReviewer) != before+1 || rev == nil || rev.Round != 2 || rev.Retries != 0 || rev.Head != c1 {
		t.Fatalf("after the reset: %d new reviewer run(s), review %+v, want round 2 with Retries 0", roleRuns(*runs, flow.RoleReviewer)-before, rev)
	}

	// A reviewer that ended without a report is retried, and that retry is
	// the only one spent: waiting on the pool in between counts for nothing.
	st = state()
	a = &st.Tasks[0].Attempts[0]
	seed := now
	a.Review = &flow.ReviewRun{Round: 2, Head: c1, Code: "code123456", Reviewer: flow.Reviewer{CLI: "codex", Model: codexModel, Effort: "medium"},
		Run: &flow.RunRecord{Role: flow.RoleReviewer, CLI: "codex", State: flow.PhaseEnded, Result: flow.EndFailed, EndedAt: seed}}
	a.Acts = append(a.Acts, flow.Act{Key: "start_reviewer@2@" + c1, Result: "done", At: seed})
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute) // past pool_minutes: the next pass reads again
	exhaust()
	reset = now.Add(2 * time.Hour)
	runsBefore := roleRuns(*runs, flow.RoleReviewer)
	pass(8) // more passes than reviewer_retries (2)
	st = state()
	rev := st.Tasks[0].Attempts[0].Review
	if n := roleRuns(*runs, flow.RoleReviewer); n != runsBefore || rev == nil || rev.Retries != 0 || failedWake(st) {
		t.Fatalf("waiting for the pool spent a retry or woke the coordinator: %d new reviewer run(s), review %+v, wakes %+v", n-runsBefore, rev, st.Project.Wakes)
	}
	now = reset.Add(time.Minute)
	fake.healthy(now)
	pass(1)
	rev = state().Tasks[0].Attempts[0].Review
	if n := roleRuns(*runs, flow.RoleReviewer); n != runsBefore+1 || rev == nil || rev.Retries != 1 || rev.RunID == "" {
		t.Fatalf("after the reset: %d new reviewer run(s), review %+v, want the one retry (Retries 1)", n-runsBefore, rev)
	}
}

// ---- how the reading is taken ----

// TestPoolReadingIsSharedAndRefreshed: one reading per provider serves every
// task, coordinator and reviewer launch; it is read again only when it is
// pool_minutes old, or right after a reset it showed, and not once per task.
func TestPoolReadingIsSharedAndRefreshed(t *testing.T) {
	t.Run("pool_minutes", func(t *testing.T) {
		c := newCodexRoles(t, nil, true)
		fake := newPoolFake(t)
		c.eng.poolRun = fake.run
		fake.healthy(*c.now)
		poolPlan(c, poolTask("T1", ""), poolTask("T2", ""))
		for range 6 {
			c.pass() // coordinator, two workers: three launches, one reading
		}
		if len(*c.runs) < 3 {
			t.Fatalf("workers did not start: %d runs", len(*c.runs))
		}
		if n := fake.claudeReads(); n != 1 {
			t.Fatalf("claude read %d times for three launches in one minute, want 1", n)
		}
		*c.now = c.now.Add(4 * time.Minute) // under pool_minutes (5)
		c.pass()
		c.pass()
		if n := fake.claudeReads(); n != 1 {
			t.Fatalf("claude read %d times inside pool_minutes, want 1", n)
		}
		*c.now = c.now.Add(2 * time.Minute) // 6 minutes in
		for range 3 {
			c.pass()
		}
		if n := fake.claudeReads(); n != 2 {
			t.Fatalf("claude read %d times after pool_minutes passed, want 2 (one refresh shared by every launch)", n)
		}
	})
	t.Run("right after a reset", func(t *testing.T) {
		// The coordinator runs on Codex, so the first Claude reading is the
		// workers' and shows a reset in 10 minutes. pool_minutes is 60:
		// only the reset can make the engine read again before then.
		c := newCodexRoles(t, codexCoordOnly, true)
		fake := newPoolFake(t)
		c.eng.poolRun = fake.run
		fake.healthy(*c.now)
		c.edit(func(s *flow.State) { s.Settings.PoolMinutes = 60 })
		fake.set("claude", poolOut("claude", *c.now, &poolWin{2, 10 * time.Minute}, &poolWin{60, 72 * time.Hour}, nil))
		c.pass() // J7 on codex
		if len(*c.runs) != 1 {
			t.Fatalf("codex coordinator did not start: %d runs", len(*c.runs))
		}
		tok := (*c.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
		if err := c.eng.Report(tok, "plan apply", []byte(`{"tasks":[`+poolTask("T1", "")+`]}`)); err != nil {
			t.Fatal(err)
		}
		c.pass()
		c.pass()
		c.noWorktreeNoSlot()
		if n := fake.claudeReads(); n != 1 {
			t.Fatalf("claude read %d times, want 1", n)
		}
		*c.now = c.now.Add(9 * time.Minute) // before the reset: no new reading
		c.pass()
		if n := fake.claudeReads(); n != 1 {
			t.Fatalf("claude read again %d times before its reset, want 1", n)
		}
		*c.now = c.now.Add(2 * time.Minute) // 11 minutes: past the reset, far inside pool_minutes
		fake.healthy(*c.now)
		c.pass()
		if n := fake.claudeReads(); n != 2 {
			t.Fatalf("claude read %d times; the first pass after a reset must read again, want 2", n)
		}
		if len(c.state().Tasks[0].Attempts) != 1 {
			t.Fatal("task did not start on the first pass after the reset")
		}
	})
}

// TestPoolReadRunsThroughHCWithATimeLimit: with no test hook the reader is
// `codexbar usage --provider <p> --json` run through hc's machine.exec (the
// daemon runs no outside tool itself, KB-ENV-7), with a time limit.
func TestPoolReadRunsThroughHCWithATimeLimit(t *testing.T) {
	c := newCodexRoles(t, nil, true)
	var mu sync.Mutex
	var seen []hc.ExecParams
	c.srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		mu.Lock()
		defer mu.Unlock()
		zero := 0
		if slices.Contains(p.Argv, "codexbar") {
			seen = append(seen, p)
			out := poolOut("claude", *c.now, &poolWin{80, 3 * time.Hour}, &poolWin{60, 72 * time.Hour}, nil)
			return hc.ExecResult{Code: &zero, Stdout: []byte(out.Stdout)}, nil
		}
		return hc.ExecResult{Code: &zero}, nil
	}
	c.pass() // J7 reads the coordinator's pool
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("no codexbar call reached hc machine.exec")
	}
	p := seen[0]
	i := slices.Index(p.Argv, "codexbar")
	tail := p.Argv[i:]
	if len(tail) < 2 || tail[1] != "usage" || argValue(tail, "--provider") != "claude" || !slices.Contains(tail, "--json") {
		t.Errorf("argv = %q, want codexbar usage --provider claude --json", tail)
	}
	if p.Timeout == nil || *p.Timeout <= 0 || *p.Timeout > 60 {
		t.Errorf("exec time limit = %v, want a limit of at most 60 seconds", p.Timeout)
	}
	if len(*c.runs) != 1 {
		t.Errorf("coordinator did not start on a healthy reading: %d runs", len(*c.runs))
	}
}

// TestPoolReadIsBoundedAndHoldsNoEngineLock: the reader's context has a time
// limit, and no engine lock is held while it runs (the pass loop must not
// stall behind a slow provider read; design.md section 2).
func TestPoolReadIsBoundedAndHoldsNoEngineLock(t *testing.T) {
	c := newCodexRoles(t, nil, true)
	fake := newPoolFake(t)
	fake.eng = c.eng
	c.eng.poolRun = fake.run
	fake.healthy(*c.now)
	poolPlan(c, poolTask("T1", ""))
	for range 3 {
		c.pass()
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) == 0 {
		t.Fatal("the pool was never read")
	}
	if fake.underLock != 0 {
		t.Errorf("%d pool reads ran while the engine lock was held", fake.underLock)
	}
	if fake.noLimit != 0 {
		t.Errorf("%d pool reads had no time limit", fake.noLimit)
	}
	for _, d := range fake.remaining {
		if d <= 0 || d > time.Minute {
			t.Errorf("a pool read was given %v, want a limit of at most a minute", d)
		}
	}
}

// reasonHasDay reports whether a wait reason includes both the reset's day
// (for example "Sun", "Sunday", "Nov 22" or "2026-11-22") and its clock time.
func reasonHasDay(why string, reset time.Time) bool {
	if !reasonHasTime(why, reset) {
		return false
	}
	for _, ts := range []time.Time{reset.Local(), reset.UTC()} {
		for _, layout := range []string{"Mon", "Monday", "Jan 2", "2006-01-02"} {
			if strings.Contains(why, ts.Format(layout)) {
				return true
			}
		}
	}
	return false
}

// TestPoolFloorSurvivesFailedRefreshUntilReset (round 2, design 7.3): a known
// reading at or under the floor whose blocking window's reset is still in the
// future stays in force until that reset even when a refresh after pool_minutes
// fails (timeout, hc error or bad JSON). After the reset passes, either a fresh
// good reading or a failed reading (unknown never blocks) lets the task start.
func TestPoolFloorSurvivesFailedRefreshUntilReset(t *testing.T) {
	failures := []struct {
		name string
		out  CommandOutput
	}{
		{name: "timeout", out: CommandOutput{Exit: -1, TimedOut: true}},
		{name: "hc error", out: CommandOutput{Exit: -1, Err: errors.New("hc: connection refused")}},
		{name: "bad JSON", out: CommandOutput{Stdout: "<html>not json</html>"}},
	}
	for _, f := range failures {
		for _, afterReset := range []string{"good reading after reset", "failed reading after reset"} {
			t.Run(f.name+"/"+afterReset, func(t *testing.T) {
				c := newCodexRoles(t, nil, true)
				fake := newPoolFake(t)
				c.eng.poolRun = fake.run
				fake.healthy(*c.now)
				poolPlan(c, poolTask("T1", ""))

				poolNext(c)
				reset := c.now.Add(90 * time.Minute)
				fake.set("claude", poolOut("claude", *c.now, &poolWin{2, 90 * time.Minute}, &poolWin{60, 72 * time.Hour}, nil))
				c.pass()
				c.noWorktreeNoSlot()
				if n := roleRuns(*c.runs, flow.RoleWorker); n != 0 {
					t.Fatalf("worker started at 2%% floor: %d runs", n)
				}

				// Advance past pool_minutes before the reset and fail the refresh.
				*c.now = c.now.Add(6 * time.Minute)
				readsBefore := fake.claudeReads()
				fake.set("claude", f.out)
				c.pass()
				if fake.claudeReads() != readsBefore+1 {
					t.Fatalf("claude reads = %d, want %d (refresh should have run)", fake.claudeReads(), readsBefore+1)
				}
				if n := roleRuns(*c.runs, flow.RoleWorker); n != 0 {
					t.Fatalf("failed refresh before reset started a worker: %d worker run(s)", n)
				}
				c.noWorktreeNoSlot()
				if why := c.state().Tasks[0].HoldWhy; !strings.Contains(strings.ToLower(why), "quota") || !strings.Contains(why, "claude") || !reasonHasTime(why, reset) {
					t.Fatalf("hold reason after failed refresh = %q, want quota, claude and %s", why, reset.Format("3:04 PM"))
				}
				if w := c.quotaWaits(); len(w) != 1 {
					t.Fatalf("quota waits logged %d times before reset, want 1: %+v", len(w), w)
				}

				// A second refresh failure before the reset also keeps the task held.
				*c.now = c.now.Add(6 * time.Minute)
				c.pass()
				c.noWorktreeNoSlot()
				if n := roleRuns(*c.runs, flow.RoleWorker); n != 0 {
					t.Fatalf("second failed refresh before reset started a worker: %d worker run(s)", n)
				}
				if w := c.quotaWaits(); len(w) != 1 {
					t.Fatalf("quota waits logged %d times, want 1: %+v", len(w), w)
				}

				// After the reset passes, both a fresh good reading and a failed
				// (unknown) reading let the task start.
				*c.now = reset.Add(time.Minute)
				if afterReset == "good reading after reset" {
					fake.healthy(*c.now)
				} else {
					fake.set("claude", f.out)
				}
				c.pass()
				c.pass()
				st := c.state()
				if len(st.Tasks[0].Attempts) != 1 || roleRuns(*c.runs, flow.RoleWorker) != 1 || st.Tasks[0].HoldWhy != "" {
					t.Fatalf("after reset (%s): attempts=%d workerRuns=%d hold=%q", afterReset, len(st.Tasks[0].Attempts), roleRuns(*c.runs, flow.RoleWorker), st.Tasks[0].HoldWhy)
				}
				if w := c.quotaWaits(); len(w) != 1 {
					t.Errorf("total quota waits = %d, want 1", len(w))
				}
			})
		}
	}
}

// TestPoolWaitReasonAcrossCandidates (round 2): when every candidate is at its
// floor, the wait reason names the soonest reset across them; and in a mixed
// list where an earlier candidate is not installed or not signed in while a
// later candidate is at its floor, the quota reason (naming quota and the
// reset time) is visible and not hidden (satisfied whether quota wins as the
// binding blocker or both reasons are shown).
func TestPoolWaitReasonAcrossCandidates(t *testing.T) {
	t.Run("worker two candidates at floor names soonest reset", func(t *testing.T) {
		models := map[string][]flow.Candidate{
			flow.RoleWorker: {
				{CLI: "claude", Model: "sonnet", Effort: "medium"},
				{CLI: "codex", Model: codexModel, Effort: "high"},
			},
		}
		c := newCodexRoles(t, models, true)
		fake := newPoolFake(t)
		c.eng.poolRun = fake.run
		fake.healthy(*c.now)
		poolPlan(c, poolTask("T1", ""))

		poolNext(c)
		soonest := c.now.Add(1 * time.Hour)
		later := c.now.Add(3 * time.Hour)
		fake.set("claude", poolOut("claude", *c.now, &poolWin{2, 3 * time.Hour}, &poolWin{60, 72 * time.Hour}, nil))
		fake.set("codex", poolOut("codex", *c.now, nil, &poolWin{2, 1 * time.Hour}, nil))
		c.pass()
		c.pass()

		c.noWorktreeNoSlot()
		if n := roleRuns(*c.runs, flow.RoleWorker); n != 0 {
			t.Fatalf("worker started while both pools at floor: %d runs", n)
		}
		why := c.state().Tasks[0].HoldWhy
		if !strings.Contains(strings.ToLower(why), "quota") || !reasonHasTime(why, soonest) || reasonHasTime(why, later) {
			t.Fatalf("hold reason = %q, want soonest reset %s (not later %s)", why, soonest.Format("3:04 PM"), later.Format("3:04 PM"))
		}
		if w := c.quotaWaits(); len(w) != 1 || !reasonHasTime(w[0].Why, soonest) || reasonHasTime(w[0].Why, later) {
			t.Fatalf("logged waits = %+v, want one naming soonest %s", w, soonest.Format("3:04 PM"))
		}

		// Once codex resets (claude still at floor), T1 starts on codex.
		*c.now = soonest.Add(time.Minute)
		fake.set("codex", poolOut("codex", *c.now, nil, &poolWin{60, 72 * time.Hour}, nil))
		c.pass()
		c.pass()
		st := c.state()
		if len(st.Tasks[0].Attempts) != 1 || st.Tasks[0].Attempts[0].Run == nil || st.Tasks[0].Attempts[0].Run.CLI != "codex" {
			t.Fatalf("after soonest reset: attempts = %+v, want worker started on codex", st.Tasks[0].Attempts)
		}
	})

	t.Run("reviewer two candidates at floor names soonest reset", func(t *testing.T) {
		_, eng, paths, id, _, _, runs, _ := codexSetup(t)
		now := time.Now().Add(10 * time.Minute)
		eng.Now = func() time.Time { return now }
		fake := newPoolFake(t)
		eng.poolRun = fake.run
		soonest := now.Add(1 * time.Hour)
		later := now.Add(3 * time.Hour)
		fake.set("claude", poolOut("claude", now, &poolWin{2, 3 * time.Hour}, &poolWin{60, 72 * time.Hour}, nil))
		fake.set("codex", poolOut("codex", now, nil, &poolWin{2, 1 * time.Hour}, nil))
		before := roleRuns(*runs, flow.RoleReviewer)
		for range 3 {
			eng.passProject(context.Background(), id, false)
			now = now.Add(time.Minute)
		}
		if n := roleRuns(*runs, flow.RoleReviewer); n != before {
			t.Fatalf("reviewer started while both pools at floor: %d new runs", n-before)
		}
		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		why := st.Tasks[0].HoldWhy
		if !strings.Contains(strings.ToLower(why), "quota") || !reasonHasTime(why, soonest) || reasonHasTime(why, later) {
			t.Fatalf("reviewer hold reason = %q, want soonest reset %s (not later %s)", why, soonest.Format("3:04 PM"), later.Format("3:04 PM"))
		}
	})

	for _, tc := range []struct {
		name  string
		codex flow.CLIFacts
	}{
		{name: "mixed list not installed", codex: flow.CLIFacts{Installed: false}},
		{name: "mixed list not signed in", codex: flow.CLIFacts{Installed: true, SignedIn: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			models := map[string][]flow.Candidate{
				flow.RoleWorker: {
					{CLI: "codex", Model: codexModel, Effort: "high"},
					{CLI: "claude", Model: "sonnet", Effort: "medium"},
				},
			}
			c := newCodexRoles(t, models, true)
			c.eng.probeLaunch = func(context.Context, []string) flow.ResolveFacts {
				return flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{
					"codex":  tc.codex,
					"claude": {Installed: true, SignedIn: true},
				}}
			}
			fake := newPoolFake(t)
			c.eng.poolRun = fake.run
			fake.healthy(*c.now)
			poolPlan(c, poolTask("T1", ""))

			poolNext(c)
			reset := c.now.Add(90 * time.Minute)
			fake.set("claude", poolOut("claude", *c.now, &poolWin{2, 90 * time.Minute}, &poolWin{60, 72 * time.Hour}, nil))
			c.pass()
			c.pass()

			c.noWorktreeNoSlot()
			if n := roleRuns(*c.runs, flow.RoleWorker); n != 0 {
				t.Fatalf("worker started: %d runs", n)
			}
			why := c.state().Tasks[0].HoldWhy
			if !strings.Contains(strings.ToLower(why), "quota") || !strings.Contains(why, "claude") || !reasonHasTime(why, reset) {
				t.Fatalf("hold reason = %q, want visible quota reason for claude with reset %s", why, reset.Format("3:04 PM"))
			}
			if w := c.quotaWaits(); len(w) != 1 || !reasonHasTime(w[0].Why, reset) {
				t.Fatalf("logged waits = %+v, want one quota wait naming %s", w, reset.Format("3:04 PM"))
			}

			// When claude resets, T1 starts on claude (confirming quota was binding).
			*c.now = reset.Add(time.Minute)
			fake.healthy(*c.now)
			c.pass()
			c.pass()
			st := c.state()
			if len(st.Tasks[0].Attempts) != 1 || st.Tasks[0].Attempts[0].Run == nil || st.Tasks[0].Attempts[0].Run.CLI != "claude" {
				t.Fatalf("after claude reset: attempts = %+v, want worker started on claude", st.Tasks[0].Attempts)
			}
		})
	}
}

// TestPoolWaitReasonShowsDayWhenMoreThanADayAway (round 2): when the blocking
// reset is more than a day away, the wait reason names the day as well as the
// clock time so voice and logs do not sound like today.
func TestPoolWaitReasonShowsDayWhenMoreThanADayAway(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		models   map[string][]flow.Candidate
		five     *poolWin
		week     *poolWin
	}{
		{
			name:     "codex weekly 50h away",
			provider: "codex",
			models:   codexWorkerOnly,
			week:     &poolWin{2, 50 * time.Hour},
		},
		{
			name:     "claude weekly 50h away",
			provider: "claude",
			five:     &poolWin{80, 2 * time.Hour},
			week:     &poolWin{2, 50 * time.Hour},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCodexRoles(t, tc.models, true)
			fake := newPoolFake(t)
			c.eng.poolRun = fake.run
			fake.healthy(*c.now)
			poolPlan(c, poolTask("T1", ""))

			poolNext(c)
			reset := c.now.Add(50 * time.Hour)
			fake.set(tc.provider, poolOut(tc.provider, *c.now, tc.five, tc.week, nil))
			c.pass()
			c.pass()

			c.noWorktreeNoSlot()
			why := c.state().Tasks[0].HoldWhy
			if !strings.Contains(strings.ToLower(why), "quota") || !reasonHasDay(why, reset) {
				t.Fatalf("hold reason = %q, want quota with day and clock time for %s", why, reset.Format(time.RFC3339))
			}
			if w := c.quotaWaits(); len(w) != 1 || !reasonHasDay(w[0].Why, reset) {
				t.Fatalf("logged waits = %+v, want one with day and clock time for %s", w, reset.Format(time.RFC3339))
			}
		})
	}
}

// TestCoordinatorPoolFloorWaitsAndStartsAfterReset (round 2): a coordinator
// whose only candidate is at its floor stays held (J7 logs one wait with the
// quota reason and launches nothing) and starts on the first pass after reset.
func TestCoordinatorPoolFloorWaitsAndStartsAfterReset(t *testing.T) {
	c := newCodexRoles(t, nil, true) // default coordinator: claude/opus only
	fake := newPoolFake(t)
	c.eng.poolRun = fake.run
	reset := c.now.Add(90 * time.Minute)
	fake.set("claude", poolOut("claude", *c.now, &poolWin{2, 90 * time.Minute}, &poolWin{60, 72 * time.Hour}, nil))

	for range 3 {
		c.pass()
		*c.now = c.now.Add(time.Minute)
	}
	if len(*c.runs) != 0 {
		t.Fatalf("coordinator started while its pool was at the floor: %d run(s)", len(*c.runs))
	}
	st := c.state()
	if st.Project.RunID != "" || flow.LastAct(st.Project.Acts, flow.AskStartCoordinator) != nil {
		t.Fatalf("coordinator state changed while waiting on quota: RunID=%q Acts=%+v", st.Project.RunID, st.Project.Acts)
	}
	var j7Waits []LogLine
	for _, l := range c.logLinesWaiting() {
		if l.Rule == "J7" {
			j7Waits = append(j7Waits, l)
		}
	}
	if len(j7Waits) != 1 {
		t.Fatalf("J7 waiting log lines = %d, want 1: %+v", len(j7Waits), j7Waits)
	}
	if why := j7Waits[0].Why; !strings.Contains(strings.ToLower(why), "quota") || !strings.Contains(why, "claude") || !reasonHasTime(why, reset) {
		t.Fatalf("J7 wait reason = %q, want quota, claude and %s", why, reset.Format("3:04 PM"))
	}

	*c.now = reset.Add(time.Minute)
	fake.healthy(*c.now)
	c.pass()
	if len(*c.runs) != 1 || c.last().Labels["sheepdog.role"] != flow.RoleCoordinator {
		t.Fatalf("coordinator did not start after reset: runs=%d", len(*c.runs))
	}
	if c.state().Project.RunID == "" {
		t.Fatal("Project.RunID is empty after coordinator started")
	}
	j7Waits = nil
	for _, l := range c.logLinesWaiting() {
		if l.Rule == "J7" {
			j7Waits = append(j7Waits, l)
		}
	}
	if len(j7Waits) != 1 {
		t.Errorf("total J7 waiting log lines = %d, want 1", len(j7Waits))
	}
}
