package prose

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The ear cap (docs/bench-2026-10-03.md #13): the harness does not judge
// how much a spoken line should say, the brain's prompt does (short by
// default, longer when the shepherd asks for detail). The code only stops
// a runaway: one high cap for every spoken line, about 45 seconds of
// speech, measured by estimated speaking time so Chinese and English
// (and mixed text) are treated alike.
const (
	// WordsPerSecond and HanziPerSecond are the speaking rates the
	// estimate uses.
	WordsPerSecond = 2.5
	HanziPerSecond = 4.5
	// EarSeconds is the runaway cap: ~110 English words or ~200 CJK
	// characters.
	EarSeconds = 44.0
	// EarWords is the cap in English words, for callers and tests that
	// count words.
	EarWords = 110
)

// EarCaps is a spoken headline's cap: at most Seconds of estimated speech.
type EarCaps struct{ Seconds float64 }

// EarRunaway is the one cap on every spoken line.
var EarRunaway = EarCaps{Seconds: EarSeconds}

// earMinCut is the least speech (about six words) a headline cut at a
// comma keeps: a shorter first clause says nothing on its own, so the cut
// falls at the cap instead.
const earMinCut = 6 / WordsPerSecond

var (
	// reClause matches a dash or semicolon that joins two clauses: an
	// em dash (spaced or not), a spaced en dash, a spaced "-" or "--",
	// and "; ". An unspaced en dash ("3–5") or hyphen ("dead-code",
	// "--force") is part of a word and is kept.
	reClause = regexp.MustCompile(`\s*—\s*|\s*；\s*|\s+(?:–|--?)\s+|\s*;(?:\s+|$)`)
)

// Ear cuts a spoken line for the ear. It scrubs s like Spoken (D9: no
// URLs, paths, hashes, pane ids, PR or issue numbers), turns every
// dash- or semicolon-joined clause into its own sentence, then keeps
// whole sentences up to the runaway cap (EarSeconds). When the first
// sentence alone is over the cap it is cut at its last comma within the
// cap (or at the cap itself), never mid-word. Every sentence ends with
// terminal punctuation.
// overflow is everything cut, scrubbed, so the caller can keep it (the
// reply's detail): nothing the model wrote is lost, it is just not read
// aloud.
func Ear(s string) (spoken, overflow string) {
	return earCut(earSentences(s), EarRunaway, false)
}

// EarLine is the line the voice reads for a reply: the headline cut by
// Ear, then ask — the one question — as its own last sentence, so the
// question is always heard last and always survives the cut. With an
// ask, any question in the headline moves to overflow (one question per
// reply). The ask is scrubbed, its dashes become commas, and only its
// first question is kept (the rest joins overflow); it always ends with
// "?". An empty headline with an ask is just the ask.
func EarLine(headline, ask string) (spoken, overflow string) {
	return EarLineWith(EarRunaway, headline, ask)
}

// EarLineWith is EarLine with an explicit cap (every cap is EarRunaway).
func EarLineWith(caps EarCaps, headline, ask string) (spoken, overflow string) {
	q, qOver := earAsk(ask)
	h, hOver := earCut(earSentences(headline), caps, q != "")
	spoken = joinSentences(terminate(h), q)
	return spoken, joinSentences(hOver, qOver)
}

// earSentences scrubs s and splits it into sentences, a dash- or
// semicolon-joined clause counting as one. Sentences end at . ! ? and at
// the full-width 。！？ .
func earSentences(s string) []string {
	s = scrub(s)
	if s == "" {
		return nil
	}
	var out []string
	for i, clause := range reClause.Split(s, -1) {
		clause = strings.TrimSpace(clause)
		if strings.Trim(clause, ".,:!? 。，：！？、") == "" {
			continue
		}
		if i > 0 {
			// It followed a dash: it starts a sentence now.
			clause = capitalise(clause)
		}
		for _, sen := range splitSentences(clause) {
			// A clause cut at a dash (or a path scrubbed with its
			// full stop) ends without punctuation: close it.
			out = append(out, terminate(sen))
		}
	}
	return out
}

// earCut keeps the first sentences within caps. dropQuestions moves
// every question to the overflow first (the reply carries its own ask).
func earCut(sens []string, caps EarCaps, dropQuestions bool) (kept, overflow string) {
	var keep, over []string
	if dropQuestions {
		var rest []string
		for _, sen := range sens {
			if isQuestion(sen) {
				over = append(over, sen)
			} else {
				rest = append(rest, sen)
			}
		}
		sens = rest
	}
	secs := 0.0
	for i, sen := range sens {
		n := SpeechSeconds(sen)
		if secs+n <= caps.Seconds {
			keep = append(keep, sen)
			secs += n
			continue
		}
		if len(keep) == 0 {
			head, tail := cutSpeech(sen, caps.Seconds)
			keep = append(keep, head)
			if tail != "" {
				over = append(over, tail)
			}
			i++
		}
		over = append(over, sens[i:]...)
		break
	}
	return strings.Join(keep, " "), joinSentences(over...)
}

// earAsk makes ask one question: scrubbed, dashes as commas, the first
// sentence that is a question (or the first sentence, made one).
func earAsk(ask string) (q, overflow string) {
	s := scrub(ask)
	s = reClause.ReplaceAllString(s, ", ")
	s = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), ","))
	if s == "" {
		return "", ""
	}
	sens := splitSentences(s)
	pick := 0
	for i, sen := range sens {
		if isQuestion(sen) {
			pick = i
			break
		}
	}
	q = capitalise(strings.TrimRight(sens[pick], ".!,:; 。！，：；、"))
	if !isQuestion(q) {
		if endsCJK(q) {
			q += "？"
		} else {
			q += "?"
		}
	}
	rest := append(append([]string{}, sens[:pick]...), sens[pick+1:]...)
	return q, strings.Join(rest, " ")
}

