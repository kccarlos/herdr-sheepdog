package cli

import (
	"context"
	"slices"
	"testing"
)

func TestAuditCLIProtection(t *testing.T) {
	got := scrubEnv([]string{"GEMINI_API_KEY=x", "VERTEX_PROJECT=x", "DEEPSEEK_API_KEY=x", "OPENAI_ORCH_API_KEY=x", "OPENAI_API_KEY=x"})
	if !slices.Equal(got, []string{"OPENAI_API_KEY=x"}) {
		t.Errorf("environment: %v", got)
	}
	for _, tmpl := range [][]string{defaultClaudeCmd, defaultClaudeResumeCmd, defaultClaudeForkCmd} {
		i := slices.Index(tmpl, "--setting-sources")
		if i < 0 || i+1 == len(tmpl) || tmpl[i+1] != "user" {
			t.Errorf("settings: %v", tmpl)
		}
	}
	bin := fakeBinPath(t)
	r := newTestRunner(t, Config{CodexBin: bin}, Detection{Codex: true})
	res, err := r.Run(context.Background(), Query{Backend: Codex, Workspace: t.TempDir(), Question: "- check this"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Argv, " - check this") {
		t.Errorf("argv: %q", res.Argv)
	}
}
