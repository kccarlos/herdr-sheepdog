package web

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/pair"
	"github.com/kccarlos/herdr-sheepdog/internal/termproto"
)

// GET /term: the terminal socket (docs/dev/term-contract.md). One
// connection per app carries up to termproto.MaxChannels windows
// ("channels"), each a herdr terminal session bridge on one tracked
// agent's pane. Text frames are JSON control messages; binary frames are
// terminal frames out and raw input in. Frames are diffs, so nothing is
// dropped silently: a channel whose queue overflows drops its queued
// diffs and asks herdr for a full repaint.

// TermBridge is the part of *herdr.Bridge the terminal socket uses (tests
// pass a fake).
type TermBridge interface {
	Input(p []byte) error
	Resize(cols, rows int) error
	Scroll(up bool, lines int, col, row *int) error
	Mouse(action, button string, col, row int) error
	Release()
	Done() <-chan struct{}
}

// TermDeps back GET /term and the `term` feature; nil Deps.Term (herdr
// mock mode) means neither.
type TermDeps struct {
	// Bin is the herdr binary (config herdr.bin, resolved per open);
	// Socket is herdr's API socket (config herdr.socket).
	Bin, Socket string
	// Version returns the running herdr's version (Ping). The feature is
	// listed, and /term served, only for herdr >= herdr.TerminalMinVersion.
	Version func(ctx context.Context) (string, error)
	// Check, when set, replaces the Version check: it returns nil when the
	// backend can run a live terminal now (herdr-core: its core has
	// terminal.attach).
	Check func(ctx context.Context) error
	// Off is /term's 503 message when the feature is not available
	// (default: the herdr version message).
	Off string
	// Start runs one bridge (default herdr.StartBridge on Bin). A backend
	// that is not the old herdr brings its own and needs no Bin.
	Start func(ctx context.Context, o herdr.BridgeOptions) (TermBridge, error)
	// Ping is the WebSocket ping period (default 20 s); a client that
	// does not answer within Timeout (default 60 s) is closed.
	Ping, Timeout time.Duration
	// QueueBytes is each channel's send queue (default 256 KB).
	QueueBytes int
	// Shells opens phone shells (shell.go); nil means no shell is ever
	// offered. ProjectRoots are the folders a shell may open in (config
	// supervisor.project_roots), besides a tracked agent's own folder.
	Shells       ShellHost
	ProjectRoots []string
}

// termServer is the state shared by every /term connection.
type termServer struct {
	s    *Server
	deps TermDeps

	mu      sync.Mutex
	bridges int // open or opening, across connections
	// perDevice counts bridges per paired device, across its connections.
	perDevice map[string]int
	nextID    int
	conns     map[*termConn]struct{}
	// version cache for termAvailable
	checked time.Time
	ok      bool
	// shells tracks the live phone shells and their folders (shell.go).
	shells *ShellBook
}

func newTermServer(s *Server, d *TermDeps) *termServer {
	if d == nil {
		return nil
	}
	deps := *d
	if deps.Start == nil {
		deps.Start = func(ctx context.Context, o herdr.BridgeOptions) (TermBridge, error) {
			bin, err := herdr.ResolveBin(deps.Bin)
			if err != nil {
				return nil, err
			}
			o.Bin = bin
			b, err := herdr.StartBridge(ctx, o)
			if err != nil {
				return nil, err
			}
			return b, nil
		}
	}
	if deps.Ping <= 0 {
		deps.Ping = 20 * time.Second
	}
	if deps.Timeout <= 0 {
		deps.Timeout = 60 * time.Second
	}
	if deps.QueueBytes <= 0 {
		deps.QueueBytes = 256 << 10
	}
	return &termServer{s: s, deps: deps, shells: NewShellBook()}
}

