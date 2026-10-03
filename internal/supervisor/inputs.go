package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The one rail reads the shepherd's real input (docs/roles.md "The one
// rail"), and so does everything else: the words an utterance runs on
// are the ones the daemon recorded, never the voice model's
// to_supervisor argument (docs/roles.md "One decision-maker"). The
// argument is only what the model chose to relay: on 2026-09-23 it
// relayed a push back as if the shepherd had said it, called
// to_supervisor {text: "yes"} on its own while the shepherd's microphone
// was muted, relayed only the tail of a sentence ("sometimes") and
// turned a transcript of "16" into "What does it say?". So the daemon
// records what the shepherd actually said, typed or tapped — the voice's
// input transcription, typed text, a tap on the pending question — and
//
//   - a to_supervisor call claims every input since the previous
//     accepted call and the last spoken push (claimInput): their words,
//     joined, are the utterance; the argument is logged beside them as
//     a hint (supervisor.relay {heard, said});
//   - a to_supervisor call with no shepherd input newer than both the
//     previous accepted call and the last spoken push is the model's own
//     initiative: no fast path, no brain turn, nothing said
//     (supervisor.voice_initiative_ignored). This is the one guard left
//     of three: a push relayed back (the old echo guard) can no longer
//     reach the brain as the shepherd's words, since the words are the
//     recorded ones, but a call with nothing recorded behind it must
//     still do nothing — it is what the rail stands on. "Spoken" means
//     sent to the voice model (EventPush), not queued (markPushed). Two
//     exceptions are logged: a call that repeats the last spoken push
//     stays refused as supervisor.self_relay, and one that repeats the
//     shepherd's own unclaimed words of the last 10 s is accepted on
//     them as supervisor.relay_rescued (rescueInput);
//   - answering a worker's approval prompt (the brain's send_keys, the
//     bare-answer fast path, a tap) needs an input that arrived after the
//     prompt appeared and within supervisor.authorise_window_s, whose own
//     text — not the tool argument — plainly answers it: never empty, and
//     a "no"-class input never approves (brain.rail_blocked {reason}).
//
// A muted client sends no audio, so no transcript arrives and nothing can
// be authorised by voice while it is muted.

// InputKind is where a shepherd input came from.
type InputKind string

// The shepherd's inputs.
const (
	// InputVoice is the voice model's input transcription of the
	// shepherd's speech, fragments merged per user turn.
	InputVoice InputKind = "voice"
	// InputTyped is text the shepherd typed (/api/inject/utterance, the
	// /voice text box; the only input with --voice=off).
	InputTyped InputKind = "typed"
	// InputTap is a tap on one option of the pending question
	// (POST /api/pending/answer).
	InputTap InputKind = "tap"
)

// shepherdInput is one thing the shepherd said, typed or tapped.
type shepherdInput struct {
	seq  uint64
	kind InputKind
	text string
	at   time.Time
	// open is a voice input still collecting transcript fragments.
	open bool
	// claimed is set once a to_supervisor call was accepted on it: a
	// fragment that trails the call belongs to that call, not to a new
	// one.
	claimed bool
	// used is set once the input authorised an answer to a prompt: one
	// "yes" answers one prompt.
	used bool
	// answered is set when the voice spoke on its own after this input
	// and before the next one (small talk it answered itself): a later
	// relay does not glue these words onto the next utterance, and the
	// daemon does not relay them (relay.go).
	answered bool
	// answeredAt is when answered was set (silentAnswer).
	answeredAt time.Time
	// saidBefore is what the voice was saying, or had said within
	// echoWindow, when this voice input began: what it may be an echo of
	// (echoOf, utterance.go). Empty for anything else.
	saidBefore string
}

// inputGraceDefault is how long a to_supervisor call with no fresh
// shepherd input waits for one before it is the voice model's own
// initiative. The two are not ordered: the input transcription reaches
// the ledger on the event pump (internal/app), the tool call reaches
// ToSupervisor on the voice session's own goroutine, and Gemini's input
// transcription can even trail the tool call in its stream (2026-09-18
// 06:33:02). The wait ends the moment an input arrives.
const inputGraceDefault = 1500 * time.Millisecond

