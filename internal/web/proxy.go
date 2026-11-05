package web

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	funnelCacheTTL   = 5 * time.Second
	funnelCheckLimit = 2 * time.Second
)

// funnelCache caches Deps.TailscaleFunnel answers per port and coalesces
// concurrent checks so bursts of loopback requests do not spawn bursts of
// subprocesses.
type funnelCache struct {
	mu       sync.Mutex
	checked  time.Time
	port     int
	active   bool
	inflight chan struct{}
}

// isProxiedRequest reports whether r carries reverse-proxy or tunnel headers
// showing the HTTP request was forwarded rather than originated by a local
// process on this computer.
func isProxiedRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	for k, vals := range r.Header {
		nonEmpty := false
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				nonEmpty = true
				break
			}
		}
		if !nonEmpty {
			continue
		}
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-forwarded-") {
			return true
		}
		switch lk {
		case "forwarded",
			"x-real-ip",
			"tailscale-funnel-request",
			"tailscale-user-login",
			"tailscale-user-name",
			"tailscale-headers-info",
			"cf-connecting-ip",
			"true-client-ip":
			return true
		}
	}
	return false
}

// firstForwardedValue returns the first comma-separated token of v.
func firstForwardedValue(v string) string {
	first, _, _ := strings.Cut(v, ",")
	return strings.TrimSpace(first)
}

// forwardedHTTPS reports whether proxy headers mark the client-facing hop as
// HTTPS (so browser session cookies keep the Secure attribute behind an HTTPS
// reverse proxy).
func forwardedHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.EqualFold(firstForwardedValue(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	for _, entry := range strings.Split(r.Header.Get("Forwarded"), ",") {
		for _, part := range strings.Split(entry, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(k), "proto") {
				continue
			}
			if strings.EqualFold(strings.Trim(strings.TrimSpace(v), `"`), "https") {
				return true
			}
		}
	}
	return false
}

// rateLimitSource keys sign-in and pairing rate limits. Direct remote peers
// are keyed by TCP source IP; proxied loopback requests are keyed in a
// separate "proxy:" namespace using the proxy-appended client address so a
// tunnel brute-force attempt gets 429 without blocking genuine local callers.
func rateLimitSource(r *http.Request) string {
	if !isLoopbackAddr(r.RemoteAddr) {
		return sourceIP(r.RemoteAddr)
	}
	for _, hdr := range []string{"CF-Connecting-IP", "True-Client-IP", "X-Real-IP"} {
		if v := strings.TrimSpace(r.Header.Get(hdr)); v != "" {
			return "proxy:" + v
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			if hop := strings.TrimSpace(parts[i]); hop != "" {
				return "proxy:" + hop
			}
		}
	}
	return "proxy:loopback"
}

// isUntrustedLoopback returns why a loopback TCP peer must not receive
// ticketless local authority ("proxied", "tailscale_funnel", or "" when the
// caller is trusted local loopback).
func (s *Server) isUntrustedLoopback(r *http.Request) string {
	if isProxiedRequest(r) {
		return "proxied"
	}
	if s.funnelActive(r.Context(), r) {
		return "tailscale_funnel"
	}
	return ""
}

// requestKind classifies r's network path: a loopback request arriving through
// a reverse proxy or while Funnel is active is recorded as "proxy", never
// "loopback" ("this computer").
func (s *Server) requestKind(r *http.Request) string {
	kind := s.kindOf(r.RemoteAddr)
	if kind == "loopback" && s.isUntrustedLoopback(r) != "" {
		return "proxy"
	}
	return kind
}

// isRateLimitedCaller reports whether r should pass through the per-source
// auth/enroll rate limiter.
func (s *Server) isRateLimitedCaller(r *http.Request) bool {
	return !isLoopbackAddr(r.RemoteAddr) || s.isUntrustedLoopback(r) != ""
}

// hasValidTicket reports (without mutating state) whether r presents an
// unexpired session ticket belonging to an active paired device.
func (s *Server) hasValidTicket(r *http.Request) bool {
	t := presentedTicket(r)
	if t == "" {
		return false
	}
	now := s.deps.Now()
	s.auth.mu.Lock()
	tk, ok := s.auth.tickets[t]
	s.auth.mu.Unlock()
	if !ok || now.After(tk.expires) {
		return false
	}
	d, found := s.devices.get(tk.device)
	return found && d.Revoked == nil
}

func (s *Server) targetPort(r *http.Request) int {
	if addr := s.Addr(); addr != "" {
		if _, p, err := net.SplitHostPort(addr); err == nil {
			if n, err := strconv.Atoi(p); err == nil && n > 0 {
				return n
			}
		}
	}
	if r != nil && r.Host != "" {
		if _, p, err := net.SplitHostPort(r.Host); err == nil {
			if n, err := strconv.Atoi(p); err == nil && n > 0 {
				return n
			}
		}
	}
	if s.deps.Setup != nil && s.deps.Setup.Config.Web.Port > 0 {
		return s.deps.Setup.Config.Web.Port
	}
	return 0
}

func (s *Server) funnelActive(ctx context.Context, r *http.Request) bool {
	fn := s.deps.TailscaleFunnel
	if fn == nil {
		return false
	}
	port := s.targetPort(r)

	s.funnel.mu.Lock()
	for {
		now := s.deps.Now()
		if !s.funnel.checked.IsZero() && s.funnel.port == port {
			if age := now.Sub(s.funnel.checked); age >= 0 && age < funnelCacheTTL {
				active := s.funnel.active
				s.funnel.mu.Unlock()
				return active
			}
		}
		if s.funnel.inflight != nil {
			wait := s.funnel.inflight
			s.funnel.mu.Unlock()
			select {
			case <-wait:
				s.funnel.mu.Lock()
				continue
			case <-ctx.Done():
				return false
			}
		}
		ch := make(chan struct{})
		s.funnel.inflight = ch
		s.funnel.mu.Unlock()

		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), funnelCheckLimit)
		active, err := fn(checkCtx, port)
		cancel()
		if err != nil {
			active = false
		}

		s.funnel.mu.Lock()
		s.funnel.checked = s.deps.Now()
		s.funnel.port = port
		s.funnel.active = active
		s.funnel.inflight = nil
		close(ch)
		s.funnel.mu.Unlock()
		return active
	}
}
