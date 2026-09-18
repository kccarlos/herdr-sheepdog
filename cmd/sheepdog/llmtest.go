package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func printLLMTestUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog llm-test --template NAME [flags]")
	fprintln(w, "")
	fprintln(w, "Runs one cheap-LLM template against the configured backend and prints the")
	fprintln(w, "parsed, validated JSON on stdout. llm.* events go to stderr as JSON lines.")
	fprintln(w, "Backend: SHEEPDOG_LLM_BASE_URL / SHEEPDOG_LLM_MODEL / SHEEPDOG_LLM_API_KEY or")
	fprintln(w, "the [llm] config section; the Gemini key pool backs the Gemini endpoint.")
	fprintln(w, "")
	fprintln(w, "templates:")
	fprintln(w, "  summarize_block      --screen FILE [--alias NAME]")
	fprintln(w, "  resolve_instruction  --text WORDS --agents LIST [--agent ALIAS]")
	fprintln(w, "                       LIST = alias[:state[:task]],...  e.g. auth:working:run tests,docs:blocked:whether to write")
	fprintln(w, "  spoken_answer        --text QUESTION --screen FILE [--alias NAME]")
	fprintln(w, "  summarize_done       --screen FILE [--alias NAME]")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH   config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --base-url URL  override the backend base URL")
	fprintln(w, "  --model NAME    override the model")
	fprintln(w, "  --api-key KEY   override the key (any non-empty string for Ollama)")
	fprintln(w, "  --timeout D     per-attempt timeout (default 15s; raise for a cold local model)")
}

type llmTestOpts struct {
	configPath, baseURL, model, apiKey           string
	template, screen, alias, text, agents, agent string
	timeout                                      time.Duration
}

func parseLLMTestFlags(args []string, stderr io.Writer) (llmTestOpts, int, bool) {
	fs := flag.NewFlagSet("llm-test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printLLMTestUsage(stderr) }
	var o llmTestOpts
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.StringVar(&o.baseURL, "base-url", "", "backend base URL")
	fs.StringVar(&o.model, "model", "", "model")
	fs.StringVar(&o.apiKey, "api-key", "", "API key")
	fs.StringVar(&o.template, "template", "", "template name")
	fs.StringVar(&o.screen, "screen", "", "screen file")
	fs.StringVar(&o.alias, "alias", "auth", "agent alias for summarize_block / spoken_answer")
	fs.StringVar(&o.text, "text", "", "user text / question")
	fs.StringVar(&o.agents, "agents", "", "agent list")
	fs.StringVar(&o.agent, "agent", "", "alias hint for resolve_instruction")
	fs.DurationVar(&o.timeout, "timeout", llm.DefaultTimeout, "per-attempt timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	switch o.template {
	case "summarize_block", "spoken_answer", "summarize_done":
		if o.screen == "" {
			fprintf(stderr, "sheepdog llm-test: %s needs --screen\n", o.template)
			return o, 2, false
		}
		if o.template == "spoken_answer" && o.text == "" {
			fprintln(stderr, "sheepdog llm-test: spoken_answer needs --text")
			return o, 2, false
		}
	case "resolve_instruction":
		if o.text == "" || o.agents == "" {
			fprintln(stderr, "sheepdog llm-test: resolve_instruction needs --text and --agents")
			return o, 2, false
		}
	default:
		fprintf(stderr, "sheepdog llm-test: unknown or missing --template %q\n\n", o.template)
		printLLMTestUsage(stderr)
		return o, 2, false
	}
	return o, 0, true
}

// parseAgentList turns "auth:working:run tests,docs:blocked:whether to X"
// into AgentSummary rows with synthetic pane ids.
func parseAgentList(list string) []llm.AgentSummary {
	var out []llm.AgentSummary
	for i, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, ":", 3)
		a := llm.AgentSummary{Alias: parts[0], PaneID: fmt.Sprintf("w%d:p1", i+1), Kind: "codex", State: "idle"}
		if len(parts) > 1 && parts[1] != "" {
			a.State = parts[1]
		}
		if len(parts) > 2 {
			if a.State == "blocked" {
				a.Question = parts[2]
			} else {
				a.Task = parts[2]
			}
		}
		out = append(out, a)
	}
	return out
}

// runLLMTest implements `sheepdog llm-test`.
func runLLMTest(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code, run := parseLLMTestFlags(args, stderr)
	if !run {
		return code
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
		return 1
	}
	if o.baseURL != "" {
		cfg.LLM.BaseURL = o.baseURL
	}
	if o.model != "" {
		cfg.LLM.Model = o.model
	}
	if o.apiKey != "" {
		cfg.LLM.APIKey = o.apiKey
	}
	keys := cfg.LLMKeys()
	if len(keys) == 0 {
		fprintln(stderr, "sheepdog llm-test: no API key (set SHEEPDOG_LLM_API_KEY, GEMINI_API_KEY or --api-key)")
		return 2
	}
	client, err := llm.New(llm.Config{BaseURL: cfg.LLM.BaseURL, Model: cfg.LLM.Model, APIKeys: keys, Timeout: o.timeout, Log: logging.NewJSONLLogger(stderr)})
	if err != nil {
		fprintf(stderr, "sheepdog llm-test: %v\n", err)
		return 1
	}
	fprintf(stderr, "sheepdog llm-test: base_url=%s model=%s keys=%d template=%s\n", cfg.LLM.BaseURL, cfg.LLM.Model, len(keys), o.template)

	var screen string
	if o.screen != "" {
		b, err := os.ReadFile(o.screen)
		if err != nil {
			fprintf(stderr, "sheepdog llm-test: %v\n", err)
			return 1
		}
		screen = string(b)
	}

	var result any
	switch o.template {
	case "summarize_block":
		result, err = client.SummarizeBlock(ctx, screen, o.alias)
	case "resolve_instruction":
		result, err = client.ResolveInstruction(ctx, o.text, parseAgentList(o.agents), o.agent)
	case "spoken_answer":
		result, err = client.SpokenAnswer(ctx, screen, o.text, o.alias)
	case "summarize_done":
		result, err = client.SummarizeDone(ctx, screen, o.alias)
	}
	if err != nil {
		fprintf(stderr, "sheepdog llm-test: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fprintf(stderr, "sheepdog llm-test: %v\n", err)
		return 1
	}
	return 0
}
