package supervisor

import (
	"context"
	"fmt"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
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
	// stopPhaseInvestigation: a background look the withdrawn request
	// was about (or the most recent one, with nothing in flight) is
	// cancelled, so its answer never arrives.
	stopPhaseInvestigation = "investigation"
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
	// Whatever the shepherd just said is not relayed by the daemon after
	// a stop (relay.go): the stop tool, the app's stop button and the
	// bare word all come through here.
	s.toolCalled()
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
	var withdrawn stopOutcome
	s.mu.Lock()
	if u := s.utterance; u != nil {
		u.dropped = true
		s.utterance = nil
		phase, when := stopPhaseDropCall, "before anything was sent"
		if u.inBrain {
			phase, when = stopPhaseCancelTurn, "before it acted"
		}
		withdrawn = stopOutcome{phase: phase, what: fmt.Sprintf("your request %q was withdrawn %s", u.text, when)}
		cancel := u.cancel
		s.mu.Unlock()
		cancel()
		// Re-take the lock behind whatever the cancelled turn was in the
		// middle of: a send it had already started holds s.mu until its
		// pending action exists, so phase 4 below sees it.
		s.mu.Lock()
	}
	// The later phases take the lock themselves; the investigation phase
	// must not hold it while it stops a child process.
	s.mu.Unlock()

	// Phase 3: a background look is running. The one the withdrawn
	// request was about, or — with nothing in flight — the most recent.
	investigated, cancelled := s.stopInvestigation(withdrawn)

	s.mu.Lock()
	// Phase 4: an instruction went to a working agent a moment ago. This
	// runs even after a withdrawal — the turn may have sent something
	// before the cancel landed, and then "withdrawn before it acted"
	// would be a lie and the worker would carry on.
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

	if cancelled {
		s.mu.Unlock()
		return investigated
	}
	if withdrawn.phase != "" {
		s.mu.Unlock()
		return withdrawn
	}

	// Phase 5: a question stays a question. Stop is not "no".
	if has && p.Kind == state.PendingAnswerPrompt {
		alias := s.aliasOf(p.PaneID)
		s.mu.Unlock()
		return stopOutcome{phase: stopPhasePromptOpen, paneID: p.PaneID, alias: alias, what: alias + " is still waiting for your answer, so I left its question open"}
	}
	s.mu.Unlock()
	return stopOutcome{phase: stopPhaseNone, what: "nothing was in flight"}
}

// stopInvestigation cancels the running background look a stop applies
// to: the one about the agent the withdrawn request named, else — when
// nothing was in flight — the most recent one. A look the shepherd did
// not just withdraw a request about is left alone unless it is the only
// thing to stop.
func (s *Supervisor) stopInvestigation(withdrawn stopOutcome) (stopOutcome, bool) {
	var inv state.Investigation
	var ok bool
	if withdrawn.phase != "" {
		if paneID, found := s.investigationAbout(withdrawn.what); found {
			inv, ok = s.cancelInvestigationFor(paneID, "stop")
		}
	} else {
		inv, ok = s.cancelLatestInvestigation("stop")
	}
	if !ok {
		return stopOutcome{}, false
	}
	alias := aliasOr(inv.Alias)
	return stopOutcome{phase: stopPhaseInvestigation, paneID: inv.PaneID, alias: alias, what: "the look into " + alias + " was called off"}, true
}

// investigationAbout picks the running look the withdrawn words are
// about: the agent they name, else the one the conversation is on.
func (s *Supervisor) investigationAbout(text string) (string, bool) {
	lower := strings.ToLower(text)
	s.mu.Lock()
	defer s.mu.Unlock()
	for paneID, live := range s.investigating {
		if live.alias != "" && strings.Contains(lower, strings.ToLower(live.alias)) {
			return paneID, true
		}
	}
	if live, has := s.investigating[s.lastSubject]; has && live != nil {
		return s.lastSubject, true
	}
	return "", false
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
	res := toolResultFrom(rep, prose.EarBrief)
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
