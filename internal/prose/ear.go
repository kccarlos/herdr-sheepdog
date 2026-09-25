package prose

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The ear caps (docs/roles.md "The brain — do / don't", the answer
// contract): a spoken headline is at most EarSentences sentences and
// EarWords words, one idea per sentence, no dashes or semicolons. The
// shepherd hears it once and cannot scroll back.
const (
	EarSentences = 2
	EarWords     = 30
)

// The caps on a reply to the shepherd's own words (docs/dive-deep.md
// WP8b): about twenty seconds of speech, so a detail they asked for
// ("what exactly did it say?") can be heard. The prompt keeps a reply
// brief by default; this cap is the safety net against a runaway reply,
// not the style rule. Announce, finish and delivery lines keep EarBrief.
const (
	ReplySentences = 4
	ReplyWords     = 60
)

// EarCaps are a spoken headline's caps: at most Sentences sentences and
// Words words.
type EarCaps struct{ Sentences, Words int }

var (
	// EarBrief is the cap on every line the shepherd did not ask for:
	// announcements, finishes, deliveries (EarLine).
	EarBrief = EarCaps{Sentences: EarSentences, Words: EarWords}
	// EarReply is the cap on a reply to an utterance (EarLineWith).
	EarReply = EarCaps{Sentences: ReplySentences, Words: ReplyWords}
)

// earMinCut is the fewest words a headline cut at a comma keeps: a
// shorter first clause ("Both landed,") says nothing on its own, so the
// cut falls at the word cap instead.
const earMinCut = 6

var (
	// reClause matches a dash or semicolon that joins two clauses: an
	// em dash (spaced or not), a spaced en dash, a spaced "-" or "--",
	// and "; ". An unspaced en dash ("3–5") or hyphen ("dead-code",
	// "--force") is part of a word and is kept.
	reClause = regexp.MustCompile(`\s*—\s*|\s+(?:–|--?)\s+|\s*;(?:\s+|$)`)
)

// Ear cuts a spoken line for the ear. It scrubs s like Spoken (D9: no
// URLs, paths, hashes, pane ids, PR or issue numbers), turns every
// dash- or semicolon-joined clause into its own sentence, then keeps
// the first EarSentences sentences within EarWords words. When the first
// sentence alone is over the cap it is cut at its last comma within the
// cap (or at the cap itself), never mid-word. Every sentence ends with
// terminal punctuation.
// overflow is everything cut, scrubbed, so the caller can keep it (the
// reply's detail): nothing the model wrote is lost, it is just not read
// aloud.
func Ear(s string) (spoken, overflow string) {
	return earCut(earSentences(s), EarBrief, false)
}

// EarLine is the line the voice reads for a reply: the headline cut by
// Ear, then ask — the one question — as its own last sentence, so the
// question is always heard last and always survives the cut. With an
// ask, any question in the headline moves to overflow (one question per
// reply). The ask is scrubbed, its dashes become commas, and only its
// first question is kept (the rest joins overflow); it always ends with
// "?". An empty headline with an ask is just the ask.
func EarLine(headline, ask string) (spoken, overflow string) {
	return EarLineWith(EarBrief, headline, ask)
}

// EarLineWith is EarLine with the headline cut to caps instead of
// EarBrief: EarReply for a reply to the shepherd's own words.
func EarLineWith(caps EarCaps, headline, ask string) (spoken, overflow string) {
	q, qOver := earAsk(ask)
	h, hOver := earCut(earSentences(headline), caps, q != "")
	spoken = joinSentences(terminate(h), q)
	return spoken, joinSentences(hOver, qOver)
}

// earSentences scrubs s and splits it into sentences, a dash- or
// semicolon-joined clause counting as one.
func earSentences(s string) []string {
	s = scrub(s)
	if s == "" {
		return nil
	}
	var out []string
	for i, clause := range reClause.Split(s, -1) {
		clause = strings.TrimSpace(clause)
		if strings.Trim(clause, ".,:!? ") == "" {
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
			if strings.HasSuffix(sen, "?") {
				over = append(over, sen)
			} else {
				rest = append(rest, sen)
			}
		}
		sens = rest
	}
	words := 0
	for i, sen := range sens {
		n := len(strings.Fields(sen))
		if len(keep) < caps.Sentences && words+n <= caps.Words {
			keep = append(keep, sen)
			words += n
			continue
		}
		if len(keep) == 0 {
			head, tail := cutWords(sen, caps.Words)
			keep = append(keep, head)
			over = append(over, tail)
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
		if strings.HasSuffix(sen, "?") {
			pick = i
			break
		}
	}
	q = capitalise(strings.TrimRight(sens[pick], ".!,:; "))
	if !strings.HasSuffix(q, "?") {
		q += "?"
	}
	rest := append(append([]string{}, sens[:pick]...), sens[pick+1:]...)
	return q, strings.Join(rest, " ")
}

// cutWords cuts sen to at most n words: at the last comma within them
// when that keeps at least earMinCut words, else at the n-th word. The
// head ends with a full stop; tail is the rest, capitalised.
func cutWords(sen string, n int) (head, tail string) {
	fields := strings.Fields(sen)
	if len(fields) <= n {
		return sen, ""
	}
	at := n
	for i := n; i >= earMinCut; i-- {
		if strings.HasSuffix(fields[i-1], ",") {
			at = i
			break
		}
	}
	head = strings.Join(fields[:at], " ")
	head = strings.TrimRight(head, ",:;")
	return terminate(head), capitalise(strings.Join(fields[at:], " "))
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
	s = strings.TrimRight(s, ",:;")
	if last, _ := utf8.DecodeLastRuneInString(s); strings.ContainsRune(".!?\"')]", last) {
		return s
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
