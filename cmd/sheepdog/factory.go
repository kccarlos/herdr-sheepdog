package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// factoryDoctorRunner is overridden in CLI tests so no test touches the real
// machine's PATH, CLIs, herdr-core socket or ~/.config/sheepdog.
var factoryDoctorRunner = factory.Doctor

func printFactoryUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog factory <doctor> [flags] [folder]")
	fprintln(w, "")
	fprintln(w, "  doctor   check unattended factory prerequisites and print one-line fixes:")
	fprintln(w, "           herdr-core (hc), timed project-volume probe inside herdr-core")
	fprintln(w, "           (macOS consent prompts and external-disk stalls), at least one")
	fprintln(w, "           logged-in agent CLI (claude, codex or agy) plus a canary start,")
	fprintln(w, "           gh login with the default repo pinned, and codexbar. Exit 1 on FAIL.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --folder PATH      project folder to check (default . or [folder])")
	fprintln(w, "  --config PATH      config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --hc-socket PATH   herdr-core socket (overrides config and HC_SOCKET)")
	fprintln(w, "  --no-canary        skip the agent CLI canary start (no tokens spent)")
}

// runFactory dispatches the `sheepdog factory` command group.
func runFactory(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		printFactoryUsage(stderr)
		return 2
	}
	switch args[0] {
	case "doctor":
		return runFactoryDoctor(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printFactoryUsage(stdout)
		return 0
	default:
		fprintf(stderr, "sheepdog factory: unknown subcommand %q\n\n", args[0])
		printFactoryUsage(stderr)
		return 2
	}
}

func runFactoryDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("factory doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFactoryUsage(stderr) }
	folderFlag := fs.String("folder", "", "project folder")
	configPath := fs.String("config", "", "config file")
	hcSocket := fs.String("hc-socket", "", "herdr-core socket")
	noCanary := fs.Bool("no-canary", false, "skip agent CLI canary start")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 || (fs.NArg() == 1 && *folderFlag != "") {
		fprintln(stderr, "sheepdog factory doctor: pass either --folder PATH or one [folder] argument, not both")
		printFactoryUsage(stderr)
		return 2
	}

	home, _ := os.UserHomeDir()
	folder := *folderFlag
	if fs.NArg() == 1 {
		folder = fs.Arg(0)
	}
	folder = resolveTargetFolder(folder, home)

	socket, badCfg := resolveFactoryHCSocket(*hcSocket, *configPath, home)
	if badCfg != nil {
		fprintln(stdout, badCfg.Line())
		return 1
	}

	results := factoryDoctorRunner(ctx, factory.DoctorOptions{
		Folder:   folder,
		HCSocket: socket,
		Canary:   !*noCanary,
		Home:     home,
		GHHost:   os.Getenv("GH_HOST"),
	})
	for _, r := range results {
		fprintln(stdout, r.Line())
	}
	if onboard.Failed(results) {
		return 1
	}
	return 0
}

func resolveTargetFolder(raw, home string) string {
	f := expandHomePath(strings.TrimSpace(raw), home)
	if f == "" {
		f = "."
	}
	if filepath.IsAbs(f) {
		return filepath.Clean(f)
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return filepath.Join(wd, f)
	}
	return filepath.Clean(f)
}

func resolveFactoryHCSocket(explicitSocket, configPath, home string) (string, *onboard.Result) {
	if s := strings.TrimSpace(explicitSocket); s != "" {
		return expandHomePath(s, home), nil
	}
	if strings.TrimSpace(configPath) != "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			r := onboard.Result{
				Name:   "config",
				Status: onboard.Fail,
				Detail: err.Error(),
				Fix:    "fix the config file or pass --hc-socket PATH",
			}
			return "", &r
		}
		return cfg.Herdr.HCSocketPath(), nil
	}
	if envSock := strings.TrimSpace(os.Getenv("SHEEPDOG_HC_SOCKET")); envSock != "" {
		return expandHomePath(envSock, home), nil
	}
	defCfg := config.DefaultPath()
	if _, err := os.Stat(defCfg); err == nil {
		if cfg, err := config.Load(defCfg); err == nil {
			return cfg.Herdr.HCSocketPath(), nil
		}
	}
	return hc.DefaultSocket(os.Getenv, home, os.Getuid(), nil), nil
}

func expandHomePath(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}
