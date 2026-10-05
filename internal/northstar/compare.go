package northstar

import (
	"context"
	"fmt"
	"io"
	"sort"
)

// Diff is one north-star metric in two arms: B minus A with its 95 % CI
// (Newcombe for rates, bootstrap for means).
type Diff struct {
	Metric string  `json:"metric"`
	A      string  `json:"a"`
	B      string  `json:"b"`
	D      float64 `json:"diff"`
	Lo     float64 `json:"lo"`
	Hi     float64 `json:"hi"`
	// OK is false when either arm has no data for the metric.
	OK bool `json:"ok"`
}

// Comparison is arm A against arm B.
type Comparison struct {
	A     *Block   `json:"a"`
	B     *Block   `json:"b"`
	Diffs []Diff   `json:"diffs"`
	Notes []string `json:"notes,omitempty"`
}

// PairwiseJudge is the hook for pairwise judging (a later step of
// "Evals first"): for a style dimension such as depth or tone, which of
// two transcripts of the same scenario is better ("a", "b" or "tie").
// Pass/fail stays for safety. Nothing calls it yet.
type PairwiseJudge interface {
	Prefer(ctx context.Context, dimension, transcriptA, transcriptB string) (string, error)
}

// Compare computes B - A for every north-star metric, and the
// diagnostics' rates, from the two arms' runs.
func Compare(a, b []Run) *Comparison {
	ba, bb := Summarize("A", a), Summarize("B", b)
	c := &Comparison{A: ba, B: bb}
	rate := func(name string, ra, rb Rate) {
		d := Diff{Metric: name, A: rateShort(ra), B: rateShort(rb)}
		if ra.N > 0 && rb.N > 0 {
			d.D, d.Lo, d.Hi = NewcombeDiff(ra.K, ra.N, rb.K, rb.N)
			d.OK = true
		}
		c.Diffs = append(c.Diffs, d)
	}
	means := func(name string, xa, xb []float64, unit string) {
		d := Diff{Metric: name, A: meanCI(NewMean(xa), unit, 1), B: meanCI(NewMean(xb), unit, 1)}
		if len(xa) > 0 && len(xb) > 0 {
			d.D = mean(xb) - mean(xa)
			d.Lo, d.Hi = BootstrapMeanDiff(xa, xb)
			d.OK = true
		}
		c.Diffs = append(c.Diffs, d)
	}
	rate("Understood", ba.Understood, bb.Understood)
	rate("Done", ba.Done, bb.Done)
	rate("Low effort", ba.LowEffort, bb.LowEffort)
	rate("Safe", ba.Safe, bb.Safe)
	means("Turns to goal", goalTurns(ba), goalTurns(bb), "")
	means("Time to goal", goalTimes(ba), goalTimes(bb), " s")
	rate("Unmatched claims (heard)", ba.Claims.Unmatched, bb.Claims.Unmatched)
	rate("Actions never told", ba.Claims.Unmentioned, bb.Claims.Unmentioned)
	rate("Missed news", ba.MissedNews, bb.MissedNews)
	sa, sb := scenarioSet(a), scenarioSet(b)
	var onlyA, onlyB []string
	for s := range sa {
		if !sb[s] {
			onlyA = append(onlyA, s)
		}
	}
	for s := range sb {
		if !sa[s] {
			onlyB = append(onlyB, s)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	if len(onlyA)+len(onlyB) > 0 {
		c.Notes = append(c.Notes, fmt.Sprintf("The arms ran different scenarios (only in A: %v; only in B: %v): a difference may come from the scenario mix.", onlyA, onlyB))
	}
	return c
}

func goalTurns(b *Block) []float64 {
	var out []float64
	for _, o := range b.Outcomes {
		if o.Effort.GoalReached && o.HasRecord && (o.Status == "pass" || o.Status == "fail") {
			out = append(out, float64(o.Effort.Turns))
		}
	}
	return out
}

func goalTimes(b *Block) []float64 {
	var out []float64
	for _, o := range b.Outcomes {
		if o.Effort.GoalReached && o.HasRecord && o.Effort.TimeToGoal > 0 && (o.Status == "pass" || o.Status == "fail") {
			out = append(out, float64(o.Effort.TimeToGoal)/1000)
		}
	}
	return out
}

func scenarioSet(runs []Run) map[string]bool {
	m := map[string]bool{}
	for _, r := range runs {
		m[r.Scenario] = true
	}
	return m
}

// WriteMarkdown renders the comparison.
func (c *Comparison) WriteMarkdown(w io.Writer, nameA, nameB string) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("# North star: A vs B\n\n- A: %s (%d runs, %d graded)\n- B: %s (%d runs, %d graded)\n\n", nameA, c.A.Runs, c.A.Graded, nameB, c.B.Runs, c.B.Graded)
	p("Difference = B − A with its 95 %% CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.\n\n")
	p("| Metric | A | B | B − A | 95 %% CI |\n|---|---|---|---|---|\n")
	for _, d := range c.Diffs {
		if !d.OK {
			p("| %s | %s | %s | n/a | – |\n", d.Metric, d.A, d.B)
			continue
		}
		if d.Metric == "Turns to goal" || d.Metric == "Time to goal" {
			p("| %s | %s | %s | %+.2f | %+.2f to %+.2f |\n", d.Metric, d.A, d.B, d.D, d.Lo, d.Hi)
			continue
		}
		p("| %s | %s | %s | %+.0f pp | %+.0f to %+.0f pp |\n", d.Metric, d.A, d.B, 100*d.D, 100*d.Lo, 100*d.Hi)
	}
	for _, n := range c.Notes {
		p("\n- %s\n", n)
	}
	p("\nPairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.\n")
}
