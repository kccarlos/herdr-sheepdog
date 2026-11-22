package factory

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

// Change is one event on Engine.Changes() (design.md 12.3).
type Change struct {
	Project string `json:"project,omitempty"`
	Kind    string `json:"kind"`
	AskID   string `json:"ask_id,omitempty"`
	Task    string `json:"task,omitempty"`
	Seq     int64  `json:"seq,omitempty"`
}

// Changes returns a buffered stream emitting one Change per ask, answer or say.
func (e *Engine) Changes() <-chan Change {
	e.mu.Lock()
	defer e.mu.Unlock()
	ch := make(chan Change, 64)
	e.changeSubs = append(e.changeSubs, ch)
	return ch
}

func (e *Engine) commitChangeLocked(id string, s *flow.State, l LogLine, c Change) error {
	if err := e.commitLocked(id, s, l); err != nil {
		return err
	}
	c.Project, c.Seq = id, s.Seq
	for _, ch := range e.changeSubs {
		select {
		case ch <- c:
		default:
		}
	}
	return nil
}

// Asks returns asks across projects (open only when open is true) in creation order.
func (e *Engine) Asks(open bool) []flow.Ask {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []flow.Ask
	for _, ent := range e.projectDirs() {
		if s, err := LoadState(e.Paths.State(ent.Name())); err == nil {
			for _, a := range s.Asks {
				if !open || a.Status == flow.AskStatusOpen {
					a.Project, a.Folder, a.Options = cmp.Or(a.Project, ent.Name()), cmp.Or(a.Folder, s.Project.Folder), slices.Clone(a.Options)
					out = append(out, a)
				}
			}
		}
	}
	slices.SortStableFunc(out, func(a, b flow.Ask) int { return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.Project, b.Project)) })
	return out
}

// Answer records an owner's answer to askID, requiring a verified AnswerProof on gated asks.
func (e *Engine) Answer(askID, option string, proof AnswerProof) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	askID = strings.TrimSpace(askID)
	for _, ent := range e.projectDirs() {
		if s, err := LoadState(e.Paths.State(ent.Name())); err == nil && slices.ContainsFunc(s.Asks, func(a flow.Ask) bool { return a.ID == askID }) {
			return e.answerAskLocked(ent.Name(), &s, askID, option, "", owner, &proof)
		}
	}
	return e.refuse("", "not_found", fmt.Sprintf("No ask %q exists.", askID))
}

// Say queues the owner's verbatim words as a coordinator wake on project.
func (e *Engine) Say(project, text string) error {
	project, msg := strings.TrimSpace(project), strings.TrimSpace(text)
	ctx, cancel := context.WithTimeout(context.Background(), lockGitTimeout)
	defer cancel()
	e.mu.Lock()
	defer e.mu.Unlock()
	id, s, err := e.findProjectLocked(ctx, project)
	if project == "" || err != nil {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, project)
	}
	if msg == "" {
		return e.refuse(id, "why_missing", "Say requires non-empty text.")
	}
	s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: fmt.Sprintf("owner_say:%d", s.Seq+1), Why: "owner said", Facts: msg, At: e.now()})
	return e.commitChangeLocked(id, &s, LogLine{By: owner, Input: map[string]string{"verb": "say", "text": msg}, Rule: "say", Asked: []string{flow.AskWake}, Decision: "owner said", Why: msg}, Change{Kind: "say"})
}

func (e *Engine) acceptAskLocked(id string, s *flow.State, role string, ti int, args json.RawMessage) error {
	var req struct {
		Kind, Task, Question, Recommend string
		Options                         []string
	}
	if len(bytes.TrimSpace(args)) == 0 || decodeStrict(args, &req) != nil {
		return e.refuse(id, "invalid_report", "An ask must carry kind, question, options and recommend.")
	}
	ask, code, why := flow.ValidateAsk(flow.Ask{Kind: req.Kind, Task: req.Task, Question: req.Question, Options: req.Options, Recommend: req.Recommend})
	if code != "" {
		return e.refuse(id, code, why)
	}
	if role != flow.RoleCoordinator && ask.Gated {
		return e.refuse(id, "kind_not_allowed", fmt.Sprintf("A %s may open only decision asks, not %q.", role, ask.Kind))
	}
	ask.ID, ask.Project, ask.Folder, ask.AskedBy, ask.RunID = fmt.Sprintf("ask-%s-%d", id, s.Seq+1), id, s.Project.Folder, role, s.Project.RunID
	if ti >= 0 {
		ask.Task = s.Tasks[ti].ID
	}
	if a := openTaskAttempt(s.Tasks, ask.Task); a != nil {
		ask.Head = flow.H(*a)
		if role == flow.RoleWorker {
			ask.RunID = a.RunID
		} else if role == flow.RoleReviewer && a.Review != nil {
			ask.RunID = a.Review.RunID
		}
	}
	now := e.now()
	ask, row, dec := flow.StepAsk(ask, flow.AskEvent{Kind: flow.EvAskOpen}, now)
	s.Asks = append(s.Asks, ask)
	if slices.Contains(dec.Asked, flow.AskWake) {
		facts := fmt.Sprintf("options [%s]; recommend %q; run `%s factory answer %s \"<option>\" --why \"<why>\"`", strings.Join(ask.Options, " | "), ask.Recommend, cmp.Or(e.Bin, "sheepdog"), ask.ID)
		s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: "ask:" + ask.ID, Task: ask.Task, Why: fmt.Sprintf("%s: %s asked %q (%s)", row, ask.AskedBy, ask.Question, ask.ID), Facts: facts, At: now})
	}
	return e.commitChangeLocked(id, s, LogLine{By: role, Task: ask.Task, Input: map[string]any{"verb": "ask", "id": ask.ID, "kind": ask.Kind, "question": ask.Question, "options": ask.Options, "recommend": ask.Recommend}, Rule: row, Asked: dec.Asked, Decision: "ask opened: " + ask.ID, Why: ask.Question}, Change{Kind: flow.EvAskOpen, AskID: ask.ID, Task: ask.Task})
}

