package factory

// PR 5b.5, TDD step 1: Codex as a worker and as a coordinator. Every test
// drives the engine through its existing entry points (passProject, Report,
// the fake herdr-core server) and states what a user sees: the argv, the
// environment and the prompt of the launched terminal, the run record, and
// what the engine sends or does not send. Codex gives herdr-core no status
// (spike S0.3), so a turn's end is read from the screen: the composer line
// "› " with no "esc to interrupt" working line above it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

const (
	rolesSessionA = "01a0c540-e202-74a3-9439-8ec094855a8a"
	rolesSessionB = "01a0c541-0000-7000-8000-000000000001"
)

var codexRolesT0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// codexWorkingScreen is a Codex terminal in the middle of a turn: the working
// line and, below it, the composer (Codex draws the composer during a turn).
func codexWorkingScreen(echo string) string {
	return "› " + echo + "\n\n• Working (12s • esc to interrupt)\n\n› Steer the current task\n\n  gpt-6.2-sol high · 97% left\n"
}

// codexIdleScreen is the same terminal after the turn: "Worked for", then the
// composer with nothing after the turn's reply. The duration varies per turn.
func codexIdleScreen(echo string, secs int) string {
	return "› " + echo + "\n\n• Done. I changed the files you asked for.\n\n  Worked for " + fmt.Sprint(secs) + "s • 8:44 PM\n\n› Ask Codex to do anything\n\n  gpt-6.2-sol high · 96% left\n"
}

type codexRoles struct {
	t     *testing.T
	srv   *hctest.Server
	cl    *hc.Client
	eng   *Engine
	paths Paths
	id    string
	repo  string
	gh    string
	runs  *[]hc.RunParams
	now   *time.Time
	logs  *bytes.Buffer
	home  string // the engine's scoped Codex home
}

func (c *codexRoles) last() hc.RunParams { return (*c.runs)[len(*c.runs)-1] }
func (c *codexRoles) pass()              { c.eng.passProject(context.Background(), c.id, false) }
func (c *codexRoles) state() flow.State {
	c.t.Helper()
	s, err := LoadState(c.paths.State(c.id))
	if err != nil {
		c.t.Fatal(err)
	}
	return s
}
func (c *codexRoles) edit(fn func(*flow.State)) {
	c.t.Helper()
	s := c.state()
	fn(&s)
	if err := SaveState(c.paths.State(c.id), s); err != nil {
		c.t.Fatal(err)
	}
}

// restart replaces the engine, as a daemon restart does: same data dir, same
// herdr-core, same clock, nothing in memory.
func (c *codexRoles) restart() {
	c.eng = newGitTestEngine(c.t, c.paths, c.cl)
	c.eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	c.eng.Now = func() time.Time { return *c.now }
}

// turnEnds advances the clock past idle_grace_seconds over three passes.
func (c *codexRoles) turnEnds() {
	c.t.Helper()
	c.pass()
	*c.now = c.now.Add(25 * time.Second)
	c.pass()
	c.pass()
}

// rollout writes a Codex session file the way Codex does, under the scoped
// home: sessions/Y/M/D/rollout-<time>-<uuid>.jsonl, first line session_meta.
func (c *codexRoles) rollout(id, cwd string, at time.Time, firstLine string) {
	c.t.Helper()
	dir := filepath.Join(c.home, "sessions", at.Format("2006"), at.Format("01"), at.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		c.t.Fatal(err)
	}
	if firstLine == "" {
		meta, _ := json.Marshal(map[string]any{"timestamp": at.Format(time.RFC3339Nano), "type": "session_meta",
			"payload": map[string]any{"session_id": id, "id": id, "timestamp": at.Format(time.RFC3339Nano), "cwd": cwd, "originator": "codex-tui", "source": "cli"}})
		firstLine = string(meta)
	}
	name := "rollout-" + at.Format("2006-01-02T15-04-05") + "-" + id + ".jsonl"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(firstLine+"\n{\"type\":\"event_msg\"}\n"), 0o600); err != nil {
		c.t.Fatal(err)
	}
}

