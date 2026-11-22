package factory

// PR 5b.8, TDD step 1: failing tests for the persisted alert episode lifecycle,
// stopping-alert start gating, J3 (no_pool eligibility and partial shortage),
// J5 (coordinator signed-out hold, scheduled recheck with no run, unknown
// handling, configured fallback, and clear evidence), doctor unknown sign-in
// reporting, N36 (ci_stalled on proven pending checks at current head only),
// main_red (non-stopping, N38 narrower hold, strict clear), and outage episodes
// (github_unavailable, hc_unavailable).
//
// No production code is modified by this step; no test touches the real home,
// real accounts, a running daemon, or GitHub.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

type scriptedProbe struct {
	signedIn bool
	timedOut bool
	rpcErr   error
}

type alertWorld struct {
	t                *testing.T
	ctx              context.Context
	srv              *hctest.Server
	hcOnline         atomic.Bool
	proxySock        string
	client           *hc.Client
	paths            Paths
	codexHome        string
	repo             string
	baseSHA          string
	gh               string
	eng              *Engine
	id               string
	now              time.Time
	trimNext         bool
	everyStepRestart bool
	daemonLog        *bytes.Buffer

	mu          sync.Mutex
	claudeProbe scriptedProbe
	codexProbe  scriptedProbe
	claudeCalls int
	codexCalls  int
	pools       map[string]string
	runs        []hc.RunParams
	terms       []hc.Terminal
}

func newAlertWorld(t *testing.T, goal string, everyStepRestart bool) *alertWorld {
	t.Helper()
	codexHome := t.TempDir() // scoped fake sign-in; never touches real ~/.codex or real $HOME
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	srv := hctest.New(t)

	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	w := &alertWorld{
		t:                t,
		ctx:              context.Background(),
		srv:              srv,
		paths:            testPaths(t),
		codexHome:        codexHome,
		repo:             repo,
		baseSHA:          base,
		gh:               gh,
		now:              time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC),
		everyStepRestart: everyStepRestart,
		daemonLog:        &bytes.Buffer{},
		claudeProbe:      scriptedProbe{signedIn: true},
		codexProbe:       scriptedProbe{signedIn: true},
		pools:            map[string]string{},
	}
	w.hcOnline.Store(true)
	w.proxySock = attachHCProxy(t, srv.Path, &w.hcOnline)
	w.client = hc.NewClient(w.proxySock)

	srv.OnRun = func(term hc.Terminal, p hc.RunParams) {
		w.mu.Lock()
		w.terms = append(w.terms, term)
		w.runs = append(w.runs, p)
		w.mu.Unlock()
	}
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		argv := unwrapExecArgv(p.Argv)
		if len(argv) >= 2 && filepath.Base(argv[0]) == "claude" && argv[1] == "auth" {
			if p.Env["DISABLE_AUTOUPDATER"] != "1" {
				t.Errorf("claude auth probe missing DISABLE_AUTOUPDATER=1: %v", p.Env)
			}
			w.mu.Lock()
			pr := w.claudeProbe
			w.claudeCalls++
			w.mu.Unlock()
			return execProbeResult(pr, fmt.Sprintf(`{"loggedIn":%v}`, pr.signedIn), "")
		}
		if len(argv) >= 2 && filepath.Base(argv[0]) == "codex" && argv[1] == "login" {
			w.mu.Lock()
			pr := w.codexProbe
			w.codexCalls++
			w.mu.Unlock()
			if pr.signedIn {
				return execProbeResult(pr, "Logged in using ChatGPT\n", "")
			}
			return execProbeResultWithExit(pr, 1, "", "Not logged in\n")
		}
		if len(argv) >= 4 && filepath.Base(argv[0]) == "codexbar" && argv[1] == "usage" {
			prov := ""
			for i := 0; i+1 < len(argv); i++ {
				if argv[i] == "--provider" {
					prov = argv[i+1]
				}
			}
			w.mu.Lock()
			body, ok := w.pools[prov]
			now := w.now
			w.mu.Unlock()
			if !ok {
				body = poolOut(prov, now, &poolWin{80, 3 * time.Hour}, &poolWin{80, 72 * time.Hour}, nil).Stdout
			}
			zero := 0
			return hc.ExecResult{Code: &zero, Stdout: []byte(body)}, nil
		}
		return hctest.GitExec(p)
	}

	w.newEngine()
	if err := w.eng.Start(repo, goal, 4); err != nil {
		t.Fatal(err)
	}
	w.id = w.eng.projectDirs()[0].Name()
	return w
}

func unwrapExecArgv(argv []string) []string {
	if len(argv) >= 5 && filepath.Base(argv[0]) == "sh" && argv[1] == "-c" && argv[3] == "sh" {
		return argv[4:]
	}
	return argv
}

func execProbeResult(pr scriptedProbe, stdout, stderr string) (hc.ExecResult, error) {
	return execProbeResultWithExit(pr, 0, stdout, stderr)
}

func execProbeResultWithExit(pr scriptedProbe, code int, stdout, stderr string) (hc.ExecResult, error) {
	if pr.rpcErr != nil {
		return hc.ExecResult{}, pr.rpcErr
	}
	if pr.timedOut {
		return hc.ExecResult{TimedOut: true}, nil
	}
	c := code
	return hc.ExecResult{Code: &c, Stdout: []byte(stdout), Stderr: []byte(stderr)}, nil
}

func newAlertWorldWithProxy(t *testing.T, goal string, everyStepRestart bool) *alertWorld {
	t.Helper()
	return newAlertWorld(t, goal, everyStepRestart)
}

func (w *alertWorld) newEngine() {
	eng := New(w.paths, w.client)
	eng.Log = logging.NewJSONLLogger(w.daemonLog)
	eng.Now = func() time.Time {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.now
	}
	eng.onInvariant = func(vs []flow.InvariantViolation) {
		w.t.Fatalf("invariant violation at %s: %+v", w.now.Format(time.RFC3339), vs)
	}
	w.eng = eng
}

func (w *alertWorld) restart() {
	w.newEngine()
	w.trimNext = true
}

func (w *alertWorld) pass() {
	w.t.Helper()
	if w.everyStepRestart {
		w.restart()
	}
	trim := w.trimNext
	w.trimNext = false
	w.eng.passProject(w.ctx, w.id, trim)
}

func (w *alertWorld) advance(d time.Duration) time.Time {
	w.mu.Lock()
	w.now = w.now.Add(d)
	now := w.now
	w.mu.Unlock()
	return now
}

func (w *alertWorld) setPool(provider string, five, week *poolWin) {
	w.mu.Lock()
	now := w.now
	w.pools[provider] = poolOut(provider, now, five, week, nil).Stdout
	w.mu.Unlock()
	w.eng.mu.Lock()
	delete(w.eng.pools, provider)
	w.eng.mu.Unlock()
}

func (w *alertWorld) setClaudeProbe(p scriptedProbe) {
	w.mu.Lock()
	w.claudeProbe = p
	w.mu.Unlock()
	w.eng.mu.Lock()
	delete(w.eng.cliProbes, "claude")
	w.eng.mu.Unlock()
}

func (w *alertWorld) setCodexProbe(p scriptedProbe) {
	w.mu.Lock()
	w.codexProbe = p
	if p.signedIn {
		_ = os.WriteFile(filepath.Join(w.codexHome, "auth.json"), []byte("{}"), 0o600)
	} else if !p.timedOut && p.rpcErr == nil {
		_ = os.Remove(filepath.Join(w.codexHome, "auth.json"))
	}
	w.mu.Unlock()
	w.eng.mu.Lock()
	delete(w.eng.cliProbes, "codex")
	w.eng.mu.Unlock()
}

func (w *alertWorld) state() flow.State {
	w.t.Helper()
	s, err := LoadState(w.paths.State(w.id))
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

func (w *alertWorld) edit(fn func(*flow.State)) {
	w.t.Helper()
	s := w.state()
	fn(&s)
	if err := SaveState(w.paths.State(w.id), s); err != nil {
		w.t.Fatal(err)
	}
}

func (w *alertWorld) runCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.runs)
}

func (w *alertWorld) roleRunCount(role string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, r := range w.runs {
		if r.Labels["sheepdog.role"] == role {
			n++
		}
	}
	return n
}

func (w *alertWorld) claudeProbeCalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.claudeCalls
}

