package push

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// fakeRelay is the relay's API: /v1/register hands out "rt-<n>" tokens,
// /v1/send answers from sendStatus (in order; 200 when exhausted).
type fakeRelay struct {
	srv        *httptest.Server
	mu         sync.Mutex
	registers  []map[string]string
	sends      []relaySend
	sendStatus []int
	activities []activityBody
	actStatus  []int
	minted     int
}

const relayTokPrefix = "rt-relaytoken-0000000000-"

func newFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	f := &fakeRelay{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/v1/register":
			var m map[string]string
			_ = json.Unmarshal(b, &m)
			f.registers = append(f.registers, m)
			f.minted++
			_ = json.NewEncoder(w).Encode(map[string]string{"relay_token": relayTokPrefix + string(rune('a'+f.minted))})
		case "/v1/send":
			var s relaySend
			_ = json.Unmarshal(b, &s)
			f.sends = append(f.sends, s)
			status := 200
			if len(f.sendStatus) > 0 {
				status, f.sendStatus = f.sendStatus[0], f.sendStatus[1:]
			}
			w.WriteHeader(status)
			switch status {
			case 200:
				_, _ = w.Write([]byte(`{"ok":true}`))
			case 410:
				_, _ = w.Write([]byte(`{"error":"device token is no longer valid","reason":"Unregistered"}`))
			case 401:
				_, _ = w.Write([]byte(`{"error":"bad relay_token"}`))
			default:
				_, _ = w.Write([]byte(`{"error":"apns refused","status":429,"reason":"TooManyRequests"}`))
			}
		case "/v1/activity":
			var a activityBody
			_ = json.Unmarshal(b, &a)
			f.activities = append(f.activities, a)
			status := 200
			if len(f.actStatus) > 0 {
				status, f.actStatus = f.actStatus[0], f.actStatus[1:]
			}
			w.WriteHeader(status)
			if status == 410 {
				_, _ = w.Write([]byte(`{"error":"device token is no longer valid","reason":"Unregistered"}`))
			} else {
				_, _ = w.Write([]byte(`{"ok":true}`))
			}
		case "/v1/health":
			_, _ = w.Write([]byte(`{"ok":true,"version":"1.0.0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newRelaySender(t *testing.T, f *fakeRelay, dataDir string) (*Sender, *recLog) {
	t.Helper()
	log := &recLog{}
	s, err := New(Config{Mode: ModeRelay, RelayURL: f.srv.URL + "/", DataDir: dataDir, Client: f.srv.Client()}, log)
	if err != nil {
		t.Fatal(err)
	}
	return s, log
}

// The relay path: the daemon gets a relay token for the device token,
// seals the same {aps, sheepdog} payload the direct path sends with the
// phone's content key, and the relay sees only the ciphertext, the kind
// and the collapse id.
func TestRelaySendsSealedPayload(t *testing.T) {
	f := newFakeRelay(t)
	dir := t.TempDir()
	s, log := newRelaySender(t, f, dir)
	if !s.Enabled() || s.Status().Mode != ModeRelay {
		t.Fatalf("status = %+v", s.Status())
	}
	dev, err := s.RegisterDevice(Registration{Token: tokA, Environment: EnvSandbox, Name: "iPhone"})
	if err != nil {
		t.Fatal(err)
	}
	n := approval()
	s.Notify(n)
	s.Wait()
	if len(f.registers) != 1 || f.registers[0]["device_token"] != tokA || f.registers[0]["environment"] != EnvSandbox || f.registers[0]["bundle_id"] != DefaultBundleID {
		t.Fatalf("registers = %v", f.registers)
	}
	if len(f.sends) != 1 {
		t.Fatalf("sends = %d", len(f.sends))
	}
	got := f.sends[0]
	if got.RelayToken != relayTokPrefix+"b" || got.Payload.Kind != KindApproval || got.Payload.CollapseID != "pa-3" || got.Priority != 10 {
		t.Errorf("send = %+v", got)
	}
	ct, _ := base64.StdEncoding.DecodeString(got.Payload.Ciphertext)
	nonce, _ := base64.StdEncoding.DecodeString(got.Payload.Nonce)
	plain, err := Open(dev.ContentKey, ct, nonce)
	if err != nil {
		t.Fatalf("open with the phone's key: %v", err)
	}
	want, _ := json.Marshal(n.Payload())
	if string(plain) != string(want) {
		t.Errorf("plaintext = %s, want %s", plain, want)
	}
	if strings.Contains(got.Payload.Ciphertext, "git push") {
		t.Error("ciphertext carries the text")
	}
	if len(log.of("push.sent")) != 1 {
		t.Errorf("push.sent = %v", log.of("push.sent"))
	}
	// The relay token is kept: a second event does not register again,
	// and a restart reads it back.
	n2 := approval()
	n2.EventID, n2.PendingID = "evt-8", "pa-4"
	s.Notify(n2)
	s.Wait()
	if len(f.registers) != 1 || len(f.sends) != 2 {
		t.Errorf("after a second event: registers=%d sends=%d", len(f.registers), len(f.sends))
	}
	s2, _ := newRelaySender(t, f, dir)
	if d := s2.Devices(); len(d) != 1 || d[0].RelayToken != relayTokPrefix+"b" || d[0].ContentKey != dev.ContentKey {
		t.Errorf("reloaded = %+v", d)
	}
	// The log carries only the token's tail.
	for _, e := range log.events {
		b, _ := json.Marshal(e.payload)
		if strings.Contains(string(b), tokA) || strings.Contains(string(b), dev.ContentKey) {
			t.Errorf("%s logs a whole token or key: %s", e.typ, b)
		}
	}
}

// A 410 from the relay (APNs: the device is gone) drops the device.
func TestRelayGoneDropsDevice(t *testing.T) {
	f := newFakeRelay(t)
	f.sendStatus = []int{410}
	s, log := newRelaySender(t, f, "")
	_ = s.Register(tokA, EnvProduction, "")
	s.Notify(approval())
	s.Wait()
	if s.Status().Devices != 0 {
		t.Errorf("devices = %d, want 0", s.Status().Devices)
	}
	if u := log.of("push.unregistered"); len(u) != 1 || u[0]["reason"] != "Unregistered" {
		t.Errorf("push.unregistered = %v", u)
	}
}

// Another relay failure is logged and keeps the device.
func TestRelayFailureKeepsDevice(t *testing.T) {
	f := newFakeRelay(t)
	f.sendStatus = []int{502}
	s, log := newRelaySender(t, f, "")
	_ = s.Register(tokA, EnvProduction, "")
	s.Notify(approval())
	s.Wait()
	if s.Status().Devices != 1 {
		t.Error("device dropped on a 502")
	}
	if fl := log.of("push.failed"); len(fl) != 1 || fl[0]["status"] != 502 || !strings.Contains(fl[0]["reason"].(string), "TooManyRequests") {
		t.Errorf("push.failed = %v", fl)
	}
}

// A relay token the relay refuses (401) is renewed once from the device
// token; one sent by the app alone cannot be, so that device is dropped.
func TestRelayUnauthorizedRenewsOrDrops(t *testing.T) {
	f := newFakeRelay(t)
	f.sendStatus = []int{401, 200}
	s, _ := newRelaySender(t, f, "")
	if _, err := s.RegisterDevice(Registration{Token: tokA, RelayToken: "stale-relay-token-from-elsewhere", Environment: EnvProduction}); err != nil {
		t.Fatal(err)
	}
	s.Notify(approval())
	s.Wait()
	if len(f.registers) != 1 || len(f.sends) != 2 || f.sends[0].RelayToken != "stale-relay-token-from-elsewhere" || f.sends[1].RelayToken != relayTokPrefix+"b" {
		t.Errorf("registers=%v sends=%+v", f.registers, f.sends)
	}
	if d := s.Devices(); len(d) != 1 || d[0].RelayToken != relayTokPrefix+"b" {
		t.Errorf("devices = %+v", d)
	}

	f2 := newFakeRelay(t)
	f2.sendStatus = []int{401}
	s2, _ := newRelaySender(t, f2, "")
	if _, err := s2.RegisterDevice(Registration{RelayToken: "relay-only-token-0000"}); err != nil {
		t.Fatal(err)
	}
	s2.Notify(approval())
	s2.Wait()
	if len(f2.registers) != 0 || s2.Status().Devices != 0 {
		t.Errorf("relay-only 401: registers=%d devices=%d", len(f2.registers), s2.Status().Devices)
	}
}

// The content key: 32 random bytes per phone, the same on every
// re-registration (the app re-registers on each launch), new for a new
// device; a relay-only registration works and unregisters by its token.
func TestContentKeyPerDevice(t *testing.T) {
	f := newFakeRelay(t)
	s, _ := newRelaySender(t, f, t.TempDir())
	a1, err := s.RegisterDevice(Registration{Token: tokA, Environment: EnvProduction})
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := s.RegisterDevice(Registration{Token: strings.ToUpper(tokA), Environment: EnvProduction, Name: "renamed"})
	b, _ := s.RegisterDevice(Registration{Token: tokB, Environment: EnvProduction})
	k, err := base64.StdEncoding.DecodeString(a1.ContentKey)
	if err != nil || len(k) != ContentKeySize {
		t.Fatalf("content key = %q", a1.ContentKey)
	}
	if a2.ContentKey != a1.ContentKey || a2.Name != "renamed" {
		t.Errorf("re-registration changed the key or kept the old name: %+v", a2)
	}
	if b.ContentKey == a1.ContentKey {
		t.Error("two phones share a key")
	}
	r, err := s.RegisterDevice(Registration{RelayToken: "relay-only-token-0000"})
	if err != nil || r.ContentKey == "" || r.Token != "" {
		t.Fatalf("relay-only = %+v, %v", r, err)
	}
	if s.Status().Devices != 3 {
		t.Errorf("devices = %d", s.Status().Devices)
	}
	if err := s.Unregister("relay-only-token-0000"); err != nil || s.Status().Devices != 2 {
		t.Errorf("unregister relay token: %v, devices=%d", err, s.Status().Devices)
	}
	for _, bad := range []Registration{{RelayToken: "has spaces in it!!"}, {Token: tokA, Environment: "dev"}, {RelayToken: "relay-only-token-0000", Environment: "dev"}, {}} {
		if _, err := s.RegisterDevice(bad); err == nil {
			t.Errorf("RegisterDevice(%+v) accepted", bad)
		}
	}
}

// Mode selection inside the sender: off, a relay URL that is still the
// placeholder or empty, and the default (apns) when Mode is unset.
func TestSenderModes(t *testing.T) {
	cases := []struct {
		cfg     Config
		enabled bool
		mode    string
		reason  string
	}{
		{Config{Mode: ModeOff}, false, ModeOff, "off"},
		{Config{Mode: ModeRelay, RelayURL: "https://sheepdog-relay.REPLACE.workers.dev"}, false, ModeRelay, "not deployed"},
		{Config{Mode: ModeRelay}, false, ModeRelay, "empty"},
		{Config{Mode: ModeRelay, RelayURL: "https://relay.example"}, true, ModeRelay, ""},
		{Config{}, false, ModeAPNs, "push.apns not configured"},
	}
	for _, c := range cases {
		s, err := New(c.cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		st := s.Status()
		if st.Enabled != c.enabled || st.Mode != c.mode || !strings.Contains(st.Reason, c.reason) {
			t.Errorf("%+v: status = %+v", c.cfg, st)
		}
	}
	s, _ := New(Config{Mode: ModeRelay, RelayURL: "https://relay.example", Topic: "com.example.own"}, nil)
	if s.cfg.BundleID != "com.example.own" {
		t.Errorf("bundle id = %q, want the apns topic", s.cfg.BundleID)
	}
}

func TestSealOpen(t *testing.T) {
	k, _ := NewContentKey()
	ct, nonce, err := Seal(k, []byte("hello"))
	if err != nil || len(nonce) != 12 || len(ct) != len("hello")+16 {
		t.Fatalf("seal: %v nonce=%d ct=%d", err, len(nonce), len(ct))
	}
	if p, err := Open(k, ct, nonce); err != nil || string(p) != "hello" {
		t.Errorf("open = %q, %v", p, err)
	}
	other, _ := NewContentKey()
	if _, err := Open(other, ct, nonce); err == nil {
		t.Error("opened with another key")
	}
	ct[0] ^= 1
	if _, err := Open(k, ct, nonce); err == nil {
		t.Error("opened a tampered ciphertext")
	}
	if _, _, err := Seal("short", nil); err != ErrBadContentKey {
		t.Errorf("bad key: %v", err)
	}
}

// A sentence too long for the relay is shortened so the sealed payload
// fits, on a rune boundary.
func TestSealedPayloadFits(t *testing.T) {
	n := approval()
	n.Text = strings.Repeat("näive ✓ ", 600)
	b, err := n.SealedPayload()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxPlaintext || !utf8.Valid(b) {
		t.Fatalf("len=%d valid=%v", len(b), utf8.Valid(b))
	}
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	body := p["aps"].(map[string]any)["alert"].(map[string]any)["body"].(string)
	if !strings.HasSuffix(body, "…") || p["sheepdog"].(map[string]any)["pending_id"] != "pa-3" {
		t.Errorf("payload = %s", b)
	}
	// Base64 of the sealed bytes stays under the relay's cap.
	if enc := base64.StdEncoding.EncodedLen(len(b) + 16); enc > 3500 {
		t.Errorf("ciphertext would be %d characters", enc)
	}
}

func TestHealth(t *testing.T) {
	f := newFakeRelay(t)
	if v, err := Health(context.Background(), f.srv.Client(), f.srv.URL+"/"); err != nil || v != "1.0.0" {
		t.Errorf("health = %q, %v", v, err)
	}
	if _, err := Health(context.Background(), f.srv.Client(), "https://sheepdog-relay.REPLACE.workers.dev"); err == nil || !strings.Contains(err.Error(), "not deployed") {
		t.Errorf("placeholder: %v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":false,"version":"1.0.0"}`)) }))
	defer bad.Close()
	if _, err := Health(context.Background(), bad.Client(), bad.URL); err == nil {
		t.Error("ok:false passed")
	}
	nf := httptest.NewServer(http.NotFoundHandler())
	defer nf.Close()
	if _, err := Health(context.Background(), nf.Client(), nf.URL); err == nil {
		t.Error("404 passed")
	}
}
