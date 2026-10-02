package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recLog records log events.
type recLog struct {
	mu     sync.Mutex
	events []recEvent
}

type recEvent struct {
	typ     string
	payload map[string]any
}

func (l *recLog) Log(_, typ, _ string, payload any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, _ := payload.(map[string]any)
	l.events = append(l.events, recEvent{typ, p})
}

func (l *recLog) of(typ string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, e := range l.events {
		if e.typ == typ {
			out = append(out, e.payload)
		}
	}
	return out
}

// apnsReq is one request the fake APNs saw.
type apnsReq struct {
	proto  int
	path   string
	header http.Header
	body   map[string]any
}

// fakeAPNs is an HTTP/2 TLS server answering with respond.
type fakeAPNs struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []apnsReq
	respond func(r *http.Request) (int, string)
}

func newFakeAPNs(t *testing.T) *fakeAPNs {
	t.Helper()
	f := &fakeAPNs{respond: func(*http.Request) (int, string) { return 200, "" }}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		f.mu.Lock()
		f.reqs = append(f.reqs, apnsReq{proto: r.ProtoMajor, path: r.URL.Path, header: r.Header.Clone(), body: body})
		status, reason := f.respond(r)
		f.mu.Unlock()
		w.Header().Set("apns-id", "test-id")
		w.WriteHeader(status)
		if reason != "" {
			_, _ = w.Write([]byte(`{"reason":"` + reason + `"}`))
		}
	}))
	f.srv.EnableHTTP2 = true
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPNs) requests() []apnsReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apnsReq(nil), f.reqs...)
}

// writeKey writes a fresh P-256 PKCS#8 key and returns its path and key.
func writeKey(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey_TEST.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, k
}

const (
	tokA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa123456"
	tokB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbabcdef"
)

func newTestSender(t *testing.T, f *fakeAPNs, dataDir string) (*Sender, *recLog, *ecdsa.PrivateKey) {
	t.Helper()
	path, key := writeKey(t)
	log := &recLog{}
	s, err := New(Config{KeyPath: path, KeyID: "KEYID12345", TeamID: "TEAM123456", Topic: "com.example.app", DataDir: dataDir,
		ProductionURL: f.srv.URL, SandboxURL: f.srv.URL + "/sandbox", Client: f.srv.Client()}, log)
	if err != nil {
		t.Fatal(err)
	}
	return s, log, key
}

func approval() Notification {
	return Notification{EventID: "evt-7", PendingID: "pa-3", PaneID: "w1:p2", Agent: "auth", Kind: KindApproval,
		Text: "auth is asking to run git push. Yes or no?", NeedsYou: true,
		Options: []Option{{Index: 1, Label: "Yes, proceed"}, {Index: 2, Label: "No"}}}
}

