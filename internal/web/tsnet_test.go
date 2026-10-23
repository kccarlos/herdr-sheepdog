package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

const testTSName = "studio.tail1234.ts.net"

// fakeTailscaleCert stands in for `tailscale cert`: a certificate for
// name, valid for 90 days like Let's Encrypt's.
func fakeTailscaleCert(t *testing.T, name string) TailscaleCertFunc {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
	return func(context.Context) (string, []byte, []byte, error) {
		return name + ".", certPEM, keyPEM, nil // tailscale's DNSName ends in a dot
	}
}

// servedTo dials addr with SNI serverName ("" sends none, as for an IP)
// and returns the leaf certificate the daemon chose.
func servedTo(t *testing.T, addr, serverName string) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}) //nolint:gosec // the test reads which certificate was served
	if err != nil {
		t.Fatalf("dial with SNI %q: %v", serverName, err)
	}
	defer func() { _ = conn.Close() }()
	return conn.ConnectionState().PeerCertificates[0]
}

// Off by default: every client, whatever name it asks for, gets the
// daemon's own pinned key, and /api/hello names no ts.net host.
func TestTailscaleHTTPSIsOffByDefault(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.srv.deps.TailscaleCert = fakeTailscaleCert(t, testTSName)
	addr := serveReal(t, h)
	if leaf := servedTo(t, addr, testTSName); SPKIPin(leaf.RawSubjectPublicKeyInfo) != h.srv.deps.TLS.Pin() {
		t.Error("the ts.net certificate was served with the switch off")
	}
	var hello HelloResponse
	h.getJSON("/api/hello", &hello)
	if hello.Security.TailscaleHTTPS != "" {
		t.Errorf("hello names %q with the switch off", hello.Security.TailscaleHTTPS)
	}
}

// The setup page's switch: a failed fetch saves nothing and says how to
// fix it; a good one is saved and applied at once. Only a client asking
// for the ts.net name (any case, trailing dot) gets the real certificate;
// the app, connecting by address with no SNI, keeps the pinned key. The
// pairing link's browser host becomes the name; turning it off goes back.
func TestTailscaleHTTPSSwitch(t *testing.T) {
	f := newSetupFixture(t, config.Default())
	h := f.h
	addr := serveReal(t, h)
	pin := h.srv.deps.TLS.Pin()

	h.srv.deps.TailscaleCert = func(context.Context) (string, []byte, []byte, error) {
		return "", nil, nil, errors.New("your tailnet does not support getting TLS certs")
	}
	code, body := f.post(t, "/api/setup/tailscale_https", `{"enable":true}`)
	if code != http.StatusBadGateway || !strings.Contains(body, "HTTPS certificates are on for your tailnet") {
		t.Fatalf("failed fetch = %d %s", code, body)
	}
	if env, _ := os.ReadFile(f.env); strings.Contains(string(env), "TAILSCALE_HTTPS") || h.srv.tsOn.Load() {
		t.Fatalf("a failed fetch was saved: %q", env)
	}

	h.srv.deps.TailscaleCert = fakeTailscaleCert(t, testTSName)
	if code, body := f.post(t, "/api/setup/tailscale_https", `{"enable":true}`); code != 200 || !strings.Contains(body, testTSName) {
		t.Fatalf("enable = %d %s", code, body)
	}
	if env, _ := os.ReadFile(f.env); !strings.Contains(string(env), "SHEEPDOG_WEB_TAILSCALE_HTTPS=true") {
		t.Errorf("env file = %q", env)
	}
	for _, sni := range []string{testTSName, strings.ToUpper(testTSName), testTSName + "."} {
		if leaf := servedTo(t, addr, sni); leaf.Subject.CommonName != testTSName {
			t.Errorf("SNI %q got %q, want the ts.net certificate", sni, leaf.Subject.CommonName)
		}
	}
	for _, sni := range []string{"", "other.tail1234.ts.net", "sheepdog"} {
		if leaf := servedTo(t, addr, sni); SPKIPin(leaf.RawSubjectPublicKeyInfo) != pin {
			t.Errorf("SNI %q did not get the pinned key", sni)
		}
	}
	var hello HelloResponse
	h.getJSON("/api/hello", &hello)
	if hello.Security.TailscaleHTTPS != testTSName || hello.Security.Pin != pin {
		t.Errorf("hello security = %+v", hello.Security)
	}
	var st SetupState
	h.getJSON("/api/setup/state", &st)
	if !st.Phone.TailscaleHTTPS || st.Phone.TailscaleName != testTSName || st.Phone.TailscaleError != "" {
		t.Errorf("setup phone = %+v", st.Phone)
	}
	if got := h.srv.tailscaleHost("7777"); got != testTSName+":7777" {
		t.Errorf("link host = %q", got)
	}

	if code, _ := f.post(t, "/api/setup/tailscale_https", `{"enable":false}`); code != 200 {
		t.Fatalf("disable = %d", code)
	}
	if leaf := servedTo(t, addr, testTSName); SPKIPin(leaf.RawSubjectPublicKeyInfo) != pin {
		t.Error("the ts.net certificate is still served after turning it off")
	}
	if got := h.srv.tailscaleHost("7777"); got != "" {
		t.Errorf("link host after off = %q", got)
	}
	select {
	case r := <-f.restarts:
		t.Errorf("the switch restarted the daemon (%s)", r)
	default:
	}
}

// A certificate for another name, or a name outside ts.net, is refused.
func TestTailscaleHTTPSRefusesAWrongCertificate(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	wrong := fakeTailscaleCert(t, "other.tail1234.ts.net")
	h.srv.deps.TailscaleCert = func(ctx context.Context) (string, []byte, []byte, error) {
		_, c, k, err := wrong(ctx)
		return testTSName, c, k, err
	}
	if err := h.srv.fetchTailscale(context.Background()); err == nil {
		t.Error("a certificate for another name was served")
	}
	h.srv.deps.TailscaleCert = fakeTailscaleCert(t, "studio.example.com")
	if err := h.srv.fetchTailscale(context.Background()); err == nil || !strings.Contains(err.Error(), "ts.net") {
		t.Errorf("a non-ts.net name = %v", err)
	}
	if name, _ := h.srv.deps.TLS.Tailscale(); name != "" {
		t.Errorf("serving %q", name)
	}
}

// The browser link opens the ts.net name; the app's addresses stay IPs.
func TestPairLinkHostIsTheTailscaleName(t *testing.T) {
	addrs := []pair.Address{{URL: "https://100.101.102.103:7777", Kind: pair.KindTailscale}, {URL: "https://192.168.1.20:7777", Kind: pair.KindLAN}}
	link := pair.Link(pair.LinkInfo{DaemonID: "d", Name: "Studio", Pin: "p", Code: "c", Addresses: addrs, Host: testTSName + ":7777"})
	if !strings.HasPrefix(link, "https://"+testTSName+":7777/voice#pair=1&") {
		t.Errorf("link = %s", link)
	}
	in, err := pair.ParseLink(link)
	if err != nil || len(in.Addresses) != 2 || in.Addresses[0].URL != "https://100.101.102.103:7777" {
		t.Errorf("addresses = %+v %v", in.Addresses, err)
	}
}
