package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// wsClient is a test browser.
type wsClient struct {
	t    *testing.T
	conn *websocket.Conn
	ctx  context.Context
}

func dialWS(t *testing.T, h *harness) *wsClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	url := "ws" + strings.TrimPrefix(h.http.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	conn.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return &wsClient{t: t, conn: conn, ctx: ctx}
}

func (c *wsClient) send(m wsproto.Message) {
	c.t.Helper()
	b, err := m.Encode()
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.conn.Write(c.ctx, websocket.MessageText, b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *wsClient) sendRaw(s string) {
	c.t.Helper()
	if err := c.conn.Write(c.ctx, websocket.MessageText, []byte(s)); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *wsClient) sendAudio(b []byte) {
	c.t.Helper()
	if err := c.conn.Write(c.ctx, websocket.MessageBinary, b); err != nil {
		c.t.Fatalf("write audio: %v", err)
	}
}

// read returns the next frame; binary frames come back with a nil message.
func (c *wsClient) read(d time.Duration) (wsproto.Message, []byte, error) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.ctx, d)
	defer cancel()
	typ, data, err := c.conn.Read(ctx)
	if err != nil {
		return wsproto.Message{}, nil, err
	}
	if typ == websocket.MessageBinary {
		return wsproto.Message{}, data, nil
	}
	m, err := wsproto.Parse(data)
	if err != nil {
		c.t.Fatalf("bad control frame %s: %v", data, err)
	}
	return m, nil, nil
}

// expect reads until a control message of the given type arrives.
func (c *wsClient) expect(typ string) wsproto.Message {
	c.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m, _, err := c.read(time.Until(deadline))
		if err != nil {
			c.t.Fatalf("waiting for %s: %v", typ, err)
		}
		if m.Type == typ {
			return m
		}
	}
	c.t.Fatalf("no %s message", typ)
	return wsproto.Message{}
}

func TestWSAudioAndControlWithVoice(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)

	// First message is the status.
	st := c.expect(wsproto.TypeStatus)
	if st.Voice != wsproto.VoiceConnected || st.Muted == nil || *st.Muted {
		t.Fatalf("initial status = %+v", st)
	}

	// Binary in → session.
	c.sendAudio([]byte{1, 2, 3, 4})
	waitFor(t, 2*time.Second, func() bool { return h.session.sentFrames() == 1 }, "audio reached session")

	// Audio out → binary frame to the client.
	h.session.audio <- []byte{9, 9, 9, 9, 9, 9}
	m, data, err := c.read(2 * time.Second)
	if err != nil || m.Type != "" || len(data) != 6 {
		t.Fatalf("audio out = %+v %v %v", m, data, err)
	}

	// ping → pong.
	c.send(wsproto.Ping())
	c.expect(wsproto.TypePong)

	// mute → session muted, status echoes it, audio dropped by the daemon.
	c.send(wsproto.Mute(true))
	st = c.expect(wsproto.TypeStatus)
	if st.Muted == nil || !*st.Muted {
		t.Fatalf("mute status = %+v", st)
	}
	waitFor(t, time.Second, h.session.isMuted, "session muted")
	c.sendAudio([]byte{5, 6})
	c.send(wsproto.Ping())
	c.expect(wsproto.TypePong)
	if h.session.sentFrames() != 1 {
		t.Fatalf("muted audio forwarded: %d frames", h.session.sentFrames())
	}
	c.send(wsproto.Mute(false))
	c.expect(wsproto.TypeStatus)

	// Interrupted → flush; transcript and error events are mirrored.
	h.session.emit(voice.EventInterrupted{})
	c.expect(wsproto.TypeFlush)
	h.session.emit(voice.EventTranscript{Dir: voice.DirIn, Text: "hello", Final: true})
	tr := c.expect(wsproto.TypeTranscript)
	if tr.Dir != "in" || tr.Text != "hello" || tr.Final == nil || !*tr.Final {
		t.Fatalf("transcript = %+v", tr)
	}
	h.session.emit(voice.EventError{Err: errors.New("rate limited")})
	if e := c.expect(wsproto.TypeError); e.Message != "rate limited" {
		t.Fatalf("error = %+v", e)
	}
	h.session.emit(voice.EventStatus{Status: voice.StatusReconnecting, Reason: "goaway"})
	if st = c.expect(wsproto.TypeStatus); st.Voice != wsproto.VoiceReconnecting || st.Reason != "goaway" {
		t.Fatalf("reconnecting status = %+v", st)
	}

	// text → session.SendText.
	c.send(wsproto.SimulateText("what's going on"))
	waitFor(t, time.Second, func() bool { return len(h.session.sentTexts()) == 1 }, "text reached session")

	// cancel → cancel tool, flush, interrupting push of the result.
	c.send(wsproto.Cancel())
	c.expect(wsproto.TypeFlush)
	waitFor(t, 2*time.Second, func() bool { return len(h.session.pushed()) == 1 }, "cancel result pushed")
	if p := h.session.pushed(); p[0] != "Nothing to cancel." {
		t.Fatalf("pushed = %v", p)
	}
	if !subsequence(h.types(), []string{"web.client.connect", "voice.tool_call", "voice.tool_result", "web.client.flush"}) {
		t.Fatalf("log = %v", h.types())
	}

	// Bad control messages get an error and the connection stays open.
	c.sendRaw(`{"type":"nope"}`)
	if e := c.expect(wsproto.TypeError); !strings.Contains(e.Message, "unknown type") {
		t.Fatalf("error = %+v", e)
	}
	c.sendRaw(`{"type":"flush"}`)
	if e := c.expect(wsproto.TypeError); !strings.Contains(e.Message, "unexpected") {
		t.Fatalf("error = %+v", e)
	}
	c.send(wsproto.Ping())
	c.expect(wsproto.TypePong)
}

