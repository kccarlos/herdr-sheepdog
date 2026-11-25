package flow

// PR 14.3, TDD step 1: failing tests for ParseCandidates/FormatCandidates,
// models.<role> rows (including plan_reviewer), Settings.With refusals, and
// models read-back wording (plan.md 14.3, design.md 10.1, slice14-wording.md).

import (
	"slices"
	"strings"
	"testing"
)

// TestParseFormatCandidatesRoundTripSlashAndCommaRefused covers round trip of
// ParseCandidates and FormatCandidates, model names containing "/", comma
// refusal, and refusals for unknown CLI, missing model, bad effort, and empty list.
func TestParseFormatCandidatesRoundTripSlashAndCommaRefused(t *testing.T) {
	cases := []struct {
		name  string
		cands []Candidate
	}{
		{"single", []Candidate{{CLI: "claude", Model: "sonnet", Effort: "medium"}}},
		{"multiple", []Candidate{{CLI: "agy", Model: "gemini-4-neon", Effort: "medium"}, {CLI: "claude", Model: "sonnet", Effort: "high"}}},
		{"model with slash", []Candidate{{CLI: "agy", Model: "google/gemini-4-neon", Effort: "low"}}},
	}
	for _, tc := range cases {
		raw := FormatCandidates(tc.cands)
		if got, err := ParseCandidates(raw); raw == "" || err != nil || !slices.Equal(got, tc.cands) {
			t.Fatalf("%s: round trip(%q) = (%+v, %v), want %+v", tc.name, raw, got, err, tc.cands)
		}
	}
	if err := ValidateCandidates("models", map[string][]Candidate{RoleWorker: {{CLI: "claude", Model: "sonnet,opus", Effort: "medium"}}}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "comma") {
		t.Errorf("ValidateCandidates with comma = %v, want error naming comma", err)
	}
	for _, tc := range []struct {
		name, raw string
		want      []string
	}{
		{"comma in model", "claude/foo,bar/medium", nil},
		{"unknown cli", "claud/sonnet/medium", []string{`unknown cli "claud"`, "claude, codex, agy"}},
		{"missing model", "claude//medium", []string{"model"}},
		{"blank model", "claude/   /medium", []string{"model"}},
		{"bad effort", "claude/sonnet/max", []string{"effort", "low, medium, high"}},
		{"empty list", "", nil},
		{"blank list", "   ", nil},
		{"trailing comma", "claude/sonnet/medium,", nil},
	} {
		if got, err := ParseCandidates(tc.raw); err == nil {
			t.Errorf("ParseCandidates(%s: %q) = %+v, want refusal", tc.name, tc.raw, got)
		} else {
			for _, sub := range tc.want {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("ParseCandidates(%s) err %q missing %q", tc.name, err.Error(), sub)
				}
			}
		}
	}
}

