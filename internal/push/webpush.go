package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// VAPIDFile holds the daemon's auto-generated P-256 VAPID keypair under
	// the data directory (mode 0600).
	VAPIDFile = "vapid_key.json"
	// WebDevicesFile holds registered browser Web Push subscriptions under
	// the data directory (mode 0600).
	WebDevicesFile = "web_push_devices.json"

	defaultVAPIDSubject = "https://github.com/kccarlos/herdr-sheepdog"
	maxWebDevices       = 64
	maxWebPlainBytes    = 3000
	webPushRecordSize   = 4096
)

// Errors returned by RegisterWebPush.
var (
	ErrBadSubscription = errors.New("push subscription needs an https endpoint and valid p256dh and auth keys")
	ErrPrivateEndpoint = errors.New("push endpoint must be a public https host")
)

// WebPushKeys are the browser's RFC 8291 encryption keys from
// PushSubscription.toJSON().keys.
type WebPushKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// WebSubscription is one registered browser Web Push subscription.
type WebSubscription struct {
	Endpoint     string      `json:"endpoint"`
	Keys         WebPushKeys `json:"keys"`
	Name         string      `json:"device,omitempty"`
	DeviceID     string      `json:"device_id,omitempty"`
	RegisteredAt time.Time   `json:"registered_at"`
}

func (w WebSubscription) host() string {
	u, err := url.Parse(w.Endpoint)
	if err != nil {
		return ""
	}
	return u.Host
}

func (w WebSubscription) origin() string {
	u, err := url.Parse(w.Endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (w WebSubscription) tail() string {
	return tokenTail(w.Endpoint)
}

// vapidKey holds the daemon's P-256 ES256 keypair for RFC 8292 VAPID.
type vapidKey struct {
	priv *ecdsa.PrivateKey
	pub  string // base64url of 65-byte uncompressed P-256 point
}

type storedVAPID struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"` // base64url PKCS#8 DER
}

func loadOrCreateVAPID(path string) (*vapidKey, error) {
	enc := base64.RawURLEncoding
	if path != "" {
		b, err := os.ReadFile(path)
		if err == nil {
			var sv storedVAPID
			if err := json.Unmarshal(b, &sv); err != nil {
				return nil, fmt.Errorf("push: parse %s: %w", path, err)
			}
			der, err := enc.DecodeString(sv.PrivateKey)
			if err != nil {
				return nil, fmt.Errorf("push: decode %s: %w", path, err)
			}
			raw, err := x509.ParsePKCS8PrivateKey(der)
			if err != nil {
				return nil, fmt.Errorf("push: key in %s: %w", path, err)
			}
			ec, ok := raw.(*ecdsa.PrivateKey)
			if !ok || ec.Curve.Params().Name != "P-256" {
				return nil, fmt.Errorf("push: key in %s is not P-256", path)
			}
			ecdhKey, err := ec.ECDH()
			if err != nil {
				return nil, err
			}
			return &vapidKey{priv: ec, pub: enc.EncodeToString(ecdhKey.PublicKey().Bytes())}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("push: read %s: %w", path, err)
		}
	}

	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("push: generate vapid key: %w", err)
	}
	ecdhKey, err := ec.ECDH()
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		return nil, err
	}
	pub := enc.EncodeToString(ecdhKey.PublicKey().Bytes())
	if path != "" {
		b, err := json.MarshalIndent(storedVAPID{
			PublicKey:  pub,
			PrivateKey: enc.EncodeToString(der),
		}, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("push: save vapid key: %w", err)
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
			return nil, fmt.Errorf("push: save vapid key: %w", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return nil, fmt.Errorf("push: save vapid key: %w", err)
		}
	}
	return &vapidKey{priv: ec, pub: pub}, nil
}

// decodeB64URL accepts URL-safe or standard base64 with or without '=' padding.
func decodeB64URL(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	if s == "" {
		return nil, ErrBadSubscription
	}
	return base64.RawURLEncoding.DecodeString(s)
}

func isDisallowedPushAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return !addr.IsValid() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
}

func isDisallowedPushHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if sh, _, err := net.SplitHostPort(h); err == nil {
		h = sh
	}
	h = strings.Trim(h, "[]")
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		return isDisallowedPushAddr(ip)
	}
	return false
}