// available reports whether the backend can run a bridge (herdr new
// enough, or the core has terminal.attach). The answer is cached for
// 30 s (a failed ping for 5 s), so bot-ready costs at most one short ping.
func (t *termServer) available() bool {
	if t == nil || (t.deps.Version == nil && t.deps.Check == nil) {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ttl := 30 * time.Second
	if !t.ok {
		ttl = 5 * time.Second
	}
	if !t.checked.IsZero() && time.Since(t.checked) < ttl {
		return t.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if t.deps.Check != nil {
		t.ok = t.deps.Check(ctx) == nil
	} else {
		v, err := t.deps.Version(ctx)
		t.ok = err == nil && herdr.TerminalSupported(v)
	}
	t.checked = time.Now()
	return t.ok
}

// reserve takes one of the daemon's terminal bridges (MaxBridges) and one
// of the device's (MaxChannels per paired device, across all its
// connections; this computer itself has no per-device cap).
func (t *termServer) reserve(device string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.bridges >= termproto.MaxBridges {
		return false
	}
	if device != "" && device != localDevice {
		if t.perDevice == nil {
			t.perDevice = map[string]int{}
		}
		if t.perDevice[device] >= termproto.MaxChannels {
			return false
		}
		t.perDevice[device]++
	}
	t.bridges++
	return true
}

func (t *termServer) unreserve(device string) {
	t.mu.Lock()
	t.bridges--
	if t.perDevice[device] > 0 {
		t.perDevice[device]--
		if t.perDevice[device] == 0 {
			delete(t.perDevice, device)
		}
	}
	t.mu.Unlock()
}

// remoteKind classifies the request's peer: loopback, tailscale or lan.
func remoteKind(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return pair.KindLAN
	}
	if ip.IsLoopback() {
		return "loopback"
	}
	if ip4 := ip.To4(); ip4 != nil {
		return pair.Kind(ip4, "")
	}
	return pair.Kind(ip, "")
}

// handleTerm upgrades GET /term.
func (s *Server) handleTerm(w http.ResponseWriter, r *http.Request) {
	t := s.term
	if !t.available() {
		msg := "the live terminal needs herdr " + herdr.TerminalMinVersion + " or newer"
		if t != nil && t.deps.Off != "" {
			msg = t.deps.Off
		}
		writeError(w, http.StatusServiceUnavailable, msg)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Log(logSource, "web.term.reject", "", map[string]any{"remote": r.RemoteAddr, "error": err.Error()})
		return
	}
	conn.SetReadLimit(termproto.MaxInput + 64)
	t.mu.Lock()
	t.nextID++
	id := t.nextID
	t.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	c := &termConn{t: t, conn: conn, id: id, kind: s.kindOf(r.RemoteAddr), device: deviceOf(r), ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), chans: map[int]*termChan{}}
	defer s.track(r, func() { c.end("device_revoked") })()
	t.mu.Lock()
	if t.conns == nil {
		t.conns = map[*termConn]struct{}{}
	}
	t.conns[c] = struct{}{}
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.conns, c)
		t.mu.Unlock()
	}()
	s.log.Log(logSource, "web.term.connect", "", map[string]any{"client_id": id, "remote_kind": c.kind})
	c.serve()
}

// termConn is one /term connection.
type termConn struct {
	t      *termServer
	conn   *websocket.Conn
	id     int
	kind   string
	device string // the paired device (per-device channel cap)
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}

	mu     sync.Mutex
	hello  bool
	out    []termOut
	chans  map[int]*termChan
	reason string // why the connection ended (first one wins)
	wg     sync.WaitGroup
	// writing is held while writer runs, so flush never races it.
	writing sync.WaitGroup
}

// termOut is one queued outbound message: a frame (ch > 0, binary) or a
// control message.
type termOut struct {
	ch     int
	binary bool
	data   []byte
}

// termChan is one open (or opening) window.
type termChan struct {
	ch         int
	pane       string
	kind       string // agent or shell
	mode       string
	cols, rows int
	gen        int // bridge generation: an observer reopened on resize or overflow
	bridge     TermBridge
	opened     bool // term-opened sent; frames go straight to the queue
	pending    []herdr.TerminalFrame
	closing    bool // the client closed it, or the connection ended
	queued     int  // bytes of this channel's frames in c.out
	stale      bool // diffs dropped; waiting for a full frame
	takeover   bool
	start      time.Time
	frames     int
	bytesOut   int
	bytesIn    int
	resyncs    int
	lastResize time.Time
}