func newCodexRoles(t *testing.T, models map[string][]flow.Candidate, signedIn bool) *codexRoles {
	t.Helper()
	codexHome := t.TempDir() // a fake sign-in: tests never touch the real ~/.codex
	if signedIn {
		if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", codexHome)
	c := &codexRoles{t: t, srv: hctest.New(t), paths: testPaths(t), runs: &[]hc.RunParams{}, logs: &bytes.Buffer{}}
	now := codexRolesT0
	c.now = &now
	c.srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { *c.runs = append(*c.runs, p) }
	c.cl = hc.NewClient(c.srv.Path)
	c.repo, _ = initGitRepoWithRemote(t)
	githubOrigin(t, c.repo)
	c.gh = installFakeGH(t)
	c.eng = newGitTestEngine(t, c.paths, c.cl)
	c.eng.Log = logging.NewJSONLLogger(c.logs)
	c.eng.Now = func() time.Time { return *c.now }
	if err := c.eng.Start(c.repo, "Codex roles", 2); err != nil {
		t.Fatal(err)
	}
	c.id = c.eng.projectDirs()[0].Name()
	c.home = filepath.Join(c.paths.DataDir, "factory", "codex-home")
	c.edit(func(s *flow.State) { s.Settings.Models = models })
	return c
}

var (
	codexWorkerOnly = map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "codex", Model: codexModel, Effort: "high"}}}
	codexCoordOnly  = map[string][]flow.Candidate{flow.RoleCoordinator: {{CLI: "codex", Model: codexModel, Effort: "high"}}}
)

// planAndStartWorker runs J7 (Claude coordinator), applies a one-task plan
// through the coordinator's own token, and passes N11 and N23. It returns the
// worker's terminal address.
func (c *codexRoles) planAndStartWorker() string {
	c.t.Helper()
	c.pass() // J7
	tok := (*c.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	plan := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: codex hello","brief":"Add hello.go with a Hello function.",` + testScopeJSON + `,"outcomes":[{"text":"hello exists","proved_by":"manual: hello.go"}]}]}`
	if err := c.eng.Report(tok, "plan apply", []byte(plan)); err != nil {
		c.t.Fatalf("plan apply: %v", err)
	}
	c.pass() // N11 worktree
	c.pass() // N23 worker
	st := c.state()
	if len(st.Tasks) != 1 || st.Tasks[0].Attempts[0].RunID == "" {
		c.t.Fatalf("worker not started: %+v", st.Tasks)
	}
	return st.Tasks[0].Attempts[0].RunID
}

// hasAll reports whether argv holds every element of want.
func hasAll(argv []string, want ...string) bool {
	for _, w := range want {
		if !slices.Contains(argv, w) {
			return false
		}
	}
	return true
}

// assertCodexUnattended checks the per-run prompt prevention and the stated
// model, effort and access of a full-permission Codex run in cwd (design.md
// 9.2: trust, hooks and update prompts are answered with -c overrides that
// write nothing to the owner's config; KB-LCH-2: the permission mode is
// stated, never the CLI's default).
func assertCodexUnattended(t *testing.T, role string, p hc.RunParams, cwd, effort string) {
	t.Helper()
	argv := p.Argv
	if len(argv) == 0 || filepath.Base(argv[0]) != "codex" {
		t.Fatalf("%s argv = %q, want a codex start", role, argv)
	}
	if !hasAll(argv, "--no-daemon", "--no-alt-screen") {
		t.Errorf("%s argv lacks --no-daemon or --no-alt-screen (own app server, scrollback for hc read): %q", role, argv)
	}
	if i := slices.Index(argv, "-m"); i < 0 || argv[i+1] != codexModel {
		t.Errorf("%s argv does not state -m %s: %q", role, codexModel, argv)
	}
	if !slices.Contains(argv, `model_reasoning_effort="`+effort+`"`) {
		t.Errorf("%s argv does not state the effort %q: %q", role, effort, argv)
	}
	// Full access in its worktree or folder, and never a prompt: either the
	// bypass flag, or an explicit danger-full-access sandbox with approvals off.
	bypass := slices.Contains(argv, "--dangerously-bypass-approvals-and-sandbox")
	explicit := argValue(argv, "-s") == "danger-full-access" && argValue(argv, "-a") == "never"
	if !bypass && !explicit {
		t.Errorf("%s argv does not state full access (bypass flag, or -s danger-full-access -a never): %q", role, argv)
	}
	if slices.Contains(argv, "read-only") || slices.Contains(argv, "on-request") {
		t.Errorf("%s argv carries a reviewer's sandbox or an approval prompt: %q", role, argv)
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := json.Marshal(real)
	if !slices.ContainsFunc(argv, func(a string) bool {
		return strings.HasPrefix(a, "projects={") && strings.Contains(a, string(key)+"={trust_level=\"trusted\"}")
	}) {
		t.Errorf("%s argv does not trust the resolved folder %s per run: %q", role, real, argv)
	}
	for _, a := range []string{"features.hooks=false", "check_for_update_on_startup=false"} {
		if !slices.Contains(argv, a) {
			t.Errorf("%s argv lacks -c %s: %q", role, a, argv)
		}
	}
	if !slices.Contains(argv, "shell_environment_policy.ignore_default_excludes=true") {
		// Codex strips env vars named like *TOKEN* from its shell commands by
		// default, which would hide SHEEPDOG_RUN_TOKEN from `sheepdog factory
		// report ready`. (From Codex's documented default; verify live.)
		t.Errorf("%s argv lacks -c shell_environment_policy.ignore_default_excludes=true, so the run token never reaches its report command: %q", role, argv)
	}
	if p.Env["SHEEPDOG_RUN_TOKEN"] == "" || p.Env["HC_SOCKET"] == "" {
		t.Errorf("%s env lacks the run token or HC_SOCKET: %v", role, mapKeys(p.Env))
	}
	if p.Cwd != cwd {
		t.Errorf("%s cwd = %q, want %q", role, p.Cwd, cwd)
	}
}

func mapKeys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// TestPlanApplyAcceptsCodexWorker: a task whose worker resolves to Codex is
// accepted, since the engine can now launch it; one that resolves to agy is
// still refused (agy launch is a later slice).
func TestPlanApplyAcceptsCodexWorker(t *testing.T) {
	newCoord := func(s flow.Settings) (*Engine, string, string) {
		eng := newGitTestEngine(t, testPaths(t), nil)
		st := flow.State{Seq: 1, Settings: s, Project: flow.Project{ID: "p1", Started: true, RunTokenHash: tokenHash("tok")}}
		if err := SaveState(eng.Paths.State("p1"), st); err != nil {
			t.Fatal(err)
		}
		return eng, "p1", "tok"
	}
	plan := json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",` + testPlanJSON + `}]}`)

	s := flow.DefaultSettings()
	s.Models = codexWorkerOnly
	eng, id, tok := newCoord(s)
	if err := eng.Report(tok, "plan apply", plan); err != nil {
		t.Fatalf("a Codex worker plan was refused: %v", err)
	}
	if st, err := LoadState(eng.Paths.State(id)); err != nil || len(st.Tasks) != 1 {
		t.Fatalf("stored tasks = %+v, err = %v", st.Tasks, err)
	}

	// Per task, through request: same answer.
	eng, id, tok = newCoord(flow.DefaultSettings())
	req := json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",` + testPlanJSON + `,"request":{"worker":[{"cli":"codex","model":"` + codexModel + `","effort":"high"}]}}]}`)
	if err := eng.Report(tok, "plan apply", req); err != nil {
		t.Fatalf("a task asking for a Codex worker was refused: %v", err)
	}
	_ = id

	// agy is still not launchable as a worker.
	s = flow.DefaultSettings()
	s.Models = map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "agy", Model: "gemini-4-neon-medium", Effort: "medium"}}}
	eng, _, tok = newCoord(s)
	err := eng.Report(tok, "plan apply", plan)
	if refusalCode(err) != "plan_invalid" || !strings.Contains(err.Error(), "the engine cannot launch agy as worker yet") {
		t.Fatalf("agy worker: err = %v", err)
	}
}

