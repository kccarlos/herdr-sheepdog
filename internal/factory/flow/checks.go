package flow

import (
	"cmp"
	"strings"
)

// Check is one item of GitHub's statusCheckRollup (or REST check-runs): a
// CheckRun (Status, Conclusion) or a StatusContext (State).
type Check struct {
	Kind         string `json:"__typename"`
	ID           int64  `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	Context      string `json:"context,omitempty"`
	Status       string `json:"status,omitempty"`
	Conclusion   string `json:"conclusion,omitempty"`
	State        string `json:"state,omitempty"`
	StartedAt    string `json:"startedAt,omitempty"`
	WorkflowName string `json:"workflowName,omitempty"` // or the REST check suite: part of the check's key
	DetailsURL   string `json:"detailsUrl,omitempty"`
	TimedOut     bool   `json:"-"` // set from check-run annotations when CANCELLED hit a job timeout
}

// LatestChecks keeps the newest run per check, keyed by workflow and name:
// same-named checks of two workflows are two checks. An unfinished run (not
// completed, or no start time yet: gh prints year 1) is the newest, so a
// queued re-run replaces the verdict it reruns. Otherwise by StartedAt, else
// ID, else later position. Unnamed checks are kept as-is.
func LatestChecks(checks []Check) []Check {
	idx, out := map[string]int{}, make([]Check, 0, len(checks))
	for _, c := range checks {
		name := cmp.Or(c.Name, c.Context)
		if prev, ok := idx[c.WorkflowName+"/"+name]; name != "" && ok {
			b := out[prev]
			if cu, bu := c.unfinished(), b.unfinished(); cu != bu {
				if cu {
					out[prev] = c
				}
			} else if c.StartedAt != "" && b.StartedAt != "" && c.StartedAt != b.StartedAt {
				if c.StartedAt > b.StartedAt {
					out[prev] = c
				}
			} else if c.ID == 0 || b.ID == 0 || c.ID >= b.ID {
				out[prev] = c
			}
			continue
		}
		if name != "" {
			idx[c.WorkflowName+"/"+name] = len(out)
		}
		out = append(out, c)
	}
	return out
}

func (c Check) unfinished() bool {
	return c.Kind != "StatusContext" && (!strings.EqualFold(c.Status, "COMPLETED") || c.StartedAt == "" || strings.HasPrefix(c.StartedAt, "0001-"))
}

// verdict folds one check to "success", "pass" (neutral or skipped: passing,
// but nothing ran), "pending", "stopped_timeout", "stopped_external" or "red".
// Anything unknown is "red": it is never green.
func (c Check) verdict() string {
	if c.Kind == "StatusContext" {
		switch strings.ToUpper(c.State) {
		case "SUCCESS":
			return "success"
		case "PENDING", "EXPECTED":
			return "pending"
		}
		return "red"
	}
	if strings.ToUpper(c.Status) != "COMPLETED" {
		return "pending"
	}
	switch strings.ToUpper(c.Conclusion) {
	case "SUCCESS":
		return "success"
	case "NEUTRAL", "SKIPPED":
		return "pass"
	case "TIMED_OUT":
		return "stopped_timeout"
	case "CANCELLED":
		if c.TimedOut {
			return "stopped_timeout"
		}
		return "stopped_external"
	}
	return "red"
}

// Rollup is the checks rollup table (design.md 7.3, first match wins), over
// the newest run per check. "No checks" is never green (KB-MRG-15).
func Rollup(checks []Check, workflowPending bool) Checks {
	latest := LatestChecks(checks)
	seen := map[string]bool{}
	for _, c := range latest {
		seen[c.verdict()] = true
	}
	switch {
	case seen["red"]:
		return ChecksRed
	case seen["stopped_timeout"]:
		return ChecksStoppedTimeout
	case seen["stopped_external"]:
		return ChecksStoppedExternal
	case seen["pending"] || workflowPending:
		return ChecksPending
	case seen["success"]:
		return ChecksGreen
	}
	return ChecksAbsent
}
