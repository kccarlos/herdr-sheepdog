package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	herdrsheepdog "github.com/kccarlos/herdr-sheepdog"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

func printSetupUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog setup [--terminal] [--no-open] [--config PATH] [--env-file PATH]")
	fprintln(w, "")
	fprintln(w, "Sets sheepdog up in your browser: starts the daemon if it is not running (it")
	fprintln(w, "comes up without keys) and opens its setup page, http://127.0.0.1:<port>/setup:")
	fprintln(w, "the Gemini key (checked live), the checks, start at login, pairing your phone,")
	fprintln(w, "and a button to talk to it. Run it again any time to change keys or re-pair.")
	fprintln(w, "")
	fprintln(w, "Without a display (over SSH) or with --terminal it asks the same questions in")
	fprintln(w, "the terminal instead (sheepdog init), then restarts a running daemon.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --terminal        ask in the terminal, no browser")
	fprintln(w, "  --no-open         print the page's URL without opening a browser")
	fprintln(w, "  --config PATH     config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH   secret file (default ~/.config/sheepdog/.env)")
}

// hasDisplay reports whether a browser can be opened here: not over
// SSH, and on Linux only with an X11 or Wayland display (tests replace
// it).
var hasDisplay = func() bool {
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	if runtime.GOOS == "linux" {
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
	return true
}

// runSetup implements `sheepdog setup`.
func runSetup(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printSetupUsage(stderr) }
	configPath, envFile := lifecycleFlags(fs)
	terminal := fs.Bool("terminal", false, "terminal prompts")
	noOpen := fs.Bool("no-open", false, "do not open a browser")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	if *terminal || !hasDisplay() {
		return setupTerminal(ctx, *configPath, *envFile, *terminal, stdin, stdout, stderr)
	}
	envPath := setupEnvPath(*configPath, *envFile)
	l, code, ok := loadLifecycle(ctx, "setup", *configPath, existingSetupEnvPath(envPath, *envFile), stdout, stderr)
	if !ok {
		return code
	}
	l.useEnvFile(envPath)
	l.fromSetup = true
	prompt := newPrompter(stdin, stdout)
	wrote, err := setupEnvironment(ctx, l.cfg, envPath, setupEnvironmentOptions{
		Probe:         setupEnvironmentProbe(),
		Panel:         setupPanel,
		Layout:        herdrsheepdog.Layout,
		Prompt:        prompt,
		Interactive:   prompt.tty != nil,
		HerdrAppCheck: setupHerdrAppCheck,
		LayoutSwitch:  setupLayoutSwitch,
		Setenv:        os.Setenv,
		Out:           stdout,
	})
	if err != nil {
		fprintf(stderr, "sheepdog setup: %v\n", err)
		return 1
	} else if wrote {
		if _, _, running := l.running(ctx); running {
			fprintln(stdout, "")
			fprintln(stdout, "Restarting the running daemon so it uses the new settings…")
			if code := l.stop(ctx); code != 0 {
				return code
			}
			if code := l.start(ctx); code != 0 {
				return code
			}
		}
	}
	info, _, running := l.running(ctx)
	if !running {
		fprintln(stdout, "Starting sheepdog…")
		if code := l.start(ctx); code != 0 {
			fprintln(stderr, "\nThe setup page needs the daemon. Fix the above, or set up in the terminal: sheepdog setup --terminal")
			return code
		}
		if info, _, running = l.running(ctx); !running {
			fprintln(stderr, "sheepdog started but does not answer; see sheepdog logs")
			return 1
		}
	}
	url := info.URL + "/setup"
	fprintln(stdout, "")
	fprintln(stdout, "Set up sheepdog in your browser:")
	fprintln(stdout, "")
	fprintf(stdout, "    %s\n", url)
	fprintln(stdout, "")
	if *noOpen {
		return 0
	}
	if err := openBrowser(url); err != nil {
		fprintf(stderr, "Could not open a browser (%v): open the URL above yourself, or run: sheepdog setup --terminal\n", err)
		return 1
	}
	fprintln(stdout, "The page opened in your browser. It works on its own: you can close this terminal.")
	return 0
}