// TestCodexWorkerLaunch: a Codex worker starts in its worktree with model and
// effort stated, full access, no prompt the owner would have to answer, its
// home scoped as the reviewer's is, its brief as the positional prompt, and a
// run record that names Codex.
func TestCodexWorkerLaunch(t *testing.T) {
	c := newCodexRoles(t, codexWorkerOnly, true)
	w := c.planAndStartWorker()
	p := c.last()
	st := c.state()
	a := st.Tasks[0].Attempts[0]
	assertCodexUnattended(t, "worker", p, a.Worktree, "high")
	if p.Labels["sheepdog.role"] != flow.RoleWorker || p.Labels["sheepdog.model"] != codexModel || p.Labels["sheepdog.effort"] != "high" {
		t.Errorf("labels = %v", p.Labels)
	}
	if p.Env["CODEX_HOME"] != c.home {
		t.Errorf("CODEX_HOME = %q, want the scoped home %q", p.Env["CODEX_HOME"], c.home)
	}
	if link, err := os.Readlink(filepath.Join(c.home, "auth.json")); err != nil || link != filepath.Join(os.Getenv("CODEX_HOME"), "auth.json") {
		t.Errorf("scoped home holds %q (%v), want only a link to the owner's auth.json", link, err)
	}
	prompt := p.Argv[len(p.Argv)-1]
	for _, want := range []string{"T1", "Add hello.go with a Hello function.", "factory report ready", a.Worktree} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt (last argv element) lacks %q:\n%s", want, prompt)
		}
	}
	if a.Run == nil || a.Run.CLI != "codex" || a.Run.Model != codexModel || a.RunID != w {
		t.Errorf("run record = %+v, runID %q", a.Run, a.RunID)
	}
	if slices.Contains(p.Argv, "--session-id") || slices.Contains(p.Argv, "--permission-mode") {
		t.Errorf("Claude flags on a Codex worker: %q", p.Argv)
	}
}

