// Package earmetrics measures how a single spoken line reads to a
// user who is listening, not reading: length, cadence, technical
// noise and jargon that `internal/prose`'s D9 scrubber (Spoken) does not
// already strip. It is pure (no I/O, no clock) so `internal/scorecard`
// and tests can call it over any text, live or recorded.
//
// docs/iteration-2.md "Scorecard metrics" is the brief: "words, sentences,
// dashes/semicolons, technical tokens (PR numbers, ms, versions) and
// unfamiliar terms per spoken line". This package is the pure-function
// half of that; `internal/scorecard` is the part that walks the event
// log and decides which lines to measure.
package earmetrics

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Closers is the small, checked-in list of short closing sentences a
// spoken line may end with instead of a question (J1/J4 in
// docs/iteration-2.md: "closes explicitly when there is no ask"). It is a
// package var, like Jargon, so a real transcript can add to it.
var Closers = []string{
	"That's all.",
	"Nothing needs you.",
	"Done.",
}

// Metrics is what Measure reports about one spoken line.
type Metrics struct {
	Words            int
	Sentences        int
	Dashes           int // " — ", " – ", " -- "
	Semicolons       int
	Technical        int // PR numbers, ms values, versions, git hashes, file paths
	EndsWithQuestion bool
	EndsWithCloser   bool
	Jargon           []string // jargon terms found, in order, one entry per hit
}

