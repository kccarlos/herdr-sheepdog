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

// The one rail reads the user's real input (docs/roles.md "The one
// rail"), and so does everything else: the words an utterance runs on
// are the ones the daemon recorded, never the voice model's
// to_supervisor argument (docs/roles.md "One decision-maker"). The
// argument is only what the model chose to relay: on 2026-09-23 it
// relayed a push back as if the user had said it, called
// to_supervisor {text: "yes"} on its own while the user's microphone
// was muted, relayed only the tail of a sentence ("sometimes") and
// turned a transcript of "16" into "What does it say?". So the daemon
// records what the user actually said, typed or tapped — the voice's
// input transcription, typed text, a tap on the pending question — and
//
//   - a to_supervisor call claims every input since the previous
//     accepted call and the last spoken push (claimInput): their words,
//     joined, are the utterance; the argument is logged beside them as
//     a hint (supervisor.relay {heard, said});
//   - a to_supervisor call with no user input newer than both the
//     previous accepted call and the last spoken push is the model's own
//     initiative: no fast path, no orchestrator turn, nothing said
//     (supervisor.voice_initiative_ignored). This is the one guard left
//     of three: a push relayed back (the old echo guard) can no longer
//     reach the orchestrator as the user's words, since the words are the
//     recorded ones, but a call with nothing recorded behind it must
//     still do nothing — it is what the rail stands on. "Spoken" means
//     sent to the voice model (EventPush), not queued (markPushed). The
//     ignored call gets a silent fact back (the user said nothing new,
//     with their last words), so the voice knows nothing was done;
//   - answering a worker's approval prompt (the orchestrator's send_keys, the
//     bare-answer fast path, a tap) needs an input that arrived after the
//     prompt appeared and within supervisor.authorise_window_s, whose own
//     text — not the tool argument — plainly answers it: never empty, and
//     a "no"-class input never approves (brain.rail_blocked {reason}).
//
// A muted client sends no audio, so no transcript arrives and nothing can
// be authorised by voice while it is muted.

// InputKind is where a user input came from.
type InputKind string

// The user's inputs.
const (
	// InputVoice is the voice model's input transcription of the
	// user's speech, fragments merged per user turn.
	InputVoice InputKind = "voice"
	// InputTyped is text the user typed (/api/inject/utterance, the
	// /voice text box; the only input with --voice=off).
	InputTyped InputKind = "typed"
	// InputTap is a tap on one option of the pending question
	// (POST /api/pending/answer).
	InputTap InputKind = "tap"
)

// shepherdInput is one thing the user said, typed or tapped.
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
	// answeredAt is when answered was set (silentAnswer); answeredWith
	// is what the voice said since.
	answeredAt   time.Time
	answeredWith string
	// saidBefore is what the voice was saying, or had said within
	// echoWindow, when this voice input began: what it may be an echo of
	// (echoOf, utterance.go). Empty for anything else.
	saidBefore string
}

// inputGraceDefault is how long a to_supervisor call with no fresh
// user input waits for one before it is the voice model's own
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
	// event of it: the relay, the orchestrator turn, the reply push). The relay
	// mints it as it claims the words.
	id string
	// start is when the first of these words reached the daemon (the
	// first input's arrival): what the user had heard by then is
	// what the words can answer (heard.go).
	start time.Time
	// ins are the claimed inputs, oldest first (the echo guard reads
	// each one).
	ins []*shepherdInput
	// reading is the voice's own short reading of the words (the
	// to_supervisor call's optional "reading" argument): a hint for the
	// orchestrator, labelled as the voice's, never the user's words.
	reading string
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

// ShepherdInput records something the user typed (or, for tests and
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

