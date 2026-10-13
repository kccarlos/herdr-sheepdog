package supervisor

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Unanswered questions (CEO product note, 2026-10-13; harness iteration
// 7). A question the user asked and never got a real answer to was
// dropped: the next turn was about whatever they said next. The daemon
// now records the clear cases, and only those, as a fact in the
// orchestrator's state block ("## Unanswered"):
//
//   - the orchestrator's turn on the words fell back (an error, a
//     timeout, a reply that broke the answer contract), unless a retry
//     of the same words (rerelay.go) then answered;
//   - the answer was cut off by the user talking over it before most of
//     it was said (the first cut of a run tagged with the utterance it
//     answered, not a flush for an urgent push, with less than half of
//     the answer's text heard);
//   - the answer was never voiced: a late answer judged unheard and not
//     re-queued (voice.EventPushUnheard), or dropped as stale before it
//     was said because a newer request came first.
//
// Whether an answer was good is not judged. The orchestrator decides
// whether and when to come back to a question ("about your earlier
// question on …") and clears it by naming its id in answers_earlier,
// also when the user dropped it or had it answered since. No words are
// matched here. A news briefing that went unheard is not recorded (a
// separate item); only answers to the user's own words are.

// Runaway rails, not judgments: at most unansweredKeep questions are
// kept (the oldest goes first), and each expires unansweredMaxAge after
// it was asked. In the live logs to 2026-10-13, 92 % of the user's
// utterances came within 10 minutes of the previous one; a longer gap is
// a new sitting, where an old question is more likely noise than news.
const (
	unansweredKeep   = 3
	unansweredMaxAge = 10 * time.Minute
)

// Plain reasons for the record (UnansweredInfo.Why).
const (
	whyNeverVoiced = "its answer was never said to the user"
	whyStale       = "its answer was dropped unsaid: the user said something new before it came"
)

// unansweredQ is one recorded question.
type unansweredQ struct {
	id    string
	text  string
	asked time.Time
	why   string
}

// askedRec is an utterance whose answer was handed to the voice: what a
// cut of that answer is recorded with.
type askedRec struct {
	id    string
	text  string
	asked time.Time
	// answer is the spoken answer handed to the voice; cut is set once a
	// cut of it was judged (only the first counts: the tail of the same
	// barge-in often flushes as a second, tiny run).
	answer string
	cut    bool
}

// answeredKeep bounds the utterances remembered for a cut.
const answeredKeep = 8

// recordUnanswered records the user's words id as unanswered (case:
// fallback, cut or unvoiced, for the log), replacing an earlier record
// of the same utterance. Caller does not hold s.mu.
func (s *Supervisor) recordUnanswered(id, text string, asked time.Time, why, kind string) {
	text = strings.TrimSpace(text)
	if s.brain == nil || id == "" || text == "" {
		return
	}
	now := s.cfg.Now()
	if asked.IsZero() {
		asked = now
	}
	s.mu.Lock()
	kept := s.unanswered[:0]
	for _, q := range s.unanswered {
		if q.id != id {
			kept = append(kept, q)
		}
	}
	s.unanswered = append(kept, unansweredQ{id: id, text: text, asked: asked, why: why})
	var dropped []string
	for len(s.unanswered) > unansweredKeep {
		dropped = append(dropped, s.unanswered[0].id)
		s.unanswered = s.unanswered[1:]
	}
	s.mu.Unlock()
	s.log.Log(logSource, "supervisor.unanswered_recorded", "", map[string]any{"utterance_id": id, "case": kind, "text": text, "why": why})
	for _, d := range dropped {
		s.log.Log(logSource, "supervisor.unanswered_cleared", "", map[string]any{"utterance_id": d, "by": "cap"})
	}
}

// clearUnanswered drops the records ids name; by says who cleared them
// (model, retry, reset) for the log.
func (s *Supervisor) clearUnanswered(ids []string, by string) {
	want := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	if len(want) == 0 {
		return
	}
	s.mu.Lock()
	var cleared []string
	kept := s.unanswered[:0]
	for _, q := range s.unanswered {
		if want[q.id] {
			cleared = append(cleared, q.id)
			continue
		}
		kept = append(kept, q)
	}
	s.unanswered = kept
	s.mu.Unlock()
	for _, id := range cleared {
		s.log.Log(logSource, "supervisor.unanswered_cleared", "", map[string]any{"utterance_id": id, "by": by})
	}
}

// forgetUnanswered drops every record ("New conversation").
func (s *Supervisor) forgetUnanswered() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.unanswered))
	for _, q := range s.unanswered {
		ids = append(ids, q.id)
	}
	s.mu.Unlock()
	s.clearUnanswered(ids, "reset")
}

