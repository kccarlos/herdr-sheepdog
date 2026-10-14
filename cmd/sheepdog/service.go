package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/service"
)

// serviceRunner runs launchctl / systemctl / journalctl (tests replace
// it, so no test touches the real service manager).
var serviceRunner service.Runner = service.ExecRunner

// serviceHome is the home directory the unit file goes under (tests
// point it at a temp dir).
var serviceHome = os.UserHomeDir

func printServiceUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog service install|uninstall|status|logs [flags]")
	fprintln(w, "")
	fprintln(w, "Runs `sheepdog serve --env-file ~/.config/sheepdog/.env` as a user service, so")
	fprintln(w, "the daemon starts at login and restarts after a crash without a terminal:")
	fprintln(w, "a launchd agent on macOS (~/Library/LaunchAgents/"+service.Label+".plist,")
	fprintln(w, "output in ~/Library/Logs/sheepdog/serve.log), a systemd --user unit on Linux")
	fprintln(w, "(~/.config/systemd/user/"+service.Unit+", output in the journal).")
	fprintln(w, "")
	fprintln(w, "  install     write the unit and start it (again, when it runs: picks up a new binary)")
	fprintln(w, "  uninstall   stop it and remove the unit")
	fprintln(w, "  status      installed or not, and what the service manager says")
	fprintln(w, "  logs        the daemon's last lines (-n N, -f to follow)")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH     config file passed to serve (default: serve's own default)")
	fprintln(w, "  --env-file PATH   secret file passed to serve (default ~/.config/sheepdog/.env)")
	fprintln(w, "  --binary PATH     the sheepdog to run (default: this one; a Homebrew Cellar path")
	fprintln(w, "                    becomes the stable bin/ link, so upgrades keep working)")
	fprintln(w, "  --dry-run         install: print the unit and where it would go; change nothing")
}

// runService implements `sheepdog service`.
func runService(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printServiceUsage(stderr)
		if len(args) < 1 {
			return 2
		}
		return 0
	}
	verb := args[0]
	switch verb {
	case "install", "uninstall", "status", "logs":
	default:
		fprintf(stderr, "sheepdog service: unknown command %q\n\n", verb)
		printServiceUsage(stderr)
		return 2
	}
	fs := flag.NewFlagSet("service "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printServiceUsage(stderr) }
	configPath := fs.String("config", "", "config file")
	envFile := fs.String("env-file", "", "secret file")
	binary := fs.String("binary", "", "the sheepdog binary")
	dryRun := fs.Bool("dry-run", false, "print, change nothing")
	lines := fs.Int("n", 100, "log lines")
	follow := fs.Bool("f", false, "follow the log")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	spec, err := serviceSpec(*configPath, *envFile, *binary)
	if err != nil {
		fprintf(stderr, "sheepdog service: %v\n", err)
		return 1
	}
	m := service.Manager{Spec: spec, Run: serviceRunner, Out: stdout}
	switch verb {
	case "install":
		data, err := spec.Render()
		if err != nil {
			fprintf(stderr, "sheepdog service: %v\n", err)
			return 1
		}
		if *dryRun {
			fprintf(stdout, "would write %s:\n\n%s", spec.UnitPath(), data)
			return 0
		}
		if _, err := os.Stat(spec.EnvFile); err != nil {
			fprintf(stderr, "sheepdog service: warning: %s does not exist yet (run sheepdog init); the daemon starts without its keys\n", spec.EnvFile)
		}
		if addr, busy := portTaken(*configPath); busy {
			if st, _ := m.Status(ctx); strings.HasPrefix(st, "not installed") {
				fprintf(stderr, "sheepdog service: warning: something already listens on %s (a daemon started in a terminal?); the service's daemon cannot bind until it stops\n", addr)
			}
		}
		if err := m.Install(ctx); err != nil {
			fprintf(stderr, "sheepdog service: %v\n", err)
			return 1
		}
		return 0
	case "uninstall":
		if err := m.Uninstall(ctx); err != nil {
			fprintf(stderr, "sheepdog service: %v\n", err)
			return 1
		}
		return 0
	case "status":
		st, err := m.Status(ctx)
		if err != nil {
			fprintf(stderr, "sheepdog service: %v\n", err)
			return 1
		}
		fprintln(stdout, st)
		return 0
	case "logs":
		name, cargs, err := m.LogsCommand(*lines, *follow)
		if err != nil {
			fprintf(stderr, "sheepdog service: %v\n", err)
			return 1
		}
		cmd := exec.CommandContext(ctx, name, cargs...)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Run(); err != nil && ctx.Err() == nil {
			fprintf(stderr, "sheepdog service: %s: %v\n", name, err)
			return 1
		}
		return 0
	}
	return 2
}

// serviceSpec is the service for this machine and user.
func serviceSpec(configPath, envFile, binary string) (service.Spec, error) {
	home, err := serviceHome()
	if err != nil {
		return service.Spec{}, err
	}
	if binary == "" {
		if binary, err = stableExecutable(); err != nil {
			return service.Spec{}, err
		}
	}
	if binary, err = filepath.Abs(binary); err != nil {
		return service.Spec{}, err
	}
	if envFile == "" {
		envFile = config.DefaultEnvPath()
	}
	if envFile, err = filepath.Abs(envFile); err != nil {
		return service.Spec{}, err
	}
	if configPath != "" {
		if configPath, err = filepath.Abs(configPath); err != nil {
			return service.Spec{}, err
		}
	}
	return service.Spec{OS: runtimeGOOS, Binary: binary, EnvFile: envFile, ConfigPath: configPath, Home: home, UID: os.Getuid(), PATH: os.Getenv("PATH")}, nil
}

// stableExecutable is this binary's path, made stable across upgrades:
// a Homebrew Cellar path (…/Cellar/sheepdog/<version>/bin/sheepdog)
// becomes the prefix's bin/sheepdog link. A `go run` build lives in a
// temp dir that disappears: refused.
func stableExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if prefix, _, ok := strings.Cut(exe, "/Cellar/"); ok {
		link := filepath.Join(prefix, "bin", "sheepdog")
		if _, err := os.Stat(link); err == nil {
			return link, nil
		}
	}
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return "", errors.New("this sheepdog was built by `go run` into a temp dir; install it (go install ./cmd/sheepdog) or pass --binary")
	}
	return exe, nil
}

// portTaken reports whether something already listens on the daemon's
// web port (config web.bind / web.port).
func portTaken(configPath string) (string, bool) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", false
	}
	host := cfg.Web.Bind
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(cfg.Web.Port))
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return addr, false
	}
	_ = conn.Close()
	return addr, true
}

// runtimeGOOS is runtime.GOOS (a variable so tests read the same value).
var runtimeGOOS = runtime.GOOS