// claim is what one relay took up: the newest input (the one the rail
// reads), the words of every input claimed with it, joined, and how many
// inputs that was.
type claim struct {
	in   *shepherdInput
	text string
	n    int
	// id names the utterance these words are (utterance_id on every
	// event of it: the relay, the brain turn, the reply push). The relay
	// mints it as it claims the words.
	id string
	// start is when the first of these words reached the daemon (the
	// first input's arrival): what the shepherd had heard by then is
	// what the words can answer (heard.go).
	start time.Time
}

// maxInputs bounds the remembered inputs.
const maxInputs = 32

// ErrPendingGone is AnswerTap's answer for a question that is no longer
// the open one (answered, replaced, expired and gone from the screen).
var ErrPendingGone = errors.New("that question is no longer open")

// ErrNoSuchOption is AnswerTap's answer for an option index the question
// does not have.
var ErrNoSuchOption = errors.New("no such option")

// ErrNotSent is AnswerTap's answer when the keys could not be sent to the
// worker; the question stays open.
var ErrNotSent = errors.New("the answer could not be sent")

// ShepherdInput records something the shepherd typed (or, for tests and
// the tap path, any other kind): the web layer calls it before a typed
// utterance reaches the voice model or the text router.
func (s *Supervisor) ShepherdInput(kind InputKind, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeVoiceLocked()
	s.addInputLocked(&shepherdInput{kind: kind, text: text})
}

// heard records a fragment of the shepherd's speech (voice.transcript.in):
// fragments accumulate into one input until the turn closes (final, the
// model speaking, turn complete, or a relay claiming it).
func (s *Supervisor) heard(text string, final bool) {
	playback := s.lastPlayback() // outside s.mu: the voice session has its own lock
	s.mu.Lock()
	defer s.mu.Unlock()
	if text == "" {
		// An empty fragment carries no words: it can only close the input
		// being collected (GPT-Live marks an utterance's end this way). It
		// must never become an input of its own — an empty newest input
		// made the rail refuse the "yes" just before it (audio-e2e,
		// 2026-09-23).
		if last := s.lastInputLocked(); last != nil && last.open && final {
			last.open = false
		}
		return
	}
	if last := s.lastInputLocked(); last != nil && last.open {
		s.inputSeq++
		last.text += text
		last.seq, last.at, last.open = s.inputSeq, s.cfg.Now(), !final
		if last.claimed {
			// The input transcription trails the tool call: these words
			// are the relay already made, not fresh input for another.
			s.relayedSeq = s.inputSeq
		} else {
			s.armRelayLocked(last)
		}
		s.wakeLocked()
		return
	}
	in := &shepherdInput{kind: InputVoice, text: text, open: !final, saidBefore: s.echoCandidateLocked(playback)}
	s.addInputLocked(in)
	s.armRelayLocked(in)
}

// voiceSpoke records the voice's own speech (voice.transcript.out): it
// ends the voice input being collected, and — unless it is a push being
// spoken — it is the voice answering the shepherd's last words itself,
// so they are neither glued onto the next relay nor relayed by the
// daemon (relay.go).
//
// An output that is not speech is no answer (docs/bench-2026-10-03.md
// #11): on 2026-10-02 23:55:05 and 23:55:13 Gemini's whole output was
// the transcript "<no speech detected>" with 0.2 ms of audio, and the
// shepherd's "结果。" / "你说。" counted as answered, so nobody relayed
// them. A run whose transcript, angle-bracket placeholders removed, has
// no letter or digit is ignored here; a run the client played for less
// than silentAnswerMs is caught when the relay watch fires (the
// playback report arrives after the transcript; relayUnanswered). So
// the watch is no longer disarmed here: it checks answered when it
// fires.
func (s *Supervisor) voiceSpoke(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeVoiceLocked()
	s.echoSpokeLocked(text)
	if strings.TrimSpace(text) == "" || s.relay.pushSpeaking || noSpeech(s.echo.said) {
		return
	}
	if last := s.lastInputLocked(); last != nil && !last.answered {
		last.answered, last.answeredAt = true, s.cfg.Now()
	}
}