// newSafeWebPushClient builds an HTTP client whose dialer refuses private,
// loopback and link-local addresses so a paired browser cannot turn Web Push
// into an SSRF probe against the daemon's LAN or localhost.
func newSafeWebPushClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, ErrPrivateEndpoint
			}
			for _, ip := range ips {
				if isDisallowedPushAddr(ip) {
					return nil, ErrPrivateEndpoint
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func normalizeWebSubscription(sub WebSubscription, allowPrivate bool, now time.Time) (WebSubscription, error) {
	ep := strings.TrimSpace(sub.Endpoint)
	if len(ep) == 0 || len(ep) > 2048 {
		return WebSubscription{}, ErrBadSubscription
	}
	u, err := url.Parse(ep)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return WebSubscription{}, ErrBadSubscription
	}
	if !allowPrivate && isDisallowedPushHost(u.Host) {
		return WebSubscription{}, ErrPrivateEndpoint
	}
	pubBytes, err := decodeB64URL(sub.Keys.P256DH)
	if err != nil || len(pubBytes) != 65 {
		return WebSubscription{}, ErrBadSubscription
	}
	if _, err := ecdh.P256().NewPublicKey(pubBytes); err != nil {
		return WebSubscription{}, ErrBadSubscription
	}
	authBytes, err := decodeB64URL(sub.Keys.Auth)
	if err != nil || len(authBytes) != 16 {
		return WebSubscription{}, ErrBadSubscription
	}
	enc := base64.RawURLEncoding
	name := strings.TrimSpace(sub.Name)
	if len(name) > 200 {
		name = name[:200]
	}
	return WebSubscription{
		Endpoint: ep,
		Keys: WebPushKeys{
			P256DH: enc.EncodeToString(pubBytes),
			Auth:   enc.EncodeToString(authBytes),
		},
		Name:         name,
		DeviceID:     sub.DeviceID,
		RegisteredAt: now.UTC(),
	}, nil
}

// encryptRFC8291 encrypts plaintext for one Web Push subscription using the
// RFC 8291 aes128gcm content encoding (RFC 8188 single-record framing).
func encryptRFC8291(plaintext []byte, p256dh, auth string) ([]byte, error) {
	uaBytes, err := decodeB64URL(p256dh)
	if err != nil {
		return nil, err
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaBytes)
	if err != nil {
		return nil, err
	}
	authSecret, err := decodeB64URL(auth)
	if err != nil || len(authSecret) != 16 {
		return nil, ErrBadSubscription
	}

	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	asPubBytes := asPriv.PublicKey().Bytes()
	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}

	keyInfo := make([]byte, 0, 14+len(uaBytes)+len(asPubBytes))
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaBytes...)
	keyInfo = append(keyInfo, asPubBytes...)

	prkKey, err := hkdf.Extract(sha256.New, shared, authSecret)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}

	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}

	blk, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}

	padded := make([]byte, len(plaintext)+1)
	copy(padded, plaintext)
	padded[len(plaintext)] = 0x02

	header := make([]byte, 21+len(asPubBytes))
	copy(header[:16], salt)
	binary.BigEndian.PutUint32(header[16:20], webPushRecordSize)
	header[20] = byte(len(asPubBytes))
	copy(header[21:], asPubBytes)

	return gcm.Seal(header, nonce, padded, nil), nil
}

// signVAPIDJWT signs an RFC 8292 VAPID ES256 JWT for push service origin aud.
func signVAPIDJWT(key *ecdsa.PrivateKey, aud, sub string, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	h, err := json.Marshal(map[string]string{"typ": "JWT", "alg": "ES256"})
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(map[string]any{
		"aud": aud,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": sub,
	})
	if err != nil {
		return "", err
	}
	signing := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	digest := sha256.Sum256([]byte(signing))
	r, sv, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", fmt.Errorf("push: sign vapid jwt: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	sv.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// webTopic maps an arbitrary collapse ID onto an RFC 8030 §5.4 Topic token
// (at most 32 URL-safe base64 characters).
func webTopic(id string) string {
	if id == "" {
		return ""
	}
	if len(id) <= 32 {
		clean := true
		for _, c := range id {
			if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' && c != '_' {
				clean = false
				break
			}
		}
		if clean {
			return id
		}
	}
	sum := sha256.Sum256([]byte(id))
	return base64.RawURLEncoding.EncodeToString(sum[:24]) // 32 chars
}

