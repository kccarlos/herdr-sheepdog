package cli

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSessionCaptureAndResume proves both halves of an investigation's
// follow-up: the session id comes out of each backend's own output
// (codex's thread.started, claude's session_id), and the resume template
// is rendered with it — the argv verified against the real CLIs in
// docs/cli-query-tools.md §3, exercised here against the fake binary.
func TestSessionCaptureAndResume(t *testing.T) {
	bin := fakeBinPath(t)
	cfg := Config{
		CodexBin: bin, ClaudeBin: bin,
		CodexCmd:        []string{bin, "-C", "{cwd}", "{question}"},
		ClaudeCmd:       []string{bin, "{question}"},
		CodexResumeCmd:  []string{bin, "exec", "resume", "{session}", "{question}", "-c", "sandbox_mode=read-only"},
		ClaudeResumeCmd: []string{bin, "{question}", "--resume", "{session}"},
	}
	r := newTestRunner(t, cfg, Detection{Codex: true, Claude: true})
	ws := t.TempDir()

	t.Setenv("FAKE_SESSION", "sess-1")
	for _, tc := range []struct {
		backend Backend
		env     string
	}{{Codex, "FAKE_CODEX_JSONL"}, {Claude, "FAKE_CLAUDE_JSON"}} {
		t.Run(string(tc.backend), func(t *testing.T) {
			t.Setenv(tc.env, "1")
			res, err := r.Run(context.Background(), Query{Backend: tc.backend, Workspace: ws, Question: "why did it fail?"})
			if err != nil {
				t.Fatalf("first run: %v", err)
			}
			if res.SessionID != "sess-1" || res.Resumed {
				t.Fatalf("first run = %+v (want the reported session, not a resume)", res)
			}
			if !strings.Contains(res.Raw, "why did it fail?") {
				t.Errorf("answer not extracted: %q", res.Raw)
			}

			res, err = r.Run(context.Background(), Query{Backend: tc.backend, Workspace: ws, Question: "and why does that guard exist?", Session: res.SessionID})
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			if !res.Resumed || res.SessionID != "sess-1" {
				t.Errorf("resume = %+v", res)
			}
			argv := strings.Join(res.Argv, " ")
			if !strings.Contains(argv, "sess-1") || !strings.Contains(argv, "and why does that guard exist?") {
				t.Errorf("resume argv = %q (want {session} and {question} substituted)", argv)
			}
			if strings.Contains(argv, "{session}") || strings.Contains(argv, "{question}") {
				t.Errorf("resume argv still holds a placeholder: %q", argv)
			}
		})
	}
}

// TestCodexJSONLParsing covers the shapes stdout can take: the verified
// event stream, a truncated head (the output cap keeps the tail, so the
// thread id can be missing) and plain text from a template without
// --json.
func TestCodexJSONLParsing(t *testing.T) {
	const stream = `{"type":"thread.started","thread_id":"01a0c540-e202-74a3-9439-8ec094855a8a"}
{"type":"turn.started"}
{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"ls"}}
{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"ls","exit_code":0}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"The clamp test asserts the wrong bound."}}
{"type":"turn.completed","usage":{"input_tokens":24665,"output_tokens":143}}`
	answer, thread, ok := parseCodexJSONL(stream)
	if !ok || answer != "The clamp test asserts the wrong bound." || thread != "01a0c540-e202-74a3-9439-8ec094855a8a" {
		t.Errorf("parse = %q %q %t", answer, thread, ok)
	}

	tail := "ted\":2}}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"only the tail survived\"}}"
	answer, thread, ok = parseCodexJSONL(tail)
	if !ok || answer != "only the tail survived" || thread != "" {
		t.Errorf("truncated parse = %q %q %t", answer, thread, ok)
	}

	if _, _, ok := parseCodexJSONL("Files in the directory:\n- README.md\n"); ok {
		t.Error("plain text parsed as JSONL")
	}
}

// TestRunSlotAndTimeout proves a background investigation does not lock
// the brain out of a short lookup in the same repository, and that its
// own timeout is what bounds it.
func TestRunSlotAndTimeout(t *testing.T) {
	bin := fakeBinPath(t)
	cfg := Config{CodexBin: bin, CodexCmd: []string{bin, "{question}"}, Timeout: 50 * time.Millisecond}
	r := newTestRunner(t, cfg, Detection{Codex: true})
	ws := t.TempDir()

	r.mu.Lock()
	r.busy[ws] = true // a run_cli lookup in flight
	r.mu.Unlock()
	if _, err := r.Run(context.Background(), Query{Backend: Codex, Workspace: ws, Question: "q"}); err != ErrBusy {
		t.Errorf("same slot = %v, want ErrBusy", err)
	}
	if _, err := r.Run(context.Background(), Query{Backend: Codex, Workspace: ws, Question: "q", Slot: "investigation:" + ws}); err != nil {
		t.Errorf("own slot = %v, want it to run anyway", err)
	}

	t.Setenv("FAKE_SLEEP", "1")
	start := time.Now()
	if _, err := r.Run(context.Background(), Query{Backend: Codex, Workspace: t.TempDir(), Question: "q", Slot: "s", Timeout: 80 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), "80ms") {
		t.Errorf("timeout = %v, want the query's own", err)
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Errorf("the query's timeout did not apply (took %s)", d)
	}
}