// silentAnswerMs is the least audio, in milliseconds of playback, an
// output of the voice must have had to count as answering the shepherd.
const silentAnswerMs = 50

// noSpeech reports whether the voice's output transcript says nothing:
// with every <…> placeholder removed (and a trailing one still open,
// since "<no speech " and "detected>" arrive as two fragments), no letter
// or digit is left.
func noSpeech(said string) bool {
	depth := 0
	for _, r := range said {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			return false
		}
	}
	return true
}

// silentAnswer reports whether the client's playback report shows that
// the output which answered in carried less than silentAnswerMs of audio:
// a run that ended (drained or flushed) after the answer was recorded.
func silentAnswer(in *shepherdInput, playback voice.PlaybackReport) bool {
	if in.answeredAt.IsZero() || !playback.At.After(in.answeredAt) {
		return false
	}
	switch playback.State {
	case voice.PlaybackDrained, voice.PlaybackFlushed:
		return playback.RunMs < silentAnswerMs
	}
	return false
}

// closeVoiceInput ends the voice input being collected, so the next
// fragment starts a new one.
func (s *Supervisor) closeVoiceInput() {
	s.mu.Lock()
	s.closeVoiceLocked()
	s.mu.Unlock()
}

func (s *Supervisor) closeVoiceLocked() {
	if last := s.lastInputLocked(); last != nil {
		last.open = false
	}
}

// addInputLocked appends in, numbered and stamped now. Caller holds s.mu.
func (s *Supervisor) addInputLocked(in *shepherdInput) {
	s.inputSeq++
	in.seq, in.at = s.inputSeq, s.cfg.Now()
	s.inputs = append(s.inputs, in)
	if len(s.inputs) > maxInputs {
		s.inputs = append([]*shepherdInput(nil), s.inputs[len(s.inputs)-maxInputs:]...)
	}
	s.wakeLocked()
}

// wakeLocked releases a call waiting out inputGrace. Caller holds s.mu.
func (s *Supervisor) wakeLocked() {
	close(s.inputWake)
	s.inputWake = make(chan struct{})
}

func (s *Supervisor) lastInputLocked() *shepherdInput {
	if len(s.inputs) == 0 {
		return nil
	}
	return s.inputs[len(s.inputs)-1]
}

// markPushed records that a spoken push was just sent to the voice model
// (OnVoiceEvent's EventPush): an input older than it cannot carry a later
// to_supervisor call. It runs on delivery, not when the push was queued:
// on 2026-10-02 23:54:18 the shepherd said "然后让 Claude 加一个删除功能。"
// (transcript.in 18.277), a late reply was queued at 19.220 and moved
// the gate, the voice relayed the words at 19.414 — refused at 20.9 as
// its own initiative — and the push only went to the model at 21.9.
// The instruction was lost (docs/bench-2026-10-03.md #10).
func (s *Supervisor) markPushed(text string) {
	s.mu.Lock()
	s.pushSeq = s.inputSeq
	s.lastPush = text
	s.recordSentLocked(text)
	s.mu.Unlock()
}

// rescueWindow is how recent the shepherd's words must be for a gated
// to_supervisor call that repeats them to be taken up anyway
// (rescueInput).
const rescueWindow = 10 * time.Second

// rescueInput is the gate's safety net. Invariant it protects: a real
// shepherd utterance is never dropped as the voice model's own
// initiative. A push sent to the model between the shepherd's words and
// the model's relay of them still moves the gate past those words; the
// order of the two streams is not the daemon's to fix (the transcript
// arrives on the event pump, the call on the session's goroutine, the
// push on the push queue). So when the gate would refuse a call, but
// its argument is the shepherd's own unclaimed words from the last
// rescueWindow — the same run freshRunLocked would take with the push
// floor left out — the call is accepted on those words
// (supervisor.relay_rescued). A bare fast-path phrase ("yes", "stop")
// is never rescued: one word matches too easily, and it could answer a
// prompt the shepherd had not heard yet.
func (s *Supervisor) rescueInput(said string) (claim, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.freshRunLocked(s.relayedSeq)
	if len(run) == 0 || s.cfg.Now().Sub(run[len(run)-1].at) > rescueWindow {
		return claim{}, false
	}
	words := make([]string, 0, len(run))
	for _, in := range run {
		words = append(words, strings.TrimSpace(in.text))
	}
	joined := strings.Join(words, " ")
	if fastPathPhrase(joined) || fastPathPhrase(said) {
		return claim{}, false
	}
	if !sameWords(said, joined) && !sameWords(said, run[len(run)-1].text) {
		return claim{}, false
	}
	return s.claimRunLocked(run), true
}

