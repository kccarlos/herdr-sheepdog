package flow

import (
	"cmp"
	"slices"
	"strconv"
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
	Stop         string `json:"-"` // why CANCELLED, from annotations: "timeout", "unknown" (unread) or "" (from outside)
}

// LatestChecks keeps the newest run per check, keyed by workflow and name:
// same-named checks of two workflows are two checks. An unfinished run (not
// completed) is the newest, so a queued re-run (gh prints its start as
// year 1) replaces the verdict it reruns. Otherwise by StartedAt, else
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
	return c.Kind != "StatusContext" && !strings.EqualFold(c.Status, "COMPLETED")
}

// verdict folds one check to "success", "pass" (neutral or skipped: passing,
// but nothing ran), "pending", "stopped_<timeout|unknown|external>" or
// "red". Anything unknown is "red": it is never green.
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
		return cmp.Or(map[string]string{"timeout": "stopped_timeout", "unknown": "stopped_unknown"}[c.Stop], "stopped_external")
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
	case seen["stopped_unknown"]:
		return ChecksStoppedUnknown
	case seen["stopped_external"]:
		return ChecksStoppedExternal
	case seen["pending"] || workflowPending:
		return ChecksPending
	case seen["success"]:
		return ChecksGreen
	}
	return ChecksAbsent
}

// CheckRunID returns c's numeric check-run/job ID from c.ID or DetailsURL.
func CheckRunID(c Check) int64 {
	if c.ID > 0 {
		return c.ID
	}
	id, _ := strconv.ParseInt(urlID(c.DetailsURL, "/job/"), 10, 64)
	return id
}

func urlID(u, marker string) string {
	_, after, _ := strings.Cut(u, marker)
	return after[:len(after)-len(strings.TrimLeft(after, "0123456789"))]
}

// FailingRuns extracts the unique sorted workflow run IDs, job IDs and names
// of the latest checks matching a failing or externally-stopped class.
func FailingRuns(checks []Check, class Checks) (runs, jobs, names string) {
	want := map[Checks]string{ChecksRed: "red", ChecksStoppedExternal: "stopped_external"}[class]
	if want == "" {
		return "", "", ""
	}
	var rs, js, ns []string
	add := func(dst *[]string, v string) {
		if v != "" && !slices.Contains(*dst, v) {
			*dst = append(*dst, v)
		}
	}
	for _, c := range LatestChecks(checks) {
		if c.verdict() == want {
			add(&rs, urlID(c.DetailsURL, "/actions/runs/"))
			if id := CheckRunID(c); id > 0 {
				add(&js, strconv.FormatInt(id, 10))
			}
			add(&ns, cmp.Or(c.Name, c.Context))
		}
	}
	slices.Sort(rs)
	slices.Sort(js)
	slices.Sort(ns)
	return strings.Join(rs, ","), strings.Join(js, ","), strings.Join(ns, ", ")
}
