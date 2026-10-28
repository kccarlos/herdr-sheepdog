package web

import (
	"context"
	"encoding/json"
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
	// skipBotReady: dialWS sent client-ready, so read drops the one
	// bot-ready that answers it.
	skipBotReady bool
}

// testClient is who dialWS says it is on client-ready.
var testClient = wsproto.ClientInfo{Name: "sheepdog-test", Version: "test", Platform: "go"}

// dialWS dials /ws and sends client-ready as the first frame, as every
// client does; read hides the bot-ready answer, so a test sees the frames
// that follow the handshake. Handshake tests use dialWSRaw.
func dialWS(t *testing.T, h *harness) *wsClient {
	t.Helper()
	c := dialWSRaw(t, h)
	c.send(wsproto.ClientReady(wsproto.ProtocolVersion, testClient))
	c.skipBotReady = true
	return c
}

// dialWSRaw dials /ws and sends nothing.
func dialWSRaw(t *testing.T, h *harness) *wsClient {
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
	if c.skipBotReady && m.Type == wsproto.TypeBotReady {
		c.skipBotReady = false
		return c.read(d)
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
	h.session.emit(voice.EventStatus{Status: voice.StatusConnected, Stalled: true})
	if st = c.expect(wsproto.TypeStatus); st.Voice != wsproto.VoiceConnected || !st.Stalled {
		t.Fatalf("stalled status = %+v", st)
	}
	var state StateResponse
	h.getJSON("/api/state", &state)
	if !state.Voice.Stalled {
		t.Fatalf("/api/state voice = %+v, want stalled", state.Voice)
	}
	h.session.emit(voice.EventStatus{Status: voice.StatusConnected})
	if st = c.expect(wsproto.TypeStatus); st.Stalled {
		t.Fatalf("status still stalled: %+v", st)
	}

	// text → session.SendText.
	c.send(wsproto.SimulateText("what's going on"))
	waitFor(t, time.Second, func() bool { return len(h.session.sentTexts()) == 1 }, "text reached session")

	// cancel → stop tool, flush, interrupting push of the result.
	c.send(wsproto.Cancel())
	c.expect(wsproto.TypeFlush)
	waitFor(t, 2*time.Second, func() bool { return len(h.session.pushed()) == 1 }, "stop result pushed")
	if p := h.session.pushed(); p[0] != "Nothing was in flight." {
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

// TestWSSilenceGate: a silence message is logged, closing the gate tells
// the session the audio stream paused, and the counters carried by the
// ping show up in web.client.stats.
func TestWSSilenceGate(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, statsInterval: 50 * time.Millisecond})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	c.send(wsproto.Silence(true))
	waitFor(t, time.Second, func() bool { return h.session.streamEnds() == 1 }, "audio stream end sent to session")
	c.send(wsproto.Silence(false))
	c.sendAudio([]byte{1, 2})
	waitFor(t, time.Second, func() bool { return h.session.sentFrames() == 1 }, "audio after the gate reopened")
	if h.session.streamEnds() != 1 {
		t.Fatalf("stream ends = %d, want 1 (only on gate close)", h.session.streamEnds())
	}

	gated, peak := 42, 5
	c.send(wsproto.Message{Type: wsproto.TypePing, GatedFrames: &gated, Peak: &peak})
	c.expect(wsproto.TypePong)
	c.sendRaw(`{"type":"silence"}`)
	if e := c.expect(wsproto.TypeError); !strings.Contains(e.Message, "silence") {
		t.Fatalf("error = %+v", e)
	}

	var silence []map[string]any
	var stats map[string]any
	waitFor(t, 2*time.Second, func() bool {
		for _, ev := range h.log.Tail(0) {
			if ev.Type == "web.client.stats" && strings.Contains(string(ev.Payload), `"gated_frames":42`) {
				return true
			}
		}
		return false
	}, "stats with the ping counters")
	for _, ev := range h.log.Tail(0) {
		var p map[string]any
		_ = json.Unmarshal(ev.Payload, &p)
		switch ev.Type {
		case "web.client.silence":
			silence = append(silence, p)
		case "web.client.stats":
			stats = p
		}
	}
	if len(silence) != 2 || silence[0]["on"] != true || silence[1]["on"] != false {
		t.Fatalf("web.client.silence = %v", silence)
	}
	if stats == nil {
		t.Fatal("no web.client.stats logged")
	}
	if stats["gated_frames"] != float64(42) || stats["peak"] != float64(5) || stats["silent"] != false || stats["silence_transitions"] != float64(2) {
		t.Fatalf("stats = %v", stats)
	}
}

// TestWSPlaybackStatsAndSendWait: the client's ping carries the jitter
// buffer's underruns/drains/buffer_ms into web.client.stats, and an audio
// chunk sent to the client shows up as a send_wait sample (WP: audible
// gaps investigation, docs/dev/ws-contract.md "ping").
func TestWSPlaybackStatsAndSendWait(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, statsInterval: 50 * time.Millisecond})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	h.session.audio <- []byte{1, 2, 3, 4}
	if _, _, err := c.read(2 * time.Second); err != nil {
		t.Fatalf("read audio: %v", err)
	}

	underruns, drains, bufferMs := 2, 5, 230
	rate, stretched := 0.92, 1830
	c.send(wsproto.Message{Type: wsproto.TypePing, Underruns: &underruns, Drains: &drains, BufferMs: &bufferMs, Rate: &rate, StretchedMs: &stretched, AudioState: "running"})
	c.expect(wsproto.TypePong)

	var stats map[string]any
	waitFor(t, 2*time.Second, func() bool {
		for _, ev := range h.log.Tail(0) {
			if ev.Type != "web.client.stats" {
				continue
			}
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			if p["underruns"] == float64(2) {
				stats = p
				return true
			}
		}
		return false
	}, "stats with the ping's playback counters")
	if stats["underruns"] != float64(2) || stats["drains"] != float64(5) || stats["buffer_ms"] != float64(230) || stats["rate"] != 0.92 || stats["stretched_ms"] != float64(1830) {
		t.Fatalf("stats = %v", stats)
	}
	if _, ok := stats["send_wait_max_ms"]; !ok {
		t.Errorf("send_wait_max_ms missing: %v", stats)
	}
	if _, ok := stats["playback_age_ms"]; !ok {
		t.Errorf("playback_age_ms missing: %v", stats)
	}
	if stats["audio_state"] != "running" {
		t.Errorf("audio_state = %v, want running", stats["audio_state"])
	}
	if _, ok := stats["send_wait_p95_ms"]; !ok {
		t.Errorf("send_wait_p95_ms missing: %v", stats)
	}
}

