package cli

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// TestForkArgv pins the fork command line of each backend, verified
// against the real CLIs in docs/cli-query-tools.md §2c: claude resumes
// the worker's session with --fork-session and no --model; codex uses
// `exec fork`, never `exec resume`, which would append to the worker's
// live rollout. The fork's own id is the session a follow-up resumes.
func TestForkArgv(t *testing.T) {
	bin := fakeBinPath(t)
	r := newTestRunner(t, Config{CodexBin: bin, ClaudeBin: bin}, Detection{Codex: true, Claude: true})
	ws := t.TempDir()
	t.Setenv("FAKE_SESSION", "fork-1")

	for _, tc := range []struct {
		backend Backend
		env     string
		want    []string
	}{
		{Claude, "FAKE_CLAUDE_JSON", []string{bin, "-p", "why did you skip the migration?",
			"--resume", "worker-1", "--fork-session",
			"--tools", "Read,Grep,Glob,WebSearch", "--allowedTools", "Read,Grep,Glob,WebSearch",
			"--strict-mcp-config", "--permission-mode", "dontAsk", "--permission-prompts", "none",
			"--output-format", "json", "--max-budget-usd", "2.00"}},
		{Codex, "FAKE_CODEX_JSONL", []string{bin, "exec", "fork", "worker-1", "why did you skip the migration?",
			"-c", "sandbox_mode=read-only", "-c", "model_reasoning_effort=low",
			"--skip-git-repo-check", "--json"}},
	} {
		t.Run(string(tc.backend), func(t *testing.T) {
			t.Setenv(tc.env, "1")
			res, err := r.Run(context.Background(), Query{Backend: tc.backend, Workspace: ws, Question: "why did you skip the migration?", Session: "worker-1", Fork: true})
			if err != nil {
				t.Fatalf("fork: %v", err)
			}
			if !slices.Equal(res.Argv, tc.want) {
				t.Errorf("argv =\n%q\nwant\n%q", res.Argv, tc.want)
			}
			if !res.Forked || res.Resumed || res.SessionID != "fork-1" {
				t.Errorf("result = %+v (want forked, the fork's own session)", res)
			}
		})
	}
}

// A fork that reports the worker's own session back did not fork: the
// run is an error, so the worker's id can never become the job's
// session (a follow-up would then resume the worker and write into it).
func TestForkThatReturnsTheOriginalIsAnError(t *testing.T) {
	bin := fakeBinPath(t)
	r := newTestRunner(t, Config{ClaudeBin: bin}, Detection{Claude: true})
	t.Setenv("FAKE_CLAUDE_JSON", "1")
	t.Setenv("FAKE_SESSION", "worker-1")
	_, err := r.Run(context.Background(), Query{Backend: Claude, Workspace: t.TempDir(), Question: "q", Session: "worker-1", Fork: true})
	if !errors.Is(err, ErrNotForked) {
		t.Errorf("err = %v, want ErrNotForked", err)
	}
}

// A fork whose output named no session answers anyway, with no session:
// a follow-up starts cold instead of resuming the worker.
func TestForkWithoutReportedSessionKeepsNone(t *testing.T) {
	bin := fakeBinPath(t)
	r := newTestRunner(t, Config{CodexBin: bin}, Detection{Codex: true})
	t.Setenv("FAKE_CODEX_JSONL", "1")
	res, err := r.Run(context.Background(), Query{Backend: Codex, Workspace: t.TempDir(), Question: "q", Session: "worker-1", Fork: true})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if res.SessionID != "" {
		t.Errorf("session = %q, want none (never the worker's)", res.SessionID)
	}
}

// A fork needs the session to fork, and Has reports detection per
// backend.
func TestForkNeedsASessionAndHas(t *testing.T) {
	bin := fakeBinPath(t)
	r := newTestRunner(t, Config{ClaudeBin: bin}, Detection{Claude: true})
	if _, err := r.Run(context.Background(), Query{Backend: Claude, Workspace: t.TempDir(), Question: "q", Fork: true}); err == nil {
		t.Error("a fork with no session ran")
	}
	if !r.Has(Claude) || r.Has(Codex) || r.Has("agy") {
		t.Errorf("Has = claude %t codex %t agy %t", r.Has(Claude), r.Has(Codex), r.Has("agy"))
	}
}
