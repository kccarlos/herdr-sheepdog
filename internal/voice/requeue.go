package voice

import (
	"strings"
	"time"
)

// Owed unheard lines, shared by the providers' sessions. A spoken push
// judged unheard (heard.go) that the user is owed a hearing of waits
// here for a quiet moment (RequeueHold and the session's own checks),
// then goes back in the push queue once — or is dropped, when what the
// line was for has already happened.
//
// Campaign D (docs/dev/eval/campaign-d-2026-10-03.md N3): of the 15
// unheard pushes that were not re-queued, 13 answered the user's own
// question (answers_utterance). The voice had said "On it." or
// "Checking." in their place, or the line was cut, and the answer was
// lost for good. An answer is now owed too, unless by the time it would
// go back it is stale: a newer answer went out since it was sent (to the
// same question, or to a newer one), or the user has since handed
// the voice new words (to_supervisor, or stop). A stale answer is not
// re-queued; the orchestrator gets the unheard fact instead (EventPushUnheard
// with Requeue false), as for any other unheard line.
//
// #111's collision-finish-push runs (2 of 6, and 4 of 6 in its two
// arms together): the user talked over a kept-promise finish line ("…is
// done. It added the changelog note…"), asked "did it touch the
// changelog?", and the voice answered from the part it had voiced ("Yes,
// it added the changelog note about the fix…"). The re-queued line then
// replayed the same news. A line whose content has reached the
// user since — in what the voice said after the verdict, the voice's
// own replies and later pushes alike — is now dropped
// (voice.push_dropped {reason: already_heard}).

// Reasons an owed line is dropped instead of going back in the queue
// (voice.push_dropped).
const (
	DropAlreadyHeard = "already_heard"  // its content reached the user since
	DropSuperseded   = "superseded"     // an answer: a newer answer went out since
	DropAskedSince   = "asked_since"    // an answer: the user has handed on new words since
	DropNoLongerOpen = "no_longer_open" // the supervisor says it is no longer open
)

// maxAsks bounds the user's recent hand-offs a Requeue remembers;
// maxSaidSince bounds what it keeps of the voice's words per line.
const (
	maxAsks      = 16
	maxSaidSince = 4000
)

// Requeue holds a session's owed unheard lines. It is not safe for
// concurrent use: the session calls it under its own lock and acts on
// what Take returns after unlocking.
type Requeue struct {
	lines []owedLine
	// lastAnswer is when the last spoken push answering an utterance was
	// sent; asks are the user's words the voice handed on since.
	lastAnswer time.Time
	asks       []handOff
}

type owedLine struct {
	push UnheardPush
	// said is what the voice said after the line was judged unheard (a
	// string: lines are copied as the slice grows).
	said string
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
	// orchestrator.
	Tell bool
}

// Owed reports whether an unheard push goes back in the queue: never a
// re-queue itself; an answer to the user's question unless it is
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

// Said records the voice's output transcript.
func (r *Requeue) Said(text string) {
	for i := range r.lines {
		if l := &r.lines[i]; len(l.said) < maxSaidSince {
			l.said += text
		}
	}
}

// Pushed records a spoken push sent at now (the time its heard watch
// starts, so an answer is never stale by its own send).
func (r *Requeue) Pushed(now time.Time, tags map[string]any) {
	if PushUtterance(tags) != "" && now.After(r.lastAnswer) {
		r.lastAnswer = now
	}
}

// Asked records the voice handing on the user's words (to_supervisor
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
// answer when stale (superseded, asked_since; the orchestrator is told); any
// line whose content has reached the user since (already_heard);
// otherwise back in the queue — after the session has
// asked the supervisor's open check where OpenCheckDecides
// (no_longer_open), outside its lock.
func (r *Requeue) Take() []Release {
	out := make([]Release, 0, len(r.lines))
	for i := range r.lines {
		l := &r.lines[i]
		rel := Release{Push: l.push}
		// A stale answer first: the orchestrator is told, and it can see in its
		// transcript whether the voice retold it since (campaign D
		// barge-in-long-answer: a later answer's words covered an earlier
		// one the user had cut off).
		if PushUtterance(l.push.Tags) != "" {
			rel.Drop = r.stale(l.push)
			rel.Tell = rel.Drop != ""
		}
		if rel.Drop == "" && alreadyHeard(l.push, l.said) {
			rel.Drop = DropAlreadyHeard
		}
		out = append(out, rel)
	}
	r.lines = nil
	return out
}

// stale says why an answer would no longer be news ("" when it would):
// a newer answer went out after it was sent, or the user handed on
// new words after it was sent. The voice relaying the push's own text
// back (supervisor.self_relay, campaign D lang-mixed-codeswitch r1) is
// not the user's words.
func (r *Requeue) stale(v UnheardPush) string {
	if v.SentAt.IsZero() {
		return ""
	}
	if r.lastAnswer.After(v.SentAt) {
		return DropSuperseded
	}
	// The words the answer answers: the last hand-off before it was sent.
	// The voice calling again with exactly those words after the answer
	// came (seen with the answer as a late function response: "On it.",
	// the same question again, the body never read) is not the user
	// asking something new.
	var asked string
	for _, a := range r.asks {
		if !a.at.After(v.SentAt) {
			asked = strings.TrimSpace(a.text)
		}
	}
	for _, a := range r.asks {
		if !a.at.After(v.SentAt) {
			continue
		}
		t := strings.TrimSpace(a.text)
		if asked != "" && strings.EqualFold(t, asked) {
			continue
		}
		if t == "" || PushCoverage(v.Text, a.text) < HeardMinCoverage {
			return DropAskedSince
		}
	}
	return ""
}

// alreadyHeard reports whether a line's content reached the user:
// what they heard of it plus what the voice said after the verdict
// covers at least HeardMinCoverage of its content words
// (ContentCoverage), and the words said since added to it. Content
// words, not word pairs as the heard judge uses: the voice retells the
// news in its own words ("It added the changelog note" → "it added a
// note to the changelog").
func alreadyHeard(v UnheardPush, saidSince string) bool {
	if strings.TrimSpace(saidSince) == "" {
		return false
	}
	before := ContentCoverage(v.Text, v.Heard)
	after := ContentCoverage(v.Text, v.Heard+" "+saidSince)
	return after >= HeardMinCoverage && after > before
}

// ContentCoverage is the share of push's distinct content words (its
// "Options: …" list aside, as in PushCoverage) that said also has: the
// coverage tokens less a short list of function words, so a retelling
// in other words still counts. A push with no content words is covered.
func ContentCoverage(push, said string) float64 {
	if i := strings.LastIndex(push, "Options:"); i > 0 {
		push = push[:i]
	}
	want := map[string]bool{}
	for _, t := range coverageTokens(push) {
		if !functionWords[t] {
			want[t] = true
		}
	}
	if len(want) == 0 {
		return 1
	}
	have := map[string]bool{}
	for _, t := range coverageTokens(said) {
		have[t] = true
	}
	n := 0
	for t := range want {
		if have[t] {
			n++
		}
	}
	return float64(n) / float64(len(want))
}

// functionWords are the coverage tokens ContentCoverage ignores:
// English function words and common Chinese particles (each Han
// character is a token of its own).
var functionWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an the is are was were be been being it its s this that these those
		to of in on for and or but so as at by with from you your i me my we our he she they them his her
		their do does did have has had will would shall should can could may might just now then than
		there here what which who whom when where why how not no yes ok okay about also into up out over
		all any some one
		的 了 是 在 我 你 它 吗 呢 吧 啊 个 这 那 就 也 都 还 和 要`) {
		m[w] = true
	}
	return m
}()
