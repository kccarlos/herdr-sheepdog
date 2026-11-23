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

const factoryAskPrefix, factoryPanePrefix, factoryVoiceDeviceID = "fask-", "factory:", "voice-session"

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

type factoryAnnounce struct{ id, paneID, text string }

// FactoryQuestions returns open gated factory asks in creation order.
func (s *Supervisor) FactoryQuestions() []state.PendingAction {
	s.factoryMu.Lock()
	defer s.factoryMu.Unlock()
	return slices.Clone(s.factoryAsks)
}

func (s *Supervisor) syncFactoryAsks() {
	s.factoryOpMu.Lock()
	all := s.cfg.Factory.Asks(false)
	var next []state.PendingAction
	names, overrideIDs := make(map[string]string, len(all)), map[string]bool{}
	for _, a := range all {
		if !a.Gated || a.Status != flow.AskStatusOpen {
			continue
		}
		opts := make([]state.Option, len(a.Options))
		for i, lbl := range a.Options {
			opts[i] = state.Option{Label: lbl, Recommended: lbl == a.Recommend}
		}
		pid := factoryAskPrefix + a.ID
		overrideIDs[pid] = a.Kind == flow.KindOverride
		if names[pid] = strings.TrimSpace(a.Task); names[pid] == "" {
			names[pid] = strings.TrimSpace(a.Project)
		}
		next = append(next, state.PendingAction{ID: pid, PaneID: factoryPanePrefix + a.Project, Kind: state.PendingAnswerPrompt, SpokenSummary: a.Question, Question: a.Question, Command: flow.FormatAskCommand(a), Options: opts, CreatedAt: a.At})
	}
	var notifies []push.Notification
	var announces []factoryAnnounce
	s.mu.Lock()
	s.factoryMu.Lock()
	s.factoryAsks, s.factoryAskNames = next, names
	s.factoryMu.Unlock()
	snap := s.store.Snapshot()
	prev := snap.Session.FactoryAsks
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
				notifies = append(notifies, push.Notification{EventID: "fask:" + p.ID, PendingID: p.ID, PaneID: p.PaneID, Kind: kind, Agent: s.cfg.Redactor.String(s.aliasOf(p.PaneID)), Text: s.cfg.Redactor.String(p.Question), Options: pushOpts, NeedsYou: true})
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
	s.syncToldLocked()
	if snap.Session.VoiceConnected {
		announces = s.collectFactoryAnnouncesLocked()
	}
	s.mu.Unlock()
	s.factoryOpMu.Unlock()
	for _, n := range notifies {
		s.cfg.Notifier.Notify(n)
	}
	s.pushFactoryAnnounces(announces)
}

func (s *Supervisor) announceFactoryAsks() {
	if s.cfg.Factory == nil {
		return
	}
	s.mu.Lock()
	var announces []factoryAnnounce
	if s.store.Snapshot().Session.VoiceConnected {
		s.syncToldLocked()
		announces = s.collectFactoryAnnouncesLocked()
	}
	s.mu.Unlock()
	s.pushFactoryAnnounces(announces)
}

func (s *Supervisor) pushFactoryAnnounces(announces []factoryAnnounce) {
	for _, a := range announces {
		s.pushTagged(a.paneID, a.text, voice.UrgencyIdle, fromDaemon(map[string]any{tagPendingID: a.id, "needs_user": true}))
	}
}

func (s *Supervisor) collectFactoryAnnouncesLocked() []factoryAnnounce {
	asks, names := s.snapshotFactoryAsksLocked()
	zh := s.ceoChineseLocked()
	var out []factoryAnnounce
	for i, p := range asks {
		k := keyOf(p)
		if e := s.told.told[k]; (e != nil && e.closedAt.IsZero()) || s.told.factoryQueued[k] != "" {
			continue
		}
		line := factoryAskLine(names[i], p, zh)
		if s.told.factoryQueued == nil {
			s.told.factoryQueued = map[promptKey]string{}
		}
		s.told.factoryQueued[k] = line
		out = append(out, factoryAnnounce{p.ID, p.PaneID, line})
	}
	return out
}

func (s *Supervisor) snapshotFactoryAsksLocked() ([]state.PendingAction, []string) {
	s.factoryMu.Lock()
	defer s.factoryMu.Unlock()
	asks, names := slices.Clone(s.factoryAsks), make([]string, len(s.factoryAsks))
	for i, p := range asks {
		if names[i] = s.factoryAskNames[p.ID]; names[i] == "" {
			names[i] = strings.TrimPrefix(p.PaneID, factoryPanePrefix)
		}
	}
	return asks, names
}

func pickLang(zh bool, en, cn string) string {
	if zh {
		return cn
	}
	return en
}

