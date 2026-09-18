// Command sheepdog is the sheepdog daemon entry point.
//
// Subcommands so far: "version", "help" and "herdr watch" (WP1). The
// supervisor, voice session and web server land in later work packages per
// sheepdog-v1-plan.md.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// version, commit, and date are injected at build time via:
//
//	-ldflags "-X main.version=... -X main.commit=... -X main.date=..."
//
// They default to placeholder values for `go build`/`go run` without
// ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run implements the CLI and returns the process exit code. It is
// separated from main so it can be exercised by tests without calling
// os.Exit. ctx ends on SIGINT/SIGTERM; long-running subcommands return
// cleanly when it does.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case "herdr":
		return runHerdr(ctx, args[1:], stdout, stderr)
	case "version":
		fprintln(stdout, versionString(version, commit, date))
		return 0
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fprintf(stderr, "sheepdog: unknown subcommand %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

// versionString formats the version, commit, and build date for the
// "version" subcommand.
func versionString(version, commit, date string) string {
	return fmt.Sprintf("sheepdog %s (commit %s, built %s)", version, commit, date)
}

func printUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog <command>")
	fprintln(w, "")
	fprintln(w, "commands:")
	fprintln(w, "  herdr watch   print debounced agent state transitions, persist the table")
	fprintln(w, "  version       print version, commit, and build date")
	fprintln(w, "  help          show this help message")
}

// fprintln and fprintf write to w and intentionally discard the error:
// stdout/stderr write failures are not actionable for a CLI like this.
func fprintln(w io.Writer, a string) {
	_, _ = fmt.Fprintln(w, a)
}

func fprintf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}
