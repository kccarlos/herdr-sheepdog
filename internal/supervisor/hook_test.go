package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

func buildLongClaudeBashPrompt(lines int, tailCmd string) (fullCmd, screen string) {
	var cmdLines, screenLines []string
	screenLines = append(screenLines,
		"────────────────────────────────────────────────────────────────────────────────",
		" Bash command",
		" Tip: auto mode handles these prompts for you — choose \"switch to auto mode\" below",
		" Echo lines then create marker",
		"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	)
	for i := 1; i < lines; i++ {
		l := fmt.Sprintf("echo line-%02d", i)
		cmdLines = append(cmdLines, l)
		screenLines = append(screenLines, " │ "+l)
	}
	cmdLines = append(cmdLines, tailCmd)
	screenLines = append(screenLines,
		" │ "+tailCmd,
		"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. Yes, and always allow access to /tmp/probe/repo from this project",
		"   3. Yes, and switch to auto mode · auto mode handles these prompts for you",
		"   4. No",
		"",
		" Esc to cancel · Tab to amend",
	)
	return strings.Join(cmdLines, "\n"), strings.Join(screenLines, "\n")
}

func realClaudeHookPayload(cmd string) []byte {
	req := map[string]any{
		"session_id":      "b0c8a978-2086-4b5c-8783-a873aae6f99a",
		"transcript_path": "/tmp/probe/.claude/projects/-tmp-probe-repo/b0c8a978.jsonl",
		"cwd":             "/tmp/probe/repo",
		"scratchpad_dir":  "/tmp/claude-501/-tmp-probe-repo/b0c8a978/scratchpad",
		"prompt_id":       "acc14928-4cce-453b-a69a-6b9279e7ca2c",
		"permission_mode": "default",
		"effort":          map[string]any{"level": "medium"},
		"hook_event_name": "PermissionRequest",
		"tool_name":       "Bash",
		"tool_input": map[string]any{
			"command":     cmd,
			"description": "Echo lines then create marker",
		},
	}
	b, _ := json.Marshal(req)
	return b
}

func TestClaudePermissionHookAttachesFullCommandAndKeepsRail(t *testing.T) {
	secret := "sk-secret-test-token-9876543210"
	h := newHarness(t, func(c *Config) {
		c.Redactor = redact.New(secret)
	})
	h.addAgent("w5:p1", "auth", "claude", herdr.StateWorking)

	fullCmd, fullScreen := buildLongClaudeBashPrompt(50, "TOKEN="+secret+" touch probe-ok.txt")
	allLines := strings.Split(fullScreen, "\n")
	cutScreen := strings.Join(allLines[len(allLines)-40:], "\n")

	// 1. Without a hook report: ParsePrompt cannot see "Bash command" above its 40-line window, so Command is "".
	h.srv.SetScreen("w5:p1", fullScreen)
	row, _ := h.store.Get("w5:p1")
	h.transition("w5:p1", row.State, herdr.StateBlocked)
	p, ok := h.pending()
	if !ok || p.Command != "" {
		t.Fatalf("without hook: ok=%v command=%q, want empty", ok, p.Command)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)

	// 2. Wrong pane or wrong command on screen is ignored.
	h.sup.RecordClaudeHook("w6:p1", realClaudeHookPayload(fullCmd))
	h.sup.RecordClaudeHook("w5:p1", realClaudeHookPayload("echo unrelated\ntouch other.txt"))
	h.srv.SetScreen("w5:p1", cutScreen)
	row, _ = h.store.Get("w5:p1")
	h.transition("w5:p1", row.State, herdr.StateBlocked)
	p, ok = h.pending()
	if !ok || p.Command != "" {
		t.Fatalf("mismatched hook attached: command=%q", p.Command)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)

	// 3. Matching pane and matching command attaches the full 50-line command, redacted, and AnswerTap still works.
	h.sup.RecordClaudeHook("w5:p1", realClaudeHookPayload(fullCmd))
	h.srv.SetScreen("w5:p1", cutScreen)
	row, _ = h.store.Get("w5:p1")
	h.transition("w5:p1", row.State, herdr.StateBlocked)
	p, ok = h.pending()
	if !ok {
		t.Fatal("expected pending question")
	}
	wantRedacted := strings.ReplaceAll(fullCmd, secret, "[REDACTED]")
	if p.Command != wantRedacted {
		t.Fatalf("pending command =\n%s\nwant:\n%s", p.Command, wantRedacted)
	}
	if strings.Contains(p.Command, secret) {
		t.Fatal("unredacted secret in pending command")
	}
	if !strings.Contains(p.SpokenSummary, "asks to run echo") {
		t.Errorf("SpokenSummary = %q, want 'asks to run echo...'", p.SpokenSummary)
	}
	if qs := h.store.Questions(); len(qs) != 1 || qs[0].Command != wantRedacted {
		t.Fatalf("store.Questions() = %+v", qs)
	}

	// Reconcile must keep the prompt open (signature agreement), and AnswerTap must send keys through the rail.
	h.sup.reconcileReprompts(context.Background())
	if _, stillOpen := h.pending(); !stillOpen {
		t.Fatal("reconcileReprompts dropped hook-enriched pending prompt")
	}
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 1); err != nil {
		t.Fatalf("AnswerTap failed on hook-enriched prompt: %v", err)
	}
	if got := h.sendKeysRequests(); len(got) != 1 || got[0] != ([2]string{"w5:p1", "1"}) {
		t.Fatalf("sendKeys = %v, want [[w5:p1 1]]", got)
	}
}
