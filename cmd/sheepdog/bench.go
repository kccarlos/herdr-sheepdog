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
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/speech"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
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
	fprintln(w, "  run        a tier-2 campaign over testdata/bench/scenarios (costs money)")
	fprintln(w, "  render     pre-render the scenarios' opening lines and write the manifest")
	fprintln(w, "  validate   check the scenario files offline")
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
func (b *benchCommon) synths(cfg config.Config) (speech.Synth, speech.Synth, error) {
	say := &speech.Cache{Dir: b.cacheDir, Inner: &speech.Say{CacheDir: filepath.Join(b.cacheDir, "say-raw")}}
	if b.noGemini {
		return say, say, nil
	}
	g, err := speech.NewGemini(cfg.Gemini.Vertex.APIKey, cfg.Gemini.Vertex.Project, b.ttsModel)
	if err != nil {
		return nil, nil, fmt.Errorf("gemini tts: %w (or pass --say)", err)
	}
	return speech.Fallback{&speech.Cache{Dir: b.cacheDir, Inner: g}, say}, say, nil
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
	samples := fs.String("samples", "", "save 10 sample clips + transcripts here (default ~/Documents/sheepdog-bench/calibration-<date>)")
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
	syn, say, err := common.synths(cfg)
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
		*samples = filepath.Join(home, "Documents", "sheepdog-bench", "calibration-"+time.Now().Format("2006-01-02"))
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

func runBenchRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fprintln(stderr, "sheepdog bench run: not yet")
	return 2
}

func runBenchRender(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fprintln(stderr, "sheepdog bench render: not yet")
	return 2
}

func runBenchValidate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fprintln(stderr, "sheepdog bench validate: not yet")
	return 2
}
