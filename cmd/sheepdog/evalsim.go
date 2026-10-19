package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/bench"
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

func printEvalSimUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog eval sim [flags]")
	fprintln(w, "")
	fprintln(w, "Simulated-user conversations (testdata/eval/sim/*.json): an LLM plays the user")
	fprintln(w, "with a goal and talks, in text, to the real supervisor and orchestrator over a mock")
	fprintln(w, "herdr whose workers change on cue; it hangs up with end_call, or a cap (turns,")
	fprintln(w, "time, silence) ends the call. The judge, not the simulated user, then decides")
	fprintln(w, "each rubric item. Offline by default: the scripted orchestrator, the scenario's own")
	fprintln(w, "script lines and a fake judge (a check of the harness and the scenario files).")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --live                real orchestrator, simulated user and judge (costs money)")
	fprintln(w, "  --provider ID         live orchestrator provider: vertex, deepseek or anthropic")
	fprintln(w, "                        (gemini, the AI Studio free tier, only with --allow-ai-studio);")
	fprintln(w, "                        anthropic's effort comes from [anthropic] in --config")
	fprintln(w, "  --fallback-provider ID second orchestrator provider the failover can retry a step on:")
	fprintln(w, "                        vertex, deepseek, anthropic, gemini (needs --allow-ai-studio)")
	fprintln(w, "                        or none.")
	fprintln(w, "                        Default: every provider with credentials, as the daemon")
	fprintln(w, "                        registers them (gemini only with --allow-ai-studio)")
	fprintln(w, "  --model ID            live orchestrator model (default brain.model)")
	fprintln(w, "  --utterance-timeout D an experiment: override brain.utterance_timeout_s for this")
	fprintln(w, "                        run (e.g. 25s), to measure what the timeout costs")
	fprintln(w, "  --simuser KIND        vertex (default with --live), deepseek, ollama or fake")
	fprintln(w, "  --simuser-model ID    simulated user model (default "+eval.DefaultVertexModel+")")
	fprintln(w, judgeHelp)
	fprintln(w, "  --judge-model ID      judge model (default sonnet for claude, "+eval.DefaultVertexModel+" for vertex)")
	fprintln(w, "  --ollama-url URL      Ollama OpenAI-compatible root (default "+eval.DefaultOllamaURL+")")
	fprintln(w, "  --repeat N            run every scenario N times (default 1); pass rates exclude")
	fprintln(w, "                        errored and not-run runs")
	fprintln(w, "  --parallel N          conversations at once (default 1)")
	fprintln(w, "  --budget-usd X        stop starting runs once the estimated spend reaches X")
	fprintln(w, "                        (the rest are not_run; default 0 = no limit)")
	fprintln(w, "  --out DIR             write report.json and report.md under DIR")
	fprintln(w, "  --files GLOB          scenario files (default testdata/eval/sim/*.json)")
	fprintln(w, "  --filter SUBSTR       only scenarios whose id contains SUBSTR")
	fprintln(w, "  --fixtures PATH       screen fixture directory (default testdata/herdr)")
	fprintln(w, "  --events DIR          write each run's daemon events as JSONL under DIR")
	fprintln(w, "  --config PATH         config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH       load KEY=VALUE lines into the environment first")
	fprintln(w, "  --prompts-dir PATH    prompt override directory (default config prompts.dir)")
}

type evalSimOpts struct {
	live, allowAIStudio                              bool
	provider, fallback, model, simuser, simuserModel string
	judge, judgeModel, ollamaURL                     string
	files, filter, fixtures, events                  string
	configPath, envFile, promptsDir                  string
	out                                              string
	uttTimeout                                       time.Duration
	repeat, parallel                                 int
	budget                                           float64
	personas                                         string
}

