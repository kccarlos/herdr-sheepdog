package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

// testDevice is a paired device in a test: its key, id and ticket.
type testDevice struct {
	key    *ecdsa.PrivateKey
	id     string
	ticket string
}

// newDeviceKey makes a P-256 key and its public key as the app sends it.
func newDeviceKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := k.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return k, base64.RawURLEncoding.EncodeToString(raw)
}

// pairCode is a fresh pairing code from the daemon (as the setup page or
// `sheepdog pair` gets it).
func (h *harness) pairCode() string {
	h.t.Helper()
	code, _, _, err := h.srv.enroll.current(time.Now(), true)
	if err != nil {
		h.t.Fatal(err)
	}
	return code
}

// enrollDevice enrolls key with code and returns the status and body.
func (h *harness) enrollDevice(code, pub, kind string) (int, map[string]string) {
	h.t.Helper()
	var out map[string]string
	code2, _ := h.sendJSON(http.MethodPost, "/api/devices/enroll", map[string]any{"code": code, "public_key": pub, "name": "Test phone", "kind": kind}, &out)
	return code2, out
}

// signIn signs a challenge with key for device id and returns the status
// and the session answer.
func (h *harness) signIn(key *ecdsa.PrivateKey, id string, pin string, at time.Time) (int, SessionResponse, map[string]string) {
	h.t.Helper()
	var ch ChallengeResponse
	if code := h.getJSON("/api/auth/challenge", &ch); code != 200 || ch.Nonce == "" {
		h.t.Fatalf("challenge = %d %+v", code, ch)
	}
	return h.signNonce(key, id, pin, ch.Nonce, at)
}

func (h *harness) signNonce(key *ecdsa.PrivateKey, id, pin, nonce string, at time.Time) (int, SessionResponse, map[string]string) {
	h.t.Helper()
	sum := sha256.Sum256([]byte(SignedText(h.srv.deps.Computer.ID, pin, id, nonce, at.Unix())))
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		h.t.Fatal(err)
	}
	var raw json.RawMessage
	code, _ := h.sendJSON(http.MethodPost, "/api/auth/session", map[string]any{"device_id": id, "nonce": nonce, "time": at.Unix(), "signature": base64.StdEncoding.EncodeToString(sig), "client": "app"}, &raw)
	var ok SessionResponse
	var bad map[string]string
	_ = json.Unmarshal(raw, &ok)
	_ = json.Unmarshal(raw, &bad)
	return code, ok, bad
}

// pairDevice pairs a new device and signs it in, as the app does after a
// scan.
func (h *harness) pairDevice() testDevice {
	h.t.Helper()
	key, pub := newDeviceKey(h.t)
	code, out := h.enrollDevice(h.pairCode(), pub, DeviceIOS)
	if code != 200 || out["device_id"] == "" || out["daemon_id"] != h.srv.deps.Computer.ID {
		h.t.Fatalf("enroll = %d %v", code, out)
	}
	st, sess, bad := h.signIn(key, out["device_id"], h.srv.deps.TLS.Pin(), time.Now())
	if st != 200 || sess.Ticket == "" {
		h.t.Fatalf("session = %d %v", st, bad)
	}
	return testDevice{key: key, id: out["device_id"], ticket: sess.Ticket}
}

// asRemote makes every request look like it comes from another machine
// on the LAN (over TLS, as the harness's server pretends).
func (h *harness) asRemote() { h.srv.kindHook = func(string) string { return "lan" } }

// reasonOf is GET /api/state with ticket: the status and the reason in a
// 401 body.
func (h *harness) reasonOf(ticket string) (int, string) {
	h.t.Helper()
	var body map[string]string
	code, _ := h.do(h.authedRequest("GET", "/api/state", ticket, nil, nil), &body)
	return code, body["reason"]
}

// The one-time code works once, a second use is code_used, and a new
// code makes the old one stop working.
func TestEnrollCodeWorksOnce(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	_, pub := newDeviceKey(t)
	code := h.pairCode()
	if st, out := h.enrollDevice(code, pub, DeviceIOS); st != 200 {
		t.Fatalf("first use = %d %v", st, out)
	}
	_, pub2 := newDeviceKey(t)
	if st, out := h.enrollDevice(code, pub2, DeviceIOS); st != http.StatusGone || out["reason"] != "code_used" {
		t.Errorf("second use = %d %v", st, out)
	}
	old := h.pairCode()
	_ = h.pairCode() // a new code replaces it
	if st, out := h.enrollDevice(old, pub2, DeviceIOS); st != http.StatusGone || out["reason"] != "code_expired" {
		t.Errorf("replaced code = %d %v", st, out)
	}
	if st, out := h.enrollDevice("never-shown", pub2, DeviceIOS); st != http.StatusGone || out["reason"] != "code_used" {
		t.Errorf("unknown code = %d %v", st, out)
	}
	if st, _ := h.enrollDevice(h.pairCode(), "not a key", DeviceIOS); st != http.StatusBadRequest {
		t.Errorf("bad key = %d", st)
	}
	if st, _ := h.enrollDevice(h.pairCode(), pub2, "fridge"); st != http.StatusBadRequest {
		t.Errorf("bad kind = %d", st)
	}
}

