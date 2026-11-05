package voice

import (
	"strconv"
	"strings"
	"unicode"
)

// Text normalisation shared by HeardTracker (PushCoverage, KeyTermCoverage)
// and EchoGate (EchoText): numbers in digits, English words (0..99, 100,
// ordinals) and Chinese numerals map to the same canonical digit tokens,
// and code identifiers split on joiners (_, -, ., /, ') and camelCase
// boundaries while keeping CJK per-character segmentation intact.

var (
	englishExactNumbers = map[string]int{
		"zero": 0, "zeroth": 0,
		"one": 1, "first": 1,
		"two": 2, "second": 2,
		"three": 3, "third": 3,
		"four": 4, "fourth": 4,
		"five": 5, "fifth": 5,
		"six": 6, "sixth": 6,
		"seven": 7, "seventh": 7,
		"eight": 8, "eighth": 8,
		"nine": 9, "ninth": 9,
		"ten": 10, "tenth": 10,
		"eleven": 11, "eleventh": 11,
		"twelve": 12, "twelfth": 12,
		"thirteen": 13, "thirteenth": 13,
		"fourteen": 14, "fourteenth": 14,
		"fifteen": 15, "fifteenth": 15,
		"sixteen": 16, "sixteenth": 16,
		"seventeen": 17, "seventeenth": 17,
		"eighteen": 18, "eighteenth": 18,
		"nineteen": 19, "nineteenth": 19,
		"twenty": 20, "twentieth": 20,
		"thirty": 30, "thirtieth": 30,
		"forty": 40, "fortieth": 40,
		"fifty": 50, "fiftieth": 50,
		"sixty": 60, "sixtieth": 60,
		"seventy": 70, "seventieth": 70,
		"eighty": 80, "eightieth": 80,
		"ninety": 90, "ninetieth": 90,
		"hundred": 100, "hundredth": 100,
	}

	englishTensWords = map[string]int{
		"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50,
		"sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
	}

	englishOnesWords = map[string]int{
		"one": 1, "first": 1,
		"two": 2, "second": 2,
		"three": 3, "third": 3,
		"four": 4, "fourth": 4,
		"five": 5, "fifth": 5,
		"six": 6, "sixth": 6,
		"seven": 7, "seventh": 7,
		"eight": 8, "eighth": 8,
		"nine": 9, "ninth": 9,
	}

	englishCardinalOnes = map[string]int{
		"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
		"six": 6, "seven": 7, "eight": 8, "nine": 9,
	}

	// oneDeterminers mark "one" as a pronoun ("the one", "this one") rather
	// than the number 1.
	oneDeterminers = map[string]bool{
		"the": true, "this": true, "that": true, "which": true, "another": true,
		"each": true, "every": true, "some": true, "any": true, "no": true,
	}

	// spokenJoiners are words a voice or transcriber says for code joiner
	// punctuation (_, -, ., /).
	spokenJoiners = map[string]bool{
		"dot": true, "underscore": true, "dash": true, "hyphen": true, "slash": true,
	}
)

type rawToken struct {
	text string
	// joinable: only spaces or hyphens separated this token from the
	// previous one, so "twenty three" and "twenty-three" form 23 while
	// "twenty, three" stays two numbers.
	joinable bool
}

func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

func isChineseNumeralRune(r rune) bool {
	_, d := chineseDigits[r]
	_, u := chineseUnits[r]
	return d || u
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

// trimDigitZeros canonicalises an ASCII digit string ("007" -> "7", "00" -> "0").
func trimDigitZeros(s string) string {
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0"
	}
	return s
}

// ordinalDigits matches <digits>(st|nd|rd|th) case-insensitively ("1st", "21st").
func ordinalDigits(run []rune) (string, bool) {
	if len(run) < 3 {
		return "", false
	}
	suf := strings.ToLower(string(run[len(run)-2:]))
	if suf != "st" && suf != "nd" && suf != "rd" && suf != "th" {
		return "", false
	}
	for _, r := range run[:len(run)-2] {
		if !isASCIIDigit(r) {
			return "", false
		}
	}
	return trimDigitZeros(string(run[:len(run)-2])), true
}

// splitIdent splits a Latin/digit identifier run on camelCase, acronym
// ("HTTPResponse" -> "http", "response") and letter-digit boundaries.
func splitIdent(run []rune) []string {
	if d, ok := ordinalDigits(run); ok {
		return []string{d}
	}
	var out []string
	start := 0
	emit := func(end int) {
		if end <= start {
			return
		}
		tok := strings.ToLower(string(run[start:end]))
		if isASCIIDigit(run[start]) {
			tok = trimDigitZeros(tok)
		}
		out = append(out, tok)
		start = end
	}
	for i := 1; i < len(run); i++ {
		prev, cur := run[i-1], run[i]
		switch {
		case isASCIIDigit(prev) != isASCIIDigit(cur):
			emit(i)
		case unicode.IsLower(prev) && unicode.IsUpper(cur):
			emit(i)
		case unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(run) && unicode.IsLower(run[i+1]):
			emit(i)
		}
	}
	emit(len(run))
	return out
}

