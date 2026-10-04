package prose

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// DefaultResultBytes is the truncation limit for a screen tail put in a
// model's window: what the agent did or said may sit above a status bar
// or banner, so a generous ~200 lines of the pane are kept.
const DefaultResultBytes = 16 << 10

// ErrNoJSON is returned when no JSON object can be found in a reply.
var ErrNoJSON = errors.New("prose: no JSON object in reply")

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
	rePath    = regexp.MustCompile(`(?:(?:~|\.{1,2})?(?:/[\w.@+\-]+){2,}/?|\b[\w.@+\-]+(?:/[\w.@+\-]+){2,})`)
	reWinPath = regexp.MustCompile(`\b[A-Za-z]:\\[^\s"']+`)
	rePaneID  = regexp.MustCompile(`\bw\d+:[pt]\d+\b`)
	// reIssue matches a PR or issue number ("#49", "PR #49", "PR 49"):
	// the shepherd hears it and cannot use it.
	reIssue  = regexp.MustCompile(`(?i)(?:\b(?:PR|pull request|issue)\s*#?\s*\d+\b|#\d+\b)`)
	reSpaces = regexp.MustCompile(`[ \t]+`)
	// reOrphan is punctuation left standing alone after a scrub ("the
	// file ." once a path is gone): it is joined back to the word before
	// only when it ends a token, so the space before a "." that opens a
	// path or command token stays ("go test -race ./..." was spoken as
	// "-race./....", docs/eval/eval-campaign-2026-10-03.md H3).
	reOrphan   = regexp.MustCompile(`\s+([,.;:!?]+)(\s|$)`)
	reSentence = regexp.MustCompile(`[.!?]+["')\]]*(?:\s+|$)|[。！？]+[”’」』）"')\]]*\s*`)
)

// Spoken enforces decision D9 on model output: at most maxSentences
// sentences, with URLs, hashes, file paths, pane ids and PR or issue
// numbers removed and
// whitespace normalised. It never returns an empty string for non-empty
// meaningful input; a result that would be empty comes back as "".
func Spoken(s string, maxSentences int) string {
	s = scrub(s)
	if maxSentences > 0 {
		s = firstSentences(s, maxSentences)
	}
	return strings.TrimSpace(s)
}

// scrub is D9 without the sentence cap: URLs, paths, hashes, pane ids
// and PR or issue numbers removed, whitespace normalised.
func scrub(s string) string {
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
	s = reIssue.ReplaceAllString(s, "")
	s = reSpaces.ReplaceAllString(s, " ")
	s = reOrphan.ReplaceAllString(s, "$1$2")
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

// TruncateRunes cuts s to n runes, appending an ellipsis when it did.
func TruncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