func TestNotifyPayloadHeadersAndJWT(t *testing.T) {
	f := newFakeAPNs(t)
	s, log, key := newTestSender(t, f, t.TempDir())
	if err := s.Register(strings.ToUpper(tokA), EnvProduction, "KC's iPhone"); err != nil {
		t.Fatal(err)
	}
	s.Notify(approval())
	s.Wait()
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.proto != 2 {
		t.Errorf("proto = HTTP/%d, want HTTP/2", r.proto)
	}
	if r.path != "/3/device/"+tokA {
		t.Errorf("path = %s (the token is lowercased)", r.path)
	}
	for h, want := range map[string]string{"apns-topic": "com.example.app", "apns-push-type": "alert", "apns-priority": "10", "apns-collapse-id": "pa-3"} {
		if got := r.header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	verifyJWT(t, strings.TrimPrefix(r.header.Get("authorization"), "bearer "), &key.PublicKey)

	aps := r.body["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	if alert["title"] != "auth" || alert["body"] != "auth is asking to run git push. Yes or no?" {
		t.Errorf("alert = %v", alert)
	}
	if aps["sound"] != "default" || aps["thread-id"] != "w1:p2" || aps["interruption-level"] != "time-sensitive" || aps["category"] != "APPROVAL" {
		t.Errorf("aps = %v", aps)
	}
	sd := r.body["sheepdog"].(map[string]any)
	if sd["kind"] != "approval" || sd["pane_id"] != "w1:p2" || sd["agent"] != "auth" || sd["pending_id"] != "pa-3" {
		t.Errorf("sheepdog = %v", sd)
	}
	opts := sd["options"].([]any)
	if len(opts) != 2 || opts[0].(map[string]any)["label"] != "Yes, proceed" || opts[0].(map[string]any)["index"] != float64(1) {
		t.Errorf("options = %v", opts)
	}
	sent := log.of("push.sent")
	if len(sent) != 1 || sent[0]["token_tail"] != "123456" {
		t.Errorf("push.sent = %v", sent)
	}
	for _, e := range log.events {
		b, _ := json.Marshal(e.payload)
		if strings.Contains(string(b), tokA) {
			t.Errorf("%s logged the full token: %s", e.typ, b)
		}
	}
}

// verifyJWT checks the token's header, claims and ES256 signature.
func verifyJWT(t *testing.T, tok string, pub *ecdsa.PublicKey) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	enc := base64.RawURLEncoding
	var h map[string]string
	var c map[string]any
	hb, _ := enc.DecodeString(parts[0])
	cb, _ := enc.DecodeString(parts[1])
	_ = json.Unmarshal(hb, &h)
	_ = json.Unmarshal(cb, &c)
	if h["alg"] != "ES256" || h["kid"] != "KEYID12345" {
		t.Errorf("header = %v", h)
	}
	if c["iss"] != "TEAM123456" || c["iat"] == nil {
		t.Errorf("claims = %v", c)
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature: %d bytes, %v", len(sig), err)
	}
	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, d[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("signature does not verify with the public key")
	}
}

func TestPromisePayloadIsActiveAtPriority5(t *testing.T) {
	f := newFakeAPNs(t)
	s, _, _ := newTestSender(t, f, "")
	_ = s.Register(tokA, EnvSandbox, "")
	s.Notify(Notification{EventID: "evt-9", PaneID: "w1:p1", Kind: KindDone, Text: "As you asked: auth finished."})
	s.Wait()
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests", len(reqs))
	}
	r := reqs[0]
	if r.path != "/sandbox/3/device/"+tokA {
		t.Errorf("sandbox device went to %s", r.path)
	}
	if r.header.Get("apns-priority") != "5" || r.header.Get("apns-collapse-id") != "evt-9" {
		t.Errorf("headers = %v", r.header)
	}
	aps := r.body["aps"].(map[string]any)
	if aps["category"] != "AGENT" || aps["interruption-level"] != "active" || aps["alert"].(map[string]any)["title"] != "Sheepdog" {
		t.Errorf("aps = %v", aps)
	}
	if _, ok := r.body["sheepdog"].(map[string]any)["options"]; ok {
		t.Error("options on a non-approval push")
	}
}

func TestDedupeByEventID(t *testing.T) {
	f := newFakeAPNs(t)
	s, _, _ := newTestSender(t, f, "")
	_ = s.Register(tokA, EnvProduction, "")
	s.Notify(approval())
	s.Notify(approval())
	n := approval()
	n.EventID, n.PendingID = "evt-8", "pa-4"
	s.Notify(n)
	s.Wait()
	if got := len(f.requests()); got != 2 {
		t.Errorf("got %d requests, want 2 (one per event)", got)
	}
}

func TestGoneAndBadTokenUnregister(t *testing.T) {
	f := newFakeAPNs(t)
	dir := t.TempDir()
	s, log, _ := newTestSender(t, f, dir)
	_ = s.Register(tokA, EnvProduction, "")
	_ = s.Register(tokB, EnvProduction, "")
	f.respond = func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, tokA) {
			return 410, "Unregistered"
		}
		return 400, "BadDeviceToken"
	}
	s.Notify(approval())
	s.Wait()
	if st := s.Status(); st.Devices != 0 {
		t.Errorf("devices = %d after 410 and BadDeviceToken, want 0", st.Devices)
	}
	failed := log.of("push.failed")
	if len(failed) != 2 {
		t.Fatalf("push.failed = %v", failed)
	}
	reasons := map[any]bool{failed[0]["reason"]: true, failed[1]["reason"]: true}
	if !reasons["Unregistered"] || !reasons["BadDeviceToken"] {
		t.Errorf("reasons = %v", reasons)
	}
	// Persisted: a new Sender over the same data dir sees no devices.
	s2, _, _ := newTestSender(t, f, dir)
	if n := s2.Status().Devices; n != 0 {
		t.Errorf("reloaded devices = %d", n)
	}
}

