package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The two APNs environments a device token belongs to: TestFlight and App
// Store builds are production, Xcode debug builds sandbox.
const (
	EnvProduction = "production"
	EnvSandbox    = "sandbox"
)

// DevicesFile is the name of the registered-devices file under the data
// directory.
const DevicesFile = "push_devices.json"

// Device is one registered iOS device.
type Device struct {
	// Token is the APNs device token (hex); "" when the app registered
	// only a relay token.
	Token string `json:"token,omitempty"`
	// RelayToken is the relay's sealed form of the device token
	// (push.mode = "relay"): sent by the app, or fetched by the daemon
	// from the relay's /v1/register on the first notification.
	RelayToken string `json:"relay_token,omitempty"`
	// ContentKey is the phone's end-to-end key (base64 of 32 bytes): the
	// relay path encrypts each notification with it. Generated at the
	// first registration and handed to the app in the answer.
	ContentKey  string `json:"content_key,omitempty"`
	Environment string `json:"environment"`
	Name        string `json:"device,omitempty"`
	// DeviceID is the paired device (docs/dev/design/security.md) this
	// registration belongs to: revoking it drops the registration, and a
	// device has one registration at a time.
	DeviceID     string    `json:"device_id,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

// id is the registry key: the device token, else the relay token.
func (d Device) id() string {
	if d.Token != "" {
		return d.Token
	}
	return "relay:" + d.RelayToken
}

// tail is what a log line carries of the device: the last six characters
// of its device token, else of its relay token.
func (d Device) tail() string {
	if d.Token != "" {
		return tokenTail(d.Token)
	}
	return tokenTail(d.RelayToken)
}

// ErrBadToken, ErrBadRelayToken and ErrBadEnvironment are what Register
// rejects.
var (
	ErrBadToken       = errors.New("token must be a hex device token")
	ErrBadRelayToken  = errors.New("relay_token must be the relay's base64url token")
	ErrBadEnvironment = errors.New(`environment must be "production" or "sandbox"`)
)

// checkRelayToken accepts a relay token's shape: base64url, at most 1024
// characters (the relay itself decides whether it is valid).
func checkRelayToken(tok string) (string, error) {
	tok = strings.TrimSpace(tok)
	if len(tok) < 16 || len(tok) > 1024 {
		return "", ErrBadRelayToken
	}
	for _, c := range tok {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' && c != '_' {
			return "", ErrBadRelayToken
		}
	}
	return tok, nil
}

// NormalizeToken lowercases a hex device token and checks it: an even
// number of hex digits, 32 to 200 bytes' worth (APNs tokens are 32 bytes
// today; Apple says not to hard-code the length).
func NormalizeToken(tok string) (string, error) {
	tok = strings.ToLower(strings.TrimSpace(tok))
	if len(tok) < 64 || len(tok) > 400 || len(tok)%2 != 0 {
		return "", ErrBadToken
	}
	for _, c := range tok {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", ErrBadToken
		}
	}
	return tok, nil
}

// tokenTail is the last six characters of a token: all a log line ever
// carries of it.
func tokenTail(tok string) string {
	if len(tok) <= 6 {
		return tok
	}
	return tok[len(tok)-6:]
}

// devices is the persisted device set, keyed by token.
type devices struct {
	mu   sync.Mutex
	path string // "" keeps the set in memory only
	m    map[string]Device
}

// loadDevices reads path (a missing file is an empty set).
func loadDevices(path string) (*devices, error) {
	d := &devices{path: path, m: map[string]Device{}}
	if path == "" {
		return d, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, fmt.Errorf("push: read devices: %w", err)
	}
	var list []Device
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("push: parse %s: %w", path, err)
	}
	for _, dev := range list {
		d.m[dev.id()] = dev
	}
	return d, nil
}

// put adds or replaces dev and persists; it returns the stored device. A
// re-registration keeps the device's content key (the phone already has
// it) and, while the environment is unchanged, a relay token the new
// registration does not carry. An unchanged device is not rewritten.
func (d *devices) put(dev Device) (Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dev.DeviceID != "" {
		for id, other := range d.m {
			if other.DeviceID == dev.DeviceID && id != dev.id() {
				delete(d.m, id)
			}
		}
	}
	old, had := d.m[dev.id()]
	if had {
		dev.ContentKey = old.ContentKey
		if dev.RelayToken == "" && old.Environment == dev.Environment {
			dev.RelayToken = old.RelayToken
		}
		if old.Environment == dev.Environment && old.Name == dev.Name && old.RelayToken == dev.RelayToken && old.DeviceID == dev.DeviceID && old.ContentKey != "" {
			return old, nil
		}
	}
	if dev.ContentKey == "" {
		k, err := NewContentKey()
		if err != nil {
			return Device{}, err
		}
		dev.ContentKey = k
	}
	d.m[dev.id()] = dev
	return dev, d.saveLocked()
}

// update applies fn to the device with id and persists; a device removed
// meanwhile is left alone.
func (d *devices) update(id string, fn func(*Device)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	dev, ok := d.m[id]
	if !ok {
		return nil
	}
	fn(&dev)
	d.m[id] = dev
	return d.saveLocked()
}

// remove drops the device with id; it reports whether it was there.
func (d *devices) remove(id string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.m[id]; !ok {
		return false, nil
	}
	delete(d.m, id)
	return true, d.saveLocked()
}

// removeDevices drops every registration of the paired devices ids and
// returns how many went.
func (d *devices) removeDevices(ids []string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	drop := map[string]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	n := 0
	for id, dev := range d.m {
		if dev.DeviceID != "" && drop[dev.DeviceID] {
			delete(d.m, id)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, d.saveLocked()
}

// list returns the devices, oldest registration first.
func (d *devices) list() []Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Device, 0, len(d.m))
	for _, dev := range d.m {
		out = append(out, dev)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RegisteredAt.Equal(out[j].RegisteredAt) {
			return out[i].RegisteredAt.Before(out[j].RegisteredAt)
		}
		return out[i].id() < out[j].id()
	})
	return out
}

// idOfRelayToken is the id of the device holding relay token rt ("" for
// none).
func (d *devices) idOfRelayToken(rt string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, dev := range d.m {
		if dev.RelayToken == rt {
			return id
		}
	}
	return ""
}

func (d *devices) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.m)
}

// saveLocked writes the set atomically (temp file + rename), mode 0600:
// device tokens are not secrets, but they are the user's, and the
// content keys are.
func (d *devices) saveLocked() error {
	if d.path == "" {
		return nil
	}
	list := make([]Device, 0, len(d.m))
	for _, dev := range d.m {
		list = append(list, dev)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].id() < list[j].id() })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return fmt.Errorf("push: save devices: %w", err)
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("push: save devices: %w", err)
	}
	if err := os.Rename(tmp, d.path); err != nil {
		return fmt.Errorf("push: save devices: %w", err)
	}
	return nil
}
