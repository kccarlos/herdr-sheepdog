package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CertLifetime is how long the daemon's self-signed certificate is valid
// (docs/dev/design/security.md "The daemon's identity").
const CertLifetime = 365 * 24 * time.Hour

// The certificate rotates on its own (docs/dev/design/security.md
// "Certificate rotation"): RotateLead before it expires the daemon makes
// the next key and announces its pin (SessionResponse.NextPin); RotateSwitch
// before it expires it serves the next key.
const (
	RotateLead   = 37 * 24 * time.Hour
	RotateSwitch = 7 * 24 * time.Hour
)

// TLSIdentity is the daemon's TLS key and self-signed certificate, kept
// in <data_dir>/tls/ (key.pem, cert.pem; the directory 0700, the files
// 0600). The app pins the certificate's key by its fingerprint (Pin),
// never by a host name or address, so no name in it matters.
type TLSIdentity struct {
	dir string // "" keeps everything in memory (tests)
	now func() time.Time
	ts  tsCert // the *.ts.net certificate, when served (tsnet.go)

	mu   sync.RWMutex
	cur  *certPair
	next *certPair // made RotateLead before expiry (Maintain)
}

// certPair is one key and its certificate.
type certPair struct {
	cert    tls.Certificate
	leaf    *x509.Certificate
	pin     string
	expires time.Time
}

// LoadTLSIdentity reads the identity in <dataDir>/tls/, making a new key
// and certificate the first time. An empty dataDir keeps a fresh one in
// memory. A damaged file is an error, never silently replaced: every
// paired device pins the key.
func LoadTLSIdentity(dataDir string, now func() time.Time) (*TLSIdentity, error) {
	if now == nil {
		now = time.Now
	}
	id := &TLSIdentity{now: now}
	if dataDir != "" {
		id.dir = filepath.Join(dataDir, "tls")
	}
	if id.dir == "" {
		p, err := newCertPair(now())
		if err != nil {
			return nil, err
		}
		id.cur = p
		return id, nil
	}
	if err := os.MkdirAll(id.dir, 0o700); err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	cur, err := readCertPair(id.dir, "key.pem", "cert.pem")
	next, nextErr := readCertPair(id.dir, "next_key.pem", "next_cert.pem")
	switch {
	case err != nil && nextErr == nil && !errors.Is(err, os.ErrNotExist):
		// A crash in the middle of a switch (Maintain writes cert.pem,
		// then key.pem, then removes next_*): finish it.
		if err := writeCertPair(id.dir, "key.pem", "cert.pem", next); err != nil {
			return nil, err
		}
		cur, next = next, nil
		id.removeNext()
	case errors.Is(err, os.ErrNotExist):
		if cur, err = newCertPair(now()); err != nil {
			return nil, err
		}
		if err := writeCertPair(id.dir, "key.pem", "cert.pem", cur); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}
	if next != nil && next.pin == cur.pin {
		// The switch wrote key.pem and cert.pem but did not get to
		// removing next_*.
		next = nil
		id.removeNext()
	}
	id.cur, id.next = cur, next
	return id, nil
}

// Rotation is what one Maintain call did, for the web.tls.rotate event.
type Rotation struct {
	// Phase is "" (nothing to do), "next" (the next key was made) or
	// "switch" (the next key is now served).
	Phase  string
	OldPin string
	NewPin string
}

// Maintain rotates the certificate when it is due: at expiry − RotateLead
// it makes the next key (next_key.pem, next_cert.pem), at expiry −
// RotateSwitch it serves it. A daemon that was off through the whole
// window makes the next key and switches at once, so it never serves a
// certificate that is about to expire. The server calls it when it starts
// and every hour.
func (t *TLSIdentity) Maintain() (Rotation, error) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	switchAt := t.cur.expires.Add(-RotateSwitch)
	if now.Before(t.cur.expires.Add(-RotateLead)) {
		return Rotation{}, nil
	}
	var r Rotation
	if t.next == nil {
		next, err := newCertPair(now)
		if err != nil {
			return Rotation{}, err
		}
		if t.dir != "" {
			if err := writeCertPair(t.dir, "next_key.pem", "next_cert.pem", next); err != nil {
				return Rotation{}, err
			}
		}
		t.next = next
		r = Rotation{Phase: "next", OldPin: t.cur.pin, NewPin: next.pin}
	}
	if now.Before(switchAt) {
		return r, nil
	}
	if t.dir != "" {
		if err := writeCertPair(t.dir, "key.pem", "cert.pem", t.next); err != nil {
			return r, err
		}
		t.removeNext()
	}
	r = Rotation{Phase: "switch", OldPin: t.cur.pin, NewPin: t.next.pin}
	t.cur, t.next = t.next, nil
	return r, nil
}

