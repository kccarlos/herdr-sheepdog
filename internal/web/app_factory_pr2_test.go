package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/termproto"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

func newFactoryWindowHarness(t *testing.T) (*harness, *fakeTerm, *factory.Engine, factory.Paths) {
	t.Helper()
	t0, paths := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC), factory.Paths{DataDir: t.TempDir()}
	save := func(st flow.State) {
		st.Seq, st.Settings = 2, flow.DefaultSettings()
		if err := factory.SaveState(paths.State(st.Project.ID), st); err != nil {
			t.Fatal(err)
		}
	}
	save(flow.State{
		Project: flow.Project{ID: "proj-1a2b3c4d", Folder: "/work/notes", Goal: "Notes", Started: true, RunID: "coord-notes-1",
			Run: &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", Address: "coord-notes-1", State: flow.PhaseIdle}},
		Tasks: []flow.Task{
			{ID: "T1", Title: "worker", Attempts: []flow.Attempt{{BaseCommit: "0", RunID: "worker-t1", Run: &flow.RunRecord{Role: flow.RoleWorker, CLI: "codex", Address: "worker-t1", State: flow.PhaseWorking}}}},
			{ID: "T2", Title: "reviewer", Attempts: []flow.Attempt{{BaseCommit: "0", RunID: "w-2", Ready: []flow.Ready{{Head: "a"}}, Review: &flow.ReviewRun{RunID: "rev-t2", Round: 1, Kind: flow.RoundFull, Head: "a", Run: &flow.RunRecord{Role: flow.RoleReviewer, CLI: "claude", Address: "rev-t2", State: flow.PhaseWorking}}}}},
		},
		Asks: []flow.Ask{{ID: "ask-1", Project: "proj-1a2b3c4d", Kind: flow.KindGate, Gated: true, Task: "T1", Question: "Delete old notes?", Options: []string{"Delete", "Keep"}, Recommend: "Keep", At: t0, Status: flow.AskStatusOpen}},
	})
	save(flow.State{Project: flow.Project{ID: "proj-ended", Folder: "/work/ended", Goal: "Ended", Started: true, RunID: "coord-ended", Run: &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", Address: "coord-ended", State: flow.PhaseEnded}}})
	save(flow.State{Project: flow.Project{ID: "proj-blocked", Folder: "/work/blocked", Goal: "Blocked", Started: true, RunID: "coord-blocked-1", Run: &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", Address: "coord-blocked-1", State: flow.PhaseBlocked}}})
	eng, ft := factory.New(paths, nil), &fakeTerm{version: "0.9.5"}
	h := newHarness(t, harnessOpts{term: ft.deps(), factory: eng, screenInterval: 50 * time.Millisecond, screenMinGap: 10 * time.Millisecond})
	h.srv.deps.Factory = eng
	h.herdr.SetScreen("coord-notes-1", "claude", "Coordinator ready\n\x1b[1;32mnotes\x1b[0m", herdr.StateIdle)
	h.herdr.SetScreen("worker-t1", "codex", "worker screen", herdr.StateWorking)
	h.herdr.SetScreen("rev-t2", "claude", "reviewer screen", herdr.StateWorking)
	h.herdr.SetScreen("coord-ended", "claude", "ended screen", herdr.StateIdle)
	h.herdr.SetScreen("coord-blocked-1", "claude", codexScreen(t), herdr.StateBlocked)
	return h, ft, eng, paths
}

// Pins app-factory.md D10 & §4: /term open succeeds for a project's current
// coordinator pane (no store row needed) and refuses worker/reviewer/ended panes.
func TestTermCoordinatorLiveWindowAndRefusesWorkerReviewer(t *testing.T) {
	h, ft, _, _ := newFactoryWindowHarness(t)
	c := dialTerm(t, h, true)
	c.send(openMsg(1, "coord-notes-1", termproto.ModeControl))
	if m, _ := c.next(); m.Type != termproto.TypeOpened || m.Ch != 1 || m.PaneID != "coord-notes-1" || m.Mode != termproto.ModeControl {
		t.Errorf("term-open(coord-notes-1) = %+v, want term-opened", m)
	} else {
		c.expectFrame()
		c.input(1, "why T1?\r")
		b := ft.bridge(t, 0)
		waitFor(t, 2*time.Second, func() bool { return len(b.inputs()) == 1 }, "coordinator bridge input")
		if string(b.inputs()[0]) != "why T1?\r" {
			t.Errorf("coordinator bridge input = %q", b.inputs()[0])
		}
	}
	for i, pane := range []string{"worker-t1", "rev-t2", "coord-ended"} {
		c.send(openMsg(10+i, pane, termproto.ModeControl))
		if m := c.expect(termproto.TypeClosed); m.Reason != termproto.RefusedNotTracked {
			t.Errorf("term-open(%s) = %+v, want refused:not_tracked", pane, m)
		}
	}
}

// Pins app-factory.md D10 & §4: screen read (Screen, ScreenANSI, /ws) and typed
// input (WindowPrompt, WindowKeys) reach the coordinator without a store row,
// owner_say, or ask proof; worker/reviewer stay refused; Send while blocked at
// its own dialog is refused (409) while keys still reach the dialog.
func TestCoordinatorScreenWindowInputNoOwnerSayNoProofAndBlockedRefusal(t *testing.T) {
	h, _, eng, paths := newFactoryWindowHarness(t)
	var scr, ansiScr ScreenResponse
	if code := h.getJSON(windowPath("coord-notes-1", "screen"), &scr); code != http.StatusOK {
		t.Errorf("GET screen(coord) = %d, want 200", code)
	} else if name := strings.ToLower(scr.Spoken + " " + scr.Alias); scr.PaneID != "coord-notes-1" || scr.Kind != "claude" ||
		!strings.Contains(scr.Text, "Coordinator ready") || strings.Contains(scr.Text, "\x1b") || !strings.Contains(name, "notes") || !strings.Contains(name, "coordinator") {
		t.Errorf("coord screen = %+v", scr)
	}
	if code := h.getJSON(windowPath("coord-notes-1", "screen")+"?ansi=1", &ansiScr); code != http.StatusOK || !ansiScr.ANSI || !strings.Contains(ansiScr.Text, "\x1b[1;32m") {
		t.Errorf("GET screen?ansi=1(coord) = %d %+v", code, ansiScr)
	}
	for _, pane := range []string{"worker-t1", "rev-t2", "coord-ended"} {
		c1 := h.getJSON(windowPath(pane, "screen"), nil)
		c2, _ := h.postJSON(windowPath(pane, "input"), map[string]any{"text": "hi"}, nil)
		c3, _ := h.postJSON(windowPath(pane, "input"), map[string]any{"keys": []string{"esc"}}, nil)
		if c1 != 404 || c2 != 404 || c3 != 404 {
			t.Errorf("pane %s screen/text/keys = (%d,%d,%d), want 404", pane, c1, c2, c3)
		}
	}
	var res map[string]any
	if code, body := h.postJSON(windowPath("coord-notes-1", "input"), map[string]any{"text": "Delete"}, &res); code != http.StatusOK || res["kind"] != "prompt" {
		t.Errorf("WindowPrompt(coord) = %d %s, want 200 prompt", code, body)
	}
	if code, body := h.postJSON(windowPath("coord-notes-1", "input"), map[string]any{"keys": []string{"1"}}, &res); code != http.StatusOK || res["kind"] != "keys" {
		t.Errorf("WindowKeys(coord) = %d %s, want 200 keys", code, body)
	}
	st, err := factory.LoadState(paths.State("proj-1a2b3c4d"))
	_, logErr := os.Stat(paths.Log("proj-1a2b3c4d"))
	_, proofErr := os.Stat(filepath.Join(paths.DataDir, "factory", "proofs.json"))
	if err != nil || len(st.Project.Wakes) != 0 || !errors.Is(logErr, os.ErrNotExist) || !errors.Is(proofErr, os.ErrNotExist) {
		t.Errorf("side effects: wakes=%v err=%v logErr=%v proofErr=%v", st.Project.Wakes, err, logErr, proofErr)
	}
	if asks, _, inStore := eng.Asks(true), 0, func() bool { _, ok := h.store.Get("coord-notes-1"); return ok }(); len(asks) != 1 || asks[0].Status != flow.AskStatusOpen || inStore {
		t.Errorf("asks=%+v inStore=%v, want ask-1 open and no store row", asks, inStore)
	}
	checkBlocked := func(pane string) {
		t.Helper()
		var errBody map[string]string
		if code, body := h.postJSON(windowPath(pane, "input"), map[string]any{"text": "hold on"}, &errBody); code != http.StatusConflict || !strings.Contains(errBody["error"], "waiting at a prompt") {
			t.Errorf("WindowPrompt(blocked %s) = %d %s, want 409 waiting at a prompt", pane, code, body)
		}
		for _, ev := range h.log.Tail(0) {
			if ev.Type == "herdr.send" && ev.PaneID == pane && strings.Contains(string(ev.Payload), `"prompt"`) {
				t.Errorf("blocked %s received prompt send: %s", pane, ev.Payload)
			}
		}
		if code, body := h.postJSON(windowPath(pane, "input"), map[string]any{"keys": []string{"down"}}, &res); code != http.StatusOK || res["kind"] != "keys" {
			t.Errorf("WindowKeys(blocked %s) = %d %s, want 200 keys", pane, code, body)
		}
	}
	checkBlocked("coord-blocked-1") // blocked in engine, no store row
	h.herdr.SetScreen("coord-blocked-2", "claude", codexScreen(t), herdr.StateBlocked)
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "coord-blocked-2", From: herdr.StateWorking, To: herdr.StateBlocked, At: time.Now(), Agent: "claude", Name: "coord-blocked-2", WorkspaceID: "ws-b", WorkspaceLabel: "blocked", Labels: map[string]string{"sheepdog.project": "proj-blocked", "sheepdog.role": flow.RoleCoordinator}})
	checkBlocked("coord-blocked-2") // blocked with store row
	ws, _ := readyClient(t, h)
	ws.send(wsproto.ScreenSubscribe("coord-notes-1"))
	if m, _, err := ws.read(time.Second); err != nil || m.Type != wsproto.TypeScreen || m.Screen == nil || m.Screen.PaneID != "coord-notes-1" {
		t.Errorf("/ws screen(coord) = (%+v, err=%v), want screen for coord-notes-1", m, err)
	}
}
