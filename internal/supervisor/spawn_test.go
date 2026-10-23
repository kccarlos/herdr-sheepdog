package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// spawnWorld is a harness with a project root holding bench, app and a
// hidden directory, and a fake herdr that opens panes w20:p1, w20:p2, …
type spawnWorld struct {
	*harness
	root  string // the project root, symlinks resolved
	bench string // root/bench
	// fgBusy, while it returns true, makes pane.process_info report a
	// child (not the shell) in the foreground.
	fgBusy func() bool

	mu sync.Mutex
	// startAs is the state herdr detects a started agent at; noStart
	// makes every launch fail.
	startAs herdr.State
	noStart bool
}

func shellInfo(busy func() bool) map[string]any {
	if busy != nil && busy() {
		return map[string]any{"pane_id": "w20:p1", "shell_pid": 100, "foreground_process_group_id": 200,
			"foreground_processes": []map[string]any{{"pid": 200, "name": "git"}}}
	}
	return map[string]any{"pane_id": "w20:p1", "shell_pid": 100, "foreground_process_group_id": 100,
		"foreground_processes": []map[string]any{{"pid": 100, "name": "zsh"}}}
}

func newSpawnWorld(t *testing.T) *spawnWorld {
	t.Helper()
	return newSpawnWorldWith(t, newHarness)
}

