package supervisor

import "github.com/kccarlos/herdr-sheepdog/internal/ctxcap"

// Link ids tie a piece of the brain's request to its counterpart in the
// voice model's context (the /debug Context tab, docs/architecture.md
// "Context capture"): a push's id travels on its voice.push tags and on
// the brain's push message; a worker event's id on the brain's
// worker_event message, the announce turn's request and every push about
// that event; the voice tool call id on the utterance turn's request and
// on a late reply's push.

// The push tags carrying link ids (ctxcap.LinksFromTags reads them).
const (
	tagPushID  = ctxcap.TagPushID
	tagEventID = ctxcap.TagEventID
	tagCallID  = ctxcap.TagCallID
)

// withPushID returns tags with a fresh push id added (a copy: the
// caller's map is not changed).
func (s *Supervisor) withPushID(tags map[string]any) map[string]any {
	s.mu.Lock()
	id := s.nextID("push")
	s.mu.Unlock()
	out := make(map[string]any, len(tags)+1)
	for k, v := range tags {
		out[k] = v
	}
	out[tagPushID] = id
	return out
}

// newEventID mints the id of a worker event on paneID and remembers it
// as the pane's latest.
func (s *Supervisor) newEventID(paneID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID("evt")
	if s.eventIDs == nil {
		s.eventIDs = map[string]string{}
	}
	s.eventIDs[paneID] = id
	return id
}

// eventIDOf is paneID's latest worker event id ("" when none).
func (s *Supervisor) eventIDOf(paneID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventIDs[paneID]
}

// tags returns tags with the announcement's event id added.
func (a announcement) tags(tags map[string]any) map[string]any {
	if a.eventID == "" && a.pending == "" {
		return tags
	}
	out := make(map[string]any, len(tags)+2)
	for k, v := range tags {
		out[k] = v
	}
	if a.eventID != "" {
		out[tagEventID] = a.eventID
	}
	if a.pending != "" {
		// The answer_prompt the line announces (buried.go).
		out[tagPendingID] = a.pending
	}
	return out
}

func pushLink(tags map[string]any) string {
	id, _ := tags[tagPushID].(string)
	return ctxcap.PushLink(id)
}

func eventLink(id string) string { return ctxcap.EventLink(id) }

func callLink(id string) string { return ctxcap.CallLink(id) }
