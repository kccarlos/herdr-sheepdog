package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"

	herdrsheepdog "github.com/kccarlos/herdr-sheepdog"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// factoryDoctorRunner is overridden in CLI tests so no test touches the real
// machine's PATH, CLIs, herdr-core socket or ~/.config/sheepdog.
var factoryDoctorRunner = factory.Doctor

func printFactoryUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog factory <doctor> [flags] [folder]")
	fprintln(w, "")
	fprintln(w, "  doctor   check unattended factory prerequisites, unpack the embedded engine")
	fprintln(w, "           and install/repair the pinned CPython runtime with uv if missing:")
	fprintln(w, "           embedded engine, uv-managed CPython in data_dir, herdr-core (hc),")
	fprintln(w, "           timed project-volume probe inside herdr-core (macOS consent prompts")
	fprintln(w, "           and external-disk stalls), at least one logged-in agent CLI (claude,")
	fprintln(w, "           codex or agy) plus a canary start inside herdr-core, gh login with")
	fprintln(w, "           the default repo pinned, codexbar, and tart VMs (when tart is installed). Exit 1 on FAIL.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --folder PATH      project folder to check (default . or [folder])")
	fprintln(w, "  --config PATH      config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --hc-socket PATH   herdr-core socket (overrides config and HC_SOCKET)")
	fprintln(w, "  --no-canary        skip the agent CLI canary start (no tokens spent)")
	fprintln(w, "  --no-repair        check only; do not unpack the engine or run uv python install")
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
	case "python-version":
		fprintln(stdout, factory.PinnedPythonVersion())
		return 0
	case "python-dir":
		return runFactoryPythonDir(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printFactoryUsage(stdout)
		return 0
	default:
		fprintf(stderr, "sheepdog factory: unknown subcommand %q\n\n", args[0])
		printFactoryUsage(stderr)
		return 2
	}
}

func runFactoryPythonDir(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("factory python-dir", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	home, _ := os.UserHomeDir()
	_, dataDir, badCfg := resolveFactoryConfig("", *configPath, home)
	if badCfg != nil {
		if strings.TrimSpace(*configPath) != "" {
			fprintln(stderr, badCfg.Line())
			return 1
		}
		dataDir = filepath.Join(home, ".local", "share", "sheepdog")
	}
	fprintln(stdout, factory.PythonInstallDir(dataDir))
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
	noRepair := fs.Bool("no-repair", false, "do not unpack engine or run uv python install")
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

	socket, dataDir, badCfg := resolveFactoryConfig(*hcSocket, *configPath, home)
	if badCfg != nil {
		fprintln(stdout, badCfg.Line())
		return 1
	}

	results := factoryDoctorRunner(ctx, factory.DoctorOptions{
		Folder:       folder,
		DataDir:      dataDir,
		SettingsPath: resolveFactorySettingsPath(home),
		Engine:       factory.EngineSource{FS: herdrsheepdog.Factory, Version: version},
		NoRepair:     *noRepair,
		HCSocket:     socket,
		Canary:       !*noCanary,
		Home:         home,
		GHHost:       os.Getenv("GH_HOST"),
		TartHome:     os.Getenv("TART_HOME"),
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

func resolveFactorySettingsPath(home string) string {
	if p := strings.TrimSpace(os.Getenv("HERDR_TASK_SETTINGS")); p != "" {
		return p
	}
	for _, env := range []string{"SHEEPDOG_FACTORY_HOME", "SHEEPDOG_HOME", "SHEPHERDR_HOME"} {
		if v := strings.TrimSpace(os.Getenv(env)); filepath.IsAbs(v) {
			return filepath.Join(v, "config", "settings.json")
		}
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "sheepdog", "settings.json")
	}
	return filepath.Join(home, ".config", "sheepdog", "settings.json")
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

func resolveFactoryConfig(explicitSocket, configPath, home string) (socket, dataDir string, badCfg *onboard.Result) {
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
		return "", "", &r
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		r := onboard.Result{
			Name:   "config",
			Status: onboard.Fail,
			Detail: err.Error(),
			Fix:    "fix " + cfgPath + " or pass --config PATH",
		}
		return "", "", &r
	}
	resolvedDataDir := expandHomePath(cfg.DataDir, home)
	if explicitSock := strings.TrimSpace(explicitSocket); explicitSock != "" {
		return expandHomePath(explicitSock, home), resolvedDataDir, nil
	}
	return expandHomePath(cfg.Herdr.HCSocketPath(), home), resolvedDataDir, nil
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
