package supervisor_test

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

var pr4T0 = time.Date(2026, 9, 17, 16, 55, 0, 0, time.UTC)

type pr4Env struct {
	t             *testing.T
	srv           *herdrtest.Server
	store         *state.Store
	brain         *brain.Brain
	sup           *supervisor.Supervisor
	mu            sync.Mutex
	now           time.Time
	blocks, qCall int
}

func (e *pr4Env) Now() time.Time                   { e.mu.Lock(); defer e.mu.Unlock(); return e.now }
func (e *pr4Env) Push(string, voice.Urgency) error { return nil }
func (e *pr4Env) Log(_, typ, _ string, _ any) {
	if typ == "brain.rail_blocked" {
		e.mu.Lock()
		e.blocks++
		e.mu.Unlock()
	}
}
func (e *pr4Env) Query(context.Context, string, string) (string, error) {
	e.mu.Lock()
	e.qCall++
	e.mu.Unlock()
	return "QUERY", nil
}
func newPR4Env(t *testing.T, m *braintest.Model, fac supervisor.Factory) *pr4Env {
	t.Helper()
	dir, e := t.TempDir(), &pr4Env{t: t, srv: herdrtest.New(t), now: pr4T0}
	if m.EventDefault == nil && m.Default == nil {
		m.EventDefault = braintest.NothingToAdd()
	}
	var err error
	e.store, err = state.Open(filepath.Join(dir, "state.json"), state.Options{Aliases: map[string]string{"w5:p1": "auth", "w6:p1": "docs"}})
	if err == nil {
		e.brain, err = brain.New(brain.Config{DataDir: dir, RunID: "t", Now: e.Now}, m, e)
	}
	if err == nil {
		t.Cleanup(func() { _ = e.brain.Close() })
		wl := workerlog.New(workerlog.Options{ClaudeProjects: filepath.Join(dir, "c"), CodexSessions: filepath.Join(dir, "x"), OpenFiles: func([]int) []string { return nil }})
		e.sup, err = supervisor.New(e.store, herdr.NewClient(e.srv.Path), e, e, e, supervisor.Config{DataDir: dir, Brain: e.brain, Factory: fac, Now: e.Now, PendingTTL: 120 * time.Second, ReplyWait: -1, Chattiness: string(supervisor.Chatty), WorkerLog: wl})
	}
	if err != nil {
		t.Fatal(err)
	}
	e.sup.SetVoiceConnected(true)
	t.Cleanup(e.sup.ShutdownForTest)
	return e
}
func (e *pr4Env) addPane(pane, ws, kind string, from, to herdr.State, lbl map[string]string, scrFile string) {
	e.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: ws, Label: ws})
	e.srv.SetAgent(herdr.AgentInfo{PaneID: pane, WorkspaceID: ws, Agent: kind, Name: ws, Status: to, Cwd: "/work/" + ws, Labels: lbl})
	if scrFile != "" {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", scrFile))
		if err != nil {
			e.t.Fatal(err)
		}
		e.srv.SetScreen(pane, string(b))
	}
	e.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: pane, From: from, To: to, At: e.Now(), Agent: kind, Name: ws, WorkspaceID: ws, WorkspaceLabel: ws, Labels: lbl})
}
func (e *pr4Env) sayTool(m *braintest.Model, text string, fc *genai.FunctionCall) (res, toolErr string, blocked int) {
	e.mu.Lock()
	before := e.blocks
	e.mu.Unlock()
	m.Add(braintest.Step{Calls: []*genai.FunctionCall{fc}}, braintest.Step{Text: braintest.Reply("Done.", "")})
	e.sup.ShepherdInput(supervisor.InputTyped, text)
	e.sup.OnVoiceEvent(voice.EventToolCall{Call: voice.ToolCall{Name: supervisor.ToolToSupervisor}})
	if _, err := e.sup.ToSupervisor(context.Background(), text); err != nil {
		e.t.Fatalf("ToSupervisor(%q): %v", text, err)
	}
	e.mu.Lock()
	blocked = e.blocks - before
	e.mu.Unlock()
	for _, msg := range slices.Backward(e.brain.Transcript().Messages()) {
		if msg.Kind == brain.KindToolResult && msg.ToolResult != nil {
			return msg.Content, msg.ToolResult.Error, blocked
		}
	}
	return "", "", blocked
}
func (e *pr4Env) roster() string {
	for _, s := range e.brain.PromptSections() {
		if s.Name == "roster" {
			return s.Text
		}
	}
	return ""
}
func (e *pr4Env) sendKeys() (out [][2]string) {
	for _, r := range e.srv.RequestsFor("pane.send_keys") {
		var p struct {
			PaneID string   `json:"pane_id"`
			Keys   []string `json:"keys"`
		}
		_ = json.Unmarshal(r.Params, &p)
		out = append(out, [2]string{p.PaneID, strings.Join(p.Keys, " ")})
	}
	return out
}

