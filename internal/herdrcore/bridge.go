package herdrcore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// The live terminal on herdr-core (docs/dev/design/herdr-core-backend.md,
// docs/dev/term-contract.md): one framed hc connection and one
// terminal.attach stream per open phone window. hc sends the program's raw
// output where the old herdr's bridge sends rendered diffs, but the app
// feeds every frame to a full terminal emulator, so the bytes go through
// unchanged: each DATA frame is one terminal frame, the first paint is
// `full`, and the daemon needs no emulator of its own.

// attachMethod is the hc method the live terminal needs.
const attachMethod = "terminal.attach"

// attachTimeout bounds one attach, including the re-attach of a resync.
const attachTimeout = 5 * time.Second

// repaintPrefix goes before a resync's first paint, which lands on a
// screen that already shows the old one: reset the colours and clear it.
const repaintPrefix = "\x1b[0m\x1b[H\x1b[2J"

// TermReady returns nil when the core can run a live terminal: it
// answers hello and lists terminal.attach.
func (b *Backend) TermReady(ctx context.Context) error {
	hello, err := b.client.Hello(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(hello.Methods, attachMethod) {
		return fmt.Errorf("herdr-core %s has no %s", hello.Version, attachMethod)
	}
	return nil
}

// Bridge is one open phone window: a framed connection with one attach
// stream. It is the hc counterpart of herdr.Bridge, and safe for
// concurrent use.
type Bridge struct {
	opts   herdr.BridgeOptions
	framed *hc.Framed

	ready  chan struct{} // closed when StartBridge has returned
	ok     bool          // StartBridge succeeded; set before ready closes
	done   chan struct{}
	once   sync.Once
	closed herdr.Closed

	// reattach serialises resyncs; mu guards the rest.
	reattach   sync.Mutex
	mu         sync.Mutex
	stream     *hc.Stream
	gen        int // the current attach: an older stream's callbacks are ignored
	cols, rows int
	seq        uint32
	released   bool
	modes      termModes
}

// StartBridge attaches to opts.Pane and returns once hc accepted it; the
// first paint arrives through opts.OnFrame. Mode, Cols, Rows, OnFrame and
// OnClose mean what they do for herdr.StartBridge; Bin, Socket, Takeover,
// Retry and StartTimeout do not apply (hc has no single controller to
// refuse or take over from). A refusal comes back as *herdr.AttachError.
func (b *Backend) StartBridge(ctx context.Context, opts herdr.BridgeOptions) (*Bridge, error) {
	if opts.Mode == "" {
		opts.Mode = herdr.BridgeControl
	}
	if opts.Mode != herdr.BridgeControl && opts.Mode != herdr.BridgeObserve {
		return nil, fmt.Errorf("herdrcore: bridge mode %q", opts.Mode)
	}
	if opts.Pane == "" {
		return nil, errors.New("herdrcore: bridge needs a Pane")
	}
	if opts.Cols <= 0 || opts.Rows <= 0 || opts.Cols > 1000 || opts.Rows > 1000 {
		return nil, fmt.Errorf("herdrcore: bridge size %dx%d", opts.Cols, opts.Rows)
	}
	framed, err := b.client.Framed(ctx)
	if err != nil {
		return nil, &herdr.AttachError{Closed: herdr.Closed{Reason: herdr.CloseHerdr, Message: err.Error()}}
	}
	br := &Bridge{
		opts: opts, framed: framed, ready: make(chan struct{}), done: make(chan struct{}),
		cols: opts.Cols, rows: opts.Rows,
	}
	if err := br.attach(ctx, false); err != nil {
		framed.Close()
		close(br.ready)
		reason := herdr.CloseHerdr
		if errors.Is(err, hc.ErrNotFound) || errors.Is(err, hc.ErrFinished) {
			reason = herdr.CloseGone
		}
		return nil, &herdr.AttachError{Closed: herdr.Closed{Reason: reason, Message: err.Error()}}
	}
	br.ok = true
	close(br.ready)
	return br, nil
}

// attach opens the stream at the current size. repaint marks a resync: its
// first paint clears the screen first.
func (br *Bridge) attach(ctx context.Context, repaint bool) error {
	br.mu.Lock()
	br.gen++
	gen := br.gen
	cols, rows := br.cols, br.rows
	br.mu.Unlock()
	first := true
	s, _, err := br.framed.Attach(ctx, hc.AttachParams{
		Terminal: br.opts.Pane, Cols: uint16(cols), Rows: uint16(rows), Observe: br.opts.Mode == herdr.BridgeObserve,
	}, hc.StreamHandlers{
		OnData: func(p []byte) {
			br.onData(gen, first, repaint, p)
			first = false
		},
		OnEnd: func(end hc.StreamEnd) { br.onEnd(gen, end) },
	})
	if err != nil {
		return err
	}
	br.mu.Lock()
	br.stream = s
	br.mu.Unlock()
	return nil
}

// onData turns one DATA frame into one terminal frame. It runs on the hc
// connection's reader goroutine and never blocks.
func (br *Bridge) onData(gen int, first, repaint bool, p []byte) {
	br.mu.Lock()
	if gen != br.gen || br.released {
		br.mu.Unlock()
		return
	}
	br.seq++
	f := herdr.TerminalFrame{Seq: br.seq, Width: br.cols, Height: br.rows, Full: first}
	br.modes.feed(p)
	br.mu.Unlock()
	if first && repaint {
		f.Bytes = append([]byte(repaintPrefix), p...)
	} else {
		f.Bytes = append([]byte(nil), p...) // p is only valid during the call
	}
	if br.opts.OnFrame != nil {
		br.opts.OnFrame(f)
	}
}

// onEnd is the current stream ending: the program ended, hc stopped, the
// connection broke, or the viewer released it.
func (br *Bridge) onEnd(gen int, end hc.StreamEnd) {
	br.mu.Lock()
	if gen != br.gen {
		br.mu.Unlock()
		return
	}
	released := br.released
	br.mu.Unlock()
	br.finish(classifyEnd(end, released))
}

// classifyEnd maps how an attach stream ended onto the bridge's reasons:
// a program that exited or was killed is gone; anything else (hc stopping,
// a lost shell, a broken connection) is a herdr failure.
func classifyEnd(end hc.StreamEnd, released bool) herdr.Closed {
	switch {
	case released:
		return herdr.Closed{Reason: herdr.CloseReleased, Message: "released"}
	case end.Err != nil:
		return herdr.Closed{Reason: herdr.CloseHerdr, Message: end.Err.Error()}
	case end.Reason == "exited" || end.Reason == "killed":
		return herdr.Closed{Reason: herdr.CloseGone, Message: end.Reason}
	}
	return herdr.Closed{Reason: herdr.CloseHerdr, Message: "stream ended: " + end.Reason}
}

// finish records how the bridge ended and closes its connection, once.
func (br *Bridge) finish(end herdr.Closed) {
	br.once.Do(func() {
		br.closed = end
		close(br.done)
		// OnClose waits for StartBridge to have returned: the caller
		// learns of the bridge before it ends.
		go func() {
			<-br.ready
			if br.ok && br.opts.OnClose != nil {
				br.opts.OnClose(end)
			}
		}()
	})
	// Outside the once: closing ends the streams, which report back here.
	br.framed.Close()
}

// Done is closed when the bridge has ended.
func (br *Bridge) Done() <-chan struct{} { return br.done }

// Closed is how the bridge ended (valid after Done).
func (br *Bridge) Closed() herdr.Closed {
	<-br.done
	return br.closed
}

// live returns the stream a command goes to: only a control bridge takes
// commands, and none after it ended.
func (br *Bridge) live() (*hc.Stream, error) {
	if br.opts.Mode != herdr.BridgeControl {
		return nil, herdr.ErrObserveOnly
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	select {
	case <-br.done:
		return nil, herdr.ErrBridgeClosed
	default:
	}
	if br.released || br.stream == nil {
		return nil, herdr.ErrBridgeClosed
	}
	return br.stream, nil
}

// write sends bytes to the program, as input.
func (br *Bridge) write(p []byte) error {
	s, err := br.live()
	if err != nil {
		return err
	}
	if err := s.Write(p); err != nil {
		return herdr.ErrBridgeClosed
	}
	return nil
}

// Input writes raw terminal input bytes. The daemon never interprets them.
func (br *Bridge) Input(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	return br.write(p)
}

// Resize resizes the terminal. A resize to the current size repaints, the
// resync primitive: the bridge attaches again, and that first paint is a
// full frame.
func (br *Bridge) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 1000 {
		return fmt.Errorf("herdrcore: resize %dx%d", cols, rows)
	}
	s, err := br.live()
	if err != nil {
		return err
	}
	br.mu.Lock()
	same := br.cols == cols && br.rows == rows
	br.cols, br.rows = cols, rows
	br.mu.Unlock()
	if !same {
		if err := s.Resize(uint16(cols), uint16(rows)); err != nil {
			return herdr.ErrBridgeClosed
		}
		return nil
	}
	return br.resync()
}

// resync attaches again and lets go of the old stream, so hc paints the
// screen afresh (a viewer that falls behind gets that from hc itself, but
// one the daemon dropped frames for has to ask).
func (br *Bridge) resync() error {
	br.reattach.Lock()
	defer br.reattach.Unlock()
	br.mu.Lock()
	old := br.stream
	br.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), attachTimeout)
	defer cancel()
	if err := br.attach(ctx, true); err != nil {
		// The old stream's frames are ignored by now: nothing is left to show.
		br.finish(herdr.Closed{Reason: herdr.CloseHerdr, Message: err.Error()})
		return err
	}
	if old != nil {
		_ = old.Detach()
	}
	return nil
}

