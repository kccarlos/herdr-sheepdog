package herdr_test

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

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
)

const fixtureDir = "../../testdata/herdr"

// recLogger records Logger calls for assertions.
type recLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

type logEntry struct {
	Source, Type, PaneID string
	Payload              any
}

func (l *recLogger) Log(source, typ, paneID string, payload any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, logEntry{source, typ, paneID, payload})
}

func (l *recLogger) count(typ string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.entries {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func codexAgent() herdr.AgentInfo {
	return herdr.AgentInfo{
		PaneID: "w5:p1", WorkspaceID: "w5", TabID: "w5:t1", TerminalID: "term_1",
		Status: herdr.StateIdle, Agent: "codex", Name: "recon", Cwd: "/repo",
	}
}

func newClient(t *testing.T, srv *herdrtest.Server) (*herdr.Client, *recLogger) {
	t.Helper()
	log := &recLogger{}
	c := herdr.NewClient(srv.Path)
	c.Log = log
	c.ReconnectBackoff = herdr.Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond}
	return c, log
}

func TestClientRoundTrips(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w5", Label: "sheepdog-recon"})
	srv.SetAgent(codexAgent())
	screen, err := os.ReadFile(filepath.Join(fixtureDir, "codex-approval-prompt.screen.txt"))
	if err != nil {
		t.Fatal(err)
	}
	srv.SetScreen("w5:p1", string(screen))
	c, log := newClient(t, srv)
	ctx := context.Background()

	pong, err := c.Ping(ctx)
	if err != nil || pong.Version != "0.9.0" || pong.Protocol != 22 {
		t.Fatalf("Ping = %+v, %v", pong, err)
	}

	agents, err := c.ListAgents(ctx)
	if err != nil || len(agents) != 1 || agents[0].PaneID != "w5:p1" || agents[0].Agent != "codex" {
		t.Fatalf("ListAgents = %+v, %v", agents, err)
	}

	a, err := c.GetAgent(ctx, "w5:p1")
	if err != nil || a.Name != "recon" || a.Status != herdr.StateIdle {
		t.Fatalf("GetAgent = %+v, %v", a, err)
	}
	if _, err := c.GetAgent(ctx, "nope"); !errors.Is(err, herdr.ErrAgentNotFound) {
		t.Fatalf("GetAgent(nope) err = %v, want ErrAgentNotFound", err)
	}

	snap, err := c.Snapshot(ctx)
	if err != nil || len(snap.Agents) != 1 || snap.WorkspaceLabel("w5") != "sheepdog-recon" || len(snap.Panes) != 1 {
		t.Fatalf("Snapshot = %+v, %v", snap, err)
	}

	rd, err := c.Read(ctx, "w5:p1", herdr.SourceVisible, 0)
	if err != nil || rd.Truncated || !strings.Contains(rd.Text, "Would you like to run the following command?") {
		t.Fatalf("Read visible = truncated=%v err=%v", rd.Truncated, err)
	}
	allLines := len(rd.Lines())
	rd, err = c.Read(ctx, "w5:p1", herdr.SourceRecentUnwrapped, 3)
	if err != nil || !rd.Truncated || len(rd.Lines()) != 3 || allLines <= 3 {
		t.Fatalf("Read lines=3: truncated=%v n=%d err=%v", rd.Truncated, len(rd.Lines()), err)
	}
	if strings.TrimSpace(rd.Lines()[2]) != "Press enter to confirm or esc to cancel" {
		t.Errorf("last line = %q", rd.Lines()[2])
	}
	reads := srv.RequestsFor("pane.read")
	if len(reads) != 2 || !strings.Contains(string(reads[1].Params), `"source":"recent_unwrapped"`) || !strings.Contains(string(reads[1].Params), `"lines":3`) {
		t.Errorf("pane.read params = %s", reads[1].Params)
	}
	if log.count("herdr.read") != 2 {
		t.Errorf("herdr.read log entries = %d, want 2", log.count("herdr.read"))
	}

	if _, err := c.Prompt(ctx, "w5:p1", "Create hello.txt"); err != nil {
		t.Fatalf("Prompt (idle) = %v", err)
	}
	srv.SetStatus("w5:p1", herdr.StateBlocked)
	_, err = c.Prompt(ctx, "w5:p1", "again")
	if !errors.Is(err, herdr.ErrAgentBlocked) {
		t.Fatalf("Prompt while blocked err = %v, want ErrAgentBlocked", err)
	}
	var he *herdr.Error
	if !errors.As(err, &he) || he.Code != "agent_blocked" || !strings.Contains(he.Message, "blocked") {
		t.Errorf("error detail = %#v", err)
	}

	if err := c.SendKeys(ctx, "w5:p1", "esc"); err != nil {
		t.Fatalf("SendKeys = %v", err)
	}
	if err := c.Interrupt(ctx, "w5:p1"); err != nil {
		t.Fatalf("Interrupt = %v", err)
	}
	keys := srv.RequestsFor("pane.send_keys")
	if len(keys) != 2 || !strings.Contains(string(keys[0].Params), `"keys":["esc"]`) || !strings.Contains(string(keys[1].Params), `"keys":["ctrl+c"]`) {
		t.Errorf("send_keys params = %v", keys)
	}
	if err := c.SendKeys(ctx, "w5:p1"); err == nil {
		t.Error("SendKeys with no keys should fail locally")
	}
	if err := c.SendKeys(ctx, "w99:p1", "enter"); !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Errorf("SendKeys to missing pane err = %v", err)
	}
	if log.count("herdr.send") != 5 {
		t.Errorf("herdr.send log entries = %d, want 5", log.count("herdr.send"))
	}

	// One connection per request: the server saw every call as a separate
	// request record, and unknown methods come back as invalid_request.
	if err := c.Call(ctx, "bogus.method", nil, nil); err == nil || !strings.Contains(err.Error(), "invalid_request") {
		t.Errorf("bogus method err = %v", err)
	}
}

