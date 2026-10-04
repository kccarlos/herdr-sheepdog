package supervisor

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// What the shepherd had heard when they began speaking
// (docs/eval/eval-campaign-2026-10-03.md H6). In j4-finish-finding the
// shepherd said "Yes, let me know when it's done." to the brain's offer;
// a second later a finish line asking "Want it to fix those two links?"
// went out, and the brain took the yes as the answer to that newer
// question and sent the worker a fix. No rule here decides what the
// words answer: the brain's utterance request carries when the words
// began, the last line the voice was given before that and the lines it
// was given since (with the voice layer's not-heard verdicts), and the
// brain binds the words or asks.

// maxSentPushes bounds the remembered spoken pushes.
const maxSentPushes = 8

// recordSentLocked remembers a spoken push as sent now. Caller holds
// s.mu.
func (s *Supervisor) recordSentLocked(text string) {
	s.sentPushes = append(s.sentPushes, brain.PushedLine{Text: text, At: s.cfg.Now()})
	if len(s.sentPushes) > maxSentPushes {
		s.sentPushes = append([]brain.PushedLine(nil), s.sentPushes[len(s.sentPushes)-maxSentPushes:]...)
	}
}

// markUnheard records the voice layer's verdict that the newest sent
// push with text did not reach the shepherd.
func (s *Supervisor) markUnheard(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.promptUnheardLocked(text) // buried.go
	for i := len(s.sentPushes) - 1; i >= 0; i-- {
		if s.sentPushes[i].Text == text {
			s.sentPushes[i].Unheard = true
			return
		}
	}
}

// heardFacts is what the shepherd had heard when words that began at
// start reached the daemon: the last spoken push sent before start and
// those sent since. Nil when start is unknown.
func (s *Supervisor) heardFacts(start time.Time) *brain.HeardFacts {
	if start.IsZero() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &brain.HeardFacts{SpokeAt: start}
	for _, p := range s.sentPushes {
		if p.At.After(start) {
			h.After = append(h.After, p)
		} else {
			before := p
			h.Before = &before
		}
	}
	return h
}
