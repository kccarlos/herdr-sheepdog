package factory

// PR 14.3, TDD step 1: failing tests for ProposeSetting launch checks, saved
// models resolving without restarting live runs, and SettingRows running no CLI
// (plan.md 14.3, design.md 10.1 SE1/SE8, KB-SET-9, KB-LCH-19, KB-LCH-23, KB-REV-69).

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// TestModelsProposeLaunchChecks covers Codex as retro and plan_reviewer, CLI
// not installed, CLI not signed in, model the account does not offer (listing
// what it offers), and inconclusive/unknown-list probes not refusing.
func TestModelsProposeLaunchChecks(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	facts := flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{
		"claude": {Installed: true, SignedIn: true},
		"codex":  {Installed: true, SignedIn: true},
		"agy":    {Installed: true, SignedIn: true, Models: map[string]bool{"gemini-4-carbon": true, "gemini-4-neon": true}},
	}}
	r.eng.probeLaunch = func(context.Context, []string) flow.ResolveFacts { return facts }

	// Codex as retro and as plan_reviewer refused with launchCapability's sentence.
	for _, tc := range []struct{ key, want string }{
		{"models.retro", "the engine cannot launch codex as retro yet"},
		{"models.plan_reviewer", "the engine cannot launch codex as plan_reviewer yet"},
	} {
		if _, err := r.eng.ProposeSetting(r.id, tc.key, "codex/gpt-6.2-sol/medium", "dev-browser-1"); refusalCode(err) != "not_launchable" || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ProposeSetting(%s, codex) = %v, want not_launchable containing %q", tc.key, err, tc.want)
		}
	}

	// CLI not installed (KB-LCH-23).
	facts.CLIs["agy"] = flow.CLIFacts{Installed: false}
	if _, err := r.eng.ProposeSetting(r.id, "models.worker", "agy/gemini-4-neon/medium", "dev-browser-1"); refusalCode(err) != "not_launchable" || !strings.Contains(err.Error(), "agy is not installed") {
		t.Errorf("not installed = %v, want not_launchable 'agy is not installed'", err)
	}

	// CLI not signed in (even with a signed-in fallback second).
	facts.CLIs["codex"] = flow.CLIFacts{Installed: true, SignedIn: false}
	if _, err := r.eng.ProposeSetting(r.id, "models.reviewer", "codex/gpt-6.2-sol/medium, claude/sonnet/medium", "dev-browser-1"); refusalCode(err) != "not_launchable" || !strings.Contains(err.Error(), "codex is not signed in") {
		t.Errorf("not signed in = %v, want not_launchable 'codex is not signed in'", err)
	}

	// Model the account does not offer lists what it offers (KB-LCH-19, KB-REV-69).
	facts.CLIs["agy"] = flow.CLIFacts{Installed: true, SignedIn: true, Models: map[string]bool{"gemini-4-carbon": true, "gemini-4-neon": true}}
	if _, err := r.eng.ProposeSetting(r.id, "models.worker", "agy/gemini-4-neo/medium", "dev-browser-1"); refusalCode(err) != "not_launchable" ||
		!strings.Contains(err.Error(), "agy does not offer gemini-4-neo") ||
		!strings.Contains(err.Error(), "gemini-4-carbon") || !strings.Contains(err.Error(), "gemini-4-neon") {
		t.Errorf("unoffered model = %v, want not_launchable listing offered models", err)
	}
	if len(r.state().Asks) != 0 {
		t.Fatalf("refused proposals mutated Asks: %+v", r.state().Asks)
	}

	// Inconclusive probe (CLI absent from facts.CLIs) does not refuse.
	facts = flow.ResolveFacts{}
	if ask, err := r.eng.ProposeSetting(r.id, "models.worker", "agy/gemini-4-neon/medium", "dev-browser-1"); err != nil || ask.Set == nil || ask.Set.Name != "models.worker" {
		t.Fatalf("inconclusive probe refused: ask=%+v err=%v", ask, err)
	}
	// Known signed-in CLI with unknown model list (claude Models==nil) does not refuse.
	facts = flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{"claude": {Installed: true, SignedIn: true}}}
	if ask, err := r.eng.ProposeSetting(r.id, "models.worker", "claude/sonnet-next/medium", "dev-browser-1"); err != nil || ask.Set == nil {
		t.Fatalf("unknown model list refused: ask=%+v err=%v", ask, err)
	}
}

