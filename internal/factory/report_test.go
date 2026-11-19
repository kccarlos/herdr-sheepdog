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
	tok := st.Project.RunToken
	if len(tok) < 20 || got.Env["SHEEPDOG_RUN_TOKEN"] != tok || got.Env["SHEEPDOG_WEB_PORT"] != "17777" {
		t.Fatalf("run env = %v, state token %q", got.Env, tok)
	}
	logFile, _ := os.ReadFile(eng.Paths.Log(id))
	for where, text := range map[string]string{"argv": strings.Join(got.Argv, " "), "log.jsonl": string(logFile), "daemon log": logBuf.String()} {
		if strings.Contains(text, tok) {
			t.Fatalf("token leaked into %s: %s", where, text)
		}
	}
	if err := eng.Stop(repo); err != nil {
		t.Fatal(err)
	}
	if st, _ := LoadState(eng.Paths.State(id)); st.Project.RunToken != "" {
		t.Fatalf("stopped project kept its run token")
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
	coord := flow.State{Seq: 1, Project: flow.Project{ID: "proj-a", Started: true, RunToken: "tok-coord-a"}}
	worker := flow.State{Seq: 1, Project: flow.Project{ID: "proj-b", Started: true, RunToken: "tok-coord-b"},
		Tasks: []flow.Task{{ID: "T1", Title: "feat: x", Attempts: []flow.Attempt{{RunToken: "tok-worker-b"}}}}}
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
		code              string
		why               []string
	}{
		{"made-up token", "tok-made-up", "plan apply", plan, "invalid_token", nil},
		{"no token", "", "plan apply", plan, "invalid_token", nil},
		{"worker sends plan apply", "tok-worker-b", "plan apply", plan, "kind_not_allowed", nil},
		{"run's terminal starts", "tok-coord-a", "start", nil, "kind_not_allowed", nil},
		{"run's terminal stops", "tok-worker-b", "stop", nil, "kind_not_allowed", nil},
		{"unknown verb", "tok-coord-a", "merge", nil, "kind_not_allowed", nil},
		{"every shape failure", "tok-coord-a", "plan apply", bad, "plan_invalid",
			[]string{`unknown field "estimate"`, `"C" is after "Z", which is not in the plan`, `cycle through "A"`, `task "C" has no title`}},
		{"another project's plan exists", "tok-coord-b", "plan apply", plan, "plan_exists", nil},
	} {
		err := eng.Report(c.token, c.verb, c.args)
		if refusalCode(err) != c.code {
			t.Fatalf("%s: err = %v, want %s", c.name, err, c.code)
		}
		for _, w := range c.why {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q does not list %q", c.name, err, w)
			}
		}
		if !strings.Contains(logBuf.String(), c.code) {
			t.Errorf("%s: refusal not logged with its code", c.name)
		}
	}
	for _, id := range []string{"proj-a", "proj-b"} {
		if st, _ := LoadState(eng.Paths.State(id)); st.Seq != 1 {
			t.Fatalf("a refusal changed %s: seq %d", id, st.Seq)
		}
	}
	if strings.Contains(logBuf.String(), "tok-") {
		t.Fatalf("a token was logged: %s", logBuf.String())
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
