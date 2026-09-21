package supervisor

import (
	"context"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// blocked -> blocked is a block (docs/issues-2026-09-18.md, docs/roles.md
// "Not yet"): herdr reports a pane's status only on change, so a worker
// that answers one approval prompt and is already showing the next
// within herdr's own poll window never leaves `blocked` — there is no
// transition for the shepherd to hear about. Two mechanisms close the
// gap, both ending the same way: a synthetic blocked -> blocked
// herdr.Transition through the normal HandleTransition path, so the new
// prompt gets a pending action and a push exactly like a real block
// (source tells the two apart: "reprompt" or "reconcile").
//
//   - pollReprompt watches one pane directly for Config.RepromptPoll
//     after keys go to it (tools.go's answerWithKeys): the next prompt,
//     if there is one, is usually seconds away.
//   - reconcileReprompts sweeps every blocked pane with nothing pending
//     and an already-answered (or never recorded) question, on every
//     periodic snapshot and once at startup (Run) — this also catches a
//     prompt that appeared while the daemon was down, which no poll ever
//     ran for.
//
// Both compare a freshly parsed prompt against s.lastPrompt[paneID], the
// signature of the prompt behind the pane's last answer_prompt pending
// action (set in newAnswerPrompt, the one place every such action is
// built, real block or synthetic): unchanged means nothing new to say,
// whichever mechanism is asking.

// defaultRepromptPollInterval is the poll's cadence
// (docs/issues-2026-09-18.md: 1 s). Config.RepromptPollInterval exists
// only so tests can shrink it; production always gets this value.
const defaultRepromptPollInterval = time.Second

// startReprompt launches the post-answer poll for paneID in the
// background. answerWithKeys calls it right after keys went to a blocked
// pane, whichever path sent them (the brain's send_keys, the bare-answer
// fast path, or confirm). Detached from the caller's context, which ends
// long before the poll should (a web request, a brain turn, a voice tool
// call).
func (s *Supervisor) startReprompt(paneID string) {
	if s.cfg.RepromptPoll <= 0 {
		return
	}
	go s.pollReprompt(paneID)
}

// pollReprompt is the post-answer poll: every Config.RepromptPollInterval,
// for up to Config.RepromptPoll, read the pane and compare what it is
// asking now against s.lastPrompt[paneID] (the prompt just answered,
// unless something else has already raised a newer one). It stops at the
// first tick that finds the pane no longer blocked or a genuinely new
// prompt (which it raises); a tick that finds the same prompt, no
// prompt, or a failed read keeps waiting.
func (s *Supervisor) pollReprompt(paneID string) {
	interval := s.cfg.RepromptPollInterval
	maxPolls := int(s.cfg.RepromptPoll / interval)
	start := time.Now()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	polls, outcome, sawSamePrompt := 0, "timeout", false
	for polls < maxPolls {
		<-ticker.C
		polls++
		row, ok := s.store.Get(paneID)
		if !ok || row.State != herdr.StateBlocked {
			outcome = "left_blocked"
			break
		}
		read, err := s.herdr.Read(context.Background(), paneID, herdr.SourceRecentUnwrapped, s.cfg.ReadLines)
		if err != nil {
			continue
		}
		prompt, ok := herdr.ParsePrompt(read.Text)
		if !ok {
			continue
		}
		s.mu.Lock()
		unchanged := promptSignature(prompt) == s.lastPrompt[paneID]
		s.mu.Unlock()
		if unchanged {
			sawSamePrompt = true
			continue
		}
		s.raiseReprompt(context.Background(), row, prompt, "reprompt", time.Since(start))
		outcome = "raised"
		break
	}
	if outcome == "timeout" && sawSamePrompt {
		outcome = "same_prompt"
	}
	s.log.Log(logSource, "supervisor.reprompt_poll", paneID, map[string]any{"polls": polls, "outcome": outcome})
}

// reconcileReprompts sweeps every blocked pane with nothing pending and
// an already-answered (or never recorded) question: a prompt the
// post-answer poll's window closed on, or one that appeared while the
// daemon was down, which no poll ever ran for. Called once at startup
// (Run, after the roster is loaded from the state file) and on every
// Config.SnapshotInterval afterwards.
func (s *Supervisor) reconcileReprompts(ctx context.Context) {
	for _, row := range s.store.List() {
		if row.State != herdr.StateBlocked {
			continue
		}
		if row.LastBlock != nil && !row.LastBlock.Answered {
			continue
		}
		if p, has := s.store.Pending(); has && p.PaneID == row.PaneID {
			continue
		}
		read, err := s.herdr.Read(ctx, row.PaneID, herdr.SourceRecentUnwrapped, s.cfg.ReadLines)
		if err != nil {
			s.log.Log(logSource, "supervisor.reconcile_error", row.PaneID, map[string]any{"error": err.Error()})
			continue
		}
		prompt, ok := herdr.ParsePrompt(read.Text)
		if !ok {
			continue
		}
		s.mu.Lock()
		unchanged := promptSignature(prompt) == s.lastPrompt[row.PaneID]
		s.mu.Unlock()
		if unchanged {
			continue
		}
		s.raiseReprompt(ctx, row, prompt, "reconcile", 0)
	}
}

// raiseReprompt hands a newly observed prompt to HandleTransition as a
// synthetic blocked -> blocked transition: from herdr's point of view
// nothing changed, so this is the only way the new question reaches a
// pending action and a push. after is how long since the answer that
// started the poll (0 from the reconciler, which has no such anchor).
func (s *Supervisor) raiseReprompt(ctx context.Context, row state.Agent, prompt herdr.Prompt, source string, after time.Duration) {
	s.log.Log(logSource, "supervisor.reprompt", row.PaneID, map[string]any{
		"after_ms": after.Milliseconds(), "question": prompt.Phrase(), "source": source,
	})
	t := herdr.Transition{
		PaneID: row.PaneID, From: herdr.StateBlocked, To: herdr.StateBlocked, At: s.cfg.Now(),
		Agent: row.Kind, Name: row.Name, WorkspaceID: row.Workspace, WorkspaceLabel: row.WorkspaceLabel, Source: source,
	}
	s.log.Log(logSource, "herdr.state", row.PaneID, map[string]any{"state": herdr.StateBlocked, "source": source, "agent": row.Kind})
	s.log.Log(logSource, "herdr.transition", row.PaneID, t)
	s.HandleTransition(ctx, t)
}

// promptSignature is what tells one parsed prompt from another for this
// feature: the question, the command it is about (Codex's "$ …" line;
// empty for agents that do not show one) and each option's label, in
// order — never the spoken summary, which the daemon composes fresh
// every time and would never compare equal.
func promptSignature(p herdr.Prompt) string {
	labels := make([]string, len(p.Options))
	for i, o := range p.Options {
		labels[i] = o.Label
	}
	return p.Phrase() + "\x00" + p.Command + "\x00" + strings.Join(labels, "\x00")
}
