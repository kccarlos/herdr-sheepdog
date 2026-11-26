package supervisor

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// Pins app-factory.md §3.2 (D2): project name (last folder segment, fallback to
// id) replaces factory:<id> / id in sayOf/aliasOf, ask lines ("notes, T1: …"),
// push titles, voice stop/start, and blocked coordinator ("the notes factory").
func TestProjectNameInSupervisorNamesAsksVoiceControlAndBlockedCoordinator(t *testing.T) {
	const projID, noFolderID = "proj-1a2b3c4d", "proj-nofolder"
	paths, hcSrv := factory.Paths{DataDir: t.TempDir()}, hctest.New(t)
	hcSrv.OnExec = hctest.GitExec
	notesDir := filepath.Join(t.TempDir(), "notes")
	for _, args := range [][]string{{"init", "-b", "main", notesDir}, {"-C", notesDir, "config", "--local", "sheepdog.projectId", projID}} {
		if b, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
	}
	for _, st := range []flow.State{
		{Seq: 2, Settings: flow.DefaultSettings(), Project: flow.Project{ID: projID, Folder: notesDir, Goal: "Notes", Started: true}, Asks: []flow.Ask{
			{ID: "ask-t1", Project: projID, Kind: flow.KindGate, Gated: true, Question: "Delete old notes?", Options: []string{"Delete", "Keep"}, Recommend: "Keep", Task: "T1", At: t0, Status: flow.AskStatusOpen},
			{ID: "ask-rel", Project: projID, Kind: flow.KindRelease, Gated: true, Question: "Tag v1.0?", Options: []string{"Tag", "Wait"}, Recommend: "Wait", At: t0.Add(time.Second), Status: flow.AskStatusOpen},
		}},
		{Seq: 1, Settings: flow.DefaultSettings(), Project: flow.Project{ID: noFolderID, Goal: "No folder"}},
	} {
		if err := factory.SaveState(paths.State(st.Project.ID), st); err != nil {
			t.Fatal(err)
		}
	}
	eng := factory.New(paths, hc.NewClient(hcSrv.Path))
	eng.StartCheck = func(context.Context, factory.DoctorOptions) []onboard.Result { return nil }
	fn := &fakeNotifier{}
	h := newHarness(t, withNotifier(fn), func(c *Config) { c.Factory = eng; c.Chattiness = string(Chatty) })
	for lbl, got := range map[string]string{"sayOf": h.sup.sayOf(factoryPanePrefix + projID), "aliasOf": h.sup.aliasOf(factoryPanePrefix + projID)} {
		if !strings.Contains(strings.ToLower(got), "notes") || strings.Contains(got, projID) {
			t.Errorf("%s(factory:%s) = %q, want notes (never id)", lbl, projID, got)
		}
	}
	if got := h.sup.sayOf(factoryPanePrefix + noFolderID); !strings.Contains(got, noFolderID) {
		t.Errorf("sayOf(factory:%s) = %q, want fallback id", noFolderID, got)
	}
	startFactorySupervisor(t, h)
	if p, _ := waitFactoryPush(t, h, factoryAskPrefix+"ask-t1"); !strings.HasPrefix(p.Text, "notes, T1:") || strings.Contains(p.Text, projID) {
		t.Errorf("task ask line = %q, want prefix %q", p.Text, "notes, T1:")
	}
	if p, _ := waitFactoryPush(t, h, factoryAskPrefix+"ask-rel"); !strings.HasPrefix(p.Text, "notes:") || strings.Contains(p.Text, projID) {
		t.Errorf("project ask line = %q, want prefix %q", p.Text, "notes:")
	}
	waitFor(t, func() bool { return len(fn.all()) >= 2 }, "ask APNs pushes")
	for _, n := range fn.all() {
		if !strings.Contains(strings.ToLower(n.Agent), "notes") || strings.Contains(n.Agent, projID) {
			t.Errorf("ask push Agent = %q, want notes (never id)", n.Agent)
		}
	}
	h.clock.Advance(time.Second)
	if r := h.say("stop the factory"); !strings.Contains(r.Spoken, "notes") || strings.Contains(r.Spoken, projID) {
		t.Errorf("voice stop = %q, want notes (never id)", r.Spoken)
	}
	h.clock.Advance(time.Second)
	if r := h.say("start the factory"); !strings.Contains(r.Spoken, "notes") || strings.Contains(r.Spoken, projID) {
		t.Errorf("voice start = %q, want notes (never id)", r.Spoken)
	}
	const coordPane, coordName = "hc:coord-1", "coord-1a2b"
	labels := map[string]string{"sheepdog.project": projID, "sheepdog.role": flow.RoleCoordinator}
	h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "ws-notes", Label: "notes"})
	h.srv.SetAgent(herdr.AgentInfo{PaneID: coordPane, WorkspaceID: "ws-notes", Agent: "claude", Name: coordName, Status: herdr.StateWorking, Cwd: notesDir, Labels: labels})
	bp, bn := len(h.pusher.all()), len(fn.all())
	h.srv.SetScreen(coordPane, fixture(t, "codex-approval-prompt.screen.txt"))
	sendPaneLabeledTransition(h, coordPane, coordName, herdr.StateWorking, herdr.StateBlocked, labels)
	if ps := h.pusher.all()[bp:]; len(ps) == 0 || !strings.Contains(strings.ToLower(ps[0].Text), "the notes factory") || strings.Contains(ps[0].Text, coordName) || strings.Contains(ps[0].Text, projID) {
		t.Errorf("blocked coordinator spoken = %+v, want %q", ps, "the notes factory")
	}
	if ns := fn.all()[bn:]; len(ns) == 0 || !strings.Contains(strings.ToLower(ns[0].Agent), "notes") || strings.Contains(ns[0].Agent, coordName) || strings.Contains(ns[0].Agent, projID) {
		t.Errorf("blocked coordinator push Agent = %+v, want notes", ns)
	}
}

