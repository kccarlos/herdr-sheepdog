package flow

import (
	"slices"
	"strings"
	"testing"
)

func allCLIsSignedIn() ResolveFacts {
	ok := CLIFacts{Installed: true, SignedIn: true}
	return ResolveFacts{CLIs: map[string]CLIFacts{"claude": ok, "codex": ok, "agy": ok}}
}

func TestResolveFirstCandidateWins(t *testing.T) {
	task := Task{Request: map[string][]Candidate{
		RoleWorker: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "high"}, {CLI: "claude", Model: "sonnet", Effort: "medium"}},
	}}
	in, why := Resolve(RoleWorker, task, DefaultSettings(), allCLIsSignedIn())
	want := Intent{Role: RoleWorker, CLI: "codex", Model: "gpt-6.2-sol", Effort: "high", Perm: PermWorktree, Continue: ContinueFresh}
	if why != "" || in != want {
		t.Fatalf("Resolve = (%+v, %q), want (%+v, \"\")", in, why, want)
	}
}

func TestResolveSkipsSignedOutCLI(t *testing.T) {
	facts := ResolveFacts{CLIs: map[string]CLIFacts{
		"codex":  {Installed: true, SignedIn: false},
		"claude": {Installed: true, SignedIn: true},
	}}
	task := Task{Request: map[string][]Candidate{
		RoleWorker: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}, {CLI: "claude", Model: "opus", Effort: "high"}},
	}}
	in, why := Resolve(RoleWorker, task, DefaultSettings(), facts)
	if why != "" || in.CLI != "claude" || in.Model != "opus" || in.Effort != "high" {
		t.Fatalf("Resolve = (%+v, %q), want claude/opus/high", in, why)
	}
	// With only the signed-out CLI available (no signed-in fallback), its reason is returned.
	s := DefaultSettings()
	s.Models = map[string][]Candidate{RoleWorker: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}}}
	if _, why := Resolve(RoleWorker, Task{}, s, facts); why != "codex is not signed in" {
		t.Fatalf("signed-out reason = %q, want %q", why, "codex is not signed in")
	}
}

func TestResolveReviewerDiffersFromWorker(t *testing.T) {
	facts := allCLIsSignedIn()
	facts.Worker = Candidate{CLI: "claude", Model: "sonnet", Effort: "medium"}
	s := DefaultSettings()
	s.Models = map[string][]Candidate{
		RoleReviewer: {{CLI: "claude", Model: "sonnet", Effort: "high"}, {CLI: "claude", Model: "opus", Effort: "medium"}},
	}
	in, why := Resolve(RoleReviewer, Task{}, s, facts)
	if why != "" || in.CLI != "claude" || in.Model != "opus" || in.Perm != PermReadOnly || in.Continue != ContinueFresh {
		t.Fatalf("Resolve(reviewer) = (%+v, %q), want claude/opus read-only", in, why)
	}
	// When every candidate matches the worker in (cli, model), refuse with design 9.1's wording.
	s.Models[RoleReviewer] = []Candidate{{CLI: "claude", Model: "sonnet", Effort: "high"}}
	for _, role := range []string{RoleReviewer, RoleRetro} {
		s.Models[role] = []Candidate{{CLI: "claude", Model: "sonnet", Effort: "high"}}
		if _, why := Resolve(role, Task{}, s, facts); why != "the reviewer must differ from the worker" {
			t.Fatalf("Resolve(%s) = %q, want %q", role, why, "the reviewer must differ from the worker")
		}
	}
}

