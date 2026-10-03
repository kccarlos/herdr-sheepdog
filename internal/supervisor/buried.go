package supervisor

import (
	"fmt"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// A prompt the CEO has likely not heard (tier-3 bench 2026-10-03 #9,
// t3-codex-report): Codex's approval push ("Codex asks to run go test.")
// was followed four seconds later by the reply to the CEO's previous
// words, written before the block ("I told Codex to rerun the shop
// tests…"). The CEO waited; the prompt stayed open for more than three
// minutes and nothing raised it again. The voice layer re-queues a line
// it judged unheard once (voice/gemini/heard.go), but a line that was
// heard and then buried under another, or one whose re-queue was cut
// again, had no second chance.
//
// The daemon tracks the announcement of the open answer_prompt: when it
// was handed to the voice, and whether another line followed it (a push,
// or the voice speaking on its own after the announcement's turn) or the
// voice layer judged it unheard, all before the CEO said anything. Such
// a prompt is
//   - re-announced once, at the next quiet moment (promptRemindQuiet with
//     no line, no CEO words and no turn in flight), at idle, tagged with
//     its event so the voice drops it if the prompt closed meanwhile
//     (supervisor.prompt_reminded);
//   - stated as a fact on the brain's next utterance request
//     (EventFacts.OpenPrompt): what the CEO says next may not answer it.
// No judgement is made here about what the CEO meant.

// promptRemindQuiet is how long nothing has been said, by either side,
// before a buried prompt is re-announced.
var promptRemindQuiet = 8 * time.Second

// promptHearing is what the daemon knows about whether the CEO heard the
// open prompt's announcement (under s.mu).
type promptHearing struct {
	// pendingID is the answer_prompt it is about; lines are the spoken
	// texts pushed about it (the code sentence, the brain's line, the
	// reminder).
	pendingID string
	lines     map[string]bool
	// sentAt is when its newest line was handed to the voice; done is
	// set once the voice's turn that spoke it completed.
	sentAt time.Time
	done   bool
	// buriedAt is when another line followed it before the CEO spoke;
	// unheardAt when the voice layer judged it unheard.
	buriedAt  time.Time
	unheardAt time.Time
	// reminded is set once the reminder was pushed (once per prompt).
	reminded bool
	// quietSince is when anything was last said: a line sent, the voice
	// speaking, the CEO speaking.
	quietSince time.Time
}

// tagPendingID marks a push that announces an answer_prompt: its id.
const tagPendingID = "pending_id"

// notePromptLine records text as a spoken line about the open prompt
// when tags name it (tagPendingID).
func (s *Supervisor) notePromptLine(text string, tags map[string]any) {
	id, _ := tags[tagPendingID].(string)
	if id == "" || text == "" {
		return
	}
	p, has := s.store.Pending()
	if !has || p.Kind != state.PendingAnswerPrompt || p.ID != id {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &s.promptHeard
	if h.pendingID != p.ID {
		*h = promptHearing{pendingID: p.ID, lines: map[string]bool{}, quietSince: h.quietSince}
	}
	h.lines[text] = true
}

// promptPushedLocked updates the hearing for a spoken push handed to the
// voice now (markPushed). Caller holds s.mu.
func (s *Supervisor) promptPushedLocked(text string) {
	h := &s.promptHeard
	now := s.cfg.Now()
	h.quietSince = now
	if h.pendingID == "" {
		return
	}
	if h.lines[text] {
		h.sentAt, h.done, h.buriedAt, h.unheardAt = now, false, time.Time{}, time.Time{}
		return
	}
	s.promptBuriedLocked(now)
}

// promptBuriedLocked marks the prompt's announcement as followed by
// another line, unless the CEO spoke since it went out. Caller holds
// s.mu.
func (s *Supervisor) promptBuriedLocked(now time.Time) {
	h := &s.promptHeard
	if h.sentAt.IsZero() || !h.buriedAt.IsZero() || s.ceoSpokeSinceLocked(h.sentAt) {
		return
	}
	h.buriedAt = now
}

// promptTurnDone records that the voice finished a turn: the prompt's
// announcement, once sent, has been spoken.
func (s *Supervisor) promptTurnDone() {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &s.promptHeard
	h.quietSince = s.cfg.Now()
	if !h.sentAt.IsZero() {
		h.done = true
	}
}

// promptVoiceSpokeLocked records the voice's own speech: after the
// prompt's announcement was spoken, speech that is not a push being read
// (a reply) buries it. Caller holds s.mu.
func (s *Supervisor) promptVoiceSpokeLocked(pushSpeaking bool) {
	h := &s.promptHeard
	now := s.cfg.Now()
	h.quietSince = now
	if h.done && !pushSpeaking {
		s.promptBuriedLocked(now)
	}
}

// promptUnheardLocked records the voice layer's verdict that text did
// not reach the CEO. Caller holds s.mu.
func (s *Supervisor) promptUnheardLocked(text string) {
	h := &s.promptHeard
	if h.lines[text] && !h.sentAt.IsZero() && h.unheardAt.IsZero() {
		h.unheardAt = s.cfg.Now()
	}
}

// promptCEOSpokeLocked records the CEO's words: the moment is not quiet.
// Caller holds s.mu.
func (s *Supervisor) promptCEOSpokeLocked() { s.promptHeard.quietSince = s.cfg.Now() }

// ceoSpokeSinceLocked reports whether the CEO's newest input came at or
// after t. Caller holds s.mu.
func (s *Supervisor) ceoSpokeSinceLocked(t time.Time) bool {
	in := s.lastInputLocked()
	return in != nil && !in.at.Before(t)
}

// unheardPromptLocked returns the open prompt and why the CEO has likely
// not heard it, as of at: its announcement was buried or judged unheard
// before at, and the CEO said nothing between the announcement and that.
// ok is false otherwise. Caller holds s.mu.
func (s *Supervisor) unheardPromptLocked(at time.Time) (p state.PendingAction, reason string, ok bool) {
	h := s.promptHeard
	p, has := s.store.Pending()
	if !has || p.Kind != state.PendingAnswerPrompt || p.ID != h.pendingID || h.sentAt.IsZero() || h.sentAt.After(at) {
		return state.PendingAction{}, "", false
	}
	switch {
	case !h.unheardAt.IsZero() && !h.unheardAt.After(at):
		reason = "unheard"
	case !h.buriedAt.IsZero() && !h.buriedAt.After(at):
		reason = "buried"
	default:
		return state.PendingAction{}, "", false
	}
	for i := len(s.inputs) - 1; i >= 0; i-- {
		in := s.inputs[i]
		if in.at.Before(h.sentAt) {
			break
		}
		if in.at.Before(at) {
			// The CEO spoke after the announcement and before at: they
			// have had their say about it already.
			return state.PendingAction{}, "", false
		}
	}
	return p, reason, true
}

// openPromptFact is the brain's fact about a prompt the CEO has likely
// not heard when words that began at start reached the daemon; "" when
// there is none.
func (s *Supervisor) openPromptFact(start time.Time) string {
	if start.IsZero() {
		start = s.cfg.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, reason, ok := s.unheardPromptLocked(start)
	if !ok {
		return ""
	}
	why := "another line was spoken right after it, before the CEO said anything"
	if reason == "unheard" {
		why = "the voice judged that it did not reach the CEO"
	}
	return fmt.Sprintf("%s's prompt is still open and the CEO has likely not heard it: %q (%s). Their words may not be about it.", s.sayOf(p.PaneID), p.SpokenSummary, why)
}

// remindBuriedPrompt re-announces, once, an open prompt the CEO has
// likely not heard, when the conversation has been quiet for
// promptRemindQuiet and no turn is in flight. Run calls it every tick.
func (s *Supervisor) remindBuriedPrompt() {
	s.mu.Lock()
	now := s.cfg.Now()
	h := &s.promptHeard
	if h.reminded || s.utterance != nil || now.Sub(h.quietSince) < promptRemindQuiet {
		s.mu.Unlock()
		return
	}
	p, reason, ok := s.unheardPromptLocked(now)
	if !ok {
		s.mu.Unlock()
		return
	}
	h.reminded = true
	text := reminderLine(p.SpokenSummary, s.ceoChineseLocked())
	h.lines[text] = true
	age := now.Sub(h.sentAt)
	s.mu.Unlock()
	s.log.Log(logSource, "supervisor.prompt_reminded", p.PaneID, map[string]any{
		"pending": p.ID, "reason": reason, "since_announced_s": int(age.Seconds()), "text": text, "agent": s.aliasOf(p.PaneID),
	})
	tags := map[string]any{tagPendingID: p.ID, "prompt_reminder": true}
	if evt := s.eventIDOf(p.PaneID); evt != "" {
		// The voice asks OpenCheck whether the event's prompt is still
		// the pending one before it speaks a held line.
		tags[tagEventID] = evt
	}
	s.pushTagged(p.PaneID, text, voice.UrgencyIdle, tags)
}

// reminderLine is the re-announcement of an open prompt, in the CEO's
// language.
func reminderLine(summary string, zh bool) string {
	if zh {
		return "还在等你回答：" + prose.Terminate(summary)
	}
	return "Still waiting for your answer: " + prose.Terminate(summary)
}
