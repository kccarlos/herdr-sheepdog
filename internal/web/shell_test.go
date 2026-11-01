package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/termproto"
)

// fakeShells is herdr's side of the phone shell: a workspace list and
// pane list that CreateWorkspace and CreateTab add to.
type fakeShells struct {
	mu      sync.Mutex
	snap    herdr.Snapshot
	creates []string // "workspace <cwd>" or "tab <ws> <cwd>"
	closed  []string
	next    int
}

func (f *fakeShells) Snapshot(context.Context) (herdr.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snap
	s.Workspaces = append([]herdr.WorkspaceInfo(nil), f.snap.Workspaces...)
	s.Panes = append([]herdr.PaneInfo(nil), f.snap.Panes...)
	return s, nil
}

func (f *fakeShells) add(ws, cwd string) herdr.PaneInfo {
	f.next++
	p := herdr.PaneInfo{PaneID: fmt.Sprintf("%s:p%d", ws, f.next), WorkspaceID: ws, Cwd: cwd}
	f.snap.Panes = append(f.snap.Panes, p)
	return p
}

func (f *fakeShells) CreateWorkspace(_ context.Context, cwd, label string) (herdr.PaneInfo, error) {
	time.Sleep(20 * time.Millisecond) // herdr takes a moment: two taps overlap
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, "workspace "+cwd)
	f.snap.Workspaces = append(f.snap.Workspaces, herdr.WorkspaceInfo{WorkspaceID: "w9", Label: label})
	return f.add("w9", cwd), nil
}

func (f *fakeShells) ShellWorkspace(snap herdr.Snapshot, _ string) string {
	for _, w := range snap.Workspaces {
		if w.Label == ShellWorkspace {
			return w.WorkspaceID
		}
	}
	return ""
}

func (f *fakeShells) IsShellPane(p herdr.PaneInfo, workspaceID string) bool {
	return workspaceID != "" && p.WorkspaceID == workspaceID
}

func (f *fakeShells) OpenShell(ctx context.Context, workspaceID, cwd string) (herdr.PaneInfo, error) {
	if workspaceID == "" {
		return f.CreateWorkspace(ctx, cwd, ShellWorkspace)
	}
	return f.CreateTab(ctx, workspaceID, cwd, filepath.Base(cwd))
}

func (f *fakeShells) CloseShell(ctx context.Context, pane string) error {
	return f.ClosePane(ctx, pane)
}

func (f *fakeShells) CreateTab(_ context.Context, ws, cwd, _ string) (herdr.PaneInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates = append(f.creates, "tab "+ws+" "+cwd)
	return f.add(ws, cwd), nil
}

func (f *fakeShells) ClosePane(_ context.Context, pane string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, pane)
	kept := f.snap.Panes[:0]
	for _, p := range f.snap.Panes {
		if p.PaneID != pane {
			kept = append(kept, p)
		}
	}
	f.snap.Panes = kept
	return nil
}

func (f *fakeShells) createCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.creates...)
}

// newShellHarness is a /term harness with phone shells, whose project root
// holds two folders, app and lib. kind stands in for the client's network.
func newShellHarness(t *testing.T, shellOn bool) (*harness, *fakeShells, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"app", "lib"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fs := &fakeShells{}
	ft := &fakeTerm{version: "0.9.5"}
	d := ft.deps()
	d.Shells, d.ProjectRoots = fs, []string{root}
	h := newHarness(t, harnessOpts{term: d})
	h.srv.shellOn.Store(shellOn)
	return h, fs, root
}

func helloReady(t *testing.T, h *harness) (*termClient, bool) {
	t.Helper()
	c := dialTerm(t, h, false)
	p := 1
	c.send(termproto.Message{Type: termproto.TypeHello, Protocol: &p})
	m := c.expect(termproto.TypeReady)
	return c, *m.Shell
}

func shellOpen(ch int, cwd string) termproto.Message {
	return termproto.Message{Type: termproto.TypeOpen, Ch: ch, Target: &termproto.Target{Shell: &termproto.Shell{Cwd: cwd}}, Mode: termproto.ModeControl, Cols: 58, Rows: 30}
}

