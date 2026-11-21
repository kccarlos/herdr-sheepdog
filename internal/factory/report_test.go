package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

const (
	testScopeJSON    = `"for_whom":"users","if_skipped":"broken","smallest":"minimal","not_doing":"extras"`
	testOutcomesJSON = `"outcomes":[{"text":"works","proved_by":"cmd: go test ./..."}]`
	testPlanJSON     = testScopeJSON + "," + testOutcomesJSON
)

// TestRunTokenInEnvNotArgvOrLog: the coordinator's token reaches it only
// through the run's environment; argv, log.jsonl and the daemon log never
// carry it (design.md 7.2).
func TestRunTokenInEnvNotArgvOrLog(t *testing.T) {
	srv := hctest.New(t)
	var got hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { got = p }
	var logBuf bytes.Buffer
	eng := New(testPaths(t), hc.NewClient(srv.Path))
	eng.Log = logging.NewJSONLLogger(&logBuf)
	eng.RunEnv = map[string]string{"SHEEPDOG_WEB_PORT": "17777"}
	repo := initGitRepo(t)
	if err := eng.Start(repo, "a goal", 0); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(context.Background(), id, false)

	st, err := LoadState(eng.Paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	tok := got.Env["SHEEPDOG_RUN_TOKEN"]
	if len(tok) < 20 || st.Project.RunTokenHash != tokenHash(tok) || got.Env["SHEEPDOG_WEB_PORT"] != "17777" {
		t.Fatalf("run env = %v, state hash %q", got.Env, st.Project.RunTokenHash)
	}
	logFile, _ := os.ReadFile(eng.Paths.Log(id))
	stateFile, _ := os.ReadFile(eng.Paths.State(id))
	for where, text := range map[string]string{"argv": strings.Join(got.Argv, " "), "log.jsonl": string(logFile), "daemon log": logBuf.String(), "state.json": string(stateFile)} {
		if strings.Contains(text, tok) {
			t.Fatalf("token leaked into %s: %s", where, text)
		}
	}

	// A crash after the launch, before RunID was committed: the next pass
	// adopts the live run, and the token in its environment still works.
	st.Project.RunID = ""
	if err := SaveState(eng.Paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	got = hc.RunParams{}
	eng.passProject(context.Background(), id, false)
	if got.Argv != nil {
		t.Fatalf("the live coordinator was launched again: %v", got.Argv)
	}
	if err := eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`}]}`)); err != nil {
		t.Fatalf("adopted run's token: %v", err)
	}

	if err := eng.Stop(repo); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(eng.Paths.State(id)); strings.Contains(string(raw), "run_token") {
		t.Fatalf("stopped project kept its run token: %s", raw)
	}
}

// TestFailedLaunchClearsToken: when the pass finds the coordinator gone and
// cannot start it, no token is left that a run could report with.
func TestFailedLaunchClearsToken(t *testing.T) {
	eng := New(testPaths(t), hc.NewClient(hctest.New(t).Path))
	if err := eng.Start(initGitRepo(t), "a goal", 0); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	st, _ := LoadState(eng.Paths.State(id))
	st.Project.Folder = "" // StartRun refuses an empty cwd
	if err := SaveState(eng.Paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.passProject(context.Background(), id, false)
	raw, _ := os.ReadFile(eng.Paths.State(id))
	logFile, _ := os.ReadFile(eng.Paths.Log(id))
	if strings.Contains(string(raw), "run_token") || !strings.Contains(string(logFile), "requires hc client, Cwd") {
		t.Fatalf("failed launch: state=%s log=%s", raw, logFile)
	}
}

func refusalCode(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

// TestReportAcceptance walks slice 1's acceptance table: tokens, roles,
// plan apply's Shape and Apply checks (design.md 7.2, 11.7; KB-REV-5).
func TestReportAcceptance(t *testing.T) {
	var logBuf bytes.Buffer
	eng := New(testPaths(t), nil)
	eng.Log = logging.NewJSONLLogger(&logBuf)
	coord := flow.State{Seq: 1, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "proj-a", Started: true, RunTokenHash: tokenHash("tok-coord-a")}}
	worker := flow.State{Seq: 1, Project: flow.Project{ID: "proj-b", Started: true, RunTokenHash: tokenHash("tok-coord-b")},
		Tasks: []flow.Task{{ID: "T1", Title: "feat: x", EstimateLines: 50, Attempts: []flow.Attempt{{SizeCap: 100, RunTokenHash: tokenHash("tok-worker-b")}}}}}
	for _, s := range []flow.State{coord, worker} {
		if err := SaveState(eng.Paths.State(s.Project.ID), s); err != nil {
			t.Fatal(err)
		}
	}
	plan := json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: add a greeting helper","brief":"b",` + testScopeJSON + `,"outcomes":[{"text":"go test passes","proved_by":"cmd: go test ./..."}],"after":[]}]}`)
	bad := json.RawMessage(`{"tasks":[{"id":"A","estimate_lines":50,"title":"a","after":["B"],"estimate":5},{"id":"B","estimate_lines":50,"title":"b","after":["A"]},{"id":"C","estimate_lines":50,"title":" ","after":["Z"]}]}`)

	for _, c := range []struct {
		name, token, verb string
		args              json.RawMessage
		code, project     string
		why               []string
	}{
		{"made-up token", "tok-made-up", "plan apply", plan, "invalid_token", "", nil},
		{"no token", "", "plan apply", plan, "invalid_token", "", nil},
		{"worker sends plan apply", "tok-worker-b", "plan apply", plan, "kind_not_allowed", "proj-b", nil},
		{"run's terminal starts", "tok-coord-a", "start", nil, "kind_not_allowed", "proj-a", nil},
		{"run's terminal stops", "tok-worker-b", "stop", nil, "kind_not_allowed", "proj-b", nil},
		{"unknown verb", "tok-coord-a", "merge", nil, "kind_not_allowed", "", nil},
		{"every shape failure", "tok-coord-a", "plan apply", bad, "plan_invalid", "proj-a",
			[]string{`unknown field "estimate"`, `"C" is after "Z", which is not in the plan`, `cycle through "A"`, `task "C" has no title`}},
		{"another project's plan exists", "tok-coord-b", "plan apply", plan, "plan_exists", "proj-b", nil},
	} {
		logBuf.Reset()
		err := eng.Report(c.token, c.verb, c.args)
		if strings.Contains(logBuf.String(), "tok-") {
			t.Fatalf("%s: a token was logged: %s", c.name, logBuf.String())
		}
		if refusalCode(err) != c.code {
			t.Fatalf("%s: err = %v, want %s", c.name, err, c.code)
		}
		for _, w := range c.why {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q does not list %q", c.name, err, w)
			}
		}
		if n := strings.Count(logBuf.String(), "factory.refused"); n != 1 || !strings.Contains(logBuf.String(), `"code":"`+c.code+`"`) ||
			!strings.Contains(logBuf.String(), `"project":"`+c.project+`"`) {
			t.Errorf("%s: refusal not logged once with its code and project: %s", c.name, logBuf.String())
		}
	}
	for _, id := range []string{"proj-a", "proj-b"} {
		if st, _ := LoadState(eng.Paths.State(id)); st.Seq != 1 {
			t.Fatalf("a refusal changed %s: seq %d", id, st.Seq)
		}
	}

	if err := eng.Report("tok-coord-a", "plan apply", plan); err != nil {
		t.Fatalf("valid plan: %v", err)
	}
	st, _ := LoadState(eng.Paths.State("proj-a"))
	logFile, _ := os.ReadFile(eng.Paths.Log("proj-a"))
	if st.Seq != 2 || len(st.Tasks) != 1 || st.Tasks[0].Title != "feat: add a greeting helper" ||
		!strings.Contains(string(logFile), "T1 feat: add a greeting helper") {
		t.Fatalf("plan not applied and logged: state=%+v log=%s", st, logFile)
	}
	if err := eng.Report("tok-coord-a", "plan apply", plan); refusalCode(err) != "plan_exists" {
		t.Fatalf("second apply: %v, want plan_exists", err)
	}
}

// TestPlanShape covers the Shape check's edge cases one by one (design.md 11.7).
func TestPlanShape(t *testing.T) {
	for _, c := range []struct {
		name, plan string
		want       []string // nil: the plan passes
	}{
		{"duplicate id", `{"tasks":[{"id":"A","estimate_lines":50,"title":"feat: a",` + testPlanJSON + `},{"id":"A","estimate_lines":50,"title":"feat: b",` + testPlanJSON + `}]}`, []string{"task 2 needs an id"}},
		{"empty id is not an id", `{"tasks":[{"id":"","estimate_lines":50,"title":"feat: a",` + testPlanJSON + `},{"id":"B","estimate_lines":50,"title":"feat: b",` + testPlanJSON + `,"after":[""]}]}`,
			[]string{"task 1 needs an id", `"B" is after "", which is not in the plan`}},
		{"whitespace id", `{"tasks":[{"id":"  ","estimate_lines":50,"title":"feat: a",` + testPlanJSON + `}]}`, []string{"task 1 needs an id"}},
		{"empty plan", `{"tasks":[]}`, []string{"the plan has no tasks"}},
		{"no tasks key", `{}`, []string{"the plan has no tasks"}},
		{"self after", `{"tasks":[{"id":"A","estimate_lines":50,"title":"feat: a",` + testPlanJSON + `,"after":["A"]}]}`, []string{`cycle through "A"`}},
		{"tasks of the wrong type", `{"tasks":"A"}`, []string{"cannot unmarshal"}},
		{"ids are trimmed", `{"tasks":[{"id":" A ","estimate_lines":50,"title":"feat: a",` + testPlanJSON + `},{"id":"B","estimate_lines":50,"title":"feat: b",` + testPlanJSON + `,"after":[" A"]}]}`, nil},
		{"valid DAG", `{"tasks":[{"id":"A","estimate_lines":50,"title":"feat: a",` + testPlanJSON + `},{"id":"B","estimate_lines":50,"title":"feat: b",` + testPlanJSON + `,"after":["A"]},{"id":"C","estimate_lines":50,"title":"feat: c",` + testPlanJSON + `,"after":["A"]},{"id":"D","estimate_lines":50,"title":"feat: d",` + testPlanJSON + `,"after":["B","C"]}]}`, nil},
	} {
		tasks, fails := checkPlanWith(json.RawMessage(c.plan), nil, "", flow.DefaultSettings(), flow.ResolveFacts{})
		got := strings.Join(fails, "; ")
		if (c.want == nil) != (len(fails) == 0) {
			t.Errorf("%s: fails = %q, want %q", c.name, got, c.want)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q does not list %q", c.name, got, w)
			}
		}
		if c.want == nil && (tasks[0].ID != "A" || len(tasks[1].After) != 1 || tasks[1].After[0] != "A") {
			t.Errorf("%s: tasks = %+v", c.name, tasks)
		}
	}
}

