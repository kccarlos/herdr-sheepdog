// Package conformance holds the behaviour every herdr backend must share.
//
// The scenarios drive a small test driver rather than backend internals.
// Each backend's tests implement the driver over its fake server and the
// production client, then run RunAll, so an edge case is specified once and
// must pass on both the old herdr and herdr-core.
package conformance

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
)

// Program describes a program run in an existing shell.
type Program struct {
	Kind string
	// App is the status app name the program reports. Empty uses Kind.
	App string
	// Args are the program arguments. Resume is represented here just as
	// the agent's own command line reports it.
	Args []string
	// Reported says the program reports OSC 7501 status (hc); the old
	// backend's screen detection does not depend on this field.
	Reported bool
	// State is the state while the program runs. For an hc program that
	// does not report, the screen decides blocked versus unknown.
	State herdr.State
}

// Driver performs the machine actions a scenario needs. Its implementations
// wrap herdrtest or hctest; the assertions above them are shared.
type Driver interface {
	Backend() herdr.Backend
	ShellHost() ShellHost

	// StartPlain opens a terminal running a plain shell.
	StartPlain(context.Context, string) (herdr.PaneInfo, error)
	// Run starts p in an existing shell terminal.
	Run(context.Context, string, Program) error
	// Exit returns the terminal to its plain shell.
	Exit(context.Context, string) error
	SetScreen(context.Context, string, string) error
	// NewAgent creates the terminal for a new agent in cwd, joining the
	// folder's existing space when it has one.
	NewAgent(context.Context, string, string) (herdr.PaneInfo, error)
}

// ShellHost is the phone shell seam shared with internal/web.
type ShellHost = web.ShellHost

// ShellBook is the daemon's production phone-shell registry.
type ShellBook = web.ShellBook

// NewShellBook returns an empty phone-shell registry.
func NewShellBook() *ShellBook { return web.NewShellBook() }

// NewDriver constructs one scenario's backend over a fresh fake.
type NewDriver func(*testing.T) Driver

// Scenario is one named behaviour both backends must pass.
type Scenario struct {
	Name string
	Run  func(*testing.T, NewDriver)
}

// Scenarios is the backend conformance contract. Add an edge case here
// first; a fix for one backend may not be accepted without the other.
func Scenarios() []Scenario {
	return []Scenario{
		{
			Name: "plain shell is never an agent",
			Run:  plainShellIsNeverAnAgent,
		},
		{
			Name: "claude and codex are detected fresh and resumed",
			Run:  agentsAreDetectedFreshAndResumed,
		},
		{
			Name: "an exited agent leaves the terminal at a plain shell",
			Run:  exitedAgentIsNoLongerListed,
		},
		{
			Name: "a different agent in the same shell is detected",
			Run:  differentAgentInSameShell,
		},
		{
			Name: "phone shell open reuse and close survive agent use",
			Run:  phoneShellLifecycle,
		},
		{
			Name: "new agent in an existing folder joins its space",
			Run:  newAgentJoinsTheFoldersSpace,
		},
		{
			Name: "blocked question with options is parsed",
			Run:  blockedQuestionOptions,
		},
		{
			Name: "a periodic snapshot with no change logs nothing and emits no transition",
			Run:  unchangedSnapshotIsSilent,
		},
	}
}

