package herdr

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The terminal session bridge: one `herdr terminal session control|observe
// <pane>` child process per open phone window (docs/dev/design/phone-terminal.md
// §3). It speaks newline-delimited JSON: herdr's TerminalAnsi render stream
// comes out on stdout as terminal.frame lines (base64 ANSI diffs, each
// wrapped in DEC 2026), and a controller takes terminal.input, .resize,
// .scroll, .mouse and .release lines on stdin. There is no PTY.

// TerminalMinVersion is the oldest herdr whose bridge the daemon uses:
// `terminal session` exists since 0.7.2, terminal.mouse since 0.9.2.
const TerminalMinVersion = "0.9.2"

// TerminalSupported reports whether a herdr version (Ping's Version,
// "0.9.5" or "herdr 0.9.5-preview.1") has a bridge the daemon can use.
func TerminalSupported(version string) bool {
	return versionAtLeast(version, TerminalMinVersion)
}

// versionAtLeast compares dotted numeric versions; anything unparsable is
// too old.
func versionAtLeast(have, want string) bool {
	h, ok1 := parseVersion(have)
	w, ok2 := parseVersion(want)
	if !ok1 || !ok2 {
		return false
	}
	for i := range 3 {
		if h[i] != w[i] {
			return h[i] > w[i]
		}
	}
	return true
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimSpace(v)
	if i := strings.LastIndexByte(v, ' '); i >= 0 {
		v = v[i+1:]
	}
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// ResolveBin finds the herdr binary: the configured path when set (config
// herdr.bin), else `herdr` on PATH, else ~/.local/bin/herdr (where herdr's
// installer puts it; a launchd daemon's PATH often lacks it).
func ResolveBin(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	if p, err := exec.LookPath("herdr"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "herdr")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("herdr: binary not found (set herdr.bin)")
}

// BridgeMode is how a bridge attaches: Control resizes the pane and takes
// input; Observe renders at its own size, read-only.
type BridgeMode string

// The two bridge modes.
const (
	BridgeControl BridgeMode = "control"
	BridgeObserve BridgeMode = "observe"
)

// CloseReason is why a bridge ended, classified from herdr's
// terminal.closed reason text or the process exit.
type CloseReason string

// The close reasons (docs/dev/design/phone-terminal.md §4 term-closed).
const (
	// CloseReleased: the daemon released control (herdr says "detached").
	CloseReleased CloseReason = "released"
	// CloseTakenOver: another client attached with --takeover.
	CloseTakenOver CloseReason = "taken_over"
	// CloseBusy: another client controls the pane and takeover was not
	// asked for.
	CloseBusy CloseReason = "busy"
	// CloseGone: the pane closed or herdr does not know it.
	CloseGone CloseReason = "gone"
	// CloseHerdr: the bridge failed (no server, a crash, a handoff).
	CloseHerdr CloseReason = "herdr"
)

// classifyClose maps herdr's terminal.closed reason text (src/server/
// headless.rs) onto a CloseReason; retry reports the transient
// "has a read in progress; retry" refusal.
func classifyClose(msg string) (reason CloseReason, retry bool) {
	m := strings.ToLower(msg)
	switch {
	case m == "detached":
		return CloseReleased, false
	case strings.Contains(m, "read in progress"):
		return CloseHerdr, true
	case strings.Contains(m, "taken over"):
		return CloseTakenOver, false
	case strings.Contains(m, "already has an attached client"):
		return CloseBusy, false
	case strings.Contains(m, "not found"), strings.Contains(m, "exited"):
		return CloseGone, false
	}
	return CloseHerdr, false
}

// TerminalFrame is one terminal.frame from herdr: changed cells as ANSI.
// Seq is herdr's per-attach sequence; Full marks a complete repaint.
type TerminalFrame struct {
	Seq    uint32
	Width  int
	Height int
	Full   bool
	Bytes  []byte
}

// Closed is how a bridge ended. Message is herdr's own text (or the
// process error); it never carries screen content.
type Closed struct {
	Reason  CloseReason
	Message string
}

// BridgeOptions configure StartBridge.
type BridgeOptions struct {
	// Bin is the herdr binary (ResolveBin).
	Bin string
	// Socket is herdr's API socket (config herdr.socket). The child gets it
	// as HERDR_SOCKET_PATH and herdr derives its client socket from it;
	// every inherited HERDR_* variable is dropped, so the bridge can never
	// attach to a session other than the configured one.
	Socket string
	Pane   string
	Mode   BridgeMode
	// Cols and Rows are the attach size (a controller resizes the pane to
	// it; an observer renders at it).
	Cols, Rows int
	Takeover   bool
	// OnFrame gets every frame, in order, from the goroutine that drains
	// the child's stdout. It must not block: herdr drops a stream reader
	// that makes no progress for 30 s.
	OnFrame func(TerminalFrame)
	// OnClose is called once, when the bridge has ended for good after
	// StartBridge returned (not for a refusal StartBridge reports).
	OnClose func(Closed)
	// Retry is the backoff between attach attempts refused with "read in
	// progress" (default 100, 200, 400 ms).
	Retry []time.Duration
	// StartTimeout bounds the wait for the first frame (default 10 s).
	StartTimeout time.Duration
	// Env is the base environment (default os.Environ()).
	Env []string
}

// AttachError is a bridge that ended before its first frame (StartBridge's
// error): busy, gone, or a herdr failure.
type AttachError struct{ Closed }

func (e *AttachError) Error() string {
	return fmt.Sprintf("herdr: terminal attach %s: %s", e.Reason, e.Message)
}

// ErrObserveOnly is a command sent to an observe bridge.
var ErrObserveOnly = errors.New("herdr: observe bridge takes no commands")

// ErrBridgeClosed is a command sent after the bridge ended.
var ErrBridgeClosed = errors.New("herdr: bridge closed")

// Bridge is one running terminal session child. Safe for concurrent use.
type Bridge struct {
	opts BridgeOptions

	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Reader

	wmu      sync.Mutex
	released bool

	smu        sync.Mutex // guards cols, rows: the pane size the wheel centres on
	cols, rows int

	done   chan struct{}
	once   sync.Once
	closed Closed
	stderr *tailBuffer
}

// StartBridge runs the bridge and waits for herdr's first frame, retrying
// the transient "read in progress" refusal. A refusal comes back as
// *AttachError. On success the first frame has already gone to OnFrame,
// and a goroutine drains stdout until the bridge ends.
func StartBridge(ctx context.Context, opts BridgeOptions) (*Bridge, error) {
	if opts.Mode == "" {
		opts.Mode = BridgeControl
	}
	if opts.Mode != BridgeControl && opts.Mode != BridgeObserve {
		return nil, fmt.Errorf("herdr: bridge mode %q", opts.Mode)
	}
	if opts.Bin == "" || opts.Socket == "" || opts.Pane == "" {
		return nil, errors.New("herdr: bridge needs Bin, Socket and Pane")
	}
	if opts.Cols <= 0 || opts.Rows <= 0 || opts.Cols > 1000 || opts.Rows > 1000 {
		return nil, fmt.Errorf("herdr: bridge size %dx%d", opts.Cols, opts.Rows)
	}
	if opts.Retry == nil {
		opts.Retry = []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 10 * time.Second
	}
	for attempt := 0; ; attempt++ {
		b, first, err := startOnce(ctx, opts)
		if err == nil {
			if opts.OnFrame != nil {
				opts.OnFrame(first)
			}
			go b.drain()
			return b, nil
		}
		var ae *AttachError
		retry := errors.As(err, &ae) && ae.retry()
		if !retry || attempt >= len(opts.Retry) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(opts.Retry[attempt]):
		}
	}
}

func (e *AttachError) retry() bool {
	_, r := classifyClose(e.Message)
	return r
}

// bridgeEnv is base without any HERDR_* variable, plus HERDR_SOCKET_PATH.
func bridgeEnv(base []string, socket string) []string {
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, "HERDR_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HERDR_SOCKET_PATH="+socket)
}