// TestCodexWorkerReadyAndMerge: the plan row's demo as one history. A Codex
// worker works (its screen carries a working line), its turn ends, ready is
// accepted through its own token, the PR is pushed, two reviewers pass, and
// the task merges and finishes.
func TestCodexWorkerReadyAndMerge(t *testing.T) {
	c := newCodexRoles(t, codexWorkerOnly, true)
	w := c.planAndStartWorker()
	st := c.state()
	wt, tok := st.Tasks[0].Attempts[0].Worktree, c.last().Env["SHEEPDOG_RUN_TOKEN"]

	c.srv.SetScreen(w, codexWorkingScreen("Add hello.go"))
	*c.now = c.now.Add(5 * time.Second)
	c.pass()
	if got := c.state().Tasks[0].Attempts[0].Run.State; got != flow.PhaseWorking {
		t.Fatalf("run state while the working line shows = %s, want working", got)
	}
	if err := os.WriteFile(filepath.Join(wt, "hello.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "hello.go")
	gitIn(t, wt, "commit", "-m", "feat: add hello")
	c1 := gitIn(t, wt, "rev-parse", "HEAD")
	writePRJSON(t, c.gh, "pr.json", c1, greenRollup)
	if err := c.eng.Report(tok, "ready", []byte(`{"head":"`+c1+`","proofs":[{"outcome":"hello exists","artifact":"hello.go"}],"touched":"hello.go","skipped":""}`)); err != nil {
		t.Fatalf("ready from a Codex worker: %v", err)
	}
	c.srv.SetScreen(w, codexIdleScreen("Add hello.go", 31))
	c.turnEnds()
	if got := c.state().Tasks[0].Attempts[0].Run.State; got != flow.PhaseIdle {
		t.Fatalf("run state after the turn ended = %s, want idle (no hc status exists for Codex)", got)
	}
	c.pass() // N26 pushes and records PR #7
	writePRJSON(t, c.gh, "view.json", c1, greenRollup)
	c.pass() // N29 round 1 reviewer
	for round := 1; round <= 2; round++ {
		rv := c.state().Tasks[0].Attempts[0].Review
		if rv == nil || rv.Round != round {
			t.Fatalf("round %d review = %+v", round, rv)
		}
		c.srv.SetScreen(rv.RunID, "❯ prompt\n\n⏺ Looks good.\nself-check misses: 0\ndeletable: none\n<report>{\"code\":\""+rv.Code+"\",\"round\":"+jsonNumber(round)+",\"head\":\""+c1+"\",\"verdict\":\"pass\",\"majors\":0}</report>\n")
		c.srv.SetStatus(rv.RunID, &hc.Status{App: "claude-code", State: "done"})
		c.pass()
		*c.now = c.now.Add(25 * time.Second)
		c.pass()
		if round == 1 {
			c.pass() // N29 starts round 2
		}
	}
	c.pass() // N42 asks merge
	merged := `[{"number":7,"state":"MERGED","headRefOid":"` + c1 + `","baseRefName":"main","isCrossRepository":false,"mergeCommit":{"oid":"merged99"},"mergeStateStatus":"CLEAN","reviewDecision":"","statusCheckRollup":` + greenRollup + `}]`
	writeGHFile(t, c.gh, "pr.json", merged)
	writeGHFile(t, c.gh, "view.json", merged[1:len(merged)-1])
	c.pass() // N2 done
	c.pass() // N1 cleanup
	if d := c.state().Tasks[0].Done; d == nil || d.MergedCommit != "merged99" {
		t.Fatalf("task did not finish merged: %+v", c.state().Tasks[0])
	}
}

// TestCodexTurnEndIsNotGuessed: unknown is not known. A screen that shows the
// working line together with the composer, an empty screen, and a screen that
// only quotes a composer line inside a tool block never read as a finished
// turn, so held text is not typed into a busy Codex.
func TestCodexTurnEndIsNotGuessed(t *testing.T) {
	c := newCodexRoles(t, codexWorkerOnly, true)
	w := c.planAndStartWorker()
	c.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Run.Held = []string{"Rebase on main when done."} })
	for name, screen := range map[string]string{
		"working line with composer": codexWorkingScreen("Add hello.go"),
		"empty screen":               "",
		"quoted composer in output":  "› Add hello.go\n\n• Explored\n  └ Read notes.md\n    › Ask Codex to do anything\n\n• Working (40s • esc to interrupt)\n",
	} {
		c.srv.SetScreen(w, screen)
		c.turnEnds()
		if n := len(sendsTo(t, c.srv.Requests(), w)); n != 0 {
			t.Fatalf("%s: %d sends to a worker whose turn has not ended", name, n)
		}
		if got := c.state().Tasks[0].Attempts[0].Run.State; got == flow.PhaseIdle {
			t.Fatalf("%s: run read as idle", name)
		}
		*c.now = c.now.Add(time.Minute)
	}
}

