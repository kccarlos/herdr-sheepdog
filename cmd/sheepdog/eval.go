package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
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
	fprintln(w, "       sheepdog eval sim [flags]       (simulated-user conversations, judged)")
	fprintln(w, "       sheepdog eval calib CMD [flags] (judge calibration set: judge, agreement)")
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
	fprintln(w, "  --provider ID         live brain provider: gemini (AI Studio keys, the default),")
	fprintln(w, "                        vertex or deepseek")
	fprintln(w, "  --model ID            live brain model (default brain.model)")
	fprintln(w, "  --judge KIND          LLM judge for the wording checks and judge criteria:")
	fprintln(w, "                        claude (default with --live; SHEEPDOG_JUDGE overrides), vertex,")
	fprintln(w, "                        ollama, fake or none")
	fprintln(w, "  --judge-model ID      judge model (default sonnet on claude, "+eval.DefaultVertexModel+" on vertex,")
	fprintln(w, "                        "+eval.DefaultOllamaModel+" on ollama)")
	fprintln(w, "  --ollama-url URL      Ollama OpenAI-compatible root (default "+eval.DefaultOllamaURL+")")
	fprintln(w, "  --repeat N            live: run each case N times; per-case pass rates (errored")
	fprintln(w, "                        and not-run runs excluded) and the failure taxonomy")
}

type evalOpts struct {
	set, filter, dir, fixtures, configPath, envFile, promptsDir string
	live                                                        bool
	provider, model, judge, judgeModel, ollamaURL               string
	repeat                                                      int
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
	fs.StringVar(&o.provider, "provider", "", "live brain provider: gemini, vertex or deepseek")
	fs.StringVar(&o.model, "model", "", "live brain model")
	fs.StringVar(&o.judge, "judge", "", "LLM judge: vertex, ollama, fake or none")
	fs.StringVar(&o.judgeModel, "judge-model", "", "judge model id")
	fs.StringVar(&o.ollamaURL, "ollama-url", "", "Ollama OpenAI-compatible root")
	fs.IntVar(&o.repeat, "repeat", 1, "live: run each case N times and report pass rates")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	if o.repeat < 1 || (o.repeat > 1 && !o.live) {
		fprintln(stderr, "sheepdog eval: --repeat N (N ≥ 1) is for --live runs; offline is deterministic")
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
		case "sim":
			return runEvalSim(ctx, args[1:], stdout, stderr)
		case "calib":
			return runEvalCalib(ctx, args[1:], stdout, stderr)
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
		m, err := evalLiveModels(o, cfg, log)
		if err != nil {
			fprintf(stderr, "sheepdog eval --live: %v\n", err)
			return 1
		}
		models = m
		if m.judgeNote != "" {
			fprintf(stderr, "sheepdog eval --live: %s\n", m.judgeNote)
		}
	}
	failed := false
	for _, set := range sets {
		var recs []eval.RunRecord
		for rep := 1; rep <= o.repeat; rep++ {
			results, err := runEvalSet(ctx, o, set, pl, models)
			if err != nil {
				fprintf(stderr, "sheepdog eval: %v\n", err)
				return 1
			}
			if o.repeat == 1 {
				eval.WriteTable(stdout, set, results)
			}
			for _, r := range results {
				recs = append(recs, r.Record(rep))
			}
			failed = failed || eval.HasFailures(results)
		}
		if o.repeat > 1 {
			eval.WriteStatsTable(stdout, fmt.Sprintf("eval --set %s, repeat %d", set, o.repeat), recs)
		}
	}
	if models != nil {
		eval.WriteSpend(stdout, models.meter)
	}
	if failed {
		return 1
	}
	return 0
}

// evalModels is the live run's models (nil: offline): the brain's, the
// assistant set's (AI Studio Gemini keys; nil without them) and the
// judge (nil: none).
type evalModels struct {
	model      brain.Model
	provider   string
	brainModel string
	assistant  *brain.GenaiModel
	judge      eval.Judge
	meter      *eval.Meter
	// judgeNote says why the default judge is off, when it is.
	judgeNote string
}

