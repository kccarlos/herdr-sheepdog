package scorecard

import (
	"encoding/json"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

// computeNorthStar is the north-star block of a slice of the real event
// log (internal/northstar): said vs done, missed news and user effort
// over its items; Understood needs a judge and is not computed.
func computeNorthStar(label string, events []eventlog.Event) *northstar.SessionBlock {
	if len(events) == 0 {
		return nil
	}
	origin := events[0].TS
	evs := make([]northstar.Event, 0, len(events))
	for _, e := range events {
		ne := northstar.Event{TS: e.TS, AtMS: e.TS.Sub(origin).Milliseconds(), Type: e.Type, Pane: e.PaneID}
		if len(e.Payload) > 0 {
			_ = json.Unmarshal(e.Payload, &ne.Payload)
		}
		evs = append(evs, ne)
	}
	return northstar.SummarizeSession(northstar.RunFromLog(label, evs))
}
