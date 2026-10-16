package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// TestAuthHeaderQueryCookie covers the three ways a request may carry
// web.token (Authorization: Bearer, ?token= and the login cookie) and
// the two ways it may fail (missing, wrong), against a plain protected
// GET (/api/state) and against /ws and /events, which additionally
// accept ?token=.
func TestAuthHeaderQueryCookie(t *testing.T) {
	h := newHarness(t, harnessOpts{token: "s3cr3t"})

	t.Run("missing", func(t *testing.T) {
		var body map[string]string
		req := h.authedRequest(http.MethodGet, "/api/state", "", nil, nil)
		if code, _ := h.do(req, &body); code != http.StatusUnauthorized || body["error"] != "unauthorized" {
			t.Errorf("missing token: code=%d body=%v", code, body)
		}
	})

	t.Run("wrong bearer", func(t *testing.T) {
		req := h.authedRequest(http.MethodGet, "/api/state", "nope", nil, nil)
		if code, _ := h.do(req, nil); code != http.StatusUnauthorized {
			t.Errorf("wrong bearer: code=%d", code)
		}
	})

	t.Run("correct bearer", func(t *testing.T) {
		req := h.authedRequest(http.MethodGet, "/api/state", "s3cr3t", nil, nil)
		if code, _ := h.do(req, nil); code != http.StatusOK {
			t.Errorf("correct bearer: code=%d", code)
		}
	})

	t.Run("query token not accepted on a plain API route", func(t *testing.T) {
		if code := h.getJSON("/api/state?token=s3cr3t", nil); code != http.StatusUnauthorized {
			t.Errorf("?token= on /api/state: code=%d, want 401 (only /ws and /events accept it)", code)
		}
	})

	t.Run("query token accepted on /events", func(t *testing.T) {
		resp, err := http.Get(h.http.URL + "/events?token=s3cr3t")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("/events?token=s3cr3t: code=%d", resp.StatusCode)
		}
	})

	t.Run("query token wrong on /events", func(t *testing.T) {
		if code := h.getJSON("/events?token=nope", nil); code != http.StatusUnauthorized {
			t.Errorf("/events?token=nope: code=%d", code)
		}
	})

	t.Run("cookie", func(t *testing.T) {
		cookie := &http.Cookie{Name: tokenCookie, Value: "s3cr3t"}
		req := h.authedRequest(http.MethodGet, "/api/state", "", []*http.Cookie{cookie}, nil)
		if code, _ := h.do(req, nil); code != http.StatusOK {
			t.Errorf("cookie: code=%d", code)
		}
		wrong := &http.Cookie{Name: tokenCookie, Value: "nope"}
		req = h.authedRequest(http.MethodGet, "/api/state", "", []*http.Cookie{wrong}, nil)
		if code, _ := h.do(req, nil); code != http.StatusUnauthorized {
			t.Errorf("wrong cookie: code=%d", code)
		}
	})

	// web.unauthorized is logged with the path and remote address, never
	// the token.
	found := false
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "web.unauthorized" {
			continue
		}
		found = true
		if strings.Contains(string(ev.Payload), "s3cr3t") || strings.Contains(string(ev.Payload), "nope") {
			t.Errorf("web.unauthorized leaked a token: %s", ev.Payload)
		}
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p["path"] == nil || p["remote"] == nil {
			t.Errorf("web.unauthorized missing path/remote: %s", ev.Payload)
		}
	}
	if !found {
		t.Error("no web.unauthorized event logged")
	}
}

// TestAuthNoTokenConfigured proves every route stays public when
// web.token is unset (v1's behaviour, unchanged).
func TestAuthNoTokenConfigured(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if code := h.getJSON("/api/state", nil); code != http.StatusOK {
		t.Errorf("/api/state without a configured token: %d", code)
	}
}