// statusAlert checks `sheepdog factory status` via ProjectStatus + FormatStatusLine
// and returns the active status entry for alert kind (e.g. "no_pool",
// "agents_down", "ci_stalled", "main_red", "github_unavailable", "hc_unavailable").
func statusAlert(t *testing.T, paths Paths, folder, kind string) (TaskStatus, bool) {
	t.Helper()
	rows, err := ProjectStatus(paths, folder)
	if err != nil {
		t.Fatalf("ProjectStatus(%s): %v", folder, err)
	}
	for _, ts := range rows {
		line := FormatStatusLine(ts)
		joined := strings.Join([]string{ts.ID, ts.Title, ts.Stage, ts.Rule, ts.Why, line}, " ")
		if strings.Contains(joined, kind) {
			return ts, true
		}
	}
	return TaskStatus{}, false
}

// persistedAlertBlob returns the persisted JSON fields on state.json outside
// pre-5b.8 non-alert fields (wakes, acts, tasks, run, base, etc.), so tests can
// verify that alert episodes (kind, cause, and raised timestamp) live on disk
// across daemon restarts without coupling to private struct field names.
func persistedAlertBlob(t *testing.T, paths Paths, id string) string {
	t.Helper()
	raw, err := os.ReadFile(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var proj map[string]json.RawMessage
	if p, ok := doc["project"]; ok {
		_ = json.Unmarshal(p, &proj)
	}
	for _, nonAlert := range []string{
		"id", "folder", "goal", "started", "new_goal", "new_goal_tasks",
		"run_id", "run_token_sha256", "run",
		"coord_dialog_stucks", "coord_error_at", "coord_error_turn",
		"coord_wake_at", "coord_wakes_in_flight", "coord_wake_turn", "coord_last_wake_at",
		"base", "invariants", "sweep_items", "sweep_unknown", "wakes", "acts",
	} {
		delete(proj, nonAlert)
	}
	delete(doc, "seq")
	delete(doc, "settings")
	delete(doc, "tasks")
	projBytes, _ := json.Marshal(proj)
	doc["project"] = projBytes
	out, _ := json.Marshal(doc)
	return string(out)
}

// alertEpisodeLogs returns log.jsonl entries that record an alert/outage episode
// event for kind (excluding N36's pre-existing per-head coordinator wake act).
func alertEpisodeLogs(t *testing.T, paths Paths, folder, kind string) []LogLine {
	t.Helper()
	lines, _, err := ProjectLog(context.Background(), paths, folder, 0)
	if err != nil {
		t.Fatalf("ProjectLog: %v", err)
	}
	var out []LogLine
	for _, l := range lines {
		if l.Task != "" && l.Rule == "N36" && l.Decision == "woke coordinator" {
			continue
		}
		hay := strings.Join(append([]string{l.Rule, l.Decision, l.Why}, l.Asked...), " ")
		if l.Input != nil {
			b, _ := json.Marshal(l.Input)
			hay += " " + string(b)
		}
		if strings.Contains(hay, kind) {
			out = append(out, l)
		}
	}
	return out
}

func runBothModes(t *testing.T, fn func(t *testing.T, everyStepRestart bool)) {
	t.Helper()
	t.Run("continuous", func(t *testing.T) { fn(t, false) })
	t.Run("restart_every_step", func(t *testing.T) { fn(t, true) })
}

// startDialingHCProxy replaces w.client with a toggleable Unix proxy to w.srv
// so w.hcOnline can drop and restore herdr-core mid-test while keeping all
// terminals in w.srv intact.
func attachHCProxy(t *testing.T, srvPath string, online *atomic.Bool) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sd-hcp-")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "hc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if !online.Load() {
				_ = c.Close()
				continue
			}
			up, err := net.Dial("unix", srvPath)
			if err != nil {
				_ = c.Close()
				continue
			}
			go func() {
				defer func() { _ = c.Close(); _ = up.Close() }()
				_, _ = io.Copy(up, c)
			}()
			go func() {
				defer func() { _ = c.Close(); _ = up.Close() }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return sock
}

// makeCoordinatorIdle advances the live Claude coordinator through R7 -> R20
// so it has completed a turn and sits idle.
func (w *alertWorld) makeCoordinatorIdle() string {
	w.t.Helper()
	addr := w.state().Project.RunID
	if addr == "" {
		w.t.Fatal("expected live coordinator RunID")
	}
	w.srv.SetStatus(addr, &hc.Status{App: "claude-code", State: "idle"})
	w.pass() // R7 sends first prompt -> working
	w.advance(25 * time.Second)
	w.srv.SetStatus(addr, &hc.Status{App: "claude-code", State: "idle"})
	w.pass() // R20 -> idle (turn 1 complete)
	return addr
}

// Decision 1 (KB-QUI-3, KB-ENV-5, KB-QTA-2):
// An alert is a persisted episode: raised once with its cause and time, logged
// and shown once per episode, visible in `factory status` even with zero tasks,
// kept across a restart without repeating, cleared when its own clear evidence
// is seen, and raised again as a new episode if the cause returns.
func TestPR5b8_AlertEpisodeLifecycleZeroTasksAndRestart(t *testing.T) {
	runBothModes(t, func(t *testing.T, everyStepRestart bool) {
		w := newAlertWorldWithProxy(t, "Zero-task alert episode", everyStepRestart)
		if got := len(w.state().Tasks); got != 0 {
			t.Fatalf("expected 0 tasks, got %d", got)
		}

		// Episode 1: coordinator pool at floor (2% <= 5%), no coordinator run alive.
		t1 := w.now
		w.setPool("claude", &poolWin{2, 90 * time.Minute}, &poolWin{60, 72 * time.Hour})
		w.pass()

		ts1, ok := statusAlert(t, w.paths, w.repo, "no_pool")
		if !ok {
			rows, _ := ProjectStatus(w.paths, w.repo)
			t.Fatalf("expected active no_pool alert in factory status even with zero tasks; got rows=%+v", rows)
		}
		if strings.TrimSpace(ts1.Why) == "" && strings.TrimSpace(ts1.Title) == "" {
			t.Fatalf("active no_pool alert in status must include its cause: %+v", ts1)
		}
		if !ts1.At.IsZero() && !ts1.At.Equal(t1) {
			t.Fatalf("episode 1 status At = %v, want %v", ts1.At, t1)
		}
		blob1 := persistedAlertBlob(t, w.paths, w.id)
		if !strings.Contains(blob1, "no_pool") || !strings.Contains(blob1, t1.UTC().Format(time.RFC3339)) {
			t.Fatalf("state.json must persist no_pool with episode 1 timestamp %s; got %s", t1.UTC().Format(time.RFC3339), blob1)
		}
		logsAfterRaise1 := alertEpisodeLogs(t, w.paths, w.repo, "no_pool")
		if len(logsAfterRaise1) != 1 {
			t.Fatalf("expected 1 logged raise line for episode 1 of no_pool, got %d: %+v", len(logsAfterRaise1), logsAfterRaise1)
		}

		// Repeat passes and an explicit daemon restart while the cause still holds:
		// kept across restart without repeating, original episode time preserved.
		w.advance(time.Minute)
		w.pass()
		w.restart()
		w.advance(time.Minute)
		w.pass()
		w.advance(time.Minute)
		w.pass()

		tsAfterRestart, ok := statusAlert(t, w.paths, w.repo, "no_pool")
		if !ok {
			t.Fatal("no_pool disappeared across restart while pool remained at floor")
		}
		if !tsAfterRestart.At.IsZero() && !tsAfterRestart.At.Equal(t1) {
			t.Fatalf("no_pool timestamp drifted across restart: got %v, want %v", tsAfterRestart.At, t1)
		}
		if !strings.Contains(persistedAlertBlob(t, w.paths, w.id), t1.UTC().Format(time.RFC3339)) {
			t.Fatalf("state.json lost original episode 1 timestamp %s across restart", t1.UTC().Format(time.RFC3339))
		}
		if got := alertEpisodeLogs(t, w.paths, w.repo, "no_pool"); len(got) != 1 {
			t.Fatalf("no_pool repeated in log.jsonl across passes/restart: got %d lines %+v, want 1", len(got), got)
		}

		// Clear evidence: usable pool appears -> episode 1 clears, status with 0 tasks has no active alert.
		w.advance(95 * time.Minute)
		w.setPool("claude", &poolWin{80, 4 * time.Hour}, &poolWin{60, 72 * time.Hour})
		w.pass()

		if ts, still := statusAlert(t, w.paths, w.repo, "no_pool"); still {
			t.Fatalf("no_pool remained active in factory status after usable pool appeared: %+v", ts)
		}
		logsAfterClear1 := len(alertEpisodeLogs(t, w.paths, w.repo, "no_pool"))

		// Restart while clear: stays cleared, emits nothing extra.
		w.restart()
		w.advance(time.Minute)
		w.pass()
		if _, still := statusAlert(t, w.paths, w.repo, "no_pool"); still {
			t.Fatal("cleared no_pool re-appeared across restart while pool was healthy")
		}
		if got := len(alertEpisodeLogs(t, w.paths, w.repo, "no_pool")); got != logsAfterClear1 {
			t.Fatalf("unexpected no_pool log activity while cleared across restart: got %d, want %d", got, logsAfterClear1)
		}

		// Cause returns after coordinator exits -> raised again as a new episode at t2 > t1.
		coordAddr := w.state().Project.RunID
		if coordAddr != "" {
			w.srv.FinishExit(coordAddr, "exited", 0)
			w.pass()
		}
		t2 := w.advance(15 * time.Minute)
		w.setPool("claude", &poolWin{1, 2 * time.Hour}, &poolWin{60, 72 * time.Hour})
		w.pass()

		ts2, ok := statusAlert(t, w.paths, w.repo, "no_pool")
		if !ok {
			t.Fatal("expected no_pool to raise again as a new episode when cause returned")
		}
		if !ts2.At.IsZero() && !ts2.At.Equal(t2) {
			t.Fatalf("episode 2 status At = %v, want new episode time %v (after %v)", ts2.At, t2, t1)
		}
		if !strings.Contains(persistedAlertBlob(t, w.paths, w.id), t2.UTC().Format(time.RFC3339)) {
			t.Fatalf("state.json did not record new episode timestamp %s", t2.UTC().Format(time.RFC3339))
		}
		logsAfterRaise2 := len(alertEpisodeLogs(t, w.paths, w.repo, "no_pool"))
		if logsAfterRaise2 <= logsAfterClear1 {
			t.Fatalf("re-raised episode 2 did not log a new episode entry: count stayed %d", logsAfterRaise2)
		}
		w.restart()
		w.advance(time.Minute)
		w.pass()
		if got := len(alertEpisodeLogs(t, w.paths, w.repo, "no_pool")); got != logsAfterRaise2 {
			t.Fatalf("episode 2 repeated across restart: got %d, want %d", got, logsAfterRaise2)
		}
	})
}

// Decision 2 (KB-ENV-5, KB-COO-18, design.md 4.8 & 6):
// A stopping alert holds every new start: coordinator, worker, reviewer (N29),
// and restarts across roles. Runs already live stay managed (a live coordinator
// keeps getting wakes and heartbeats).
func TestPR5b8_StoppingAlertHoldsNewStartsAndManagesLiveCoordinator(t *testing.T) {
	runBothModes(t, func(t *testing.T, everyStepRestart bool) {
		w := newAlertWorldWithProxy(t, "Stopping alert gates all starts", everyStepRestart)

		// Start Claude coordinator while healthy and bring it to idle.
		w.pass() // J7 starts coordinator
		coordAddr := w.makeCoordinatorIdle()

		// Configure workers as Claude-only, while coordinator and round-2 reviewer
		// can use Codex (whose pool stays at 80% healthy!). That way, when Claude's
		// pool hits its floor, `no_pool` is raised (no eligible open task has a
		// usable worker candidate), while N29 reviewer starts/retries and coordinator
		// restarts HAVE a healthy Codex candidate and are held purely by Decision 2's
		// stopping-alert gate.
		ctx := w.ctx
		sharedHead := ""
		makeReviewReadyAttempt := func(taskID, branch, file string, withFailedRetry bool) flow.Attempt {
			wt, err := fixtureGit(t).NewWorktree(ctx, w.repo, w.paths.Worktree(w.id, taskID, 1), branch, "feat: "+taskID)
			if err != nil {
				t.Fatal(err)
			}
			var head string
			if sharedHead == "" {
				if err := os.WriteFile(filepath.Join(wt.Path, file), []byte("package demo\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				gitIn(t, wt.Path, "add", file)
				gitIn(t, wt.Path, "commit", "-m", "feat: review-ready")
				head = gitIn(t, wt.Path, "rev-parse", "HEAD")
				sharedHead = head
			} else {
				head = sharedHead
				gitIn(t, wt.Path, "reset", "--hard", head)
			}
			gitIn(t, wt.Path, "push", "origin", head+":refs/heads/"+wt.Branch)
			wkAddr, err := startRun(ctx, w.client, world.RunSpec{Name: workerName(w.id, taskID, 1), Cwd: wt.Path, Argv: []string{"sh"}})
			if err != nil {
				t.Fatal(err)
			}
			setIdle(t, w.srv, w.client, wkAddr, "idle")
			att := flow.Attempt{
				SizeCap: 100, BaseCommit: w.baseSHA, Branch: wt.Branch, Worktree: wt.Path, PR: 10,
				RunID: wkAddr,
				Run:   &flow.RunRecord{ID: wkAddr, Role: flow.RoleWorker, CLI: "claude", Model: "sonnet", State: flow.PhaseIdle, StartedAt: w.now.Add(-10 * time.Minute), Turn: 1},
				Ready: []flow.Ready{{Head: head}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: head, Reviewer: flow.Reviewer{CLI: "claude", Model: "opus", Effort: "high"}, Verdict: flow.VerdictPass},
				},
			}
			if withFailedRetry {
				att.Review = &flow.ReviewRun{
					Round: 2, Head: head, Kind: flow.RoundFull,
					Reviewer: flow.Reviewer{CLI: "codex", Model: codexModel, Effort: "high"},
					Retries:  1,
					Run:      &flow.RunRecord{Role: flow.RoleReviewer, CLI: "codex", Model: codexModel, State: flow.PhaseEnded, Result: flow.EndError, EndedAt: w.now.Add(-5 * time.Minute)},
				}
			}
			writePRJSON(t, w.gh, "view.json", head, greenRollup)
			return att
		}

		wtCrash, err := fixtureGit(t).NewWorktree(ctx, w.repo, w.paths.Worktree(w.id, "T2", 1), "T2-1", "feat: T2")
		if err != nil {
			t.Fatal(err)
		}
		att3 := makeReviewReadyAttempt("T3", "T3-1", "t3.go", false)
		att4 := makeReviewReadyAttempt("T4", "T4-1", "t4.go", true)

		w.edit(func(s *flow.State) {
			s.Settings.MaxRunning = 4
			s.Settings.SecondReviewer = "codex"
			s.Settings.Models = map[string][]flow.Candidate{
				flow.RoleCoordinator: {
					{CLI: "claude", Model: "opus", Effort: "high"},
					{CLI: "codex", Model: codexModel, Effort: "high"},
				},
				flow.RoleWorker: {
					{CLI: "claude", Model: "sonnet", Effort: "medium"},
				},
			}
			s.Tasks = []flow.Task{
				{ID: "T1", Title: "feat: new worker", Brief: "b", EstimateLines: 40, Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}}},
				{
					ID: "T2", Title: "feat: worker restart", Brief: "b", EstimateLines: 40, Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t2"}},
					Attempts: []flow.Attempt{{
						SizeCap: 100, BaseCommit: w.baseSHA, Branch: wtCrash.Branch, Worktree: wtCrash.Path,
						Restarts: 0,
						Run:      &flow.RunRecord{Role: flow.RoleWorker, CLI: "claude", Model: "sonnet", State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: w.now.Add(-5 * time.Minute)},
					}},
				},
				{ID: "T3", Title: "feat: new N29 reviewer", Brief: "b", EstimateLines: 40, Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t3"}}, Attempts: []flow.Attempt{att3}},
				{ID: "T4", Title: "feat: N29 reviewer retry", Brief: "b", EstimateLines: 40, Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t4"}}, Attempts: []flow.Attempt{att4}},
			}
			s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: "owner_say@1", Why: "status check from owner", At: w.now})
		})

		// Exhaust Claude pool (workers); Codex stays at 80% healthy!
		w.setPool("claude", &poolWin{2, 2 * time.Hour}, &poolWin{60, 72 * time.Hour})
		w.setPool("codex", &poolWin{80, 3 * time.Hour}, &poolWin{80, 72 * time.Hour})

		runsBefore := w.runCount()
		j12Before := len(logRules(t, w.paths.Log(w.id), "J12"))

		w.pass()
		w.advance(30 * time.Second)
		w.pass()

		if _, ok := statusAlert(t, w.paths, w.repo, "no_pool"); !ok {
			t.Error("expected stopping alert no_pool to be active when no eligible task has a usable worker candidate")
		}
		if got := w.runCount(); got != runsBefore {
			w.mu.Lock()
			var roles []string
			for _, r := range w.runs[runsBefore:] {
				roles = append(roles, r.Labels["sheepdog.role"]+":"+r.Labels["sheepdog.task"])
			}
			w.mu.Unlock()
			t.Fatalf("stopping alert failed to hold new starts/restarts: %d run(s) launched (%v)", got-runsBefore, roles)
		}
		st := w.state()
		if len(st.Tasks[0].Attempts) != 0 {
			t.Fatalf("T1 started an attempt during stopping alert: %+v", st.Tasks[0].Attempts)
		}
		if st.Tasks[1].Attempts[0].RunID != "" {
			t.Fatalf("T2 restarted worker during stopping alert: %+v", st.Tasks[1].Attempts[0])
		}
		if st.Tasks[2].Attempts[0].Review != nil && st.Tasks[2].Attempts[0].Review.RunID != "" {
			t.Fatalf("T3 started N29 reviewer during stopping alert: %+v", st.Tasks[2].Attempts[0].Review)
		}
		if st.Tasks[3].Attempts[0].Review != nil && st.Tasks[3].Attempts[0].Review.RunID != "" {
			t.Fatalf("T4 retried N29 reviewer during stopping alert: %+v", st.Tasks[3].Attempts[0].Review)
		}

		// Meanwhile, the already-live coordinator stays managed: held wake was delivered (J12),
		// and after returning to idle + advancing heartbeat_minutes, J13 delivers a heartbeat.
		if len(logRules(t, w.paths.Log(w.id), "J12")) <= j12Before || len(st.Project.Wakes) != 0 {
			t.Fatalf("live coordinator did not receive held wakes (J12) during stopping alert: wakes=%+v", st.Project.Wakes)
		}
		w.advance(25 * time.Second)
		w.srv.SetStatus(coordAddr, &hc.Status{App: "claude-code", State: "idle"})
		w.pass() // R20 -> idle again
		j13Before := len(logRules(t, w.paths.Log(w.id), "J13"))
		w.advance(time.Duration(w.state().Settings.HeartbeatMinutes)*time.Minute + time.Minute)
		w.pass()
		if len(logRules(t, w.paths.Log(w.id), "J13")) <= j13Before {
			t.Fatal("live coordinator did not receive J13 heartbeat while stopping alert was active")
		}

		// Now the live coordinator exits; even though Codex (80%) is configured for
		// coordinator, J3 holds the coordinator restart while stopping alert no_pool is active.
		w.srv.FinishExit(coordAddr, "exited", 0)
		w.pass()
		w.advance(time.Duration(w.state().Settings.CoordinatorGapMinutes)*time.Minute + time.Minute)
		w.pass()
		if got := w.runCount(); got != runsBefore {
			t.Fatalf("J3 failed to hold coordinator restart while stopping alert no_pool remained active: %d new run(s)", got-runsBefore)
		}
	})
}