// The gate is the computer's shell switch (web.shell), the same on every
// network: term-ready says so, and term-open enforces it.
func TestShellGate(t *testing.T) {
	for _, tc := range []struct {
		kind string
		on   bool
		want bool
	}{
		{"switch on", true, true},
		{"switch off", false, false},
	} {
		h, fs, root := newShellHarness(t, tc.on)
		c, shell := helloReady(t, h)
		if shell != tc.want {
			t.Errorf("%s: term-ready.shell = %v, want %v", tc.kind, shell, tc.want)
		}
		var hello HelloResponse
		if code := h.getJSON("/api/hello", &hello); code != http.StatusOK || hello.Shell != tc.want {
			t.Errorf("%s: /api/hello shell = %v (code %d), want %v", tc.kind, hello.Shell, code, tc.want)
		}
		c.send(shellOpen(1, filepath.Join(root, "app")))
		m := c.expect(map[bool]string{true: termproto.TypeOpened, false: termproto.TypeClosed}[tc.want])
		if !tc.want && (m.Reason != termproto.RefusedShellOff || len(fs.createCalls()) != 0) {
			t.Errorf("%s: refused = %+v, creates %v", tc.kind, m, fs.createCalls())
		}
		if tc.want && m.Kind != "shell" {
			t.Errorf("%s: term-opened = %+v", tc.kind, m)
		}
	}
	// No shell host (herdr mock mode, or an old wiring): never offered.
	h, _ := newTermHarness(t, "0.9.5")
	if _, shell := helloReady(t, h); shell {
		t.Error("term-ready.shell without a shell host")
	}
	var hello HelloResponse
	if code := h.getJSON("/api/hello", &hello); code != http.StatusOK || hello.Shell {
		t.Errorf("/api/hello shell without a shell host = %v (code %d)", hello.Shell, code)
	}
}

func TestShellIsNotAdvertisedWithoutTheLiveTerminal(t *testing.T) {
	h, _, _ := newShellHarness(t, true)
	h.srv.term.deps.Version = func(context.Context) (string, error) {
		return "", errors.New("terminal.attach missing")
	}
	h.srv.term.mu.Lock()
	h.srv.term.checked = time.Time{}
	h.srv.term.mu.Unlock()

	var hello HelloResponse
	if code := h.getJSON("/api/hello", &hello); code != http.StatusOK || hello.Shell {
		t.Fatalf("/api/hello shell without a live terminal = %v (code %d)", hello.Shell, code)
	}
}

// One shell per folder, reused: the first opens the "phone shell"
// workspace, another folder gets a tab in it, and the same folder again
// (or two quick taps at once) reuses the pane.
func TestShellOpenAndReuse(t *testing.T) {
	h, fs, root := newShellHarness(t, true)
	app, lib := filepath.Join(root, "app"), filepath.Join(root, "lib")
	c, _ := helloReady(t, h)
	c.send(shellOpen(1, app))
	c.send(shellOpen(2, app)) // the second tap, before the first is open
	panes := map[string]bool{}
	for range 2 {
		m := c.expect(termproto.TypeOpened)
		if m.Kind != "shell" {
			t.Errorf("term-opened = %+v", m)
		}
		panes[m.PaneID] = true
		c.expectFrame()
	}
	if len(panes) != 1 {
		t.Errorf("two taps opened panes %v", panes)
	}
	c.send(shellOpen(3, lib+"/"))
	m := c.expect(termproto.TypeOpened)
	c.expectFrame()
	if panes[m.PaneID] {
		t.Errorf("lib reused app's shell %s", m.PaneID)
	}
	want := []string{"workspace " + app, "tab w9 " + lib}
	if got := fs.createCalls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("creates = %q, want %q", got, want)
	}
	// After a restart the daemon finds its shells in herdr again.
	h.srv.term.mu.Lock()
	h.srv.term.shells = NewShellBook()
	h.srv.term.mu.Unlock()
	c.send(shellOpen(4, lib))
	if m2 := c.expect(termproto.TypeOpened); m2.PaneID != m.PaneID {
		t.Errorf("after a restart: %s, want %s", m2.PaneID, m.PaneID)
	}
	if n := len(fs.createCalls()); n != 2 {
		t.Errorf("creates after the restart = %d", n)
	}
	opens := termEvents(h, "web.shell.open")
	if len(opens) != 4 || opens[3]["reused"] != true {
		t.Fatalf("web.shell.open = %v", opens)
	}
	// The two taps are opened concurrently, so either may be logged
	// first: one created the shell and the other reused it.
	fresh := 0
	for _, o := range opens[:2] {
		if o["cwd"] != app || o["remote_kind"] != "loopback" {
			t.Errorf("web.shell.open for a tap = %v", o)
		}
		if o["reused"] == false {
			fresh++
		}
	}
	if fresh != 1 || opens[2]["reused"] != false || opens[2]["cwd"] != lib {
		t.Errorf("web.shell.open = %v, want one new shell for the two taps and a new one for lib", opens)
	}
	if got := termEvents(h, "web.term.open"); len(got) != 4 || got[0]["kind"] != "shell" {
		t.Errorf("web.term.open = %v", got)
	}
}

