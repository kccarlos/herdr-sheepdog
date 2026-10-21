package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/feedback"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

func feedbackEvents(h *harness) []feedback.Payload {
	var out []feedback.Payload
	for _, ev := range h.log.Tail(0) {
		if ev.Type == feedback.EventType {
			var p feedback.Payload
			if err := json.Unmarshal(ev.Payload, &p); err == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

// TestWSDrainIsLogged: a drain message is logged as web.client.drain
// with the server's skew, and never touches the session's playback.
func TestWSDrainIsLogged(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	at := time.Now().Add(-20 * time.Millisecond).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	c.send(wsproto.Drain(at, 3900, 3800, 7800, wsproto.DrainEarly))
	c.sendRaw(`{"type":"drain","at":"later","buffer_ms":0,"played_ms":1}`)
	if e := c.expect(wsproto.TypeError); !strings.Contains(e.Message, "at") {
		t.Fatalf("error = %+v", e)
	}
	var p map[string]any
	waitFor(t, time.Second, func() bool {
		for _, ev := range h.log.Tail(0) {
			if ev.Type == "web.client.drain" {
				_ = json.Unmarshal(ev.Payload, &p)
				return true
			}
		}
		return false
	}, "web.client.drain")
	if p["at"] != at || p["buffer_ms"] != 3900.0 || p["played_ms"] != 3800.0 || p["received_ms"] != 7800.0 || p["reason"] != "early" {
		t.Fatalf("drain = %v", p)
	}
	if skew, ok := p["skew_ms"].(float64); !ok || skew < 0 || skew > 5000 {
		t.Fatalf("skew_ms = %v", p["skew_ms"])
	}
	if got := h.session.playbackReports(); len(got) != 0 {
		t.Fatalf("a drain reached the session as a playback report: %v", got)
	}
}

// TestFeedbackSnapshot: POST /api/feedback logs user.feedback with the
// voice's audio run as the client received it and the window's events.
func TestFeedbackSnapshot(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	h.session.audio <- make([]byte, 4800) // 100 ms
	h.session.audio <- make([]byte, 9600) // 200 ms
	for n := 0; n < 2; {
		if _, b, err := c.read(2 * time.Second); err != nil {
			t.Fatal(err)
		} else if b != nil {
			n++
		}
	}
	// The writer marks a chunk sent after Write returns, which can be
	// after the client has read it.
	waitFor(t, 2*time.Second, func() bool {
		cs := h.srv.audio.Since(time.Time{})
		return len(cs) == 2 && !cs[0].SentAt.IsZero() && !cs[1].SentAt.IsZero()
	}, "both chunks marked sent")
	h.session.emit(voice.EventTranscript{Dir: voice.DirOut, Text: "hello"})
	h.log.Log("voice", "voice.transcript.out", "", map[string]any{"text": strings.Repeat("x", 300)})
	h.log.Log("brain", "brain.turn_started", "", map[string]any{"turn_id": "turn-1", "kind": "utterance"})
	h.log.Log("herdr", "herdr.state", "", map[string]any{"state": "idle"})

	var resp FeedbackResponse
	clientTS := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	code, body := h.postJSON("/api/feedback", FeedbackRequest{Tag: "paused", Note: " that paused ", ClientTS: clientTS, ConversationTurn: "b-12"}, &resp)
	if code != http.StatusOK || !resp.OK || !strings.HasPrefix(resp.ID, "fb-") {
		t.Fatalf("POST = %d %s", code, body)
	}
	fbs := feedbackEvents(h)
	if len(fbs) != 1 {
		t.Fatalf("user.feedback events = %d", len(fbs))
	}
	p := fbs[0]
	if p.ID != resp.ID || p.Tag != "paused" || p.Note != "that paused" || p.ClientTS != clientTS || p.ConversationTurn != "b-12" || p.SkewMs == nil || *p.SkewMs < 900 {
		t.Fatalf("payload = %+v", p)
	}
	s := p.Snapshot
	if s.WindowS != 30 || len(s.Runs) != 1 {
		t.Fatalf("snapshot = %+v", s)
	}
	r := s.Runs[0]
	if len(r.Chunks) != 2 || r.Chunks[1][1] != 200 || r.Chunks[0][2] < 0 || r.Chunks[1][2] < 0 || r.Unsent != 0 || r.Timing["audio_ms"] != 300.0 {
		t.Fatalf("run = %+v", r)
	}
	var types []string
	for _, ev := range s.Events {
		types = append(types, ev.Type)
		if ev.Type == "voice.transcript.out" && strings.Count(string(ev.Payload), "x") > 201 {
			t.Errorf("long text kept whole: %s", ev.Payload)
		}
	}
	if strings.Contains(strings.Join(types, ","), "herdr.") || !strings.Contains(strings.Join(types, ","), "voice.transcript.out") {
		t.Fatalf("event types = %v", types)
	}
	if len(s.BrainInFlight) != 1 || s.BrainInFlight[0].TurnID != "turn-1" {
		t.Fatalf("in flight = %+v", s.BrainInFlight)
	}

	// The tag defaults to paused; an unknown one or a bad time is refused.
	if code, body := h.postJSON("/api/feedback", map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("empty body = %d %s", code, body)
	}
	if got := feedbackEvents(h); len(got) != 2 || got[1].Tag != "paused" {
		t.Fatalf("default tag: %+v", got)
	}
	if code, _ := h.postJSON("/api/feedback", map[string]any{"tag": "loud"}, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown tag = %d", code)
	}
	if code, _ := h.postJSON("/api/feedback", map[string]any{"tag": "misheard", "client_ts": "now"}, nil); code != http.StatusBadRequest {
		t.Fatalf("bad client_ts = %d", code)
	}
}

// TestFeedbackNeedsTheToken: POST /api/feedback is behind web.token like
// every other /api route.
func TestFeedbackNeedsTheToken(t *testing.T) {
	h := newHarness(t, harnessOpts{token: "s3cr3t"})
	if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/feedback", "", nil, map[string]any{"tag": "paused"}), nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/feedback", "s3cr3t", nil, map[string]any{"tag": "paused"}), nil); code != http.StatusOK {
		t.Fatalf("with token: %d", code)
	}
}
