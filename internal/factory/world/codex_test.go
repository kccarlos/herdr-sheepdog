package world

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// TestCodexArgvGolden pins the Codex reviewer's launch flags found live in
// PR D3's step 0: no folder-trust or hooks prompt, read-only, never asks.
func TestCodexArgvGolden(t *testing.T) {
	got := CodexArgv("gpt-6.2-sol", "medium", `/tmp/wt "x"`)
	want := []string{"codex", "--no-daemon", "--no-alt-screen", "-m", "gpt-6.2-sol", "-s", "read-only", "-a", "never",
		"-c", `projects={"/tmp/wt \"x\""={trust_level="trusted"}}`,
		"-c", "features.hooks=false", "-c", "check_for_update_on_startup=false",
		"-c", `model_reasoning_effort="medium"`}
	if !slices.Equal(got, want) {
		t.Fatalf("CodexArgv =\n%q\nwant\n%q", got, want)
	}
	if CodexArgv("", "medium", "/tmp") != nil || CodexArgv("m", "", "/tmp") != nil || CodexArgv("m", "medium", " ") != nil {
		t.Fatal("CodexArgv without model, effort or cwd should be nil")
	}
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if r, _ := filepath.EvalSymlinks(real); CodexArgv("m", "high", link)[10] != `projects={"`+r+`"={trust_level="trusted"}}` {
		t.Fatalf("trust key not resolved: %q", CodexArgv("m", "high", link)[10])
	}
	if got := CodexArgv("m", "high", "/tmp"); got[len(got)-1] != `model_reasoning_effort="high"` {
		t.Fatalf("effort arg = %q", got[len(got)-1])
	}
	if slices.Contains(got, "--dangerously-bypass-hook-trust") || isClaudeArgv(got) {
		t.Fatal("CodexArgv must not bypass hook trust or look like Claude")
	}
}

// Screens shaped like s03-codex-screen.txt: the echo wraps the tag's
// template, the reply's bullets start with "•", the input box with "›".
const (
	codexEcho = "  >_ OpenAI Codex (v0.170.0)\n\n› You are the factory code reviewer for task T1\n" +
		"  <report>{\"code\":\"c0de\",\"round\":2,\"head\":\"abc\",\"verdict\":\"pass|changes\",\"majors\":<n>}</report>\n\n"
	codexReplyText = "• Ran git diff abc..def\n  └ +1 line\n\n• minor: name the helper.\n\n" +
		"  <report>{\"code\":\"c0de\",\"round\":2,\"head\":\"abc\",\"verdict\":\"pass\",\"majors\":0}</report>\n\n" +
		"  Worked for 12s • 8:44 PM\n\n› Ask Codex to do anything\n\n  GPT-6.2-Sol medium · ~/wt\n"
)

// TestReadCodexReport: with no hc status at all, the echo alone is not yet a
// report, and the reply's tag is read at once (no wait for idle).
func TestReadCodexReport(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	term, err := client.Run(context.Background(), hc.RunParams{Name: "rev", Cwd: t.TempDir(), Argv: []string{"codex"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, tc := range []struct {
		name, screen string
		want         ReportStatus
	}{
		{"working", "", ReportNotYet},
		{"echo only", codexEcho + "• Working (3s • esc to interrupt)\n› Ask Codex to do anything\n", ReportNotYet},
		{"reply tag", codexEcho + codexReplyText, ReportFound},
	} {
		srv.SetScreen(term.Address, tc.screen)
		rd, err := ReadCodexReport(ctx, client, term.Address, "c0de")
		if err != nil || rd.Status != tc.want {
			t.Fatalf("%s: status %s err %v, want %s", tc.name, rd.Status, err, tc.want)
		}
		if tc.want == ReportFound && (rd.Report.Verdict != "pass" || rd.Report.Round != 2 || rd.Body != "• Ran git diff abc..def\n  └ +1 line\n\n• minor: name the helper.") {
			t.Fatalf("report = %+v body %q", rd.Report, rd.Body)
		}
	}
	if ctx.Err() != nil {
		t.Fatal("ReadCodexReport waited for a status Codex never sends")
	}
}

// TestCodexHome: the run's home holds only a link to the sign-in, and a
// missing sign-in is an error, not a login prompt in a reviewer terminal.
func TestCodexHome(t *testing.T) {
	src, dir := t.TempDir(), filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", src)
	if _, err := CodexHome(dir); err == nil {
		t.Fatal("CodexHome without auth.json should fail")
	}
	if err := os.WriteFile(filepath.Join(src, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 { // idempotent
		if got, err := CodexHome(dir); err != nil || got != dir {
			t.Fatalf("CodexHome = %q, %v", got, err)
		}
	}
	if l, err := os.Readlink(filepath.Join(dir, "auth.json")); err != nil || l != filepath.Join(src, "auth.json") {
		t.Fatalf("auth link = %q, %v", l, err)
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Fatalf("home holds %d entries, want only auth.json", len(es))
	}
	link := filepath.Join(dir, "auth.json")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "old.json"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := CodexHome(dir); err != nil { // our own stale link is replaced
		t.Fatal(err)
	}
	if l, _ := os.Readlink(link); l != filepath.Join(src, "auth.json") {
		t.Fatalf("stale link kept: %q", l)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CodexHome(dir); !errors.Is(err, ErrCodexAuthDiverged) {
		t.Fatalf("regular auth.json: err %v, want ErrCodexAuthDiverged", err)
	}
	if b, _ := os.ReadFile(link); string(b) != "rotated" {
		t.Fatalf("regular auth.json changed: %q", b)
	}
}
