package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// decryptRFC8291 is the browser side of RFC 8291 (aes128gcm) so tests prove
// byte-for-byte interoperability with a standard Web Push UA.
func decryptRFC8291(t *testing.T, uaPriv *ecdh.PrivateKey, authSecret []byte, body []byte) []byte {
	t.Helper()
	if len(body) < 21+65+17 {
		t.Fatalf("aes128gcm body too short: %d bytes", len(body))
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	if rs != 4096 {
		t.Fatalf("record size = %d, want 4096", rs)
	}
	idLen := int(body[20])
	if idLen != 65 {
		t.Fatalf("keyid len = %d, want 65", idLen)
	}
	asPubBytes := body[21 : 21+idLen]
	ciphertext := body[21+idLen:]

	asPub, err := ecdh.P256().NewPublicKey(asPubBytes)
	if err != nil {
		t.Fatalf("server ephemeral public key: %v", err)
	}
	shared, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	uaPubBytes := uaPriv.PublicKey().Bytes()

	keyInfo := append([]byte("WebPush: info\x00"), uaPubBytes...)
	keyInfo = append(keyInfo, asPubBytes...)
	prkKey, err := hkdf.Extract(sha256.New, shared, authSecret)
	if err != nil {
		t.Fatal(err)
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	if err != nil {
		t.Fatal(err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		t.Fatal(err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		t.Fatal(err)
	}
	padded, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("AES-128-GCM open: %v", err)
	}
	// Strip trailing 0x00 padding back to the 0x02 final-record delimiter.
	i := len(padded) - 1
	for i >= 0 && padded[i] == 0 {
		i--
	}
	if i < 0 || padded[i] != 0x02 {
		t.Fatalf("missing 0x02 delimiter in decrypted record: %x", padded)
	}
	return padded[:i]
}

func newBrowserKeys(t *testing.T) (WebPushKeys, *ecdh.PrivateKey, []byte) {
	t.Helper()
	// Use crypto/rand via ecdsa -> ECDH so key generation is deterministic-free & standard.
	ec, err := ecdsa.GenerateKey(elliptic.P256(), randReader(t))
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ec.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	auth := []byte("0123456789abcdef") // 16 bytes
	enc := base64.RawURLEncoding
	return WebPushKeys{
		P256DH: enc.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   enc.EncodeToString(auth),
	}, priv, auth
}

func randReader(t *testing.T) io.Reader {
	t.Helper()
	f, err := os.Open("/dev/urandom")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestVAPIDKeyPersistenceAndModeOff(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(Config{DataDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st1 := s1.Status()
	if !st1.WebPushEnabled || st1.VAPIDPublicKey == "" {
		t.Fatalf("status 1 = %+v, want web_push_enabled with vapid_public_key", st1)
	}
	rawPub, err := base64.RawURLEncoding.DecodeString(st1.VAPIDPublicKey)
	if err != nil || len(rawPub) != 65 || rawPub[0] != 0x04 {
		t.Fatalf("vapid_public_key = %q (%d bytes, err=%v), want 65-byte uncompressed P-256 point", st1.VAPIDPublicKey, len(rawPub), err)
	}
	info, err := os.Stat(filepath.Join(dir, VAPIDFile))
	if err != nil {
		t.Fatalf("stat %s: %v", VAPIDFile, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("%s perm = %o, want 0600", VAPIDFile, mode)
	}

	// Restarting in the same DataDir reuses the exact VAPID keypair.
	s2, err := New(Config{DataDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s2.VAPIDPublicKey() != st1.VAPIDPublicKey {
		t.Errorf("restarted vapid key = %q, want %q", s2.VAPIDPublicKey(), st1.VAPIDPublicKey)
	}

	// push.mode = "off" disables Web Push too.
	sOff, err := New(Config{DataDir: dir, Mode: ModeOff}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sOff.WebPushEnabled() || sOff.Status().WebPushEnabled {
		t.Errorf("mode=off: WebPushEnabled = true, want false")
	}
}

func TestWebPushEndToEndVAPIDAndCleanup(t *testing.T) {
	type webReq struct {
		path   string
		header http.Header
		body   []byte
	}
	var (
		mu         sync.Mutex
		reqs       []webReq
		nextStatus = http.StatusCreated
	)
	pushSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, webReq{path: r.URL.Path, header: r.Header.Clone(), body: b})
		code := nextStatus
		mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(pushSrv.Close)

	now := time.Date(2026, 11, 4, 19, 0, 0, 0, time.UTC)
	log := &recLog{}
	// Unconfigured APNs (the Linux / self-hosted default): APNs is disabled,
	// yet Web Push works out of the box using the local VAPID key.
	s, err := New(Config{
		DataDir:    t.TempDir(),
		ComputerID: "comp-123",
		Client:     pushSrv.Client(),
		Now:        func() time.Time { return now },
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled() {
		t.Fatalf("APNs Enabled() = true with empty APNs config")
	}
	if !s.WebPushEnabled() {
		t.Fatalf("WebPushEnabled() = false")
	}

	keys, uaPriv, authSecret := newBrowserKeys(t)
	secretEndpoint := pushSrv.URL + "/wpush/v2/SUPER_SECRET_CAPABILITY_TOKEN_987654"
	if err := s.RegisterWebPush(WebSubscription{
		Endpoint: secretEndpoint,
		Keys:     keys,
		Name:     "Android · Chrome",
		DeviceID: "dev-android",
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.Status().WebDevices; got != 1 {
		t.Fatalf("WebDevices = %d, want 1", got)
	}

	s.Notify(Notification{
		EventID:   "evt-42",
		PendingID: "ap:tm9bu0:1",
		PaneID:    "w5:p1",
		Agent:     "auth",
		Kind:      KindApproval,
		Text:      "auth asks to run git push. Yes or no?",
		NeedsYou:  true,
		Options:   []Option{{Index: 1, Label: "Yes"}, {Index: 2, Label: "No"}},
	})
	s.Wait()

	mu.Lock()
	if len(reqs) != 1 {
		mu.Unlock()
		t.Fatalf("push requests = %d, want 1", len(reqs))
	}
	got := reqs[0]
	mu.Unlock()

	if got.header.Get("Content-Encoding") != "aes128gcm" || got.header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("content headers = %v", got.header)
	}
	if got.header.Get("Urgency") != "high" {
		t.Errorf("Urgency = %q, want high", got.header.Get("Urgency"))
	}
	// RFC 8030 §5.4: Topic must be <= 32 base64url characters (no colons!).
	topic := got.header.Get("Topic")
	if topic == "" || len(topic) > 32 || strings.ContainsAny(topic, ":+/=") {
		t.Errorf("Topic = %q (len %d), want <=32 URL-safe base64 chars", topic, len(topic))
	}

	// Verify RFC 8292 Authorization: vapid t=<jwt>, k=<pub>.
	authHdr := got.header.Get("Authorization")
	if !strings.HasPrefix(authHdr, "vapid t=") {
		t.Fatalf("Authorization = %q", authHdr)
	}
	parts := strings.Split(strings.TrimPrefix(authHdr, "vapid "), ",")
	if len(parts) != 2 {
		t.Fatalf("vapid parts = %v", parts)
	}
	jwtStr := strings.TrimPrefix(strings.TrimSpace(parts[0]), "t=")
	kStr := strings.TrimPrefix(strings.TrimSpace(parts[1]), "k=")
	if kStr != s.VAPIDPublicKey() {
		t.Errorf("vapid k = %q, want %q", kStr, s.VAPIDPublicKey())
	}
	verifyVAPIDJWT(t, jwtStr, kStr, pushSrv.URL, now)

	// Decrypt the RFC 8291 body with the browser's private key and auth secret.
	plain := decryptRFC8291(t, uaPriv, authSecret, got.body)
	var payload struct {
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Tag       string   `json:"tag"`
		Kind      string   `json:"kind"`
		PaneID    string   `json:"pane_id"`
		Agent     string   `json:"agent"`
		PendingID string   `json:"pending_id"`
		NeedsYou  bool     `json:"needs_you"`
		Options   []Option `json:"options"`
		Computer  string   `json:"computer"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("unmarshal decrypted payload %q: %v", plain, err)
	}
	if payload.Title != "auth" || payload.Body != "auth asks to run git push. Yes or no?" || payload.PendingID != "ap:tm9bu0:1" || payload.Computer != "comp-123" || len(payload.Options) != 2 || !payload.NeedsYou {
		t.Errorf("decrypted payload = %+v", payload)
	}

	// Logs must never leak the full capability endpoint URL or keys.
	logBytes, _ := json.Marshal(log.events)
	if strings.Contains(string(logBytes), "SUPER_SECRET_CAPABILITY_TOKEN") || strings.Contains(string(logBytes), keys.Auth) {
		t.Errorf("secret leaked into event log: %s", logBytes)
	}
	if len(log.of("push.sent")) != 1 {
		t.Errorf("push.sent events = %v, want 1", log.of("push.sent"))
	}

	// 410 Gone drops the subscription automatically.
	mu.Lock()
	nextStatus = http.StatusGone
	mu.Unlock()
	s.Notify(Notification{EventID: "evt-43", Kind: KindDone, Text: "auth finished."})
	s.Wait()
	if got := s.Status().WebDevices; got != 0 {
		t.Errorf("WebDevices after 410 = %d, want 0", got)
	}
}

func verifyVAPIDJWT(t *testing.T, jwtStr, pubB64, wantOrigin string, now time.Time) {
	t.Helper()
	enc := base64.RawURLEncoding
	segs := strings.Split(jwtStr, ".")
	if len(segs) != 3 {
		t.Fatalf("jwt segments = %d", len(segs))
	}
	pubBytes, err := enc.DecodeString(pubB64)
	if err != nil {
		t.Fatalf("bad pub key %q: %v", pubB64, err)
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pubBytes)
	if err != nil {
		t.Fatalf("bad uncompressed P-256 pub key: %v", err)
	}
	sig, err := enc.DecodeString(segs[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("bad jwt sig: %v", err)
	}
	digest := sha256.Sum256([]byte(segs[0] + "." + segs[1]))
	r := new(big.Int).SetBytes(sig[:32])
	sv := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, sv) {
		t.Fatalf("VAPID JWT signature invalid")
	}
	claimsBytes, err := enc.DecodeString(segs[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(wantOrigin)
	if claims.Aud != u.Scheme+"://"+u.Host {
		t.Errorf("aud = %q, want %q", claims.Aud, u.Scheme+"://"+u.Host)
	}
	if claims.Exp <= now.Unix() || claims.Exp > now.Add(24*time.Hour).Unix() {
		t.Errorf("exp = %d, now = %d", claims.Exp, now.Unix())
	}
	if !strings.HasPrefix(claims.Sub, "https://") && !strings.HasPrefix(claims.Sub, "mailto:") {
		t.Errorf("sub = %q, want https: or mailto:", claims.Sub)
	}
}

func TestWebPushDeviceRevocationAndValidation(t *testing.T) {
	// Default sender (no custom Client) rejects private/loopback endpoints (SSRF guard).
	sProd, err := New(Config{DataDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys, _, _ := newBrowserKeys(t)
	for _, badURL := range []string{
		"http://fcm.googleapis.com/fcm/send/abc",
		"https://127.0.0.1:8443/push",
		"https://192.168.1.1/admin",
		"https://10.0.0.5/hook",
		"https://169.254.169.254/latest/meta-data",
		"https://localhost/push",
	} {
		if err := sProd.RegisterWebPush(WebSubscription{Endpoint: badURL, Keys: keys, DeviceID: "dev-1"}); err == nil {
			t.Errorf("accepted unsafe endpoint %q", badURL)
		}
	}
	// Bad crypto keys are rejected.
	if err := sProd.RegisterWebPush(WebSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/abc",
		Keys:     WebPushKeys{P256DH: "not-a-point", Auth: keys.Auth},
	}); err == nil {
		t.Errorf("accepted invalid p256dh key")
	}
	if err := sProd.RegisterWebPush(WebSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/abc",
		Keys:     WebPushKeys{P256DH: keys.P256DH, Auth: "short"},
	}); err == nil {
		t.Errorf("accepted short auth secret")
	}

	// Paired device replacement, ExceptDevice skip, and ForgetDevices cleanup.
	if err := sProd.RegisterWebPush(WebSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/old-sub",
		Keys:     keys,
		DeviceID: "dev-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sProd.RegisterWebPush(WebSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/new-sub",
		Keys:     keys,
		DeviceID: "dev-1",
	}); err != nil {
		t.Fatal(err)
	}
	if len(sProd.WebDevices()) != 1 || sProd.WebDevices()[0].Endpoint != "https://fcm.googleapis.com/fcm/send/new-sub" {
		t.Fatalf("WebDevices after re-register = %+v, want single replaced sub", sProd.WebDevices())
	}
	if err := sProd.ForgetDevices([]string{"dev-1"}); err != nil {
		t.Fatal(err)
	}
	if len(sProd.WebDevices()) != 0 {
		t.Errorf("WebDevices after ForgetDevices = %+v, want empty", sProd.WebDevices())
	}
}
