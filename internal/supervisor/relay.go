package supervisor

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// The daemon relays an utterance the voice left unanswered
// (docs/roles.md "One decision-maker"). On 2026-09-24 00:16:55 the
// shepherd asked "What's the progress of a Discord Copilot so far?"; the
// voice model neither called a tool nor said anything (voice.turn
// {responded: false, tool_calls: 0}), and the question was lost — the
// lost-utterance rate of the baseline scorecard. The daemon recorded the
// words itself, so it does not need the voice to relay them: when an
// input of the shepherd's speech is followed, for relayUnansweredAfter,
// by neither a tool call nor any speech of the voice's own, it runs
// through the same path as a relay (fast paths, then one brain turn) and
// the reply is pushed, as a late reply is (deliverLate) — the voice
// never asked, so there is no tool result to return it in.
//
// What cancels the watch: any voice tool call (to_supervisor claims the
// words; stop means the voice took them as a stop), a newer input (the
// watch re-arms on it), and a reset. The voice speaking on its own
// (small talk it answered itself) marks the input answered, and the
// watch does nothing when it fires — unless that speech turns out to
// have had no audio (silentAnswer), or was only an acknowledgement
// (ackOnly). The voice speaking a push, or a "<no speech detected>"
// placeholder, is no answer (voiceSpoke).
//
// An acknowledgement is not an answer (docs/bench/tier2-2026-10-03.md
// #1): in approve-claude-permission r3 the shepherd said "Yes, make the
// edit.", the voice said "On it." and called no tool, and the approval
// was lost; in status-en r1 "What's going on?" got "I'll check on the
// agents for you right now." and nothing else. Speech that only promises
// work, or says it does not know, hands nothing to anyone, so when the
// watch fires on it the daemon relays the words after all
// (supervisor.ack_not_answer, then relay_by_daemon).

// relayUnansweredAfter is how long an input of the shepherd's speech may
// go with no tool call and no speech of the voice's own before the
// daemon relays it (docs/iteration-2.md's lost-utterance metric counts
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

// toolCalled records that the voice called a tool: whatever the shepherd
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
		// nothing the shepherd could hear (inputs.go, voiceSpoke).
		last.answered = false
		s.log.Log(logSource, "supervisor.silent_answer", "", map[string]any{"text": last.text, "run_ms": playback.RunMs})
	}
	if last != nil && last.answered && ackOnly(last.answeredWith) {
		last.answered = false
		s.log.Log(logSource, "supervisor.ack_not_answer", "", map[string]any{"text": last.text, "said": strings.TrimSpace(last.answeredWith)})
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
	// still be open: close it, or words the shepherd adds now would be
	// folded into this relay (heard's trailing-fragment case) and reach
	// nobody.
	c.in.open = false
	waited := s.cfg.Now().Sub(c.in.at)
	s.replies.Add(1)
	s.mu.Unlock()
	defer s.replies.Done()
	s.log.Log(logSource, "supervisor.relay_by_daemon", "", map[string]any{"text": c.text, "waited_ms": waited.Milliseconds(), "inputs": c.n, "utterance_id": c.id})
	res := s.handleUtterance(context.Background(), c, "", true)
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

// ackCues are what an acknowledgement says in place of an answer: a
// promise of work ("on it", "I'll check", "let me look", "one moment",
// 我查一下, 稍等), not knowing ("I don't have an update", 不清楚) or a
// reply that has not come back ("A reply on the workers' status has not
// come back yet.", "the results aren't in yet", 还没有结果: campaign D
// N2, the voice prompt's own old wording). The voice knows nothing about
// the agents (prompts/assistant.md), so each means the words still need
// the brain. Matched anywhere in a short output (ackMaxWords).
var ackCues = regexp.MustCompile(`(?i)^\W*(checking|looking)\b|\b(has(n'?t| not) (yet )?come back|(have|has)n'?t (yet )?come in|(is|are)n'?t back|not back yet|(results?|repl(y|ies)|answers?|updates?|word|news) (is|are)n'?t in|(is|are) not in yet|no (reply|replies|answer|result|results|update|updates|word|news) (back )?yet|nothing (back |in )?yet|still waiting (on|for) (a |the )?(reply|replies|answer|result|results|update|word)|on it|checking (on|in|with|that|now|it)|let me check|i'?ll (check|look|see|find|ask|get|pass|relay|tell|let|go|find out|have a look)|i will (check|look|see|find|ask|get|pass|relay|tell|let)|let me (check|see|look|find|ask|get)|i'?m (checking|looking|asking|on it|finding)|looking into|working on it|one (moment|sec|second)|just a (moment|sec|second)|give me a (moment|sec|second)|right away|hang on|hold on|bear with me|i don'?t (have|know)|no update|not sure|i can'?t (tell|see|say))\b|好的?[，,。]?\s*我|我(来|去|先)?(查|看|问|确认)(一下|看|看看)?|让我(查|看|问|确认)|稍等|马上|这就|正在(查|看|问|确认)|等我|不(太)?清楚|不知道|没有(新的)?(更新|消息)|还没(有)?(结果|回复|回音|消息|回来|反馈|答复)|结果还没|(回复|答复)还没`)

// ackWords are whole outputs that only acknowledge ("Okay.", "Sure.",
// "好的。"): matched only as the entire output, since they also open
// real answers.
var ackWords = map[string]bool{
	"ok": true, "okay": true, "sure": true, "alright": true, "all right": true, "got it": true, "will do": true,
	"sure thing": true, "yes": true, "yeah": true, "yep": true, "understood": true, "copy": true, "copy that": true, "roger": true,
	"okay will do": true, "sounds good": true, "absolutely": true, "of course": true,
	"好": true, "好的": true, "收到": true, "明白": true, "可以": true, "行": true, "没问题": true, "好嘞": true, "嗯": true, "嗯好的": true,
}

// ackMaxWords bounds an acknowledgement: an output longer than this
// (words, or CJK runes / 2) said something beyond the promise.
const ackMaxWords = 24

// ackOnly reports whether the voice's output said is only an
// acknowledgement: a whole-output ack word, or a short output with an
// ack cue. Placeholders ("<no speech detected>") are ignored.
func ackOnly(said string) bool {
	said = stripPlaceholders(said)
	if strings.TrimSpace(said) == "" {
		return false
	}
	if ackWords[strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '\'' {
			return unicode.ToLower(r)
		}
		return ' '
	}, said)), " ")] {
		return true
	}
	if spokenLength(said) > ackMaxWords {
		return false
	}
	return ackCues.MatchString(said)
}

// stripPlaceholders removes every <…> placeholder from said.
func stripPlaceholders(said string) string {
	var b strings.Builder
	depth := 0
	for _, r := range said {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
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