// TestReportReadyAcceptance checks design.md 7.2 for `ready` (KB-RUN-58,
// KB-REV-5, KB-MRG-78): role enforcement, dirty_tree, nothing_new, pull_first,
// and moving H on a clean commit ahead of H.
func TestReportReadyAcceptance(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, baseSHA := initGitRepoWithRemote(t)
	eng := New(paths, nil)

	const projID = "proj-ready"
	created, err := world.NewWorktree(ctx, repo, paths.Worktree(projID, "T1", 1), "T1", "feat: x")
	if err != nil {
		t.Fatal(err)
	}

	st := flow.State{
		Seq: 1,
		Project: flow.Project{
			ID: projID, Folder: repo, Started: true, RunTokenHash: tokenHash("tok-coord"),
		},
		Settings: flow.DefaultSettings(),
		Tasks: []flow.Task{{
			ID: "T1", Title: "feat: x",
			Attempts: []flow.Attempt{{SizeCap: 100,
				BaseCommit: baseSHA, Branch: created.Branch, Worktree: created.Path,
				RunTokenHash: tokenHash("tok-worker"),
				Ready:        []flow.Ready{}, Rounds: []flow.Round{}, Accepts: []flow.Accept{}, Acts: []flow.Act{},
			}},
		}},
	}
	if err := SaveState(paths.State(projID), st); err != nil {
		t.Fatal(err)
	}

	// 1. Coordinator token refused kind_not_allowed; unknown token refused invalid_token.
	if err := eng.Report("tok-coord", "ready", json.RawMessage(`{"head":"`+baseSHA+`"}`)); refusalCode(err) != "kind_not_allowed" {
		t.Fatalf("coordinator ready = %v, want kind_not_allowed", err)
	}
	if err := eng.Report("tok-bad", "ready", json.RawMessage(`{"head":"`+baseSHA+`"}`)); refusalCode(err) != "invalid_token" {
		t.Fatalf("bad token ready = %v, want invalid_token", err)
	}
	// 2. Missing or mismatched head refused invalid_report.
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{}`)); refusalCode(err) != "invalid_report" {
		t.Fatalf("empty head = %v, want invalid_report", err)
	}
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"deadbeef"}`)); refusalCode(err) != "invalid_report" {
		t.Fatalf("mismatched head = %v, want invalid_report", err)
	}
	// 3. Clean at BaseCommit (same as H) refused nothing_new.
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+baseSHA+`"}`)); refusalCode(err) != "nothing_new" {
		t.Fatalf("at base ready = %v, want nothing_new", err)
	}

	// Commit c1 ahead of BaseCommit, then dirty the tree (untracked and modified).
	fPath := filepath.Join(created.Path, "a.txt")
	if err := os.WriteFile(fPath, []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, created.Path, "add", "a.txt")
	gitIn(t, created.Path, "commit", "-m", "feat: add a.txt")
	c1 := gitIn(t, created.Path, "rev-parse", "HEAD")

	// 4. Dirty tree (untracked file, then modified tracked file) refused dirty_tree.
	untracked := filepath.Join(created.Path, "scratch.tmp")
	if err := os.WriteFile(untracked, []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`"}`)); refusalCode(err) != "dirty_tree" {
		t.Fatalf("untracked dirty ready = %v, want dirty_tree", err)
	}
	_ = os.Remove(untracked)
	if err := os.WriteFile(fPath, []byte("v1-dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`"}`)); refusalCode(err) != "dirty_tree" {
		t.Fatalf("modified dirty ready = %v, want dirty_tree", err)
	}
	gitIn(t, created.Path, "checkout", "--", "a.txt")

	// Verify no refusal changed Seq.
	if cur, _ := LoadState(paths.State(projID)); cur.Seq != 1 {
		t.Fatalf("refusals changed state seq to %d", cur.Seq)
	}

	// 5. Clean and ahead at c1 -> accepted; H moves to c1.
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`"}`)); err != nil {
		t.Fatalf("clean ahead ready: %v", err)
	}
	cur, err := LoadState(paths.State(projID))
	if err != nil {
		t.Fatal(err)
	}
	if cur.Seq != 2 || flow.H(cur.Tasks[0].Attempts[0]) != c1 {
		t.Fatalf("after ready: seq=%d H=%s, want 2 and %s", cur.Seq, flow.H(cur.Tasks[0].Attempts[0]), c1)
	}

	// Reporting ready again at c1 without a new commit -> nothing_new.
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`"}`)); refusalCode(err) != "nothing_new" {
		t.Fatalf("duplicate ready = %v, want nothing_new", err)
	}

	// 6. Move H ahead to c2 while worktree is reset back to c1 -> refused pull_first.
	if err := os.WriteFile(fPath, []byte("v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, created.Path, "commit", "-am", "feat: v2")
	c2 := gitIn(t, created.Path, "rev-parse", "HEAD")
	cur.Tasks[0].Attempts[0].Ready = append(cur.Tasks[0].Attempts[0].Ready, flow.Ready{Head: c2})
	if err := SaveState(paths.State(projID), cur); err != nil {
		t.Fatal(err)
	}
	gitIn(t, created.Path, "reset", "--hard", c1)
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`"}`)); refusalCode(err) != "pull_first" {
		t.Fatalf("behind H ready = %v, want pull_first", err)
	}
}

// TestVerdictAcceptance checks design.md 7.2 for `verdict` (KB-REV-5, KB-REV-53):
// wrong code, wrong round, wrong head, pass with majors, changes with 0 majors,
// bad JSON, reviewer token role refusal on HTTP Report, logged-once invalid_report
// leaving the round unused, and valid acceptance writing reports/<task>-r<n>.md
// and stopping the reviewer run.
func TestVerdictAcceptance(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	var logBuf bytes.Buffer
	eng := New(paths, client)
	eng.Log = logging.NewJSONLLogger(&logBuf)

	const projID = "proj-verdict"
	pastMS := uint64(time.Now().Add(-time.Minute).UnixMilli())
	srv.Add(hc.Terminal{Address: "t-rev", Name: "rev-1", State: "live", Status: &hc.Status{App: "claude-code", State: "done"}, StatusMS: &pastMS})

	st := flow.State{
		Seq:     1,
		Project: flow.Project{ID: projID, Started: true, RunTokenHash: tokenHash("tok-coord")},
		Tasks: []flow.Task{{
			ID: "T1", Title: "feat: add parser",
			Attempts: []flow.Attempt{{SizeCap: 100,
				BaseCommit: "b0", RunTokenHash: tokenHash("tok-worker"),
				Ready: []flow.Ready{{Head: "c1"}},
				Review: &flow.ReviewRun{
					RunID: "t-rev", RunTokenHash: tokenHash("tok-rev"),
					Round: 1, Head: "c1", Code: "nonce123",
					Reviewer: flow.Reviewer{CLI: "claude", Model: "opus", Effort: "medium"},
				},
			}},
		}},
	}
	if err := SaveState(paths.State(projID), st); err != nil {
		t.Fatal(err)
	}

	// Reviewer token cannot send ready, plan apply, stop, or verdict over Report HTTP.
	for _, verb := range []string{"ready", "plan apply", "stop", "verdict"} {
		if err := eng.Report("tok-rev", verb, json.RawMessage(`{}`)); refusalCode(err) != "kind_not_allowed" {
			t.Fatalf("reviewer Report(%q) = %v, want kind_not_allowed", verb, err)
		}
	}

	// Invalid verdict cases: each refused invalid_report, round stays unused, reviewer stays untouched.
	badCases := []struct {
		name string
		rd   world.ReportRead
	}{
		{"malformed json", world.ReportRead{Status: world.ReportInvalid, Raw: "{bad"}},
		{"wrong code", world.ReportRead{Status: world.ReportFound, Raw: "c", Report: world.Report{Code: "wrong", Round: 1, Head: "c1", Verdict: "pass"}}},
		{"wrong round", world.ReportRead{Status: world.ReportFound, Raw: "r", Report: world.Report{Code: "nonce123", Round: 2, Head: "c1", Verdict: "pass"}}},
		{"wrong head", world.ReportRead{Status: world.ReportFound, Raw: "h", Report: world.Report{Code: "nonce123", Round: 1, Head: "c0", Verdict: "pass"}}},
		{"pass with majors", world.ReportRead{Status: world.ReportFound, Raw: "pm", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "pass", Majors: 1}}},
		{"changes with 0 majors", world.ReportRead{Status: world.ReportFound, Raw: "cm", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "changes", Majors: 0}}},
		{"unknown verdict word", world.ReportRead{Status: world.ReportFound, Raw: "uv", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "pass|changes", Majors: 0}}},
		{"pass with a major line", world.ReportRead{Status: world.ReportFound, Raw: "pl", Body: "major: nil deref\nminor: naming\nself-check misses: 0\ndeletable: none", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "pass", Majors: 0}}},
		{"majors above the major lines", world.ReportRead{Status: world.ReportFound, Raw: "ml", Body: "  major: nil deref\n  minor: naming\nself-check misses: 0\ndeletable: none", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "changes", Majors: 2}}},
		{"missing self-check misses", world.ReportRead{Status: world.ReportFound, Raw: "nm", Body: "deletable: none", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "pass", Majors: 0}}},
		{"non-integer self-check misses", world.ReportRead{Status: world.ReportFound, Raw: "bm", Body: "self-check misses: many\ndeletable: none", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "pass", Majors: 0}}},
		{"negative self-check misses", world.ReportRead{Status: world.ReportFound, Raw: "negm", Body: "self-check misses: -1\ndeletable: none", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "pass", Majors: 0}}},
		{"missing deletable", world.ReportRead{Status: world.ReportFound, Raw: "nd", Body: "self-check misses: 0", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "pass", Majors: 0}}},
		{"empty deletable", world.ReportRead{Status: world.ReportFound, Raw: "ed", Body: "self-check misses: 0\ndeletable:   ", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "pass", Majors: 0}}},
	}
	for _, tc := range badCases {
		logBuf.Reset()
		err := eng.acceptVerdictLocked(ctx, projID, &st, 0, tc.rd)
		if refusalCode(err) != "invalid_report" {
			t.Fatalf("%s: err = %v, want invalid_report", tc.name, err)
		}
		if len(st.Tasks[0].Attempts[0].Rounds) != 0 || st.Seq != 1 {
			t.Fatalf("%s mutated rounds/seq: %+v seq=%d", tc.name, st.Tasks[0].Attempts[0].Rounds, st.Seq)
		}
		if n := strings.Count(logBuf.String(), "factory.refused"); n != 1 {
			t.Fatalf("%s logged %d refusals, want 1:\n%s", tc.name, n, logBuf.String())
		}
		// Repeated read of the exact same invalid output on the same run is not re-logged.
		if err2 := eng.acceptVerdictLocked(ctx, projID, &st, 0, tc.rd); refusalCode(err2) != "invalid_report" {
			t.Fatalf("%s repeat: err = %v", tc.name, err2)
		}
		if n := strings.Count(logBuf.String(), "factory.refused"); n != 1 {
			t.Fatalf("%s repeat re-logged refusal (%d):\n%s", tc.name, n, logBuf.String())
		}
	}

	// Valid changes verdict records round{n:1, kind:"full", ...}, reply lines
	// (cleaned and capped at 500), writes T1-r1.md, logs self-check misses, and stops t-rev.
	longDel := strings.Repeat("x", 600)
	good := world.ReportRead{
		Status: world.ReportFound,
		Raw:    `{"code":"nonce123","round":1,"head":"c1","verdict":"changes","majors":4}`,
		Body: "- major: nil deref in Parse\n**major:** missing error check\n1. major: no test\n  * major: race\n" +
			"• **self-check misses:** 2\n- class: missing\t\x00guards\n1. deletable: " + longDel + "\n  * not run: live e2e\n" +
			"Detailed review notes line 42.",
		Report: world.Report{
			Code: "nonce123", Round: 1, Head: "c1", Verdict: "changes", Majors: 4,
		},
	}
	if err := eng.acceptVerdictLocked(ctx, projID, &st, 0, good); err != nil {
		t.Fatalf("valid verdict: %v", err)
	}
	att := st.Tasks[0].Attempts[0]
	if att.Review != nil || len(att.Rounds) != 1 || st.Seq != 2 {
		t.Fatalf("after valid verdict: review=%+v rounds=%+v seq=%d", att.Review, att.Rounds, st.Seq)
	}
	wantPath := paths.Report(projID, "T1", 1)
	r1 := att.Rounds[0]
	if r1.N != 1 || r1.Kind != "full" || r1.Head != "c1" || r1.Verdict != "changes" || r1.Majors != 4 ||
		r1.Reviewer != (flow.Reviewer{CLI: "claude", Model: "opus", Effort: "medium"}) || r1.ReportPath != wantPath ||
		r1.SelfCheckMisses == nil || *r1.SelfCheckMisses != 2 || r1.Class != "missing guards" ||
		len(r1.Deletable) != maxRejectWhyRunes || r1.NotRun != "live e2e" {
		t.Fatalf("round 1 record = %+v", r1)
	}
	logRaw, _ := os.ReadFile(paths.Log(projID))
	if !strings.Contains(string(logRaw), "2 self-check miss(es)") || !strings.Contains(string(logRaw), `"self_check_misses":2`) {
		t.Fatalf("verdict log line missing self-check misses:\n%s", logRaw)
	}
	for _, dup := range []string{`"class":`, `"deletable":`, `"not_run":`} {
		if strings.Contains(string(logRaw), dup) {
			t.Fatalf("verdict log input duplicated %s:\n%s", dup, logRaw)
		}
	}
	reportBytes, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read report file: %v", err)
	}
	for _, want := range []string{"nil deref in Parse", "missing error check", "Detailed review notes line 42."} {
		if !strings.Contains(string(reportBytes), want) {
			t.Errorf("report file missing %q:\n%s", want, reportBytes)
		}
	}
	if rs, err := termState(ctx, client, "t-rev"); err != nil || rs != flow.RunGone {
		t.Fatalf("reviewer terminal after verdict = (%s, %v), want gone", rs, err)
	}
}

