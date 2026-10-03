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
	for _, b := range []struct {
		name string
		max  int64
		got  int64
	}{
		{"end of speech → first input transcript", e.MaxEOSToFirstInMS, r.EOSToFirstIn},
		{"end of speech → tool call", e.MaxEOSToToolMS, r.EOSToTool},
		{"end of speech → first reply audio", e.MaxEOSToFirstAudioMS, r.EOSToFirstAudio},
		{"end of speech → first output transcript", e.MaxEOSToFirstOutMS, r.EOSToFirstOut},
	} {
		switch {
		case b.max <= 0:
		case b.got == -1:
			fail = append(fail, fmt.Sprintf("%s: never happened (max %d ms)", b.name, b.max))
		case b.got > b.max:
			fail = append(fail, fmt.Sprintf("%s: %d ms, max %d ms", b.name, b.got, b.max))
		}
	}
	for _, a := range e.Absent {
		if !inList(provider, a.Providers) {
			continue
		}
		if n := countAbsent(a, r); n > a.MaxCount {
			what := a.Type
			if a.Timeline != "" {
				what = "timeline " + a.Timeline
			}
			if a.Contains != "" {
				what += fmt.Sprintf(" containing %q", a.Contains)
			}
			msg := fmt.Sprintf("%s seen %d times in the window, at most %d allowed", what, n, a.MaxCount)
			if a.Note != "" {
				msg += " (" + a.Note + ")"
			}
			fail = append(fail, msg)
		}
	}
	return fail
}

// countAbsent counts what a matches in r inside its window, which is
// relative to r's end of speech.
func countAbsent(a Absent, r *Result) int {
	in := func(at int64) bool {
		d := at - r.EOSMS
		return d >= a.FromMS && (a.ToMS == 0 || d <= a.ToMS)
	}
	contains := func(s string) bool {
		return a.Contains == "" || strings.Contains(strings.ToLower(s), strings.ToLower(a.Contains))
	}
	n := 0
	if a.Timeline != "" {
		for _, t := range r.Timeline {
			if t.Kind == a.Timeline && in(t.AtMS) && contains(t.Text) {
				n++
			}
		}
		return n
	}
	for _, ev := range r.Events {
		if ev.Type != a.Type || ev.AtMS < 0 || !in(ev.AtMS) {
			continue
		}
		if !hasEvent([]Event{ev}, EventMatch{Type: a.Type, Payload: a.Payload}) || !contains(fmt.Sprint(ev.Payload)) {
			continue
		}
		n++
	}
	return n
}

// latencies computes the four end-of-speech latencies of a window
// (-1 = never) from the timeline and events: the first input
// transcript after the speech started, the first tool call, and the
// first reply audio and output transcript at or after the end of speech.
func latencies(timeline []Timed, events []Event, speechStartMS, eosMS int64, spoke bool) (in, tool, audio, out int64) {
	in, tool, audio, out = -1, -1, -1, -1
	for _, tl := range timeline {
		d := tl.AtMS - eosMS
		switch tl.Kind {
		case "in":
			if in == -1 && spoke && tl.AtMS >= speechStartMS {
				in = d
			}
		case "audio":
			if audio == -1 && d >= 0 {
				audio = d
			}
		case "out":
			if out == -1 && d >= 0 {
				out = d
			}
		}
	}
	for _, ev := range events {
		if ev.Type == "voice.tool_call" && tool == -1 && spoke {
			tool = ev.AtMS - eosMS
		}
	}
	return in, tool, audio, out
}

// TurnResult is one turn's outcome in a multi-turn scenario.
type TurnResult struct {
	Name          string   `json:"name"`
	StartMS       int64    `json:"start_ms"`
	EndMS         int64    `json:"end_ms"`
	EOSMS         int64    `json:"eos_ms"`
	Pass          bool     `json:"pass"`
	Failures      []string `json:"failures,omitempty"`
	InTranscript  string   `json:"in_transcript"`
	OutTranscript string   `json:"out_transcript"`
	Tools         []string `json:"tools,omitempty"`

	EOSToFirstIn    int64 `json:"eos_to_first_in_ms"`
	EOSToTool       int64 `json:"eos_to_tool_ms"`
	EOSToFirstAudio int64 `json:"eos_to_first_audio_ms"`
	EOSToFirstOut   int64 `json:"eos_to_first_out_ms"`
}

