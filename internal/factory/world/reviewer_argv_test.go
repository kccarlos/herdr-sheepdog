package world

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestClaudeReviewerArgvRules checks design.md 9.2 and spike S-REV-2:
// Claude reviewers in dontAsk mode carry exactly one --allowedTools= argument
// containing the base read-only tools plus Bash(<bin> factory report review:*)
// and exactly one Edit(//<findingsPath>) rule for that round's scratch file.
// No Write(...), no report ask, no bare sheepdog variant when bin is absolute,
// and no ready/plan/task/answer rules (KB-REV-4).
func TestClaudeReviewerArgvRules(t *testing.T) {
	const (
		bin          = "/opt/bin/sheepdog"
		findingsPath = "/tmp/sd-factory/projects/p1/reports/T1-r1.draft.md"
	)

	t.Run("exact report review and single Edit rule", func(t *testing.T) {
		argv := ClaudeReviewerArgv("opus", "high", bin, findingsPath)
		if len(argv) == 0 {
			t.Fatal("ClaudeReviewerArgv returned nil/empty argv")
		}
		allowed := ""
		for _, a := range argv {
			if v, ok := strings.CutPrefix(a, "--allowedTools="); ok {
				if allowed != "" {
					t.Fatalf("duplicate --allowedTools= argument in %q", argv)
				}
				allowed = v
			}
			if a == "--allowedTools" {
				t.Fatalf("bare --allowedTools flag would swallow the prompt: %q", argv)
			}
		}
		if allowed == "" {
			t.Fatalf("missing --allowedTools= argument in %q", argv)
		}

		wantBash := "Bash(" + bin + " factory report review:*)"
		if !strings.Contains(allowed, wantBash) {
			t.Errorf("--allowedTools= missing %q:\n%s", wantBash, allowed)
		}

		var edits []string
		for _, tok := range splitAllowedTools(allowed) {
			if strings.HasPrefix(tok, "Edit(") {
				edits = append(edits, tok)
			}
			if strings.HasPrefix(tok, "Write(") {
				t.Errorf("--allowedTools= must not include Write rule %q (spike S-REV-2)", tok)
			}
		}
		if len(edits) != 1 {
			t.Fatalf("Edit(...) rules = %q (count %d), want exactly 1 for %s", edits, len(edits), findingsPath)
		}
		if !matchesClaudeAbsEditRule(edits[0], findingsPath) {
			t.Errorf("Edit rule = %q, want Edit(//<abs path>) matching %q", edits[0], findingsPath)
		}

		for _, forbidden := range []string{
			"Bash(sheepdog ",
			"report ask",
			"report ready",
			"factory plan",
			"factory task",
			"factory answer",
		} {
			if strings.Contains(allowed, forbidden) {
				t.Errorf("--allowedTools= must not contain %q (KB-REV-4):\n%s", forbidden, allowed)
			}
		}
	})

	t.Run("empty bin defaults to sheepdog", func(t *testing.T) {
		argv := ClaudeReviewerArgv("opus", "high", "", findingsPath)
		joined := strings.Join(argv, " ")
		if !strings.Contains(joined, "Bash(sheepdog factory report review:*)") {
			t.Errorf("empty bin argv = %q, want Bash(sheepdog factory report review:*)", joined)
		}
	})

	t.Run("missing required args or relative findings path returns nil", func(t *testing.T) {
		for _, tc := range []struct {
			name, model, effort, bin, path string
		}{
			{"empty model", "", "high", bin, findingsPath},
			{"empty effort", "opus", "", bin, findingsPath},
			{"empty findings path", "opus", "high", bin, ""},
			{"relative findings path", "opus", "high", bin, "reports/T1-r1.draft.md"},
		} {
			if got := ClaudeReviewerArgv(tc.model, tc.effort, tc.bin, tc.path); got != nil {
				t.Errorf("%s: ClaudeReviewerArgv = %q, want nil", tc.name, got)
			}
		}
	})
}

