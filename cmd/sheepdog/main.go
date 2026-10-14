// Command sheepdog is the sheepdog daemon entry point.
//
// Subcommands: "serve" (the daemon, WP5), "herdr watch" (WP1),
// "voice-test" (WP2), "brain-test" (phase 1a), "version" and "help".
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
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr)
	case "config":
		return runConfig(ctx, args[1:], stdout, stderr)
	case "init":
		return runInit(ctx, args[1:], os.Stdin, stdout, stderr)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr)
	case "service":
		return runService(ctx, args[1:], stdout, stderr)
	case "herdr":
		return runHerdr(ctx, args[1:], stdout, stderr)
	case "voice-test":
		return runVoiceTest(ctx, args[1:], stdout, stderr)
	case "brain-test":
		return runBrainTest(ctx, args[1:], stdout, stderr)
	case "prompts":
		return runPrompts(ctx, args[1:], stdout, stderr)
	case "eval":
		return runEval(ctx, args[1:], stdout, stderr)
	case "scorecard":
		return runScorecard(ctx, args[1:], stdout, stderr)
	case "feedback":
		return runFeedback(ctx, args[1:], stdout, stderr)
	case "bench":
		return runBench(ctx, args[1:], stdout, stderr)
	case "audio-e2e":
		return runAudioE2E(ctx, args[1:], stdout, stderr)
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
	fprintln(w, "  init          set up: check herdr, ask for and validate a key, write config + secrets")
	fprintln(w, "  doctor        check the setup (keys, herdr, port, token, push, voice) with fixes")
	fprintln(w, "  serve         run the daemon (web server, supervisor, voice session)")
	fprintln(w, "  service       install|uninstall|status|logs: run serve at login (launchd / systemd --user)")
	fprintln(w, "  config        show|init: inspect or scaffold config.toml")
	fprintln(w, "  herdr watch   print debounced agent state transitions, persist the table")
	fprintln(w, "  voice-test    stream a WAV through a Gemini Live session with echo tools")
	fprintln(w, "  brain-test    run one orchestrator turn against a mock herdr and a fixture")
	fprintln(w, "  prompts       export|show: inspect or scaffold the prompt override directory")
	fprintln(w, "  eval          run the conversation eval sets (--set NAME|all [--live]); extract, expand")
	fprintln(w, "  scorecard     print docs/iteration-2.md's scorecard metrics over the event log")
	fprintln(w, "  feedback      list the user's feedback marks with a diagnosis of each")
	fprintln(w, "  audio-e2e     speak scenarios (macOS say + noise) into a running daemon's /ws")
	fprintln(w, "  bench         the voice benchmark: synthetic speech into scratch daemons (tier 2)")
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
