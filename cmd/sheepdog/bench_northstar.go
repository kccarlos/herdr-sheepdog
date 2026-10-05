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
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/bench"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/tier3"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

func printBenchNorthStarUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog bench northstar --from DIR [--events DIR] [--vs DIR [--vs-events DIR]] [flags]")
	fprintln(w, "")
	fprintln(w, "The north-star block (Understood, Done, Low effort, Safe with 95 % CIs) and the")
	fprintln(w, "programmatic checks (said vs done, missed news, user effort) of a recorded result")
	fprintln(w, "directory, offline: no model call. DIR is a tier-2 campaign (runs/ and events/),")
	fprintln(w, "an eval sim or tier-3 report.json (full runs), or a tier-2 report.json alone (summary")
	fprintln(w, "only: Understood and Safe from the failure labels). With --vs, arm A (--from)")
	fprintln(w, "against arm B: per metric B - A with its 95 % CI.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --from DIR          result directory (arm A with --vs)")
	fprintln(w, "  --events DIR        per-run event logs <id>.r<n>.jsonl (default DIR/events)")
	fprintln(w, "  --vs DIR            arm B's result directory")
	fprintln(w, "  --vs-events DIR     arm B's event logs (default DIR/events)")
	fprintln(w, "  --format md|json    output format (default md)")
	fprintln(w, "  --out FILE          write there instead of stdout")
	fprintln(w, "  --scenarios GLOB    tier-2 scenario files (default "+defaultBenchScenarios+")")
	fprintln(w, "  --sim GLOB          eval sim scenario files (default "+defaultBenchSims+")")
	fprintln(w, "  --tier3-files GLOB  tier-3 scenario files (default testdata/bench/tier3/*.json)")
}

type nsSources struct{ scenarios, sims, tier3 string }

func runBenchNorthStar(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bench northstar", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printBenchNorthStarUsage(stderr) }
	from := fs.String("from", "", "result directory")
	events := fs.String("events", "", "event logs")
	vs := fs.String("vs", "", "arm B's result directory")
	vsEvents := fs.String("vs-events", "", "arm B's event logs")
	format := fs.String("format", "md", "md | json")
	out := fs.String("out", "", "output file")
	var src nsSources
	fs.StringVar(&src.scenarios, "scenarios", defaultBenchScenarios, "tier-2 scenario files")
	fs.StringVar(&src.sims, "sim", defaultBenchSims, "eval sim scenario files")
	fs.StringVar(&src.tier3, "tier3-files", "testdata/bench/tier3/*.json", "tier-3 scenario files")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *from == "" || (*format != "md" && *format != "json") {
		printBenchNorthStarUsage(stderr)
		return 2
	}
	a, tierA, err := loadNorthStarRuns(*from, *events, src, stderr)
	if err != nil {
		fprintf(stderr, "sheepdog bench northstar: %v\n", err)
		return 1
	}
	w := stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fprintf(stderr, "sheepdog bench northstar: %v\n", err)
			return 1
		}
		defer func() { _ = f.Close() }()
		w = f
	}
	if *vs == "" {
		b := northstar.Summarize(tierA, a)
		if *format == "json" {
			return writeJSONOut(w, b, stderr)
		}
		fprintf(w, "# North star: %s (tier %s)\n\n", *from, tierA)
		b.WriteMarkdown(w)
		return 0
	}
	bb, tierB, err := loadNorthStarRuns(*vs, *vsEvents, src, stderr)
	if err != nil {
		fprintf(stderr, "sheepdog bench northstar: %v\n", err)
		return 1
	}
	c := northstar.Compare(a, bb)
	if tierA != tierB {
		c.Notes = append(c.Notes, fmt.Sprintf("Arm A is tier %s and arm B tier %s: the difference includes the tier's.", tierA, tierB))
	}
	if *format == "json" {
		return writeJSONOut(w, c, stderr)
	}
	c.WriteMarkdown(w, *from, *vs)
	return 0
}

func writeJSONOut(w io.Writer, v any, stderr io.Writer) int {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fprintf(stderr, "sheepdog bench northstar: %v\n", err)
		return 1
	}
	return 0
}

// loadNorthStarRuns reads a result directory in whichever form it has:
// a tier-2 campaign's runs/, an eval sim or tier-3 report.json with full
// runs, or a tier-2 report.json with summaries only.
func loadNorthStarRuns(dir, events string, src nsSources, stderr io.Writer) ([]northstar.Run, string, error) {
	warn := func(n int, err error, glob string) {
		if n == 0 || err != nil {
			fprintf(stderr, "sheepdog bench northstar: no scenarios from %s (%v): Done counts only runs with an action or a claim, and agent names come from the records\n", glob, err)
		}
	}
	if events == "" {
		events = filepath.Join(dir, "events")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "runs", "*.json")); len(files) > 0 {
		runs, err := bench.LoadRuns(dir)
		if err != nil {
			return nil, "", err
		}
		scs, err := bench.LoadScenarios(src.scenarios, src.sims)
		warn(len(scs), err, src.scenarios)
		return bench.NorthStarRuns(runs, scs, events), "2", nil
	}
	b, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		return nil, "", fmt.Errorf("%s: no runs/ and no report.json", dir)
	}
	var probe struct {
		Setup map[string]string `json:"setup"`
		Cases json.RawMessage   `json:"cases"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil, "", fmt.Errorf("%s/report.json: %w", dir, err)
	}
	if len(probe.Cases) > 0 {
		var rep bench.Report
		if err := json.Unmarshal(b, &rep); err != nil {
			return nil, "", err
		}
		scs, err := bench.LoadScenarios(src.scenarios, src.sims)
		warn(len(scs), err, src.scenarios)
		return bench.SummaryRuns(rep.Runs, scs), "2 (summary)", nil
	}
	var rep eval.CampaignReport
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil, "", err
	}
	mode := rep.Setup["mode"]
	if strings.Contains(mode, "tier 3") {
		scs, err := tier3.LoadGlob(src.tier3, "")
		warn(len(scs), err, src.tier3)
		recs := make([]tier3.Record, len(rep.Runs))
		for i, r := range rep.Runs {
			recs[i] = tier3.Record{SimRun: r}
		}
		voice := strings.Contains(mode, "voice")
		tier := "3"
		if voice {
			tier = "3 voice"
		}
		return tier3.NorthStarRuns(recs, scs, events, voice), tier, nil
	}
	scs, err := loadSimScenarios(src.sims, "")
	warn(len(scs), err, src.sims)
	return eval.NorthStarRuns(rep.Runs, eval.NorthStarOptions{Tier: "1", Scenarios: scs, EventsDir: events}), "1", nil
}
