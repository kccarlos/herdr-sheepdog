package eval

import (
	"fmt"
	"io"
)

// WriteSimTable prints one line per simulated run: status, end reason,
// turns, primary failure and cost, then the failures and the judge's
// rationales for whoever reads a failure.
func WriteSimTable(w io.Writer, runs []SimRun) {
	idw := 8
	for _, r := range runs {
		idw = max(idw, len(r.Scenario))
	}
	total := 0.0
	for _, r := range runs {
		total += r.CostUSD
		_, _ = fmt.Fprintf(w, "  %-7s %-*s r%-2d %-9s %2d turns %6.1fs  %-22s ≈ $%.4f\n", r.Status, idw, r.Scenario, r.Repeat, r.EndReason, r.Turns, float64(r.DurationMS)/1000, string(Primary(r.Failures)), r.CostUSD)
		for _, f := range r.Failures {
			_, _ = fmt.Fprintf(w, "          %s: %s\n", f.Type, firstLine(f.Detail))
		}
	}
	_, _ = fmt.Fprintf(w, "-- %d runs, spend ≈ $%.4f (estimate at list prices) --\n", len(runs), total)
}
