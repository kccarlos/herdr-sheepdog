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

const factoryAskPrefix, factoryPanePrefix, factoryVoiceDevice = "fask-", "factory:", "voice-live"

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
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.factoryAsks)
}

func (s *Supervisor) syncFactoryAsks() {
	s.factoryMu.Lock()
	defer s.factoryMu.Unlock()
	all, now, next := s.cfg.Factory.Asks(false), s.cfg.Now(), []state.PendingAction(nil)
	for _, a := range all {
		if !a.Gated || a.Status != flow.AskStatusOpen {
			continue
		}
		opts := make([]state.Option, len(a.Options))
		for i, lbl := range a.Options {
			opts[i] = state.Option{Label: lbl, Recommended: lbl == a.Recommend}
		}
		cmd := a.Kind + map[bool]string{true: " · " + a.Task}[a.Task != ""] + map[bool]string{true: " @ " + a.Head[:min(len(a.Head), 7)]}[a.Head != ""]
		if a.At.IsZero() {
			a.At = now
		}
		next = append(next, state.PendingAction{ID: factoryAskPrefix + a.ID, PaneID: factoryPanePrefix + a.Project, Kind: state.PendingAnswerPrompt, SpokenSummary: a.Question, Question: a.Question, Command: cmd, Options: opts, CreatedAt: a.At})
	}
	s.mu.Lock()
	prev := s.factoryAsks
	s.factoryAsks = next
	s.mu.Unlock()
	for _, p := range next {
		if slices.ContainsFunc(prev, func(x state.PendingAction) bool { return x.ID == p.ID }) {
			continue
		}
		s.log.Log(logSource, "pending.created", p.PaneID, s.pendingCreated(p))
		if s.cfg.Notifier != nil {
			opts := make([]push.Option, len(p.Options))
			for i, o := range p.Options {
				opts[i] = push.Option{Index: i + 1, Label: s.cfg.Redactor.String(o.Label)}
			}
			s.cfg.Notifier.Notify(push.Notification{EventID: "fask:" + p.ID, PendingID: p.ID, PaneID: p.PaneID, Kind: push.KindApproval, Agent: s.cfg.Redactor.String(s.aliasOf(p.PaneID)), Text: s.cfg.Redactor.String(p.Question), Options: opts, NeedsYou: true})
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

func (s *Supervisor) submitFactoryAnswerLocked(p state.PendingAction, optLabel, inputKind, deviceID string, option int) (voice.ToolResult, error) {
	askID := strings.TrimPrefix(p.ID, factoryAskPrefix)
	proof, err := s.cfg.Factory.MintProof(askID, optLabel, inputKind, deviceID)
	if err != nil {
		return voice.ToolResult{}, err
	}
	err = s.cfg.Factory.Answer(askID, optLabel, proof)
	var ref *factory.Refusal
	gone := errors.As(err, &ref) && (ref.Code == flow.RefuseClosed || ref.Code == "not_found")
	if err != nil && !gone {
		return voice.ToolResult{}, err
	}
	s.mu.Lock()
	s.factoryAsks = slices.DeleteFunc(s.factoryAsks, func(x state.PendingAction) bool { return x.ID == p.ID })
	s.mu.Unlock()
	if gone {
		return voice.ToolResult{}, ErrPendingGone
	}
	s.log.Log(logSource, "pending.confirmed", p.PaneID, map[string]any{"id": p.ID, "kind": p.Kind, "answer": optLabel, "option": option, "via": inputKind, "device_id": proof.DeviceID, "proof_id": proof.ID})
	return voice.ToolResult{Spoken: "Answered " + optLabel + "."}, nil
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
	if option < 1 || option > len(s.factoryAsks[i].Options) {
		return voice.ToolResult{}, fmt.Errorf("%w: %d (the question has %d)", ErrNoSuchOption, option, len(s.factoryAsks[i].Options))
	}
	return s.submitFactoryAnswerLocked(s.factoryAsks[i], s.factoryAsks[i].Options[option-1].Label, factory.ProofInputTap, deviceID, option)
}

func (s *Supervisor) factoryVoiceFastPath(c claim, forceSync bool) (voice.ToolResult, bool) {
	if s.cfg.Factory == nil {
		return voice.ToolResult{}, false
	}
	if w := BareWord(c.text); w == "stop the factory" || w == "stop factory" || w == "start the factory" || w == "start factory" {
		return s.factoryControl(strings.HasPrefix(w, "start")), true
	}
	wp, hasWP := s.store.Pending()
	if forceSync || c.in == nil || c.in.kind != InputVoice || !s.store.Snapshot().Session.VoiceConnected ||
		(hasWP && wp.Kind == state.PendingAnswerPrompt) || slices.ContainsFunc(c.ins, func(in *shepherdInput) bool { return in.kind != InputVoice }) {
		return voice.ToolResult{}, false
	}
	s.factoryMu.Lock()
	defer s.factoryMu.Unlock()
	if len(s.factoryAsks) != 1 {
		return voice.ToolResult{}, false
	}
	p := s.factoryAsks[0]
	s.mu.Lock()
	optLabel, ok := "", false
	if !c.in.used && !c.in.at.Before(p.CreatedAt) && s.cfg.Now().Sub(c.in.at) <= s.cfg.AuthoriseWindow {
		optLabel, ok = matchFactoryVoiceOption(p, c.text)
		c.in.used = ok
	}
	s.mu.Unlock()
	if !ok {
		return voice.ToolResult{}, false
	}
	res, err := s.submitFactoryAnswerLocked(p, optLabel, factory.ProofInputVoice, factoryVoiceDevice, 0)
	if err != nil {
		s.mu.Lock()
		c.in.used = false
		s.mu.Unlock()
	}
	return res, err == nil
}

func matchFactoryVoiceOption(p state.PendingAction, text string) (string, bool) {
	word, chosen := BareWord(text), ""
	has := func(label string, words ...string) bool {
		l := strings.ToLower(strings.TrimSpace(label))
		return slices.ContainsFunc(words, func(w string) bool { return l == w || strings.HasPrefix(l, w+" ") || strings.HasPrefix(l, w+",") })
	}
	approve := func(l string) bool {
		return has(l, "yes", "allow", "approve", "always", "proceed", "accept", "run", "continue")
	}
	decline := func(l string) bool {
		return has(l, "no", "don't", "do not", "deny", "decline", "reject", "cancel", "skip", "hold", "wait", "stop")
	}
	pick := func(fn func(string) bool) string {
		if i := slices.IndexFunc(p.Options, func(o state.Option) bool { return fn(o.Label) }); i >= 0 {
			return p.Options[i].Label
		}
		return ""
	}
	switch {
	case bareYesWords[word]:
		if chosen = pick(approve); chosen == "" && len(p.Options) > 0 && !decline(p.Options[0].Label) {
			chosen = p.Options[0].Label
		}
	case bareNoWords[word]:
		chosen = pick(decline)
	case bareOptionWords[word] > 0 && bareOptionWords[word] <= len(p.Options):
		chosen = p.Options[bareOptionWords[word]-1].Label
	case word != "":
		chosen = pick(func(l string) bool { return BareWord(l) == word })
	}
	return chosen, chosen != "" && (!negativeInput(text) || !approve(chosen))
}

func (s *Supervisor) factoryControl(start bool) voice.ToolResult {
	var changed, errs []string
	for _, p := range s.cfg.Factory.Projects() {
		if start == p.Running {
			continue
		}
		if err := map[bool]func() error{false: func() error { return s.cfg.Factory.Stop(p.ID) }, true: func() error { return s.cfg.Factory.Start(p.Folder, p.Goal, 0) }}[start](); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p.ID, err))
		} else {
			changed = append(changed, p.ID)
		}
	}
	s.log.Log(logSource, "supervisor.fast_path", "", map[string]any{"path": map[bool]string{false: "factory_stop", true: "factory_start"}[start], "projects": changed})
	switch {
	case len(changed) > 0:
		return voice.ToolResult{Spoken: fmt.Sprintf(map[bool]string{false: "Stopped the factory for %s. The plan is kept.", true: "Started the factory for %s."}[start], strings.Join(changed, ", "))}
	case len(errs) > 0:
		return voice.ToolResult{Spoken: "Could not update the factory: " + strings.Join(errs, "; ") + "."}
	}
	return voice.ToolResult{Spoken: map[bool]string{false: "No factory project is running.", true: "No stopped factory project to start."}[start]}
}
