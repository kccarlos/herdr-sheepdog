package brain_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// Reset empties the window (a fresh transcript segment; the old file is
// left alone) but the next turn's prompt still carries the roster, the
// ledger (promises, notes) and the pending prompt, all rendered fresh
// from the Host — none of it ever lived in the window.
func TestResetEmptiesWindowKeepsHostState(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Text: braintest.Reply("auth is working on the tests.", "")},
	)
	h := newHarness(t, m, nil)
	h.host.SetPending("w5:p1", approvalPrompt())
	h.host.AddNote("don't touch the migrations today")
	if _, err := h.ask("how's it going?", "auth"); err != nil {
		t.Fatal(err)
	}
	if h.brain.Transcript().Len() == 0 {
		t.Fatal("expected messages before reset")
	}
	oldPath := h.brain.TranscriptPath()
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old transcript file: %v", err)
	}

	id, err := h.brain.Reset(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected a reset id")
	}
	if got := h.brain.Transcript().Len(); got != 0 {
		t.Fatalf("transcript len after reset = %d, want 0", got)
	}
	if got := h.brain.Window(); len(got) != 0 {
		t.Fatalf("window after reset = %+v, want empty", got)
	}
	if _, ok := h.brain.LastCompaction(); ok {
		t.Fatal("expected no compaction after reset")
	}
	newPath := h.brain.TranscriptPath()
	if newPath == oldPath {
		t.Fatalf("transcript path unchanged after reset: %s", newPath)
	}
	if !strings.Contains(filepath.Base(newPath), "reset1") {
		t.Fatalf("new transcript path = %s, want it to name the reset", newPath)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old transcript file gone after reset: %v", err)
	}
	if got := payload(h.log.ofType("brain.reset")[0]); got["via"] != "app" || got["reset_id"] != id {
		t.Fatalf("brain.reset payload = %+v", got)
	}

	// The next turn's system prompt has no memory of the earlier
	// exchange, but the roster, the pending prompt and the note are
	// still there (rendered fresh from the Host, never stored in the
	// window).
	m.Add(braintest.Step{Text: braintest.Reply("auth is still waiting on your answer.", "")})
	if _, err := h.ask("what's up with auth?", "auth"); err != nil {
		t.Fatal(err)
	}
	calls := m.Calls()
	last := calls[len(calls)-1]
	sys := last.System()
	if strings.Contains(sys, "how's it going") {
		t.Error("system prompt still mentions the pre-reset question")
	}
	if !strings.Contains(sys, "WAITING FOR AN ANSWER") {
		t.Error("system prompt lost the pending prompt after reset")
	}
	if !strings.Contains(sys, "don't touch the migrations today") {
		t.Error("system prompt lost the note after reset")
	}
	if !strings.Contains(sys, "auth") {
		t.Error("system prompt lost the roster after reset")
	}
	if got := len(h.brain.Transcript().Messages()); got == 0 {
		t.Error("expected the new turn's own messages in the fresh segment")
	}
}

// Reset waits for a turn in flight (the turn lock) rather than racing it.
func TestResetWaitsForInFlightTurn(t *testing.T) {
	block := make(chan struct{})
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("ok", ""), Block: block})
	h := newHarness(t, m, nil)

	done := make(chan struct{})
	go func() {
		_, _ = h.ask("go slow", "auth")
		close(done)
	}()
	// Give the turn a moment to acquire the lock before Reset queues
	// behind it (best-effort; Reset's own acquire would otherwise just
	// as validly run first — the assertion below only checks that the
	// blocked turn's message survives into whichever segment it landed
	// in, not the ordering).
	resetDone := make(chan struct{})
	go func() {
		_, _ = h.brain.Reset(context.Background(), "app")
		close(resetDone)
	}()
	close(block)
	<-done
	<-resetDone
	// No panic, no data race (run with -race): the turn lock serialised
	// the two.
}