func TestExpiredProviderTokenRetriesOnce(t *testing.T) {
	f := newFakeAPNs(t)
	s, log, _ := newTestSender(t, f, "")
	_ = s.Register(tokA, EnvProduction, "")
	var calls int
	f.respond = func(*http.Request) (int, string) {
		calls++
		if calls == 1 {
			return 403, "ExpiredProviderToken"
		}
		return 200, ""
	}
	s.Notify(approval())
	s.Wait()
	if len(f.requests()) != 2 || len(log.of("push.sent")) != 1 {
		t.Errorf("requests = %d, sent = %v", len(f.requests()), log.of("push.sent"))
	}
}

func TestTokenCachedAndRefreshed(t *testing.T) {
	f := newFakeAPNs(t)
	s, _, _ := newTestSender(t, f, "")
	now := time.Unix(1_800_000_000, 0)
	s.cfg.Now = func() time.Time { return now }
	a, _ := s.token(false)
	now = now.Add(49 * time.Minute)
	b, _ := s.token(false)
	now = now.Add(2 * time.Minute)
	c, _ := s.token(false)
	if a != b || b == c {
		t.Errorf("token reuse wrong: same within 50 min %v, refreshed after %v", a == b, b != c)
	}
}

func TestDisabledWhenUnconfigured(t *testing.T) {
	f := newFakeAPNs(t)
	log := &recLog{}
	s, err := New(Config{KeyID: "K", TeamID: "T", Topic: "x", ProductionURL: f.srv.URL, Client: f.srv.Client()}, log)
	if err != nil {
		t.Fatal(err)
	}
	s.LogStartup()
	if err := s.Register(tokA, EnvProduction, ""); err != nil {
		t.Fatalf("register while disabled: %v", err)
	}
	s.Notify(approval())
	s.Wait()
	if len(f.requests()) != 0 {
		t.Error("a disabled sender sent")
	}
	st := s.Status()
	if st.Enabled || !strings.Contains(st.Reason, "key_path") || st.Devices != 1 {
		t.Errorf("status = %+v", st)
	}
	if d := log.of("push.disabled"); len(d) != 1 {
		t.Errorf("push.disabled = %v", d)
	}
	// A missing key file disables too, with the reason.
	s2, _ := New(Config{KeyPath: filepath.Join(t.TempDir(), "none.p8"), KeyID: "K", TeamID: "T", Topic: "x"}, nil)
	if s2.Enabled() || !strings.Contains(s2.Status().Reason, "cannot read") {
		t.Errorf("missing key: %+v", s2.Status())
	}
	var nilSender *Sender
	nilSender.Notify(approval())
	if nilSender.Status().Enabled {
		t.Error("nil sender enabled")
	}
}

func TestRegisterValidation(t *testing.T) {
	s, _ := New(Config{}, nil)
	if err := s.Register("xyz", EnvProduction, ""); err != ErrBadToken {
		t.Errorf("short token: %v", err)
	}
	if err := s.Register(strings.Repeat("zz", 32), EnvProduction, ""); err != ErrBadToken {
		t.Errorf("non-hex token: %v", err)
	}
	if err := s.Register(tokA, "dev", ""); err != ErrBadEnvironment {
		t.Errorf("bad environment: %v", err)
	}
	_ = s.Register(tokA, EnvProduction, "a")
	_ = s.Register(tokA, EnvSandbox, "b")
	if d := s.Devices(); len(d) != 1 || d[0].Environment != EnvSandbox || d[0].Name != "b" {
		t.Errorf("re-register = %+v", d)
	}
	if err := s.Unregister(tokA); err != nil || s.Status().Devices != 0 {
		t.Errorf("unregister: %v, %d", err, s.Status().Devices)
	}
	if err := s.Unregister(tokB); err != nil {
		t.Errorf("unknown token: %v", err)
	}
}
