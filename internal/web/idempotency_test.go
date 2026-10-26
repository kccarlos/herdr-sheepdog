package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// idemClock is a manual clock.
type idemClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *idemClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *idemClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// idemServer serves h behind idempotent, with the device taken from the
// X-Test-Device header.
func idemServer(t *testing.T, clock *idemClock, h http.HandlerFunc) (*Server, *httptest.Server) {
	t.Helper()
	hh := newHarness(t, harnessOpts{})
	s := &Server{log: hh.srv.log, idem: newIdemStore(clock.now)}
	wrapped := s.idempotent(h)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), deviceCtxKey{}, r.Header.Get("X-Test-Device")))
		wrapped(w, r)
	}))
	t.Cleanup(ts.Close)
	return s, ts
}

func idemPost(t *testing.T, url, device, key string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
	if key != "" {
		req.Header.Set(idemHeader, key)
	}
	req.Header.Set("X-Test-Device", device)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// counting answers {"n": the number of runs} with status 201.
func counting(runs *atomic.Int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := runs.Add(1)
		writeJSON(w, http.StatusCreated, map[string]any{"n": n})
	}
}

// A repeat with the same key gets the first reply and runs nothing; a
// new key, another device or another route runs again.
func TestIdempotencyRepeatReplays(t *testing.T) {
	clock := &idemClock{t: time.Unix(1_000_000, 0)}
	var runs atomic.Int64
	_, ts := idemServer(t, clock, counting(&runs))

	code1, body1, _ := idemPost(t, ts.URL+"/api/utterance", "phone", "k1")
	code2, body2, hdr := idemPost(t, ts.URL+"/api/utterance", "phone", "k1")
	if code1 != http.StatusCreated || code2 != code1 || body1 != body2 || runs.Load() != 1 {
		t.Fatalf("repeat = %d %q then %d %q, runs %d", code1, body1, code2, body2, runs.Load())
	}
	if hdr.Get("Idempotent-Replayed") != "true" || hdr.Get("Content-Type") != "application/json" {
		t.Errorf("replay headers = %v", hdr)
	}
	idemPost(t, ts.URL+"/api/utterance", "phone", "k2")
	idemPost(t, ts.URL+"/api/utterance", "ipad", "k1")
	idemPost(t, ts.URL+"/api/agents", "phone", "k1")
	idemPost(t, ts.URL+"/api/utterance", "phone", "")
	idemPost(t, ts.URL+"/api/utterance", "phone", "")
	if runs.Load() != 6 {
		t.Errorf("runs = %d, want 6 (new key, other device, other route, two without a key)", runs.Load())
	}
	if code, _, _ := idemPost(t, ts.URL+"/api/utterance", "phone", strings.Repeat("x", 129)); code != http.StatusBadRequest {
		t.Errorf("long key = %d, want 400", code)
	}
	if code, _, _ := idemPost(t, ts.URL+"/api/utterance", "phone", "has space"); code != http.StatusBadRequest {
		t.Errorf("key with a space = %d, want 400", code)
	}
}

// A reply is kept for 10 minutes; after that the key runs again.
func TestIdempotencyExpiry(t *testing.T) {
	clock := &idemClock{t: time.Unix(1_000_000, 0)}
	var runs atomic.Int64
	s, ts := idemServer(t, clock, counting(&runs))
	idemPost(t, ts.URL+"/x", "phone", "k")
	clock.add(idemTTL - time.Second)
	idemPost(t, ts.URL+"/x", "phone", "k")
	if runs.Load() != 1 {
		t.Fatalf("runs before expiry = %d", runs.Load())
	}
	clock.add(2 * time.Second)
	_, body, hdr := idemPost(t, ts.URL+"/x", "phone", "k")
	if runs.Load() != 2 || !strings.Contains(body, `"n": 2`) || hdr.Get("Idempotent-Replayed") != "" {
		t.Errorf("after expiry: runs %d, body %q", runs.Load(), body)
	}
	// An expired entry of another key is pruned by the next new key.
	idemPost(t, ts.URL+"/x", "phone", "other")
	clock.add(idemTTL)
	idemPost(t, ts.URL+"/x", "phone", "fresh")
	if n := s.idem.len(); n != 1 {
		t.Errorf("entries after pruning = %d, want 1", n)
	}
}

