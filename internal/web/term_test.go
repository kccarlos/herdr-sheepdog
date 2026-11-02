package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/termproto"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// The /term tests never run herdr: Deps.Term.Start returns fakeBridge,
// which delivers the first full frame the way herdr.StartBridge does.

type fakeBridge struct {
	opts herdr.BridgeOptions
	mu   sync.Mutex
	in   [][]byte
	cmds []string
	done chan struct{}
	once sync.Once
}

func (b *fakeBridge) record(s string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.done:
		return herdr.ErrBridgeClosed
	default:
	}
	b.cmds = append(b.cmds, s)
	return nil
}

func (b *fakeBridge) Input(p []byte) error {
	b.mu.Lock()
	b.in = append(b.in, append([]byte(nil), p...))
	b.mu.Unlock()
	return nil
}
func (b *fakeBridge) Resize(c, r int) error { return b.record(fmt.Sprintf("resize %dx%d", c, r)) }
func (b *fakeBridge) Scroll(up bool, lines int, col, row *int) error {
	s := fmt.Sprintf("scroll up=%t %d", up, lines)
	if col != nil {
		s += fmt.Sprintf(" at %d,%d", *col, *row)
	}
	return b.record(s)
}
func (b *fakeBridge) Mouse(a, btn string, col, row int) error {
	return b.record(fmt.Sprintf("mouse %s %s %d,%d", a, btn, col, row))
}
func (b *fakeBridge) Release()              { b.end(herdr.CloseReleased) }
func (b *fakeBridge) Done() <-chan struct{} { return b.done }

func (b *fakeBridge) end(r herdr.CloseReason) {
	b.once.Do(func() {
		close(b.done)
		b.opts.OnClose(herdr.Closed{Reason: r, Message: string(r)})
	})
}

func (b *fakeBridge) frame(seq uint32, full bool, data string) {
	b.opts.OnFrame(herdr.TerminalFrame{Seq: seq, Width: b.opts.Cols, Height: b.opts.Rows, Full: full, Bytes: []byte(data)})
}

func (b *fakeBridge) commands() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.cmds...)
}

func (b *fakeBridge) inputs() [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([][]byte(nil), b.in...)
}

func (b *fakeBridge) released() bool {
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

type fakeTerm struct {
	mu      sync.Mutex
	version string
	refuse  error
	bridges []*fakeBridge
}

func (f *fakeTerm) deps() *TermDeps {
	return &TermDeps{
		Bin: "/fake/herdr", Socket: "/fake/herdr.sock",
		Version: func(context.Context) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.version, nil
		},
		Start: func(_ context.Context, o herdr.BridgeOptions) (TermBridge, error) {
			f.mu.Lock()
			refuse := f.refuse
			f.mu.Unlock()
			if refuse != nil {
				return nil, refuse
			}
			b := &fakeBridge{opts: o, done: make(chan struct{})}
			f.mu.Lock()
			f.bridges = append(f.bridges, b)
			f.mu.Unlock()
			b.frame(1, true, "\x1b[?2026hfirst\x1b[?2026l")
			return b, nil
		},
	}
}

func (f *fakeTerm) bridge(t *testing.T, i int) *fakeBridge {
	t.Helper()
	waitFor(t, 2*time.Second, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.bridges) > i }, "bridge started")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bridges[i]
}

func newTermHarness(t *testing.T, version string) (*harness, *fakeTerm) {
	ft := &fakeTerm{version: version}
	h := newHarness(t, harnessOpts{term: ft.deps()})
	return h, ft
}

type termClient struct {
	t    *testing.T
	conn *websocket.Conn
	ctx  context.Context
}

