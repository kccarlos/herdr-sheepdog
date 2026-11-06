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

func buildLongClaudeBashPromptWithHead(headCmd string, lines int, tailCmd string) (fullCmd, screen string) {
	var cmdLines, screenLines []string
	screenLines = append(screenLines,
		"────────────────────────────────────────────────────────────────────────────────",
		" Bash command",
		" Tip: auto mode handles these prompts for you — choose \"switch to auto mode\" below",
		" Echo lines then create marker",
		"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
	)
	cmdLines = append(cmdLines, headCmd)
	screenLines = append(screenLines, " │ "+headCmd)
	for i := 2; i < lines; i++ {
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

func TestClaudePermissionHookAttachesFullCommandAndKeepsRail(t *testing.T) {
	workerToken := "fixture-value-987654321"
	h := newHarness(t, func(c *Config) {
		c.Redactor = redact.New()
	})
	h.addAgent("w5:p1", "auth", "claude", herdr.StateWorking)

	fullCmd, fullScreen := buildLongClaudeBashPromptWithHead(
		"DEPLOY_TOKEN="+workerToken+" echo line-01",
		50,
		"touch probe-ok.txt",
	)
	allLines := strings.Split(fullScreen, "\n")
	cutScreen := strings.Join(allLines[len(allLines)-40:], "\n")
	rawParsed, rawOK := herdr.ParsePrompt(fullScreen)
	if !rawOK {
		t.Fatal("fullScreen must parse as a prompt")
	}

	// 1. Without a hook report: ParsePrompt cannot see "Bash command" above its 40-line window, so Command is "".
	// If an async hook arrives after PendingAction is already open, reconcileReprompts and stillAsking keep the card alive.
	h.srv.SetScreen("w5:p1", fullScreen)
	row, _ := h.store.Get("w5:p1")
	h.transition("w5:p1", row.State, herdr.StateBlocked)
	p, ok := h.pending()
	if !ok || p.Command != "" {
		t.Fatalf("without hook: ok=%v command=%q, want empty", ok, p.Command)
	}
	h.sup.RecordClaudeHook("w5:p1", realClaudeHookPayload(fullCmd))
	h.sup.reconcileReprompts(context.Background())
	if _, stillOpen := h.pending(); !stillOpen {
		t.Fatal("late async hook caused reconcileReprompts to drop open prompt")
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)

	// 2. Truncated screen (missing top rule/header) or colliding tail or whitespace-distinct command never attaches.
	h.sup.RecordClaudeHook("w6:p1", realClaudeHookPayload(fullCmd))
	h.sup.RecordClaudeHook("w5:p1", realClaudeHookPayload(fullCmd))
	h.srv.SetScreen("w5:p1", cutScreen)
	row, _ = h.store.Get("w5:p1")
	h.transition("w5:p1", row.State, herdr.StateBlocked)
	p, ok = h.pending()
	if !ok || p.Command != "" {
		t.Fatalf("truncated screen attached hook without verifiable identity: command=%q", p.Command)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)

	// Colliding command B with identical last 49 lines when A was cached and B's hook was lost.
	cmdA, _ := buildLongClaudeBashPromptWithHead("echo safe-preview", 50, "touch probe-ok.txt")
	_, fullScreenB := buildLongClaudeBashPromptWithHead("rm -rf /tmp/important", 50, "touch probe-ok.txt")
	h.sup.RecordClaudeHook("w5:p1", realClaudeHookPayload(cmdA))
	h.srv.SetScreen("w5:p1", fullScreenB)
	row, _ = h.store.Get("w5:p1")
	h.transition("w5:p1", row.State, herdr.StateBlocked)
	p, ok = h.pending()
	if !ok || p.Command != "" {
		t.Fatalf("colliding tail attached stale command A: command=%q", p.Command)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)

	// Meaningful whitespace difference must keep the screen text as today.
	_, spaceScreen := buildLongClaudeBashPromptWithHead(`rm "draft notes.txt"`, 2, `echo done`)
	h.sup.RecordClaudeHook("w5:p1", realClaudeHookPayload("rm \"draftnotes.txt\"\necho done"))
	h.srv.SetScreen("w5:p1", spaceScreen)
	row, _ = h.store.Get("w5:p1")
	h.transition("w5:p1", row.State, herdr.StateBlocked)
	p, ok = h.pending()
	if !ok || strings.Contains(p.Command, "draftnotes.txt") {
		t.Fatalf("whitespace-distinct hook attached: command=%q", p.Command)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)

	// 3. Matching pane and complete 50-line box attaches full command, redacts worker-only token, and keeps options/keys untouched.
	h.sup.RecordClaudeHook("w5:p1", realClaudeHookPayload(fullCmd))
	h.srv.SetScreen("w5:p1", fullScreen)
	row, _ = h.store.Get("w5:p1")
	h.transition("w5:p1", row.State, herdr.StateBlocked)
	p, ok = h.pending()
	if !ok {
		t.Fatal("expected pending question")
	}
	wantRedacted := strings.ReplaceAll(fullCmd, workerToken, "[REDACTED]")
	if p.Command != wantRedacted {
		t.Fatalf("pending command =\n%s\nwant:\n%s", p.Command, wantRedacted)
	}
	if strings.Contains(p.Command, workerToken) {
		t.Fatal("unredacted worker-only token in pending command")
	}
	if len(p.Options) != len(rawParsed.Options) || strings.Join(p.YesKeys, ",") != strings.Join(rawParsed.YesKeys, ",") || strings.Join(p.NoKeys, ",") != strings.Join(rawParsed.NoKeys, ",") {
		t.Fatalf("hook altered options or keys: got opts=%+v yes=%v no=%v", p.Options, p.YesKeys, p.NoKeys)
	}
	if qs := h.store.Questions(); len(qs) != 1 || qs[0].Command != wantRedacted {
		t.Fatalf("store.Questions() = %+v", qs)
	}

	// Reconcile must keep the prompt open, stillAsking must reject if screen changes to B, and AnswerTap must send keys.
	h.sup.reconcileReprompts(context.Background())
	if _, stillOpen := h.pending(); !stillOpen {
		t.Fatal("reconcileReprompts dropped hook-enriched pending prompt")
	}
	h.srv.SetScreen("w5:p1", fullScreenB)
	if asking, _ := h.sup.stillAsking(context.Background(), p); asking {
		t.Fatal("stillAsking accepted different command B for card A")
	}
	h.srv.SetScreen("w5:p1", fullScreen)
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 1); err != nil {
		t.Fatalf("AnswerTap failed on hook-enriched prompt: %v", err)
	}
	if got := h.sendKeysRequests(); len(got) != 1 || got[0] != ([2]string{"w5:p1", "1"}) {
		t.Fatalf("sendKeys = %v, want [[w5:p1 1]]", got)
	}

	// 4. Report expires when the request ends: re-showing the same screen without a new hook report must not reuse it.
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
	h.srv.SetScreen("w5:p1", fullScreen)
	h.transition("w5:p1", herdr.StateWorking, herdr.StateBlocked)
	pAfter, ok := h.pending()
	if !ok || pAfter.Command != "" {
		t.Fatalf("expired hook reused on next request: ok=%v command=%q", ok, pAfter.Command)
	}
}

func realClaudeHookPayload(cmd string) []byte {
	b, _ := json.Marshal(map[string]any{
		"session_id":      "b0c8a978-2086-4b5c-8783-a873aae6f99a",
		"transcript_path": "/tmp/claude-projects/b0c8a978.jsonl",
		"cwd":             "/tmp/probe/repo",
		"permission_mode": "default",
		"hook_event_name": "PermissionRequest",
		"tool_name":       "Bash",
		"tool_input": map[string]any{
			"command":     cmd,
			"description": "Echo lines then create marker",
		},
	})
	return b
}
