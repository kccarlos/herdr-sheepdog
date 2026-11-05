package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Review item 5: a tap re-reads the screen before its keys go, but only a
// working pane or a screen without that prompt ends the question. agy
// reports done at its prompts, a finish that asks leaves the pane done or
// idle, and a menu shows on an idle pane: their cards answer as before.

// agyAtPrompt puts an agy pane at its approval prompt, reported done.
func agyAtPrompt(t *testing.T, h *harness) {
	t.Helper()
	h.addAgent(authPane, "auth", "agy", herdr.StateWorking)
	h.srv.SetScreen(authPane, fixture(t, "agy-approval-prompt.screen.txt"))
	h.transition(authPane, herdr.StateWorking, herdr.StateDone)
	if _, has := h.pending(); !has {
		t.Fatal("no pending for agy's prompt")
	}
}

func TestTapAnswersAPromptOnAPaneReportedDone(t *testing.T) {
	h := newHarness(t)
	agyAtPrompt(t, h)
	p, _ := h.pending()
	h.clock.Advance(time.Second)
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 1); err != nil {
		t.Fatalf("tap on agy's prompt: %v", err)
	}
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("keys to agy = %d, want 1", n)
	}
}

// A moved-past question on a pane reported done is tappable too.
func TestTapAnswersAMovedPastPromptOnAPaneReportedDone(t *testing.T) {
	h := newHarness(t)
	agyAtPrompt(t, h)
	p, _ := h.pending()
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	h.block(docsPane, "codex-approval-prompt.screen.txt")
	if cur, _ := h.pending(); cur.PaneID != docsPane {
		t.Fatalf("pending = %+v, want docs'", cur)
	}
	h.clock.Advance(time.Second)
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 1); err != nil {
		t.Fatalf("tap on agy's moved-past prompt: %v", err)
	}
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("keys to agy = %d, want 1", n)
	}
}

// The reconciler keeps a moved-past question on a pane reported done while
// its screen shows that prompt.
func TestReconcileKeepsAMovedPastPromptOnAPaneReportedDone(t *testing.T) {
	h := newHarness(t)
	agyAtPrompt(t, h)
	p, _ := h.pending()
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	h.block(docsPane, "codex-approval-prompt.screen.txt")
	h.clock.Advance(30 * time.Second)
	h.sup.reconcileReprompts(context.Background())
	if _, ok := h.store.Question(p.ID); !ok {
		t.Fatal("the reconciler dropped agy's open question")
	}
}

// A failed screen read sends nothing and keeps the question: the tap can
// be tried again.
func TestTapReadErrorKeepsTheQuestion(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	p, _ := h.pending()
	h.srv.Handle("pane.read", func(json.RawMessage) (any, error) { return nil, errors.New("boom") })
	h.clock.Advance(time.Second)
	_, err := h.sup.AnswerTap(context.Background(), p.ID, 1)
	if err == nil || errors.Is(err, ErrPendingGone) {
		t.Fatalf("tap err = %v, want a send failure that keeps the question", err)
	}
	if n := h.keysTo(authPane); n != 0 {
		t.Fatalf("keys sent without a screen read: %d", n)
	}
	if cur, has := h.pending(); !has || cur.ID != p.ID {
		t.Fatalf("pending = %+v, %v; want %s kept", cur, has, p.ID)
	}
}

// The screen read runs without the supervisor's lock: a slow herdr must not
// stall voice events and transitions.
func TestTapReadsTheScreenWithoutTheLock(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	p, _ := h.pending()
	screen := fixture(t, "codex-approval-prompt.screen.txt")
	var locked atomic.Bool
	h.srv.Handle("pane.read", func(json.RawMessage) (any, error) {
		if h.sup.mu.TryLock() {
			h.sup.mu.Unlock()
		} else {
			locked.Store(true)
		}
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": authPane, "source": "recent_unwrapped", "format": "text", "text": screen}}, nil
	})
	h.clock.Advance(time.Second)
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 1); err != nil {
		t.Fatalf("tap: %v", err)
	}
	if locked.Load() {
		t.Fatal("the tap held s.mu across the screen read")
	}
}