// turnWindow is where one turn sits in the run (ms from the start).
type turnWindow struct {
	start, end, speechStart, eos int64
	spoke                        bool
}

// sliceTurn is the part of r inside w, as a Result the checks can read:
// transcripts, tool calls, audio, flushes and events of that window, and
// its latencies from the turn's own end of speech.
func sliceTurn(r *Result, w turnWindow) Result {
	s := Result{Name: r.Name, Provider: r.Provider, EOSMS: w.eos}
	inWin := func(at int64) bool { return at >= w.start && (w.end <= 0 || at < w.end) }
	var in, out strings.Builder
	for _, t := range r.Timeline {
		if !inWin(t.AtMS) {
			continue
		}
		s.Timeline = append(s.Timeline, t)
		switch t.Kind {
		case "in":
			in.WriteString(t.Text)
		case "out":
			out.WriteString(t.Text)
		case "audio":
			s.AudioOutBytes++ // a burst started here: some audio arrived
		case "flush":
			s.Flushes++
		}
	}
	s.InTranscript, s.OutTranscript = strings.TrimSpace(in.String()), strings.TrimSpace(out.String())
	for _, ev := range r.Events {
		if ev.AtMS < 0 || !inWin(ev.AtMS) {
			continue
		}
		s.Events = append(s.Events, ev)
		if ev.Type == "voice.tool_call" {
			name, _ := ev.Payload["name"].(string)
			args, _ := ev.Payload["args"].(map[string]any)
			text, _ := args["text"].(string)
			s.Tools = append(s.Tools, strings.TrimSpace(name+" "+quoteIf(text)))
		}
	}
	s.EOSToFirstIn, s.EOSToTool, s.EOSToFirstAudio, s.EOSToFirstOut = latencies(s.Timeline, s.Events, w.speechStart, w.eos, w.spoke)
	return s
}

// checkTurns checks every turn's Expect against its window, and returns
// the turn results and their failures prefixed with the turn's name.
func checkTurns(sc Scenario, r *Result, windows []turnWindow, provider string) ([]TurnResult, []string) {
	if len(sc.Turns) == 0 {
		return nil, nil
	}
	var out []TurnResult
	var fails []string
	for i, t := range sc.Turns {
		if i >= len(windows) {
			name := orName(t.Name, i)
			out = append(out, TurnResult{Name: name, Failures: []string{"not run"}})
			fails = append(fails, name+": not run")
			continue
		}
		w := windows[i]
		s := sliceTurn(r, w)
		f := check(t.Expect, &s, provider)
		name := orName(t.Name, i)
		out = append(out, TurnResult{Name: name, StartMS: w.start, EndMS: w.end, EOSMS: w.eos, Pass: len(f) == 0, Failures: f,
			InTranscript: s.InTranscript, OutTranscript: s.OutTranscript, Tools: s.Tools,
			EOSToFirstIn: s.EOSToFirstIn, EOSToTool: s.EOSToTool, EOSToFirstAudio: s.EOSToFirstAudio, EOSToFirstOut: s.EOSToFirstOut})
		for _, x := range f {
			fails = append(fails, name+": "+x)
		}
	}
	return out, fails
}

func orName(name string, i int) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf("turn %d", i+1)
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
		for _, t := range r.Turns {
			ts := "pass"
			if !t.Pass {
				ts = "**FAIL**"
			}
			_, _ = fmt.Fprintf(w, "| ↳ %s | %s | %s | %s | %s | %s / %s / %s / %s | %s |\n",
				t.Name, ts, cell(t.InTranscript, 80), cell(t.OutTranscript, 100), cell(strings.Join(t.Tools, ", "), 80),
				ms(t.EOSToFirstIn), ms(t.EOSToTool), ms(t.EOSToFirstAudio), ms(t.EOSToFirstOut), cell(strings.Join(t.Failures, "; "), 200))
		}
	}
}

// ms renders a latency; -1 is "never" (a negative value is real: the
// streaming transcript arrived before the speech ended).
func ms(v int64) string {
	if v == -1 {
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