func (c *termConn) serve() {
	c.writing.Add(1)
	go func() { defer c.writing.Done(); c.writer() }()
	go c.pinger()
	c.reader()
	c.end("client_gone")
	c.mu.Lock()
	reason := c.reason
	chans := make([]*termChan, 0, len(c.chans))
	for _, tc := range c.chans {
		tc.closing = true
		chans = append(chans, tc)
	}
	c.mu.Unlock()
	for _, tc := range chans {
		c.mu.Lock()
		b := tc.bridge
		c.mu.Unlock()
		if b != nil {
			c.wg.Add(1)
			go func() { defer c.wg.Done(); b.Release() }()
		}
	}
	c.wg.Wait()
	c.writing.Wait()
	c.flush()
	code := websocket.StatusNormalClosure
	if reason == "device_revoked" {
		code = closeDeviceRevoked
	}
	_ = c.conn.Close(code, reason)
	c.t.s.log.Log(logSource, "web.term.disconnect", "", map[string]any{"client_id": c.id, "reason": reason})
}

// end stops the connection, remembering why.
func (c *termConn) end(reason string) {
	c.mu.Lock()
	if c.reason == "" {
		c.reason = reason
	}
	c.mu.Unlock()
	c.cancel()
}

func (c *termConn) reader() {
	for {
		typ, data, err := c.conn.Read(c.ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
				c.end("input_too_big")
			}
			return
		}
		if typ == websocket.MessageBinary {
			c.onInput(data)
			continue
		}
		m, err := termproto.Parse(data)
		if err != nil {
			c.control(termproto.Error(m.Ch, "bad message: "+err.Error()))
			continue
		}
		c.mu.Lock()
		hello := c.hello
		c.mu.Unlock()
		if !hello && m.Type != termproto.TypeHello {
			c.control(termproto.Error(0, "send term-hello first"))
			c.end("no_hello")
			return
		}
		c.onMessage(m)
	}
}

func (c *termConn) onMessage(m termproto.Message) {
	switch m.Type {
	case termproto.TypeHello:
		c.mu.Lock()
		c.hello = true
		c.mu.Unlock()
		var info any
		if m.Client != nil {
			info = map[string]any{"name": m.Client.Name, "version": m.Client.Version, "platform": m.Client.Platform}
		}
		c.t.s.log.Log(logSource, "web.term.hello", "", map[string]any{"client_id": c.id, "protocol": *m.Protocol, "client": info})
		// Advice only: open checks the gate again on every shell.
		c.control(termproto.Ready(c.t.shellAllowed()))
	case termproto.TypeOpen:
		c.open(m)
	case termproto.TypeResize:
		c.resize(m)
	case termproto.TypeScroll:
		if b := c.controlBridge(m.Ch); b != nil {
			_ = b.Scroll(m.Dir == "up", m.Lines, m.Col, m.Row)
		}
	case termproto.TypeMouse:
		if b := c.controlBridge(m.Ch); b != nil {
			_ = b.Mouse(m.Action, m.Button, *m.Col, *m.Row)
		}
	case termproto.TypeClose:
		c.closeChan(m.Ch)
	default:
		c.control(termproto.Error(m.Ch, "not a client message: "+m.Type))
	}
}

// controlBridge is the running bridge of a control channel, or nil.
func (c *termConn) controlBridge(ch int) TermBridge {
	c.mu.Lock()
	defer c.mu.Unlock()
	tc := c.chans[ch]
	if tc == nil || tc.closing || tc.mode != termproto.ModeControl {
		return nil
	}
	return tc.bridge
}

func (c *termConn) onInput(data []byte) {
	ch, p, err := termproto.DecodeInput(data)
	if err != nil {
		c.control(termproto.Error(0, err.Error()))
		return
	}
	c.mu.Lock()
	tc := c.chans[int(ch)]
	var b TermBridge
	if tc != nil && !tc.closing && tc.mode == termproto.ModeControl {
		b = tc.bridge
		tc.bytesIn += len(p)
	}
	c.mu.Unlock()
	if b == nil {
		c.control(termproto.Error(int(ch), "no control channel"))
		return
	}
	if err := b.Input(p); err != nil {
		c.control(termproto.Error(int(ch), "input not sent: "+err.Error()))
	}
}

func (c *termConn) refuse(ch int, pane, reason string) {
	c.t.s.log.Log(logSource, "web.term.refused", pane, map[string]any{"client_id": c.id, "ch": ch, "pane_id": pane, "reason": reason})
	c.control(termproto.Closed(ch, reason))
}

