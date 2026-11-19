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
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// factoryDoctorRunner is overridden in CLI tests so no test touches the real
// machine's PATH, CLIs, herdr-core socket or ~/.config/sheepdog.
var factoryDoctorRunner = factory.Doctor

func printFactoryUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog factory <start|stop|doctor> [flags] [folder]")
	fprintln(w, "  start <folder> --goal \"<sentence>\"   start the factory coordinator for a repo")
	fprintln(w, "  stop [<folder>]                      stop a started factory project and its runs")
	fprintln(w, "  doctor                               check unattended factory prerequisites:")
	fprintln(w, "           herdr-core (hc), timed project-volume probe inside herdr-core")
	fprintln(w, "           (macOS consent prompts and external-disk stalls), at least one")
	fprintln(w, "           logged-in agent CLI (claude, codex or agy) plus a canary start")
	fprintln(w, "           inside herdr-core, gh login with the default repo pinned, and")
	fprintln(w, "           codexbar. Exit 1 on FAIL.")
	fprintln(w, "flags: --goal TEXT, --folder PATH, --config PATH, --hc-socket PATH, --no-canary")
}

// runFactory dispatches the `sheepdog factory` command group.
func runFactory(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		printFactoryUsage(stderr)
		return 2
	}
	switch args[0] {
	case "start", "stop":
		return runFactoryCmd(ctx, args[0], args[1:], stdout, stderr)
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

func runFactoryCmd(ctx context.Context, sub string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("factory "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFactoryUsage(stderr) }
	configPath, goal := fs.String("config", "", "config file"), fs.String("goal", "", "project goal")
	if err := fs.Parse(reorderFactoryDoctorArgs(args)); err != nil {
		return flagExit(err)
	}
	if (sub == "start" && (fs.NArg() != 1 || strings.TrimSpace(*goal) == "")) || (sub == "stop" && (fs.NArg() > 1 || *goal != "")) {
		printFactoryUsage(stderr)
		return 2
	}
	folder := ""
	if fs.NArg() == 1 {
		home, _ := os.UserHomeDir()
		folder = resolveTargetFolder(fs.Arg(0), home)
	}
	cfg, err := config.Load(*configPath)
	if err == nil {
		err = postJSON(ctx, daemon.LocalURL(cfg.Web.Bind, cfg.Web.Port), "/api/factory/"+sub, map[string]string{"folder": folder, "project": folder, "goal": strings.TrimSpace(*goal)})
	}
	if err != nil {
		fprintf(stderr, "sheepdog factory %s: %v\n", sub, err)
		return 1
	}
	if sub == "start" {
		fprintf(stdout, "factory started for %s\n", folder)
	} else if folder != "" {
		fprintf(stdout, "factory stopped for %s\n", folder)
	} else {
		fprintln(stdout, "factory stopped")
	}
	return 0
}

func runFactoryDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	reordered := reorderFactoryDoctorArgs(args)
	fs := flag.NewFlagSet("factory doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printFactoryUsage(stderr) }
	folderFlag := fs.String("folder", "", "project folder")
	configPath := fs.String("config", "", "config file")
	hcSocket := fs.String("hc-socket", "", "herdr-core socket")
	noCanary := fs.Bool("no-canary", false, "skip agent CLI canary start")
	if err := fs.Parse(reordered); err != nil {
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

	socket, badCfg := resolveFactoryConfig(*hcSocket, *configPath, home)
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

// reorderFactoryDoctorArgs allows flags to follow [folder] (e.g.
// `sheepdog factory doctor ~/repo --no-canary`) despite Go's flag.FlagSet
// stopping at the first positional argument.
func reorderFactoryDoctorArgs(args []string) []string {
	var flags, pos []string
	valueFlags := map[string]bool{
		"-goal": true, "--goal": true,
		"-folder": true, "--folder": true,
		"-config": true, "--config": true,
		"-hc-socket": true, "--hc-socket": true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if valueFlags[a] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
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

func resolveFactoryConfig(explicitSocket, configPath, home string) (socket string, badCfg *onboard.Result) {
	explicitCfg := strings.TrimSpace(configPath)
	cfgPath := explicitCfg
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	} else if _, err := os.Stat(cfgPath); err != nil {
		r := onboard.Result{
			Name:   "config",
			Status: onboard.Fail,
			Detail: err.Error(),
			Fix:    "pass an existing --config PATH",
		}
		return "", &r
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		r := onboard.Result{
			Name:   "config",
			Status: onboard.Fail,
			Detail: err.Error(),
			Fix:    "fix " + cfgPath + " or pass --config PATH",
		}
		return "", &r
	}
	if explicitSock := strings.TrimSpace(explicitSocket); explicitSock != "" {
		return expandHomePath(explicitSock, home), nil
	}
	return expandHomePath(cfg.Herdr.HCSocketPath(), home), nil
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
