package main

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/feedback"
	"github.com/kccarlos/herdr-sheepdog/internal/scorecard"
)

func printFeedbackUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog feedback [flags]")
	fprintln(w, "")
	fprintln(w, "Lists the user's feedback marks (the app's \"that paused\" button, POST")
	fprintln(w, "/api/feedback) from the event log, each with a diagnosis read off the")
	fprintln(w, "30 s snapshot it carries: model_gap (the voice's audio stopped), delivery_gap")
	fprintln(w, "(on time at the daemon, late to the app), client_playback (the app's player")
	fprintln(w, "dropped or ran out of audio it had), reply_wait (no audio: waiting for an")
	fprintln(w, "answer) or no_gap_found. Read-only.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH        config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --events-dir PATH    event log directory (default <data_dir>/events from config)")
	fprintln(w, "  --from YYYY-MM-DD    only days on or after this date")
	fprintln(w, "  --to YYYY-MM-DD      only days on or before this date")
	fprintln(w, "  --last N             only the N newest marks (default all)")
	fprintln(w, "  --format text|json   output format (default text)")
}

// runFeedback implements `sheepdog feedback`.
func runFeedback(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("feedback", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFeedbackUsage(stderr) }
	var configPath, eventsDir, from, to, format string
	var last int
	fs.StringVar(&configPath, "config", "", "config file")
	fs.StringVar(&eventsDir, "events-dir", "", "event log directory")
	fs.StringVar(&from, "from", "", "only days on or after this date")
	fs.StringVar(&to, "to", "", "only days on or before this date")
	fs.IntVar(&last, "last", 0, "only the N newest marks")
	fs.StringVar(&format, "format", "text", "text | json")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if format != "text" && format != "json" {
		fprintln(stderr, "sheepdog feedback: --format must be text or json")
		return 2
	}
	if eventsDir == "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			fprintf(stderr, "sheepdog feedback: %v\n", err)
			return 1
		}
		eventsDir = cfg.DataDir + "/events"
	}
	events, err := scorecard.Load(eventsDir, from, to)
	if err != nil {
		fprintf(stderr, "sheepdog feedback: %v\n", err)
		return 1
	}
	marks := feedback.Marks(events)
	if last > 0 && len(marks) > last {
		marks = marks[len(marks)-last:]
	}
	if format == "json" {
		err = feedback.WriteJSON(stdout, marks)
	} else {
		err = feedback.WriteText(stdout, marks)
	}
	if err != nil {
		fprintf(stderr, "sheepdog feedback: %v\n", err)
		return 1
	}
	return 0
}
