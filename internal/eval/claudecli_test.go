package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// fakeClaude writes a stand-in for the claude CLI that records its
// arguments, stdin and environment and prints out.
func fakeClaude(t *testing.T, out string) (bin, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir = t.TempDir()
	bin = filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + dir + "/args\"\ncat > \"" + dir + "/stdin\"\nenv > \"" + dir + "/env\"\ncat <<'EOF'\n" + out + "\nEOF\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

// The Claude judge runs `claude -p` with no tools, settings or session,
// the system instruction and schema as flags, the prompt on stdin, and
// no API key in its environment; the structured output is the verdict.
func TestClaudeCLIJudge(t *testing.T) {
	bin, dir := fakeClaude(t, `{"type":"result","is_error":false,"result":"ok","structured_output":{"verdict":"no","confidence":0.9,"evidence":"Sheepdog: done"},"usage":{"input_tokens":3,"cache_creation_input_tokens":1000,"cache_read_input_tokens":200,"output_tokens":40}}`)
	t.Setenv("ANTHROPIC_API_KEY", "must-not-pass")
	meter := NewMeter()
	j := &LLMJudge{Model: Metered(&ClaudeCLI{Bin: bin, Dir: t.TempDir()}, meter, "judge"), ModelID: DefaultClaudeModel, NoRationale: true}
	v, err := j.Judge(context.Background(), JudgeInput{Criterion: "Did it say what is done?", Transcript: "CEO: status?\nSheepdog: done", Final: true})
	if err != nil {
		t.Fatal(err)
	}
	if v.Answer != AnswerNo || v.Evidence != "Sheepdog: done" || v.Rationale != "Sheepdog: done" {
		t.Fatalf("verdict %+v", v)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	for _, want := range []string{"-p", "--model\nsonnet", "--output-format\njson", "--tools\n\n", "--strict-mcp-config", "--no-session-persistence", "--system-prompt\nYou are a strict, fair evaluator", `"enum":["yes","no","wait"]`} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if !strings.Contains(string(stdin), "CRITERION: Did it say what is done?") {
		t.Errorf("stdin %q", stdin)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env"))
	if strings.Contains(string(env), "ANTHROPIC_API_KEY") {
		t.Error("the API key reached the CLI")
	}
	l := meter.Lines()
	if len(l) != 1 || l[0].Usage.Calls != 1 || l[0].Usage.In != 1203 || l[0].Usage.Cached != 200 || l[0].Usage.Out != 40 || l[0].CostUSD != 0 {
		t.Errorf("meter %+v", l)
	}
}

// The subscription's usage limit is an error of its own, never retried.
func TestClaudeCLIUsageLimitNotRetried(t *testing.T) {
	bin, _ := fakeClaude(t, `{"type":"result","is_error":true,"subtype":"success","result":"Claude AI usage limit reached|1791300000"}`)
	m := Patient(&ClaudeCLI{Bin: bin, Dir: t.TempDir()}, nil, 3, "")
	_, err := m.Generate(context.Background(), "sonnet", []*genai.Content{genai.NewContentFromText("x", genai.RoleUser)}, nil)
	if !errors.Is(err, ErrClaudeUsageLimit) {
		t.Fatalf("err %v", err)
	}
	if transient(err) {
		t.Error("usage limit counted as transient")
	}
}

// The rejudged failures replace the old judge's, keep every other one.
func TestRejudgedFailures(t *testing.T) {
	old := []Failure{{FailJudgeNo, "answered: thin"}, {FailError, "judge language: 429"}, {FailBrainFallback, "yielded"}}
	vs := []NamedVerdict{{Name: "answered", Verdict: Verdict{Answer: AnswerYes}}, {Name: "length_fits", Verdict: Verdict{Answer: AnswerNo, Rationale: "too thin"}}}
	got := RejudgedFailures(old, vs)
	if len(got) != 2 || got[0].Type != FailBrainFallback || got[1].Detail != "length_fits: too thin" {
		t.Fatalf("got %+v", got)
	}
}

// ReplayFacts rebuilds a record's end screens from its timeline.
func TestReplayFacts(t *testing.T) {
	sc := SimScenario{
		Agents: []SimAgent{{Alias: "bench-codex", Kind: "codex", State: "working", Screen: "› fix it\n• Working\n"}},
		Events: []SimEvent{{AtMS: 1000, AppendScreen: "• Fixed the clamp; tests pass", State: "done", Agent: "bench-codex"}},
	}
	run := SimRun{
		Timeline: []SimLine{{AtMS: 1000, Who: "worker", Agent: "bench-codex", Text: "bench-codex is now done; its screen ends: › fix it / • Working / • Fixed the clamp; tests pass"}},
		Actions:  []SimLine{{AtMS: 2000, Who: "action", Agent: "bench-codex", Text: "sheepdog sent bench-codex the prompt \"commit\""}},
	}
	f, err := ReplayFacts(sc, run, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Worker bench-codex (codex), now done.", "New output on the screen since the start", "• Fixed the clamp; tests pass", "[t+2s] sheepdog sent bench-codex the prompt"} {
		if !strings.Contains(f, want) {
			t.Errorf("facts lack %q:\n%s", want, f)
		}
	}
}

// RejudgeSim replaces a record's verdicts with the new judge's.
func TestRejudgeSim(t *testing.T) {
	sc := SimScenario{ID: "s", Goal: "know the status", Agents: []SimAgent{{Alias: "bench-codex", Screen: "› x\n"}}}
	run := SimRun{Scenario: "s", EndReason: EndCall, Facts: "the facts", Transcript: []SimLine{{Who: "ceo", Text: "status?"}, {Who: "sheepdog", Text: "Working."}},
		Verdicts: []NamedVerdict{{Name: "answered", Verdict: Verdict{Answer: AnswerNo}}}, Failures: []Failure{{FailJudgeNo, "answered: no"}}}
	steps := []braintest.Step{}
	for range DefaultRubric() {
		steps = append(steps, braintest.Step{Text: `{"verdict":"yes","confidence":1,"evidence":"Sheepdog: Working."}`})
	}
	j := &LLMJudge{Model: braintest.NewModel(steps...), ModelID: "j", NoRationale: true}
	if err := RejudgeSim(context.Background(), j, sc, &run, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(run.Verdicts) != len(DefaultRubric()) || len(run.Failures) != 0 || run.Status != StatusOf(nil) {
		t.Fatalf("run %+v", run)
	}
}