// Scroll turns `lines` of scrolling into that many wheel notches, as the
// mouse encoding the program asked for. hc has no scrollback to move for a
// program that does not use the mouse, so that is an error
// (docs/dev/term-contract.md#wheel-notches). up is the direction; the wheel
// lands on col,row, or on the middle of the terminal when either is nil.
func (br *Bridge) Scroll(up bool, lines int, col, row *int) error {
	if br.opts.Mode != herdr.BridgeControl {
		return herdr.ErrObserveOnly
	}
	if lines <= 0 {
		return errors.New("herdrcore: scroll lines must be positive")
	}
	if col == nil || row == nil {
		br.mu.Lock()
		c, r := br.cols/2, br.rows/2
		br.mu.Unlock()
		col, row = &c, &r
	}
	button := flagWheel
	if !up {
		button++
	}
	br.mu.Lock()
	notch := br.modes.event(button, *col, *row, false)
	br.mu.Unlock()
	if notch == nil {
		return ErrNoWheel
	}
	var batch []byte
	for range min(lines, herdr.MaxWheelNotches) {
		batch = append(batch, notch...)
	}
	return br.write(batch)
}

// ErrNoWheel is a scroll the program cannot hear: it asked for no mouse,
// and hc keeps no scrollback a viewer can move.
var ErrNoWheel = errors.New("herdrcore: the program takes no mouse wheel")