// A repeat that arrives while the first is still running waits for it
// and gets its reply: the action runs once.
func TestIdempotencyRepeatWhileRunning(t *testing.T) {
	clock := &idemClock{t: time.Unix(1_000_000, 0)}
	var runs atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	_, ts := idemServer(t, clock, func(w http.ResponseWriter, r *http.Request) {
		n := runs.Add(1)
		started <- struct{}{}
		<-release
		writeJSON(w, http.StatusOK, map[string]any{"n": n})
	})
	var wg sync.WaitGroup
	bodies := make([]string, 3)
	for i := range bodies {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, bodies[i], _ = idemPost(t, ts.URL+"/api/agents", "phone", "spawn-1")
		}(i)
	}
	<-started
	time.Sleep(50 * time.Millisecond) // let the repeats arrive
	close(release)
	wg.Wait()
	if runs.Load() != 1 {
		t.Fatalf("runs = %d, want 1", runs.Load())
	}
	for _, b := range bodies {
		if b != bodies[0] {
			t.Errorf("bodies differ: %q", bodies)
		}
	}
}

// Memory is bounded: per device and in all, the oldest finished reply
// goes first; a reply over the size cap is not kept.
func TestIdempotencyBounded(t *testing.T) {
	clock := &idemClock{t: time.Unix(1_000_000, 0)}
	var runs atomic.Int64
	s, ts := idemServer(t, clock, counting(&runs))
	for i := 0; i < idemMaxPerDevice+10; i++ {
		clock.add(time.Millisecond)
		idemPost(t, ts.URL+"/x", "phone", fmt.Sprint("k", i))
	}
	if n := s.idem.len(); n != idemMaxPerDevice {
		t.Fatalf("one device's entries = %d, want %d", n, idemMaxPerDevice)
	}
	// The oldest were evicted, the newest are kept.
	before := runs.Load()
	idemPost(t, ts.URL+"/x", "phone", fmt.Sprint("k", idemMaxPerDevice+9))
	if runs.Load() != before {
		t.Error("the newest key ran again")
	}
	idemPost(t, ts.URL+"/x", "phone", "k0")
	if runs.Load() != before+1 {
		t.Error("the oldest key was still kept")
	}
	for d := 0; d < idemMax/idemMaxPerDevice+2; d++ {
		for i := 0; i < idemMaxPerDevice; i++ {
			clock.add(time.Millisecond)
			idemPost(t, ts.URL+"/x", fmt.Sprint("dev", d), fmt.Sprint("k", i))
		}
	}
	if n := s.idem.len(); n > idemMax {
		t.Errorf("entries = %d, over the cap %d", n, idemMax)
	}

	big := 0
	_, ts2 := idemServer(t, clock, func(w http.ResponseWriter, r *http.Request) {
		big++
		_, _ = w.Write([]byte(strings.Repeat("a", idemMaxBody+1)))
	})
	idemPost(t, ts2.URL+"/x", "phone", "big")
	_, body, _ := idemPost(t, ts2.URL+"/x", "phone", "big")
	if big != 2 || len(body) != idemMaxBody+1 {
		t.Errorf("a reply over the cap: ran %d times, body %d bytes", big, len(body))
	}
}

// Through the real routes: a typed line repeated with the same key
// reaches the voice once.
func TestIdempotencyTypedLine(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	send := func(key string) (int, string) {
		body, _ := json.Marshal(map[string]string{"text": "status please"})
		req, _ := http.NewRequest(http.MethodPost, h.http.URL+"/api/utterance", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(idemHeader, key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	c1, b1 := send("utter-1")
	c2, b2 := send("utter-1")
	if c1 != http.StatusOK || c2 != c1 || b1 != b2 {
		t.Fatalf("typed line: %d %q then %d %q", c1, b1, c2, b2)
	}
	if got := h.session.sentTexts(); len(got) != 1 {
		t.Errorf("voice got %q, want one line", got)
	}
	send("utter-2")
	if got := h.session.sentTexts(); len(got) != 2 {
		t.Errorf("a new key: voice got %q", got)
	}
}
