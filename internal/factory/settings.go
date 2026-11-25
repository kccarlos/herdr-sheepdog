package factory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
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

func (e *Engine) decorateModelRows(rows []flow.SettingRow) []flow.SettingRow {
	e.mu.Lock()
	probes := maps.Clone(e.cliProbes)
	e.mu.Unlock()
	for i := range rows {
		role, ok := strings.CutPrefix(rows[i].Name, "models.")
		for _, cli := range []string{"claude", world.CodexCLI, world.AgyCLI} {
			if ok && launchCapability(role)(flow.Candidate{CLI: cli}).Why == "" {
				rows[i].CLIs = append(rows[i].CLIs, cli)
				if p := probes[cli]; p.known && p.facts.Models != nil {
					if rows[i].Models == nil {
						rows[i].Models = map[string][]string{}
					}
					rows[i].Models[cli] = slices.Sorted(maps.Keys(p.facts.Models))
				}
			}
		}
	}
	return rows
}

// SettingRows returns the settings table for project, or DefaultSettings().Rows() when project is empty (M4).
func (e *Engine) SettingRows(project string) ([]flow.SettingRow, error) {
	if project = strings.TrimSpace(project); project == "" {
		return e.decorateModelRows(flow.DefaultSettings().Rows()), nil
	}
	s, err := e.loadProjectState(project)
	if err != nil {
		return nil, err
	}
	return e.decorateModelRows(s.Settings.Rows()), nil
}

// ProposeSetting opens an engine-written rule ask with Set{Name, Value, Was} for a paired session (SE1–SE6, SE13).
func (e *Engine) ProposeSetting(project, name, value, deviceID string) (flow.Ask, error) {
	project, name, value, deviceID = strings.TrimSpace(project), strings.TrimSpace(name), strings.TrimSpace(value), strings.TrimSpace(deviceID)
	if !proofSourceOK(ProofInputTap, deviceID) {
		return flow.Ask{}, e.refuse("", "proof_required", "Changing a setting requires a paired device or browser session.")
	}
	var probed flow.ResolveFacts
	var probedCLIs []string
	cands, cErr := flow.ParseCandidates(value)
	role, isModels := strings.CutPrefix(name, "models.")
	if isModels && value != "" && cErr == nil {
		_, wErr := flow.DefaultSettings().With(name, value)
		if _, sErr := e.loadProjectState(project); wErr == nil && sErr == nil {
			for _, c := range cands {
				if launchCapability(role)(c).Why == "" && !slices.Contains(probedCLIs, c.CLI) {
					probedCLIs = append(probedCLIs, c.CLI)
				}
			}
			if len(probedCLIs) > 0 && e.probeLaunch != nil {
				probed = e.probeLaunch(context.Background(), probedCLIs)
			} else if len(probedCLIs) > 0 && e.HC != nil && e.HC.Socket != "" {
				probed = probeResolveFacts(context.Background(), probedCLIs, hcExecRunner(e.HC.Socket))
			} else {
				probedCLIs = nil
			}
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(probedCLIs) > 0 && e.cliProbes == nil {
		e.cliProbes = map[string]cachedCLIProbe{}
	}
	for _, cli := range probedCLIs {
		f, known := probed.CLIs[cli]
		e.cliProbes[cli] = cachedCLIProbe{at: e.now(), facts: f, known: known}
	}
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
	for _, c := range cands {
		if !isModels {
			break
		}
		if r := launchCapability(role)(c); r.Why != "" {
			return flow.Ask{}, e.refuse(project, "not_launchable", r.Why)
		} else if cf, ok := probed.CLIs[c.CLI]; ok && (!cf.Installed || !cf.SignedIn) {
			p := map[string][2]string{"claude": {"Claude Code", "run: claude auth login"}, world.CodexCLI: {"Codex CLI", "run: codex login"}, world.AgyCLI: {"Antigravity CLI (agy)", "run agy without arguments and sign in"}}[c.CLI]
			fix := map[bool]string{true: p[1], false: "install " + p[0] + ", then " + p[1]}[cf.Installed]
			return flow.Ask{}, e.refuse(project, "not_launchable", c.CLI+map[bool]string{true: " is not signed in", false: " is not installed"}[cf.Installed], fix)
		} else if ok && len(cf.Models) > 0 && !cf.Models[c.Model] {
			return flow.Ask{}, e.refuse(project, "not_launchable", fmt.Sprintf("%s does not offer %s. It offers: %s.", c.CLI, c.Model, strings.Join(slices.Sorted(maps.Keys(cf.Models)), ", ")))
		}
	}
	now, cancelled := e.now(), []string{}
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