// selfRelay reports whether said repeats the last spoken push: the voice
// model relaying our own line (its question, its options) back as if the
// shepherd had said it (2026-10-02 23:54:22.9, "要它跑完直接动手修吗?").
// Such a call is always refused (supervisor.self_relay).
func (s *Supervisor) selfRelay(said string) bool {
	s.mu.Lock()
	push := s.lastPush
	s.mu.Unlock()
	return push != "" && partOf(said, push)
}

// normWords is text lower-cased with every rune that is not a letter or
// a digit dropped: spaces and punctuation go, so Chinese (no spaces) and
// English compare the same way, and "Claude 动手。" equals "claude动手".
func normWords(text string) []rune {
	var out []rune
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, r)
		}
	}
	return out
}

// sameWords reports whether a and b are the same words after normWords,
// allowing for a paraphrase's small edits: equal, one containing the
// other with the shorter at least 60% of the longer, or within an edit
// distance of a quarter of the longer. Both need at least two runes.
func sameWords(a, b string) bool {
	x, y := normWords(a), normWords(b)
	if len(x) < 2 || len(y) < 2 {
		return false
	}
	if len(x) > len(y) {
		x, y = y, x
	}
	if string(x) == string(y) {
		return true
	}
	if strings.Contains(string(y), string(x)) && 5*len(x) >= 3*len(y) {
		return true
	}
	return 4*editDistance(x, y) <= len(y)
}

