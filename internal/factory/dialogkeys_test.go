package factory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// Slice 5b row 5b.4 (design.md section 5, R3, R15, R29; KB-RUN-28, KB-RUN-43):
// a captured trust dialog gets one verified key. Sign-in, ordinary permission
// requests, quoted dialog text, a dialog whose selected action is not "Yes"
// and a recycled terminal get no keys. The tests read the keys that reach
// herdr-core through a forwarding proxy that can fail one call.

// hcProxy forwards hc calls to an hctest server, counts terminal.keys calls
// (failed ones too) and can fail the next keys calls or every read.
type hcProxy struct {
	path        string
	keyAttempts atomic.Int32
	failKeys    atomic.Int32
	failRead    atomic.Bool
}

func startHCProxy(t *testing.T, upstream string) *hcProxy {
	t.Helper()
	dir, err := os.MkdirTemp("", "hcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := &hcProxy{path: filepath.Join(dir, "hc.sock")}
	ln, err := net.Listen("unix", p.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(conn, upstream)
		}
	}()
	return p
}

func (p *hcProxy) serve(conn net.Conn, upstream string) {
	defer func() { _ = conn.Close() }()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var req struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
	}
	if json.Unmarshal(line, &req) != nil {
		return
	}
	fail := false
	switch req.Method {
	case "terminal.keys":
		p.keyAttempts.Add(1)
		for n := p.failKeys.Load(); n > 0 && !fail; n = p.failKeys.Load() {
			fail = p.failKeys.CompareAndSwap(n, n-1)
		}
	case "terminal.read":
		fail = p.failRead.Load()
	}
	if fail {
		_ = json.NewEncoder(conn).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": "injected failure"}})
		return
	}
	up, err := net.Dial("unix", upstream)
	if err != nil {
		return
	}
	defer func() { _ = up.Close() }()
	if _, err := up.Write(line); err != nil {
		return
	}
	reply, err := bufio.NewReader(up).ReadBytes('\n')
	if err == nil {
		_, _ = conn.Write(reply)
	}
}

func readScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// dialogRig is one project with one starting run on terminal t1.
type dialogRig struct {
	t     *testing.T
	srv   *hctest.Server
	proxy *hcProxy
	paths Paths
	log   *bytes.Buffer
	eng   *Engine
	id    string
	st    flow.State
	now   time.Time
}

const dialogRunID = "run-dialog"

func newDialogRig(t *testing.T, cli, screen string, status *hc.Status) *dialogRig {
	t.Helper()
	r := &dialogRig{t: t, srv: hctest.New(t), paths: testPaths(t), log: &bytes.Buffer{}, id: "proj-dialogkeys", now: time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)}
	r.proxy = startHCProxy(t, r.srv.Path)
	r.eng = r.newEngine()
	r.putTerminal(dialogRunID, 100, screen, status)
	r.st = flow.State{Seq: 1, Settings: flow.DefaultSettings(), Project: flow.Project{ID: r.id, Started: true, Run: &flow.RunRecord{
		ID: dialogRunID, Address: "t1", CreatedMS: 100, Role: flow.RoleWorker, CLI: cli,
		State: flow.PhaseStarting, StartedAt: r.now, LastActivity: r.now,
	}}}
	return r
}

func (r *dialogRig) newEngine() *Engine {
	e := New(r.paths, hc.NewClient(r.proxy.path))
	e.Now = func() time.Time { return r.now }
	e.Log = logging.NewJSONLLogger(r.log)
	return e
}

func (r *dialogRig) putTerminal(runID string, createdMS uint64, screen string, status *hc.Status) {
	r.srv.Add(hc.Terminal{Address: "t1", State: "live", CreatedMS: createdMS, Status: status, Reported: status != nil, Labels: map[string]string{"sheepdog.run": runID}})
	r.srv.SetScreen("t1", screen)
}

// pass observes the run once and runs its acts, 15 seconds after the last pass.
func (r *dialogRig) pass() {
	r.t.Helper()
	r.now = r.now.Add(15 * time.Second)
	if _, err := r.eng.advanceRunLocked(context.Background(), r.id, "", &r.st, "t1", flow.RoleWorker, r.st.Project.Run.CLI, &r.st.Project.Run); err != nil {
		r.t.Fatalf("advanceRunLocked: %v", err)
	}
}

// restart drops the engine and reloads the saved state, as a daemon restart does.
func (r *dialogRig) restart() {
	r.t.Helper()
	st, err := LoadState(r.paths.State(r.id))
	if err != nil {
		r.t.Fatalf("LoadState: %v", err)
	}
	if st.Project.Run == nil {
		r.t.Fatalf("saved state has no run: %+v", st.Project)
	}
	r.st, r.eng = st, r.newEngine()
}

