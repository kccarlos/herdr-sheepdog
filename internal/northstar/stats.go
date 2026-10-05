package northstar

import (
	"math"
	"math/rand/v2"
	"sort"
)

// z95 is the normal quantile of a two-sided 95 % interval.
const z95 = 1.959963984540054

// BootstrapResamples and BootstrapSeed make the bootstrap intervals
// reproducible: the same data always gives the same interval.
const (
	BootstrapResamples = 2000
	BootstrapSeed      = 20261005
)

// Interval is a point estimate with its 95 % confidence interval.
type Interval struct {
	Est float64 `json:"est"`
	Lo  float64 `json:"lo"`
	Hi  float64 `json:"hi"`
}

// Rate is k successes out of n with a Wilson interval.
type Rate struct {
	K int `json:"k"`
	N int `json:"n"`
	// CI is nil when n is 0.
	CI *Interval `json:"ci,omitempty"`
	// NA says why the rate could not be computed (n is then 0).
	NA string `json:"na,omitempty"`
}

// NewRate is k of n with its Wilson interval.
func NewRate(k, n int) Rate {
	r := Rate{K: k, N: n}
	if n > 0 {
		lo, hi := Wilson(k, n)
		r.CI = &Interval{Est: float64(k) / float64(n), Lo: lo, Hi: hi}
	}
	return r
}

// Wilson is the 95 % Wilson score interval of k successes in n trials.
func Wilson(k, n int) (lo, hi float64) {
	if n <= 0 {
		return 0, 1
	}
	p, nf, z2 := float64(k)/float64(n), float64(n), z95*z95
	den := 1 + z2/nf
	mid := (p + z2/(2*nf)) / den
	half := z95 * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / den
	return math.Max(0, mid-half), math.Min(1, mid+half)
}

// Mean is a sample mean with a bootstrap interval.
type Mean struct {
	N  int       `json:"n"`
	CI *Interval `json:"ci,omitempty"`
	NA string    `json:"na,omitempty"`
}

// NewMean is the mean of xs with its percentile-bootstrap interval.
func NewMean(xs []float64) Mean {
	m := Mean{N: len(xs)}
	if len(xs) == 0 {
		return m
	}
	lo, hi := BootstrapMean(xs)
	m.CI = &Interval{Est: mean(xs), Lo: lo, Hi: hi}
	return m
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// BootstrapMean is the 95 % percentile-bootstrap interval of the mean
// (BootstrapResamples resamples, fixed seed).
func BootstrapMean(xs []float64) (lo, hi float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	rng := rand.New(rand.NewPCG(BootstrapSeed, uint64(len(xs)))) //nolint:gosec // reproducible resampling, not security
	ms := make([]float64, BootstrapResamples)
	for i := range ms {
		s := 0.0
		for range xs {
			s += xs[rng.IntN(len(xs))]
		}
		ms[i] = s / float64(len(xs))
	}
	return percentiles(ms)
}

// BootstrapMeanDiff is the 95 % percentile-bootstrap interval of
// mean(b) - mean(a), resampling the two samples independently.
func BootstrapMeanDiff(a, b []float64) (lo, hi float64) {
	if len(a) == 0 || len(b) == 0 {
		return 0, 0
	}
	rng := rand.New(rand.NewPCG(BootstrapSeed, uint64(len(a)*7919+len(b)))) //nolint:gosec // reproducible resampling, not security
	ds := make([]float64, BootstrapResamples)
	for i := range ds {
		sa, sb := 0.0, 0.0
		for range a {
			sa += a[rng.IntN(len(a))]
		}
		for range b {
			sb += b[rng.IntN(len(b))]
		}
		ds[i] = sb/float64(len(b)) - sa/float64(len(a))
	}
	return percentiles(ds)
}

func percentiles(xs []float64) (lo, hi float64) {
	sort.Float64s(xs)
	at := func(p float64) float64 {
		i := int(math.Round(p * float64(len(xs)-1)))
		return xs[max(0, min(i, len(xs)-1))]
	}
	return at(0.025), at(0.975)
}

// NewcombeDiff is the 95 % interval of p2 - p1 for two independent
// proportions k1/n1 and k2/n2 (Newcombe's hybrid score method, built from
// the two Wilson intervals).
func NewcombeDiff(k1, n1, k2, n2 int) (d, lo, hi float64) {
	if n1 == 0 || n2 == 0 {
		return 0, 0, 0
	}
	p1, p2 := float64(k1)/float64(n1), float64(k2)/float64(n2)
	l1, u1 := Wilson(k1, n1)
	l2, u2 := Wilson(k2, n2)
	d = p2 - p1
	lo = d - math.Sqrt((p2-l2)*(p2-l2)+(u1-p1)*(u1-p1))
	hi = d + math.Sqrt((u2-p2)*(u2-p2)+(p1-l1)*(p1-l1))
	return d, math.Max(-1, lo), math.Min(1, hi)
}