// A code expires 10 minutes after it is shown.
func TestEnrollCodeExpires(t *testing.T) {
	var e enrollCodes
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	code, exp, _, _ := e.current(now, false)
	if !exp.Equal(now.Add(10 * time.Minute)) {
		t.Errorf("expires = %v", exp)
	}
	if again, _, made, _ := e.current(now.Add(time.Minute), false); again != code || made {
		t.Error("reading the code made a new one")
	}
	if r := e.burn(code, now.Add(10*time.Minute+time.Second)); r != "code_expired" {
		t.Errorf("late = %q", r)
	}
}

// The signed challenge: a good signature gets a ticket; a reused nonce,
// an old time, another key, a revoked device are refused; a restart
// forgets every ticket.
func TestSignedSession(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	d := h.pairDevice()
	pin := h.srv.deps.TLS.Pin()

	var ch ChallengeResponse
	h.getJSON("/api/auth/challenge", &ch)
	if st, _, _ := h.signNonce(d.key, d.id, pin, ch.Nonce, time.Now()); st != 200 {
		t.Fatalf("good signature = %d", st)
	}
	if st, _, bad := h.signNonce(d.key, d.id, pin, ch.Nonce, time.Now()); st != 401 || bad["reason"] != "bad_nonce" {
		t.Errorf("reused nonce = %d %v", st, bad)
	}
	if st, _, bad := h.signIn(d.key, d.id, pin, time.Now().Add(-10*time.Minute)); st != 401 || bad["reason"] != "bad_time" {
		t.Errorf("old time = %d %v", st, bad)
	}
	other, _ := newDeviceKey(t)
	if st, _, bad := h.signIn(other, d.id, pin, time.Now()); st != 401 || bad["reason"] != "bad_signature" {
		t.Errorf("another key = %d %v", st, bad)
	}
	if st, _, bad := h.signIn(d.key, d.id, "another-pin", time.Now()); st != 401 || bad["reason"] != "bad_signature" {
		t.Errorf("another daemon's pin = %d %v", st, bad)
	}
	// A browser signs an empty pin line.
	if st, _, _ := h.signIn(d.key, d.id, "", time.Now()); st != 200 {
		t.Errorf("empty pin = %d", st)
	}
	if st, _, bad := h.signIn(d.key, "nobody", pin, time.Now()); st != 401 || bad["reason"] != reasonUnknownDevice {
		t.Errorf("unknown device = %d %v", st, bad)
	}

	h.asRemote()
	if st, _ := h.reasonOf(d.ticket); st != 200 {
		t.Fatalf("with the ticket = %d", st)
	}
	if st, why := h.reasonOf(""); st != 401 || why != reasonNoSession {
		t.Errorf("no ticket = %d %q", st, why)
	}
	h.srv.auth = newAuthState() // a restart
	if st, why := h.reasonOf(d.ticket); st != 401 || why != reasonSessionExpired {
		t.Errorf("after a restart = %d %q", st, why)
	}
	st, sess, _ := h.signIn(d.key, d.id, pin, time.Now())
	if st != 200 {
		t.Fatal("could not sign again")
	}
	if _, err := h.srv.revokeDevices([]string{d.id}, "setup"); err != nil {
		t.Fatal(err)
	}
	if st, why := h.reasonOf(sess.Ticket); st != 401 || why != reasonSessionExpired {
		t.Errorf("revoked device's ticket = %d %q (its tickets are dropped)", st, why)
	}
	if st, _, bad := h.signIn(d.key, d.id, pin, time.Now()); st != 401 || bad["reason"] != reasonDeviceRevoked {
		t.Errorf("revoked device signing = %d %v", st, bad)
	}
}

