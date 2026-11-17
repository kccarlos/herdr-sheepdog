package supervisor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

const (
	// FactoryAskPrefix prefixes PendingAction.ID for open factory owner asks.
	FactoryAskPrefix = "fask-"
	// FactoryPanePrefix prefixes PendingAction.PaneID for factory project asks.
	FactoryPanePrefix = "factory:"

	defaultFactoryPoll = 2 * time.Second
)

func isFactoryAskID(id string) bool {
	return strings.HasPrefix(id, FactoryAskPrefix)
}

func factoryAskRawID(id string) string {
	return strings.TrimPrefix(id, FactoryAskPrefix)
}

func isFactoryPane(paneID string) bool {
	return strings.HasPrefix(paneID, FactoryPanePrefix)
}

func factoryPaneID(project string) string {
	return FactoryPanePrefix + project
}

// SetVoiceDevice records the authenticated paired-device ID of the active /ws
// voice connection (or "" when detached / local). Live voice answers record
// this device ID (or "voice-session" when attached without a paired ticket)
// on minted AnswerProofs.
func (s *Supervisor) SetVoiceDevice(deviceID string) {
	s.mu.Lock()
	s.voiceDevice = strings.TrimSpace(deviceID)
	s.mu.Unlock()
}

func (s *Supervisor) voiceDeviceLocked() string {
	dev := strings.TrimSpace(s.voiceDevice)
	if dev == "" || dev == "local" {
		return "voice-session"
	}
	return dev
}

// SyncFactoryAsks polls `owner asks --open --json` via Config.Factory and
// reconciles open factory asks into question cards.
func (s *Supervisor) SyncFactoryAsks(ctx context.Context) error {
	if s.cfg.Factory == nil {
		return nil
	}
	s.factorySyncMu.Lock()
	defer s.factorySyncMu.Unlock()

	resp, rejected, err := s.cfg.Factory.AsksPartial(ctx, factory.AsksFilter{Open: true})
	if err != nil {
		if errors.Is(err, factory.ErrOwnerUnsupported) || ctx.Err() != nil {
			return nil
		}
		s.mu.Lock()
		msg := err.Error()
		already := s.factoryWarned[msg]
		if s.factoryWarned == nil {
			s.factoryWarned = map[string]bool{}
		}
		s.factoryWarned[msg] = true
		s.mu.Unlock()
		if !already {
			s.log.Log("factory", "factory.sync_error", "", map[string]any{"error": msg})
		}
		return err
	}

	activeWarnings := make(map[string]bool, len(rejected))
	var firstReject error
	for _, rej := range rejected {
		if firstReject == nil {
			firstReject = rej.Err
		}
		key := "ask:" + rej.ID + ":" + rej.Err.Error()
		activeWarnings[key] = true
		s.mu.Lock()
		already := s.factoryWarned[key]
		if s.factoryWarned == nil {
			s.factoryWarned = map[string]bool{}
		}
		s.factoryWarned[key] = true
		s.mu.Unlock()
		if !already {
			pane := ""
			if strings.TrimSpace(rej.Project) != "" {
				pane = factoryPaneID(rej.Project)
			}
			s.log.Log("factory", "factory.ask_rejected", pane, map[string]any{
				"ask_id": rej.ID,
				"kind":   rej.Kind,
				"code":   factory.ErrorCode(rej.Err),
				"error":  rej.Err.Error(),
			})
		}
	}
	s.mu.Lock()
	for k := range s.factoryWarned {
		if !activeWarnings[k] {
			delete(s.factoryWarned, k)
		}
	}
	s.mu.Unlock()

	if ierr := s.IngestFactoryAsks(resp.Asks); ierr != nil && firstReject == nil {
		firstReject = ierr
	}
	return firstReject
}

