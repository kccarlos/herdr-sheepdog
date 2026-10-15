package scorecard

import (
	"math"
	"sort"
)

// Stats is a mean/median/p90 summary of a set of numbers (a latency in
// ms, a token count), used wherever docs/dev/iteration-2.md asks for a
// "distribution".
type Stats struct {
	N      int
	Mean   float64
	Median float64
	P90    float64
}

// computeStats summarises values (order not required). N is 0 and every
// other field is the zero value for an empty input.
func computeStats(values []float64) Stats {
	n := len(values)
	if n == 0 {
		return Stats{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	return Stats{
		N:      n,
		Mean:   sum / float64(n),
		Median: percentile(sorted, 50),
		P90:    percentile(sorted, 90),
	}
}

// percentile returns the p-th percentile (0-100) of sorted (already
// ascending) by linear interpolation between the two nearest ranks.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := p / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}
