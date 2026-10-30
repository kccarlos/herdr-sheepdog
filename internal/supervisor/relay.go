package supervisor

import (
	"context"
	"time"
	"unicode"
)

// The daemon relays an utterance the voice left unanswered
// (docs/dev/roles.md "One decision-maker"). On 2026-09-24 00:16:55 the
// user asked "What's the progress of a Discord Copilot so far?"; the
// voice model neither called a tool nor said anything (voice.turn
// {responded: false, tool_calls: 0}), and the question was lost — the
// lost-utterance rate of the baseline scorecard. The daemon recorded the
// words itself, so it does not need the voice to relay them: when an
// input of the user's speech is followed, for relayUnansweredAfter,
// by neither a tool call nor any speech of the voice's own, it runs
// through the same path as a relay (fast paths, then one orchestrator turn) and
// the reply is pushed, as a late reply is (deliverLate) — the voice
// never asked, so there is no tool result to return it in.
//
// What cancels the watch: any voice tool call (to_supervisor claims the
// words; stop means the voice took them as a stop), a newer input (the
// watch re-arms on it), and a reset. The voice speaking on its own
// (small talk it answered itself) marks the input answered, and the
// watch does nothing when it fires — unless that speech turns out to
// have had no audio (silentAnswer). The voice speaking a push, or a "<no speech detected>"
// placeholder, is no answer (voiceSpoke).
//
// An acknowledgement used to be judged in code (ackCues, #108/#114: a
// short "On it." or "I'll check" with no tool call relayed the words
// anyway). The voice now owns that judgment (docs/dev/design/dual-brain.md):
// what it said is its answer. The watch still relays words the voice
// met with no speech and no tool call at all, a lost utterance.

// relayUnansweredAfter is how long an input of the user's speech may
// go with no tool call and no speech of the voice's own before the
// daemon relays it (docs/dev/iteration-2.md's lost-utterance metric counts
// 5 s; the voice's own tool call arrives within a second when it comes).
const relayUnansweredAfter = 4 * time.Second

// relayWatch is the unanswered-utterance watch, under s.mu.
type relayWatch struct {
	timer *time.Timer
	// seq is the input the timer was armed for; gen invalidates a timer
	// that already fired but has not taken s.mu yet.
	seq uint64
	gen uint64
	// pushSpeaking: a push was handed to the voice and its turn has not
	// completed, so what the voice says now is the push, not an answer.
	pushSpeaking bool
	// closed: the daemon is going down; nothing is relayed any more.
	closed bool
}

// armRelayLocked (re)starts the watch for in, the voice input that just
// got words. Caller holds s.mu.
func (s *Supervisor) armRelayLocked(in *shepherdInput) {
	if s.cfg.relayAfter < 0 || s.relay.closed {
		return
	}
	s.disarmRelayLocked()
	gen := s.relay.gen
	s.relay.seq = in.seq
	s.relay.timer = time.AfterFunc(s.cfg.relayAfter, func() { s.relayUnanswered(gen) })
}

// disarmRelayLocked stops the watch. Caller holds s.mu.
func (s *Supervisor) disarmRelayLocked() {
	if s.relay.timer != nil {
		s.relay.timer.Stop()
		s.relay.timer = nil
	}
	s.relay.gen++
}

// toolCalled records that the voice called a tool: whatever the user
// said was taken up, so the daemon does not relay it.
func (s *Supervisor) toolCalled() {
	s.mu.Lock()
	s.disarmRelayLocked()
	s.mu.Unlock()
}

// pushSpoken records that a push is (true) or no longer is (false, the
// voice's turn completed) being spoken.
func (s *Supervisor) pushSpoken(speaking bool) {
	s.mu.Lock()
	s.relay.pushSpeaking = speaking
	s.mu.Unlock()
}

// stopRelay ends the watch for good (shutdown).
func (s *Supervisor) stopRelay() {
	s.mu.Lock()
	s.relay.closed = true
	s.disarmRelayLocked()
	s.mu.Unlock()
}

// relayUnanswered is the watch firing: the input it was armed for is
// still the newest, unclaimed and unanswered, so the daemon claims it
// (with any unanswered words before it in the same run) and relays it.
func (s *Supervisor) relayUnanswered(gen uint64) {
	playback := s.lastPlayback() // outside s.mu: the voice session has its own lock
	s.mu.Lock()
	if gen != s.relay.gen || s.relay.closed {
		s.mu.Unlock()
		return
	}
	s.relay.timer = nil
	last := s.lastInputLocked()
	if last != nil && last.answered && silentAnswer(last, playback) {
		// The voice's "answer" played for under silentAnswerMs: it said
		// nothing the user could hear (inputs.go, voiceSpoke).
		last.answered = false
		s.log.Log(logSource, "supervisor.silent_answer", "", map[string]any{"text": last.text, "run_ms": playback.RunMs})
	}
	if last == nil || last.seq != s.relay.seq || last.kind != InputVoice || last.claimed || last.answered {
		s.mu.Unlock()
		return
	}
	// The floor is the last relay only, not the last push: a push spoken
	// after the question does not answer it.
	run := s.freshRunLocked(s.relayedSeq)
	if len(run) == 0 {
		s.mu.Unlock()
		return
	}
	c := s.claimRunLocked(run)
	// Gemini never marks its input transcription final, so the input may
	// still be open: close it, or words the user adds now would be
	// folded into this relay (heard's trailing-fragment case) and reach
	// nobody.
	c.in.open = false
	waited := s.cfg.Now().Sub(c.in.at)
	s.replies.Add(1)
	s.mu.Unlock()
	defer s.replies.Done()
	s.log.Log(logSource, "supervisor.relay_by_daemon", "", map[string]any{"text": c.text, "waited_ms": waited.Milliseconds(), "inputs": c.n, "utterance_id": c.id})
	res, _ := s.handleUtterance(context.Background(), c, "", true, false)
	if res.Spoken == "" {
		return
	}
	// A fast path answered at once (a stop, an answer to the open prompt,
	// the reset question): it is said like any other push.
	tags := map[string]any{"utterance_id": c.id, "from": "daemon"}
	if bareStop(c.text) {
		tags["stop"] = true
	}
	s.pushTagged("", res.Spoken, res.Scheduling, tags)
}

// spokenLength counts said's words, a CJK character counting half.
func spokenLength(said string) int {
	cjk, words := 0, 0
	inWord := false
	for _, r := range said {
		switch {
		case unicode.Is(unicode.Han, r):
			cjk++
			inWord = false
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'':
			if !inWord {
				words++
			}
			inWord = true
		default:
			inWord = false
		}
	}
	return words + (cjk+1)/2
}
