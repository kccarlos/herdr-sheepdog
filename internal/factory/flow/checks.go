package flow

// Check is one item of GitHub's statusCheckRollup, as gh prints it: a
// CheckRun (Status, Conclusion) or a StatusContext (State).
type Check struct {
	Kind       string `json:"__typename"`
	Name       string `json:"name,omitempty"`
	Context    string `json:"context,omitempty"`
	Status     string `json:"status,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
	State      string `json:"state,omitempty"`
}

// verdict folds one check to "success", "pass" (neutral or skipped: passing,
// but nothing ran), "pending" or "fail". Anything unknown fails: it is never
// green. Slice 1 folds TIMED_OUT and CANCELLED into fail (design.md 7.3's
// stopped_* classes come with N33/N34).
func (c Check) verdict() string {
	if c.Kind == "StatusContext" {
		switch c.State {
		case "SUCCESS":
			return "success"
		case "PENDING", "EXPECTED":
			return "pending"
		}
		return "fail"
	}
	if c.Status != "COMPLETED" {
		return "pending"
	}
	switch c.Conclusion {
	case "SUCCESS":
		return "success"
	case "NEUTRAL", "SKIPPED":
		return "pass"
	}
	return "fail"
}

// Rollup is slice 1's checks table (design.md 7.3, first match wins): any
// failure is red; else anything not finished is pending; else at least one
// success is green; else (no checks, or only neutral and skipped) none.
// "No checks" is never green (KB-MRG-15).
func Rollup(checks []Check) Checks {
	seen := map[string]bool{}
	for _, c := range checks {
		seen[c.verdict()] = true
	}
	switch {
	case seen["fail"]:
		return ChecksRed
	case seen["pending"]:
		return ChecksPending
	case seen["success"]:
		return ChecksGreen
	}
	return ChecksNone
}