// delivered returns the key lists that reached the terminal.
func (r *dialogRig) delivered() [][]string {
	var out [][]string
	for _, q := range r.srv.Requests() {
		var p struct {
			Terminal string   `json:"terminal"`
			Keys     []string `json:"keys"`
		}
		if q.Method == "terminal.keys" && json.Unmarshal(q.Params, &p) == nil {
			out = append(out, p.Keys)
		}
	}
	return out
}

// keyLogLines counts the project log lines that record a press_keys ask.
func (r *dialogRig) keyLogLines() int {
	data, err := os.ReadFile(r.paths.Log(r.id))
	if err != nil {
		return 0
	}
	n := 0
	for ln := range strings.Lines(string(data)) {
		var l LogLine
		if json.Unmarshal([]byte(ln), &l) == nil && slices.Contains(l.Asked, flow.RunActKeys) {
			n++
		}
	}
	return n
}

func (r *dialogRig) wantAttempts(when string, n int) {
	r.t.Helper()
	if got := int(r.proxy.keyAttempts.Load()); got != n {
		r.t.Errorf("%s: terminal.keys attempts = %d, want %d", when, got, n)
	}
}

func permission() *hc.Status {
	k := "permission"
	return &hc.Status{State: "blocked", Kind: &k, Msg: "Do you trust the contents of this project?"}
}

// quotedAgyTrust is the agy trust dialog text inside agent output, with more
// output after it: it is not the bottom UI.
const quotedAgyTrust = "⏺ The start screen of the agent said:\n" +
	"  ⏺ Do you trust the contents of this project?\n" +
	"  > Yes, I trust this folder\n" +
	"    No, exit\n" +
	"⏺ I pressed enter by hand earlier, so I carry on with the task now.\n" +
	"⏺ Reading internal/factory/world/world.go\n" +
	"  Type your message or @path\n\n" +
	"                                            Gemini 3.8 Flash · high\n"

// TestDialogKeysAgyTrustBlockedPermissionOnceAcrossRestart: agy reports its
// verified trust menu as blocked(permission). It gets exactly one Enter, logged
// once, and not another on later passes or after a restart (R3, KB-RUN-28).
func TestDialogKeysAgyTrustBlockedPermissionOnceAcrossRestart(t *testing.T) {
	r := newDialogRig(t, "agy", readScreen(t, "agy-trust-dialog.screen.txt"), permission())
	r.pass()
	r.wantAttempts("first pass", 1)
	if got := r.delivered(); len(got) != 1 || !slices.Equal(got[0], []string{"enter"}) {
		t.Fatalf("delivered keys = %v, want exactly [[enter]]", got)
	}
	if got := r.keyLogLines(); got != 1 {
		t.Errorf("project log lines asking press_keys = %d, want 1 (a wait or answer always logs)", got)
	}
	r.pass()
	r.pass()
	r.wantAttempts("two more passes on the same screen", 1)
	r.restart()
	r.pass()
	r.wantAttempts("after daemon restart", 1)
	if run := r.st.Project.Run; run.State == flow.PhaseEnded {
		t.Errorf("run ended (%s) while the dialog was being answered", run.Result)
	}
}

// TestDialogKeysAgyTrustWithoutStatusOnceAcrossRestart: the same screen with no
// status (before agy reports one) is answered once and not again after a restart.
// This passes before the change; it keeps the existing path from regressing.
func TestDialogKeysAgyTrustWithoutStatusOnceAcrossRestart(t *testing.T) {
	r := newDialogRig(t, "agy", readScreen(t, "agy-trust-dialog.screen.txt"), nil)
	r.pass()
	r.wantAttempts("first pass", 1)
	r.restart()
	r.pass()
	r.wantAttempts("after daemon restart", 1)
}

