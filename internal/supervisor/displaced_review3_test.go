package supervisor

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// The displacement rail after the 2026-11-04 bench
// (docs/dev/eval/approval-rail-2026-11-04.md, "Refused answers") and the
// third review: a question the orchestrator told the user about counts as
// told, words that name the agent are about it, a repeated answer after a
// confirmed one is refused without a re-ask, and a muted space's question
// stays out of the voice's reach.

// Bench case 1, by name: docs' question displaced auth's, and the user's
// words name docs ("approve docs's command"). They are about docs, not
// about auth's question they heard: send_keys to docs goes through. Words
// that name both agents, or neither, are still refused.
func TestWordsNamingTheDisplacingAgentAnswerIt(t *testing.T) {
	for _, tc := range []struct {
		words string
		sent  bool
	}{
		{"Yes, once, approve docs's command, that one run.", true},
		{"Yes, approve it.", false},
		{"Yes, approve auth and docs.", false},
	} {
		t.Run(tc.words, func(t *testing.T) {
			h := toldAuth(t)
			blockDocs(t, h)
			h.clock.Advance(500 * time.Millisecond)
			h.hear(tc.words)
			p, _ := h.pending()
			_, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, p.YesKeys)
			if got := h.keysTo(docsPane); (got == 1) != tc.sent {
				t.Fatalf("%q: keys to docs = %d (err %v), want sent=%v", tc.words, got, err, tc.sent)
			}
		})
	}
}

// Naming the agent does not cover a same-pane change: auth's prompt
// changed as the user answered its earlier one, and "yes, auth" may be
// about the earlier prompt.
func TestWordsNamingTheAgentDoNotCoverItsChangedPrompt(t *testing.T) {
	h := toldAuth(t)
	h.srv.SetScreen(authPane, "running the tests")
	h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)
	h.clock.Advance(time.Second)
	h.block(authPane, "claude-permission-prompt.screen.txt")
	h.clock.Advance(300 * time.Millisecond)
	h.hear("Yes, auth can go ahead.")
	p, _ := h.pending()
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, p.YesKeys); err == nil {
		t.Fatal("words naming auth answered its new prompt, which the user had not heard")
	}
}

// Bench case 2: the user's yes went to docs and was confirmed; docs then
// shows the same prompt again, and the orchestrator sends y a second time
// on the same words. It is refused, and the user is not asked again: the
// answer they gave was delivered.
func TestRepeatedAnswerAfterAConfirmedOneIsRefusedSilently(t *testing.T) {
	h := toldAuth(t)
	h.speak(blockDocs(t, h))
	h.clock.Advance(time.Second)
	h.hear("Yes, go ahead, docs.")
	p, _ := h.pending()
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, p.YesKeys); err != nil {
		t.Fatalf("first send_keys: %v", err)
	}
	// docs still shows the prompt (it has not moved on yet): send_keys
	// revives it as a new question.
	h.clock.Advance(time.Second)
	from := len(h.pusher.all())
	_, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, p.YesKeys)
	if err == nil {
		t.Fatal("a second y on the same words was sent")
	}
	if strings.Contains(err.Error(), "asked them again") {
		t.Fatalf("refusal = %v, want no re-ask", err)
	}
	if line, ok := h.askedAgain(from, "docs"); ok {
		t.Fatalf("the user was asked again: %q", line)
	}
	if n := h.keysTo(docsPane); n != 1 {
		t.Fatalf("keys to docs = %d, want 1", n)
	}
}

// Review item 3.1: a status reply that mentions a muted space's question
// (the user did not ask about that agent) does not promote it: the voice's
// pending question stays the one the user heard, and the held one stays
// held. The roster does not hand the orchestrator its prompt either.
func TestReplyMentioningAHeldQuestionDoesNotPromoteIt(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	w.block(docsPane, "codex-approval-prompt.screen.txt")
	w.speak(w.lastPushText())
	w.mute(t)
	w.block(authPane, "codex-approval-prompt.screen.txt")
	q, ok := w.store.Question(firstQuestionOf(w.harness, authPane))
	if !ok || !w.sup.heldQuestion(q.ID) {
		t.Fatalf("auth's question not held: %+v", q)
	}
	for _, a := range (brainHost{w.sup}).Agents() {
		if a.PaneID == authPane && (a.Pending != nil || a.LastBlock != "") {
			t.Fatalf("the roster shows a held question: pending %+v, last block %q", a.Pending, a.LastBlock)
		}
	}
	w.clock.Advance(time.Second)
	w.sup.ShepherdInput(InputTyped, "How are things going?")
	w.sup.NoteReturnedText("Auth is paused on printf in your muted space.")
	if p, _ := w.pending(); p.PaneID != docsPane {
		t.Fatalf("pending = %+v, want docs' question the user heard", p)
	}
	if !w.sup.heldQuestion(q.ID) {
		t.Fatal("a reply the user did not ask for promoted the held question")
	}
	w.clock.Advance(time.Second)
	w.sup.ShepherdInput(InputTyped, "yes")
	if _, err := (brainHost{w.sup}).AnswerPrompt(context.Background(), authPane, q.YesKeys); err == nil {
		t.Fatal("a yes answered the muted space's question")
	}
}

