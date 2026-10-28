package app

import (
	"encoding/json"
	"time"
)

// heldPushRestartReason is the voice.push_dropped reason for a push that
// was held (voice.push_held) when the previous daemon stopped: the queue
// is in memory, so nothing will ever say it.
const heldPushRestartReason = "restart"

// resolveHeldPushes logs voice.push_dropped {reason: restart} for every
// spoken push the event log shows held without a fate (released or
// dropped) from before this start, so the app never shows it as "Waiting
// for a listener" for good. Called from Run right after daemon.start,
// before the voice session can hold a push of its own. It reads today's
// and yesterday's day files (the hold lasts 5 minutes), and a push it
// resolves has a fate, so the next start does not log it again.
func (a *App) resolveHeldPushes(now time.Time) {
	type held struct {
		id      int64
		ts      time.Time
		payload map[string]any
	}
	open := map[string]held{}
	var order []string
	for _, day := range []time.Time{now.AddDate(0, 0, -1), now} {
		evs, err := a.elog.ReadDay(day.UTC().Format("2006-01-02"))
		if err != nil {
			continue
		}
		for _, ev := range evs {
			switch ev.Type {
			case "voice.push_held", "voice.push_released", "voice.push_dropped":
			default:
				continue
			}
			var p map[string]any
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			key := heldPushKey(p)
			if key == "" {
				continue
			}
			if ev.Type != "voice.push_held" {
				delete(open, key)
				continue
			}
			if urgency, _ := p["urgency"].(string); urgency == "silent" {
				continue
			}
			if _, seen := open[key]; !seen {
				order = append(order, key)
			}
			open[key] = held{ev.ID, ev.TS, p}
		}
	}
	for _, key := range order {
		h, ok := open[key]
		if !ok {
			continue
		}
		delete(open, key) // a key held twice is dropped once
		p := map[string]any{"reason": heldPushRestartReason, "age_ms": now.Sub(h.ts).Milliseconds()}
		for _, k := range []string{"push_id", "text", "urgency", "agent"} {
			if v, ok := h.payload[k]; ok {
				p[k] = v
			}
		}
		a.log.Log("voice", "voice.push_dropped", "", p)
	}
}

// heldPushKey identifies a push across its held / released / dropped
// events: its push_id, else its text (older daemons sent no id), the same
// key the app uses.
func heldPushKey(p map[string]any) string {
	if id, _ := p["push_id"].(string); id != "" {
		return id
	}
	if text, _ := p["text"].(string); text != "" {
		return "text:" + text
	}
	return ""
}
