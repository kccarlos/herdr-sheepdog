package tier3

import (
	"fmt"
	"path/filepath"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

// NorthStarRuns turns tier-3 records into the north-star checks' input:
// the conversation as eval sim reads it, each run's event log from
// eventsDir (wall-clock ts, aligned on the first send or relayed utterance), and
// the end-state checks' outcomes. voice: the CEO spoke (--voice), so the
// user heard the voice model.
func NorthStarRuns(recs []Record, scs []Scenario, eventsDir string, voice bool) []northstar.Run {
	byID := map[string]eval.SimScenario{}
	for _, s := range scs {
		byID[s.ID] = s.Sim()
	}
	o := eval.NorthStarOptions{Tier: "3", Voice: voice, WallClockEvents: true}
	if voice {
		o.Tier = "3 voice"
	}
	out := make([]northstar.Run, 0, len(recs))
	for _, r := range recs {
		var evs []northstar.Event
		if eventsDir != "" {
			evs, _ = northstar.ReadEvents(filepath.Join(eventsDir, fmt.Sprintf("%s.r%d.jsonl", r.Scenario, r.Repeat)))
		}
		var sc *eval.SimScenario
		if s, ok := byID[r.Scenario]; ok {
			sc = &s
		}
		nr := eval.NorthStarRun(r.SimRun, sc, evs, o)
		nr.EndState = r.EndState
		for _, row := range r.Roster {
			for i := range nr.Agents {
				if nr.Agents[i].Alias == row.Alias {
					nr.Agents[i].Spoken = row.Spoken
				}
			}
		}
		out = append(out, nr)
	}
	return out
}
