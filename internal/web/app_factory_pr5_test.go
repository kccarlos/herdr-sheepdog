package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// Pins app-factory.md §6.2 & §6.3: POST /api/factory/projects/{id}/say requires
// a paired device identity, resolves by project id only, enforces empty/length
// limits, collapses line breaks, holds on stopped, refuses unknown/finished,
// and never answers an open gated ask or mints a proof.
func TestFactoryProjectSayDeviceRoute(t *testing.T) {
	f, hcSrv, repo := setupWebFactory(t, "https://github.com/o/r.git")
	if err := f.eng.Start(repo, "Goal 1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	id := getProjectsWire(t, f)[0].ID
	hcSrv.Add(hc.Terminal{Address: "coord-say", Name: "sd-" + id + "-coord", State: "live", Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator, "sheepdog.run": "r-say"}})
	st, _ := factory.LoadState(f.paths.State(id))
	st.Project.RunID = "coord-say"
	st.Project.Run = &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", Address: "coord-say", State: flow.PhaseIdle}
	st.Asks = []flow.Ask{{ID: "ask-rel", Project: id, Folder: repo, Kind: flow.KindRelease, Gated: true, Question: "Tag 0.2.0 now?", Options: []string{"Yes, tag 0.2.0", "Hold"}, Recommend: "Hold", At: time.Now(), Status: flow.AskStatusOpen}}
	if err := factory.SaveState(f.paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	sayPath := "/api/factory/projects/" + id + "/say"

	if w := f.doRemote(http.MethodPost, sayPath, map[string]any{"text": "hi"}, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("remote say = %d %s, want 401", w.Code, w.Body.String())
	}
	if w := f.doLoopback(http.MethodPost, sayPath, map[string]any{"text": "hi"}, ""); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "proof_required") {
		t.Fatalf("ticketless say = %d %s, want 403 proof_required", w.Code, w.Body.String())
	}
	_, ticket := f.enrollAndSignInApp("Test Phone", DeviceIOS)
	for _, bad := range []string{"/api/factory/projects/proj-missing/say", "/api/factory/projects/repo/say", "/api/factory/projects/..%2Frepo/say"} {
		if w := f.doLoopback(http.MethodPost, bad, map[string]any{"text": "hi"}, ticket); w.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d %s, want 404", bad, w.Code, w.Body.String())
		}
	}
	for _, badText := range []string{"   ", strings.Repeat("a", 1001)} {
		if w := f.doLoopback(http.MethodPost, sayPath, map[string]any{"text": badText}, ticket); w.Code != http.StatusBadRequest {
			t.Errorf("bad say len=%d -> %d %s, want 400", len(badText), w.Code, w.Body.String())
		}
	}
	w := f.doLoopback(http.MethodPost, sayPath, map[string]any{"text": "Yes, tag 0.2.0\nplease"}, ticket)
	var okBody map[string]any
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &okBody) != nil || okBody["ok"] != true {
		t.Fatalf("paired say = %d %s, want 200 ok", w.Code, w.Body.String())
	}
	st, _ = factory.LoadState(f.paths.State(id))
	if len(st.Project.Wakes) != 1 || !strings.HasPrefix(st.Project.Wakes[0].Key, "owner_say:") || !strings.Contains(st.Project.Wakes[0].Facts+st.Project.Wakes[0].Why, "Yes, tag 0.2.0 please") {
		t.Errorf("wakes after paired say = %+v, want 1 single-line owner_say wake", st.Project.Wakes)
	}
	if open := f.eng.Asks(true); len(open) != 1 || open[0].ID != "ask-rel" || open[0].Status != flow.AskStatusOpen {
		t.Errorf("open asks after paired say = %+v, want ask-rel still open", open)
	}
	if _, err := os.Stat(filepath.Join(f.paths.DataDir, "factory", "proofs.json")); !os.IsNotExist(err) {
		t.Errorf("paired say minted proofs file: stat err=%v", err)
	}
	if err := f.eng.Stop(id); err != nil {
		t.Fatal(err)
	}
	if w := f.doLoopback(http.MethodPost, sayPath, map[string]any{"text": "hold deploys"}, ticket); w.Code != http.StatusOK {
		t.Errorf("stopped say = %d %s, want 200", w.Code, w.Body.String())
	}
	st, _ = factory.LoadState(f.paths.State(id))
	st.Project.Finished = true
	_ = factory.SaveState(f.paths.State(id), st)
	if w := f.doLoopback(http.MethodPost, sayPath, map[string]any{"text": "too late"}, ticket); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), flow.RefuseClosed) {
		t.Errorf("finished say = %d %s, want 409 %s", w.Code, w.Body.String(), flow.RefuseClosed)
	}
}