func TestWSSingleClientReplacement(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	first := dialWS(t, h)
	first.expect(wsproto.TypeStatus)

	second := dialWS(t, h)
	second.expect(wsproto.TypeStatus)

	// The first client is told it was replaced, then closed with 1000.
	st := first.expect(wsproto.TypeStatus)
	if st.Voice != wsproto.VoiceClosed || st.Reason != "replaced" {
		t.Fatalf("replaced status = %+v", st)
	}
	_, _, err := first.read(2 * time.Second)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusNormalClosure {
		t.Fatalf("first client close = %v", err)
	}

	// Audio now goes to the second client only.
	h.session.audio <- []byte{1, 1}
	if m, data, err := second.read(2 * time.Second); err != nil || m.Type != "" || len(data) != 2 {
		t.Fatalf("second client audio = %+v %v %v", m, data, err)
	}
	waitFor(t, 2*time.Second, func() bool {
		n := 0
		for _, ty := range h.types() {
			if ty == "web.client.disconnect" {
				n++
			}
		}
		return n == 1
	}, "disconnect logged")
	var st2 StateResponse
	h.getJSON("/api/state", &st2)
	if !st2.Voice.ClientConnected {
		t.Fatal("client_connected should be true")
	}
}

func TestWSBackpressureDropsOldestAudio(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true}) // OutQueue = 8
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	// Flood without reading: the queue keeps the newest frames and logs a
	// backpressure event. Feed enough to exceed the per-connection write
	// buffers too, so the writer actually stalls.
	chunk := make([]byte, 64<<10)
	for i := 0; i < 200; i++ {
		h.session.audio <- chunk
	}
	waitFor(t, 5*time.Second, func() bool {
		for _, ty := range h.types() {
			if ty == "web.client.backpressure" {
				return true
			}
		}
		return false
	}, "backpressure event")

	// Control messages still get through: drain the audio, then ping.
	c.send(wsproto.Ping())
	c.expect(wsproto.TypePong)
	waitFor(t, 2*time.Second, func() bool {
		for _, ev := range h.log.Tail(0) {
			if ev.Type == "web.client.backpressure" && strings.Contains(string(ev.Payload), `"queue":8`) {
				return true
			}
		}
		return false
	}, "backpressure payload names the queue size")
}

func TestWSWithoutVoice(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	c := dialWS(t, h)
	st := c.expect(wsproto.TypeStatus)
	if st.Voice != wsproto.VoiceClosed || st.Reason != "voice_off" {
		t.Fatalf("status = %+v", st)
	}
	// Audio is dropped silently; text goes through the router.
	c.sendAudio([]byte{1, 2})
	h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, nil)
	c.send(wsproto.SimulateText("yes"))
	in := c.expect(wsproto.TypeTranscript)
	out := c.expect(wsproto.TypeTranscript)
	if in.Dir != "in" || in.Text != "yes" || out.Dir != "out" || !strings.Contains(out.Text, "Told auth yes") {
		t.Fatalf("transcripts = %+v %+v", in, out)
	}
	c.send(wsproto.SimulateText("reticulate"))
	if e := c.expect(wsproto.TypeError); !strings.Contains(e.Message, "text router") {
		t.Fatalf("error = %+v", e)
	}
	c.send(wsproto.Cancel())
	c.expect(wsproto.TypeFlush)
	if tr := c.expect(wsproto.TypeTranscript); tr.Text != "Nothing to cancel." {
		t.Fatalf("cancel transcript = %+v", tr)
	}
	// A typed "stop" is echoed, flushed and answered exactly once.
	c.send(wsproto.SimulateText("stop"))
	if in := c.expect(wsproto.TypeTranscript); in.Dir != "in" || in.Text != "stop" {
		t.Fatalf("stop echo = %+v", in)
	}
	c.expect(wsproto.TypeFlush)
	if tr := c.expect(wsproto.TypeTranscript); tr.Dir != "out" || tr.Text != "Nothing to cancel." {
		t.Fatalf("stop transcript = %+v", tr)
	}
	c.send(wsproto.Ping())
	for {
		m, _, err := c.read(2 * time.Second)
		if err != nil {
			t.Fatalf("waiting for pong: %v", err)
		}
		if m.Type == wsproto.TypePong {
			break
		}
		if m.Type == wsproto.TypeTranscript {
			t.Fatalf("duplicate transcript after stop: %+v", m)
		}
	}
}