// TestSettingsModelsRowsTopWithAndRuleQuestion checks models.<role> rows (Top,
// plain "<role> models" labels, plan_reviewer included), With parse/reset/refusals,
// second_reviewer Note interaction, and RuleQuestion "then" wording (rb.q.models).
func TestSettingsModelsRowsTopWithAndRuleQuestion(t *testing.T) {
	def := DefaultSettings()
	rows := def.Rows()
	for _, wr := range []struct{ role, label string }{
		{RoleCoordinator, "coordinator models"}, {RoleWorker, "worker models"},
		{RoleReviewer, "reviewer models"}, {RoleRetro, "retro models"}, {RolePlanReviewer, "plan reviewer models"},
	} {
		r := findRow(t, rows, "models."+wr.role)
		wantDef := FormatCandidates(DefaultModels(def)[wr.role])
		if !r.Top || r.Label != wr.label || strings.Contains(r.Label, "_") || r.Default != wantDef || r.Value != wantDef {
			t.Errorf("row models.%s = %+v, want Top=true Label=%q val/def=%q", wr.role, r, wr.label, wantDef)
		}
	}
	s2, err := def.With("models.worker", "agy/gemini-4-neon/medium, claude/sonnet/medium")
	wantWorker := []Candidate{{CLI: "agy", Model: "gemini-4-neon", Effort: "medium"}, {CLI: "claude", Model: "sonnet", Effort: "medium"}}
	if err != nil || !slices.Equal(Candidates(RoleWorker, Task{}, s2), wantWorker) {
		t.Fatalf("With(models.worker) = (%+v, %v), want %+v", Candidates(RoleWorker, Task{}, s2), err, wantWorker)
	}
	if sReset, err := s2.With("models.worker", ""); err != nil || len(sReset.Models[RoleWorker]) != 0 {
		t.Fatalf("With(models.worker, \"\") = (%+v, %v), want default restored", sReset.Models, err)
	}
	if sPR, err := def.With("models.plan_reviewer", "claude/sonnet/medium"); err != nil || len(sPR.Models[RolePlanReviewer]) != 1 {
		t.Fatalf("With(models.plan_reviewer) = (%+v, %v)", sPR.Models, err)
	} else if sPR0, err := sPR.With("models.plan_reviewer", ""); err != nil || len(sPR0.Models[RolePlanReviewer]) != 0 {
		t.Fatalf("With(models.plan_reviewer, \"\") = (%+v, %v)", sPR0.Models, err)
	}
	if sRev, err := def.With("models.reviewer", "agy/gemini-4-neon/medium"); err != nil || findRow(t, sRev.Rows(), "second_reviewer").Note != "not used while reviewer models are set" {
		t.Fatalf("With(models.reviewer) err=%v note=%q", err, findRow(t, sRev.Rows(), "second_reviewer").Note)
	}
	for _, bad := range []string{"claud/sonnet/medium", "claude//medium", "claude/sonnet/max", "claude/a,b/medium", ","} {
		if _, err := def.With("models.worker", bad); err == nil || strings.Contains(err.Error(), "models.worker") {
			t.Errorf("With(models.worker, %q) = %v, want refusal without raw key", bad, err)
		}
	}
	wantQ := "This project: worker models agy/gemini-4-neon/medium then claude/sonnet/medium (now claude/sonnet/medium). Save?"
	if got := RuleQuestion(SettingChange{Name: "models.worker", Value: "agy/gemini-4-neon/medium, claude/sonnet/medium", Was: "claude/sonnet/medium"}); got != wantQ {
		t.Errorf("RuleQuestion(models.worker) = %q, want %q", got, wantQ)
	}
}

// TestSettingsWithCommaMatchesWordingTable verifies Settings.With prefixes
// comma refusals with the row label to match slice14-wording.md mo.r.comma.
func TestSettingsWithCommaMatchesWordingTable(t *testing.T) {
	want := "worker models: a model name cannot hold a comma"
	if _, err := DefaultSettings().With("models.worker", "claude/sonnet,opus/medium"); err == nil || err.Error() != want {
		t.Fatalf("With(models.worker, comma) = %v, want %q", err, want)
	}
}

// TestParseCandidatesMalformedMultiEntryDoesNotBlameComma verifies malformed
// multi-entry candidate lists report the cli/model/effort shape error rather
// than blaming a comma in a model name, while genuine comma-in-model still does.
func TestParseCandidatesMalformedMultiEntryDoesNotBlameComma(t *testing.T) {
	for _, raw := range []string{"claude/sonnet/medium, bad", "claude/sonnet, claude/opus"} {
		if _, err := ParseCandidates(raw); err == nil || err.Error() != "each entry must be cli/model/effort" {
			t.Errorf("ParseCandidates(%q) = %v, want %q", raw, err, "each entry must be cli/model/effort")
		}
	}
	if _, err := ParseCandidates("claude/foo,bar/medium"); err == nil || err.Error() != "a model name cannot hold a comma" {
		t.Errorf("ParseCandidates(comma in model) = %v, want %q", err, "a model name cannot hold a comma")
	}
}
