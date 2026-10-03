package voice

import (
	"strings"
	"time"
	"unicode"
)

// Heard detection (docs/bench-2026-10-03.md #17, #18, #19). A push that
// was sent is not a push that was heard. In run 2 (2026-10-03) a kept
// promise went out at 00:48:38.760 just as the CEO spoke again
// (transcript.in 0.5 s later): the model answered them and never voiced
// the line. At 00:48:12 an interrupting approval cut a spoken answer
// after its first phrase. At 00:50:54 a line with options was read as
// the question and the options alone, its body skipped. Each spoken
// push is now watched from its send until its speech is over and judged
// by what reached the shepherd: the model's output transcript since the
// push, cut to what the client played when it flushed. A push whose
// text that covers less than HeardMinCoverage is unheard.

// HeardMinCoverage is the share of a push's text (its options list
// aside) the voice must have said, and the client played, for the push
// to count as heard: "most of the body".
const HeardMinCoverage = 0.5

// heardTimeout bounds a watch the model never voiced and nothing else
// closed (no later push, no user turn, no reconnect).
const heardTimeout = 45 * time.Second

// Reasons a push was not heard (voice.push_unheard).
const (
	UnheardInterruptedByPush = "interrupted_by_push" // another push cut it
	UnheardUserTurn          = "user_turn"           // the shepherd spoke before it was heard
	UnheardBodySkipped       = "body_skipped"        // voiced, but not most of it
	UnheardNotVoiced         = "not_voiced"          // the model never said it
	UnheardConnectionLost    = "connection_lost"     // the connection went before it was said
)

// UnheardPush is one spoken push judged not heard.
type UnheardPush struct {
	Text string
	Tags map[string]any
	// Coverage is the share of the push's text that reached the
	// shepherd; Heard is that part as the output transcript has it.
	Coverage float64
	Heard    string
	Reason   string
	// Requeued marks a push that was itself a re-queue of an unheard
	// one: it is never re-queued again.
	Requeued bool
}

// Payload is the voice.push_unheard payload (push_id, event_id, agent
// and the other tags; coverage rounded to two places).
func (u UnheardPush) Payload() map[string]any {
	p := map[string]any{}
	for k, v := range u.Tags {
		p[k] = v
	}
	p["text"] = u.Text
	p["coverage"] = float64(int(u.Coverage*100+0.5)) / 100
	p["reason"] = u.Reason
	if u.Heard != "" {
		p["heard"] = u.Heard
	}
	return p
}

// pushWatch follows one spoken push.
type pushWatch struct {
	text     string
	tags     map[string]any
	requeued bool
	sentAt   time.Time
	said     strings.Builder
	voiced   bool // the model said something since the push
	turnDone bool // and then completed its turn
	userTurn bool // the shepherd spoke after the push
	cutBy    string
}

// HeardTracker watches spoken pushes for one session. It is not safe
// for concurrent use: the session calls it under its own lock and acts
// on the verdicts it returns after unlocking.
type HeardTracker struct {
	cur *pushWatch
	// cut are watches whose speech the daemon flushed, waiting for the
	// client's flushed report to say how much was played (oldest first,
	// like PlaybackTracker's awaiting runs).
	cut []cutRun
}

// cutRun is one flushed run waiting for the client's report: the watch
// it cut (nil for a run no push was watched in) and when.
type cutRun struct {
	w  *pushWatch
	at time.Time
}

// Pushed starts watching a spoken push; a push still watched is judged
// now (the new one ends its chance to be heard).
func (h *HeardTracker) Pushed(now time.Time, text string, tags map[string]any, requeued bool) []UnheardPush {
	out := h.expire(now)
	if w := h.cur; w != nil {
		reason := UnheardInterruptedByPush
		if w.turnDone {
			reason = UnheardBodySkipped // its turn was over: nothing cut it
		}
		out = h.judge(out, w, w.said.String(), reason)
	}
	h.cur = &pushWatch{text: text, tags: tags, requeued: requeued, sentAt: now}
	return out
}

// Said records an output transcript fragment.
func (h *HeardTracker) Said(text string) {
	if h.cur != nil && text != "" {
		h.cur.said.WriteString(text)
		h.cur.voiced = true
	}
}

// UserInput records the shepherd speaking (an input transcript).
func (h *HeardTracker) UserInput() {
	if h.cur != nil {
		h.cur.userTurn = true
	}
}

// TurnComplete records the model's turn end. One that arrives before
// the push was voiced belongs to the turn the push cut, not to it.
func (h *HeardTracker) TurnComplete() {
	if h.cur != nil && h.cur.voiced {
		h.cur.turnDone = true
	}
}

// Interrupted records the daemon flushing the output: by "push" (an
// interrupting push) or "model" (a barge-in). awaited says the flushed
// run waits for the client's flushed report (PlaybackTracker.Flush), so
// the two queues stay in step; a run nobody watches holds a place too.
// A push not voiced yet is not what was cut — Gemini also reports the
// interruption an interrupting push itself causes — so it stays
// watched.
func (h *HeardTracker) Interrupted(now time.Time, by string, awaited bool) []UnheardPush {
	w := h.cur
	if w == nil || !w.voiced {
		w = nil
	} else {
		h.cur = nil
		w.cutBy = by
	}
	switch {
	case awaited:
		h.cut = append(h.cut, cutRun{w: w, at: now})
		if len(h.cut) > maxAwaiting {
			out := h.judgeRuns(h.cut[:len(h.cut)-maxAwaiting])
			h.cut = h.cut[len(h.cut)-maxAwaiting:]
			return out
		}
	case w != nil:
		return h.judge(nil, w, w.said.String(), cutReason(w))
	}
	return nil
}