// Pins app-factory.md §5.1 & §10 PR 4: zero projects match the pre-PR4 golden.
func TestZeroProjectsRosterGolden(t *testing.T) {
	build := func(fac supervisor.Factory) string {
		e := newPR4Env(t, braintest.NewModel(), fac)
		e.addPane("w5:p1", "auth", "codex", "", herdr.StateWorking, nil, "")
		e.now = e.now.Add(2 * time.Minute)
		e.addPane("w5:p1", "auth", "codex", herdr.StateWorking, herdr.StateBlocked, nil, "codex-approval-prompt.screen.txt")
		e.addPane("w6:p1", "docs", "claude", "", herdr.StateIdle, nil, "")
		e.now = e.now.Add(30 * time.Second)
		_, _ = e.sup.BrainHostForTest().Note("keep migrations untouched")
		var parts []string
		for _, s := range e.brain.PromptSections() {
			if s.Source == brain.SourceDynamic {
				parts = append(parts, s.Text)
			}
		}
		return strings.Join(append(parts, "## Briefing\n\n"+e.sup.BriefingForTest()), "\n\n") + "\n"
	}
	want, err := os.ReadFile(filepath.Join("testdata", "zero_projects_roster.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if a, b := build(nil), build(inject.NewFactory(nil)); a != string(want) || b != string(want) {
		t.Errorf("zero-project standing context drifted\nwant:\n%s\ngot(nil):\n%s\ngot(empty):\n%s", want, a, b)
	}
}

// Pins app-factory.md §3.1, §3.2 & §5.1: running/stopped project rows, no
// factory terminals, finished unlisted yet resolvable, tie/listed-wins rules,
// gated asks never Pending, and blocked coordinator merge.
func TestProjectRowsRosterResolveAndBlockedMerge(t *testing.T) {
	fac := inject.NewFactory(nil)
	fac.SetProject(inject.FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-1a2b3c4d", Folder: "/work/notes", Goal: "Notes", State: "running", CoordinatorPane: "hc:coord-1", Counts: map[string]int{"working": 3, "owner_answer": 1, "done": 9}}, Asks: []flow.Ask{{ID: "ask-1", Project: "proj-1a2b3c4d", Task: "T10", Kind: flow.KindDelete, Question: "Delete the old notes table?", Options: []string{"Delete", "Keep"}, Recommend: "Keep", At: pr4T0}}})
	fac.SetProject(inject.FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-2b3c4d5e", Folder: "/work/recipes", Goal: "Recipes", State: "stopped"}})
	fac.SetProject(inject.FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-3c4d5e6f", Folder: "/work/archive", Goal: "Old", State: "finished"}})
	e := newPR4Env(t, braintest.NewModel(), fac)
	e.addPane("w5:p1", "auth", "codex", "", herdr.StateIdle, nil, "")
	e.addPane("hc:w1", "worker-t8", "codex", herdr.StateIdle, herdr.StateBlocked, map[string]string{"sheepdog.project": "proj-1a2b3c4d", "sheepdog.role": flow.RoleWorker}, "codex-approval-prompt.screen.txt")
	e.addPane("hc:r1", "rev-t7", "claude", herdr.StateIdle, herdr.StateWorking, map[string]string{"sheepdog.project": "proj-1a2b3c4d", "sheepdog.role": flow.RoleReviewer}, "")
	e.addPane("hc:coord-1", "coord-1a2b", "claude", herdr.StateIdle, herdr.StateWorking, map[string]string{"sheepdog.project": "proj-1a2b3c4d", "sheepdog.role": flow.RoleCoordinator}, "")
	e.sup.SyncFactoryAsksForTest()

	host := e.sup.BrainHostForTest()
	paneMap := func() (m map[string]brain.AgentInfo) {
		m = map[string]brain.AgentInfo{}
		for _, a := range host.Agents() {
			m[a.PaneID] = a
		}
		return m
	}
	byPane := paneMap()
	notesRow, hasNotes := byPane["factory:proj-1a2b3c4d"]
	if _, hasRec := byPane["factory:proj-2b3c4d5e"]; len(byPane) != 3 || !hasNotes || !hasRec {
		t.Fatalf("Agents() panes = %v, want [factory:proj-1a2b3c4d factory:proj-2b3c4d5e w5:p1]", slices.Sorted(maps.Keys(byPane)))
	}
	if notesRow.Spoken != "the notes factory" || notesRow.Alias == "" || notesRow.Alias == "notes" || notesRow.Pending != nil {
		t.Errorf("notes row = %+v", notesRow)
	}
	lower := strings.ToLower(e.roster())
	for _, w := range []string{"notes", "running", "recipes", "stopped", "delete the old notes table?"} {
		if !strings.Contains(lower, w) {
			t.Errorf("roster missing %q", w)
		}
	}
	if !strings.Contains(lower, "1 waiting") && !strings.Contains(lower, "waiting on you") {
		t.Errorf("roster missing waiting count")
	}
	for _, bad := range []string{"archive", "worker-t8", "rev-t7", "coord-1a2b"} {
		if strings.Contains(lower, bad) {
			t.Errorf("roster leaked %q", bad)
		}
	}
	if fin, ok := host.Resolve("the archive factory"); !ok || fin.PaneID != "factory:proj-3c4d5e6f" {
		t.Errorf("Resolve(finished) = (%+v, %v)", fin, ok)
	}
	fac.SetProject(inject.FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-4d5e6f70", Folder: "/other/notes", Goal: "Other", State: "running"}})
	tie := paneMap()
	if a1, a2 := tie["factory:proj-1a2b3c4d"].Alias, tie["factory:proj-4d5e6f70"].Alias; a1 == "" || a2 == "" || a1 == a2 {
		t.Fatalf("tie aliases = %q / %q", a1, a2)
	}
	if _, ok := host.Resolve("the notes factory"); ok {
		t.Errorf("Resolve(tie) = true, want false")
	}
	fac.SetProject(inject.FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-4d5e6f70", Folder: "/other/notes", Goal: "Other", State: "finished"}})
	if got, ok := host.Resolve("the notes factory"); !ok || got.PaneID != "factory:proj-1a2b3c4d" {
		t.Errorf("Resolve(listed vs finished) = (%+v, %v)", got, ok)
	}
	e.addPane("hc:coord-1", "coord-1a2b", "claude", herdr.StateWorking, herdr.StateBlocked, map[string]string{"sheepdog.project": "proj-1a2b3c4d", "sheepdog.role": flow.RoleCoordinator}, "codex-approval-prompt.screen.txt")
	byPane = paneMap()
	if _, leaked := byPane["hc:coord-1"]; leaked || byPane["factory:proj-1a2b3c4d"].Pending == nil || !strings.Contains(e.roster(), "WAITING FOR AN ANSWER") || strings.Contains(e.roster(), "coord-1a2b") {
		t.Fatalf("blocked coordinator merge failed: panes=%v", slices.Sorted(maps.Keys(byPane)))
	}
	if row, ok := e.store.Get("hc:coord-1"); ok {
		if got, ok := host.Resolve(row.Alias); !ok || got.PaneID != "factory:proj-1a2b3c4d" {
			t.Errorf("Resolve(coord alias %q) = (%+v, %v)", row.Alias, got, ok)
		}
	}
}

// Pins app-factory.md §5.2 & §5.1: tool helper refuses interrupt, close, rename
// (note branch), run_cli, promise, read_screen(session), investigate(worker),
// and unblocked send_keys; blocked coordinator send_keys routes to hc:coord-1.
func TestProjectToolHelperAllowsAndRefuses(t *testing.T) {
	fac := inject.NewFactory(nil)
	fac.SetProject(inject.FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-1a2b3c4d", Folder: "/work/notes", Goal: "Notes", State: "running", CoordinatorPane: "hc:coord-1"}, Asks: []flow.Ask{{ID: "ask-1", Project: "proj-1a2b3c4d", Task: "T1", Kind: flow.KindDelete, Question: "Delete old table?", Options: []string{"Delete", "Keep"}, Recommend: "Keep", At: pr4T0}}})
	m, lbl := braintest.NewModel(), map[string]string{"sheepdog.project": "proj-1a2b3c4d", "sheepdog.role": flow.RoleCoordinator}
	e := newPR4Env(t, m, fac)
	e.srv.Handle("pane.close", func(json.RawMessage) (any, error) { return map[string]any{"type": "ok"}, nil })
	e.srv.Handle("agent.rename", func(json.RawMessage) (any, error) { return map[string]any{"type": "ok"}, nil })
	e.addPane("hc:coord-1", "coord-1a2b", "claude", "", herdr.StateWorking, lbl, "")
	e.sup.SyncFactoryAsksForTest()

	const refuse = "That works on an agent, not on a factory project."
	for _, fc := range []*genai.FunctionCall{
		braintest.FC(brain.ToolInterrupt, map[string]any{"agent": "the notes factory"}),
		braintest.FC(brain.ToolInterrupt, map[string]any{"agent": "the notes factory", "close": true}),
		braintest.FC(brain.ToolNote, map[string]any{"agent": "the notes factory", "name": "renamed"}),
		braintest.FC(brain.ToolRunCLI, map[string]any{"agent": "the notes factory", "question": "git status"}),
		braintest.FC(brain.ToolPromise, map[string]any{"agent": "the notes factory", "text": "tell me", "event": "done"}),
	} {
		if _, err, b := e.sayTool(m, "close or act on notes", fc); b != 1 || !strings.Contains(err, refuse) {
			t.Errorf("%s: rail_blocked+=%d err=%q", fc.Name, b, err)
		}
	}
	if _, err, b := e.sayTool(m, "tell notes yes", braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "the notes factory", "keys": []any{"y"}})); b != 1 || len(e.sendKeys()) != 0 {
		t.Errorf("unblocked send_keys: rail_blocked+=%d sends=%v err=%q", b, e.sendKeys(), err)
	}
	if _, err, _ := e.sayTool(m, "read session", braintest.FC(brain.ToolReadScreen, map[string]any{"agent": "the notes factory", "source": brain.SourceSession})); err == "" || strings.Contains(err, "no agent called") || len(e.srv.RequestsFor("pane.read")) != 0 {
		t.Errorf("read_screen(session): reads=%d err=%q", len(e.srv.RequestsFor("pane.read")), err)
	}
	if _, err, _ := e.sayTool(m, "ask coordinator", braintest.FC(brain.ToolInvestigate, map[string]any{"agent": "the notes factory", "source": brain.InvestigationSourceWorker, "question": "why?"})); !strings.Contains(strings.ToLower(err), "without source") || len(e.sup.BrainHostForTest().Investigations()) != 0 {
		t.Errorf("investigate(worker): err=%q", err)
	}
	if len(e.srv.RequestsFor("pane.close")) != 0 || len(e.srv.RequestsFor("agent.rename")) != 0 || e.qCall != 0 || len(e.sup.BrainHostForTest().Promises()) != 0 {
		t.Errorf("refused tools leaked side effects")
	}
	e.addPane("hc:coord-1", "coord-1a2b", "claude", herdr.StateWorking, herdr.StateBlocked, lbl, "codex-approval-prompt.screen.txt")
	target := "the notes factory"
	if row, ok := e.store.Get("hc:coord-1"); ok && row.Alias != "" {
		target = row.Alias
	}
	for _, fc := range []*genai.FunctionCall{braintest.FC(brain.ToolInterrupt, map[string]any{"agent": target}), braintest.FC(brain.ToolInterrupt, map[string]any{"agent": target, "close": true}), braintest.FC(brain.ToolNote, map[string]any{"agent": target, "name": "renamed"})} {
		if res, err, b := e.sayTool(m, "close and stop blocked coordinator", fc); b != 1 || !strings.Contains(err, refuse) {
			t.Errorf("blocked %s(%s): rail_blocked+=%d res=%q err=%q", fc.Name, target, b, res, err)
		}
	}
	if len(e.sendKeys()) != 0 || len(e.srv.RequestsFor("pane.close")) != 0 || len(e.srv.RequestsFor("agent.rename")) != 0 {
		t.Fatalf("refused actions reached blocked coordinator")
	}
	if _, err, _ := e.sayTool(m, "tell the notes factory yes go ahead", braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "the notes factory", "keys": []any{"y"}})); err != "" || !slices.Equal(e.sendKeys(), [][2]string{{"hc:coord-1", "y"}}) {
		t.Errorf("blocked project send_keys = (%v, %q), want [[hc:coord-1 y]]", e.sendKeys(), err)
	}
}

// Pins app-factory.md §5.3: read_screen on a project returns board text built
// from engine views inside wrapWorker delimiters (never terminal screen), and
// works on a stopped project with no terminal.
func TestReadScreenOnProjectReturnsBoardText(t *testing.T) {
	fac := inject.NewFactory(nil)
	fac.SetProject(inject.FactoryProject{
		ProjectHead: factory.ProjectHead{ID: "proj-1a2b3c4d", Folder: "/work/notes", Goal: "Notes", State: "running", CoordinatorPane: "hc:coord-1", Counts: map[string]int{"working": 2, "owner_answer": 1, "waiting_on_dep": 1, "done": 1}},
		Tasks: []factory.TaskView{
			{Ref: "T7", Title: "feat: search", Stage: "working", WhyWaiting: "In review round 2", Rounds: 2, RunRole: flow.RoleReviewer, RunCLI: "claude", Steps: []factory.StepView{{Name: "review", State: "now", Text: "review round 2"}}},
			{Ref: "T8", Title: "feat: tags", Stage: "working", WhyWaiting: "Worker is working", RunRole: flow.RoleWorker, RunCLI: "codex", RunStartedAt: pr4T0.Add(-31 * time.Minute), LastActorDecision: &factory.LogEntry{At: pr4T0, By: "coordinator", Decision: "task send-back", Why: "case-insensitive"}, Steps: []factory.StepView{{Name: "implementing", State: "now"}}},
			{Ref: "T9", Title: "feat: export", Stage: "done", LastActorDecision: &factory.LogEntry{At: pr4T0, By: "coordinator", Decision: "task drop", Why: "skip the export"}, Steps: []factory.StepView{{Name: "dropped", State: "ended"}}},
			{Ref: "T10", Title: "feat: import", Stage: "owner_answer", WhyWaiting: "Delete the old notes table?", Steps: []factory.StepView{{Name: "implementing", State: "now"}}},
			{Ref: "T11", Title: "feat: sync", Stage: "waiting_on_dep", After: []string{"T8"}, WhyWaiting: "Waiting on T8", Steps: []factory.StepView{{Name: "planned", State: "now"}}},
		},
		Log:  []factory.LogEntry{{At: pr4T0, By: "coordinator", Decision: "plan amend", Why: "sync was missing from the goal"}},
		Asks: []flow.Ask{{ID: "ask-1", Project: "proj-1a2b3c4d", Task: "T10", Kind: flow.KindDelete, Question: "Delete the old notes table?", Options: []string{"Delete", "Keep"}, Recommend: "Keep", At: pr4T0}},
	})
	fac.SetProject(inject.FactoryProject{ProjectHead: factory.ProjectHead{ID: "proj-2b3c4d5e", Folder: "/work/recipes", Goal: "Recipes", State: "stopped"}, Tasks: []factory.TaskView{{Ref: "T1", Title: "feat: index", Stage: "queued", WhyWaiting: "Project is stopped", Steps: []factory.StepView{{Name: "planned", State: "now"}}}}})
	m := braintest.NewModel()
	e := newPR4Env(t, m, fac)
	e.srv.SetScreen("hc:coord-1", "RAW_COORDINATOR_TERMINAL_SENTINEL\n")
	notesOut, notesErr, _ := e.sayTool(m, "how is notes?", braintest.FC(brain.ToolReadScreen, map[string]any{"agent": "the notes factory"}))
	recipesOut, recipesErr, _ := e.sayTool(m, "how is recipes?", braintest.FC(brain.ToolReadScreen, map[string]any{"agent": "the recipes factory"}))
	if notesErr != "" || recipesErr != "" {
		t.Fatalf("read_screen errors: notes=%q recipes=%q", notesErr, recipesErr)
	}
	if reads := e.srv.RequestsFor("pane.read"); len(reads) != 0 || strings.Contains(notesOut, "RAW_COORDINATOR_TERMINAL_SENTINEL") {
		t.Errorf("read_screen touched terminal: reads=%d", len(reads))
	}
	for _, want := range []string{"<<<BEGIN_WORKER_OUTPUT", "<<<END_WORKER_OUTPUT", "notes", "running", "T7", "review", "T8", "codex", "case-insensitive", "T9", "dropped", "skip the export", "T10", "Delete the old notes table?", "T11", "T8", "sync was missing from the goal"} {
		if !strings.Contains(notesOut, want) {
			t.Errorf("notes board missing %q", want)
		}
	}
	for _, want := range []string{"<<<BEGIN_WORKER_OUTPUT", "<<<END_WORKER_OUTPUT", "recipes", "stopped", "T1", "Project is stopped"} {
		if !strings.Contains(recipesOut, want) {
			t.Errorf("stopped recipes board missing %q", want)
		}
	}
}
