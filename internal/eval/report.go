package eval

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Status of one case's result.
type Status string

// The statuses a Result may hold.
const (
	StatusPass    Status = "PASS"
	StatusFail    Status = "FAIL"
	StatusSkipped Status = "SKIP"
)

// Result is one case's outcome.
type Result struct {
	ID       string
	Status   Status
	Detail   string
	Err      error
	Duration time.Duration
	// Got is optional extra context for the table (assistant: tool/agent/
	// text; svp: tools called).
	Got map[string]any
}

// WriteTable prints a pass/fail table and a summary line to w.
func WriteTable(w io.Writer, set string, results []Result) {
	_, _ = fmt.Fprintf(w, "== eval --set %s (%d cases) ==\n", set, len(results))
	idw := 0
	for _, r := range results {
		if len(r.ID) > idw {
			idw = len(r.ID)
		}
	}
	pass, fail, skip := 0, 0, 0
	for _, r := range results {
		switch r.Status {
		case StatusPass:
			pass++
		case StatusFail:
			fail++
		case StatusSkipped:
			skip++
		}
		detail := r.Detail
		if detail == "" && r.Status == StatusPass {
			detail = summarizeGot(r.Got)
		}
		_, _ = fmt.Fprintf(w, "  %-4s  %-*s  %6s  %s\n", r.Status, idw, r.ID, r.Duration.Round(time.Millisecond), detail)
	}
	_, _ = fmt.Fprintf(w, "-- %d pass, %d fail, %d skipped (of %d) --\n", pass, fail, skip, len(results))
}

func summarizeGot(got map[string]any) string {
	if len(got) == 0 {
		return ""
	}
	var parts []string
	for _, k := range []string{"tool", "agent", "text", "tools_called", "spoken"} {
		if v, ok := got[k]; ok {
			s := fmt.Sprint(v)
			if s == "" {
				continue
			}
			parts = append(parts, k+"="+s)
		}
	}
	return strings.Join(parts, " ")
}

// HasFailures reports whether any result failed.
func HasFailures(results []Result) bool {
	for _, r := range results {
		if r.Status == StatusFail {
			return true
		}
	}
	return false
}