// partOf reports whether said is made of pieces of line: split at its
// punctuation, the pieces (two runes or more after normWords) found in
// line cover at least half of said. The voice relays a push's question
// and its options joined with its own separators.
func partOf(said, line string) bool {
	full := string(normWords(line))
	if full == "" {
		return false
	}
	pieces := strings.FieldsFunc(said, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
	found, total := 0, 0
	for _, p := range pieces {
		w := string(normWords(p))
		n := len([]rune(w))
		if n < 2 {
			continue
		}
		total += n
		if strings.Contains(full, w) {
			found += n
		}
	}
	return total > 0 && 2*found >= total
}

// editDistance is the Levenshtein distance of two rune slices.
func editDistance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// claimInput is the to_supervisor gate: every shepherd input newer than
// both the previous accepted call and the last spoken push, waiting up
// to inputGrace for one (see inputGraceDefault). ok is false when none
// arrived: the call is the voice model's own initiative.
func (s *Supervisor) claimInput(ctx context.Context) (claim, bool) {
	var deadline <-chan time.Time
	for {
		s.mu.Lock()
		if run := s.freshRunLocked(max(s.relayedSeq, s.pushSeq)); len(run) > 0 {
			c := s.claimRunLocked(run)
			s.mu.Unlock()
			return c, true
		}
		wake := s.inputWake
		s.mu.Unlock()
		if s.cfg.inputGrace <= 0 {
			return claim{}, false
		}
		if deadline == nil {
			t := time.NewTimer(s.cfg.inputGrace)
			defer t.Stop()
			deadline = t.C
		}
		select {
		case <-wake:
		case <-deadline:
			return claim{}, false
		case <-ctx.Done():
			return claim{}, false
		}
	}
}

// freshRunLocked is the run of unclaimed inputs newer than floor, oldest
// first, ending with the newest input: what the shepherd said since the
// daemon last acted on their words. A mid-sentence pause splits one
// sentence into two inputs (GPT-Live closes an input at every pause,
// 2026-09-23 23:53:26), so the run takes both; it stops at an input the
// voice already answered itself, which belongs to that exchange, not to
// this one. Empty when the newest input is not fresh. Caller holds s.mu.
func (s *Supervisor) freshRunLocked(floor uint64) []*shepherdInput {
	var run []*shepherdInput
	for i := len(s.inputs) - 1; i >= 0; i-- {
		in := s.inputs[i]
		if in.seq <= floor || in.claimed || in.kind == InputTap || (len(run) > 0 && in.answered) {
			break
		}
		run = append(run, in)
	}
	for i, j := 0, len(run)-1; i < j; i, j = i+1, j-1 {
		run[i], run[j] = run[j], run[i]
	}
	return run
}

// claimRunLocked takes run up as one request: its inputs are claimed
// (fragments trailing the newest stay with it, heard), the relay
// counters move past them, and no daemon relay is left armed for them.
// Caller holds s.mu.
func (s *Supervisor) claimRunLocked(run []*shepherdInput) claim {
	words := make([]string, 0, len(run))
	for _, in := range run {
		in.claimed = true
		if t := strings.TrimSpace(in.text); t != "" {
			words = append(words, t)
		}
	}
	s.relayedSeq = s.inputSeq
	s.acceptedSeq++
	s.disarmRelayLocked()
	return claim{in: run[len(run)-1], text: strings.Join(words, " "), n: len(run), id: s.nextID("utt"), start: run[0].at}
}

// promptSinceLocked is when p's prompt appeared: its pending action's
// creation, or earlier when the same prompt was recorded before and this
// pending action is a revival (the shepherd's "yes" to a prompt whose
// pending expired arrives before the revival it causes). Caller holds
// s.mu.
func (s *Supervisor) promptSinceLocked(p state.PendingAction) time.Time {
	if t, ok := s.promptSeenAt[p.PaneID]; ok && t.Before(p.CreatedAt) {
		return t
	}
	return p.CreatedAt
}

// railInputLocked is the one rail's reading of the shepherd for keys
// that answer p: the newest input, which must have arrived after the
// prompt appeared and within Config.AuthoriseWindow, not already have
// answered a prompt, be non-empty, and — when the keys approve — not be
// a "no". It returns the input, or the refusal reason. Caller holds s.mu.
func (s *Supervisor) railInputLocked(p state.PendingAction, keys []string) (*shepherdInput, string) {
	alias := s.aliasOf(p.PaneID)
	in := s.lastInputLocked()
	now := s.cfg.Now()
	switch {
	case in == nil:
		return nil, fmt.Sprintf("the shepherd has said nothing that could answer %s's prompt", alias)
	case in.at.Before(s.promptSinceLocked(p)):
		return nil, fmt.Sprintf("the shepherd has said nothing since %s's prompt appeared", alias)
	case now.Sub(in.at) > s.cfg.AuthoriseWindow:
		return nil, fmt.Sprintf("the shepherd's last words were %d s ago, past supervisor.authorise_window_s (%d s)", int(now.Sub(in.at).Seconds()), int(s.cfg.AuthoriseWindow.Seconds()))
	case in.used:
		return nil, "the shepherd's last words already answered a prompt"
	case strings.TrimSpace(in.text) == "":
		return nil, "the shepherd's last words were empty"
	case negativeInput(in.text) && approves(p, keys):
		return nil, fmt.Sprintf("the shepherd said %q, which cannot approve %s's prompt", strings.TrimSpace(in.text), alias)
	}
	return in, ""
}

// negativeWords open a "no"-class input: "no", "don't …", "stop", "cancel".
var negativeWords = map[string]bool{"no": true, "nope": true, "nah": true, "don't": true, "dont": true, "do not": true, "stop": true, "cancel": true, "never": true, "deny": true, "decline": true, "reject": true, "wait": true, "hold": true, "negative": true}

// negativeInput reports whether the shepherd's words are a "no": the
// whole input is one of mapAnswer's no words, or it opens with one.
func negativeInput(text string) bool {
	a := normalizeAnswer(text)
	if noWords[a] {
		return true
	}
	fields := strings.Fields(a)
	if len(fields) == 0 {
		return false
	}
	first := strings.Trim(fields[0], ".!?,;:")
	if negativeWords[first] {
		return true
	}
	return len(fields) > 1 && first == "do" && strings.Trim(fields[1], ".!?,;:") == "not"
}

// approves reports whether keys select p's yes answer or an option that
// approves (its label opens with yes / allow / approve / always /
// proceed / accept / run / continue).
func approves(p state.PendingAction, keys []string) bool {
	label := strings.ToLower(strings.TrimSpace(keysLabel(p, keys)))
	if label == "yes" {
		return true
	}
	for _, w := range []string{"yes", "allow", "approve", "always", "proceed", "accept", "run", "continue"} {
		if strings.HasPrefix(label, w) {
			return true
		}
	}
	return false
}

// railBlockedLocked logs a refusal of the code paths (fast path, tap)
// the way the brain logs its own. Caller holds s.mu.
func (s *Supervisor) railBlockedLocked(p state.PendingAction, keys []string, via, reason string) {
	s.log.Log(logSource, "brain.rail_blocked", p.PaneID, map[string]any{"agent": s.aliasOf(p.PaneID), "keys": keys, "trigger": via, "reason": reason, "id": p.ID})
}

// AnswerTap answers the open question with the option the shepherd
// tapped (POST /api/pending/answer): option is 1-based, as in
// PendingOptions. The tap is recorded as a shepherd input first, so it
// passes the same rail as a spoken answer; the keys go through the same
// executor (the post-answer poll included), and the voice gets a silent
// line saying what was tapped. id must name the open question, which
// lives as long as its pane is blocked at that prompt (pending.go,
// ExpirePending): anything else is ErrPendingGone.
func (s *Supervisor) AnswerTap(ctx context.Context, id string, option int) (voice.ToolResult, error) {
	s.mu.Lock()
	p, has := s.store.Pending()
	if !has || p.ID != id || p.Kind != state.PendingAnswerPrompt {
		s.mu.Unlock()
		return voice.ToolResult{}, ErrPendingGone
	}
	if option < 1 || option > len(p.Options) || p.Options[option-1].Meta {
		s.mu.Unlock()
		return voice.ToolResult{}, fmt.Errorf("%w: %d (the question has %d)", ErrNoSuchOption, option, len(p.Options))
	}
	o := p.Options[option-1]
	s.closeVoiceLocked()
	zh := s.ceoChineseLocked()
	s.addInputLocked(&shepherdInput{kind: InputTap, text: o.Label})
	// The tap is answered here and now: it is not fresh input for a
	// later to_supervisor call (the voice relaying the silent line below
	// back, say).
	s.relayedSeq = s.inputSeq
	s.acceptedSeq++
	in, reason := s.railInputLocked(p, o.Keys)
	if reason != "" {
		s.railBlockedLocked(p, o.Keys, "tap", reason)
		s.mu.Unlock()
		return voice.ToolResult{}, errors.New(reason)
	}
	alias := s.aliasOf(p.PaneID)
	res, sent := s.answerKeys(ctx, p, o.Keys, o.Label, func(l string) string { return toldLine(alias, l, zh) }, "tap", map[string]any{"option": option})
	if sent {
		in.used = true
	}
	s.mu.Unlock()
	if !sent {
		return res, ErrNotSent
	}
	s.push(p.PaneID, fmt.Sprintf("The shepherd tapped %s on %s's question.", strings.TrimRight(o.Label, "."), alias), voice.UrgencySilent)
	if s.brain != nil {
		s.brain.Daemon(fmt.Sprintf("the shepherd tapped %q on %s's question; %s", o.Label, alias, lowerFirst(res.Spoken)))
	}
	return res, nil
}

// PendingOption is one choice of a pending question as the pages and a
// tap see it: Index is 1-based (what POST /api/pending/answer takes),
// Default marks the option the cursor is on.
type PendingOption struct {
	Index   int      `json:"index"`
	Label   string   `json:"label"`
	Keys    []string `json:"keys"`
	Default bool     `json:"default"`
}

// PendingOptions lists p's options for /api/state and pending.created.
func PendingOptions(p state.PendingAction) []PendingOption {
	out := make([]PendingOption, 0, len(p.Options))
	for i, o := range p.Options {
		if o.Meta {
			continue
		}
		out = append(out, PendingOption{Index: i + 1, Label: o.Label, Keys: append([]string{}, o.Keys...), Default: o.Default})
	}
	return out
}