func dialTerm(t *testing.T, h *harness, hello bool) *termClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.http.URL, "http")+"/term", nil)
	if err != nil {
		t.Fatalf("dial /term: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	c := &termClient{t: t, conn: conn, ctx: ctx}
	if hello {
		p := 1
		c.send(termproto.Message{Type: termproto.TypeHello, Protocol: &p, Client: &termproto.ClientInfo{Name: "test", Version: "1", Platform: "go"}})
		m := c.expect(termproto.TypeReady)
		if *m.Protocol != 1 || m.Limits.Channels != termproto.MaxChannels || *m.Shell {
			t.Fatalf("term-ready = %+v", m)
		}
	}
	return c
}

func (c *termClient) send(m termproto.Message) {
	c.t.Helper()
	b, _ := json.Marshal(m)
	if err := c.conn.Write(c.ctx, websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

func (c *termClient) input(ch uint16, s string) {
	c.t.Helper()
	if err := c.conn.Write(c.ctx, websocket.MessageBinary, termproto.EncodeInput(ch, []byte(s))); err != nil {
		c.t.Fatal(err)
	}
}

// next returns the next message (frame nil) or frame.
func (c *termClient) next() (termproto.Message, *termproto.Frame) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	typ, data, err := c.conn.Read(ctx)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	if typ == websocket.MessageBinary {
		f, err := termproto.DecodeFrame(data)
		if err != nil {
			c.t.Fatal(err)
		}
		return termproto.Message{}, &f
	}
	m, err := termproto.Parse(data)
	if err != nil {
		c.t.Fatalf("bad message %s: %v", data, err)
	}
	return m, nil
}

func (c *termClient) expect(typ string) termproto.Message {
	c.t.Helper()
	m, f := c.next()
	if f != nil || m.Type != typ {
		c.t.Fatalf("got %+v / frame %+v, want %s", m, f, typ)
	}
	return m
}

func (c *termClient) expectFrame() termproto.Frame {
	c.t.Helper()
	m, f := c.next()
	if f == nil {
		c.t.Fatalf("got %+v, want a frame", m)
	}
	return *f
}

func openMsg(ch int, pane, mode string) termproto.Message {
	return termproto.Message{Type: termproto.TypeOpen, Ch: ch, Target: &termproto.Target{PaneID: pane}, Mode: mode, Cols: 58, Rows: 30}
}

func ptr(i int) *int { return &i }

func termEvents(h *harness, typ string) []map[string]any {
	var out []map[string]any
	for _, ev := range h.log.Tail(0) {
		if ev.Type == typ {
			var m map[string]any
			_ = json.Unmarshal(ev.Payload, &m)
			out = append(out, m)
		}
	}
	return out
}

func TestTermFeatureFollowsHerdrVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{{"0.9.5", true}, {"0.9.1", false}} {
		h, _ := newTermHarness(t, tc.version)
		c := dialWSRaw(t, h)
		c.expect(wsproto.TypeStatus)
		c.send(wsproto.ClientReady(1, iosClient))
		m := c.expect(wsproto.TypeBotReady)
		if got := slices.Contains(m.Features, wsproto.FeatureTerm); got != tc.want {
			t.Errorf("herdr %s: features %v, term listed = %v", tc.version, m.Features, got)
		}
		var hello HelloResponse
		if code := h.getJSON("/api/hello", &hello); code != http.StatusOK {
			t.Fatalf("herdr %s: /api/hello = %d", tc.version, code)
		}
		if tc.want && hello.TermUnavailable != "" {
			t.Errorf("herdr %s: available terminal has reason %q", tc.version, hello.TermUnavailable)
		}
		if !tc.want && !strings.Contains(hello.TermUnavailable, herdr.TerminalMinVersion) {
			t.Errorf("herdr %s: term_unavailable = %q, want the version reason", tc.version, hello.TermUnavailable)
		}
		resp, err := http.Get(h.http.URL + "/term")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if !tc.want && resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("herdr %s: GET /term = %d, want 503", tc.version, resp.StatusCode)
		}
	}
	// A backend whose Check fails (herdr-core without terminal.attach):
	// not listed, and /term says why in plain words.
	ft := &fakeTerm{}
	deps := ft.deps()
	deps.Version = nil
	deps.Check = func(context.Context) error { return errors.New("no terminal.attach") }
	deps.Off = "the live terminal needs herdr-core with terminal.attach"
	off := newHarness(t, harnessOpts{term: deps})
	resp, err := http.Get(off.http.URL + "/term")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "herdr-core") {
		t.Errorf("GET /term without a live terminal = %d %s", resp.StatusCode, body)
	}
	var hello HelloResponse
	if code := off.getJSON("/api/hello", &hello); code != http.StatusOK || hello.TermUnavailable != deps.Off {
		t.Errorf("/api/hello term_unavailable = %d %q, want %q", code, hello.TermUnavailable, deps.Off)
	}
	oc := dialWSRaw(t, off)
	oc.expect(wsproto.TypeStatus)
	oc.send(wsproto.ClientReady(1, iosClient))
	if m := oc.expect(wsproto.TypeBotReady); slices.Contains(m.Features, wsproto.FeatureTerm) {
		t.Errorf("failed Check lists term: %v", m.Features)
	}
	// No Deps.Term (herdr mock mode): never listed.
	h := newHarness(t, harnessOpts{})
	c := dialWSRaw(t, h)
	c.expect(wsproto.TypeStatus)
	c.send(wsproto.ClientReady(1, iosClient))
	if m := c.expect(wsproto.TypeBotReady); slices.Contains(m.Features, wsproto.FeatureTerm) {
		t.Errorf("mock mode lists term: %v", m.Features)
	}
}

