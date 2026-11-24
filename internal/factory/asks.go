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
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

// Change is one event on Engine.Changes() (design.md 12.3).
type Change struct {
	Project string `json:"project,omitempty"`
	Kind    string `json:"kind"`
	AskID   string `json:"ask_id,omitempty"`
	Task    string `json:"task,omitempty"`
	Text    string `json:"text,omitempty"`
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

func (e *Engine) notifyChangeLocked(id string, seq int64, c Change) {
	c.Project, c.Seq = id, seq
	for _, ch := range e.changeSubs {
		select {
		case ch <- c:
		default:
		}
	}
}

func (e *Engine) commitChangeLocked(id string, s *flow.State, l LogLine, c Change) error {
	if err := e.commitLocked(id, s, l); err != nil {
		return err
	}
	e.notifyChangeLocked(id, s.Seq, c)
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
				a.Project, a.Folder, a.Options, a.Targets = cmp.Or(a.Project, id), cmp.Or(a.Folder, s.Project.Folder), slices.Clone(a.Options), slices.Clone(a.Targets)
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
		Options, Tasks                  []string
	}
	if len(bytes.TrimSpace(args)) == 0 || decodeStrict(args, &req) != nil {
		return e.refuse(id, "invalid_report", "An ask must carry kind, question, options and recommend.")
	}
	if strings.TrimSpace(req.Kind) == flow.KindOverride {
		return e.acceptOverrideAskLocked(id, s, role, runID, req.Tasks, req.Task)
	}
	if len(req.Tasks) > 0 {
		return e.refuse(id, "invalid_report", "Only override asks accept tasks.")
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

func (e *Engine) acceptOverrideAskLocked(id string, s *flow.State, role, runID string, rawTasks []string, rawTask string) error {
	if role != flow.RoleCoordinator {
		return e.refuse(id, "kind_not_allowed", fmt.Sprintf("A %s may not open an override ask.", role))
	}
	if strings.TrimSpace(rawTask) != "" || len(rawTasks) == 0 {
		return e.refuse(id, "invalid_ask", "An override ask must name at least one task in tasks.")
	}
	seen := make(map[string]bool, len(rawTasks))
	taskIdxs := make([]int, 0, len(rawTasks))
	names := make([]string, 0, len(rawTasks))
	for _, raw := range rawTasks {
		tid := strings.TrimSpace(raw)
		if tid == "" || seen[tid] {
			return e.refuse(id, "invalid_ask", "Override tasks must be distinct non-blank task ids.")
		}
		ti := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return t.ID == tid })
		if ti < 0 {
			return e.refuse(id, "invalid_ask", fmt.Sprintf("Unknown task %q.", tid))
		}
		if t := s.Tasks[ti]; flow.Ended(t) {
			return e.refuse(id, "invalid_ask", fmt.Sprintf("Task %q has already ended.", tid))
		} else if att := flow.OpenAttempt(t); att == nil || len(att.Ready) == 0 {
			return e.refuse(id, "invalid_ask", fmt.Sprintf("Task %q has no ready report.", tid))
		}
		seen[tid] = true
		taskIdxs, names = append(taskIdxs, ti), append(names, tid)
	}
	repoCtx := world.GitRepository(context.Background(), s.Project.Folder)
	targets := make([]flow.OverrideTarget, 0, len(taskIdxs))
	for _, ti := range taskIdxs {
		t := s.Tasks[ti]
		att := flow.OpenAttempt(t)
		h := flow.H(*att)
		unreported := false
		if e.git != nil && att.Worktree != "" {
			gctx, cancel := context.WithTimeout(repoCtx, lockGitTimeout)
			_, rel, clean, err := e.git.InspectWorktree(gctx, att.Worktree, h)
			cancel()
			if err != nil {
				return err
			}
			unreported = !clean || rel == flow.RelAhead || rel == flow.RelDiverged
		}
		p := e.prs[att.Worktree]
		if e.git != nil {
			p = e.readPRLocked(repoCtx, id, s, ti)
		}
		if p.State == flow.PRClosed || p.State == flow.PRMerged {
			return e.refuse(id, "invalid_ask", fmt.Sprintf("Task %q PR is already %s.", t.ID, p.State))
		}
		prNum := att.PR
		if prNum == 0 && p.State == flow.PROpen {
			prNum = p.Number
		}
		checks := flow.ChecksAbsent
		if p.Answered && p.State == flow.PROpen && p.Head == h && p.Checks != "" {
			checks = p.Checks
		} else if !p.Answered && prNum > 0 {
			checks = flow.ChecksUnreadable
		}
		targets = append(targets, flow.OverrideTarget{Task: t.ID, PR: prNum, Head: h, Checks: checks, Unreported: unreported})
	}
	ask, row, dec, code, why := e.openEngineAskLocked(id, s, flow.Ask{
		Kind:      flow.KindOverride,
		Question:  fmt.Sprintf("Merge %s now without waiting for CI or review?", strings.Join(names, ", ")),
		Options:   []string{flow.OverrideOptionMerge, flow.OverrideOptionCancel},
		Recommend: flow.OverrideOptionCancel,
		Targets:   targets,
		AskedBy:   role,
		RunID:     runID,
	})
	if code != "" {
		return e.refuse(id, code, why)
	}
	return e.commitChangeLocked(id, s, LogLine{By: role, Input: map[string]any{"verb": "ask", "id": ask.ID, "kind": ask.Kind, "question": ask.Question, "options": ask.Options, "recommend": ask.Recommend, "targets": ask.Targets}, Rule: row, Asked: dec.Asked, Decision: "ask opened: " + ask.ID, Why: ask.Question}, Change{Kind: flow.EvAskOpen, AskID: ask.ID})
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
	resolved, ok := option, slices.Contains(a.Options, option)
	if a.Kind != flow.KindOverride {
		resolved, ok = flow.ResolveAskOption(a.Options, option)
	}
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
		if next.AskedBy == flow.RoleReviewer || next.AskedBy == flow.RoleRetro {
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
	if next.AskedBy == flow.AskedByN24 && next.AnsweredBy == owner && strings.TrimSpace(next.Answer) == flow.N24OptionDrop {
		if ti := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return t.ID == next.Task }); ti >= 0 && !flow.Ended(s.Tasks[ti]) {
			s.Tasks[ti].Dropped = &flow.Dropped{Why: "owner chose " + next.Answer}
		}
	}
	if next.Kind == flow.KindOverride && next.AnsweredBy == owner && next.Answer == flow.OverrideOptionMerge {
		for _, tgt := range next.Targets {
			if ti := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return t.ID == tgt.Task }); ti >= 0 && !flow.Ended(s.Tasks[ti]) {
				if att := flow.OpenAttempt(s.Tasks[ti]); att != nil && (slices.ContainsFunc(att.Ready, func(r flow.Ready) bool { return r.Head == tgt.Head }) || slices.ContainsFunc(att.MergeOnly, func(m flow.Advance) bool { return m.From == tgt.Head || m.To == tgt.Head })) {
					att.Overrides = append(att.Overrides, flow.Override{Head: tgt.Head, ChecksShown: tgt.Checks, Spent: false, ProofID: next.ProofID})
					if flow.Covers(*att, tgt.Head, flow.H(*att)) {
						if rc := flow.OpenRecap(s.Tasks[ti]); rc != nil {
							rc.Decided = &flow.RecapDecision{Choice: "override", Why: "owner override " + next.ID, At: now}
						}
					}
				}
			}
		}
	}
	return e.commitChangeLocked(id, s, LogLine{By: next.AnsweredBy, Task: next.Task, Input: input, Rule: row, Asked: dec.Asked, Decision: "answered " + next.ID, Why: next.Answer}, Change{Kind: "ask_answer", AskID: next.ID, Task: next.Task})
}