// heard records a fragment of the user's speech (voice.transcript.in):
// fragments accumulate into one input until the turn closes (final, the
// model speaking, turn complete, or a relay claiming it).
func (s *Supervisor) heard(text string, final bool) {
	playback := s.lastPlayback() // outside s.mu: the voice session has its own lock
	onset, onsetOK := s.lastSpeechOnset()
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
	if !onsetOK {
		onset = time.Time{}
	}
	in := &shepherdInput{kind: InputVoice, text: text, open: !final, saidBefore: s.echoCandidateLocked(playback, onset)}
	s.addInputLocked(in)
	s.armRelayLocked(in)
}

// voiceSpoke records the voice's own speech (voice.transcript.out): it
// ends the voice input being collected, and — unless it is a push being
// spoken — it is the voice answering the user's last words itself,
// so they are neither glued onto the next relay nor relayed by the
// daemon (relay.go).
//
// An output that is not speech is no answer (docs/history/bench-2026-10-03.md
// #11): on 2026-10-02 23:55:05 and 23:55:13 Gemini's whole output was
// the transcript "<no speech detected>" with 0.2 ms of audio, and the
// user's "结果。" / "你说。" counted as answered, so nobody relayed
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
	if strings.TrimSpace(text) != "" {
		s.promptVoiceSpokeLocked(s.relay.pushSpeaking) // buried.go
	}
	if strings.TrimSpace(text) == "" || s.relay.pushSpeaking || noSpeech(s.echo.said) {
		return
	}
	last := s.lastInputLocked()
	if last == nil {
		return
	}
	if !last.answered {
		last.answered, last.answeredAt, last.answeredWith = true, s.cfg.Now(), ""
	}
	last.answeredWith += text
}

// silentAnswerMs is the least audio, in milliseconds of playback, an
// output of the voice must have had to count as answering the user.
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
	s.promptCEOSpokeLocked() // buried.go
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
// on 2026-10-02 23:54:18 the user said "然后让 Claude 加一个删除功能。"
// (transcript.in 18.277), a late reply was queued at 19.220 and moved
// the gate, the voice relayed the words at 19.414 — refused at 20.9 as
// its own initiative — and the push only went to the model at 21.9.
// The instruction was lost (docs/history/bench-2026-10-03.md #10).
func (s *Supervisor) markPushed(text string) {
	s.mu.Lock()
	s.pushSeq = s.inputSeq
	s.lastPush = text
	s.recordSentLocked(text)
	s.promptPushedLocked(text) // buried.go
	s.mu.Unlock()
}

// claimInput is the to_supervisor gate: every user input newer than
// the previous accepted call, waiting up to inputGrace for one (see
// inputGraceDefault). ok is false when none arrived: the call is the
// voice model's own, and no user words authorise an orchestrator turn. A spoken
// push no longer moves the floor (dual-brain ablation G2): briefings go
// as function responses, which the voice does not mistake for the user,
// so words the user said before a briefing still count, and the fuzzy
// rescue of words a push had hidden (rescueInput) is gone with it.
func (s *Supervisor) claimInput(ctx context.Context) (claim, bool) {
	var deadline <-chan time.Time
	for {
		s.mu.Lock()
		if run := s.freshRunLocked(s.relayedSeq); len(run) > 0 {
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
// first, ending with the newest input: what the user said since the
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
	return claim{in: run[len(run)-1], text: strings.Join(words, " "), n: len(run), id: s.nextID("utt"), start: run[0].at, ins: run}
}

// promptSinceLocked is when p's prompt appeared: its pending action's
// creation, or earlier when the same prompt was recorded before and this
// pending action is a revival (the user's "yes" to a prompt whose
// pending expired arrives before the revival it causes). Caller holds
// s.mu.
func (s *Supervisor) promptSinceLocked(p state.PendingAction) time.Time {
	if t, ok := s.promptSeenAt[p.PaneID]; ok && t.Before(p.CreatedAt) {
		return t
	}
	return p.CreatedAt
}

// railInputLocked is the one rail's reading of the user for keys
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
		return nil, fmt.Sprintf("the user has said nothing that could answer %s's prompt", alias)
	case in.at.Before(s.promptSinceLocked(p)):
		return nil, fmt.Sprintf("the user has said nothing since %s's prompt appeared", alias)
	case now.Sub(in.at) > s.cfg.AuthoriseWindow:
		return nil, fmt.Sprintf("the user's last words were %d s ago, past supervisor.authorise_window_s (%d s)", int(now.Sub(in.at).Seconds()), int(s.cfg.AuthoriseWindow.Seconds()))
	case in.used:
		return nil, "the user's last words already answered a prompt"
	case strings.TrimSpace(in.text) == "":
		return nil, "the user's last words were empty"
	case negativeInput(in.text) && approves(p, keys):
		return nil, fmt.Sprintf("the user said %q, which cannot approve %s's prompt", strings.TrimSpace(in.text), alias)
	}
	return in, ""
}