func TestClientTimeoutAndDialErrors(t *testing.T) {
	srv := herdrtest.New(t)
	srv.Handle("ping", func(json.RawMessage) (any, error) {
		time.Sleep(300 * time.Millisecond)
		return map[string]any{"type": "pong"}, nil
	})
	c := herdr.NewClient(srv.Path)
	c.Timeout = 50 * time.Millisecond
	start := time.Now()
	if _, err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected timeout error")
	}
	if d := time.Since(start); d > 250*time.Millisecond {
		t.Errorf("timeout took %v", d)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Ping(ctx); err == nil {
		t.Error("cancelled ctx should fail")
	}

	dead := herdr.NewClient(filepath.Join(t.TempDir(), "none.sock"))
	if _, err := dead.Ping(context.Background()); err == nil {
		t.Error("missing socket should fail")
	}
}

// A reply that never comes after the request was written is ErrNoReply:
// the prompt probably reached herdr (live log 2026-10-27), unlike a
// dial failure where nothing was written.
func TestPromptWrittenButNoReplyIsErrNoReply(t *testing.T) {
	srv := herdrtest.New(t)
	srv.Handle("agent.prompt", func(json.RawMessage) (any, error) {
		time.Sleep(400 * time.Millisecond)
		return map[string]any{}, nil
	})
	c := herdr.NewClient(srv.Path)
	c.Timeout = 80 * time.Millisecond
	_, err := c.Prompt(context.Background(), "w5:p1", "Create hello.txt")
	if !errors.Is(err, herdr.ErrNoReply) {
		t.Fatalf("Prompt err = %v, want ErrNoReply", err)
	}
	if n := len(srv.RequestsFor("agent.prompt")); n != 1 {
		t.Errorf("server saw %d agent.prompt requests, want 1", n)
	}

	dead := herdr.NewClient(filepath.Join(t.TempDir(), "none.sock"))
	if _, err := dead.Prompt(context.Background(), "w5:p1", "x"); err == nil || errors.Is(err, herdr.ErrNoReply) {
		t.Errorf("dial failure err = %v, want an error that is not ErrNoReply", err)
	}
}

func TestParseEventFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixtureDir, "events-agent-status.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var status, fleet, other int
	at := time.Date(2026, 9, 17, 16, 54, 0, 0, time.UTC)
	var blockedSeen bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, `"subscription_started"`) {
			continue
		}
		s, f, err := herdr.ParseEvent([]byte(line), at)
		if err != nil {
			t.Fatalf("ParseEvent(%s): %v", line, err)
		}
		switch {
		case s != nil:
			status++
			if s.PaneID != "w5:p1" || s.WorkspaceID != "w5" || !s.At.Equal(at) || s.Source != "socket" {
				t.Errorf("status event fields: %+v", s)
			}
			if s.State == herdr.StateBlocked {
				blockedSeen = true
			}
		case f != nil:
			fleet++
			if f.Kind != herdr.FleetAgentDetected || f.PaneID != "w5:p1" {
				t.Errorf("fleet event fields: %+v", f)
			}
			if f.Released && f.FinalStatus != herdr.StateDone {
				t.Errorf("released event lacks final_status: %+v", f)
			}
		default:
			other++
		}
	}
	if status != 20 || fleet != 4 || other != 0 || !blockedSeen {
		t.Errorf("status=%d fleet=%d other=%d blocked=%v; want 20/4/0/true", status, fleet, other, blockedSeen)
	}

	// The lifecycle shapes from the workspace-close capture.
	wsData, err := os.ReadFile(filepath.Join(fixtureDir, "events-workspace-close.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []herdr.FleetKind
	for _, line := range strings.Split(strings.TrimSpace(string(wsData)), "\n") {
		if strings.Contains(line, `"subscription_started"`) {
			continue
		}
		_, f, err := herdr.ParseEvent([]byte(line), at)
		if err != nil {
			t.Fatal(err)
		}
		if f != nil {
			kinds = append(kinds, f.Kind)
			if f.Kind == herdr.FleetPaneCreated && (f.Pane == nil || f.PaneID != "w6:p1" || f.WorkspaceID != "w6") {
				t.Errorf("pane_created should carry the pane: %+v", f)
			}
			if f.Kind == herdr.FleetWorkspaceClosed && f.WorkspaceID != "w6" {
				t.Errorf("workspace_closed workspace = %q", f.WorkspaceID)
			}
		}
	}
	if len(kinds) != 2 || kinds[0] != herdr.FleetPaneCreated || kinds[1] != herdr.FleetWorkspaceClosed {
		t.Errorf("kinds = %v (workspace_created is ignored)", kinds)
	}

	if _, _, err := herdr.ParseEvent([]byte(`{"event":"pane.agent_status_changed","data":{"pane_id":"x","agent_status":"weird"}}`), at); err == nil {
		t.Error("invalid state should be rejected")
	}
	if _, _, err := herdr.ParseEvent([]byte(`not json`), at); err == nil {
		t.Error("garbage should be rejected")
	}
}

func TestSubscribeAndReconnect(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetAgent(codexAgent())
	c, log := newClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := c.Subscribe(ctx, "w5:p1")
	waitSubs(t, srv, 1)

	srv.EmitStatus("w5:p1", herdr.StateWorking)
	ev := recvStatus(t, ch)
	if ev.State != herdr.StateWorking || ev.Agent != "codex" || ev.At.IsZero() {
		t.Fatalf("event = %+v", ev)
	}

	// Server drops the connection: the client reconnects and keeps going.
	srv.CloseSubscriptions()
	waitSubs(t, srv, 1)
	srv.EmitStatus("w5:p1", herdr.StateBlocked)
	if ev := recvStatus(t, ch); ev.State != herdr.StateBlocked {
		t.Fatalf("after reconnect event = %+v", ev)
	}
	if log.count("herdr.reconnect") < 1 {
		t.Error("expected a herdr.reconnect log entry")
	}
	if n := len(srv.RequestsFor("events.subscribe")); n < 2 {
		t.Errorf("subscribe requests = %d, want >= 2", n)
	}

	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Error("channel not closed after cancel")
	}
}

func TestSubscribeUnknownPaneEnds(t *testing.T) {
	srv := herdrtest.New(t)
	c, _ := newClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := c.Subscribe(ctx, "w9:p9")
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected closed channel, got an event")
		}
	case <-time.After(2 * time.Second):
		t.Error("subscription for a missing pane should end")
	}
}

func TestSubscribeFleet(t *testing.T) {
	srv := herdrtest.New(t)
	c, _ := newClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := c.SubscribeFleet(ctx)
	waitSubs(t, srv, 1)

	srv.EmitAgentDetected("w5:p1", "w5", "codex", false, "")
	srv.EmitRaw(`{"data":{"agent":"codex","agent_status":"idle","pane_id":"w5:p1","workspace_id":"w5"},"event":"pane.agent_status_changed"}`)
	srv.EmitAgentDetected("w5:p1", "w5", "codex", true, herdr.StateDone)
	srv.EmitWorkspaceClosed("w5")

	got := []herdr.FleetEvent{recvFleet(t, ch), recvFleet(t, ch), recvFleet(t, ch)}
	if got[0].Kind != herdr.FleetAgentDetected || got[0].Released {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Kind != herdr.FleetAgentDetected || !got[1].Released || got[1].FinalStatus != herdr.StateDone {
		t.Errorf("second = %+v", got[1])
	}
	if got[2].Kind != herdr.FleetWorkspaceClosed || got[2].WorkspaceID != "w5" {
		t.Errorf("third = %+v", got[2])
	}
}

func recvStatus(t *testing.T, ch <-chan herdr.StatusEvent) herdr.StatusEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("status channel closed")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for status event")
	}
	return herdr.StatusEvent{}
}