// TestReportReadyRejectAcceptance checks design.md 7.2 for `ready --reject`
// (KB-REV-62, KB-REV-5): valid reject records flow.Reject without creating a
// round; empty/whitespace why -> why_missing; nothing_to_reject before any
// round, after a pass, after a non-reviewer round (send_back, retro), after an
// already-rejected round, and when the head moved (commit ahead or H moved).
func TestReportReadyRejectAcceptance(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, baseSHA := initGitRepoWithRemote(t)
	var logBuf bytes.Buffer
	eng := New(paths, nil)
	eng.Log = logging.NewJSONLLogger(&logBuf)

	const projID = "proj-reject"
	wt, err := world.NewWorktree(ctx, repo, paths.Worktree(projID, "T1", 1), "T1", "feat: add constant")
	if err != nil {
		t.Fatal(err)
	}
	fPath := filepath.Join(wt.Path, "const.go")
	if err := os.WriteFile(fPath, []byte("package demo\nconst Version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt.Path, "add", "const.go")
	gitIn(t, wt.Path, "commit", "-m", "feat: add Version")
	c1 := gitIn(t, wt.Path, "rev-parse", "HEAD")

	st := flow.State{
		Seq:      1,
		Project:  flow.Project{ID: projID, Folder: repo, Started: true, RunTokenHash: tokenHash("tok-coord")},
		Settings: flow.DefaultSettings(),
		Tasks: []flow.Task{{
			ID: "T1", Title: "feat: add constant",
			Attempts: []flow.Attempt{{SizeCap: 100,
				BaseCommit: baseSHA, Branch: wt.Branch, Worktree: wt.Path,
				RunTokenHash: tokenHash("tok-worker"),
				Ready:        []flow.Ready{{Head: c1}},
			}},
		}},
	}
	save := func() {
		t.Helper()
		if err := SaveState(paths.State(projID), st); err != nil {
			t.Fatal(err)
		}
	}
	save()

	// Role enforcement: coordinator refused kind_not_allowed.
	if err := eng.Report("tok-coord", "ready", json.RawMessage(`{"head":"`+c1+`","reject":"nope"}`)); refusalCode(err) != "kind_not_allowed" {
		t.Fatalf("coordinator reject = %v, want kind_not_allowed", err)
	}

	// No round covering H yet -> nothing_to_reject.
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`","reject":"no round"}`)); refusalCode(err) != "nothing_to_reject" {
		t.Fatalf("before round 1 reject = %v, want nothing_to_reject", err)
	}

	// After a pass round -> nothing_to_reject.
	st.Tasks[0].Attempts[0].Rounds = []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass}}
	save()
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`","reject":"passed"}`)); refusalCode(err) != "nothing_to_reject" {
		t.Fatalf("after pass reject = %v, want nothing_to_reject", err)
	}

	// After non-reviewer changes rounds (send_back, retro) -> nothing_to_reject.
	for _, kind := range []string{"send_back", "retro", ""} {
		st.Tasks[0].Attempts[0].Rounds = []flow.Round{{N: 1, Kind: kind, Head: c1, Verdict: flow.VerdictChanges, Majors: 1}}
		save()
		if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`","reject":"not a reviewer round"}`)); refusalCode(err) != "nothing_to_reject" {
			t.Fatalf("after kind=%q reject = %v, want nothing_to_reject", kind, err)
		}
	}

	// Reviewer changes round at c1, but empty or whitespace why -> why_missing.
	st.Tasks[0].Attempts[0].Rounds = []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictChanges, Majors: 1, ReportPath: paths.Report(projID, "T1", 1)}}
	save()
	for _, why := range []string{`""`, `"   \t\n "`} {
		logBuf.Reset()
		err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`","reject":`+why+`}`))
		if refusalCode(err) != "why_missing" || !strings.Contains(logBuf.String(), `"code":"why_missing"`) {
			t.Fatalf("reject %s = %v, log = %s; want why_missing", why, err, logBuf.String())
		}
	}

	// Dirty worktree -> dirty_tree.
	dirtyFile := filepath.Join(wt.Path, "dirty.tmp")
	if err := os.WriteFile(dirtyFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`","reject":"reason"}`)); refusalCode(err) != "dirty_tree" {
		t.Fatalf("dirty tree reject = %v, want dirty_tree", err)
	}
	_ = os.Remove(dirtyFile)

	// Valid reject at c1: writes flow.Reject{Head: c1, Round: 1, Why: ...}, adds no round.
	const reason = "Brief states no unit test is needed for this one-line constant."
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`","reject":"  `+reason+`  "}`)); err != nil {
		t.Fatalf("valid reject: %v", err)
	}
	got, err := LoadState(paths.State(projID))
	if err != nil {
		t.Fatal(err)
	}
	att := got.Tasks[0].Attempts[0]
	if got.Seq != 2 || len(att.Rounds) != 1 || len(att.Rejects) != 1 ||
		att.Rejects[0] != (flow.Reject{Head: c1, Round: 1, Why: reason}) {
		t.Fatalf("after valid reject: seq=%d rounds=%+v rejects=%+v", got.Seq, att.Rounds, att.Rejects)
	}

	// Rejecting the same round a second time -> nothing_to_reject.
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`","reject":"again"}`)); refusalCode(err) != "nothing_to_reject" {
		t.Fatalf("duplicate reject = %v, want nothing_to_reject", err)
	}

	// Suppose round 2 (delta) asks for changes at c1, then worker commits c2 (head moved ahead of H) -> nothing_to_reject.
	got.Tasks[0].Attempts[0].Rounds = append(got.Tasks[0].Attempts[0].Rounds,
		flow.Round{N: 2, Kind: flow.RoundDelta, Head: c1, Verdict: flow.VerdictChanges, Majors: 1})
	if err := SaveState(paths.State(projID), got); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fPath, []byte("package demo\nconst Version = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt.Path, "commit", "-am", "feat: bump Version")
	c2 := gitIn(t, wt.Path, "rev-parse", "HEAD")
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c2+`","reject":"moved head"}`)); refusalCode(err) != "nothing_to_reject" {
		t.Fatalf("reject after new commit = %v, want nothing_to_reject", err)
	}

	// Normal ready at c2 moves H to c2; with no round covering c2 yet, reject is still nothing_to_reject.
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c2+`"}`)); err != nil {
		t.Fatalf("normal ready at c2: %v", err)
	}
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c2+`","reject":"H moved"}`)); refusalCode(err) != "nothing_to_reject" {
		t.Fatalf("reject at unreviewed H=c2 = %v, want nothing_to_reject", err)
	}
}

// TestReportReadyRejectWhySanitizeAndLength checks that ready --reject cleans
// control characters (including NUL, ESC/ANSI, U+2028/U+2029) and collapses
// spaces, refuses reasons over 500 runes with invalid_report, and accepts
// exactly 500 runes.
func TestReportReadyRejectWhySanitizeAndLength(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, baseSHA := initGitRepoWithRemote(t)
	eng := New(paths, nil)

	const projID = "proj-reject-why"
	wt, err := world.NewWorktree(ctx, repo, paths.Worktree(projID, "T1", 1), "T1", "feat: clean why")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "f.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt.Path, "add", "f.go")
	gitIn(t, wt.Path, "commit", "-m", "feat: f")
	c1 := gitIn(t, wt.Path, "rev-parse", "HEAD")

	reset := func() {
		t.Helper()
		st := flow.State{
			Seq:      1,
			Project:  flow.Project{ID: projID, Folder: repo, Started: true},
			Settings: flow.DefaultSettings(),
			Tasks: []flow.Task{{
				ID: "T1", Title: "feat: clean why",
				Attempts: []flow.Attempt{{SizeCap: 100,
					BaseCommit: baseSHA, Branch: wt.Branch, Worktree: wt.Path,
					RunTokenHash: tokenHash("tok-worker"),
					Ready:        []flow.Ready{{Head: c1}},
					Rounds:       []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictChanges, Majors: 1}},
				}},
			}},
		}
		if err := SaveState(paths.State(projID), st); err != nil {
			t.Fatal(err)
		}
	}
	sendReject := func(why string) error {
		t.Helper()
		raw, err := json.Marshal(map[string]string{"head": c1, "reject": why})
		if err != nil {
			t.Fatal(err)
		}
		return eng.Report("tok-worker", "ready", raw)
	}

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"multiline", "  line 1\r\n\t line 2\nline 3  ", "line 1 line 2 line 3"},
		{"nul", "before\x00after", "before after"},
		{"ansi", "bad \x1b[31mred\x1b[0m\x07 text", "bad [31mred [0m text"},
		{"u2028", "sep\u2028line\u2029para", "sep line para"},
		{"exact500", strings.Repeat("é", 500), strings.Repeat("é", 500)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			if err := sendReject(tc.raw); err != nil {
				t.Fatalf("reject %s: %v", tc.name, err)
			}
			got, err := LoadState(paths.State(projID))
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Tasks[0].Attempts[0].Rejects) != 1 || got.Tasks[0].Attempts[0].Rejects[0].Why != tc.want {
				t.Fatalf("stored why = %q, want %q", got.Tasks[0].Attempts[0].Rejects[0].Why, tc.want)
			}
		})
	}

	t.Run("over500", func(t *testing.T) {
		reset()
		err := sendReject(strings.Repeat("a", 501))
		if refusalCode(err) != "invalid_report" || !strings.Contains(err.Error(), "the reason is longer than 500 characters; put details in a file and name it") {
			t.Fatalf("over-long reject = %v, want invalid_report with 500-char guidance", err)
		}
	})
}

