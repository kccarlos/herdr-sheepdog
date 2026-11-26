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

const factoryAskPrefix, factoryPanePrefix, factoryVoiceDeviceID = "fask-", "factory:", "voice-session"

// factoryControl is the ask bridge and owner voice start/stop contract.
type factoryControl interface {
	Projects() []factory.ProjectView
	Asks(open bool) []flow.Ask
	MintProof(askID, option, inputKind, deviceID string) (factory.AnswerProof, error)
	Answer(askID, option string, proof factory.AnswerProof) error
	Start(folder, goal string, maxRunning int) error
	Stop(project string) error
	Changes() <-chan factory.Change
}

// Factory adds the views the orchestrator reads for project contacts.
type Factory interface {
	factoryControl
	ProjectHeads() []factory.ProjectHead
	Graph(project string) (factory.GraphView, error)
	LogLines(project string, n int, task string, observations bool) ([]factory.LogEntry, error)
}

var _ Factory = (*factory.Engine)(nil)

type factoryAnnounce struct{ id, paneID, text string }

func (s *Supervisor) factoryProjectName(id string) string {
	if f, ok := s.cfg.Factory.(interface{ ProjectName(string) string }); ok {
		return f.ProjectName(id)
	}
	if s.cfg.Factory != nil {
		for _, p := range s.cfg.Factory.Projects() {
			if p.ID == id {
				return cmp.Or(p.Name, factory.ProjectName(id, p.Folder))
			}
		}
	}
	return id
}

