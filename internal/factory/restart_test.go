package factory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

type restartWorld struct {
	t        *testing.T
	ctx      context.Context
	srv      *hctest.Server
	client   *hc.Client
	paths    Paths
	repo     string
	gh       string
	eng      *Engine
	id       string
	now      time.Time
	trimNext bool

	coordTok string
	workTok  string
	workerID string
	commits  []string
	merged   string
	runs     []hc.RunParams
}

func installParallelFakeGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := strings.Replace(fakeGHScript, "D=%DIR%", `D="$(git config --get sheepdog.fakeGhDir)"`, 1)
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	orig := os.Getenv("PATH")
	if err := os.Setenv("PATH", bin+string(os.PathListSeparator)+orig); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", orig) })
}

func newRestartWorld(t *testing.T, goal string) *restartWorld {
	t.Helper()
	srv := hctest.New(t)
	repo, _ := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := t.TempDir()
	gitIn(t, repo, "config", "--local", "sheepdog.fakeGhDir", gh)
	w := &restartWorld{
		t:      t,
		ctx:    context.Background(),
		srv:    srv,
		client: hc.NewClient(srv.Path),
		paths:  testPaths(t),
		repo:   repo,
		gh:     gh,
		now:    time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}
	srv.OnRun = func(term hc.Terminal, p hc.RunParams) {
		w.runs = append(w.runs, p)
		switch p.Labels["sheepdog.role"] {
		case flow.RoleCoordinator:
			w.coordTok = p.Env["SHEEPDOG_RUN_TOKEN"]
		case flow.RoleWorker:
			w.workTok = p.Env["SHEEPDOG_RUN_TOKEN"]
			w.workerID = term.Address
		}
	}
	w.newEngine()
	if err := w.eng.Start(repo, goal, 2); err != nil {
		t.Fatal(err)
	}
	w.id = w.eng.projectDirs()[0].Name()
	return w
}

func (w *restartWorld) newEngine() {
	w.eng = New(w.paths, w.client)
	w.eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	w.eng.Now = func() time.Time { return w.now }
	w.eng.onInvariant = func(vs []flow.InvariantViolation) {
		w.t.Helper()
		w.t.Fatalf("invariant broken after pass: %+v", vs)
	}
}

func (w *restartWorld) restart() {
	w.newEngine()
	w.trimNext = true
}

func (w *restartWorld) pass() {
	trim := w.trimNext
	w.trimNext = false
	w.eng.passProject(w.ctx, w.id, trim)
}