// TestWSPingAECInClientStats: the ping's aec (the browser's echo
// cancellation: true/false/unknown) shows up in web.client.stats; a value
// outside the three is ignored (WP-E, docs/dev/ws-contract.md "ping").
func TestWSPingAECInClientStats(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, statsInterval: 50 * time.Millisecond})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	aecOf := func(want string) {
		t.Helper()
		waitFor(t, 2*time.Second, func() bool {
			tail := h.log.Tail(0)
			for i := len(tail) - 1; i >= 0; i-- {
				if tail[i].Type != "web.client.stats" {
					continue
				}
				var p map[string]any
				_ = json.Unmarshal(tail[i].Payload, &p)
				return p["aec"] == want
			}
			return false
		}, "web.client.stats aec "+want)
	}
	c.send(wsproto.Message{Type: wsproto.TypePing, AEC: wsproto.AECOff})
	c.expect(wsproto.TypePong)
	aecOf("false")
	c.send(wsproto.Message{Type: wsproto.TypePing, AEC: "maybe"})
	c.expect(wsproto.TypePong)
	time.Sleep(120 * time.Millisecond)
	aecOf("false")
	c.send(wsproto.Message{Type: wsproto.TypePing, AEC: wsproto.AECUnknown})
	c.expect(wsproto.TypePong)
	aecOf("unknown")
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

