package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// The API contract the iOS app builds against: GET /api/voice/providers,
// PUT /api/voice/provider, voice_provider/voice_model on /api/hello and
// /api/state, provider on the /ws status message.
func TestVoiceProviderEndpoints(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, switcher: true})
	var got VoiceProvidersResponse
	if code := h.getJSON("/api/voice/providers", &got); code != http.StatusOK || got.Provider != "gemini" || got.Model != "gemini-3.8-live" || len(got.Providers) != 3 {
		t.Fatalf("GET = %d %+v", code, got)
	}
	if p := got.Providers[1]; p.ID != "openai" || p.Label != "GPT-Live" || p.Model != "gpt-live-1" || !p.Available || p.Billing != voice.BillingPerMinute {
		t.Errorf("openai = %+v", p)
	}
	if p := got.Providers[2]; p.Available || p.Reason != "no OTHER_KEY" {
		t.Errorf("other = %+v", p)
	}
	// The raw JSON shape: reason only when unavailable, billing only when
	// per minute.
	_, raw := h.sendJSON(http.MethodGet, "/api/voice/providers", nil, nil)
	if !strings.Contains(raw, `"billing": "per_minute"`) || strings.Count(raw, `"reason"`) != 1 {
		t.Errorf("raw = %s", raw)
	}

	c := dialWS(t, h)
	st := c.expect(wsproto.TypeStatus)
	if st.Provider != "gemini" {
		t.Errorf("ws status provider = %q", st.Provider)
	}
	waitUntil(t, "client attached", h.session.isAttached)

	var e map[string]string
	if code, _ := h.sendJSON(http.MethodPut, "/api/voice/provider", map[string]any{"provider": "other"}, &e); code != http.StatusBadRequest || !strings.Contains(e["error"], "no OTHER_KEY") {
		t.Errorf("PUT other = %d %v", code, e)
	}
	if code, _ := h.sendJSON(http.MethodPut, "/api/voice/provider", map[string]any{"provider": "nope"}, &e); code != http.StatusBadRequest || !strings.Contains(e["error"], "unknown provider") {
		t.Errorf("PUT nope = %d %v", code, e)
	}
	if code, raw := h.sendJSON(http.MethodPut, "/api/voice/provider", map[string]any{"provider": "OpenAI"}, &got); code != http.StatusOK || got.Provider != "openai" || got.Model != "gpt-live-1" {
		t.Fatalf("PUT openai = %d %s", code, raw)
	}
	if ss := h.store.Snapshot().Session; ss.VoiceProvider != "openai" {
		t.Errorf("session.voice_provider = %q", ss.VoiceProvider)
	}
	// The new provider's session got the client state.
	if !h.session2.isAttached() {
		t.Error("client state not replayed onto the new provider")
	}
	var hello HelloResponse
	if h.getJSON("/api/hello", &hello); hello.VoiceProvider != "openai" || hello.VoiceModel != "gpt-live-1" {
		t.Errorf("hello = %+v", hello)
	}
	var state StateResponse
	if h.getJSON("/api/state", &state); state.VoiceProvider != "openai" || state.VoiceModel != "gpt-live-1" {
		t.Errorf("state = %s/%s", state.VoiceProvider, state.VoiceModel)
	}
	// A status from the new session reaches the client with its provider.
	h.session2.emit(voice.EventStatus{Status: voice.StatusConnected, Reason: "standby"})
	deadline := time.Now().Add(2 * time.Second)
	for {
		m := c.expect(wsproto.TypeStatus)
		if m.Provider == "openai" && m.Reason == "standby" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no openai status: %+v", m)
		}
	}
	var changed []string
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "voice.provider_changed" {
			var p struct{ From, To, Via string }
			_ = json.Unmarshal(ev.Payload, &p)
			changed = append(changed, p.From+">"+p.To+"/"+p.Via)
		}
	}
	if strings.Join(changed, " ") != "gemini>openai/api" {
		t.Errorf("voice.provider_changed = %v", changed)
	}
	_ = c.conn.CloseNow()
	waitUntil(t, "client detached", func() bool { return !h.session2.isAttached() })
}

func TestVoiceProviderEndpointsVoiceOff(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	var got VoiceProvidersResponse
	if code := h.getJSON("/api/voice/providers", &got); code != http.StatusOK || got.Provider != "off" || got.Providers == nil {
		t.Fatalf("GET = %d %+v", code, got)
	}
	if code, _ := h.sendJSON(http.MethodPut, "/api/voice/provider", map[string]any{"provider": "openai"}, nil); code != http.StatusServiceUnavailable {
		t.Errorf("PUT = %d", code)
	}
	var hello HelloResponse
	if h.getJSON("/api/hello", &hello); hello.VoiceProvider != "off" {
		t.Errorf("hello = %+v", hello)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
