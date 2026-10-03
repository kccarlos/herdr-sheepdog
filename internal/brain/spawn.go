package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Starting a new agent (docs/roles.md "Starting an agent"): the prompt
// tool with new_agent_dir starts a Claude Code or Codex worker in a
// project directory instead of instructing a running one, with text, when
// given, as its first instruction. Which directory the CEO meant is the
// brain's judgement; the daemon only resolves a name against the
// directories it allows and refuses the rest with the candidates listed.

// Spawn kinds: the agent CLIs a new agent may be.
const (
	SpawnClaude = "claude"
	SpawnCodex  = "codex"
)

// SpawnKinds are the accepted new_agent_kind values.
var SpawnKinds = []string{SpawnClaude, SpawnCodex}

// SpawnRequest is one new agent the brain asked for.
type SpawnRequest struct {
	// Dir is the project directory: a name ("sheepdog-bench") or a path.
	Dir string
	// Kind is SpawnClaude or SpawnCodex.
	Kind string
	// Model is the CLI's --model, "" for its default.
	Model string
	// Text is the first instruction, "" for none.
	Text string
}

// Spawner is a Host that can start a new agent. A Host without it
// refuses new_agent_dir.
type Spawner interface {
	// Spawn starts the agent and returns at once with the spoken echo;
	// the first instruction follows once the agent is ready. A refusal
	// by the rail (no recent words of the CEO's, the rate cap) wraps
	// ErrNotPermitted.
	Spawn(ctx context.Context, req SpawnRequest) (string, error)
}

// spawning reports whether a prompt call asks for a new agent.
func spawning(args map[string]any) bool {
	for _, k := range []string{"new_agent_dir", "new_agent_kind", "new_agent_model"} {
		if v, _ := args[k].(string); strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// spawn runs a prompt call that asks for a new agent.
func (b *Brain) spawn(ctx context.Context, arg func(string) string) toolOutcome {
	sp, ok := b.host.(Spawner)
	if !ok {
		return toolOutcome{err: errors.New("starting a new agent is not available here")}
	}
	if arg("agent") != "" {
		return toolOutcome{err: errors.New("prompt: give agent to instruct a running agent, or new_agent_dir to start a new one, not both")}
	}
	req := SpawnRequest{Dir: arg("new_agent_dir"), Kind: strings.ToLower(arg("new_agent_kind")), Model: arg("new_agent_model"), Text: arg("text")}
	if req.Dir == "" {
		return toolOutcome{err: errors.New("prompt: new_agent_dir is required to start a new agent; ask the CEO which project")}
	}
	if req.Kind == "" {
		req.Kind = SpawnClaude
	}
	if req.Kind != SpawnClaude && req.Kind != SpawnCodex {
		return toolOutcome{err: fmt.Errorf("prompt: new_agent_kind %q: want %s", req.Kind, strings.Join(SpawnKinds, " or "))}
	}
	echo, err := sp.Spawn(ctx, req)
	if err != nil {
		out := toolOutcome{err: err}
		if errors.Is(err, ErrNotPermitted) {
			out.railBlocked = err.Error()
		}
		return out
	}
	return toolOutcome{acted: echo, result: echo}
}
