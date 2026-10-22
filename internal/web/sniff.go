package web

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// One port, two protocols (docs/dev/design/security.md "One port, two
// protocols"): the listener reads the first byte of each connection; 0x16
// starts a TLS handshake, anything else is plain HTTP. Plain HTTP is
// served to loopback only (guard below).

// sniffTimeout is how long a new connection has to send its first byte.
const sniffTimeout = 10 * time.Second

// sniffListener hands out *tls.Conn for TLS connections (so net/http sets
// r.TLS) and the plain connection otherwise. The first byte is read in a
// goroutine per connection, so one idle socket never holds up the next.
type sniffListener struct {
	net.Listener
	tlsCfg  *tls.Config
	timeout time.Duration

	conns chan net.Conn
	errc  chan error
	done  chan struct{}
	once  sync.Once
}

func newSniffListener(ln net.Listener, cfg *tls.Config, timeout time.Duration) *sniffListener {
	l := &sniffListener{Listener: ln, tlsCfg: cfg, timeout: timeout, conns: make(chan net.Conn), errc: make(chan error, 1), done: make(chan struct{})}
	go l.loop()
	return l
}

func (l *sniffListener) loop() {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			select {
			case l.errc <- err:
			case <-l.done:
			}
			return
		}
		go l.sniff(c)
	}
}

func (l *sniffListener) sniff(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(l.timeout))
	br := bufio.NewReader(c)
	b, err := br.Peek(1)
	if err != nil {
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	var out net.Conn = &peekedConn{Conn: c, r: br}
	if b[0] == 0x16 {
		out = tls.Server(out, l.tlsCfg)
	}
	select {
	case l.conns <- out:
	case <-l.done:
		_ = c.Close()
	}
}

func (l *sniffListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case err := <-l.errc:
		return nil, err
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *sniffListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

// peekedConn reads through the buffer that holds the peeked byte.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekedConn) Read(b []byte) (int, error) { return p.r.Read(b) }

// isLoopbackHost reports whether a Host header (with or without a port)
// names this machine: localhost, 127.0.0.0/8 or ::1.
func isLoopbackHost(h string) bool {
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// isPageRoute reports whether a path is a page a browser opens (redirected
// to https), not an API route or an upgrade (refused with 426).
func isPageRoute(path string) bool {
	switch {
	case path == "/ws", path == "/term", path == "/events",
		strings.HasPrefix(path, "/api/"):
		return false
	}
	return true
}

// guard is the first check on every request:
//   - plain HTTP from another machine: a page is redirected (308) to the
//     same address over https, anything else is refused with 426
//     {"error":"tls_required"};
//   - from loopback, the Host must be a loopback name (127.0.0.1, ::1,
//     localhost), which stops a DNS-rebinding page.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loop := s.kindOf(r.RemoteAddr) == "loopback"
		if !loop && r.TLS == nil {
			s.log.Log(logSource, "web.tls_required", "", map[string]any{"path": r.URL.Path, "remote": r.RemoteAddr})
			if (r.Method == http.MethodGet || r.Method == http.MethodHead) && isPageRoute(r.URL.Path) {
				u := *r.URL
				u.Scheme, u.Host = "https", r.Host
				http.Redirect(w, r, u.String(), http.StatusPermanentRedirect)
				return
			}
			writeError(w, http.StatusUpgradeRequired, "tls_required")
			return
		}
		if loop && !isLoopbackHost(r.Host) {
			s.log.Log(logSource, "web.host_refused", "", map[string]any{"path": r.URL.Path, "host": r.Host})
			writeError(w, http.StatusForbidden, "host_not_allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}