// IngestFactoryAsks validates asks against owner contract v1 (refusing any
// unknown ask kind before creating a card while still reconciling valid asks)
// and updates Supervisor's pending / question store.
func (s *Supervisor) IngestFactoryAsks(asks []factory.Ask) error {
	openList := make([]factory.Ask, 0, len(asks))
	openMap := make(map[string]factory.Ask, len(asks))
	var firstErr error
	for _, ask := range asks {
		if err := ask.Validate(); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			s.log.Log("factory", "factory.ask_rejected", factoryPaneID(ask.Project), map[string]any{
				"ask_id": ask.ID,
				"kind":   string(ask.Kind),
				"code":   factory.ErrorCode(err),
				"error":  err.Error(),
			})
			continue
		}
		if ask.Status != "open" {
			continue
		}
		pid := FactoryAskPrefix + ask.ID
		openList = append(openList, ask)
		openMap[pid] = ask
	}

	type newAskPush struct {
		pending state.PendingAction
		ask     factory.Ask
		spoken  string
		muted   bool
	}
	var announce []newAskPush

	s.mu.Lock()
	if s.factoryAsks == nil {
		s.factoryAsks = map[string]factory.Ask{}
	}

	// Cancel any previously open factory ask that is no longer open in the store.
	if cur, has := s.store.Pending(); has && isFactoryAskID(cur.ID) {
		if _, stillOpen := openMap[cur.ID]; !stillOpen {
			delete(s.factoryAsks, cur.ID)
			s.clearPending(cur, "prompt_gone")
		}
	}
	for _, q := range s.store.Questions() {
		if !isFactoryAskID(q.ID) {
			continue
		}
		if _, stillOpen := openMap[q.ID]; !stillOpen {
			delete(s.factoryAsks, q.ID)
			s.dropQuestionLocked(q)
			s.log.Log(logSource, "pending.cancelled", q.PaneID, map[string]any{
				"id": q.ID, "kind": q.Kind, "reason": "prompt_gone",
			})
		}
	}

	for _, ask := range openList {
		pid := FactoryAskPrefix + ask.ID
		s.factoryAsks[pid] = ask
		if _, exists, ok := s.questionByID(pid); ok {
			_ = exists
			continue
		}
		muted := false
		if ask.Folder != nil && strings.TrimSpace(*ask.Folder) != "" {
			_, muted = s.SpaceMutedPath(*ask.Folder)
		}
		p := s.newFactoryPendingLocked(ask)
		spoken := s.setPendingMuted(p, muted)
		announce = append(announce, newAskPush{
			pending: p,
			ask:     ask,
			spoken:  spoken,
			muted:   muted,
		})
	}
	s.mu.Unlock()

	if len(announce) > 0 {
		s.syncNames()
	}
	for _, item := range announce {
		if item.muted {
			continue
		}
		s.notifyFactoryAsk(item.pending, item.ask, item.spoken)
		s.pushTagged(item.pending.PaneID, item.spoken, voice.UrgencyInterrupt, map[string]any{
			"from":       "daemon",
			"agent":      item.pending.Agent,
			"pending_id": item.pending.ID,
		})
	}
	return firstErr
}

func (s *Supervisor) newFactoryPendingLocked(ask factory.Ask) state.PendingAction {
	now := s.cfg.Now()
	paneID := factoryPaneID(ask.Project)
	agentName := s.cfg.Redactor.String(strings.TrimSpace(ask.Name))
	if agentName == "" {
		agentName = s.cfg.Redactor.String(ask.Project)
	}
	question := s.cfg.Redactor.String(strings.TrimSpace(ask.Question))
	cmd := s.cfg.Redactor.String(factoryAskCommand(ask))

	opts := make([]state.Option, 0, len(ask.Options))
	for i, opt := range ask.Options {
		clean := s.cfg.Redactor.String(opt)
		rec := opt == ask.Recommend
		opts = append(opts, state.Option{
			Label:       clean,
			Keys:        []string{strconv.Itoa(i + 1)},
			Default:     rec,
			Recommended: rec,
		})
	}
	yesKeys, noKeys := factoryYesNoKeys(ask, opts)
	summary := s.cfg.Redactor.String(prose.Spoken(factorySpokenSummary(agentName, question, opts, s.ceoChineseLocked()), 0))

	return state.PendingAction{
		ID:            FactoryAskPrefix + ask.ID,
		PaneID:        paneID,
		Agent:         agentName,
		Kind:          state.PendingAnswerPrompt,
		SpokenSummary: summary,
		Question:      question,
		Command:       cmd,
		Options:       opts,
		YesKeys:       yesKeys,
		NoKeys:        noKeys,
		CreatedAt:     now,
	}
}

