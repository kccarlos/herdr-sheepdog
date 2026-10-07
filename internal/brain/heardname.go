package brain

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// A name the user said that matches no agent (docs/bench/tier2-2026-10-03.md
// #6). "Let me know when bench-claude is done." reached the orchestrator as
// "Oh, I know when Bench Squad is done." (also "Bench Quad", "Bench
// Clad", "Bench Clear", "Bench Claw"); with no agent named, the request
// carried no record, and the orchestrator invented a rename ("Bench Squad is
// the agent you just renamed") instead of registering the promise or
// asking. The daemon only states the fact — no agent has that name, and
// which names are closest — and the orchestrator decides whether to ask or to
// take the closest.

// maxClosestNames bounds the names an unmatched-name line lists.
const maxClosestNames = 3

// unmatchedNameLine states that the words carry a name no agent has: a
// run of two words that begins with the first word of an agent's name
// ("Bench" of bench-claude or "Bench Claude") and whose second word is
// capitalised the way speech recognition writes a name, while no agent's
// alias, spoken name, earlier spoken name or title is named at all. ""
// otherwise.
func unmatchedNameLine(agents []AgentInfo, words string) string {
	if len(agents) == 0 {
		return ""
	}
	firsts := map[string]bool{}
	for _, a := range agents {
		for _, form := range nameForms(a) {
			if namesAgent(words, form) {
				return ""
			}
			if f := strings.Fields(form); len(f) > 1 && len([]rune(f[0])) >= 3 {
				firsts[f[0]] = true
			}
		}
	}
	tokens := strings.FieldsFunc(words, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var heard []string
	for i := 0; i+1 < len(tokens); i++ {
		next := []rune(tokens[i+1])
		if !firsts[strings.ToLower(tokens[i])] || len(next) < 2 || !unicode.IsUpper(next[0]) {
			continue
		}
		h := tokens[i] + " " + tokens[i+1]
		if !containsFold(heard, h) {
			heard = append(heard, h)
		}
	}
	if len(heard) == 0 {
		return ""
	}
	parts := make([]string, len(heard))
	for i, h := range heard {
		parts[i] = fmt.Sprintf("%q", h)
	}
	return fmt.Sprintf("No agent is called %s, and none was renamed to that; the closest names: %s. Speech recognition can mishear a name.",
		strings.Join(parts, " or "), closestNames(agents, heard[0]))
}

// nameForms are the lower-case names an agent answers to, dashes heard
// as spaces: alias, spoken name, the spoken name before a recent rename,
// session title.
func nameForms(a AgentInfo) []string {
	var out []string
	for _, n := range []string{a.Alias, a.Spoken, a.SpokenWas, a.Title} {
		n = strings.ToLower(strings.TrimSpace(strings.NewReplacer("-", " ", "_", " ").Replace(n)))
		if n != "" && !contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// closestNames lists the agents whose names are nearest to heard, nearest
// first, at most maxClosestNames: the spoken name with the alias the
// tools take. Nearest is the longest run of letters in common, in order
// (a misheard name keeps most of the sounds: "Bench Clad", "Bench
// Squad" for bench-claude), then the fewest edits.
func closestNames(agents []AgentInfo, heard string) string {
	type cand struct {
		label  string
		common int
		dist   int
	}
	h := []rune(strings.ToLower(heard))
	var cands []cand
	for _, a := range agents {
		common, best := 0, -1
		for _, form := range nameForms(a) {
			f := []rune(form)
			c, d := commonRunes(h, f), runeDistance(h, f)
			if c > common || (c == common && (best < 0 || d < best)) {
				common, best = c, d
			}
		}
		label := fmt.Sprintf("%q", aliasOr(a.Alias))
		if a.Spoken != "" && a.Spoken != a.Alias {
			label = fmt.Sprintf("%q (%s)", a.Spoken, aliasOr(a.Alias))
		}
		cands = append(cands, cand{label, common, best})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].common != cands[j].common {
			return cands[i].common > cands[j].common
		}
		return cands[i].dist < cands[j].dist
	})
	if len(cands) > maxClosestNames {
		cands = cands[:maxClosestNames]
	}
	labels := make([]string, len(cands))
	for i, c := range cands {
		labels[i] = c.label
	}
	return strings.Join(labels, ", ")
}

// commonRunes is the length of the longest common subsequence of a and b.
func commonRunes(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else {
				cur[j] = max(prev[j], cur[j-1])
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// runeDistance is the Levenshtein distance of a and b.
func runeDistance(a, b []rune) int {
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

// containsFold reports whether list holds s, ignoring case.
func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