func (w *restartWorld) state() flow.State {
	w.t.Helper()
	s, err := LoadState(w.paths.State(w.id))
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

func (w *restartWorld) edit(fn func(*flow.State)) {
	w.t.Helper()
	s := w.state()
	fn(&s)
	if err := SaveState(w.paths.State(w.id), s); err != nil {
		w.t.Fatal(err)
	}
}

func (w *restartWorld) applyPlan() {
	w.t.Helper()
	st := w.state()
	w.srv.SetStatus(st.Project.RunID, &hc.Status{App: "claude-code", State: "working"})
	body := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: demo change","brief":"Add demo.go.",` + testScopeJSON + `,"outcomes":[{"text":"demo exists","proved_by":"manual: demo.go"}]}]}`
	if err := w.eng.Report(w.coordTok, "plan apply", []byte(body)); err != nil {
		w.t.Fatal(err)
	}
}

func (w *restartWorld) commitAndReady(content, rollup string, openPR bool) {
	w.t.Helper()
	st := w.state()
	wt := st.Tasks[0].Attempts[0].Worktree
	c := commitFile(w.t, wt, "demo.go", content)
	w.commits = append(w.commits, c)
	if openPR {
		writePRJSON(w.t, w.gh, "pr.json", c, rollup)
	}
	body := `{"head":"` + c + `","proofs":[{"outcome":"demo exists","artifact":"demo.go"}],"touched":"demo.go","skipped":""}`
	if err := w.eng.Report(w.workTok, "ready", []byte(body)); err != nil {
		w.t.Fatal(err)
	}
}

func (w *restartWorld) setReviewerVerdict(verdict string, majors int) {
	w.t.Helper()
	rv := w.state().Tasks[0].Attempts[0].Review
	if rv == nil || rv.RunID == "" {
		w.t.Fatalf("expected active reviewer, got %+v", rv)
	}
	var body strings.Builder
	body.WriteString("❯ prompt\n\n⏺ Review done.\n")
	for range majors {
		body.WriteString("major: fix issue\n")
	}
	fmt.Fprintf(&body, "self-check misses: 0\ndeletable: none\n<report>{\"code\":%q,\"round\":%d,\"head\":%q,\"verdict\":%q,\"majors\":%d}</report>\n",
		rv.Code, rv.Round, rv.Head, verdict, majors)
	w.srv.SetScreen(rv.RunID, body.String())
	w.srv.SetStatus(rv.RunID, &hc.Status{App: "claude-code", State: "done"})
}

func (w *restartWorld) markPRMerged(sha string) {
	w.t.Helper()
	w.merged = sha
	head := w.commits[len(w.commits)-1]
	body := `{"number":7,"state":"MERGED","headRefOid":"` + head + `","mergeable":"UNKNOWN","statusCheckRollup":` + greenRollup + `,"baseRefName":"main","mergeCommit":{"oid":"` + sha + `"}}`
	writeGHFile(w.t, w.gh, "view.json", body)
}

func (w *restartWorld) normalize(s string) string {
	for i, c := range w.commits {
		s = strings.ReplaceAll(s, c, "<c"+strconv.Itoa(i+1)+">")
	}
	return s
}

type restartEnd struct {
	TaskDone     string
	PRState      string
	PRCreates    int
	PRMerges     int
	CIReruns     int
	Rounds       []string
	Accepts      []string
	Restarts     int
	ProjectActs  []string
	TaskActs     []string
	AttemptActs  []string
	LiveTaskRuns int
	CoordAlive   bool
	WorktreeGone bool
}

func (w *restartWorld) summarize() restartEnd {
	w.t.Helper()
	st := w.state()
	t0 := st.Tasks[0]
	a0 := t0.Attempts[0]
	done := ""
	if t0.Done != nil {
		done = t0.Done.MergedCommit
	}
	normActs := func(acts []flow.Act) []string {
		seen := map[string]string{}
		for _, a := range acts {
			seen[w.normalize(a.Key)] = a.Result
		}
		var out []string
		for k, r := range seen {
			out = append(out, k+"="+r)
		}
		slices.Sort(out)
		return out
	}
	var rounds, accepts []string
	for _, r := range a0.Rounds {
		rounds = append(rounds, fmt.Sprintf("r%d:%s:%s", r.N, w.normalize(r.Head), r.Verdict))
	}
	for _, ac := range a0.Accepts {
		accepts = append(accepts, fmt.Sprintf("%s:%s:%v", w.normalize(ac.Head), ac.By, ac.Rounds))
	}
	list, err := w.client.List(w.ctx, false)
	if err != nil {
		w.t.Fatal(err)
	}
	liveTask, coordAlive := 0, false
	for _, tm := range list.Terminals {
		if tm.State != "live" {
			continue
		}
		if tm.Labels["sheepdog.role"] == flow.RoleCoordinator {
			coordAlive = true
		} else {
			liveTask++
		}
	}
	_, wtErr := os.Stat(a0.Worktree)
	prState := ""
	if raw, err := os.ReadFile(filepath.Join(w.gh, "view.json")); err == nil && strings.Contains(string(raw), `"MERGED"`) {
		prState = "MERGED"
	}
	return restartEnd{
		TaskDone:     done,
		PRState:      prState,
		PRCreates:    len(ghCalls(w.t, w.gh, "pr create")),
		PRMerges:     len(ghCalls(w.t, w.gh, "pr merge")),
		CIReruns:     len(ghCalls(w.t, w.gh, "run rerun")),
		Rounds:       rounds,
		Accepts:      accepts,
		Restarts:     a0.Restarts,
		ProjectActs:  normActs(st.Project.Acts),
		TaskActs:     normActs(t0.Acts),
		AttemptActs:  normActs(a0.Acts),
		LiveTaskRuns: liveTask,
		CoordAlive:   coordAlive,
		WorktreeGone: os.IsNotExist(wtErr),
	}
}

type restartStep struct {
	name string
	run  func(*restartWorld)
}

type restartHistory struct {
	name  string
	setup func(*testing.T) *restartWorld
	steps []restartStep
}

func restartHistories() []restartHistory {
	passStep := func(name string, advance time.Duration) restartStep {
		return restartStep{name, func(w *restartWorld) {
			w.now = w.now.Add(advance)
			w.pass()
		}}
	}
	return []restartHistory{
		{
			name:  "plan_to_merge_two_rounds",
			setup: func(t *testing.T) *restartWorld { return newRestartWorld(t, "Plan to merge with two review rounds") },
			steps: []restartStep{
				passStep("J7 start coord", 0),
				{"plan apply", func(w *restartWorld) { w.applyPlan() }},
				passStep("N11 new attempt", 0),
				passStep("N23 start worker", 0),
				{"worker idle R9", func(w *restartWorld) {
					w.srv.SetStatus(w.workerID, &hc.Status{App: "claude-code", State: "idle"})
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("worker idle R20", 25*time.Second),
				{"commit c1 and ready", func(w *restartWorld) { w.commitAndReady("package demo\n", greenRollup, true) }},
				passStep("N26 push and open PR", 0),
				passStep("N29 start round 1", 0),
				{"round 1 changes R9", func(w *restartWorld) {
					w.setReviewerVerdict(flow.VerdictChanges, 1)
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("round 1 changes verdict", 25*time.Second),
				passStep("N22 tell findings", 0),
				{"commit c2 and ready", func(w *restartWorld) { w.commitAndReady("package demo\n// fixed\n", greenRollup, false) }},
				{"N26 push c2", func(w *restartWorld) {
					w.pass()
					writePRJSON(w.t, w.gh, "view.json", w.commits[len(w.commits)-1], greenRollup)
				}},
				passStep("N29 start round 2", 0),
				{"round 2 pass R9", func(w *restartWorld) {
					w.setReviewerVerdict(flow.VerdictPass, 0)
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("round 2 pass verdict", 25*time.Second),
				passStep("N29 start round 3", 0),
				{"round 3 pass R9", func(w *restartWorld) {
					w.setReviewerVerdict(flow.VerdictPass, 0)
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("round 3 pass verdict", 25*time.Second),
				passStep("N42 accept and merge", 0),
				passStep("N41 wait while open", 15*time.Second),
				{"PR merged and N2 done", func(w *restartWorld) {
					w.markPRMerged("merged-h1")
					w.now = w.now.Add(15 * time.Second)
					w.pass()
				}},
				passStep("N1 cleanup", 15*time.Second),
			},
		},
		{
			name: "ci_red_then_green",
			setup: func(t *testing.T) *restartWorld {
				w := newRestartWorld(t, "CI red then green")
				w.pass()
				w.applyPlan()
				w.pass()
				w.pass()
				w.srv.SetStatus(w.workerID, &hc.Status{App: "claude-code", State: "idle"})
				w.now = w.now.Add(5 * time.Second)
				w.pass()
				w.now = w.now.Add(25 * time.Second)
				w.pass()
				red := checkRollup("FAILURE", "44714", "13621")
				w.commitAndReady("package demo\n", red, true)
				w.pass() // N26 pushes and opens PR #7 with red check
				w.edit(func(s *flow.State) {
					c1 := w.commits[0]
					s.Tasks[0].Attempts[0].Rounds = []flow.Round{
						{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
						{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					}
				})
				return w
			},
			steps: []restartStep{
				passStep("N33 rerun failed CI", 0),
				passStep("N33 wait stale red", 30*time.Second),
				{"CI turns green and N42 merges", func(w *restartWorld) {
					writePRJSON(w.t, w.gh, "view.json", w.commits[0], greenRollup)
					w.now = w.now.Add(30 * time.Second)
					w.pass()
				}},
				passStep("N41 wait while open", 15*time.Second),
				{"PR merged and N2 done", func(w *restartWorld) {
					w.markPRMerged("merged-h2")
					w.now = w.now.Add(15 * time.Second)
					w.pass()
				}},
				passStep("N1 cleanup", 15*time.Second),
			},
		},
		{
			name:  "worker_killed_and_restarted",
			setup: func(t *testing.T) *restartWorld { return newRestartWorld(t, "Worker killed and restarted") },
			steps: []restartStep{
				passStep("J7 start coord", 0),
				{"plan apply", func(w *restartWorld) { w.applyPlan() }},
				passStep("N11 new attempt", 0),
				passStep("N23 start worker", 0),
				{"worker working", func(w *restartWorld) {
					w.srv.SetStatus(w.workerID, &hc.Status{App: "claude-code", State: "working"})
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				{"kill worker mid-turn", func(w *restartWorld) {
					if _, err := w.client.Kill(w.ctx, w.workerID); err != nil {
						w.t.Fatal(err)
					}
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("wait in backoff", 30*time.Second),
				passStep("N23 resume worker", 35*time.Second),
				{"resumed worker idle R9", func(w *restartWorld) {
					w.srv.SetStatus(w.workerID, &hc.Status{App: "claude-code", State: "idle"})
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("resumed worker idle R20", 25*time.Second),
				{"commit c1 and ready", func(w *restartWorld) { w.commitAndReady("package demo\n", greenRollup, true) }},
				passStep("N26 push and open PR", 0),
				passStep("N29 start round 1", 0),
				{"round 1 pass R9", func(w *restartWorld) {
					w.setReviewerVerdict(flow.VerdictPass, 0)
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("round 1 pass verdict", 25*time.Second),
				passStep("N29 start round 2", 0),
				{"round 2 pass R9", func(w *restartWorld) {
					w.setReviewerVerdict(flow.VerdictPass, 0)
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("round 2 pass verdict", 25*time.Second),
				passStep("N42 accept and merge", 0),
				{"PR merged and N2 done", func(w *restartWorld) {
					w.markPRMerged("merged-h3")
					w.now = w.now.Add(15 * time.Second)
					w.pass()
				}},
				passStep("N1 cleanup", 15*time.Second),
			},
		},
		{
			name:  "coordinator_killed_and_replaced",
			setup: func(t *testing.T) *restartWorld { return newRestartWorld(t, "Coordinator killed and replaced") },
			steps: []restartStep{
				passStep("J7 start coord 1", 0),
				{"plan apply", func(w *restartWorld) { w.applyPlan() }},
				passStep("N11 new attempt", 0),
				passStep("N23 start worker", 0),
				{"worker working", func(w *restartWorld) {
					w.srv.SetStatus(w.workerID, &hc.Status{App: "claude-code", State: "working"})
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				{"kill coordinator 1", func(w *restartWorld) {
					if _, err := w.client.Kill(w.ctx, w.state().Project.RunID); err != nil {
						w.t.Fatal(err)
					}
					w.now = w.now.Add(2 * time.Minute)
					w.pass()
				}},
				passStep("J6 gap wait", 4*time.Minute),
				passStep("J7 replace coordinator", 5*time.Minute),
				{"worker idle R9", func(w *restartWorld) {
					w.srv.SetStatus(w.workerID, &hc.Status{App: "claude-code", State: "idle"})
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("worker idle R20", 25*time.Second),
				{"commit c1 and ready", func(w *restartWorld) { w.commitAndReady("package demo\n", greenRollup, true) }},
				passStep("N26 push and open PR", 0),
				passStep("N29 start round 1", 0),
				{"round 1 pass R9", func(w *restartWorld) {
					w.setReviewerVerdict(flow.VerdictPass, 0)
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("round 1 pass verdict", 25*time.Second),
				passStep("N29 start round 2", 0),
				{"round 2 pass R9", func(w *restartWorld) {
					w.setReviewerVerdict(flow.VerdictPass, 0)
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("round 2 pass verdict", 25*time.Second),
				passStep("N42 accept and merge", 0),
				{"PR merged and N2 done", func(w *restartWorld) {
					w.markPRMerged("merged-h4")
					w.now = w.now.Add(15 * time.Second)
					w.pass()
				}},
				passStep("N1 cleanup", 15*time.Second),
				{"coord 2 idle R9", func(w *restartWorld) {
					w.srv.SetStatus(w.state().Project.RunID, &hc.Status{App: "claude-code", State: "idle"})
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("J4 final journal", 25*time.Second),
				{"coord 2 working", func(w *restartWorld) {
					w.srv.SetStatus(w.state().Project.RunID, &hc.Status{App: "claude-code", State: "working"})
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				{"coord 2 idle R9", func(w *restartWorld) {
					w.srv.SetStatus(w.state().Project.RunID, &hc.Status{App: "claude-code", State: "idle"})
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("J4 stop coord 2", 25*time.Second),
			},
		},
	}
}

func runRestartHistory(t *testing.T, h restartHistory, restartAfter int) restartEnd {
	t.Helper()
	w := h.setup(t)
	for i, step := range h.steps {
		step.run(w)
		if i == restartAfter {
			w.restart()
		}
	}
	if restartAfter == len(h.steps)-1 {
		w.pass() // prove a restarted daemon at end state is a no-op
	}
	return w.summarize()
}

// TestRestartAfterEveryStep verifies plan.md slice 5a's done-when: a restart of
// the daemon after every step of every history gives the same end state (task
// done/merged, PR merged, same act keys, no duplicate PRs or merges, no extra
// live runs, and all invariants I1–I5 holding after every pass).
func TestRestartAfterEveryStep(t *testing.T) {
	installParallelFakeGH(t)
	histories := restartHistories()
	totalSteps := 0
	for _, h := range histories {
		totalSteps += len(h.steps)
		t.Run(h.name, func(t *testing.T) {
			t.Parallel()
			want := runRestartHistory(t, h, -1)
			if want.TaskDone == "" || want.PRState != "MERGED" || want.PRMerges != 1 || want.PRCreates > 1 || want.LiveTaskRuns != 0 || !want.WorktreeGone {
				t.Fatalf("baseline %s bad end state: %+v", h.name, want)
			}
			for k, step := range h.steps {
				t.Run(fmt.Sprintf("after_%02d_%s", k, strings.ReplaceAll(step.name, " ", "_")), func(t *testing.T) {
					t.Parallel()
					got := runRestartHistory(t, h, k)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("restart after step %d (%s) diverged:\ngot:  %+v\nwant: %+v", k, step.name, got, want)
					}
				})
			}
		})
	}
	t.Logf("covered %d histories x %d total steps", len(histories), totalSteps)
}