// Decision 3 (KB-QTA-2, KB-QTA-3, KB-QUI-2):
// J3, no usable choice: a partial shortage only holds affected tasks (5b.6).
// The project-level stop is raised only when there is no usable coordinator
// candidate (with no live coordinator), or no usable worker candidate for any
// eligible open task. Eligible means not done, not held by a dependency, and
// not approved and waiting only on CI. Clears when a usable candidate appears.
func TestPR5b8_J3NoUsableChoiceEligibilityAndPartialShortage(t *testing.T) {
	t.Run("partial_shortage_holds_task_without_raising_no_pool", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorldWithProxy(t, "Partial shortage", everyStepRestart)
			w.pass() // start coordinator
			w.makeCoordinatorIdle()

			w.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{
					{
						ID: "T1", Title: "feat: claude worker", Brief: "b", EstimateLines: 30,
						Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
						Request:  map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "claude", Model: "sonnet", Effort: "medium"}}},
					},
					{
						ID: "T2", Title: "feat: codex worker", Brief: "b", EstimateLines: 30,
						Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t2"}},
						Request:  map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "codex", Model: codexModel, Effort: "high"}}},
					},
				}
			})
			w.setPool("claude", &poolWin{2, 90 * time.Minute}, &poolWin{60, 72 * time.Hour})
			w.setPool("codex", &poolWin{80, 3 * time.Hour}, &poolWin{80, 72 * time.Hour})

			w.pass()
			w.pass()
			w.pass()

			if ts, active := statusAlert(t, w.paths, w.repo, "no_pool"); active {
				t.Fatalf("partial shortage raised project-level no_pool: %+v", ts)
			}
			st := w.state()
			if len(st.Tasks[0].Attempts) != 0 {
				t.Fatalf("T1 should be held on Claude quota, got %d attempts", len(st.Tasks[0].Attempts))
			}
			if len(st.Tasks[1].Attempts) != 1 || st.Tasks[1].Attempts[0].RunID == "" {
				t.Fatalf("T2 with healthy Codex candidate did not start: %+v", st.Tasks[1].Attempts)
			}
		})
	})

	t.Run("non_eligible_tasks_neither_trigger_nor_mask_no_pool", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorldWithProxy(t, "Eligibility rules", everyStepRestart)
			w.pass() // start coordinator
			w.makeCoordinatorIdle()

			// Build an approved task T_ci (passed 2 review rounds at headCI, waiting only on CI).
			wtCI, err := fixtureGit(t).NewWorktree(w.ctx, w.repo, w.paths.Worktree(w.id, "T-CI", 1), "T-CI-1", "feat: ci")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wtCI.Path, "ci.go"), []byte("package demo\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitIn(t, wtCI.Path, "add", "ci.go")
			gitIn(t, wtCI.Path, "commit", "-m", "feat: ci")
			headCI := gitIn(t, wtCI.Path, "rev-parse", "HEAD")
			gitIn(t, wtCI.Path, "push", "origin", headCI+":refs/heads/"+wtCI.Branch)
			wkCI, err := startRun(w.ctx, w.client, world.RunSpec{Name: workerName(w.id, "T-CI", 1), Cwd: wtCI.Path, Argv: []string{"sh"}})
			if err != nil {
				t.Fatal(err)
			}
			setIdle(t, w.srv, w.client, wkCI, "idle")
			writePRJSON(t, w.gh, "view.json", headCI, `[{"__typename":"CheckRun","name":"ci","status":"IN_PROGRESS"}]`)

			// Case 1: Only non-eligible tasks exist (T_done is Done, T_ci is approved & waiting
			// only on CI, T_dep depends on T_ci). Even with both worker pools at floor, no
			// eligible open task exists while the coordinator is live -> no_pool must NOT raise.
			w.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{
					{
						ID: "T-DONE", Title: "feat: done", Brief: "b", EstimateLines: 20,
						Done:     &flow.Merged{MergedCommit: "abc1234"},
						Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: d"}},
					},
					{
						ID: "T-CI", Title: "feat: waiting on ci", Brief: "b", EstimateLines: 20,
						Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: ci"}},
						Request:  map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "codex", Model: codexModel, Effort: "high"}}},
						Attempts: []flow.Attempt{{
							SizeCap: 100, BaseCommit: w.baseSHA, Branch: wtCI.Branch, Worktree: wtCI.Path, PR: 7,
							RunID: wkCI,
							Run:   &flow.RunRecord{ID: wkCI, Role: flow.RoleWorker, CLI: "codex", Model: codexModel, State: flow.PhaseIdle, StartedAt: w.now.Add(-10 * time.Minute), Turn: 1},
							Ready: []flow.Ready{{Head: headCI}},
							Rounds: []flow.Round{
								{N: 1, Kind: flow.RoundFull, Head: headCI, Verdict: flow.VerdictPass},
								{N: 2, Kind: flow.RoundFull, Head: headCI, Verdict: flow.VerdictPass},
							},
						}},
					},
					{
						ID: "T-DEP", Title: "feat: dep held", Brief: "b", EstimateLines: 20,
						After:    []string{"T-CI"},
						Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: dep"}},
						Request:  map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "codex", Model: codexModel, Effort: "high"}}},
					},
				}
			})
			w.setPool("claude", &poolWin{2, 2 * time.Hour}, &poolWin{60, 72 * time.Hour})
			w.setPool("codex", &poolWin{2, 2 * time.Hour}, &poolWin{60, 72 * time.Hour})
			w.pass()
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "no_pool"); active {
				t.Fatalf("non-eligible tasks (done, waiting only on CI, dep-held) falsely raised no_pool: %+v", ts)
			}

			// Case 2: Make Codex healthy (80%), so T-DONE, T-CI, and T-DEP all reference a
			// healthy Codex worker pool, and add ONE eligible open task T-ELIG that requires
			// Claude (still at 2%). Non-eligible tasks with healthy Codex candidates must NOT
			// mask T-ELIG having zero usable worker candidates -> no_pool MUST raise, and clear
			// when Claude becomes usable again.
			w.setPool("codex", &poolWin{80, 3 * time.Hour}, &poolWin{80, 72 * time.Hour})
			w.edit(func(s *flow.State) {
				s.Tasks = append(s.Tasks, flow.Task{
					ID: "T-ELIG", Title: "feat: sole eligible task", Brief: "b", EstimateLines: 20,
					Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: elig"}},
					Request:  map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "claude", Model: "sonnet", Effort: "medium"}}},
				})
			})
			w.pass()
			w.pass()
			if _, active := statusAlert(t, w.paths, w.repo, "no_pool"); !active {
				t.Fatal("expected no_pool when the sole eligible open task has no usable worker candidate (non-eligible tasks must not count)")
			}

			// Clear when a usable candidate appears for T-ELIG.
			w.setPool("claude", &poolWin{80, 3 * time.Hour}, &poolWin{80, 72 * time.Hour})
			w.pass()
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "no_pool"); active {
				t.Fatalf("no_pool did not clear when usable worker candidate appeared: %+v", ts)
			}
		})
	})
}