func TestResolveModelsOfferedAndFirstFailingReason(t *testing.T) {
	facts := ResolveFacts{CLIs: map[string]CLIFacts{
		"codex": {Installed: true, SignedIn: false},
		"agy":   {Installed: true, SignedIn: true, Models: map[string]bool{"gemini-4-neon-medium": true}},
	}}
	s := DefaultSettings()
	s.Models = map[string][]Candidate{
		RoleWorker: {
			{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"},
			{CLI: "agy", Model: "claude-opus-5-5-medium", Effort: "medium"},
		},
	}
	// First candidate fails signed-in, second fails model-offered -> first failing reason wins.
	if _, why := Resolve(RoleWorker, Task{}, s, facts); why != "codex is not signed in" {
		t.Fatalf("first failing reason = %q, want %q", why, "codex is not signed in")
	}
	// Only the unoffered model -> model reason.
	s.Models[RoleWorker] = []Candidate{{CLI: "agy", Model: "claude-opus-5-5-medium", Effort: "medium"}}
	if _, why := Resolve(RoleWorker, Task{}, s, facts); why != "model claude-opus-5-5-medium is not offered to this account" {
		t.Fatalf("model reason = %q", why)
	}
	// Offered model succeeds.
	s.Models[RoleWorker] = []Candidate{{CLI: "agy", Model: "gemini-4-neon-medium", Effort: "medium"}}
	if in, why := Resolve(RoleWorker, Task{}, s, facts); why != "" || in.CLI != "agy" || in.Model != "gemini-4-neon-medium" {
		t.Fatalf("offered model = (%+v, %q)", in, why)
	}
	// Not installed CLI -> not installed reason.
	s.Models[RoleWorker] = []Candidate{{CLI: "claude", Model: "sonnet", Effort: "medium"}}
	if _, why := Resolve(RoleWorker, Task{}, s, facts); why != "claude is not installed" {
		t.Fatalf("not installed reason = %q", why)
	}
}

func TestResolveDefaultsMatchToday(t *testing.T) {
	facts := allCLIsSignedIn()
	def := DefaultSettings()
	cIn, why := Resolve(RoleCoordinator, Task{}, def, facts)
	if why != "" || cIn != (Intent{RoleCoordinator, "claude", "opus", "medium", PermFull, ContinueFresh}) {
		t.Fatalf("coordinator default = (%+v, %q)", cIn, why)
	}
	wIn, why := Resolve(RoleWorker, Task{}, def, facts)
	if why != "" || wIn != (Intent{RoleWorker, "claude", "sonnet", "medium", PermWorktree, ContinueFresh}) {
		t.Fatalf("worker default = (%+v, %q)", wIn, why)
	}
	facts.Worker = Candidate{CLI: wIn.CLI, Model: wIn.Model, Effort: wIn.Effort}
	rIn, why := Resolve(RoleReviewer, Task{}, def, facts)
	if why != "" || rIn != (Intent{RoleReviewer, "claude", "opus", "medium", PermReadOnly, ContinueFresh}) {
		t.Fatalf("reviewer default = (%+v, %q)", rIn, why)
	}
	// DefaultModels with second_reviewer = codex puts codex/gpt-6.2-sol second.
	def.SecondReviewer = "codex"
	got := DefaultModels(def)[RoleReviewer]
	want := []Candidate{{CLI: "claude", Model: "opus", Effort: "medium"}, {CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}}
	if !slices.Equal(got, want) {
		t.Fatalf("DefaultModels(codex)[reviewer] = %+v, want %+v", got, want)
	}
}

func TestSettingsModelsValidation(t *testing.T) {
	s := DefaultSettings()
	s.Models = map[string][]Candidate{RoleWorker: {{CLI: "claude", Model: "sonnet", Effort: "medium"}}}
	if err := s.Validate(); err != nil {
		t.Fatalf("valid models: %v", err)
	}
	if !slices.Contains(s.Changed(), "models worker") {
		t.Fatalf("Changed() = %q, want models worker", s.Changed())
	}
	for name, m := range map[string]map[string][]Candidate{
		"unknown role":   {"boss": {{CLI: "claude", Model: "opus", Effort: "medium"}}},
		"empty list":     {RoleWorker: {}},
		"unknown cli":    {RoleWorker: {{CLI: "gemini", Model: "x", Effort: "medium"}}},
		"empty model":    {RoleWorker: {{CLI: "claude", Model: "  ", Effort: "medium"}}},
		"unknown effort": {RoleWorker: {{CLI: "claude", Model: "sonnet", Effort: "max"}}},
	} {
		s := DefaultSettings()
		s.Models = m
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
	if _, why := Resolve("boss", Task{}, DefaultSettings(), allCLIsSignedIn()); !strings.Contains(why, "unknown role") {
		t.Fatalf("Resolve(unknown role) = %q", why)
	}
}
