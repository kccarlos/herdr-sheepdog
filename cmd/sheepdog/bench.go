package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/speech"
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

func printBenchUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog bench <calibrate|run|render|validate> [flags]")
	fprintln(w, "")
	fprintln(w, "The voice benchmark (docs/bench/README.md): synthetic speech (Gemini TTS on")
	fprintln(w, "Vertex) from a phone emulator into scratch daemons on ports 7790-7792 with the")
	fprintln(w, "real voice model and brain over a mock herdr scripted from the eval sim worlds.")
	fprintln(w, "")
	fprintln(w, "  calibrate  TTS clips -> Gemini Live input transcription WER per voice,")
	fprintln(w, "             language and noise; saves 10 sample clips for listening")
	fprintln(w, "  run        a tier-2 campaign over testdata/bench/scenarios (costs money);")
	fprintln(w, "             --tier 3: real daemon, real herdr session, real workers")
	fprintln(w, "             (sheepdog bench run --tier 3 --help)")
	fprintln(w, "  render     pre-render the scenarios' opening lines and write the manifest")
	fprintln(w, "  validate   check the scenario files offline")
	fprintln(w, "  report     rebuild report.json / report.md from a campaign's runs/")
	fprintln(w, "")
	fprintln(w, "Run `sheepdog bench <sub> -h` for the flags.")
}

func runBench(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		printBenchUsage(stderr)
		return 2
	}
	switch args[0] {
	case "calibrate":
		return runBenchCalibrate(ctx, args[1:], stdout, stderr)
	case "run":
		return runBenchRun(ctx, args[1:], stdout, stderr)
	case "render":
		return runBenchRender(ctx, args[1:], stdout, stderr)
	case "validate":
		return runBenchValidate(ctx, args[1:], stdout, stderr)
	case "report":
		return runBenchReport(ctx, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printBenchUsage(stdout)
		return 0
	}
	fprintf(stderr, "sheepdog bench: unknown subcommand %q\n", args[0])
	printBenchUsage(stderr)
	return 2
}

// benchCommon are the flags every live bench subcommand shares.
type benchCommon struct {
	configPath, envFile, cacheDir, ttsModel, scratch string
	noGemini                                         bool
}

func (b *benchCommon) register(fs *flag.FlagSet) {
	fs.StringVar(&b.configPath, "config", "", "config file (default ~/.config/sheepdog/config.toml)")
	fs.StringVar(&b.envFile, "env-file", "", "load KEY=VALUE lines first (default ./.env if present)")
	fs.StringVar(&b.cacheDir, "cache-dir", defaultBenchCache(), "rendered speech cache")
	fs.StringVar(&b.ttsModel, "tts-model", speech.DefaultModel, "Gemini TTS model on Vertex")
	fs.StringVar(&b.scratch, "scratch", "", "scratch directory for the daemons (default a temp dir)")
	fs.BoolVar(&b.noGemini, "say", false, "render with macOS say only (no Gemini TTS)")
}

func defaultBenchCache() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "sheepdog-bench", "tts")
}

// load reads the env file and the config.
func (b *benchCommon) load(stderr io.Writer) (config.Config, bool) {
	if code, ok := loadServeEnvFile(b.envFile, stderr); !ok || code != 0 {
		return config.Config{}, false
	}
	cfg, err := config.Load(b.configPath)
	if err != nil {
		fprintf(stderr, "sheepdog bench: %v\n", err)
		return cfg, false
	}
	return cfg, true
}

// synths are the cached Gemini TTS (say behind it) and the cached say.
func (b *benchCommon) synths(cfg config.Config, stderr io.Writer) (speech.Synth, speech.Synth, error) {
	return benchSynths(cfg.Gemini.Vertex.APIKey, cfg.Gemini.Vertex.Project, b.cacheDir, b.ttsModel, b.noGemini, stderr)
}

