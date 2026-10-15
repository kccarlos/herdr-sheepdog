package supervisor

import (
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// A re-relay right after a turn that gave the user nothing
// (2026-10-08 03:04:58). The user asked a question; the orchestrator's
// model call broke on a connection reset and the turn fell back to
// brainFailedLine. The voice did not say it: a second later it called
// to_supervisor again with the same words. That call had no new input
// behind it, so the gate ignored it with "Nothing was handed on: … the
// user has said nothing new" — true, and useless: the voice went
// quiet, and the user heard nothing for five minutes.
//
// The gate still stands on recorded input (docs/dev/roles.md "The one
// rail"): what is retried is the user's own claimed words, never the
// voice's argument, and only when nothing newer was said. So:
//
//   - a re-relay within rerelayWindow of a turn that fell back without
//     acting, on the newest input, retries that turn once over the same
//     recorded words (claimRetry; supervisor.relay {retry_of});
//   - any other re-relay with no new words gets the silent fact, now
//     with what became of the previous turn: it failed (the plain
//     reason, and that the retry was spent), or it answered and the
//     voice has said nothing since — the user has heard nothing yet
//     (noFreshWordsFact). An answer the voice spoke leaves the plain
//     fact: that is the self-relay the gate exists for.

// rerelayWindow is how long after a turn's outcome a re-relay counts as
// "right after" it: the incident's came 1 s after the fallback, and the
// user who has heard nothing for longer has likely moved on or will
// speak again.
const rerelayWindow = 30 * time.Second

// relayOutcome is what the newest delivered utterance turn did.
type relayOutcome struct {
	// ins are the inputs its words came from; id names the utterance.
	ins []*shepherdInput
	id  string
	at  time.Time
	// failed: the turn fell back (no answer); reason says why in plain
	// words (brain.FailureReason); acted: it sent something anyway, so
	// running it again could send it twice.
	failed bool
	reason string
	acted  bool
	// answer is the reply handed to the voice when it did answer.
	answer string
}

// noteOutcome records u's outcome as the newest one. Caller does not
// hold s.mu.
func (s *Supervisor) noteOutcome(u *inflightUtterance, out utteranceOutcome) {
	if s.brain == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(u.ins) == 0 {
		return
	}
	o := &relayOutcome{ins: u.ins, id: u.id, at: s.cfg.Now(), acted: out.acted}
	if out.ok {
		o.answer = strings.TrimSpace(out.res.Spoken)
	} else {
		o.failed = true
		o.reason = failureReason(out.err)
	}
	s.lastOutcome = o
}

// failureReason is a failed turn's reason in plain words.
func failureReason(err error) string {
	if err == nil {
		return "the orchestrator gave no answer"
	}
	return brain.FailureReason(err)
}

// outcomeForLastLocked is the newest outcome when it is about the
// newest input (nothing was said since) and within rerelayWindow; nil
// otherwise. Caller holds s.mu.
func (s *Supervisor) outcomeForLastLocked() *relayOutcome {
	o := s.lastOutcome
	last := s.lastInputLocked()
	if o == nil || last == nil || len(o.ins) == 0 || o.ins[len(o.ins)-1] != last {
		return nil
	}
	if s.cfg.Now().Sub(o.at) > rerelayWindow {
		return nil
	}
	return o
}

// claimRetry takes the newest input's words up again when the turn on
// them fell back without acting, just now, and was not retried yet: the
// re-relay is a retry of the user's own unanswered words. ok is false
// otherwise (the call stays ignored).
func (s *Supervisor) claimRetry() (claim, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.outcomeForLastLocked()
	if o == nil || !o.failed || o.acted {
		return claim{}, false
	}
	last := o.ins[len(o.ins)-1]
	if last.retried {
		return claim{}, false
	}
	last.retried = true
	c := s.claimRunLocked(o.ins)
	c.retryOf, c.retryReason = o.id, o.reason
	return c, true
}

// noFreshWordsFact is the silent result of a to_supervisor call with no
// new words of the user's: what happened, for the voice to know — and,
// right after a turn on the user's last words, what became of it.
func (s *Supervisor) noFreshWordsFact() string {
	s.mu.Lock()
	in := s.lastInputLocked()
	o := s.outcomeForLastLocked()
	spokeSince := o != nil && !s.voiceSpokeAt.Before(o.at)
	s.mu.Unlock()
	fact := "Nothing was handed on: the orchestrator acts only on the user's own words, and the user has said nothing new since your last call."
	if in != nil && strings.TrimSpace(in.text) != "" {
		fact += fmt.Sprintf(" Their last words, at %s, were %q.", in.at.Format("15:04:05"), strings.TrimSpace(in.text))
	}
	switch {
	case o == nil:
	case o.failed:
		fact += fmt.Sprintf(" The orchestrator's turn on those words failed at %s: %s.", o.at.Format("15:04:05"), o.reason)
		switch {
		case o.acted:
			fact += " It had already acted, so it is not run again."
		case len(o.ins) > 0 && o.ins[len(o.ins)-1].retried:
			fact += " It was already retried once."
		}
		if !spokeSince {
			fact += " You have said nothing since, so the user has heard no answer yet."
		}
		fact += " The orchestrator runs again on the user's next words."
	case !spokeSince && o.answer != "":
		fact += fmt.Sprintf(" The orchestrator answered those words at %s: %q. You have said nothing since, so the user has not heard that answer yet.", o.at.Format("15:04:05"), o.answer)
	}
	return fact
}
