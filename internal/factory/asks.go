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
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
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
	e.eachStateLocked(func(id string, s flow.State) bool {
		for _, a := range s.Asks {
			if !open || a.Status == flow.AskStatusOpen {
				a.Project, a.Folder, a.Options = cmp.Or(a.Project, id), cmp.Or(a.Folder, s.Project.Folder), slices.Clone(a.Options)
				out = append(out, a)
			}
		}
		return true
	})
	slices.SortStableFunc(out, func(a, b flow.Ask) int { return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.Project, b.Project)) })
	return out
}

// Answer records an owner's answer to askID, requiring a verified AnswerProof on gated asks.
func (e *Engine) Answer(askID, option string, proof AnswerProof) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	askID = strings.TrimSpace(askID)
	err := e.refuse("", "not_found", fmt.Sprintf("No ask %q exists.", askID))
	e.eachStateLocked(func(id string, s flow.State) bool {
		if ai := slices.IndexFunc(s.Asks, func(a flow.Ask) bool { return a.ID == askID }); ai >= 0 {
			err = e.answerAskLocked(id, &s, ai, option, "", owner, proof)
			return false
		}
		return true
	})
	return err
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
	holdProjectWake(&s, fmt.Sprintf("owner_say:%d", s.Seq+1), "owner said", msg, e.now())
	return e.commitChangeLocked(id, &s, LogLine{By: owner, Input: map[string]string{"verb": "say", "text": msg}, Rule: "say", Asked: []string{flow.AskWake}, Decision: "owner said", Why: msg}, Change{Kind: "say"})
}

func (e *Engine) acceptAskLocked(id string, s *flow.State, role string, ti int, runID string, args json.RawMessage) error {
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
	ask.ID, ask.Project, ask.Folder, ask.AskedBy, ask.RunID = fmt.Sprintf("ask-%s-%d", id, s.Seq+1), id, s.Project.Folder, role, runID
	if ti >= 0 {
		ask.Task = s.Tasks[ti].ID
	}
	if a := openTaskAttempt(s.Tasks, ask.Task); a != nil {
		ask.Head = flow.H(*a)
	}
	now := e.now()
	ask, row, dec := flow.StepAsk(ask, flow.AskEvent{Kind: flow.EvAskOpen}, now)
	s.Asks = append(s.Asks, ask)
	if slices.Contains(dec.Asked, flow.AskWake) {
		facts := fmt.Sprintf("options [%s]; recommend %q; run `%s factory answer %s \"<option>\" --why \"<why>\"`", strings.Join(ask.Options, " | "), ask.Recommend, cmp.Or(e.Bin, "sheepdog"), ask.ID)
		appendWake(s, ask.Task, "ask:"+ask.ID, fmt.Sprintf("%s: %s asked %q (%s)", row, ask.AskedBy, ask.Question, ask.ID), facts, now)
	}
	return e.commitChangeLocked(id, s, LogLine{By: role, Task: ask.Task, Input: map[string]any{"verb": "ask", "id": ask.ID, "kind": ask.Kind, "question": ask.Question, "options": ask.Options, "recommend": ask.Recommend}, Rule: row, Asked: dec.Asked, Decision: "ask opened: " + ask.ID, Why: ask.Question}, Change{Kind: flow.EvAskOpen, AskID: ask.ID, Task: ask.Task})
}

func (e *Engine) acceptAnswerLocked(id string, s *flow.State, role string, args json.RawMessage) error {
	var req struct{ Ask, Option, Why string }
	if len(bytes.TrimSpace(args)) == 0 || decodeStrict(args, &req) != nil || strings.TrimSpace(req.Ask) == "" || strings.TrimSpace(req.Option) == "" {
		return e.refuse(id, "invalid_report", "An answer must carry ask and option.")
	}
	why, err := e.cleanWhy(id, req.Why, "", "")
	if err != nil {
		return err
	}
	if ai := slices.IndexFunc(s.Asks, func(a flow.Ask) bool { return a.ID == strings.TrimSpace(req.Ask) }); ai < 0 {
		return e.refuse(id, "not_found", fmt.Sprintf("No ask %q exists.", strings.TrimSpace(req.Ask)))
	} else {
		return e.answerAskLocked(id, s, ai, req.Option, why, role, AnswerProof{})
	}
}

