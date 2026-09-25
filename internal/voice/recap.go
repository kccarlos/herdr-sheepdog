package voice

import (
	"fmt"
	"strings"
)

// Recap is the recent conversation a voice session keeps so a fresh
// model session can be told what was said before it: GPT-Live replays it
// when it reopens a billed session, and Gemini re-seeds a connection that
// came back without its resumption handle (docs/timelines.md C23). It is
// not safe for concurrent use: the session calls it under its own lock.
type Recap struct {
	// Max bounds the entries kept (the oldest go first); 0 keeps
	// DefaultRecapTurns.
	Max     int
	entries []RecapEntry
}

// Recap roles.
const (
	RecapUser       = "user"       // the shepherd
	RecapAssistant  = "assistant"  // the voice itself
	RecapSupervisor = "supervisor" // a spoken push the voice was given
)

// DefaultRecapTurns is how many entries a Recap keeps by default.
const DefaultRecapTurns = 12

// RecapEntry is one remembered utterance.
type RecapEntry struct {
	Role string
	Text string
	// CutAfterMs, on an assistant entry, says the client cut its speech
	// off after this long: Text is only the part the shepherd heard.
	CutAfterMs int
	Cut        bool
}

// Add remembers one finished utterance (blank text is ignored).
func (r *Recap) Add(role, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	r.entries = append(r.entries, RecapEntry{Role: role, Text: text})
	max := r.Max
	if max <= 0 {
		max = DefaultRecapTurns
	}
	if len(r.entries) > max {
		r.entries = r.entries[len(r.entries)-max:]
	}
}

// Cut replaces the newest assistant entry that full (everything the cut
// run said) contains with heard, the part the client played: the recap
// then says what the shepherd actually heard, as the brain's "Assistant
// said (cut off after N s)" does. It reports whether an entry matched.
func (r *Recap) Cut(full, heard string, heardMs int) bool {
	full = strings.TrimSpace(full)
	for i := len(r.entries) - 1; i >= 0; i-- {
		e := &r.entries[i]
		if e.Role != RecapAssistant || e.Cut {
			continue
		}
		if full == "" || !strings.Contains(full, e.Text) {
			return false
		}
		e.Text, e.Cut, e.CutAfterMs = strings.TrimSpace(heard), true, heardMs
		return true
	}
	return false
}

// Reset forgets everything ("New conversation").
func (r *Recap) Reset() { r.entries = nil }

// Len is the number of entries kept.
func (r *Recap) Len() int { return len(r.entries) }

// Recent returns the newest entries whose text fits in budget
// characters, oldest first; the oldest one included is cut to its tail
// when only part of it fits.
func (r *Recap) Recent(budget int) []RecapEntry {
	var out []RecapEntry
	for i := len(r.entries) - 1; i >= 0 && budget > 0; i-- {
		e := r.entries[i]
		if e.Cut && e.Text == "" {
			continue
		}
		if len(e.Text) > budget {
			e.Text = e.Text[len(e.Text)-budget:]
		}
		budget -= len(e.Text)
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// RecapLines renders entries as "Shepherd: …" / "You: …" /
// "Supervisor update: …" lines for a model told about its own earlier
// conversation.
func RecapLines(entries []RecapEntry) string {
	var b strings.Builder
	for _, e := range entries {
		switch e.Role {
		case RecapUser:
			b.WriteString("Shepherd: ")
		case RecapAssistant:
			if e.Cut {
				fmt.Fprintf(&b, "You (cut off after %.0f s): ", float64(e.CutAfterMs)/1000)
			} else {
				b.WriteString("You: ")
			}
		default:
			b.WriteString("Supervisor update: ")
		}
		b.WriteString(e.Text)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
