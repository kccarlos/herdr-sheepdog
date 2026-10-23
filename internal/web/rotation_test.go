package web

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeClock is a settable clock for TLSIdentity.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func mustMaintain(t *testing.T, id *TLSIdentity, want string) Rotation {
	t.Helper()
	r, err := id.Maintain()
	if err != nil {
		t.Fatal(err)
	}
	if r.Phase != want {
		t.Fatalf("Maintain = %+v, want phase %q", r, want)
	}
	return r
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// The yearly rotation on a fake clock: nothing until expiry − 37 days,
// then the next key appears (on disk too) while the old one is still
// served; a restart in that window keeps the same next pin; at expiry − 7
// days the next key is served and next_* is gone.
func TestCertificateRotationTimeline(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: t0}
	id, err := LoadTLSIdentity(dir, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	old, expires := id.Pin(), id.Expires()
	tlsDir := filepath.Join(dir, "tls")

	mustMaintain(t, id, "")
	clock.Set(expires.Add(-RotateLead - time.Minute))
	mustMaintain(t, id, "")
	if id.NextPin() != "" {
		t.Fatal("a next pin before the window")
	}

	clock.Set(expires.Add(-RotateLead))
	r := mustMaintain(t, id, "next")
	next := id.NextPin()
	if next == "" || next == old || r.OldPin != old || r.NewPin != next {
		t.Fatalf("next = %q, rotation %+v, old %q", next, r, old)
	}
	if id.Pin() != old {
		t.Error("the next key is served before the switch")
	}
	for _, name := range []string{"next_key.pem", "next_cert.pem"} {
		if fi, err := os.Stat(filepath.Join(tlsDir, name)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s = %v, %v", name, fi, err)
		}
	}
	mustMaintain(t, id, "") // an hour later: nothing new

	// A restart in the window reads the same next key back.
	clock.Set(expires.Add(-20 * 24 * time.Hour))
	again, err := LoadTLSIdentity(dir, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	if again.Pin() != old || again.NextPin() != next {
		t.Fatalf("after a restart: pin %q next %q", again.Pin(), again.NextPin())
	}
	mustMaintain(t, again, "")

	clock.Set(expires.Add(-RotateSwitch))
	r = mustMaintain(t, again, "switch")
	if again.Pin() != next || again.NextPin() != "" || r.OldPin != old || r.NewPin != next {
		t.Fatalf("after the switch: pin %q next %q rotation %+v", again.Pin(), again.NextPin(), r)
	}
	if !again.Expires().After(expires) {
		t.Errorf("the new certificate expires %v, the old one %v", again.Expires(), expires)
	}
	if exists(filepath.Join(tlsDir, "next_key.pem")) || exists(filepath.Join(tlsDir, "next_cert.pem")) {
		t.Error("next_* left behind after the switch")
	}
	back, err := LoadTLSIdentity(dir, clock.Now)
	if err != nil || back.Pin() != next || back.NextPin() != "" {
		t.Fatalf("reloaded after the switch: %v pin %q next %q", err, back.Pin(), back.NextPin())
	}
}

// A daemon that was off through the whole window makes a key and switches
// at once: it never serves a certificate about to expire. An in-memory
// identity rotates too.
func TestCertificateRotationAfterBeingOff(t *testing.T) {
	for _, dir := range []string{t.TempDir(), ""} {
		t0 := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
		clock := &fakeClock{now: t0}
		id, err := LoadTLSIdentity(dir, clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		old, expires := id.Pin(), id.Expires()
		clock.Set(expires.Add(-24 * time.Hour))
		mustMaintain(t, id, "switch")
		if id.Pin() == old || id.NextPin() != "" || id.Expires().Sub(clock.Now()) < CertLifetime-time.Minute {
			t.Errorf("dir %q: pin %q (old %q) next %q expires %v", dir, id.Pin(), old, id.NextPin(), id.Expires())
		}
		// Even past the old expiry.
		clock.Set(id.Expires().Add(time.Hour))
		mustMaintain(t, id, "switch")
		if !id.Expires().After(clock.Now()) {
			t.Errorf("dir %q serves an expired certificate", dir)
		}
	}
}

// A crash in the middle of the switch never leaves the daemon unable to
// start: cert.pem already new but key.pem still old finishes the switch
// from next_*, and both written but next_* not yet removed drops next_*.
func TestCertificateRotationSurvivesACrash(t *testing.T) {
	copyFile := func(t *testing.T, from, to string) {
		t.Helper()
		b, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setup := func(t *testing.T) (string, string, *fakeClock) {
		dir := t.TempDir()
		clock := &fakeClock{now: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)}
		id, err := LoadTLSIdentity(dir, clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		clock.Set(id.Expires().Add(-RotateLead))
		mustMaintain(t, id, "next")
		return dir, id.NextPin(), clock
	}

	t.Run("cert written, key not", func(t *testing.T) {
		dir, next, clock := setup(t)
		tlsDir := filepath.Join(dir, "tls")
		copyFile(t, filepath.Join(tlsDir, "next_cert.pem"), filepath.Join(tlsDir, "cert.pem"))
		id, err := LoadTLSIdentity(dir, clock.Now)
		if err != nil {
			t.Fatalf("a half-done switch stops the daemon: %v", err)
		}
		if id.Pin() != next || id.NextPin() != "" || exists(filepath.Join(tlsDir, "next_key.pem")) {
			t.Errorf("pin %q (want %q) next %q", id.Pin(), next, id.NextPin())
		}
	})
	t.Run("both written, next not removed", func(t *testing.T) {
		dir, next, clock := setup(t)
		tlsDir := filepath.Join(dir, "tls")
		copyFile(t, filepath.Join(tlsDir, "next_cert.pem"), filepath.Join(tlsDir, "cert.pem"))
		copyFile(t, filepath.Join(tlsDir, "next_key.pem"), filepath.Join(tlsDir, "key.pem"))
		id, err := LoadTLSIdentity(dir, clock.Now)
		if err != nil || id.Pin() != next || id.NextPin() != "" || exists(filepath.Join(tlsDir, "next_cert.pem")) {
			t.Errorf("%v pin %q (want %q) next %q", err, id.Pin(), next, id.NextPin())
		}
	})
	t.Run("damaged key without a next key", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := LoadTLSIdentity(dir, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tls", "key.pem"), []byte("nope"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTLSIdentity(dir, nil); err == nil {
			t.Error("a damaged key was replaced silently")
		}
	})
}

// pinsTLS trusts any of pins, checks no name and records the pin the
// server's key matched, as the app's PinnedTrustDelegate does.
func pinsTLS(matched *string, pins ...string) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // the pins below are the check
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			leaf, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			pin := SPKIPin(leaf.RawSubjectPublicKeyInfo)
			if !slices.Contains(pins, pin) {
				return errors.New("pin mismatch")
			}
			*matched = pin
			return nil
		},
	}
}