// setupTerminal is setup without a browser: `sheepdog init`'s questions,
// then the running daemon (if any) is restarted so it uses them.
func setupTerminal(ctx context.Context, configPath, envFile string, asked bool, stdin io.Reader, stdout, stderr io.Writer) int {
	if !asked {
		fprintln(stdout, "No display here (SSH?): setting up in the terminal. The setup page is http://127.0.0.1:<port>/setup on the computer itself.")
		fprintln(stdout, "")
	}
	var initArgs []string
	if configPath != "" {
		initArgs = append(initArgs, "--config", configPath)
	}
	if envFile != "" {
		initArgs = append(initArgs, "--env-file", envFile)
	}
	shared := newPrompter(stdin, stdout)
	if code := runInitWith(ctx, initArgs, shared, stdout, stderr); code != 0 {
		return code
	}
	envPath := setupEnvPath(configPath, envFile)
	l, code, ok := loadLifecycle(ctx, "setup", configPath, existingSetupEnvPath(envPath, envFile), stdout, stderr)
	if !ok {
		return code
	}
	l.useEnvFile(envPath)
	if _, err := setupEnvironment(ctx, l.cfg, envPath, setupEnvironmentOptions{
		Probe:         setupEnvironmentProbe(),
		Panel:         setupPanel,
		Layout:        herdrsheepdog.Layout,
		Prompt:        shared,
		Interactive:   shared.tty != nil,
		HerdrAppCheck: setupHerdrAppCheck,
		LayoutSwitch:  setupLayoutSwitch,
		Setenv:        os.Setenv,
		Out:           stdout,
	}); err != nil {
		fprintf(stderr, "sheepdog setup: %v\n", err)
		return 1
	}
	if p := shared; p.tty != nil && l.cfg.Web.IsLoopback() {
		fprintln(stdout, "")
		yes, err := p.yesNo("Let the iOS app reach sheepdog on your network (Wi-Fi, Tailscale), encrypted and for paired devices only?", false)
		if err == nil && yes {
			if err := onboard.SetPhoneAccess(envPath, true); err != nil {
				fprintf(stderr, "sheepdog setup: %v\n", err)
				return 1
			}
			fprintf(stdout, "Phone access on: SHEEPDOG_WEB_BIND=%s in %s. Scan the code from: sheepdog pair\n", onboard.PhoneBind, envPath)
			if l, code, ok = loadLifecycle(ctx, "setup", configPath, envPath, io.Discard, stderr); !ok {
				return code
			}
			l.out = stdout
		}
	}
	if _, pidKnown, running := l.running(ctx); running && pidKnown {
		fprintln(stdout, "")
		fprintln(stdout, "Restarting the running daemon so it uses the new settings…")
		if code := l.stop(ctx); code != 0 {
			return code
		}
		if code := l.start(ctx); code != 0 {
			return code
		}
	}
	fprintln(stdout, "")
	fprintln(stdout, strings.Join([]string{
		"Next:",
		"  sheepdog start         start the daemon (if it is not running)",
		"  sheepdog pair          the QR code for the iOS app (needs phone access: run sheepdog setup again and answer y above)",
		"  sheepdog open          the voice page, on this computer",
	}, "\n"))
	if !hasDisplay() {
		fprintln(stdout, "")
		fprintf(stdout, "No browser here? Talk from your own computer: ssh -N -L %[1]d:127.0.0.1:%[1]d <this host>, then open http://127.0.0.1:%[1]d/voice there.\n", l.cfg.Web.Port)
	}
	return 0
}

// setupEnvPath is the env file setup edits, matching init: an explicit
// --env-file, else .env beside the config file.
func setupEnvPath(configPath, envFile string) string {
	if envFile != "" {
		return envFile
	}
	return initEnvPath(configPath)
}

// existingSetupEnvPath is the setup env file loadLifecycle should read now:
// an explicit path, or the resolved one once init has created it.
func existingSetupEnvPath(envPath, requested string) string {
	if requested != "" {
		return requested
	}
	if _, err := os.Stat(envPath); err == nil {
		return envPath
	}
	return ""
}

type setupHerdrAppCheckFunc func(context.Context, string) (string, error)
type setupLayoutSwitchFunc func(context.Context) (string, error)

type setupEnvironmentOptions struct {
	Probe         onboard.Probe
	Panel         fs.FS
	Layout        []byte
	Prompt        *prompter
	Interactive   bool
	HerdrAppCheck setupHerdrAppCheckFunc
	LayoutSwitch  setupLayoutSwitchFunc
	Setenv        func(string, string) error
	Out           io.Writer
}