// TestCodexWorkerSessionFromRollout: the engine cannot choose a Codex session
// id, so it reads it from Codex's own session file in the scoped home: the
// rollout that began after this run started, in this run's folder. Anything
// else leaves the id unknown (and a restart then uses a handoff, never
// `resume --last`, which picks by folder only).
func TestCodexWorkerSessionFromRollout(t *testing.T) {
	cases := []struct {
		name  string
		write func(c *codexRoles, cwd string)
		want  string
	}{
		{"one rollout in the worktree after the start", func(c *codexRoles, cwd string) {
			c.rollout(rolesSessionA, cwd, codexRolesT0.Add(3*time.Second), "")
		}, rolesSessionA},
		{"a rollout older than the run is ignored", func(c *codexRoles, cwd string) {
			c.rollout(rolesSessionA, cwd, codexRolesT0.Add(-time.Hour), "")
		}, ""},
		{"another folder is ignored", func(c *codexRoles, cwd string) {
			c.rollout(rolesSessionA, filepath.Join(cwd, "elsewhere"), codexRolesT0.Add(3*time.Second), "")
		}, ""},
		{"two sessions in the folder are ambiguous", func(c *codexRoles, cwd string) {
			c.rollout(rolesSessionA, cwd, codexRolesT0.Add(3*time.Second), "")
			c.rollout(rolesSessionB, cwd, codexRolesT0.Add(4*time.Second), "")
		}, ""},
		{"an unreadable first line gives no id", func(c *codexRoles, cwd string) {
			c.rollout(rolesSessionA, cwd, codexRolesT0.Add(3*time.Second), `{"type":"session_meta","payload":{"id":`)
		}, ""},
		{"a first line that is not session_meta gives no id", func(c *codexRoles, cwd string) {
			c.rollout(rolesSessionA, cwd, codexRolesT0.Add(3*time.Second), `{"type":"event_msg","payload":{"id":"`+rolesSessionA+`","cwd":"`+cwd+`"}}`)
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCodexRoles(t, codexWorkerOnly, true)
			w := c.planAndStartWorker()
			real, _ := filepath.EvalSymlinks(c.state().Tasks[0].Attempts[0].Worktree)
			tc.write(c, real)
			c.srv.SetScreen(w, codexWorkingScreen("Add hello.go"))
			*c.now = c.now.Add(10 * time.Second)
			c.pass()
			c.pass()
			r := c.state().Tasks[0].Attempts[0].Run
			if r.SessionID != tc.want || r.Resumable != (tc.want != "") {
				t.Fatalf("SessionID = %q, Resumable = %v, want %q", r.SessionID, r.Resumable, tc.want)
			}
		})
	}
}

// restartAfterCrash kills the worker, passes the 1 minute backoff and returns
// the argv of the relaunch.
func (c *codexRoles) restartAfterCrash(w string, held ...string) hc.RunParams {
	c.t.Helper()
	c.srv.Finish(w, "killed")
	*c.now = c.now.Add(5 * time.Second)
	c.pass() // ended, waiting on backoff
	if len(held) > 0 {
		c.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Held = held })
	}
	n := len(*c.runs)
	*c.now = c.now.Add(time.Minute)
	c.pass() // N23 restart
	if len(*c.runs) != n+1 {
		c.t.Fatalf("the worker was not restarted: %d runs, was %d", len(*c.runs), n)
	}
	return c.last()
}

