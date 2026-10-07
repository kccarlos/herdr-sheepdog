package supervisor

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The stop procedure (docs/roles.md "Stop"). "Stop" means something
// different per phase and the daemon knows the phase from state it
// already has, so no model decides *what* to stop: the phases below are
// resolved in order, the first that applies is carried out, and the
// daemon's own sentence says what was stopped at once — no model turn
// stands between the stop and its acknowledgement (an orchestrator turn here
// once held the reply 5.3 s behind a Vertex 504, 2026-10-02). What was
// stopped goes into the orchestrator's transcript as a context line, so later
// questions know about it. Event turns (announce) are not cancelled by a
// stop.

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
	// stopPhaseDropCall: the relayed words had not reached the orchestrator.
	stopPhaseDropCall = "drop_call"
	// stopPhaseCancelTurn: an orchestrator user turn was in flight; its context
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
	// leaves open — stop is not "no", the user still decides.
	stopPhasePromptOpen = "prompt_open"
	// stopPhaseNone: nothing was in flight.
	stopPhaseNone = "none"
)

// stopOutcome is what the procedure resolved and did.
type stopOutcome struct {
	phase string
	// what is the sentence fragment naming what was stopped, for the
	// log, the orchestrator's transcript line and the code sentence.
	what   string
	paneID string
	alias  string
	// failed is set when the interrupt phase could not send its Ctrl-C.
	failed bool
	// queued is the instruction the interrupt phase's Ctrl-C followed
	// when it went to an agent already busy with a turn of its own:
	// typed into the running turn, it may stay in the worker's history
	// (queuedLine).
	queued string
}

// Stop runs the stop procedure and says what it stopped. trigger is one
// of the StopTrigger constants.
func (s *Supervisor) Stop(ctx context.Context, trigger string) (voice.ToolResult, error) {
	// Whatever the user just said is not relayed by the daemon after
	// a stop (relay.go): the stop tool, the app's stop button and the
	// bare word all come through here.
	s.toolCalled()
	out := s.resolveStop(ctx)
	stopped := map[string]any{"phase": out.phase, "what": out.what, "trigger": trigger}
	if out.queued != "" {
		stopped["queued"] = out.queued
	}
	s.log.Log(logSource, "supervisor.stopped", out.paneID, stopped)
	if s.brain != nil {
		// A context line, not a turn: no model call.
		s.brain.Daemon("the user said stop: " + out.what)
		if out.queued != "" {
			s.brain.Daemon(queuedLine(out))
		}
	}
	if trigger == StopTriggerTool {
		s.relayStopRest()
	}
	return voice.ToolResult{Spoken: stopSentence(out), Scheduling: voice.UrgencyInterrupt}, nil
}

// stopRestWindow bounds how old the words around a stop() may be for
// the daemon to relay what followed the stop word.
const stopRestWindow = 10 * time.Second

// relayStopRest hands the orchestrator what the user said after the stop
// word. The #113 echo runs: "Stop. Stop. Is anything waiting for me?"
// went to the voice's stop() tool, the answer was "Nothing was in
// flight.", and the question was never answered — stop() carries no
// text, and the tool call kept the daemon from relaying the words. The
// stop has run by now (and the orchestrator has its context line); the rest of
// the words go through the same path as a relay, and the reply is pushed
// as a late reply is. Words that only repeat the stop ("Stop, stop
// everything") leave nothing to relay.
func (s *Supervisor) relayStopRest() {
	s.mu.Lock()
	last := s.lastInputLocked()
	if last == nil || last.kind != InputVoice || last.claimed || last.used || s.cfg.Now().Sub(last.at) > stopRestWindow {
		s.mu.Unlock()
		return
	}
	rest := stopRest(last.text)
	if rest == "" {
		s.mu.Unlock()
		return
	}
	c := s.claimRunLocked([]*shepherdInput{last})
	c.in.open = false
	c.text = rest
	s.replies.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.replies.Done()
		s.log.Log(logSource, "supervisor.stop_rest", "", map[string]any{"text": rest, "heard": last.text, "utterance_id": c.id})
		res := s.handleUtterance(s.life, c, "", true)
		if res.Spoken == "" {
			return
		}
		s.pushTagged("", res.Spoken, res.Scheduling, map[string]any{"utterance_id": c.id})
	}()
}

