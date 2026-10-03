package speech

import (
	"math/rand"
	"strings"
	"unicode"
)

// Disfluent rewrites text the way people talk when they are not reading
// a script: a filler up front ("um,", "嗯…"), a restart on a word ("the…
// the README one", "那个…那个测试"), with probability p for each kind,
// deterministic for rng. lang is en or zh. p <= 0 returns text as is.
func Disfluent(text, lang string, p float64, rng *rand.Rand) string {
	text = strings.TrimSpace(text)
	if p <= 0 || text == "" || rng == nil {
		return text
	}
	if lang == "zh" || hasHan(text) {
		return disfluentZH(text, p, rng)
	}
	return disfluentEN(text, p, rng)
}

var (
	fillersEN = []string{"Um,", "Uh,", "So,", "Hmm,", "Okay so,"}
	fillersZH = []string{"嗯…", "那个…", "就是…", "呃，", "嗯，那个，"}
)

func disfluentEN(text string, p float64, rng *rand.Rand) string {
	words := strings.Fields(text)
	if len(words) >= 3 && rng.Float64() < p {
		// Restart on a short word in the first half: "the… the".
		i := rng.Intn((len(words) + 1) / 2)
		w := strings.TrimRight(words[i], ",.?!")
		words = append(words[:i+1], words[i:]...)
		words[i] = w + "…"
	}
	out := strings.Join(words, " ")
	if rng.Float64() < p {
		f := fillersEN[rng.Intn(len(fillersEN))]
		out = f + " " + lowerFirst(out)
	}
	return out
}

func disfluentZH(text string, p float64, rng *rand.Rand) string {
	r := []rune(text)
	if len(r) >= 6 && rng.Float64() < p {
		// Restart on a two-character word near the start: "那个…那个".
		i := rng.Intn(len(r) / 2)
		if i+2 <= len(r) && isHan(r[i]) && isHan(r[i+1]) {
			rep := string(r[i : i+2])
			r = []rune(string(r[:i]) + rep + "…" + string(r[i:]))
		}
	}
	out := string(r)
	if rng.Float64() < p {
		out = fillersZH[rng.Intn(len(fillersZH))] + out
	}
	return out
}

func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) > 1 && unicode.IsUpper(r[0]) && !unicode.IsUpper(r[1]) && string(r[0:1]) != "I" {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

func isHan(r rune) bool { return unicode.Is(unicode.Han, r) }

func hasHan(s string) bool {
	for _, r := range s {
		if isHan(r) {
			return true
		}
	}
	return false
}
