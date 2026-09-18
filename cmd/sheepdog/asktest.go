package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/cli"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func printAskTestUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog ask-test --workspace DIR --question \"Q\" [flags]")
	fprintln(w, "")
	fprintln(w, "Runs the read-only CLI query tool (plan §3.5, WP4) once against workspace")
	fprintln(w, "and question, then (unless --raw) the spoken_answer cheap-LLM pass, and")
	fprintln(w, "prints one JSON object with backend, dur_ms, exit, killed, capped, raw and")
	fprintln(w, "spoken. cli.detected / cli.run / llm.call events go to stderr as JSON lines.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH     config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --workspace DIR   the agent's workspace (cwd for the CLI backend)")
	fprintln(w, "  --question TEXT   the question")
	fprintln(w, "  --backend NAME    codex | claude (default: cli.prefer / auto)")
	fprintln(w, "  --raw             skip spoken_answer; print the CLI's raw output only")
	fprintln(w, "  --alias NAME      agent alias for the spoken_answer prompt (default \"the agent\")")
	fprintln(w, "  --base-url URL    spoken_answer backend base URL (default from config)")
	fprintln(w, "  --model NAME      spoken_answer model (default from config)")
	fprintln(w, "  --api-key KEY     spoken_answer API key (any non-empty string for Ollama)")
	fprintln(w, "  --llm-timeout D   spoken_answer per-attempt timeout (default 15s; raise for a cold local model)")
}

type askTestOpts struct {
	configPath, workspace, question, backend, alias string
	baseURL, model, apiKey                          string
	raw                                             bool
	llmTimeout                                      time.Duration
}

func parseAskTestFlags(args []string, stderr io.Writer) (askTestOpts, int, bool) {
	fs := flag.NewFlagSet("ask-test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printAskTestUsage(stderr) }
	var o askTestOpts
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.StringVar(&o.workspace, "workspace", "", "agent workspace directory")
	fs.StringVar(&o.question, "question", "", "question")
	fs.StringVar(&o.backend, "backend", "", "codex | claude")
	fs.BoolVar(&o.raw, "raw", false, "skip the spoken_answer pass")
	fs.StringVar(&o.alias, "alias", "the agent", "agent alias for spoken_answer")
	fs.StringVar(&o.baseURL, "base-url", "", "spoken_answer backend base URL")
	fs.StringVar(&o.model, "model", "", "spoken_answer model")
	fs.StringVar(&o.apiKey, "api-key", "", "spoken_answer API key")
	fs.DurationVar(&o.llmTimeout, "llm-timeout", llm.DefaultTimeout, "spoken_answer per-attempt timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	if o.workspace == "" {
		fprintln(stderr, "sheepdog ask-test: needs --workspace")
		return o, 2, false
	}
	if o.question == "" {
		fprintln(stderr, "sheepdog ask-test: needs --question")
		return o, 2, false
	}
	switch o.backend {
	case "", string(cli.Codex), string(cli.Claude):
	default:
		fprintf(stderr, "sheepdog ask-test: --backend must be %s or %s\n", cli.Codex, cli.Claude)
		return o, 2, false
	}
	return o, 0, true
}

// runAskTest implements `sheepdog ask-test`.
func runAskTest(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code, run := parseAskTestFlags(args, stderr)
	if !run {
		return code
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
		return 1
	}
	log := logging.NewJSONLLogger(stderr)

	runnerCfg := cli.Config{
		Prefer: cfg.CLI.Prefer, CodexBin: cfg.CLI.CodexBin, ClaudeBin: cfg.CLI.ClaudeBin,
		CodexCmd: cfg.CLI.CodexCmd, ClaudeCmd: cfg.CLI.ClaudeCmd,
		ClaudeModel: cfg.CLI.ClaudeModel, CodexReasoning: cfg.CLI.CodexReasoning,
		Timeout: time.Duration(cfg.CLI.TimeoutS) * time.Second, MaxOutputBytes: cfg.CLI.MaxOutputKB << 10,
		Log: log,
	}
	det := cli.Detect(runnerCfg, log)
	runner, err := cli.NewRunner(runnerCfg, det)
	if err != nil {
		fprintf(stderr, "sheepdog ask-test: %v\n", err)
		return 1
	}

	backend := cli.Backend(o.backend)
	if backend == "" {
		backend, err = runner.ResolveBackend()
		if err != nil {
			fprintf(stderr, "sheepdog ask-test: %v\n", err)
			return 2
		}
	}
	fprintf(stderr, "sheepdog ask-test: backend=%s workspace=%s\n", backend, o.workspace)

	result, runErr := runner.RunBackend(ctx, backend, o.workspace, o.question)
	out := map[string]any{
		"backend": string(result.Backend),
		"dur_ms":  result.DurMS,
		"exit":    result.Exit,
		"killed":  result.Killed,
		"capped":  result.Capped,
		"raw":     result.Raw,
	}
	if runErr != nil {
		out["error"] = runErr.Error()
	}

	if runErr == nil && !o.raw {
		if err := addSpokenAnswer(ctx, out, cfg, o, result.Raw, log); err != nil {
			out["spoken_error"] = err.Error()
		}
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fprintf(stderr, "sheepdog ask-test: %v\n", err)
		return 1
	}
	if runErr != nil {
		return 1
	}
	return 0
}

// addSpokenAnswer runs the spoken_answer template on raw and adds "spoken"
// to out.
func addSpokenAnswer(ctx context.Context, out map[string]any, cfg config.Config, o askTestOpts, raw string, log logging.Logger) error {
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
		if config.IsGeminiBaseURL(cfg.LLM.BaseURL) {
			return errors.New("no API key (set SHEEPDOG_LLM_API_KEY, GEMINI_API_KEY or --api-key)")
		}
		keys = []string{"local"} // local endpoints (Ollama) ignore the key
	}
	client, err := llm.New(llm.Config{BaseURL: cfg.LLM.BaseURL, Model: cfg.LLM.Model, APIKeys: keys, Timeout: o.llmTimeout, Log: log})
	if err != nil {
		return err
	}
	ans, err := client.SpokenAnswer(ctx, raw, o.question, o.alias)
	if err != nil {
		return err
	}
	out["spoken"] = ans.Spoken
	return nil
}
