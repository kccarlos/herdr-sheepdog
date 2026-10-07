package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/eval/gen"
)

func printEvalExtractUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog eval extract [--day YYYY-MM-DD] [--from T] [--to T] [flags]")
	fprintln(w, "")
	fprintln(w, "Turns a window of the event log into orchestrator eval cases (JSONL): one utterance case")
	fprintln(w, "per to_supervisor relay and one announce case per worker block or finish, each")
	fprintln(w, "with the transcript, roster, pending prompts and screens the log carries, the")
	fprintln(w, "logged behaviour as its baseline, and its source {day, ts, event_id}. Secrets and")
	fprintln(w, "email addresses are scrubbed. Reads the day files only.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --events-dir PATH   event day files (default <data_dir>/events)")
	fprintln(w, "  --day YYYY-MM-DD    the day file (UTC); required unless --from is RFC3339")
	fprintln(w, "  --from T, --to T    window bounds: RFC3339, or HH:MM:SS (UTC) on --day")
	fprintln(w, "  --context N         prior transcript lines per case (default 8)")
	fprintln(w, "  --out PATH          write here instead of stdout")
	fprintln(w, "  --redact WORDS      comma-separated words to replace with <redacted> (a person's")
	fprintln(w, "                      name on a worker's screen); the hostname always is")
	fprintln(w, "  --config PATH       config file (for data_dir)")
}

// runEvalExtract implements `sheepdog eval extract`.
func runEvalExtract(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval extract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printEvalExtractUsage(stderr) }
	var eventsDir, day, from, to, out, configPath, redactList string
	fs.StringVar(&redactList, "redact", "", "words to redact")
	var context int
	fs.StringVar(&eventsDir, "events-dir", "", "event day files")
	fs.StringVar(&day, "day", "", "day file YYYY-MM-DD")
	fs.StringVar(&from, "from", "", "window start")
	fs.StringVar(&to, "to", "", "window end")
	fs.IntVar(&context, "context", eval.DefaultExtractContext, "prior transcript lines")
	fs.StringVar(&out, "out", "", "output file")
	fs.StringVar(&configPath, "config", "", "config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if eventsDir == "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			fprintf(stderr, "sheepdog: %v\n", err)
			return 1
		}
		eventsDir = filepath.Join(cfg.DataDir, "events")
	}
	fromT, err := parseWindowTime(from, day)
	if err != nil {
		fprintf(stderr, "sheepdog eval extract: --from: %v\n", err)
		return 2
	}
	toT, err := parseWindowTime(to, day)
	if err != nil {
		fprintf(stderr, "sheepdog eval extract: --to: %v\n", err)
		return 2
	}
	var files []string
	switch {
	case day != "":
		files = []string{filepath.Join(eventsDir, day+".jsonl")}
	case !fromT.IsZero():
		files, err = eval.DayFiles(eventsDir, fromT, toT)
		if err != nil {
			fprintf(stderr, "sheepdog eval extract: %v\n", err)
			return 1
		}
	default:
		fprintln(stderr, "sheepdog eval extract: give --day, or --from as RFC3339")
		return 2
	}
	var cases []eval.SVPCase
	for _, f := range files {
		redact := append(eval.DefaultRedactions(), splitList(redactList)...)
		cs, err := eval.ExtractDayFile(f, eval.ExtractOptions{From: fromT, To: toT, Context: context, Redact: redact})
		if err != nil {
			fprintf(stderr, "sheepdog eval extract: %v\n", err)
			return 1
		}
		cases = append(cases, cs...)
	}
	if err := writeCases(out, stdout, cases); err != nil {
		fprintf(stderr, "sheepdog eval extract: %v\n", err)
		return 1
	}
	fprintf(stderr, "sheepdog eval extract: %d cases from %d day file(s)\n", len(cases), len(files))
	return 0
}

// parseWindowTime reads a window bound: RFC3339, or HH:MM:SS (UTC) on day.
func parseWindowTime(s, day string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if day == "" {
		return time.Time{}, fmt.Errorf("%q is not RFC3339, and HH:MM:SS needs --day", s)
	}
	t, err := time.Parse("2006-01-02 15:04:05", day+" "+s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q: want RFC3339 or HH:MM:SS", s)
	}
	return t.UTC(), nil
}

func printEvalExpandUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog eval expand --in cases.jsonl[,more.jsonl] --out expanded.jsonl [flags]")
	fprintln(w, "")
	fprintln(w, "Writes generated variants of orchestrator (and, with --assistant-in, assistant) cases:")
	fprintln(w, "paraphrases of the utterance from the checked-in table for the case's job, and")
	fprintln(w, "seeded speech-recognition noise (a dropped word, a homophone, a truncation to the")
	fprintln(w, "last words, a stray token). Deterministic for a given --seed; no model call.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --in FILES             orchestrator seed files, comma-separated (every utterance case)")
	fprintln(w, "  --pick FILES           more orchestrator seed files, of which --per-job cases per job")
	fprintln(w, "  --per-job N            cases per job taken from --pick (default 1)")
	fprintln(w, "  --out PATH             orchestrator output file (default stdout)")
	fprintln(w, "  --assistant-in FILES   assistant seed files, comma-separated")
	fprintln(w, "  --assistant-out PATH   assistant output file")
	fprintln(w, "  --paraphrase           add the job's paraphrases")
	fprintln(w, "  --paraphrase-max N     at most N paraphrases per case (default all)")
	fprintln(w, "  --noise N              noisy variants per case and per paraphrase (default 0)")
	fprintln(w, "  --seed N               noise seed (default 1)")
}

// expandFlags are `eval expand`'s flags. The checked-in
// testdata/eval/expanded*.jsonl are generated with gen.CorpusOptions
// (see that for the command line); internal/eval/gen's corpus test
// checks they still match.
type expandFlags struct {
	in, pick, out, assistantIn, assistantOut string
	perJob                                   int
	opt                                      gen.Options
}

// runEvalExpand implements `sheepdog eval expand`.
func runEvalExpand(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval expand", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printEvalExpandUsage(stderr) }
	var f expandFlags
	fs.StringVar(&f.in, "in", "", "orchestrator seed files")
	fs.StringVar(&f.pick, "pick", "", "orchestrator files to pick seeds from")
	fs.IntVar(&f.perJob, "per-job", 1, "cases per job from --pick")
	fs.StringVar(&f.out, "out", "", "orchestrator output file")
	fs.StringVar(&f.assistantIn, "assistant-in", "", "assistant seed files")
	fs.StringVar(&f.assistantOut, "assistant-out", "", "assistant output file")
	fs.BoolVar(&f.opt.Paraphrase, "paraphrase", false, "add paraphrases")
	fs.IntVar(&f.opt.ParaphraseMax, "paraphrase-max", 0, "paraphrases per case")
	fs.IntVar(&f.opt.Noise, "noise", 0, "noisy variants per case")
	fs.Int64Var(&f.opt.Seed, "seed", 1, "noise seed")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.in == "" && f.pick == "" && f.assistantIn == "" {
		fprintln(stderr, "sheepdog eval expand: give --in, --pick or --assistant-in")
		return 2
	}
	seeds, err := loadSVPFiles(f.in)
	if err != nil {
		fprintf(stderr, "sheepdog eval expand: %v\n", err)
		return 1
	}
	picked, err := loadSVPFiles(f.pick)
	if err != nil {
		fprintf(stderr, "sheepdog eval expand: %v\n", err)
		return 1
	}
	seeds = append(seeds, gen.PickSeeds(picked, f.perJob)...)
	if len(seeds) > 0 {
		cases := gen.Expand(seeds, f.opt)
		if err := writeCases(f.out, os.Stdout, cases); err != nil {
			fprintf(stderr, "sheepdog eval expand: %v\n", err)
			return 1
		}
		fprintf(stderr, "sheepdog eval expand: %d orchestrator cases from %d seeds\n", len(cases), len(seeds))
	}
	if f.assistantIn != "" {
		var aseeds []eval.AssistantCase
		for _, p := range splitList(f.assistantIn) {
			cs, err := eval.LoadAssistantCases(p)
			if err != nil {
				fprintf(stderr, "sheepdog eval expand: %v\n", err)
				return 1
			}
			aseeds = append(aseeds, cs...)
		}
		cases := gen.ExpandAssistant(aseeds, f.opt)
		if err := writeCases(f.assistantOut, os.Stdout, cases); err != nil {
			fprintf(stderr, "sheepdog eval expand: %v\n", err)
			return 1
		}
		fprintf(stderr, "sheepdog eval expand: %d assistant cases from %d seeds\n", len(cases), len(aseeds))
	}
	return 0
}

func loadSVPFiles(list string) ([]eval.SVPCase, error) {
	var out []eval.SVPCase
	for _, p := range splitList(list) {
		files, err := globOrOne(p)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			cs, err := eval.LoadSVPCases(f)
			if err != nil {
				return nil, err
			}
			out = append(out, cs...)
		}
	}
	return out, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// writeCases writes cases as JSONL to path, or to w when path is empty.
func writeCases[T any](path string, w io.Writer, cases []T) error {
	if path == "" {
		return eval.WriteJSONL(w, cases)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := eval.WriteJSONL(f, cases); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