// TestCodexWorkerResumesItsOwnSession: after a crash the worker is continued in
// the same Codex session: `codex resume <id>`, model, effort, full access and
// the same scoped home stated again, the held instruction as the new turn's
// prompt, and never `--last`.
func TestCodexWorkerResumesItsOwnSession(t *testing.T) {
	c := newCodexRoles(t, codexWorkerOnly, true)
	w := c.planAndStartWorker()
	wt := c.state().Tasks[0].Attempts[0].Worktree
	real, _ := filepath.EvalSymlinks(wt)
	c.rollout(rolesSessionA, real, codexRolesT0.Add(3*time.Second), "")
	c.srv.SetScreen(w, codexWorkingScreen("Add hello.go"))
	*c.now = c.now.Add(10 * time.Second)
	c.pass()
	if r := c.state().Tasks[0].Attempts[0].Run; r.SessionID != rolesSessionA {
		t.Fatalf("session not recorded before the crash: %+v", r)
	}
	p := c.restartAfterCrash(w, "Also add a doc comment.")
	argv := p.Argv
	i := slices.Index(argv, "resume")
	if i < 1 || i+1 >= len(argv) || !slices.Contains(argv[i+1:], rolesSessionA) {
		t.Fatalf("restart argv has no `resume %s`: %q", rolesSessionA, argv)
	}
	if slices.Contains(argv, "--last") || slices.Contains(argv, "--all") {
		t.Errorf("a resume that picks by folder: %q", argv)
	}
	assertCodexUnattended(t, "resumed worker", p, wt, "high")
	if p.Env["CODEX_HOME"] != c.home {
		t.Errorf("resume CODEX_HOME = %q, want %q (the session lives there)", p.Env["CODEX_HOME"], c.home)
	}
	if got := argv[len(argv)-1]; !strings.Contains(got, "Also add a doc comment.") || strings.Contains(got, "Handoff from an earlier worker run") {
		t.Errorf("resume prompt = %q, want the held instruction and no handoff", got)
	}
	a := c.state().Tasks[0].Attempts[0]
	if a.Restarts != 1 || !a.Run.Resumed || a.Run.SessionID != rolesSessionA {
		t.Errorf("after resume: restarts %d, run %+v", a.Restarts, a.Run)
	}
}

// TestCodexWorkerUnknownSessionGetsHandoff: with no readable session id the
// restart is a fresh Codex session with the handoff note, never a resume.
func TestCodexWorkerUnknownSessionGetsHandoff(t *testing.T) {
	c := newCodexRoles(t, codexWorkerOnly, true)
	w := c.planAndStartWorker()
	c.srv.SetScreen(w, codexWorkingScreen("Add hello.go"))
	*c.now = c.now.Add(10 * time.Second)
	c.pass()
	p := c.restartAfterCrash(w)
	if slices.Contains(p.Argv, "resume") || slices.Contains(p.Argv, "--last") {
		t.Fatalf("restart with an unknown session resumed: %q", p.Argv)
	}
	if got := p.Argv[len(p.Argv)-1]; !strings.Contains(got, "Handoff from an earlier worker run:") {
		t.Errorf("fresh restart prompt lacks the handoff note:\n%s", got)
	}
	assertCodexUnattended(t, "restarted worker", p, c.state().Tasks[0].Attempts[0].Worktree, "high")
}

// TestCodexWorkerAdoptedAfterRestart: a live Codex worker survives a daemon
// restart: no second terminal, same record and session, and an instruction
// held across the restart is typed into the same terminal once its turn ends
// (exactly once), not while it works.
func TestCodexWorkerAdoptedAfterRestart(t *testing.T) {
	c := newCodexRoles(t, codexWorkerOnly, true)
	w := c.planAndStartWorker()
	real, _ := filepath.EvalSymlinks(c.state().Tasks[0].Attempts[0].Worktree)
	c.rollout(rolesSessionA, real, codexRolesT0.Add(3*time.Second), "")
	c.srv.SetScreen(w, codexWorkingScreen("Add hello.go"))
	*c.now = c.now.Add(10 * time.Second)
	c.pass()
	c.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Run.Held = []string{"Rebase on main when done."} })
	runs := len(*c.runs)

	c.restart()
	c.pass()
	c.pass()
	if n := len(sendsTo(t, c.srv.Requests(), w)); n != 0 {
		t.Fatalf("held text typed while Codex works: %d sends", n)
	}
	c.srv.SetScreen(w, codexIdleScreen("Add hello.go", 44))
	c.turnEnds()
	sends := sendsTo(t, c.srv.Requests(), w)
	if len(sends) != 1 || !strings.Contains(sends[0], "Rebase on main when done.") {
		t.Fatalf("sends to the adopted worker = %q, want the held text once", sends)
	}
	c.pass()
	if len(*c.runs) != runs || len(sendsTo(t, c.srv.Requests(), w)) != 1 {
		t.Fatalf("after adoption: %d runs (was %d), %d sends", len(*c.runs), runs, len(sendsTo(t, c.srv.Requests(), w)))
	}
	a := c.state().Tasks[0].Attempts[0]
	if a.RunID != w || a.Run.CLI != "codex" || a.Run.SessionID != rolesSessionA || len(a.Run.Held) != 0 {
		t.Fatalf("adopted worker = runID %q run %+v", a.RunID, a.Run)
	}
}

