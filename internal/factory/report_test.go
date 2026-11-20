package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
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
	if err := eng.Start(repo, "a goal"); err != nil {
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
	if err := eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","title":"feat: x"}]}`)); err != nil {
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
	if err := eng.Start(initGitRepo(t), "a goal"); err != nil {
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
	coord := flow.State{Seq: 1, Project: flow.Project{ID: "proj-a", Started: true, RunTokenHash: tokenHash("tok-coord-a")}}
	worker := flow.State{Seq: 1, Project: flow.Project{ID: "proj-b", Started: true, RunTokenHash: tokenHash("tok-coord-b")},
		Tasks: []flow.Task{{ID: "T1", Title: "feat: x", Attempts: []flow.Attempt{{RunTokenHash: tokenHash("tok-worker-b")}}}}}
	for _, s := range []flow.State{coord, worker} {
		if err := SaveState(eng.Paths.State(s.Project.ID), s); err != nil {
			t.Fatal(err)
		}
	}
	plan := json.RawMessage(`{"tasks":[{"id":"T1","title":"feat: add a greeting helper","brief":"b","outcomes":["go test passes"],"after":[]}]}`)
	bad := json.RawMessage(`{"tasks":[{"id":"A","title":"a","after":["B"],"estimate":5},{"id":"B","title":"b","after":["A"]},{"id":"C","title":" ","after":["Z"]}]}`)

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
		{"duplicate id", `{"tasks":[{"id":"A","title":"a"},{"id":"A","title":"b"}]}`, []string{"task 2 needs an id"}},
		{"empty id is not an id", `{"tasks":[{"id":"","title":"a"},{"id":"B","title":"b","after":[""]}]}`,
			[]string{"task 1 needs an id", `"B" is after "", which is not in the plan`}},
		{"whitespace id", `{"tasks":[{"id":"  ","title":"a"}]}`, []string{"task 1 needs an id"}},
		{"empty plan", `{"tasks":[]}`, []string{"the plan has no tasks"}},
		{"no tasks key", `{}`, []string{"the plan has no tasks"}},
		{"self after", `{"tasks":[{"id":"A","title":"a","after":["A"]}]}`, []string{`cycle through "A"`}},
		{"tasks of the wrong type", `{"tasks":"A"}`, []string{"cannot unmarshal"}},
		{"ids are trimmed", `{"tasks":[{"id":" A ","title":"a"},{"id":"B","title":"b","after":[" A"]}]}`, nil},
		{"valid DAG", `{"tasks":[{"id":"A","title":"a"},{"id":"B","title":"b","after":["A"]},{"id":"C","title":"c","after":["A"]},{"id":"D","title":"d","after":["B","C"]}]}`, nil},
	} {
		tasks, fails := checkPlan(json.RawMessage(c.plan))
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
			Attempts: []flow.Attempt{{
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
	srv.Add(hc.Terminal{Address: "t-rev", Name: "rev-1", State: "live", Status: &hc.Status{App: "claude-code", State: "done"}})

	st := flow.State{
		Seq:     1,
		Project: flow.Project{ID: projID, Started: true, RunTokenHash: tokenHash("tok-coord")},
		Tasks: []flow.Task{{
			ID: "T1", Title: "feat: add parser",
			Attempts: []flow.Attempt{{
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
		{"pass with a major line", world.ReportRead{Status: world.ReportFound, Raw: "pl", Body: "major: nil deref\nminor: naming", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "pass", Majors: 0}}},
		{"majors above the major lines", world.ReportRead{Status: world.ReportFound, Raw: "ml", Body: "  major: nil deref\n  minor: naming", Report: world.Report{Code: "nonce123", Round: 1, Head: "c1", Verdict: "changes", Majors: 2}}},
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

	// Valid changes verdict records round{n:1, kind:"full", ...}, writes T1-r1.md, and stops t-rev.
	good := world.ReportRead{
		Status: world.ReportFound,
		Raw:    `{"code":"nonce123","round":1,"head":"c1","verdict":"changes","majors":4}`,
		Body:   "- major: nil deref in Parse\n**major:** missing error check\n1. major: no test\n  * major: race\nDetailed review notes line 42.",
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
		r1.Reviewer != (flow.Reviewer{CLI: "claude", Model: "opus", Effort: "medium"}) || r1.ReportPath != wantPath {
		t.Fatalf("round 1 record = %+v", r1)
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
	if rs, err := world.RunState(ctx, client, "t-rev"); err != nil || rs != flow.RunGone {
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
			Attempts: []flow.Attempt{{
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
				Attempts: []flow.Attempt{{
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
					Attempts: []flow.Attempt{{
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
			if refusalCode(err) != "nothing_to_reject" {
				t.Fatalf("reject when ladder says recap = %v, want nothing_to_reject", err)
			}
		})
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
				{ID: "T1", Title: "feat: big", Recaps: recaps, Attempts: []flow.Attempt{{RunTokenHash: tokenHash("tok-worker")}}},
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
		"missing title":    `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":" "}]}`,
		"existing id":      `{"task":"T1","choice":"split","why":"w","children":[{"id":"T0","title":"feat: dup"}]}`,
		"after missing":    `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":"feat: a","after":["NOPE"]}]}`,
		"after parent":     `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":"feat: a","after":["T1"]}]}`,
		"cycle":            `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":"feat: a","after":["T1b"]},{"id":"T1b","title":"feat: b","after":["T1a"]}]}`,
		"unknown field":    `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":"feat: a","bogus":1}]}`,
		"cycle via parent": `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":"feat: a","after":["T2"]}]}`,
	} {
		if err := eng.Report("tok-coord", "recap decide", json.RawMessage(body)); refusalCode(err) != "plan_invalid" {
			t.Errorf("%s: err = %v, want plan_invalid", name, err)
		}
	}
	if st, _ := LoadState(paths.State(id)); st.Seq != 1 {
		t.Fatalf("refusals mutated state: seq=%d", st.Seq)
	}

	// Valid split: parent marked split, children added with split_from, dependants moved.
	splitBody := `{"task":"T1","choice":"split","why":"two independent parts","children":[{"id":"T1a","title":"feat: part a","brief":"a","outcomes":["test a"],"after":["T0"]},{"id":"T1b","title":"feat: part b","brief":"b","outcomes":["test b"],"after":["T1a"]}]}`
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
