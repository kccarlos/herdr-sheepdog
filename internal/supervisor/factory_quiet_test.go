package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

func (h *harness) addLabeledAgent(pane, workspace, kind string, st herdr.State, labels map[string]string) {
	h.t.Helper()
	h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: workspace, Label: workspace})
	h.srv.SetAgent(herdr.AgentInfo{PaneID: pane, WorkspaceID: workspace, TabID: workspace + ":t1", Agent: kind, Status: st, Cwd: "/work/" + pane, Labels: labels})
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: pane, To: st, At: h.clock.Now(), Agent: kind, WorkspaceID: workspace, WorkspaceLabel: workspace, Labels: labels})
}

// TestFactoryQuietRule verifies that factory terminals carrying worker,
// reviewer, checks, helper, or task labels trigger zero announce turns, zero
// voice pushes (even silent ones), zero pending prompt cards, and zero phone
// notifications across starts, blocks, finishes, exits, and closes — even at
// Chatty with an orchestrator, an open promise, and an owed instruction — while
// non-factory terminals and coordinators still speak and notify as before.
func TestFactoryQuietRule(t *testing.T) {
	tests := []struct {
		name      string
		labels    map[string]string
		wantQuiet bool
	}{
		{
			name:      "factory worker",
			labels:    map[string]string{herdr.LabelFactoryProject: "proj", herdr.LabelFactoryRole: "worker", herdr.LabelFactoryTask: "proj#1"},
			wantQuiet: true,
		},
		{
			name:      "factory reviewer",
			labels:    map[string]string{herdr.LabelFactoryProject: "proj", herdr.LabelFactoryRole: "reviewer", herdr.LabelFactoryTask: "proj#1"},
			wantQuiet: true,
		},
		{
			name:      "worker-spawned reviewer without project",
			labels:    map[string]string{herdr.LabelFactoryRole: "reviewer", herdr.LabelFactoryTask: "proj#1"},
			wantQuiet: true,
		},
		{
			name:      "factory checks pane",
			labels:    map[string]string{herdr.LabelFactoryProject: "proj", herdr.LabelFactoryRole: "checks"},
			wantQuiet: true,
		},
		{
			name:      "factory helper pane",
			labels:    map[string]string{herdr.LabelFactoryRole: "helper", herdr.LabelFactoryTask: "proj#1"},
			wantQuiet: true,
		},
		{
			name:      "task label without role",
			labels:    map[string]string{herdr.LabelFactoryTask: "proj#1"},
			wantQuiet: true,
		},
		{
			name:      "factory coordinator",
			labels:    map[string]string{herdr.LabelFactoryProject: "proj", herdr.LabelFactoryRole: "coordinator"},
			wantQuiet: false,
		},
		{
			name:      "non-factory terminal",
			labels:    map[string]string{"herdr.space": "s1", "herdr.tab": "t1"},
			wantQuiet: false,
		},
	}

	for _, withBrain := range []bool{false, true} {
		for _, tc := range tests {
			suffix := "no orchestrator"
			if withBrain {
				suffix = "orchestrator"
			}
			t.Run(tc.name+" ("+suffix+")", func(t *testing.T) {
				fn := &fakeNotifier{}
				var model *braintest.Model
				var h *harness
				opts := []func(*Config){
					withNotifier(fn),
					func(c *Config) { c.Chattiness = string(Chatty) },
				}
				if withBrain {
					model = braintest.NewModel()
					model.EventDefault = announceReply("Agent update.", "idle", "", "", false)
					h = newBrainHarness(t, model, opts...)
				} else {
					h = newHarness(t, opts...)
				}

				const pane = "w5:p1"
				h.addLabeledAgent(pane, "w5", "codex", herdr.StateIdle, tc.labels)

				// Even an open promise and an owed instruction must not break
				// the factory quiet rule.
				if _, err := h.sup.createPromise(pane, brain.PromiseRequest{Text: "tell me when done", Event: brain.PromiseEventDone}); err != nil {
					t.Fatal(err)
				}
				h.send(pane, "run tests")
				h.transition(pane, herdr.StateIdle, herdr.StateWorking)

				// Block on an approval prompt.
				h.block(pane, "codex-approval-prompt.screen.txt")
				if tc.wantQuiet {
					if p, has := h.pending(); has && p.Kind == state.PendingAnswerPrompt {
						t.Fatalf("factory-quiet pane installed an answer_prompt pending action: %+v", p)
					}
					if qs := h.store.Questions(); len(qs) != 0 {
						t.Fatalf("factory-quiet pane recorded questions: %+v", qs)
					}
					if got := h.sup.openDecisions(); len(got) != 0 {
						t.Fatalf("factory-quiet pane leaked into openDecisions: %v", got)
					}
					if !h.sup.PaneMuted(pane) {
						t.Fatal("PaneMuted = false for factory-quiet pane even with Mutes == nil")
					}
				} else {
					if p, has := h.pending(); !has || p.Kind != state.PendingAnswerPrompt {
						t.Fatalf("non-quiet pane did not install an answer_prompt pending action: %+v (has=%v)", p, has)
					}
				}

				// Work and finish (screen ending in a prompt would normally trigger announceFinishAsking).
				h.transition(pane, herdr.StateBlocked, herdr.StateWorking)
				h.srv.SetScreen(pane, fixture(t, "codex-after-approval.screen.txt"))
				h.transition(pane, herdr.StateWorking, herdr.StateDone)

				// Reconcile reprompt must not spin on a blocked factory pane.
				h.srv.SetScreen(pane, fixture(t, "codex-approval-prompt.screen.txt"))
				h.transition(pane, herdr.StateDone, herdr.StateBlocked)
				h.sup.reconcileReprompts(context.Background())

				// Exit while busy (StateUnknown, Released, Closed).
				h.transition(pane, herdr.StateBlocked, herdr.StateWorking)
				h.sup.HandleTransition(context.Background(), herdr.Transition{
					PaneID: pane, From: herdr.StateWorking, To: herdr.StateUnknown,
					At: h.clock.Now(), Agent: "codex", WorkspaceID: "w5", Labels: tc.labels,
				})
				h.addLabeledAgent("w5:p2", "w5", "claude", herdr.StateWorking, tc.labels)
				h.sup.HandleTransition(context.Background(), herdr.Transition{
					PaneID: "w5:p2", From: herdr.StateWorking, To: herdr.StateUnknown,
					At: h.clock.Now(), Agent: "claude", WorkspaceID: "w5", Closed: true, Labels: tc.labels,
				})

				if tc.wantQuiet {
					if model != nil && len(model.Calls()) != 0 {
						t.Fatalf("factory-quiet pane ran %d orchestrator turn(s), want 0", len(model.Calls()))
					}
					if pushes := h.pusher.all(); len(pushes) != 0 {
						t.Fatalf("factory-quiet pane emitted %d voice push(es): %+v", len(pushes), pushes)
					}
					if notes := fn.all(); len(notes) != 0 {
						t.Fatalf("factory-quiet pane emitted %d phone notification(s): %+v", len(notes), notes)
					}
					var quietReasonCount int
					for _, e := range h.log.ofType("supervisor.push_quiet") {
						if payload(e)["reason"] == quietReasonFactory {
							quietReasonCount++
						}
					}
					if quietReasonCount == 0 {
						t.Error("expected supervisor.push_quiet events with reason factory_quiet")
					}
					if row, ok := h.store.Get(pane); !ok || row.LastResult == nil || row.LastResult.Spoken != doneResult {
						t.Errorf("last_result not recorded for quiet finish: %+v (ok=%v)", row.LastResult, ok)
					}
				} else {
					if model != nil && len(model.Calls()) == 0 {
						t.Error("non-quiet pane ran 0 orchestrator turns")
					}
					if len(h.pusher.all()) == 0 {
						t.Error("non-quiet pane emitted 0 voice pushes")
					}
					if len(fn.all()) == 0 {
						t.Error("non-quiet pane emitted 0 phone notifications")
					}
				}
			})
		}
	}
}