// These three seams are replaced in focused tests. They stay variables so
// runSetup can use the real machine, panel and command without a test ever
// needing to touch any of them.
var (
	setupEnvironmentProbe                        = onboard.SystemProbe
	setupPanel            fs.FS                  = herdrsheepdog.Panel
	setupHerdrAppCheck    setupHerdrAppCheckFunc = func(ctx context.Context, dir string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "herdr-app", "check", dir).CombinedOutput()
		return string(out), err
	}
	setupLayoutSwitch setupLayoutSwitchFunc = func(ctx context.Context) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "herdr-app", "do", "layout.switch", `{"name":"sheepdog"}`).CombinedOutput()
		return string(out), err
	}
)

// setupEnvironment detects herdr-tui, herdr-core, herdr-app and Sheepdog.app,
// resolves an ambiguous backend, and offers or installs the panel. It returns
// whether it wrote the backend env setting.
func setupEnvironment(ctx context.Context, cfg config.Config, envPath string, o setupEnvironmentOptions) (bool, error) {
	if o.Out == nil {
		return false, fmt.Errorf("no output writer")
	}
	env := onboard.Detect(cfg, o.Probe, o.Panel, o.Layout)
	fprintln(o.Out, "Your setup:")
	fprintf(o.Out, "  %-23s %s\n", "herdr-tui", setupPresence(env.OldHerdr))
	fprintf(o.Out, "  %-23s %s\n", "herdr-core", setupPresence(env.HerdrCore))
	fprintf(o.Out, "  %-23s %s\n", "herdr-app", setupFound(env.HerdrApp))
	if env.OS == "darwin" {
		fprintf(o.Out, "  %-23s %s\n", "Sheepdog.app", setupFound(env.SheepdogApp))
	}

	advice := onboard.AdviseBackend(cfg, env)
	wroteBackend := false
	writeBackend := func(value string) error {
		if err := onboard.EditEnvFile(envPath, map[string]string{"SHEEPDOG_HERDR_BACKEND": value}, nil); err != nil {
			return err
		}
		if o.Setenv != nil {
			if err := o.Setenv("SHEEPDOG_HERDR_BACKEND", value); err != nil {
				return fmt.Errorf("update SHEEPDOG_HERDR_BACKEND for the restart: %w", err)
			}
		}
		return nil
	}
	if advice.Why != "" {
		fprintln(o.Out, "")
		fprintln(o.Out, "herdr backend: "+advice.Why)
	}
	if advice.Set != "" {
		if err := writeBackend(advice.Set); err != nil {
			return false, err
		}
		fprintf(o.Out, "Set SHEEPDOG_HERDR_BACKEND=%s in %s.\n", advice.Set, envPath)
		wroteBackend = true
	} else if advice.Ask {
		chosen := ""
		if o.Interactive && o.Prompt != nil {
			var err error
			chosen, err = o.Prompt.chooseDef(
				"Which herdr should Sheepdog use",
				[]string{config.HerdrBackendHC, config.HerdrBackendHerdr},
				[]string{"herdr-core (hc) — herdr-app", "herdr-tui"},
				advice.Default,
			)
			if err != nil {
				return false, err
			}
		}
		if chosen != "" {
			if err := writeBackend(chosen); err != nil {
				return false, err
			}
			fprintf(o.Out, "Set SHEEPDOG_HERDR_BACKEND=%s in %s.\n", chosen, envPath)
			wroteBackend = true
		} else {
			fprintf(o.Out, "Set it with this line in %s:\n  SHEEPDOG_HERDR_BACKEND=%s\n", envPath, advice.Default)
		}
	}

	installedPanel := env.Panel == onboard.PanelCurrent
	if env.HerdrApp && (env.Panel == onboard.PanelMissing || env.Panel == onboard.PanelOutdated) {
		install := !o.Interactive
		if o.Interactive && o.Prompt != nil {
			var err error
			install, err = o.Prompt.yesNo("Install the Sheepdog panel for herdr-app?", true)
			if err != nil {
				return wroteBackend, err
			}
		}
		if install {
			if err := onboard.InstallPanel(env.HerdrAppHome, o.Panel); err != nil {
				return wroteBackend, fmt.Errorf("install the herdr-app panel: %w", err)
			}
			installedPanel = true
			fprintln(o.Out, "Installed the Sheepdog panel for herdr-app.")
			if o.HerdrAppCheck != nil && probeHasCommand(o.Probe, "herdr-app") {
				dir := filepath.Join(env.HerdrAppHome, "mods", "sheepdog")
				line, err := o.HerdrAppCheck(ctx, dir)
				if text := setupLastLine(line); text != "" {
					fprintln(o.Out, "herdr-app check: "+text)
				}
				if err != nil {
					fprintf(o.Out, "Warning: herdr-app check failed: %v\n", err)
				}
			}
		}
	}
	if env.HerdrApp && installedPanel {
		setupSheepdogLayout(ctx, env, o)
	}
	if env.OS == "darwin" && env.HerdrApp && !env.SheepdogApp {
		fprintln(o.Out, "")
		fprintln(o.Out, "Install Sheepdog.app for voice on this Mac:")
		fprintln(o.Out, "  brew install --cask kccarlos/tap/sheepdog-app")
	}
	return wroteBackend, nil
}

