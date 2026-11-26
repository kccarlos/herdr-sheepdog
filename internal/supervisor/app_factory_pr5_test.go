package supervisor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Pins app-factory.md §5.2, §6.1, §6.2 & §6.3: orchestrator prompt on a project
// row routes to Engine.Say (owner_say wake + log line with heard words, single
// line, no SignOff trailer, 1,000-char cap, state confirmations), never writes
// to the coordinator terminal, never answers an open gated ask, and stays
// refused in event-triggered turns.
func TestPromptOnProjectRoutesToSayWithHeardWordsAndNoTerminalWrite(t *testing.T) {
	paths := factory.Paths{DataDir: t.TempDir()}
	for _, st := range []flow.State{
		{Project: flow.Project{ID: "proj-1a2b3c4d", Folder: "/work/notes", Goal: "Notes", Started: true, RunID: "hc:coord-1",
			Run: &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", Address: "hc:coord-1", State: flow.PhaseIdle}},
			Asks: []flow.Ask{{ID: "ask-rel", Project: "proj-1a2b3c4d", Folder: "/work/notes", Kind: flow.KindRelease, Gated: true,
				Question: "Tag 0.2.0 now?", Options: []string{"Yes, tag 0.2.0", "Hold"}, Recommend: "Hold", At: pr4T0, Status: flow.AskStatusOpen}}},
		{Project: flow.Project{ID: "proj-2b3c4d5e", Folder: "/work/nocoord", Goal: "NoCoord", Started: true}},
		{Project: flow.Project{ID: "proj-3c4d5e6f", Folder: "/work/stopped", Goal: "Stopped", Started: false}},
		{Project: flow.Project{ID: "proj-4d5e6f70", Folder: "/work/done", Goal: "Done", Finished: true}},
	} {
		st.Seq, st.Settings = 1, flow.DefaultSettings()
		if err := factory.SaveState(paths.State(st.Project.ID), st); err != nil {
			t.Fatal(err)
		}
	}

	eng, m := factory.New(paths, nil), braintest.NewModel()
	e := newPR4Env(t, m, eng)
	lbl := map[string]string{"sheepdog.project": "proj-1a2b3c4d", "sheepdog.role": flow.RoleCoordinator}
	e.addPane("hc:coord-1", "coord-1a2b", "claude", "", herdr.StateWorking, lbl, "")
	e.sup.SyncFactoryAsksForTest()

	voicePrompt := func(heard, agent, text string) (res, toolErr string) {
		m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": agent, "text": text})}}, braintest.Step{Text: braintest.Reply("Done.", "")})
		e.sup.ShepherdInput(supervisor.InputVoice, heard)
		e.sup.OnVoiceEvent(voice.EventToolCall{Call: voice.ToolCall{Name: supervisor.ToolToSupervisor}})
		if _, err := e.sup.ToSupervisor(context.Background(), heard); err != nil {
			t.Fatalf("ToSupervisor(%q): %v", heard, err)
		}
		msgs := e.brain.Transcript().Messages()
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Kind == brain.KindToolResult && msgs[i].ToolResult != nil {
				return msgs[i].Content, msgs[i].ToolResult.Error
			}
		}
		return "", ""
	}

	const heard = "Hey, tell the notes factory to skip the PDF export"
	if res, err := voicePrompt(heard, "the notes factory", "skip the PDF\nexport task"); err != "" || res != "Told the notes factory." {
		t.Fatalf("running prompt = (%q, %q), want (\"Told the notes factory.\", \"\")", res, err)
	}
	if len(e.srv.RequestsFor("pane.prompt")) != 0 || len(e.sendKeys()) != 0 {
		t.Fatalf("prompt wrote to coordinator terminal: prompts=%d keys=%v", len(e.srv.RequestsFor("pane.prompt")), e.sendKeys())
	}
	st, _ := factory.LoadState(paths.State("proj-1a2b3c4d"))
	if len(st.Project.Wakes) != 1 || !strings.HasPrefix(st.Project.Wakes[0].Key, "owner_say:") {
		t.Fatalf("notes wakes = %+v, want 1 owner_say wake", st.Project.Wakes)
	}
	rawState, _ := os.ReadFile(paths.State("proj-1a2b3c4d"))
	rawLog, _ := os.ReadFile(paths.Log("proj-1a2b3c4d"))
	combined := string(rawState) + "\n" + string(rawLog)
	for _, want := range []string{"skip the PDF export task", heard} {
		if !strings.Contains(combined, want) {
			t.Errorf("state/log missing %q", want)
		}
	}
	if strings.Contains(combined, "three short lines") || strings.Contains(st.Project.Wakes[0].Facts+st.Project.Wakes[0].Why, "\n") {
		t.Errorf("wake leaked SignOff trailer or newline: %+v", st.Project.Wakes[0])
	}
	var logLine factory.LogLine
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(rawLog))), &logLine); err != nil || logLine.Rule != "say" || logLine.By != "owner" {
		t.Errorf("log.jsonl = %q (err=%v), want owner say line", rawLog, err)
	}
	if res, err := voicePrompt("tell nocoord to pause", "the nocoord factory", "pause"); err != "" || res != "The nocoord coordinator is not running. It gets this when it is back." {
		t.Errorf("no-coordinator prompt = (%q, %q)", res, err)
	}
	if res, err := voicePrompt("tell stopped to skip export", "the stopped factory", "skip export"); err != "" || res != "stopped is stopped. It gets this when you resume it." {
		t.Errorf("stopped prompt = (%q, %q)", res, err)
	}
	if s, _ := factory.LoadState(paths.State("proj-3c4d5e6f")); len(s.Project.Wakes) != 1 {
		t.Errorf("stopped project wakes = %+v, want 1 held wake", s.Project.Wakes)
	}
	if res, err := voicePrompt("tell done to add tests", "the done factory", "add tests"); err == "" || !strings.Contains(err, "done is finished. Give it a new goal.") {
		t.Errorf("finished prompt = (%q, %q), want finished refusal", res, err)
	}
	if s, _ := factory.LoadState(paths.State("proj-4d5e6f70")); len(s.Project.Wakes) != 0 {
		t.Errorf("finished project queued wake: %+v", s.Project.Wakes)
	}
	if _, err := voicePrompt("long note", "the notes factory", strings.Repeat("a", 1001)); err == "" {
		t.Errorf("prompt(>1000 chars) succeeded, want refusal")
	}

	// Blocked coordinator + §6.3 safety: no direct terminal write, open ask untouched, no proof minted.
	e.addPane("hc:coord-1", "coord-1a2b", "claude", herdr.StateWorking, herdr.StateBlocked, lbl, "codex-approval-prompt.screen.txt")
	if _, err := voicePrompt("yes go ahead and tag it", "the notes factory", "Yes, tag 0.2.0"); err != "" {
		t.Errorf("prompt while ask open = %q, want Say accepted", err)
	}
	if len(e.srv.RequestsFor("pane.prompt")) != 0 || len(e.sendKeys()) != 0 {
		t.Errorf("blocked coordinator got direct terminal write: prompts=%d keys=%v", len(e.srv.RequestsFor("pane.prompt")), e.sendKeys())
	}
	if open := eng.Asks(true); len(open) != 1 || open[0].ID != "ask-rel" || open[0].Status != flow.AskStatusOpen {
		t.Errorf("open asks after prompt = %+v, want ask-rel still open", open)
	}
	if _, err := os.Stat(filepath.Join(paths.DataDir, "factory", "proofs.json")); !os.IsNotExist(err) {
		t.Errorf("prompt minted answer proofs: stat err=%v", err)
	}

	// Event turn cannot prompt a project row.
	wakesBefore := len(func() []flow.Wake { s, _ := factory.LoadState(paths.State("proj-1a2b3c4d")); return s.Project.Wakes }())
	m.Add(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "the notes factory", "text": "unsolicited"})}}, braintest.Step{Text: braintest.Reply("", "")})
	_, _ = e.brain.Turn(context.Background(), brain.Request{Kind: brain.KindAnnounce, Trigger: brain.TriggerEvent, Agent: "notes", Event: "done"})
	if s, _ := factory.LoadState(paths.State("proj-1a2b3c4d")); len(s.Project.Wakes) != wakesBefore {
		t.Errorf("event turn queued Say wake: before=%d after=%d", wakesBefore, len(s.Project.Wakes))
	}
}

