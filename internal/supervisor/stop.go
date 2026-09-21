package supervisor

import (
	"context"
	"fmt"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The stop procedure (docs/roles.md "Stop"). "Stop" means something
// different per phase and the daemon knows the phase from state it
// already has, so no model decides *what* to stop: the phases below are
// resolved in order, the first that applies is carried out, and only
// then does one short read-only brain turn say what was stopped. Event
// turns (announce) are not cancelled by a stop.

// Stop triggers, as logged in supervisor.stopped.
const (
	// StopTriggerTool is the voice's stop() tool.
	StopTriggerTool = "stop_tool"
	// StopTriggerBareWord is the code fast path: the whole utterance
	// was "stop" / "wait" / "cancel" / "no no", so no model ran at all.
	StopTriggerBareWord = "bare_word"
)

// The phases, as logged in supervisor.stopped.
const (
	// stopPhaseDropCall: the relayed words had not reached the brain.
	stopPhaseDropCall = "drop_call"
	// stopPhaseCancelTurn: a brain CEO turn was in flight; its context
	// is cancelled, so its write tools never execute.
	stopPhaseCancelTurn = "cancel_turn"
	// stopPhaseInterrupt: an instruction went to a working agent within
	// the cancel window, so it gets a Ctrl-C.
	stopPhaseInterrupt = "interrupt"
	// stopPhasePromptOpen: an agent is waiting at a prompt, which stop
	// leaves open — stop is not "no", the CEO still decides.
	stopPhasePromptOpen = "prompt_open"
	// stopPhaseNone: nothing was in flight.
	stopPhaseNone = "none"
)

// stopOutcome is what the procedure resolved and did.
type stopOutcome struct {
	phase string
	// what is the sentence fragment naming what was stopped, for the
	// log, the brain's request and the code sentence.
	what   string
	paneID string
	alias  string
}

// Stop runs the stop procedure and says what it stopped. trigger is one
// of the StopTrigger constants.
func (s *Supervisor) Stop(ctx context.Context, trigger string) (voice.ToolResult, error) {
	out := s.resolveStop(ctx)
	s.log.Log(logSource, "supervisor.stopped", out.paneID, map[string]any{"phase": out.phase, "what": out.what, "trigger": trigger})
	if s.brain != nil {
		s.brain.Daemon("the CEO said stop: " + out.what)
	}
	if res, ok := s.stopTurn(ctx, out); ok {
		return res, nil
	}
	return voice.ToolResult{Spoken: stopSentence(out), Scheduling: voice.UrgencyInterrupt}, nil
}

// resolveStop works out the phase from state and carries it out.
func (s *Supervisor) resolveStop(ctx context.Context) stopOutcome {
	// Phases 1 and 2: the relayed words are in flight. Cancelling the
	// turn's context stops its write tools before they execute.
	s.mu.Lock()
	if u := s.utterance; u != nil {
		u.dropped = true
		s.utterance = nil
		phase, when := stopPhaseDropCall, "before anything was sent"
		if u.inBrain {
			phase, when = stopPhaseCancelTurn, "before it acted"
		}
		text := u.text
		s.mu.Unlock()
		u.cancel()
		return stopOutcome{phase: phase, what: fmt.Sprintf("your request %q was withdrawn %s", text, when)}
	}

	// Phase 3: an instruction went to a working agent a moment ago.
	p, has := s.store.Pending()
	if has && p.Kind == state.PendingSendInstruction && s.cfg.Now().Sub(p.CreatedAt) <= s.cfg.CancelWindow {
		if row, ok := s.store.Get(p.PaneID); ok && row.State == herdr.StateWorking {
			alias := aliasOr(row.Alias)
			out := stopOutcome{phase: stopPhaseInterrupt, paneID: p.PaneID, alias: alias, what: "the instruction to " + alias + " was interrupted"}
			if err := s.herdr.Interrupt(ctx, p.PaneID); err != nil {
				s.log.Log(logSource, "supervisor.cancel_error", p.PaneID, map[string]any{"id": p.ID, "error": err.Error(), "via": "stop"})
				out.what = "I could not interrupt " + alias
			} else {
				// The next working -> done is the Ctrl-C, not a finish.
				s.interrupted[p.PaneID] = s.cfg.Now()
			}
			s.clearPending(p, "stop")
			s.mu.Unlock()
			return out
		}
	}

	// Phase 4: a question stays a question. Stop is not "no".
	if has && p.Kind == state.PendingAnswerPrompt {
		alias := s.aliasOf(p.PaneID)
		s.mu.Unlock()
		return stopOutcome{phase: stopPhasePromptOpen, paneID: p.PaneID, alias: alias, what: alias + " is still waiting for your answer, so I left its question open"}
	}
	s.mu.Unlock()
	return stopOutcome{phase: stopPhaseNone, what: "nothing was in flight"}
}

// stopTurn runs the closing brain turn: read tools only, a short
// budget, and the CEO's own words triggered it, so it keeps the CEO
// priority of the turn lock. ok is false when there is no brain or it
// could not answer, and the code sentence is spoken instead.
func (s *Supervisor) stopTurn(ctx context.Context, out stopOutcome) (voice.ToolResult, bool) {
	if s.brain == nil {
		return voice.ToolResult{}, false
	}
	rep, err := s.brain.Turn(ctx, brain.Request{Kind: brain.KindStop, Trigger: brain.TriggerCEO, Agent: out.alias, Stopped: out.what})
	if err != nil {
		return voice.ToolResult{}, false
	}
	res := toolResultFrom(brain.KindStop, rep)
	if res.Spoken == "" {
		return voice.ToolResult{}, false
	}
	res.Scheduling = voice.UrgencyInterrupt
	return res, true
}

// stopSentence is what the daemon says with no brain to phrase it.
func stopSentence(out stopOutcome) string {
	switch out.phase {
	case stopPhaseNone:
		return "Nothing was in flight."
	case stopPhasePromptOpen:
		return "Nothing to stop; " + out.what + "."
	default:
		return "Stopped: " + out.what + "."
	}
}
