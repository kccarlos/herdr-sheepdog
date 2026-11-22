package flow

import "fmt"

// SweepItem is one leftover or drift found by the sweep (design.md 11.3).
type SweepItem struct{ Kind, Key, Summary string }

// SweepProbe is one finder's result; Unknown != "" means the probe could not
// run and never counts as "nothing found" (design.md 11.3).
type SweepProbe struct {
	Probe, Unknown string
	Items          []SweepItem
}

// SweepWake is one coordinator wake from the project-level sweep rule.
type SweepWake struct{ ActKey, WakeKey, Summary string }

// SweepWakes emits one coordinator wake per found item unless already recorded.
// Wake keys are stable (sweep@<kind>@<key>) and permanent in the project's act
// log, so a reused terminal address or worktree path does not wake twice (accepted).
func SweepWakes(acts []Act, probes []SweepProbe) (out []SweepWake) {
	seen := map[string]bool{}
	for _, p := range probes {
		for _, it := range p.Items {
			if wk := "sweep@" + it.Kind + "@" + it.Key; p.Unknown == "" && !seen[wk] && !Done(acts, AskWake+"@"+wk) {
				seen[wk], out = true, append(out, SweepWake{AskWake + "@" + wk, wk, it.Summary})
			}
		}
	}
	return out
}

// SweepEpisodes reconciles probes against persisted active sweep items and
// unknown probe reasons, returning new wakes, newly unknown probes, and
// recovered probe names. Unknown probes never clear active items.
func SweepEpisodes(active, unknown *map[string]string, probes []SweepProbe) (wakes []SweepWake, unknownProbes, unknownMsgs, recovered []string) {
	for _, p := range probes {
		if p.Unknown != "" {
			if (*unknown)[p.Probe] != p.Unknown {
				if *unknown == nil {
					*unknown = map[string]string{}
				}
				(*unknown)[p.Probe] = p.Unknown
				unknownProbes = append(unknownProbes, p.Probe)
				unknownMsgs = append(unknownMsgs, fmt.Sprintf("%s unknown: %s", p.Probe, p.Unknown))
			}
			continue
		}
		if _, ok := (*unknown)[p.Probe]; ok {
			delete(*unknown, p.Probe)
			recovered = append(recovered, p.Probe)
		}
		present := map[string]bool{}
		for _, it := range p.Items {
			wk := "sweep@" + it.Kind + "@" + it.Key
			present[wk] = true
			if (*active)[wk] == "" {
				if *active == nil {
					*active = map[string]string{}
				}
				(*active)[wk] = p.Probe
				wakes = append(wakes, SweepWake{AskWake + "@" + wk, wk, it.Summary})
			}
		}
		for wk, pr := range *active {
			if pr == p.Probe && !present[wk] {
				delete(*active, wk)
			}
		}
	}
	if len(*active) == 0 {
		*active = nil
	}
	if len(*unknown) == 0 {
		*unknown = nil
	}
	return wakes, unknownProbes, unknownMsgs, recovered
}
