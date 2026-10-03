package eval

import (
	"fmt"
	"io"
	"sort"
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
	// SkippedChecks names the checks the case sets that this run could
	// not apply (offline: the live-only ones about phrasing and
	// judgement), so a pass never hides what was not checked
	// (docs/iteration-2.md WP0b).
	SkippedChecks []string
	// Pending is the case's pending_check: what it should assert but the
	// harness cannot yet.
	Pending string
	// Baseline is the case's grade against the logged behaviour, when it
	// carries one ("fails as logged: …" / "passes as logged").
	Baseline string
	// Verdicts are the judge's answers, when a judge ran (live).
	Verdicts []Verdict
	// Failures label a failed result with the fixed taxonomy.
	Failures []Failure
}

// RunStatus maps a result onto the run statuses repeat statistics use: a
// skipped case never ran, and a failure that is only a harness error
// (a rate limit, a judge error) is an errored run.
func (r Result) RunStatus() RunStatus {
	switch r.Status {
	case StatusPass:
		return RunPass
	case StatusSkipped:
		return RunNotRun
	}
	if s := statusOf(r.Failures); s == RunError {
		return RunError
	}
	return RunFail
}

// WriteTable prints a pass/fail table and a summary line to w: per case
// its status and detail, the checks skipped, what is pending and the
// baseline grade; then the counts, which checks were skipped how often,
// and how many cases have a pending check.
func WriteTable(w io.Writer, set string, results []Result) {
	_, _ = fmt.Fprintf(w, "== eval --set %s (%d cases) ==\n", set, len(results))
	idw := 0
	for _, r := range results {
		if len(r.ID) > idw {
			idw = len(r.ID)
		}
	}
	pass, fail, skip, pending, baseFails := 0, 0, 0, 0, 0
	skippedChecks := map[string]int{}
	for _, r := range results {
		switch r.Status {
		case StatusPass:
			pass++
		case StatusFail:
			fail++
		case StatusSkipped:
			skip++
		}
		for _, c := range r.SkippedChecks {
			skippedChecks[c]++
		}
		if r.Pending != "" {
			pending++
		}
		if strings.HasPrefix(r.Baseline, "fails") {
			baseFails++
		}
		detail := r.Detail
		if detail == "" && r.Status == StatusPass {
			detail = summarizeGot(r.Got)
		}
		_, _ = fmt.Fprintf(w, "  %-4s  %-*s  %6s  %s\n", r.Status, idw, r.ID, r.Duration.Round(time.Millisecond), detail)
		if len(r.SkippedChecks) > 0 && (r.Status != StatusPass || r.Pending != "" || r.Baseline != "") {
			// Every skipped check is counted in the summary; per case it
			// is listed where someone reads the case anyway, so hundreds
			// of generated passes do not triple the table.
			_, _ = fmt.Fprintf(w, "        skipped (live only): %s\n", strings.Join(r.SkippedChecks, ", "))
		}
		if r.Pending != "" {
			_, _ = fmt.Fprintf(w, "        pending: %s\n", r.Pending)
		}
		if r.Baseline != "" {
			_, _ = fmt.Fprintf(w, "        baseline: %s\n", firstLine(r.Baseline))
		}
		for _, v := range r.Verdicts {
			if v.Passed() {
				continue
			}
			why := v.Rationale
			if v.Err != "" {
				why = "error: " + v.Err
			}
			_, _ = fmt.Fprintf(w, "        judge %s: %s — %s\n", orDefault(string(v.Answer), "error"), firstLine(v.Criterion), firstLine(why))
		}
	}
	_, _ = fmt.Fprintf(w, "-- %d pass, %d fail, %d skipped (of %d) --\n", pass, fail, skip, len(results))
	if len(skippedChecks) > 0 {
		names := make([]string, 0, len(skippedChecks))
		for n := range skippedChecks {
			names = append(names, n)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, n := range names {
			parts = append(parts, fmt.Sprintf("%s ×%d", n, skippedChecks[n]))
		}
		_, _ = fmt.Fprintf(w, "-- checks skipped offline (live only): %s --\n", strings.Join(parts, ", "))
	}
	if pending > 0 || baseFails > 0 {
		_, _ = fmt.Fprintf(w, "-- %d with a pending check; %d fail against the logged baseline --\n", pending, baseFails)
	}
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

// firstLine truncates a (possibly multi-line, e.g. a 429's quota-failure
// body) error message to its first line and 200 runes, for the table.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
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

// WriteSpend prints the token counts and the estimated spend per role
// and model (the prices are estimates: eval.Prices). Nothing for an
// empty meter.
func WriteSpend(w io.Writer, m *Meter) {
	lines := m.Lines()
	if len(lines) == 0 {
		return
	}
	total := 0.0
	for _, l := range lines {
		total += l.CostUSD
		_, _ = fmt.Fprintf(w, "-- spend %-8s %-24s calls %5d  in %9d  out %8d  reasoning %8d  ≈ $%.4f --\n", l.Role, l.Model, l.Usage.Calls, l.Usage.In, l.Usage.Out, l.Usage.Reasoning, l.CostUSD)
	}
	_, _ = fmt.Fprintf(w, "-- spend total ≈ $%.4f (estimate at list prices) --\n", total)
}