// TestCodexCoordinatorLaunchWakeAndSecondTurn: a Codex coordinator starts in the
// repo folder with its goal as the prompt; a wake is held while its turn runs,
// typed as a new turn when the first turn ends (and counted as a turn), is
// not followed by a second wake on a stale screen, and a second wake is
// delivered after the wake's own turn ends, across a daemon restart.
func TestCodexCoordinatorLaunchWakeAndSecondTurn(t *testing.T) {
	c := newCodexRoles(t, codexCoordOnly, true)
	c.pass() // J7
	st := c.state()
	if len(*c.runs) != 1 || st.Project.RunID == "" {
		t.Fatalf("coordinator not started: %d runs, runID %q", len(*c.runs), st.Project.RunID)
	}
	co, p := st.Project.RunID, c.last()
	assertCodexUnattended(t, "coordinator", p, st.Project.Folder, "high")
	if p.Labels["sheepdog.role"] != flow.RoleCoordinator || p.Env["CODEX_HOME"] != c.home {
		t.Errorf("labels %v, CODEX_HOME %q", p.Labels, p.Env["CODEX_HOME"])
	}
	if prompt := p.Argv[len(p.Argv)-1]; !strings.Contains(prompt, "Codex roles") || !strings.Contains(prompt, "plan apply") {
		t.Errorf("coordinator prompt lacks the goal or its commands:\n%s", prompt)
	}
	if st.Project.Run == nil || st.Project.Run.CLI != "codex" || st.Project.Run.Model != codexModel {
		t.Fatalf("coordinator run record = %+v", st.Project.Run)
	}
	wake := func(key string) {
		c.edit(func(s *flow.State) {
			s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: key, Task: "T1", Why: "N23: the worker is working", Facts: `title "feat: x"`, At: *c.now})
		})
	}
	wake("worker_failed")

	// First turn running: the wake stays held.
	c.srv.SetScreen(co, codexWorkingScreen("You are the coordinator"))
	c.turnEnds()
	if n := len(sendsTo(t, c.srv.Requests(), co)); n != 0 {
		t.Fatalf("wake typed into a working coordinator: %d sends", n)
	}
	// First turn ends: one wake, one turn counted.
	c.srv.SetScreen(co, codexIdleScreen("You are the coordinator", 20))
	c.turnEnds()
	sends := sendsTo(t, c.srv.Requests(), co)
	if len(sends) != 1 || !strings.Contains(sends[0], "Wake:") || !strings.Contains(sends[0], "worker_failed") {
		t.Fatalf("sends = %q, want one wake", sends)
	}
	s1 := c.state()
	if len(s1.Project.Wakes) != 0 || s1.Project.Run.Turn < 1 {
		t.Fatalf("after delivery: wakes %d, turn %d", len(s1.Project.Wakes), s1.Project.Run.Turn)
	}
	turn1 := s1.Project.Run.Turn

	// A second wake while the screen still shows the finished first turn: no
	// evidence yet that the wake's turn ran, so it stays held.
	wake("main_red")
	c.turnEnds()
	if n := len(sendsTo(t, c.srv.Requests(), co)); n != 1 {
		t.Fatalf("second wake typed on a stale screen: %d sends", n)
	}

	// The daemon restarts mid-turn; the wake's turn runs and ends.
	c.restart()
	c.srv.SetScreen(co, codexWorkingScreen("Wake: 1 item(s) need you."))
	c.turnEnds()
	if n := len(sendsTo(t, c.srv.Requests(), co)); n != 1 {
		t.Fatalf("wake typed while the wake's turn runs: %d sends", n)
	}
	if len(*c.runs) != 1 {
		t.Fatalf("restart relaunched the coordinator: %d runs", len(*c.runs))
	}
	c.srv.SetScreen(co, codexIdleScreen("Wake: 1 item(s) need you.", 17))
	c.turnEnds()
	sends = sendsTo(t, c.srv.Requests(), co)
	if len(sends) != 2 || !strings.Contains(sends[1], "main_red") {
		t.Fatalf("sends = %q, want the second wake after the second turn", sends)
	}
	if got := c.state().Project.Run.Turn; got <= turn1 {
		t.Fatalf("turn count after the wake's turn = %d, was %d", got, turn1)
	}
}

