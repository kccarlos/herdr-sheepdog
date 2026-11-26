package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

func sayToolPR6(t *testing.T, h *harness, m *braintest.Model, text string, fc *genai.FunctionCall) (string, string) {
	t.Helper()
	m.Add(braintest.Step{Calls: []*genai.FunctionCall{fc}}, braintest.Step{Text: braintest.Reply("Looking into it.", "")})
	_ = h.say(text)
	for _, msg := range slices.Backward(h.sup.brain.Transcript().Messages()) {
		if msg.Kind == brain.KindToolResult && msg.ToolResult != nil {
			return msg.Content, msg.ToolResult.Error
		}
	}
	return "", ""
}

// Pins app-factory.md §3.1, §5.2, §6.5 & §9: project investigate runs default-source
// CLI in the project's folder with board text, asks and log.jsonl path (never
// touching coordinator/worker terminals or queueing Say), delivers via
// KindInvestigation with untrusted wrapping and answers_job tag, supports
// recall and follow-up resume, and leaves stopped/finished states unchanged.
func TestInvestigateProjectRunsFromRecordAndDelivers(t *testing.T) {
	root, paths := t.TempDir(), factory.Paths{DataDir: t.TempDir()}
	notesDir, recipesDir, archiveDir := filepath.Join(root, "notes"), filepath.Join(root, "recipes"), filepath.Join(root, "archive")
	for _, d := range []string{notesDir, recipesDir, archiveDir} {
		_ = os.MkdirAll(d, 0o755)
	}
	for _, st := range []flow.State{
		{Seq: 3, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "proj-1a2b3c4d", Folder: notesDir, Goal: "Notes", Started: true, RunID: "hc:coord-1",
			Run: &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", Address: "hc:coord-1", State: flow.PhaseIdle}},
			Tasks: []flow.Task{{ID: "T8", Title: "feat: tags", Brief: "tags", Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "test:go test"}}}},
			Asks: []flow.Ask{{ID: "ask-del", Project: "proj-1a2b3c4d", Folder: notesDir, Task: "T10", Kind: flow.KindDelete, Gated: true,
				Question: "Delete old notes table?", Options: []string{"Delete", "Keep"}, Recommend: "Keep", At: t0, Status: flow.AskStatusOpen}}},
		{Seq: 2, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "proj-3c4d5e6f", Folder: recipesDir, Goal: "Recipes"}},
		{Seq: 2, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "proj-4d5e6f70", Folder: archiveDir, Goal: "Old archive", Finished: true}},
	} {
		if err := factory.SaveState(paths.State(st.Project.ID), st); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range []factory.LogLine{
		{Seq: 1, At: t0, Project: "proj-1a2b3c4d", By: "coordinator", Rule: "plan.amend", Decision: "plan amended", Why: "T11 added: sync missing"},
		{Seq: 2, At: t0.Add(time.Minute), Project: "proj-1a2b3c4d", By: "coordinator", Task: "T8", Rule: "recap.decide", Decision: "split", Why: "two changes over 400 lines"},
		{Seq: 1, At: t0, Project: "proj-3c4d5e6f", By: "coordinator", Rule: "plan.amend", Decision: "plan amended", Why: "recipes split"},
		{Seq: 1, At: t0, Project: "proj-4d5e6f70", By: "coordinator", Rule: "plan.amend", Decision: "plan amended", Why: "archive shipped"},
	} {
		if err := factory.AppendLog(paths.Log(l.Project), l); err != nil {
			t.Fatal(err)
		}
	}

	const spoken = "From the project's record, T8 was split to stay under 400 lines."
	f := &fakeInvestigator{answer: "From log.jsonl: T8 was split into parser and index to stay under 400 lines.", session: "sess-notes-1"}
	m := braintest.NewModel()
	m.EventDefault = &braintest.Step{Text: braintest.Reply(spoken, "")}
	h := newBrainHarness(t, m, func(c *Config) { c.Factory = factory.New(paths, nil); c.InvestigateSessionTTL = time.Hour })
	h.sup.investigator = f
	h.srv.SetAgent(herdr.AgentInfo{PaneID: "hc:coord-1", Agent: "claude", Status: herdr.StateIdle, Cwd: notesDir, Labels: map[string]string{"sheepdog.project": "proj-1a2b3c4d", "sheepdog.role": flow.RoleCoordinator}})
	h.srv.SetAgent(herdr.AgentInfo{PaneID: "hc:w1", Agent: "codex", Status: herdr.StateWorking, Cwd: notesDir, Labels: map[string]string{"sheepdog.project": "proj-1a2b3c4d", "sheepdog.role": flow.RoleWorker}})

	res, toolErr := sayToolPR6(t, h, m, "ask the notes factory why it split T8", braintest.FC(brain.ToolInvestigate, map[string]any{"agent": "the notes factory", "question": "Why was T8 split?"}))
	if toolErr != "" {
		t.Fatalf("investigate on running project failed: %s", toolErr)
	}
	var started brain.InvestigationStart
	if err := json.Unmarshal([]byte(res), &started); err != nil || started.Job == "" || started.ETASeconds <= 0 {
		t.Fatalf("start = (%q, %v)", res, err)
	}
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "project investigation delivery")
	q0 := f.all()[0]
	if q0.Workspace != notesDir || q0.Fork || q0.Session != "" || q0.Slot != "investigation:factory:proj-1a2b3c4d" {
		t.Errorf("query = %+v", q0)
	}
	for _, want := range []string{"Why was T8 split?", paths.Log("proj-1a2b3c4d"), "T11 added: sync missing", "Delete old notes table?", "two changes over 400 lines"} {
		if !strings.Contains(q0.Question, want) {
			t.Errorf("query missing %q:\n%s", want, q0.Question)
		}
	}
	if p := h.pusher.last(t); p.Text != spoken || p.Urgency != voice.UrgencyIdle || h.pusher.tagsOf(0)["answers_job"] != started.Job {
		t.Errorf("push = %+v tags=%v", p, h.pusher.tagsOf(0))
	}
	if len(h.srv.RequestsFor("pane.read"))+len(h.srv.RequestsFor("pane.prompt"))+len(h.srv.RequestsFor("pane.send_keys")) != 0 {
		t.Errorf("project investigation touched a terminal")
	}
	if st, _ := factory.LoadState(paths.State("proj-1a2b3c4d")); len(st.Project.Wakes) != 0 {
		t.Errorf("project investigation queued Say wakes: %+v", st.Project.Wakes)
	}
	if !slices.ContainsFunc(h.sup.brain.Transcript().Messages(), func(msg brain.Message) bool {
		return msg.Kind == brain.KindRequest && strings.Contains(msg.Content, "<<<BEGIN_WORKER_OUTPUT") && strings.Contains(msg.Content, "T8 was split into parser and index")
	}) {
		t.Errorf("delivery turn missing untrusted worker output wrapper")
	}
	if invs := (brainHost{h.sup}).Investigations(); len(invs) != 1 || invs[0].Status != state.InvestigationDone || !invs[0].Resumable {
		t.Fatalf("Investigations = %+v", invs)
	}
	if rRes, rErr := sayToolPR6(t, h, m, "what did we find?", braintest.FC(brain.ToolRecall, map[string]any{"query": "split"})); rErr != "" || !strings.Contains(rRes, "T8 was split") {
		t.Errorf("recall = (%q, %q)", rRes, rErr)
	}
	if _, err := sayToolPR6(t, h, m, "dig deeper", braintest.FC(brain.ToolInvestigate, map[string]any{"agent": "the notes factory", "question": "Which files?", "follow_up_of": started.Job})); err != "" {
		t.Fatalf("follow-up failed: %s", err)
	}
	waitFor(t, func() bool { return len(h.pusher.all()) == 2 }, "follow-up delivery")
	if q1 := f.all()[1]; q1.Session != "sess-notes-1" || q1.Workspace != notesDir || q1.Fork {
		t.Errorf("follow-up query = %+v", q1)
	}
	for i, tc := range []struct{ agent, id, dir, state string }{{"the recipes factory", "proj-3c4d5e6f", recipesDir, "stopped"}, {"the archive factory", "proj-4d5e6f70", archiveDir, "finished"}} {
		if _, err := sayToolPR6(t, h, m, "ask "+tc.agent, braintest.FC(brain.ToolInvestigate, map[string]any{"agent": tc.agent, "question": "Why?"})); err != "" {
			t.Fatalf("%s investigate failed: %s", tc.state, err)
		}
		waitFor(t, func() bool { return len(h.pusher.all()) == 3+i }, tc.state+" delivery")
		qs := f.all()
		if q := qs[len(qs)-1]; q.Workspace != tc.dir || !strings.Contains(q.Question, paths.Log(tc.id)) {
			t.Errorf("%s query = %+v", tc.state, q)
		}
		if st, _ := factory.LoadState(paths.State(tc.id)); (tc.state == "stopped" && st.Project.Started) || (tc.state == "finished" && !st.Project.Finished) {
			t.Errorf("%s state mutated: %+v", tc.state, st.Project)
		}
	}
}