// benchSynths builds the speech renderers: Gemini TTS on Vertex behind
// a cache, with macOS say behind it (sayOnly: say alone).
func benchSynths(apiKey, project, cacheDir, ttsModel string, sayOnly bool, stderr io.Writer) (speech.Synth, speech.Synth, error) {
	say := &speech.Cache{Dir: cacheDir, Inner: &speech.Say{CacheDir: filepath.Join(cacheDir, "say-raw")}}
	if sayOnly {
		return say, say, nil
	}
	g, err := speech.NewGemini(apiKey, project, ttsModel)
	if err != nil {
		return nil, nil, fmt.Errorf("gemini tts: %w (or pass --say)", err)
	}
	warn := func(r speech.Request, err error) {
		fprintf(stderr, "tts: %q: %v\n", r.Text, err)
	}
	return speech.Fallback{Synths: []speech.Synth{&speech.Cache{Dir: cacheDir, Inner: g}, say}, Warn: warn}, say, nil
}

func (b *benchCommon) scratchDir() (string, error) {
	if b.scratch != "" {
		return b.scratch, os.MkdirAll(b.scratch, 0o755)
	}
	return os.MkdirTemp("", "sheepdog-bench-")
}

func runBenchCalibrate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bench calibrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common benchCommon
	common.register(fs)
	port := fs.Int("port", bench.DefaultPorts[0], "scratch daemon port")
	out := fs.String("out", "", "write calibration.json and calibration.md here")
	samples := fs.String("samples", "", "save 10 sample clips + transcripts here (default ~/Documents/sheepdog-voice-bench/calibration-<date>)")
	limit := fs.Int("limit", 0, "only the first N clips of the plan")
	seed := fs.Int64("seed", 1, "plan seed (disfluencies)")
	gate := fs.String("gate", "ios", "capture gate: ios, web or off")
	gateOpen := fs.Bool("gate-starts-open", false, "start the ios gate open (no silence on at connect)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cfg, ok := common.load(stderr)
	if !ok {
		return 1
	}
	syn, say, err := common.synths(cfg, stderr)
	if err != nil {
		fprintf(stderr, "sheepdog bench calibrate: %v\n", err)
		return 1
	}
	dir, err := common.scratchDir()
	if err != nil {
		fprintf(stderr, "sheepdog bench calibrate: %v\n", err)
		return 1
	}
	if *samples == "" {
		home, _ := os.UserHomeDir()
		*samples = filepath.Join(home, "Documents", "sheepdog-voice-bench", "calibration-"+time.Now().Format("2006-01-02"))
	}
	plan := bench.CalibrationPlan(*seed)
	if *limit > 0 && *limit < len(plan) {
		plan = plan[:*limit]
	}
	fprintf(stderr, "calibration: %d clips, scratch daemon on :%d (%s)\n", len(plan), *port, dir)
	rep, err := bench.Calibrate(ctx, bench.CalibrationOptions{
		Daemon: bench.DaemonOptions{Config: cfg, Port: *port, Dir: filepath.Join(dir, "calibrate"), Log: stderr},
		Synth:  syn, Say: say, Plan: plan, SamplesDir: *samples, Log: stderr, Gate: *gate, GateStartsOpen: *gateOpen,
	})
	if err != nil {
		fprintf(stderr, "sheepdog bench calibrate: %v\n", err)
		if len(rep.Results) == 0 {
			return 1
		}
	}
	bench.WriteCalibrationMarkdown(stdout, rep)
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fprintf(stderr, "sheepdog bench calibrate: %v\n", err)
			return 1
		}
		j, _ := json.MarshalIndent(rep, "", "  ")
		_ = os.WriteFile(filepath.Join(*out, "calibration.json"), j, 0o644)
		f, err := os.Create(filepath.Join(*out, "calibration.md"))
		if err == nil {
			bench.WriteCalibrationMarkdown(f, rep)
			_ = f.Close()
		}
	}
	return 0
}

const (
	defaultBenchScenarios = "testdata/bench/scenarios/*.json"
	defaultBenchSims      = "testdata/eval/sim/*.json"
	defaultBenchManifest  = "testdata/bench/openings.json"
)