// Decision 4 (KB-COO-31, KB-ENV-9, KB-COO-27):
// J5, coordinator signed out: stays held until a positive sign-in check; a
// timed-out or unrunnable check is unknown (not positive, never signed out);
// rechecks run on schedule even with no live run; another coordinator candidate
// is used only if configured and positively signed in; clear evidence is a
// positive check for the held candidate or a completed turn of a coordinator
// (a healthy unrelated worker never clears it).
func TestPR5b8_J5CoordinatorSignedOutRecheckUnknownAndClear(t *testing.T) {
	runBothModes(t, func(t *testing.T, everyStepRestart bool) {
		w := newAlertWorldWithProxy(t, "Coordinator sign-out J5", everyStepRestart)

		// Configure coordinator = [claude/opus] only; worker = [codex] (signed in).
		w.edit(func(s *flow.State) {
			s.Settings.Models = map[string][]flow.Candidate{
				flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "high"}},
				flow.RoleWorker:      {{CLI: "codex", Model: codexModel, Effort: "high"}},
			}
			s.Tasks = []flow.Task{{
				ID: "T1", Title: "feat: unrelated codex worker", Brief: "b", EstimateLines: 30,
				Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
			}}
		})

		// Start coordinator, then simulate sign-out via R4 ("Not logged in · Please run /login")
		// and fake `claude auth status --json` -> loggedIn:false.
		w.pass() // J7 launches Claude coordinator + N11 creates T1 worktree
		coord1 := w.state().Project.RunID
		if coord1 == "" {
			t.Fatal("expected initial coordinator launch")
		}
		w.setClaudeProbe(scriptedProbe{signedIn: false})
		w.srv.SetScreen(coord1, "Not logged in · Please run /login\n")
		w.pass() // R4 -> ended(signed_out); N23 starts Codex worker on T1
		w.pass()

		st := w.state()
		if st.Project.RunID != "" {
			t.Fatalf("signed-out coordinator still live: RunID=%q", st.Project.RunID)
		}
		if _, ok := statusAlert(t, w.paths, w.repo, "agents_down"); !ok {
			t.Error("expected agents_down alert when coordinator is held at J5 on sign-out")
		}
		if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
			t.Fatalf("coordinator restarted or fell back to unconfigured CLI while signed out: %d runs", got)
		}
		episodesAfterSignOut := len(alertEpisodeLogs(t, w.paths, w.repo, "agents_down"))
		if episodesAfterSignOut != 1 {
			t.Errorf("expected 1 agents_down episode log on sign-out, got %d", episodesAfterSignOut)
		}

		// Advance past coordinator_gap_minutes and complete a turn on the healthy
		// unrelated Codex worker: worker runs fine (agents_down is non-stopping),
		// and a healthy unrelated worker NEVER clears J5 / agents_down.
		workerAddr := st.Tasks[0].Attempts[0].RunID
		if workerAddr == "" {
			t.Fatal("expected unrelated Codex worker to start while agents_down (non-stopping) is active")
		}
		w.srv.SetScreen(workerAddr, codexWorkingScreen("Add t1"))
		w.pass()
		w.srv.SetScreen(workerAddr, codexIdleScreen("Add t1", 9))
		w.pass()
		w.advance(30 * time.Second)
		w.pass() // Codex worker completes turn -> idle

		w.advance(time.Duration(st.Settings.CoordinatorGapMinutes)*time.Minute + time.Minute)
		w.pass()
		if _, ok := statusAlert(t, w.paths, w.repo, "agents_down"); !ok {
			t.Error("expected J5 / agents_down to remain active after healthy unrelated worker completed a turn")
		}
		if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
			t.Fatalf("coordinator restarted while still signed out: %d coordinator runs", got)
		}

		// Scheduled rechecks with NO coordinator run alive:
		// 1) Timed-out sign-in check -> unknown (not positive, so stays held; never
		//    counted as a new sign-out episode).
		// 2) Unrunnable sign-in check (RPC error) -> unknown as well.
		callsBeforeTimeout := w.claudeProbeCalls()
		w.setClaudeProbe(scriptedProbe{timedOut: true})
		w.advance(time.Duration(st.Settings.PoolMinutes)*time.Minute + time.Minute)
		w.pass()
		if w.claudeProbeCalls() <= callsBeforeTimeout {
			t.Fatal("J5 did not recheck sign-in on schedule while no coordinator run was alive")
		}
		if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
			t.Fatalf("J5 treated timed-out (unknown) sign-in check as positive and started coordinator: %d runs", got)
		}
		if got := len(alertEpisodeLogs(t, w.paths, w.repo, "agents_down")); got != episodesAfterSignOut {
			t.Fatalf("unknown (timed-out) sign-in check logged extra agents_down episode: got %d, want %d", got, episodesAfterSignOut)
		}

		callsBeforeErr := w.claudeProbeCalls()
		w.setClaudeProbe(scriptedProbe{rpcErr: errors.New("exec failed")})
		w.advance(time.Duration(st.Settings.PoolMinutes)*time.Minute + time.Minute)
		w.pass()
		if w.claudeProbeCalls() <= callsBeforeErr {
			t.Fatal("J5 stopped scheduled sign-in rechecks after timeout")
		}
		if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
			t.Fatalf("J5 treated failed-to-run (unknown) sign-in check as positive: %d runs", got)
		}

		// Configure a second coordinator candidate (Codex):
		// - While Codex's sign-in probe is unknown (timed out), it must NOT be used.
		// - Once Codex has a positive sign-in check (while Claude remains signed out),
		//   Codex coordinator starts, and completing a coordinator turn clears J5 / agents_down.
		w.setClaudeProbe(scriptedProbe{signedIn: false})
		w.setCodexProbe(scriptedProbe{timedOut: true})
		w.edit(func(s *flow.State) {
			s.Settings.Models[flow.RoleCoordinator] = []flow.Candidate{
				{CLI: "claude", Model: "opus", Effort: "high"},
				{CLI: "codex", Model: codexModel, Effort: "high"},
			}
		})
		w.advance(time.Duration(st.Settings.PoolMinutes)*time.Minute + time.Minute)
		w.pass()
		if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
			t.Fatalf("configured second coordinator candidate with unknown sign-in check was used: %d runs", got)
		}

		w.setCodexProbe(scriptedProbe{signedIn: true})
		w.advance(time.Duration(st.Settings.PoolMinutes)*time.Minute + time.Minute)
		w.pass()
		if got := w.roleRunCount(flow.RoleCoordinator); got != 2 {
			t.Fatalf("configured second coordinator candidate with positive sign-in check was not used: got %d coordinator runs, want 2", got)
		}
		codexCoord := w.state().Project.RunID
		w.srv.SetScreen(codexCoord, codexWorkingScreen("Goal"))
		w.pass()
		w.srv.SetScreen(codexCoord, codexIdleScreen("Goal", 8))
		w.pass()
		w.advance(30 * time.Second)
		w.pass() // Codex coordinator completes turn 1 -> clears agents_down
		if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
			t.Fatalf("completed coordinator turn did not clear agents_down: %+v", ts)
		}

		// Re-raise with single candidate [claude/opus], then clear via a positive
		// scheduled sign-in check for the held candidate with no run alive.
		w.srv.FinishExit(codexCoord, "exited", 0)
		w.edit(func(s *flow.State) {
			s.Settings.Models[flow.RoleCoordinator] = []flow.Candidate{{CLI: "claude", Model: "opus", Effort: "high"}}
		})
		w.setClaudeProbe(scriptedProbe{signedIn: true})
		w.advance(time.Duration(st.Settings.CoordinatorGapMinutes)*time.Minute + time.Minute)
		w.pass() // starts Claude coordinator #3
		coord3 := w.state().Project.RunID
		w.setClaudeProbe(scriptedProbe{signedIn: false})
		w.srv.SetScreen(coord3, "Not logged in · Please run /login\n")
		w.pass() // signed_out again -> episode 2 of agents_down
		if _, ok := statusAlert(t, w.paths, w.repo, "agents_down"); !ok {
			t.Fatal("expected second agents_down episode when Claude signed out again")
		}

		w.setClaudeProbe(scriptedProbe{signedIn: true})
		w.advance(time.Duration(st.Settings.PoolMinutes)*time.Minute + time.Minute)
		w.pass()
		if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
			t.Fatalf("positive sign-in check for held coordinator candidate did not clear J5 / agents_down: %+v", ts)
		}
	})
}