// Pins app-factory.md §3.2, §5.2 & §6.5: one live investigation per project
// (superseded), Stop and error lines speak the project name (never hex id),
// direct source:worker/unknown-project are refused, and event turns cannot investigate.
func TestInvestigateProjectLifecycleAndGuardrails(t *testing.T) {
	paths, notesDir := factory.Paths{DataDir: t.TempDir()}, filepath.Join(t.TempDir(), "notes")
	_ = os.MkdirAll(notesDir, 0o755)
	if err := factory.SaveState(paths.State("proj-1a2b3c4d"), flow.State{Seq: 1, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "proj-1a2b3c4d", Folder: notesDir, Goal: "Notes", Started: true, RunID: "hc:coord-1"}}); err != nil {
		t.Fatal(err)
	}
	f := &fakeInvestigator{answer: "ok", gate: make(chan struct{})}
	m := braintest.NewModel()
	h := newBrainHarness(t, m, func(c *Config) { c.Factory = factory.New(paths, nil) })
	m.EventDefault = nil
	h.sup.investigator = f
	host := brainHost{h.sup}

	j1, err := host.Investigate(context.Background(), "factory:proj-1a2b3c4d", brain.InvestigationRequest{Question: "First?"})
	if err != nil {
		t.Fatalf("first project investigate: %v", err)
	}
	if _, err := host.Investigate(context.Background(), "factory:proj-1a2b3c4d", brain.InvestigationRequest{Question: "Second?"}); err != nil {
		t.Fatalf("second: %v", err)
	}
	if c := h.log.ofType("investigation.cancelled"); len(c) != 1 || payload(c[0])["id"] != j1.Job || payload(c[0])["reason"] != "superseded" {
		t.Errorf("investigation.cancelled = %v", c)
	}
	if stopRes, err := h.sup.Stop(context.Background(), StopTriggerTool); err != nil || !strings.Contains(strings.ToLower(stopRes.Spoken), "notes") || strings.Contains(stopRes.Spoken, "1a2b3c4d") {
		t.Errorf("Stop = (%+v, %v), want notes without hex id", stopRes, err)
	}
	f.mu.Lock()
	f.gate, f.err = nil, context.DeadlineExceeded
	f.mu.Unlock()
	if _, err := host.Investigate(context.Background(), "factory:proj-1a2b3c4d", brain.InvestigationRequest{Question: "Timeout?"}); err != nil {
		t.Fatalf("timeout investigate: %v", err)
	}
	waitFor(t, func() bool { return len(h.pusher.all()) == 1 }, "timeout push")
	if got := h.pusher.last(t).Text; !strings.Contains(strings.ToLower(got), "notes") || strings.Contains(got, "1a2b3c4d") {
		t.Errorf("timeout push = %q, want notes without hex id", got)
	}
	if _, err := host.Investigate(context.Background(), "factory:proj-1a2b3c4d", brain.InvestigationRequest{Question: "Why?", Source: brain.InvestigationSourceWorker}); err == nil {
		t.Errorf("direct Host.Investigate(source=worker) succeeded")
	}
	if _, err := host.Investigate(context.Background(), "factory:proj-missing", brain.InvestigationRequest{Question: "Why?"}); err == nil || (!errors.Is(err, factory.ErrProjectNotFound) && !strings.Contains(strings.ToLower(err.Error()), "not found")) {
		t.Errorf("unknown project err = %v", err)
	}
	before := len(h.log.ofType("brain.rail_blocked"))
	m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolInvestigate, map[string]any{"agent": "the notes factory", "question": "unsolicited"})}}, braintest.Step{Text: braintest.Reply("", "")})
	_, _ = h.sup.brain.Turn(context.Background(), brain.Request{Kind: brain.KindAnnounce, Trigger: brain.TriggerEvent, Agent: "notes", Event: "done"})
	if len(h.log.ofType("brain.rail_blocked")) <= before {
		t.Errorf("event turn investigate on project was not rail-blocked")
	}
}
