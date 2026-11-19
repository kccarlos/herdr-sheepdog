package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
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