func factoryAskLine(name string, p state.PendingAction, zh bool) string {
	parts, rec := make([]string, len(p.Options)), ""
	for i, o := range p.Options {
		parts[i] = pickLang(zh, fmt.Sprintf("%d, %s", i+1, o.Label), fmt.Sprintf("%d，%s", i+1, o.Label))
		if o.Recommended {
			rec = o.Label
		}
	}
	line := pickLang(zh, fmt.Sprintf("%s: %s Options: %s.", name, p.Question, strings.Join(parts, "; ")), fmt.Sprintf("%s：%s 选项：%s。", name, p.Question, strings.Join(parts, "；")))
	if rec != "" {
		line += pickLang(zh, fmt.Sprintf(" Recommended: %s.", rec), fmt.Sprintf(" 推荐：%s。", rec))
	}
	return line
}

func (s *Supervisor) execFactoryAnswer(p state.PendingAction, option int, inputKind, deviceID, heard string, ins []*shepherdInput) (string, bool, error) {
	optLabel, askID := p.Options[option-1].Label, strings.TrimPrefix(p.ID, factoryAskPrefix)
	s.factoryOpMu.Lock()
	proof, err := s.cfg.Factory.MintProof(askID, optLabel, inputKind, deviceID)
	if err == nil {
		err = s.cfg.Factory.Answer(askID, optLabel, proof)
	}
	var ref *factory.Refusal
	gone := errors.As(err, &ref) && (ref.Code == flow.RefuseClosed || ref.Code == "not_found")
	s.mu.Lock()
	if err != nil {
		for _, in := range ins {
			in.used = false
		}
	}
	if err == nil || gone {
		s.factoryMu.Lock()
		s.factoryAsks = slices.DeleteFunc(s.factoryAsks, func(x state.PendingAction) bool { return x.ID == p.ID })
		delete(s.factoryAskNames, p.ID)
		s.factoryMu.Unlock()
		_ = s.store.UpdateSession(func(ss *state.Session) {
			ss.FactoryAsks = slices.DeleteFunc(ss.FactoryAsks, func(x string) bool { return x == p.ID })
		})
		s.closedLocked()
	}
	if err == nil {
		pl := map[string]any{"id": p.ID, "kind": p.Kind, "answer": optLabel, "option": option, "via": inputKind, "device_id": proof.DeviceID, "proof_id": proof.ID}
		if heard != "" {
			pl["heard"] = heard
		}
		s.log.Log(logSource, "pending.confirmed", p.PaneID, pl)
	}
	s.mu.Unlock()
	s.factoryOpMu.Unlock()
	return optLabel, gone, err
}

func (s *Supervisor) answerFactoryTap(id string, option int, deviceID string) (voice.ToolResult, error) {
	if deviceID = strings.TrimSpace(deviceID); deviceID == "" || deviceID == "local" {
		return voice.ToolResult{}, errors.New("proof_required: owner asks require a paired device session")
	}
	asks := s.FactoryQuestions()
	i := slices.IndexFunc(asks, func(p state.PendingAction) bool { return p.ID == id })
	if i < 0 {
		return voice.ToolResult{}, ErrPendingGone
	}
	p := asks[i]
	if option < 1 || option > len(p.Options) {
		return voice.ToolResult{}, fmt.Errorf("%w: %d (the question has %d)", ErrNoSuchOption, option, len(p.Options))
	}
	optLabel, gone, err := s.execFactoryAnswer(p, option, factory.ProofInputTap, deviceID, "", nil)
	if gone {
		return voice.ToolResult{}, ErrPendingGone
	}
	if err != nil {
		return voice.ToolResult{}, err
	}
	return voice.ToolResult{Spoken: "Answered " + optLabel + "."}, nil
}

func (s *Supervisor) factoryVoiceFastPath(c claim, callID string, daemon, forceSync bool) (voice.ToolResult, bool) {
	if s.cfg.Factory == nil || forceSync || c.in == nil || c.in.kind != InputVoice || !s.store.Snapshot().Session.VoiceConnected {
		return voice.ToolResult{}, false
	}
	if w := BareWord(c.text); w == "stop the factory" || w == "stop factory" || w == "start the factory" || w == "start factory" {
		return s.factoryControl(strings.HasPrefix(w, "start")), true
	}
	if c.retryOf != "" || strings.TrimSpace(c.text) == "" || strings.TrimSpace(c.in.text) == "" {
		return voice.ToolResult{}, false
	}
	ins := c.ins
	if len(ins) == 0 {
		ins = []*shepherdInput{c.in}
	}
	s.mu.Lock()
	for _, in := range ins {
		if in.kind != InputVoice || in.used || strings.TrimSpace(in.saidBefore) != "" {
			s.mu.Unlock()
			return voice.ToolResult{}, false
		}
	}
	asks, names := s.snapshotFactoryAsksLocked()
	if len(asks) == 0 {
		s.mu.Unlock()
		return voice.ToolResult{}, false
	}
	s.syncToldLocked()
	zh := s.ceoChineseLocked()
	target, targetName, optIdx, clarify, readBack := s.resolveFactoryVoiceLocked(asks, names, c.text, ins[0].began, zh)
	if optIdx == 0 && clarify == "" {
		s.mu.Unlock()
		return voice.ToolResult{}, false
	}
	if clarify != "" {
		if readBack {
			s.noteCallUtteranceLocked(callID, c.id)
			if daemon {
				s.promptLineForLocked(clarify, target.ID, c.id)
			} else {
				s.toldKeyForLocked(keyOf(target), "", c.id)
			}
		}
		s.mu.Unlock()
		return voice.ToolResult{Spoken: clarify, Clarify: true, Scheduling: voice.UrgencyInterrupt}, true
	}
	for _, in := range ins {
		in.used = true
	}
	s.mu.Unlock()

	optLabel, gone, err := s.execFactoryAnswer(target, optIdx, factory.ProofInputVoice, factoryVoiceDeviceID, c.text, ins)
	spoken := pickLang(zh, fmt.Sprintf("Answered %s: %s.", targetName, optLabel), fmt.Sprintf("已回复 %s：%s。", targetName, optLabel))
	if gone {
		spoken = pickLang(zh, "That question is already closed.", "这个问题已经关闭了。")
	} else if err != nil {
		spoken = pickLang(zh, fmt.Sprintf("Could not answer %s: %v.", targetName, err), fmt.Sprintf("无法回复 %s：%v。", targetName, err))
	} else if s.brain != nil {
		s.brain.Daemon(fmt.Sprintf("factory voice answer: answered %q for %s (heard %q)", optLabel, targetName, c.text))
	}
	return voice.ToolResult{Spoken: spoken, Scheduling: voice.UrgencyInterrupt}, true
}