func startOnce(ctx context.Context, opts BridgeOptions) (*Bridge, TerminalFrame, error) {
	args := []string{"terminal", "session", string(opts.Mode), opts.Pane,
		"--cols", strconv.Itoa(opts.Cols), "--rows", strconv.Itoa(opts.Rows)}
	if opts.Mode == BridgeControl && opts.Takeover {
		args = append(args, "--takeover")
	}
	cmd := exec.Command(opts.Bin, args...)
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = bridgeEnv(env, opts.Socket)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, TerminalFrame{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, TerminalFrame{}, err
	}
	b := &Bridge{opts: opts, cmd: cmd, stdin: stdin, out: bufio.NewReaderSize(stdout, 64<<10),
		done: make(chan struct{}), stderr: &tailBuffer{max: 2048},
		cols: opts.Cols, rows: opts.Rows}
	cmd.Stderr = b.stderr
	if err := cmd.Start(); err != nil {
		return nil, TerminalFrame{}, &AttachError{Closed{CloseHerdr, err.Error()}}
	}
	type first struct {
		f   TerminalFrame
		err error
	}
	ch := make(chan first, 1)
	go func() {
		f, closed, err := b.next()
		switch {
		case err != nil:
			ch <- first{err: &AttachError{Closed{CloseHerdr, b.exitMessage(err)}}}
		case closed != nil:
			ch <- first{err: &AttachError{*closed}}
		default:
			ch <- first{f: f}
		}
	}()
	timer := time.NewTimer(opts.StartTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		if r.err != nil {
			b.kill()
			return nil, TerminalFrame{}, r.err
		}
		return b, r.f, nil
	case <-ctx.Done():
		b.kill()
		return nil, TerminalFrame{}, ctx.Err()
	case <-timer.C:
		b.kill()
		return nil, TerminalFrame{}, &AttachError{Closed{CloseHerdr, "no first frame within " + opts.StartTimeout.String()}}
	}
}

