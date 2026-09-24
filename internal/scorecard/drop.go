package scorecard

import "github.com/kccarlos/herdr-sheepdog/internal/eventlog"

// DropReport is docs/iteration-2.md's "brain-line drop rate
// (brain.push_late added:false, split by needs_user)", plus
// brain.fallback counted by reason (the turns that never produced a line
// at all).
type DropReport struct {
	Total               int // every brain.push_late seen
	Dropped             int // added:false
	DropRate            float64
	DroppedNeedsUser    int // added:false, needs_user:true
	DroppedNotNeedsUser int // added:false, needs_user:false
	DroppedEmptySpoken  int // added:false, spoken == ""
	FallbackByReason    map[string]int
}

type brainFallbackPayload struct {
	Reason string `json:"reason"`
}

// computeDrop implements the brain-line-drop and brain.fallback sections.
func computeDrop(events []eventlog.Event) DropReport {
	r := DropReport{FallbackByReason: map[string]int{}}
	for _, ev := range events {
		switch {
		case ev.Source == "supervisor" && ev.Type == "brain.push_late":
			r.Total++
			p := decode[pushLatePayload](ev.Payload)
			if p.Added {
				continue
			}
			r.Dropped++
			if p.NeedsUser {
				r.DroppedNeedsUser++
			} else {
				r.DroppedNotNeedsUser++
			}
			if p.Spoken == "" {
				r.DroppedEmptySpoken++
			}
		case ev.Source == "brain" && ev.Type == "brain.fallback":
			reason := decode[brainFallbackPayload](ev.Payload).Reason
			if reason == "" {
				reason = "unknown"
			}
			r.FallbackByReason[reason]++
		}
	}
	if r.Total > 0 {
		r.DropRate = float64(r.Dropped) / float64(r.Total)
	}
	return r
}
