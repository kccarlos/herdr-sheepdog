package voice

import (
	"strings"
	"time"
)

// Owed unheard lines, shared by the providers' sessions. A spoken push
// judged unheard (heard.go) that the shepherd is owed a hearing of waits
// here for a quiet moment (RequeueHold and the session's own checks),
// then goes back in the push queue once — or is dropped, when what the
// line was for has already happened.
//
// Campaign D (docs/eval/campaign-d-2026-10-03.md N3): of the 15
// unheard pushes that were not re-queued, 13 answered the shepherd's own
// question (answers_utterance). The voice had said "On it." or
// "Checking." in their place, or the line was cut, and the answer was
// lost for good. An answer is now owed too, unless by the time it would
// go back it is stale: a newer answer went out since it was sent (to the
// same question, or to a newer one), or the shepherd has since handed
// the voice new words (to_supervisor, or stop). A stale answer is not
// re-queued; the brain gets the unheard fact instead (EventPushUnheard
// with Requeue false), as for any other unheard line.

// Reasons an owed line is dropped instead of going back in the queue
// (voice.push_dropped).
const (
	DropSuperseded   = "superseded"     // an answer: a newer answer went out since
	DropAskedSince   = "asked_since"    // an answer: the shepherd has handed on new words since
	DropNoLongerOpen = "no_longer_open" // the supervisor says it is no longer open
)

// maxAsks bounds the shepherd's recent hand-offs a Requeue remembers.
const maxAsks = 16

// Requeue holds a session's owed unheard lines. It is not safe for
// concurrent use: the session calls it under its own lock and acts on
// what Take returns after unlocking.
type Requeue struct {
	lines []owedLine
	// lastAnswer is when the last spoken push answering an utterance was
	// sent; asks are the shepherd's words the voice handed on since.
	lastAnswer time.Time
	asks       []handOff
}

type owedLine struct {
	push UnheardPush
}

type handOff struct {
	at   time.Time
	text string
}

// Release is what Take decided for one owed line.
type Release struct {
	Push UnheardPush
	// Drop is the voice.push_dropped reason ("": back in the queue).
	Drop string
	// Tell: the line is dropped without having been heard; the session
	// emits EventPushUnheard (Requeue false) so the supervisor tells the
	// brain.
	Tell bool
}

// Owed reports whether an unheard push goes back in the queue: never a
// re-queue itself; an answer to the shepherd's question unless it is
// already stale; otherwise a decision they must take, a kept or due
// promise, or a line the supervisor's open check says is still open
// (open: the session asks the check outside its lock).
func (r *Requeue) Owed(v UnheardPush, open bool) bool {
	if v.Requeued {
		return false
	}
	if PushUtterance(v.Tags) != "" {
		return r.stale(v) == ""
	}
	return OwedTags(v.Tags) || open
}

// OpenCheckDecides reports whether the supervisor's open check decides
// an owed line's release: not for an answer (it is about a question, not
// a prompt) and not for a kept or due promise (owed whatever the
// prompt's state).
func OpenCheckDecides(tags map[string]any) bool {
	return PushUtterance(tags) == "" && !OwedPromise(tags)
}

// Add keeps an owed line until Take.
func (r *Requeue) Add(v UnheardPush) { r.lines = append(r.lines, owedLine{push: v}) }

// Len is the number of owed lines waiting.
func (r *Requeue) Len() int { return len(r.lines) }

// Pushed records a spoken push sent at now (the time its heard watch
// starts, so an answer is never stale by its own send).
func (r *Requeue) Pushed(now time.Time, tags map[string]any) {
	if PushUtterance(tags) != "" && now.After(r.lastAnswer) {
		r.lastAnswer = now
	}
}

// Asked records the voice handing on the shepherd's words (to_supervisor
// with their text, stop with none).
func (r *Requeue) Asked(now time.Time, text string) {
	r.asks = append(r.asks, handOff{at: now, text: text})
	if len(r.asks) > maxAsks {
		r.asks = r.asks[len(r.asks)-maxAsks:]
	}
}

// Reset forgets everything ("New conversation").
func (r *Requeue) Reset() { *r = Requeue{} }

// Take empties the waiting lines and says what to do with each: an
// answer when stale (superseded, asked_since; the
// brain is told); otherwise back in the queue — after the session has
// asked the supervisor's open check where OpenCheckDecides
// (no_longer_open), outside its lock.
func (r *Requeue) Take() []Release {
	out := make([]Release, 0, len(r.lines))
	for i := range r.lines {
		l := &r.lines[i]
		rel := Release{Push: l.push}
		if PushUtterance(l.push.Tags) != "" {
			if why := r.stale(l.push); why != "" {
				rel.Drop, rel.Tell = why, true
			}
		}
		out = append(out, rel)
	}
	r.lines = nil
	return out
}

// stale says why an answer would no longer be news ("" when it would):
// a newer answer went out after it was sent, or the shepherd handed on
// new words after it was sent. The voice relaying the push's own text
// back (supervisor.self_relay, campaign D lang-mixed-codeswitch r1) is
// not the shepherd's words.
func (r *Requeue) stale(v UnheardPush) string {
	if v.SentAt.IsZero() {
		return ""
	}
	if r.lastAnswer.After(v.SentAt) {
		return DropSuperseded
	}
	for _, a := range r.asks {
		if a.at.After(v.SentAt) && (strings.TrimSpace(a.text) == "" || PushCoverage(v.Text, a.text) < HeardMinCoverage) {
			return DropAskedSince
		}
	}
	return ""
}