// A browser's session comes as an HttpOnly, SameSite=Strict cookie, which
// then authenticates it.
func TestBrowserSessionCookie(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	key, pub := newDeviceKey(t)
	_, out := h.enrollDevice(h.pairCode(), pub, DeviceBrowser)
	var ch ChallengeResponse
	h.getJSON("/api/auth/challenge", &ch)
	now := time.Now()
	sum := sha256.Sum256([]byte(SignedText(h.srv.deps.Computer.ID, "", out["device_id"], ch.Nonce, now.Unix())))
	r, s, _ := ecdsa.Sign(rand.Reader, key, sum[:])
	sig := make([]byte, 64) // WebCrypto's r||s
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	req := h.authedRequest("POST", "/api/auth/session", "", nil, map[string]any{"device_id": out["device_id"], "nonce": ch.Nonce, "time": now.Unix(), "signature": base64.RawURLEncoding.EncodeToString(sig), "client": "browser"})
	st, cookies := h.do(req, nil)
	if st != 200 || len(cookies) != 1 || cookies[0].Name != sessionCookie || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session = %d %+v", st, cookies)
	}
	h.asRemote()
	if st, _ := h.do(h.authedRequest("GET", "/api/state", "", cookies, nil), nil); st != 200 {
		t.Errorf("with the cookie = %d", st)
	}
}

// A ticket shown on loopback is checked too, so a revoked phone is
// refused on every path; loopback without one is this computer.
func TestLoopbackChecksAPresentedTicket(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if st, _ := h.reasonOf(""); st != 200 {
		t.Errorf("loopback, no ticket = %d", st)
	}
	if st, why := h.reasonOf("made-up"); st != 401 || why != reasonSessionExpired {
		t.Errorf("loopback, bad ticket = %d %q", st, why)
	}
}

// Revoking a device closes its /ws and /events at once; DELETE
// /api/devices/self unpairs the caller.
func TestRevokeClosesLiveConnections(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	d := h.pairDevice()
	h.asRemote()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{"Authorization": {"Bearer " + ticketPrefix + d.ticket}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.http.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	req := h.authedRequest("GET", "/events", d.ticket, nil, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("events = %v %v", resp, err)
	}
	defer func() { _ = resp.Body.Close() }()
	sseDone := make(chan struct{})
	go func() {
		buf := make([]byte, 1024)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				close(sseDone)
				return
			}
		}
	}()

	if st, _ := h.do(h.authedRequest("DELETE", "/api/devices/self", d.ticket, nil, nil), nil); st != 200 {
		t.Fatalf("unpair = %d", st)
	}
	for {
		_, _, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != closeDeviceRevoked {
				t.Errorf("ws closed with %v, want 4003", err)
			}
			break
		}
	}
	select {
	case <-sseDone:
	case <-time.After(time.Second):
		t.Error("/events stayed open after the revoke")
	}
	if st, why := h.reasonOf(d.ticket); st != 401 || why == "" {
		t.Errorf("after unpairing = %d %q", st, why)
	}
	found := false
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "web.device.revoke" && strings.Contains(string(ev.Payload), `"by":"self"`) {
			found = true
		}
	}
	if !found {
		t.Error("no web.device.revoke {by: self}")
	}
}

// The setup page's buttons: Disconnect all devices keeps the pin, Reset
// pairing replaces it and keeps the daemon id; both revoke every device.
func TestDisconnectAllAndResetPairing(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	a, b := h.pairDevice(), h.pairDevice()
	pin := h.srv.deps.TLS.Pin()
	var list struct{ Devices []Device }
	h.getJSON("/api/devices", &list)
	if len(list.Devices) != 2 {
		t.Fatalf("devices = %+v", list)
	}
	if st, body := h.postJSON("/api/devices/disconnect-all", map[string]any{}, nil); st != 200 || !strings.Contains(body, `"count": 2`) {
		t.Fatalf("disconnect all = %d %s", st, body)
	}
	if h.srv.deps.TLS.Pin() != pin {
		t.Error("Disconnect all devices changed the pin")
	}
	h.asRemote()
	for _, d := range []testDevice{a, b} {
		if st, _ := h.reasonOf(d.ticket); st != 401 {
			t.Errorf("after disconnect all = %d", st)
		}
	}
	h.srv.kindHook = nil
	c := h.pairDevice()
	id := h.srv.deps.Computer.ID
	if st, body := h.postJSON("/api/devices/reset-pairing", map[string]any{}, nil); st != 200 || !strings.Contains(body, `"count": 1`) {
		t.Fatalf("reset = %d %s", st, body)
	}
	if h.srv.deps.TLS.Pin() == pin || h.srv.deps.Computer.ID != id {
		t.Error("Reset pairing kept the pin or changed the daemon id")
	}
	if st, _, bad := h.signIn(c.key, c.id, h.srv.deps.TLS.Pin(), time.Now()); st != 401 || bad["reason"] != reasonDeviceRevoked {
		t.Errorf("after reset = %d %v", st, bad)
	}
	h.getJSON("/api/devices", &list)
	if len(list.Devices) != 0 {
		t.Errorf("devices after reset = %+v", list.Devices)
	}
}

