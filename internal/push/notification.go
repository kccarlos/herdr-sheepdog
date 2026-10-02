package push

// The sheepdog.kind values.
const (
	KindApproval = "approval" // a pending answer-prompt action the app can answer
	KindBlocked  = "blocked"  // an agent waiting at something with no answerable options
	KindDone     = "done"     // a finish the shepherd asked to hear about
	KindIdle     = "idle"     // an idle the shepherd asked to hear about
)

// The aps.category values (the app registers matching
// UNNotificationCategory ids).
const (
	CategoryApproval = "APPROVAL"
	CategoryAgent    = "AGENT"
)

// Option is one choice of a pending approval prompt (1-based index, as
// POST /api/pending/answer takes it).
type Option struct {
	Index int    `json:"index"`
	Label string `json:"label"`
}

// Notification is one event the shepherd should hear about.
type Notification struct {
	// EventID is the supervisor's worker event id ("" when none was
	// minted); PendingID the answer_prompt action the event created.
	EventID, PendingID string
	PaneID             string
	// Agent is the agent's alias ("" for none: the title is "Sheepdog").
	Agent string
	// Kind is KindApproval, KindBlocked, KindDone, ….
	Kind string
	// Text is the spoken sentence: the same line the voice says.
	Text string
	// Options are the pending prompt's choices (approval only).
	Options []Option
	// NeedsYou marks a decision or action the shepherd must take: it is
	// sent time-sensitive at priority 10. Otherwise (a kept promise) the
	// alert is active at priority 5.
	NeedsYou bool
}

// Key is the dedupe key: the event id, else the pending id, else none.
func (n Notification) Key() string {
	if n.EventID != "" {
		return "evt:" + n.EventID
	}
	if n.PendingID != "" {
		return "pending:" + n.PendingID
	}
	return ""
}

// CollapseID is the apns-collapse-id: the pending id, else the event id
// (at most 64 bytes, APNs' limit).
func (n Notification) CollapseID() string {
	id := n.PendingID
	if id == "" {
		id = n.EventID
	}
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}

func (n Notification) priority() string {
	if n.NeedsYou {
		return "10"
	}
	return "5"
}

// Payload is the APNs JSON body (docs/push.md).
func (n Notification) Payload() map[string]any {
	title := n.Agent
	if title == "" {
		title = "Sheepdog"
	}
	category := CategoryAgent
	if n.PendingID != "" && len(n.Options) > 0 {
		category = CategoryApproval
	}
	aps := map[string]any{
		"alert":    map[string]any{"title": title, "body": n.Text},
		"sound":    "default",
		"category": category,
	}
	if n.PaneID != "" {
		aps["thread-id"] = n.PaneID
	}
	if n.NeedsYou {
		aps["interruption-level"] = "time-sensitive"
	} else {
		aps["interruption-level"] = "active"
	}
	sd := map[string]any{"kind": n.Kind, "pane_id": n.PaneID, "agent": n.Agent}
	if n.PendingID != "" {
		sd["pending_id"] = n.PendingID
	}
	if n.EventID != "" {
		sd["event_id"] = n.EventID
	}
	if len(n.Options) > 0 {
		sd["options"] = n.Options
	}
	return map[string]any{"aps": aps, "sheepdog": sd}
}