// Flushed applies the client's flushed report: cut (nil when the run
// was heard whole) and heard (the played part). It judges the oldest
// watch the daemon flushed, or, with none, a voiced watch the client
// flushed on its own (the stop button).
func (h *HeardTracker) Flushed(now time.Time, cut *TranscriptCut, heard string) []UnheardPush {
	out := h.expire(now)
	var w *pushWatch
	switch {
	case len(h.cut) > 0:
		w, h.cut = h.cut[0].w, h.cut[1:]
		if w == nil {
			return out // a run no push was watched in
		}
	case h.cur != nil && h.cur.voiced && cut != nil:
		w, h.cur = h.cur, nil
		w.cutBy = "client"
	default:
		return out
	}
	said := w.said.String()
	if cut != nil {
		said = heardPart(said, cut.Full, heard)
	}
	return h.judge(out, w, said, cutReason(w))
}

// Settle judges the watched push once its speech is over: the model
// completed the turn that voiced it and the client is no longer
// playing it. playing is the session's view of the client.
func (h *HeardTracker) Settle(now time.Time, playing bool) []UnheardPush {
	out := h.expire(now)
	if w := h.cur; w != nil && w.turnDone && !playing {
		h.cur = nil
		reason := UnheardBodySkipped
		if w.userTurn {
			reason = UnheardUserTurn
		}
		out = h.judge(out, w, w.said.String(), reason)
	}
	return out
}

// Lost judges everything watched: the connection went away.
func (h *HeardTracker) Lost() []UnheardPush {
	out := h.judgeRuns(h.cut)
	h.cut = nil
	if w := h.cur; w != nil {
		h.cur = nil
		out = h.judge(out, w, w.said.String(), UnheardConnectionLost)
	}
	return out
}

// Reset forgets every watch ("New conversation").
func (h *HeardTracker) Reset() { *h = HeardTracker{} }

// expire judges watches past their time: a flushed one whose client
// never reported (judged on what was generated), and one the model never
// voiced at all.
func (h *HeardTracker) expire(now time.Time) []UnheardPush {
	n := 0
	for n < len(h.cut) && now.Sub(h.cut[n].at) > awaitingTTL {
		n++
	}
	out := h.judgeRuns(h.cut[:n])
	h.cut = h.cut[n:]
	if w := h.cur; w != nil && now.Sub(w.sentAt) > heardTimeout {
		h.cur = nil
		reason := UnheardNotVoiced
		if w.userTurn {
			reason = UnheardUserTurn
		} else if w.voiced {
			reason = UnheardBodySkipped
		}
		out = h.judge(out, w, w.said.String(), reason)
	}
	return out
}

// judge appends w to out when what reached the shepherd (said) covers
// too little of it.
func (h *HeardTracker) judge(out []UnheardPush, w *pushWatch, said, reason string) []UnheardPush {
	said = strings.TrimSpace(said)
	cov := PushCoverage(w.text, said)
	if cov >= HeardMinCoverage {
		return out
	}
	if w.userTurn && reason != UnheardInterruptedByPush {
		reason = UnheardUserTurn
	}
	return append(out, UnheardPush{Text: w.text, Tags: w.tags, Coverage: cov, Heard: said, Reason: reason, Requeued: w.requeued})
}

// judgeRuns judges the watches of flushed runs on what was generated
// (no client report came for them).
func (h *HeardTracker) judgeRuns(runs []cutRun) []UnheardPush {
	var out []UnheardPush
	for _, r := range runs {
		if r.w != nil {
			out = h.judge(out, r.w, r.w.said.String(), cutReason(r.w))
		}
	}
	return out
}

func cutReason(w *pushWatch) string {
	if w.cutBy == "push" {
		return UnheardInterruptedByPush
	}
	return UnheardUserTurn
}

// heardPart replaces the cut run's text (full) at the end of said with
// the part the client played.
func heardPart(said, full, heard string) string {
	t := strings.TrimSpace(said)
	full = strings.TrimSpace(full)
	if full != "" && strings.HasSuffix(t, full) {
		return t[:len(t)-len(full)] + heard
	}
	return heard
}

// PushCoverage is the share of push's text (an "Options: …" list at its
// end aside: the choices are often read when the body is not) that said
// covers: the share of its adjacent token pairs said also has, a token
// being a word of letters or digits or a single CJK character, so
// Chinese counts per character and punctuation not at all. A one-token
// push compares single tokens.
func PushCoverage(push, said string) float64 {
	if i := strings.LastIndex(push, "Options:"); i > 0 {
		push = push[:i]
	}
	pt := coverageTokens(push)
	if len(pt) == 0 {
		return 1
	}
	st := coverageTokens(said)
	if len(pt) == 1 {
		for _, t := range st {
			if t == pt[0] {
				return 1
			}
		}
		return 0
	}
	have := map[[2]string]bool{}
	for i := 1; i < len(st); i++ {
		have[[2]string{st[i-1], st[i]}] = true
	}
	want := map[[2]string]bool{}
	for i := 1; i < len(pt); i++ {
		want[[2]string{pt[i-1], pt[i]}] = true
	}
	n := 0
	for k := range want {
		if have[k] {
			n++
		}
	}
	return float64(n) / float64(len(want))
}

// coverageTokens splits text into lower-case words and single CJK
// characters, dropping everything else.
func coverageTokens(text string) []string {
	var out []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	for _, c := range strings.ToLower(text) {
		switch {
		case unicode.Is(unicode.Han, c) || unicode.Is(unicode.Hiragana, c) || unicode.Is(unicode.Katakana, c) || unicode.Is(unicode.Hangul, c):
			flush()
			out = append(out, string(c))
		case unicode.IsLetter(c) || unicode.IsDigit(c):
			word.WriteRune(c)
		default:
			flush()
		}
	}
	flush()
	return out
}
