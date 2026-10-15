package main

import (
	"bufio"
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// stdin is what the confirmation prompts read (tests replace it).
var stdin io.Reader = os.Stdin

// confirm asks question and reports a yes. Anything but y/yes is a no,
// and so is end of input (no terminal).
func confirm(in io.Reader, out io.Writer, question string) bool {
	fprintf(out, "%s [y/N] ", question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func printDataUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog data clear [--events | --all] [--yes] [--config PATH]")
	fprintln(w, "")
	fprintln(w, "Deletes what sheepdog stored (CONFIG.md \"What sheepdog stores\"), after asking:")
	fprintln(w, "  --events   the event log and the orchestrator's transcripts, which hold what")
	fprintln(w, "             you said (default)")
	fprintln(w, "  --all      everything under data_dir: also the agent table, notes, the")
	fprintln(w, "             registered phones and the logs. Your config and keys stay.")
	fprintln(w, "  --yes      do not ask")
	fprintln(w, "The daemon must be stopped first (sheepdog stop).")
}

// runData implements `sheepdog data clear`.
func runData(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "clear" {
		printDataUsage(stderr)
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
			return 0
		}
		return 2
	}
	fs := flag.NewFlagSet("data clear", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printDataUsage(stderr) }
	configPath, envFile := lifecycleFlags(fs)
	events := fs.Bool("events", false, "event log")
	all := fs.Bool("all", false, "everything")
	yes := fs.Bool("yes", false, "do not ask")
	if err := fs.Parse(args[1:]); err != nil {
		return flagExit(err)
	}
	if *events && *all {
		fprintln(stderr, "sheepdog data clear: --events or --all, not both")
		return 2
	}
	l, code, ok := loadLifecycle(ctx, "data", *configPath, *envFile, stdout, stderr)
	if !ok {
		return code
	}
	if _, _, running := l.running(ctx); running {
		fprintln(stderr, "sheepdog is running; stop it first (sheepdog stop), then run this again.")
		return 1
	}
	data := l.cfg.DataDir
	if *all {
		if _, err := os.Stat(data); err != nil {
			fprintf(stdout, "nothing to delete: %s does not exist\n", data)
			return 0
		}
		if !*yes && !confirm(stdin, stdout, "Delete everything in "+data+" (event log, transcripts, agent table, notes, phones, logs)?") {
			fprintln(stdout, "nothing deleted")
			return 1
		}
		if err := os.RemoveAll(data); err != nil {
			fprintf(stderr, "sheepdog data clear: %v\n", err)
			return 1
		}
		fprintf(stdout, "deleted %s\n", data)
		return 0
	}
	files, _ := eventlog.DayFiles(data)
	brain, _ := eventlog.BrainFiles(data)
	if len(files)+len(brain) == 0 {
		fprintf(stdout, "nothing to delete: no event log in %s\n", filepath.Join(data, "events"))
		return 0
	}
	q := "Delete the event log (" + plural(len(files), "day file") + ") and the orchestrator's transcripts (" + plural(len(brain), "file") + ") in " + data + "?"
	if !*yes && !confirm(stdin, stdout, q) {
		fprintln(stdout, "nothing deleted")
		return 1
	}
	removed, err := eventlog.Clear(data)
	if err != nil {
		fprintf(stderr, "sheepdog data clear: %v\n", err)
		return 1
	}
	fprintf(stdout, "deleted %s\n", plural(len(removed), "file"))
	return 0
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}
