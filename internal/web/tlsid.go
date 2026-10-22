package web

import (
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

// TLSIdentity is the daemon's TLS key and self-signed certificate, kept
// in <data_dir>/tls/ (key.pem, cert.pem; the directory 0700, the files
// 0600). The app pins the certificate's key by its fingerprint (Pin),
// never by a host name or address, so no name in it matters.
type TLSIdentity struct {
	dir string // "" keeps everything in memory (tests)
	now func() time.Time

	mu   sync.RWMutex
	cur  *certPair
	next *certPair // made ahead of a rotation (rotation.go)
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
	switch {
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
	id.cur = cur
	if next, err := readCertPair(id.dir, "next_key.pem", "next_cert.pem"); err == nil {
		id.next = next
	}
	return id, nil
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
		_ = os.Remove(filepath.Join(t.dir, "next_key.pem"))
		_ = os.Remove(filepath.Join(t.dir, "next_cert.pem"))
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
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			t.mu.RLock()
			defer t.mu.RUnlock()
			c := t.cur.cert
			return &c, nil
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
