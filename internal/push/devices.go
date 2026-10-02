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
	Token        string    `json:"token"`
	Environment  string    `json:"environment"`
	Name         string    `json:"device,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

// ErrBadToken and ErrBadEnvironment are what Register rejects.
var (
	ErrBadToken       = errors.New("token must be a hex device token")
	ErrBadEnvironment = errors.New(`environment must be "production" or "sandbox"`)
)

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
		d.m[dev.Token] = dev
	}
	return d, nil
}

// put adds or replaces dev and persists.
func (d *devices) put(dev Device) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.m[dev.Token]; ok && old.Environment == dev.Environment && old.Name == dev.Name {
		return nil
	}
	d.m[dev.Token] = dev
	return d.saveLocked()
}

// remove drops token; it reports whether it was there.
func (d *devices) remove(token string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.m[token]; !ok {
		return false, nil
	}
	delete(d.m, token)
	return true, d.saveLocked()
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
		return out[i].Token < out[j].Token
	})
	return out
}

func (d *devices) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.m)
}

// saveLocked writes the set atomically (temp file + rename), mode 0600:
// device tokens are not secrets, but they are the user's.
func (d *devices) saveLocked() error {
	if d.path == "" {
		return nil
	}
	list := make([]Device, 0, len(d.m))
	for _, dev := range d.m {
		list = append(list, dev)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Token < list[j].Token })
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
