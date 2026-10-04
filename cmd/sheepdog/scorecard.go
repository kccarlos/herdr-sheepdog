package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/scorecard"
)

func printScorecardUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog scorecard [flags]")
	fprintln(w, "")
	fprintln(w, "Computes docs/iteration-2.md's scorecard metrics (latency and the")
	fprintln(w, "per-utterance turn.latency breakdown, ear-metrics")
	fprintln(w, "quality of spoken lines, repeat-ask rate, lost-utterance rate, the brain-line")
	fprintln(w, "drop rate, Ctrl-C sends, comprehension follow-ups, duplicate speech, and the")
	fprintln(w, "Gemini resume retention: prompt tokens after/before each resume, per day) from the")
	fprintln(w, "recorded event log and prints a report. Read-only: it never writes to the")
	fprintln(w, "event log or changes daemon behaviour.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH        config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --events-dir PATH    event log directory (default <data_dir>/events from config)")
	fprintln(w, "  --from YYYY-MM-DD    only days on or after this date")
	fprintln(w, "  --to YYYY-MM-DD      only days on or before this date")
	fprintln(w, "  --session            split the report per daemon run (at each daemon.start)")
	fprintln(w, "  --format md|json     report format (default md)")
}

type scorecardOpts struct {
	configPath, eventsDir, from, to, format string
	session                                 bool
}

func parseScorecardFlags(args []string, stderr io.Writer) (scorecardOpts, int, bool) {
	fs := flag.NewFlagSet("scorecard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printScorecardUsage(stderr) }
	var o scorecardOpts
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.StringVar(&o.eventsDir, "events-dir", "", "event log directory")
	fs.StringVar(&o.from, "from", "", "only days on or after this date")
	fs.StringVar(&o.to, "to", "", "only days on or before this date")
	fs.BoolVar(&o.session, "session", false, "split the report per daemon run")
	fs.StringVar(&o.format, "format", "md", "md | json")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	if o.format != "md" && o.format != "json" {
		fprintln(stderr, "sheepdog scorecard: --format must be md or json")
		return o, 2, false
	}
	return o, 0, true
}

// runScorecard implements `sheepdog scorecard` (docs/iteration-2.md WP0a).
func runScorecard(_ context.Context, args []string, stdout, stderr io.Writer) int {
	o, code, run := parseScorecardFlags(args, stderr)
	if !run {
		return code
	}

	eventsDir := o.eventsDir
	if eventsDir == "" {
		cfg, err := config.Load(o.configPath)
		if err != nil {
			fprintf(stderr, "sheepdog scorecard: %v\n", err)
			return 1
		}
		eventsDir = cfg.DataDir + "/events"
	}

	events, err := scorecard.Load(eventsDir, o.from, o.to)
	if err != nil {
		fprintf(stderr, "sheepdog scorecard: %v\n", err)
		return 1
	}

	var reports []scorecard.Report
	if o.session {
		for _, s := range scorecard.SplitSessions(events) {
			reports = append(reports, scorecard.Analyze(s.Label, s.Events))
		}
	} else {
		reports = []scorecard.Report{scorecard.Analyze("total", events)}
	}

	cmdLine := "sheepdog scorecard " + strings.Join(args, " ")
	switch o.format {
	case "json":
		err = scorecard.WriteJSON(stdout, reports)
	default:
		err = scorecard.WriteMarkdown(stdout, reports, cmdLine)
	}
	if err != nil {
		fprintf(stderr, "sheepdog scorecard: %v\n", err)
		return 1
	}
	return 0
}
