package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

func TestEvalExtractAndExpand(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "none.toml")
	extracted := filepath.Join(dir, "x.jsonl")
	var out, errb bytes.Buffer
	args := []string{"eval", "extract", "--events-dir", "../../testdata/eval/extract", "--day", "2026-01-02", "--out", extracted, "--config", cfg}
	if code := run(context.Background(), args, &out, &errb); code != 0 {
		t.Fatalf("extract: code %d: %s", code, errb.String())
	}
	if n := countLines(t, extracted); n != 3 {
		t.Errorf("extract wrote %d cases, want 3", n)
	}

	windowed := filepath.Join(dir, "w.jsonl")
	args = []string{"eval", "extract", "--events-dir", "../../testdata/eval/extract", "--day", "2026-01-02", "--from", "10:00:15", "--out", windowed, "--config", cfg}
	if code := run(context.Background(), args, &out, &errb); code != 0 {
		t.Fatalf("extract --from: code %d: %s", code, errb.String())
	}
	if n := countLines(t, windowed); n != 2 {
		t.Errorf("extract --from 10:00:15 wrote %d cases, want 2", n)
	}

	expanded := filepath.Join(dir, "e.jsonl")
	args = []string{"eval", "expand", "--in", extracted, "--out", expanded, "--paraphrase", "--noise=2", "--seed=1"}
	if code := run(context.Background(), args, &out, &errb); code != 0 {
		t.Fatalf("expand: code %d: %s", code, errb.String())
	}
	first := countLines(t, expanded)
	if first < 20 {
		t.Errorf("expand wrote %d cases, want paraphrases and noise of the J1 relay", first)
	}
	again := filepath.Join(dir, "e2.jsonl")
	args[5] = again
	if code := run(context.Background(), args, &out, &errb); code != 0 {
		t.Fatalf("expand again: code %d", code)
	}
	a, _ := os.ReadFile(expanded)
	b, _ := os.ReadFile(again)
	if !bytes.Equal(a, b) {
		t.Error("expand is not deterministic for the same seed")
	}

	if code := run(context.Background(), []string{"eval", "extract", "--events-dir", dir, "--config", cfg}, &out, &errb); code != 2 {
		t.Errorf("extract without --day or --from: code %d, want 2", code)
	}
}

// `sheepdog eval --set all` offline is `make eval`: every set runs, the
// report lists the checks it skipped, and nothing fails.
func TestEvalSetAllOffline(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the whole offline corpus")
	}
	var out, errb bytes.Buffer
	args := []string{"eval", "--set", "all", "--dir", "../../testdata/eval", "--fixtures", "../../testdata/herdr", "--config", filepath.Join(t.TempDir(), "none.toml")}
	code := run(context.Background(), args, &out, &errb)
	if code != 0 {
		t.Fatalf("code %d\n%s\n%s", code, lastLines(out.String(), 40), errb.String())
	}
	if os.Getenv("SHEEPDOG_SHOW_REPORT") != "" {
		t.Log("\n" + out.String())
	}
	for _, want := range []string{
		"== eval --set assistant", "== eval --set svp", "== eval --set regressions",
		"== eval --set extracted", "== eval --set expanded",
		"checks skipped offline (live only)", "baseline: fails as logged",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q", want)
		}
	}
}

func TestEvalBadSet(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"eval", "--set", "nope"}, &out, &errb); code != 2 {
		t.Errorf("code %d, want 2", code)
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Without Vertex credentials, --live keeps working with no judge (the
// wording checks stay the code's) instead of failing on the default.
func TestEvalLiveDefaultJudgeFallsBackWithoutVertex(t *testing.T) {
	var cfg config.Config
	cfg.Gemini.APIKeys = []string{"k"}
	m, err := evalLiveModels(evalOpts{live: true}, cfg, logging.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if m.judge != nil || m.judgeNote == "" {
		t.Fatalf("judge %v note %q", m.judge, m.judgeNote)
	}
	if _, err := evalLiveModels(evalOpts{live: true, judge: "vertex"}, cfg, logging.NopLogger{}); err == nil {
		t.Fatal("an explicit --judge vertex without credentials must fail")
	}
}
