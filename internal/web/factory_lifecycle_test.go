package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

type webProjWire struct {
	ID, State, RepoURL string
	Live               bool
}

func webExpect(t *testing.T, ok bool, m string, a ...any) {
	t.Helper()
	if !ok {
		t.Errorf(m, a...)
	}
}

func setupWebFactory(t *testing.T, origin string) (*browserEnrolFixture, *hctest.Server, string) {
	t.Helper()
	f, hcSrv := newBrowserEnrolFixture(t, nil), hctest.New(t)
	hcSrv.OnExec = hctest.GitExec
	eng := factory.New(f.paths, hc.NewClient(hcSrv.Path))
	eng.Now, eng.StartCheck = f.eng.Now, func(context.Context, factory.DoctorOptions) []onboard.Result { return nil }
	f.eng, f.srv.deps.Factory = eng, eng
	repo := filepath.Join(t.TempDir(), "repo")
	for _, args := range [][]string{{"init", "-b", "main", repo}, {"-C", repo, "remote", "add", "origin", origin}} {
		if b, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
	}
	return f, hcSrv, repo
}

func getProjectsWire(t *testing.T, f *browserEnrolFixture) []webProjWire {
	t.Helper()
	w := f.doLoopback(http.MethodGet, "/api/factory/projects", nil, "")
	var resp struct {
		Projects []webProjWire `json:"projects"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &resp) != nil {
		t.Fatalf("GET /api/factory/projects = %d %s", w.Code, w.Body.String())
	}
	return resp.Projects
}

// TestFactoryStateTableCLIRoutes verifies the project state table and
// --max-running refusal through the CLI loopback routes POST /api/factory/{start,stop}.
func TestFactoryStateTableCLIRoutes(t *testing.T) {
	f, hcSrv, repo := setupWebFactory(t, "https://github.com/o/r.git")
	post := func(p string, b any) (int, string) {
		w := f.doLoopback(http.MethodPost, p, b, "")
		return w.Code, w.Body.String()
	}
	c, s := post("/api/factory/stop", map[string]any{"project": "missing"})
	webExpect(t, c != http.StatusOK && strings.Contains(s, "not_found"), "CLI stop(none) = %d %s", c, s)
	c, s = post("/api/factory/start", map[string]any{"folder": repo, "goal": "Goal 1", "max_running": 2})
	webExpect(t, c == http.StatusOK, "CLI first start = %d %s", c, s)
	ps := getProjectsWire(t, f)
	webExpect(t, len(ps) == 1 && ps[0].State == "running" && ps[0].RepoURL == "https://github.com/o/r", "after start: %+v", ps)
	id := ps[0].ID
	c, s = post("/api/factory/start", map[string]any{"folder": repo, "goal": "Goal 1", "max_running": 5})
	webExpect(t, c != http.StatusOK && strings.Contains(s, "Change max running in the settings panel on the factory page."), "CLI diff max_running = %d %s", c, s)
	c, _ = post("/api/factory/stop", map[string]any{"project": id})
	webExpect(t, c == http.StatusOK && getProjectsWire(t, f)[0].State == "stopped", "CLI running stop: %d %+v", c, getProjectsWire(t, f))
	c, _ = post("/api/factory/stop", map[string]any{"project": id})
	webExpect(t, c == http.StatusOK && getProjectsWire(t, f)[0].State == "stopped", "CLI stopped stop: %d %+v", c, getProjectsWire(t, f))
	c, _ = post("/api/factory/start", map[string]any{"folder": repo, "goal": "Goal 1"})
	webExpect(t, c == http.StatusOK && getProjectsWire(t, f)[0].State == "running", "CLI resume: %d %+v", c, getProjectsWire(t, f))
	hcSrv.Add(hc.Terminal{Address: "coord-cli", Name: "sd-" + id + "-coord", State: "live", Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator, "sheepdog.run": "r-cli"}})
	st, _ := factory.LoadState(f.paths.State(id))
	st.Project.Finished, st.Project.RunID = true, "coord-cli"
	_ = factory.SaveState(f.paths.State(id), st)
	c, s = post("/api/factory/stop", map[string]any{"project": id})
	webExpect(t, c == http.StatusOK && getProjectsWire(t, f)[0].State == "finished" && !getProjectsWire(t, f)[0].Live, "CLI finished live stop = %d %s view=%+v", c, s, getProjectsWire(t, f))
	c, s = post("/api/factory/start", map[string]any{"folder": repo, "goal": "Goal 1"})
	webExpect(t, c != http.StatusOK && strings.Contains(s, "This project is finished. Give it a new goal."), "CLI finished same goal = %d %s", c, s)
	c, s = post("/api/factory/start", map[string]any{"folder": repo, "goal": "Goal 2"})
	webExpect(t, c == http.StatusOK && getProjectsWire(t, f)[0].State == "running", "CLI finished reopen = %d %s", c, s)
}

// TestFactoryOwnerPageRoutesAndTickets_KB_ASK_19_KB_SET_2 verifies POST
// /api/factory/projects/{check,start,stop}: ticketless check, 403 proof_required,
// run token kind_not_allowed, {name, message}, ac.refused.set, and device_id log.
func TestFactoryOwnerPageRoutesAndTickets_KB_ASK_19_KB_SET_2(t *testing.T) {
	f, _, repo := setupWebFactory(t, "https://github.com/o/r.git")
	routes := []string{"/api/factory/projects/check", "/api/factory/projects/start", "/api/factory/projects/stop"}
	for _, p := range routes {
		webExpect(t, f.doRemote(http.MethodPost, p, map[string]any{"folder": repo}, "").Code == http.StatusUnauthorized, "remote %s want 401", p)
	}
	f.eng.StartCheck = func(context.Context, factory.DoctorOptions) []onboard.Result {
		return []onboard.Result{{Name: "git version", Status: onboard.OK, Detail: "git 2.45"}, {Name: "start claude", Status: onboard.Fail, Detail: "claude is not signed in", Fix: "run: claude auth login"}}
	}
	wc := f.doLoopback(http.MethodPost, routes[0], map[string]any{"folder": repo}, "")
	var chk map[string][]any
	webExpect(t, wc.Code == http.StatusOK && json.Unmarshal(wc.Body.Bytes(), &chk) == nil && len(chk["results"]) == 2 && len(f.eng.Projects()) == 0, "ticketless check = %d %s (projs=%d)", wc.Code, wc.Body.String(), len(f.eng.Projects()))
	w := f.doLoopback(http.MethodPost, routes[0], map[string]any{"folder": repo, "set": map[string]string{"max_running": "0"}}, "")
	webExpect(t, w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), `"name":"max_running"`) && strings.Contains(w.Body.String(), "1–20"), "check bad set = %d %s", w.Code, w.Body.String())
	for _, p := range routes[1:] {
		w := f.doLoopback(http.MethodPost, p, map[string]any{"folder": repo, "goal": "g", "project": "p1"}, "")
		webExpect(t, w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "proof_required"), "ticketless %s = %d %s", p, w.Code, w.Body.String())
	}
	devID, ticket := f.enrollAndSignInApp("Test Browser", DeviceBrowser)
	for _, p := range routes {
		w := f.doLoopback(http.MethodPost, p, map[string]any{"folder": repo, "goal": "g", "project": "p1", "token": "tok-run"}, ticket)
		webExpect(t, w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "kind_not_allowed"), "run token on %s = %d %s", p, w.Code, w.Body.String())
	}
	w = f.doLoopback(http.MethodPost, routes[1], map[string]any{"folder": repo, "goal": "g", "set": map[string]string{"max_running": "0"}}, ticket)
	webExpect(t, w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), `"name":"max_running"`) && strings.Contains(w.Body.String(), "1–20") && len(f.eng.Projects()) == 0, "start bad set = %d %s", w.Code, w.Body.String())
	w = f.doLoopback(http.MethodPost, routes[1], map[string]any{"folder": repo, "goal": "g"}, ticket)
	webExpect(t, w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "claude auth login") && strings.Contains(w.Body.String(), `"results"`) && len(f.eng.Projects()) == 0, "start failed check = %d %s", w.Code, w.Body.String())
	f.eng.StartCheck = func(context.Context, factory.DoctorOptions) []onboard.Result { return nil }
	if w = f.doLoopback(http.MethodPost, routes[1], map[string]any{"folder": repo, "goal": "g", "set": map[string]string{"max_running": "2"}}, ticket); w.Code != http.StatusOK {
		t.Fatalf("paired start = %d %s, want 200", w.Code, w.Body.String())
	}
	id := getProjectsWire(t, f)[0].ID
	w = f.doLoopback(http.MethodPost, routes[1], map[string]any{"folder": repo, "goal": "g", "set": map[string]string{"max_running": "5"}}, ticket)
	webExpect(t, w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "Change max running in the settings panel on the factory page."), "paired start diff set = %d %s", w.Code, w.Body.String())
	w = f.doLoopback(http.MethodPost, routes[2], map[string]any{"project": id}, ticket)
	logBytes, _ := json.Marshal(f.log.Tail(100))
	webExpect(t, w.Code == http.StatusOK && strings.Contains(string(logBytes), devID), "paired stop = %d %s (dev=%q logged=%v)", w.Code, w.Body.String(), devID, strings.Contains(string(logBytes), devID))
	st, _ := factory.LoadState(f.paths.State(id))
	st.Settings.ReviewRounds = 0
	_ = factory.SaveState(f.paths.State(id), st)
	w = f.doLoopback(http.MethodPost, routes[1], map[string]any{"folder": repo, "goal": "g"}, ticket)
	webExpect(t, w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "Not started: a setting is not allowed. Change it in settings below.") && !strings.Contains(w.Body.String(), "review_rounds"), "stored bad setting = %d %s", w.Code, w.Body.String())
}