// RunAll runs every scenario against one backend through newDriver.
func RunAll(t *testing.T, newDriver NewDriver) {
	t.Helper()
	for _, scenario := range Scenarios() {
		t.Run(scenario.Name, func(t *testing.T) {
			scenario.Run(t, newDriver)
		})
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

const blockedScreen = "Do you want to proceed?\n\n" +
	"› 1. Yes\n" +
	"  2. No\n\n" +
	"Enter to confirm · Esc to cancel\n"

func plainShellIsNeverAnAgent(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	if _, err := d.StartPlain(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	shells := NewShellBook()
	if _, _, err := shells.Open(ctx, d.ShellHost(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	assertAgents(t, ctx, d.Backend())
}

func agentsAreDetectedFreshAndResumed(t *testing.T, newDriver NewDriver) {
	cases := []struct {
		name string
		prog Program
		kind string
		say  string
	}{
		{
			name: "claude fresh",
			prog: Program{Kind: "claude", App: "claude-code", Reported: true, State: herdr.StateIdle},
			kind: "claude",
			say:  "Claude",
		},
		{
			name: "codex fresh",
			prog: Program{Kind: "codex", App: "codex", Reported: true, State: herdr.StateIdle},
			kind: "codex",
			say:  "Codex",
		},
		{
			name: "claude resumed",
			prog: Program{Kind: "claude", App: "claude-code", Args: []string{"--resume"}, Reported: true, State: herdr.StateIdle},
			kind: "claude",
			say:  "Claude",
		},
		{
			name: "codex resumed",
			prog: Program{Kind: "codex", App: "codex", Args: []string{"resume"}, Reported: true, State: herdr.StateIdle},
			kind: "codex",
			say:  "Codex",
		},
		{
			name: "codex foreground without status",
			prog: Program{Kind: "codex", State: herdr.StateBlocked},
			kind: "codex",
			say:  "Codex",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDriver(t)
			ctx := testContext(t)
			shells := NewShellBook()
			pane, _, err := shells.Open(ctx, d.ShellHost(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if !tc.prog.Reported {
				if err := d.SetScreen(ctx, pane.PaneID, blockedScreen); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.Run(ctx, pane.PaneID, tc.prog); err != nil {
				t.Fatal(err)
			}
			agent := oneAgent(t, ctx, d.Backend())
			if agent.PaneID != pane.PaneID || agent.Agent != tc.kind {
				t.Fatalf("agent = %+v, want pane %s kind %s", agent, pane.PaneID, tc.kind)
			}
			if got := spoken(agent); got != tc.say {
				t.Fatalf("spoken name = %q, want %q", got, tc.say)
			}
		})
	}
}

func exitedAgentIsNoLongerListed(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	store := watchState(t, ctx, d.Backend())
	shells := NewShellBook()
	pane, _, err := shells.Open(ctx, d.ShellHost(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Run(ctx, pane.PaneID, Program{Kind: "claude", App: "claude-code", Reported: true, State: herdr.StateIdle}); err != nil {
		t.Fatal(err)
	}
	oneAgent(t, ctx, d.Backend())
	waitAgentRows(t, store, 1)
	if err := d.Exit(ctx, pane.PaneID); err != nil {
		t.Fatal(err)
	}
	assertAgents(t, ctx, d.Backend())
	waitAgentRows(t, store, 0)

	snap, err := d.Backend().Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range snap.Panes {
		if p.PaneID == pane.PaneID {
			found = true
			if p.Agent != "" {
				t.Fatalf("terminal back at a shell still has agent kind %q: %+v", p.Agent, p)
			}
		}
	}
	if !found {
		t.Fatalf("terminal %s disappeared instead of returning to its shell", pane.PaneID)
	}

	if err := d.Run(ctx, pane.PaneID, Program{Kind: "codex", App: "codex", Reported: true, State: herdr.StateUnknown}); err != nil {
		t.Fatal(err)
	}
	rows := waitAgentRows(t, store, 1)
	if rows[0].PaneID != pane.PaneID || rows[0].Kind != "codex" {
		t.Fatalf("state row after the next agent = %+v, want Codex in %s", rows[0], pane.PaneID)
	}
}

func differentAgentInSameShell(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	shells := NewShellBook()
	pane, _, err := shells.Open(ctx, d.ShellHost(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Run(ctx, pane.PaneID, Program{Kind: "claude", App: "claude-code", Reported: true, State: herdr.StateIdle}); err != nil {
		t.Fatal(err)
	}
	if got := oneAgent(t, ctx, d.Backend()).Agent; got != "claude" {
		t.Fatalf("first kind = %q, want claude", got)
	}
	if err := d.Exit(ctx, pane.PaneID); err != nil {
		t.Fatal(err)
	}
	if err := d.Run(ctx, pane.PaneID, Program{Kind: "codex", App: "codex", Reported: true, State: herdr.StateIdle}); err != nil {
		t.Fatal(err)
	}
	agent := oneAgent(t, ctx, d.Backend())
	if agent.Agent != "codex" || spoken(agent) != "Codex" {
		t.Fatalf("second agent = %+v, want Codex", agent)
	}
}

func phoneShellLifecycle(t *testing.T, newDriver NewDriver) {
	t.Run("without an agent", func(t *testing.T) {
		d := newDriver(t)
		ctx := testContext(t)
		dir := t.TempDir()
		shells := NewShellBook()
		first, reused, err := shells.Open(ctx, d.ShellHost(), dir)
		if err != nil || reused {
			t.Fatalf("first open = %+v reused=%v err=%v", first, reused, err)
		}
		assertAgents(t, ctx, d.Backend())
		again, reused, err := shells.Open(ctx, d.ShellHost(), dir)
		if err != nil || !reused || again.PaneID != first.PaneID {
			t.Fatalf("reopen = %+v reused=%v err=%v, want %s", again, reused, err, first.PaneID)
		}
		if err := shells.Close(ctx, d.ShellHost(), first.PaneID); err != nil {
			t.Fatal(err)
		}
		fresh, reused, err := shells.Open(ctx, d.ShellHost(), dir)
		if err != nil || reused || fresh.PaneID == first.PaneID {
			t.Fatalf("after close = %+v reused=%v err=%v", fresh, reused, err)
		}
	})

	t.Run("with an agent running", func(t *testing.T) {
		d := newDriver(t)
		ctx := testContext(t)
		dir := t.TempDir()
		shells := NewShellBook()
		first, _, err := shells.Open(ctx, d.ShellHost(), dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Run(ctx, first.PaneID, Program{Kind: "codex", App: "codex", Reported: true, State: herdr.StateWorking}); err != nil {
			t.Fatal(err)
		}
		again, reused, err := shells.Open(ctx, d.ShellHost(), dir)
		if err != nil || !reused || again.PaneID != first.PaneID {
			t.Fatalf("reopen while running = %+v reused=%v err=%v, want %s", again, reused, err, first.PaneID)
		}
		if err := shells.Close(ctx, d.ShellHost(), first.PaneID); err != nil {
			t.Fatal(err)
		}
		assertAgents(t, ctx, d.Backend())
	})

	t.Run("after an agent ran", func(t *testing.T) {
		d := newDriver(t)
		ctx := testContext(t)
		dir := t.TempDir()
		shells := NewShellBook()
		first, _, err := shells.Open(ctx, d.ShellHost(), dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Run(ctx, first.PaneID, Program{Kind: "claude", App: "claude-code", Reported: true, State: herdr.StateIdle}); err != nil {
			t.Fatal(err)
		}
		if err := d.Exit(ctx, first.PaneID); err != nil {
			t.Fatal(err)
		}
		assertAgents(t, ctx, d.Backend())
		again, reused, err := shells.Open(ctx, d.ShellHost(), dir)
		if err != nil || !reused || again.PaneID != first.PaneID {
			t.Fatalf("reopen = %+v reused=%v err=%v, want %s", again, reused, err, first.PaneID)
		}
		if err := shells.Close(ctx, d.ShellHost(), first.PaneID); err != nil {
			t.Fatal(err)
		}
	})
}

func newAgentJoinsTheFoldersSpace(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	dir := t.TempDir()
	first, err := d.NewAgent(ctx, dir, "claude")
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.NewAgent(ctx, dir, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkspaceID == "" || second.WorkspaceID != first.WorkspaceID || second.PaneID == first.PaneID {
		t.Fatalf("agents = %+v and %+v, want two panes in one workspace", first, second)
	}
	agents, err := d.Backend().ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("agents = %+v, want Claude and Codex", agents)
	}
}

func blockedQuestionOptions(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	shells := NewShellBook()
	pane, _, err := shells.Open(ctx, d.ShellHost(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetScreen(ctx, pane.PaneID, blockedScreen); err != nil {
		t.Fatal(err)
	}
	if err := d.Run(ctx, pane.PaneID, Program{Kind: "claude", App: "claude-code", Reported: true, State: herdr.StateBlocked}); err != nil {
		t.Fatal(err)
	}
	if agent := oneAgent(t, ctx, d.Backend()); agent.Status != herdr.StateBlocked {
		t.Fatalf("agent state = %s, want blocked", agent.Status)
	}
	read, err := d.Backend().Read(ctx, pane.PaneID, herdr.SourceVisible, 0)
	if err != nil {
		t.Fatal(err)
	}
	prompt, ok := herdr.ParsePrompt(read.Text)
	if !ok {
		t.Fatalf("prompt not parsed from:\n%s", read.Text)
	}
	if prompt.Question != "Do you want to proceed?" || len(prompt.Answers()) != 2 ||
		prompt.Answers()[0].Label != "Yes" || prompt.Answers()[1].Label != "No" {
		t.Fatalf("prompt = %+v", prompt)
	}
}

func unchangedSnapshotIsSilent(t *testing.T, newDriver NewDriver) {
	d := newDriver(t)
	ctx := testContext(t)
	shells := NewShellBook()
	pane, _, err := shells.Open(ctx, d.ShellHost(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Run(ctx, pane.PaneID, Program{
		Kind: "codex", App: "codex", Reported: true, State: herdr.StateIdle,
	}); err != nil {
		t.Fatal(err)
	}
	oneAgent(t, ctx, d.Backend())

	const snapshotEvery = 25 * time.Millisecond
	log := &recordingLogger{}
	watchCtx, cancel := context.WithCancel(ctx)
	w := herdr.NewWatcher(d.Backend(), herdr.WatcherConfig{
		Debounce:         10 * time.Millisecond,
		SnapshotInterval: snapshotEvery,
		DedupWindow:      5 * time.Millisecond,
	}, log)
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(watchCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("watcher: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("watcher did not stop")
		}
	})

	transitions := w.Transitions()
	select {
	case tr, ok := <-transitions:
		if !ok {
			t.Fatal("transitions channel closed")
		}
		if tr.From != "" || tr.To != herdr.StateIdle || tr.Source != "snapshot" {
			t.Fatalf("initial transition = %+v, want snapshot to idle", tr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the initial snapshot")
	}

	// Let the initial refresh and any immediately queued socket work settle
	// before using LastBeat to count the periodic snapshots.
	time.Sleep(2 * snapshotEvery)
	before := w.LastBeat()
	log.reset()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	lastBeat := before
	for beats := 0; beats < 2; {
		select {
		case tr, ok := <-transitions:
			if !ok {
				t.Fatal("transitions channel closed")
			}
			t.Fatalf("unchanged periodic snapshot emitted transition %+v", tr)
		case <-deadline.C:
			t.Fatal("timed out waiting for periodic snapshots")
		case <-tick.C:
			if now := w.LastBeat(); now.After(lastBeat) {
				lastBeat = now
				beats++
			}
		}
	}
	select {
	case tr, ok := <-transitions:
		if !ok {
			t.Fatal("transitions channel closed")
		}
		t.Fatalf("unchanged periodic snapshot emitted transition %+v", tr)
	default:
	}
	if entries := log.snapshot(); len(entries) != 0 {
		t.Fatalf("unchanged periodic snapshot logged %+v", entries)
	}
}

type recordingLogger struct {
	mu      sync.Mutex
	entries []string
}

func (l *recordingLogger) Log(_, typ, paneID string, _ any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, typ+" "+paneID)
}

func (l *recordingLogger) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = nil
}

func (l *recordingLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

func assertAgents(t *testing.T, ctx context.Context, backend herdr.Backend) {
	t.Helper()
	agents, err := backend.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Fatalf("agents = %+v, want none", agents)
	}
}

func oneAgent(t *testing.T, ctx context.Context, backend herdr.Backend) herdr.AgentInfo {
	t.Helper()
	agents, err := backend.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("agents = %+v, want one", agents)
	}
	return agents[0]
}

// watchState runs the production watcher over backend and folds its
// transitions into the state table, the same two steps the daemon uses.
func watchState(t *testing.T, ctx context.Context, backend herdr.Backend) *state.Store {
	t.Helper()
	store, err := state.Open("", state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	watchCtx, cancel := context.WithCancel(ctx)
	w := herdr.NewWatcher(backend, herdr.WatcherConfig{
		Debounce:         10 * time.Millisecond,
		SnapshotInterval: 20 * time.Millisecond,
	}, nil)
	runDone := make(chan error, 1)
	applyDone := make(chan struct{})
	go func() { runDone <- w.Run(watchCtx) }()
	go func() {
		defer close(applyDone)
		for tr := range w.Transitions() {
			if _, _, err := store.Apply(tr); err != nil {
				t.Errorf("state.Apply(%+v): %v", tr, err)
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("watcher: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("watcher did not stop")
		}
		<-applyDone
	})
	return store
}

func waitAgentRows(t *testing.T, store *state.Store, want int) []state.Agent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows := store.List()
		if len(rows) == want {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("state agents = %+v, want %d", rows, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func spoken(agent herdr.AgentInfo) string {
	names := state.SpokenNames([]state.Agent{{
		PaneID:         agent.PaneID,
		Kind:           agent.Agent,
		WorkspaceLabel: filepath.Base(agent.WorkspaceID),
		State:          agent.Status,
		Name:           agent.Name,
	}})
	return names[agent.PaneID]
}
