package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/audioe2e"
)

func printAudioE2EUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog audio-e2e [flags]")
	fprintln(w, "")
	fprintln(w, "Text-to-speech end-to-end test against a running daemon: every scenario's")
	fprintln(w, "speech is rendered with macOS `say` (+ afconvert to 16 kHz), mixed with a")
	fprintln(w, "generated noise bed, and streamed into /ws like a client (40 ms frames at")
	fprintln(w, "real-time pace, the apps' silence gate). Server messages and /events are")
	fprintln(w, "recorded and each scenario's expectations checked. Point it at a scratch")
	fprintln(w, "daemon (`serve --herdr=mock --port 7799`), never at your own.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --url URL          daemon base URL (default http://127.0.0.1:7799)")
	fprintln(w, "  --scenarios FILE   scenarios (default testdata/audio-e2e/scenarios.json)")
	fprintln(w, "  --only LIST        comma-separated scenario names to run")
	fprintln(w, "  --cache DIR        rendered speech cache (default $TMPDIR/sheepdog-audio-e2e)")
	fprintln(w, "  --save DIR         also write <scenario>.in.wav / .out.wav there")
	fprintln(w, "  --json FILE        write the full results (timelines, events) as JSON")
	fprintln(w, "  --floor-dbfs DB    add a microphone noise floor (e.g. -72) to every frame; 0 (the")
	fprintln(w, "                     default) streams digital silence, so the silence gate closes")
	fprintln(w, "  --audio-timeline   record every server→client audio frame's arrival offset and")
	fprintln(w, "                     size in the JSON output (--json), for an offline jitter-buffer replay")
	fprintln(w, "")
	fprintln(w, "Prints a Markdown table; exits 1 when a scenario failed.")
}

func runAudioE2E(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audio-e2e", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printAudioE2EUsage(stderr) }
	base := fs.String("url", "http://127.0.0.1:7799", "daemon URL")
	scen := fs.String("scenarios", filepath.Join("testdata", "audio-e2e", "scenarios.json"), "scenarios file")
	only := fs.String("only", "", "scenario names")
	cache := fs.String("cache", filepath.Join(os.TempDir(), "sheepdog-audio-e2e"), "speech cache")
	save := fs.String("save", "", "save audio here")
	jsonOut := fs.String("json", "", "results JSON")
	floor := fs.Float64("floor-dbfs", 0, "microphone noise floor")
	timeline := fs.Bool("audio-timeline", false, "record the per-frame audio arrival timeline")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	file, err := audioe2e.Load(*scen)
	if err != nil {
		fprintf(stderr, "sheepdog audio-e2e: %v\n", err)
		return 1
	}
	url := strings.TrimRight(*base, "/")
	if strings.Contains(url, ":7777") {
		fprintf(stderr, "sheepdog audio-e2e: refusing port 7777 (the everyday daemon); run a scratch one\n")
		return 2
	}
	provider, model, err := audioe2e.Hello(ctx, url)
	if err != nil {
		fprintf(stderr, "sheepdog audio-e2e: %s/api/hello: %v\n", url, err)
		return 1
	}
	fprintf(stderr, "audio-e2e: %s voice=%s/%s\n", url, provider, model)
	want := map[string]bool{}
	for _, n := range strings.Split(*only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	opts := audioe2e.Options{BaseURL: url, CacheDir: *cache, FixtureDir: filepath.Join(filepath.Dir(filepath.Dir(*scen)), "herdr"),
		Provider: provider, DefaultVoice: file.Voice, Save: *save, Log: stderr, FloorDBFS: *floor, RecordAudioTimeline: *timeline}
	var results []audioe2e.Result
	failed := false
	for _, sc := range file.Scenarios {
		if len(want) > 0 && !want[sc.Name] {
			continue
		}
		if !sc.For(provider) {
			continue
		}
		fprintf(stderr, "audio-e2e: %s …\n", sc.Name)
		started := time.Now()
		res, err := audioe2e.Run(ctx, sc, opts)
		if err != nil {
			fprintf(stderr, "audio-e2e: %s: %v\n", sc.Name, err)
			res.Failures = append(res.Failures, err.Error())
			res.Pass = false
		}
		if !res.Pass {
			failed = true
		}
		fprintf(stderr, "audio-e2e: %s %s in %s: in=%q out=%q tools=%v fail=%v\n", sc.Name, passWord(res.Pass), time.Since(started).Round(time.Millisecond), res.InTranscript, res.OutTranscript, res.Tools, res.Failures)
		results = append(results, res)
		if ctx.Err() != nil {
			break
		}
	}
	audioe2e.Table(stdout, results)
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(map[string]any{"provider": provider, "model": model, "at": time.Now().UTC(), "results": results}, "", "  ")
		if err := os.WriteFile(*jsonOut, b, 0o644); err != nil {
			fprintf(stderr, "sheepdog audio-e2e: %v\n", err)
		}
	}
	if failed {
		return 1
	}
	return 0
}

func passWord(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}