// normTokens tokenises text for heard coverage and echo detection: CJK
// characters stay one per token (except Chinese numeral runs, which map to
// canonical digits), identifiers split on joiners and camelCase, and
// English number words (0..99, 100, ordinals) map to canonical digits.
func normTokens(text string) []string {
	var raw []rawToken
	var ident []rune
	var zhNum []rune
	sepJoinable := false
	hasTokens := false

	appendTok := func(t string, join bool) {
		raw = append(raw, rawToken{text: t, joinable: hasTokens && join})
		hasTokens = true
	}
	flushIdent := func() {
		if len(ident) == 0 {
			return
		}
		parts := splitIdent(ident)
		ident = ident[:0]
		for i, p := range parts {
			join := sepJoinable
			if i > 0 {
				join = false
			}
			appendTok(p, join)
		}
		sepJoinable = false
	}
	flushZhNum := func() {
		if len(zhNum) == 0 {
			return
		}
		// A lone 百 or 千 ("百度", "千万") without a digit is a word character,
		// whereas 十 ("十", "十五") is 10 / 15.
		if len(zhNum) == 1 && (zhNum[0] == '百' || zhNum[0] == '千') {
			appendTok(string(zhNum[0]), false)
		} else {
			appendTok(chineseNumber(zhNum), false)
		}
		zhNum = zhNum[:0]
		sepJoinable = false
	}

	inSep := false
	sepClean := true
	markSep := func(r rune) {
		if !inSep {
			inSep = true
			sepClean = true
		}
		if !unicode.IsSpace(r) && r != '-' {
			sepClean = false
		}
	}

	for _, r := range text {
		switch {
		case isChineseNumeralRune(r):
			flushIdent()
			inSep = false
			zhNum = append(zhNum, r)
		case isCJKRune(r):
			flushIdent()
			flushZhNum()
			inSep = false
			appendTok(string(r), false)
		case unicode.IsLetter(r) || isASCIIDigit(r):
			flushZhNum()
			if inSep {
				sepJoinable = sepClean
				inSep = false
			}
			ident = append(ident, r)
		default:
			flushIdent()
			flushZhNum()
			markSep(r)
		}
	}
	flushIdent()
	flushZhNum()

	out := make([]string, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		w := raw[i].text
		if i+1 < len(raw) && raw[i+1].joinable {
			next := raw[i+1].text
			if tens, ok := englishTensWords[w]; ok {
				if ones, ok := englishOnesWords[next]; ok {
					out = append(out, strconv.Itoa(tens+ones))
					i++
					continue
				}
			}
			if mult, ok := englishCardinalOnes[w]; ok && (next == "hundred" || next == "hundredth") {
				out = append(out, strconv.Itoa(mult*100))
				i++
				continue
			}
		}
		if w == "one" && len(out) > 0 && oneDeterminers[out[len(out)-1]] {
			out = append(out, w)
			continue
		}
		if n, ok := englishExactNumbers[w]; ok {
			out = append(out, strconv.Itoa(n))
			continue
		}
		out = append(out, w)
	}
	return out
}

func isDigitsToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isASCIIDigit(r) {
			return false
		}
	}
	return true
}

// tokenBigrams builds the set of adjacent token pairs in toks. When
// bridgeJoiners is true, a spoken punctuation word ("dot", "underscore",
// "dash", "hyphen", "slash") between two tokens also records the pair
// across it so "ws underscore proto dot go" covers "ws_proto.go".
func tokenBigrams(toks []string, bridgeJoiners bool) map[[2]string]bool {
	out := make(map[[2]string]bool, len(toks))
	for i := 1; i < len(toks); i++ {
		out[[2]string{toks[i-1], toks[i]}] = true
	}
	if bridgeJoiners {
		for i := 1; i+1 < len(toks); i++ {
			if spokenJoiners[toks[i]] {
				out[[2]string{toks[i-1], toks[i+1]}] = true
			}
		}
	}
	return out
}

// containsTokenSeq reports whether sub appears contiguously in toks.
func containsTokenSeq(toks, sub []string) bool {
	if len(sub) == 0 || len(sub) > len(toks) {
		return false
	}
	for i := 0; i+len(sub) <= len(toks); i++ {
		match := true
		for j := range sub {
			if toks[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
