package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

func TestReview4ShortNames(t *testing.T) {
	for _, tc := range []struct{ name, words string }{
		{"db", "yes approve db"}, {"go", "yes approve go"}, {"do", "yes approve do"},
		{"ok", "yes approve ok"}, {"a", "yes approve a"}, {"1", "yes approve 1"},
		{"go-1", "yes approve go-1"}, {"甲", "请批准甲"}, {"数据库", "请批准数据库"}, {"db", "请批准db"},
	} {
		t.Run(tc.words, func(t *testing.T) {
			h := newHarness(t)
			h.addAgent(docsPane, tc.name, "codex", herdr.StateWorking)
			if _, err := h.store.Update(docsPane, func(a *state.Agent) { a.Alias = tc.name; a.Spoken = tc.name }); err != nil {
				t.Fatal(err)
			}
			h.block(docsPane, "codex-approval-prompt.screen.txt")
			p, _ := h.pending()
			h.sup.OnVoiceEvent(voice.EventPush{Text: tc.name, Urgency: voice.UrgencyIdle, PendingID: p.ID})
			t0 := h.clock.Now()
			h.sup.OnVoiceEvent(voice.EventPlayed{Text: tc.name + " asks for approval.", Start: t0, End: t0.Add(time.Second)})
			if !h.sup.toldOpenLocked(p) {
				t.Fatalf("short/Chinese alias %q was not told by whole run", tc.name)
			}
			h.clock.Advance(2 * time.Second)
			h.hear(tc.words)
			if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, p.YesKeys); err != nil {
				t.Fatalf("naming %q in %q failed: %v", tc.name, tc.words, err)
			}
		})
	}
}

func TestReview4RefusedRevivalKeepsPending(t *testing.T) {
	h := toldAuth(t)
	h.speak(blockDocs(t, h))
	before, _ := h.pending()
	h.clock.Advance(time.Second)
	h.hear("no")
	q, _ := h.store.Question(firstQuestionOf(h, authPane))
	_, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, q.YesKeys)
	after, _ := h.pending()
	if after.ID != before.ID {
		t.Fatalf("refusal changed pending from %s to %s, err %v", before.ID, after.ID, err)
	}
}

func TestReview4AnnouncementFinishingAfterMute(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.block(authPane, "codex-approval-prompt.screen.txt")
	p, _ := w.pending()
	row, _ := w.store.Get(authPane)
	a := announcement{row: row, pending: p.ID, sentence: p.SpokenSummary, kind: pushQuestion}
	if _, err := w.sup.SetSpaceMute(context.Background(), "auth", true, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	n := len(w.pushTexts())
	w.sup.codePath(a, nil, Normal, "timeout")
	if !w.sup.heldQuestion(p.ID) || len(w.pushTexts()) != n {
		t.Fatal("in-flight announcement escaped mute")
	}
}

func TestReview2DropHeldQuestionClearsBookkeeping(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.mute(t)
	w.block(authPane, "codex-approval-prompt.screen.txt")
	q, ok := w.store.Question(firstQuestionOf(w.harness, authPane))
	if !ok {
		t.Fatal("missing question")
	}
	w.sup.mu.Lock()
	w.sup.dropQuestionLocked(q)
	w.sup.mu.Unlock()
	if w.sup.heldQuestion(q.ID) {
		t.Fatal("dropped question remains held")
	}
}

func TestReview2MutedRevivalNeverSends(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.block(authPane, "codex-approval-prompt.screen.txt")
	p, ok := w.pending()
	if !ok {
		t.Fatal("missing pending question")
	}
	w.playText(p.SpokenSummary)
	if err := w.store.ClearPending(); err != nil {
		t.Fatal(err)
	}
	w.mute(t)
	w.clock.Advance(time.Second)
	w.sup.ShepherdInput(InputTyped, "yes approve auth")
	if _, err := (brainHost{w.sup}).AnswerPrompt(context.Background(), authPane, p.YesKeys); err == nil {
		t.Fatal("muted revival was answered")
	}
	if n := w.keysTo(authPane); n != 0 {
		t.Fatalf("sent %d keys", n)
	}
}
