package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The newest-hand-over rule (displaced.go): a played voice run tells only
// the question handed to the voice last, and only when the run names its
// agent. Other agents a run names in passing are told nothing.

// hand is the voice sending a push that states question id to the model.
func (h *harness) hand(id, text string) {
	h.sup.OnVoiceEvent(voice.EventPush{Text: text, Urgency: voice.UrgencyIdle, PendingID: id})
}

// playRun is the client confirming a run from start to end played out.
func (h *harness) playRun(text string, start, end time.Time) {
	h.sup.OnVoiceEvent(voice.EventPlayed{Text: text, Start: start, End: end})
}

// twoUntold opens auth's and then docs' question, neither announced yet:
// docs' is pending and its announcement names auth (replacedLine).
func twoUntold(t *testing.T) (*harness, state.PendingAction, state.PendingAction, string) {
	t.Helper()
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	ap, _ := h.pending()
	h.block(docsPane, "codex-approval-prompt.screen.txt")
	dp, _ := h.pending()
	if dp.PaneID != docsPane {
		t.Fatalf("pending = %+v, want docs'", dp)
	}
	return h, ap, dp, h.lastPushText()
}

// Regression 1: docs' announcement names auth, whose own announcement was
// handed over but never played. Only docs is told: a bare yes lands on
// docs, and an approving key never reaches auth.
func TestRunNamingAnUnplayedQuestionTellsOnlyTheHandedOne(t *testing.T) {
	h, ap, dp, docsLine := twoUntold(t)
	h.hand(ap.ID, ap.SpokenSummary) // flushed before it played: no EventPlayed
	h.clock.Advance(300 * time.Millisecond)
	h.hand(dp.ID, docsLine)
	start := h.clock.Now().Add(100 * time.Millisecond)
	h.clock.Advance(3 * time.Second)
	h.playRun(docsLine, start, h.clock.Now())
	if h.sup.toldOpenLocked(ap) {
		t.Fatal("a run about docs that names auth told auth's unplayed question")
	}
	h.clock.Advance(time.Second)
	bareYesReachesOnly(t, h, dp, ap)
}

// bareYesReachesOnly hears a bare yes and checks that send_keys may answer
// heard with it but not unheard.
func bareYesReachesOnly(t *testing.T, h *harness, heard, unheard state.PendingAction) {
	t.Helper()
	h.hear("Yes.")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), unheard.PaneID, unheard.YesKeys); err == nil {
		t.Fatalf("a bare yes approved %s, whose question the user did not hear", unheard.PaneID)
	}
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), heard.PaneID, heard.YesKeys); err != nil {
		t.Fatalf("a bare yes did not answer %s, the question the user heard: %v", heard.PaneID, err)
	}
	if n := h.keysTo(unheard.PaneID); n != 0 {
		t.Fatalf("keys to %s = %d, want 0", unheard.PaneID, n)
	}
}

// Regression 2: a second hand-over of the same question before the first
// run drained does not move the start gate: the fully played first
// announcement still tells it.
func TestSecondHandOverBeforeDrainKeepsTheFirstRun(t *testing.T) {
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	ap, _ := h.pending()
	h.hand(ap.ID, ap.SpokenSummary)
	h.clock.Advance(100 * time.Millisecond)
	start := h.clock.Now()
	h.clock.Advance(400 * time.Millisecond)
	h.hand(ap.ID, ap.SpokenSummary)
	h.clock.Advance(2 * time.Second)
	h.playRun(ap.SpokenSummary, start, h.clock.Now())
	h.clock.Advance(time.Second)
	h.say("Yes.")
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("keys to auth = %d, want 1", n)
	}
}

// Regression 3: typed words refused for auth re-ask auth while docs'
// announcement (which names auth) is playing. Typed input hands nothing to
// the voice: docs' run tells docs, and the user's spoken yes lands on docs,
// never on auth.
func TestTypedReaskDoesNotTakeOverAPlayingAnnouncement(t *testing.T) {
	h, ap, dp, docsLine := twoUntold(t)
	h.hand(ap.ID, ap.SpokenSummary) // flushed before it played
	h.clock.Advance(300 * time.Millisecond)
	h.hand(dp.ID, docsLine)
	start := h.clock.Now().Add(100 * time.Millisecond)
	h.clock.Advance(time.Second)
	h.sup.ShepherdInput(InputTyped, "yes")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, ap.YesKeys); err == nil {
		t.Fatal("typed yes approved auth's unplayed question")
	}
	reasked := false
	for _, p := range h.pusher.all() {
		if p.Text != ap.SpokenSummary && p.Text != docsLine && p.Urgency != voice.UrgencySilent {
			h.sup.OnVoiceEvent(p) // the re-ask goes to the voice model
			reasked = true
		}
	}
	if !reasked {
		t.Fatal("no re-ask of auth was pushed")
	}
	h.clock.Advance(2 * time.Second)
	h.playRun(docsLine, start, h.clock.Now())
	if h.sup.toldOpenLocked(ap) {
		t.Fatal("docs' run told auth")
	}
	h.clock.Advance(time.Second)
	bareYesReachesOnly(t, h, dp, ap)
}