// Decision 4 doctor check (KB-ENV-9):
// A sign-in probe that times out or cannot run is unknown, never signed out:
// doctor must report "unknown (<reason>)", never "not signed in" or "not logged in".
// Virtual fake Home ("/Users/alice") stays outside store.go's temp-dir fixture guard
// and never touches the real home.
func TestPR5b8_DoctorSignInProbeUnknownNeverSaysNotSignedIn(t *testing.T) {
	ctx := context.Background()
	virtualHome := "/Users/alice"
	tmp := t.TempDir()

	for _, mode := range []struct {
		name       string
		out        CommandOutput
		wantSubstr string
	}{
		{name: "timeout", out: CommandOutput{Exit: -1, TimedOut: true}, wantSubstr: "time"},
		{name: "exec_error", out: CommandOutput{Exit: -1, Err: errors.New("hc exec transport closed")}, wantSubstr: "hc exec transport closed"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			env := newFakeEnv("claude", "codex", "agy", "gh")
			env.handlers["claude auth"] = func(string, []string, []string) CommandOutput { return mode.out }
			env.handlers["codex login"] = func(string, []string, []string) CommandOutput { return mode.out }
			env.handlers["agy models"] = func(string, []string, []string) CommandOutput { return mode.out }
			env.handlers["gh auth"] = func(string, []string, []string) CommandOutput { return mode.out }

			opts := DoctorOptions{
				Folder:      filepath.Join(virtualHome, "Documents", "demo"),
				GOOS:        "darwin",
				Home:        virtualHome,
				TempDir:     tmp,
				LookPath:    env.lookPath,
				RunCommand:  env.run,
				AuthTimeout: time.Second,
			}
			cliResults := CheckAgentCLIs(ctx, opts, false)
			ghResult := CheckGH(ctx, opts, false)

			for _, name := range []string{"claude", "codex", "agy"} {
				r := findResult(t, cliResults, name)
				low := strings.ToLower(r.Detail)
				if !strings.Contains(low, "unknown (") || !strings.Contains(low, strings.ToLower(mode.wantSubstr)) {
					t.Errorf("%s (%s): detail = %q, want \"unknown (<reason>)\" containing %q", name, mode.name, r.Detail, mode.wantSubstr)
				}
				if strings.Contains(low, "not signed in") || strings.Contains(low, "not logged in") {
					t.Errorf("%s (%s): reported unknown probe as signed out: %+v", name, mode.name, r)
				}
			}

			ghLow := strings.ToLower(ghResult.Detail)
			if !strings.Contains(ghLow, "unknown (") || !strings.Contains(ghLow, strings.ToLower(mode.wantSubstr)) {
				t.Errorf("gh (%s): detail = %q, want \"unknown (<reason>)\" containing %q", mode.name, ghResult.Detail, mode.wantSubstr)
			}
			if strings.Contains(ghLow, "not signed in") || strings.Contains(ghLow, "not logged in") {
				t.Errorf("gh (%s): reported unknown probe as signed out: %+v", mode.name, ghResult)
			}
		})
	}
}