// TestClaudeRetroArgvRules checks design.md 9.2, KB-RET-12, and KB-RET-15:
// Claude retro runs in dontAsk mode carry exactly one --allowedTools= argument
// containing the base read-only tools plus Bash(<bin> factory report retro:*)
// and exactly one Edit(//<findingsPath>) rule for that round's scratch file.
// No report review, no Write(...), no report ask, no bare sheepdog variant when
// bin is absolute, and no ready/plan/task/answer rules.
func TestClaudeRetroArgvRules(t *testing.T) {
	const (
		bin          = "/opt/bin/sheepdog"
		findingsPath = "/tmp/sd-factory/projects/p1/reports/T1-r4.draft.md"
	)

	t.Run("exact report retro and single Edit rule", func(t *testing.T) {
		argv := ClaudeRetroArgv("opus", "high", bin, findingsPath)
		if len(argv) == 0 {
			t.Fatal("ClaudeRetroArgv returned nil/empty argv")
		}
		joined := strings.Join(argv, " ")
		if !strings.Contains(joined, "DISABLE_AUTOUPDATER=1") || !strings.Contains(joined, "--permission-mode dontAsk") {
			t.Errorf("ClaudeRetroArgv missing auto-updater disable or dontAsk: %q", argv)
		}
		allowed := ""
		for _, a := range argv {
			if v, ok := strings.CutPrefix(a, "--allowedTools="); ok {
				if allowed != "" {
					t.Fatalf("duplicate --allowedTools= argument in %q", argv)
				}
				allowed = v
			}
			if a == "--allowedTools" {
				t.Fatalf("bare --allowedTools flag would swallow the prompt: %q", argv)
			}
		}
		if allowed == "" {
			t.Fatalf("missing --allowedTools= argument in %q", argv)
		}

		wantBash := "Bash(" + bin + " factory report retro:*)"
		if !strings.Contains(allowed, wantBash) {
			t.Errorf("--allowedTools= missing %q:\n%s", wantBash, allowed)
		}

		var edits []string
		for _, tok := range splitAllowedTools(allowed) {
			if strings.HasPrefix(tok, "Edit(") {
				edits = append(edits, tok)
			}
			if strings.HasPrefix(tok, "Write(") {
				t.Errorf("--allowedTools= must not include Write rule %q (spike S-REV-2)", tok)
			}
		}
		if len(edits) != 1 {
			t.Fatalf("Edit(...) rules = %q (count %d), want exactly 1 for %s", edits, len(edits), findingsPath)
		}
		if !matchesClaudeAbsEditRule(edits[0], findingsPath) {
			t.Errorf("Edit rule = %q, want Edit(//<abs path>) matching %q", edits[0], findingsPath)
		}

		for _, forbidden := range []string{
			"report review",
			"Bash(sheepdog ",
			"report ask",
			"report ready",
			"factory plan",
			"factory task",
			"factory answer",
		} {
			if strings.Contains(allowed, forbidden) {
				t.Errorf("--allowedTools= must not contain %q (KB-RET-12, KB-RET-15):\n%s", forbidden, allowed)
			}
		}
	})

	t.Run("empty bin defaults to sheepdog", func(t *testing.T) {
		argv := ClaudeRetroArgv("opus", "high", "", findingsPath)
		joined := strings.Join(argv, " ")
		if !strings.Contains(joined, "Bash(sheepdog factory report retro:*)") {
			t.Errorf("empty bin argv = %q, want Bash(sheepdog factory report retro:*)", joined)
		}
	})

	t.Run("missing required args or relative findings path returns nil", func(t *testing.T) {
		for _, tc := range []struct {
			name, model, effort, bin, path string
		}{
			{"empty model", "", "high", bin, findingsPath},
			{"empty effort", "opus", "", bin, findingsPath},
			{"empty findings path", "opus", "high", bin, ""},
			{"relative findings path", "opus", "high", bin, "reports/T1-r4.draft.md"},
		} {
			if got := ClaudeRetroArgv(tc.model, tc.effort, tc.bin, tc.path); got != nil {
				t.Errorf("%s: ClaudeRetroArgv = %q, want nil", tc.name, got)
			}
		}
	})
}

