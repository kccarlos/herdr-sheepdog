package voice

// Push arbitration shared by the providers' push queues
// (docs/dev/timelines.md §4 (a)(b)(e), C29). The queue used to look at its
// head only, so an urgent or owed line waited behind an idle line that
// was itself waiting for a pause — up to TurnTimeout, 30 s (log
// 2026-09-24: voice.push_timeout at 00:24:11 and 16:21:42, an idle head
// stalling). The queue now picks the first *eligible* push by class:
//
//   - class 0: what the user is owed or must hear now — a push
//     requested at INTERRUPT (even when downgraded to WHEN_IDLE while the
//     voice speaks), an answer to their utterance (answers_utterance), a
//     kept promise (keeps_promise_ids, due_promise), a decision they must
//     take (needs_user) and the stop reply (stop). Any ready class-0 item
//     goes first, in arrival order.
//   - class 1: plain idle news and silent context, strictly FIFO: only
//     the earliest class-1 item is ever considered, exactly as before.
//
// Two lines that answer the same utterance are never reordered. The
// queue does not coalesce (it never did); coalescing per agent happens
// upstream, in the orchestrator's event queue.

// Push tags the arbitration reads.
const (
	TagAnswersUtterance = "answers_utterance"
	TagKeepsPromiseIDs  = "keeps_promise_ids"
	TagPushID           = "push_id"
)

// Push tags that carry facts into a briefing (gemini/briefing.go).
const (
	// TagNotVerified: []string, the orchestrator's checks that did not
	// finish (brain.Reply.NotVerified).
	TagNotVerified = "not_verified"
	// TagAgents: []string, the aliases an answer is about
	// (brain.Reply.Agents).
	TagAgents = "agents"
)

// PushOwed reports whether tags mark a line the user is owed or must
// hear now: an answer to their utterance, a kept or due promise, a
// decision they must take, or the stop reply.
func PushOwed(tags map[string]any) bool {
	if tags == nil {
		return false
	}
	if id, _ := tags[TagAnswersUtterance].(string); id != "" {
		return true
	}
	if nonEmpty(tags[TagKeepsPromiseIDs]) {
		return true
	}
	return tags["due_promise"] == true || tags["needs_user"] == true || tags["stop"] == true
}

// PushFirstClass reports whether a push with this requested urgency and
// these tags may overtake plain idle news (class 0 above).
func PushFirstClass(requested Urgency, tags map[string]any) bool {
	return requested == UrgencyInterrupt || PushOwed(tags)
}

// PushUtterance is the utterance id a push answers ("" when none).
func PushUtterance(tags map[string]any) string {
	id, _ := tags[TagAnswersUtterance].(string)
	return id
}

// TagUtteranceID is the push tag naming the user utterance a reply push
// answers, for the event log's utterance_id (docs/dev/architecture.md "Event
// ids").
const TagUtteranceID = "utterance_id"

// PushUtteranceID is a push's utterance_id tag ("" when none).
func PushUtteranceID(tags map[string]any) string {
	id, _ := tags[TagUtteranceID].(string)
	return id
}

// PushID is a push's id ("" when it has none).
func PushID(tags map[string]any) string {
	id, _ := tags[TagPushID].(string)
	return id
}

func nonEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case []string:
		return len(x) > 0
	case []any:
		return len(x) > 0
	case string:
		return x != ""
	default:
		return true
	}
}

// QueuedPush is one push still waiting in a session's queue, as a
// provider switch carries it over to the new session (Switcher).
type QueuedPush struct {
	Text string
	// Urgency is what the caller asked for (not a downgrade).
	Urgency Urgency
	Tags    map[string]any
}