// TestFactoryQuietSurvivesRestart verifies that factory labels persisted in
// state.json continue to silence transitions after a daemon restart.
func TestFactoryQuietSurvivesRestart(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Chattiness = string(Chatty) })
	lbls := map[string]string{herdr.LabelFactoryProject: "proj", herdr.LabelFactoryRole: "worker", herdr.LabelFactoryTask: "proj#2"}
	h.addLabeledAgent("w5:p1", "w5", "codex", herdr.StateWorking, lbls)

	reopened, err := state.Open(h.store.Path(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fn := &fakeNotifier{}
	pusher := &fakePusher{}
	model := braintest.NewModel()
	model.EventDefault = announceReply("Should never speak.", "idle", "", "", false)
	b, err := brain.New(brain.Config{DataDir: h.dir, RunID: "t-restart"}, model, h.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	sup, err := New(reopened, h.client, nil, pusher, h.log, Config{
		Now: h.clock.Now, DataDir: h.dir, Brain: b, Notifier: fn, Chattiness: string(Chatty), inputGrace: -1, relayAfter: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	sup.SetVoiceConnected(true)
	t.Cleanup(sup.shutdown)

	// Transition with nil labels (as after restart before first snapshot).
	sup.HandleTransition(context.Background(), herdr.Transition{
		PaneID: "w5:p1", From: herdr.StateWorking, To: herdr.StateDone, At: h.clock.Now(), Agent: "codex",
	})
	if len(model.Calls()) != 0 || len(pusher.all()) != 0 || len(fn.all()) != 0 {
		t.Fatalf("restarted factory pane leaked: brain=%d pushes=%d notes=%d", len(model.Calls()), len(pusher.all()), len(fn.all()))
	}
}

// TestFactoryQuietEndToEndFromHerdrCore exercises real hc.Terminal labels
// flowing through herdrcore -> herdr.Watcher -> Supervisor.HandleTransition.
func TestFactoryQuietEndToEndFromHerdrCore(t *testing.T) {
	srv := hctest.New(t)
	srv.Add(hc.Terminal{
		Address:  "t1",
		Argv:     []string{"codex"},
		Cwd:      t.TempDir(),
		State:    "live",
		Reported: true,
		Status:   &hc.Status{State: "working", App: "codex"},
		Labels: map[string]string{
			herdr.LabelFactoryProject: "sheepdog",
			herdr.LabelFactoryRole:    "worker",
			herdr.LabelFactoryTask:    "sheepdog#23",
		},
	})
	client := hc.NewClient(srv.Path)
	log := &recLog{}
	backend := herdrcore.New(client, log)
	store, err := state.Open("", state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fn := &fakeNotifier{}
	pusher := &fakePusher{}
	model := braintest.NewModel()
	model.EventDefault = announceReply("Worker spoke.", "idle", "", "", false)
	b, err := brain.New(brain.Config{DataDir: t.TempDir(), RunID: "t-e2e"}, model, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	sup, err := New(store, backend, nil, pusher, log, Config{
		Chattiness: string(Chatty), Brain: b, Notifier: fn, inputGrace: -1, relayAfter: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	sup.SetVoiceConnected(true)
	t.Cleanup(sup.shutdown)

	w := herdr.NewWatcher(backend, herdr.WatcherConfig{
		Debounce:         20 * time.Millisecond,
		SnapshotInterval: time.Hour,
	}, log)
	ctx, cancel := context.WithCancel(context.Background())
	wDone := make(chan error, 1)
	go func() { wDone <- w.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-wDone })

	// Wait for initial transition.
	select {
	case tr := <-w.Transitions():
		sup.HandleTransition(ctx, tr)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial transition")
	}

	// Drive blocked -> done -> finished through hc.
	srv.SetStatus("t1", &hc.Status{State: "blocked", App: "codex"})
	select {
	case tr := <-w.Transitions():
		sup.HandleTransition(ctx, tr)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for blocked transition")
	}
	srv.SetStatus("t1", &hc.Status{State: "done", App: "codex"})
	select {
	case tr := <-w.Transitions():
		sup.HandleTransition(ctx, tr)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for done transition")
	}

	if len(model.Calls()) != 0 {
		t.Fatalf("brain called %d times for factory worker", len(model.Calls()))
	}
	if len(pusher.all()) != 0 {
		t.Fatalf("voice pushes = %+v, want 0", pusher.all())
	}
	if len(fn.all()) != 0 {
		t.Fatalf("phone notifications = %+v, want 0", fn.all())
	}
	if _, has := store.Pending(); has {
		t.Fatal("factory worker installed pending action")
	}
}
