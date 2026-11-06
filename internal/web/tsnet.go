package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// The Tailscale certificate (web.tailscale_https, docs/dev/design/security.md
// "Public *.ts.net certificates"): a real Let's Encrypt certificate for the
// computer's *.ts.net name, from `tailscale cert`, served only to a TLS
// client that asks for that name (SNI). Browsers opening the name get no
// warning. The app connects by address, sends no SNI and keeps getting the
// daemon's own pinned key. Off by default: issuing the certificate puts the
// name in the public certificate transparency logs.

// TailscaleCertFunc fetches the certificate for this computer's *.ts.net
// name: the name (lower case, no trailing dot) and the PEM certificate
// chain and key.
type TailscaleCertFunc func(ctx context.Context) (name string, certPEM, keyPEM []byte, err error)

// TailscaleFunnelFunc reports whether Tailscale Funnel is currently exposing
// port (or any port when port <= 0) on this computer.
type TailscaleFunnelFunc func(ctx context.Context, port int) (bool, error)

// AnyFunnelActive reports whether `tailscale serve status --json` output shows
// any active Funnel listener (background or foreground).
func AnyFunnelActive(data []byte) bool {
	cfg, ok := parseTailscaleServeConfig(data)
	if !ok {
		return false
	}
	for _, on := range cfg.AllowFunnel {
		if on {
			return true
		}
	}
	for _, fg := range cfg.Foreground {
		for _, on := range fg.AllowFunnel {
			if on {
				return true
			}
		}
	}
	return false
}

// FunnelTargetsPort reports whether `tailscale serve status --json` output
// shows an active Funnel forwarding to local port. If AllowFunnel is true on
// an entry whose handler schema is unrecognised, FunnelTargetsPort fails closed
// (returns true).
func FunnelTargetsPort(data []byte, port int) bool {
	cfg, ok := parseTailscaleServeConfig(data)
	if !ok {
		return false
	}
	if port <= 0 {
		return AnyFunnelActive(data)
	}
	if serveConfigTargetsPort(cfg, port) {
		return true
	}
	for _, fg := range cfg.Foreground {
		if serveConfigTargetsPort(fg, port) {
			return true
		}
	}
	return false
}

type tsServeConfig struct {
	TCP         map[string]tsTCPPortHandler  `json:"TCP"`
	Web         map[string]tsWebServerConfig `json:"Web"`
	AllowFunnel map[string]bool              `json:"AllowFunnel"`
	Foreground  map[string]tsServeConfig     `json:"Foreground"`
}

type tsTCPPortHandler struct {
	HTTPS      bool   `json:"HTTPS"`
	HTTP       bool   `json:"HTTP"`
	TCPForward string `json:"TCPForward"`
}

type tsWebServerConfig struct {
	Handlers map[string]tsHTTPHandler `json:"Handlers"`
}

type tsHTTPHandler struct {
	Proxy string `json:"Proxy"`
	Path  string `json:"Path"`
	Text  string `json:"Text"`
}

func parseTailscaleServeConfig(data []byte) (tsServeConfig, bool) {
	if len(bytes.TrimSpace(data)) == 0 {
		return tsServeConfig{}, false
	}
	var cfg tsServeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return tsServeConfig{}, false
	}
	return cfg, true
}

func serveConfigTargetsPort(cfg tsServeConfig, port int) bool {
	for hostPort, enabled := range cfg.AllowFunnel {
		if !enabled {
			continue
		}
		_, extPort, _ := net.SplitHostPort(hostPort)
		recognized := false
		if extPort != "" {
			if tcp, ok := cfg.TCP[extPort]; ok && tcp.TCPForward != "" {
				matched, valid := forwardTargetsPort(tcp.TCPForward, port)
				if !valid || matched {
					return true
				}
				recognized = true
			}
		}
		if webCfg, ok := lookupWebConfig(cfg.Web, hostPort); ok && len(webCfg.Handlers) > 0 {
			for _, h := range webCfg.Handlers {
				switch {
				case h.Proxy != "":
					matched, valid := proxyTargetsPort(h.Proxy, port)
					if !valid || matched {
						return true
					}
					recognized = true
				case h.Path != "" || h.Text != "":
					recognized = true
				default:
					return true
				}
			}
		}
		if !recognized {
			return true
		}
	}
	return false
}

func lookupWebConfig(web map[string]tsWebServerConfig, hostPort string) (tsWebServerConfig, bool) {
	if w, ok := web[hostPort]; ok {
		return w, true
	}
	low := strings.ToLower(hostPort)
	for k, w := range web {
		if strings.ToLower(k) == low {
			return w, true
		}
	}
	return tsWebServerConfig{}, false
}

