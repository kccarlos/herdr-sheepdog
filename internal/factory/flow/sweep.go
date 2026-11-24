package flow

import "slices"

// SweepItem is one leftover or drift found by the sweep (design.md 11.3).
type SweepItem struct{ Kind, Key, Summary string }

// SweepProbe is one finder's result; Unknown != "" means the probe could not
// run and never counts as "nothing found" (design.md 11.3).
type SweepProbe struct {
	Probe, Unknown string
	Items          []SweepItem
}

// SweepNotice is one new leftover wake (WakeKey != "") or unknown probe from SweepEpisodes.
type SweepNotice struct{ ActKey, WakeKey, Summary string }

// SweepEpisodes reconciles probes against persisted active sweep items and
// unknown probes, clearing recovered held wakes and returning new notices.
func SweepEpisodes(active, unknown *map[string]string, wakes *[]Wake, probes []SweepProbe) (notices []SweepNotice, changed bool) {
	put := func(m *map[string]string, k, v string) {
		if *m == nil {
			*m = map[string]string{}
		}
		(*m)[k], changed = v, true
	}
	for _, p := range probes {
		if p.Unknown != "" {
			if (*unknown)[p.Probe] == "" {
				put(unknown, p.Probe, p.Unknown)
				notices = append(notices, SweepNotice{ActKey: "sweep_unknown@" + p.Probe, Summary: p.Probe + " unknown: " + p.Unknown})
			}
			continue
		}
		if (*unknown)[p.Probe] != "" {
			delete(*unknown, p.Probe)
			changed = true
		}
		present := map[string]bool{}
		for _, it := range p.Items {
			wk := "sweep@" + it.Kind + "@" + it.Key
			present[wk] = true
			if (*active)[wk] == "" {
				put(active, wk, p.Probe)
				notices = append(notices, SweepNotice{AskWake + "@" + wk, wk, it.Summary})
			}
		}
		for wk, pr := range *active {
			if pr == p.Probe && !present[wk] {
				delete(*active, wk)
				changed = true
				if wakes != nil {
					*wakes = slices.DeleteFunc(*wakes, func(w Wake) bool { return w.Task == "" && w.Key == wk })
				}
			}
		}
	}
	for _, m := range []*map[string]string{active, unknown} {
		if len(*m) == 0 {
			*m = nil
		}
	}
	return notices, changed
}