func factoryAskCommand(ask factory.Ask) string {
	var parts []string
	if ask.Kind != "" && ask.Kind != factory.AskKindDecision {
		parts = append(parts, string(ask.Kind))
	}
	switch {
	case ask.Task != nil && strings.TrimSpace(*ask.Task) != "":
		parts = append(parts, strings.TrimSpace(*ask.Task))
	case ask.Milestone != nil && strings.TrimSpace(*ask.Milestone) != "":
		parts = append(parts, strings.TrimSpace(*ask.Milestone))
	}
	return strings.Join(parts, " · ")
}

func factoryYesNoKeys(ask factory.Ask, opts []state.Option) (yesKeys, noKeys []string) {
	if len(opts) != 2 {
		return nil, nil
	}
	if ask.Kind != factory.AskKindDecision {
		if factoryOptionDeclines(opts[1].Label) && !factoryOptionDeclines(opts[0].Label) {
			return append([]string(nil), opts[0].Keys...), append([]string(nil), opts[1].Keys...)
		}
		if factoryOptionDeclines(opts[0].Label) && !factoryOptionDeclines(opts[1].Label) {
			return append([]string(nil), opts[1].Keys...), append([]string(nil), opts[0].Keys...)
		}
		return append([]string(nil), opts[0].Keys...), append([]string(nil), opts[1].Keys...)
	}
	l0 := strings.ToLower(strings.TrimSpace(opts[0].Label))
	l1 := strings.ToLower(strings.TrimSpace(opts[1].Label))
	if yesWords[l0] && noWords[l1] {
		return append([]string(nil), opts[0].Keys...), append([]string(nil), opts[1].Keys...)
	}
	return nil, nil
}

func factoryOptionDeclines(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	for _, w := range []string{"no", "deny", "cancel", "skip", "hold", "keep", "reject", "decline", "send back", "don't", "do not"} {
		if l == w || strings.HasPrefix(l, w+" ") || strings.HasPrefix(l, w+",") {
			return true
		}
	}
	return false
}

func factorySpokenSummary(agent, question string, opts []state.Option, zh bool) string {
	labels := make([]string, 0, len(opts))
	for _, o := range opts {
		labels = append(labels, o.Label)
	}
	q := strings.TrimRight(strings.TrimSpace(question), ".。")
	if zh {
		return fmt.Sprintf("%s 正在询问：%s。选项：%s。", agent, q, strings.Join(labels, "，"))
	}
	if !strings.HasSuffix(q, "?") && !strings.HasSuffix(q, "!") {
		q += "."
	}
	return fmt.Sprintf("%s is asking: %s Options: %s.", agent, q, strings.Join(labels, ", "))
}

func (s *Supervisor) notifyFactoryAsk(p state.PendingAction, ask factory.Ask, spoken string) {
	if s.cfg.Notifier == nil || spoken == "" {
		return
	}
	n := push.Notification{
		EventID:   "factory-ask-" + ask.ID,
		PaneID:    p.PaneID,
		Agent:     s.cfg.Redactor.String(p.Agent),
		Text:      s.cfg.Redactor.String(spoken),
		NeedsYou:  true,
		Kind:      push.KindApproval,
		PendingID: p.ID,
	}
	for i, o := range p.Options {
		if o.Meta {
			continue
		}
		n.Options = append(n.Options, push.Option{
			Index: i + 1,
			Label: s.cfg.Redactor.String(o.Label),
		})
	}
	s.cfg.Notifier.Notify(n)
}

func (s *Supervisor) factoryAgentName(paneID string) string {
	if cur, has := s.store.Pending(); has && cur.PaneID == paneID && cur.Agent != "" {
		return cur.Agent
	}
	for _, q := range s.store.Questions() {
		if q.PaneID == paneID && q.Agent != "" {
			return q.Agent
		}
	}
	return ""
}

