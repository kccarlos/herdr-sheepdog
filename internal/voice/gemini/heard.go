package gemini

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Unheard pushes (voice/heard.go; docs/bench-2026-10-03.md #17, #18,
// #19). Each verdict is logged voice.push_unheard and emitted as
// voice.EventPushUnheard. A line the shepherd is owed a hearing of — a
// decision they must take (tagged needs_user, or one the supervisor's
// open check says is still open), a kept or due promise — is put back
// in the session's own queue, once, when the shepherd's turn and its
// reply are over; if the supervisor then says it is no longer open it
// is dropped instead. Every other unheard line is left to the
// supervisor, which tells the brain.

// requeueSettle is how long a re-queued line waits after the shepherd's
// last words and after the client last stopped playing.
const requeueSettle = 2 * time.Second

// tagRequeued marks a re-queued push (it is never re-queued again).
const tagRequeued = voice.TagRequeued

// owedTags reports whether tags alone mark a line the shepherd is owed
// a hearing of (voice.OwedTags).
func owedTags(tags map[string]any) bool { return voice.OwedTags(tags) }

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
		requeue := !v.Requeued && (owedTags(v.Tags) || (check != nil && check(v.Tags)))
		p := v.Payload()
		p["requeue"] = requeue
		s.log.Log(logSource, "voice.push_unheard", "", p)
		if requeue {
			s.mu.Lock()
			s.requeue = append(s.requeue, v)
			s.mu.Unlock()
		}
		s.emit(voice.EventPushUnheard{Push: v, Requeue: requeue})
	}
}

// settleHeard judges the watched push if its speech is over, then
// releases owed lines when the moment is quiet.
func (s *Session) settleHeard() {
	s.mu.Lock()
	now := s.now()
	playing, _ := s.play.Playing(now)
	v := s.heard.Settle(now, playing || s.speaking)
	s.mu.Unlock()
	s.reportUnheard(v)
	s.releaseRequeue()
}

// releaseRequeue puts the owed unheard lines back in the push queue, at
// idle, once the shepherd's turn and the voice's reply to it are over:
// nothing generating or playing, no tool call or owed reply in flight,
// requeueSettle since the shepherd last spoke and since playback ended,
// and voice.RequeueHold clear (the client's detector settled, the
// speech it heard transcribed, the reply to it complete). A line the supervisor says is no longer open is dropped
// (voice.push_dropped {reason: no_longer_open}).
func (s *Session) releaseRequeue() {
	s.mu.Lock()
	now := s.now()
	if len(s.requeue) == 0 || !s.quietLocked(now) || now.Sub(s.lastInput) < requeueSettle || now.Sub(s.play.EndedAt(now)) < requeueSettle ||
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
		if check != nil && !owedPromise(tags) && !check(tags) {
			p := heldPayload(pushItem{text: v.Text, requested: voice.UrgencyIdle, tags: tags}, "no_longer_open", 0)
			s.log.Log(logSource, "voice.push_dropped", "", p)
			continue
		}
		tags[tagRequeued] = true
		item := pushItem{text: v.Text, urgency: voice.UrgencyIdle, requested: voice.UrgencyIdle, tags: tags, at: now}
		s.mu.Lock()
		s.pushQ = append(s.pushQ, item)
		s.mu.Unlock()
		p := heldPayload(item, v.Reason, 0)
		s.log.Log(logSource, "voice.push_requeued", "", p)
	}
	s.poke()
}

// owedPromise reports whether tags mark a kept or due promise
// (voice.OwedPromise).
func owedPromise(tags map[string]any) bool { return voice.OwedPromise(tags) }
