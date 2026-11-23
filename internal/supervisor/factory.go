package supervisor

import (
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

func (s *Supervisor) syncFactoryAsks() {
	s.factoryMu.Lock()
	defer s.factoryMu.Unlock()
	all := s.cfg.Factory.Asks(false)
	var next []state.PendingAction
	for _, a := range all {
		if !a.Gated || a.Status != flow.AskStatusOpen {
			continue
		}
		opts := make([]state.Option, len(a.Options))
		for i, lbl := range a.Options {
			opts[i] = state.Option{Label: lbl, Recommended: lbl == a.Recommend}
		}
		cmd := a.Kind
		if a.Task != "" {
			cmd += " · " + a.Task
		}
		if a.Head != "" {
			cmd += " @ " + a.Head[:min(len(a.Head), 7)]
		}
		next = append(next, state.PendingAction{ID: factoryAskPrefix + a.ID, PaneID: factoryPanePrefix + a.Project, Kind: state.PendingAnswerPrompt, SpokenSummary: a.Question, Question: a.Question, Command: cmd, Options: opts, CreatedAt: a.At})
	}
	prev := s.factoryAsks
	s.factoryAsks = next
	for _, p := range next {
		if slices.ContainsFunc(prev, func(x state.PendingAction) bool { return x.ID == p.ID }) {
			continue
		}
		s.log.Log(logSource, "pending.created", p.PaneID, s.pendingCreated(p))
		if s.cfg.Notifier != nil {
			s.cfg.Notifier.Notify(push.Notification{EventID: "fask:" + p.ID, PendingID: p.ID, PaneID: p.PaneID, Kind: push.KindApproval, Agent: s.cfg.Redactor.String(s.aliasOf(p.PaneID)), Text: s.cfg.Redactor.String(p.Question), Options: s.pushOptions(p.Options), NeedsYou: true})
		}
	}
	for _, p := range prev {
		if slices.ContainsFunc(next, func(x state.PendingAction) bool { return x.ID == p.ID }) {
			continue
		}
		if i := slices.IndexFunc(all, func(a flow.Ask) bool { return factoryAskPrefix+a.ID == p.ID }); i >= 0 && all[i].Status == flow.AskStatusAnswered {
			s.log.Log(logSource, "pending.confirmed", p.PaneID, map[string]any{"id": p.ID, "kind": p.Kind, "answer": all[i].Answer})
		} else {
			s.log.Log(logSource, "pending.cancelled", p.PaneID, map[string]any{"id": p.ID, "kind": p.Kind, "reason": "cancelled"})
		}
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