func recvFleet(t *testing.T, ch <-chan herdr.FleetEvent) herdr.FleetEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("fleet channel closed")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for fleet event")
	}
	return herdr.FleetEvent{}
}

// waitSubs blocks until the fake server has exactly n open subscriptions.
func waitSubs(t *testing.T, srv *herdrtest.Server, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if srv.SubscriptionCount() == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscriptions = %d, want %d", srv.SubscriptionCount(), n)
}

// The spawn calls (docs/dev/roles.md "Starting an agent"): workspace.create
// returns the root pane, pane.split the new pane, and pane.send_input
// types the command line with its Enter in one write.
func TestClientSpawnCalls(t *testing.T) {
	srv := herdrtest.New(t)
	srv.Handle("workspace.create", func(json.RawMessage) (any, error) {
		return map[string]any{"type": "workspace_created", "root_pane": map[string]any{"pane_id": "w7:p1", "workspace_id": "w7", "cwd": "/p/app"}}, nil
	})
	srv.Handle("pane.split", func(json.RawMessage) (any, error) {
		return map[string]any{"type": "pane_info", "pane": map[string]any{"pane_id": "w7:p2", "workspace_id": "w7", "cwd": "/p/app"}}, nil
	})
	srv.Handle("pane.send_input", func(json.RawMessage) (any, error) { return map[string]any{"type": "ok"}, nil })
	c := herdr.NewClient(srv.Path)
	ctx := context.Background()
	root, err := c.CreateWorkspace(ctx, "/p/app", "app")
	if err != nil || root.PaneID != "w7:p1" {
		t.Fatalf("CreateWorkspace = %+v, %v", root, err)
	}
	pane, err := c.SplitPane(ctx, "w7:p1", "/p/app")
	if err != nil || pane.PaneID != "w7:p2" {
		t.Fatalf("SplitPane = %+v, %v", pane, err)
	}
	if err := c.SendInput(ctx, "w7:p2", "claude --model haiku", "enter"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"workspace.create": `{"cwd":"/p/app","env":{"DISABLE_AUTO_UPDATE":"true"},"focus":false,"label":"app"}`,
		"pane.split":       `{"cwd":"/p/app","direction":"right","env":{"DISABLE_AUTO_UPDATE":"true"},"focus":false,"target_pane_id":"w7:p1"}`,
		"pane.send_input":  `{"keys":["enter"],"pane_id":"w7:p2","text":"claude --model haiku"}`,
	}
	for _, r := range srv.Requests() {
		if w, ok := want[r.Method]; ok {
			if string(r.Params) != w {
				t.Errorf("%s params = %s, want %s", r.Method, r.Params, w)
			}
			delete(want, r.Method)
		}
	}
	if len(want) > 0 {
		t.Errorf("never called: %v", want)
	}
}

// tab.create (the phone shell's tab) returns the tab's root pane.
func TestClientCreateTab(t *testing.T) {
	srv := herdrtest.New(t)
	srv.Handle("tab.create", func(json.RawMessage) (any, error) {
		return map[string]any{"type": "tab_created", "tab": map[string]any{"tab_id": "w7:t2"}, "root_pane": map[string]any{"pane_id": "w7:p3", "workspace_id": "w7", "cwd": "/p/app"}}, nil
	})
	c := herdr.NewClient(srv.Path)
	pane, err := c.CreateTab(context.Background(), "w7", "/p/app", "app")
	if err != nil || pane.PaneID != "w7:p3" {
		t.Fatalf("CreateTab = %+v, %v", pane, err)
	}
	reqs := srv.Requests()
	if got := string(reqs[len(reqs)-1].Params); got != `{"cwd":"/p/app","env":{"DISABLE_AUTO_UPDATE":"true"},"focus":false,"label":"app","workspace_id":"w7"}` {
		t.Errorf("tab.create params = %s", got)
	}
}

// pane.close takes the pane as pane_id (PaneTarget), and herdr's error
// for a pane that is gone matches ErrPaneNotFound.
func TestClientClosePane(t *testing.T) {
	srv := herdrtest.New(t)
	srv.Handle("pane.close", func(p json.RawMessage) (any, error) {
		if string(p) != `{"pane_id":"w7:p2"}` {
			return nil, &herdr.Error{Code: "pane_not_found", Message: "pane not found"}
		}
		return map[string]any{"type": "ok"}, nil
	})
	c := herdr.NewClient(srv.Path)
	if err := c.ClosePane(context.Background(), "w7:p2"); err != nil {
		t.Fatal(err)
	}
	if err := c.ClosePane(context.Background(), "w9:p9"); !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Errorf("closing a gone pane = %v", err)
	}
}
