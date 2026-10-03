package gptlive

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Unheard pushes on GPT-Live (voice/heard.go; docs/bench-2026-10-03.md
// #17, #18, #19). GPT-Live voices an idle push (commentary.append) in its
// own words, so a spoken push is judged by its key terms — the agents it
// names, its numbers, its option labels — against the output transcript
// since it was sent (voice.KeyTermCoverage), not word for word as on
// Gemini. The verdicts take the same path as Gemini's: logged
// voice.push_unheard and emitted as voice.EventPushUnheard (the
// supervisor tells the brain); a line the shepherd is owed a hearing of
// (needs_user, a kept or due promise, or a question the supervisor's
// open check says is still open) goes back in the queue once, at idle,
// after the shepherd's turn and its reply, or is dropped if no longer
// open.

// requeueSettle is how long a re-queued line waits after the shepherd's
// last words and after the client last stopped playing (a var for
// tests).
var requeueSettle = 2 * time.Second

// SetOpenCheck installs the supervisor's open check (voice.OpenCheck):
// asked with a push's tags whether what it is about is still open.
func (s *Session) SetOpenCheck(f voice.OpenCheck) {
	s.mu.Lock()
	s.openCheck = f
	s.mu.Unlock()
}

// keyTermCoverage is the heard tracker's judge: the push's key terms
// against what was said, with the agents' current spoken names
// (Config.Names) and the alias the push is tagged with as the names.
func (s *Session) keyTermCoverage(text string, tags map[string]any, said string) float64 {
	var names []string
	if s.cfg.Names != nil {
		names = s.cfg.Names()
	}
	if a, _ := tags["agent"].(string); a != "" {
		names = append(names, a)
	}
	return voice.KeyTermCoverage(text, names, said)
}

// reportUnheard logs and emits the verdicts; an owed line waits in
// s.requeue for a quiet moment.
func (s *Session) reportUnheard(vs []voice.UnheardPush) {
	if len(vs) == 0 {
		return
	}
	s.mu.Lock()
	check := s.openCheck
	s.mu.Unlock()
	for _, v := range vs {
		requeue := !v.Requeued && (voice.OwedTags(v.Tags) || (check != nil && check(v.Tags)))
		p := v.Payload()
		p["requeue"] = requeue
		p["judge"] = "key_terms"
		s.log.Log(logSource, "voice.push_unheard", "", p)
		if requeue {
			s.mu.Lock()
			s.requeue = append(s.requeue, v)
			s.mu.Unlock()
		}
		s.emit(voice.EventPushUnheard{Push: v, Requeue: requeue})
	}
}

// settleHeard judges the watched push once its speech is over (the
// model stopped and the client is not playing it), then releases owed
// lines when the moment is quiet. Called every tick.
func (s *Session) settleHeard(now time.Time) {
	s.mu.Lock()
	v := s.heard.Settle(now, s.speakingOrPlayingLocked(now))
	s.mu.Unlock()
	s.reportUnheard(v)
	s.releaseRequeue(now)
}

// releaseRequeue puts the owed unheard lines back in the push queue, at
// idle, once the shepherd's turn and the voice's reply are over: nothing
// generating or playing, no delegation in flight, the shepherd not
// speaking, requeueSettle since they last spoke and since playback
// ended, and voice.RequeueHold clear (the client's detector settled, the
// reply to the shepherd's last words over). A line the supervisor says is no longer open is dropped
// (voice.push_dropped {reason: no_longer_open}).
func (s *Session) releaseRequeue(now time.Time) {
	s.mu.Lock()
	if len(s.requeue) == 0 || s.speakingOrPlayingLocked(now) || len(s.inflight) > 0 || s.inputOpen || s.vad.Hold(now) > 0 ||
		now.Sub(s.lastInput) < requeueSettle || now.Sub(s.play.EndedAt(now)) < requeueSettle ||
		voice.RequeueHold(now, voice.RequeueFacts{LastInput: s.lastInput, ReplyDoneAt: s.turnDoneAt, Speech: s.vad}) != "" {
		s.mu.Unlock()
		return
	}
	items, check := s.requeue, s.openCheck
	s.requeue = nil
	s.mu.Unlock()
	for _, v := range items {
		tags := make(map[string]any, len(v.Tags)+1)
		for k, val := range v.Tags {
			tags[k] = val
		}
		if check != nil && !voice.OwedPromise(tags) && !check(tags) {
			p := heldPayload(pushItem{text: v.Text, requested: voice.UrgencyIdle, tags: tags})
			p["reason"] = "no_longer_open"
			s.log.Log(logSource, "voice.push_dropped", "", p)
			continue
		}
		tags[voice.TagRequeued] = true
		item := pushItem{text: v.Text, urgency: voice.UrgencyIdle, requested: voice.UrgencyIdle, tags: tags, at: now}
		s.mu.Lock()
		s.pushQ = append(s.pushQ, item)
		s.mu.Unlock()
		p := heldPayload(item)
		p["reason"] = v.Reason
		s.log.Log(logSource, "voice.push_requeued", "", p)
	}
}