func setupSheepdogLayout(ctx context.Context, env onboard.Environment, o setupEnvironmentOptions) {
	if env.PanelDocked {
		fprintf(o.Out, "The Sheepdog panel is docked in herdr-app's %s layout.\n", env.ShownLayout)
		return
	}

	switchNow := false
	declined := false
	if o.Interactive && o.Prompt != nil {
		yes, err := o.Prompt.yesNo("Switch herdr-app to the Sheepdog layout (herdr-app's window with the panel docked on the right)?", true)
		if err != nil {
			fprintf(o.Out, "Warning: could not ask about the Sheepdog layout: %v\n", err)
		} else {
			switchNow = yes
			declined = !yes
		}
	}
	if declined {
		fprintln(o.Out, "Skipped the Sheepdog layout. To dock the panel later, run sheepdog setup again, or open the panel and choose Copy my layout.")
		return
	}
	if !switchNow {
		switch env.Layout {
		case onboard.LayoutMissing:
			if err := onboard.InstallLayout(env.HerdrAppHome, o.Layout); err != nil {
				fprintf(o.Out, "Warning: could not install the Sheepdog layout: %v\n", err)
			} else {
				fprintln(o.Out, "Installed the Sheepdog layout for herdr-app.")
			}
		case onboard.LayoutDifferent:
			fprintln(o.Out, "The Sheepdog layout already exists but differs from this version, so setup kept it.")
		}
		if probeHasCommand(o.Probe, "herdr-app") {
			fprintln(o.Out, "To switch herdr-app to it, run:")
			fprintln(o.Out, `  herdr-app do layout.switch '{"name":"sheepdog"}'`)
		} else {
			fprintln(o.Out, "Open herdr-app's layout picker and choose Sheepdog, or press Ctrl+Shift+L to cycle to it.")
		}
		return
	}

	switch env.Layout {
	case onboard.LayoutMissing:
		if err := onboard.InstallLayout(env.HerdrAppHome, o.Layout); err != nil {
			fprintf(o.Out, "Warning: could not install the Sheepdog layout: %v\n", err)
			return
		}
		fprintln(o.Out, "Installed the Sheepdog layout for herdr-app.")
	case onboard.LayoutDifferent:
		fprintln(o.Out, "The Sheepdog layout already exists but differs from this version, so setup kept it.")
	}
	if !probeHasCommand(o.Probe, "herdr-app") {
		fprintln(o.Out, "Open herdr-app's layout picker and choose Sheepdog, or press Ctrl+Shift+L to cycle to it.")
		return
	}
	if o.LayoutSwitch == nil {
		printLayoutSwitchManual(o.Out, nil)
		return
	}
	line, err := o.LayoutSwitch(ctx)
	if err != nil {
		printLayoutSwitchManual(o.Out, err)
		return
	}
	if text := setupLastLine(line); text != "" {
		fprintln(o.Out, "herdr-app layout switch: "+text)
	}
	fprintln(o.Out, "Switched herdr-app to the Sheepdog layout.")
}

func printLayoutSwitchManual(w io.Writer, err error) {
	if err != nil {
		fprintf(w, "Warning: could not switch herdr-app to the Sheepdog layout: %v\n", err)
	}
	fprintln(w, "Switch it with:")
	fprintln(w, `  herdr-app do layout.switch '{"name":"sheepdog"}'`)
	fprintln(w, "or press Ctrl+Shift+L in herdr-app.")
}

func setupPresence(p onboard.Presence) string {
	switch {
	case p.Answers:
		return "installed, running"
	case p.Installed:
		return "installed, not running"
	default:
		return "not found"
	}
}

func setupFound(found bool) string {
	if found {
		return "installed"
	}
	return "not installed"
}

func probeHasCommand(p onboard.Probe, name string) bool {
	if p.LookPath == nil {
		return false
	}
	_, err := p.LookPath(name)
	return err == nil
}

func setupLastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// initEnvPath is the env file init writes for configPath: .env next to
// the config file.
func initEnvPath(configPath string) string {
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	return filepath.Join(filepath.Dir(configPath), ".env")
}