// TestReportReadyRejectRefusedWhenLadderSaysRecap checks the PR 2.3 carry
// (Opus minor 1): ready --reject is accepted only when the ladder answers fix
// for H, never when L12 says recap (streak or max_rounds).
func TestReportReadyRejectRefusedWhenLadderSaysRecap(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, baseSHA := initGitRepoWithRemote(t)
	eng := New(paths, nil)

	const projID = "proj-reject-recap"
	wt, err := world.NewWorktree(ctx, repo, paths.Worktree(projID, "T1", 1), "T1", "feat: x")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "f.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt.Path, "add", "f.go")
	gitIn(t, wt.Path, "commit", "-m", "feat: f")
	c1 := gitIn(t, wt.Path, "rev-parse", "HEAD")

	cases := map[string][]flow.Round{
		"streak at round 3": {
			{N: 1, Kind: flow.RoundFull, Head: "c0", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 2, Kind: flow.RoundDelta, Head: "c0b", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 3, Kind: flow.RoundDelta, Head: c1, Verdict: flow.VerdictChanges, Majors: 1},
		},
		"max_rounds reached": {
			{N: 1, Kind: flow.RoundFull, Head: "c0", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 2, Kind: flow.RoundDelta, Head: "c0b", Verdict: flow.VerdictPass},
			{N: 3, Kind: flow.RoundFull, Head: "c0c", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 4, Kind: flow.RoundDelta, Head: "c0d", Verdict: flow.VerdictPass},
			{N: 5, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictChanges, Majors: 1},
		},
	}
	for name, rounds := range cases {
		t.Run(name, func(t *testing.T) {
			st := flow.State{
				Seq:      1,
				Project:  flow.Project{ID: projID, Folder: repo, Started: true},
				Settings: flow.DefaultSettings(),
				Tasks: []flow.Task{{
					ID: "T1", Title: "feat: x",
					Attempts: []flow.Attempt{{SizeCap: 100,
						BaseCommit: baseSHA, Branch: wt.Branch, Worktree: wt.Path,
						RunTokenHash: tokenHash("tok-worker"),
						Ready:        []flow.Ready{{Head: c1}},
						Rounds:       rounds,
					}},
				}},
			}
			if err := SaveState(paths.State(projID), st); err != nil {
				t.Fatal(err)
			}
			err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`","reject":"still wrong"}`))
			if refusalCode(err) != "nothing_to_reject" || !strings.Contains(err.Error(), "the coordinator decides") {
				t.Fatalf("reject when ladder says recap = %v, want nothing_to_reject naming the coordinator", err)
			}
		})
	}
	// After a new commit the recap text does not apply: use plain ready.
	if err := os.WriteFile(filepath.Join(wt.Path, "g.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt.Path, "add", "g.go")
	gitIn(t, wt.Path, "commit", "-m", "feat: g")
	c2 := gitIn(t, wt.Path, "rev-parse", "HEAD")
	err = eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c2+`","reject":"still wrong"}`))
	if refusalCode(err) != "nothing_to_reject" || !strings.Contains(err.Error(), "after a commit") {
		t.Fatalf("reject after a commit with a recap = %v, want the plain-ready text", err)
	}
}

// TestRecapDecideAcceptance checks design.md 4.9 and 7.2 for `recap decide`:
// coordinator-only, valid split (children added with split_from, dependants
// moved, parent marked split), valid drop (marked dropped{why}), and refusals
// (not_allowed naming allowed set, why_missing, no_recap, plan_invalid).
func TestRecapDecideAcceptance(t *testing.T) {
	paths := testPaths(t)
	var logBuf bytes.Buffer
	eng := New(paths, nil)
	eng.Log = logging.NewJSONLLogger(&logBuf)
	const id = "proj-recap"

	reset := func(open bool) {
		t.Helper()
		var recaps []flow.Recap
		if open {
			recaps = []flow.Recap{{Trigger: "review", Allowed: []string{"split", "drop"}}}
		}
		st := flow.State{
			Seq:      1,
			Project:  flow.Project{ID: id, Started: true, RunTokenHash: tokenHash("tok-coord")},
			Settings: flow.DefaultSettings(),
			Tasks: []flow.Task{
				{ID: "T0", Title: "feat: base"},
				{ID: "T1", Title: "feat: big", Recaps: recaps, Attempts: []flow.Attempt{{SizeCap: 100, RunTokenHash: tokenHash("tok-worker")}}},
				{ID: "T2", Title: "feat: after big", After: []string{"T0", "T1"}},
			},
		}
		if err := SaveState(paths.State(id), st); err != nil {
			t.Fatal(err)
		}
	}

	// Refusals: role, no_recap, not_allowed (naming allowed set), why_missing, plan_invalid.
	reset(true)
	if err := eng.Report("tok-worker", "recap decide", json.RawMessage(`{"task":"T1","choice":"drop","why":"x"}`)); refusalCode(err) != "kind_not_allowed" {
		t.Fatalf("worker recap decide = %v, want kind_not_allowed", err)
	}
	if err := eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T0","choice":"drop","why":"x"}`)); refusalCode(err) != "no_recap" {
		t.Fatalf("no open recap = %v, want no_recap", err)
	}
	if err := eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T9","choice":"drop","why":"x"}`)); refusalCode(err) != "no_recap" {
		t.Fatalf("unknown task = %v, want no_recap", err)
	}
	if err := eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T1","choice":"continue","why":"x"}`)); refusalCode(err) != "not_allowed" ||
		!strings.Contains(err.Error(), "split, drop") {
		t.Fatalf("disallowed choice = %v, want not_allowed naming split, drop", err)
	}
	if err := eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T1","choice":"drop","why":"   "}`)); refusalCode(err) != "why_missing" {
		t.Fatalf("empty why = %v, want why_missing", err)
	}
	for name, body := range map[string]string{
		"no children":      `{"task":"T1","choice":"split","why":"too big"}`,
		"missing title":    `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","estimate_lines":50,"title":" "}]}`,
		"existing id":      `{"task":"T1","choice":"split","why":"w","children":[{"id":"T0","estimate_lines":50,"title":"feat: dup"}]}`,
		"after missing":    `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","estimate_lines":50,"title":"feat: a","after":["NOPE"]}]}`,
		"after parent":     `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","estimate_lines":50,"title":"feat: a","after":["T1"]}]}`,
		"cycle":            `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","estimate_lines":50,"title":"feat: a","after":["T1b"]},{"id":"T1b","estimate_lines":50,"title":"feat: b","after":["T1a"]}]}`,
		"unknown field":    `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","estimate_lines":50,"title":"feat: a","bogus":1}]}`,
		"cycle via parent": `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","estimate_lines":50,"title":"feat: a","after":["T2"]}]}`,
	} {
		if err := eng.Report("tok-coord", "recap decide", json.RawMessage(body)); refusalCode(err) != "plan_invalid" {
			t.Errorf("%s: err = %v, want plan_invalid", name, err)
		}
	}
	if st, _ := LoadState(paths.State(id)); st.Seq != 1 {
		t.Fatalf("refusals mutated state: seq=%d", st.Seq)
	}

	// Valid split: parent marked split, children added with split_from, dependants moved.
	splitBody := `{"task":"T1","choice":"split","why":"two independent parts","children":[{"id":"T1a","estimate_lines":50,"title":"feat: part a","brief":"a",` + testScopeJSON + `,"outcomes":[{"text":"test a","proved_by":"test: TestA"}],"after":["T0"]},{"id":"T1b","estimate_lines":50,"title":"feat: part b","brief":"b",` + testScopeJSON + `,"outcomes":[{"text":"test b","proved_by":"test: TestB"}],"after":["T1a"]}]}`
	if err := eng.Report("tok-coord", "recap decide", json.RawMessage(splitBody)); err != nil {
		t.Fatalf("valid split: %v", err)
	}
	st, _ := LoadState(paths.State(id))
	if st.Tasks[1].Split == nil || strings.Join(st.Tasks[1].Split.Children, ",") != "T1a,T1b" ||
		st.Tasks[1].Recaps[0].Decided == nil || st.Tasks[1].Recaps[0].Decided.Choice != "split" {
		t.Fatalf("parent after split = %+v", st.Tasks[1])
	}
	if strings.Join(st.Tasks[2].After, ",") != "T0,T1a,T1b" {
		t.Fatalf("dependant T2.After = %v, want [T0 T1a T1b]", st.Tasks[2].After)
	}
	if len(st.Tasks) != 5 || st.Tasks[3].ID != "T1a" || st.Tasks[3].SplitFrom != "T1" || st.Tasks[4].ID != "T1b" || st.Tasks[4].SplitFrom != "T1" {
		t.Fatalf("children after split = %+v", st.Tasks[3:])
	}
	// Deciding again after recap closed -> no_recap.
	if err := eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T1","choice":"drop","why":"again"}`)); refusalCode(err) != "no_recap" {
		t.Fatalf("second decide = %v, want no_recap", err)
	}

	// Valid drop: marked dropped{why}, recap closed.
	reset(true)
	if err := eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T1","choice":"drop","why":"contradictory goal"}`)); err != nil {
		t.Fatalf("valid drop: %v", err)
	}
	st, _ = LoadState(paths.State(id))
	if st.Tasks[1].Dropped == nil || st.Tasks[1].Dropped.Why != "contradictory goal" ||
		st.Tasks[1].Recaps[0].Decided == nil || st.Tasks[1].Recaps[0].Decided.Choice != "drop" {
		t.Fatalf("parent after drop = %+v", st.Tasks[1])
	}
}

// TestRecapDecideRefusesEndedTaskAndCleansWhy covers the PR 2.4 review round 1:
// a recap left open on a task that ended another way (merged by hand) is not
// decidable, and the why is cleaned and capped like a reject why.
func TestRecapDecideRefusesEndedTaskAndCleansWhy(t *testing.T) {
	paths := testPaths(t)
	eng := New(paths, nil)
	const id = "proj-recap-ended"
	save := func(done bool) {
		t.Helper()
		task := flow.Task{ID: "T1", Title: "feat: big", EstimateLines: 50, Recaps: []flow.Recap{{Trigger: "review", Allowed: []string{"split", "drop"}}}}
		if done {
			task.Done = &flow.Merged{MergedCommit: "m1"}
		}
		st := flow.State{Seq: 1, Project: flow.Project{ID: id, Started: true, RunTokenHash: tokenHash("tok-coord")}, Settings: flow.DefaultSettings(), Tasks: []flow.Task{task}}
		if err := SaveState(paths.State(id), st); err != nil {
			t.Fatal(err)
		}
	}
	save(true)
	for _, body := range []string{
		`{"task":"T1","choice":"drop","why":"x"}`,
		`{"task":"T1","choice":"split","why":"x","children":[{"id":"T1a","estimate_lines":50,"title":"feat: a"}]}`,
	} {
		if err := eng.Report("tok-coord", "recap decide", json.RawMessage(body)); refusalCode(err) != "already_closed" {
			t.Fatalf("decide on done task %s = %v, want already_closed", body, err)
		}
	}
	st, _ := LoadState(paths.State(id))
	if st.Seq != 1 || len(st.Tasks) != 1 || st.Tasks[0].Done == nil || st.Tasks[0].Dropped != nil || st.Tasks[0].Split != nil || st.Tasks[0].Recaps[0].Decided != nil {
		t.Fatalf("state changed after refused decisions: %+v", st)
	}

	save(false)
	long, _ := json.Marshal(map[string]string{"task": "T1", "choice": "drop", "why": strings.Repeat("a", 501)})
	if err := eng.Report("tok-coord", "recap decide", long); refusalCode(err) != "invalid_report" {
		t.Fatalf("over-long why = %v, want invalid_report", err)
	}
	if err := eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T1","choice":"drop","why":"a\nb\u001b[31m\tc"}`)); err != nil {
		t.Fatal(err)
	}
	st, _ = LoadState(paths.State(id))
	if got := st.Tasks[0].Dropped.Why; got != "a b [31m c" {
		t.Fatalf("why = %q, want control characters cleaned", got)
	}
}

