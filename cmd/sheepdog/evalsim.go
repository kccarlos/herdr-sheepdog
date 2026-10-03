package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

func printEvalSimUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog eval sim [flags]")
	fprintln(w, "")
	fprintln(w, "Simulated-user conversations (testdata/eval/sim/*.json): an LLM plays the CEO")
	fprintln(w, "with a goal and talks, in text, to the real supervisor and brain over a mock")
	fprintln(w, "herdr whose workers change on cue; it hangs up with end_call, or a cap (turns,")
	fprintln(w, "time, silence) ends the call. The judge, not the simulated user, then decides")
	fprintln(w, "each rubric item. Offline by default: the scripted brain, the scenario's own")
	fprintln(w, "script lines and a fake judge (a check of the harness and the scenario files).")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --live                real brain, simulated user and judge (costs money)")
	fprintln(w, "  --provider ID         live brain provider: vertex or deepseek (gemini, the AI")
	fprintln(w, "                        Studio free tier, only with --allow-ai-studio)")
	fprintln(w, "  --model ID            live brain model (default brain.model)")
	fprintln(w, "  --simuser KIND        vertex (default with --live), ollama or fake")
	fprintln(w, "  --simuser-model ID    simulated user model (default "+eval.DefaultJudgeModel+")")
	fprintln(w, "  --judge KIND          vertex (default with --live), ollama or fake")
	fprintln(w, "  --judge-model ID      judge model (default "+eval.DefaultJudgeModel+")")
	fprintln(w, "  --ollama-url URL      Ollama OpenAI-compatible root (default "+eval.DefaultOllamaURL+")")
	fprintln(w, "  --files GLOB          scenario files (default testdata/eval/sim/*.json)")
	fprintln(w, "  --filter SUBSTR       only scenarios whose id contains SUBSTR")
	fprintln(w, "  --fixtures PATH       screen fixture directory (default testdata/herdr)")
	fprintln(w, "  --events DIR          write each run's daemon events as JSONL under DIR")
	fprintln(w, "  --config PATH         config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH       load KEY=VALUE lines into the environment first")
	fprintln(w, "  --prompts-dir PATH    prompt override directory (default config prompts.dir)")
}

type evalSimOpts struct {
	live, allowAIStudio                    bool
	provider, model, simuser, simuserModel string
	judge, judgeModel, ollamaURL           string
	files, filter, fixtures, events        string
	configPath, envFile, promptsDir        string
}