// TestCodexCoordinatorRestartedFresh: a Codex coordinator whose terminal is
// gone is replaced (after coordinator_gap_minutes) by a fresh Codex session
// with the goal prompt (no tasks yet, so no handoff), and the held wake
// follows once that session is idle. A coordinator's session is never
// resumed by guesswork.
func TestCodexCoordinatorRestartedFresh(t *testing.T) {
	c := newCodexRoles(t, codexCoordOnly, true)
	c.pass() // J7
	co := c.state().Project.RunID
	c.srv.SetScreen(co, codexWorkingScreen("You are the coordinator"))
	c.pass()
	c.edit(func(s *flow.State) {
		s.Project.Wakes = []flow.Wake{{Key: "worker_failed", Task: "T1", Why: "N23: waiting", Facts: "f", At: *c.now}}
	})
	c.srv.Finish(co, "killed")
	for range 3 {
		*c.now = c.now.Add(11 * time.Minute)
		c.pass()
		if len(*c.runs) == 2 {
			break // stop before the new terminal's blank screen times out
		}
	}
	if len(*c.runs) != 2 {
		t.Fatalf("coordinator not replaced: %d runs", len(*c.runs))
	}
	p := c.last()
	assertCodexUnattended(t, "replacement coordinator", p, c.state().Project.Folder, "high")
	if slices.Contains(p.Argv, "resume") || slices.Contains(p.Argv, "--last") || !strings.Contains(p.Argv[len(p.Argv)-1], "Codex roles") {
		t.Fatalf("replacement argv = %q, want a fresh session with the goal prompt", p.Argv)
	}
	co2 := c.state().Project.RunID
	if co2 == "" || co2 == co {
		t.Fatalf("replacement address = %q", co2)
	}
	c.srv.SetScreen(co2, codexIdleScreen("You are the coordinator", 9))
	c.turnEnds()
	if sends := sendsTo(t, c.srv.Requests(), co2); len(sends) != 1 || !strings.Contains(sends[0], "worker_failed") {
		t.Fatalf("held wake after the replacement's first turn: %q", sends)
	}
}

// TestCodexLaunchWaitsAreLoggedOnce: with Codex the only candidate and no
// Codex sign-in, neither role starts, nothing falls back to Claude, and the
// wait is logged once with its reason.
func TestCodexLaunchWaitsAreLoggedOnce(t *testing.T) {
	t.Run("coordinator", func(t *testing.T) {
		c := newCodexRoles(t, codexCoordOnly, false)
		for range 3 {
			c.pass()
		}
		if len(*c.runs) != 0 {
			t.Fatalf("a coordinator started without a Codex sign-in: %q", c.last().Argv)
		}
		var waits []LogLine
		for _, l := range logRules(t, c.paths.Log(c.id), "J7") {
			if l.Decision == "waiting" {
				waits = append(waits, l)
			}
		}
		if len(waits) != 1 || !strings.Contains(strings.ToLower(waits[0].Why), "not signed in") {
			t.Fatalf("J7 waits = %+v, want one that says Codex is not signed in", waits)
		}
	})
	t.Run("worker", func(t *testing.T) {
		c := newCodexRoles(t, codexWorkerOnly, false)
		c.pass() // J7 (Claude coordinator)
		tok := (*c.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
		if err := c.eng.Report(tok, "plan apply", []byte(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`}]}`)); err != nil {
			t.Fatalf("plan apply: %v", err)
		}
		for range 4 {
			c.pass()
		}
		st := c.state()
		if len(*c.runs) != 1 || st.Tasks[0].Attempts[0].RunID != "" || !strings.Contains(strings.ToLower(st.Tasks[0].HoldWhy), "not signed in") {
			t.Fatalf("runs %d, runID %q, hold %q", len(*c.runs), st.Tasks[0].Attempts[0].RunID, st.Tasks[0].HoldWhy)
		}
		n := 0
		for _, l := range c.logLinesWaiting() {
			if strings.Contains(strings.ToLower(l.Why), "not signed in") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("logged %d waits with the sign-in reason, want 1", n)
		}
	})
}

func (c *codexRoles) logLinesWaiting() []LogLine {
	raw, _ := os.ReadFile(c.paths.Log(c.id))
	var out []LogLine
	for ln := range strings.Lines(string(raw)) {
		var l LogLine
		if json.Unmarshal([]byte(ln), &l) == nil && l.Decision == "waiting" {
			out = append(out, l)
		}
	}
	return out
}
