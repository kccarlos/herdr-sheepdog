package brain_test

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// "Open a new Claude in sheepdog-bench and have it run the tests" is the
// prompt tool with new_agent_dir: a spawn through the Host, with the
// text as its first instruction, never an instruction to a running agent.
func TestPromptToolSpawnsNewAgent(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolPrompt, map[string]any{"new_agent_dir": "sheepdog-bench", "new_agent_model": "haiku", "text": "run the tests and tell me what fails"})}},
		braintest.Step{Text: braintest.Reply("Starting a Claude in sheepdog-bench.", "")},
	), nil)
	if _, err := h.ask("open a cheap claude in sheepdog-bench and have it run the tests", ""); err != nil {
		t.Fatal(err)
	}
	want := SpawnRequest{Dir: "sheepdog-bench", Kind: SpawnClaude, Model: "haiku", Text: "run the tests and tell me what fails"}
	if len(h.host.Spawned) != 1 || h.host.Spawned[0] != want {
		t.Fatalf("spawned = %+v", h.host.Spawned)
	}
	if len(h.host.Prompts) != 0 {
		t.Errorf("a spawn also prompted a running agent: %v", h.host.Prompts)
	}
	if !strings.Contains(h.model.Calls()[1].Text(), "Starting a new claude in sheepdog-bench.") {
		t.Errorf("the model did not see the spawn echo:\n%s", h.model.Calls()[1].Text())
	}
}

// A spawn needs a directory and a known kind; with agent as well it is
// ambiguous. Each is an error the model reads, and nothing starts.
func TestPromptToolSpawnArgumentErrors(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{
			braintest.FC(ToolPrompt, map[string]any{"new_agent_kind": "codex"}),
			braintest.FC(ToolPrompt, map[string]any{"new_agent_dir": "app", "new_agent_kind": "gemini"}),
			braintest.FC(ToolPrompt, map[string]any{"new_agent_dir": "app", "agent": "auth", "text": "hi"}),
		}},
		braintest.Step{Text: braintest.Reply("Which project?", "")},
	), nil)
	if _, err := h.ask("start a codex", ""); err != nil {
		t.Fatal(err)
	}
	text := h.model.Calls()[1].Text()
	for _, want := range []string{"new_agent_dir is required", `new_agent_kind "gemini"`, "not both"} {
		if !strings.Contains(text, want) {
			t.Errorf("model did not read %q:\n%s", want, text)
		}
	}
	if len(h.host.Spawned) != 0 || len(h.host.Prompts) != 0 {
		t.Errorf("spawned %v, prompted %v", h.host.Spawned, h.host.Prompts)
	}
}

// The daemon's rail refusing a spawn (no recent words of the CEO's, the
// rate cap) is a brain.rail_blocked, like a refused send_keys.
func TestPromptToolSpawnRailBlocked(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolPrompt, map[string]any{"new_agent_dir": "app"})}},
		braintest.Step{Text: braintest.Reply("I can't start one now.", "")},
	), nil)
	h.host.SpawnErr = fmt.Errorf("%w: the user has said nothing that asks for a new agent", ErrNotPermitted)
	if _, err := h.ask("start a claude in app", ""); err != nil {
		t.Fatal(err)
	}
	if h.log.count("brain.rail_blocked") != 1 {
		t.Error("no brain.rail_blocked for a refused spawn")
	}
	if len(h.host.Spawned) != 0 {
		t.Errorf("spawned = %v", h.host.Spawned)
	}
}
