package voice

import (
	"fmt"
	"strings"
	"unicode"
)

// Recap is the recent conversation a voice session keeps so a fresh
// model session can be told what was said before it: GPT-Live replays it
// when it reopens a billed session, and Gemini re-seeds a connection that
// came back without its resumption handle (docs/dev/timelines.md C23). It is
// not safe for concurrent use: the session calls it under its own lock.
type ackMarker struct {
	seq         uint64
	prevUserSeq uint64
}

type Recap struct {
	// Max bounds the entries kept (the oldest go first); 0 keeps
	// DefaultRecapTurns.
	Max     int
	entries []RecapEntry
	// focus is the supervisor's latest current-focus line (a silent push
	// tagged TagFocus): a fresh session gets it after the recap.
	focus string
	// joinNextUser merges the next RecapUser utterance onto the trailing
	// RecapUser entry when an intervening 0-heard assistant turn was cut.
	joinNextUser bool
	nextSeq      uint64
	acks         []ackMarker
}

// CutEllipsis formats the heard prefix of a cut assistant reply with a
// trailing ellipsis ("…") without doubling existing punctuation.
func CutEllipsis(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	trimmed := strings.TrimRight(text, ".,;:!?…，。；：！？、 ")
	if trimmed == "" {
		return text + "…"
	}
	return trimmed + "…"
}

// TagFocus marks the supervisor's silent current-focus line (the agent
// in focus, the open question, key points of the last answer): the
// session keeps the latest one in its Recap (SetFocus), so a fresh
// model session is told it with the recap of the conversation.
const TagFocus = "focus"

// IsFocus reports whether tags mark the current-focus line.
func IsFocus(tags map[string]any) bool { return tags[TagFocus] == true }

// SetFocus keeps the supervisor's latest current-focus line.
func (r *Recap) SetFocus(text string) { r.focus = strings.TrimSpace(text) }

// Focus is the latest current-focus line ("" when none).
func (r *Recap) Focus() string { return r.focus }

// Recap roles.
const (
	RecapUser       = "user"       // the user
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
	// off after this long: Text is only the part the user heard.
	CutAfterMs int
	Cut        bool
	seq        uint64
}

// Add remembers one finished utterance. Blank text is ignored, and so
// is what would only teach a fresh model the wrong habit
// (docs/dev/history/bench-2026-10-03.md #9): a bare acknowledgement of the voice's
// own ("On it.", "checking", "好的") and its "<no speech detected>"
// placeholder, and a user entry that only repeats the line the
// voice was just given or said (its own speech heard back, or a push's
// question relayed as if the user had asked it). Run 1's 18-hour
// session had relayed 17 of 17 Chinese push questions with "on it".
func (r *Recap) Add(role, text string) {
	r.AddSeq(role, text)
}

// AddSeq is Add returning a monotonic sequence id for the utterance (0 when
// ignored as blank or duplicate), so a later CutSeq targets this exact turn.
func (r *Recap) AddSeq(role, text string) uint64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	if role == RecapAssistant && BareAck(text) {
		r.joinNextUser = false
		r.nextSeq++
		seq := r.nextSeq
		var prevUser uint64
		if n := len(r.entries); n > 0 && r.entries[n-1].Role == RecapUser {
			prevUser = r.entries[n-1].seq
		}
		r.acks = append(r.acks, ackMarker{seq: seq, prevUserSeq: prevUser})
		if len(r.acks) > maxAwaiting {
			r.acks = r.acks[len(r.acks)-maxAwaiting:]
		}
		return seq
	}
	if role == RecapUser && r.repeatsRecent(text) {
		return 0
	}
	if r.joinNextUser {
		r.joinNextUser = false
		if role == RecapUser && len(r.entries) > 0 && r.entries[len(r.entries)-1].Role == RecapUser {
			r.entries[len(r.entries)-1].Text = strings.TrimSpace(r.entries[len(r.entries)-1].Text) + " " + text
			return r.entries[len(r.entries)-1].seq
		}
	}
	r.nextSeq++
	seq := r.nextSeq
	r.entries = append(r.entries, RecapEntry{Role: role, Text: text, seq: seq})
	max := r.Max
	if max <= 0 {
		max = DefaultRecapTurns
	}
	if len(r.entries) > max {
		r.entries = r.entries[len(r.entries)-max:]
	}
	return seq
}

// recapAckWords are the voice's bare acknowledgements, normalised
// (lower case, letters and digits only).
var recapAckWords = map[string]bool{
	"onit": true, "checking": true, "checkingnow": true, "letmecheck": true, "ok": true, "okay": true,
	"gotit": true, "sure": true, "onemoment": true, "justamoment": true, "nospeechdetected": true,
	"好的": true, "好": true, "收到": true, "明白": true, "明白了": true, "稍等": true, "马上": true,
	"我看看": true, "我查一下": true, "正在查": true, "好的我看看": true, "好的稍等": true,
}

// BareAck reports whether text is only an acknowledgement ("On it.",
// "checking", "好的。") or the "<no speech detected>" placeholder: a
// line with nothing in it a later model could follow up on.
func BareAck(text string) bool {
	return recapAckWords[recapNorm(text)]
}

