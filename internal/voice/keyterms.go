package voice

import (
	"strconv"
	"strings"
	"unicode"
)

// Paraphrase-tolerant heard check (GPT-Live). GPT-Live voices an idle
// push from session.commentary.append in its own words, so PushCoverage's
// word-for-word token pairs would call almost every line unheard (why #89
// left GPT-Live unwatched). What must survive a paraphrase are the key
// terms the user acts on: the agents named, the numbers, and the
// option labels of a choice. KeyTermCoverage is the share of those terms
// the voice said; a line with none (plain news) is judged on its content
// words instead, leniently. It answers one question only — did the line
// reach the user at all — and HeardMinCoverage is still the bar.

// KeyTermCoverage is the share of push's key terms that said carries:
//   - every name of names (the agents' spoken names and aliases) push
//     mentions, the longest form only ("README Claude", not also
//     "Claude"), found in said case-insensitively;
//   - every number push writes in digits, found in said as digits, an
//     English number word or Chinese numerals ("3" / "three" / "三");
//   - the option labels of an "Options: a; b." tail, together one term:
//     covered when at least half of the labels are, a label when at
//     least half of its words (or CJK characters) are in said. One term,
//     not one each, so a voice that translates the worker's English
//     labels still counts as heard on the rest.
//
// With no key term it is twice the share of push's content words (Latin
// words of three letters or more outside a short stop list, CJK
// characters outside common particles) said contains, at most 1: a fair
// paraphrase keeps a quarter of them, a reply about something else does
// not.
func KeyTermCoverage(push string, names []string, said string) float64 {
	body, options := splitOptions(push)
	saidLower := strings.ToLower(said)
	saidSeq := coverageTokens(said)
	saidTokens := map[string]bool{}
	for _, t := range saidSeq {
		saidTokens[t] = true
	}
	saidNumbers := map[string]bool{}
	for _, n := range spokenNumbers(said) {
		saidNumbers[n] = true
	}

	total, covered := 0, 0
	for _, n := range pushNames(push, names) {
		total++
		if strings.Contains(saidLower, n) || containsTokenSeq(saidSeq, coverageTokens(n)) {
			covered++
		}
	}
	for _, n := range digitRuns(body) {
		total++
		if saidNumbers[n] {
			covered++
		}
	}
	labels, labelsHeard := 0, 0
	for _, o := range options {
		toks := coverageTokens(o)
		if len(toks) == 0 {
			continue
		}
		labels++
		have := 0
		for _, t := range toks {
			if saidTokens[t] || saidNumbers[t] {
				have++
			}
		}
		if 2*have >= len(toks) {
			labelsHeard++
		}
	}
	if labels > 0 {
		total++
		if 2*labelsHeard >= labels {
			covered++
		}
	}
	if total > 0 {
		return float64(covered) / float64(total)
	}

	words := contentTokens(body)
	if len(words) == 0 {
		return 1
	}
	have := 0
	for _, w := range words {
		if saidTokens[w] {
			have++
		}
	}
	cov := 2 * float64(have) / float64(len(words))
	if cov > 1 {
		cov = 1
	}
	return cov
}

// splitOptions cuts an "Options: a; b; c." tail off push.
func splitOptions(push string) (body string, options []string) {
	i := strings.LastIndex(push, "Options:")
	if i < 0 {
		return push, nil
	}
	for _, o := range strings.Split(push[i+len("Options:"):], ";") {
		if o = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(o), ".。")); o != "" {
			options = append(options, o)
		}
	}
	return push[:i], options
}

// pushNames are the names push mentions, lower case, distinct, a name
// inside a longer matched one left out.
func pushNames(push string, names []string) []string {
	lower := strings.ToLower(push)
	var found []string
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || !strings.Contains(lower, n) {
			continue
		}
		found = append(found, n)
	}
	var out []string
	for _, n := range found {
		inner := false
		for _, m := range found {
			if m != n && strings.Contains(m, n) {
				inner = true
				break
			}
		}
		dup := false
		for _, o := range out {
			if o == n {
				dup = true
			}
		}
		if !inner && !dup {
			out = append(out, n)
		}
	}
	return out
}

// digitRuns are the distinct runs of ASCII digits in text.
func digitRuns(text string) []string {
	var out []string
	seen := map[string]bool{}
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			n := strings.TrimLeft(b.String(), "0")
			if n == "" {
				n = "0"
			}
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
			b.Reset()
		}
	}
	for _, c := range text {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// chineseDigits are the Chinese numeral characters and their values;
// chineseUnits the multipliers.
var (
	chineseDigits = map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	chineseUnits  = map[rune]int{'十': 10, '百': 100, '千': 1000}
)

// spokenNumbers are the numbers text says, as digit strings: digit
// runs, English number words (including compounds and ordinals), and
// runs of Chinese numerals.
func spokenNumbers(text string) []string {
	out := digitRuns(text)
	seen := map[string]bool{}
	for _, n := range out {
		seen[n] = true
	}
	for _, t := range normTokens(text) {
		if isDigitsToken(t) && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// chineseNumber reads a run of Chinese numerals: 三 → 3, 十五 → 15,
// 二十五 → 25, 三百二十 → 320, and a run of bare digits read one by one
// (一二三 → 123).
func chineseNumber(run []rune) string {
	hasUnit := false
	for _, c := range run {
		if _, ok := chineseUnits[c]; ok {
			hasUnit = true
		}
	}
	if !hasUnit {
		var b strings.Builder
		for _, c := range run {
			b.WriteString(strconv.Itoa(chineseDigits[c]))
		}
		n := strings.TrimLeft(b.String(), "0")
		if n == "" {
			n = "0"
		}
		return n
	}
	total, cur := 0, 0
	for _, c := range run {
		if d, ok := chineseDigits[c]; ok {
			cur = d
			continue
		}
		u := chineseUnits[c]
		if cur == 0 {
			cur = 1 // 十五: the ten has no digit before it
		}
		total += cur * u
		cur = 0
	}
	return strconv.Itoa(total + cur)
}

// stopWords are the Latin words too common to show a paraphrase.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "was": true, "were": true, "has": true, "have": true,
	"had": true, "but": true, "not": true, "you": true, "your": true, "its": true, "it's": true, "this": true,
	"that": true, "with": true, "from": true, "now": true, "just": true, "all": true, "any": true, "can": true,
	"will": true, "would": true, "should": true, "about": true, "into": true, "than": true, "then": true,
	"there": true, "they": true, "them": true, "what": true, "when": true, "which": true, "who": true, "one": true,
}

// cjkParticles are the CJK characters too common to show a paraphrase.
var cjkParticles = map[string]bool{
	"的": true, "了": true, "是": true, "在": true, "我": true, "你": true, "他": true, "她": true, "它": true,
	"们": true, "这": true, "那": true, "和": true, "也": true, "就": true, "都": true, "要": true, "吗": true,
	"呢": true, "吧": true, "啊": true, "个": true, "有": true, "不": true, "还": true, "说": true,
}

// contentTokens are push's distinct content words.
func contentTokens(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range coverageTokens(text) {
		r := []rune(t)
		cjk := len(r) == 1 && unicode.Is(unicode.Han, r[0])
		switch {
		case cjk && cjkParticles[t]:
		case !cjk && !unicode.IsDigit(r[0]) && (len(r) < 3 || stopWords[t] || spokenJoiners[t]):
		case seen[t]:
		default:
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
