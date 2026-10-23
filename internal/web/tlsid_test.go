package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The key and certificate are made once, kept 0600 in a 0700 directory,
// and read back with the same pin; Reset makes a new pin.
func TestTLSIdentityIsMadeOnceAndKept(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	id, err := LoadTLSIdentity(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if len(id.Pin()) != 43 {
		t.Errorf("pin = %q", id.Pin())
	}
	if got := id.Expires(); !got.Equal(now.Add(CertLifetime)) {
		t.Errorf("expires = %v", got)
	}
	again, err := LoadTLSIdentity(dir, nil)
	if err != nil || again.Pin() != id.Pin() {
		t.Fatalf("reloaded pin = %q, %v; want %q", again.Pin(), err, id.Pin())
	}
	for _, name := range []string{"key.pem", "cert.pem"} {
		fi, err := os.Stat(filepath.Join(dir, "tls", name))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, %v", name, fi.Mode(), err)
		}
	}
	if di, err := os.Stat(filepath.Join(dir, "tls")); err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, %v", di.Mode(), err)
	}
	old := id.Pin()
	if err := id.Reset(); err != nil {
		t.Fatal(err)
	}
	if id.Pin() == old {
		t.Error("Reset kept the pin")
	}
	if back, _ := LoadTLSIdentity(dir, nil); back.Pin() != id.Pin() {
		t.Error("Reset was not saved")
	}
}

// A damaged key is an error, not a new identity.
func TestTLSIdentityRefusesADamagedKey(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadTLSIdentity(dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls", "key.pem"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTLSIdentity(dir, nil); err == nil {
		t.Error("a damaged key was accepted")
	}
}

// serveReal runs the harness's server on a real loopback listener, as
// the daemon does, and returns its address.
func serveReal(t *testing.T, h *harness) string {
	t.Helper()
	if err := h.srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = h.srv.Serve(ctx, time.Second); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return h.srv.Addr()
}

// pinnedClient trusts exactly one pin and checks no name, as the app does.
func pinnedClient(pin string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: pinnedTLS(pin)}}
}

func pinnedTLS(pin string) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // the pin below is the check
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			leaf, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			if SPKIPin(leaf.RawSubjectPublicKeyInfo) != pin {
				return errors.New("pin mismatch")
			}
			return nil
		},
	}
}

// One port serves TLS and plain HTTP; the pin in /api/hello is the key
// the TLS side serves; a wrong pin fails the handshake; /ws upgrades over
// TLS.
func TestOnePortServesTLSAndPlain(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	addr := serveReal(t, h)

	resp, err := http.Get("http://" + addr + "/api/hello")
	if err != nil {
		t.Fatal(err)
	}
	var hello HelloResponse
	_ = json.NewDecoder(resp.Body).Decode(&hello)
	_ = resp.Body.Close()
	if hello.Security.Pin == "" || hello.Security.Pin != h.srv.deps.TLS.Pin() || hello.Security.CertExpires.IsZero() {
		t.Fatalf("security = %+v", hello.Security)
	}

	resp, err = pinnedClient(hello.Security.Pin).Get("https://" + addr + "/api/hello")
	if err != nil {
		t.Fatalf("TLS: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("TLS hello = %d %+v", resp.StatusCode, resp.TLS)
	}

	if _, err := pinnedClient("AAAA").Get("https://" + addr + "/api/hello"); err == nil {
		t.Error("a wrong pin connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "wss://"+addr+"/ws", &websocket.DialOptions{HTTPClient: pinnedClient(hello.Security.Pin)})
	if err != nil {
		t.Fatalf("wss: %v", err)
	}
	_ = conn.CloseNow()
}

// Plain HTTP from another machine is never served: a page is redirected
// to https, the API and upgrades get 426.
func TestPlainHTTPFromTheNetworkIsRefused(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	addr := serveReal(t, h)
	h.srv.kindHook = func(string) string { return "lan" }
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := noFollow.Get("http://" + addr + "/voice?x=1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "https://"+addr+"/voice?x=1" {
		t.Errorf("page = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, path := range []string{"/api/state", "/api/hello", "/ws", "/events", "/term"} {
		resp, err := noFollow.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUpgradeRequired || body["error"] != "tls_required" {
			t.Errorf("%s = %d %v", path, resp.StatusCode, body)
		}
	}
	// The same client over TLS gets through.
	resp, err = pinnedClient(h.srv.deps.TLS.Pin()).Get("https://" + addr + "/api/hello")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("TLS from the LAN = %d", resp.StatusCode)
	}
}

// On loopback every route checks the Host, so a DNS-rebinding page
// (evil.example resolving to 127.0.0.1) gets nothing.
func TestLoopbackChecksTheHost(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	addr := serveReal(t, h)
	for host, want := range map[string]int{"evil.example:7777": 403, "localhost:7777": 200, "127.0.0.1": 200, "[::1]:7777": 200} {
		req, _ := http.NewRequest("GET", "http://"+addr+"/api/hello", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %s = %d, want %d", host, resp.StatusCode, want)
		}
	}
}

// A connection that never sends its first byte is dropped, and does not
// hold up the next one.
func TestSilentConnectionDoesNotBlock(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := LoadTLSIdentity("", nil)
	sl := newSniffListener(ln, id.TLSConfig(), 200*time.Millisecond)
	defer func() { _ = sl.Close() }()

	silent, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	talker, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = talker.Close() }()
	_, _ = talker.Write([]byte("GET / HTTP/1.1\r\n\r\n"))

	got := make(chan net.Conn, 1)
	go func() {
		c, err := sl.Accept()
		if err == nil {
			got <- c
		}
	}()
	select {
	case c := <-got:
		if _, isTLS := c.(*tls.Conn); isTLS {
			t.Error("plain connection handed out as TLS")
		}
		buf := make([]byte, 3)
		if _, err := c.Read(buf); err != nil || !strings.HasPrefix(string(buf), "GET") {
			t.Errorf("first bytes = %q, %v", buf, err)
		}
		_ = c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("the talking connection was held up")
	}
	_ = silent.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := silent.Read(make([]byte, 1)); err == nil {
		t.Error("the silent connection was not closed")
	}
}