// TestPlanScopeAndTitle checks KB-PLN-4 and design.md 11.7: for_whom,
// if_skipped, smallest and not_doing are required on plan apply, tasks added
// by plan amend, and split children, naming every missing field of every task
// in one plan_invalid refusal with nothing changed; titles must be
// Conventional Commit subjects everywhere.
func TestPlanScopeAndTitle(t *testing.T) {
	paths := testPaths(t)
	eng := New(paths, nil)
	const id = "proj-scope"
	st := flow.State{
		Seq:      1,
		Project:  flow.Project{ID: id, Started: true, RunTokenHash: tokenHash("tok-coord")},
		Settings: flow.DefaultSettings(),
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	// plan apply: missing scope fields across two tasks named in one refusal; nothing changes.
	missingScope := `{"tasks":[` +
		`{"id":"A","title":"feat: a","estimate_lines":40,"if_skipped":"x","not_doing":"y"},` +
		`{"id":"B","title":"fix(cli): b","estimate_lines":40,"for_whom":"u","if_skipped":" \n\t ","smallest":"s","not_doing":"\u0007"}` +
		`]}`
	err := eng.Report("tok-coord", "plan apply", json.RawMessage(missingScope))
	if refusalCode(err) != "plan_invalid" {
		t.Fatalf("missing scope apply = %v, want plan_invalid", err)
	}
	for _, want := range []string{`task "A" needs for_whom`, `task "A" needs smallest`, `task "B" needs if_skipped`, `task "B" needs not_doing`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q missing %q", err, want)
		}
	}
	if got, _ := LoadState(paths.State(id)); got.Seq != 1 || len(got.Tasks) != 0 {
		t.Fatalf("refused apply mutated state: seq=%d tasks=%d", got.Seq, len(got.Tasks))
	}

	// Title check: all 11 Conventional Commit types pass; non-conforming subjects fail.
	for _, okTitle := range []string{
		"feat: add x", "fix(web): handle nil", "docs(engine-v2/design): clarify",
		"refactor!: drop legacy", "test(flow): cover N12", "chore: tidy",
		"perf(db): cache", "ci: pin go", "build(ios): bump", "style: gofmt", "revert: undo",
	} {
		raw := `{"tasks":[{"id":"T1","title":` + strconvQuote(okTitle) + `,"estimate_lines":20,` + testPlanJSON + `}]}`
		if _, fails := checkPlanWith(json.RawMessage(raw), nil, "", flow.Settings{TaskSizeLimit: 400}, flow.ResolveFacts{}); len(fails) != 0 {
			t.Errorf("valid title %q failed: %v", okTitle, fails)
		}
	}
	for _, badTitle := range []string{
		"", "   ", "Add a helper", "wip: not a type", "Feat: uppercase",
		"feat:no space", "feat: ", "feat(): empty scope", "feat(a b): space in scope",
	} {
		raw := `{"tasks":[{"id":"T1","title":` + strconvQuote(badTitle) + `,"estimate_lines":20,` + testPlanJSON + `}]}`
		if err := eng.Report("tok-coord", "plan apply", json.RawMessage(raw)); refusalCode(err) != "plan_invalid" {
			t.Errorf("bad title %q = %v, want plan_invalid", badTitle, err)
		}
	}
	if got, _ := LoadState(paths.State(id)); got.Seq != 1 || len(got.Tasks) != 0 {
		t.Fatalf("bad titles mutated state: seq=%d tasks=%d", got.Seq, len(got.Tasks))
	}

	// Valid apply cleans and stores scope fields.
	validPlan := `{"tasks":[{"id":"T1","title":"feat(factory): add scope","estimate_lines":40,` +
		`"for_whom":"owners\nusing factory","if_skipped":"no scope fence","smallest":"4 fields","not_doing":"plan\treviewer",` + testOutcomesJSON + `}]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(validPlan)); err != nil {
		t.Fatalf("valid apply: %v", err)
	}
	got, _ := LoadState(paths.State(id))
	if got.Seq != 2 || got.Tasks[0].ForWhom != "owners using factory" || got.Tasks[0].NotDoing != "plan reviewer" {
		t.Fatalf("stored scope fields = %+v", got.Tasks[0])
	}

	// plan amend: bad title on existing task, and added task missing scope or bad title -> refused, nothing changes.
	for _, amend := range []string{
		`{"why":"w","tasks":[{"id":"T1","title":"not conventional"}]}`,
		`{"why":"w","tasks":[{"id":"T2","title":"feat: two","estimate_lines":30}]}`,
		`{"why":"w","tasks":[{"id":"T2","title":"bad title","estimate_lines":30,` + testScopeJSON + `}]}`,
	} {
		if err := eng.Report("tok-coord", "plan amend", json.RawMessage(amend)); refusalCode(err) != "plan_invalid" {
			t.Errorf("amend %s = %v, want plan_invalid", amend, err)
		}
	}
	if st2, _ := LoadState(paths.State(id)); st2.Seq != 2 || len(st2.Tasks) != 1 {
		t.Fatalf("refused amend mutated state: seq=%d tasks=%d", st2.Seq, len(st2.Tasks))
	}

	// recap decide split: child missing scope or bad title -> refused naming all missing fields, nothing changes.
	got.Tasks[0].Recaps = []flow.Recap{{Trigger: "review", Allowed: []string{"split", "drop"}}}
	if err := SaveState(paths.State(id), got); err != nil {
		t.Fatal(err)
	}
	splitErr := eng.Report("tok-coord", "recap decide", json.RawMessage(
		`{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":"not conventional","estimate_lines":20,"for_whom":"u"}]}`,
	))
	if refusalCode(splitErr) != "plan_invalid" {
		t.Fatalf("bad split = %v, want plan_invalid", splitErr)
	}
	for _, want := range []string{`title "not conventional"`, `task "T1a" needs if_skipped`, `task "T1a" needs smallest`, `task "T1a" needs not_doing`} {
		if !strings.Contains(splitErr.Error(), want) {
			t.Errorf("split refusal %q missing %q", splitErr, want)
		}
	}
	if st3, _ := LoadState(paths.State(id)); st3.Seq != 2 || len(st3.Tasks) != 1 || st3.Tasks[0].Split != nil {
		t.Fatalf("refused split mutated state: %+v", st3.Tasks)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestPlanOutcomesProvedBy checks design.md 11.7 and PR 3.2: at least one
// outcome per task, each with text and a proved_by of kind cmd:, test:, or
// manual:, enforced in plan apply, plan amend (existing and added tasks) and
// split children; nothing changes on refusal; plain-string outcomes are
// rejected in plans/amends/splits.
func TestPlanOutcomesProvedBy(t *testing.T) {
	paths := testPaths(t)
	eng := New(paths, nil)
	const id = "proj-outcomes"
	st := flow.State{
		Seq:      1,
		Project:  flow.Project{ID: id, Started: true, RunTokenHash: tokenHash("tok-coord")},
		Settings: flow.DefaultSettings(),
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	// plan apply: missing outcome on A, bad outcomes on B -> one refusal naming each task and outcome; nothing changes.
	badApply := `{"tasks":[` +
		`{"id":"A","title":"feat: a","estimate_lines":30,` + testScopeJSON + `},` +
		`{"id":"B","title":"feat: b","estimate_lines":30,` + testScopeJSON + `,"outcomes":[` +
		`{"text":"first","proved_by":""},` +
		`{"text":"second","proved_by":"guess: ok"},` +
		`{"text":"third","proved_by":"cmd:   "},` +
		`{"text":"   ","proved_by":"test: TestX"}` +
		`]}` +
		`]}`
	err := eng.Report("tok-coord", "plan apply", json.RawMessage(badApply))
	if refusalCode(err) != "plan_invalid" {
		t.Fatalf("badApply = %v, want plan_invalid", err)
	}
	for _, want := range []string{
		`task "A" needs at least one outcome`,
		`task "B" outcome "first" needs proved_by`,
		`task "B" outcome "second" has unknown proved_by "guess: ok"`,
		`task "B" outcome "third" proved_by "cmd:" needs a value after cmd:`,
		`task "B" outcome 4 needs text`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q missing %q", err, want)
		}
	}
	// Plain-string outcomes are refused through checkOutcomes naming task and
	// outcome; unknown outcome fields, duplicates, and >500-rune fields are refused.
	for _, bad := range []struct{ body, want string }{
		{`{"tasks":[{"id":"A","title":"feat: a","estimate_lines":30,` + testScopeJSON + `,"outcomes":["plain string"]}]}`, `task "A" outcome "plain string" needs proved_by`},
		{`{"tasks":[{"id":"A","title":"feat: a","estimate_lines":30,` + testScopeJSON + `,"outcomes":[{"text":"a","proved_by":"cmd: true","extra":1}]}]}`, `unknown field "extra"`},
		{`{"tasks":[{"id":"A","title":"feat: a","estimate_lines":30,` + testScopeJSON + `,"outcomes":[{"text":"dup","proved_by":"cmd: true"},{"text":"dup\t","proved_by":"cmd: true"}]}]}`, `task "A" has duplicate outcome "dup"`},
		{`{"tasks":[{"id":"A","title":"feat: a","estimate_lines":30,` + testScopeJSON + `,"outcomes":[{"text":"` + strings.Repeat("x", 501) + `","proved_by":"cmd: true"}]}]}`, `task "A" outcome 1 text is longer than 500 characters`},
		{`{"tasks":[{"id":"A","title":"feat: a","estimate_lines":30,` + testScopeJSON + `,"outcomes":[{"text":"ok","proved_by":"cmd: ` + strings.Repeat("y", 496) + `"}]}]}`, `task "A" outcome "ok" proved_by is longer than 500 characters`},
	} {
		err := eng.Report("tok-coord", "plan apply", json.RawMessage(bad.body))
		if refusalCode(err) != "plan_invalid" || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("bad apply %s = %v, want plan_invalid containing %q", bad.body, err, bad.want)
		}
	}
	if got, _ := LoadState(paths.State(id)); got.Seq != 1 || len(got.Tasks) != 0 {
		t.Fatalf("refused apply mutated state: seq=%d tasks=%d", got.Seq, len(got.Tasks))
	}

	// Valid plan apply with all three proved_by kinds (cmd:, test:, manual:).
	validApply := `{"tasks":[{"id":"T1","title":"feat: greet","estimate_lines":30,` + testScopeJSON + `,"outcomes":[` +
		`{"text":"builds  cleanly","proved_by":"cmd:go build ./..."},` +
		`{"text":"greets name","proved_by":"test: TestGreet"},` +
		`{"text":"looks right","proved_by":"manual: screenshot of home screen"}` +
		`]}]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(validApply)); err != nil {
		t.Fatalf("valid apply: %v", err)
	}
	got, _ := LoadState(paths.State(id))
	wantOutcomes := []flow.Outcome{
		{Text: "builds cleanly", ProvedBy: "cmd: go build ./..."},
		{Text: "greets name", ProvedBy: "test: TestGreet"},
		{Text: "looks right", ProvedBy: "manual: screenshot of home screen"},
	}
	if len(got.Tasks) != 1 || len(got.Tasks[0].Outcomes) != 3 || got.Tasks[0].Outcomes[0] != wantOutcomes[0] || got.Tasks[0].Outcomes[1] != wantOutcomes[1] || got.Tasks[0].Outcomes[2] != wantOutcomes[2] {
		t.Fatalf("stored outcomes = %+v, want %+v", got.Tasks[0].Outcomes, wantOutcomes)
	}

	// plan amend: empty outcomes, missing/unknown proved_by, plain string on existing task, duplicate, >500 runes, and bad added task -> refused, nothing changes.
	for _, amend := range []struct {
		body string
		want string
	}{
		{`{"why":"w","tasks":[{"id":"T1","outcomes":[]}]}`, `task "T1" needs at least one outcome`},
		{`{"why":"w","tasks":[{"id":"T1","outcomes":[{"text":"x"}]}]}`, `task "T1" outcome "x" needs proved_by`},
		{`{"why":"w","tasks":[{"id":"T1","outcomes":[{"text":"x","proved_by":"shell: ls"}]}]}`, `task "T1" outcome "x" has unknown proved_by "shell: ls"`},
		{`{"why":"w","tasks":[{"id":"T1","outcomes":["plain"]}]}`, `task "T1" outcome "plain" needs proved_by`},
		{`{"why":"w","tasks":[{"id":"T1","outcomes":[{"text":"same","proved_by":"cmd: true"},{"text":"same","proved_by":"cmd: true"}]}]}`, `task "T1" has duplicate outcome "same"`},
		{`{"why":"w","tasks":[{"id":"T2","title":"feat: two","estimate_lines":30,` + testScopeJSON + `}]}`, `task "T2" needs at least one outcome`},
		{`{"why":"w","tasks":[{"id":"T2","title":"feat: two","estimate_lines":30,` + testScopeJSON + `,"outcomes":[{"text":"y","proved_by":"bogus"}]}]}`, `task "T2" outcome "y" has unknown proved_by "bogus"`},
	} {
		err := eng.Report("tok-coord", "plan amend", json.RawMessage(amend.body))
		if refusalCode(err) != "plan_invalid" || !strings.Contains(err.Error(), amend.want) {
			t.Errorf("amend %s = %v, want plan_invalid containing %q", amend.body, err, amend.want)
		}
	}
	if st2, _ := LoadState(paths.State(id)); st2.Seq != 2 || len(st2.Tasks) != 1 || len(st2.Tasks[0].Outcomes) != 3 || st2.Tasks[0].Outcomes[0] != wantOutcomes[0] {
		t.Fatalf("refused amend mutated state: seq=%d tasks=%+v", st2.Seq, st2.Tasks)
	}

	// split children: missing outcome, missing/unknown proved_by, plain string -> refused naming task & outcome, nothing changes.
	got.Tasks[0].Recaps = []flow.Recap{{Trigger: "review", Allowed: []string{"split", "drop"}}}
	if err := SaveState(paths.State(id), got); err != nil {
		t.Fatal(err)
	}
	for _, split := range []struct {
		body string
		want string
	}{
		{`{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":"feat: a","estimate_lines":20,` + testScopeJSON + `}]}`, `task "T1a" needs at least one outcome`},
		{`{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":"feat: a","estimate_lines":20,` + testScopeJSON + `,"outcomes":[{"text":"a works"}]}]}`, `task "T1a" outcome "a works" needs proved_by`},
		{`{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":"feat: a","estimate_lines":20,` + testScopeJSON + `,"outcomes":[{"text":"a works","proved_by":"other: x"}]}]}`, `task "T1a" outcome "a works" has unknown proved_by "other: x"`},
		{`{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":"feat: a","estimate_lines":20,` + testScopeJSON + `,"outcomes":["plain"]}]}`, `task "T1a" outcome "plain" needs proved_by`},
	} {
		err := eng.Report("tok-coord", "recap decide", json.RawMessage(split.body))
		if refusalCode(err) != "plan_invalid" || !strings.Contains(err.Error(), split.want) {
			t.Errorf("split %s = %v, want plan_invalid containing %q", split.body, err, split.want)
		}
	}
	if st3, _ := LoadState(paths.State(id)); st3.Seq != 2 || len(st3.Tasks) != 1 || st3.Tasks[0].Split != nil {
		t.Fatalf("refused split mutated state: seq=%d tasks=%+v", st3.Seq, st3.Tasks)
	}
}