// Pins Carbon F1: an orchestrator turn whose recorded user text exceeds 1,000
// runes still relays a short instruction via prompt on a factory project row.
func TestPromptOnProjectAcceptsLongHeardTurnWithShortInstruction(t *testing.T) {
	paths := factory.Paths{DataDir: t.TempDir()}
	st := flow.State{
		Seq:      1,
		Settings: flow.DefaultSettings(),
		Project: flow.Project{
			ID: "proj-1a2b3c4d", Folder: "/work/notes", Goal: "Notes", Started: true, RunID: "hc:coord-1",
			Run: &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", Address: "hc:coord-1", State: flow.PhaseIdle},
		},
	}
	if err := factory.SaveState(paths.State(st.Project.ID), st); err != nil {
		t.Fatal(err)
	}
	eng, m := factory.New(paths, nil), braintest.NewModel()
	e := newPR4Env(t, m, eng)
	lbl := map[string]string{"sheepdog.project": "proj-1a2b3c4d", "sheepdog.role": flow.RoleCoordinator}
	e.addPane("hc:coord-1", "coord-1a2b", "claude", "", herdr.StateWorking, lbl, "")

	longHeard := "Please look at this pasted context and tell the notes factory to skip export: " + strings.Repeat("x", 1050)
	res, toolErr, _ := e.sayTool(m, longHeard, braintest.FC(brain.ToolPrompt, map[string]any{"agent": "the notes factory", "text": "skip the export task"}))
	if toolErr != "" || res != "Told the notes factory." {
		t.Fatalf("prompt with >1000-rune heard = (%q, %q), want (\"Told the notes factory.\", \"\")", res, toolErr)
	}
	after, err := factory.LoadState(paths.State("proj-1a2b3c4d"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Project.Wakes) != 1 || !strings.HasPrefix(after.Project.Wakes[0].Key, "owner_say:") {
		t.Fatalf("wakes = %+v, want 1 owner_say wake", after.Project.Wakes)
	}
	if !strings.Contains(after.Project.Wakes[0].Facts, "skip the export task") || !strings.Contains(after.Project.Wakes[0].Facts, "Please look at this pasted context") {
		t.Errorf("wake facts = %q, want short instruction and capped heard prefix", after.Project.Wakes[0].Facts)
	}
}

// Pins Carbon F2: prompting a factory project updates lastSubject so a
// follow-up "and tell it ..." turn is not focused on a stale ordinary agent.
func TestPromptOnProjectClearsStaleOrdinaryAgentFocus(t *testing.T) {
	fac := inject.NewFactory(nil)
	fac.SetProject(inject.FactoryProject{
		ProjectHead: factory.ProjectHead{ID: "proj-1a2b3c4d", Name: "notes", Folder: "/work/notes", Goal: "Notes", State: "running", CoordinatorPane: "hc:coord-1"},
	})
	m := braintest.NewModel()
	e := newPR4Env(t, m, fac)
	e.addPane("w5:p1", "auth", "codex", "", herdr.StateIdle, nil, "")
	e.srv.SetScreen("w5:p1", "auth screen\n")

	e.sayTool(m, "check auth", braintest.FC(brain.ToolReadScreen, map[string]any{"agent": "auth"}))
	res, toolErr, _ := e.sayTool(m, "tell the notes factory to skip PDF export", braintest.FC(brain.ToolPrompt, map[string]any{"agent": "the notes factory", "text": "skip PDF export"}))
	if toolErr != "" || res != "Told the notes factory." {
		t.Fatalf("prompt on notes factory = (%q, %q), want (\"Told the notes factory.\", \"\")", res, toolErr)
	}

	e.sayTool(m, "and tell it to keep Markdown export", &genai.FunctionCall{Name: brain.ToolListAgents})
	found := false
	for _, msg := range slices.Backward(e.brain.Transcript().Messages()) {
		if msg.Kind == brain.KindRequest && strings.Contains(msg.Content, "and tell it to keep Markdown export") {
			found = true
			if strings.Contains(msg.Content, "the one in focus is\n- auth") {
				t.Errorf("after prompting notes factory, follow-up 'it' still focused on auth:\n%s", msg.Content)
			}
			break
		}
	}
	if !found {
		t.Fatal("follow-up request not found in brain transcript")
	}
}
