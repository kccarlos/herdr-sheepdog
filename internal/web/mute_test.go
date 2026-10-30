package web

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

func TestHTTPMuteUsesWSStatusPath(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	c := dialWS(t, h)
	c.expect(wsproto.TypeStatus)

	c.send(wsproto.Mute(true))
	wantOn := c.expect(wsproto.TypeStatus)
	if wantOn.Muted == nil || !*wantOn.Muted {
		t.Fatalf("ws mute status = %+v", wantOn)
	}

	var off MuteResponse
	if code, body := h.postJSON("/api/voice/mute", map[string]any{"on": false}, &off); code != http.StatusOK || off.Muted {
		t.Fatalf("http unmute = %d %s %+v", code, body, off)
	}
	gotOff := c.expect(wsproto.TypeStatus)
	if gotOff.Muted == nil || *gotOff.Muted {
		t.Fatalf("http unmute status = %+v", gotOff)
	}
	waitFor(t, time.Second, func() bool { return !h.session.isMuted() }, "session unmuted over HTTP")

	var on MuteResponse
	if code, body := h.postJSON("/api/voice/mute", map[string]any{"on": true}, &on); code != http.StatusOK || !on.Muted {
		t.Fatalf("http mute = %d %s %+v", code, body, on)
	}
	gotOn := c.expect(wsproto.TypeStatus)
	if !reflect.DeepEqual(gotOn, wantOn) {
		t.Fatalf("HTTP mute status = %+v, want the ws mute status %+v", gotOn, wantOn)
	}
	waitFor(t, time.Second, h.session.isMuted, "session muted over HTTP")

	var vias []string
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "web.client.mute" {
			continue
		}
		var payload struct {
			Via string `json:"via"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("decode web.client.mute: %v", err)
		}
		vias = append(vias, payload.Via)
	}
	if want := []string{"ws", "http", "http"}; !reflect.DeepEqual(vias, want) {
		t.Fatalf("web.client.mute via = %v, want %v", vias, want)
	}
}

func TestHTTPMuteRejectsBadBodies(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	for _, body := range []string{
		"",
		"{}",
		`{"on":null}`,
		`{"on":"true"}`,
		`{"on":1}`,
		`{"on":`,
		`[]`,
	} {
		req, err := http.NewRequest(http.MethodPost, h.http.URL+"/api/voice/mute", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		var got map[string]string
		if code, _ := h.do(req, &got); code != http.StatusBadRequest {
			t.Errorf("body %q = %d %v, want 400", body, code, got)
		}
	}
	if h.session.isMuted() || h.srv.hub.state().muted {
		t.Fatal("a bad body changed the mute state")
	}
}

func TestHTTPMuteWithoutVoice(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	var got map[string]string
	code, body := h.postJSON("/api/voice/mute", map[string]any{"on": true}, &got)
	if code != http.StatusServiceUnavailable || !strings.Contains(got["error"], "voice is off") {
		t.Fatalf("mute without voice = %d %s", code, body)
	}
}

func TestHTTPMuteAuthMatchesState(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	h.asRemote()

	var stateBody map[string]string
	stateCode, _ := h.do(h.authedRequest(http.MethodGet, "/api/state", "", nil, nil), &stateBody)
	var muteBody map[string]string
	muteCode, _ := h.do(h.authedRequest(http.MethodPost, "/api/voice/mute", "", nil, map[string]any{"on": true}), &muteBody)
	if stateCode != http.StatusUnauthorized || muteCode != stateCode || !reflect.DeepEqual(muteBody, stateBody) {
		t.Fatalf("state auth = %d %v, mute auth = %d %v", stateCode, stateBody, muteCode, muteBody)
	}
}

func TestStateVoiceSpeaking(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	var state StateResponse
	if code := h.getJSON("/api/state", &state); code != http.StatusOK || state.Voice.Speaking {
		t.Fatalf("initial /api/state = %d speaking=%v", code, state.Voice.Speaking)
	}

	h.session.setSpeaking(true)
	if code := h.getJSON("/api/state", &state); code != http.StatusOK || !state.Voice.Speaking {
		t.Fatalf("speaking /api/state = %d speaking=%v", code, state.Voice.Speaking)
	}

	h.session.setSpeaking(false)
	if code := h.getJSON("/api/state", &state); code != http.StatusOK || state.Voice.Speaking {
		t.Fatalf("stopped /api/state = %d speaking=%v", code, state.Voice.Speaking)
	}
	var raw struct {
		Voice map[string]any `json:"voice"`
	}
	if err := json.Unmarshal(getRaw(t, h, "/api/state"), &raw); err != nil {
		t.Fatal(err)
	}
	if speaking, ok := raw.Voice["speaking"].(bool); !ok || speaking {
		t.Fatalf("voice.speaking is not a present false boolean: %#v", raw.Voice["speaking"])
	}

	off := newHarness(t, harnessOpts{})
	if code := off.getJSON("/api/state", &state); code != http.StatusOK || state.Voice.Speaking {
		t.Fatalf("voice-off /api/state = %d speaking=%v", code, state.Voice.Speaking)
	}
}