// A folder outside the project roots is refused, unless a tracked agent
// works there; an empty cwd is the first project root.
func TestShellFolder(t *testing.T) {
	h, fs, root := newShellHarness(t, true)
	outside := t.TempDir()
	c, _ := helloReady(t, h)
	for i, dir := range []string{outside, filepath.Join(root, "nope"), root + "/app/../../"} {
		c.send(shellOpen(i+1, dir))
		if m := c.expect(termproto.TypeClosed); m.Reason != termproto.RefusedOutsideProjects {
			t.Errorf("%s: %+v", dir, m)
		}
	}
	c.send(shellOpen(5, ""))
	c.expect(termproto.TypeOpened)
	c.expectFrame()
	if got := fs.createCalls(); len(got) != 1 || got[0] != "workspace "+root {
		t.Errorf("empty cwd: creates %v", got)
	}
	// A worktree herdr put outside the roots: allowed as the agent's own.
	real, _ := filepath.EvalSymlinks(outside)
	if _, _, err := h.srv.deps.Store.Apply(herdr.Transition{PaneID: "w2:p1", Agent: "claude", To: herdr.StateIdle, Cwd: real}); err != nil {
		t.Fatal(err)
	}
	c.send(shellOpen(6, outside))
	c.expect(termproto.TypeOpened)
	c.expectFrame()
}

// A shell stays gated by pane id: with the switch turned off, nobody can
// attach to it, nor close it.
func TestShellPaneGatedBySwitch(t *testing.T) {
	h, fs, root := newShellHarness(t, true)
	c, _ := helloReady(t, h)
	c.send(shellOpen(1, filepath.Join(root, "app")))
	pane := c.expect(termproto.TypeOpened).PaneID
	c.expectFrame()

	h.srv.shellOn.Store(false)
	off, _ := helloReady(t, h)
	off.send(openMsg(1, pane, termproto.ModeObserve))
	if m := off.expect(termproto.TypeClosed); m.Reason != termproto.RefusedShellOff {
		t.Errorf("attach by pane id with the switch off = %+v", m)
	}
	if code, _ := h.postJSON("/api/shells/"+pane+"/close", map[string]any{}, nil); code != http.StatusForbidden {
		t.Errorf("close with the switch off = %d", code)
	}

	h.srv.shellOn.Store(true)
	if code, _ := h.postJSON("/api/shells/w1:p1/close", map[string]any{}, nil); code != http.StatusNotFound {
		t.Errorf("closing a pane that is no shell = %d", code)
	}
	if code, body := h.postJSON("/api/shells/"+pane+"/close", map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("close = %d %s", code, body)
	}
	if len(fs.closed) != 1 || fs.closed[0] != pane || h.srv.term.isShell(pane) {
		t.Errorf("closed %v, still a shell %v", fs.closed, h.srv.term.isShell(pane))
	}
	closes := termEvents(h, "web.shell.close")
	if len(closes) != 1 || closes[0]["pane_id"] != pane || closes[0]["remote_kind"] != "loopback" {
		t.Errorf("web.shell.close = %v", closes)
	}
	// Metadata only: no screen content, no input in any shell event.
	for _, ev := range h.log.Tail(0) {
		if strings.HasPrefix(ev.Type, "web.shell") || strings.HasPrefix(ev.Type, "web.term") {
			var m map[string]any
			_ = json.Unmarshal(ev.Payload, &m)
			if strings.Contains(string(ev.Payload), "first") {
				t.Errorf("%s logged screen content: %s", ev.Type, ev.Payload)
			}
		}
	}
}

// The setup page's switch writes web.shell to the env file and applies
// at once; attachments follow it.
func TestSetupShell(t *testing.T) {
	f := newSetupFixture(t, config.Default())
	if code, body := f.post(t, "/api/setup/shell", `{"enable":false}`); code != http.StatusOK || !strings.Contains(body, `"shell": false`) {
		t.Fatalf("disable = %d %s", code, body)
	}
	if f.h.srv.shellOn.Load() {
		t.Error("not applied")
	}
	env, _ := os.ReadFile(f.env)
	if !strings.Contains(string(env), "SHEEPDOG_WEB_SHELL=false") {
		t.Errorf("env file = %q", env)
	}
	var st SetupState
	if code := f.h.getJSON("/api/setup/state", &st); code != http.StatusOK || st.Phone.Shell {
		t.Errorf("state phone.shell = %v", st.Phone.Shell)
	}
	if code, body := f.h.postJSON("/api/agents/w1:p1/attachments", map[string]any{}, nil); code != http.StatusForbidden || !strings.Contains(body, "like the shell") {
		t.Errorf("attachment with the shell off = %d %s", code, body)
	}
	if code, _ := f.post(t, "/api/setup/shell", `{"enable":true}`); code != http.StatusOK || !f.h.srv.shellOn.Load() {
		t.Error("enable not applied")
	}
	select {
	case r := <-f.restarts:
		t.Errorf("the switch restarted the daemon (%s)", r)
	default:
	}
}
