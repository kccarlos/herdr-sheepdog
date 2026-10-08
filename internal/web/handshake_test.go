package web

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

var iosClient = wsproto.ClientInfo{Name: "sheepdog-ios", Version: "0.2.0 (12)", Platform: "ios"}

// helloEvents returns the payloads of the web.client.hello events.
func helloEvents(h *harness) []map[string]any {
	var out []map[string]any
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "web.client.hello" {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(ev.Payload, &p)
		out = append(out, p)
	}
	return out
}

func waitHello(t *testing.T, h *harness, n int) []map[string]any {
	t.Helper()
	waitFor(t, 3*time.Second, func() bool { return len(helloEvents(h)) >= n }, "web.client.hello logged")
	return helloEvents(h)
}

// expectClose reads until the connection closes and returns the close
// error.
func (c *wsClient) expectClose() websocket.CloseError {
	c.t.Helper()
	for {
		_, _, err := c.read(3 * time.Second)
		if err == nil {
			continue
		}
		var ce websocket.CloseError
		if !errors.As(err, &ce) {
			c.t.Fatalf("expected a close, got %v", err)
		}
		return ce
	}
}

// A protocol-aware client: status first (as for every client), then
// bot-ready answering its client-ready, and the hello logged.
func TestHandshakeClientReady(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, brain: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	c.send(wsproto.ClientReady(1, iosClient))
	m := c.expect(wsproto.TypeBotReady)
	if *m.Protocol != wsproto.ProtocolVersion || *m.MinProtocol != wsproto.MinProtocolVersion || m.Daemon.Version != "test" {
		t.Fatalf("bot-ready = %+v", m)
	}
	for _, f := range []string{wsproto.FeaturePlayback, wsproto.FeatureSpeaking, wsproto.FeatureSilence, wsproto.FeatureHistory, wsproto.FeatureModels, wsproto.FeatureFeedback} {
		if !slices.Contains(m.Features, f) {
			t.Errorf("features %v lack %s", m.Features, f)
		}
	}
	if slices.Contains(m.Features, wsproto.FeaturePush) || slices.Contains(m.Features, wsproto.FeatureProvider) {
		t.Errorf("features %v list a service this daemon lacks", m.Features)
	}
	ev := waitHello(t, h, 1)[0]
	cl, _ := ev["client"].(map[string]any)
	if ev["legacy"] != false || ev["protocol"] != float64(1) || ev["accepted"] != true || cl["name"] != "sheepdog-ios" || cl["platform"] != "ios" || cl["version"] != "0.2.0 (12)" {
		t.Fatalf("hello = %v", ev)
	}
	// The connection carries on as usual.
	c.send(wsproto.Ping())
	c.expect(wsproto.TypePong)
}

// A newer client gets the daemon's own protocol back: it steps down.
func TestHandshakeNewerClientStepsDown(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	c := dialWS(t, h)
	c.send(wsproto.ClientReady(wsproto.ProtocolVersion+3, iosClient))
	m := c.expect(wsproto.TypeBotReady)
	if *m.Protocol != wsproto.ProtocolVersion {
		t.Fatalf("bot-ready protocol = %d", *m.Protocol)
	}
}