func TestTermNeedsADevice(t *testing.T) {
	ft := &fakeTerm{version: "0.9.5"}
	h := newHarness(t, harnessOpts{term: ft.deps()})
	d := h.pairDevice()
	h.asRemote()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(h.http.URL, "http") + "/term"
	if _, resp, err := websocket.Dial(ctx, url, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no ticket: err %v", err)
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + ticketPrefix + d.ticket}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.CloseNow()
}

func TestTermWindowLifecycle(t *testing.T) {
	h, ft := newTermHarness(t, "0.9.5")
	trackWorking(t, h, "w1:p2", "auth", "$ ")
	c := dialTerm(t, h, true)
	c.send(openMsg(7, "w1:p2", termproto.ModeControl))
	m := c.expect(termproto.TypeOpened)
	if m.Ch != 7 || m.PaneID != "w1:p2" || m.Mode != "control" || m.Cols != 58 || m.Rows != 30 || m.Kind != "agent" {
		t.Fatalf("term-opened = %+v", m)
	}
	f := c.expectFrame()
	if f.Ch != 7 || f.Seq != 1 || !f.Full || f.Cols != 58 || f.Rows != 30 || string(f.Data) != "\x1b[?2026hfirst\x1b[?2026l" {
		t.Fatalf("first frame = %+v", f)
	}
	b := ft.bridge(t, 0)
	if b.opts.Pane != "w1:p2" || b.opts.Mode != herdr.BridgeControl || b.opts.Socket != "/fake/herdr.sock" || b.opts.Takeover {
		t.Errorf("bridge opts = %+v", b.opts)
	}

	b.frame(2, false, "diff")
	if f := c.expectFrame(); f.Seq != 2 || f.Full || string(f.Data) != "diff" {
		t.Errorf("diff = %+v", f)
	}

	c.input(7, "ls\r")
	c.send(termproto.Message{Type: termproto.TypeResize, Ch: 7, Cols: 44, Rows: 28})
	c.send(termproto.Message{Type: termproto.TypeScroll, Ch: 7, Dir: "up", Lines: 3, Col: ptr(1), Row: ptr(2)})
	c.send(termproto.Message{Type: termproto.TypeMouse, Ch: 7, Action: "down", Col: ptr(0), Row: ptr(5)})
	want := []string{"resize 44x28", "scroll up=true 3 at 1,2", "mouse down  0,5"}
	waitFor(t, 2*time.Second, func() bool { return len(b.commands()) == len(want) && len(b.inputs()) == 1 }, "commands reach the bridge")
	if got := b.commands(); !slices.Equal(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
	if string(b.inputs()[0]) != "ls\r" {
		t.Errorf("input = %q", b.inputs()[0])
	}

	c.send(termproto.Message{Type: termproto.TypeClose, Ch: 7})
	if m := c.expect(termproto.TypeClosed); m.Ch != 7 || m.Reason != termproto.ReasonReleased {
		t.Errorf("term-closed = %+v", m)
	}
	if !b.released() {
		t.Error("bridge not released")
	}
	opens, closes := termEvents(h, "web.term.open"), termEvents(h, "web.term.close")
	if len(opens) != 1 || opens[0]["mode"] != "control" || opens[0]["remote_kind"] != "loopback" {
		t.Errorf("web.term.open = %v", opens)
	}
	if len(closes) != 1 || closes[0]["reason"] != "released" || closes[0]["frames"] != float64(2) || closes[0]["bytes_in"] != float64(3) {
		t.Errorf("web.term.close = %v", closes)
	}
	// Screen content and input never reach the log.
	for _, ev := range h.log.Tail(0) {
		if strings.Contains(string(ev.Payload), "first") || strings.Contains(string(ev.Payload), "ls\\r") {
			t.Errorf("%s logged content: %s", ev.Type, ev.Payload)
		}
	}
}

func TestTermRefusals(t *testing.T) {
	h, ft := newTermHarness(t, "0.9.5")
	for _, p := range []string{"w1:p1", "w1:p2", "w1:p3", "w1:p4", "w1:p5"} {
		trackWorking(t, h, p, strings.ReplaceAll(p, ":", ""), "$ ")
	}
	c := dialTerm(t, h, true)

	c.send(openMsg(1, "w9:p9", termproto.ModeControl))
	if m := c.expect(termproto.TypeClosed); m.Reason != termproto.RefusedNotTracked {
		t.Errorf("untracked = %+v", m)
	}
	c.send(termproto.Message{Type: termproto.TypeOpen, Ch: 2, Target: &termproto.Target{Shell: &termproto.Shell{}}, Mode: "control", Cols: 58, Rows: 30})
	if m := c.expect(termproto.TypeClosed); m.Reason != termproto.RefusedShellOff {
		t.Errorf("shell = %+v", m)
	}

	ft.mu.Lock()
	ft.refuse = &herdr.AttachError{Closed: herdr.Closed{Reason: herdr.CloseBusy, Message: "already has an attached client"}}
	ft.mu.Unlock()
	c.send(openMsg(3, "w1:p1", termproto.ModeControl))
	if m := c.expect(termproto.TypeClosed); m.Reason != termproto.RefusedBusy || !m.CanTakeOver {
		t.Errorf("busy = %+v", m)
	}
	ft.mu.Lock()
	ft.refuse = &herdr.AttachError{Closed: herdr.Closed{Reason: herdr.CloseHerdr, Message: "failed to connect"}}
	ft.mu.Unlock()
	c.send(openMsg(3, "w1:p1", termproto.ModeControl))
	if m := c.expect(termproto.TypeClosed); m.Reason != termproto.ReasonHerdr || m.CanTakeOver {
		t.Errorf("herdr down = %+v", m)
	}
	ft.mu.Lock()
	ft.refuse = nil
	ft.mu.Unlock()

	for i := 1; i <= 4; i++ {
		c.send(openMsg(10+i, fmt.Sprintf("w1:p%d", i), termproto.ModeObserve))
		c.expect(termproto.TypeOpened)
		c.expectFrame()
	}
	c.send(openMsg(20, "w1:p5", termproto.ModeControl))
	if m := c.expect(termproto.TypeClosed); m.Reason != termproto.RefusedTooMany {
		t.Errorf("fifth channel = %+v", m)
	}
	// Input to an observer is refused, not forwarded.
	c.input(11, "x")
	if m := c.expect(termproto.TypeError); m.Ch != 11 {
		t.Errorf("observer input = %+v", m)
	}
	if got := len(termEvents(h, "web.term.refused")); got != 5 {
		t.Errorf("web.term.refused = %d, want 5", got)
	}
}

func TestTermDaemonWideLimit(t *testing.T) {
	h, _ := newTermHarness(t, "0.9.5")
	trackWorking(t, h, "w1:p1", "auth", "$ ")
	var clients []*termClient
	for i := range 3 {
		c := dialTerm(t, h, true)
		clients = append(clients, c)
		for ch := 1; ch <= 4; ch++ {
			c.send(openMsg(ch, "w1:p1", termproto.ModeObserve))
			m, _ := c.next()
			if i*4+ch <= termproto.MaxBridges {
				if m.Type != termproto.TypeOpened {
					t.Fatalf("client %d ch %d = %+v", i, ch, m)
				}
				c.expectFrame()
			} else if m.Type != termproto.TypeClosed || m.Reason != termproto.RefusedTooMany {
				t.Fatalf("client %d ch %d = %+v, want too_many", i, ch, m)
			}
		}
	}
	_ = clients
}

func TestTermHelloFirst(t *testing.T) {
	h, _ := newTermHarness(t, "0.9.5")
	c := dialTerm(t, h, false)
	c.send(openMsg(1, "w1:p1", termproto.ModeControl))
	c.expect(termproto.TypeError)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := c.conn.Read(ctx); err == nil {
		t.Fatal("connection still open")
	}
}

func TestTermBridgeEndsOnItsOwn(t *testing.T) {
	h, ft := newTermHarness(t, "0.9.5")
	trackWorking(t, h, "w1:p1", "auth", "$ ")
	c := dialTerm(t, h, true)
	for i, r := range []herdr.CloseReason{herdr.CloseTakenOver, herdr.CloseGone, herdr.CloseHerdr} {
		c.send(openMsg(i+1, "w1:p1", termproto.ModeControl))
		c.expect(termproto.TypeOpened)
		c.expectFrame()
		ft.bridge(t, i).end(r)
		want := map[herdr.CloseReason]string{herdr.CloseTakenOver: "taken_over", herdr.CloseGone: "gone", herdr.CloseHerdr: "herdr"}[r]
		if m := c.expect(termproto.TypeClosed); m.Ch != i+1 || m.Reason != want {
			t.Errorf("%s: term-closed = %+v", r, m)
		}
	}
}

func TestTermDisconnectReleasesBridges(t *testing.T) {
	h, ft := newTermHarness(t, "0.9.5")
	trackWorking(t, h, "w1:p1", "auth", "$ ")
	c := dialTerm(t, h, true)
	c.send(openMsg(1, "w1:p1", termproto.ModeControl))
	c.expect(termproto.TypeOpened)
	c.expectFrame()
	_ = c.conn.Close(websocket.StatusNormalClosure, "bye")
	b := ft.bridge(t, 0)
	waitFor(t, 2*time.Second, b.released, "bridge released on disconnect")
	waitFor(t, 2*time.Second, func() bool { return len(termEvents(h, "web.term.disconnect")) == 1 }, "disconnect logged")
	h.srv.term.mu.Lock()
	n := h.srv.term.bridges
	h.srv.term.mu.Unlock()
	if n != 0 {
		t.Errorf("bridges after disconnect = %d", n)
	}
}

func TestTermObserverResizeReopens(t *testing.T) {
	h, ft := newTermHarness(t, "0.9.5")
	trackWorking(t, h, "w1:p1", "auth", "$ ")
	c := dialTerm(t, h, true)
	c.send(openMsg(1, "w1:p1", termproto.ModeObserve))
	c.expect(termproto.TypeOpened)
	c.expectFrame()
	c.send(termproto.Message{Type: termproto.TypeResize, Ch: 1, Cols: 80, Rows: 24})
	f := c.expectFrame()
	if !f.Full || f.Cols != 80 || f.Rows != 24 {
		t.Errorf("reopened frame = %+v", f)
	}
	if !ft.bridge(t, 0).released() || ft.bridge(t, 1).opts.Cols != 80 {
		t.Error("observer not reopened at the new size")
	}
	c.send(termproto.Message{Type: termproto.TypeClose, Ch: 1})
	if m := c.expect(termproto.TypeClosed); m.Reason != "released" {
		t.Errorf("close = %+v", m)
	}
}

// Overflow: a channel whose queue is full drops its queued diffs, asks
// herdr for a full frame (same-size resize) and skips diffs until it comes.
func TestTermOverflowResyncs(t *testing.T) {
	b := &fakeBridge{done: make(chan struct{})}
	srv := &termServer{deps: TermDeps{QueueBytes: 250}}
	c := &termConn{t: srv, wake: make(chan struct{}, 1), chans: map[int]*termChan{}}
	tc := &termChan{ch: 1, mode: termproto.ModeControl, cols: 58, rows: 30, bridge: b, opened: true}
	other := &termChan{ch: 2, mode: termproto.ModeControl, opened: true, bridge: b}
	c.chans[1], c.chans[2] = tc, other
	diff := herdr.TerminalFrame{Seq: 1, Width: 58, Height: 30, Bytes: make([]byte, 60)}
	c.mu.Lock()
	c.queueFrameLocked(other, diff)
	for i := range 3 {
		diff.Seq = uint32(i + 1)
		c.queueFrameLocked(tc, diff)
	}
	c.mu.Unlock()
	if tc.stale || len(c.out) != 4 {
		t.Fatalf("before overflow: stale %v, out %d", tc.stale, len(c.out))
	}
	c.mu.Lock()
	diff.Seq = 4
	c.queueFrameLocked(tc, diff)
	c.mu.Unlock()
	if !tc.stale || len(c.out) != 1 || c.out[0].ch != 2 || tc.queued != 0 || tc.resyncs != 1 {
		t.Fatalf("after overflow: stale %v out %d queued %d resyncs %d", tc.stale, len(c.out), tc.queued, tc.resyncs)
	}
	waitFor(t, time.Second, func() bool { return slices.Equal(b.commands(), []string{"resize 58x30"}) }, "same-size resize")
	c.mu.Lock()
	diff.Seq = 5
	c.queueFrameLocked(tc, diff) // a diff while stale: dropped
	full := herdr.TerminalFrame{Seq: 6, Width: 58, Height: 30, Full: true, Bytes: []byte("all")}
	c.queueFrameLocked(tc, full)
	c.mu.Unlock()
	if tc.stale || len(c.out) != 2 {
		t.Fatalf("after full frame: stale %v out %d", tc.stale, len(c.out))
	}
	if f, _ := termproto.DecodeFrame(c.out[1].data); f.Seq != 6 || !f.Full {
		t.Errorf("queued = %+v", f)
	}
}

func TestTermRemoteKind(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:5000": "loopback", "[::1]:80": "loopback", "100.101.102.103:1": "tailscale", "192.168.1.9:1": "lan", "junk": "lan",
		"[fd7a:115c:a1e0::1]:7777": "tailscale", "[::ffff:100.64.0.9]:1": "tailscale", "[fd7a:115d::1]:1": "lan", "[fe80::1]:1": "lan",
	} {
		if got := remoteKind(in); got != want {
			t.Errorf("remoteKind(%q) = %q, want %q", in, got, want)
		}
	}
}

