package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

func printEvalUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog eval --set assistant|svp|regressions|extracted|expanded|all [flags]")
	fprintln(w, "       sheepdog eval extract [flags]   (cases from an event-log window)")
	fprintln(w, "       sheepdog eval expand [flags]    (paraphrase and noise variants)")
	fprintln(w, "")
	fprintln(w, "Runs the conversation eval sets under testdata/eval and prints a pass/fail table.")
	fprintln(w, "Offline by default: assistant cases are only validated (no model; a case that")
	fprintln(w, "carries its logged baseline is graded against it), SVP cases run through the")
	fprintln(w, "brain with a scripted fake model (a check of the harness — the rail, provenance")
	fprintln(w, "wrapping, tool calls — not of model judgement); the checks about phrasing are")
	fprintln(w, "live only and listed as skipped. --live calls gemini-3.5-flash in plain text")
	fprintln(w, "mode (never Live audio).")
	fprintln(w, "")
	fprintln(w, "sets: assistant (assistant.jsonl), svp (svp.jsonl), regressions (regressions.jsonl")
	fprintln(w, "and regressions.assistant.jsonl), extracted (extracted/*.jsonl), expanded")
	fprintln(w, "(expanded.jsonl and expanded.assistant.jsonl), all (every set).")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --set NAME            which set to run (required)")
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

// evalSets are the --set names and the files each reads, relative to
// --dir (docs/iteration-2.md WP0b: the L0 corpus).
var evalSets = map[string]struct{ svp, assistant []string }{
	"assistant":   {assistant: []string{"assistant.jsonl"}},
	"svp":         {svp: []string{"svp.jsonl"}},
	"regressions": {svp: []string{"regressions.jsonl"}, assistant: []string{"regressions.assistant.jsonl"}},
	"extracted":   {svp: []string{"extracted/*.jsonl"}},
	"expanded":    {svp: []string{"expanded.jsonl"}, assistant: []string{"expanded.assistant.jsonl"}},
}

// evalSetOrder is the order `--set all` runs the sets in.
var evalSetOrder = []string{"assistant", "svp", "regressions", "extracted", "expanded"}

func parseEvalFlags(args []string, stderr io.Writer) (evalOpts, int, bool) {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printEvalUsage(stderr) }
	var o evalOpts
	fs.StringVar(&o.set, "set", "", "assistant | svp | regressions | extracted | expanded | all")
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
	if _, ok := evalSets[o.set]; !ok && o.set != "all" {
		fprintln(stderr, "sheepdog eval: --set must be one of assistant, svp, regressions, extracted, expanded, all")
		return o, 2, false
	}
	return o, 0, true
}

// runEval implements `sheepdog eval`.
func runEval(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "extract":
			return runEvalExtract(args[1:], stdout, stderr)
		case "expand":
			return runEvalExpand(args[1:], stderr)
		}
	}
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

	sets := []string{o.set}
	if o.set == "all" {
		sets = evalSetOrder
	}
	var models *evalModels
	if o.live {
		if len(cfg.Gemini.APIKeys) == 0 {
			fprintln(stderr, "sheepdog eval --live: no Gemini key (set GEMINI_API_KEY or use --env-file)")
			return 1
		}
		m, err := brain.NewGenaiModel(cfg.Gemini.APIKeys, log)
		if err != nil {
			fprintf(stderr, "sheepdog eval: %v\n", err)
			return 1
		}
		models = &evalModels{model: m, brainModel: cfg.Brain.Model}
	}
	failed := false
	for _, set := range sets {
		results, err := runEvalSet(ctx, o, set, pl, models)
		if err != nil {
			fprintf(stderr, "sheepdog eval: %v\n", err)
			return 1
		}
		eval.WriteTable(stdout, set, results)
		failed = failed || eval.HasFailures(results)
	}
	if failed {
		return 1
	}
	return 0
}

// evalModels is the live run's model (nil: offline).
type evalModels struct {
	model      *brain.GenaiModel
	brainModel string
}

// runEvalSet loads and runs one set: its SVP files through the brain,
// its assistant files through the voice prompt.
func runEvalSet(ctx context.Context, o evalOpts, set string, pl *prompts.Loader, models *evalModels) ([]eval.Result, error) {
	spec := evalSets[set]
	var results []eval.Result
	for _, pattern := range spec.assistant {
		files, err := globOrOne(filepath.Join(o.dir, pattern))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			cases, err := eval.LoadAssistantCases(f)
			if err != nil {
				return nil, err
			}
			cases = filterAssistant(cases, o.filter)
			if models == nil {
				results = append(results, eval.RunAssistant(ctx, cases, pl, nil, "")...)
			} else {
				results = append(results, eval.RunAssistant(ctx, cases, pl, models.model, eval.DefaultAssistantModel)...)
			}
		}
	}
	for _, pattern := range spec.svp {
		files, err := globOrOne(filepath.Join(o.dir, pattern))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			cases, err := eval.LoadSVPCases(f)
			if err != nil {
				return nil, err
			}
			cases = filterSVP(cases, o.filter)
			if models == nil {
				results = append(results, eval.RunSVPOffline(ctx, cases, o.fixtures)...)
			} else {
				results = append(results, eval.RunSVPLive(ctx, cases, o.fixtures, models.model, models.brainModel)...)
			}
		}
	}
	return results, nil
}

// globOrOne expands a pattern with a wildcard (sorted; none is an error)
// or returns a plain path as is, so a missing file is reported by the
// loader.
func globOrOne(pattern string) ([]string, error) {
	if !strings.ContainsAny(pattern, "*?[") {
		return []string{pattern}, nil
	}
	m, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, errors.New("no files match " + pattern)
	}
	sort.Strings(m)
	return m, nil
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