// stillAskingFactory checks the contract client rather than terminal screen-scraping
// for `fask-*` actions (docs/dev/factory/owner-contract.md §5.2 item 1).
func (s *Supervisor) stillAskingFactory(ctx context.Context, p state.PendingAction) (asking, known bool) {
	rawID := factoryAskRawID(p.ID)
	if s.cfg.Factory == nil {
		s.mu.Lock()
		ask, ok := s.factoryAsks[p.ID]
		s.mu.Unlock()
		return ok && ask.Status == "open", true
	}
	resp, _, err := s.cfg.Factory.AsksPartial(ctx, factory.AsksFilter{Open: true})
	if err != nil {
		s.log.Log(logSource, "supervisor.reconcile_error", p.PaneID, map[string]any{"error": err.Error(), "for": "factory_tap"})
		return false, false
	}
	for _, ask := range resp.Asks {
		if ask.ID == rawID && ask.Status == "open" {
			s.mu.Lock()
			if s.factoryAsks == nil {
				s.factoryAsks = map[string]factory.Ask{}
			}
			s.factoryAsks[p.ID] = ask
			s.mu.Unlock()
			return true, true
		}
	}
	return false, true
}

func resolveFactoryOption(p state.PendingAction, keys []string) (index int, label string, ok bool) {
	for i, o := range p.Options {
		if o.Meta {
			continue
		}
		if slices.Equal(o.Keys, keys) || (len(keys) == 1 && keys[0] == strconv.Itoa(i+1)) {
			return i + 1, o.Label, true
		}
	}
	return 0, "", false
}

func (s *Supervisor) lookupFactoryAskLocked(p state.PendingAction) (factory.Ask, error) {
	if ask, ok := s.factoryAsks[p.ID]; ok {
		if err := ask.Validate(); err != nil {
			return factory.Ask{}, err
		}
		return ask, nil
	}
	// Fallback after a daemon restart if the question survived in state.json
	// before SyncFactoryAsks refreshed s.factoryAsks: infer minimal metadata
	// only when Command names a valid AskKind; otherwise require a fresh sync.
	rawID := factoryAskRawID(p.ID)
	project := strings.TrimPrefix(p.PaneID, FactoryPanePrefix)
	kind := factory.AskKindDecision
	if p.Command != "" {
		head, _, _ := strings.Cut(p.Command, " · ")
		kind = factory.AskKind(strings.TrimSpace(head))
	}
	if !factory.ValidAskKind(kind) {
		return factory.Ask{}, factory.ErrUnknownAskKind
	}
	opts := make([]string, 0, len(p.Options))
	rec := ""
	for _, o := range p.Options {
		opts = append(opts, o.Label)
		if o.Recommended && rec == "" {
			rec = o.Label
		}
	}
	if rec == "" && len(opts) > 0 {
		rec = opts[0]
	}
	return factory.Ask{
		ID:        rawID,
		Project:   project,
		Name:      p.Agent,
		Kind:      kind,
		Gated:     kind.Gated(),
		Question:  p.Question,
		Options:   opts,
		Recommend: rec,
		At:        max(int64(1), p.CreatedAt.UnixMilli()),
		Status:    "open",
	}, nil
}