// Pins PR 1 fix round (§3.2 D2): a blocked coordinator never leaks its raw
// coord-<hex> terminal name in voice.push agent tags, SpokenNames,
// revivePrompt summaries, or checkQuestion summaries.
func TestBlockedCoordinatorNameInPushTagReviveReconcileAndSpokenNames(t *testing.T) {
	const projID, coordPane, coordName = "proj-1a2b3c4d", "hc:coord-1", "coord-1a2b3c4d"
	paths, hcSrv := factory.Paths{DataDir: t.TempDir()}, hctest.New(t)
	hcSrv.OnExec = hctest.GitExec
	notesDir := filepath.Join(t.TempDir(), "notes")
	for _, args := range [][]string{{"init", "-b", "main", notesDir}, {"-C", notesDir, "config", "--local", "sheepdog.projectId", projID}} {
		if b, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
	}
	if err := factory.SaveState(paths.State(projID), flow.State{
		Seq: 1, Settings: flow.DefaultSettings(),
		Project: flow.Project{ID: projID, Folder: notesDir, Goal: "Notes", Started: true, RunID: coordPane, Run: &flow.RunRecord{Role: flow.RoleCoordinator, State: flow.PhaseWorking}},
	}); err != nil {
		t.Fatal(err)
	}
	eng := factory.New(paths, hc.NewClient(hcSrv.Path))
	eng.StartCheck = func(context.Context, factory.DoctorOptions) []onboard.Result { return nil }
	h := newHarness(t, func(c *Config) { c.Factory = eng; c.Chattiness = string(Chatty) })

	labels := map[string]string{"sheepdog.project": projID, "sheepdog.role": flow.RoleCoordinator}
	h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "ws-notes", Label: "notes"})
	h.srv.SetAgent(herdr.AgentInfo{PaneID: coordPane, WorkspaceID: "ws-notes", Agent: "claude", Name: coordName, Status: herdr.StateWorking, Cwd: notesDir, Labels: labels})
	h.srv.SetScreen(coordPane, fixture(t, "codex-approval-prompt.screen.txt"))
	sendPaneLabeledTransition(h, coordPane, coordName, herdr.StateWorking, herdr.StateBlocked, labels)

	h.pusher.mu.Lock()
	var pushAgents []string
	for _, tags := range h.pusher.tags {
		if ag, _ := tags["agent"].(string); ag != "" {
			pushAgents = append(pushAgents, ag)
		}
	}
	h.pusher.mu.Unlock()
	if len(pushAgents) == 0 || !strings.Contains(strings.ToLower(pushAgents[0]), "notes") || strings.Contains(strings.ToLower(pushAgents[0]), "coord") || strings.Contains(pushAgents[0], "1a2b3c4d") {
		t.Errorf("voice.push agent tags = %q, want project name (never %q)", pushAgents, coordName)
	}

	spoken := h.sup.SpokenNames()
	hasNotes := false
	for _, n := range spoken {
		if strings.Contains(strings.ToLower(n), "notes") {
			hasNotes = true
		}
		if strings.Contains(strings.ToLower(n), "coord") || strings.Contains(n, "1a2b3c4d") {
			t.Errorf("SpokenNames() leaked raw coordinator name: %q", spoken)
		}
	}
	if !hasNotes {
		t.Errorf("SpokenNames() = %q, want entry naming notes", spoken)
	}

	h.sup.mu.Lock()
	_ = h.sup.store.ClearPending()
	_ = h.sup.store.DropQuestion(coordPane)
	p, revived, ok, why := h.sup.revivePrompt(coordPane, "test", &paneScreen{text: fixture(t, "codex-approval-prompt.screen.txt")})
	h.sup.mu.Unlock()
	if !revived || !ok {
		t.Fatalf("revivePrompt = (revived=%v ok=%v why=%q), want revived", revived, ok, why)
	}
	if !strings.Contains(strings.ToLower(p.SpokenSummary), "the notes factory") || strings.Contains(strings.ToLower(p.SpokenSummary), "coord") || strings.Contains(p.SpokenSummary, "1a2b3c4d") {
		t.Errorf("revivePrompt SpokenSummary = %q, want %q (never coord-<hex>)", p.SpokenSummary, "the notes factory")
	}

	h.sup.mu.Lock()
	_ = h.sup.store.SetQuestion(p)
	h.sup.mu.Unlock()
	h.srv.SetScreen(coordPane, fixture(t, "claude-permission-prompt.screen.txt"))
	row, _ := h.sup.store.Get(coordPane)
	h.sup.checkQuestion(context.Background(), row, p)
	var gotSummary string
	for _, q := range h.sup.store.Questions() {
		if q.PaneID == coordPane {
			gotSummary = q.SpokenSummary
		}
	}
	if !strings.Contains(strings.ToLower(gotSummary), "the notes factory") || strings.Contains(strings.ToLower(gotSummary), "coord") || strings.Contains(gotSummary, "1a2b3c4d") {
		t.Errorf("checkQuestion SpokenSummary = %q, want %q (never coord-<hex>)", gotSummary, "the notes factory")
	}
}