func (s *Supervisor) resolveFactoryVoiceLocked(asks []state.PendingAction, names []string, text string, began uint64, zh bool) (target state.PendingAction, name string, opt int, clarify string, readBack bool) {
	var named bool
	if len(asks) == 1 {
		target, name = asks[0], names[0]
		var ok bool
		if opt, ok = matchFactoryOption(target, text); !ok {
			opt, ok = matchFactoryNamed(target, name, text)
			named = ok
		}
		if !ok {
			return state.PendingAction{}, "", 0, "", false
		}
	} else {
		hits, wholeAny := 0, false
		for i, p := range asks {
			if _, ok := matchFactoryOption(p, text); ok {
				wholeAny = true
			} else if o, ok := matchFactoryNamed(p, names[i], text); ok {
				target, name, opt, hits = p, names[i], o, hits+1
			}
		}
		if hits != 1 {
			if hits > 1 || wholeAny {
				return state.PendingAction{}, "", 0, pickLang(zh, fmt.Sprintf("Which ask do you mean: %s? Say the name and your answer.", strings.Join(names, ", ")), fmt.Sprintf("你说的是哪一个：%s？请带上名字再说一次。", strings.Join(names, "、"))), false
			}
			return state.PendingAction{}, "", 0, "", false
		}
		named = true
	}
	agent := ""
	if !named {
		agent = s.collidingAgentPromptLocked(text)
	}
	if e := s.told.told[keyOf(target)]; e == nil || !e.closedAt.IsZero() || began <= e.seq {
		if agent != "" {
			return state.PendingAction{}, "", 0, "", false
		}
		return target, name, 0, factoryAskLine(name, target, zh), true
	}
	if agent != "" {
		return target, name, 0, pickLang(zh, fmt.Sprintf("Did you mean %s or %s? Repeat your answer with the name.", agent, name), fmt.Sprintf("你说的是 %s 还是 %s？请带上名字再说一次。", agent, name)), false
	}
	return target, name, opt, "", false
}

func matchFactoryOption(p state.PendingAction, rem string) (int, bool) {
	w := BareWord(rem)
	if w == "" {
		return 0, false
	}
	for i, o := range p.Options {
		if BareWord(o.Label) == w {
			return i + 1, true
		}
	}
	if n := bareOptionWords[w]; n > 0 && n <= len(p.Options) {
		return n, true
	}
	return 0, false
}

func matchFactoryNamed(p state.PendingAction, name, text string) (int, bool) {
	ut, nt := strings.Fields(BareWord(text)), strings.Fields(BareWord(name))
	if len(nt) == 0 || len(ut) <= len(nt) {
		return 0, false
	}
	if slices.Equal(ut[:len(nt)], nt) {
		if opt, ok := matchFactoryOption(p, strings.Join(ut[len(nt):], " ")); ok {
			return opt, true
		}
	}
	if slices.Equal(ut[len(ut)-len(nt):], nt) {
		return matchFactoryOption(p, strings.Join(ut[:len(ut)-len(nt)], " "))
	}
	return 0, false
}

func (s *Supervisor) collidingAgentPromptLocked(text string) string {
	qs := s.store.Questions()
	if p, ok := s.store.Pending(); ok {
		qs = append(qs, p)
	}
	for _, q := range qs {
		if e := s.told.told[keyOf(q)]; q.Kind == state.PendingAnswerPrompt && e != nil && e.closedAt.IsZero() {
			if _, _, ok := bareAnswer(q, text); ok {
				return s.sayOf(q.PaneID)
			}
		}
	}
	return ""
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