// Decision 5 (design.md N36 & section 6):
// The N36 alert (ci_stalled) covers only proven pending checks on the current
// head: raised once per head after ci_stalled_minutes, cleared when checks
// complete or the head changes (a new head is a new episode only if its checks
// also stay pending).
func TestPR5b8_N36CIStalledProvenPendingCurrentHeadOnly(t *testing.T) {
	runBothModes(t, func(t *testing.T, everyStepRestart bool) {
		w := newAlertWorldWithProxy(t, "N36 ci_stalled", everyStepRestart)
		w.pass() // start coordinator
		w.makeCoordinatorIdle()

		wt, err := fixtureGit(t).NewWorktree(w.ctx, w.repo, w.paths.Worktree(w.id, "T1", 1), "T1-1", "feat: ci")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt.Path, "a.go"), []byte("package demo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, wt.Path, "add", "a.go")
		gitIn(t, wt.Path, "commit", "-m", "feat: c1")
		c1 := gitIn(t, wt.Path, "rev-parse", "HEAD")
		gitIn(t, wt.Path, "push", "origin", c1+":refs/heads/"+wt.Branch)
		wk, err := startRun(w.ctx, w.client, world.RunSpec{Name: workerName(w.id, "T1", 1), Cwd: wt.Path, Argv: []string{"sh"}})
		if err != nil {
			t.Fatal(err)
		}
		setIdle(t, w.srv, w.client, wk, "idle")

		w.edit(func(s *flow.State) {
			s.Tasks = []flow.Task{{
				ID: "T1", Title: "feat: ci", Brief: "b", EstimateLines: 30,
				Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: a.go"}},
				Attempts: []flow.Attempt{{
					SizeCap: 100, BaseCommit: w.baseSHA, Branch: wt.Branch, Worktree: wt.Path, PR: 7,
					RunID: wk,
					Run:   &flow.RunRecord{ID: wk, Role: flow.RoleWorker, CLI: "claude", Model: "sonnet", State: flow.PhaseIdle, StartedAt: w.now.Add(-5 * time.Minute), Turn: 1},
					Ready: []flow.Ready{{Head: c1}},
					Rounds: []flow.Round{
						{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
						{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					},
				}},
			}}
		})
		stalledDur := time.Duration(w.state().Settings.CIStalledMinutes) * time.Minute

		// 1) Non-pending N36 causes (mergeable UNKNOWN with green checks, unreadable
		//    checks, and unanswered snapshot) past ci_stalled_minutes must NOT raise ci_stalled.
		unknownMergeableView := fmt.Sprintf(`{"number":7,"state":"OPEN","headRefOid":%q,"mergeable":"UNKNOWN","mergeStateStatus":"UNKNOWN","reviewDecision":"","statusCheckRollup":%s}`, c1, greenRollup)
		writeGHFile(t, w.gh, "view.json", unknownMergeableView)
		w.pass()
		w.advance(stalledDur + 5*time.Minute)
		w.pass()
		if ts, active := statusAlert(t, w.paths, w.repo, "ci_stalled"); active {
			t.Fatalf("mergeable UNKNOWN (checks green) falsely raised ci_stalled: %+v", ts)
		}

		writeGHFile(t, w.gh, "fail", "")
		w.advance(stalledDur + 5*time.Minute)
		w.pass()
		if ts, active := statusAlert(t, w.paths, w.repo, "ci_stalled"); active {
			t.Fatalf("unanswered PR snapshot falsely raised ci_stalled: %+v", ts)
		}
		_ = os.Remove(filepath.Join(w.gh, "fail"))

		// 2) Proven pending checks on c1: not raised before ci_stalled_minutes; raised
		//    once at ci_stalled_minutes; kept across restart without repeating.
		writePRJSON(t, w.gh, "view.json", c1, `[{"__typename":"CheckRun","name":"ci","status":"IN_PROGRESS"}]`)
		w.pass() // first pending observation at c1
		w.advance(stalledDur - time.Second)
		w.pass()
		if ts, active := statusAlert(t, w.paths, w.repo, "ci_stalled"); active {
			t.Fatalf("ci_stalled raised before ci_stalled_minutes elapsed: %+v", ts)
		}

		tRaise1 := w.advance(time.Second)
		w.pass()
		ts1, active := statusAlert(t, w.paths, w.repo, "ci_stalled")
		if !active {
			t.Fatal("expected ci_stalled alert once proven pending checks on c1 reached ci_stalled_minutes")
		}
		if !strings.Contains(persistedAlertBlob(t, w.paths, w.id), "ci_stalled") {
			t.Fatal("state.json did not persist ci_stalled episode")
		}
		epLogs1 := len(alertEpisodeLogs(t, w.paths, w.repo, "ci_stalled"))
		if epLogs1 == 0 {
			t.Fatal("expected ci_stalled episode raise in log.jsonl")
		}
		w.restart()
		w.advance(time.Minute)
		w.pass()
		if got := len(alertEpisodeLogs(t, w.paths, w.repo, "ci_stalled")); got != epLogs1 {
			t.Fatalf("ci_stalled repeated across restart on same head c1: got %d, want %d", got, epLogs1)
		}
		_ = tRaise1
		_ = ts1

		// 3) Head changes c1 -> c2 (with pending checks on c2): clears c1's alert
		//    immediately; c2 is NOT an episode unless c2 itself stays pending past ci_stalled_minutes.
		if err := os.WriteFile(filepath.Join(wt.Path, "b.go"), []byte("package demo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, wt.Path, "add", "b.go")
		gitIn(t, wt.Path, "commit", "-m", "feat: c2")
		c2 := gitIn(t, wt.Path, "rev-parse", "HEAD")
		gitIn(t, wt.Path, "push", "origin", c2+":refs/heads/"+wt.Branch)
		w.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			a.Ready = append(a.Ready, flow.Ready{Head: c2})
			a.Rounds = append(a.Rounds,
				flow.Round{N: 3, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass},
				flow.Round{N: 4, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass},
			)
		})
		writePRJSON(t, w.gh, "view.json", c2, `[{"__typename":"CheckRun","name":"ci","status":"IN_PROGRESS"}]`)
		w.advance(time.Minute)
		w.pass()
		if ts, active := statusAlert(t, w.paths, w.repo, "ci_stalled"); active {
			t.Fatalf("ci_stalled was not cleared when head changed from c1 to c2: %+v", ts)
		}

		// c2 stays pending past ci_stalled_minutes -> new episode raised.
		w.advance(stalledDur + time.Second)
		w.pass()
		if _, active := statusAlert(t, w.paths, w.repo, "ci_stalled"); !active {
			t.Fatal("expected new ci_stalled episode when new head c2 also stayed pending past ci_stalled_minutes")
		}

		// 4) Checks complete on c2 (green, held behind base so task stays open) -> clears ci_stalled.
		writePRJSON(t, w.gh, "view.json", c2, greenRollup)
		writeGHFile(t, w.gh, "compare.json", `{"behind_by":1}`)
		w.advance(time.Minute)
		w.pass()
		if ts, active := statusAlert(t, w.paths, w.repo, "ci_stalled"); active {
			t.Fatalf("ci_stalled did not clear when checks completed on c2: %+v", ts)
		}
	})
}

// Decision 6 (design.md section 6 & N38; KB-MRG-47, KB-MRG-50, KB-MRG-91):
// main_red does not stop: new starts and up-to-date merges proceed while N38's
// narrower hold on behind/unknown-behind PRs stays; main_red clears only on a
// positively known completed good base snapshot.
func TestPR5b8_MainRedDoesNotStopKeepsN38AndClearsOnlyOnGoodCompletedBase(t *testing.T) {
	runBothModes(t, func(t *testing.T, everyStepRestart bool) {
		w := newAlertWorldWithProxy(t, "main_red non-stopping and strict clear", everyStepRestart)
		w.pass() // start coordinator
		w.makeCoordinatorIdle()

		// Prepare T1 (passed review, green PR checks, behind base by 1 -> held by N38).
		wt1, err := fixtureGit(t).NewWorktree(w.ctx, w.repo, w.paths.Worktree(w.id, "T1", 1), "T1-1", "feat: behind")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt1.Path, "t1.go"), []byte("package demo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, wt1.Path, "add", "t1.go")
		gitIn(t, wt1.Path, "commit", "-m", "feat: behind")
		c1 := gitIn(t, wt1.Path, "rev-parse", "HEAD")
		gitIn(t, wt1.Path, "push", "origin", c1+":refs/heads/"+wt1.Branch)
		wk1, err := startRun(w.ctx, w.client, world.RunSpec{Name: workerName(w.id, "T1", 1), Cwd: wt1.Path, Argv: []string{"sh"}})
		if err != nil {
			t.Fatal(err)
		}
		setIdle(t, w.srv, w.client, wk1, "idle")

		w.edit(func(s *flow.State) {
			s.Tasks = []flow.Task{{
				ID: "T1", Title: "feat: behind", Brief: "b", EstimateLines: 30,
				Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1.go"}},
				Attempts: []flow.Attempt{{
					SizeCap: 100, BaseCommit: w.baseSHA, Branch: wt1.Branch, Worktree: wt1.Path, PR: 7,
					RunID: wk1,
					Run:   &flow.RunRecord{ID: wk1, Role: flow.RoleWorker, CLI: "claude", Model: "sonnet", State: flow.PhaseIdle, StartedAt: w.now.Add(-5 * time.Minute), Turn: 1},
					Ready: []flow.Ready{{Head: c1}},
					Rounds: []flow.Round{
						{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
						{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					},
				}},
			}}
		})

		baseHead1 := "deadbeef1234567890deadbeef1234567890dead"
		writePRJSON(t, w.gh, "view.json", c1, greenRollup)
		writeGHFile(t, w.gh, "compare.json", `{"behind_by":1}`)
		writeGHFile(t, w.gh, "base-ref.json", fmt.Sprintf(`{"object":{"sha":%q}}`, baseHead1))
		writeBaseRuns(t, w.gh, fmt.Sprintf(`{"workflow_runs":[{"id":101,"head_sha":%q,"status":"completed","conclusion":"failure"}]}`, baseHead1))

		mainRedDur := time.Duration(w.state().Settings.MainRedMinutes) * time.Minute
		w.pass() // first red observation; N38 holds T1, main_red not yet raised
		if ts, active := statusAlert(t, w.paths, w.repo, "main_red"); active {
			t.Fatalf("main_red raised before main_red_minutes: %+v", ts)
		}

		// Advance past main_red_minutes -> main_red raises once and persists across restart.
		w.advance(mainRedDur + time.Minute)
		w.pass()
		if _, active := statusAlert(t, w.paths, w.repo, "main_red"); !active {
			t.Fatal("expected main_red alert once base stayed red past main_red_minutes")
		}
		epLogs1 := len(alertEpisodeLogs(t, w.paths, w.repo, "main_red"))
		if epLogs1 == 0 {
			t.Fatal("expected main_red raise in log.jsonl")
		}
		w.restart()
		w.advance(time.Minute)
		w.pass()
		if got := len(alertEpisodeLogs(t, w.paths, w.repo, "main_red")); got != epLogs1 {
			t.Fatalf("main_red repeated across restart: got %d, want %d", got, epLogs1)
		}

		// N38 narrower hold stays: T1 (behind=1) neither updates branch nor merges.
		if got := ghCalls(t, w.gh, "api -X PUT"); len(got) != 0 {
			t.Fatalf("N38 failed to hold branch update while base is red: %q", got)
		}
		if got := ghCalls(t, w.gh, "pr merge"); len(got) != 0 {
			t.Fatalf("N38 failed to hold behind PR merge while base is red: %q", got)
		}

		// main_red does NOT stop new starts: adding an open task T2 starts its worktree and worker!
		workersBefore := w.roleRunCount(flow.RoleWorker)
		w.edit(func(s *flow.State) {
			s.Tasks = append(s.Tasks, flow.Task{
				ID: "T2", Title: "feat: repair main", Brief: "Fix broken main.", EstimateLines: 30,
				Outcomes: []flow.Outcome{{Text: "fixed", ProvedBy: "manual: fix.go"}},
			})
		})
		w.pass() // N11 worktree for T2
		w.pass() // N23 worker for T2
		if w.roleRunCount(flow.RoleWorker) <= workersBefore {
			t.Fatal("main_red stopped a new task's worker from starting (main_red must not stop)")
		}

		// And when T1 is NOT behind (behind_by: 0), N42 still merges even while main_red is active;
		// restore behind_by: 1 right after verifying merge ability on a separate check or before merge:
		// verify strict clear conditions on main_red first while T1 stays behind!
		// 1) Base read failure (fail) must NOT clear main_red.
		writeGHFile(t, w.gh, "fail", "")
		w.advance(time.Minute)
		w.pass()
		if _, active := statusAlert(t, w.paths, w.repo, "main_red"); !active {
			t.Fatal("main_red cleared on a failed base snapshot read")
		}
		_ = os.Remove(filepath.Join(w.gh, "fail"))

		// 2) In-progress run on base head must NOT clear main_red.
		baseHead2 := "cafebabe1234567890cafebabe1234567890cafe"
		writeGHFile(t, w.gh, "base-ref.json", fmt.Sprintf(`{"object":{"sha":%q}}`, baseHead2))
		writeBaseRuns(t, w.gh, fmt.Sprintf(`{"workflow_runs":[{"id":102,"head_sha":%q,"status":"completed","conclusion":"success"},{"id":103,"head_sha":%q,"status":"in_progress","conclusion":null}]}`, baseHead2, baseHead2))
		w.advance(time.Minute)
		w.pass()
		if _, active := statusAlert(t, w.paths, w.repo, "main_red"); !active {
			t.Fatal("main_red cleared while a base workflow run was still in_progress")
		}

		// 3) Zero runs on base head must NOT clear main_red.
		writeBaseRuns(t, w.gh, `{"workflow_runs":[]}`)
		w.advance(time.Minute)
		w.pass()
		if _, active := statusAlert(t, w.paths, w.repo, "main_red"); !active {
			t.Fatal("main_red cleared on a base commit with zero workflow runs")
		}

		// 4) Positively known completed good base snapshot -> clears main_red!
		writeBaseRuns(t, w.gh, fmt.Sprintf(`{"workflow_runs":[{"id":102,"head_sha":%q,"status":"completed","conclusion":"success"}]}`, baseHead2))
		w.advance(time.Minute)
		w.pass()
		if ts, active := statusAlert(t, w.paths, w.repo, "main_red"); active {
			t.Fatalf("main_red did not clear on positively known completed good base snapshot: %+v", ts)
		}
	})
}

// Decision 7 (design.md section 6 & 7.3; KB-GIT-20, KB-COO-13, KB-MRG-85):
// Outage signals (github_unavailable, hc_unavailable) use the same persisted
// episode lifecycle: raised once per outage, kept across restart without
// repeating, cleared on the first good observation, and raised again on a new outage.
func TestPR5b8_OutageEpisodesGitHubAndHCUnavailable(t *testing.T) {
	t.Run("github_unavailable", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorldWithProxy(t, "GitHub outage episode", everyStepRestart)
			w.pass() // start coordinator
			w.makeCoordinatorIdle()

			// Outage 1: fake gh fails (triggers base observer / PR snapshot gh failure).
			t1 := w.advance(time.Minute)
			writeGHFile(t, w.gh, "fail", "")
			w.pass()

			ts1, active := statusAlert(t, w.paths, w.repo, "github_unavailable")
			if !active {
				t.Fatal("expected github_unavailable episode in factory status on gh outage")
			}
			if !ts1.At.IsZero() && !ts1.At.Equal(t1) {
				t.Fatalf("github_unavailable episode 1 At = %v, want %v", ts1.At, t1)
			}
			if !strings.Contains(persistedAlertBlob(t, w.paths, w.id), "github_unavailable") {
				t.Fatal("state.json did not persist github_unavailable episode")
			}
			ep1 := len(alertEpisodeLogs(t, w.paths, w.repo, "github_unavailable"))
			if ep1 != 1 {
				t.Fatalf("expected 1 logged raise for github_unavailable episode 1, got %d", ep1)
			}

			// Repeat passes + restart during same outage: no duplicate log line.
			w.advance(time.Minute)
			w.pass()
			w.restart()
			w.advance(time.Minute)
			w.pass()
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "github_unavailable")); got != ep1 {
				t.Fatalf("github_unavailable repeated across passes/restart: got %d, want %d", got, ep1)
			}

			// First good observation clears github_unavailable.
			_ = os.Remove(filepath.Join(w.gh, "fail"))
			writeGHFile(t, w.gh, "base-ref.json", fmt.Sprintf(`{"object":{"sha":%q}}`, w.baseSHA))
			writeBaseRuns(t, w.gh, fmt.Sprintf(`{"workflow_runs":[{"id":1,"head_sha":%q,"status":"completed","conclusion":"success"}]}`, w.baseSHA))
			w.advance(time.Minute)
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "github_unavailable"); active {
				t.Fatalf("github_unavailable did not clear on first good gh observation: %+v", ts)
			}
			afterClear1 := len(alertEpisodeLogs(t, w.paths, w.repo, "github_unavailable"))

			// Second outage raises a new episode.
			t2 := w.advance(5 * time.Minute)
			writeGHFile(t, w.gh, "fail", "")
			w.pass()
			ts2, active := statusAlert(t, w.paths, w.repo, "github_unavailable")
			if !active {
				t.Fatal("expected second github_unavailable episode on new outage")
			}
			if !ts2.At.IsZero() && !ts2.At.Equal(t2) {
				t.Fatalf("github_unavailable episode 2 At = %v, want %v", ts2.At, t2)
			}
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "github_unavailable")); got <= afterClear1 {
				t.Fatalf("second github_unavailable outage did not log a new episode: got %d", got)
			}
		})
	})

	t.Run("hc_unavailable", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorldWithProxy(t, "hc outage episode", everyStepRestart)
			w.pass() // start coordinator
			coordAddr := w.makeCoordinatorIdle()

			// Outage 1: drop hc proxy connections -> hc_unavailable raised once,
			// live coordinator run is NOT marked lost, kept across restart without repeating.
			t1 := w.advance(time.Minute)
			w.hcOnline.Store(false)
			w.pass()

			ts1, active := statusAlert(t, w.paths, w.repo, "hc_unavailable")
			if !active {
				t.Fatal("expected hc_unavailable episode in factory status when herdr-core is unreachable")
			}
			if !ts1.At.IsZero() && !ts1.At.Equal(t1) {
				t.Fatalf("hc_unavailable episode 1 At = %v, want %v", ts1.At, t1)
			}
			if !strings.Contains(persistedAlertBlob(t, w.paths, w.id), "hc_unavailable") {
				t.Fatal("state.json did not persist hc_unavailable episode")
			}
			if st := w.state(); st.Project.RunID != coordAddr || st.Project.Run == nil || st.Project.Run.State == flow.PhaseEnded {
				t.Fatalf("hc outage marked live coordinator lost/ended: RunID=%q Run=%+v", st.Project.RunID, st.Project.Run)
			}
			ep1 := len(alertEpisodeLogs(t, w.paths, w.repo, "hc_unavailable"))
			if ep1 != 1 {
				t.Fatalf("expected 1 logged raise for hc_unavailable episode 1, got %d", ep1)
			}

			w.advance(time.Minute)
			w.pass()
			w.restart()
			w.advance(time.Minute)
			w.pass()
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "hc_unavailable")); got != ep1 {
				t.Fatalf("hc_unavailable repeated across passes/restart: got %d, want %d", got, ep1)
			}

			// First good hc observation clears hc_unavailable.
			w.hcOnline.Store(true)
			w.advance(time.Minute)
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "hc_unavailable"); active {
				t.Fatalf("hc_unavailable did not clear on first good hc observation: %+v", ts)
			}
			afterClear1 := len(alertEpisodeLogs(t, w.paths, w.repo, "hc_unavailable"))

			// Second hc outage raises a new episode (including when zero runs are live!).
			w.srv.FinishExit(coordAddr, "exited", 0)
			w.pass() // coordinator ended, now in J6 gap with RunID == ""
			t2 := w.advance(time.Minute)
			w.hcOnline.Store(false)
			w.pass()
			ts2, active := statusAlert(t, w.paths, w.repo, "hc_unavailable")
			if !active {
				t.Fatal("expected second hc_unavailable episode even with no live runs")
			}
			if !ts2.At.IsZero() && !ts2.At.Equal(t2) {
				t.Fatalf("hc_unavailable episode 2 At = %v, want %v", ts2.At, t2)
			}
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "hc_unavailable")); got <= afterClear1 {
				t.Fatalf("second hc_unavailable outage did not log a new episode: got %d", got)
			}

			// And clears on first good hc observation even with no live runs!
			w.hcOnline.Store(true)
			w.advance(time.Minute)
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "hc_unavailable"); active {
				t.Fatalf("hc_unavailable did not clear on good hc observation with no live runs: %+v", ts)
			}
		})
	})
}