// The voice re-asks auth (the user's spoken yes was refused) while docs'
// announcement, which names auth, is playing: that run started before
// auth became the newest hand-over again, so it tells auth nothing.
func TestVoiceReaskDuringAnotherRunDoesNotTellIt(t *testing.T) {
	h, ap, dp, docsLine := twoUntold(t)
	h.hand(ap.ID, ap.SpokenSummary) // flushed before it played
	h.clock.Advance(300 * time.Millisecond)
	h.hand(dp.ID, docsLine)
	start := h.clock.Now().Add(100 * time.Millisecond)
	h.clock.Advance(time.Second)
	h.hear("Yes.")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, ap.YesKeys); err == nil {
		t.Fatal("spoken yes approved auth's unplayed question")
	}
	for _, p := range h.pusher.all() {
		if p.PendingID == ap.ID && p.Text != ap.SpokenSummary {
			h.sup.OnVoiceEvent(p) // the re-ask goes to the voice model
		}
	}
	h.clock.Advance(2 * time.Second)
	h.playRun(docsLine, start, h.clock.Now())
	if h.sup.toldOpenLocked(ap) {
		t.Fatal("docs' run told auth after auth's re-ask was handed over")
	}
	h.clock.Advance(time.Second)
	h.hear("Yes.")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, ap.YesKeys); err == nil {
		t.Fatal("a bare yes approved auth, whose question never played")
	}
}

// A run that names only another agent in passing tells nothing: not that
// agent's question, and not the handed one it does not name.
func TestIncidentalMentionTellsNothing(t *testing.T) {
	h, ap, dp, docsLine := twoUntold(t)
	h.hand(ap.ID, ap.SpokenSummary)
	h.clock.Advance(300 * time.Millisecond)
	h.hand(dp.ID, docsLine)
	start := h.clock.Now()
	h.clock.Advance(time.Second)
	h.playRun("auth finished its tests earlier.", start, h.clock.Now())
	if h.sup.toldOpenLocked(ap) || h.sup.toldOpenLocked(dp) {
		t.Fatal("a run naming auth in passing told a question")
	}
	h.clock.Advance(time.Second)
	h.hear("Yes.")
	for _, q := range []state.PendingAction{ap, dp} {
		if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), q.PaneID, q.YesKeys); err == nil {
			t.Fatalf("a bare yes approved %s after a passing mention", q.PaneID)
		}
	}
}

// The normal case: one announcement handed over and played, then a bare yes.
func TestBareYesAfterASingleAnnouncementLands(t *testing.T) {
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	ap, _ := h.pending()
	line := h.lastPushText()
	h.hand(ap.ID, line)
	start := h.clock.Now()
	h.clock.Advance(2 * time.Second)
	h.playRun(line, start, h.clock.Now())
	h.clock.Advance(time.Second)
	h.say("Yes.")
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("keys to auth = %d, want 1", n)
	}
}

// Adversarial: auth's prompt changes right after its first announcement
// was handed over, and audio of that first announcement already on its way
// starts after the new one was handed. That run, which names auth, is about
// the old prompt: it does not tell the new one.
func TestOldAnnouncementAfterTheSamePaneChangedDoesNotTellTheNewPrompt(t *testing.T) {
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	p1, _ := h.pending()
	h.hand(p1.ID, p1.SpokenSummary)
	h.clock.Advance(time.Second)
	h.srv.SetScreen(authPane, "running the tests")
	h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)
	h.block(authPane, "claude-permission-prompt.screen.txt")
	p2, _ := h.pending()
	if p2.ID == p1.ID {
		t.Fatal("auth's prompt did not change")
	}
	h.hand(p2.ID, p2.SpokenSummary)
	h.clock.Advance(50 * time.Millisecond)
	start := h.clock.Now() // the old announcement's audio starts now
	h.clock.Advance(2 * time.Second)
	h.playRun(p1.SpokenSummary, start, h.clock.Now())
	h.clock.Advance(time.Second)
	h.hear("Yes.")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, p2.YesKeys); err == nil {
		t.Fatal("a bare yes approved auth's new prompt after only the old one played")
	}
	// Its own announcement then tells it.
	start = h.clock.Now()
	h.clock.Advance(2 * time.Second)
	h.playRun(p2.SpokenSummary, start, h.clock.Now())
	h.clock.Advance(time.Second)
	h.hear("Yes.")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, p2.YesKeys); err != nil {
		t.Fatalf("a bare yes after auth's new prompt played: %v", err)
	}
}

// Typed text that names an agent in passing does not tell its question:
// returned text tells only the questions it reads out.
func TestTypedMentionInPassingTellsNothing(t *testing.T) {
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	ap, _ := h.pending()
	h.clock.Advance(time.Second)
	h.sup.ShepherdInput(InputTyped, "how is it going?")
	h.sup.NoteReturnedText("auth is busy; nothing else to report.")
	if h.sup.toldOpenLocked(ap) {
		t.Fatal("typed text naming auth in passing told its question")
	}
	h.sup.NoteReturnedText(ap.SpokenSummary + " Should it go ahead?")
	if !h.sup.toldOpenLocked(ap) {
		t.Fatal("typed text reading out auth's question did not tell it")
	}
}

// Muting the newest hand-over's space before its run drained: the run
// tells nothing, and the question stays out of a bare yes's reach.
func TestMuteBeforeDrainTellsNothing(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.block(authPane, "codex-approval-prompt.screen.txt")
	ap, _ := w.pending()
	w.hand(ap.ID, ap.SpokenSummary)
	start := w.clock.Now()
	w.mute(t)
	w.clock.Advance(2 * time.Second)
	w.playRun(ap.SpokenSummary, start, w.clock.Now())
	w.clock.Advance(time.Second)
	w.hear("Yes.")
	if _, err := (brainHost{w.sup}).AnswerPrompt(context.Background(), authPane, ap.YesKeys); err == nil {
		t.Fatal("a bare yes approved a muted question after its run drained")
	}
}
