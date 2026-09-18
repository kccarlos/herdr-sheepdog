package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// DefaultScreenBytes is the input truncation limit for screen tails.
const DefaultScreenBytes = 8 << 10

// ErrNoJSON is returned when no JSON object can be found in a reply.
var ErrNoJSON = errors.New("llm: no JSON object in reply")

// ExtractJSON returns the first JSON object in text. It tolerates a bare
// object, an object inside ``` fences (with or without a "json" tag), and
// prose before or after the object. The result is verified to be valid
// JSON; when it is not, the search continues from the next "{".
func ExtractJSON(text string) ([]byte, error) {
	s := strings.TrimSpace(text)
	s = stripFences(s)
	start := 0
	for {
		i := strings.IndexByte(s[start:], '{')
		if i < 0 {
			return nil, ErrNoJSON
		}
		i += start
		if end := matchBrace(s, i); end > i {
			candidate := s[i : end+1]
			if json.Valid([]byte(candidate)) {
				return []byte(candidate), nil
			}
		}
		start = i + 1
	}
}

// stripFences removes a leading ```lang and trailing ``` when present.
func stripFences(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// matchBrace returns the index of the brace closing the one at start,
// honouring JSON strings and escapes; -1 when unbalanced.
func matchBrace(s string, start int) int {
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// TailBytes keeps the last whole lines of text that fit in max bytes
// (newest lines first, i.e. the end of the screen is always kept). A
// single line longer than max is cut from the front.
func TailBytes(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	cut := text[len(text)-max:]
	if nl := strings.IndexByte(cut, '\n'); nl >= 0 && nl+1 < len(cut) {
		cut = cut[nl+1:]
	}
	return cut
}

var (
	reURL  = regexp.MustCompile(`(?i)\b(?:https?|ftp|ws|wss)://\S+`)
	reWWW  = regexp.MustCompile(`(?i)\bwww\.\S+`)
	reHash = regexp.MustCompile(`\b[0-9a-f]{7,64}\b`)
	// rePath matches file paths for scrubbing: an absolute/home/dotted
	// path ("/Users/kc/x.go", "~/repo/x.go", "./a/b.go") or a bare
	// relative path with a leading word ("internal/x/calc_test.go"). It
	// requires at least two "/segment" groups so a lone identifier like
	// "TestTotal" or a bare filename like "calc_test.go" is never
	// matched (D9 keeps test/function names, only paths are scrubbed).
	rePath     = regexp.MustCompile(`(?:(?:~|\.{1,2})?(?:/[\w.@+\-]+){2,}/?|\b[\w.@+\-]+(?:/[\w.@+\-]+){2,})`)
	reWinPath  = regexp.MustCompile(`\b[A-Za-z]:\\[^\s"']+`)
	rePaneID   = regexp.MustCompile(`\bw\d+:[pt]\d+\b`)
	reSpaces   = regexp.MustCompile(`[ \t]+`)
	reOrphan   = regexp.MustCompile(`\s+([,.;:!?])`)
	reSentence = regexp.MustCompile(`[.!?]+(?:["')\]]*)(?:\s+|$)`)
)

// Spoken enforces decision D9 on model output: at most maxSentences
// sentences, with URLs, hashes, file paths and pane ids removed and
// whitespace normalised. It never returns an empty string for non-empty
// meaningful input; a result that would be empty comes back as "".
func Spoken(s string, maxSentences int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = reURL.ReplaceAllString(s, "a link")
	s = reWWW.ReplaceAllString(s, "a link")
	s = reWinPath.ReplaceAllString(s, "a file")
	s = rePath.ReplaceAllString(s, "a file")
	s = reHash.ReplaceAllStringFunc(s, func(m string) string {
		if strings.ContainsAny(m, "0123456789") && strings.ContainsAny(m, "abcdef") {
			return ""
		}
		return m
	})
	s = rePaneID.ReplaceAllString(s, "")
	s = reSpaces.ReplaceAllString(s, " ")
	s = reOrphan.ReplaceAllString(s, "$1")
	s = strings.TrimSpace(s)
	if maxSentences > 0 {
		s = firstSentences(s, maxSentences)
	}
	return strings.TrimSpace(s)
}

// firstSentences keeps the first n sentences of s.
func firstSentences(s string, n int) string {
	ends := reSentence.FindAllStringIndex(s, -1)
	if len(ends) <= n {
		return s
	}
	return strings.TrimSpace(s[:ends[n-1][1]])
}

// endSentence makes sure s ends with terminal punctuation.
func endSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	last := rune(s[len(s)-1])
	if unicode.IsPunct(last) && strings.ContainsRune(".!?", last) {
		return s
	}
	if strings.ContainsRune(`"')]`, last) {
		return s
	}
	return s + "."
}

// decode unmarshals the first JSON object in text into v.
func decode(text string, v any) error {
	raw, err := ExtractJSON(text)
	if err != nil {
		return fmt.Errorf("%w: %q", err, truncateRunes(text, 120))
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("llm: decode reply: %w", err)
	}
	return nil
}
