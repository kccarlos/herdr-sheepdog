package web

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

var (
	_ factorySettings  = (*factory.Engine)(nil)
	_ http.HandlerFunc = (&Server{}).handleFactorySettings
)

// TestFactorySettingsRoutes verifies GET /api/factory/settings, GET/POST
// /api/factory/projects/{id}/settings, proof_required, and paired propose + Save.
func TestFactorySettingsRoutes(t *testing.T) {
	f := newBrowserEnrolFixture(t, nil)
	s0 := flow.DefaultSettings()
	s0.MaxRunning = 1
	if err := factory.SaveState(f.paths.State("proj-demo"), flow.State{
		Seq: 1, Settings: s0, Project: flow.Project{ID: "proj-demo", Folder: filepath.Join(f.dir, "repo"), Goal: "demo", Started: true},
	}); err != nil {
		t.Fatal(err)
	}
	for _, req := range [][2]string{{http.MethodGet, "/api/factory/settings"}, {http.MethodGet, "/api/factory/projects/proj-demo/settings"}, {http.MethodPost, "/api/factory/projects/proj-demo/settings"}} {
		if w := f.doRemote(req[0], req[1], nil, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("remote %s %s = %d, want 401", req[0], req[1], w.Code)
		}
	}
	wDef := f.doLoopback(http.MethodGet, "/api/factory/settings", nil, "")
	var rowsResp struct {
		Protocol int               `json:"protocol"`
		Rows     []flow.SettingRow `json:"rows"`
	}
	if wDef.Code != http.StatusOK || json.Unmarshal(wDef.Body.Bytes(), &rowsResp) != nil ||
		rowsResp.Protocol != 1 || len(rowsResp.Rows) != len(flow.DefaultSettings().Rows()) {
		t.Fatalf("GET /api/factory/settings = %d %s", wDef.Code, wDef.Body.String())
	}
	if w := f.doLoopback(http.MethodGet, "/api/factory/projects/missing/settings", nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown project GET settings = %d, want 404", w.Code)
	}
	if w := f.doLoopback(http.MethodGet, "/api/factory/projects/proj-demo/settings", nil, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"max_running"`) {
		t.Fatalf("project GET settings = %d %s", w.Code, w.Body.String())
	}
	wNoTicket := f.doLoopback(http.MethodPost, "/api/factory/projects/proj-demo/settings", map[string]string{"name": "max_running", "value": "2"}, "")
	if wNoTicket.Code != http.StatusForbidden || !strings.Contains(wNoTicket.Body.String(), "proof_required") || len(f.eng.Asks(true)) != 0 {
		t.Fatalf("ticketless POST = %d %s (asks=%d), want 403 proof_required", wNoTicket.Code, wNoTicket.Body.String(), len(f.eng.Asks(true)))
	}
	_, ticket := f.enrollAndSignInApp("Test Browser", DeviceBrowser)
	if w := f.doLoopback(http.MethodPost, "/api/factory/projects/proj-demo/settings", map[string]string{"name": "review_rounds", "value": "0"}, ticket); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "review rounds 0: allowed 1–5") {
		t.Fatalf("bad value POST = %d %s, want 400 review rounds 0: allowed 1–5", w.Code, w.Body.String())
	}
	if w := f.doLoopback(http.MethodPost, "/api/factory/projects/proj-demo/settings", map[string]string{"name": "max_running", "value": "2"}, ticket); w.Code != http.StatusOK {
		t.Fatalf("paired POST = %d %s, want 200", w.Code, w.Body.String())
	}
	asks := f.eng.Asks(true)
	if len(asks) != 1 || asks[0].Question != "This project: max running 2 (now 1). Save?" || asks[0].Set == nil {
		t.Fatalf("opened setting ask = %+v", asks)
	}
	if w := f.doLoopback(http.MethodPost, "/api/factory/asks/"+asks[0].ID+"/answer", map[string]string{"option": flow.RuleOptionSave}, ticket); w.Code != http.StatusOK {
		t.Fatalf("POST answer Save = %d %s, want 200", w.Code, w.Body.String())
	}
	if st, err := factory.LoadState(f.paths.State("proj-demo")); err != nil || st.Settings.MaxRunning != 2 {
		t.Fatalf("saved MaxRunning = %d (err=%v), want 2", st.Settings.MaxRunning, err)
	}
}

func TestFactorySettingsBlankProjectIDReturns404(t *testing.T) {
	f := newBrowserEnrolFixture(t, nil)
	if w := f.doLoopback(http.MethodGet, "/api/factory/projects/%20/settings", nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("GET /api/factory/projects/%%20/settings = %d %s, want 404", w.Code, w.Body.String())
	}
}

func TestFactorySettingsModelsRouteRefusesUnlaunchable(t *testing.T) {
	f := newBrowserEnrolFixture(t, nil)
	if err := factory.SaveState(f.paths.State("proj-demo"), flow.State{
		Seq: 1, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "proj-demo", Folder: filepath.Join(f.dir, "repo"), Goal: "demo", Started: true},
	}); err != nil {
		t.Fatal(err)
	}
	wDef := f.doLoopback(http.MethodGet, "/api/factory/settings", nil, "")
	if wDef.Code != http.StatusOK || !strings.Contains(wDef.Body.String(), `"models.worker"`) || !strings.Contains(wDef.Body.String(), `"clis"`) {
		t.Fatalf("GET /api/factory/settings = %d %s, want models.worker with clis", wDef.Code, wDef.Body.String())
	}
	_, ticket := f.enrollAndSignInApp("Test Browser", DeviceBrowser)
	wBad := f.doLoopback(http.MethodPost, "/api/factory/projects/proj-demo/settings", map[string]string{"name": "models.retro", "value": "codex/gpt-6.2-sol/medium"}, ticket)
	if wBad.Code != http.StatusBadRequest || !strings.Contains(wBad.Body.String(), `"not_launchable"`) || !strings.Contains(wBad.Body.String(), "the engine cannot launch codex as retro yet") {
		t.Fatalf("POST models.retro=codex = %d %s, want 400 not_launchable", wBad.Code, wBad.Body.String())
	}
}
