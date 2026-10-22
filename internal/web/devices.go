package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
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

// Device kinds (docs/dev/design/security.md "Device identity").
const (
	DeviceIOS     = "ios"
	DeviceIPadOS  = "ipados"
	DeviceBrowser = "browser"
)

// Device is one paired phone, tablet or browser: its public key and what
// the setup page shows about it. Never a secret: the private key never
// leaves the device.
type Device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// PublicKey is the device's P-256 key, the uncompressed point
	// (0x04 || X || Y), base64url without padding.
	PublicKey string    `json:"public_key"`
	Hardware  bool      `json:"hardware"`
	Created   time.Time `json:"created"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	// LastNetwork is how it last came in: loopback, tailscale or lan.
	LastNetwork string `json:"last_network,omitempty"`
	// Revoked is set by Revoke, Disconnect all devices and Reset
	// pairing; a revoked device is refused with device_revoked.
	Revoked *time.Time `json:"revoked,omitempty"`
}

// deviceStore is <data_dir>/devices.json (0600): every device ever
// paired, revoked ones kept so the daemon can say why it refuses them.
type deviceStore struct {
	path string // "" keeps it in memory (tests)
	mu   sync.Mutex
	devs map[string]*Device
}

func loadDeviceStore(dataDir string) (*deviceStore, error) {
	st := &deviceStore{devs: map[string]*Device{}}
	if dataDir == "" {
		return st, nil
	}
	st.path = filepath.Join(dataDir, "devices.json")
	b, err := os.ReadFile(st.path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("devices: %w", err)
	}
	var list []*Device
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("devices: %s: %w", st.path, err)
	}
	for _, d := range list {
		st.devs[d.ID] = d
	}
	return st, nil
}

// saveLocked writes the store; the caller holds mu.
func (st *deviceStore) saveLocked() error {
	if st.path == "" {
		return nil
	}
	list := make([]*Device, 0, len(st.devs))
	for _, d := range st.devs {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return fmt.Errorf("devices: %w", err)
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("devices: %w", err)
	}
	return os.Rename(tmp, st.path)
}

// get returns a copy of the device.
func (st *deviceStore) get(id string) (Device, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	d, ok := st.devs[id]
	if !ok {
		return Device{}, false
	}
	return *d, true
}

// active lists the devices that are not revoked, oldest first.
func (st *deviceStore) active() []Device {
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []Device
	for _, d := range st.devs {
		if d.Revoked == nil {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// enroll records a device's key. The same key enrolled again (the app
// scanning a new code for a computer it knows) keeps its device record.
func (st *deviceStore) enroll(pub, name, kind string, hardware bool, now time.Time) (Device, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, d := range st.devs {
		if d.PublicKey == pub && d.Revoked == nil {
			d.Name, d.Kind, d.Hardware = name, kind, hardware
			return *d, st.saveLocked()
		}
	}
	id, err := randomID(16)
	if err != nil {
		return Device{}, err
	}
	d := &Device{ID: id, Name: name, Kind: kind, PublicKey: pub, Hardware: hardware, Created: now.UTC()}
	st.devs[id] = d
	return *d, st.saveLocked()
}

// seen notes a device's last contact (saved when persist is set).
func (st *deviceStore) seen(id, network string, now time.Time, persist bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	d, ok := st.devs[id]
	if !ok {
		return
	}
	d.LastSeen, d.LastNetwork = now.UTC(), network
	if persist {
		_ = st.saveLocked()
	}
}

// rename sets a device's name.
func (st *deviceStore) rename(id, name string) (Device, bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	d, ok := st.devs[id]
	if !ok || d.Revoked != nil {
		return Device{}, false, nil
	}
	d.Name = name
	return *d, true, st.saveLocked()
}

// revoke marks devices revoked (every active one when ids is empty) and
// returns the ids it revoked.
func (st *deviceStore) revoke(ids []string, now time.Time) ([]string, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	at := now.UTC()
	var out []string
	for id, d := range st.devs {
		if d.Revoked != nil {
			continue
		}
		if len(ids) > 0 && !contains(ids, id) {
			continue
		}
		d.Revoked = &at
		out = append(out, id)
	}
	return out, st.saveLocked()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// randomID is n random bytes, base64url without padding.
func randomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// decodeB64 reads base64 in any of the four alphabets clients use.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("not base64")
}

// parseDeviceKey reads a device public key: the uncompressed P-256 point
// (65 bytes) or, from a client that exports SPKI, its DER form.
func parseDeviceKey(s string) (*ecdsa.PublicKey, string, error) {
	raw, err := decodeB64(s)
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 91 { // SPKI DER of a P-256 key: a fixed 26-byte header
		raw = raw[26:]
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return nil, "", fmt.Errorf("not a P-256 public key: %w", err)
	}
	return pub, base64.RawURLEncoding.EncodeToString(raw), nil
}

// deviceKindName is the name a device gets when it sends none.
func deviceKindName(kind string) string {
	switch kind {
	case DeviceIOS:
		return "iPhone"
	case DeviceIPadOS:
		return "iPad"
	}
	return "Browser"
}
