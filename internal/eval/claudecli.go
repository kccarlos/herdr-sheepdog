package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"google.golang.org/genai"
)

// The Claude judge: Claude Sonnet through the Claude Code CLI in headless
// mode (`claude -p`), on the signed-in user's Claude subscription. No API
// key is used or read: ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN are
// removed from the child's environment, so a key in the shell can never
// turn the judge into API spend. Picked by the 2026-10-06 judge bake-off
// (docs/dev/eval/calibration/README.md).
//
// One call is one `claude -p` process with no tools, no MCP servers, no
// settings files, no CLAUDE.md (an empty working directory) and no saved
// session: the system instruction goes in as --system-prompt, the
// response schema as --json-schema, the user text on stdin. The process
// answers with one JSON object whose structured_output is the verdict.
// The CLI has no temperature setting; the verdicts are therefore not
// deterministic (a rerun moves a few pairs).

// BackendClaude is the --judge kind of the Claude Code CLI judge.
const BackendClaude = "claude"

// DefaultClaudeModel is the CLI model alias the Claude judge calls.
const DefaultClaudeModel = "sonnet"

// ErrClaudeUsageLimit is returned when the subscription's usage limit
// (the 5-hour session or the weekly limit) stops the CLI. Never retried.
var ErrClaudeUsageLimit = errors.New("claude cli: usage limit reached")

// ClaudeCLI is a brain.Model over `claude -p`. Safe for concurrent use;
// bound the calls in flight with Patient (each call is a process).
type ClaudeCLI struct {
	// Bin is the claude executable (default "claude" on PATH).
	Bin string
	// Dir is the working directory of each call; empty makes one empty
	// temporary directory on first use (so no project CLAUDE.md loads).
	Dir string

	mu sync.Mutex
}

func (c *ClaudeCLI) workDir() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Dir != "" {
		return c.Dir, nil
	}
	d, err := os.MkdirTemp("", "sheepdog-claude-judge-")
	if err != nil {
		return "", err
	}
	c.Dir = d
	return d, nil
}

// cliResult is the part of `claude -p --output-format json` read here.
type cliResult struct {
	IsError          bool            `json:"is_error"`
	Subtype          string          `json:"subtype"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	Usage            struct {
		Input         int `json:"input_tokens"`
		CacheCreation int `json:"cache_creation_input_tokens"`
		CacheRead     int `json:"cache_read_input_tokens"`
		Output        int `json:"output_tokens"`
	} `json:"usage"`
}

// Generate implements brain.Model.
func (c *ClaudeCLI) Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	dir, err := c.workDir()
	if err != nil {
		return nil, err
	}
	args := []string{"-p", "--model", orDefault(model, DefaultClaudeModel), "--output-format", "json",
		"--tools", "", "--strict-mcp-config", "--setting-sources", "", "--no-session-persistence", "--disable-slash-commands"}
	if cfg != nil && cfg.SystemInstruction != nil {
		args = append(args, "--system-prompt", contentText(cfg.SystemInstruction))
	}
	if cfg != nil && cfg.ResponseSchema != nil {
		b, err := json.Marshal(jsonSchema(cfg.ResponseSchema))
		if err != nil {
			return nil, err
		}
		args = append(args, "--json-schema", string(b))
	}
	var user []string
	for _, ct := range contents {
		if t := contentText(ct); t != "" {
			user = append(user, t)
		}
	}
	cmd := exec.CommandContext(ctx, orDefault(c.Bin, "claude"), args...)
	cmd.Dir = dir
	cmd.Env = cliEnv(os.Environ())
	cmd.Stdin = strings.NewReader(strings.Join(user, "\n\n"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var res cliResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("claude cli: %v: %s", runErr, firstLine(strings.TrimSpace(stderr.String()+" "+stdout.String())))
		}
		return nil, fmt.Errorf("claude cli: unparseable output %q", firstLine(stdout.String()))
	}
	if res.IsError || runErr != nil {
		msg := firstLine(strings.TrimSpace(res.Result + " " + res.Subtype))
		if usageLimited(msg) {
			return nil, fmt.Errorf("%w: %s", ErrClaudeUsageLimit, msg)
		}
		return nil, fmt.Errorf("claude cli: %s", msg)
	}
	text := res.Result
	if len(res.StructuredOutput) > 0 && string(res.StructuredOutput) != "null" {
		text = string(res.StructuredOutput)
	}
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: genai.NewContentFromText(text, genai.RoleModel)}},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        int32(res.Usage.Input + res.Usage.CacheCreation + res.Usage.CacheRead),
			CachedContentTokenCount: int32(res.Usage.CacheRead),
			CandidatesTokenCount:    int32(res.Usage.Output),
		},
	}, nil
}

// usageLimited reports whether a CLI error is the subscription's limit.
func usageLimited(msg string) bool {
	s := strings.ToLower(msg)
	return strings.Contains(s, "usage limit") || strings.Contains(s, "limit reached") || strings.Contains(s, "limit will reset") || strings.Contains(s, "out of extra usage")
}

// cliEnv is env without API credentials (the judge runs on the
// subscription only) and without the marker of a parent Claude Code
// session.
func cliEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDECODE":
			continue
		}
		out = append(out, kv)
	}
	return out
}

func contentText(c *genai.Content) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range c.Parts {
		if p != nil && p.Text != "" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// jsonSchema converts a genai response schema to JSON Schema.
func jsonSchema(s *genai.Schema) map[string]any {
	out := map[string]any{}
	if s == nil {
		return out
	}
	if s.Type != "" {
		out["type"] = strings.ToLower(string(s.Type))
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for k, v := range s.Properties {
			props[k] = jsonSchema(v)
		}
		out["properties"] = props
	}
	if len(s.Required) > 0 {
		out["required"] = s.Required
	}
	if s.Items != nil {
		out["items"] = jsonSchema(s.Items)
	}
	return out
}