// Review item 3.2: a moved-past question whose prompt changes in a space
// muted since it was asked is replaced held, and the old id's hold is not
// left behind.
func TestChangedQuestionInAMutedSpaceIsReplacedHeld(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	w.block(authPane, "codex-approval-prompt.screen.txt")
	w.block(docsPane, "codex-approval-prompt.screen.txt")
	q, ok := w.store.Question(firstQuestionOf(w.harness, authPane))
	if !ok || q.Held {
		t.Fatalf("auth's question = %+v", q)
	}
	w.mute(t)
	w.srv.SetScreen(authPane, fixture(t, "claude-permission-prompt.screen.txt"))
	row, _ := w.store.Get(authPane)
	w.sup.checkQuestion(context.Background(), row, q)
	nq, ok := w.store.Question(firstQuestionOf(w.harness, authPane))
	if !ok || nq.ID == q.ID {
		t.Fatalf("auth's question not replaced: %+v", nq)
	}
	if !nq.Held || !w.sup.heldQuestion(nq.ID) {
		t.Fatalf("replacement in a muted space not held: %+v", nq)
	}
	if w.sup.heldQuestion(q.ID) {
		t.Fatal("the replaced question's hold was left behind")
	}
}

// Review item 3.3: muting a space takes its open question out of the
// voice's reach, even one the user heard: a bare yes (the fast path) and
// send_keys answer nothing; the phone still can.
func TestMutingASpaceHoldsItsOpenQuestion(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.block(authPane, "codex-approval-prompt.screen.txt")
	w.speak(w.lastPushText())
	p, ok := w.pending()
	if !ok || p.PaneID != authPane {
		t.Fatalf("pending = %+v", p)
	}
	if _, err := w.sup.SetSpaceMute(context.Background(), "auth", true, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	if cur, ok := w.pending(); ok && cur.PaneID == authPane {
		t.Fatalf("auth's question is still the voice's after its space was muted: %+v", cur)
	}
	if !w.sup.heldQuestion(p.ID) {
		t.Fatal("auth's question not held after its space was muted")
	}
	w.clock.Advance(time.Second)
	w.say("yes")
	w.clock.Advance(time.Second)
	w.sup.ShepherdInput(InputTyped, "yes, auth can go ahead")
	_, _ = (brainHost{w.sup}).AnswerPrompt(context.Background(), authPane, p.YesKeys)
	if n := w.keysTo(authPane); n != 0 {
		t.Fatalf("a muted space's question got %d key presses by voice", n)
	}
	if _, err := w.sup.AnswerTap(context.Background(), p.ID, 1); err != nil {
		t.Fatalf("tap: %v", err)
	}
}

// Review item 3.3, fast path: a question recorded unheld while the
// folder index was cold (its space muted) is not answered by a bare yes.
func TestFastPathRefusesAMutedSpacesQuestion(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.block(authPane, "codex-approval-prompt.screen.txt")
	w.mute(t)
	w.clock.Advance(time.Second)
	w.say("yes")
	if n := w.keysTo(authPane); n != 0 {
		t.Fatalf("a bare yes answered a muted space's question (%d key presses)", n)
	}
}

// Review item 4.1: the held questions announced on unmute run on tracked
// work, so a daemon going down starts none (and waits for those running).
func TestUnmuteAnnouncesNothingOnceShutdownBegan(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	ctx := context.Background()
	if _, err := w.sup.SetSpaceMute(ctx, "auth", true, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	w.block(authPane, "codex-approval-prompt.screen.txt")
	w.sup.shutdown()
	if _, err := w.sup.SetSpaceMute(ctx, "auth", false, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := w.pushTexts(); len(got) != 0 {
		t.Fatalf("pushes after shutdown = %v", got)
	}
}

// Review item 4.1: two held questions in one space are announced one
// after the other, so the question the voice is on is the last one spoken.
func TestUnmuteAnnouncesHeldQuestionsInTurn(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	ctx := context.Background()
	w.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	w.srv.SetPane(herdr.PaneInfo{PaneID: docsPane, WorkspaceID: "auth", Cwd: w.path})
	if _, err := w.sup.SetSpaceMute(ctx, "auth", true, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	w.block(authPane, "codex-approval-prompt.screen.txt")
	w.clock.Advance(time.Second)
	w.block(docsPane, "codex-approval-prompt.screen.txt")
	if n := len(w.sup.heldQuestions()); n != 2 {
		t.Fatalf("held = %d, want 2", n)
	}
	if _, err := w.sup.SetSpaceMute(ctx, "auth", false, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(w.pushTexts()) < 2 || w.log.count("pending.promoted") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("held questions not announced: %v", w.pushTexts())
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := w.pushTexts()
	p, _ := w.pending()
	if !strings.Contains(got[len(got)-1], w.sup.sayOf(p.PaneID)) {
		t.Fatalf("pending is %s but the last question spoken was %q", p.PaneID, got[len(got)-1])
	}
	if !strings.Contains(got[0], "auth") {
		t.Fatalf("pushes = %v, want auth's (the older) first", got)
	}
}

// Review item 4.2: send_keys that revives a question reads the screen
// without holding s.mu.
func TestSendKeysRevivalReadsTheScreenWithoutTheLock(t *testing.T) {
	h := toldAuth(t)
	h.speak(blockDocs(t, h))
	h.clock.Advance(time.Second)
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
	h.hear("Yes to auth.")
	q, _ := h.store.Question(firstQuestionOf(h, authPane))
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, q.YesKeys); err != nil {
		t.Fatalf("send_keys to auth: %v", err)
	}
	if locked.Load() {
		t.Fatal("send_keys held s.mu across the screen read")
	}
}
