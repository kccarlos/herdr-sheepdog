package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Connection limits (docs/dev/design/security.md section 8). Loopback is
// never limited: the setup page must always open.
const (
	// maxRemoteConns is how many connections from non-loopback addresses
	// may be open at once.
	maxRemoteConns = 32
	// challengesPerMinute is GET /api/auth/challenge per source address.
	challengesPerMinute = 30
	// badSignaturesPerMinute bad sign-ins from one address block it.
	badSignaturesPerMinute = 10
	// badCodesPerMinute wrong or used enroll codes from one address block it.
	badCodesPerMinute = 5
	// limitBlock is how long a blocked address waits.
	limitBlock = 5 * time.Minute
)

// rateLimiter counts events per (bucket, source address) in one-minute
// windows, and blocks an address for limitBlock once a failure bucket
// overflows.
type rateLimiter struct {
	mu      sync.Mutex
	windows map[string]*rateWindow
	blocked map[string]time.Time // bucket+ip → until
}

type rateWindow struct {
	start time.Time
	n     int
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{windows: map[string]*rateWindow{}, blocked: map[string]time.Time{}}
}

// count adds one event and reports how many fell in the current window.
func (l *rateLimiter) count(bucket, ip string, now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.windows) > 4096 {
		for k, w := range l.windows {
			if now.Sub(w.start) > time.Minute {
				delete(l.windows, k)
			}
		}
	}
	k := bucket + "|" + ip
	w := l.windows[k]
	if w == nil || now.Sub(w.start) > time.Minute {
		w = &rateWindow{start: now}
		l.windows[k] = w
	}
	w.n++
	return w.n
}

// allow counts one event and reports whether it is within limit.
//
//nolint:unparam // one bucket per route; only the challenge uses it so far
func (l *rateLimiter) allow(bucket, ip string, limit int, now time.Time) bool {
	return l.count(bucket, ip, now) <= limit
}

// fail counts one failure; past limit, the address is blocked for bucket.
func (l *rateLimiter) fail(bucket, ip string, limit int, now time.Time) {
	if l.count(bucket, ip, now) > limit {
		l.mu.Lock()
		l.blocked[bucket+"|"+ip] = now.Add(limitBlock)
		l.mu.Unlock()
	}
}

// isBlocked reports whether ip waits out a block for bucket.
func (l *rateLimiter) isBlocked(bucket, ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.blocked[bucket+"|"+ip]
	if !ok {
		return false
	}
	if now.After(until) {
		delete(l.blocked, bucket+"|"+ip)
		return false
	}
	return true
}

// sourceIP is the host part of a remote address.
func sourceIP(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return host
}

// isLoopbackAddr reports whether a remote address is this machine.
func isLoopbackAddr(remote string) bool {
	ip := net.ParseIP(sourceIP(remote))
	return ip != nil && ip.IsLoopback()
}

// capListener closes connections from non-loopback addresses past max
// open ones. Each accepted connection is counted until it is closed,
// WebSocket upgrades included.
type capListener struct {
	net.Listener
	max  int
	mu   sync.Mutex
	open int
	// onRefuse runs for each connection turned away (logging).
	onRefuse func(remote string)
}

func (l *capListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if isLoopbackAddr(c.RemoteAddr().String()) {
			return c, nil
		}
		l.mu.Lock()
		if l.open >= l.max {
			l.mu.Unlock()
			if l.onRefuse != nil {
				l.onRefuse(c.RemoteAddr().String())
			}
			_ = c.Close()
			continue
		}
		l.open++
		l.mu.Unlock()
		return &countedConn{Conn: c, l: l}, nil
	}
}

// openRemote is how many non-loopback connections are open.
func (l *capListener) openRemote() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.open
}

type countedConn struct {
	net.Conn
	l    *capListener
	once sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() {
		c.l.mu.Lock()
		c.l.open--
		c.l.mu.Unlock()
	})
	return c.Conn.Close()
}

// writeRateLimited answers 429 with a reason the clients read.
func writeRateLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many attempts from this address. Wait a few minutes and try again.", "reason": "rate_limited"})
}