// TestWSMuteDoesNotPersistAcrossClients: a client that muted and then
// disconnected must not leave the next client muted — reconnecting
// clients start unmuted (docs/dev/ws-contract.md, audit B2).
func TestWSMuteDoesNotPersistAcrossClients(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	first := dialWS(t, h)
	first.expect(wsproto.TypeStatus)

	first.send(wsproto.Mute(true))
	st := first.expect(wsproto.TypeStatus)
	if st.Muted == nil || !*st.Muted {
		t.Fatalf("mute status = %+v", st)
	}
	waitFor(t, time.Second, h.session.isMuted, "session muted")

	if err := first.conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatalf("close first: %v", err)
	}
	// Wait until the server has torn the first client down. A client that
	// dials while another is still attached is only "pending": with no
	// client-ready it attaches (and the session unmutes) after the
	// handshake timeout, which is a different path from the one under test.
	waitFor(t, 2*time.Second, func() bool {
		var st StateResponse
		h.getJSON("/api/state", &st)
		return !st.Voice.ClientConnected
	}, "first client detached")

	second := dialWS(t, h)
	st = second.expect(wsproto.TypeStatus)
	if st.Muted == nil || *st.Muted {
		t.Fatalf("second client's initial status = %+v, want muted:false", st)
	}
	waitFor(t, 2*time.Second, func() bool { return !h.session.isMuted() }, "session unmuted for the new client")

	// Frames flow for the new, unmuted client.
	second.sendAudio([]byte{1, 2, 3})
	waitFor(t, 2*time.Second, func() bool { return h.session.sentFrames() == 1 }, "audio reached session")
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
	// Anything the fast paths do not claim is relayed verbatim.
	c.send(wsproto.SimulateText("reticulate"))
	c.expect(wsproto.TypeTranscript)
	c.expect(wsproto.TypeTranscript)
	c.send(wsproto.Cancel())
	c.expect(wsproto.TypeFlush)
	if tr := c.expect(wsproto.TypeTranscript); tr.Text != "Nothing was in flight." {
		t.Fatalf("stop transcript = %+v", tr)
	}
	// A typed "stop" is echoed, flushed and answered exactly once.
	c.send(wsproto.SimulateText("stop"))
	if in := c.expect(wsproto.TypeTranscript); in.Dir != "in" || in.Text != "stop" {
		t.Fatalf("stop echo = %+v", in)
	}
	c.expect(wsproto.TypeFlush)
	if tr := c.expect(wsproto.TypeTranscript); tr.Dir != "out" || tr.Text != "Nothing was in flight." {
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

// TestWSStatsSayHowOldThePlaybackSampleIs replays 2026-09-24 18:21:30–40:
// the page pinged every 15 s while web.client.stats logged every 5 s,
// so three lines repeated one buffer_ms 3155 sample and read as a player
// frozen for 15 s (it had played on: the next ping showed it drained).
// Each line now carries playback_age_ms, the sample's age, so a repeated
// value is visibly old; before the first ping there is none.
func TestWSStatsSayHowOldThePlaybackSampleIs(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, statsInterval: 30 * time.Millisecond})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	statsLines := func() []map[string]any {
		var out []map[string]any
		for _, ev := range h.log.Tail(0) {
			if ev.Type != "web.client.stats" {
				continue
			}
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			out = append(out, p)
		}
		return out
	}
	waitFor(t, 2*time.Second, func() bool { return len(statsLines()) >= 1 }, "a stats line before any ping")
	if _, ok := statsLines()[0]["playback_age_ms"]; ok {
		t.Fatalf("playback_age_ms before any ping: %v", statsLines()[0])
	}

	bufferMs, drains := 3155, 1
	c.send(wsproto.Message{Type: wsproto.TypePing, BufferMs: &bufferMs, Drains: &drains})
	c.expect(wsproto.TypePong)
	n := len(statsLines())
	waitFor(t, 2*time.Second, func() bool { return len(statsLines()) >= n+4 }, "four more stats lines")
	var ages []float64
	for _, p := range statsLines()[n:] {
		if p["buffer_ms"] != float64(3155) {
			continue
		}
		age, ok := p["playback_age_ms"].(float64)
		if !ok {
			t.Fatalf("stats line without playback_age_ms: %v", p)
		}
		ages = append(ages, age)
	}
	if len(ages) < 3 {
		t.Fatalf("got %d lines with the ping's sample, want >= 3", len(ages))
	}
	for i := 1; i < len(ages); i++ {
		if ages[i] <= ages[i-1] {
			t.Fatalf("playback_age_ms does not grow while the sample repeats: %v", ages)
		}
	}
}

// TestWSModelAudioWithNoClientIsLoggedUnheard: audio the model produces
// while no page is attached is dropped (real time), but the log says so
// once per turn (web.audio_unheard) instead of losing it silently
// (2026-09-24 18:22:00: "Hello. How can I help you?" spoken to a closed
// page).
func TestWSModelAudioWithNoClientIsLoggedUnheard(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	unheard := func() int {
		n := 0
		for _, ty := range h.types() {
			if ty == "web.audio_unheard" {
				n++
			}
		}
		return n
	}
	h.session.audio <- []byte{1, 2}
	h.session.audio <- []byte{3, 4}
	waitFor(t, 2*time.Second, func() bool { return unheard() == 1 }, "web.audio_unheard for the first unheard chunk")
	h.session.emit(voice.EventTurnComplete{})
	time.Sleep(50 * time.Millisecond) // events and audio are pumped separately
	h.session.audio <- []byte{5, 6}
	waitFor(t, 2*time.Second, func() bool { return unheard() == 2 }, "a second web.audio_unheard for the next turn")
	time.Sleep(50 * time.Millisecond)
	if unheard() != 2 {
		t.Fatalf("web.audio_unheard logged %d times, want once per turn (2)", unheard())
	}

	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	h.session.audio <- []byte{7, 8}
	if m, data, err := c.read(2 * time.Second); err != nil || m.Type != "" || len(data) != 2 {
		t.Fatalf("attached client audio = %+v %v %v", m, data, err)
	}
	if unheard() != 2 {
		t.Fatalf("audio to an attached client logged as unheard")
	}
}

// TestWSTurnCompleteClosesTheOpenReply: output fragments carry no final, so
// the turn's end sends an empty final `out` fragment (once, and only when a
// reply is open) for the clients that have no other boundary.
func TestWSTurnCompleteClosesTheOpenReply(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	h.session.emit(voice.EventTurnComplete{}) // nothing open: nothing sent
	h.session.emit(voice.EventTranscript{Dir: voice.DirOut, Text: "All idle."})
	if m := c.expect(wsproto.TypeTranscript); m.Text != "All idle." || (m.Final != nil && *m.Final) {
		t.Fatalf("fragment = %+v", m)
	}
	h.session.emit(voice.EventTurnComplete{})
	m := c.expect(wsproto.TypeTranscript)
	if m.Dir != "out" || m.Text != "" || m.Final == nil || !*m.Final {
		t.Fatalf("turn end = %+v, want an empty final out fragment", m)
	}
}