func proxyTargetsPort(proxy string, port int) (matched, valid bool) {
	proxy = strings.TrimSpace(proxy)
	if strings.Contains(proxy, "://") {
		u, err := url.Parse(proxy)
		if err != nil || u.Host == "" {
			return false, false
		}
		if p := u.Port(); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n <= 0 || n > 65535 {
				return false, false
			}
			return n == port, true
		}
		switch strings.ToLower(u.Scheme) {
		case "http":
			return port == 80, true
		case "https", "https+insecure":
			return port == 443, true
		default:
			return false, false
		}
	}
	return forwardTargetsPort(proxy, port)
}

func forwardTargetsPort(addr string, port int) (matched, valid bool) {
	addr = strings.TrimSpace(addr)
	if _, p, err := net.SplitHostPort(addr); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return false, false
		}
		return n == port, true
	}
	n, err := strconv.Atoi(addr)
	if err != nil || n <= 0 || n > 65535 {
		return false, false
	}
	return n == port, true
}

// TailscaleFunnelCLI checks `tailscale serve status --json` when tailscale is
// installed.
func TailscaleFunnelCLI() TailscaleFunnelFunc {
	return func(ctx context.Context, port int) (bool, error) {
		bin := tailscaleBin()
		if bin == "" {
			return false, nil
		}
		cctx, cancel := context.WithTimeout(ctx, funnelCheckLimit)
		defer cancel()
		out, err := exec.CommandContext(cctx, bin, "serve", "status", "--json").Output() //nolint:gosec // bin is resolved via tailscaleBin()
		if err != nil {
			return false, nil
		}
		return FunnelTargetsPort(out, port), nil
	}
}

// tailscaleFetchTimeout bounds one `tailscale cert` run (it may wait for
// Let's Encrypt).
const tailscaleFetchTimeout = 90 * time.Second

// tailscaleRenewBefore re-fetches a certificate this close to its expiry
// (Let's Encrypt's last 30 days; tailscale renews it then).
const tailscaleRenewBefore = 30 * 24 * time.Hour

// tsCert is the Tailscale certificate the TLS side serves, if any.
type tsCert struct {
	mu      sync.RWMutex
	name    string
	cert    *tls.Certificate
	expires time.Time
}

func (c *tsCert) get() (string, *tls.Certificate, time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.name, c.cert, c.expires
}

func (c *tsCert) set(name string, cert *tls.Certificate, expires time.Time) {
	c.mu.Lock()
	c.name, c.cert, c.expires = name, cert, expires
	c.mu.Unlock()
}

// normalizeHost is a DNS name as compared: lower case, no trailing dot.
func normalizeHost(s string) string { return strings.TrimSuffix(strings.ToLower(s), ".") }

// SetTailscale serves cert to clients that ask for name (SNI); a nil cert
// stops it.
func (t *TLSIdentity) SetTailscale(name string, cert *tls.Certificate) {
	if cert == nil {
		t.ts.set("", nil, time.Time{})
		return
	}
	var exp time.Time
	if cert.Leaf != nil {
		exp = cert.Leaf.NotAfter
	}
	t.ts.set(normalizeHost(name), cert, exp)
}

// Tailscale is the served *.ts.net name and its certificate's expiry, ""
// when none is served.
func (t *TLSIdentity) Tailscale() (string, time.Time) {
	name, cert, exp := t.ts.get()
	if cert == nil {
		return "", time.Time{}
	}
	return name, exp
}

// certFor is the certificate for a ClientHello: the Tailscale one when it
// asks for that name, else the daemon's own.
func (t *TLSIdentity) certFor(hello *tls.ClientHelloInfo) *tls.Certificate {
	if hello != nil && hello.ServerName != "" {
		if name, cert, _ := t.ts.get(); cert != nil && normalizeHost(hello.ServerName) == name {
			return cert
		}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	c := t.cur.cert
	return &c
}

// fetchTailscale gets the certificate through Deps.TailscaleCert and
// serves it.
func (s *Server) fetchTailscale(ctx context.Context) error {
	if s.deps.TailscaleCert == nil {
		return errors.New("tailscale is not available on this computer")
	}
	ctx, cancel := context.WithTimeout(ctx, tailscaleFetchTimeout)
	defer cancel()
	name, certPEM, keyPEM, err := s.deps.TailscaleCert(ctx)
	if err != nil {
		return err
	}
	name = normalizeHost(name)
	if !strings.HasSuffix(name, ".ts.net") {
		return fmt.Errorf("tailscale named this computer %q, not a .ts.net name", name)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("tailscale cert: %w", err)
	}
	if cert.Leaf == nil {
		if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return fmt.Errorf("tailscale cert: %w", err)
		}
	}
	if err := cert.Leaf.VerifyHostname(name); err != nil {
		return fmt.Errorf("tailscale cert: %w", err)
	}
	s.deps.TLS.SetTailscale(name, &cert)
	s.log.Log(logSource, "web.tls.tsnet", "", map[string]any{"name": name, "expires": cert.Leaf.NotAfter.UTC()})
	return nil
}