// evalLiveModels builds the live run's models from the flags and config.
func evalLiveModels(o evalOpts, cfg config.Config, log logging.Logger) (*evalModels, error) {
	out := &evalModels{provider: o.provider, brainModel: cfg.Brain.Model, meter: eval.NewMeter()}
	if o.model != "" {
		out.brainModel = o.model
	}
	if len(cfg.Gemini.APIKeys) > 0 {
		m, err := brain.NewGenaiModel(cfg.Gemini.APIKeys, log)
		if err != nil {
			return nil, err
		}
		out.assistant = m
	}
	switch o.provider {
	case "", brain.ProviderGemini:
		if out.assistant == nil {
			return nil, errors.New("no Gemini key (set GEMINI_API_KEY or use --env-file, or pick --provider vertex|deepseek)")
		}
		out.model, out.provider = out.assistant, ""
	default:
		models, err := app.BrainModels(cfg, log)
		if err != nil {
			return nil, err
		}
		m, ok := models[o.provider]
		if !ok {
			return nil, fmt.Errorf("provider %q has no credentials (vertex: VERTEX_API_KEY and VERTEX_PROJECT; deepseek: DEEPSEEK_API_KEY)", o.provider)
		}
		out.model = m
	}
	kind, model := judgeChoice(o.judge, o.judgeModel)
	if o.judge == "" {
		// The default judge needs its backend (the claude CLI, or Vertex
		// credentials); without it the wording checks stay the code's (as
		// before the judge existed) rather than failing a run that asked
		// for no judge.
		if miss := judgeMissing(kind, cfg); miss != "" {
			out.judgeNote = "the default judge is unavailable (" + miss + "): wording checks use the code's substring and word lists; pass --judge to choose one"
			kind = eval.BackendNone
		}
	}
	j, err := evalJudge(kind, model, o.ollamaURL, cfg, log, out.meter)
	if err != nil {
		return nil, fmt.Errorf("judge: %w", err)
	}
	out.judge = j
	return out, nil
}

// evalJudge builds the judge of kind (nil for none).
func evalJudge(kind, model, ollamaURL string, cfg config.Config, log logging.Logger, meter *eval.Meter) (eval.Judge, error) {
	switch kind {
	case eval.BackendNone:
		return nil, nil
	case eval.BackendFake:
		return &eval.FakeJudge{}, nil
	}
	jm, id, err := newJudgeModel(kind, model, judgeVertex{cfg.Gemini.Vertex.APIKey, cfg.Gemini.Vertex.Project, ollamaURL}, make(chan struct{}, 4), log)
	if err != nil {
		return nil, err
	}
	return newLLMJudge(jm, id, kind, meter), nil
}

// judgeMissing says why a judge backend cannot run here ("" when it can).
func judgeMissing(kind string, cfg config.Config) string {
	switch kind {
	case eval.BackendClaude:
		if _, err := exec.LookPath("claude"); err != nil {
			return "no claude CLI on PATH"
		}
	case eval.BackendVertex:
		return app.VertexBrainMissing(cfg.Gemini.Vertex)
	}
	return ""
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
			switch {
			case models == nil:
				results = append(results, eval.RunAssistant(ctx, cases, pl, nil, "")...)
			case models.assistant == nil:
				for _, c := range cases {
					results = append(results, eval.Result{ID: c.ID, Status: eval.StatusSkipped, Detail: "no Gemini key for the assistant set"})
				}
			default:
				results = append(results, eval.RunAssistant(ctx, cases, pl, models.assistant, eval.DefaultAssistantModel)...)
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
				results = append(results, eval.RunSVPLiveWith(ctx, cases, o.fixtures, eval.SVPLive{Model: models.model, ModelName: models.brainModel, Provider: models.provider, Judge: models.judge, Meter: models.meter})...)
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

// altFor is the alternate model a judge's retries use (Vertex Flash-Lite
// only).
func altFor(kind, id string) string {
	if kind == eval.BackendVertex && id == eval.DefaultVertexModel {
		return eval.AltJudgeModel
	}
	return ""
}
