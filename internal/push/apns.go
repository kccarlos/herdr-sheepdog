package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// The APNs hosts (HTTP/2 over TLS, port 443).
const (
	ProductionURL = "https://api.push.apple.com"
	SandboxURL    = "https://api.sandbox.push.apple.com"
)

// tokenRefresh is how long one provider token is reused: Apple rejects a
// token older than an hour and throttles refreshes more often than every
// 20 minutes.
const tokenRefresh = 50 * time.Minute

// logSource is the event log source of this package's events.
const logSource = "push"

// The Config.Mode values (config.PushMode*).
const (
	ModeRelay = "relay"
	ModeAPNs  = "apns"
	ModeOff   = "off"
)

// Config configures the sender. Mode "apns" (the default when empty)
// sends straight to APNs: empty KeyPath, KeyID, TeamID or Topic leaves
// sending disabled. Mode "relay" sends through the push relay at
// RelayURL, end-to-end encrypted. Mode "off" sends nothing. Registration
// works in every mode.
type Config struct {
	Mode string
	// RelayURL is the relay's base URL (mode relay).
	RelayURL string
	// BundleID is the app the relay registers device tokens for (mode
	// relay; default Topic, else DefaultBundleID).
	BundleID string
	KeyPath  string // the .p8 auth key (PKCS#8 PEM, ES256)
	KeyID    string // the key's 10-character id
	TeamID   string // the Apple Developer team id (the JWT's iss)
	Topic    string // the app's bundle id (apns-topic)
	// DataDir is where push_devices.json lives; "" keeps devices in
	// memory only (tests).
	DataDir string

	// ProductionURL / SandboxURL override the APNs hosts (tests).
	ProductionURL, SandboxURL string
	// Client overrides the HTTP client (tests: an httptest TLS server's).
	Client *http.Client
	// Now overrides the clock (tests).
	Now func() time.Time
	// Timeout bounds one notification's fan-out (default 15 s).
	Timeout time.Duration
}

// Status is what GET /api/push/status answers.
type Status struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
	Devices int    `json:"devices"`
	// Mode is how notifications are sent: relay, apns or off.
	Mode string `json:"mode,omitempty"`
}

// Sender sends notifications to the registered devices. All methods are
// safe for concurrent use; a nil *Sender is disabled and does nothing.
type Sender struct {
	cfg     Config
	log     logging.Logger
	on      bool              // sending enabled
	key     *ecdsa.PrivateKey // mode apns
	relay   relayClient       // mode relay
	reason  string            // why disabled
	devs    *devices
	acts    activities
	client  *http.Client
	baseURL map[string]string

	mu       sync.Mutex
	jwt      string
	jwtAt    time.Time
	seen     map[string]bool // dedupe keys sent
	seenList []string        // the same keys, oldest first (bounded)
	wg       sync.WaitGroup
}

// maxSeen bounds the dedupe set.
const maxSeen = 512

