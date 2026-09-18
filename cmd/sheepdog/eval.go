package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

func printEvalUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog eval --set assistant|svp [flags]")
	fprintln(w, "")
	fprintln(w, "Runs the conversation eval set (testdata/eval/{assistant,svp}.jsonl) and prints")
	fprintln(w, "a pass/fail table. Offline by default: the assistant set is only validated")
	fprintln(w, "(no model), the SVP set runs through the brain with a scripted fake model (a")
	fprintln(w, "check of the harness — the rail, provenance wrapping — not of model judgement).")
	fprintln(w, "--live calls gemini-3.5-flash in plain text mode (never Live audio) for both.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --set assistant|svp   which set to run (required)")
	fprintln(w, "  --live                call the real model instead of validating/faking it")
	fprintln(w, "  --filter SUBSTR       only run cases whose id contains SUBSTR")
	fprintln(w, "  --dir PATH            eval file directory (default testdata/eval)")
	fprintln(w, "  --fixtures PATH       screen fixture directory (default testdata/herdr)")
	fprintln(w, "  --config PATH         config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH       load KEY=VALUE lines into the environment first")
	fprintln(w, "  --prompts-dir PATH    prompt override directory (default config prompts.dir)")
}

type evalOpts struct {
	set, filter, dir, fixtures, configPath, envFile, promptsDir string
	live                                                        bool
}

func parseEvalFlags(args []string, stderr io.Writer) (evalOpts, int, bool) {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printEvalUsage(stderr) }
	var o evalOpts
	fs.StringVar(&o.set, "set", "", "assistant | svp")
	fs.BoolVar(&o.live, "live", false, "call the real model")
	fs.StringVar(&o.filter, "filter", "", "only run cases whose id contains this")
	fs.StringVar(&o.dir, "dir", "testdata/eval", "eval file directory")
	fs.StringVar(&o.fixtures, "fixtures", "testdata/herdr", "screen fixture directory")
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.StringVar(&o.envFile, "env-file", "", "env file")
	fs.StringVar(&o.promptsDir, "prompts-dir", "", "prompt override directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	if o.set != "assistant" && o.set != "svp" {
		fprintln(stderr, "sheepdog eval: --set must be assistant or svp")
		return o, 2, false
	}
	return o, 0, true
}

// runEval implements `sheepdog eval`.
func runEval(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code, run := parseEvalFlags(args, stderr)
	if !run {
		return code
	}
	if o.envFile != "" {
		if err := config.LoadEnvFile(o.envFile); err != nil {
			fprintf(stderr, "sheepdog eval: %v\n", err)
			return 1
		}
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
		return 1
	}
	log := logging.NewJSONLLogger(stderr)
	promptsDir := o.promptsDir
	if promptsDir == "" {
		promptsDir = cfg.Prompts.Dir
	}
	pl := prompts.NewLoader(promptsDir, log)

	switch o.set {
	case "assistant":
		return runEvalAssistant(ctx, o, cfg, pl, stdout, stderr)
	case "svp":
		return runEvalSVP(ctx, o, cfg, log, stdout, stderr)
	}
	return 1
}

func runEvalAssistant(ctx context.Context, o evalOpts, cfg config.Config, pl *prompts.Loader, stdout, stderr io.Writer) int {
	cases, err := eval.LoadAssistantCases(o.dir + "/assistant.jsonl")
	if err != nil {
		fprintf(stderr, "sheepdog eval: %v\n", err)
		return 1
	}
	cases = filterAssistant(cases, o.filter)
	var results []eval.Result
	if !o.live {
		results = eval.RunAssistant(ctx, cases, pl, nil, "")
	} else {
		if len(cfg.Gemini.APIKeys) == 0 {
			fprintln(stderr, "sheepdog eval --live: no Gemini key (set GEMINI_API_KEY or use --env-file)")
			return 1
		}
		model, err := brain.NewGenaiModel(cfg.Gemini.APIKeys, logging.NewJSONLLogger(stderr))
		if err != nil {
			fprintf(stderr, "sheepdog eval: %v\n", err)
			return 1
		}
		results = eval.RunAssistant(ctx, cases, pl, model, eval.DefaultAssistantModel)
	}
	eval.WriteTable(stdout, "assistant", results)
	if eval.HasFailures(results) {
		return 1
	}
	return 0
}

func runEvalSVP(ctx context.Context, o evalOpts, cfg config.Config, log logging.Logger, stdout, stderr io.Writer) int {
	cases, err := eval.LoadSVPCases(o.dir + "/svp.jsonl")
	if err != nil {
		fprintf(stderr, "sheepdog eval: %v\n", err)
		return 1
	}
	cases = filterSVP(cases, o.filter)
	var results []eval.Result
	if !o.live {
		results = eval.RunSVPOffline(ctx, cases, o.fixtures)
	} else {
		if len(cfg.Gemini.APIKeys) == 0 {
			fprintln(stderr, "sheepdog eval --live: no Gemini key (set GEMINI_API_KEY or use --env-file)")
			return 1
		}
		model, err := brain.NewGenaiModel(cfg.Gemini.APIKeys, log)
		if err != nil {
			fprintf(stderr, "sheepdog eval: %v\n", err)
			return 1
		}
		results = eval.RunSVPLive(ctx, cases, o.fixtures, model, cfg.Brain.Model)
	}
	eval.WriteTable(stdout, "svp", results)
	if eval.HasFailures(results) {
		return 1
	}
	return 0
}

func filterAssistant(cases []eval.AssistantCase, substr string) []eval.AssistantCase {
	if substr == "" {
		return cases
	}
	out := cases[:0:0]
	for _, c := range cases {
		if strings.Contains(c.ID, substr) {
			out = append(out, c)
		}
	}
	return out
}

func filterSVP(cases []eval.SVPCase, substr string) []eval.SVPCase {
	if substr == "" {
		return cases
	}
	out := cases[:0:0]
	for _, c := range cases {
		if strings.Contains(c.ID, substr) {
			out = append(out, c)
		}
	}
	return out
}
