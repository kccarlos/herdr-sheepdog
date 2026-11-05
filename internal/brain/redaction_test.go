package brain_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
	"google.golang.org/genai"
)

func TestWorkerSecretsDoNotReachModelOrBrainFiles(t *testing.T) {
	secret := "sk-" + "example1234567890abcdef"
	model := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth"}), braintest.FC(ToolReadScreen, map[string]any{"agent": "auth", "source": SourceSession}), braintest.FC(ToolRunCLI, map[string]any{"agent": "auth", "question": "check"})}},
		braintest.Step{Text: braintest.Reply("Done.", "")},
	)
	h := newHarness(t, model, nil)
	h.host.SetScreen("w5:p1", "screen "+secret)
	h.host.SetSession("w5:p1", SessionRead{LastAssistant: "session " + secret})
	h.host.CLI = func(_, _ string) (string, error) { return "cli " + secret, nil }
	h.host.SetAgent(AgentInfo{PaneID: "w5:p1", Alias: "auth", Kind: "codex", State: "working", LastResult: "record " + secret})
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindUtterance, Question: "what happened?", Screen: "event " + secret, Agent: "auth"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range model.Calls() {
		if strings.Contains(c.Text(), secret) {
			t.Error("secret reached model")
		}
	}
	for _, name := range []string{"run1.jsonl", "run1.requests.jsonl"} {
		data, err := os.ReadFile(filepath.Join(h.dir, "brain", name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secret) {
			t.Errorf("secret saved in %s", name)
		}
		if !strings.Contains(string(data), "[REDACTED]") {
			t.Errorf("no redaction marker in %s", name)
		}
	}
}

func TestConfiguredSecretRedactedBeforeTruncation(t *testing.T) {
	secret := "custom-credential-value-123456789"
	model := braintest.NewModel(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth"})}}, braintest.Step{Text: braintest.Reply("Done.", "")})
	h := newHarness(t, model, func(c *Config) { c.Redactor = redact.New(secret) })
	h.host.SetScreen("w5:p1", secret+strings.Repeat("x", prose.DefaultResultBytes-(len(secret)-12)))
	if _, err := h.ask("check auth", "auth"); err != nil {
		t.Fatal(err)
	}
	for _, c := range model.Calls() {
		if strings.Contains(c.Text(), secret[12:]) {
			t.Error("partial secret reached model")
		}
	}
}

func TestRosterRedactsBeforeClipping(t *testing.T) {
	secret := "custom-credential-value-123456789"
	model := braintest.NewModel(braintest.Step{Text: braintest.Reply("Done.", "")})
	h := newHarness(t, model, func(c *Config) { c.Redactor = redact.New(secret) })
	h.host.SetAgent(AgentInfo{PaneID: "w5:p1", Alias: "auth", State: "working", Task: strings.Repeat("x", 25) + secret})
	if _, err := h.ask("check auth", "auth"); err != nil {
		t.Fatal(err)
	}
	for _, c := range model.Calls() {
		if strings.Contains(c.Text(), "custom-cred") {
			t.Fatal("partial secret reached model")
		}
	}
}