// tlsCheck is how often the server calls Maintain.
const tlsCheck = time.Hour

// watchTLS rotates the certificate when it is due, until ctx ends.
func (s *Server) watchTLS(ctx context.Context) {
	t := time.NewTicker(tlsCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.maintainTLS()
		}
	}
}

// maintainTLS is one Maintain call, logged as web.tls.rotate. New
// connections get the new key at once; open ones keep the key they
// shook hands with.
func (s *Server) maintainTLS() {
	r, err := s.deps.TLS.Maintain()
	if err != nil {
		s.log.Log(logSource, "web.tls.rotate_failed", "", map[string]any{"error": err.Error()})
		return
	}
	if r.Phase == "" {
		return
	}
	s.log.Log(logSource, "web.tls.rotate", "", map[string]any{"phase": r.Phase, "old_pin": r.OldPin, "new_pin": r.NewPin, "expires": s.deps.TLS.Expires().UTC()})
}

func (t *TLSIdentity) removeNext() {
	_ = os.Remove(filepath.Join(t.dir, "next_key.pem"))
	_ = os.Remove(filepath.Join(t.dir, "next_cert.pem"))
}

// Pin is base64url(SHA-256(SubjectPublicKeyInfo)) of the key being
// served.
func (t *TLSIdentity) Pin() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cur.pin
}

// Expires is when the served certificate stops being valid.
func (t *TLSIdentity) Expires() time.Time {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cur.expires
}

// NextPin is the pin of the key the daemon will switch to, "" outside a
// rotation window.
func (t *TLSIdentity) NextPin() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.next == nil {
		return ""
	}
	return t.next.pin
}

// Reset replaces the key and certificate at once (the setup page's Reset
// pairing): every paired device sees a new pin and scans again.
func (t *TLSIdentity) Reset() error {
	p, err := newCertPair(t.now())
	if err != nil {
		return err
	}
	if t.dir != "" {
		if err := writeCertPair(t.dir, "key.pem", "cert.pem", p); err != nil {
			return err
		}
		t.removeNext()
	}
	t.mu.Lock()
	t.cur, t.next = p, nil
	t.mu.Unlock()
	return nil
}

// TLSConfig is the server side of every TLS connection: TLS 1.2 or newer,
// ECDHE only, HTTP/1.1 (the WebSocket upgrades need it).
func (t *TLSIdentity) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return t.certFor(hello), nil
		},
	}
}

// SPKIPin is the pin of a DER SubjectPublicKeyInfo.
func SPKIPin(spki []byte) string {
	sum := sha256.Sum256(spki)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// newCertPair makes a P-256 key and a self-signed certificate for it,
// valid from an hour ago (clock skew) for CertLifetime.
func newCertPair(now time.Time) (*certPair, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "sheepdog"},
		DNSNames:              []string{"sheepdog"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(CertLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	return pairFrom(der, key)
}

func pairFrom(der []byte, key *ecdsa.PrivateKey) (*certPair, error) {
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	return &certPair{
		cert:    tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		leaf:    leaf,
		pin:     SPKIPin(leaf.RawSubjectPublicKeyInfo),
		expires: leaf.NotAfter,
	}, nil
}

// readCertPair reads one key and certificate; os.ErrNotExist when the key
// is missing.
func readCertPair(dir, keyName, certName string) (*certPair, error) {
	kb, err := os.ReadFile(filepath.Join(dir, keyName))
	if err != nil {
		return nil, err
	}
	cb, err := os.ReadFile(filepath.Join(dir, certName))
	if err != nil {
		return nil, fmt.Errorf("tls: %s without %s: %w", keyName, certName, err)
	}
	kblk, _ := pem.Decode(kb)
	cblk, _ := pem.Decode(cb)
	if kblk == nil || cblk == nil {
		return nil, fmt.Errorf("tls: %s or %s is not PEM", keyName, certName)
	}
	key, err := x509.ParseECPrivateKey(kblk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("tls: %s: %w", keyName, err)
	}
	p, err := pairFrom(cblk.Bytes, key)
	if err != nil {
		return nil, err
	}
	if pub, ok := p.leaf.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		return nil, fmt.Errorf("tls: %s does not match %s", certName, keyName)
	}
	return p, nil
}

// writeCertPair writes one key and certificate (0600), each through a
// temporary file so a crash never leaves half a key.
func writeCertPair(dir, keyName, certName string, p *certPair) error {
	kder, err := x509.MarshalECPrivateKey(p.cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, certName), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.cert.Certificate[0]})); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, keyName), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	return nil
}
