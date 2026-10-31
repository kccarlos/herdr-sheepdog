package herdrcore_test

import (
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
)

// bridgeFixture is a bridge over a fake core with one live terminal t1.
type bridgeFixture struct {
	srv    *hctest.Server
	bridge *herdrcore.Bridge
	frames chan herdr.TerminalFrame
	closed chan herdr.Closed
}

func startBridge(t *testing.T, mode herdr.BridgeMode, paint string) *bridgeFixture {
	t.Helper()
	b, srv := newBackend(t)
	srv.Add(liveTerminal("t1", "claude"))
	srv.SetPaint("t1", paint)
	fx := &bridgeFixture{srv: srv, frames: make(chan herdr.TerminalFrame, 64), closed: make(chan herdr.Closed, 2)}
	br, err := b.StartBridge(testContext(t), herdr.BridgeOptions{
		Pane: "t1", Mode: mode, Cols: 58, Rows: 30,
		OnFrame: func(f herdr.TerminalFrame) { fx.frames <- f },
		OnClose: func(c herdr.Closed) { fx.closed <- c },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Release)
	fx.bridge = br
	return fx
}

// viewerData waits until the fake has seen n input frames and returns
// their payloads.
func viewerData(t *testing.T, srv *hctest.Server, n int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var got []string
		for _, f := range srv.StreamFrames() {
			if f.Kind == hctest.FrameData {
				got = append(got, string(f.Payload))
			}
		}
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d input frames, have %d", n, len(got))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTermReadyNeedsAttach(t *testing.T) {
	b, _ := newBackend(t)
	if err := b.TermReady(testContext(t)); err != nil {
		t.Fatalf("TermReady against a core with terminal.attach = %v", err)
	}
	down := herdrcore.New(hc.NewClient(t.TempDir()+"/none.sock"), nil)
	if err := down.TermReady(testContext(t)); err == nil {
		t.Fatal("TermReady without a core = nil")
	}
}

func TestBridgeFramesReachTheApp(t *testing.T) {
	fx := startBridge(t, herdr.BridgeControl, "\x1b[Hpaint")
	first := receive(t, fx.frames)
	if !first.Full || first.Seq != 1 || first.Width != 58 || first.Height != 30 || string(first.Bytes) != "\x1b[Hpaint" {
		t.Fatalf("first frame = %+v", first)
	}
	fx.srv.Emit("t1", []byte("more"))
	next := receive(t, fx.frames)
	if next.Full || next.Seq != 2 || string(next.Bytes) != "more" {
		t.Fatalf("next frame = %+v, want a diff with seq 2", next)
	}
	if got := requestParams[map[string]any](t, fx.srv, "terminal.attach")[0]; got["observe"] != nil || got["terminal"] != "t1" {
		t.Errorf("control attach params = %v", got)
	}
}

func TestBridgeInputAndResizeGoBack(t *testing.T) {
	fx := startBridge(t, herdr.BridgeControl, "p")
	receive(t, fx.frames)
	if err := fx.bridge.Input([]byte("hi\r")); err != nil {
		t.Fatal(err)
	}
	if err := fx.bridge.Resize(80, 24); err != nil {
		t.Fatal(err)
	}
	if got := viewerData(t, fx.srv, 1); got[0] != "hi\r" {
		t.Errorf("input = %q", got)
	}
	var rz []byte
	waitUntil(t, func() bool {
		for _, f := range fx.srv.StreamFrames() {
			if f.Kind == hctest.FrameResize {
				rz = f.Payload
			}
		}
		return rz != nil
	}, "the resize frame")
	if len(rz) != 4 || binary.LittleEndian.Uint16(rz) != 80 || binary.LittleEndian.Uint16(rz[2:]) != 24 {
		t.Errorf("resize payload = %v", rz)
	}
	// The next frame carries the new size.
	fx.srv.Emit("t1", []byte("x"))
	if f := receive(t, fx.frames); f.Width != 80 || f.Height != 24 {
		t.Errorf("frame size %dx%d after resize, want 80x24", f.Width, f.Height)
	}
}

func TestObserveBridgeTakesNoCommands(t *testing.T) {
	fx := startBridge(t, herdr.BridgeObserve, "p")
	receive(t, fx.frames)
	if got := requestParams[map[string]any](t, fx.srv, "terminal.attach")[0]; got["observe"] != true {
		t.Errorf("observe attach params = %v", got)
	}
	for name, err := range map[string]error{
		"input":  fx.bridge.Input([]byte("x")),
		"resize": fx.bridge.Resize(80, 24),
		"scroll": fx.bridge.Scroll(true, 1, nil, nil),
		"mouse":  fx.bridge.Mouse("down", "", 1, 1),
	} {
		if !errors.Is(err, herdr.ErrObserveOnly) {
			t.Errorf("%s on an observer = %v, want ErrObserveOnly", name, err)
		}
	}
}

func TestBridgeResyncAttachesAgain(t *testing.T) {
	fx := startBridge(t, herdr.BridgeControl, "paint")
	receive(t, fx.frames)
	fx.srv.Emit("t1", []byte("diff"))
	receive(t, fx.frames)

	// A resize to the current size is the resync primitive.
	if err := fx.bridge.Resize(58, 30); err != nil {
		t.Fatal(err)
	}
	f := receive(t, fx.frames)
	if !f.Full || f.Seq != 3 || !strings.HasSuffix(string(f.Bytes), "paint") || !strings.HasPrefix(string(f.Bytes), "\x1b[0m\x1b[H\x1b[2J") {
		t.Fatalf("resync frame = %+v, want a full repaint with the clear first", f)
	}
	waitUntil(t, func() bool { return fx.srv.Streams("t1") == 1 }, "the old stream detached")
	if n := len(requestParams[map[string]any](t, fx.srv, "terminal.attach")); n != 2 {
		t.Errorf("%d attaches, want 2", n)
	}
	fx.srv.Emit("t1", []byte("after"))
	if f := receive(t, fx.frames); f.Full || f.Seq != 4 || string(f.Bytes) != "after" {
		t.Errorf("frame after the resync = %+v", f)
	}
	select {
	case c := <-fx.closed:
		t.Fatalf("resync ended the bridge: %+v", c)
	default:
	}
}

func TestBridgeReleaseDetaches(t *testing.T) {
	fx := startBridge(t, herdr.BridgeControl, "p")
	receive(t, fx.frames)
	fx.bridge.Release()
	select {
	case <-fx.bridge.Done():
	default:
		t.Fatal("Release returned before the bridge was done")
	}
	if c := receive(t, fx.closed); c.Reason != herdr.CloseReleased {
		t.Errorf("closed as %+v, want released", c)
	}
	waitUntil(t, func() bool { return fx.srv.Streams("t1") == 0 }, "the stream detached")
	if err := fx.bridge.Input([]byte("x")); !errors.Is(err, herdr.ErrBridgeClosed) {
		t.Errorf("input after release = %v, want ErrBridgeClosed", err)
	}
}

func TestBridgeClosedReasons(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   herdr.CloseReason
	}{
		{"exited", herdr.CloseGone},
		{"killed", herdr.CloseGone},
		{"server_stop", herdr.CloseHerdr},
	} {
		fx := startBridge(t, herdr.BridgeControl, "p")
		receive(t, fx.frames)
		fx.srv.EndStreams("t1", tc.reason, nil)
		if c := receive(t, fx.closed); c.Reason != tc.want {
			t.Errorf("stream ended %q: closed as %+v, want %s", tc.reason, c, tc.want)
		}
		if c := fx.bridge.Closed(); c.Reason != tc.want {
			t.Errorf("Closed() = %+v", c)
		}
	}
	// The connection to the core dropping is a herdr failure.
	fx := startBridge(t, herdr.BridgeControl, "p")
	receive(t, fx.frames)
	fx.srv.Close()
	if c := receive(t, fx.closed); c.Reason != herdr.CloseHerdr {
		t.Errorf("lost core: closed as %+v, want herdr", c)
	}
}

