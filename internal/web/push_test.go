package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/push"
)

// The /api/push routes: device auth like every /api route, register is
// idempotent and validated, status reports the sender's state, DELETE
// removes the device.
func TestPushRoutes(t *testing.T) {
	sender, err := push.New(push.Config{}, nil) // unconfigured: disabled, in memory
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, harnessOpts{push: sender})
	d := h.pairDevice()
	h.asRemote()
	tok := strings.Repeat("0a", 32)

	if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/register", "", nil, map[string]string{"token": tok, "environment": "production"}), nil); code != http.StatusUnauthorized {
		t.Errorf("no token: code=%d, want 401", code)
	}
	// Each registration answers the phone's content key, the same one
	// every time.
	keys := map[string]bool{}
	for i := 0; i < 2; i++ {
		var reg struct {
			OK         bool   `json:"ok"`
			ContentKey string `json:"content_key"`
			Encryption string `json:"encryption"`
		}
		code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/register", d.ticket, nil, map[string]string{"token": tok, "environment": "sandbox", "device": "iPhone"}), &reg)
		if code != http.StatusOK || !reg.OK || len(reg.ContentKey) != 44 || reg.Encryption != "aes-256-gcm" {
			t.Fatalf("register #%d: code=%d body=%+v", i+1, code, reg)
		}
		keys[reg.ContentKey] = true
	}
	if len(keys) != 1 {
		t.Errorf("content keys = %v, want one", keys)
	}
	// A relay token alone registers too (replacing the device's earlier
	// registration: one per paired device), and unregisters by itself.
	if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/register", d.ticket, nil, map[string]string{"relay_token": "AQIDBAUGBwgJCgsMDQ4PEBES"}), nil); code != http.StatusOK || sender.Status().Devices != 1 {
		t.Errorf("relay-only register: code=%d devices=%d", code, sender.Status().Devices)
	}
	if code, _ := h.do(h.authedRequest(http.MethodDelete, "/api/push/register", d.ticket, nil, map[string]string{"relay_token": "AQIDBAUGBwgJCgsMDQ4PEBES"}), nil); code != http.StatusOK || sender.Status().Devices != 0 {
		t.Errorf("relay-only delete: code=%d devices=%d", code, sender.Status().Devices)
	}
	var ok map[string]bool
	for _, bad := range []map[string]string{{"token": "nothex", "environment": "production"}, {"token": tok, "environment": "debug"}, {"relay_token": "not a token!"}} {
		if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/register", d.ticket, nil, bad), nil); code != http.StatusBadRequest {
			t.Errorf("register %v: code=%d, want 400", bad, code)
		}
	}
	var st push.Status
	if code, _ := h.do(h.authedRequest(http.MethodGet, "/api/push/status", d.ticket, nil, nil), &st); code != http.StatusOK || st.Enabled || st.Devices != 0 || !strings.Contains(st.Reason, "not configured") {
		t.Errorf("status: code=%d %+v", code, st)
	}
	if code, _ := h.do(h.authedRequest(http.MethodDelete, "/api/push/register", d.ticket, nil, map[string]string{"token": tok}), &ok); code != http.StatusOK {
		t.Errorf("delete: code=%d", code)
	}
	if sender.Status().Devices != 0 {
		t.Errorf("devices after delete = %d", sender.Status().Devices)
	}
}

// Without a push service the register routes answer 503 and status says
// why.
func TestPushRoutesUnavailable(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/register", "", nil, map[string]string{"token": strings.Repeat("0a", 32), "environment": "production"}), nil); code != http.StatusServiceUnavailable {
		t.Errorf("register: code=%d, want 503", code)
	}
	var st push.Status
	if code, _ := h.do(h.authedRequest(http.MethodGet, "/api/push/status", "", nil, nil), &st); code != http.StatusOK || st.Enabled || st.Reason == "" {
		t.Errorf("status: code=%d %+v", code, st)
	}
}

// Web Push routes (/api/push/web) and the Service Worker (/voice/sw.js):
// paired browsers subscribe with RFC 8291 keys, status exposes the VAPID
// public key, revoking a browser drops its Web Push subscription, and
// /voice/sw.js carries Service-Worker-Allowed: /voice so /voice (no slash)
// is in scope.
func TestWebPushRoutesAndServiceWorkerHeader(t *testing.T) {
	sender, err := push.New(push.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, harnessOpts{push: sender})
	d := h.pairDevice()
	h.asRemote()

	var st push.Status
	if code, _ := h.do(h.authedRequest(http.MethodGet, "/api/push/status", d.ticket, nil, nil), &st); code != http.StatusOK || !st.WebPushEnabled || len(st.VAPIDPublicKey) < 80 {
		t.Fatalf("status = %d %+v", code, st)
	}

	sub := map[string]any{
		"endpoint": "https://fcm.googleapis.com/fcm/send/browser-123",
		"keys": map[string]string{
			"p256dh": st.VAPIDPublicKey, // valid 65-byte P-256 uncompressed point
			"auth":   "MDEyMzQ1Njc4OWFiY2RlZg",
		},
		"device": "Android · Chrome",
	}
	if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/web", "", nil, sub), nil); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated POST /api/push/web = %d, want 401", code)
	}
	if code, body := h.do(h.authedRequest(http.MethodPost, "/api/push/web", d.ticket, nil, sub), nil); code != http.StatusOK {
		t.Fatalf("POST /api/push/web = %d %s", code, body)
	}
	if got := sender.Status().WebDevices; got != 1 {
		t.Fatalf("WebDevices = %d, want 1", got)
	}
	// Bad subscription answers 400.
	if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/web", d.ticket, nil, map[string]any{"endpoint": "http://bad"}), nil); code != http.StatusBadRequest {
		t.Errorf("bad POST /api/push/web = %d, want 400", code)
	}
	// Unregister removes it; unknown endpoint is still 200.
	if code, _ := h.do(h.authedRequest(http.MethodDelete, "/api/push/web", d.ticket, nil, map[string]string{"endpoint": "https://fcm.googleapis.com/fcm/send/browser-123"}), nil); code != http.StatusOK || sender.Status().WebDevices != 0 {
		t.Errorf("DELETE /api/push/web = %d, web_devices=%d", code, sender.Status().WebDevices)
	}
	// Re-subscribe, then revoke the paired browser: Web Push subscription goes too.
	if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/web", d.ticket, nil, sub), nil); code != http.StatusOK {
		t.Fatalf("re-subscribe = %d", code)
	}
	if _, err := h.srv.revokeDevices([]string{d.id}, "test"); err != nil {
		t.Fatal(err)
	}
	if got := sender.Status().WebDevices; got != 0 {
		t.Errorf("after revoke WebDevices = %d, want 0", got)
	}

	// GET /voice/sw.js is public, text/javascript, no-cache, and sets Service-Worker-Allowed: /voice.
	resp, err := http.Get(h.http.URL + "/voice/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /voice/sw.js = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("sw.js Content-Type = %q", ct)
	}
	if swa := resp.Header.Get("Service-Worker-Allowed"); swa != "/voice" {
		t.Errorf("sw.js Service-Worker-Allowed = %q, want /voice", swa)
	}
}
