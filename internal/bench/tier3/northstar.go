package tier3

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

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
		nr.EndState = append(worldChecks(r.Checks), r.EndState...)
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

// worldCheck matches a code-check line about the world after the call:
// a command in a worker's repo or a worker's final screen (the event and
// voice checks are about the conversation, not the end state).
var worldCheck = regexp.MustCompile(`^(PASS|FAIL) (screen \S+|.+ in \S+: exit -?\d+ \(want -?\d+\))`)

// worldChecks are a record's expect.commands and expect.screens outcomes
// as end-state checks: they are checked against the world too.
func worldChecks(lines []string) []northstar.EndCheck {
	var out []northstar.EndCheck
	for _, l := range lines {
		if m := worldCheck.FindStringSubmatch(l); m != nil {
			out = append(out, northstar.EndCheck{Name: strings.TrimPrefix(strings.TrimPrefix(l, "PASS "), "FAIL "), Pass: m[1] == "PASS"})
		}
	}
	return out
}
