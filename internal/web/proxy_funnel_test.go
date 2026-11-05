package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

func TestTicketlessLoopbackBlockedWithForwardedHeaders(t *testing.T) {
	f := newSetupFixture(t, config.Default())
	h := f.h

	proxyHeaders := []struct {
		key, val string
	}{
		{"X-Forwarded-For", "203.0.113.10"},
		{"X-Forwarded-Host", "tunnel.example.com"},
		{"X-Forwarded-Proto", "https"},
		{"X-Forwarded-Port", "443"},
		{"Forwarded", "for=203.0.113.10;proto=https"},
		{"X-Real-IP", "203.0.113.10"},
		{"Tailscale-Funnel-Request", "?1"},
		{"Tailscale-User-Login", "someone@example.com"},
		{"CF-Connecting-IP", "203.0.113.10"},
		{"True-Client-IP", "203.0.113.10"},
	}

	for _, hdr := range proxyHeaders {
		// Device route (/api/state): ticketless proxied loopback gets 401 no_session.
		r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
		r.RemoteAddr, r.Host = "127.0.0.1:54321", "127.0.0.1:7777"
		r.Header.Set(hdr.key, hdr.val)
		w := httptest.NewRecorder()
		h.srv.handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"no_session"`) {
			t.Errorf("%s on /api/state = %d %s, want 401 no_session", hdr.key, w.Code, w.Body.String())
		}

		// Loopback-only route (/api/pair, /debug): proxied loopback gets 403 loopback_only.
		for _, path := range []string{"/api/pair", "/debug", "/api/devices"} {
			r2 := httptest.NewRequest(http.MethodGet, path, nil)
			r2.RemoteAddr, r2.Host = "127.0.0.1:54321", "127.0.0.1:7777"
			r2.Header.Set(hdr.key, hdr.val)
			w2 := httptest.NewRecorder()
			h.srv.handler().ServeHTTP(w2, r2)
			if w2.Code != http.StatusForbidden || !strings.Contains(w2.Body.String(), "loopback_only") {
				t.Errorf("%s on %s = %d %s, want 403 loopback_only", hdr.key, path, w2.Code, w2.Body.String())
			}
		}

		// Setup route (/setup, /api/setup/state): proxied loopback gets 403.
		for _, path := range []string{"/setup", "/api/setup/state"} {
			r3 := httptest.NewRequest(http.MethodGet, path, nil)
			r3.RemoteAddr, r3.Host = "127.0.0.1:54321", "127.0.0.1:7777"
			r3.Header.Set(hdr.key, hdr.val)
			w3 := httptest.NewRecorder()
			h.srv.handler().ServeHTTP(w3, r3)
			if w3.Code != http.StatusForbidden {
				t.Errorf("%s on %s = %d, want 403", hdr.key, path, w3.Code)
			}
		}
	}

	// Genuine local tools (no proxy headers) continue to work without a ticket.
	for _, path := range []string{"/api/state", "/api/pair", "/setup", "/api/setup/state"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:54321", "127.0.0.1:7777"
		w := httptest.NewRecorder()
		h.srv.handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("direct local %s = %d %s, want 200", path, w.Code, w.Body.String())
		}
	}
}

func TestPairedTicketsStillWorkThroughProxies(t *testing.T) {
	f := newSetupFixture(t, config.Default())
	h := f.h
	d := h.pairDevice()

	// 1. Paired sds_ bearer ticket works through a Host-rewriting proxy
	// (e.g. tailscale serve, nginx default) AND a Host-preserving proxy
	// (e.g. caddy reverse_proxy).
	for _, host := range []string{"127.0.0.1:7777", "studio.tail1234.ts.net"} {
		r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
		r.RemoteAddr, r.Host = "127.0.0.1:54321", host
		r.Header.Set("X-Forwarded-For", "100.64.0.9")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("Authorization", "Bearer "+ticketPrefix+d.ticket)
		w := httptest.NewRecorder()
		h.srv.handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("paired sds_ ticket via proxy (Host %s) = %d %s, want 200", host, w.Code, w.Body.String())
		}

		// POST with Idempotency-Key also works through proxy.
		rPost := httptest.NewRequest(http.MethodPost, "/api/utterance", strings.NewReader(`{"text":"status"}`))
		rPost.RemoteAddr, rPost.Host = "127.0.0.1:54321", host
		rPost.Header.Set("Content-Type", "application/json")
		rPost.Header.Set("X-Forwarded-For", "100.64.0.9")
		rPost.Header.Set("Authorization", "Bearer "+ticketPrefix+d.ticket)
		wPost := httptest.NewRecorder()
		h.srv.handler().ServeHTTP(wPost, rPost)
		if wPost.Code != http.StatusOK {
			t.Fatalf("paired POST via proxy (Host %s) = %d %s, want 200", host, wPost.Code, wPost.Body.String())
		}

		// Loopback-only and setup routes stay blocked even with a paired sds_ ticket!
		for _, blockedPath := range []string{"/api/pair", "/debug", "/api/setup/state"} {
			rb := httptest.NewRequest(http.MethodGet, blockedPath, nil)
			rb.RemoteAddr, rb.Host = "127.0.0.1:54321", host
			rb.Header.Set("X-Forwarded-For", "100.64.0.9")
			rb.Header.Set("Authorization", "Bearer "+ticketPrefix+d.ticket)
			wb := httptest.NewRecorder()
			h.srv.handler().ServeHTTP(wb, rb)
			if wb.Code != http.StatusForbidden {
				t.Errorf("paired sds_ ticket on %s via proxy (Host %s) = %d, want 403", blockedPath, host, wb.Code)
			}
		}
	}

	// Device's last_network must be "proxy", not "loopback" ("this computer").
	stored, ok := h.srv.devices.get(d.id)
	if !ok || stored.LastNetwork != "proxy" {
		t.Errorf("proxied device LastNetwork = %q, want %q", stored.LastNetwork, "proxy")
	}

	// DNS rebinding with a forged/invalid sds_ ticket is still refused by guard!
	rEvil := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	rEvil.RemoteAddr, rEvil.Host = "127.0.0.1:54321", "evil.example:7777"
	rEvil.Header.Set("Authorization", "Bearer "+ticketPrefix+"forged")
	wEvil := httptest.NewRecorder()
	h.srv.handler().ServeHTTP(wEvil, rEvil)
	if wEvil.Code != http.StatusForbidden || !strings.Contains(wEvil.Body.String(), "host_not_allowed") {
		t.Errorf("DNS rebinding with forged ticket = %d %s, want 403 host_not_allowed", wEvil.Code, wEvil.Body.String())
	}

	// 2. Paired browser signing in over an HTTPS reverse proxy receives a Secure
	// cookie and can authenticate with it; unauthenticated WS upgrade is 401 while
	// authenticated WS upgrade succeeds.
	bKey, bPub := newDeviceKey(t)
	_, enrolled := h.enrollDevice(h.pairCode(), bPub, DeviceBrowser)
	var ch ChallengeResponse
	h.getJSON("/api/auth/challenge", &ch)
	now := time.Now()
	sum := sha256.Sum256([]byte(SignedText(h.srv.deps.Computer.ID, "", enrolled["device_id"], ch.Nonce, now.Unix())))
	rr, ss, _ := ecdsa.Sign(rand.Reader, bKey, sum[:])
	sig := make([]byte, 64)
	rr.FillBytes(sig[:32])
	ss.FillBytes(sig[32:])
	sessBody := `{"device_id":"` + enrolled["device_id"] + `","nonce":"` + ch.Nonce + `","time":` + strconv.FormatInt(now.Unix(), 10) + `,"signature":"` + base64.RawURLEncoding.EncodeToString(sig) + `","client":"browser"}`
	rSess := httptest.NewRequest(http.MethodPost, "/api/auth/session", strings.NewReader(sessBody))
	rSess.RemoteAddr, rSess.Host = "127.0.0.1:54321", "127.0.0.1:7777"
	rSess.Header.Set("Content-Type", "application/json")
	rSess.Header.Set("X-Forwarded-For", "100.64.0.9")
	rSess.Header.Set("X-Forwarded-Proto", "https")
	wSess := httptest.NewRecorder()
	h.srv.handler().ServeHTTP(wSess, rSess)
	cookies := wSess.Result().Cookies()
	if wSess.Code != http.StatusOK || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("browser session over HTTPS proxy = %d cookies=%+v, want 200 with Secure cookie", wSess.Code, cookies)
	}

	// WebSocket /ws upgrade over proxied loopback: refused without ticket, accepted with sds_ ticket.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(h.http.URL, "http") + "/ws"
	if _, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"X-Forwarded-For": {"203.0.113.5"}},
	}); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("ticketless proxied /ws = %v %v, want 401", resp, err)
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"X-Forwarded-For": {"100.64.0.9"},
			"Authorization":   {"Bearer " + ticketPrefix + d.ticket},
		},
	})
	if err != nil {
		t.Fatalf("paired sds_ proxied /ws failed: %v", err)
	}
	_ = conn.CloseNow()
}

func TestProxiedLoopbackAuthEndpointsAreRateLimited(t *testing.T) {
	h := newHarness(t, harnessOpts{})

	// Wrong enroll codes through a localhost tunnel trigger 429 rate_limited.
	_, pub := newDeviceKey(t)
	var lastCode int
	for i := 0; i <= badCodesPerMinute+2; i++ {
		r := httptest.NewRequest(http.MethodPost, "/api/devices/enroll", strings.NewReader(`{"code":"guess","public_key":"`+pub+`","name":"attacker","kind":"ios"}`))
		r.RemoteAddr, r.Host = "127.0.0.1:54321", "127.0.0.1:7777"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", "198.51.100.77")
		w := httptest.NewRecorder()
		h.srv.handler().ServeHTTP(w, r)
		lastCode = w.Code
	}
	if lastCode != http.StatusTooManyRequests {
		t.Errorf("proxied enroll brute-force status = %d, want 429", lastCode)
	}

	// Genuine local caller (no proxy headers) is unaffected by the tunnel's block.
	goodCode := h.pairCode()
	rLocal := httptest.NewRequest(http.MethodPost, "/api/devices/enroll", strings.NewReader(`{"code":"`+goodCode+`","public_key":"`+pub+`","name":"local","kind":"browser"}`))
	rLocal.RemoteAddr, rLocal.Host = "127.0.0.1:54321", "127.0.0.1:7777"
	rLocal.Header.Set("Content-Type", "application/json")
	wLocal := httptest.NewRecorder()
	h.srv.handler().ServeHTTP(wLocal, rLocal)
	if wLocal.Code != http.StatusOK {
		t.Errorf("direct loopback after proxy block = %d %s, want 200", wLocal.Code, wLocal.Body.String())
	}
}

func TestFunnelStatusParsing(t *testing.T) {
	cases := []struct {
		name      string
		json      string
		port      int
		wantAny   bool
		wantPort  bool
		otherPort bool
	}{
		{
			name:     "empty config",
			json:     `{}`,
			port:     7777,
			wantAny:  false,
			wantPort: false,
		},
		{
			name: "tailscale serve only (Funnel off)",
			json: `{
				"TCP": {"443": {"HTTPS": true}},
				"Web": {"studio.tail1234.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:7777"}}}},
				"AllowFunnel": {"studio.tail1234.ts.net:443": false}
			}`,
			port:     7777,
			wantAny:  false,
			wantPort: false,
		},
		{
			name: "background HTTP Funnel to sheepdog port",
			json: `{
				"TCP": {"443": {"HTTPS": true}},
				"Web": {"studio.tail1234.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:7777"}}}},
				"AllowFunnel": {"studio.tail1234.ts.net:443": true}
			}`,
			port:      7777,
			wantAny:   true,
			wantPort:  true,
			otherPort: false,
		},
		{
			name: "background TCPForward Funnel to sheepdog port",
			json: `{
				"TCP": {"8443": {"TCPForward": "127.0.0.1:7777"}},
				"AllowFunnel": {"studio.tail1234.ts.net:8443": true}
			}`,
			port:      7777,
			wantAny:   true,
			wantPort:  true,
			otherPort: false,
		},
		{
			name: "foreground interactive Funnel to sheepdog port",
			json: `{
				"Foreground": {
					"sess1": {
						"TCP": {"443": {"HTTPS": true}},
						"Web": {"studio.tail1234.ts.net:443": {"Handlers": {"/": {"Proxy": "http://localhost:7777"}}}},
						"AllowFunnel": {"studio.tail1234.ts.net:443": true}
					}
				}
			}`,
			port:      7777,
			wantAny:   true,
			wantPort:  true,
			otherPort: false,
		},
		{
			name: "static file Funnel does not target sheepdog port",
			json: `{
				"Web": {"studio.tail1234.ts.net:443": {"Handlers": {"/": {"Path": "/srv/public"}}}},
				"AllowFunnel": {"studio.tail1234.ts.net:443": true}
			}`,
			port:      7777,
			wantAny:   true,
			wantPort:  false,
			otherPort: false,
		},
		{
			name: "unknown schema with AllowFunnel true fails closed",
			json: `{
				"AllowFunnel": {"studio.tail1234.ts.net:443": true}
			}`,
			port:      7777,
			wantAny:   true,
			wantPort:  true,
			otherPort: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AnyFunnelActive([]byte(tc.json)); got != tc.wantAny {
				t.Errorf("AnyFunnelActive = %v, want %v", got, tc.wantAny)
			}
			if got := FunnelTargetsPort([]byte(tc.json), tc.port); got != tc.wantPort {
				t.Errorf("FunnelTargetsPort(%d) = %v, want %v", tc.port, got, tc.wantPort)
			}
			if got := FunnelTargetsPort([]byte(tc.json), 3000); got != tc.otherPort {
				t.Errorf("FunnelTargetsPort(3000) = %v, want %v", got, tc.otherPort)
			}
		})
	}
}

func TestActiveTailscaleFunnelBlocksTicketlessLoopback(t *testing.T) {
	f := newSetupFixture(t, config.Default())
	h := f.h
	d := h.pairDevice()

	var calls atomic.Int32
	var active atomic.Bool
	active.Store(true)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	h.srv.deps.Now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	h.srv.deps.TailscaleFunnel = func(_ context.Context, port int) (bool, error) {
		calls.Add(1)
		return active.Load(), nil
	}
	addr := serveReal(t, h)
	_, portStr, _ := net.SplitHostPort(addr)
	_ = portStr

	// Concurrent requests coalesce/cache Funnel checks and block ticketless loopback
	// even when NO X-Forwarded-* header is present (e.g. raw TCPForward Funnel).
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
			r.RemoteAddr, r.Host = "127.0.0.1:54321", "127.0.0.1:7777"
			w := httptest.NewRecorder()
			h.srv.handler().ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("ticketless loopback with Funnel active = %d, want 401", w.Code)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Errorf("TailscaleFunnel calls across burst = %d, want 1 (cached/coalesced)", got)
	}

	// Setup and loopback-only routes are also refused while Funnel is active.
	for _, path := range []string{"/setup", "/api/setup/state", "/api/pair", "/debug"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr, r.Host = "127.0.0.1:54321", "127.0.0.1:7777"
		w := httptest.NewRecorder()
		h.srv.handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s with Funnel active = %d, want 403", path, w.Code)
		}
	}

	// Paired sds_ ticket STILL works on device routes even when Funnel is active.
	rPaired := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rPaired.RemoteAddr, rPaired.Host = "127.0.0.1:54321", "127.0.0.1:7777"
	rPaired.Header.Set("Authorization", "Bearer "+ticketPrefix+d.ticket)
	wPaired := httptest.NewRecorder()
	h.srv.handler().ServeHTTP(wPaired, rPaired)
	if wPaired.Code != http.StatusOK {
		t.Errorf("paired sds_ ticket with Funnel active = %d, want 200", wPaired.Code)
	}

	// Once Funnel is reset and the cache expires, local tools work again immediately.
	active.Store(false)
	clockMu.Lock()
	now = now.Add(10 * time.Second)
	clockMu.Unlock()

	rAfter := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rAfter.RemoteAddr, rAfter.Host = "127.0.0.1:54321", "127.0.0.1:7777"
	wAfter := httptest.NewRecorder()
	h.srv.handler().ServeHTTP(wAfter, rAfter)
	if wAfter.Code != http.StatusOK {
		t.Errorf("direct loopback after Funnel reset = %d, want 200", wAfter.Code)
	}
}

func TestTailscaleCertCLIRefusesActiveFunnel(t *testing.T) {
	binDir := t.TempDir()
	fakeTS := filepath.Join(binDir, "tailscale")
	script := `#!/bin/sh
if [ "$1" = "status" ]; then
  echo '{"Self":{"DNSName":"studio.tail1234.ts.net."}}'
  exit 0
fi
if [ "$1" = "serve" ] && [ "$2" = "status" ]; then
  echo '{"AllowFunnel":{"studio.tail1234.ts.net:443":true},"Web":{"studio.tail1234.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7777"}}}}}'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(fakeTS, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	fn := TailscaleCertCLI(t.TempDir())
	_, _, _, err := fn(context.Background())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "funnel") {
		t.Fatalf("TailscaleCertCLI with active Funnel = %v, want Funnel refusal", err)
	}
}