func (e *Engine) emitOverrideResultsLocked(id string, s *flow.State, now time.Time) {
	for _, ev := range flow.OverrideResult(*s, now) {
		ti := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return t.ID == ev.Task })
		if ti < 0 {
			continue
		}
		prevLen := len(s.Tasks[ti].Acts)
		s.Tasks[ti].Acts = append(s.Tasks[ti].Acts, flow.Act{Key: flow.AskOverrideResult + "@" + ev.AskID, Result: "done", At: now})
		err := e.commitChangeLocked(id, s, LogLine{By: "engine", Task: ev.Task, Rule: ev.Rule, Decision: "override result", Why: ev.Text}, Change{Kind: flow.AskOverrideResult, AskID: ev.AskID, Task: ev.Task, Text: ev.Text})
		if err != nil {
			s.Tasks[ti].Acts = s.Tasks[ti].Acts[:prevLen]
			e.logError("factory.commit_error", id, err)
			return
		}
	}
}

func (e *Engine) deliverReviewerAskLocked(ctx context.Context, s *flow.State, att *flow.Attempt, runID, msg string, now time.Time) string {
	if att == nil || att.Review == nil || runID == "" || att.Review.RunID != runID || (att.Review.Run != nil && att.Review.Run.State == flow.PhaseEnded) || e.HC == nil {
		return fmt.Sprintf("asking reviewer run %s has ended or been replaced", cmp.Or(runID, "(none)"))
	}
	if t, err := e.HC.Get(ctx, runID); err != nil || t.State != "live" || (att.Review.Run != nil && att.Review.Run.ID != "" && t.Labels["sheepdog.run"] != att.Review.Run.ID) {
		return fmt.Sprintf("asking reviewer run %s is no longer live", runID)
	} else if att.Review.Run == nil {
		att.Review.Run = &flow.RunRecord{Address: runID, ID: t.Labels["sheepdog.run"], CreatedMS: t.CreatedMS, Role: att.Review.Role(), CLI: cmp.Or(att.Review.Reviewer.CLI, "claude"), State: flow.PhaseIdle}
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

func (e *Engine) openEngineAskLocked(id string, s *flow.State, raw flow.Ask) (flow.Ask, string, flow.Decision, string, string) {
	raw.Question = flow.CapRunes(flow.CleanText(raw.Question), flow.MaxAskQuestionLen)
	ask, code, why := flow.ValidateAsk(raw)
	if code != "" {
		return flow.Ask{}, "", flow.Decision{}, code, why
	}
	ask.ID, ask.Project, ask.Folder = fmt.Sprintf("ask-%s-%d-%d", id, s.Seq+1, len(s.Asks)+1), id, s.Project.Folder
	ask, row, dec := flow.StepAsk(ask, flow.AskEvent{Kind: flow.EvAskOpen}, e.now())
	s.Asks = append(s.Asks, ask)
	return ask, row, dec, "", ""
}

func (e *Engine) execOpenGateAsk(_ context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, h := s.Tasks[ti], ""
	if a := flow.OpenAttempt(t); a != nil {
		h = flow.H(*a)
	}
	rule, q, opts, rec := flow.AskedByN31, fmt.Sprintf("Approve task %s (%s) at %s?", t.ID, t.Title, h[:min(len(h), 7)]), []string{flow.GateOptionMerge, flow.GateOptionHold}, flow.GateOptionMerge
	if strings.HasPrefix(f.Ask, flow.AskOpenN24) {
		rule, q, opts, rec = flow.AskedByN24, fmt.Sprintf("Task %s (split from %s after retro) hit review recap — drop it, or tell me how to go on?", t.ID, t.SplitFrom), []string{flow.N24OptionDrop, flow.N24OptionContinue}, flow.N24OptionDrop
	}
	ask, _, _, code, why := e.openEngineAskLocked(id, s, flow.Ask{
		Kind: flow.KindGate, Task: t.ID, Head: h, AskedBy: rule, Question: q, Options: opts, Recommend: rec,
	})
	if code != "" {
		return "", false, errors.New(code + ": " + why)
	}
	return "opened owner gate ask " + ask.ID, false, nil
}

// cancelStaleGateAsksLocked cancels any open gate ask for task ti whose Head
// no longer covers H (design.md 7.1). Returns false if a commit fails.
func (e *Engine) cancelStaleGateAsksLocked(id string, s *flow.State, ti int, now time.Time) bool {
	t := s.Tasks[ti]
	att := flow.OpenAttempt(t)
	for ai := range s.Asks {
		a := s.Asks[ai]
		if a.Task != t.ID || a.Kind != flow.KindGate || a.Status != flow.AskStatusOpen || a.Head == "" {
			continue
		}
		if att == nil && len(t.Attempts) == 0 || att != nil && (len(att.Ready) == 0 && len(t.Attempts) == 1 || flow.Covers(*att, a.Head, flow.H(*att))) {
			continue
		}
		next, row, dec := flow.StepAsk(a, flow.AskEvent{Kind: flow.EvAskCancel}, now)
		s.Asks[ai] = next
		if err := e.commitChangeLocked(id, s, LogLine{By: "engine", Task: t.ID, Rule: row, Asked: dec.Asked, Decision: "cancelled stale gate ask " + next.ID, Why: "H moved past " + a.Head}, Change{Kind: flow.EvAskCancel, AskID: next.ID, Task: t.ID}); err != nil {
			s.Asks[ai] = a
			e.logError("factory.commit_error", id, err)
			return false
		}
	}
	return true
}

// cancelTaskDecisionAsksLocked cancels taskID's open decision asks while
// keeping its gated owner asks alive (N1, design.md 4.1, 7.1, KB-ASK-10).
func cancelTaskDecisionAsks(s *flow.State, taskID string, now time.Time) int {
	n := 0
	for ai := range s.Asks {
		if a := s.Asks[ai]; a.Task == taskID && a.Kind == flow.KindDecision && a.Status == flow.AskStatusOpen {
			s.Asks[ai], _, _ = flow.StepAsk(a, flow.AskEvent{Kind: flow.EvAskCancel}, now)
			n++
		}
	}
	return n
}
