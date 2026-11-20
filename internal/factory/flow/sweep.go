package flow

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
