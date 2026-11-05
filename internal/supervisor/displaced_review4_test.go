package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

func TestReview4ShortNames(t *testing.T) {
	for _, tc := range []struct{ name, words string }{{"db", "yes approve db"}, {"数据库", "请批准数据库"}, {"db", "请批准db"}} {
		t.Run(tc.words, func(t *testing.T) {
			h := newHarness(t)
			h.addAgent(docsPane, tc.name, "codex", herdr.StateWorking)
			if _, err := h.store.Update(docsPane, func(a *state.Agent) { a.Alias = tc.name; a.Spoken = tc.name }); err != nil {
				t.Fatal(err)
			}
			h.block(docsPane, "codex-approval-prompt.screen.txt")
			h.clock.Advance(time.Second)
			h.hear(tc.words)
			h.sup.mu.Lock()
			named := h.sup.namedInLocked(tc.words, docsPane)
			h.sup.mu.Unlock()
			if !named {
				row, _ := h.store.Get(docsPane)
				t.Fatalf("%q did not name %q: %+v", tc.words, tc.name, row)
			}
		})
	}
}

func TestReview4NamedStatusDoesNotPromoteHeld(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.mute(t)
	w.block(authPane, "codex-approval-prompt.screen.txt")
	q, _ := w.store.Question(firstQuestionOf(w.harness, authPane))
	w.sup.ShepherdInput(InputTyped, "How is auth doing?")
	w.sup.toldInReply("Auth is paused on printf in your muted space.")
	if !w.sup.heldQuestion(q.ID) {
		t.Fatal("status promoted hidden question")
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

func TestReview4ShortNameAndScrubbedCommandInReply(t *testing.T) {
	h := newHarness(t)
	h.addAgent(docsPane, "db", "codex", herdr.StateWorking)
	if _, err := h.store.Update(docsPane, func(a *state.Agent) { a.Alias = "db"; a.Spoken = "db" }); err != nil {
		t.Fatal(err)
	}
	h.block(docsPane, "codex-approval-prompt.screen.txt")
	p, _ := h.pending()
	p.Command = "rm /tmp/example"
	p.Question = ""
	p.SpokenSummary = ""
	h.sup.mu.Lock()
	got := h.sup.statesLocked("db asks to run rm a file", p)
	h.sup.mu.Unlock()
	if !got {
		t.Fatal("reply did not match short name and command")
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

func TestReview4ScrubPlaceholderDoesNotIdentifyQuestion(t *testing.T) {
	h := newHarness(t)
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	p := state.PendingAction{PaneID: docsPane, Command: "rm /tmp/example"}
	h.sup.mu.Lock()
	got := h.sup.statesLocked("docs is working on a file", p)
	h.sup.mu.Unlock()
	if got {
		t.Fatal("generic scrub placeholder identified an unheard question")
	}
}

func TestReview2StatesNamesAndChinese(t *testing.T) {
	for _, tc := range []struct{ name, question, reply string }{
		{"go-api", "run pytest", "go api asks to run pytest"},
		{"数据库", "删除临时文件", "数据库请求删除临时文件吗"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.addAgent(docsPane, tc.name, "codex", herdr.StateWorking)
			_, err := h.store.Update(docsPane, func(a *state.Agent) { a.Alias = tc.name; a.Spoken = tc.name })
			if err != nil {
				t.Fatal(err)
			}
			p := state.PendingAction{PaneID: docsPane, Question: tc.question}
			h.sup.mu.Lock()
			got := h.sup.statesLocked(tc.reply, p)
			h.sup.mu.Unlock()
			if !got {
				t.Fatalf("%q did not identify %q's question", tc.reply, tc.name)
			}
		})
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
	w.sup.mu.Lock()
	w.sup.toldKeyLocked(keyOf(p), "")
	w.sup.mu.Unlock()
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