// TestReportReadyProofs checks design.md 7.2 and PR 3.3a: ready refuses
// outcome_missing, proof_failed (naming each failing proof with first lines of
// its output, and missing artifacts), and invalid_report (unknown/duplicate/wrong
// kind); stores proofs on Ready when valid; and leaves old tasks with no
// proved_by unaffected.
func TestReportReadyProofs(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, baseSHA := initGitRepoWithRemote(t)
	eng := New(paths, nil)

	const projID = "proj-proofs"
	wt, err := world.NewWorktree(ctx, repo, paths.Worktree(projID, "T1", 1), "T1", "feat: proofs")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "p.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt.Path, "add", "p.go")
	gitIn(t, wt.Path, "commit", "-m", "feat: p")
	c1 := gitIn(t, wt.Path, "rev-parse", "HEAD")

	saveTask := func(outcomes []flow.Outcome) {
		t.Helper()
		st := flow.State{
			Seq:      1,
			Project:  flow.Project{ID: projID, Folder: repo, Started: true},
			Settings: flow.DefaultSettings(),
			Tasks: []flow.Task{{
				ID: "T1", Title: "feat: proofs", Outcomes: outcomes,
				Attempts: []flow.Attempt{{
					SizeCap: 100, BaseCommit: baseSHA, Branch: wt.Branch, Worktree: wt.Path,
					RunTokenHash: tokenHash("tok-worker"),
					Ready:        []flow.Ready{}, Rounds: []flow.Round{}, Accepts: []flow.Accept{}, Acts: []flow.Act{},
				}},
			}},
		}
		if err := SaveState(paths.State(projID), st); err != nil {
			t.Fatal(err)
		}
	}
	send := func(proofs []flow.Proof) error {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"head": c1, "proofs": proofs})
		return eng.Report("tok-worker", "ready", raw)
	}

	outcomes := []flow.Outcome{
		{Text: "builds", ProvedBy: "cmd: go build ./..."},
		{Text: "tests pass", ProvedBy: "test: TestFoo"},
		{Text: "looks right", ProvedBy: "manual: screenshot"},
	}
	saveTask(outcomes)

	gitDir := gitIn(t, wt.Path, "rev-parse", "--git-dir")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(wt.Path, gitDir)
	}
	proofDir := filepath.Join(gitDir, "sheepdog-proofs")
	_ = os.MkdirAll(proofDir, 0o700)
	okLog := filepath.Join(proofDir, "ok.log")
	failLog := filepath.Join(proofDir, "fail.log")
	artFile := filepath.Join(t.TempDir(), "shot.png")
	_ = os.WriteFile(okLog, []byte("ok\n"), 0o600)
	_ = os.WriteFile(failLog, []byte("FAIL pkg/foo\nline two error\n"), 0o600)
	_ = os.WriteFile(artFile, []byte("png"), 0o600)

	// Missing outcomes -> outcome_missing naming each missing one.
	err = send([]flow.Proof{{Outcome: "builds", Command: "go build ./...", Exit: 0, OutputPath: okLog}})
	if refusalCode(err) != "outcome_missing" || !strings.Contains(err.Error(), `"tests pass"`) || !strings.Contains(err.Error(), `"looks right"`) {
		t.Fatalf("missing proof = %v, want outcome_missing naming tests pass and looks right", err)
	}

	// Failing command proof -> proof_failed naming command, exit, and first lines of output.
	err = send([]flow.Proof{
		{Outcome: "builds", Command: "go build ./...", Exit: 0, OutputPath: okLog},
		{Outcome: "tests pass", Command: "go test ./...", Exit: 2, OutputPath: failLog},
		{Outcome: "looks right", Artifact: artFile},
	})
	if refusalCode(err) != "proof_failed" || !strings.Contains(err.Error(), `"tests pass"`) || !strings.Contains(err.Error(), "exit 2") || !strings.Contains(err.Error(), "FAIL pkg/foo") {
		t.Fatalf("failing proof = %v, want proof_failed with output head", err)
	}

	// Missing artifact -> proof_failed naming artifact.
	err = send([]flow.Proof{
		{Outcome: "builds", Command: "go build ./...", Exit: 0, OutputPath: okLog},
		{Outcome: "tests pass", Command: "go test ./...", Exit: 0, OutputPath: okLog},
		{Outcome: "looks right", Artifact: filepath.Join(t.TempDir(), "missing.png")},
	})
	if refusalCode(err) != "proof_failed" || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing artifact = %v, want proof_failed", err)
	}

	// Unknown outcome, duplicate proof, artifact on non-manual, command on manual -> invalid_report.
	for name, ps := range map[string][]flow.Proof{
		"unknown":            {{Outcome: "nope", Command: "true", Exit: 0, OutputPath: okLog}},
		"duplicate":          {{Outcome: "builds", Command: "true", Exit: 0, OutputPath: okLog}, {Outcome: "builds", Command: "true", Exit: 0, OutputPath: okLog}},
		"artifact on cmd":    {{Outcome: "builds", Artifact: artFile}},
		"command on manual":  {{Outcome: "looks right", Command: "true", Exit: 0, OutputPath: okLog}},
		"missing outputpath": {{Outcome: "builds", Command: "true", Exit: 0}},
	} {
		if err := send(ps); refusalCode(err) != "invalid_report" {
			t.Errorf("%s: err = %v, want invalid_report", name, err)
		}
	}
	if cur, _ := LoadState(paths.State(projID)); cur.Seq != 1 {
		t.Fatalf("refusals mutated state: seq=%d", cur.Seq)
	}

	// Valid proofs -> accepted and persisted on flow.Ready.
	good := []flow.Proof{
		{Outcome: "builds", Command: "go build ./...", Exit: 0, OutputPath: okLog},
		{Outcome: "tests pass", Command: "go test ./...", Exit: 0, OutputPath: okLog},
		{Outcome: "looks right", Artifact: artFile},
	}
	goodRaw, _ := json.Marshal(map[string]any{"head": c1, "proofs": good, "touched": "p.go", "skipped": ""})
	if err := eng.Report("tok-worker", "ready", goodRaw); err != nil {
		t.Fatalf("valid proofs: %v", err)
	}
	cur, _ := LoadState(paths.State(projID))
	if cur.Seq != 2 || len(cur.Tasks[0].Attempts[0].Ready) != 1 || len(cur.Tasks[0].Attempts[0].Ready[0].Proofs) != 3 ||
		cur.Tasks[0].Attempts[0].Ready[0].Proofs[2].Artifact != artFile {
		t.Fatalf("stored ready = %+v", cur.Tasks[0].Attempts[0].Ready)
	}

	// Old task whose outcomes have no proved_by still accepts ready with no proofs.
	saveTask([]flow.Outcome{{Text: "legacy outcome"}})
	if err := eng.Report("tok-worker", "ready", json.RawMessage(`{"head":"`+c1+`"}`)); err != nil {
		t.Fatalf("legacy task ready without proofs: %v", err)
	}
}

// TestReportReadyTouchedSkippedAndRawCommand checks PR 3.3b:
// touched and skipped are required with proofs (empty skipped "" is allowed),
// cleaned and capped at 500 runes, and the stored proof command is the exact
// raw text that ran.
func TestReportReadyTouchedSkippedAndRawCommand(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, baseSHA := initGitRepoWithRemote(t)
	eng := New(paths, nil)

	const projID = "proj-touched"
	wt, err := world.NewWorktree(ctx, repo, paths.Worktree(projID, "T1", 1), "T1", "feat: touched")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(wt.Path, "t.go"), []byte("package demo\n"), 0o600)
	gitIn(t, wt.Path, "add", "t.go")
	gitIn(t, wt.Path, "commit", "-m", "feat: t")
	c1 := gitIn(t, wt.Path, "rev-parse", "HEAD")

	gitDir := gitIn(t, wt.Path, "rev-parse", "--git-dir")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(wt.Path, gitDir)
	}
	proofDir := filepath.Join(gitDir, "sheepdog-proofs")
	_ = os.MkdirAll(proofDir, 0o700)
	okLog := filepath.Join(proofDir, "ok.log")
	_ = os.WriteFile(okLog, []byte("a  b\nok\n"), 0o600)

	reset := func() {
		t.Helper()
		st := flow.State{
			Seq:      1,
			Project:  flow.Project{ID: projID, Folder: repo, Started: true},
			Settings: flow.DefaultSettings(),
			Tasks: []flow.Task{{
				ID: "T1", Title: "feat: touched",
				Outcomes: []flow.Outcome{{Text: "prints", ProvedBy: "cmd: printf 'a  b'"}},
				Attempts: []flow.Attempt{{
					SizeCap: 100, BaseCommit: baseSHA, Branch: wt.Branch, Worktree: wt.Path,
					RunTokenHash: tokenHash("tok-worker"),
					Ready:        []flow.Ready{}, Rounds: []flow.Round{}, Accepts: []flow.Accept{}, Acts: []flow.Act{},
				}},
			}},
		}
		if err := SaveState(paths.State(projID), st); err != nil {
			t.Fatal(err)
		}
	}
	rawCmd := "printf 'a  b'\necho ok"
	proof := []flow.Proof{{Outcome: "prints", Command: "  " + rawCmd + "  ", Exit: 0, OutputPath: okLog}}
	send := func(body map[string]any) error {
		b, _ := json.Marshal(body)
		return eng.Report("tok-worker", "ready", b)
	}

	// Required with proofs; over-500 refused; state unchanged.
	reset()
	longStr := strings.Repeat("x", 501)
	for name, body := range map[string]map[string]any{
		"missing touched": {"head": c1, "proofs": proof, "skipped": ""},
		"blank touched":   {"head": c1, "proofs": proof, "touched": " \n\t ", "skipped": ""},
		"missing skipped": {"head": c1, "proofs": proof, "touched": "t.go"},
		"long touched":    {"head": c1, "proofs": proof, "touched": longStr, "skipped": ""},
		"long skipped":    {"head": c1, "proofs": proof, "touched": "t.go", "skipped": longStr},
	} {
		if err := send(body); refusalCode(err) != "invalid_report" {
			t.Errorf("%s: err = %v, want invalid_report", name, err)
		}
	}
	if cur, _ := LoadState(paths.State(projID)); cur.Seq != 1 {
		t.Fatalf("refusals mutated state: seq=%d", cur.Seq)
	}

	// Empty skipped "" is allowed; touched is cleaned; raw proof command preserved verbatim.
	if err := send(map[string]any{"head": c1, "proofs": proof, "touched": "t.go\n\tcallers\x1b[31m", "skipped": ""}); err != nil {
		t.Fatalf("valid ready with empty skipped: %v", err)
	}
	cur, _ := LoadState(paths.State(projID))
	got := cur.Tasks[0].Attempts[0].Ready[0]
	if got.Touched != "t.go callers [31m" || got.Skipped == nil || *got.Skipped != "" || got.Proofs[0].Command != rawCmd {
		t.Fatalf("stored ready = %+v, want cleaned touched, empty skipped, raw command %q", got, rawCmd)
	}

	// Non-empty skipped is cleaned and stored.
	reset()
	if err := send(map[string]any{"head": c1, "proofs": proof, "touched": "t.go", "skipped": "slow\te2e\ncheck"}); err != nil {
		t.Fatalf("valid ready with skipped: %v", err)
	}
	cur, _ = LoadState(paths.State(projID))
	if got := cur.Tasks[0].Attempts[0].Ready[0]; got.Skipped == nil || *got.Skipped != "slow e2e check" {
		t.Fatalf("stored skipped = %v, want cleaned", got.Skipped)
	}
}

