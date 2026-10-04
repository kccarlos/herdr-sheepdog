package gemini

import (
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Resumption handles and the silent context after them (docs/
// gemini-live-notes.md §7). The server sends a new handle after every
// completed turn, and a resume continues from the context the handle was
// issued at: a clean resume loses nothing up to it, but silent context
// sent after it (a names line, a focus line, a briefing: pushes that
// complete no turn) is not in the resumed context (the CEO's experiment,
// 2026-10-03). So the session keeps the silent pushes sent since the
// last accepted handle and, on a resume, sends them again. Every handle
// update is logged (voice.handle_update) with the prompt size at issue,
// so a context shrink in the log can be matched to the handle it came
// from.

// postHandleMax caps the silent pushes kept for re-sending: names, focus
// and briefings come a few per turn, and a turn ends with a new handle.
const postHandleMax = 32

// tagResent marks a silent push sent again after a resume.
const tagResent = "resent_after_resume"

// handleTail is the part of a handle the log keeps (enough to tell
// handles apart).
func handleTail(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// noteHandleLocked records one sessionResumptionUpdate: accepted is
// whether it became the handle a reconnect presents. An accepted
// handle covers everything sent before it, so the silent pushes kept for
// re-sending are dropped. It returns the voice.handle_update payload.
// Caller holds s.mu.
func (s *Session) noteHandleLocked(newHandle string, resumable, accepted bool) map[string]any {
	p := map[string]any{
		"handle":        handleTail(newHandle),
		"resumable":     resumable,
		"accepted":      accepted,
		"prompt_tokens": s.lastPromptTokens,
		"silent_since":  len(s.postHandle),
	}
	if !s.handleAt.IsZero() {
		p["since_last_ms"] = s.now().Sub(s.handleAt).Milliseconds()
	}
	if accepted {
		s.handleAt = s.now()
		s.postHandle = nil
	}
	return p
}

// keepSilentLocked records a silent push that went out on the live
// connection, for re-sending if the session resumes before the next
// handle. A line that was itself a re-send is kept as well (it is in the
// context only once the next handle arrives). Caller holds s.mu.
func (s *Session) keepSilentLocked(item pushItem) {
	if voice.IsFocus(item.tags) {
		// Only the newest focus line matters.
		kept := s.postHandle[:0]
		for _, it := range s.postHandle {
			if !voice.IsFocus(it.tags) {
				kept = append(kept, it)
			}
		}
		s.postHandle = kept
	}
	s.postHandle = append(s.postHandle, item)
	if n := len(s.postHandle); n > postHandleMax {
		s.postHandle = append([]pushItem(nil), s.postHandle[n-postHandleMax:]...)
	}
}

// resendAfterResumeLocked puts the silent pushes sent after the last
// handle back at the front of the queue, in their order, and returns how
// many. A focus line is skipped when a newer one is already queued.
// Caller holds s.mu.
func (s *Session) resendAfterResumeLocked() int {
	items := s.postHandle
	s.postHandle = nil
	if len(items) == 0 {
		return 0
	}
	focusQueued := false
	for _, it := range s.pushQ {
		if voice.IsFocus(it.tags) {
			focusQueued = true
		}
	}
	var front []pushItem
	for _, it := range items {
		if focusQueued && voice.IsFocus(it.tags) {
			continue
		}
		tags := make(map[string]any, len(it.tags)+1)
		for k, v := range it.tags {
			tags[k] = v
		}
		tags[tagResent] = true
		front = append(front, pushItem{text: it.text, urgency: voice.UrgencySilent, requested: voice.UrgencySilent, tags: tags, at: s.now()})
	}
	s.pushQ = append(front, s.pushQ...)
	return len(front)
}
