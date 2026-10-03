package bench

import (
	"strings"
	"unicode"
)

// ErrorRate is the word error rate of hyp against ref (character error
// rate when ref is Chinese): edit distance over the reference length,
// after normalising case, punctuation and fillers away. 0 is perfect; it
// can exceed 1 when hyp has many extra words.
func ErrorRate(ref, hyp string) float64 {
	r, h := Tokens(ref), Tokens(hyp)
	if len(r) == 0 {
		if len(h) == 0 {
			return 0
		}
		return 1
	}
	return float64(editDistance(r, h)) / float64(len(r))
}

// fillers are dropped before scoring: the bench adds them on purpose
// (speech.Disfluent) and a recogniser may or may not write them.
var fillers = map[string]bool{"um": true, "uh": true, "hmm": true, "er": true, "嗯": true, "呃": true, "啊": true}

// Tokens normalises s for scoring: Chinese characters one by one, other
// words lowercased without punctuation, fillers dropped.
func Tokens(s string) []string {
	var out []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			w := word.String()
			if !fillers[w] {
				out = append(out, w)
			}
			word.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			if !fillers[string(r)] {
				out = append(out, string(r))
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			word.WriteRune(r)
		case r == '\'' || r == '’':
			// "what's" stays one word
		default:
			flush()
		}
	}
	flush()
	return out
}

func editDistance(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