// TestDialogKeysNoKeyForOtherScreens: nothing but a captured, exact agy trust
// dialog with "Yes" selected gets a key, with or without a blocked status.
func TestDialogKeysNoKeyForOtherScreens(t *testing.T) {
	authKind := "auth"
	authStatus := &hc.Status{State: "blocked", Kind: &authKind, Msg: "Sign in"}
	for _, tc := range []struct {
		name, cli, screen string
		status            *hc.Status
		failRead          bool
	}{
		{"quoted dialog in agent output, no status", "agy", quotedAgyTrust, nil, false},
		{"quoted dialog in agent output, blocked permission", "agy", quotedAgyTrust, permission(), false},
		{"selected action is No, exit", "agy", "Do you trust the contents of this project?\n\nAntigravity CLI requires permission to read, edit, and execute files here.\n\n  Yes, I trust this folder\n> No, exit\n\n  ↑/↓ Navigate · enter Confirm\n                                  Gemini 3.8 Flash · high\n", permission(), false},
		{"ordinary agy permission question", "agy", readScreen(t, "agy-approval-prompt.screen.txt"), permission(), false},
		{"agy sign-in screen", "agy", "Not signed in\nPlease sign in to continue\n\n> Sign in with Google\n  Quit\n", authStatus, false},
		{"codex hooks dialog", world.CodexCLI, "Hooks need review\n\n1. Review hooks\n2. Trust all\n3. Continue without trusting\n", nil, false},
		{"codex update dialog", world.CodexCLI, readScreen(t, "codex-update-menu.screen.txt"), nil, false},
		{"claude trust dialog with its default on No", "claude", readScreen(t, "claude-trust-dialog.screen.txt"), nil, false},
		{"unreadable screen, blocked permission", "agy", readScreen(t, "agy-trust-dialog.screen.txt"), permission(), true},
		{"unreadable screen, no status", "agy", readScreen(t, "agy-trust-dialog.screen.txt"), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDialogRig(t, tc.cli, tc.screen, tc.status)
			r.proxy.failRead.Store(tc.failRead)
			r.pass()
			r.pass()
			r.wantAttempts("two passes", 0)
			if got := r.delivered(); len(got) != 0 {
				t.Errorf("keys reached the terminal: %v", got)
			}
			if r.keyLogLines() != 0 {
				t.Errorf("log claims a key press that never happened")
			}
		})
	}
}

// TestDialogKeysRecycledTerminalGetsNone: the address now holds a terminal of
// another run (label) or another creation time. It gets no key and the run is lost.
func TestDialogKeysRecycledTerminalGetsNone(t *testing.T) {
	for _, tc := range []struct {
		name      string
		runID     string
		createdMS uint64
	}{
		{"different run label", "run-someone-else", 100},
		{"different creation time", dialogRunID, 999},
	} {
		for _, st := range []*hc.Status{nil, permission()} {
			name := tc.name + ", no status"
			if st != nil {
				name = tc.name + ", blocked permission"
			}
			t.Run(name, func(t *testing.T) {
				r := newDialogRig(t, "agy", readScreen(t, "agy-trust-dialog.screen.txt"), st)
				r.putTerminal(tc.runID, tc.createdMS, readScreen(t, "agy-trust-dialog.screen.txt"), st)
				r.pass()
				r.wantAttempts("recycled terminal", 0)
				if run := r.st.Project.Run; run.Result != flow.EndLost {
					t.Errorf("run = %s(%s), want ended(lost)", run.State, run.Result)
				}
			})
		}
	}
}

// TestDialogKeysFailedSendIsNotAnswered: a key-send error is logged and does not
// mark the screen answered. The next pass sends again, once, and then stops.
func TestDialogKeysFailedSendIsNotAnswered(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status *hc.Status
	}{{"no status", nil}, {"blocked permission", permission()}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDialogRig(t, "agy", readScreen(t, "agy-trust-dialog.screen.txt"), tc.status)
			r.proxy.failKeys.Store(1)
			r.pass()
			r.wantAttempts("first pass (send fails)", 1)
			if !strings.Contains(r.log.String(), "factory.exec_error") {
				t.Errorf("failed key send not logged as factory.exec_error: %q", r.log.String())
			}
			r.pass()
			r.wantAttempts("retry pass", 2)
			if got := r.delivered(); len(got) != 1 || !slices.Equal(got[0], []string{"enter"}) {
				t.Errorf("delivered keys = %v, want exactly one [enter] after the retry", got)
			}
			r.pass()
			r.restart()
			r.pass()
			r.wantAttempts("after the retry succeeded, later passes and a restart", 2)
		})
	}
}

// TestDialogKeysRetryRechecksTerminalAndScreen: after a failed send the retry
// goes through the same identity and screen checks. A recycled terminal or a
// screen that is no longer the dialog gets no second attempt.
func TestDialogKeysRetryRechecksTerminalAndScreen(t *testing.T) {
	trust := readScreen(t, "agy-trust-dialog.screen.txt")
	for _, tc := range []struct {
		name  string
		after func(r *dialogRig)
	}{
		{"terminal recycled by another run", func(r *dialogRig) {
			r.srv.Remove("t1")
			r.putTerminal("run-someone-else", 555, trust, nil)
		}},
		{"screen moved on to the prompt", func(r *dialogRig) {
			r.srv.SetScreen("t1", "⏺ Reading the repository\n  Type your message or @path\n\n                              Gemini 3.8 Flash · high\n")
		}},
		{"screen now quotes the dialog in output", func(r *dialogRig) { r.srv.SetScreen("t1", quotedAgyTrust) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDialogRig(t, "agy", trust, nil)
			r.proxy.failKeys.Store(1)
			r.pass()
			r.wantAttempts("first pass (send fails)", 1)
			tc.after(r)
			r.pass()
			r.wantAttempts("retry pass after the change", 1)
			if got := r.delivered(); len(got) != 0 {
				t.Errorf("keys reached the terminal: %v", got)
			}
		})
	}
}
