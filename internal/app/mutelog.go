package app

import (
	"sync/atomic"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// mutedLogger marks the events that carry a notification with muted: true
// when the agent's space is muted: the one place that does it, for the
// Watcher, the injector and the supervisor alike. Sheepdog.app and the
// phone's own notices read it, so a muted agent's transition or question
// does not become a notification there (docs/dev/ws-contract.md "Attention").
// The mute check must not call herdr: events are logged under locks.
type mutedLogger struct {
	logging.Logger
	// muted is set once the supervisor exists.
	muted atomic.Pointer[func(paneID string) bool]
}

// notificationEvents are the event types a notification is made from.
var notificationEvents = map[string]bool{"pending.created": true, "herdr.transition": true}

// Log implements logging.Logger.
func (l *mutedLogger) Log(source, typ, paneID string, payload any) {
	if notificationEvents[typ] && paneID != "" {
		if f := l.muted.Load(); f != nil && (*f)(paneID) {
			payload = withMuted(payload)
		}
	}
	l.Logger.Log(source, typ, paneID, payload)
}

// withMuted adds "muted": true to a payload: a map, or a Transition (whose
// fields stay as they are).
func withMuted(payload any) any {
	switch p := payload.(type) {
	case map[string]any:
		out := make(map[string]any, len(p)+1)
		for k, v := range p {
			out[k] = v
		}
		out["muted"] = true
		return out
	case herdr.Transition:
		return struct {
			herdr.Transition
			Muted bool `json:"muted"`
		}{p, true}
	}
	return payload
}