// stopLead is the stop words an utterance opens with, repeated: "Stop.
// Stop.", "Wait, no,", "停下，等一下，".
var stopLead = regexp.MustCompile(`(?i)^(\s*((stop( it| that| now)?|wait|cancel|hold on|hang on|no|okay|ok)\b|停下|停止|停|等等|等一下|取消|别说了)[\s,.!;:，。！；：、…-]*)+`)

// stopRest is what text says after its opening stop words, or "" when
// that is nothing more than another word or two of the stop.
func stopRest(text string) string {
	lead := stopLead.FindString(text)
	if strings.TrimSpace(lead) == "" {
		return ""
	}
	rest := strings.TrimSpace(text[len(lead):])
	if rest == "" || fillerOnly(rest) || bareStop(rest) {
		return ""
	}
	if !strings.ContainsAny(rest, "?？") && spokenLength(rest) < 3 {
		return ""
	}
	return rest
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
			alias := aliasOr(state.Say(row))
			out := stopOutcome{phase: stopPhaseInterrupt, paneID: p.PaneID, alias: alias, what: "an interrupt (Ctrl-C) was sent to " + alias}
			if err := s.herdr.Interrupt(ctx, p.PaneID); err != nil {
				s.log.Log(logSource, "supervisor.cancel_error", p.PaneID, map[string]any{"id": p.ID, "error": err.Error(), "via": "stop"})
				out.what, out.failed = "I could not interrupt "+alias, true
			} else {
				// The next working -> done is the Ctrl-C, not a finish.
				s.interrupted[p.PaneID] = s.cfg.Now()
				if row.StateSince.Before(p.CreatedAt) {
					// Busy before the instruction went in: it was queued
					// into the running turn.
					out.queued = withoutSignOff(p.PayloadText)
				}
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
		alias := s.sayOf(p.PaneID)
		s.mu.Unlock()
		return stopOutcome{phase: stopPhasePromptOpen, paneID: p.PaneID, alias: alias, what: alias + " is still waiting for your answer, so I left its question open"}
	}
	s.mu.Unlock()
	return stopOutcome{phase: stopPhaseNone, what: "nothing was in flight"}
}

// stopInvestigation cancels the running background look a stop applies
// to: the one about the agent the withdrawn request named, else — when
// nothing was in flight — the most recent one. A look the user did
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
	alias := s.sayPane(inv.PaneID, inv.Alias)
	return stopOutcome{phase: stopPhaseInvestigation, paneID: inv.PaneID, alias: alias, what: "the look into " + alias + " was called off"}, true
}

// investigationAbout picks the running look the withdrawn words are
// about: the agent they name, else the one the conversation is on.
func (s *Supervisor) investigationAbout(text string) (string, bool) {
	lower := strings.ToLower(text)
	s.mu.Lock()
	defer s.mu.Unlock()
	for paneID, live := range s.investigating {
		if live.alias != "" && strings.Contains(lower, strings.ToLower(live.alias)) || strings.Contains(lower, strings.ToLower(s.sayOf(paneID))) {
			return paneID, true
		}
	}
	if live, has := s.investigating[s.lastSubject]; has && live != nil {
		return s.lastSubject, true
	}
	return "", false
}

// queuedLine is the fact the orchestrator gets after a stop interrupted an agent
// that had an instruction queued (tier-3 bench 2026-10-03 #8): Claude
// Code folds a message typed into a busy session into its running turn,
// and Ctrl-C stops both but leaves the request in its conversation, so
// the next instruction may revive it. Nothing is sent to the worker:
// whether to tell it to drop the request is the user's call.
func queuedLine(out stopOutcome) string {
	return fmt.Sprintf("the instruction %q was typed into %s's running turn before the interrupt, so it may still be in %s's history and come back with its next instruction; nothing was sent to drop it", out.queued, out.alias, out.alias)
}

// stopSentence is the stop's acknowledgement, short for the ear. The
// interrupt phase says only that Ctrl-C was sent: that does not
// guarantee a queued instruction was dropped.
func stopSentence(out stopOutcome) string {
	switch out.phase {
	case stopPhaseInterrupt:
		if out.failed {
			return out.what + "."
		}
		return "Sent an interrupt to " + out.alias + "."
	case stopPhaseNone:
		return "Nothing was in flight."
	case stopPhasePromptOpen:
		return "Nothing to stop; " + out.what + "."
	default:
		return "Stopped: " + out.what + "."
	}
}