// New builds the Sender: it loads the device file and, when the four APNs
// settings are present, the auth key. A missing or unreadable key does
// not fail: sending is disabled with the reason (Status, LogStartup), so
// the daemon still starts. Only a corrupt device file is an error.
func New(cfg Config, log logging.Logger) (*Sender, error) {
	if log == nil {
		log = logging.NopLogger{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.ProductionURL == "" {
		cfg.ProductionURL = ProductionURL
	}
	if cfg.SandboxURL == "" {
		cfg.SandboxURL = SandboxURL
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	path := ""
	if cfg.DataDir != "" {
		path = filepath.Join(cfg.DataDir, DevicesFile)
	}
	devs, err := loadDevices(path)
	if err != nil {
		return nil, err
	}
	actsPath := ""
	if cfg.DataDir != "" {
		actsPath = filepath.Join(cfg.DataDir, ActivitiesFile)
	}
	s := &Sender{cfg: cfg, log: log, devs: devs, client: client, seen: map[string]bool{},
		baseURL: map[string]string{EnvProduction: cfg.ProductionURL, EnvSandbox: cfg.SandboxURL}}
	s.acts.load(actsPath, cfg.Now())
	switch cfg.Mode {
	case ModeOff:
		s.reason = "push.mode is off"
	case ModeRelay:
		s.relay = relayClient{base: strings.TrimRight(cfg.RelayURL, "/"), client: client}
		switch {
		case strings.TrimSpace(cfg.RelayURL) == "":
			s.reason = "push.relay.url is empty"
		case strings.Contains(cfg.RelayURL, relayPlaceholder):
			s.reason = "the shared relay is not deployed yet: set push.relay.url, or push.mode = \"apns\" with your own key"
		default:
			s.on = true
		}
		if s.cfg.BundleID == "" {
			s.cfg.BundleID = cfg.Topic
		}
		if s.cfg.BundleID == "" {
			s.cfg.BundleID = DefaultBundleID
		}
	default:
		s.cfg.Mode = ModeAPNs
		s.key, s.reason = loadKey(cfg)
		s.on = s.key != nil
	}
	return s, nil
}

// LogStartup logs push.enabled, or push.disabled with the reason — once,
// at daemon start (after daemon.start, so the daemon calls it from Run).
func (s *Sender) LogStartup() {
	if s == nil {
		return
	}
	if !s.on {
		s.log.Log(logSource, "push.disabled", "", map[string]any{"mode": s.cfg.Mode, "reason": s.reason, "devices": s.devs.count()})
		return
	}
	if s.cfg.Mode == ModeRelay {
		s.log.Log(logSource, "push.enabled", "", map[string]any{"mode": ModeRelay, "relay": s.relay.base, "bundle_id": s.cfg.BundleID, "devices": s.devs.count()})
		return
	}
	s.log.Log(logSource, "push.enabled", "", map[string]any{"mode": ModeAPNs, "key_id": s.cfg.KeyID, "team_id": s.cfg.TeamID, "topic": s.cfg.Topic, "devices": s.devs.count()})
}

// loadKey reads and parses the auth key, or says why it cannot.
func loadKey(cfg Config) (*ecdsa.PrivateKey, string) {
	var missing []string
	for _, f := range []struct{ name, v string }{{"key_path", cfg.KeyPath}, {"key_id", cfg.KeyID}, {"team_id", cfg.TeamID}, {"topic", cfg.Topic}} {
		if strings.TrimSpace(f.v) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return nil, "push.apns not configured (missing " + strings.Join(missing, ", ") + ")"
	}
	path := cfg.KeyPath
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, rest)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "cannot read push.apns.key_path: " + err.Error()
	}
	key, err := ParseKey(b)
	if err != nil {
		return nil, "push.apns.key_path: " + err.Error()
	}
	return key, ""
}

// ParseKey parses a .p8 auth key: a PKCS#8 PEM block holding a P-256
// ECDSA private key.
func ParseKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return nil, errors.New("not a PEM file")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, errors.New("not a PKCS#8 private key")
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve.Params().Name != "P-256" {
		return nil, errors.New("not a P-256 ECDSA key")
	}
	return ec, nil
}

// Enabled reports whether notifications are sent.
func (s *Sender) Enabled() bool { return s != nil && s.on }

// Status reports whether sending is enabled, why not, and how many
// devices are registered.
func (s *Sender) Status() Status {
	if s == nil {
		return Status{Reason: "push not available"}
	}
	return Status{Enabled: s.on, Reason: s.reason, Devices: s.devs.count(), Mode: s.cfg.Mode}
}

// Registration is what POST /api/push/register carries: a device token,
// a relay token, or both.
type Registration struct {
	Token       string // hex APNs device token
	RelayToken  string // the relay's token for it (mode relay)
	Environment string // EnvProduction or EnvSandbox (required with Token)
	Name        string // the phone's name, for the logs
	DeviceID    string // the paired device it belongs to ("" for this computer)
}