// newSpawnWorldWith builds the world on a harness from mk (newHarness,
// or an orchestrator harness).
func newSpawnWorldWith(t *testing.T, mk func(*testing.T, ...func(*Config)) *harness) *spawnWorld {
	t.Helper()
	oldPoll, oldShell, oldSettle := spawnPoll, spawnShellWait, spawnShellSettle
	spawnPoll, spawnShellWait, spawnShellSettle = 5*time.Millisecond, 50*time.Millisecond, 0
	t.Cleanup(func() { spawnPoll, spawnShellWait, spawnShellSettle = oldPoll, oldShell, oldSettle })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"bench", "app", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h := mk(t, func(c *Config) { c.ProjectRoots = []string{root} })
	w := &spawnWorld{harness: h, root: root, bench: filepath.Join(root, "bench")}
	n := 0
	newPane := func(p json.RawMessage) herdr.PaneInfo {
		var params struct {
			Cwd string `json:"cwd"`
		}
		_ = json.Unmarshal(p, &params)
		n++
		pane := herdr.PaneInfo{PaneID: "w20:p" + string(rune('0'+n)), WorkspaceID: "w20", Cwd: params.Cwd, Status: herdr.StateUnknown}
		h.srv.SetPane(pane)
		h.srv.SetScreen(pane.PaneID, "bench ❯ ")
		return pane
	}
	h.srv.Handle("workspace.create", func(p json.RawMessage) (any, error) {
		return map[string]any{"type": "workspace_created", "root_pane": newPane(p)}, nil
	})
	h.srv.Handle("pane.split", func(p json.RawMessage) (any, error) {
		return map[string]any{"type": "pane_info", "pane": newPane(p)}, nil
	})
	h.srv.Handle("pane.process_info", func(p json.RawMessage) (any, error) {
		return map[string]any{"type": "pane_process_info", "process_info": shellInfo(w.fgBusy)}, nil
	})
	// agent.start: herdr detects the agent at w.startAs at once, or, with
	// w.noStart, never (the launch is dropped: agent.get finds nothing).
	w.startAs = herdr.StateIdle
	h.srv.Handle("agent.start", func(p json.RawMessage) (any, error) {
		var params struct {
			Name   string `json:"name"`
			Kind   string `json:"kind"`
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(p, &params)
		w.mu.Lock()
		noStart, as := w.noStart, w.startAs
		w.mu.Unlock()
		if !noStart {
			h.srv.SetAgent(herdr.AgentInfo{PaneID: params.PaneID, WorkspaceID: "w20", Agent: params.Kind, Name: params.Name, Status: as})
		}
		return map[string]any{"type": "agent_started", "agent": map[string]any{"pane_id": params.PaneID, "agent_status": "unknown", "launch_pending": true}, "argv": []string{params.Kind}}, nil
	})
	h.srv.Handle("agent.rename", func(json.RawMessage) (any, error) {
		return map[string]any{"type": "agent_info", "agent": map[string]any{}}, nil
	})
	h.srv.Handle("server.agent_manifests", func(json.RawMessage) (any, error) {
		var ms []map[string]any
		for _, k := range []string{"pi", "claude", "codex", "gemini", "agy", "opencode"} {
			ms = append(ms, map[string]any{"agent": k, "source": "remote", "source_kind": "remote", "local_override_shadowing_remote": false})
		}
		return map[string]any{"type": "agent_manifest_status", "manifests": ms}, nil
	})
	// Installed here: claude, codex and agy (not pi, gemini or opencode).
	oldLook := lookPath
	lookPath = func(name string) (string, error) {
		switch name {
		case "claude", "codex", "agy":
			return "/usr/local/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { lookPath = oldLook })
	return w
}

// startParams are the agent.start requests.
func (w *spawnWorld) startParams() []map[string]any { return w.params("agent.start") }

func (w *spawnWorld) spawn(req brain.SpawnRequest) (string, error) {
	return w.sup.spawn(context.Background(), req)
}

// params decodes every request of method into maps.
func (w *spawnWorld) params(method string) []map[string]any {
	var out []map[string]any
	for _, r := range w.srv.RequestsFor(method) {
		var m map[string]any
		_ = json.Unmarshal(r.Params, &m)
		out = append(out, m)
	}
	return out
}

// detect is the watcher seeing the new agent in pane at st.
func (w *spawnWorld) detect(pane string, from, to herdr.State) {
	w.t.Helper()
	w.srv.SetAgent(herdr.AgentInfo{PaneID: pane, WorkspaceID: "w20", Agent: "claude", Status: to, Cwd: w.bench})
	w.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: pane, From: from, To: to, At: w.clock.Now(), Agent: "claude", WorkspaceID: "w20", WorkspaceLabel: "bench", Cwd: w.bench})
}

// The whole start: the user asks, a workspace opens in the project, the
// CLI's command line is typed into its shell, the trust question is a
// normal block, and the first instruction goes only once the agent sits
// idle.
func TestSpawnStartsAgentAndSendsFirstInstructionWhenReady(t *testing.T) {
	w := newSpawnWorld(t)
	w.hear("open a cheap Claude in bench and have it run the tests")
	echo, err := w.spawn(brain.SpawnRequest{Dir: "bench", Kind: "claude", Model: "haiku", Text: "run the tests and tell me what fails"})
	if err != nil {
		t.Fatal(err)
	}
	if echo != "Starting a new Claude in bench. It has not been told anything yet: the instruction goes to it once it is ready." {
		t.Errorf("echo = %q", echo)
	}
	created := w.params("workspace.create")
	if len(created) != 1 || created[0]["cwd"] != w.bench || created[0]["label"] != "bench" || created[0]["focus"] != false {
		t.Fatalf("workspace.create = %v", created)
	}
	starts := w.startParams()
	if len(starts) != 1 || starts[0]["pane_id"] != "w20:p1" || starts[0]["kind"] != "claude" || starts[0]["timeout_ms"] != float64(60000) {
		t.Fatalf("agent.start = %v", starts)
	}
	if args, _ := starts[0]["args"].([]any); len(args) != 2 || args[0] != "--model" || args[1] != "haiku" {
		t.Errorf("agent.start args = %v", starts[0]["args"])
	}
	if name, _ := starts[0]["name"].(string); !state.IsLaunchName(name) {
		t.Errorf("agent.start name = %q, want the placeholder", name)
	}
	if len(w.srv.RequestsFor("pane.send_input")) != 0 {
		t.Error("the command line was typed into the shell")
	}
	ev := w.log.ofType("supervisor.spawn")
	if len(ev) != 1 {
		t.Fatalf("supervisor.spawn events = %d", len(ev))
	}
	if p := ev[0].Payload.(map[string]any); p["dir"] != w.bench || p["kind"] != "claude" || p["pane"] != "w20:p1" {
		t.Errorf("supervisor.spawn = %v", p)
	}

	// Claude's folder-trust question: blocked, no instruction typed.
	w.srv.SetScreen("w20:p1", fixture(t, "claude-trust-dialog.screen.txt"))
	w.detect("w20:p1", "", herdr.StateBlocked)
	waitFor(t, func() bool { return w.log.count("supervisor.spawn_ready") == 1 }, "supervisor.spawn_ready")
	time.Sleep(30 * time.Millisecond)
	if got := w.promptRequests(); len(got) != 0 {
		t.Fatalf("an instruction went to a blocked new agent: %v", got)
	}

	// Answered: idle at its prompt, and the instruction goes.
	w.srv.SetScreen("w20:p1", "❯ ")
	w.detect("w20:p1", herdr.StateBlocked, herdr.StateIdle)
	waitFor(t, func() bool { return len(w.promptRequests()) == 1 }, "the first instruction")
	if got := w.promptRequests()[0]; got[0] != "w20:p1" || !strings.HasPrefix(got[1], "run the tests and tell me what fails") {
		t.Errorf("first instruction = %v", got)
	}
	waitFor(t, func() bool { return w.log.count("supervisor.spawn_instruction") == 1 }, "supervisor.spawn_instruction")
	// The placeholder name was cleared once the launch was done, and
	// the agent never carried it.
	renames := w.params("agent.rename")
	if len(renames) != 1 || renames[0]["target"] != "w20:p1" || renames[0]["name"] != nil {
		t.Errorf("agent.rename = %v", renames)
	}
	row, _ := w.store.Get("w20:p1")
	if row.Name != "" || state.Say(row) != "Claude" {
		t.Errorf("the new agent is %q (name %q), want Claude", state.Say(row), row.Name)
	}
}

// The placeholder herdr name a start gives is never taken for the
// user's name.
func TestLaunchNameIsNotTheUsersName(t *testing.T) {
	w := newSpawnWorld(t)
	w.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w20:p1", To: herdr.StateIdle, At: w.clock.Now(), Agent: "codex", Name: state.LaunchNamePrefix + "0a1b2c3d", WorkspaceID: "w20", WorkspaceLabel: "bench", Cwd: w.bench})
	row, _ := w.store.Get("w20:p1")
	if row.Name != "" || state.CEOName(row) != "" || state.Say(row) != "Codex" {
		t.Errorf("row = name %q, said %q", row.Name, state.Say(row))
	}
}

// A project that already has a workspace gets a new pane split from it,
// not a second workspace; with no first instruction nothing is typed but
// the command line.
func TestSpawnReusesExistingWorkspace(t *testing.T) {
	w := newSpawnWorld(t)
	w.srv.SetPane(herdr.PaneInfo{PaneID: "w3:p1", WorkspaceID: "w3", Cwd: w.bench})
	w.hear("start a codex in bench")
	if _, err := w.spawn(brain.SpawnRequest{Dir: "Bench", Kind: "codex"}); err != nil {
		t.Fatal(err)
	}
	if n := len(w.params("workspace.create")); n != 0 {
		t.Errorf("workspace.create calls = %d", n)
	}
	split := w.params("pane.split")
	if len(split) != 1 || split[0]["target_pane_id"] != "w3:p1" || split[0]["cwd"] != w.bench {
		t.Fatalf("pane.split = %v", split)
	}
	if in := w.startParams(); len(in) != 1 || in[0]["kind"] != "codex" || in[0]["args"] != nil {
		t.Fatalf("agent.start = %v", in)
	}
}

// The user's phone session, 2026-10-03 22:24:11: "在 Sheepdog Bench 开一个
// 新的 Claude" split wP:p1 (the bench workspace's pane) but the new pane
// came back as wM:p3, in the kvoice workspace, and the agent was named
// "kvoice Claude 2". herdr's pane.split reads the pane to split from
// target_pane_id and, without it, splits the focused pane. This mock
// herdr behaves like the real one (verified against herdr 0.9.3): the
// new pane must land in the bench workspace, and its name follows.
func TestSpawnSplitLandsInTheProjectsWorkspace(t *testing.T) {
	w := newSpawnWorld(t)
	kvoice := filepath.Join(w.root, "kvoice")
	if err := os.Mkdir(kvoice, 0o755); err != nil {
		t.Fatal(err)
	}
	w.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "wM", Label: "kvoice", Focused: true})
	w.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "wP", Label: "bench"})
	w.srv.SetPane(herdr.PaneInfo{PaneID: "wM:p1", WorkspaceID: "wM", Cwd: kvoice})
	w.srv.SetPane(herdr.PaneInfo{PaneID: "wP:p1", WorkspaceID: "wP", Cwd: w.bench})
	n := 0
	w.srv.Handle("pane.split", func(p json.RawMessage) (any, error) {
		var params struct {
			Target string `json:"target_pane_id"`
			Cwd    string `json:"cwd"`
		}
		_ = json.Unmarshal(p, &params)
		ws := "wM" // herdr's fallback: the focused pane's workspace
		if params.Target != "" {
			ws = strings.SplitN(params.Target, ":", 2)[0]
		}
		n++
		pane := herdr.PaneInfo{PaneID: ws + ":p" + string(rune('2'+n)), WorkspaceID: ws, Cwd: params.Cwd, Status: herdr.StateUnknown}
		w.srv.SetScreen(pane.PaneID, "bench ❯ ")
		return map[string]any{"type": "pane_info", "pane": pane}, nil
	})
	w.hear("在 bench 开一个新的 Claude")
	if _, err := w.spawn(brain.SpawnRequest{Dir: "bench", Kind: "claude"}); err != nil {
		t.Fatal(err)
	}
	ev := w.log.ofType("supervisor.spawn")
	if len(ev) != 1 {
		t.Fatalf("supervisor.spawn events = %d", len(ev))
	}
	pane, _ := ev[0].Payload.(map[string]any)["pane"].(string)
	if !strings.HasPrefix(pane, "wP:") {
		t.Fatalf("the new agent's pane = %s, want one in the bench workspace wP", pane)
	}
	w.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: pane, To: herdr.StateIdle, At: w.clock.Now(), Agent: "claude", WorkspaceID: "wP", WorkspaceLabel: "bench", Cwd: w.bench})
	w.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "wM:p1", To: herdr.StateIdle, At: w.clock.Now(), Agent: "claude", WorkspaceID: "wM", WorkspaceLabel: "kvoice", Cwd: kvoice})
	row, _ := w.store.Get(pane)
	if got := state.Say(row); got != "bench Claude" {
		t.Errorf("the new agent is called %q, want %q", got, "bench Claude")
	}
}

