package factory

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

func (e *Engine) loadProjectState(project string) (flow.State, error) {
	if project == "" || project == "." || project == ".." || strings.ContainsAny(project, `/\`) {
		return flow.State{}, ErrProjectNotFound
	}
	s, err := LoadState(e.Paths.State(project))
	if os.IsNotExist(err) {
		err = ErrProjectNotFound
	}
	return s, err
}

// SettingRows returns the settings table for project, or DefaultSettings().Rows() when project is empty (M4).
func (e *Engine) SettingRows(project string) ([]flow.SettingRow, error) {
	if project = strings.TrimSpace(project); project == "" {
		return flow.DefaultSettings().Rows(), nil
	}
	s, err := e.loadProjectState(project)
	if err != nil {
		return nil, err
	}
	return s.Settings.Rows(), nil
}

// ProposeSetting opens an engine-written rule ask with Set{Name, Value, Was} for a paired session (SE1–SE6, SE13).
func (e *Engine) ProposeSetting(project, name, value, deviceID string) (flow.Ask, error) {
	project, name, value, deviceID = strings.TrimSpace(project), strings.TrimSpace(name), strings.TrimSpace(value), strings.TrimSpace(deviceID)
	if !proofSourceOK(ProofInputTap, deviceID) {
		return flow.Ask{}, e.refuse("", "proof_required", "Changing a setting requires a paired device or browser session.")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.loadProjectState(project)
	if errors.Is(err, ErrProjectNotFound) {
		return flow.Ask{}, e.refuse("", "not_found", fmt.Sprintf("No project %q exists.", project))
	} else if err == nil {
		err = e.freshLocked(project, s)
	}
	if err != nil {
		return flow.Ask{}, err
	}
	before := s.Settings.Rows()
	idx := slices.IndexFunc(before, func(r flow.SettingRow) bool { return r.Name == name })
	if idx < 0 {
		return flow.Ask{}, e.refuse(project, "unknown_setting", fmt.Sprintf("Unknown setting %q.", name))
	}
	next, err := s.Settings.With(name, value)
	if err != nil {
		return flow.Ask{}, e.refuse(project, "out_of_range", err.Error())
	}
	after := next.Rows()[idx]
	if after.Value == before[idx].Value {
		return flow.Ask{}, e.refuse(project, "unchanged", fmt.Sprintf("%s is already %s", after.Label, cmp.Or(after.Value, "empty")))
	}
	now, cancelled := e.now(), []string(nil)
	for ai, a := range s.Asks {
		if a.Status == flow.AskStatusOpen && a.Set != nil && a.Set.Name == name {
			s.Asks[ai], _, _ = flow.StepAsk(a, flow.AskEvent{Kind: flow.EvAskCancel}, now)
			cancelled = append(cancelled, a.ID)
		}
	}
	chg := flow.SettingChange{Name: name, Value: after.Value, Was: before[idx].Value}
	ask, row, dec, code, why := e.openEngineAskLocked(project, &s, flow.Ask{Kind: flow.KindRule, Question: flow.RuleQuestion(chg), Options: []string{flow.RuleOptionSave, flow.RuleOptionKeep}, Recommend: flow.RuleOptionSave, AskedBy: "engine", Set: &chg})
	if code != "" {
		return flow.Ask{}, e.refuse(project, code, why)
	}
	if err := e.commitLocked(project, &s, LogLine{By: "engine", Input: map[string]any{"verb": "set", "id": ask.ID, "name": chg.Name, "value": chg.Value, "was": chg.Was, "device_id": deviceID}, Rule: row, Asked: dec.Asked, Decision: "ask opened: " + ask.ID, Why: ask.Question}); err != nil {
		return flow.Ask{}, err
	}
	for _, cid := range cancelled {
		e.notifyChangeLocked(project, s.Seq, Change{Kind: flow.EvAskCancel, AskID: cid})
	}
	e.notifyChangeLocked(project, s.Seq, Change{Kind: flow.EvAskOpen, AskID: ask.ID})
	return ask, nil
}