// unansweredNow is the open records, oldest first, with the expired
// ones dropped (and logged).
func (s *Supervisor) unansweredNow() []unansweredQ {
	now := s.cfg.Now()
	s.mu.Lock()
	var expired []string
	kept := s.unanswered[:0]
	for _, q := range s.unanswered {
		if now.Sub(q.asked) > unansweredMaxAge {
			expired = append(expired, q.id)
			continue
		}
		kept = append(kept, q)
	}
	s.unanswered = kept
	out := append([]unansweredQ(nil), kept...)
	s.mu.Unlock()
	for _, id := range expired {
		s.log.Log(logSource, "supervisor.unanswered_cleared", "", map[string]any{"utterance_id": id, "by": "expiry"})
	}
	return out
}

// noteAnswerHanded remembers that u's answer went to the voice to say,
// so a cut of the run that plays it can be recorded with u's words.
func (s *Supervisor) noteAnswerHanded(u *inflightUtterance, answer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answeredRecs = append(s.answeredRecs, askedRec{id: u.id, text: u.text, asked: u.start, answer: answer})
	if len(s.answeredRecs) > answeredKeep {
		s.answeredRecs = s.answeredRecs[len(s.answeredRecs)-answeredKeep:]
	}
}

// onAnswerCut records the answer the user cut off before most of it
// was said (voice.EventTranscript with Cut): the first cut of a run
// tagged with the utterance it answered, made by the user rather than a
// flush for an urgent push, when what they heard is less than
// voice.HeardMinCoverage of the answer's text (in characters: the voice
// streams its audio, so the run's length when it is cut says nothing
// about how long the answer would have been).
func (s *Supervisor) onAnswerCut(c *voice.TranscriptCut, heard string) {
	if c == nil || c.UtteranceID == "" || c.ByPush {
		return
	}
	heard = strings.TrimSpace(heard)
	s.mu.Lock()
	var rec askedRec
	found := false
	for i := len(s.answeredRecs) - 1; i >= 0; i-- {
		if r := &s.answeredRecs[i]; r.id == c.UtteranceID {
			found = !r.cut
			r.cut = true
			rec = *r
			break
		}
	}
	s.mu.Unlock()
	answer := utf8.RuneCountInString(rec.answer)
	if !found || answer == 0 || float64(utf8.RuneCountInString(heard)) >= voice.HeardMinCoverage*float64(answer) {
		return
	}
	why := fmt.Sprintf("the user talked over its answer after %.0f s", float64(c.HeardMs)/1000)
	if heard != "" {
		why += fmt.Sprintf("; they heard only %q", clipRunes(heard, 60))
	} else {
		why += "; they heard none of it"
	}
	s.recordUnanswered(rec.id, rec.text, rec.asked, why, "cut")
}

// onAnswerUnheard records an answer push the voice layer judged unheard
// and did not re-queue (unheard.go).
func (s *Supervisor) onAnswerUnheard(e voice.EventPushUnheard) {
	id := voice.PushUtterance(e.Push.Tags)
	if id == "" {
		return
	}
	text, _ := e.Push.Tags["answers_text"].(string)
	var asked time.Time
	s.mu.Lock()
	for _, r := range s.answeredRecs {
		if r.id == id {
			text, asked = r.text, r.asked
		}
	}
	s.mu.Unlock()
	s.recordUnanswered(id, text, asked, whyNeverVoiced, "unvoiced")
}

// fellBack records the words of a turn that fell back. A retry's record
// replaces the one of the turn it retried (the same words).
func (s *Supervisor) fellBack(u *inflightUtterance, out utteranceOutcome) {
	if u.retryOf != "" {
		s.clearUnanswered([]string{u.retryOf}, "retry")
	}
	s.recordUnanswered(u.id, u.text, u.start, "your turn on it failed: "+failureReason(out.err), "fallback")
}

// answered notes a turn that answered: its answer is what a cut is
// recorded against, and a retry that answered clears the record of the
// turn it retried.
func (s *Supervisor) answered(u *inflightUtterance, out utteranceOutcome) {
	if u.retryOf != "" {
		s.clearUnanswered([]string{u.retryOf}, "retry")
	}
	if strings.TrimSpace(out.res.Spoken) != "" {
		s.noteAnswerHanded(u, strings.TrimSpace(out.res.Spoken))
	}
}

// Unanswered implements brain.UnansweredHost.
func (h brainHost) Unanswered() []brain.UnansweredInfo {
	qs := h.s.unansweredNow()
	if len(qs) == 0 {
		return nil
	}
	out := make([]brain.UnansweredInfo, len(qs))
	for i, q := range qs {
		out[i] = brain.UnansweredInfo{ID: q.id, Text: q.text, Asked: q.asked, Why: q.why}
	}
	return out
}

// AnsweredEarlier implements brain.UnansweredHost: the orchestrator's
// reply named these questions in answers_earlier.
func (h brainHost) AnsweredEarlier(ids []string) { h.s.clearUnanswered(ids, "model") }
