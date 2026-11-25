package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
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
	revMu    sync.Mutex
	revToks  map[string]string
	commits  []string
}

func newRestartWorld(t *testing.T, goal string) *restartWorld {
	t.Helper()
	srv := hctest.New(t)
	repo, _ := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)
	w := &restartWorld{
		t:       t,
		ctx:     context.Background(),
		srv:     srv,
		client:  hc.NewClient(srv.Path),
		paths:   testPaths(t),
		repo:    repo,
		gh:      gh,
		now:     time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
		revToks: map[string]string{},
	}
	srv.OnRun = func(term hc.Terminal, p hc.RunParams) {
		switch p.Labels["sheepdog.role"] {
		case flow.RoleCoordinator:
			w.coordTok = p.Env["SHEEPDOG_RUN_TOKEN"]
		case flow.RoleWorker:
			w.workTok = p.Env["SHEEPDOG_RUN_TOKEN"]
			w.workerID = term.Address
		case flow.RoleReviewer:
			w.revMu.Lock()
			w.revToks[term.Address] = p.Env["SHEEPDOG_RUN_TOKEN"]
			w.revMu.Unlock()
		}
	}
	w.newEngine()
	if err := w.eng.Start(repo, goal, 2); err != nil {
		t.Fatal(err)
	}
	w.id = w.eng.projectDirs()[0].Name()
	w.edit(func(s *flow.State) { s.Settings.PlanReviewRounds = 0 })
	return w
}