// Whether the user's words decline, put off or question a prompt is
// the orchestrator's to decide (RULES.md: "not now" puts it off; a no option is
// not a pause). The deferral, decline and question word lists that held
// a decline back (#108, #114) were removed in dual-brain ablation G4: the
// rail keeps only that the keys answer fresh words of the user's own
// and that a "no" never approves.

// declines reports whether keys select p's no answer or an option whose
// label declines (no / don't / deny / decline / reject / cancel / skip).
func declines(p state.PendingAction, keys []string) bool {
	label := strings.ToLower(strings.TrimSpace(keysLabel(p, keys)))
	if label == "no" {
		return true
	}
	for _, w := range []string{"no", "don't", "do not", "deny", "decline", "reject", "cancel", "skip"} {
		if label == w || strings.HasPrefix(label, w+" ") || strings.HasPrefix(label, w+",") {
			return true
		}
	}
	return len(keys) == 1 && keys[0] == "esc" && !approves(p, keys)
}

// fillers are hesitation sounds that answer nothing on their own.
var fillers = map[string]bool{"um": true, "umm": true, "uh": true, "uhh": true, "uhm": true, "hmm": true, "hm": true, "er": true, "erm": true, "ah": true, "eh": true, "mm": true, "嗯": true, "呃": true, "啊": true, "额": true, "那个": true, "就是": true, "这个": true}

// fillerOnly reports whether text is nothing but fillers.
func fillerOnly(text string) bool {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		if !fillers[f] {
			return false
		}
	}
	return true
}

// negativeWords open a "no"-class input: "no", "don't …", "stop", "cancel".
var negativeWords = map[string]bool{"no": true, "nope": true, "nah": true, "don't": true, "dont": true, "do not": true, "stop": true, "cancel": true, "never": true, "deny": true, "decline": true, "reject": true, "wait": true, "hold": true, "negative": true}

// negativeInput reports whether the user's words are a "no": the
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
// the way the orchestrator logs its own. Caller holds s.mu.
func (s *Supervisor) railBlockedLocked(p state.PendingAction, keys []string, via, reason string) {
	s.log.Log(logSource, "brain.rail_blocked", p.PaneID, map[string]any{"agent": s.aliasOf(p.PaneID), "keys": keys, "trigger": via, "reason": reason, "id": p.ID})
}

// AnswerTap answers the open question with the option the user
// tapped (POST /api/pending/answer): option is 1-based, as in
// PendingOptions. The tap is recorded as a user input first, so it
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
	alias := s.sayOf(p.PaneID)
	res, sent := s.answerKeys(ctx, p, o.Keys, o.Label, func(l string) string { return toldLine(alias, l, zh) }, "tap", map[string]any{"option": option})
	if sent {
		in.used = true
	}
	s.mu.Unlock()
	if !sent {
		return res, ErrNotSent
	}
	s.push(p.PaneID, fmt.Sprintf("The user tapped %s on %s's question.", strings.TrimRight(o.Label, "."), alias), voice.UrgencySilent)
	if s.brain != nil {
		s.brain.Daemon(fmt.Sprintf("the user tapped %q on %s's question; %s", o.Label, alias, lowerFirst(res.Spoken)))
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