func (e *Engine) answerAskLocked(id string, s *flow.State, ai int, option, why, by string, proof AnswerProof) error {
	a, now := s.Asks[ai], e.now()
	if a.Status != flow.AskStatusOpen {
		return e.refuse(id, flow.RefuseClosed, fmt.Sprintf("Ask %q is already %s.", a.ID, a.Status))
	}
	resolved, ok := flow.ResolveAskOption(a.Options, option)
	if a.Gated || proof != (AnswerProof{}) {
		if code, msg, err := e.verifyProofLocked(a.ID, resolved, flow.CleanText(option), proof, now); err != nil {
			return err
		} else if code != "" {
			return e.refuse(id, code, msg)
		}
	}
	if !ok {
		return e.refuse(id, "invalid_option", fmt.Sprintf("%q is not one of ask %q's options.", option, a.ID))
	}
	next, row, dec := flow.StepAsk(a, flow.AskEvent{Kind: flow.EvAskAnswer, Answer: resolved, Why: why, By: by, ProofID: proof.ID, DeviceID: proof.DeviceID}, now)
	msg := fmt.Sprintf("Answer to %s: %s", next.ID, next.Answer) + map[bool]string{true: " — " + next.Why}[next.Why != ""]
	input := map[string]any{"verb": "answer", "ask": next.ID, "option": next.Answer, "why": next.Why, "proof_id": next.ProofID, "device_id": next.DeviceID}
	if slices.Contains(dec.Asked, flow.AskDeliverAnswer) {
		ctx, cancel := context.WithTimeout(context.Background(), lockGitTimeout)
		defer cancel()
		att := openTaskAttempt(s.Tasks, next.Task)
		if next.AskedBy == flow.RoleReviewer {
			if closeWhy := e.deliverReviewerAskLocked(ctx, s, att, next.RunID, msg, now); closeWhy != "" {
				next, row, dec = flow.StepAsk(a, flow.AskEvent{Kind: flow.EvAskCancel}, now)
				s.Asks[ai] = next
				return e.commitChangeLocked(id, s, LogLine{By: by, Task: next.Task, Input: input, Rule: row, Asked: dec.Asked, Decision: "closed " + next.ID, Why: closeWhy}, Change{Kind: flow.EvAskCancel, AskID: next.ID, Task: next.Task})
			}
		} else if att != nil {
			if att.Run != nil && att.Run.State == flow.PhaseEnded {
				att.Held = appendHeld(att.Held, msg)
			} else if err := e.deliverWorkerLocked(ctx, id, next.Task, s, att, msg, false); err != nil {
				if !errors.Is(err, errNoWorkerRun) {
					e.logError("factory.exec_error", id, err)
				}
				att.Held = appendHeld(att.Held, msg)
			}
		}
	} else if slices.Contains(dec.Asked, flow.AskWake) {
		appendWake(s, next.Task, "ask_answer:"+next.ID, fmt.Sprintf("%s: %s answered %s (%s)", row, next.AnsweredBy, next.ID, next.Kind), msg, now)
	}
	s.Asks[ai] = next
	return e.commitChangeLocked(id, s, LogLine{By: next.AnsweredBy, Task: next.Task, Input: input, Rule: row, Asked: dec.Asked, Decision: "answered " + next.ID, Why: next.Answer}, Change{Kind: "ask_answer", AskID: next.ID, Task: next.Task})
}

func (e *Engine) deliverReviewerAskLocked(ctx context.Context, s *flow.State, att *flow.Attempt, runID, msg string, now time.Time) string {
	if att == nil || att.Review == nil || runID == "" || att.Review.RunID != runID || (att.Review.Run != nil && att.Review.Run.State == flow.PhaseEnded) || e.HC == nil {
		return fmt.Sprintf("asking reviewer run %s has ended or been replaced", cmp.Or(runID, "(none)"))
	}
	if t, err := e.HC.Get(ctx, runID); err != nil || t.State != "live" || (att.Review.Run != nil && att.Review.Run.ID != "" && t.Labels["sheepdog.run"] != att.Review.Run.ID) {
		return fmt.Sprintf("asking reviewer run %s is no longer live", runID)
	} else if att.Review.Run == nil {
		att.Review.Run = &flow.RunRecord{Address: runID, ID: t.Labels["sheepdog.run"], CreatedMS: t.CreatedMS, Role: flow.RoleReviewer, CLI: cmp.Or(att.Review.Reviewer.CLI, "claude"), State: flow.PhaseIdle}
	}
	if _, _, err := e.deliverRunLocked(ctx, att.Review.Run, s.Settings, msg, false, now); err != nil {
		return fmt.Sprintf("asking reviewer run %s delivery failed: %s", runID, flow.CleanErr(err.Error()))
	}
	return ""
}

func openTaskAttempt(tasks []flow.Task, taskID string) *flow.Attempt {
	if i := slices.IndexFunc(tasks, func(t flow.Task) bool { return taskID != "" && t.ID == taskID }); i >= 0 {
		return flow.OpenAttempt(tasks[i])
	}
	return nil
}