func (e *Engine) acceptAnswerLocked(id string, s *flow.State, role string, args json.RawMessage) error {
	var req struct{ Ask, Option, Why string }
	if len(bytes.TrimSpace(args)) == 0 || decodeStrict(args, &req) != nil || strings.TrimSpace(req.Ask) == "" || strings.TrimSpace(req.Option) == "" {
		return e.refuse(id, "invalid_report", "An answer must carry ask and option.")
	}
	if why := flow.CleanText(req.Why); len([]rune(why)) > maxRejectWhyRunes {
		return e.refuse(id, "invalid_report", "the reason is longer than 500 characters; put details in a file and name it")
	} else {
		return e.answerAskLocked(id, s, strings.TrimSpace(req.Ask), req.Option, why, role, nil)
	}
}

func (e *Engine) answerAskLocked(id string, s *flow.State, askID, option, why, by string, proof *AnswerProof) error {
	ai := slices.IndexFunc(s.Asks, func(a flow.Ask) bool { return a.ID == askID })
	if ai < 0 {
		return e.refuse(id, "not_found", fmt.Sprintf("No ask %q exists.", askID))
	}
	a, now := s.Asks[ai], e.now()
	resolved, ok := flow.ResolveAskOption(a.Options, option)
	switch {
	case a.Status != flow.AskStatusOpen:
		return e.refuse(id, flow.RefuseClosed, fmt.Sprintf("Ask %q is already %s.", a.ID, a.Status))
	case proof == nil && a.Gated:
		return e.refuse(id, "proof_required", fmt.Sprintf("Ask %q (%s) requires owner AnswerProof.", a.ID, a.Kind))
	case !ok:
		return e.refuse(id, "invalid_option", fmt.Sprintf("%q is not one of ask %q's options.", option, a.ID))
	}
	ev := flow.AskEvent{Kind: flow.EvAskAnswer, Answer: resolved, Why: why, By: by}
	if proof != nil {
		if a.Gated || *proof != (AnswerProof{}) {
			if code, why := e.verifyProofLocked(a.ID, resolved, flow.CleanText(option), *proof, now); code != "" {
				return e.refuse(id, code, why)
			}
		}
		ev.ProofID, ev.DeviceID = proof.ID, proof.DeviceID
	}
	next, row, dec := flow.StepAsk(a, ev, now)
	s.Asks[ai] = next
	msg := fmt.Sprintf("Answer to %s: %s", next.ID, next.Answer) + map[bool]string{true: " — " + next.Why}[next.Why != ""]
	if slices.Contains(dec.Asked, flow.AskDeliverAnswer) {
		if att := openTaskAttempt(s.Tasks, next.Task); att != nil {
			ctx, cancel := context.WithTimeout(context.Background(), lockGitTimeout)
			defer cancel()
			var err error
			if next.AskedBy == flow.RoleReviewer && att.Review != nil && att.Review.RunID != "" && e.HC != nil {
				err = world.SendRun(ctx, e.HC, att.Review.RunID, msg)
			} else if err = e.deliverWorkerLocked(ctx, id, next.Task, s, att, msg, false); errors.Is(err, errNoWorkerRun) {
				att.Held, err = appendHeld(att.Held, msg), nil
			}
			if err != nil {
				return err
			}
		}
	} else if slices.Contains(dec.Asked, flow.AskWake) {
		s.Project.Wakes = append(s.Project.Wakes, flow.Wake{Key: "ask_answer:" + next.ID, Task: next.Task, Why: fmt.Sprintf("%s: %s answered %s (%s)", row, next.AnsweredBy, next.ID, next.Kind), Facts: msg, At: now})
	}
	input := map[string]any{"verb": "answer", "ask": next.ID, "option": next.Answer, "why": next.Why, "proof_id": next.ProofID, "device_id": next.DeviceID}
	return e.commitChangeLocked(id, s, LogLine{By: next.AnsweredBy, Task: next.Task, Input: input, Rule: row, Asked: dec.Asked, Decision: "answered " + next.ID, Why: next.Answer}, Change{Kind: "ask_answer", AskID: next.ID, Task: next.Task})
}

func openTaskAttempt(tasks []flow.Task, taskID string) *flow.Attempt {
	if i := slices.IndexFunc(tasks, func(t flow.Task) bool { return taskID != "" && t.ID == taskID }); i >= 0 {
		return flow.OpenAttempt(tasks[i])
	}
	return nil
}
