package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/push"
)

// The /api/push routes: token auth like every /api route, register is
// idempotent and validated, status reports the sender's state, DELETE
// removes the device.
func TestPushRoutes(t *testing.T) {
	sender, err := push.New(push.Config{}, nil) // unconfigured: disabled, in memory
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, harnessOpts{token: "s3cr3t", push: sender})
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
		code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/register", "s3cr3t", nil, map[string]string{"token": tok, "environment": "sandbox", "device": "iPhone"}), &reg)
		if code != http.StatusOK || !reg.OK || len(reg.ContentKey) != 44 || reg.Encryption != "aes-256-gcm" {
			t.Fatalf("register #%d: code=%d body=%+v", i+1, code, reg)
		}
		keys[reg.ContentKey] = true
	}
	if len(keys) != 1 {
		t.Errorf("content keys = %v, want one", keys)
	}
	// A relay token alone registers too, and unregisters by itself.
	if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/register", "s3cr3t", nil, map[string]string{"relay_token": "AQIDBAUGBwgJCgsMDQ4PEBES"}), nil); code != http.StatusOK || sender.Status().Devices != 2 {
		t.Errorf("relay-only register: code=%d devices=%d", code, sender.Status().Devices)
	}
	if code, _ := h.do(h.authedRequest(http.MethodDelete, "/api/push/register", "s3cr3t", nil, map[string]string{"relay_token": "AQIDBAUGBwgJCgsMDQ4PEBES"}), nil); code != http.StatusOK || sender.Status().Devices != 1 {
		t.Errorf("relay-only delete: code=%d devices=%d", code, sender.Status().Devices)
	}
	var ok map[string]bool
	for _, bad := range []map[string]string{{"token": "nothex", "environment": "production"}, {"token": tok, "environment": "debug"}, {"relay_token": "not a token!"}} {
		if code, _ := h.do(h.authedRequest(http.MethodPost, "/api/push/register", "s3cr3t", nil, bad), nil); code != http.StatusBadRequest {
			t.Errorf("register %v: code=%d, want 400", bad, code)
		}
	}
	var st push.Status
	if code, _ := h.do(h.authedRequest(http.MethodGet, "/api/push/status", "s3cr3t", nil, nil), &st); code != http.StatusOK || st.Enabled || st.Devices != 1 || !strings.Contains(st.Reason, "not configured") {
		t.Errorf("status: code=%d %+v", code, st)
	}
	if code, _ := h.do(h.authedRequest(http.MethodDelete, "/api/push/register", "s3cr3t", nil, map[string]string{"token": tok}), &ok); code != http.StatusOK {
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