func TestStartBridgeRefusals(t *testing.T) {
	b, srv := newBackend(t)
	finished := liveTerminal("t2", "sh")
	finished.State = "finished"
	srv.Add(finished)
	for pane, want := range map[string]herdr.CloseReason{"t9": herdr.CloseGone, "t2": herdr.CloseGone} {
		_, err := b.StartBridge(testContext(t), herdr.BridgeOptions{Pane: pane, Cols: 58, Rows: 30})
		var ae *herdr.AttachError
		if !errors.As(err, &ae) || ae.Reason != want {
			t.Errorf("attach to %s = %v, want a %s refusal", pane, err, want)
		}
	}
	if _, err := b.StartBridge(testContext(t), herdr.BridgeOptions{Pane: "t1", Cols: 0, Rows: 30}); err == nil {
		t.Error("a zero-width bridge started")
	}
	down := herdrcore.New(hc.NewClient(t.TempDir()+"/none.sock"), nil)
	_, err := down.StartBridge(testContext(t), herdr.BridgeOptions{Pane: "t1", Cols: 58, Rows: 30})
	var ae *herdr.AttachError
	if !errors.As(err, &ae) || ae.Reason != herdr.CloseHerdr {
		t.Errorf("attach without a core = %v, want a herdr failure", err)
	}
}

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// modesSeen emits the sequence and waits for its frame, which the bridge
// delivers after scanning it.
func modesSeen(t *testing.T, fx *bridgeFixture, seq ...string) {
	t.Helper()
	for _, s := range seq {
		fx.srv.Emit("t1", []byte(s))
		receive(t, fx.frames)
	}
}