// speechToken is a unit of speech: one CJK character or one word, with
// the punctuation and space that follow it.
type speechToken struct {
	text string
	secs float64
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// endsCJK reports whether s ends in a CJK character or full-width
// punctuation, so a mark added to it should be the full-width one.
func endsCJK(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return isCJK(r) || (r >= 0xFF00 && r <= 0xFFEF) || (r >= 0x3000 && r <= 0x303F) || r == '…'
}

// isQuestion reports whether s ends with a question mark, either width.
func isQuestion(s string) bool {
	return strings.HasSuffix(s, "?") || strings.HasSuffix(s, "？")
}

func speechTokens(s string) []speechToken {
	var out []speechToken
	rs := []rune(s)
	for i := 0; i < len(rs); {
		j := i
		var secs float64
		if isCJK(rs[i]) {
			j = i + 1
			secs = 1 / HanziPerSecond
		} else {
			letters := false
			for j < len(rs) && !isCJK(rs[j]) && !unicode.IsSpace(rs[j]) {
				if unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) {
					letters = true
				}
				j++
			}
			if letters {
				secs = 1 / WordsPerSecond
			}
		}
		// Space after it belongs to it.
		for j < len(rs) && unicode.IsSpace(rs[j]) {
			j++
		}
		out = append(out, speechToken{string(rs[i:j]), secs})
		i = j
	}
	return out
}

// SpeechSeconds estimates how long s takes to say: ~2.5 English words
// per second, ~4.5 CJK characters per second, punctuation free.
func SpeechSeconds(s string) float64 {
	var t float64
	for _, tok := range speechTokens(s) {
		t += tok.secs
	}
	return t
}

// cutSpeech cuts sen to at most maxSec of speech: at the last comma
// within it when that keeps at least earMinCut seconds, else at the cap,
// never inside a word. The head ends with terminal punctuation; tail is
// the rest, capitalised.
func cutSpeech(sen string, maxSec float64) (head, tail string) {
	toks := speechTokens(sen)
	at, comma, secs := len(toks), -1, 0.0
	for i, tok := range toks {
		if secs+tok.secs > maxSec {
			at = i
			break
		}
		secs += tok.secs
		if strings.HasSuffix(strings.TrimSpace(tok.text), ",") || strings.HasSuffix(strings.TrimSpace(tok.text), "，") {
			if secs >= earMinCut {
				comma = i + 1
			}
		}
	}
	if at == len(toks) {
		return sen, ""
	}
	if comma > 0 {
		at = comma
	}
	// Punctuation that follows the last kept word stays with it.
	for at < len(toks) && toks[at].secs == 0 && comma <= 0 {
		at++
	}
	var h, t strings.Builder
	for i, tok := range toks {
		if i < at {
			h.WriteString(tok.text)
		} else {
			t.WriteString(tok.text)
		}
	}
	head = strings.TrimRight(strings.TrimSpace(h.String()), ",:;，、：；")
	return terminate(head), capitalise(strings.TrimSpace(t.String()))
}

// splitSentences splits s after each run of terminal punctuation; a
// trailing fragment without one is the last sentence.
func splitSentences(s string) []string {
	var out []string
	start := 0
	for _, loc := range reSentence.FindAllStringIndex(s, -1) {
		if sen := strings.TrimSpace(s[start:loc[1]]); sen != "" {
			out = append(out, sen)
		}
		start = loc[1]
	}
	if sen := strings.TrimSpace(s[start:]); sen != "" {
		out = append(out, sen)
	}
	return out
}

// terminate makes sure s ends with terminal punctuation.
func terminate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.TrimRight(s, ",:;，、：；")
	if s == "" {
		return ""
	}
	if last, _ := utf8.DecodeLastRuneInString(s); strings.ContainsRune(".!?\"')]。！？…」』）”’", last) {
		return s
	}
	if endsCJK(s) {
		return s + "。"
	}
	return s + "."
}

// capitalise upper-cases the first letter of s.
func capitalise(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 || !unicode.IsLower(r) {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// joinSentences joins the non-empty parts with a space.
func joinSentences(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}