// TestAPIHelloPublic proves GET /api/hello answers without a token even
// when one is configured, and reports the right auth mode and audio
// contract.
func TestAPIHelloPublic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
		auth  string
	}{
		{"no token configured", "", "none"},
		{"token configured", "s3cr3t", "token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, harnessOpts{token: tc.token})
			var resp HelloResponse
			if code := h.getJSON("/api/hello", &resp); code != http.StatusOK {
				t.Fatalf("/api/hello: %d", code)
			}
			if resp.Name != "sheepdog" || resp.WS != "/ws" || resp.Auth != tc.auth {
				t.Errorf("hello = %+v", resp)
			}
			if resp.Audio.InHz != 16000 || resp.Audio.OutHz != 24000 || resp.Audio.Format == "" {
				t.Errorf("hello.audio = %+v", resp.Audio)
			}
		})
	}
}

// TestAPILoginSetsCookie proves POST /api/login sets the httpOnly,
// SameSite=Lax cookie (no TLS in this test, so Secure is off) on a
// correct token, 401s on a wrong one, and that the cookie it hands back
// then authorizes a protected route.
func TestAPILoginSetsCookie(t *testing.T) {
	h := newHarness(t, harnessOpts{token: "s3cr3t"})

	req := h.authedRequest(http.MethodPost, "/api/login", "", nil, LoginRequest{Token: "nope"})
	if code, _ := h.do(req, nil); code != http.StatusUnauthorized {
		t.Errorf("wrong token: code=%d", code)
	}

	req = h.authedRequest(http.MethodPost, "/api/login", "", nil, LoginRequest{Token: "s3cr3t"})
	code, cookies := h.do(req, nil)
	if code != http.StatusOK {
		t.Fatalf("correct token: code=%d", code)
	}
	var got *http.Cookie
	for _, c := range cookies {
		if c.Name == tokenCookie {
			got = c
		}
	}
	if got == nil {
		t.Fatal("POST /api/login did not set sheepdog_token")
	}
	if got.Value != "s3cr3t" || !got.HttpOnly || got.Secure || got.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie = %+v", got)
	}

	req = h.authedRequest(http.MethodGet, "/api/state", "", []*http.Cookie{got}, nil)
	if code, _ := h.do(req, nil); code != http.StatusOK {
		t.Errorf("cookie from login: code=%d", code)
	}
}

// TestStaticPagesAndAssetsStayPublic proves the pages, /static assets
// and dev routes never need the token: they hold no data.
func TestStaticPagesAndAssetsStayPublic(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, harnessOpts{token: "s3cr3t", devTestdata: dir})
	for _, path := range []string{"/", "/voice", "/voice/", "/voice/app.js", "/debug", "/debug/", "/static/logo-color.svg"} {
		if code := h.getJSON(path, nil); code != http.StatusOK && code != http.StatusFound {
			t.Errorf("GET %s = %d, want public", path, code)
		}
	}
}

// TestWSUpgradeRequiresToken proves /ws refuses the upgrade with 401
// when a token is configured and none is given, accepts ?token=, and
// stays open (no auth needed) when web.token is unset.
func TestWSUpgradeRequiresToken(t *testing.T) {
	t.Run("missing token", func(t *testing.T) {
		h := newHarness(t, harnessOpts{token: "s3cr3t"})
		resp, err := http.Get(h.http.URL + "/ws")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET /ws without a token: %d, want 401", resp.StatusCode)
		}
	})

	t.Run("query token", func(t *testing.T) {
		h := newHarness(t, harnessOpts{token: "s3cr3t", voice: true})
		ctx := t.Context()
		url := "ws" + strings.TrimPrefix(h.http.URL, "http") + "/ws?token=s3cr3t"
		conn, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			t.Fatalf("dial with ?token=: %v", err)
		}
		_ = conn.CloseNow()
	})

	t.Run("no token configured", func(t *testing.T) {
		h := newHarness(t, harnessOpts{voice: true})
		c := dialWS(t, h)
		c.expect("status")
	})
}

// TestSSERequiresToken proves /events refuses without the token and
// accepts ?token=, mirroring /ws.
func TestSSERequiresToken(t *testing.T) {
	h := newHarness(t, harnessOpts{token: "s3cr3t"})
	resp, err := http.Get(h.http.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /events without a token: %d, want 401", resp.StatusCode)
	}
	resp, err = http.Get(h.http.URL + "/events?token=s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /events?token=s3cr3t: %d, want 200", resp.StatusCode)
	}
}