var (
	// reSentence mirrors internal/prose's sentence-boundary regexp
	// exactly (see firstSentences in prose.go) so Sentences counts
	// sentences the same way prose.Spoken cuts them.
	reSentence = regexp.MustCompile(`[.!?]+(?:["')\]]*)(?:\s+|$)`)

	// rePRNumber matches a PR/issue number like "#49".
	rePRNumber = regexp.MustCompile(`#\d+`)

	// reMillis matches a millisecond value like "1400 ms" or "150ms".
	reMillis = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?\s?ms\b`)

	// reVersion matches a version string like "v2.1.0" or "2.1.0": an
	// optional leading "v" and two or three dot-separated number groups.
	// It requires a dot between digits, so a lone integer like "245"
	// (a test count, an agent count) never counts as a version.
	reVersion = regexp.MustCompile(`\bv?\d+\.\d+(?:\.\d+)?\b`)

	// reHash matches a candidate git hash: 7-40 lowercase hex characters.
	// isHexHash below additionally requires at least one digit and one
	// a-f letter, so a plain word that happens to be hex-alphabet only
	// ("dead", "cafe", "face") is not counted (mirrors prose.reHash's
	// same guard).
	reHash = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)

	// reFilePath matches a path with at least three "/"-separated
	// segments (two slashes), the same threshold internal/prose's rePath
	// uses to avoid firing on an ordinary word pair like "and/or".
	reFilePath = regexp.MustCompile(`\b[\w.@+\-]+(?:/[\w.@+\-]+){2,}\b`)

	reNonWord = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)
)

// Measure reports the ear metrics of one spoken line.
func Measure(text string) Metrics {
	trimmed := strings.TrimSpace(text)
	m := Metrics{
		Words:            len(strings.Fields(trimmed)),
		Sentences:        countSentences(trimmed),
		Dashes:           countDashes(trimmed),
		Semicolons:       strings.Count(trimmed, ";"),
		Technical:        countTechnical(trimmed),
		EndsWithQuestion: strings.HasSuffix(trimmed, "?"),
		EndsWithCloser:   endsWithCloser(trimmed),
		Jargon:           findJargon(trimmed),
	}
	return m
}

// countSentences counts sentence-ending punctuation the way
// prose.firstSentences does, plus one more for a trailing clause with no
// terminal punctuation (a line that trails off still counts as at least
// one sentence when it has any words).
func countSentences(s string) int {
	if s == "" {
		return 0
	}
	ends := reSentence.FindAllStringIndex(s, -1)
	n := len(ends)
	if n == 0 {
		return 1
	}
	if last := ends[n-1]; strings.TrimSpace(s[last[1]:]) != "" {
		n++
	}
	return n
}

// countDashes counts em/en dashes and double-hyphens used as a dash, each
// surrounded by spaces (a compound word like "dead-code" is not a dash).
func countDashes(s string) int {
	return strings.Count(s, " — ") + strings.Count(s, " – ") + strings.Count(s, " -- ")
}

// countTechnical counts PR numbers, ms values, version strings, git
// hashes and file paths. Each regexp is applied independently (not
// combined into one alternation) so overlap between, say, a version
// number and a hash-shaped token cannot suppress a real match.
func countTechnical(s string) int {
	n := len(rePRNumber.FindAllString(s, -1))
	n += len(reMillis.FindAllString(s, -1))
	n += len(reVersion.FindAllString(s, -1))
	n += len(reFilePath.FindAllString(s, -1))
	for _, h := range reHash.FindAllString(s, -1) {
		if isHexHash(h) {
			n++
		}
	}
	return n
}

// isHexHash reports whether m looks like a real hex hash rather than an
// ordinary word that happens to use only hex letters (e.g. "dead",
// "face"): it must contain at least one digit and one a-f letter.
func isHexHash(m string) bool {
	return strings.ContainsAny(m, "0123456789") && strings.ContainsAny(m, "abcdef")
}

// endsWithCloser reports whether s ends with one of Closers.
func endsWithCloser(s string) bool {
	for _, c := range Closers {
		if strings.HasSuffix(s, c) {
			return true
		}
	}
	return false
}

// findJargon returns one entry per jargon hit, in the order the terms
// appear in Jargon (not the order they occur in s); a multi-word term
// like "tool surface" is matched as a phrase.
func findJargon(s string) []string {
	var hits []string
	for _, term := range Jargon {
		re := jargonRegexp(term)
		hits = append(hits, re.FindAllString(s, -1)...)
	}
	return hits
}

var jargonRegexpCache = map[string]*regexp.Regexp{}

// jargonRegexp compiles (and caches) a case-insensitive whole-word/phrase
// matcher for term.
func jargonRegexp(term string) *regexp.Regexp {
	if re, ok := jargonRegexpCache[term]; ok {
		return re
	}
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(term) + `\b`)
	jargonRegexpCache[term] = re
	return re
}

// Summary is what Aggregate reports over a set of spoken lines.
type Summary struct {
	N                    int
	MeanWords            float64
	MedianWords          float64
	P90Words             float64
	ShareOverThirtyWords float64 // fraction of lines with Words > 30
	ShareWithDash        float64 // fraction of lines with at least one dash
	ShareEndingClosed    float64 // fraction ending with a question or a closer
	MeanJargonPerLine    float64
}

// Aggregate summarises a set of per-line Metrics (typically every
// spoken line in a session or a day).
func Aggregate(ms []Metrics) Summary {
	n := len(ms)
	if n == 0 {
		return Summary{}
	}
	words := make([]float64, n)
	var overThirty, withDash, endingClosed, jargonTotal int
	var wordSum float64
	for i, m := range ms {
		words[i] = float64(m.Words)
		wordSum += float64(m.Words)
		if m.Words > 30 {
			overThirty++
		}
		if m.Dashes > 0 {
			withDash++
		}
		if m.EndsWithQuestion || m.EndsWithCloser {
			endingClosed++
		}
		jargonTotal += len(m.Jargon)
	}
	return Summary{
		N:                    n,
		MeanWords:            wordSum / float64(n),
		MedianWords:          percentile(words, 50),
		P90Words:             percentile(words, 90),
		ShareOverThirtyWords: float64(overThirty) / float64(n),
		ShareWithDash:        float64(withDash) / float64(n),
		ShareEndingClosed:    float64(endingClosed) / float64(n),
		MeanJargonPerLine:    float64(jargonTotal) / float64(n),
	}
}

// percentile returns the p-th percentile (0-100) of values using the
// nearest-rank method: values need not be sorted on entry. A single value
// is returned for any p.
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
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

// normalize lowercases s, strips punctuation and collapses whitespace,
// for word-overlap comparisons (repeat-ask, duplicate speech in
// internal/scorecard).
func normalize(s string) string {
	s = strings.ToLower(s)
	s = reNonWord.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// WordSet returns the set of distinct normalised words in s.
func WordSet(s string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, w := range strings.Fields(normalize(s)) {
		set[w] = struct{}{}
	}
	return set
}

// WordOverlap returns the fraction of shared distinct words between a and
// b, relative to the smaller of the two word sets: |A∩B| / min(|A|,|B|).
// Two empty strings have zero overlap (nothing to share).
func WordOverlap(a, b string) float64 {
	sa, sb := WordSet(a), WordSet(b)
	if len(sa) == 0 || len(sb) == 0 {
		return 0
	}
	shared := 0
	small, big := sa, sb
	if len(sb) < len(sa) {
		small, big = sb, sa
	}
	for w := range small {
		if _, ok := big[w]; ok {
			shared++
		}
	}
	return float64(shared) / float64(len(small))
}