func (w *restartWorld) newEngine() {
	w.eng = newTestEngine(w.t, w.paths, w.client)
	w.eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	w.eng.Now = func() time.Time { return w.now }
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

func (w *restartWorld) reviewerToken(runID string) string {
	w.revMu.Lock()
	defer w.revMu.Unlock()
	return w.revToks[runID]
}

// submitFakeReviewerVerdict delivers a fake reviewer's verdict through its
// CLI's real channel: Claude and agy reviewers write the engine's draft
// findings file and call Engine.Report(tok, "review", ...); Codex reviewers
// keep printing a screen <report> tag.
func submitFakeReviewerVerdict(t *testing.T, eng *Engine, srv *hctest.Server, paths Paths, projID, taskID string, rv *flow.ReviewRun, revTok, verdict string, misses int, findings string) error {
	t.Helper()
	if rv == nil || rv.RunID == "" {
		t.Fatalf("expected active reviewer, got %+v", rv)
	}
	if rv.Reviewer.CLI == world.CodexCLI {
		majors := countExactMajors(findings)
		var body strings.Builder
		body.WriteString("› prompt\n\n• Review done.\n")
		if strings.TrimSpace(findings) != "" {
			for line := range strings.Lines(findings) {
				body.WriteString("• " + strings.TrimRight(line, "\r\n") + "\n")
			}
		}
		fmt.Fprintf(&body, "• self-check misses: %d\n• deletable: none\n\n  <report>{\"code\":%q,\"round\":%d,\"head\":%q,\"verdict\":%q,\"majors\":%d}</report>\n",
			misses, rv.Code, rv.Round, rv.Head, verdict, majors)
		srv.SetScreen(rv.RunID, body.String())
		return nil
	}
	if revTok == "" {
		t.Fatalf("missing reviewer token for %s (round %d, cli %q)", rv.RunID, rv.Round, rv.Reviewer.CLI)
	}
	draft := paths.ReviewDraft(projID, taskID, rv.Round, rv.Code)
	if err := os.MkdirAll(filepath.Dir(draft), 0o700); err != nil {
		t.Fatalf("mkdir review draft dir: %v", err)
	}
	if err := os.WriteFile(draft, []byte(findings), 0o600); err != nil {
		t.Fatalf("write review draft %s: %v", draft, err)
	}
	rawFindings, err := os.ReadFile(draft)
	if err != nil {
		t.Fatalf("read review draft %s: %v", draft, err)
	}
	payload, err := json.Marshal(map[string]any{
		"verdict":           verdict,
		"code":              rv.Code,
		"head":              rv.Head,
		"self_check_misses": misses,
		"deletable":         "none",
		"class":             "none",
		"not_run":           "none",
		"findings":          string(rawFindings),
	})
	if err != nil {
		t.Fatalf("marshal review report: %v", err)
	}
	return eng.Report(revTok, "review", payload)
}

func (w *restartWorld) setReviewerVerdict(verdict string, majors int) {
	w.t.Helper()
	st := w.state()
	t0 := st.Tasks[0]
	rv := t0.Attempts[0].Review
	if rv == nil || rv.RunID == "" {
		w.t.Fatalf("expected active reviewer, got %+v", rv)
	}
	var findings strings.Builder
	if majors == 0 {
		findings.WriteString("Review done.\n")
	} else {
		for range majors {
			findings.WriteString("major: fix issue\n")
		}
	}
	if err := submitFakeReviewerVerdict(w.t, w.eng, w.srv, w.paths, w.id, t0.ID, rv, w.reviewerToken(rv.RunID), verdict, 0, findings.String()); err != nil {
		w.t.Fatalf("setReviewerVerdict(%s, %d): %v", verdict, majors, err)
	}
}

func (w *restartWorld) markPRMerged(sha string) {
	w.t.Helper()
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

func (w *restartWorld) waitIdle(addr string) {
	w.srv.SetStatus(addr, &hc.Status{App: "claude-code", State: "idle"})
	w.now = w.now.Add(5 * time.Second)
	w.pass()
	w.now = w.now.Add(25 * time.Second)
	w.pass()
}

func (w *restartWorld) passReviewRound() {
	rv := w.state().Tasks[0].Attempts[0].Review
	w.setReviewerVerdict(flow.VerdictPass, 0)
	if rv != nil && rv.Reviewer.CLI == world.CodexCLI {
		w.now = w.now.Add(5 * time.Second)
		w.pass()
		w.now = w.now.Add(25 * time.Second)
		w.pass()
	}
}

type restartEnd struct {
	TaskDone     string
	PRCreates    int
	PRMerges     []string
	CIReruns     int
	Rounds       []string
	Accepts      []string
	Restarts     int
	ProjectActs  []string
	TaskActs     []string
	AttemptActs  []string
	LiveTaskRuns int
	LiveCoords   int
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
		var out []string
		for _, a := range acts {
			out = append(out, w.normalize(a.Key)+"="+a.Result)
		}
		slices.Sort(out)
		return out
	}
	var rounds, accepts, merges []string
	for _, r := range a0.Rounds {
		rounds = append(rounds, fmt.Sprintf("r%d:%s:%s", r.N, w.normalize(r.Head), r.Verdict))
	}
	for _, ac := range a0.Accepts {
		accepts = append(accepts, fmt.Sprintf("%s:%s:%v", w.normalize(ac.Head), ac.By, ac.Rounds))
	}
	for _, call := range ghCalls(w.t, w.gh, "pr merge") {
		fs := strings.Fields(call)
		prNum, head := "", ""
		if len(fs) > 2 {
			prNum = fs[2]
		}
		if i := slices.Index(fs, "--match-head-commit"); i >= 0 && i+1 < len(fs) {
			head = w.normalize(fs[i+1])
		}
		merges = append(merges, prNum+"@"+head)
	}
	list, err := w.client.List(w.ctx, false)
	if err != nil {
		w.t.Fatal(err)
	}
	liveTask, liveCoords := 0, 0
	for _, tm := range list.Terminals {
		if tm.State != "live" {
			continue
		}
		if tm.Labels["sheepdog.role"] == flow.RoleCoordinator {
			liveCoords++
		} else {
			liveTask++
		}
	}
	_, wtErr := os.Stat(a0.Worktree)
	return restartEnd{
		TaskDone:     done,
		PRCreates:    len(ghCalls(w.t, w.gh, "pr create")),
		PRMerges:     merges,
		CIReruns:     len(ghCalls(w.t, w.gh, "run rerun")),
		Rounds:       rounds,
		Accepts:      accepts,
		Restarts:     a0.Restarts,
		ProjectActs:  normActs(st.Project.Acts),
		TaskActs:     normActs(t0.Acts),
		AttemptActs:  normActs(a0.Acts),
		LiveTaskRuns: liveTask,
		LiveCoords:   liveCoords,
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

func setupRunningWorker(t *testing.T, goal string, reviewRounds int) *restartWorld {
	t.Helper()
	w := newRestartWorld(t, goal)
	if reviewRounds > 0 {
		w.edit(func(s *flow.State) { s.Settings.ReviewRounds = reviewRounds })
	}
	w.pass() // J7 start coord
	w.applyPlan()
	w.pass() // N11 new attempt
	w.pass() // N23 start worker
	w.srv.SetStatus(w.workerID, &hc.Status{App: "claude-code", State: "working"})
	w.now = w.now.Add(5 * time.Second)
	w.pass()
	return w
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
				{"worker idle and ready c1", func(w *restartWorld) {
					w.waitIdle(w.workerID)
					w.commitAndReady("package demo\n", greenRollup, true)
				}},
				passStep("N26 push and open PR", 0),
				passStep("N29 start round 1", 0),
				{"round 1 changes verdict", func(w *restartWorld) {
					w.setReviewerVerdict(flow.VerdictChanges, 1)
				}},
				passStep("N22 tell findings", 0),
				{"worker idle and ready c2", func(w *restartWorld) {
					w.waitIdle(w.workerID)
					w.commitAndReady("package demo\n// fixed\n", greenRollup, false)
				}},
				{"N26 push c2", func(w *restartWorld) {
					w.pass()
					writePRJSON(w.t, w.gh, "view.json", w.commits[len(w.commits)-1], greenRollup)
				}},
				passStep("N29 start round 2", 0),
				{"round 2 pass verdict", func(w *restartWorld) { w.passReviewRound() }},
				passStep("N29 start round 3", 0),
				{"round 3 pass verdict", func(w *restartWorld) { w.passReviewRound() }},
				passStep("N42 accept and merge", 0),
				{"PR merged, N2 done", func(w *restartWorld) {
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
				w := setupRunningWorker(t, "CI red then green", 0)
				w.waitIdle(w.workerID)
				red := checkRollup("FAILURE", "44714", "13621")
				w.commitAndReady("package demo\n", red, true)
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
				passStep("N26 push and open red PR", 0),
				passStep("N33 rerun failed CI", 0),
				passStep("N33 wait stale red", 30*time.Second),
				{"CI turns green and N42 merges", func(w *restartWorld) {
					writePRJSON(w.t, w.gh, "view.json", w.commits[0], greenRollup)
					w.now = w.now.Add(30 * time.Second)
					w.pass()
				}},
				passStep("N41 wait merge", 15*time.Second),
				{"PR merged, N2 done", func(w *restartWorld) {
					w.markPRMerged("merged-h2")
					w.now = w.now.Add(15 * time.Second)
					w.pass()
				}},
				passStep("N1 cleanup", 15*time.Second),
			},
		},
		{
			name:  "worker_killed_and_restarted",
			setup: func(t *testing.T) *restartWorld { return setupRunningWorker(t, "Worker killed and restarted", 1) },
			steps: []restartStep{
				{"kill worker mid-turn", func(w *restartWorld) {
					if _, err := w.client.Kill(w.ctx, w.workerID); err != nil {
						w.t.Fatal(err)
					}
					w.now = w.now.Add(5 * time.Second)
					w.pass()
				}},
				passStep("wait in backoff", 30*time.Second),
				passStep("N23 resume worker", 35*time.Second),
				{"worker idle and ready", func(w *restartWorld) {
					w.waitIdle(w.workerID)
					w.commitAndReady("package demo\n", greenRollup, true)
				}},
				passStep("N26 push and open PR", 0),
				passStep("N29 start review", 0),
				{"review pass verdict", func(w *restartWorld) { w.passReviewRound() }},
				passStep("N42 accept and merge", 0),
				{"PR merged, N2 done", func(w *restartWorld) {
					w.markPRMerged("merged-h3")
					w.now = w.now.Add(15 * time.Second)
					w.pass()
				}},
				passStep("N1 cleanup", 15*time.Second),
			},
		},
		{
			name:  "coordinator_killed_and_replaced",
			setup: func(t *testing.T) *restartWorld { return setupRunningWorker(t, "Coordinator killed and replaced", 1) },
			steps: []restartStep{
				{"kill coordinator 1", func(w *restartWorld) {
					if _, err := w.client.Kill(w.ctx, w.state().Project.RunID); err != nil {
						w.t.Fatal(err)
					}
					w.now = w.now.Add(2 * time.Minute)
					w.pass()
				}},
				passStep("J6 gap wait", 4*time.Minute),
				passStep("J7 replace coordinator", 5*time.Minute),
				{"worker idle and ready", func(w *restartWorld) {
					w.waitIdle(w.workerID)
					w.commitAndReady("package demo\n", greenRollup, true)
				}},
				passStep("N26 push and open PR", 0),
				passStep("N29 start review", 0),
				{"review pass verdict", func(w *restartWorld) { w.passReviewRound() }},
				passStep("N42 accept and merge", 0),
				{"PR merged, N2 done", func(w *restartWorld) {
					w.markPRMerged("merged-h4")
					w.now = w.now.Add(15 * time.Second)
					w.pass()
				}},
				passStep("N1 cleanup", 15*time.Second),
				{"J4 final journal", func(w *restartWorld) { w.waitIdle(w.state().Project.RunID) }},
				{"J4 stop coord 2", func(w *restartWorld) {
					cid := w.state().Project.RunID
					w.srv.SetStatus(cid, &hc.Status{App: "claude-code", State: "working"})
					w.now = w.now.Add(5 * time.Second)
					w.pass()
					w.waitIdle(cid)
				}},
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
// done/merged, exact PR merge target, same act multiset, no duplicate PRs or
// merges, no extra live runs or coordinators, and all invariants I1–I5 holding
// after every pass).
func TestRestartAfterEveryStep(t *testing.T) {
	t.Parallel()
	histories := restartHistories()
	totalSteps := 0
	for _, h := range histories {
		totalSteps += len(h.steps)
		t.Run(h.name, func(t *testing.T) {
			t.Parallel()
			want := runRestartHistory(t, h, -1)
			if want.TaskDone == "" || len(want.PRMerges) != 1 || !strings.HasPrefix(want.PRMerges[0], "7@<c") || want.PRCreates > 1 || want.LiveTaskRuns != 0 || want.LiveCoords > 1 || !want.WorktreeGone {
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