// The TestFlight app 0.1.0 (10) sends mute first and never client-ready:
// it is a legacy protocol-1 client at once, and never sees a bot-ready
// (its decoder would reject the type).
func TestHandshakeLegacyFirstFrame(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	c.send(wsproto.Mute(false))
	c.send(wsproto.Ping())
	for {
		m, _, err := c.read(3 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if m.Type == wsproto.TypeBotReady {
			t.Fatal("legacy client got bot-ready")
		}
		if m.Type == wsproto.TypePong {
			break
		}
	}
	ev := waitHello(t, h, 1)[0]
	if ev["legacy"] != true || ev["protocol"] != float64(1) || ev["why"] != "first_frame" || ev["client"] != nil {
		t.Fatalf("hello = %v", ev)
	}
}

// A client that sends nothing is legacy after the timeout; a late
// client-ready is still answered.
func TestHandshakeLegacyTimeoutThenLate(t *testing.T) {
	h := newHarness(t, harnessOpts{handshakeTimeout: 50 * time.Millisecond})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	ev := waitHello(t, h, 1)[0]
	if ev["legacy"] != true || ev["why"] != "timeout" {
		t.Fatalf("hello = %v", ev)
	}
	c.send(wsproto.ClientReady(1, iosClient))
	c.expect(wsproto.TypeBotReady)
	ev = waitHello(t, h, 2)[1]
	if ev["legacy"] != false || ev["late"] != true {
		t.Fatalf("late hello = %v", ev)
	}
}

// A client below the minimum gets a readable error, status
// closed/protocol_too_old and a 4001 close; no bot-ready.
func TestHandshakeTooOld(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	c.send(wsproto.ClientReady(0, iosClient))
	e := c.expect(wsproto.TypeError)
	st := c.expect(wsproto.TypeStatus)
	if st.Voice != wsproto.VoiceClosed || st.Reason != wsproto.ReasonProtocolTooOld {
		t.Fatalf("status = %+v", st)
	}
	ce := c.expectClose()
	if ce.Code != wsproto.CloseProtocolUnsupported || ce.Reason == "" {
		t.Fatalf("close = %+v", ce)
	}
	if e.Message == "" || !strings.Contains(e.Message, "Update the app") {
		t.Fatalf("error = %q", e.Message)
	}
	if ev := waitHello(t, h, 1)[0]; ev["accepted"] != false {
		t.Fatalf("hello = %v", ev)
	}
}

// Once the minimum moves past 1, a legacy client is refused the same
// way, and the old app can show the error's words.
func TestHandshakeLegacyRefusedAboveMin(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, minProtocol: 2})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	c.send(wsproto.Mute(true))
	c.expect(wsproto.TypeError)
	st := c.expect(wsproto.TypeStatus)
	if st.Reason != wsproto.ReasonProtocolTooOld {
		t.Fatalf("status = %+v", st)
	}
	if ce := c.expectClose(); ce.Code != wsproto.CloseProtocolUnsupported {
		t.Fatalf("close = %+v", ce)
	}
	if h.session.isMuted() {
		t.Fatal("a refused client's mute reached the session")
	}
}

func TestHelloProtocol(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	var hello HelloResponse
	h.getJSON("/api/hello", &hello)
	if hello.Protocol != wsproto.ProtocolVersion || hello.MinProtocol != wsproto.MinProtocolVersion {
		t.Fatalf("hello = %+v", hello)
	}
}

// A client refused for its protocol never replaces the attached one: the
// working client is not told "replaced", keeps the audio and stays
// attached.
func TestHandshakeRefusedClientDoesNotReplace(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	first := dialWS(t, h)
	first.expect(wsproto.TypeStatus)
	first.send(wsproto.ClientReady(1, iosClient))
	first.expect(wsproto.TypeBotReady)

	second := dialWS(t, h)
	second.expect(wsproto.TypeStatus)
	second.send(wsproto.ClientReady(0, iosClient))
	second.expect(wsproto.TypeError)
	if st := second.expect(wsproto.TypeStatus); st.Reason != wsproto.ReasonProtocolTooOld {
		t.Fatalf("second status = %+v", st)
	}
	if ce := second.expectClose(); ce.Code != wsproto.CloseProtocolUnsupported {
		t.Fatalf("second close = %+v", ce)
	}

	// The first client hears nothing about it and still gets the audio.
	h.session.audio <- []byte{7, 7}
	m, data, err := first.read(2 * time.Second)
	if err != nil || m.Type != "" || len(data) != 2 {
		t.Fatalf("first client after the refusal = %+v %v %v (want the audio, not a replaced status)", m, data, err)
	}
	first.send(wsproto.Ping())
	first.expect(wsproto.TypePong)
	var st StateResponse
	h.getJSON("/api/state", &st)
	if !st.Voice.ClientConnected {
		t.Fatal("the first client should still be attached")
	}
	for _, ty := range h.types() {
		if ty == "web.client.attach" {
			t.Fatal("a refused client was attached")
		}
	}
}

// An accepted client does replace the attached one, as soon as its
// client-ready arrives (no 2 s wait).
func TestHandshakeAcceptedClientReplaces(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	first := dialWS(t, h)
	first.expect(wsproto.TypeStatus)
	first.send(wsproto.Ping())
	first.expect(wsproto.TypePong)

	second := dialWS(t, h)
	second.expect(wsproto.TypeStatus)
	start := time.Now()
	second.send(wsproto.ClientReady(1, iosClient))
	second.expect(wsproto.TypeBotReady)
	st := first.expect(wsproto.TypeStatus)
	if st.Voice != wsproto.VoiceClosed || st.Reason != "replaced" {
		t.Fatalf("first status = %+v", st)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("replacement took %v", d)
	}
	h.session.audio <- []byte{3, 3}
	if m, data, err := second.read(2 * time.Second); err != nil || m.Type != "" || len(data) != 2 {
		t.Fatalf("second client audio = %+v %v %v", m, data, err)
	}
}
