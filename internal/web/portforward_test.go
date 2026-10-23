package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/termproto"
)

// forwardedListener stands in for a router's port forward: the daemon
// sees each connection come from the client's public address
// (203.0.113.7, TEST-NET-3), as it does behind a NAT port forward, not
// from loopback. Every loopback decision in the daemon reads that
// address; nothing in this test uses kindHook.
type forwardedListener struct{ net.Listener }

func (l forwardedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return forwardedConn{c}, nil
}

type forwardedConn struct{ net.Conn }

func (c forwardedConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: c.Conn.RemoteAddr().(*net.TCPAddr).Port}
}

// serveForwarded runs the harness's server behind forwardedListener and
// returns the address a client dials.
func serveForwarded(t *testing.T, h *harness) string {
	t.Helper()
	if err := h.srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	h.srv.mu.Lock()
	h.srv.ln = forwardedListener{h.srv.ln}
	h.srv.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = h.srv.Serve(ctx, time.Second); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return h.srv.Addr()
}

// A phone reaching the daemon through a router's port forward gets TLS
// and device auth like any other network client, and never the
// loopback-only or setup routes: plain HTTP is refused, a wrong pin fails
// the handshake, no ticket is 401, the pairing and debug routes are 403
// even with a ticket; with one, the API, /ws, /term and a shell work.
func TestPortForwardGetsTLSAndDeviceAuth(t *testing.T) {
	h, _, root := newShellHarness(t, true)
	addr := serveForwarded(t, h)
	pin := h.srv.deps.TLS.Pin()
	tlsClient := pinnedClient(pin)
	plain := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	call := func(c *http.Client, method, url, ticket string, body any) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		}
		req, err := http.NewRequest(method, url, rd)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if ticket != "" {
			req.Header.Set("Authorization", "Bearer "+ticketPrefix+ticket)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	base := "https://" + addr

	// Plain HTTP: the API is 426, a page is sent to https.
	if st, out := call(plain, "GET", "http://"+addr+"/api/state", "", nil); st != http.StatusUpgradeRequired || out["error"] != "tls_required" {
		t.Errorf("plain /api/state = %d %v", st, out)
	}
	if st, out := call(plain, "GET", "http://"+addr+"/api/pair", "", nil); st != http.StatusUpgradeRequired {
		t.Errorf("plain /api/pair = %d %v", st, out)
	}
	if st, _ := call(plain, "GET", "http://"+addr+"/voice", "", nil); st != http.StatusPermanentRedirect {
		t.Errorf("plain /voice = %d", st)
	}
	// A wrong pin never gets past the handshake.
	if _, err := pinnedClient("AAAA").Get(base + "/api/hello"); err == nil {
		t.Error("a wrong pin connected")
	}
	// TLS without a ticket.
	if st, out := call(tlsClient, "GET", base+"/api/state", "", nil); st != http.StatusUnauthorized || out["reason"] != reasonNoSession {
		t.Errorf("no ticket = %d %v", st, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if c, _, err := websocket.Dial(ctx, "wss://"+addr+"/ws", &websocket.DialOptions{HTTPClient: tlsClient}); err == nil {
		_ = c.CloseNow()
		t.Error("/ws upgraded without a ticket")
	}
	// The pairing code is made on the computer only.
	if st, out := call(tlsClient, "GET", base+"/api/pair?new=1", "", nil); st != http.StatusForbidden || out["error"] != "loopback_only" {
		t.Errorf("/api/pair = %d %v", st, out)
	}

	// Pair over the forward, as the phone does after scanning the code
	// shown on the computer.
	key, pub := newDeviceKey(t)
	st, enrolled := call(tlsClient, "POST", base+"/api/devices/enroll", "", map[string]any{"code": h.pairCode(), "public_key": pub, "name": "Phone", "kind": DeviceIOS})
	if st != 200 {
		t.Fatalf("enroll = %d %v", st, enrolled)
	}
	deviceID, _ := enrolled["device_id"].(string)
	st, ch := call(tlsClient, "GET", base+"/api/auth/challenge", "", nil)
	nonce, _ := ch["nonce"].(string)
	if st != 200 || nonce == "" {
		t.Fatalf("challenge = %d %v", st, ch)
	}
	now := time.Now().Unix()
	sum := sha256.Sum256([]byte(SignedText(h.srv.deps.Computer.ID, pin, deviceID, nonce, now)))
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	st, sess := call(tlsClient, "POST", base+"/api/auth/session", "", map[string]any{"device_id": deviceID, "nonce": nonce, "time": now, "signature": base64.StdEncoding.EncodeToString(sig), "client": "app"})
	ticket, _ := sess["ticket"].(string)
	if st != 200 || ticket == "" {
		t.Fatalf("session = %d %v", st, sess)
	}

	if st, out := call(tlsClient, "GET", base+"/api/state", ticket, nil); st != 200 {
		t.Errorf("/api/state with a ticket = %d %v", st, out)
	}
	// A ticket opens no loopback-only or setup route.
	table, err := h.srv.routeTable()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, rt := range table {
		if rt.access != accessLoopback && rt.access != accessSetup {
			continue
		}
		method, path := samplePath(rt.pattern)
		st, out := call(tlsClient, method, base+path, ticket, map[string]any{})
		if st != http.StatusForbidden {
			t.Errorf("%s with a ticket over the forward = %d %v, want 403", rt.pattern, st, out)
		}
		if rt.access == accessLoopback && out["error"] != "loopback_only" {
			t.Errorf("%s = %v, want loopback_only", rt.pattern, out)
		}
		checked++
	}
	if checked < 10 {
		t.Errorf("only %d loopback/setup routes checked", checked)
	}

	header := http.Header{"Authorization": {"Bearer " + ticketPrefix + ticket}}
	ws, _, err := websocket.Dial(ctx, "wss://"+addr+"/ws", &websocket.DialOptions{HTTPClient: tlsClient, HTTPHeader: header})
	if err != nil {
		t.Fatalf("/ws with a ticket: %v", err)
	}
	_ = ws.CloseNow()

	conn, _, err := websocket.Dial(ctx, "wss://"+addr+"/term", &websocket.DialOptions{HTTPClient: tlsClient, HTTPHeader: header})
	if err != nil {
		t.Fatalf("/term with a ticket: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	tc := &termClient{t: t, conn: conn, ctx: ctx}
	p := 1
	tc.send(termproto.Message{Type: termproto.TypeHello, Protocol: &p})
	if m := tc.expect(termproto.TypeReady); m.Shell == nil || !*m.Shell {
		t.Fatalf("term-ready = %+v", m)
	}
	tc.send(shellOpen(1, filepath.Join(root, "app")))
	if m := tc.expect(termproto.TypeOpened); m.Kind != "shell" {
		t.Errorf("term-opened = %+v", m)
	}

	// The daemon saw a network client, never loopback.
	sessions := 0
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "web.device.session" {
			continue
		}
		sessions++
		var data map[string]any
		_ = json.Unmarshal(ev.Payload, &data)
		if data["remote_kind"] == "loopback" || data["remote_kind"] == nil {
			t.Errorf("the forwarded session was logged as %v", data["remote_kind"])
		}
	}
	if sessions != 1 {
		t.Errorf("%d web.device.session events, want 1", sessions)
	}
}
