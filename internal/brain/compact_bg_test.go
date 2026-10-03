package brain_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// slowSummary is a fake model whose compaction summary call blocks until
// release is closed (or the call's context ends: Reset, Close); every
// other call answers at once.
type slowSummary struct {
	started   chan struct{}
	startOnce sync.Once
	release   chan struct{}
	mu        sync.Mutex
	// inFlight / maxInFlight count summary calls running at once.
	inFlight, maxInFlight int
}

func newSlowSummary() *slowSummary {
	return &slowSummary{started: make(chan struct{}), release: make(chan struct{})}
}

func (s *slowSummary) model() *braintest.Model {
	m := braintest.NewModel()
	m.Default = &braintest.Step{FnCtx: func(ctx context.Context, call braintest.Call) (*genai.GenerateContentResponse, error) {
		if !strings.Contains(call.Text(), "Summarise the following supervisor transcript") {
			return braintest.Response(braintest.Step{Text: braintest.Reply("Answer.", "")}), nil
		}
		s.mu.Lock()
		s.inFlight++
		s.maxInFlight = max(s.maxInFlight, s.inFlight)
		s.mu.Unlock()
		defer func() { s.mu.Lock(); s.inFlight--; s.mu.Unlock() }()
		s.startOnce.Do(func() { close(s.started) })
		select {
		case <-s.release:
			return braintest.Response(braintest.Step{Text: "The CEO asked about auth many times."}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
			return nil, errors.New("summary never released")
		}
	}}
	return m
}

func fillWindow(h *harness) {
	for i := 0; i < 40; i++ {
		h.brain.Heard(fmt.Sprintf("utterance number %d about auth and its tests", i), true)
		h.brain.Said(fmt.Sprintf("reply number %d", i), true)
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// The compaction's summary call runs off the turn lock: an utterance
// arriving while a slow summary is being written is answered at once,
// and the summary still lands afterwards, keeping what the second turn
// added (2026-10 logs: compactions p90 13.2 s, max 14 s, all of it
// holding the lock).
func TestCompactionDoesNotHoldTheTurnLock(t *testing.T) {
	s := newSlowSummary()
	h := newHarness(t, s.model(), func(c *Config) { c.WindowTokens = 300 })
	fillWindow(h)
	if _, err := h.ask("how is auth?", "auth"); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, s.started, "the summary call")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t0 := time.Now()
	rep, err := h.brain.Turn(ctx, Request{Kind: KindUtterance, Question: "and the docs agent?", Agent: "docs"})
	if err != nil {
		t.Fatalf("second turn waited for the compaction: %v", err)
	}
	if rep.Spoken != "Answer." || time.Since(t0) > time.Second {
		t.Errorf("second turn = %+v after %v", rep, time.Since(t0))
	}
	if n := h.log.count("brain.compaction"); n != 0 {
		t.Fatalf("compaction finished before the summary was released: %d", n)
	}

	close(s.release)
	h.brain.Quiesce()
	// The second turn's after-work may run before or after the first
	// compaction ends: one or two compactions, never two at once.
	comp := h.log.ofType("brain.compaction")
	if len(comp) == 0 || len(comp) > 2 {
		t.Fatalf("brain.compaction = %v", comp)
	}
	if p := payload(comp[0]); p["turn"] != 1 || p["error"] != nil || p["discarded"] != nil || p["dropped"] == nil {
		t.Errorf("brain.compaction = %v", p)
	}
	if s.maxInFlight != 1 {
		t.Errorf("summary calls at once = %d, want 1", s.maxInFlight)
	}
	win := h.brain.Engine().Assemble(0).Messages
	if win[0].Kind != KindCompaction {
		t.Errorf("window does not open with the summary: %+v", win[0])
	}
	var kept bool
	for _, m := range win {
		if m.Kind == KindRequest && strings.Contains(m.Content, "and the docs agent?") {
			kept = true
		}
	}
	if !kept && len(comp) == 1 {
		t.Error("the turn taken during the compaction is missing from the window")
	}
}

// A Reset during a compaction cancels it and waits for it: no summary of
// the old conversation lands in the new segment or window.
func TestResetCancelsCompactionInFlight(t *testing.T) {
	s := newSlowSummary()
	h := newHarness(t, s.model(), func(c *Config) { c.WindowTokens = 300 })
	fillWindow(h)
	if _, err := h.ask("how is auth?", "auth"); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, s.started, "the summary call")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := h.brain.Reset(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	h.brain.Quiesce()
	for _, m := range h.brain.Transcript().Messages() {
		if m.Kind == KindCompaction {
			t.Errorf("summary landed after the reset: %+v", m)
		}
	}
	if len(h.brain.Engine().Assemble(0).Messages) != 0 {
		t.Error("window not empty after the reset")
	}
	if _, ok := h.brain.LastCompaction(); ok {
		t.Error("LastCompaction reports the cancelled compaction")
	}
}

// The engine discards a summary when the messages it summarised changed
// while it was written (here: a Retract, as Brain.Echo does).
func TestCompactDiscardsWhenPrefixChanged(t *testing.T) {
	var e *DefaultEngine
	first := true
	e = NewDefaultEngine(2, func(ctx context.Context, text string) (string, error) {
		if first {
			first = false
			if !e.Retract(1) {
				return "", errors.New("message 1 not in the window")
			}
		}
		return "summary", nil
	}, nil)
	ts := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	for i := 1; i <= 20; i++ {
		e.Ingest(Message{Seq: i, TS: ts, Role: RoleUser, Kind: KindCEOUtterance, Content: strings.Repeat("word ", 20), Tokens: 25}, i)
	}
	_, ok, err := e.Compact(context.Background(), 100)
	if !errors.Is(err, ErrCompactionStale) || ok {
		t.Fatalf("Compact = %v, %v; want ErrCompactionStale", ok, err)
	}
	win := e.Assemble(0).Messages
	if len(win) != 19 || win[0].Seq != 2 {
		t.Errorf("window after a discarded compaction: %d messages, first seq %d", len(win), win[0].Seq)
	}
	// Nothing changes during the next one: it lands.
	if _, ok, err := e.Compact(context.Background(), 100); err != nil || !ok {
		t.Fatalf("retry Compact = %v, %v", ok, err)
	}
	if e.Assemble(0).Messages[0].Kind != KindCompaction {
		t.Error("retry did not splice the summary in")
	}
}
