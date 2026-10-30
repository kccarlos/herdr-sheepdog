package web

import (
	"net/http"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

func TestStateVoiceClient(t *testing.T) {
	macClient := wsproto.ClientInfo{Name: "sheepdog-mac", Version: "test", Platform: "macos"}
	tests := []struct {
		name         string
		run          func(*testing.T, *harness) *VoiceClientInfo
		wantName     string
		wantPlatform string
	}{
		{
			name: "absent",
			run: func(t *testing.T, h *harness) *VoiceClientInfo {
				var state StateResponse
				if code := h.getJSON("/api/state", &state); code != http.StatusOK {
					t.Fatalf("/api/state = %d", code)
				}
				return state.Voice.Client
			},
		},
		{
			name:         "set",
			wantName:     testClient.Name,
			wantPlatform: testClient.Platform,
			run: func(t *testing.T, h *harness) *VoiceClientInfo {
				dialWS(t, h)
				waitFor(t, 2*time.Second, func() bool {
					var state StateResponse
					h.getJSON("/api/state", &state)
					return state.Voice.Client != nil
				}, "client-ready reaches /api/state")
				var state StateResponse
				h.getJSON("/api/state", &state)
				return state.Voice.Client
			},
		},
		{
			name: "cleared on disconnect",
			run: func(t *testing.T, h *harness) *VoiceClientInfo {
				c := dialWS(t, h)
				waitFor(t, 2*time.Second, func() bool {
					var state StateResponse
					h.getJSON("/api/state", &state)
					return state.Voice.Client != nil
				}, "client-ready reaches /api/state")
				_ = c.conn.CloseNow()
				waitFor(t, 2*time.Second, func() bool {
					var state StateResponse
					h.getJSON("/api/state", &state)
					return state.Voice.Client == nil
				}, "disconnect clears voice.client")
				var state StateResponse
				h.getJSON("/api/state", &state)
				return state.Voice.Client
			},
		},
		{
			name:         "replaced",
			wantName:     macClient.Name,
			wantPlatform: macClient.Platform,
			run: func(t *testing.T, h *harness) *VoiceClientInfo {
				dialWS(t, h)
				waitFor(t, 2*time.Second, func() bool {
					var state StateResponse
					h.getJSON("/api/state", &state)
					return state.Voice.Client != nil && state.Voice.Client.Name == testClient.Name
				}, "first client reaches /api/state")

				next := dialWSRaw(t, h)
				next.send(wsproto.ClientReady(wsproto.ProtocolVersion, macClient))
				next.expect(wsproto.TypeBotReady)
				waitFor(t, 2*time.Second, func() bool {
					var state StateResponse
					h.getJSON("/api/state", &state)
					return state.Voice.Client != nil && state.Voice.Client.Name == macClient.Name
				}, "replacement reaches /api/state")
				var state StateResponse
				h.getJSON("/api/state", &state)
				return state.Voice.Client
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{voice: true})
			start := time.Now()
			got := tc.run(t, h)
			end := time.Now()
			if tc.wantName == "" {
				if got != nil {
					t.Fatalf("voice.client = %+v, want absent", got)
				}
				return
			}
			if got == nil {
				t.Fatal("voice.client is absent")
			}
			if got.Name != tc.wantName || got.Platform != tc.wantPlatform {
				t.Fatalf("voice.client = %+v, want %s/%s", got, tc.wantName, tc.wantPlatform)
			}
			if got.Since.IsZero() || got.Since.Before(start) || got.Since.After(end) {
				t.Fatalf("voice.client.since = %v, want within [%v, %v]", got.Since, start, end)
			}
		})
	}
}