// A paired phone learns the next pin and the certificate's expiry from
// its session. After the switch, a phone that kept both pins shakes hands
// (matching the next pin), signs with it and gets a ticket; one that kept
// only the old pin fails the handshake and sends nothing; a signature over
// the retired pin is refused.
func TestRotationKeepsPairedPhonesConnected(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	clock := &fakeClock{now: time.Now()}
	id, err := LoadTLSIdentity(t.TempDir(), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	h.srv.deps.TLS = id
	old, expires := id.Pin(), id.Expires()
	d := h.pairDevice()

	st, sess, _ := h.signIn(d.key, d.id, old, time.Now())
	if st != 200 || sess.NextPin != "" || !sess.CertExpires.Equal(expires.UTC()) {
		t.Fatalf("before the window: %d %+v (expires %v)", st, sess, expires)
	}

	clock.Set(expires.Add(-RotateLead))
	h.srv.maintainTLS()
	st, sess, _ = h.signIn(d.key, d.id, old, time.Now())
	next := id.NextPin()
	if st != 200 || next == "" || sess.NextPin != next {
		t.Fatalf("in the window: %d %+v, next %q", st, sess, next)
	}
	if !slices.Contains(h.types(), "web.tls.rotate") {
		t.Error("the rotation was not logged")
	}

	clock.Set(expires.Add(-RotateSwitch))
	h.srv.maintainTLS() // the hourly check (Serve also runs one when it starts)
	addr := serveReal(t, h)
	if id.Pin() != next {
		t.Fatalf("served %q after the switch, want %q", id.Pin(), next)
	}

	var matched string
	conn, err := tls.Dial("tcp", addr, pinsTLS(&matched, old, next))
	if err != nil {
		t.Fatalf("a phone with both pins: %v", err)
	}
	_ = conn.Close()
	if matched != next {
		t.Fatalf("matched %q, want the next pin", matched)
	}
	st, sess, bad := h.signIn(d.key, d.id, matched, time.Now())
	if st != 200 || sess.Ticket == "" || sess.NextPin != "" || !sess.CertExpires.Equal(id.Expires().UTC()) {
		t.Fatalf("signing with the new pin: %d %+v %v", st, sess, bad)
	}

	if conn, err := tls.Dial("tcp", addr, pinsTLS(new(string), old)); err == nil {
		_ = conn.Close()
		t.Error("a phone with only the old pin connected")
	}
	if st, _, bad := h.signIn(d.key, d.id, old, time.Now()); st != 401 || bad["reason"] != "bad_signature" {
		t.Errorf("signing with the retired pin = %d %v", st, bad)
	}
}
