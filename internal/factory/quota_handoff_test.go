package factory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

const quotaHandoffRequest = "write your handoff and stop"

var quotaWorkerCandidates = map[string][]flow.Candidate{
	flow.RoleWorker: {
		{CLI: "claude", Model: "sonnet", Effort: "medium"},
		{CLI: "codex", Model: codexModel, Effort: "medium"},
	},
}

type quotaHistory struct {
	*codexRoles
	pools *poolFake
}

func isolateQuotaFixtureGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
}

func newQuotaHistory(t *testing.T, models map[string][]flow.Candidate) *quotaHistory {
	t.Helper()
	isolateQuotaFixtureGit(t)
	q := &quotaHistory{codexRoles: newCodexRoles(t, models, true), pools: newPoolFake(t)}
	q.edit(func(s *flow.State) { s.Settings.PoolMinutes = 1 })
	q.pools.healthy(*q.now)
	q.attachPools()
	t.Cleanup(func() {
		if q.pools.underLock != 0 {
			t.Errorf("CodexBar was called %d times while the engine lock was held", q.pools.underLock)
		}
	})
	return q
}

func (q *quotaHistory) attachPools() {
	attachQuotaPools(q.eng, q.pools)
}

func attachQuotaPools(eng *Engine, pools *poolFake) {
	pools.eng = eng
	var read sync.Mutex
	eng.poolRun = func(ctx context.Context, cwd string, env []string, argv ...string) CommandOutput {
		// Run the fake's lock checks serially so probes for two providers
		// cannot observe each other's TryLock as an engine lock violation.
		read.Lock()
		defer read.Unlock()
		return pools.run(ctx, cwd, env, argv...)
	}
}

func (q *quotaHistory) restart() {
	q.codexRoles.restart()
	q.attachPools()
	q.eng.passProject(context.Background(), q.id, true)
}

func (q *quotaHistory) attempt(index int) flow.Attempt {
	q.t.Helper()
	task := q.state().Tasks[index]
	if len(task.Attempts) == 0 {
		q.t.Fatalf("task %s has no attempt", task.ID)
	}
	return task.Attempts[len(task.Attempts)-1]
}

func (q *quotaHistory) startWorkers(tasks ...string) {
	q.t.Helper()
	poolPlan(q.codexRoles, tasks...)
	for range 3 {
		q.pass()
	}
	for i := range tasks {
		if a := q.attempt(i); a.Run == nil || a.RunID == "" {
			q.t.Fatalf("task %d worker did not launch", i+1)
		}
	}
}

func (q *quotaHistory) status(address, state string) {
	p := &hc.Status{App: "claude-code", State: state}
	if state == "blocked" {
		kind := "permission"
		p.Kind, p.Msg = &kind, "Allow this action?"
	}
	q.srv.SetStatus(address, p)
}

func (q *quotaHistory) atFloor(provider string, reset time.Time) {
	blocking := &poolWin{5, reset.Sub(*q.now)}
	if provider == "claude" {
		q.pools.set(provider, poolOut(provider, *q.now, blocking, &poolWin{60, 168 * time.Hour}, nil))
	} else {
		q.pools.set(provider, poolOut(provider, *q.now, nil, blocking, nil))
	}
}

func (q *quotaHistory) handoffs(address string) int {
	n := 0
	for _, s := range sendsTo(q.t, q.srv.Requests(), address) {
		if strings.Contains(strings.ToLower(s), quotaHandoffRequest) {
			n++
		}
	}
	return n
}

func (q *quotaHistory) assertLive(address string) {
	q.t.Helper()
	term, err := q.cl.Get(context.Background(), address)
	if err != nil || term.State != "live" {
		q.t.Errorf("worker %q state = %q, error = %v, want live", address, term.State, err)
	}
}

func (q *quotaHistory) stopped(address string) bool {
	q.t.Helper()
	term, err := q.cl.Get(context.Background(), address)
	if err != nil && !errors.Is(err, hc.ErrNotFound) {
		q.t.Fatalf("observing stopped terminal: %v", err)
	}
	return errors.Is(err, hc.ErrNotFound) || term.State == "finished"
}

func quotaTerminalCalls(reqs []hctest.Request, address string) []string {
	var methods []string
	for _, req := range reqs {
		var p struct{ Terminal string }
		if json.Unmarshal(req.Params, &p) == nil && p.Terminal == address {
			methods = append(methods, req.Method)
		}
	}
	return methods
}

