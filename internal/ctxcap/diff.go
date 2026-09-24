package ctxcap

import "strings"

// Diff says how a point's context differs from the previous point's in
// the same pane, by section key.
type Diff struct {
	// Prev is the previous point (a brain step or a voice turn), 0 when
	// there is none.
	Prev int `json:"prev"`
	// Added are the keys new at this point; Changed the keys whose text
	// changed (the roster, the time); Pruned the keys that were verbatim
	// before and are a stub now.
	Added   []string `json:"added"`
	Changed []string `json:"changed"`
	Pruned  []string `json:"pruned"`
	// Compacted are the keys of compaction summaries new at this point
	// (the messages they replaced are in Removed with reason compacted).
	Compacted []string `json:"compacted"`
	// Removed are the pieces of the previous point that are gone.
	Removed []Removed `json:"removed"`
}

// Removed is one piece of the previous point that is gone, with why:
// compacted (a new summary replaced it), reset (a "New conversation" or a
// fresh voice session came between), or removed.
type Removed struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Class  string `json:"class"`
	Reason string `json:"reason"`
	// Text is the gone piece's text, so the tab can show it struck out.
	Text string `json:"text,omitempty"`
}

// The Removed reasons.
const (
	ReasonCompacted = "compacted"
	ReasonReset     = "reset"
	ReasonRemoved   = "removed"
)

// DiffSnapshots compares cur with prev (prevID names prev; 0 means there
// is no previous point and everything is added). reset says a reset or a
// fresh session came between the two, so what is gone was dropped by it.
func DiffSnapshots(prevID int, prev, cur Snapshot, reset bool) Diff {
	d := Diff{Prev: prevID, Added: []string{}, Changed: []string{}, Pruned: []string{}, Compacted: []string{}, Removed: []Removed{}}
	type piece struct {
		sec  Section
		text string
	}
	before := map[string]piece{}
	if prevID > 0 {
		for _, s := range prev.Sections {
			if reset && strings.HasPrefix(s.Key, "seq:") {
				// Transcript seqs restart at 1 in a new segment: the same
				// key is a different message.
				continue
			}
			before[s.Key] = piece{s, slice(prev.Text, s)}
		}
	}
	seen := map[string]bool{}
	newSummary := false
	for _, s := range cur.Sections {
		seen[s.Key] = true
		old, ok := before[s.Key]
		switch {
		case prevID == 0 || !ok:
			d.Added = append(d.Added, s.Key)
			if s.Class == ClassCompaction {
				d.Compacted = append(d.Compacted, s.Key)
				newSummary = true
			}
		case s.Pruned && !old.sec.Pruned:
			d.Pruned = append(d.Pruned, s.Key)
		case trimSep(slice(cur.Text, s)) != trimSep(old.text):
			d.Changed = append(d.Changed, s.Key)
		}
	}
	if prevID == 0 {
		return d
	}
	for _, s := range prev.Sections {
		if _, matched := before[s.Key]; matched && seen[s.Key] {
			continue
		}
		reason := ReasonRemoved
		switch {
		case reset:
			reason = ReasonReset
		case newSummary:
			reason = ReasonCompacted
		}
		d.Removed = append(d.Removed, Removed{Key: s.Key, Label: s.Label, Class: s.Class, Reason: reason, Text: slice(prev.Text, s)})
	}
	return d
}

func slice(text string, s Section) string {
	if s.Start < 0 || s.End > len(text) || s.Start > s.End {
		return ""
	}
	return text[s.Start:s.End]
}

// trimSep drops trailing newlines, so a piece that only gained or lost
// the separator after it (it became the last of its block) does not
// count as changed.
func trimSep(s string) string {
	for len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return s
}