func (c *termConn) open(m termproto.Message) {
	pane := m.Target.PaneID
	if m.Target.Shell != nil {
		if !c.t.shellAllowed() {
			c.refuse(m.Ch, "", termproto.RefusedShellOff)
			return
		}
		// Finding or opening the shell's pane asks herdr: off the reader.
		go c.openShell(m)
		return
	}
	kind := "agent"
	if c.t.isShell(pane) {
		// A shell opened over Tailscale stays behind the gate by pane id.
		if !c.t.shellAllowed() {
			c.refuse(m.Ch, pane, termproto.RefusedShellOff)
			return
		}
		kind = "shell"
	} else if _, ok := c.t.s.deps.Store.Get(pane); !ok {
		c.refuse(m.Ch, pane, termproto.RefusedNotTracked)
		return
	}
	c.openChan(m, pane, kind)
}

// openChan opens channel m.Ch on pane (an agent's or a phone shell's).
func (c *termConn) openChan(m termproto.Message, pane, kind string) {
	c.mu.Lock()
	if _, dup := c.chans[m.Ch]; dup {
		c.mu.Unlock()
		c.control(termproto.Error(m.Ch, "channel already open"))
		return
	}
	if len(c.chans) >= termproto.MaxChannels || !c.t.reserve(c.device) {
		c.mu.Unlock()
		c.refuse(m.Ch, pane, termproto.RefusedTooMany)
		return
	}
	tc := &termChan{ch: m.Ch, pane: pane, kind: kind, mode: m.Mode, cols: m.Cols, rows: m.Rows, takeover: m.Takeover, start: time.Now(), gen: 1}
	c.chans[m.Ch] = tc
	c.mu.Unlock()
	go c.startBridge(tc, 1, true)
}

// startBridge runs the channel's bridge (first open, or an observer
// reopened at a new size or after an overflow). gen is the generation
// the caller assigned under c.mu; callbacks of older bridges are ignored.
func (c *termConn) startBridge(tc *termChan, gen int, first bool) {
	c.mu.Lock()
	opts := herdr.BridgeOptions{
		Socket: c.t.deps.Socket, Pane: tc.pane, Mode: herdr.BridgeMode(tc.mode),
		Cols: tc.cols, Rows: tc.rows, Takeover: tc.takeover && first,
		OnFrame: func(f herdr.TerminalFrame) { c.onFrame(tc, gen, f) },
		OnClose: func(cl herdr.Closed) { c.onBridgeClosed(tc, gen, cl) },
	}
	c.mu.Unlock()
	b, err := c.t.deps.Start(c.ctx, opts)
	if err != nil {
		reason := termproto.ReasonHerdr
		var ae *herdr.AttachError
		if errors.As(err, &ae) {
			switch ae.Reason {
			case herdr.CloseBusy:
				reason = termproto.RefusedBusy
			case herdr.CloseGone:
				reason = termproto.ReasonGone
			}
		}
		c.mu.Lock()
		stale := tc.gen != gen
		if !stale {
			delete(c.chans, tc.ch)
		}
		c.mu.Unlock()
		if stale {
			return
		}
		c.t.unreserve(c.device)
		if first {
			c.refuse(tc.ch, tc.pane, reason)
		} else {
			c.finish(tc, reason)
		}
		return
	}
	c.mu.Lock()
	if tc.gen != gen {
		// Replaced while starting (an observer resized again).
		c.mu.Unlock()
		b.Release()
		return
	}
	tc.bridge = b
	closing := tc.closing
	resize := !closing && tc.mode == termproto.ModeControl && (tc.cols != opts.Cols || tc.rows != opts.Rows)
	cols, rows := tc.cols, tc.rows
	if !closing {
		if first {
			c.t.s.log.Log(logSource, "web.term.open", tc.pane, map[string]any{
				"client_id": c.id, "ch": tc.ch, "pane_id": tc.pane, "mode": tc.mode, "kind": tc.kind,
				"cols": tc.cols, "rows": tc.rows, "remote_kind": c.kind, "takeover": tc.takeover,
			})
			c.queueLocked(termOut{data: mustJSON(termproto.Opened(tc.ch, tc.pane, tc.mode, tc.cols, tc.rows, tc.kind))})
		}
		tc.opened = true
		for _, f := range tc.pending {
			c.queueFrameLocked(tc, f)
		}
		tc.pending = nil
	}
	c.mu.Unlock()
	if closing {
		b.Release()
	}
	if resize {
		// The client resized while the bridge was starting.
		_ = b.Resize(cols, rows)
	}
}