func TestQuotaFloorHandoffOnceAcrossRestarts(t *testing.T) {
	const codex = `,"request":{"worker":[{"cli":"codex","model":"` + codexModel + `","effort":"medium"}]}`
	for _, name := range []string{"uninterrupted", "restart before observing floor", "restart after sending", "restart before recheck"} {
		t.Run(name, func(t *testing.T) {
			q := newQuotaHistory(t, nil)
			q.startWorkers(poolTask("T1", ""), poolTask("T2", codex))
			affected, healthy := q.attempt(0).RunID, q.attempt(1).RunID
			q.status(affected, "working")
			q.srv.SetScreen(healthy, codexWorkingScreen("Healthy worker"))
			q.pass()
			if q.attempt(0).Run.State != flow.PhaseWorking || q.attempt(1).Run.State != flow.PhaseWorking {
				t.Fatal("fixture workers must both be working")
			}
			if name == "restart before observing floor" {
				q.restart()
			}
			*q.now = q.now.Add(time.Minute)
			q.atFloor("claude", q.now.Add(2*time.Hour))
			offset := len(q.srv.Requests())
			q.pass()
			if got := q.handoffs(affected); got != 1 {
				t.Errorf("working worker: %d immediate handoffs, want one", got)
			}
			methods := quotaTerminalCalls(q.srv.Requests()[offset:], affected)
			interrupt, send := slices.Index(methods, "terminal.interrupt"), slices.Index(methods, "terminal.send")
			if interrupt < 0 || send <= interrupt {
				t.Errorf("working worker: interrupt index %d, send index %d, want interrupt before send", interrupt, send)
			}
			if name == "restart after sending" {
				q.restart()
			}
			q.pass()
			if name == "restart before recheck" {
				q.restart()
			}
			*q.now = q.now.Add(time.Minute)
			q.pass()
			if got := q.handoffs(affected); got != 1 {
				t.Errorf("affected worker: %d total handoffs after repeated floor readings, want one", got)
			}
			if got := q.handoffs(healthy); got != 0 {
				t.Errorf("healthy worker: %d handoffs, want zero", got)
			}
			q.assertLive(affected)
			q.assertLive(healthy)
		})
	}
}

func TestQuotaFloorIdleWorkerGetsOneTurnThenStops(t *testing.T) {
	q := newQuotaHistory(t, nil)
	q.startWorkers(poolTask("T1", ""))
	w := q.attempt(0).RunID
	q.status(w, "idle")
	q.turnEnds()
	if q.attempt(0).Run.State != flow.PhaseIdle {
		t.Fatal("fixture worker must be idle")
	}

	*q.now = q.now.Add(time.Minute)
	q.atFloor("claude", q.now.Add(2*time.Hour))
	q.pass()
	if a := q.attempt(0); q.handoffs(w) != 1 || a.Run.State != flow.PhaseWorking {
		t.Errorf("idle worker: handoffs = %d, phase = %q, want one handoff starting a turn", q.handoffs(w), a.Run.State)
	}
	q.assertLive(w)
	q.status(w, "working")
	*q.now = q.now.Add(time.Second)
	q.pass()
	q.status(w, "idle")
	q.turnEnds()
	a := q.attempt(0)
	if a.Run.Result != flow.EndQuota || !q.stopped(w) || q.handoffs(w) != 1 {
		t.Errorf("after the handoff turn: result = %q, stopped = %v, handoffs = %d, want quota, stopped and one handoff", a.Run.Result, q.stopped(w), q.handoffs(w))
	}
}

func TestQuotaFloorStartingWorkerHoldsHandoffUntilReady(t *testing.T) {
	q := newQuotaHistory(t, nil)
	q.startWorkers(poolTask("T1", ""))
	w := q.attempt(0).RunID
	*q.now = q.now.Add(time.Minute)
	q.atFloor("claude", q.now.Add(2*time.Hour))
	q.pass()
	q.restart()
	if a := q.attempt(0); q.handoffs(w) != 0 || a.Run.State != flow.PhaseStarting {
		t.Fatalf("unready worker: handoffs = %d, phase = %q, want zero and starting", q.handoffs(w), a.Run.State)
	}
	q.status(w, "working")
	q.pass()
	q.pass()
	if got := q.handoffs(w); got != 1 {
		t.Errorf("ready worker: %d handoffs, want the held request once", got)
	}
	q.assertLive(w)
}