// TestCheckOutcomesRejectsEqualsAndNewline checks item 3a: outcome text with
// '=' or a newline is refused at plan time through checkOutcomes naming task and outcome.
func TestCheckOutcomesRejectsEqualsAndNewline(t *testing.T) {
	for _, tc := range []struct {
		name, text, wantOutcome string
	}{
		{"equals", "x=1 printed", "x=1 printed"},
		{"newline", "line1\nline2", "line1 line2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"tasks":[{"id":"T1","title":"feat: x","estimate_lines":20,` + testScopeJSON + `,"outcomes":[{"text":` + strconvQuote(tc.text) + `,"proved_by":"cmd: true"}]}]}`
			_, fails := checkPlanWith(json.RawMessage(raw), nil, "", flow.Settings{TaskSizeLimit: 400}, flow.ResolveFacts{})
			got := strings.Join(fails, "; ")
			if len(fails) == 0 || !strings.Contains(got, `task "T1"`) || !strings.Contains(got, tc.wantOutcome) {
				t.Fatalf("checkPlan(%s) fails = %q, want failure naming task T1 and outcome %q", tc.name, got, tc.wantOutcome)
			}
		})
	}
}

// TestReportReadyProofCleaningCapsAndPathGuard checks item 5:
// output excerpts and command are cleaned with flow.CleanText; command,
// output_path and artifact are capped at 500 runes; proofHead reads only
// regular files inside the worktree's .git dir and never blocks on a FIFO.
func TestReportReadyProofCleaningCapsAndPathGuard(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, baseSHA := initGitRepoWithRemote(t)
	eng := New(paths, nil)

	const projID = "proj-proof-clean"
	wt, err := world.NewWorktree(ctx, repo, paths.Worktree(projID, "T1", 1), "T1", "feat: clean")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(wt.Path, "c.go"), []byte("package demo\n"), 0o600)
	gitIn(t, wt.Path, "add", "c.go")
	gitIn(t, wt.Path, "commit", "-m", "feat: c")
	c1 := gitIn(t, wt.Path, "rev-parse", "HEAD")

	st := flow.State{
		Seq:      1,
		Project:  flow.Project{ID: projID, Folder: repo, Started: true},
		Settings: flow.DefaultSettings(),
		Tasks: []flow.Task{{
			ID: "T1", Title: "feat: clean",
			Outcomes: []flow.Outcome{
				{Text: "tests pass", ProvedBy: "cmd: go test ./..."},
				{Text: "looks right", ProvedBy: "manual: screenshot"},
			},
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: baseSHA, Branch: wt.Branch, Worktree: wt.Path,
				RunTokenHash: tokenHash("tok-worker"),
				Ready:        []flow.Ready{}, Rounds: []flow.Round{}, Accepts: []flow.Accept{}, Acts: []flow.Act{},
			}},
		}},
	}
	if err := SaveState(paths.State(projID), st); err != nil {
		t.Fatal(err)
	}

	gitDir := gitIn(t, wt.Path, "rev-parse", "--git-dir")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(wt.Path, gitDir)
	}
	proofDir := filepath.Join(gitDir, "sheepdog-proofs")
	if err := os.MkdirAll(proofDir, 0o700); err != nil {
		t.Fatal(err)
	}
	artFile := filepath.Join(t.TempDir(), "shot.png")
	_ = os.WriteFile(artFile, []byte("png"), 0o600)

	send := func(proofs []flow.Proof) error {
		raw, _ := json.Marshal(map[string]any{"head": c1, "proofs": proofs})
		return eng.Report("tok-worker", "ready", raw)
	}

	// 1. ANSI/control bytes in failing output (inside .git) and command are cleaned.
	dirtyLog := filepath.Join(proofDir, "dirty.log")
	_ = os.WriteFile(dirtyLog, []byte("bad \x1b[31mred\x1b[0m\x00line\n"), 0o600)
	err = send([]flow.Proof{
		{Outcome: "tests pass", Command: "go\x1b[31m\ntest", Exit: 1, OutputPath: dirtyLog},
		{Outcome: "looks right", Artifact: artFile},
	})
	if refusalCode(err) != "proof_failed" || strings.ContainsAny(err.Error(), "\x1b\x00\n") || !strings.Contains(err.Error(), "bad [31mred [0m line") || !strings.Contains(err.Error(), "go [31m test") {
		t.Fatalf("dirty proof refusal = %q, want cleaned command and excerpt", err)
	}

	// 2. Fields longer than 500 runes are refused invalid_report.
	longStr := strings.Repeat("a", 501)
	for name, ps := range map[string][]flow.Proof{
		"long command":     {{Outcome: "tests pass", Command: longStr, Exit: 0, OutputPath: dirtyLog}, {Outcome: "looks right", Artifact: artFile}},
		"long output_path": {{Outcome: "tests pass", Command: "true", Exit: 0, OutputPath: longStr}, {Outcome: "looks right", Artifact: artFile}},
		"long artifact":    {{Outcome: "tests pass", Command: "true", Exit: 0, OutputPath: dirtyLog}, {Outcome: "looks right", Artifact: longStr}},
	} {
		if err := send(ps); refusalCode(err) != "invalid_report" {
			t.Errorf("%s: err = %v, want invalid_report", name, err)
		}
	}

	// 3. Output path outside worktree's .git is never read.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(outside, []byte("TOP_SECRET_LINE\n"), 0o600)
	err = send([]flow.Proof{
		{Outcome: "tests pass", Command: "false", Exit: 1, OutputPath: outside},
		{Outcome: "looks right", Artifact: artFile},
	})
	if refusalCode(err) != "proof_failed" || strings.Contains(err.Error(), "TOP_SECRET_LINE") || !strings.Contains(err.Error(), "(output unreadable)") {
		t.Fatalf("outside output_path = %v, want (output unreadable) without leaking file", err)
	}

	// 4. FIFO inside .git does not block os.Open.
	fifo := filepath.Join(proofDir, "hang.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- send([]flow.Proof{
			{Outcome: "tests pass", Command: "false", Exit: 1, OutputPath: fifo},
			{Outcome: "looks right", Artifact: artFile},
		})
	}()
	select {
	case err := <-done:
		if refusalCode(err) != "proof_failed" || !strings.Contains(err.Error(), "(output unreadable)") {
			t.Fatalf("fifo output_path = %v, want proof_failed with (output unreadable)", err)
		}
	case <-time.After(300 * time.Millisecond):
		// Unblock the leaked goroutine by opening the write end so the test exits cleanly.
		if wf, oerr := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); oerr == nil {
			_ = wf.Close()
		}
		t.Fatal("proofHead blocked on FIFO output_path")
	}
}

// TestLoadStateAllowsUnknownOutcomeFields checks item 6: DisallowUnknownFields
// applies only to new plans, not when loading state.json.
func TestLoadStateAllowsUnknownOutcomeFields(t *testing.T) {
	paths := testPaths(t)
	raw := `{"seq":1,"project":{"id":"proj-future","started":true},"tasks":[{"id":"T1","title":"feat: x","outcomes":[{"text":"works","proved_by":"cmd: true","future_field":123}]}]}`
	statePath := paths.State("proj-future")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState failed on unknown outcome field: %v", err)
	}
	if len(st.Tasks) != 1 || len(st.Tasks[0].Outcomes) != 1 || st.Tasks[0].Outcomes[0].Text != "works" {
		t.Fatalf("loaded tasks = %+v", st.Tasks)
	}
}

