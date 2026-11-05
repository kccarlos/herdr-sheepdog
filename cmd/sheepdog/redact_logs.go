package main

import (
	"context"
	"flag"
	"io"
	"path/filepath"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

func printRedactLogsUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog redact-logs [--config PATH] [--env-file PATH]")
	fprintln(w, "")
	fprintln(w, "Masks API keys, tokens and private keys in existing orchestrator logs under")
	fprintln(w, "<data_dir>/brain (*.jsonl and *.requests.jsonl), rewriting changed files")
	fprintln(w, "atomically and keeping each JSON line valid.")
	fprintln(w, "The daemon must be stopped first (sheepdog stop).")
}

// runRedactLogs implements `sheepdog redact-logs`.
func runRedactLogs(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("redact-logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printRedactLogsUsage(stderr) }
	configPath, envFile := lifecycleFlags(fs)
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	if fs.NArg() > 0 {
		printRedactLogsUsage(stderr)
		return 2
	}
	l, code, ok := loadLifecycle(ctx, "redact-logs", *configPath, *envFile, stdout, stderr)
	if !ok {
		return code
	}
	if _, _, running := l.running(ctx); running {
		fprintln(stderr, "sheepdog is running; stop it first (sheepdog stop), then run this again.")
		return 1
	}
	res, err := brain.RedactLogs(l.cfg.DataDir, redact.ForConfig(l.cfg))
	if err != nil {
		fprintf(stderr, "sheepdog redact-logs: %v\n", err)
		return 1
	}
	brainDir := filepath.Join(l.cfg.DataDir, brain.TranscriptDir)
	switch {
	case res.CheckedFiles == 0:
		fprintf(stdout, "nothing to redact: no orchestrator logs in %s\n", brainDir)
	case res.ChangedFiles == 0:
		fprintf(stdout, "nothing to redact (%s already clean)\n", plural(res.CheckedFiles, "file"))
	default:
		fprintf(stdout, "redacted %s (%s) in %s\n", plural(res.ChangedFiles, "file"), plural(res.ChangedLines, "line"), brainDir)
	}
	return 0
}