func (q *quotaHistory) setHandoffMinutes(minutes int) {
	q.t.Helper()
	// Use the documented JSON setting so this compiles before Settings
	// gains handoff_minutes.
	settings := struct {
		flow.Settings
		HandoffMinutes int `json:"handoff_minutes"`
	}{q.state().Settings, minutes}
	state := struct {
		flow.State
		Settings any `json:"settings"`
	}{q.state(), settings}
	raw, err := json.Marshal(state)
	if err != nil {
		q.t.Fatal(err)
	}
	if err := os.WriteFile(q.paths.State(q.id), raw, 0o600); err != nil {
		q.t.Fatal(err)
	}
}

func TestQuotaFloorBlockedWorkerStopsAtPersistedDeadline(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "uninterrupted"
		if restart {
			name = "restart before deadline"
		}
		t.Run(name, func(t *testing.T) {
			q := newQuotaHistory(t, nil)
			q.startWorkers(poolTask("T1", ""))
			w := q.attempt(0).RunID
			q.status(w, "blocked")
			q.pass()
			if q.attempt(0).Run.State != flow.PhaseBlocked {
				t.Fatal("fixture worker must be blocked on permission")
			}
			*q.now = q.now.Add(time.Minute)
			const handoffMinutes = 3
			q.setHandoffMinutes(handoffMinutes)
			deadline := q.now.Add(handoffMinutes * time.Minute)
			q.atFloor("claude", q.now.Add(2*time.Hour))
			q.pass()
			*q.now = deadline.Add(-time.Second)
			if restart {
				q.restart()
			} else {
				q.pass()
			}
			q.assertLive(w)
			*q.now = deadline
			q.pass()
			a := q.attempt(0)
			if a.Run.Result != flow.EndQuota || !a.Run.EndedAt.Equal(deadline) || !q.stopped(w) {
				t.Errorf("at original deadline: phase = %q, result = %q, ended at %v, stopped = %v, want quota stop at %v", a.Run.State, a.Run.Result, a.Run.EndedAt, q.stopped(w), deadline)
			}
			methods := quotaTerminalCalls(q.srv.Requests(), w)
			for _, method := range []string{"terminal.send", "terminal.keys", "terminal.interrupt"} {
				if slices.Contains(methods, method) {
					t.Errorf("blocked worker received %s with a permission prompt open", method)
				}
			}
		})
	}
}