// Mouse sends a click or drag: action is down, up or drag; button left
// (default), right or middle. It is dropped when the program did not ask
// for the mouse (a drag: for motion).
func (br *Bridge) Mouse(action, button string, col, row int) error {
	if br.opts.Mode != herdr.BridgeControl {
		return herdr.ErrObserveOnly
	}
	var code int
	switch button {
	case "", "left":
		code = buttonLeft
	case "middle":
		code = buttonMiddle
	case "right":
		code = buttonRight
	default:
		return fmt.Errorf("herdrcore: mouse button %q", button)
	}
	if col < 0 || row < 0 {
		return errors.New("herdrcore: mouse position")
	}
	br.mu.Lock()
	var event []byte
	switch action {
	case "down":
		event = br.modes.event(code, col, row, false)
	case "up":
		event = br.modes.event(code, col, row, true)
	case "drag":
		if br.modes.canDrag() {
			event = br.modes.event(code+flagMotion, col, row, false)
		}
	default:
		br.mu.Unlock()
		return fmt.Errorf("herdrcore: mouse action %q", action)
	}
	br.mu.Unlock()
	if event == nil {
		return nil
	}
	return br.write(event)
}

// Release leaves the terminal and waits for the connection to end (closing
// it after 3 s). The program keeps running.
func (br *Bridge) Release() {
	br.mu.Lock()
	br.released = true
	s := br.stream
	br.mu.Unlock()
	if s != nil {
		_ = s.Detach()
	}
	select {
	case <-br.done:
	case <-time.After(3 * time.Second):
		br.finish(herdr.Closed{Reason: herdr.CloseReleased, Message: "released"})
	}
}
