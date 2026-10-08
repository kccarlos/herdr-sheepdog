package brain_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// 2026-10-08 03:04:59 end to end: DeepSeek's endpoint reset the
// connection ("read tcp …: read: connection reset by peer") on the
// first request of an utterance turn. The real OpenAI-compatible adapter
// against a server that resets its first connection: the error is a
// transport error, the step is retried on the same provider over a new
// connection, and the turn answers — no fallback.
func TestConnectionResetIsRetriedEndToEnd(t *testing.T) {
	var calls atomic.Int32
	answer := ok(braintest.Reply("Keep your own adapter; no fork needed.", ""))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			if tc, ok := conn.(*net.TCPConn); ok {
				_ = tc.SetLinger(0) // close with RST: connection reset by peer
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer.body))
	}))
	t.Cleanup(srv.Close)

	m := newCompat(t, srv.URL, false, "", nil)
	log := &recLog{}
	b, err := New(Config{DataDir: t.TempDir(), RunID: "run1", Now: time.Now, Provider: ProviderDeepSeek, Model: DefaultDeepSeekModel}, m, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	b.SetHost(braintest.NewHost())

	rep, err := b.Turn(context.Background(), Request{Kind: KindUtterance, Question: "is there a better way than forking?"})
	if err != nil {
		t.Fatalf("turn fell back: %v", err)
	}
	if rep.Spoken != "Keep your own adapter; no fork needed." {
		t.Errorf("reply = %+v", rep)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("server calls = %d, want 2", n)
	}
	ev := log.ofType("brain.retry")
	if len(ev) != 1 || payload(ev[0])["reason"] != "transport" {
		t.Errorf("brain.retry = %v", ev)
	}
	if log.count("brain.fallback") != 0 {
		t.Error("brain.fallback logged")
	}
}
