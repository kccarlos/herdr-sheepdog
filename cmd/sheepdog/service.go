package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/service"
)

// serviceRunner runs launchctl / systemctl / journalctl (tests replace
// it, so no test touches the real service manager).
var serviceRunner service.Runner = service.ExecRunner

// serviceHome is the home directory the unit file goes under (tests
// point it at a temp dir).
var serviceHome = os.UserHomeDir

func printServiceUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog service install|uninstall|start|status|logs [flags]")
	fprintln(w, "")
	fprintln(w, "Runs `sheepdog serve --background --env-file ~/.config/sheepdog/.env` as a user")
	fprintln(w, "service, so the daemon starts at login and restarts after a crash without a")
	fprintln(w, "terminal: a launchd agent on macOS (~/Library/LaunchAgents/"+service.Label+".plist),")
	fprintln(w, "a systemd --user unit on Linux (~/.config/systemd/user/"+service.Unit+").")
	fprintln(w, "sheepdog start / stop / status / logs work with or without it.")
	fprintln(w, "")
	fprintln(w, "  install     write the unit and start it (again, when it runs: picks up a new binary)")
	fprintln(w, "  uninstall   stop it and remove the unit")
	fprintln(w, "  start       start the installed service if it is not running; installs nothing")
	fprintln(w, "  status      installed or not, and what the service manager says")
	fprintln(w, "  logs        the runtime log (same as sheepdog logs; -n N, -f to follow)")
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
	case "install", "uninstall", "start", "status", "logs":
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
		// The safer choice: the unit keeps the default env file (a ./.env
		// in a repo is not something a login service should depend on);
		// say so, and how to move the keys.
		if dd := cfgDataDir(*configPath); dd != "" {
			if rec := daemon.ReadEnvRecord(dd); rec.HadKeys && rec.Path != spec.EnvFile {
				fprintf(stderr, "sheepdog service: warning: the last daemon had its keys from %s, but the service reads %s; copy it there (chmod 600), or pass --env-file %s\n", rec.Path, spec.EnvFile, rec.Path)
			}
		}
		if _, err := os.Stat(spec.EnvFile); err != nil {
			fprintf(stderr, "sheepdog service: warning: %s does not exist yet (run sheepdog setup); the daemon starts without its keys\n", spec.EnvFile)
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
	case "start":
		installed, started, err := m.Start(ctx)
		switch {
		case err != nil:
			fprintf(stderr, "sheepdog service: %v\n", err)
			return 1
		case !installed:
			fprintln(stdout, "sheepdog service: not installed, nothing started (run: sheepdog service install)")
		case started:
			fprintln(stdout, "sheepdog service: started")
		default:
			fprintln(stdout, "sheepdog service: already running")
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
		la := []string{"-n", strconv.Itoa(*lines)}
		if *follow {
			la = append(la, "-f")
		}
		if *configPath != "" {
			la = append(la, "--config", *configPath)
		}
		return runLogs(ctx, la, stdout, stderr)
	}
	return 2
}

// cfgDataDir is the data directory of the config at configPath ("" on error).
func cfgDataDir(configPath string) string {
	cfg, err := config.Load(configPath)
	if err != nil {
		return ""
	}
	return cfg.DataDir
}

// serviceSpec is the service for this machine and user.
func serviceSpec(configPath, envFile, binary string) (service.Spec, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return service.Spec{}, err
	}
	return serviceSpecFor(cfg, configPath, envFile, binary)
}

// serviceSpecFor is serviceSpec for an already loaded config. It is also
// the command line `sheepdog start` runs detached, so the service and a
// background start run the same thing.
func serviceSpecFor(cfg config.Config, configPath, envFile, binary string) (service.Spec, error) {
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
	return service.Spec{OS: runtimeGOOS, Binary: binary, EnvFile: envFile, ConfigPath: configPath, Home: home, UID: os.Getuid(), PATH: os.Getenv("PATH"), StdioPath: daemon.StdioFile(cfg.DataDir)}, nil
}

// stableExecutable is this binary's path, made stable across upgrades:
// a Homebrew Cellar path (…/Cellar/sheepdog/<version>/bin/sheepdog)
// becomes the prefix's bin/sheepdog link. A `go run` build lives in a
// temp dir that disappears: refused.
func stableExecutable() (string, error) {
	exe, err := osExecutable()
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

// osExecutable is os.Executable (tests replace it).
var osExecutable = os.Executable

// runtimeGOOS is runtime.GOOS (a variable so tests read the same value).
var runtimeGOOS = runtime.GOOS