// Register stores a device by its device token (see RegisterDevice).
func (s *Sender) Register(token, environment, name string) error {
	_, err := s.RegisterDevice(Registration{Token: token, Environment: environment, Name: name})
	return err
}

// RegisterDevice stores a device (idempotent: the same token again
// updates its environment, name and relay token) and returns it, with
// its content key: generated at the first registration, the same on
// every later one. It works while sending is disabled, so a device
// registered before push is set up is not lost.
func (s *Sender) RegisterDevice(r Registration) (Device, error) {
	if s == nil {
		return Device{}, errors.New("push not available")
	}
	dev := Device{Name: strings.TrimSpace(r.Name), DeviceID: r.DeviceID, RegisteredAt: s.cfg.Now().UTC()}
	if r.Token != "" || r.RelayToken == "" {
		tok, err := NormalizeToken(r.Token)
		if err != nil {
			return Device{}, err
		}
		dev.Token = tok
		if r.Environment != EnvProduction && r.Environment != EnvSandbox {
			return Device{}, ErrBadEnvironment
		}
	}
	if r.Environment != "" && r.Environment != EnvProduction && r.Environment != EnvSandbox {
		return Device{}, ErrBadEnvironment
	}
	dev.Environment = r.Environment
	if r.RelayToken != "" {
		rt, err := checkRelayToken(r.RelayToken)
		if err != nil {
			return Device{}, err
		}
		dev.RelayToken = rt
	}
	stored, err := s.devs.put(dev)
	if err != nil {
		return Device{}, err
	}
	s.log.Log(logSource, "push.registered", "", map[string]any{"token_tail": stored.tail(), "relay_token": stored.RelayToken != "", "environment": stored.Environment, "device": stored.Name, "devices": s.devs.count()})
	return stored, nil
}

// Unregister removes a device by its device token or relay token; an
// unknown one is not an error.
func (s *Sender) Unregister(token string) error {
	if s == nil {
		return errors.New("push not available")
	}
	id := ""
	if tok, err := NormalizeToken(token); err == nil {
		id = tok
	} else if rt, rerr := checkRelayToken(token); rerr == nil {
		id = s.devs.idOfRelayToken(rt)
	} else {
		return err
	}
	if id == "" {
		return nil
	}
	had, err := s.devs.remove(id)
	if err != nil {
		return err
	}
	if had {
		s.log.Log(logSource, "push.unregistered", "", map[string]any{"token_tail": tokenTail(token), "reason": "api", "devices": s.devs.count()})
	}
	return nil
}

// ForgetDevices drops the registrations of revoked paired devices.
func (s *Sender) ForgetDevices(ids []string) error {
	if s == nil || len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		s.ForgetActivity(id)
	}
	n, err := s.devs.removeDevices(ids)
	if err != nil {
		return err
	}
	if n > 0 {
		s.log.Log(logSource, "push.unregistered", "", map[string]any{"reason": "device_revoked", "removed": n, "devices": s.devs.count()})
	}
	return nil
}

// Devices lists the registered devices.
func (s *Sender) Devices() []Device {
	if s == nil {
		return nil
	}
	return s.devs.list()
}

// Notify sends n to every registered device in the background, once per
// dedupe key (Notification.Key): a second Notify about the same event is
// dropped. It never blocks on APNs.
func (s *Sender) Notify(n Notification) {
	if !s.Enabled() {
		return
	}
	if key := n.Key(); key != "" {
		s.mu.Lock()
		if s.seen[key] {
			s.mu.Unlock()
			return
		}
		s.seen[key] = true
		s.seenList = append(s.seenList, key)
		if len(s.seenList) > maxSeen {
			delete(s.seen, s.seenList[0])
			s.seenList = s.seenList[1:]
		}
		s.mu.Unlock()
	}
	devs := s.devs.list()
	if n.ExceptDevice != "" {
		kept := devs[:0]
		for _, d := range devs {
			if d.DeviceID != n.ExceptDevice {
				kept = append(kept, d)
			}
		}
		devs = kept
	}
	if len(devs) == 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
		defer cancel()
		if s.cfg.Mode == ModeRelay {
			s.sendRelay(ctx, n, devs)
			return
		}
		s.send(ctx, n, devs)
	}()
}