// mintAnswerProofLocked enforces the paired-ticket and live-voice-only source
// guard as a second line of defense inside proof minting (§5.2 items 2 & 3).
// Caller holds s.mu.
func (s *Supervisor) mintAnswerProofLocked(ask factory.Ask, optionIndex int, optionLabel string, in *shepherdInput, via, tapDeviceID, utteranceID string) (factory.AnswerProof, error) {
	if !factory.ValidAskKind(ask.Kind) {
		return factory.AnswerProof{}, factory.ErrUnknownAskKind
	}
	if in == nil || in.seq == 0 {
		return factory.AnswerProof{}, fmt.Errorf("%w: missing verified owner input", factory.ErrProofRejected)
	}
	var (
		inputKind string
		deviceID  string
	)
	switch via {
	case "tap":
		dev := strings.TrimSpace(tapDeviceID)
		if in.kind != InputTap || dev == "" || dev == "local" {
			return factory.AnswerProof{}, fmt.Errorf("%w: ticketless local tap cannot authorize factory ask %s", factory.ErrProofRejected, ask.ID)
		}
		inputKind = "tap"
		deviceID = dev
	default:
		if in.kind != InputVoice {
			return factory.AnswerProof{}, fmt.Errorf("%w: %s input cannot authorize factory ask %s", factory.ErrProofRejected, in.kind, ask.ID)
		}
		inputKind = "voice"
		deviceID = s.voiceDeviceLocked()
	}
	if s.cfg.FactoryProofs == nil {
		return factory.AnswerProof{}, fmt.Errorf("%w: proof store is not configured", factory.ErrProofRejected)
	}
	if strings.TrimSpace(utteranceID) == "" {
		utteranceID = fmt.Sprintf("%s-%d", inputKind, in.seq)
	}
	proof, err := s.cfg.FactoryProofs.Mint(factory.MintInput{
		AskID:       ask.ID,
		Project:     ask.Project,
		Kind:        ask.Kind,
		OptionIndex: optionIndex,
		OptionLabel: optionLabel,
		InputKind:   inputKind,
		InputSeq:    in.seq,
		UtteranceID: utteranceID,
		DeviceID:    deviceID,
		AnsweredAt:  s.cfg.Now(),
	})
	if err != nil {
		return factory.AnswerProof{}, err
	}
	s.log.Log("factory", "factory.proof_minted", factoryPaneID(ask.Project), map[string]any{
		"ask_id":       ask.ID,
		"proof_id":     proof.ProofID,
		"kind":         string(ask.Kind),
		"option_index": optionIndex,
		"input_kind":   inputKind,
		"device_id":    deviceID,
	})
	return proof, nil
}

