package supervisor

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

const factoryAskPrefix, factoryPanePrefix = "fask-", "factory:"

// Factory is the subset of *factory.Engine the Supervisor needs for the ask
// bridge and owner voice start/stop commands (design.md 12.3).
type Factory interface {
	Projects() []factory.ProjectView
	Asks(open bool) []flow.Ask
	MintProof(askID, option, inputKind, deviceID string) (factory.AnswerProof, error)
	Answer(askID, option string, proof factory.AnswerProof) error
	Start(folder, goal string, maxRunning int) error
	Stop(project string) error
	Changes() <-chan factory.Change
}

var _ Factory = (*factory.Engine)(nil)

// FactoryQuestions returns open gated factory asks in creation order.
func (s *Supervisor) FactoryQuestions() []state.PendingAction {
	s.factoryMu.Lock()
	defer s.factoryMu.Unlock()
	return slices.Clone(s.factoryAsks)
}

func (s *Supervisor) handleFactoryChange(c factory.Change) {
	if c.Kind == flow.AskOverrideResult {
		s.deliverFactoryOverrideResult(c)
		return
	}
	s.syncFactoryAsks()
}

func (s *Supervisor) deliverFactoryOverrideResult(c factory.Change) {
	text := strings.TrimSpace(c.Text)
	if text == "" {
		return
	}
	s.pushTagged("", text, voice.UrgencyIdle, fromDaemon(map[string]any{"factory_override_result": c.AskID}))
	if s.cfg.Notifier != nil {
		paneID := factoryPanePrefix + c.Project
		s.cfg.Notifier.Notify(push.Notification{
			EventID: fmt.Sprintf("fovr:%s:%d", cmp.Or(c.AskID, c.Project), c.Seq),
			PaneID:  paneID,
			Kind:    push.KindDone,
			Agent:   s.cfg.Redactor.String(s.aliasOf(paneID)),
			Text:    s.cfg.Redactor.String(text),
		})
	}
}

func (s *Supervisor) syncFactoryAsks() {
	s.factoryMu.Lock()
	defer s.factoryMu.Unlock()
	all := s.cfg.Factory.Asks(false)
	var next []state.PendingAction
	overrideIDs := map[string]bool{}
	for _, a := range all {
		if !a.Gated || a.Status != flow.AskStatusOpen {
			continue
		}
		opts := make([]state.Option, len(a.Options))
		for i, lbl := range a.Options {
			opts[i] = state.Option{Label: lbl, Recommended: lbl == a.Recommend}
		}
		pid := factoryAskPrefix + a.ID
		if a.Kind == flow.KindOverride {
			overrideIDs[pid] = true
		}
		next = append(next, state.PendingAction{ID: pid, PaneID: factoryPanePrefix + a.Project, Kind: state.PendingAnswerPrompt, SpokenSummary: a.Question, Question: a.Question, Command: flow.FormatAskCommand(a), Options: opts, CreatedAt: a.At})
	}
	prev := s.store.Snapshot().Session.FactoryAsks
	s.factoryAsks = next
	var ids []string
	for _, p := range next {
		ids = append(ids, p.ID)
		if !slices.Contains(prev, p.ID) {
			s.log.Log(logSource, "pending.created", p.PaneID, s.pendingCreated(p))
			if s.cfg.Notifier != nil {
				kind, pushOpts := push.KindApproval, s.pushOptions(p.Options)
				if overrideIDs[p.ID] {
					kind, pushOpts = push.KindBlocked, nil
				}
				s.cfg.Notifier.Notify(push.Notification{EventID: "fask:" + p.ID, PendingID: p.ID, PaneID: p.PaneID, Kind: kind, Agent: s.cfg.Redactor.String(s.aliasOf(p.PaneID)), Text: s.cfg.Redactor.String(p.Question), Options: pushOpts, NeedsYou: true})
			}
		}
	}
	for _, id := range prev {
		if !slices.Contains(ids, id) {
			var paneID string
			if i := slices.IndexFunc(all, func(a flow.Ask) bool { return factoryAskPrefix+a.ID == id }); i >= 0 {
				paneID = factoryPanePrefix + all[i].Project
				if all[i].Status == flow.AskStatusAnswered {
					s.log.Log(logSource, "pending.confirmed", paneID, map[string]any{"id": id, "kind": state.PendingAnswerPrompt, "answer": all[i].Answer})
					continue
				}
			}
			s.log.Log(logSource, "pending.cancelled", paneID, map[string]any{"id": id, "kind": state.PendingAnswerPrompt, "reason": "cancelled"})
		}
	}
	if !slices.Equal(prev, ids) {
		_ = s.store.UpdateSession(func(ss *state.Session) { ss.FactoryAsks = ids })
	}
}