// Wait blocks until every notification in flight is done (tests, and
// shutdown).
func (s *Sender) Wait() {
	if s != nil {
		s.wg.Wait()
	}
}

// send posts n to each device.
func (s *Sender) send(ctx context.Context, n Notification, devs []Device) {
	body, err := json.Marshal(n.Payload())
	if err != nil {
		s.log.Log(logSource, "push.failed", n.PaneID, map[string]any{"reason": "marshal: " + err.Error(), "kind": n.Kind})
		return
	}
	for _, d := range devs {
		status, reason, err := s.post(ctx, d, n, body, false)
		if err == nil && status == http.StatusForbidden && reason == "ExpiredProviderToken" {
			// Our clock or cache went past Apple's hour: mint a new
			// token and try once more.
			status, reason, err = s.post(ctx, d, n, body, true)
		}
		p := map[string]any{"mode": ModeAPNs, "kind": n.Kind, "token_tail": d.tail(), "environment": d.Environment, "collapse_id": n.CollapseID()}
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
		case status == http.StatusOK:
			s.log.Log(logSource, "push.sent", n.PaneID, p)
		default:
			p["status"], p["reason"] = status, reason
			s.log.Log(logSource, "push.failed", n.PaneID, p)
			if status == http.StatusGone || (status == http.StatusBadRequest && reason == "BadDeviceToken") {
				s.drop(d, reason, status)
			}
		}
	}
}

// drop removes a device the push service says is gone.
func (s *Sender) drop(d Device, reason string, status int) {
	if had, _ := s.devs.remove(d.id()); had {
		s.log.Log(logSource, "push.unregistered", "", map[string]any{"token_tail": d.tail(), "reason": reason, "status": status, "devices": s.devs.count()})
	}
}

// post sends one request and returns APNs' status and reason.
func (s *Sender) post(ctx context.Context, d Device, n Notification, body []byte, fresh bool) (int, string, error) {
	base := s.baseURL[d.Environment]
	if base == "" {
		base = s.cfg.ProductionURL
	}
	jwt, err := s.token(fresh)
	if err != nil {
		return 0, "", err
	}
	if d.Token == "" {
		return 0, "", errors.New("no device token (registered with a relay token only)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+d.Token, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", s.cfg.Topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", n.priority())
	req.Header.Set("content-type", "application/json")
	if id := n.CollapseID(); id != "" {
		req.Header.Set("apns-collapse-id", id)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("apns: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var r struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(rb, &r)
	return resp.StatusCode, r.Reason, nil
}

// token returns the cached provider token, minting a new one when it is
// older than tokenRefresh or fresh is set.
func (s *Sender) token(fresh bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	if !fresh && s.jwt != "" && now.Sub(s.jwtAt) < tokenRefresh {
		return s.jwt, nil
	}
	t, err := SignJWT(s.key, s.cfg.KeyID, s.cfg.TeamID, now)
	if err != nil {
		return "", err
	}
	s.jwt, s.jwtAt = t, now
	return t, nil
}

// SignJWT mints an APNs provider token: header {alg: ES256, kid}, claims
// {iss: team id, iat}, signed with the raw 64-byte r||s ECDSA signature
// JWS requires.
func SignJWT(key *ecdsa.PrivateKey, keyID, teamID string, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	h, err := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID})
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(map[string]any{"iss": teamID, "iat": now.Unix()})
	if err != nil {
		return "", err
	}
	signing := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	digest := sha256.Sum256([]byte(signing))
	r, sv, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", fmt.Errorf("push: sign token: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	sv.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}
