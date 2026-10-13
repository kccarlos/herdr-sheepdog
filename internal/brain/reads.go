package brain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// What the orchestrator already read (harness iteration 6b, information).
// A follow-up ("why?", "more", "an example?") re-reads a screen that the
// first answer already relayed, and the model cannot tell that nothing
// is new: tool results prune to a stub after a few turns, so it never
// sees the earlier read beside the new one. The daemon keeps its own
// record of each agent's last reads, per source, and a read whose
// content an earlier turn already got carries a note saying so. The
// comparison is mechanical (normalised text contained in an earlier
// read); what to do about it is the model's.

// readsKept is how many reads per agent and source the record keeps.
const readsKept = 4

// readKey names one agent's source (screen, session).
type readKey struct{ pane, source string }

// readSeen is one read as the daemon recorded it.
type readSeen struct {
	norm string
	at   time.Time
	turn int
	// relayed: the turn that made the read ended with a spoken answer.
	relayed bool
}

var (
	// readNoise is what changes on a screen without its content
	// changing: spinner and cursor glyphs.
	readNoise = regexp.MustCompile(`[\x{2800}-\x{28FF}█▉▊▋▌▍▎▏▐▮▯✻✽✶✳✢✺◐◓◑◒◴◵◶◷]`)
	// readClock is a clock time or an elapsed time ("12:04", "3m 4s",
	// "12s", "1.2 s", "340 tokens").
	readClock = regexp.MustCompile(`\d{1,2}:\d{2}(:\d{2})?|\d+(\.\d+)?\s?(ms|s|m|h|sec|secs|min|mins|k?\s?tokens)\b`)
)

// normalizeRead is the text a read is compared by: noise and clock
// times gone, whitespace runs collapsed, blank lines dropped.
func normalizeRead(text string) string {
	text = readClock.ReplaceAllString(readNoise.ReplaceAllString(text, ""), "#")
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// noteRead records a read of pane's source made in turn (stamped with
// the turn's start, now) and, when an earlier turn already read the same
// content (this read's normalised text is contained in that one's),
// returns the daemon's note for the model and the stub's suffix. A
// relayed earlier read is preferred.
func (b *Brain) noteRead(turn int, now time.Time, pane, alias, source, text string) (note, stub string) {
	norm := normalizeRead(text)
	if norm == "" {
		return "", ""
	}
	k := readKey{pane, source}
	b.mu.Lock()
	var hit *readSeen
	list := b.reads[k]
	for i := len(list) - 1; i >= 0; i-- {
		e := list[i]
		if e.turn >= turn || !strings.Contains(e.norm, norm) {
			continue
		}
		if hit == nil || (e.relayed && !hit.relayed) {
			hit = &e
		}
	}
	if b.reads == nil {
		b.reads = map[readKey][]readSeen{}
	}
	list = append(list, readSeen{norm: norm, at: now, turn: turn})
	b.reads[k] = list[max(0, len(list)-readsKept):]
	b.mu.Unlock()
	if hit == nil {
		return "", ""
	}
	what := "screen"
	if source == SourceSession {
		what = "session's last message"
	}
	answered := ""
	if hit.relayed {
		answered = ", which you answered the user from"
	}
	b.log.Log(logSource, "brain.read_unchanged", pane, map[string]any{"agent": alias, "source": source, "turn": turn, "same_as_turn": hit.turn, "relayed": hit.relayed})
	when := hit.at.Format("15:04:05")
	note = fmt.Sprintf("Note from the daemon: this is the same as what you read of %s's %s at %s (%s ago)%s; nothing in it has changed since.", aliasOr(alias), what, when, ago(now.Sub(hit.at)), answered)
	return note, fmt.Sprintf("; unchanged since %s", when)
}

// relayedReads marks the reads turn made as relayed, when its answer
// was spoken: a reply with a spoken line that is not skipped or silent.
func (b *Brain) relayedReads(turn int, r Reply) {
	if strings.TrimSpace(r.Spoken) == "" {
		return
	}
	switch strings.ToLower(strings.TrimSpace(r.Urgency)) {
	case UrgencySkip, UrgencySilent:
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, list := range b.reads {
		for i := range list {
			if list[i].turn == turn {
				list[i].relayed = true
			}
		}
	}
}
