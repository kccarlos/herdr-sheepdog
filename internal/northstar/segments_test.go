package northstar

import (
	"strings"
	"testing"
)

func seg(kind string, vs ...string) SegmentVerdicts {
	s := SegmentVerdicts{Kind: kind}
	for i := 0; i+1 < len(vs); i += 2 {
		s.Verdicts = append(s.Verdicts, Verdict{Name: vs[i], Answer: vs[i+1]})
	}
	return s
}

// Segment Understood: a request segment passes when answered and every
// length item are yes; announcements do not count; numbered items fold
// into families; the strict per-run reading needs every segment.
func TestSummarizeSegments(t *testing.T) {
	runs := []Run{
		{Scenario: "a", Status: "pass", Segments: []SegmentVerdicts{
			seg("announcement", "length_fits", "no"),
			seg("request", "answered", "yes", "length_fits", "yes", "no_unintended_action", "yes", "clarify_1", "yes"),
			seg("request", "answered", "yes", "length_fits", "no", "right_agent", "no"),
		}},
		{Scenario: "b", Status: "fail", Segments: []SegmentVerdicts{
			seg("request", "answered", "yes", "length_fits", "yes", "clarify_2", "no"),
		}},
		{Scenario: "c", Status: "error", Segments: []SegmentVerdicts{seg("request", "answered", "no")}},
		{Scenario: "d", Status: "pass"},
	}
	b := SummarizeSegments(runs)
	if b == nil || b.Runs != 2 || b.Segments != 4 || b.Requests != 3 {
		t.Fatalf("%+v", b)
	}
	if b.Understood.K != 2 || b.Understood.N != 3 || b.Understood.Clusters != 2 || b.Understood.CI == nil {
		t.Fatalf("understood %+v", b.Understood)
	}
	if b.Safe.K != 1 || b.Safe.N != 2 {
		t.Fatalf("safe %+v", b.Safe)
	}
	if b.AllUnderstood.K != 1 || b.AllUnderstood.N != 2 {
		t.Fatalf("all %+v", b.AllUnderstood)
	}
	var fam *SegmentItemRate
	for i := range b.Items {
		if b.Items[i].Item == "clarify_*" {
			fam = &b.Items[i]
		}
	}
	if fam == nil || fam.Rate.K != 1 || fam.Rate.N != 2 {
		t.Fatalf("items %+v", b.Items)
	}
	var sb strings.Builder
	Summarize("1", runs).WriteMarkdown(&sb)
	if !strings.Contains(sb.String(), "### Segment level") || !strings.Contains(sb.String(), "| **Understood** | 2 / 3 |") {
		t.Fatal(sb.String())
	}
	if SummarizeSegments([]Run{{Status: "pass"}}) != nil {
		t.Fatal("no segments: nil block")
	}
}

// The cluster bootstrap resamples runs: identical runs give a degenerate
// interval, and it is wider than a naive interval when runs differ.
func TestClusterBootstrap(t *testing.T) {
	lo, hi := ClusterBootstrap([]int{3, 3, 3}, []int{6, 6, 6})
	if lo != 0.5 || hi != 0.5 {
		t.Fatalf("identical clusters: %v %v", lo, hi)
	}
	ks, ns := []int{10, 0, 10, 0, 10, 0}, []int{10, 10, 10, 10, 10, 10}
	lo, hi = ClusterBootstrap(ks, ns)
	wlo, whi := Wilson(30, 60)
	if hi-lo <= whi-wlo {
		t.Fatalf("cluster %v–%v not wider than Wilson %v–%v", lo, hi, wlo, whi)
	}
	l2, h2 := ClusterBootstrap(ks, ns)
	if l2 != lo || h2 != hi {
		t.Fatal("not reproducible")
	}
	dlo, dhi := ClusterBootstrapDiff([]int{3, 3}, []int{6, 6}, []int{6, 6}, []int{6, 6})
	if dlo != 0.5 || dhi != 0.5 {
		t.Fatalf("diff %v %v", dlo, dhi)
	}
}
