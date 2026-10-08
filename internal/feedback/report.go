package feedback

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// Mark is one user.feedback event with its diagnosis, as `sheepdog
// feedback` reports it.
type Mark struct {
	Payload
	Diagnosis Diagnosis `json:"diagnosis"`
	// Heard is the user's last words before the mark and Said the voice's
	// output transcript of the run diagnosed, both from the snapshot.
	Heard string `json:"heard,omitempty"`
	Said  string `json:"said,omitempty"`
}

// Marks reads the user.feedback events out of a log, oldest first.
func Marks(events []eventlog.Event) []Mark {
	var out []Mark
	for _, ev := range events {
		if ev.Type != EventType {
			continue
		}
		var p Payload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.ServerTS.IsZero() {
			p.ServerTS = ev.TS
		}
		m := Mark{Payload: p, Diagnosis: Diagnose(p)}
		m.Heard, m.Said = words(p)
		out = append(out, m)
	}
	return out
}

// words are the user's last input transcript and the voice's output
// transcript since then, from the snapshot.
func words(p Payload) (heard, said string) {
	var in, out strings.Builder
	for _, ev := range p.Snapshot.Events {
		if ev.TS.After(p.ServerTS) {
			break
		}
		var m struct {
			Text  string `json:"text"`
			Final bool   `json:"final"`
		}
		_ = json.Unmarshal(ev.Payload, &m)
		switch ev.Type {
		case "voice.transcript.in":
			if out.Len() > 0 {
				in.Reset()
				out.Reset()
			}
			in.WriteString(m.Text)
		case "voice.transcript.out":
			out.WriteString(m.Text)
		}
	}
	return strings.TrimSpace(in.String()), strings.TrimSpace(out.String())
}

// WriteText prints the marks for a reader, newest last, with a tally of
// verdicts first.
func WriteText(w io.Writer, marks []Mark) error {
	if len(marks) == 0 {
		_, err := fmt.Fprintln(w, "no feedback marks in the event log")
		return err
	}
	tally := map[string]int{}
	for _, m := range marks {
		tally[m.Diagnosis.Verdict]++
	}
	var keys []string
	for k := range tally {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, tally[k]))
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "%d marks: %s\n", len(marks), strings.Join(parts, ", "))
	for _, m := range marks {
		fmt.Fprintf(b, "\n%s  %s  %s", m.ServerTS.UTC().Format("2006-01-02 15:04:05Z"), m.ID, m.Tag)
		if m.Note != "" {
			fmt.Fprintf(b, "  %q", m.Note)
		}
		b.WriteString("\n")
		fmt.Fprintf(b, "  %s: %s\n", m.Diagnosis.Verdict, m.Diagnosis.Summary)
		for _, f := range m.Diagnosis.Facts {
			fmt.Fprintf(b, "  - %s\n", f)
		}
		if m.Heard != "" {
			fmt.Fprintf(b, "  heard: %s\n", clip(m.Heard))
		}
		if m.Said != "" {
			fmt.Fprintf(b, "  said:  %s\n", clip(m.Said))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteJSON prints the marks as one JSON array.
func WriteJSON(w io.Writer, marks []Mark) error {
	if marks == nil {
		marks = []Mark{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(marks)
}

func clip(s string) string {
	if utf8.RuneCountInString(s) > 160 {
		return string([]rune(s)[:160]) + "…"
	}
	return s
}