func loadBenchScenarios(glob, sims, filter string) ([]bench.Scenario, error) {
	scs, err := bench.LoadScenarios(glob, sims)
	if err != nil {
		return nil, err
	}
	if filter == "" {
		return scs, nil
	}
	var out []bench.Scenario
	for _, s := range scs {
		for _, f := range strings.Split(filter, ",") {
			if f != "" && (strings.Contains(s.ID, f) || s.Category == f) {
				out = append(out, s)
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no scenario matches %q", filter)
	}
	return out, nil
}

func runBenchValidate(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bench validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scen := fs.String("scenarios", defaultBenchScenarios, "scenario files")
	sims := fs.String("sim", defaultBenchSims, "eval sim files (from_sim)")
	manifest := fs.String("manifest", defaultBenchManifest, "openings manifest")
	fixtures := fs.String("fixtures", "testdata/herdr", "screen fixtures")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	scs, err := bench.LoadScenarios(*scen, *sims)
	if err != nil {
		fprintf(stderr, "sheepdog bench validate: %v\n", err)
		return 1
	}
	bad := 0
	for _, s := range scs {
		if _, err := eval.ResolveScreens(s.SimScenario, *fixtures); err != nil {
			fprintf(stderr, "%s: %v\n", s.ID, err)
			bad++
		}
	}
	m, err := bench.LoadManifest(*manifest)
	if err != nil {
		fprintf(stderr, "sheepdog bench validate: manifest: %v\n", err)
		return 1
	}
	for _, p := range m.Check(scs) {
		fprintln(stderr, p)
		bad++
	}
	counts := bench.CategoryCounts(scs)
	var parts []string
	for _, c := range bench.SortedCategories(counts) {
		parts = append(parts, fmt.Sprintf("%s %d", c, counts[c]))
	}
	fprintf(stdout, "bench: %d scenarios (%s), %d openings pinned\n", len(scs), strings.Join(parts, ", "), len(m.Openings))
	if bad > 0 {
		return 1
	}
	return 0
}

func runBenchRender(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bench render", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common benchCommon
	common.register(fs)
	scen := fs.String("scenarios", defaultBenchScenarios, "scenario files")
	sims := fs.String("sim", defaultBenchSims, "eval sim files (from_sim)")
	manifest := fs.String("manifest", defaultBenchManifest, "openings manifest to write")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cfg, ok := common.load(stderr)
	if !ok {
		return 1
	}
	syn, _, err := common.synths(cfg, stderr)
	if err != nil {
		fprintf(stderr, "sheepdog bench render: %v\n", err)
		return 1
	}
	scs, err := bench.LoadScenarios(*scen, *sims)
	if err != nil {
		fprintf(stderr, "sheepdog bench render: %v\n", err)
		return 1
	}
	m, err := bench.RenderOpenings(ctx, syn, scs)
	if err != nil {
		fprintf(stderr, "sheepdog bench render: %v\n", err)
		return 1
	}
	for _, o := range m.Openings {
		if o.Source != "gemini" {
			fprintf(stderr, "warning: %s rendered by %s, not Gemini TTS\n", o.Scenario, o.Source)
		}
	}
	if err := m.Save(*manifest); err != nil {
		fprintf(stderr, "sheepdog bench render: %v\n", err)
		return 1
	}
	fprintf(stdout, "rendered %d openings into %s; manifest %s\n", len(m.Openings), common.cacheDir, *manifest)
	return 0
}

func runBenchRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if wantsTier3(args) {
		// Tier 3: real herdr session, real workers (bench_tier3.go).
		return runBenchTier3(ctx, args, stdout, stderr)
	}
	fs := flag.NewFlagSet("bench run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common benchCommon
	common.register(fs)
	tier := fs.Int("tier", 2, "benchmark tier (2 here; --tier 3 runs bench_tier3.go: real herdr workers)")
	scen := fs.String("scenarios", defaultBenchScenarios, "scenario files")
	sims := fs.String("sim", defaultBenchSims, "eval sim files (from_sim)")
	manifest := fs.String("manifest", defaultBenchManifest, "openings manifest")
	filter := fs.String("filter", "", "comma-separated scenario id substrings or categories")
	repeat := fs.Int("repeat", 1, "runs per scenario")
	budget := fs.Float64("budget-usd", 0, "stop starting runs once the estimated spend reaches this (0 = no limit)")
	out := fs.String("out", "", "output directory (report.json, report.md, runs/, events/)")
	portsFlag := fs.String("ports", "7790,7791,7792", "scratch daemon ports (one parallel worker each)")
	parallel := fs.Int("parallel", 3, "conversations at once (at most the number of ports)")
	fixtures := fs.String("fixtures", "testdata/herdr", "screen fixtures")
	simuser := fs.String("simuser", eval.BackendVertex, "simulated CEO backend: vertex, ollama or fake (the scripts)")
	simuserModel := fs.String("simuser-model", "", "simulated CEO model (default "+eval.DefaultJudgeModel+")")
	judge := fs.String("judge", eval.BackendVertex, "judge backend: vertex, ollama or fake")
	judgeModel := fs.String("judge-model", "", "judge model (default "+eval.DefaultJudgeModel+")")
	saveAudio := fs.String("save-audio", "", "also write each run's mic and bot audio as WAV here (outside the repo)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *tier != 2 {
		fprintln(stderr, "sheepdog bench run: --tier must be 2 or 3")
		return 2
	}
	if *out == "" || *repeat < 1 || *budget < 0 {
		fprintln(stderr, "sheepdog bench run: --out is required; --repeat >= 1; --budget-usd >= 0")
		return 2
	}
	var ports []int
	for _, p := range strings.Split(*portsFlag, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n <= 0 || n == 7777 {
			fprintf(stderr, "sheepdog bench run: bad port %q (never 7777, the CEO's daemon)\n", p)
			return 2
		}
		ports = append(ports, n)
	}
	if *parallel < 1 {
		*parallel = 1
	}
	if *parallel < len(ports) {
		ports = ports[:*parallel]
	}
	cfg, ok := common.load(stderr)
	if !ok {
		return 1
	}
	scs, err := loadBenchScenarios(*scen, *sims, *filter)
	if err != nil {
		fprintf(stderr, "sheepdog bench run: %v\n", err)
		return 1
	}
	syn, _, err := common.synths(cfg, stderr)
	if err != nil {
		fprintf(stderr, "sheepdog bench run: %v\n", err)
		return 1
	}
	setup := map[string]string{
		"tier":  "2 (synthetic speech -> phone emulator -> scratch daemon: real voice model, real brain, mock herdr scripted from the eval sim worlds)",
		"voice": cfg.Gemini.Model + " (backend " + cfg.Gemini.Backend + ")", "brain": "deepseek deepseek-flash (failover: vertex)",
		"tts": syn.ID(), "simulated CEO": *simuser + " " + orDefaultStr(*simuserModel, eval.DefaultJudgeModel), "judge": *judge + " " + orDefaultStr(*judgeModel, eval.DefaultJudgeModel),
		"ports": *portsFlag, "parallel": fmt.Sprint(len(ports)), "budget_usd": fmt.Sprint(*budget), "scenarios": *scen + filterNote(*filter),
	}
	if m, err := bench.LoadManifest(*manifest); err == nil {
		if probs := m.Check(scs); len(probs) > 0 {
			setup["openings"] = fmt.Sprintf("%d stale or missing in the manifest", len(probs))
		} else {
			setup["openings"] = "pinned by " + *manifest
		}
	}
	dir, err := common.scratchDir()
	if err != nil {
		fprintf(stderr, "sheepdog bench run: %v\n", err)
		return 1
	}
	ropts := bench.RunOptions{Config: cfg, Scratch: dir, FixtureDir: *fixtures, Synth: syn, Out: *out, SaveAudio: *saveAudio}
	sem := make(chan struct{}, 4) // one limit for every judge and simulated-CEO call (#94)
	backend := func(kind, model string) (brain.Model, string, error) {
		m, id, err := eval.NewBackend(eval.BackendOptions{Kind: kind, Model: model, VertexAPIKey: cfg.Gemini.Vertex.APIKey, VertexProject: cfg.Gemini.Vertex.Project}, nil)
		return eval.Patient(m, sem, 8, altFor(kind, id)), id, err
	}
	if *simuser != eval.BackendFake {
		um, uid, err := backend(*simuser, *simuserModel)
		if err != nil {
			fprintf(stderr, "sheepdog bench run: simulated CEO: %v\n", err)
			return 1
		}
		ropts.NewSimUser = func(m *eval.Meter) eval.SimUser {
			return &eval.LLMSimUser{Model: eval.Metered(um, m, "simuser"), ModelID: uid}
		}
	}
	if *judge != eval.BackendFake {
		jm, jid, err := backend(*judge, *judgeModel)
		if err != nil {
			fprintf(stderr, "sheepdog bench run: judge: %v\n", err)
			return 1
		}
		ropts.NewJudge = func(m *eval.Meter) eval.Judge {
			return &eval.LLMJudge{Model: eval.Metered(jm, m, "judge"), ModelID: jid}
		}
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fprintf(stderr, "sheepdog bench run: %v\n", err)
		return 1
	}
	started := time.Now()
	total := len(scs) * *repeat
	n := 0
	var mu sync.Mutex
	spendLog, _ := os.OpenFile(filepath.Join(*out, "spend.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	runs, meter := bench.RunCampaign(ctx, scs, bench.CampaignOptions{Run: ropts, Ports: ports, Repeat: *repeat, BudgetUSD: *budget, Done: func(r bench.Run, spent float64) {
		mu.Lock()
		defer mu.Unlock()
		n++
		line := fmt.Sprintf("%s [%d/%d] %s r%d (:%d): %s (%s, %s) %d turns %.0fs ≈ $%.3f, total ≈ $%.2f", time.Now().Format("15:04:05"), n, total, r.Scenario, r.Repeat, r.Port, r.Status, r.EndReason, eval.Primary(r.Failures), r.Turns, float64(r.DurationMS)/1000, r.CostUSD, spent)
		fprintln(stderr, line)
		if spendLog != nil {
			_, _ = fmt.Fprintln(spendLog, line)
		}
	}})
	if spendLog != nil {
		_ = spendLog.Close()
	}
	rep := bench.NewReport(started, setup, *repeat, runs, meter.Total(), scs)
	if err := rep.WriteJSON(filepath.Join(*out, "report.json")); err != nil {
		fprintf(stderr, "sheepdog bench run: %v\n", err)
		return 1
	}
	f, err := os.Create(filepath.Join(*out, "report.md"))
	if err != nil {
		fprintf(stderr, "sheepdog bench run: %v\n", err)
		return 1
	}
	rep.WriteMarkdown(f, 40)
	_ = f.Close()
	rep.WriteMarkdown(stdout, 10)
	fprintf(stderr, "reports: %s, %s\n", filepath.Join(*out, "report.json"), filepath.Join(*out, "report.md"))
	return 0
}

func orDefaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func runBenchReport(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bench report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "a campaign's output directory (with runs/)")
	out := fs.String("out", "", "write report.json and report.md here (default: --from)")
	scen := fs.String("scenarios", defaultBenchScenarios, "scenario files")
	sims := fs.String("sim", defaultBenchSims, "eval sim files (from_sim)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *from == "" {
		fprintln(stderr, "sheepdog bench report: --from is required")
		return 2
	}
	if *out == "" {
		*out = *from
	}
	runs, err := bench.LoadRuns(*from)
	if err != nil || len(runs) == 0 {
		fprintf(stderr, "sheepdog bench report: no runs under %s (%v)\n", *from, err)
		return 1
	}
	scs, err := bench.LoadScenarios(*scen, *sims)
	if err != nil {
		fprintf(stderr, "sheepdog bench report: %v\n", err)
		return 1
	}
	var prev bench.Report
	if b, err := os.ReadFile(filepath.Join(*from, "report.json")); err == nil {
		_ = json.Unmarshal(b, &prev)
	}
	started, repeat := prev.Started, prev.Repeat
	if started.IsZero() {
		started = time.Now()
	}
	for _, r := range runs {
		repeat = max(repeat, r.Repeat)
	}
	meter := bench.NewSpendMeter("")
	for _, r := range runs {
		for _, l := range r.Spend.Lines {
			meter.Meter.Add(l.Role, l.Model, l.Usage)
		}
		meter.Audio(int64(r.Spend.LiveInMin*60000), int64(r.Spend.LiveOutMin*60000))
	}
	rep := bench.NewReport(started, prev.Setup, repeat, runs, meter.Total(), scs)
	if prev.DurationS > 0 {
		rep.DurationS = prev.DurationS
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fprintf(stderr, "sheepdog bench report: %v\n", err)
		return 1
	}
	if err := rep.WriteJSON(filepath.Join(*out, "report.json")); err != nil {
		fprintf(stderr, "sheepdog bench report: %v\n", err)
		return 1
	}
	f, err := os.Create(filepath.Join(*out, "report.md"))
	if err != nil {
		fprintf(stderr, "sheepdog bench report: %v\n", err)
		return 1
	}
	rep.WriteMarkdown(f, 40)
	_ = f.Close()
	fprintf(stdout, "%d runs: %s\n", len(runs), filepath.Join(*out, "report.md"))
	return 0
}