// WebPayload builds the JSON object decrypted by /voice/sw.js, keeping it
// within Web Push's 4 KiB budget.
func (n Notification) WebPayload() ([]byte, error) {
	title := n.Agent
	if title == "" {
		title = "Sheepdog"
	}
	tag := n.CollapseID()
	if tag == "" && n.Kind != "" {
		tag = "sheepdog-" + n.Kind
	}
	build := func(text string) ([]byte, error) {
		m := map[string]any{
			"title":     title,
			"body":      text,
			"tag":       tag,
			"kind":      n.Kind,
			"pane_id":   n.PaneID,
			"agent":     n.Agent,
			"needs_you": n.NeedsYou,
		}
		if n.PendingID != "" {
			m["pending_id"] = n.PendingID
		}
		if n.EventID != "" {
			m["event_id"] = n.EventID
		}
		if len(n.Options) > 0 {
			m["options"] = n.Options
		}
		if n.Computer != "" {
			m["computer"] = n.Computer
		}
		return json.Marshal(m)
	}
	b, err := build(n.Text)
	if err != nil || len(b) <= maxWebPlainBytes {
		return b, err
	}
	// Trim UTF-8 runes cleanly so a long announcement still fits one record.
	runes := []rune(n.Text)
	for len(runes) > 32 {
		runes = runes[:len(runes)*3/4]
		b, err = build(string(runes) + "…")
		if err != nil || len(b) <= maxWebPlainBytes {
			return b, err
		}
	}
	return build("New update")
}

// webDevices persists browser Web Push subscriptions, keyed by endpoint.
type webDevices struct {
	mu   sync.Mutex
	path string
	m    map[string]WebSubscription
}

func loadWebDevices(path string) (*webDevices, error) {
	d := &webDevices{path: path, m: map[string]WebSubscription{}}
	if path == "" {
		return d, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, fmt.Errorf("push: read web devices: %w", err)
	}
	var list []WebSubscription
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("push: parse %s: %w", path, err)
	}
	for _, sub := range list {
		if sub.Endpoint != "" {
			d.m[sub.Endpoint] = sub
		}
	}
	return d, nil
}

func (d *webDevices) put(sub WebSubscription) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if sub.DeviceID != "" {
		for ep, other := range d.m {
			if other.DeviceID == sub.DeviceID && ep != sub.Endpoint {
				delete(d.m, ep)
			}
		}
	}
	if old, ok := d.m[sub.Endpoint]; ok {
		if old.Keys == sub.Keys && old.Name == sub.Name && old.DeviceID == sub.DeviceID {
			return nil
		}
	}
	d.m[sub.Endpoint] = sub
	// Evict oldest if over cap.
	for len(d.m) > maxWebDevices {
		var oldestEP string
		var oldestAt time.Time
		for ep, s := range d.m {
			if oldestEP == "" || s.RegisteredAt.Before(oldestAt) {
				oldestEP, oldestAt = ep, s.RegisteredAt
			}
		}
		delete(d.m, oldestEP)
	}
	return d.saveLocked()
}

func (d *webDevices) remove(endpoint string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.m[endpoint]; !ok {
		return false, nil
	}
	delete(d.m, endpoint)
	return true, d.saveLocked()
}

func (d *webDevices) removeDevices(ids []string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	drop := map[string]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	n := 0
	for ep, sub := range d.m {
		if sub.DeviceID != "" && drop[sub.DeviceID] {
			delete(d.m, ep)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, d.saveLocked()
}

func (d *webDevices) list() []WebSubscription {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]WebSubscription, 0, len(d.m))
	for _, sub := range d.m {
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RegisteredAt.Equal(out[j].RegisteredAt) {
			return out[i].RegisteredAt.Before(out[j].RegisteredAt)
		}
		return out[i].Endpoint < out[j].Endpoint
	})
	return out
}

func (d *webDevices) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.m)
}

func (d *webDevices) saveLocked() error {
	if d.path == "" {
		return nil
	}
	list := make([]WebSubscription, 0, len(d.m))
	for _, sub := range d.m {
		list = append(list, sub)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Endpoint < list[j].Endpoint })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return fmt.Errorf("push: save web devices: %w", err)
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("push: save web devices: %w", err)
	}
	if err := os.Rename(tmp, d.path); err != nil {
		return fmt.Errorf("push: save web devices: %w", err)
	}
	return nil
}