// refreshTailscale fetches the certificate when the switch is on and
// there is none yet or it is near its expiry; a failure is logged and
// kept for the setup page.
func (s *Server) refreshTailscale(ctx context.Context) {
	if !s.tsOn.Load() {
		return
	}
	if name, exp := s.deps.TLS.Tailscale(); name != "" && exp.Sub(s.deps.Now()) > tailscaleRenewBefore {
		return
	}
	err := s.fetchTailscale(ctx)
	s.tsErr.Store(errText(err))
	if err != nil {
		s.log.Log(logSource, "web.tls.tsnet_failed", "", map[string]any{"error": err.Error()})
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// watchTailscale keeps the Tailscale certificate fresh until ctx ends.
func (s *Server) watchTailscale(ctx context.Context) {
	s.refreshTailscale(ctx)
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refreshTailscale(ctx)
		}
	}
}

// tailscaleHost is the name:port the pairing code's browser link uses
// while the Tailscale certificate is served, else "".
func (s *Server) tailscaleHost(port string) string {
	name, _ := s.deps.TLS.Tailscale()
	if name == "" || !s.tsOn.Load() {
		return ""
	}
	return name + ":" + port
}

// handleSetupTailscaleHTTPS is POST /api/setup/tailscale_https {"enable"}
// (setup page only). Turning it on fetches the certificate first and
// saves nothing when that fails, so the switch never claims a certificate
// the daemon cannot serve.
func (s *Server) handleSetupTailscaleHTTPS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enable bool `json:"enable"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if req.Enable {
		if err := s.fetchTailscale(r.Context()); err != nil {
			s.log.Log(logSource, "web.tls.tsnet_failed", "", map[string]any{"error": err.Error()})
			writeError(w, http.StatusBadGateway, "Tailscale did not give this computer a certificate: "+err.Error()+". "+tailscaleCertFix)
			return
		}
	}
	if err := onboard.UpsertEnvFile(s.deps.Setup.EnvPath, map[string]string{"SHEEPDOG_WEB_TAILSCALE_HTTPS": strconv.FormatBool(req.Enable)}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.tsOn.Store(req.Enable)
	s.tsErr.Store("")
	if !req.Enable {
		s.deps.TLS.SetTailscale("", nil)
	}
	name, _ := s.deps.TLS.Tailscale()
	s.log.Log(logSource, "web.setup_saved", "", map[string]any{"step": "tailscale_https", "enable": req.Enable})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tailscale_https": req.Enable, "name": name})
}

// tailscaleCertFix is the plain fix for a refused certificate.
const tailscaleCertFix = "Check that Tailscale is running and logged in, and that HTTPS certificates are on for your tailnet (Tailscale admin console, DNS page), then try again"

// TailscaleCertCLI is the daemon's TailscaleCertFunc: `tailscale status
// --json` for the name, then `tailscale cert` into <dataDir>/tls/
// (tsnet.crt, tsnet.key, 0600).
func TailscaleCertCLI(dataDir string, port int) TailscaleCertFunc {
	return func(ctx context.Context) (string, []byte, []byte, error) {
		bin := tailscaleBin()
		if bin == "" {
			return "", nil, nil, errors.New("the tailscale command is not installed")
		}
		if serveOut, err := exec.CommandContext(ctx, bin, "serve", "status", "--json").Output(); err == nil && FunnelTargetsPort(serveOut, port) { //nolint:gosec // bin is resolved via tailscaleBin()
			return "", nil, nil, errors.New("Tailscale Funnel is active on this port (public internet); run `tailscale funnel reset` first")
		}
		out, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
		if err != nil {
			return "", nil, nil, fmt.Errorf("tailscale status: %w", err)
		}
		var st struct {
			Self struct {
				DNSName string `json:"DNSName"`
			} `json:"Self"`
		}
		if err := json.Unmarshal(out, &st); err != nil {
			return "", nil, nil, fmt.Errorf("tailscale status: %w", err)
		}
		name := normalizeHost(st.Self.DNSName)
		if name == "" {
			return "", nil, nil, errors.New("tailscale has no name for this computer (is it logged in?)")
		}
		dir := filepath.Join(dataDir, "tls")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", nil, nil, err
		}
		certFile, keyFile := filepath.Join(dir, "tsnet.crt"), filepath.Join(dir, "tsnet.key")
		cmd := exec.CommandContext(ctx, bin, "cert", "--cert-file", certFile, "--key-file", keyFile, name)
		if msg, err := cmd.CombinedOutput(); err != nil {
			return "", nil, nil, fmt.Errorf("tailscale cert: %s", strings.TrimSpace(string(msg)))
		}
		_ = os.Chmod(keyFile, 0o600)
		certPEM, err := os.ReadFile(certFile)
		if err != nil {
			return "", nil, nil, err
		}
		keyPEM, err := os.ReadFile(keyFile)
		if err != nil {
			return "", nil, nil, err
		}
		return name, certPEM, keyPEM, nil
	}
}

// tailscaleBin is the tailscale CLI: on PATH, else inside the macOS app.
func tailscaleBin() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	const app = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
	if _, err := os.Stat(app); err == nil {
		return app
	}
	return ""
}
