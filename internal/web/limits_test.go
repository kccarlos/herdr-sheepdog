package web

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/termproto"
)

func TestRateLimiterWindowsAndBlocks(t *testing.T) {
	l := newRateLimiter()
	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < challengesPerMinute; i++ {
		if !l.allow("challenge", "192.0.2.9", challengesPerMinute, now) {
			t.Fatalf("challenge %d refused", i+1)
		}
	}
	if l.allow("challenge", "192.0.2.9", challengesPerMinute, now) {
		t.Error("challenge past the limit allowed")
	}
	if !l.allow("challenge", "192.0.2.10", challengesPerMinute, now) {
		t.Error("another address shares the limit")
	}
	if !l.allow("challenge", "192.0.2.9", challengesPerMinute, now.Add(61*time.Second)) {
		t.Error("a new minute did not reset the window")
	}
	for i := 0; i <= badCodesPerMinute; i++ {
		l.fail("enroll", "192.0.2.9", badCodesPerMinute, now)
	}
	if !l.isBlocked("enroll", "192.0.2.9", now.Add(time.Minute)) {
		t.Error("too many bad codes did not block")
	}
	if l.isBlocked("enroll", "192.0.2.9", now.Add(limitBlock+time.Second)) {
		t.Error("the block did not end")
	}
}

// The challenge route answers 429 past 30 a minute from one address;
// loopback is never limited.
func TestChallengeIsRateLimitedPerAddress(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	get := func(remote string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/auth/challenge", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.srv.handleChallenge(w, r)
		return w.Code
	}
	for i := 0; i < challengesPerMinute; i++ {
		if code := get("192.0.2.7:4000"); code != http.StatusOK {
			t.Fatalf("challenge %d = %d", i+1, code)
		}
	}
	if code := get("192.0.2.7:4001"); code != http.StatusTooManyRequests {
		t.Errorf("31st challenge = %d, want 429", code)
	}
	for i := 0; i < challengesPerMinute+5; i++ {
		if code := get("127.0.0.1:5000"); code != http.StatusOK {
			t.Fatalf("loopback challenge %d = %d", i+1, code)
		}
	}
}

// Wrong or used enroll codes from one address block it for a while.
func TestEnrollBadCodesBlockTheAddress(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	_, pub := newDeviceKey(t)
	enroll := func(code string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/devices/enroll", jsonBody(t, map[string]any{"code": code, "public_key": pub, "name": "x", "kind": DeviceIOS}))
		r.RemoteAddr = "192.0.2.8:4000"
		w := httptest.NewRecorder()
		h.srv.handleEnroll(w, r)
		return w.Code
	}
	for i := 0; i <= badCodesPerMinute; i++ {
		if code := enroll("wrong"); code != http.StatusGone {
			t.Fatalf("bad code %d = %d", i+1, code)
		}
	}
	if code := enroll(h.pairCode()); code != http.StatusTooManyRequests {
		t.Errorf("a good code while blocked = %d, want 429", code)
	}
}

// The listener turns away non-loopback connections past the cap and
// never counts loopback.
func TestCapListenerCountsOnlyRemoteConnections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	capped := &capListener{Listener: ln, max: 1}
	defer func() { _ = capped.Close() }()
	go func() {
		for {
			c, err := capped.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
	}
	if n := capped.openRemote(); n != 0 {
		t.Errorf("loopback counted: open = %d", n)
	}
	// A remote connection is counted until closed.
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	cc := &countedConn{Conn: a, l: capped}
	capped.open++
	_ = cc.Close()
	_ = cc.Close()
	if n := capped.openRemote(); n != 0 {
		t.Errorf("after close: open = %d", n)
	}
}

// Terminal windows: 4 per paired device across its connections, 8 per
// daemon; this computer has no per-device cap.
func TestTermReservePerDevice(t *testing.T) {
	ts := &termServer{}
	for i := 0; i < termproto.MaxChannels; i++ {
		if !ts.reserve("dev-a") {
			t.Fatalf("window %d refused", i+1)
		}
	}
	if ts.reserve("dev-a") {
		t.Error("a fifth window for one device")
	}
	for i := 0; i < termproto.MaxBridges-termproto.MaxChannels; i++ {
		if !ts.reserve(localDevice) {
			t.Fatalf("local window %d refused", i+1)
		}
	}
	if ts.reserve("dev-b") {
		t.Error("past the daemon's cap")
	}
	ts.unreserve("dev-a")
	if !ts.reserve("dev-b") {
		t.Error("a freed window was not reusable")
	}
}

func jsonBody(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b)
}
