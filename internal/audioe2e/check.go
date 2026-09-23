package audioe2e

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

// check evaluates the expectations against a result; it returns the
// failures (none = pass).
func check(e Expect, r *Result, provider string) []string {
	var fail []string
	for _, group := range e.InContains {
		if !FuzzyContains(r.InTranscript, group) {
			fail = append(fail, fmt.Sprintf("input transcript %q lacks %q", r.InTranscript, group))
		}
	}
	if e.NoInTranscript && r.InTranscript != "" {
		fail = append(fail, fmt.Sprintf("hallucinated input transcript %q", r.InTranscript))
	}
	if len(r.NoiseTranscripts) > 0 && e.NoInTranscript {
		fail = append(fail, fmt.Sprintf("transcripts during noise: %q", r.NoiseTranscripts))
	}
	if e.Tool != "" {
		found := false
		for _, t := range r.Tools {
			if strings.HasPrefix(t, e.Tool) {
				found = true
			}
		}
		if !found {
			fail = append(fail, fmt.Sprintf("no %s call (calls: %v)", e.Tool, r.Tools))
		}
	}
	if e.NoTool && len(r.Tools) > 0 {
		fail = append(fail, fmt.Sprintf("unexpected tool calls %v", r.Tools))
	}
	if e.Reply {
		if r.AudioOutBytes == 0 {
			fail = append(fail, "no reply audio")
		}
		if r.OutTranscript == "" {
			fail = append(fail, "no out transcript")
		}
	}
	if e.Flush && r.Flushes == 0 {
		fail = append(fail, "no flush")
	}
	if e.KeysSent && !hasEvent(r.Events, EventMatch{Type: "herdr.send", Payload: map[string]any{"kind": "keys"}}) {
		fail = append(fail, "no keys sent to the agent (approval not authorised)")
	}
	for _, m := range e.Events {
		if !inList(provider, m.Providers) {
			continue
		}
		if !hasEvent(r.Events, m) {
			fail = append(fail, fmt.Sprintf("no event %s %v", m.Type, m.Payload))
		}
	}
	return fail
}

func hasEvent(evs []Event, m EventMatch) bool {
	for _, ev := range evs {
		if ev.Type != m.Type {
			continue
		}
		ok := true
		for k, want := range m.Payload {
			if fmt.Sprint(ev.Payload[k]) != fmt.Sprint(want) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// words lowercases and splits on anything that is not a letter, digit or
// apostrophe.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
}

// FuzzyContains reports whether text contains one of the "|"-separated
// alternatives of group: every word of the alternative appears in text
// (a word of five letters or more may be off by one edit; "what's" and
// "what is" count alike).
func FuzzyContains(text, group string) bool {
	have := words(expand(text))
	for _, alt := range strings.Split(group, "|") {
		want := words(expand(alt))
		if len(want) == 0 {
			continue
		}
		all := true
		for _, w := range want {
			if !hasWord(have, w) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func expand(s string) string {
	r := strings.NewReplacer("’", "'", "what's", "what is", "What's", "what is", "it's", "it is", "It's", "it is", "that's", "that is", "let's", "let us")
	return r.Replace(s)
}

func hasWord(have []string, w string) bool {
	for _, h := range have {
		if h == w || (len(w) >= 5 && editDistance(h, w) <= 1) {
			return true
		}
	}
	return false
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// Table writes the results as a Markdown table.
func Table(w io.Writer, results []Result) {
	_, _ = fmt.Fprintln(w, "| Scenario | Result | Heard (in) | Said (out) | Tool calls | EOS→in / tool / audio / out (ms) | Notes |")
	_, _ = fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
	for _, r := range results {
		status := "pass"
		if !r.Pass {
			status = "**FAIL**"
		}
		notes := strings.Join(r.Failures, "; ")
		if r.SessionsOpened > 0 || len(r.SessionsClosed) > 0 {
			notes = strings.TrimPrefix(notes+fmt.Sprintf("; sessions opened %d, closed %v, billed total %.0f s", r.SessionsOpened, r.SessionsClosed, r.BilledTotalS), "; ")
		}
		if len(r.NoiseTranscripts) > 0 {
			notes = strings.TrimPrefix(notes+fmt.Sprintf("; heard during noise: %q", r.NoiseTranscripts), "; ")
		}
		_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s / %s / %s / %s | %s |\n",
			r.Name, status, cell(r.InTranscript, 80), cell(r.OutTranscript, 100), cell(strings.Join(r.Tools, ", "), 80),
			ms(r.EOSToFirstIn), ms(r.EOSToTool), ms(r.EOSToFirstAudio), ms(r.EOSToFirstOut), cell(notes, 200))
	}
}

func ms(v int64) string {
	if v < 0 {
		return "–"
	}
	return fmt.Sprint(v)
}

func cell(s string, n int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "|", "/"), "\n", " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	if s == "" {
		return "–"
	}
	return s
}
