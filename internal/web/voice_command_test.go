package web

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

func TestHTTPVoiceStopCutsSpeechAndTellsTheClient(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	var got VoiceCommandResponse
	if code, body := h.postJSON("/api/voice/stop", map[string]any{}, &got); code != http.StatusOK || !got.OK {
		t.Fatalf("stop = %d %s", code, body)
	}
	// The /ws cancel path ran: flush, then the stop result pushed.
	c.expect(wsproto.TypeFlush)
	if m := c.expect(wsproto.TypeVoiceCommand); m.Action != wsproto.ActionStop || m.On != nil {
		t.Fatalf("voice-command = %+v", m)
	}
	waitFor(t, 2*time.Second, func() bool { return len(h.session.pushed()) == 1 }, "stop result pushed")
	if p := h.session.pushed(); p[0] != "Nothing was in flight." {
		t.Fatalf("pushed = %v", p)
	}
	if !subsequence(h.types(), []string{"voice.tool_call", "voice.tool_result", "web.client.flush"}) {
		t.Fatalf("log = %v", h.types())
	}
}

func TestHTTPVoicePTTReachesTheClient(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	for _, on := range []bool{true, false} {
		var got VoiceCommandResponse
		if code, body := h.postJSON("/api/voice/ptt", map[string]any{"on": on}, &got); code != http.StatusOK || !got.OK {
			t.Fatalf("ptt %v = %d %s", on, code, body)
		}
		m := c.expect(wsproto.TypeVoiceCommand)
		if want := wsproto.VoicePTT(on); !reflect.DeepEqual(m, want) {
			t.Fatalf("voice-command = %+v, want %+v", m, want)
		}
	}
}

// An HTTP mute reaches the holder as a voice-command before the status
// that confirms it, so a client that owns its mute takes it as its own
// instead of restating the old state.
func TestHTTPMuteTellsTheClientBeforeTheStatus(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	for _, on := range []bool{true, false} {
		var got MuteResponse
		if code, body := h.postJSON("/api/voice/mute", map[string]any{"on": on}, &got); code != http.StatusOK || got.Muted != on {
			t.Fatalf("mute %v = %d %s", on, code, body)
		}
		var order []string
		for len(order) < 2 {
			m, _, err := c.read(3 * time.Second)
			if err != nil {
				t.Fatalf("mute %v: reading: %v (so far %v)", on, err, order)
			}
			switch m.Type {
			case wsproto.TypeVoiceCommand:
				if want := wsproto.VoiceMute(on); !reflect.DeepEqual(m, want) {
					t.Fatalf("voice-command = %+v, want %+v", m, want)
				}
				order = append(order, m.Type)
			case wsproto.TypeStatus:
				if m.Muted == nil || *m.Muted != on {
					t.Fatalf("status = %+v, want muted %v", m, on)
				}
				order = append(order, m.Type)
			}
		}
		if want := []string{wsproto.TypeVoiceCommand, wsproto.TypeStatus}; !reflect.DeepEqual(order, want) {
			t.Fatalf("mute %v order = %v, want %v", on, order, want)
		}
	}
}

func TestHTTPVoiceCommandWithoutClient(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	for path, body := range map[string]any{"/api/voice/stop": map[string]any{}, "/api/voice/ptt": map[string]any{"on": true}} {
		var got map[string]string
		if code, raw := h.postJSON(path, body, &got); code != http.StatusConflict || got["error"] != "no voice client" {
			t.Errorf("%s with no client = %d %s", path, code, raw)
		}
	}
	if len(h.session.pushed()) != 0 {
		t.Fatalf("stop without a client pushed %v", h.session.pushed())
	}
}

func TestHTTPVoiceCommandWithoutVoice(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	for path, body := range map[string]any{"/api/voice/stop": map[string]any{}, "/api/voice/ptt": map[string]any{"on": true}} {
		var got map[string]string
		code, raw := h.postJSON(path, body, &got)
		if code != http.StatusServiceUnavailable || !strings.Contains(got["error"], "voice is off") {
			t.Errorf("%s without voice = %d %s", path, code, raw)
		}
	}
}

func TestHTTPVoicePTTRejectsBadBodies(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)
	for _, body := range []string{
		"",
		"{}",
		`{"on":null}`,
		`{"on":"true"}`,
		`{"on":1}`,
		`{"on":`,
		`[]`,
	} {
		req, err := http.NewRequest(http.MethodPost, h.http.URL+"/api/voice/ptt", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		var got map[string]string
		if code, _ := h.do(req, &got); code != http.StatusBadRequest {
			t.Errorf("body %q = %d %v, want 400", body, code, got)
		}
	}
	// Nothing reached the client: the next frame after a ping is its pong.
	c.send(wsproto.Ping())
	c.expect(wsproto.TypePong)
}

func TestHTTPVoiceCommandAuthMatchesState(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	h.asRemote()
	for path, body := range map[string]any{"/api/voice/stop": map[string]any{}, "/api/voice/ptt": map[string]any{"on": true}} {
		var got map[string]string
		if code, _ := h.do(h.authedRequest(http.MethodPost, path, "", nil, body), &got); code != http.StatusUnauthorized {
			t.Errorf("%s without a ticket = %d %v, want 401", path, code, got)
		}
	}
}
