package gemini

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Unheard pushes (voice/heard.go; docs/bench-2026-10-03.md #17, #18,
// #19). Each verdict is logged voice.push_unheard and emitted as
// voice.EventPushUnheard. A line the shepherd is owed a hearing of — a
// decision they must take (tagged needs_user, or one the supervisor's
// open check says is still open), a kept or due promise, an answer to
// their question — waits in the session's voice.Requeue and is put back
// in the queue, once, when the shepherd's turn and its reply are over,
// unless by then it is no longer needed (voice/requeue.go: already
// heard, a stale answer, no longer open). Every other unheard line is
// left to the supervisor, which tells the brain.

// requeueSettle is how long a re-queued line waits after the shepherd's
// last words and after the client last stopped playing.
const requeueSettle = 2 * time.Second

// tagRequeued marks a re-queued push (it is never re-queued again).
const tagRequeued = voice.TagRequeued

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
		open := check != nil && voice.PushUtterance(v.Tags) == "" && !voice.OwedTags(v.Tags) && check(v.Tags)
		s.mu.Lock()
		requeue := s.requeue.Owed(v, open)
		if requeue {
			s.requeue.Add(v)
		}
		s.mu.Unlock()
		p := v.Payload()
		p["requeue"] = requeue
		s.log.Log(logSource, "voice.push_unheard", "", p)
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
// speech it heard transcribed, the reply to it complete). A line
// voice.Requeue says is no longer needed is dropped
// (voice.push_dropped with its reason); a stale answer also goes to the
// supervisor as unheard (EventPushUnheard, Requeue false).
func (s *Session) releaseRequeue() {
	s.mu.Lock()
	now := s.now()
	if s.requeue.Len() == 0 || !s.quietLocked(now) || now.Sub(s.lastInput) < requeueSettle || now.Sub(s.play.EndedAt(now)) < requeueSettle ||
		voice.RequeueHold(now, voice.RequeueFacts{LastInput: s.lastInput, ReplyDoneAt: s.turnDoneAt, Speech: s.vad}) != "" {
		s.mu.Unlock()
		return
	}
	items, check := s.requeue.Take(), s.openCheck
	s.mu.Unlock()
	for _, rel := range items {
		v := rel.Push
		if rel.Drop == "" && check != nil && voice.OpenCheckDecides(v.Tags) && !check(v.Tags) {
			rel.Drop = voice.DropNoLongerOpen
		}
		tags := make(map[string]any, len(v.Tags)+1)
		for k, val := range v.Tags {
			tags[k] = val
		}
		if rel.Drop != "" {
			p := heldPayload(pushItem{text: v.Text, requested: voice.UrgencyIdle, tags: tags}, rel.Drop, 0)
			s.log.Log(logSource, "voice.push_dropped", "", p)
			if rel.Tell {
				s.emit(voice.EventPushUnheard{Push: v, Requeue: false})
			}
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