func TestQuotaFloorHandoffRestartsInSameWorktree(t *testing.T) {
	const brief = "Full task brief: private fixture details about the original request."
	const held = "Keep the draft until the owner reviews it."
	const dirtyPath, dirty = "draft.go", "package draft\n"
	q := newQuotaHistory(t, quotaWorkerCandidates)
	q.startWorkers(poolTask("T1", `,"brief":"`+brief+`"`))
	a := q.attempt(0)
	q.status(a.RunID, "working")
	q.pass()
	if err := os.WriteFile(filepath.Join(a.Worktree, "committed.go"), []byte("package draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, a.Worktree, "add", "committed.go")
	gitIn(t, a.Worktree, "commit", "-m", "feat: save quota fixture")
	head := gitIn(t, a.Worktree, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(a.Worktree, dirtyPath), []byte(dirty), 0o600); err != nil {
		t.Fatal(err)
	}
	q.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Run.Held = []string{held} })

	*q.now = q.now.Add(time.Minute)
	q.atFloor("claude", q.now.Add(48*time.Hour))
	q.pools.set("codex", CommandOutput{Exit: 127}) // an unknown alternative is usable
	q.pass()
	q.restart()
	// Leaving the old terminal working gives its held message no chance to be
	// sent there. The deadline must carry it into the replacement launch.
	*q.now = q.now.Add(5 * time.Minute)
	gitServer := hctest.New(t)
	gitServer.OnExec = hctest.GitExec
	q.srv.OnExec = hctest.GitExec
	q.eng.git = world.NewGit(hc.NewClient(gitServer.Path), q.eng.now, nil)
	for range 3 {
		q.pass()
	}
	if !q.stopped(a.RunID) || roleRuns(*q.runs, flow.RoleWorker) != 2 {
		t.Fatalf("floor recovery: original stopped = %v, worker launches = %d, want the original stopped and one replacement", q.stopped(a.RunID), roleRuns(*q.runs, flow.RoleWorker))
	}
	replacement := q.attempt(0)
	p := q.last()
	if replacement.Run.CLI != "codex" || replacement.Run.ID == a.Run.ID || replacement.Worktree != a.Worktree || p.Cwd != a.Worktree {
		t.Errorf("replacement CLI = %q, same run = %v, worktree = %q, cwd = %q, want a fresh Codex run at %q", replacement.Run.CLI, replacement.Run.ID == a.Run.ID, replacement.Worktree, p.Cwd, a.Worktree)
	}
	if got, err := os.ReadFile(filepath.Join(a.Worktree, dirtyPath)); err != nil || string(got) != dirty {
		t.Errorf("dirty file after handoff = %q, %v, want %q", got, err, dirty)
	}
	if slices.ContainsFunc(sendsTo(t, q.srv.Requests(), a.RunID), func(s string) bool { return strings.Contains(s, held) }) {
		t.Error("held owner message was sent to the outgoing worker")
	}
	prompt := p.Argv[len(p.Argv)-1]
	task := q.state().Tasks[0]
	handoff := strings.TrimSpace(strings.TrimSuffix(prompt, workerPrompt(workerTemplate, task, replacement, "sheepdog")))
	for _, item := range []string{head[:7], dirtyPath, held, "the claude pool reached its floor", a.Worktree} {
		if !strings.Contains(handoff, item) {
			t.Errorf("replacement handoff is missing %q", item)
		}
	}
	if strings.Contains(handoff, brief) {
		t.Error("replacement handoff copied the original brief")
	}
	if strings.Count(prompt, held) != 1 {
		t.Error("replacement launch must deliver the held owner message once")
	}

	var logRead, dirtyRead bool
	for _, req := range slices.Concat(gitServer.Requests(), q.srv.Requests()) {
		var p hc.ExecParams
		if req.Method != "machine.exec" || json.Unmarshal(req.Params, &p) != nil || p.Cwd != a.Worktree {
			continue
		}
		logRead = logRead || slices.Contains(p.Argv, "log") || slices.Contains(p.Argv, "rev-list")
		dirtyRead = dirtyRead || slices.Contains(p.Argv, "status") || slices.Contains(p.Argv, "ls-files")
	}
	if !logRead || !dirtyRead {
		t.Errorf("herdr-core worktree reads: commits = %v, dirty files = %v, want both through machine.exec", logRead, dirtyRead)
	}
}

func TestQuotaFloorHandoffWaitNamesSoonestResetOnce(t *testing.T) {
	q := newQuotaHistory(t, quotaWorkerCandidates)
	q.startWorkers(poolTask("T1", ""))
	w := q.attempt(0).RunID
	q.status(w, "working")
	q.pass()

	*q.now = q.now.Add(time.Minute)
	soon, late := q.now.Add(36*time.Hour), q.now.Add(73*time.Hour)
	q.atFloor("claude", late) // listed first, but resets last
	q.atFloor("codex", soon)
	q.pass()
	*q.now = q.now.Add(5 * time.Minute)
	q.pass()
	q.restart()
	for range 3 {
		*q.now = q.now.Add(time.Minute)
		q.pass()
	}
	task := q.state().Tasks[0]
	if !q.stopped(w) || q.attempt(0).Run.Result != flow.EndQuota || roleRuns(*q.runs, flow.RoleWorker) != 1 {
		t.Errorf("no alternative: stopped = %v, result = %q, worker launches = %d, want a quota stop and no replacement", q.stopped(w), q.attempt(0).Run.Result, roleRuns(*q.runs, flow.RoleWorker))
	}
	if why := task.HoldWhy; !strings.Contains(strings.ToLower(why), "quota") || !reasonHasDay(why, soon) || reasonHasTime(why, late) {
		t.Errorf("hold reason = %q, want quota and the day and time of the soonest reset %s", why, soon.Format(time.RFC3339))
	}
	if waits := q.quotaWaits(); len(waits) != 1 || !reasonHasDay(waits[0].Why, soon) {
		t.Errorf("quota wait across restart = %+v, want one line naming the soonest reset with its day", waits)
	}
}