// TestModelsSaveResolvesNextAndKeepsLiveRun verifies a saved models list is
// returned by flow.Candidates on the next resolve (KB-SET-9) and a live run is
// not restarted (SE8, KB-LCH-23).
func TestModelsSaveResolvesNextAndKeepsLiveRun(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	okFacts := flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{
		"claude": {Installed: true, SignedIn: true},
		"agy":    {Installed: true, SignedIn: true, Models: map[string]bool{"gemini-4-neon": true}},
	}}
	r.eng.probeLaunch = func(context.Context, []string) flow.ResolveFacts { return okFacts }

	ask, err := r.eng.ProposeSetting(r.id, "models.worker", "agy/gemini-4-neon/medium, claude/sonnet/medium", "dev-browser-1")
	if err != nil {
		t.Fatalf("ProposeSetting(models.worker): %v", err)
	}
	wantQ := "This project: worker models agy/gemini-4-neon/medium then claude/sonnet/medium (now claude/sonnet/medium). Save?"
	if ask.Kind != flow.KindRule || ask.Question != wantQ || ask.Set == nil || ask.Set.Name != "models.worker" {
		t.Fatalf("proposed models ask = %+v, want question %q", ask, wantQ)
	}
	prf, err := r.eng.MintProof(ask.ID, flow.RuleOptionSave, ProofInputTap, "dev-browser-1")
	if err != nil || r.eng.Answer(ask.ID, flow.RuleOptionSave, prf) != nil {
		t.Fatalf("Save models.worker failed: err=%v", err)
	}
	st := r.state()
	wantCands := []flow.Candidate{
		{CLI: "agy", Model: "gemini-4-neon", Effort: "medium"},
		{CLI: "claude", Model: "sonnet", Effort: "medium"},
	}
	if got := flow.Candidates(flow.RoleWorker, flow.Task{}, st.Settings); !slices.Equal(got, wantCands) {
		t.Fatalf("flow.Candidates(worker) = %+v, want %+v", got, wantCands)
	}
	if in, why := flow.Resolve(flow.RoleWorker, flow.Task{}, st.Settings, okFacts); why != "" || in.CLI != "agy" || in.Model != "gemini-4-neon" {
		t.Fatalf("Resolve(worker) = (%+v, %q), want agy/gemini-4-neon", in, why)
	}

	// Next pass must not kill or replace the live worker already running on T1.
	workerBefore := r.state().Tasks[0].Attempts[0].Run.Address
	r.pass()
	after := r.state().Tasks[0].Attempts[0].Run
	if after == nil || after.Address != workerBefore || after.State == flow.PhaseEnded {
		t.Fatalf("live worker restarted after models save: before=%q after=%+v", workerBefore, after)
	}
	if term, err := r.client.Get(context.Background(), workerBefore); err != nil || term.State != "live" {
		t.Fatalf("live worker terminal %q not live after models save: term=%+v err=%v", workerBefore, term, err)
	}
}

// TestSettingRowsRunsNoCLIAndPopulatesModels checks SettingRows never runs a CLI
// probe (fetched every 3 s) and attaches launchable CLIs and cached model names.
func TestSettingRowsRunsNoCLIAndPopulatesModels(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	r.eng.probeLaunch = func(context.Context, []string) flow.ResolveFacts {
		t.Fatal("SettingRows must never run a CLI probe")
		return flow.ResolveFacts{}
	}
	r.eng.cliProbes = map[string]cachedCLIProbe{
		"agy":    {at: r.now, known: true, facts: flow.CLIFacts{Installed: true, SignedIn: true, Models: map[string]bool{"gemini-4-carbon": true, "gemini-4-neon": true}}},
		"claude": {at: r.now, known: true, facts: flow.CLIFacts{Installed: true, SignedIn: true}},
	}
	for _, proj := range []string{"", r.id} {
		rows, err := r.eng.SettingRows(proj)
		if err != nil {
			t.Fatalf("SettingRows(%q): %v", proj, err)
		}
		raw, _ := json.Marshal(rows)
		var wire []struct {
			Name   string              `json:"name"`
			Label  string              `json:"label"`
			Top    bool                `json:"top"`
			CLIs   []string            `json:"clis"`
			Models map[string][]string `json:"models"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		byName := make(map[string]int, len(wire))
		for i, w := range wire {
			byName[w.Name] = i
		}
		wi, ok := byName["models.worker"]
		if !ok || !wire[wi].Top || wire[wi].Label != "worker models" {
			t.Fatalf("SettingRows(%q) missing Top models.worker row", proj)
		}
		if !slices.Contains(wire[wi].CLIs, "claude") || !slices.Contains(wire[wi].CLIs, "codex") || !slices.Contains(wire[wi].CLIs, "agy") ||
			!slices.Contains(wire[wi].Models["agy"], "gemini-4-neon") || len(wire[wi].Models["claude"]) != 0 {
			t.Errorf("models.worker clis=%v models=%v", wire[wi].CLIs, wire[wi].Models)
		}
		for _, role := range []string{"models.retro", "models.plan_reviewer"} {
			if ri, ok := byName[role]; !ok || !wire[ri].Top || slices.Contains(wire[ri].CLIs, "codex") || !slices.Contains(wire[ri].CLIs, "claude") || !slices.Contains(wire[ri].CLIs, "agy") {
				t.Errorf("SettingRows(%q) %s bad or offers codex: ok=%v", proj, role, ok)
			}
		}
	}
}

// TestModelsProposeResetToDefaultChecksLaunch verifies resetting models.<role>
// via empty value ("", SE6) probes and runs launch checks on the resolved
// default candidate list instead of skipping them (KB-LCH-23).
func TestModelsProposeResetToDefaultChecksLaunch(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	r.edit(func(s *flow.State) {
		s.Settings.Models = map[string][]flow.Candidate{
			flow.RoleWorker: {{CLI: "agy", Model: "gemini-4-neon", Effort: "medium"}},
		}
	})
	r.eng.probeLaunch = func(_ context.Context, clis []string) flow.ResolveFacts {
		return flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{
			"claude": {Installed: false},
			"agy":    {Installed: true, SignedIn: true},
		}}
	}
	if _, err := r.eng.ProposeSetting(r.id, "models.worker", "", "dev-browser-1"); refusalCode(err) != "not_launchable" || !strings.Contains(err.Error(), "claude is not installed") {
		t.Fatalf("reset models.worker to uninstalled default = %v, want not_launchable 'claude is not installed'", err)
	}
	if len(r.state().Asks) != 0 {
		t.Fatalf("refused reset mutated Asks: %+v", r.state().Asks)
	}
}