func (s *Supervisor) answerFactoryTap(id string, option int, deviceID string) (voice.ToolResult, error) {
	if deviceID = strings.TrimSpace(deviceID); deviceID == "" || deviceID == "local" {
		return voice.ToolResult{}, errors.New("proof_required: owner asks require a paired device session")
	}
	s.factoryMu.Lock()
	defer s.factoryMu.Unlock()
	i := slices.IndexFunc(s.factoryAsks, func(p state.PendingAction) bool { return p.ID == id })
	if i < 0 {
		return voice.ToolResult{}, ErrPendingGone
	}
	p := s.factoryAsks[i]
	if option < 1 || option > len(p.Options) {
		return voice.ToolResult{}, fmt.Errorf("%w: %d (the question has %d)", ErrNoSuchOption, option, len(p.Options))
	}
	optLabel, askID := p.Options[option-1].Label, strings.TrimPrefix(p.ID, factoryAskPrefix)
	proof, err := s.cfg.Factory.MintProof(askID, optLabel, factory.ProofInputTap, deviceID)
	if err != nil {
		return voice.ToolResult{}, err
	}
	err = s.cfg.Factory.Answer(askID, optLabel, proof)
	var ref *factory.Refusal
	gone := errors.As(err, &ref) && (ref.Code == flow.RefuseClosed || ref.Code == "not_found")
	if err != nil && !gone {
		return voice.ToolResult{}, err
	}
	s.factoryAsks = slices.DeleteFunc(s.factoryAsks, func(x state.PendingAction) bool { return x.ID == p.ID })
	if gone {
		return voice.ToolResult{}, ErrPendingGone
	}
	_ = s.store.UpdateSession(func(ss *state.Session) {
		ss.FactoryAsks = slices.DeleteFunc(ss.FactoryAsks, func(x string) bool { return x == p.ID })
	})
	s.log.Log(logSource, "pending.confirmed", p.PaneID, map[string]any{"id": p.ID, "kind": p.Kind, "answer": optLabel, "option": option, "via": factory.ProofInputTap, "device_id": proof.DeviceID, "proof_id": proof.ID})
	return voice.ToolResult{Spoken: "Answered " + optLabel + "."}, nil
}

func (s *Supervisor) factoryVoiceFastPath(c claim, forceSync bool) (voice.ToolResult, bool) {
	if s.cfg.Factory == nil || forceSync || c.in == nil || c.in.kind != InputVoice || !s.store.Snapshot().Session.VoiceConnected {
		return voice.ToolResult{}, false
	}
	if w := BareWord(c.text); w == "stop the factory" || w == "stop factory" || w == "start the factory" || w == "start factory" {
		return s.factoryControl(strings.HasPrefix(w, "start")), true
	}
	return voice.ToolResult{}, false
}

func (s *Supervisor) factoryControl(start bool) voice.ToolResult {
	var changed, errs []string
	for _, p := range s.cfg.Factory.Projects() {
		if start == p.Running || (start && p.Error != "") {
			continue
		}
		var err error
		if start {
			err = s.cfg.Factory.Start(p.Folder, p.Goal, 0)
		} else {
			err = s.cfg.Factory.Stop(p.ID)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p.ID, err))
		} else {
			changed = append(changed, p.ID)
		}
	}
	path, spoken := "factory_stop", "No factory project is running."
	if start {
		path, spoken = "factory_start", "No stopped factory project to start."
	}
	s.log.Log(logSource, "supervisor.fast_path", "", map[string]any{"path": path, "projects": changed})
	switch {
	case len(changed) > 0 && start:
		spoken = fmt.Sprintf("Started the factory for %s.", strings.Join(changed, ", "))
	case len(changed) > 0:
		spoken = fmt.Sprintf("Stopped the factory for %s. The plan is kept.", strings.Join(changed, ", "))
	case len(errs) > 0:
		spoken = "Could not update the factory: " + strings.Join(errs, "; ") + "."
	}
	return voice.ToolResult{Spoken: spoken, Scheduling: voice.UrgencyInterrupt}
}