func parseEvalSimFlags(args []string, stderr io.Writer) (evalSimOpts, int, bool) {
	fs := flag.NewFlagSet("eval sim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printEvalSimUsage(stderr) }
	var o evalSimOpts
	fs.BoolVar(&o.live, "live", false, "real models")
	fs.BoolVar(&o.allowAIStudio, "allow-ai-studio", false, "allow the gemini (AI Studio) orchestrator provider")
	fs.StringVar(&o.provider, "provider", "", "live orchestrator provider")
	fs.StringVar(&o.fallback, "fallback-provider", "", "failover provider (vertex, deepseek, anthropic, gemini, none)")
	fs.StringVar(&o.model, "model", "", "live orchestrator model")
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
	fs.StringVar(&o.out, "out", "", "report directory")
	fs.DurationVar(&o.uttTimeout, "utterance-timeout", 0, "override the orchestrator's utterance timeout")
	fs.IntVar(&o.repeat, "repeat", 1, "runs per scenario")
	fs.IntVar(&o.parallel, "parallel", 1, "conversations at once")
	fs.Float64Var(&o.budget, "budget-usd", 0, "estimated spend limit")
	fs.StringVar(&o.personas, "personas", "", "simulated-user personas rotated by repeat: rotate, or a list of terse, rambling, zh, impatient, vague (default: each scenario's own)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	if _, err := eval.ParsePersonas(o.personas); err != nil {
		fprintf(stderr, "sheepdog eval sim: --personas: %v\n", err)
		return o, 2, false
	}
	if o.repeat < 1 || o.parallel < 1 || o.budget < 0 {
		fprintln(stderr, "sheepdog eval sim: --repeat and --parallel must be at least 1, --budget-usd not negative")
		return o, 2, false
	}
	if o.live {
		switch o.provider {
		case brain.ProviderVertex, brain.ProviderDeepSeek, brain.ProviderAnthropic:
		case brain.ProviderGemini:
			if !o.allowAIStudio {
				fprintln(stderr, "sheepdog eval sim: --provider gemini is the AI Studio free tier; pass --allow-ai-studio to use it anyway")
				return o, 2, false
			}
		default:
			fprintln(stderr, "sheepdog eval sim --live: --provider must be vertex, deepseek or anthropic")
			return o, 2, false
		}
	}
	switch o.fallback {
	case "", "none", brain.ProviderVertex, brain.ProviderDeepSeek, brain.ProviderAnthropic:
	case brain.ProviderGemini:
		if !o.allowAIStudio {
			fprintln(stderr, "sheepdog eval sim: --fallback-provider gemini is the AI Studio free tier; pass --allow-ai-studio to use it anyway")
			return o, 2, false
		}
	default:
		fprintln(stderr, "sheepdog eval sim: --fallback-provider must be vertex, deepseek, anthropic, gemini or none")
		return o, 2, false
	}
	if o.live && o.fallback != "" && o.fallback == o.provider {
		fprintln(stderr, "sheepdog eval sim: --fallback-provider must differ from --provider")
		return o, 2, false
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
	started := time.Now()
	total := len(scenarios) * o.repeat
	var mu sync.Mutex
	n, spent := 0, 0.0
	runs, meter := eval.Campaign(ctx, scenarios, opts, o.repeat, o.parallel, o.budget, func(r eval.SimRun) {
		mu.Lock()
		defer mu.Unlock()
		n++
		spent += r.CostUSD
		fprintf(stderr, "[%d/%d] %s r%d: %s (%s, %s) ≈ $%.4f, total ≈ $%.3f\n", n, total, r.Scenario, r.Repeat, r.Status, r.EndReason, eval.Primary(r.Failures), r.CostUSD, spent)
	})
	eval.WriteSimTable(stdout, runs)
	if o.out != "" {
		rep := eval.NewCampaignReport(started, simSetup(o, opts, cfg), o.repeat, runs, meter)
		rep.NorthStar = eval.NorthStarBlock(runs, eval.NorthStarOptions{Tier: "1", Scenarios: scenarios, EventsDir: opts.EventsDir})
		if err := writeSimReports(o.out, rep); err != nil {
			fprintf(stderr, "sheepdog eval sim: %v\n", err)
			return 1
		}
		fprintf(stderr, "reports: %s, %s\n", filepath.Join(o.out, "report.json"), filepath.Join(o.out, "report.md"))
	}
	if o.repeat > 1 {
		recs := make([]eval.RunRecord, len(runs))
		for i, r := range runs {
			recs[i] = r.Record()
		}
		eval.WriteStatsTable(stdout, fmt.Sprintf("eval sim, repeat %d", o.repeat), recs)
	}
	if o.live {
		eval.WriteSpend(stdout, meter)
	}
	if !o.live {
		// Offline is a harness check: any failed or errored run is a
		// broken scenario or harness.
		for _, r := range runs {
			if r.Status != eval.RunPass {
				return 1
			}
		}
	}
	return 0
}

// simSetup records what a campaign ran on, for its report.
func simSetup(o evalSimOpts, opts eval.SimOptions, cfg config.Config) map[string]string {
	if !o.live {
		return map[string]string{"mode": "offline (scripted orchestrator, scripted user, fake judge)"}
	}
	or := func(s, def string) string {
		if s == "" {
			return def
		}
		return s
	}
	jk, jmod := judgeChoice(o.judge, o.judgeModel)
	setup := map[string]string{
		"mode":               "live",
		"brain":              opts.BrainConfig.Provider + " " + or(opts.BrainConfig.Model, brain.DefaultModel),
		"simulated user":     or(o.simuser, eval.BackendVertex) + " " + or(o.simuserModel, eval.DefaultVertexModel),
		"judge":              jk + " " + judgeModelID(jk, jmod),
		"failover providers": providerList(opts.Fallbacks),
		"parallel":           fmt.Sprint(o.parallel),
		"budget_usd":         fmt.Sprint(o.budget),
		"scenarios":          o.files + filterNote(o.filter),
		"personas":           orDefaultStr(o.personas, "each scenario's own"),
		"utterance_timeout":  opts.BrainConfig.UtteranceTimeout.String() + " (0s = the orchestrator default, " + brain.DefaultUtteranceTimeout.String() + ")",
	}
	if line, err := bench.PromptsArm(o.promptsDir); err == nil && line != "" {
		setup["prompts_dir"] = line
	}
	if opts.BrainConfig.Provider == brain.ProviderAnthropic {
		setup["anthropic effort"] = cfg.Anthropic.Effort + " (user turns), " + cfg.Anthropic.EventEffort + " (event turns)"
	}
	return setup
}

// fallbackModels picks the orchestrator's extra providers out of models (every
// provider with credentials): the one --fallback-provider names, none
// for "none", else all but the primary, like the daemon registers them
// (gemini, the AI Studio free tier, only with --allow-ai-studio).
func fallbackModels(o evalSimOpts, models map[string]brain.Model) (map[string]brain.Model, error) {
	out := map[string]brain.Model{}
	switch o.fallback {
	case "none":
	case "":
		for id, m := range models {
			if id == o.provider || (id == brain.ProviderGemini && !o.allowAIStudio) {
				continue
			}
			out[id] = m
		}
	default:
		m, ok := models[o.fallback]
		if !ok {
			return nil, fmt.Errorf("fallback provider %q has no credentials (vertex: VERTEX_API_KEY and VERTEX_PROJECT; deepseek: DEEPSEEK_API_KEY; anthropic: ANTHROPIC_API_KEY; gemini: GEMINI_API_KEY)", o.fallback)
		}
		out[o.fallback] = m
	}
	return out, nil
}

func providerList(m map[string]brain.Model) string {
	if len(m) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

func filterNote(f string) string {
	if f == "" {
		return ""
	}
	return " (filter " + f + ")"
}

func writeSimReports(dir string, rep eval.CampaignReport) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := rep.WriteJSON(filepath.Join(dir, "report.json")); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "report.md"))
	if err != nil {
		return err
	}
	rep.WriteMarkdown(f, 25)
	return f.Close()
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
// orchestrator, simulated user and judge.
func evalSimOptions(o evalSimOpts, cfg config.Config, log logging.Logger) (eval.SimOptions, error) {
	bcfg := app.BrainConfig(cfg)
	promptsDir := o.promptsDir
	if promptsDir == "" {
		promptsDir = cfg.Prompts.Dir
	}
	if o.promptsDir != "" {
		// An A/B arm: refuse a directory whose files would be rejected
		// (the embedded prompts would run instead).
		if _, err := bench.PromptsArm(o.promptsDir); err != nil {
			return eval.SimOptions{}, err
		}
	}
	bcfg.Prompts = prompts.NewLoader(promptsDir, log)
	personas, _ := eval.ParsePersonas(o.personas)
	opts := eval.SimOptions{BrainConfig: bcfg, FixtureDir: o.fixtures, EventsDir: o.events, Personas: personas}
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
		return opts, fmt.Errorf("provider %q has no credentials (vertex: VERTEX_API_KEY and VERTEX_PROJECT; deepseek: DEEPSEEK_API_KEY; anthropic: ANTHROPIC_API_KEY)", o.provider)
	}
	opts.BrainConfig.Provider = o.provider
	if o.uttTimeout > 0 {
		opts.BrainConfig.UtteranceTimeout = o.uttTimeout
	}
	if o.model != "" {
		opts.BrainConfig.Model = o.model
	}
	opts.NewBrainModel = func() brain.Model { return m }
	opts.Fallbacks, err = fallbackModels(o, models)
	if err != nil {
		return opts, err
	}

	// One limit for every judge and simulated-user call of the campaign:
	// a burst of parallel judge calls got 429s from Vertex Flash-Lite.
	sem := make(chan struct{}, 4)
	vx := judgeVertex{cfg.Gemini.Vertex.APIKey, cfg.Gemini.Vertex.Project, o.ollamaURL}
	um, uid, err := newJudgeModel(orStr(o.simuser, eval.BackendVertex), o.simuserModel, vx, sem, log)
	if err != nil {
		return opts, fmt.Errorf("simulated user: %w", err)
	}
	if um != nil {
		opts.NewSimUser = func(meter *eval.Meter) eval.SimUser {
			return &eval.LLMSimUser{Model: eval.Metered(um, meter, "simuser"), ModelID: uid}
		}
	}
	jk, jmod := judgeChoice(o.judge, o.judgeModel)
	jm, jid, err := newJudgeModel(jk, jmod, vx, sem, log)
	if err != nil {
		return opts, fmt.Errorf("judge: %w", err)
	}
	if jm != nil {
		opts.NewJudge = func(meter *eval.Meter) eval.Judge { return newLLMJudge(jm, jid, jk, meter) }
	}
	return opts, nil
}
