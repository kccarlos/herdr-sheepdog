package gemini

import (
	"fmt"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Unvoiced briefings (phone test 2026-10-04: b-31 "Claude finished",
// b-69 and b-75 were sent WHEN_IDLE or INTERRUPT and never said; the
// CEO asked "有结论了吗" 22 s later and was told it was still working).
//
// What the daemon can observe is physical, not semantic: after a spoken
// briefing reached the model, did the model produce any speech (audio
// or output transcript) before the CEO next spoke or the next spoken
// briefing went out? When it did not, the briefing was not voiced, and
// the fast brain is told so as a silent fact — the same shape as a cut
// line — and the slow brain hears that the CEO did not hear it. Whether
// to say it now, later or never is the fast brain's call; nothing is
// re-queued and nothing is judged from the words.

// unvoicedKeep bounds the facts carried to the next spoken briefing.
const unvoicedKeep = 3

// voiceWatch is the spoken briefing awaiting the model's speech.
type voiceWatch struct {
	rec  briefRec
	text string
	tags map[string]any
}

// watchVoicedLocked starts watching a spoken briefing just sent.
// Caller holds s.mu.
func (s *Session) watchVoicedLocked(r briefRec, text string, tags map[string]any) {
	s.brief.watch = &voiceWatch{rec: r, text: text, tags: tags}
}

// nextBriefingLocked closes the watch before the next spoken briefing
// is built, so that briefing's relation carries the fact. Caller holds
// s.mu.
func (s *Session) nextBriefingLocked() *unvoicedReport {
	return s.closeWatchLocked("the next briefing went out")
}

// noteSpeechLocked records that the model spoke: the watched briefing
// drew speech. Caller holds s.mu.
func (s *Session) noteSpeechLocked() { s.brief.watch = nil }

// noteCEOSpokeLocked closes the watch when the CEO starts speaking.
// Caller holds s.mu.
func (s *Session) noteCEOSpokeLocked(now time.Time) *unvoicedReport {
	return s.closeWatchLocked("the CEO spoke at " + clock(now))
}

// unvoicedReport is one briefing found unvoiced.
type unvoicedReport struct {
	fact   string
	rec    briefRec
	text   string
	tags   map[string]any
	reason string
}

// closeWatchLocked ends the watch; a briefing still watched drew no
// speech. Caller holds s.mu.
func (s *Session) closeWatchLocked(before string) *unvoicedReport {
	w := s.brief.watch
	s.brief.watch = nil
	if w == nil {
		return nil
	}
	fact := fmt.Sprintf("briefing %s (%s, sent at %s) was not voiced: you said nothing after it before %s, so the CEO has not heard it", w.rec.id, w.rec.kind, clock(w.rec.at), before)
	s.brief.unvoiced = append(s.brief.unvoiced, fact)
	if len(s.brief.unvoiced) > unvoicedKeep {
		s.brief.unvoiced = s.brief.unvoiced[len(s.brief.unvoiced)-unvoicedKeep:]
	}
	return &unvoicedReport{fact: fact, rec: w.rec, text: w.text, tags: w.tags, reason: before}
}

// reportUnvoiced logs an unvoiced briefing and tells the supervisor
// (which tells the slow brain); with silent, the fast brain gets the
// fact at once as a silent briefing (otherwise the next briefing's
// relation carries it). Call without s.mu.
func (s *Session) reportUnvoiced(rep *unvoicedReport, silent bool) {
	if rep == nil {
		return
	}
	s.log.Log(logSource, "voice.briefing_unvoiced", "", map[string]any{"briefing": rep.rec.id, "at": clock(rep.rec.at), "kind": rep.rec.kind, "text": rep.text, "before": rep.reason})
	if silent {
		_ = s.PushTagged(rep.fact, voice.UrgencySilent, map[string]any{"kind": "delivery", "from": "daemon"})
	}
	s.emit(voice.EventPushUnheard{Push: voice.UnheardPush{Text: rep.text, Tags: rep.tags, Reason: voice.UnheardNotVoiced, SentAt: rep.rec.at}})
}

// pendingDeliveryFactsLocked hands out, once, the delivery facts the
// fast brain should see with its next answer: the unvoiced briefings
// and the last cut not yet repeated. Caller holds s.mu.
func (s *Session) pendingDeliveryFactsLocked() []string {
	out := append([]string(nil), s.brief.unvoiced...)
	s.brief.unvoiced = nil
	if s.brief.cut != "" {
		out = append(out, s.brief.cut)
		s.brief.cut = ""
	}
	return out
}