// answerFactoryAskLocked mints an AnswerProof, invokes `engine owner answer`
// outside s.mu, and settles (or rolls back) the pending card. Caller holds
// s.paneSend(p.PaneID) and s.mu.
func (s *Supervisor) answerFactoryAskLocked(
	ctx context.Context,
	p state.PendingAction,
	keys []string,
	say func(label string) string,
	via string,
	extra map[string]any,
	in *shepherdInput,
	tapDeviceID string,
) (voice.ToolResult, error, bool) {
	alias := s.sayOf(p.PaneID)
	optIdx, optLabel, ok := resolveFactoryOption(p, keys)
	if !ok {
		return voice.ToolResult{Spoken: "I could not match that option for " + alias + ".", Scheduling: voice.UrgencyInterrupt}, ErrNoSuchOption, false
	}
	ask, err := s.lookupFactoryAskLocked(p)
	if err != nil {
		s.railBlockedLocked(p, keys, via, err.Error())
		return voice.ToolResult{Spoken: "I could not verify " + alias + "'s question.", Scheduling: voice.UrgencyInterrupt}, err, false
	}
	uttID := ""
	if u := utteranceOf(ctx); u != nil && u.id != "" {
		uttID = u.id
	}
	// Mint AnswerProof with the canonical contract option label from `ask.Options`
	// (so loopback verification matches the engine's record even when Redactor
	// masked the card's display label), while keeping `optLabel` for logs/speech.
	proofOptionLabel := optLabel
	if optIdx >= 1 && optIdx <= len(ask.Options) {
		proofOptionLabel = ask.Options[optIdx-1]
	}
	proof, err := s.mintAnswerProofLocked(ask, optIdx, proofOptionLabel, in, via, tapDeviceID, uttID)
	if err != nil {
		s.railBlockedLocked(p, keys, via, err.Error())
		return voice.ToolResult{Spoken: "Factory asks can only be answered from a paired phone or live voice.", Scheduling: voice.UrgencyInterrupt}, err, false
	}
	if s.cfg.Factory == nil {
		_ = s.cfg.FactoryProofs.Delete(proof.ProofID)
		err := errors.New("factory contract client is not configured")
		s.log.Log(logSource, "supervisor.confirm_error", p.PaneID, map[string]any{"id": p.ID, "keys": keys, "error": err.Error()})
		return voice.ToolResult{Spoken: "I could not send that to " + alias + ".", Scheduling: voice.UrgencyInterrupt}, err, false
	}

	s.invalidateBriefingLocked()
	s.mu.Unlock()
	_, callErr := s.cfg.Factory.AnswerIndex(ctx, ask.ID, optIdx, proof.ProofID)
	s.mu.Lock()

	if callErr != nil {
		_ = s.cfg.FactoryProofs.Delete(proof.ProofID)
		if errors.Is(callErr, factory.ErrAlreadyClosed) || errors.Is(callErr, factory.ErrContractNotFound) {
			delete(s.factoryAsks, p.ID)
			if cur, has := s.store.Pending(); has && cur.ID == p.ID {
				s.clearPending(p, "already_closed")
			} else {
				s.dropQuestionLocked(p)
				s.log.Log(logSource, "pending.cancelled", p.PaneID, map[string]any{"id": p.ID, "kind": p.Kind, "reason": "already_closed"})
			}
			return voice.ToolResult{Spoken: alias + "'s question is no longer open.", Scheduling: voice.UrgencyInterrupt}, ErrPendingGone, false
		}
		s.invalidateBriefingLocked()
		s.log.Log(logSource, "supervisor.confirm_error", p.PaneID, map[string]any{"id": p.ID, "keys": keys, "error": callErr.Error()})
		return voice.ToolResult{Spoken: "I could not send that to " + alias + ".", Scheduling: voice.UrgencyInterrupt}, callErr, false
	}

	delete(s.factoryAsks, p.ID)
	if cur, has := s.store.Pending(); has && cur.ID == p.ID {
		if err := s.store.ClearPending(); err != nil {
			s.log.Log(logSource, "supervisor.state_error", p.PaneID, map[string]any{"error": err.Error()})
		}
	}
	s.dropQuestionLocked(p)
	if s.answeredPrompt == nil {
		s.answeredPrompt = map[string]string{}
	}
	s.answeredPrompt[p.PaneID] = p.ID
	s.invalidateBriefingLocked()
	s.lastSubject = p.PaneID
	confirmed := map[string]any{
		"id":       p.ID,
		"answer":   optLabel,
		"keys":     keys,
		"via":      via,
		"proof_id": proof.ProofID,
	}
	for k, v := range extra {
		confirmed[k] = v
	}
	s.log.Log(logSource, "pending.confirmed", p.PaneID, confirmed)
	return voice.ToolResult{Spoken: say(optLabel), Scheduling: voice.UrgencyInterrupt}, nil, true
}

func (s *Supervisor) openFactoryQuestionForPane(paneID string) (state.PendingAction, bool) {
	if cur, has := s.store.Pending(); has && cur.Kind == state.PendingAnswerPrompt && cur.PaneID == paneID && isFactoryAskID(cur.ID) {
		return cur, true
	}
	for _, q := range s.store.Questions() {
		if q.Kind == state.PendingAnswerPrompt && q.PaneID == paneID && isFactoryAskID(q.ID) {
			return q, true
		}
	}
	return state.PendingAction{}, false
}

func (h brainHost) factoryAgents() []brain.AgentInfo {
	pending, hasPending := h.s.store.Pending()
	seen := map[string]bool{}
	var ordered []state.PendingAction
	if hasPending && isFactoryAskID(pending.ID) {
		seen[pending.PaneID] = true
		ordered = append(ordered, pending)
	}
	for _, q := range h.s.store.Questions() {
		if !isFactoryAskID(q.ID) || seen[q.PaneID] {
			continue
		}
		seen[q.PaneID] = true
		ordered = append(ordered, q)
	}
	out := make([]brain.AgentInfo, 0, len(ordered))
	for _, q := range ordered {
		out = append(out, h.factoryAgentInfo(q))
	}
	return out
}

