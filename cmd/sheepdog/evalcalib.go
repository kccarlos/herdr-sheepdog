package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func printEvalCalibUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog eval calib rubric                       (print the calibration rubric as JSON)")
	fprintln(w, "       sheepdog eval calib check --items FILE           (validate an items file offline)")
	fprintln(w, "       sheepdog eval calib judge --items FILE --out FILE [flags]")
	fprintln(w, "       sheepdog eval calib agree --labels FILE --judge-file FILE [--json]")
	fprintln(w, "")
	fprintln(w, "Judge calibration: transcript segments the CEO labels pass/fail/na per rubric")
	fprintln(w, "item (items.json), the judge's verdicts on the same segments (judge.json, one")
	fprintln(w, "call per item and rubric id, with the judge's quoted evidence), and their")
	fprintln(w, "agreement per rubric item and per segment with Cohen's kappa. The judge sees")
	fprintln(w, "the segment's context as facts and its turns as the conversation. See")
	fprintln(w, "docs/eval/calibration/README.md.")
	fprintln(w, "")
	fprintln(w, "judge flags:")
	fprintln(w, judgeHelp)
	fprintln(w, "  --judge-model ID      judge model (default sonnet for claude, "+eval.DefaultVertexModel+" for vertex)")
	fprintln(w, "  --ollama-url URL      Ollama OpenAI-compatible root")
	fprintln(w, "  --config PATH         config file")
	fprintln(w, "  --env-file PATH       load KEY=VALUE lines into the environment first")
	fprintln(w, "  --parallel N          judge calls in flight (default 4)")
	fprintln(w, "  --segment             one call per item scoring all its rubric ids, as segment")
	fprintln(w, "                        judging asks a workday segment (default: one call per pair)")
}

// runEvalCalib implements `sheepdog eval calib`.
func runEvalCalib(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printEvalCalibUsage(stderr)
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("eval calib "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printEvalCalibUsage(stderr) }
	items := fs.String("items", "", "items file")
	out := fs.String("out", "", "judge.json to write")
	labels := fs.String("labels", "", "labels.json")
	judgeFile := fs.String("judge-file", "", "judge.json")
	asJSON := fs.Bool("json", false, "agreement as JSON")
	judge := fs.String("judge", "", "judge backend")
	judgeModel := fs.String("judge-model", "", "judge model")
	ollamaURL := fs.String("ollama-url", "", "Ollama root")
	configPath := fs.String("config", "", "config file")
	envFile := fs.String("env-file", "", "env file")
	parallel := fs.Int("parallel", 4, "judge calls in flight")
	segment := fs.Bool("segment", false, "one call per item scoring all its rubric ids (segment judging)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	switch sub {
	case "rubric":
		return writeJSON(stdout, stderr, eval.CalibRubric())
	case "check":
		f, err := eval.LoadCalibFile(*items)
		if err != nil {
			fprintf(stderr, "sheepdog eval calib: %v\n", err)
			return 1
		}
		pairs := 0
		for _, it := range f.Items {
			pairs += len(it.RubricIDs)
		}
		fprintf(stdout, "%d items, %d rubric items, %d labels to give\n", len(f.Items), len(f.Rubric), pairs)
		return 0
	case "judge":
		if *items == "" || *out == "" {
			fprintln(stderr, "sheepdog eval calib judge: --items and --out are required")
			return 2
		}
		f, err := eval.LoadCalibFile(*items)
		if err != nil {
			fprintf(stderr, "sheepdog eval calib: %v\n", err)
			return 1
		}
		if *envFile != "" {
			if err := config.LoadEnvFile(*envFile); err != nil {
				fprintf(stderr, "sheepdog eval calib: %v\n", err)
				return 1
			}
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			fprintf(stderr, "sheepdog: %v\n", err)
			return 1
		}
		kind, model := judgeChoice(*judge, *judgeModel)
		var j eval.Judge = &eval.FakeJudge{}
		meter := eval.NewMeter()
		if kind != eval.BackendFake {
			m, id, err := newJudgeModel(kind, model, judgeVertex{cfg.Gemini.Vertex.APIKey, cfg.Gemini.Vertex.Project, *ollamaURL}, make(chan struct{}, *parallel), logging.NopLogger{})
			if err != nil {
				fprintf(stderr, "sheepdog eval calib: judge: %v\n", err)
				return 1
			}
			// No rationale call: judge.json keeps only verdict and evidence.
			lj := newLLMJudge(m, id, kind, meter)
			lj.NoRationale = true
			j = lj
		}
		t0 := time.Now()
		progress := func(done, total int) {
			if done%20 == 0 || done == total {
				fprintf(stderr, "[%d/%d]\n", done, total)
			}
		}
		var verdicts []eval.CalibVerdict
		if *segment {
			sj, ok := j.(eval.SegmentJudge)
			if !ok {
				fprintln(stderr, "sheepdog eval calib judge: this judge cannot score segments")
				return 1
			}
			verdicts = eval.JudgeCalibSegments(ctx, sj, f, *parallel, progress)
		} else {
			verdicts = eval.JudgeCalib(ctx, j, f, *parallel, progress)
		}
		fh, err := os.Create(*out)
		if err != nil {
			fprintf(stderr, "sheepdog eval calib: %v\n", err)
			return 1
		}
		code := writeJSON(fh, stderr, verdicts)
		if err := fh.Close(); err != nil && code == 0 {
			fprintf(stderr, "sheepdog eval calib: %v\n", err)
			code = 1
		}
		if code != 0 {
			return code
		}
		errs := 0
		for _, v := range verdicts {
			if v.Error != "" {
				errs++
			}
		}
		eval.WriteSpend(stdout, meter)
		fprintf(stdout, "%d verdicts in %s\n", len(verdicts), time.Since(t0).Round(time.Second))
		if errs > 0 {
			fprintf(stderr, "sheepdog eval calib: %d judge calls failed\n", errs)
			return 1
		}
		return 0
	case "agree":
		if *labels == "" || *judgeFile == "" {
			fprintln(stderr, "sheepdog eval calib agree: --labels and --judge-file are required")
			return 2
		}
		var ls []eval.CalibLabel
		var vs []eval.CalibVerdict
		if err := readJSONList(*labels, "labels", &ls); err != nil {
			fprintf(stderr, "sheepdog eval calib: %v\n", err)
			return 1
		}
		if err := readJSONList(*judgeFile, "verdicts", &vs); err != nil {
			fprintf(stderr, "sheepdog eval calib: %v\n", err)
			return 1
		}
		for _, l := range ls {
			switch l.Label {
			case "pass", "fail", "na":
			default:
				fprintf(stderr, "sheepdog eval calib: %s/%s: label %q, want pass, fail or na\n", l.ItemID, l.RubricID, l.Label)
				return 1
			}
		}
		rep := eval.Agreement(ls, vs)
		if *asJSON {
			return writeJSON(stdout, stderr, rep)
		}
		eval.WriteAgreement(stdout, rep)
		return 0
	}
	printEvalCalibUsage(stderr)
	return 2
}

func writeJSON(w io.Writer, stderr io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fprintf(stderr, "sheepdog eval calib: %v\n", err)
		return 1
	}
	return 0
}

// readJSONList reads a JSON array, or an object holding it under key.
func readJSONList(path, key string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(string(b)); strings.HasPrefix(s, "{") {
		var wrap map[string]json.RawMessage
		if err := json.Unmarshal(b, &wrap); err != nil {
			return err
		}
		b = wrap[key]
	}
	return json.Unmarshal(b, v)
}