// Authorisation, not judgement: no words of the user's, words too old, a
// tap on a question, a "no" or a filler start nothing.
func TestSpawnRailNeedsTheCEOsRecentWords(t *testing.T) {
	cases := []struct {
		name  string
		setup func(w *spawnWorld)
		want  string
	}{
		{"nothing said", func(*spawnWorld) {}, "said nothing"},
		{"too old", func(w *spawnWorld) { w.hear("open a claude in bench"); w.clock.Advance(31 * time.Second) }, "authorise_window_s"},
		{"tap", func(w *spawnWorld) {
			w.sup.mu.Lock()
			w.sup.addInputLocked(&shepherdInput{kind: InputTap, text: "Yes"})
			w.sup.mu.Unlock()
		}, "tap"},
		{"no", func(w *spawnWorld) { w.hear("no") }, "does not ask"},
		{"filler", func(w *spawnWorld) { w.hear("um") }, "asks for nothing"},
		{"words already answered a prompt", func(w *spawnWorld) {
			w.hear("yes, and open a claude in bench")
			w.sup.mu.Lock()
			w.sup.lastInputLocked().used = true
			w.sup.mu.Unlock()
		}, "already answered"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newSpawnWorld(t)
			c.setup(w)
			_, err := w.spawn(brain.SpawnRequest{Dir: "bench", Kind: "claude"})
			if !errors.Is(err, brain.ErrNotPermitted) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want ErrNotPermitted with %q", err, c.want)
			}
			if n := len(w.srv.RequestsFor("workspace.create")) + len(w.srv.RequestsFor("agent.start")); n != 0 {
				t.Errorf("herdr spawn calls = %d", n)
			}
			if w.log.count("supervisor.spawn_refused") != 1 {
				t.Errorf("no supervisor.spawn_refused")
			}
		})
	}
}