func (h brainHost) factoryAgentInfo(q state.PendingAction) brain.AgentInfo {
	alias := state.Slug(q.Agent)
	if alias == "" {
		alias = strings.TrimPrefix(q.PaneID, FactoryPanePrefix)
	}
	spoken := q.Agent
	if spoken == "" {
		spoken = alias
	}
	info := brain.AgentInfo{
		PaneID:     q.PaneID,
		Alias:      alias,
		Spoken:     spoken,
		Kind:       "factory",
		State:      "blocked",
		StateSince: q.CreatedAt,
	}
	if !q.Held && !h.s.heldQuestion(q.ID) {
		info.LastBlock = q.Question
		pp := &brain.PendingPrompt{
			ID:       q.ID,
			Question: q.Question,
			AskedAt:  q.CreatedAt,
			YesKeys:  q.YesKeys,
			NoKeys:   q.NoKeys,
		}
		for _, o := range q.Options {
			pp.Options = append(pp.Options, brain.Option{Label: o.Label, Keys: o.Keys, Meta: o.Meta})
		}
		info.Pending = pp
	}
	return info
}

func (h brainHost) resolveFactory(name string) (brain.AgentInfo, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return brain.AgentInfo{}, false
	}
	slug := state.Slug(trimmed)
	for _, info := range h.factoryAgents() {
		if strings.EqualFold(info.PaneID, trimmed) ||
			strings.EqualFold(info.Alias, trimmed) ||
			strings.EqualFold(info.Spoken, trimmed) ||
			(slug != "" && (info.Alias == slug || state.Slug(info.Spoken) == slug)) {
			return info, true
		}
	}
	return brain.AgentInfo{}, false
}

func (h brainHost) answerFactoryPrompt(ctx context.Context, paneID string, keys []string) (string, error) {
	s := h.s
	ps := s.paneSend(paneID)
	ps.Lock()
	defer ps.Unlock()
	defer s.pushReask()

	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.openFactoryQuestionForPane(paneID)
	if !ok {
		return "", fmt.Errorf("%w: %s has no pending factory ask now", brain.ErrNotPermitted, s.aliasOf(paneID))
	}
	if p.Held || s.heldQuestion(p.ID) {
		return "", fmt.Errorf("%w: %s's question is held in a muted space; answer it on your paired phone or unmute the space first; nothing was sent", brain.ErrNotPermitted, s.aliasOf(paneID))
	}
	allowed := allowedKeys(p)
	for _, k := range keys {
		if !allowed[k] {
			return "", fmt.Errorf("%w: key %q is not in the prompt's option set; ask the user to choose an available option", brain.ErrNotPermitted, k)
		}
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("%w: no keys; ask the user to choose an available option", brain.ErrNotPermitted)
	}
	if !selectedBy(p, keys) {
		return "", fmt.Errorf("%w: keys %v mix more than one option; ask the user to choose one option", brain.ErrNotPermitted, keys)
	}
	in, reason := s.railInputLocked(p, keys)
	if reason != "" {
		if last := s.lastInputLocked(); last != nil && reason == s.displacedLocked(p, last) {
			if line := s.reaskLocked(p, last); line != "" {
				if tr := typedReasksOf(ctx); tr != nil {
					tr.add(keyOf(p), p.PaneID, line)
				} else {
					s.queueReaskLocked(p, line)
				}
				reason += "; the daemon has told the user why and asked them again, so do not ask again yourself"
			} else {
				reason += "; the daemon has already told the user why and asked them again"
			}
		} else {
			reason += "; ask the user to answer by live voice or on the paired phone"
		}
		return "", fmt.Errorf("%w: %s", brain.ErrNotPermitted, reason)
	}
	in.used = true
	alias, zh := s.sayOf(paneID), s.ceoChineseLocked()
	res, err, sent := s.answerFactoryAskLocked(ctx, p, keys, func(l string) string {
		return toldLine(alias, l, zh)
	}, "brain", nil, in, "")
	if !sent {
		in.used = false
		if errors.Is(err, factory.ErrProofRejected) || errors.Is(err, factory.ErrUnknownAskKind) {
			return "", fmt.Errorf("%w: %v", brain.ErrNotPermitted, err)
		}
		return "", fmt.Errorf("%s (%v)", strings.TrimRight(res.Spoken, "."), err)
	}
	return res.Spoken, nil
}