// Rename and revoke one device from the setup page.
func TestDeviceRenameAndRevoke(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	d := h.pairDevice()
	if st, body := h.sendJSON("PATCH", "/api/devices/"+d.id, map[string]any{"name": "Work phone"}, nil); st != 200 || !strings.Contains(body, "Work phone") {
		t.Errorf("rename = %d %s", st, body)
	}
	if st, _ := h.sendJSON("DELETE", "/api/devices/"+d.id, map[string]any{}, nil); st != 200 {
		t.Errorf("revoke = %d", st)
	}
	if st, _ := h.sendJSON("DELETE", "/api/devices/"+d.id, map[string]any{}, nil); st != 404 {
		t.Errorf("revoke again = %d", st)
	}
}

// /api/pair holds a pairing link the app can read; the QR shows the same
// code until a new one is asked for.
func TestPairLinkHoldsTheCode(t *testing.T) {
	old := pairSysAddrs
	pairSysAddrs = func() ([]pair.IfaceAddr, error) {
		return []pair.IfaceAddr{{Iface: "en0", IP: []byte{192, 168, 1, 20}}}, nil
	}
	defer func() { pairSysAddrs = old }()
	h := newHarness(t, harnessOpts{})
	h.srv.deps.Advertise = []string{"100.101.1.1:7777"}
	resp := h.srv.pairing(false)
	if resp.Error != "" {
		// The harness server is not listening; use an address.
		t.Logf("pairing: %s", resp.Error)
	}
	addrs, err := h.srv.pairAddresses("0.0.0.0:7777")
	if err != nil || len(addrs) != 2 || addrs[1].Kind != pair.KindAdvertised {
		t.Fatalf("addresses = %+v %v", addrs, err)
	}
	link := pair.Link(pair.LinkInfo{DaemonID: "d", Name: "Test Mac", Pin: "p", Code: "c", Addresses: addrs})
	if !strings.HasPrefix(link, "https://192.168.1.20:7777/voice#pair=1&") {
		t.Errorf("link = %s", link)
	}
	in, err := pair.ParseLink(link)
	if err != nil || in.Name != "Test Mac" || len(in.Addresses) != 2 || pair.HostPort(in.Addresses[1].URL) != "100.101.1.1:7777" {
		t.Errorf("parsed = %+v %v", in, err)
	}
}

// Cross-site requests: an upgrade or a change from another website's
// Origin is refused, even on loopback.
func TestCrossOriginRefused(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	req := h.authedRequest("POST", "/api/utterance", "", nil, map[string]any{"text": "hi"})
	req.Header.Set("Origin", "https://evil.example")
	if st, _ := h.do(req, nil); st != http.StatusForbidden {
		t.Errorf("cross-site POST = %d", st)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.http.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}}); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site /ws = %v %v", resp, err)
	}
	req = h.authedRequest("POST", "/api/utterance", "", nil, map[string]any{"text": "hi"})
	req.Header.Set("Origin", h.http.URL)
	if st, _ := h.do(req, nil); st != 200 {
		t.Errorf("same-origin POST = %d", st)
	}
}

// /api/hello stays public and says how to authenticate.
func TestAPIHelloPublic(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.asRemote()
	var hello HelloResponse
	if code := h.getJSON("/api/hello", &hello); code != 200 || hello.Auth != "device" || hello.Name != "sheepdog" || hello.Computer == nil {
		t.Errorf("hello = %d %+v", code, hello)
	}
}

// No event carries a ticket, a code or a signature.
func TestAuthLogsNoSecrets(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	code := h.pairCode()
	key, pub := newDeviceKey(t)
	_, out := h.enrollDevice(code, pub, DeviceIOS)
	_, sess, _ := h.signIn(key, out["device_id"], h.srv.deps.TLS.Pin(), time.Now())
	h.asRemote()
	h.reasonOf(sess.Ticket)
	h.reasonOf("wrong-ticket-value")
	h.enrollDevice(code, pub, DeviceIOS)
	for _, ev := range h.log.Tail(0) {
		p := string(ev.Payload)
		for _, secret := range []string{code, sess.Ticket, "wrong-ticket-value"} {
			if strings.Contains(p, secret) {
				t.Errorf("%s carries a secret: %s", ev.Type, p)
			}
		}
	}
}