// No arbitrary paths: outside every project root, the root itself, and
// a symlink inside a root that points out of it are all refused, by
// path and by name.
func TestSpawnRefusesPathsOutsideProjectRoots(t *testing.T) {
	w := newSpawnWorld(t)
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Symlink(outside, filepath.Join(w.root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{outside, w.root, filepath.Join(w.root, "escape"), "escape", "../bench", "/etc"} {
		w.hear("open a claude in " + dir)
		_, err := w.spawn(brain.SpawnRequest{Dir: dir, Kind: "claude"})
		if err == nil {
			t.Errorf("%s: spawned", dir)
		}
	}
	// A shell pane sitting above an outside directory (the home
	// directory, say) does not open everything below it.
	parent := filepath.Dir(outside)
	w.srv.SetPane(herdr.PaneInfo{PaneID: "w9:p1", WorkspaceID: "w9", Cwd: parent})
	w.hear("open a claude in " + outside)
	if _, err := w.spawn(brain.SpawnRequest{Dir: outside, Kind: "claude"}); err == nil {
		t.Errorf("%s below a workspace's directory: spawned", outside)
	}
	w.hear("open a claude in there")
	if _, err := w.spawn(brain.SpawnRequest{Dir: "bench", Kind: "gemini"}); err == nil {
		t.Error("spawned an unknown kind")
	}
	if n := len(w.srv.RequestsFor("workspace.create")) + len(w.srv.RequestsFor("pane.split")); n != 0 {
		t.Errorf("herdr pane calls = %d", n)
	}
	// A path inside a root is allowed even below the first level.
	deep := filepath.Join(w.bench, "sub")
	if err := os.Mkdir(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	w.hear("open a claude in bench/sub")
	if _, err := w.spawn(brain.SpawnRequest{Dir: deep, Kind: "claude"}); err != nil {
		t.Errorf("inside a root: %v", err)
	}
}

// The candidates are a fact the orchestrator reads when a name does not
// resolve: the workspaces' directories and the roots' subdirectories,
// hidden ones left out.
func TestSpawnUnknownDirListsCandidates(t *testing.T) {
	w := newSpawnWorld(t)
	other, _ := filepath.EvalSymlinks(t.TempDir())
	ws := filepath.Join(other, "kvoice")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	w.srv.SetPane(herdr.PaneInfo{PaneID: "w4:p1", WorkspaceID: "w4", Cwd: ws})
	w.hear("open a claude in the website")
	_, err := w.spawn(brain.SpawnRequest{Dir: "website", Kind: "claude"})
	if err == nil || errors.Is(err, brain.ErrNotPermitted) {
		t.Fatalf("err = %v, want a question, not a rail refusal", err)
	}
	if !strings.Contains(err.Error(), "the projects are app, bench, kvoice") || strings.Contains(err.Error(), "hidden") {
		t.Errorf("err = %v", err)
	}
	// An existing workspace's directory is a project, wherever it is.
	if _, err := w.spawn(brain.SpawnRequest{Dir: "kvoice", Kind: "claude"}); err != nil {
		t.Errorf("workspace dir: %v", err)
	}
}

// One input starts one agent: the second spawn on the same words is
// refused until the user says something new.
func TestSpawnConsumesTheWords(t *testing.T) {
	w := newSpawnWorld(t)
	w.hear("open two claudes in app")
	if _, err := w.spawn(brain.SpawnRequest{Dir: "app", Kind: "claude"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.spawn(brain.SpawnRequest{Dir: "app", Kind: "claude"}); !errors.Is(err, brain.ErrNotPermitted) || !strings.Contains(err.Error(), "already") {
		t.Fatalf("second spawn on the same words: %v", err)
	}
	w.sup.mu.Lock()
	used := w.sup.lastInputLocked().used
	w.sup.mu.Unlock()
	if !used {
		t.Error("the spawn did not mark the words used: they could still answer a prompt")
	}
	w.hear("yes, another one")
	if _, err := w.spawn(brain.SpawnRequest{Dir: "app", Kind: "claude"}); err != nil {
		t.Fatalf("after new words: %v", err)
	}
}

// Runaway cap: three new agents in ten minutes, then a refusal until
// the window moves on.
func TestSpawnRateCap(t *testing.T) {
	w := newSpawnWorld(t)
	for i := 0; i < spawnMax; i++ {
		w.hear("open another claude in app")
		if _, err := w.spawn(brain.SpawnRequest{Dir: "app", Kind: "claude"}); err != nil {
			t.Fatalf("spawn %d: %v", i+1, err)
		}
	}
	w.hear("and one more")
	if _, err := w.spawn(brain.SpawnRequest{Dir: "app", Kind: "claude"}); !errors.Is(err, brain.ErrNotPermitted) || !strings.Contains(err.Error(), "the most allowed") {
		t.Fatalf("fourth spawn err = %v", err)
	}
	w.clock.Advance(spawnWindow)
	w.hear("now one more")
	if _, err := w.spawn(brain.SpawnRequest{Dir: "app", Kind: "claude"}); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

// Only a model name reaches the shell.
func TestSpawnRefusesModelThatIsNotAName(t *testing.T) {
	w := newSpawnWorld(t)
	w.hear("open a claude in bench")
	if _, err := w.spawn(brain.SpawnRequest{Dir: "bench", Kind: "claude", Model: "haiku; rm -rf ~"}); err == nil {
		t.Fatal("spawned with a shell command for a model")
	}
	if got := spawnArgs("codex", "gpt-5.5-mini"); len(got) != 2 || got[0] != "-m" || got[1] != "gpt-5.5-mini" {
		t.Errorf("codex args = %q", got)
	}
}

// A voice start herdr drops (the agent never showed up, 2026-10-04: a
// shell prompt ate the "c" of "claude" and nobody heard that it never
// started) is told to the orchestrator with the pane's last lines, and to
// the user as a daemon fact; the instruction is not kept.
func TestSpawnThatDoesNotStartTellsTheBrainAndTheUser(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("It did not start.", "")})
	w := newSpawnWorldWith(t, func(t *testing.T, opts ...func(*Config)) *harness { return newBrainHarness(t, m, opts...) })
	w.noStart = true
	w.hear("在 kvoice 开一个新的 Claude, 让它解释一下这个项目")
	w.srv.Handle("workspace.create", func(p json.RawMessage) (any, error) {
		pane := herdr.PaneInfo{PaneID: "w20:p1", WorkspaceID: "w20", Cwd: w.bench, Status: herdr.StateUnknown}
		w.srv.SetPane(pane)
		w.srv.SetScreen(pane.PaneID, "bench ❯ laude\nzsh: command not found: laude")
		return map[string]any{"type": "workspace_created", "root_pane": pane}, nil
	})
	if _, err := w.spawn(brain.SpawnRequest{Dir: "bench", Kind: "claude", Text: "explain this project"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return w.log.count("supervisor.spawn_not_ready") == 1 }, "supervisor.spawn_not_ready")
	waitFor(t, func() bool {
		for _, p := range w.pusher.all() {
			if strings.Contains(p.Text, "The new Claude in bench has not started: herdr did not see it running") && strings.Contains(p.Text, "command not found") {
				return true
			}
		}
		return false
	}, "the user's spawn fact")
	w.say("怎么样了?")
	calls := m.Calls()
	if len(calls) == 0 {
		t.Fatal("no brain turn")
	}
	text := calls[len(calls)-1].Text()
	if !strings.Contains(text, "the new Claude in bench did not start") || !strings.Contains(text, `its first instruction was not sent: "explain this project"`) || !strings.Contains(text, "command not found") {
		t.Errorf("the brain was not told:\n%s", text)
	}
	if len(w.promptRequests()) != 0 {
		t.Error("an instruction went to an agent that never started")
	}
}

// A new agent whose pane closes before it sat idle ends the watch: the
// instruction is dropped as "the pane closed", not kept for ten minutes.
func TestSpawnWatchEndsWhenThePaneCloses(t *testing.T) {
	w := newSpawnWorld(t)
	w.hear("open a Claude in bench and have it run the tests")
	if _, err := w.spawn(brain.SpawnRequest{Dir: "bench", Kind: "claude", Text: "run the tests"}); err != nil {
		t.Fatal(err)
	}
	w.detect("w20:p1", "", herdr.StateBlocked)
	waitFor(t, func() bool { return w.log.count("supervisor.spawn_ready") == 1 }, "supervisor.spawn_ready")
	w.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w20:p1", From: herdr.StateBlocked, Closed: true, At: w.clock.Now()})
	waitFor(t, func() bool { return w.log.count("supervisor.spawn_instruction_dropped") == 1 }, "supervisor.spawn_instruction_dropped")
	if p := w.log.ofType("supervisor.spawn_instruction_dropped")[0].Payload.(map[string]any); p["reason"] != "the pane closed" {
		t.Errorf("dropped = %v", p)
	}
}

// The command line is held back while a child (an rc-file update check)
// holds the pane's foreground, and typed once the shell is back.
func TestSpawnWaitsForShellForeground(t *testing.T) {
	w := newSpawnWorld(t)
	w.hear("open a Claude in bench")
	var calls int
	var mu sync.Mutex
	w.fgBusy = func() bool { mu.Lock(); defer mu.Unlock(); calls++; return calls <= 3 }
	if _, err := w.spawn(brain.SpawnRequest{Kind: brain.SpawnClaude, Dir: "app"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 4 {
		t.Errorf("process_info polled %d times, want >= 4", calls)
	}
	if n := len(w.srv.RequestsFor("agent.start")); n != 1 {
		t.Errorf("agent.start requests = %d, want 1", n)
	}
}

// A shell that never becomes foreground is typed into after the timeout.
func TestSpawnShellTimeoutTypesAnyway(t *testing.T) {
	w := newSpawnWorld(t)
	w.hear("open a Claude in bench")
	w.fgBusy = func() bool { return true }
	if _, err := w.spawn(brain.SpawnRequest{Kind: brain.SpawnClaude, Dir: "app"}); err != nil {
		t.Fatal(err)
	}
	if n := len(w.srv.RequestsFor("agent.start")); n != 1 {
		t.Errorf("agent.start requests = %d, want 1", n)
	}
}
