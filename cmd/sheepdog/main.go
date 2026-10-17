// Command sheepdog is the sheepdog daemon and its CLI.
//
// User commands (setup, the daemon's lifecycle, maintenance) are listed
// by printUsage; developer commands live under `sheepdog dev` and keep
// their older top-level names as hidden aliases.
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
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cliNotices(ctx, os.Args[1:], os.Stderr)
	os.Exit(code)
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
	case "setup":
		return runSetup(ctx, args[1:], os.Stdin, stdout, stderr)
	case "init":
		return runInit(ctx, args[1:], os.Stdin, stdout, stderr)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr)
	case "pair":
		return runPair(ctx, args[1:], stdout, stderr)
	case "demo":
		return runDemo(ctx, args[1:], stdout, stderr)
	case "start":
		return runStart(ctx, args[1:], stdout, stderr)
	case "stop":
		return runStop(ctx, args[1:], stdout, stderr)
	case "restart":
		return runRestart(ctx, args[1:], stdout, stderr)
	case "status":
		return runStatus(ctx, args[1:], stdout, stderr)
	case "logs":
		return runLogs(ctx, args[1:], stdout, stderr)
	case "open":
		return runOpen(ctx, args[1:], stdout, stderr)
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr)
	case "update":
		return runUpdate(ctx, args[1:], stdout, stderr)
	case "uninstall":
		return runUninstall(ctx, args[1:], stdout, stderr)
	case "data":
		return runData(ctx, args[1:], stdout, stderr)
	case "service":
		return runService(ctx, args[1:], stdout, stderr)
	case "config":
		return runConfig(ctx, args[1:], stdout, stderr)
	case "dev":
		return runDev(ctx, args[1:], stdout, stderr)
	case "version", "--version":
		fprintln(stdout, versionString(version, commit, date))
		return 0
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		// The developer commands' old top-level names keep working
		// (hidden): scripts and docs call `sheepdog eval`, `sheepdog bench`….
		if devCommands[args[0]] != nil {
			return runDev(ctx, args, stdout, stderr)
		}
		fprintf(stderr, "sheepdog: unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

// devCommand is one developer command: `sheepdog dev <name>`, also
// reachable (hidden) as `sheepdog <name>`.
type devCommand struct {
	run  func(ctx context.Context, args []string, stdout, stderr io.Writer) int
	help string
}

// devOrder is the order `sheepdog dev --help` lists them in.
var devOrder = []string{"voice-test", "brain-test", "eval", "scorecard", "audio-e2e", "bench", "herdr", "prompts", "feedback"}

var devCommands = map[string]*devCommand{
	"voice-test": {runVoiceTest, "stream a WAV through a Gemini Live session with echo tools"},
	"brain-test": {runBrainTest, "run one orchestrator turn against a mock herdr and a fixture"},
	"eval":       {runEval, "run the conversation eval sets (--set NAME|all [--live]); extract, expand"},
	"scorecard":  {runScorecard, "print docs/dev/iteration-2.md's scorecard metrics over the event log"},
	"audio-e2e":  {runAudioE2E, "speak scenarios (macOS say + noise) into a running daemon's /ws"},
	"bench":      {runBench, "the voice benchmark: synthetic speech into scratch daemons (tier 2)"},
	"herdr":      {runHerdr, "herdr watch: print debounced agent state transitions, persist the table"},
	"prompts":    {runPrompts, "export|show: inspect or scaffold the prompt override directory"},
	"feedback":   {runFeedback, "list the user's feedback marks with a diagnosis of each"},
}

// runDev implements `sheepdog dev <command>`.
func runDev(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		w := stderr
		if len(args) > 0 {
			w = stdout
		}
		printDevUsage(w)
		if len(args) < 1 {
			return 2
		}
		return 0
	}
	c := devCommands[args[0]]
	if c == nil {
		fprintf(stderr, "sheepdog dev: unknown command %q\n\n", args[0])
		printDevUsage(stderr)
		return 2
	}
	return c.run(ctx, args[1:], stdout, stderr)
}

func printDevUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog dev <command> [flags]")
	fprintln(w, "")
	fprintln(w, "Developer commands, for working on sheepdog itself. Each also runs as")
	fprintln(w, "`sheepdog <command>` (the older spelling).")
	fprintln(w, "")
	for _, name := range devOrder {
		fprintf(w, "  %-12s %s\n", name, devCommands[name].help)
	}
}

// versionString formats the version, commit, and build date for the
// "version" subcommand.
func versionString(version, commit, date string) string {
	return fmt.Sprintf("sheepdog %s (commit %s, built %s)", version, commit, date)
}

func printUsage(w io.Writer) {
	fprintln(w, "sheepdog: talk to your coding agents by voice.")
	fprintln(w, "")
	fprintln(w, "usage: sheepdog <command> [flags]      (sheepdog <command> --help for its flags)")
	fprintln(w, "")
	fprintln(w, "Set up:")
	fprintln(w, "  setup       set up in your browser: key, checks, start at login, phone, talk (start here)")
	fprintln(w, "  init        the same setup as terminal questions (also: sheepdog setup --terminal)")
	fprintln(w, "  doctor      check the setup, with fixes; --report: a redacted bundle for a bug report")
	fprintln(w, "  pair        show the QR code the iOS app scans")
	fprintln(w, "  demo        try it without herdr: scripted workers (needs GEMINI_API_KEY)")
	fprintln(w, "")
	fprintln(w, "Run:")
	fprintln(w, "  start       start the daemon in the background")
	fprintln(w, "  stop        stop it")
	fprintln(w, "  restart     stop, then start")
	fprintln(w, "  status      running or not: pid, version, URL, models, herdr, phones, last error")
	fprintln(w, "  logs        print the runtime log (-f to follow, -n N lines)")
	fprintln(w, "  open        open the voice page in your browser (or: open setup | open debug)")
	fprintln(w, "  serve       run the daemon in this terminal (Ctrl-C stops it)")
	fprintln(w, "")
	fprintln(w, "Manage:")
	fprintln(w, "  update      check for a new release and print how to upgrade")
	fprintln(w, "  uninstall   stop, remove the login service, ask before deleting config and data")
	fprintln(w, "  service     install|uninstall|start|status|logs: run at login (launchd / systemd)")
	fprintln(w, "  data        clear [--events|--all]: delete the event log (your transcripts) or all data")
	fprintln(w, "  config      show|init: inspect or scaffold config.toml")
	fprintln(w, "  version     print version, commit and build date")
	fprintln(w, "  help        show this help")
	fprintln(w, "")
	fprintln(w, "Developer commands (voice-test, brain-test, eval, scorecard, audio-e2e, bench,")
	fprintln(w, "herdr watch, prompts, feedback): sheepdog dev --help")
}

// fprintln and fprintf write to w and intentionally discard the error:
// stdout/stderr write failures are not actionable for a CLI like this.
func fprintln(w io.Writer, a string) {
	_, _ = fmt.Fprintln(w, a)
}

func fprintf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}