// repeatsRecent reports whether a user entry only repeats one of
// the last few lines the voice was given or said. A short answer that
// happens to echo the question ("fix it", "允许它运行") is the
// user's own: only a repeat of at least selfRelayMin letters counts.
func (r *Recap) repeatsRecent(text string) bool {
	n := recapNorm(text)
	if len([]rune(n)) < selfRelayMin {
		return false
	}
	seen := 0
	for i := len(r.entries) - 1; i >= 0 && seen < 3; i-- {
		e := r.entries[i]
		if e.Role == RecapUser {
			continue
		}
		seen++
		if strings.Contains(recapNorm(e.Text), n) {
			return true
		}
	}
	return false
}

// selfRelayMin is the shortest user entry (letters and digits)
// taken as a repeat of the voice's own line.
const selfRelayMin = 8

// recapNorm lower-cases text and keeps only its letters and digits.
func recapNorm(text string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(text) {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// Cut replaces the newest assistant entry that full (everything the cut
// run said) contains with heard, the part the client played: the recap
// then says what the user actually heard, as the orchestrator's "Assistant
// said (cut off after N s)" does. A 0-heard reply is removed completely
// and surrounding user turns are joined (join_around). It reports whether
// an entry matched.
func (r *Recap) Cut(full, heard string, heardMs int) bool {
	return r.CutSeq(0, full, heard, heardMs)
}

// CutSeq cuts the assistant turn recorded with seq (or falls back to Cut's
// newest substring match when seq == 0).
func (r *Recap) CutSeq(seq uint64, full, heard string, heardMs int) bool {
	full = strings.TrimSpace(full)
	heard = strings.TrimSpace(heard)
	for i := len(r.entries) - 1; i >= 0; i-- {
		e := &r.entries[i]
		if e.Role != RecapAssistant || e.Cut {
			continue
		}
		if seq != 0 {
			if e.seq != seq {
				continue
			}
		} else if full == "" || !strings.Contains(full, e.Text) {
			continue
		}
		if heard == "" {
			r.entries = append(r.entries[:i], r.entries[i+1:]...)
			if i > 0 && i < len(r.entries) && r.entries[i-1].Role == RecapUser && r.entries[i].Role == RecapUser {
				r.entries[i-1].Text = strings.TrimSpace(r.entries[i-1].Text) + " " + strings.TrimSpace(r.entries[i].Text)
				r.entries = append(r.entries[:i], r.entries[i+1:]...)
			} else if i > 1 && r.entries[i-1].Role == RecapUser && r.entries[i-2].Role == RecapUser {
				r.entries[i-2].Text = strings.TrimSpace(r.entries[i-2].Text) + " " + strings.TrimSpace(r.entries[i-1].Text)
				r.entries = append(r.entries[:i-1], r.entries[i:]...)
			} else if i == len(r.entries) && i > 0 && r.entries[i-1].Role == RecapUser {
				r.joinNextUser = true
			}
			return true
		}
		e.Text, e.Cut, e.CutAfterMs = heard, true, heardMs
		if i == len(r.entries)-1 {
			r.joinNextUser = false
		}
		return true
	}
	if seq != 0 {
		for k, m := range r.acks {
			if m.seq != seq {
				continue
			}
			r.acks = append(r.acks[:k], r.acks[k+1:]...)
			if heard == "" && m.prevUserSeq != 0 {
				for u := range r.entries {
					if r.entries[u].seq != m.prevUserSeq || r.entries[u].Role != RecapUser {
						continue
					}
					if u+1 < len(r.entries) && r.entries[u+1].Role == RecapUser {
						r.entries[u].Text = strings.TrimSpace(r.entries[u].Text) + " " + strings.TrimSpace(r.entries[u+1].Text)
						r.entries = append(r.entries[:u+1], r.entries[u+2:]...)
					} else if u == len(r.entries)-1 {
						r.joinNextUser = true
					}
					break
				}
			}
			break
		}
		return false
	}
	if heard == "" && BareAck(full) && len(r.entries) > 0 && r.entries[len(r.entries)-1].Role == RecapUser {
		r.joinNextUser = true
	}
	return false
}

// Entries returns a copy of the entries kept, oldest first (a provider
// switch hands them to the new session).
func (r *Recap) Entries() []RecapEntry {
	return append([]RecapEntry(nil), r.entries...)
}

// Seed puts entries (another session's, already filtered) before the
// ones kept, within Max.
func (r *Recap) Seed(entries []RecapEntry) {
	var clean []RecapEntry
	for _, e := range entries {
		if e.Cut && strings.TrimSpace(e.Text) == "" {
			continue
		}
		r.nextSeq++
		e.seq = r.nextSeq
		clean = append(clean, e)
	}
	r.entries = append(clean, r.entries...)
	max := r.Max
	if max <= 0 {
		max = DefaultRecapTurns
	}
	if len(r.entries) > max {
		r.entries = r.entries[len(r.entries)-max:]
	}
}

// Reset forgets everything ("New conversation"), the focus included.
func (r *Recap) Reset() { r.entries, r.focus, r.joinNextUser, r.acks = nil, "", false, nil }

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

// RecapLines renders entries as "User: …" / "You: …" /
// "Supervisor update: …" lines for a model told about its own earlier
// conversation.
func RecapLines(entries []RecapEntry) string {
	var b strings.Builder
	for _, e := range entries {
		switch e.Role {
		case RecapUser:
			b.WriteString("User: ")
		case RecapAssistant:
			if e.Cut {
				fmt.Fprintf(&b, "You (cut off after %.0f s): %s\n", float64(e.CutAfterMs)/1000, CutEllipsis(e.Text))
				continue
			}
			b.WriteString("You: ")
		default:
			b.WriteString("Supervisor update: ")
		}
		b.WriteString(e.Text)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