// bridgeLine is one stdout line of the bridge.
type bridgeLine struct {
	Type   string  `json:"type"`
	Seq    uint32  `json:"seq"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Full   bool    `json:"full"`
	Bytes  string  `json:"bytes"`
	Reason *string `json:"reason"`
}

// next reads stdout until a frame or a terminal.closed (returned as
// closed). Unknown line types are skipped. err is the read error at the
// end of the stream.
func (b *Bridge) next() (TerminalFrame, *Closed, error) {
	for {
		line, err := readLine(b.out)
		if err != nil {
			return TerminalFrame{}, nil, err
		}
		var l bridgeLine
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		switch l.Type {
		case "terminal.frame":
			raw, err := base64.StdEncoding.DecodeString(l.Bytes)
			if err != nil {
				continue
			}
			return TerminalFrame{Seq: l.Seq, Width: l.Width, Height: l.Height, Full: l.Full, Bytes: raw}, nil, nil
		case "terminal.closed":
			msg := ""
			if l.Reason != nil {
				msg = *l.Reason
			}
			r, _ := classifyClose(msg)
			return TerminalFrame{}, &Closed{Reason: r, Message: msg}, nil
		}
	}
}

// drain forwards frames until the stream ends, then reaps the child and
// reports how it ended.
func (b *Bridge) drain() {
	var end Closed
	for {
		f, closed, err := b.next()
		if err != nil {
			end = Closed{CloseHerdr, b.exitMessage(err)}
			break
		}
		if closed != nil {
			end = *closed
			b.wmu.Lock()
			_ = b.stdin.Close()
			b.wmu.Unlock()
			// Keep draining to EOF so the child never blocks on a full pipe.
			_, _ = io.Copy(io.Discard, b.out)
			break
		}
		if b.opts.OnFrame != nil {
			b.opts.OnFrame(f)
		}
	}
	b.wmu.Lock()
	released := b.released
	b.wmu.Unlock()
	if released && end.Reason == CloseHerdr {
		end = Closed{CloseReleased, "released"}
	}
	b.finish(end)
}

// finish reaps the child and records how the bridge ended, once.
func (b *Bridge) finish(end Closed) {
	b.once.Do(func() {
		_ = b.stdin.Close()
		waitc := make(chan struct{})
		go func() { _ = b.cmd.Wait(); close(waitc) }()
		select {
		case <-waitc:
		case <-time.After(3 * time.Second):
			_ = b.cmd.Process.Kill()
			<-waitc
		}
		b.closed = end
		close(b.done)
		if b.opts.OnClose != nil {
			b.opts.OnClose(end)
		}
	})
}

func (b *Bridge) exitMessage(err error) string {
	if tail := strings.TrimSpace(b.stderr.String()); tail != "" {
		return tail
	}
	if errors.Is(err, io.EOF) {
		return "bridge exited"
	}
	return err.Error()
}

// kill ends a bridge that never started (no OnClose).
func (b *Bridge) kill() {
	_ = b.cmd.Process.Kill()
	_ = b.stdin.Close()
	_ = b.cmd.Wait()
}

// Done is closed when the bridge has ended and its process is reaped.
func (b *Bridge) Done() <-chan struct{} { return b.done }

// Closed is how the bridge ended (valid after Done).
func (b *Bridge) Closed() Closed {
	<-b.done
	return b.closed
}

// Mode is the bridge's attach mode.
func (b *Bridge) Mode() BridgeMode { return b.opts.Mode }

func (b *Bridge) send(v any) error { return b.sendAll(v) }

// sendAll writes the commands as one batch of lines, in order.
func (b *Bridge) sendAll(vs ...any) error {
	if b.opts.Mode != BridgeControl {
		return ErrObserveOnly
	}
	var batch []byte
	for _, v := range vs {
		line, err := json.Marshal(v)
		if err != nil {
			return err
		}
		batch = append(append(batch, line...), '\n')
	}
	b.wmu.Lock()
	defer b.wmu.Unlock()
	select {
	case <-b.done:
		return ErrBridgeClosed
	default:
	}
	if b.released {
		return ErrBridgeClosed
	}
	if _, err := b.stdin.Write(batch); err != nil {
		return ErrBridgeClosed
	}
	return nil
}

// Input writes raw terminal input bytes (terminal.input). The daemon
// never interprets them.
func (b *Bridge) Input(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	return b.send(map[string]any{"type": "terminal.input", "bytes": base64.StdEncoding.EncodeToString(p)})
}

// Resize resizes the pane (terminal.resize). A resize to the current size
// makes herdr send a full frame: the resync primitive.
func (b *Bridge) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 1000 {
		return fmt.Errorf("herdr: resize %dx%d", cols, rows)
	}
	b.smu.Lock()
	b.cols, b.rows = cols, rows
	b.smu.Unlock()
	return b.send(map[string]any{"type": "terminal.resize", "cols": cols, "rows": rows})
}

// MaxWheelNotches is the most wheel notches one Scroll sends. A notch is
// cheap (a thousand cost herdr about 10 ms in the shell), so the cap only
// bounds a runaway client; a drag frame is a few notches, and "↓ Latest"
// from a deep scrollback is the largest honest request.
const MaxWheelNotches = 1000

// Scroll turns `lines` of scrolling into that many wheel notches, one per
// line (docs/dev/term-contract.md#wheel-notches). herdr's terminal.scroll
// moves its own scrollback by `lines`, but sends a full-screen app that
// asked for the mouse (Codex) exactly ONE wheel event whatever `lines`
// says. One line per message is the same distance in the scrollback and
// the right number of notches for the app, so the daemon never has to
// know which of the two the pane is in.
//
// up is the direction. The wheel lands on col,row, or on the middle of the
// pane when either is nil: a TUI ignores a wheel outside its scrollable
// area, and herdr's own default cell is the top-left corner.
func (b *Bridge) Scroll(up bool, lines int, col, row *int) error {
	if lines <= 0 {
		return errors.New("herdr: scroll lines must be positive")
	}
	if col == nil || row == nil {
		c, r := b.middleCell()
		col, row = &c, &r
	}
	dir := "down"
	if up {
		dir = "up"
	}
	notch := map[string]any{"type": "terminal.scroll", "direction": dir, "lines": 1, "column": *col, "row": *row}
	notches := make([]any, min(lines, MaxWheelNotches))
	for i := range notches {
		notches[i] = notch
	}
	return b.sendAll(notches...)
}

// middleCell is the centre of the pane at its current size.
func (b *Bridge) middleCell() (col, row int) {
	b.smu.Lock()
	defer b.smu.Unlock()
	return b.cols / 2, b.rows / 2
}

// Mouse sends a click or drag (terminal.mouse): action is down, up or
// drag; button left (default), right or middle. herdr drops it when the
// app did not ask for the mouse.
func (b *Bridge) Mouse(action, button string, col, row int) error {
	switch action {
	case "down", "up", "drag":
	default:
		return fmt.Errorf("herdr: mouse action %q", action)
	}
	switch button {
	case "", "left", "right", "middle":
	default:
		return fmt.Errorf("herdr: mouse button %q", button)
	}
	if col < 0 || row < 0 {
		return errors.New("herdr: mouse position")
	}
	m := map[string]any{"type": "terminal.mouse", "action": action, "column": col, "row": row}
	if button != "" {
		m["button"] = button
	}
	return b.send(m)
}

// Release gives control back (terminal.release) and waits for the child
// to exit (killing it after 3 s). An observe bridge is simply stopped.
// The pane keeps running.
func (b *Bridge) Release() {
	if b.opts.Mode == BridgeControl {
		_ = b.send(map[string]any{"type": "terminal.release"})
	}
	b.wmu.Lock()
	b.released = true
	_ = b.stdin.Close()
	b.wmu.Unlock()
	if b.opts.Mode == BridgeObserve {
		_ = b.cmd.Process.Kill()
	}
	select {
	case <-b.done:
	case <-time.After(3 * time.Second):
		_ = b.cmd.Process.Kill()
		<-b.done
	}
}

// tailBuffer keeps the last max bytes written (the child's stderr).
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