// onFrame queues one herdr frame for the client (from the bridge's drain
// goroutine: it never blocks).
func (c *termConn) onFrame(tc *termChan, gen int, f herdr.TerminalFrame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tc.gen != gen || tc.closing {
		return
	}
	if !tc.opened {
		tc.pending = append(tc.pending, f)
		return
	}
	c.queueFrameLocked(tc, f)
}

func (c *termConn) queueFrameLocked(tc *termChan, f herdr.TerminalFrame) {
	if tc.stale {
		if !f.Full {
			return
		}
		tc.stale = false
	}
	data := termproto.EncodeFrame(termproto.Frame{Ch: uint16(tc.ch), Seq: f.Seq, Cols: uint16(f.Width), Rows: uint16(f.Height), Full: f.Full, Data: f.Bytes})
	if tc.queued+len(data) > c.t.deps.QueueBytes {
		// Overflow: drop this channel's queued diffs and ask for a full
		// frame (a same-size resize); an observer, which has no stdin, is
		// reopened instead.
		kept := c.out[:0]
		for _, o := range c.out {
			if o.ch != tc.ch {
				kept = append(kept, o)
			}
		}
		c.out = kept
		tc.queued, tc.stale = 0, true
		tc.resyncs++
		if tc.mode == termproto.ModeControl {
			b, cols, rows := tc.bridge, tc.cols, tc.rows
			go func() { _ = b.Resize(cols, rows) }()
		} else {
			c.reopenLocked(tc)
		}
		return
	}
	tc.queued += len(data)
	tc.frames++
	tc.bytesOut += len(f.Bytes)
	c.queueLocked(termOut{ch: tc.ch, binary: true, data: data})
}

// reopenLocked replaces an observer's bridge (a new size, or an overflow):
// the new one starts with a full frame.
func (c *termConn) reopenLocked(tc *termChan) {
	old := tc.bridge
	tc.bridge = nil
	tc.opened = false
	tc.stale = false
	tc.pending = nil
	tc.gen++
	gen := tc.gen
	go func() {
		if old != nil {
			old.Release()
		}
		c.startBridge(tc, gen, false)
	}()
}

func (c *termConn) resize(m termproto.Message) {
	c.mu.Lock()
	tc := c.chans[m.Ch]
	if tc == nil || tc.closing {
		c.mu.Unlock()
		return
	}
	if tc.bridge == nil {
		// Still starting: startBridge applies the newest size.
		if tc.mode == termproto.ModeControl {
			tc.cols, tc.rows = m.Cols, m.Rows
		}
		c.mu.Unlock()
		return
	}
	same := tc.cols == m.Cols && tc.rows == m.Rows
	tc.cols, tc.rows = m.Cols, m.Rows
	if !same && time.Since(tc.lastResize) >= time.Second {
		tc.lastResize = time.Now()
		c.t.s.log.Log(logSource, "web.term.resize", tc.pane, map[string]any{"client_id": c.id, "ch": tc.ch, "pane_id": tc.pane, "cols": m.Cols, "rows": m.Rows})
	}
	if same {
		tc.resyncs++
	}
	if tc.mode == termproto.ModeObserve {
		// An observer renders at its own size, fixed at attach: reopen.
		c.reopenLocked(tc)
		c.mu.Unlock()
		return
	}
	b := tc.bridge
	c.mu.Unlock()
	if err := b.Resize(m.Cols, m.Rows); err != nil {
		c.control(termproto.Error(m.Ch, "resize not sent: "+err.Error()))
	}
}

// closeChan is the client's term-close: release control, keep the pane.
func (c *termConn) closeChan(ch int) {
	c.mu.Lock()
	tc := c.chans[ch]
	if tc == nil || tc.closing {
		c.mu.Unlock()
		return
	}
	tc.closing = true
	b := tc.bridge
	c.mu.Unlock()
	if b != nil {
		c.wg.Add(1)
		go func() { defer c.wg.Done(); b.Release() }()
	}
	// A bridge still starting is released when it comes up; its slot is
	// freed by onBridgeClosed or startBridge.
}