var _ = errors.New

func TestTermObserverToldWhenControlIsFree(t *testing.T) {
	h, _ := newTermHarness(t, "0.9.5")
	trackWorking(t, h, "w1:p2", "auth", "$ ")
	phone := dialTerm(t, h, true)
	phone.send(openMsg(1, "w1:p2", termproto.ModeControl))
	phone.expect(termproto.TypeOpened)
	phone.expectFrame()
	ipad := dialTerm(t, h, true)
	ipad.send(openMsg(4, "w1:p2", termproto.ModeObserve))
	ipad.expect(termproto.TypeOpened)
	ipad.expectFrame()

	phone.send(termproto.Message{Type: termproto.TypeClose, Ch: 1})
	phone.expect(termproto.TypeClosed)
	if m := ipad.expect(termproto.TypeFree); m.Ch != 4 || m.PaneID != "w1:p2" {
		t.Errorf("term-free = %+v", m)
	}

	// A controller that leaves with its connection frees the pane too.
	ipad.send(termproto.Message{Type: termproto.TypeClose, Ch: 4})
	ipad.expect(termproto.TypeClosed)
	ipad.send(openMsg(5, "w1:p2", termproto.ModeControl))
	ipad.expect(termproto.TypeOpened)
	ipad.expectFrame()
	phone.send(openMsg(2, "w1:p2", termproto.ModeObserve))
	phone.expect(termproto.TypeOpened)
	phone.expectFrame()
	_ = ipad.conn.Close(websocket.StatusNormalClosure, "bye")
	if m := phone.expect(termproto.TypeFree); m.Ch != 2 {
		t.Errorf("term-free after disconnect = %+v", m)
	}
}
