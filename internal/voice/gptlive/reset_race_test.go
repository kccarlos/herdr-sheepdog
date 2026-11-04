package gptlive

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// gateDialer holds every dial until released, so a test can act while
// a session is still opening.
type gateDialer struct {
	*fakeDialer
	dialing chan struct{}
	gate    chan struct{}
	once    sync.Once
}

func (g *gateDialer) Dial(ctx context.Context, url, key string) (Conn, error) {
	g.once.Do(func() { close(g.dialing) })
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return g.fakeDialer.Dial(ctx, url, key)
}

// A Reset while a session is still opening drops the conversation: the
// connection that finishes opening afterwards carries the pre-reset
// recap and audio, so it is closed, not installed.
func TestResetDuringOpenDoesNotInstallThePreResetConnection(t *testing.T) {
	g := &gateDialer{fakeDialer: &fakeDialer{}, dialing: make(chan struct{}), gate: make(chan struct{})}
	l := &recLog{}
	cfg := testConfig()
	cfg.IdleClose = 10 * time.Second // only the reset may close it
	s, err := newSession(cfg, l, g)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	go func() {
		for range s.Audio() {
		}
	}()
	t.Cleanup(func() { _ = s.Close() })

	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	select {
	case <-g.dialing:
	case <-time.After(2 * time.Second):
		t.Fatal("no dial")
	}
	if err := s.Reset("new_conversation"); err != nil {
		t.Fatal(err)
	}
	close(g.gate)
	waitFor(t, "the dial to finish", func() bool { return g.count() == 1 })
	waitFor(t, "the pre-reset connection to close", func() bool {
		select {
		case <-g.last().closed:
			return true
		default:
			return false
		}
	})
	if s.Live() {
		t.Error("the connection opened before the reset became the live session")
	}
	s.mu.Lock()
	held := len(s.pending)
	s.mu.Unlock()
	if held != 0 {
		t.Errorf("%d audio chunks held from before the reset", held)
	}
}

// A delegation waiting for its utterance when its session retires must
// not take the words that belong to the session that replaced it.
func TestRetiredDelegationDoesNotClaimTheNewSessionsWords(t *testing.T) {
	cfg := testConfig()
	calls := make(chan voice.ToolCall, 4)
	cfg.Handler = func(_ context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		calls <- call
		return voice.ToolResult{Spoken: "ok"}, nil
	}
	cfg.Classify = func(text string) (voice.ToolCall, bool) {
		return voice.ToolCall{Name: voice.ToolToSupervisor, Args: map[string]any{"text": text}}, true
	}
	s, _, _ := startSession(t, cfg)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	s.mu.Lock()
	lc := s.live
	s.inputOpen, s.lastInput = true, s.now() // not settled: the wait goes on
	s.mu.Unlock()

	s.onDelegation(lc, "item_old", 0)
	time.Sleep(40 * time.Millisecond)
	if err := s.Reset("new_conversation"); err != nil {
		t.Fatal(err)
	}
	// The next session's words arrive before the old wait wakes.
	s.mu.Lock()
	s.unclaimed = "words for the new session"
	s.mu.Unlock()

	select {
	case call := <-calls:
		t.Fatalf("the retired delegation ran %+v on the new session's words", call)
	case <-time.After(delegationSettle + 600*time.Millisecond):
	}
	s.mu.Lock()
	left := s.unclaimed
	s.mu.Unlock()
	if left != "words for the new session" {
		t.Errorf("unclaimed = %q, want the new session's words left alone", left)
	}
}