// TestParseReplyLinesTolerance checks tolerant reply-line parsing across
// Claude and Codex shapes: leading ⏺ and bullets, **…** and backticks,
// trailing periods, leading-integer and "none" self-check misses, and
// ignoring indented tool-output lines (⎿, └, │ and their continuations).
func TestParseReplyLinesTolerance(t *testing.T) {
	t.Run("variants", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			body   string
			majors int
			misses int
			hasM   bool
			badM   bool
			class  string
			del    string
			notRun string
		}{
			{
				name: "annotated misses and trailing periods",
				body: "⏺ major: nil deref.\n◦ **major: missing check**\n`major: race`\n" +
					"**self-check misses: 1 (missed nil check).**\n" +
					"`class: nil guards.`\n" +
					"**deletable: none**\n" +
					"• not run: live e2e.",
				majors: 3, misses: 1, hasM: true, class: "nil guards", del: "none", notRun: "live e2e",
			},
			{
				name:   "zero with trailing period and backtick",
				body:   "self-check misses: 0.\n`deletable: none`",
				misses: 0, hasM: true, del: "none",
			},
			{
				name:   "trailing unmatched backtick on misses",
				body:   "self-check misses: 0`\ndeletable: none.",
				misses: 0, hasM: true, del: "none",
			},
			{
				name:   "none misses means zero",
				body:   "self-check misses: none\ndeletable: none",
				misses: 0, hasM: true, del: "none",
			},
			{
				name:   "claude second block opens with self-check misses",
				body:   "Bash(git diff)\n  ⎿  +func foo() {}\n\n⏺ self-check misses: 0\nclass: none\ndeletable: none\nnot run: none",
				misses: 0, hasM: true, class: "none", del: "none", notRun: "none",
			},
			{
				name: "indented tool output never counts as reply lines",
				body: "Bash(cat fixture.txt)\n" +
					"  ⎿  major: from claude tool\n" +
					"     self-check misses: 9\n" +
					"     deletable: from claude tool\n" +
					"     class: from claude tool\n" +
					"     not run: from claude tool\n" +
					"• Explored\n" +
					"  └ Read fixture.go\n" +
					"    major: from codex tool\n" +
					"    deletable: from codex tool\n" +
					"  │ self-check misses: 8\n" +
					"  │ deletable: from box\n",
				majors: 0, misses: 0, hasM: false,
			},
			{
				name:   "codex multi-line command then output",
				body:   "• Ran printf a\n  │ b\n  └ deletable: from codex output\n    major: from codex output\n• self-check misses: 0\n• deletable: none",
				misses: 0, hasM: true, del: "none",
			},
			{
				name: "negative misses still bad",
				body: "self-check misses: -2\ndeletable: none",
				hasM: true, badM: true, del: "none",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := parseReplyLines(tc.body)
				if got.majors != tc.majors || got.misses != tc.misses || got.hasMisses != tc.hasM || got.badMisses != tc.badM ||
					got.class != tc.class || got.deletable != tc.del || got.notRun != tc.notRun {
					t.Fatalf("parseReplyLines = %+v, want majors=%d misses=%d hasM=%v badM=%v class=%q del=%q notRun=%q",
						got, tc.majors, tc.misses, tc.hasM, tc.badM, tc.class, tc.del, tc.notRun)
				}
			})
		}
	})

	t.Run("real claude pass opening with self-check misses", func(t *testing.T) {
		ctx := context.Background()
		srv := hctest.New(t)
		srv.Add(hc.Terminal{Address: "t-cl", Name: "rev-cl", State: "live", Status: &hc.Status{App: "claude-code", State: "done"}})
		srv.SetScreen("t-cl", "❯ Review round 1\n  <report>{\"code\":\"c123\",\"round\":1,\"head\":\"h1\",\"verdict\":\"pass|changes\",\"majors\":<n>}</report>\n\n"+
			"⏺ Bash(git diff)\n"+
			"  ⎿  deletable: fake in diff\n"+
			"     major: fake in diff\n\n"+
			"⏺ self-check misses: 0\nclass: none\ndeletable: none\nnot run: none\n"+
			`<report>{"code":"c123","round":1,"head":"h1","verdict":"pass","majors":0}</report>`+"\n")
		rd, err := world.ReadReport(ctx, hc.NewClient(srv.Path), "t-cl", "c123")
		if err != nil || rd.Status != world.ReportFound {
			t.Fatalf("ReadReport = (%+v, %v), want found", rd, err)
		}
		rl := parseReplyLines(rd.Body)
		if !rl.hasMisses || rl.badMisses || rl.misses != 0 || rl.majors != 0 || rl.deletable != "none" {
			t.Fatalf("claude pass parseReplyLines = %+v (body=%q)", rl, rd.Body)
		}
	})

	t.Run("real claude table right before the reply lines", func(t *testing.T) {
		ctx := context.Background()
		srv := hctest.New(t)
		srv.Add(hc.Terminal{Address: "t-tb", Name: "rev-tb", State: "live", Status: &hc.Status{App: "claude-code", State: "done"}})
		srv.SetScreen("t-tb", "❯ Review round 1\n\n"+
			"⏺ Bash(cat notes.txt)\n"+
			"  ⎿  deletable: fake in tool output\n\n"+
			"⏺ Checked:\n"+
			"  ┌──────┬────────┐\n"+
			"  │ file │ result │\n"+
			"  ├──────┼────────┤\n"+
			"  │ a.go │ ok     │\n"+
			"  └──────┴────────┘\n"+
			"  self-check misses: 0\n"+
			"  deletable: none\n"+
			`<report>{"code":"c789","round":1,"head":"h1","verdict":"pass","majors":0}</report>`+"\n")
		rd, err := world.ReadReport(ctx, hc.NewClient(srv.Path), "t-tb", "c789")
		if err != nil || rd.Status != world.ReportFound {
			t.Fatalf("ReadReport = (%+v, %v), want found", rd, err)
		}
		rl := parseReplyLines(rd.Body)
		if !rl.hasMisses || rl.badMisses || rl.misses != 0 || rl.majors != 0 || rl.deletable != "none" {
			t.Fatalf("table reply parseReplyLines = %+v (body=%q)", rl, rd.Body)
		}
	})

	t.Run("real claude tool output with its own bullets", func(t *testing.T) {
		ctx := context.Background()
		srv := hctest.New(t)
		srv.Add(hc.Terminal{Address: "t-bu", Name: "rev-bu", State: "live", Status: &hc.Status{App: "claude-code", State: "done"}})
		srv.SetScreen("t-bu", "❯ Review round 1\n\n"+
			"⏺ Bash(cat NOTES.md)\n"+
			"  ⎿  intro\n"+
			"     • major: fake bullet in tool output\n"+
			"     ⏺ deletable: fake in tool output\n\n"+
			"⏺ self-check misses: 0\n"+
			"  deletable: none\n"+
			`<report>{"code":"c790","round":1,"head":"h1","verdict":"pass","majors":0}</report>`+"\n")
		rd, err := world.ReadReport(ctx, hc.NewClient(srv.Path), "t-bu", "c790")
		if err != nil || rd.Status != world.ReportFound {
			t.Fatalf("ReadReport = (%+v, %v), want found", rd, err)
		}
		if rl := parseReplyLines(rd.Body); !rl.hasMisses || rl.majors != 0 || rl.deletable != "none" {
			t.Fatalf("bulleted tool output parseReplyLines = %+v (body=%q)", rl, rd.Body)
		}
	})

	t.Run("real codex wrapped command then output", func(t *testing.T) {
		ctx := context.Background()
		srv := hctest.New(t)
		srv.Add(hc.Terminal{Address: "t-wr", Name: "rev-wr", State: "live"})
		srv.SetScreen("t-wr", "› Review round 1\n\n"+
			"• Ran git diff 80d9857def34945bd538faf6a162296bc4c2a5a8..657ce5090879ae2be234dc0f\n"+
			"  72bf952084c252e6\n"+
			"  └ +diff\n"+
			"    major: fake from diff\n"+
			"    • deletable: fake from diff\n\n"+
			"• self-check misses: 0\n"+
			"• deletable: none\n\n"+
			`  <report>{"code":"c791","round":1,"head":"h1","verdict":"pass","majors":0}</report>`+"\n")
		rd, err := world.ReadCodexReport(ctx, hc.NewClient(srv.Path), "t-wr", "c791")
		if err != nil || rd.Status != world.ReportFound {
			t.Fatalf("ReadCodexReport = (%+v, %v), want found", rd, err)
		}
		if rl := parseReplyLines(rd.Body); !rl.hasMisses || rl.majors != 0 || rl.deletable != "none" {
			t.Fatalf("wrapped codex command parseReplyLines = %+v (body=%q)", rl, rd.Body)
		}
	})

	t.Run("real codex changes with tool block and bold lines", func(t *testing.T) {
		ctx := context.Background()
		srv := hctest.New(t)
		srv.Add(hc.Terminal{Address: "t-cx", Name: "rev-cx", State: "live"})
		srv.SetScreen("t-cx", "› Review round 2\n  <report>{\"code\":\"c456\",\"round\":2,\"head\":\"h1\",\"verdict\":\"pass|changes\",\"majors\":<n>}</report>\n\n"+
			"• Explored\n"+
			"  └ Read fixture.go\n"+
			"    major: fake from file\n"+
			"    deletable: fake from file\n\n"+
			"• **major: nil deref in Foo.**\n"+
			"• **self-check misses: 1 (missed nil check)**\n"+
			"• `deletable: none.`\n\n"+
			`  <report>{"code":"c456","round":2,"head":"h1","verdict":"changes","majors":1}</report>`+"\n")
		rd, err := world.ReadCodexReport(ctx, hc.NewClient(srv.Path), "t-cx", "c456")
		if err != nil || rd.Status != world.ReportFound {
			t.Fatalf("ReadCodexReport = (%+v, %v), want found", rd, err)
		}
		rl := parseReplyLines(rd.Body)
		if !rl.hasMisses || rl.badMisses || rl.misses != 1 || rl.majors != 1 || rl.deletable != "none" {
			t.Fatalf("codex changes parseReplyLines = %+v (body=%q)", rl, rd.Body)
		}
	})
}

// TestVerdictRefusalTellsReviewerOnce checks that a refused verdict sends the
// refusal reason to the reviewer run once so it can fix and re-send, never
// loops on repeated refusals, and accepts the corrected reply.
func TestVerdictRefusalTellsReviewerOnce(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, baseSHA := initGitRepoWithRemote(t)
	eng := New(paths, client)

	const projID = "proj-refuse-tell"
	wt, err := world.NewWorktree(ctx, repo, paths.Worktree(projID, "T1", 1), "T1", "feat: x")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "x.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt.Path, "add", "x.go")
	gitIn(t, wt.Path, "commit", "-m", "feat: x")
	c1 := gitIn(t, wt.Path, "rev-parse", "HEAD")

	pastMS := uint64(time.Now().Add(-time.Minute).UnixMilli())
	srv.Add(hc.Terminal{Address: "t-rev", Name: "rev-1", State: "live", Status: &hc.Status{App: "claude-code", State: "done"}, StatusMS: &pastMS})
	st := flow.State{
		Seq:      1,
		Project:  flow.Project{ID: projID, Folder: repo, Started: true, RunID: "t-coord"},
		Settings: flow.DefaultSettings(),
		Tasks: []flow.Task{{
			ID: "T1", Title: "feat: x",
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: baseSHA, Branch: wt.Branch, Worktree: wt.Path,
				Ready: []flow.Ready{{Head: c1}},
				Review: &flow.ReviewRun{
					RunID: "t-rev", Round: 1, Head: c1, Code: "code1",
					Reviewer: flow.Reviewer{CLI: "claude", Model: "opus", Effort: "medium"},
				},
			}},
		}},
	}
	if err := SaveState(paths.State(projID), st); err != nil {
		t.Fatal(err)
	}

	// First refusal: missing deletable line -> reviewer is told why once.
	srv.SetScreen("t-rev", "❯ Review round 1\n\n⏺ Looks good.\nself-check misses: 0\n"+
		`<report>{"code":"code1","round":1,"head":"`+c1+`","verdict":"pass","majors":0}</report>`+"\n")
	eng.passProject(ctx, projID, false)
	sends := sendsTo(t, srv.Requests(), "t-rev")
	if len(sends) != 1 || !strings.Contains(sends[0], "deletable:") {
		t.Fatalf("after first refusal sends = %q, want 1 send mentioning deletable:", sends)
	}

	// Second pass with same invalid screen, and third with a different invalid screen: never sent again.
	eng.passProject(ctx, projID, false)
	srv.SetScreen("t-rev", "❯ Your verdict was refused\n\n⏺ Still missing misses.\ndeletable: none\n"+
		`<report>{"code":"code1","round":1,"head":"`+c1+`","verdict":"pass","majors":0}</report>`+"\n")
	eng.passProject(ctx, projID, false)
	if got := sendsTo(t, srv.Requests(), "t-rev"); len(got) != 1 {
		t.Fatalf("repeated refusals sent %d times, want 1: %q", len(got), got)
	}

	// Reviewer fixes its reply and re-sends: accepted right away.
	srv.SetScreen("t-rev", "❯ Your verdict was refused\n\n⏺ self-check misses: 0\ndeletable: none\n"+
		`<report>{"code":"code1","round":1,"head":"`+c1+`","verdict":"pass","majors":0}</report>`+"\n")
	eng.passProject(ctx, projID, false)
	cur, err := LoadState(paths.State(projID))
	if err != nil {
		t.Fatal(err)
	}
	if a := cur.Tasks[0].Attempts[0]; a.Review != nil || len(a.Rounds) != 1 || a.Rounds[0].Verdict != flow.VerdictPass {
		t.Fatalf("after fixed reply: review=%+v rounds=%+v", a.Review, a.Rounds)
	}
}

func TestReviewerPromptSelfCheckMissesWording(t *testing.T) {
	if !strings.Contains(reviewerTemplate, "self-check misses: <a number only>") {
		t.Fatalf("reviewer.md missing 'self-check misses: <a number only>':\n%s", reviewerTemplate)
	}
}
