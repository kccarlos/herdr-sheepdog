package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The one rail reads the shepherd's real input (docs/roles.md "The one
// rail"). The voice model's to_supervisor argument is only what the model
// chose to relay: on 2026-09-23 it relayed a push back as if the shepherd
// had said it, then called to_supervisor {text: "yes"} on its own while
// the shepherd's microphone was muted. So the daemon records what the
// shepherd actually said, typed or tapped — the voice's input
// transcription, typed text, a tap on the pending question — and
//
//   - a to_supervisor call with no shepherd input newer than both the
//     previous accepted call and the last spoken push is the model's own
//     initiative: no fast path, no brain turn, nothing said
//     (supervisor.voice_initiative_ignored);
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if last := s.lastInputLocked(); last != nil && last.open {
		s.inputSeq++
		last.text += text
		last.seq, last.at, last.open = s.inputSeq, s.cfg.Now(), !final
		if last.claimed {
			// The input transcription trails the tool call: these words
			// are the relay already made, not fresh input for another.
			s.relayedSeq = s.inputSeq
		}
		s.wakeLocked()
		return
	}
	s.addInputLocked(&shepherdInput{kind: InputVoice, text: text, open: !final})
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

// wakeLocked releases a call waiting out Config.InputGrace. Caller holds
// s.mu.
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

// markPushed records that a push was just spoken: an input older than it
// cannot carry a later to_supervisor call.
func (s *Supervisor) markPushed() {
	s.mu.Lock()
	s.pushSeq = s.inputSeq
	s.mu.Unlock()
}

// claimInput is the to_supervisor gate: the newest shepherd input newer
// than both the previous accepted call and the last spoken push, waiting
// up to Config.InputGrace for one (the input transcription can trail the
// tool call). ok is false when none arrived: the call is the voice
// model's own initiative.
func (s *Supervisor) claimInput(ctx context.Context) (*shepherdInput, bool) {
	var deadline <-chan time.Time
	for {
		s.mu.Lock()
		if last := s.lastInputLocked(); last != nil && last.seq > s.relayedSeq && last.seq > s.pushSeq {
			s.relayedSeq = s.inputSeq
			// Fragments still arriving for this turn stay with this call
			// (heard); the next turn is a new input.
			last.claimed = true
			s.mu.Unlock()
			return last, true
		}
		wake := s.inputWake
		s.mu.Unlock()
		if s.cfg.InputGrace <= 0 {
			return nil, false
		}
		if deadline == nil {
			t := time.NewTimer(s.cfg.InputGrace)
			defer t.Stop()
			deadline = t.C
		}
		select {
		case <-wake:
		case <-deadline:
			return nil, false
		case <-ctx.Done():
			return nil, false
		}
	}
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
// executor (the post-answer poll included), an expired pending action
// whose prompt is still on screen is revived first, and the voice gets a
// silent line saying what was tapped. id must name the open question
// (or the one that just expired): anything else is ErrPendingGone.
func (s *Supervisor) AnswerTap(ctx context.Context, id string, option int) (voice.ToolResult, error) {
	s.mu.Lock()
	p, has := s.store.Pending()
	if !has || p.ID != id || p.Kind != state.PendingAnswerPrompt || !s.cfg.Now().Before(p.ExpiresAt) {
		// Expired (or past its TTL, not yet swept) unanswered: revive it
		// from the screen, but only when the screen still shows the same
		// prompt — a tap is about the question the shepherd saw.
		var pane string
		switch {
		case has && p.ID == id && p.Kind == state.PendingAnswerPrompt:
			pane = p.PaneID
		case !has && s.expired != nil && s.expired.ID == id:
			pane = s.expired.PaneID
		default:
			s.mu.Unlock()
			return voice.ToolResult{}, ErrPendingGone
		}
		before := s.lastPrompt[pane]
		revived, ok := s.revivePrompt(ctx, pane, "tap")
		if !ok || s.lastPrompt[pane] != before {
			s.mu.Unlock()
			return voice.ToolResult{}, ErrPendingGone
		}
		p = revived
	}
	if option < 1 || option > len(p.Options) {
		s.mu.Unlock()
		return voice.ToolResult{}, fmt.Errorf("%w: %d (the question has %d)", ErrNoSuchOption, option, len(p.Options))
	}
	o := p.Options[option-1]
	s.closeVoiceLocked()
	s.addInputLocked(&shepherdInput{kind: InputTap, text: o.Label})
	// The tap is answered here and now: it is not fresh input for a
	// later to_supervisor call (the voice relaying the silent line below
	// back, say).
	s.relayedSeq = s.inputSeq
	in, reason := s.railInputLocked(p, o.Keys)
	if reason != "" {
		s.railBlockedLocked(p, o.Keys, "tap", reason)
		s.mu.Unlock()
		return voice.ToolResult{}, errors.New(reason)
	}
	alias := s.aliasOf(p.PaneID)
	res, sent := s.answerKeys(ctx, p, o.Keys, o.Label, "Told "+alias+" %s.", "tap", map[string]any{"option": option})
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
		out = append(out, PendingOption{Index: i + 1, Label: o.Label, Keys: append([]string{}, o.Keys...), Default: o.Default})
	}
	return out
}