func TestBridgeScrollIsWheelNotchesForAMouseProgram(t *testing.T) {
	fx := startBridge(t, herdr.BridgeControl, "p")
	receive(t, fx.frames)

	// No mouse asked for: hc has no scrollback to move.
	if err := fx.bridge.Scroll(true, 3, nil, nil); !errors.Is(err, herdrcore.ErrNoWheel) {
		t.Fatalf("scroll in a program without the mouse = %v, want ErrNoWheel", err)
	}

	// The sequences arrive split across frames, as a stream may split them.
	modesSeen(t, fx, "text\x1b[?10", "00;1006h")
	if err := fx.bridge.Scroll(true, 3, nil, nil); err != nil {
		t.Fatal(err)
	}
	notch := "\x1b[<64;30;16M" // the middle of 58x30, one-based
	if got := viewerData(t, fx.srv, 1)[0]; got != strings.Repeat(notch, 3) {
		t.Errorf("wheel up = %q, want 3 notches", got)
	}
	col, row := 9, 4
	if err := fx.bridge.Scroll(false, 1, &col, &row); err != nil {
		t.Fatal(err)
	}
	if got := viewerData(t, fx.srv, 2)[1]; got != "\x1b[<65;10;5M" {
		t.Errorf("wheel down at 9,4 = %q", got)
	}

	// The mouse switched off again: no wheel.
	modesSeen(t, fx, "\x1b[?1000l")
	if err := fx.bridge.Scroll(true, 1, nil, nil); !errors.Is(err, herdrcore.ErrNoWheel) {
		t.Errorf("scroll after the mouse mode ended = %v, want ErrNoWheel", err)
	}
}

func TestBridgeScrollIsCappedAtTheWheelLimit(t *testing.T) {
	fx := startBridge(t, herdr.BridgeControl, "p")
	receive(t, fx.frames)
	modesSeen(t, fx, "\x1b[?1000h\x1b[?1006h")
	if err := fx.bridge.Scroll(true, herdr.MaxWheelNotches+500, nil, nil); err != nil {
		t.Fatal(err)
	}
	got := viewerData(t, fx.srv, 1)[0]
	if n := strings.Count(got, "\x1b[<64;"); n != herdr.MaxWheelNotches {
		t.Errorf("%d notches, want the cap of %d", n, herdr.MaxWheelNotches)
	}
}

func TestBridgeMouseFollowsTheModeTheProgramAsked(t *testing.T) {
	fx := startBridge(t, herdr.BridgeControl, "p")
	receive(t, fx.frames)

	// No mouse asked for: a click is dropped without an error.
	if err := fx.bridge.Mouse("down", "", 3, 2); err != nil {
		t.Fatal(err)
	}
	// The input after it marks where the program's input ends: nothing came
	// before it.
	if err := fx.bridge.Input([]byte("k")); err != nil {
		t.Fatal(err)
	}
	if got := viewerData(t, fx.srv, 1); len(got) != 1 || got[0] != "k" {
		t.Fatalf("a click without mouse mode reached the program: %q", got)
	}

	// Presses and releases only (1000, the legacy encoding: one byte per
	// number, the cell one-based): a drag is dropped.
	modesSeen(t, fx, "\x1b[?1000h")
	for _, c := range []struct{ action, button string }{{"down", ""}, {"up", "right"}, {"drag", ""}} {
		if err := fx.bridge.Mouse(c.action, c.button, 3, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := fx.bridge.Input([]byte("z")); err != nil {
		t.Fatal(err)
	}
	got := viewerData(t, fx.srv, 4)
	// A release has no button to say: button 3.
	if want := []string{"k", "\x1b[M $#", "\x1b[M#$#", "z"}; !slices.Equal(got, want) {
		t.Errorf("legacy mouse input = %q, want %q", got, want)
	}

	// 1002 with SGR: a drag is motion on the held button; a release keeps
	// its button.
	modesSeen(t, fx, "\x1b[?1002h\x1b[?1006h")
	if err := fx.bridge.Mouse("drag", "middle", 3, 2); err != nil {
		t.Fatal(err)
	}
	if err := fx.bridge.Mouse("up", "right", 3, 2); err != nil {
		t.Fatal(err)
	}
	got = viewerData(t, fx.srv, 6)
	if got[4] != "\x1b[<33;4;3M" || got[5] != "\x1b[<2;4;3m" {
		t.Errorf("sgr drag and release = %q, %q", got[4], got[5])
	}

	if err := fx.bridge.Mouse("hover", "", 1, 1); err == nil {
		t.Error("an unknown mouse action was accepted")
	}
	if err := fx.bridge.Mouse("down", "side", 1, 1); err == nil {
		t.Error("an unknown mouse button was accepted")
	}
}