func (s *Supervisor) coordinatorName(labels map[string]string) string {
	if id := labels["sheepdog.project"]; s.cfg.Factory != nil && id != "" && labels["sheepdog.role"] == flow.RoleCoordinator {
		return s.factoryProjectName(id)
	}
	return ""
}

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
	s.factoryOpMu.Lock()
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
		pid := factoryAskPrefix + a.ID
		meta := &state.FactoryAsk{Project: a.Project, Task: a.Task, Kind: a.Kind, ProjectName: s.factoryProjectName(a.Project)}
		next = append(next, state.PendingAction{ID: pid, PaneID: factoryPanePrefix + a.Project, Kind: state.PendingAnswerPrompt,
			SpokenSummary: a.Question, Question: a.Question, Command: flow.FormatAskCommand(a), Options: opts, CreatedAt: a.At, Factory: meta})
	}
	var notifies []push.Notification
	var announces []factoryAnnounce
	s.mu.Lock()
	s.factoryMu.Lock()
	s.factoryAsks = next
	s.factoryMu.Unlock()
	snap := s.store.Snapshot()
	prev := snap.Session.FactoryAsks
	var ids []string
	for _, p := range next {
		ids = append(ids, p.ID)
		if !slices.Contains(prev, p.ID) {
			s.log.Log(logSource, "pending.created", p.PaneID, s.pendingCreated(p))
			if s.cfg.Notifier != nil {
				notifies = append(notifies, push.Notification{EventID: "fask:" + p.ID, PendingID: p.ID, PaneID: p.PaneID, Kind: push.KindBlocked, Agent: s.cfg.Redactor.String(s.aliasOf(p.PaneID)), Text: s.cfg.Redactor.String(p.Question), NeedsYou: true})
			}
		}
	}
	for _, id := range prev {
		if !slices.Contains(ids, id) {
			s.logFactoryClosed(id, "", all)
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

func (s *Supervisor) logFactoryClosed(id, paneID string, all []flow.Ask) {
	if i := slices.IndexFunc(all, func(a flow.Ask) bool { return factoryAskPrefix+a.ID == id }); i >= 0 {
		paneID = factoryPanePrefix + all[i].Project
		if all[i].Status == flow.AskStatusAnswered {
			s.log.Log(logSource, "pending.confirmed", paneID, map[string]any{"id": id, "kind": state.PendingAnswerPrompt, "answer": all[i].Answer})
			return
		}
	}
	s.log.Log(logSource, "pending.cancelled", paneID, map[string]any{"id": id, "kind": state.PendingAnswerPrompt, "reason": "cancelled"})
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
		if !s.pushTagged(a.paneID, a.text, voice.UrgencyIdle, fromDaemon(map[string]any{tagPendingID: a.id, "needs_user": true})) {
			s.mu.Lock()
			s.clearFactoryQueuedLocked(a.text)
			s.mu.Unlock()
		}
	}
}

func (s *Supervisor) collectFactoryAnnouncesLocked() []factoryAnnounce {
	zh := s.ceoChineseLocked()
	asks, names := s.snapshotFactoryAsksLocked(zh)
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

func (s *Supervisor) snapshotFactoryAsksLocked(zh bool) ([]state.PendingAction, []string) {
	asks := s.FactoryQuestions()
	names := make([]string, len(asks))
	for i, p := range asks {
		names[i] = p.Factory.ProjectName
		if task := strings.TrimSpace(p.Factory.Task); task != "" {
			names[i] += pickLang(zh, ", ", "，") + task
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
	var all []flow.Ask
	if gone {
		all = s.cfg.Factory.Asks(false)
	}
	s.mu.Lock()
	if err != nil {
		for _, in := range ins {
			in.used = false
		}
	}
	if err == nil || gone {
		s.factoryMu.Lock()
		s.factoryAsks = slices.DeleteFunc(s.factoryAsks, func(x state.PendingAction) bool { return x.ID == p.ID })
		s.factoryMu.Unlock()
		had := false
		_ = s.store.UpdateSession(func(ss *state.Session) {
			had = slices.Contains(ss.FactoryAsks, p.ID)
			ss.FactoryAsks = slices.DeleteFunc(ss.FactoryAsks, func(x string) bool { return x == p.ID })
		})
		s.closedLocked()
		if err == nil {
			pl := map[string]any{"id": p.ID, "kind": p.Kind, "answer": optLabel, "option": option, "via": inputKind, "device_id": proof.DeviceID, "proof_id": proof.ID}
			if heard != "" {
				pl["heard"] = heard
			}
			s.log.Log(logSource, "pending.confirmed", p.PaneID, pl)
		} else if had {
			s.logFactoryClosed(p.ID, p.PaneID, all)
		}
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
	if s.cfg.Factory == nil || forceSync || c.stopRest || c.in == nil || c.in.kind != InputVoice || !s.store.Snapshot().Session.VoiceConnected {
		return voice.ToolResult{}, false
	}
	if w := BareWord(c.text); w == "stop the factory" || w == "stop factory" || w == "start the factory" || w == "start factory" {
		return s.factoryControl(strings.HasPrefix(w, "start")), true
	}
	if c.retryOf != "" || len(c.ins) == 0 || strings.TrimSpace(c.text) == "" || strings.TrimSpace(c.in.text) == "" {
		return voice.ToolResult{}, false
	}
	s.mu.Lock()
	zh := s.ceoChineseLocked()
	asks, names := s.snapshotFactoryAsksLocked(zh)
	if len(asks) == 0 || slices.ContainsFunc(c.ins, func(in *shepherdInput) bool {
		return in.kind != InputVoice || in.used || strings.TrimSpace(in.saidBefore) != ""
	}) {
		s.mu.Unlock()
		return voice.ToolResult{}, false
	}
	s.syncToldLocked()
	target, targetName, optIdx, clarify, readBack := s.resolveFactoryVoiceLocked(asks, names, c.text, c.ins[0].began, zh)
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
	for _, in := range c.ins {
		in.used = true
	}
	s.mu.Unlock()

	optLabel, gone, err := s.execFactoryAnswer(target, optIdx, factory.ProofInputVoice, factoryVoiceDeviceID, c.text, c.ins)
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
		if o, ok := matchFactoryOption(target, text); ok {
			opt = o
		} else if opt, named = matchFactoryAsk(target, name, text); !named {
			return state.PendingAction{}, "", 0, "", false
		}
	} else {
		hits, wholeAny := 0, false
		for i, p := range asks {
			if _, ok := matchFactoryOption(p, text); ok {
				wholeAny = true
			} else if o, ok := matchFactoryAsk(p, names[i], text); ok {
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

func matchFactoryAsk(p state.PendingAction, name, text string) (int, bool) {
	if opt, ok := matchFactoryNamed(p, strings.ReplaceAll(name, "，", " "), text); ok {
		return opt, true
	}
	return matchFactoryNamed(p, cmp.Or(p.Factory.Task, p.Factory.Project), text)
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
	var changed, names, errs []string
	for _, p := range s.cfg.Factory.Projects() {
		st := cmp.Or(p.State, map[bool]string{true: "running", false: "stopped"}[p.Running])
		if p.Error != "" || (start && st != "stopped") || (!start && st != "running" && (st != "finished" || !p.Live)) {
			continue
		}
		var err error
		if start {
			err = s.cfg.Factory.Start(p.Folder, p.Goal, 0)
		} else {
			err = s.cfg.Factory.Stop(p.ID)
		}
		name := cmp.Or(p.Name, factory.ProjectName(p.ID, p.Folder))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		} else {
			changed = append(changed, p.ID)
			names = append(names, name)
		}
	}
	path, spoken := "factory_stop", "No factory project is running."
	if start {
		path, spoken = "factory_start", "No stopped factory project to start."
	}
	s.log.Log(logSource, "supervisor.fast_path", "", map[string]any{"path": path, "projects": changed})
	switch {
	case len(changed) > 0 && start:
		spoken = fmt.Sprintf("Started the factory for %s.", strings.Join(names, ", "))
	case len(changed) > 0:
		spoken = fmt.Sprintf("Stopped the factory for %s. The plan is kept.", strings.Join(names, ", "))
	case len(errs) > 0:
		spoken = "Could not update the factory: " + strings.Join(errs, "; ") + "."
	}
	return voice.ToolResult{Spoken: spoken, Scheduling: voice.UrgencyInterrupt}
}