func parseEvalSimFlags(args []string, stderr io.Writer) (evalSimOpts, int, bool) {
	fs := flag.NewFlagSet("eval sim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printEvalSimUsage(stderr) }
	var o evalSimOpts
	fs.BoolVar(&o.live, "live", false, "real models")
	fs.BoolVar(&o.allowAIStudio, "allow-ai-studio", false, "allow the gemini (AI Studio) brain provider")
	fs.StringVar(&o.provider, "provider", "", "live brain provider")
	fs.StringVar(&o.model, "model", "", "live brain model")
	fs.StringVar(&o.simuser, "simuser", "", "simulated user backend")
	fs.StringVar(&o.simuserModel, "simuser-model", "", "simulated user model")
	fs.StringVar(&o.judge, "judge", "", "judge backend")
	fs.StringVar(&o.judgeModel, "judge-model", "", "judge model")
	fs.StringVar(&o.ollamaURL, "ollama-url", "", "Ollama root")
	fs.StringVar(&o.files, "files", "testdata/eval/sim/*.json", "scenario files")
	fs.StringVar(&o.filter, "filter", "", "scenario id substring")
	fs.StringVar(&o.fixtures, "fixtures", "testdata/herdr", "screen fixture directory")
	fs.StringVar(&o.events, "events", "", "event JSONL directory")
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.StringVar(&o.envFile, "env-file", "", "env file")
	fs.StringVar(&o.promptsDir, "prompts-dir", "", "prompt override directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	if o.live {
		switch o.provider {
		case brain.ProviderVertex, brain.ProviderDeepSeek:
		case brain.ProviderGemini:
			if !o.allowAIStudio {
				fprintln(stderr, "sheepdog eval sim: --provider gemini is the AI Studio free tier; pass --allow-ai-studio to use it anyway")
				return o, 2, false
			}
		default:
			fprintln(stderr, "sheepdog eval sim --live: --provider must be vertex or deepseek")
			return o, 2, false
		}
	}
	return o, 0, true
}

// runEvalSim implements `sheepdog eval sim`.
func runEvalSim(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code, run := parseEvalSimFlags(args, stderr)
	if !run {
		return code
	}
	if o.envFile != "" {
		if err := config.LoadEnvFile(o.envFile); err != nil {
			fprintf(stderr, "sheepdog eval sim: %v\n", err)
			return 1
		}
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
		return 1
	}
	scenarios, err := loadSimScenarios(o.files, o.filter)
	if err != nil {
		fprintf(stderr, "sheepdog eval sim: %v\n", err)
		return 1
	}
	// Daemon events go to the run files (--events), not the terminal.
	log := logging.NopLogger{}
	opts, err := evalSimOptions(o, cfg, log)
	if err != nil {
		fprintf(stderr, "sheepdog eval sim: %v\n", err)
		return 1
	}
	var runs []eval.SimRun
	for _, sc := range scenarios {
		r := eval.RunSim(ctx, sc, opts, 1)
		runs = append(runs, r)
		fprintf(stderr, "%s: %s (%s) ≈ $%.4f\n", sc.ID, r.Status, r.EndReason, r.CostUSD)
	}
	eval.WriteSimTable(stdout, runs)
	for _, r := range runs {
		if r.Status == eval.RunFail || r.Status == eval.RunError {
			return 1
		}
	}
	return 0
}

// loadSimScenarios reads every scenario file of the glob, filtered.
func loadSimScenarios(glob, filter string) ([]eval.SimScenario, error) {
	files, err := globOrOne(glob)
	if err != nil {
		return nil, err
	}
	var out []eval.SimScenario
	for _, f := range files {
		scs, err := eval.LoadSimFile(f)
		if err != nil {
			return nil, err
		}
		for _, sc := range scs {
			if filter == "" || strings.Contains(sc.ID, filter) {
				out = append(out, sc)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no scenarios in %s (filter %q)", glob, filter)
	}
	return out, nil
}

// evalSimOptions builds the run options: offline fakes, or the live
// brain, simulated user and judge.
func evalSimOptions(o evalSimOpts, cfg config.Config, log logging.Logger) (eval.SimOptions, error) {
	bcfg := app.BrainConfig(cfg)
	promptsDir := o.promptsDir
	if promptsDir == "" {
		promptsDir = cfg.Prompts.Dir
	}
	bcfg.Prompts = prompts.NewLoader(promptsDir, log)
	opts := eval.SimOptions{BrainConfig: bcfg, FixtureDir: o.fixtures, EventsDir: o.events}
	if o.events != "" {
		opts.EventsDir = filepath.Clean(o.events)
	}
	if !o.live {
		opts.Offline = true
		opts.BrainConfig.Provider, opts.BrainConfig.Model = "", ""
		return opts, nil
	}
	models, err := app.BrainModels(cfg, log)
	if err != nil {
		return opts, err
	}
	m, ok := models[o.provider]
	if !ok {
		return opts, fmt.Errorf("provider %q has no credentials (vertex: VERTEX_API_KEY and VERTEX_PROJECT; deepseek: DEEPSEEK_API_KEY)", o.provider)
	}
	opts.BrainConfig.Provider = o.provider
	if o.model != "" {
		opts.BrainConfig.Model = o.model
	}
	opts.NewBrainModel = func() brain.Model { return m }

	// One limit for every judge and simulated-user call of the campaign:
	// a burst of parallel judge calls got 429s from Vertex Flash-Lite.
	sem := make(chan struct{}, 6)
	backend := func(kind, model string) (brain.Model, string, error) {
		if kind == "" {
			kind = eval.BackendVertex
		}
		m, id, err := eval.NewBackend(eval.BackendOptions{Kind: kind, Model: model, VertexAPIKey: cfg.Gemini.Vertex.APIKey, VertexProject: cfg.Gemini.Vertex.Project, OllamaURL: o.ollamaURL}, log)
		return eval.Patient(m, sem, 5), id, err
	}
	um, uid, err := backend(o.simuser, o.simuserModel)
	if err != nil {
		return opts, fmt.Errorf("simulated user: %w", err)
	}
	if um != nil {
		opts.NewSimUser = func(meter *eval.Meter) eval.SimUser {
			return &eval.LLMSimUser{Model: eval.Metered(um, meter, "simuser"), ModelID: uid}
		}
	}
	jm, jid, err := backend(o.judge, o.judgeModel)
	if err != nil {
		return opts, fmt.Errorf("judge: %w", err)
	}
	if jm != nil {
		opts.NewJudge = func(meter *eval.Meter) eval.Judge {
			return &eval.LLMJudge{Model: eval.Metered(jm, meter, "judge"), ModelID: jid}
		}
	}
	return opts, nil
}