// WebPushEnabled reports whether browser Web Push notifications are active
// (on whenever push.mode is not "off" and a VAPID key is loaded).
func (s *Sender) WebPushEnabled() bool {
	return s != nil && s.cfg.Mode != ModeOff && s.vapid != nil
}

// VAPIDPublicKey returns the daemon's base64url P-256 VAPID public key (""
// when Web Push is off).
func (s *Sender) VAPIDPublicKey() string {
	if !s.WebPushEnabled() {
		return ""
	}
	return s.vapid.pub
}

// WebDevices lists the registered browser Web Push subscriptions.
func (s *Sender) WebDevices() []WebSubscription {
	if s == nil || s.webDevs == nil {
		return nil
	}
	return s.webDevs.list()
}

// RegisterWebPush validates and stores a browser Web Push subscription
// (idempotent; replaces any older subscription of the same paired device).
func (s *Sender) RegisterWebPush(sub WebSubscription) error {
	if s == nil || s.webDevs == nil {
		return errors.New("push not available")
	}
	allowPrivate := s.cfg.Client != nil
	norm, err := normalizeWebSubscription(sub, allowPrivate, s.cfg.Now())
	if err != nil {
		return err
	}
	if err := s.webDevs.put(norm); err != nil {
		return err
	}
	s.log.Log(logSource, "push.registered", "", map[string]any{
		"mode":          "webpush",
		"endpoint_host": norm.host(),
		"token_tail":    norm.tail(),
		"device":        norm.Name,
		"web_devices":   s.webDevs.count(),
	})
	return nil
}

// UnregisterWebPush removes a browser Web Push subscription by endpoint; an
// unknown endpoint is not an error.
func (s *Sender) UnregisterWebPush(endpoint string) error {
	if s == nil || s.webDevs == nil {
		return errors.New("push not available")
	}
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return ErrBadSubscription
	}
	had, err := s.webDevs.remove(ep)
	if err != nil {
		return err
	}
	if had {
		s.log.Log(logSource, "push.unregistered", "", map[string]any{
			"mode":        "webpush",
			"token_tail":  tokenTail(ep),
			"reason":      "api",
			"web_devices": s.webDevs.count(),
		})
	}
	return nil
}

func (s *Sender) sendWebPush(ctx context.Context, n Notification, subs []WebSubscription) {
	plain, err := n.WebPayload()
	if err != nil {
		s.log.Log(logSource, "push.failed", n.PaneID, map[string]any{"mode": "webpush", "reason": "marshal: " + err.Error(), "kind": n.Kind})
		return
	}
	now := s.cfg.Now()
	topic := webTopic(n.CollapseID())
	urgency := "normal"
	if n.NeedsYou {
		urgency = "high"
	}
	for _, sub := range subs {
		status, err := s.postWebPush(ctx, sub, plain, topic, urgency, now)
		p := map[string]any{
			"mode":          "webpush",
			"kind":          n.Kind,
			"endpoint_host": sub.host(),
			"token_tail":    sub.tail(),
		}
		if n.EventID != "" {
			p["event_id"] = n.EventID
		}
		if n.PendingID != "" {
			p["pending_id"] = n.PendingID
		}
		switch {
		case err != nil:
			p["reason"] = err.Error()
			s.log.Log(logSource, "push.failed", n.PaneID, p)
		case status >= 200 && status < 300:
			s.log.Log(logSource, "push.sent", n.PaneID, p)
		default:
			p["status"] = status
			s.log.Log(logSource, "push.failed", n.PaneID, p)
			if status == http.StatusGone || status == http.StatusNotFound {
				if had, _ := s.webDevs.remove(sub.Endpoint); had {
					s.log.Log(logSource, "push.unregistered", "", map[string]any{
						"mode":        "webpush",
						"token_tail":  sub.tail(),
						"reason":      "gone",
						"status":      status,
						"web_devices": s.webDevs.count(),
					})
				}
			}
		}
	}
}

func (s *Sender) postWebPush(ctx context.Context, sub WebSubscription, plain []byte, topic, urgency string, now time.Time) (int, error) {
	body, err := encryptRFC8291(plain, sub.Keys.P256DH, sub.Keys.Auth)
	if err != nil {
		return 0, err
	}
	jwt, err := signVAPIDJWT(s.vapid.priv, sub.origin(), defaultVAPIDSubject, now)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", urgency)
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+s.vapid.pub)
	if topic != "" {
		req.Header.Set("Topic", topic)
	}
	resp, err := s.webClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("webpush: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