// onBridgeClosed is a bridge that ended: the client released it, another
// device took over, the pane closed, or herdr failed.
func (c *termConn) onBridgeClosed(tc *termChan, gen int, cl herdr.Closed) {
	c.mu.Lock()
	if tc.gen != gen {
		c.mu.Unlock()
		return
	}
	closing := tc.closing
	c.mu.Unlock()
	reason := termproto.ReasonHerdr
	switch {
	case closing:
		reason = termproto.ReasonReleased
	case cl.Reason == herdr.CloseTakenOver:
		reason = termproto.ReasonTakenOver
	case cl.Reason == herdr.CloseGone:
		reason = termproto.ReasonGone
	case cl.Reason == herdr.CloseReleased:
		reason = termproto.ReasonReleased
	}
	c.mu.Lock()
	delete(c.chans, tc.ch)
	c.mu.Unlock()
	c.t.unreserve(c.device)
	c.finish(tc, reason)
	if tc.kind == "shell" && reason == termproto.ReasonGone {
		c.t.forgetShell(tc.pane)
	}
	if tc.mode == termproto.ModeControl && reason == termproto.ReasonReleased {
		c.t.controlFreed(tc.pane)
	}
}

// controlFreed tells every observe channel on pane, on any connection,
// that the controller this daemon ran let go (term-free), so a watching
// phone can take control without a takeover. A controller outside the
// daemon (another herdr client) gives no such signal.
func (t *termServer) controlFreed(pane string) {
	t.mu.Lock()
	conns := make([]*termConn, 0, len(t.conns))
	for c := range t.conns {
		conns = append(conns, c)
	}
	t.mu.Unlock()
	for _, c := range conns {
		c.mu.Lock()
		for _, oc := range c.chans {
			if oc.pane == pane && oc.mode == termproto.ModeObserve && !oc.closing && oc.opened {
				c.queueLocked(termOut{data: mustJSON(termproto.Free(oc.ch, pane))})
			}
		}
		c.mu.Unlock()
	}
}

// finish tells the client a channel ended and logs its totals.
func (c *termConn) finish(tc *termChan, reason string) {
	c.mu.Lock()
	payload := map[string]any{
		"client_id": c.id, "ch": tc.ch, "pane_id": tc.pane, "reason": reason,
		"seconds": int(time.Since(tc.start).Seconds()), "frames": tc.frames,
		"bytes_out": tc.bytesOut, "bytes_in": tc.bytesIn, "resyncs": tc.resyncs,
	}
	c.mu.Unlock()
	c.t.s.log.Log(logSource, "web.term.close", tc.pane, payload)
	c.control(termproto.Closed(tc.ch, reason))
}

func mustJSON(m termproto.Message) []byte {
	b, _ := json.Marshal(m)
	return b
}

// control queues a JSON control message.
func (c *termConn) control(m termproto.Message) {
	c.mu.Lock()
	c.queueLocked(termOut{data: mustJSON(m)})
	c.mu.Unlock()
}

func (c *termConn) queueLocked(o termOut) {
	c.out = append(c.out, o)
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// writer sends the queue in order until the connection ends.
func (c *termConn) writer() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
		}
		for {
			c.mu.Lock()
			if len(c.out) == 0 {
				c.mu.Unlock()
				break
			}
			o := c.out[0]
			c.out = c.out[1:]
			if o.ch > 0 {
				if tc := c.chans[o.ch]; tc != nil {
					tc.queued -= len(o.data)
				}
			}
			c.mu.Unlock()
			typ := websocket.MessageText
			if o.binary {
				typ = websocket.MessageBinary
			}
			// Not c.ctx: a message already taken off the queue (a last
			// term-error before the connection ends) must still go out.
			ctx, cancel := context.WithTimeout(context.Background(), c.t.deps.Timeout)
			err := c.conn.Write(ctx, typ, o.data)
			cancel()
			if err != nil {
				c.end("write_failed")
				return
			}
		}
	}
}

// flush sends what is still queued (a last term-error or term-closed)
// after the connection ended, within a second.
func (c *termConn) flush() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c.mu.Lock()
	out := c.out
	c.out = nil
	c.mu.Unlock()
	for _, o := range out {
		if o.binary {
			continue
		}
		if c.conn.Write(ctx, websocket.MessageText, o.data) != nil {
			return
		}
	}
}

// pinger pings every deps.Ping; a client that does not answer within
// deps.Timeout is closed and its bridges released.
func (c *termConn) pinger() {
	tick := time.NewTicker(c.t.deps.Ping)
	defer tick.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-tick.C:
			ctx, cancel := context.WithTimeout(c.ctx, c.t.deps.Timeout)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil && c.ctx.Err() == nil {
				c.end("timeout")
				_ = c.conn.CloseNow()
				return
			}
		}
	}
}