// TestClaudePlanReviewerArgvRules checks plan.md 11.2: only report plan-review
// and one Edit(//<findingsPath>) rule in dontAsk mode.
func TestClaudePlanReviewerArgvRules(t *testing.T) {
	const bin, fp = "/opt/bin/sheepdog", "/tmp/sd-factory/projects/p1/reports/plan-r1.draft.md"
	argv := ClaudePlanReviewerArgv("sonnet", "medium", bin, fp)
	joined := strings.Join(argv, " ")
	if len(argv) == 0 || !strings.Contains(joined, "DISABLE_AUTOUPDATER=1") || !strings.Contains(joined, "--permission-mode dontAsk") {
		t.Fatalf("ClaudePlanReviewerArgv = %q, want dontAsk with DISABLE_AUTOUPDATER=1", argv)
	}
	allowed := ""
	for _, a := range argv {
		if v, ok := strings.CutPrefix(a, "--allowedTools="); ok && allowed == "" {
			allowed = v
		} else if strings.HasPrefix(a, "--allowedTools") {
			t.Fatalf("duplicate or bare --allowedTools in %q", argv)
		}
	}
	if !strings.Contains(allowed, "Bash("+bin+" factory report plan-review:*)") {
		t.Errorf("missing Bash(report plan-review:*):\n%s", allowed)
	}
	var edits []string
	for _, tok := range splitAllowedTools(allowed) {
		if strings.HasPrefix(tok, "Edit(") {
			edits = append(edits, tok)
		} else if strings.HasPrefix(tok, "Write(") {
			t.Errorf("must not include Write rule %q", tok)
		}
	}
	if len(edits) != 1 || !matchesClaudeAbsEditRule(edits[0], fp) {
		t.Errorf("Edit rules = %q, want single Edit(//%s)", edits, fp)
	}
	for _, bad := range []string{"report review", "report retro", "Bash(sheepdog ", "report ask", "report ready", "factory plan", "factory task", "factory answer"} {
		if strings.Contains(allowed, bad) {
			t.Errorf("--allowedTools= must not contain %q", bad)
		}
	}
	if got := strings.Join(ClaudePlanReviewerArgv("sonnet", "medium", "", fp), " "); !strings.Contains(got, "Bash(sheepdog factory report plan-review:*)") {
		t.Errorf("empty bin argv = %q", got)
	}
	for _, got := range [][]string{ClaudePlanReviewerArgv("", "medium", bin, fp), ClaudePlanReviewerArgv("sonnet", "", bin, fp), ClaudePlanReviewerArgv("sonnet", "medium", bin, ""), ClaudePlanReviewerArgv("sonnet", "medium", bin, "reports/plan-r1.draft.md")} {
		if got != nil {
			t.Errorf("invalid args returned %q, want nil", got)
		}
	}
}

// splitAllowedTools splits a space-separated Claude --allowedTools= string
// while keeping parenthesized arguments like Bash(git diff:*) intact.
func splitAllowedTools(s string) []string {
	var out []string
	depth, start := 0, -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			if start < 0 {
				start = i
			}
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ' ', '\t':
			if depth == 0 && start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		default:
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

// matchesClaudeAbsEditRule accepts Edit(//<path-without-leading-slash>) or
// Edit(//<abs-path>) as long as it uses the // absolute-path prefix and
// resolves cleanly to wantPath.
func matchesClaudeAbsEditRule(rule, wantPath string) bool {
	inner, ok := strings.CutPrefix(rule, "Edit(//")
	if !ok {
		return false
	}
	inner, ok = strings.CutSuffix(inner, ")")
	if !ok || inner == "" {
		return false
	}
	got := filepath.Clean("/" + inner)
	return got == filepath.Clean(wantPath)
}
