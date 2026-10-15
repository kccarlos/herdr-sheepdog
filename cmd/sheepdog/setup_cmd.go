package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
	l, code, ok := loadLifecycle(ctx, "setup", *configPath, *envFile, stdout, stderr)
	if !ok {
		return code
	}
	l.fromSetup = true
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
	if pageMissing(ctx, url) {
		fprintf(stderr, "The running sheepdog (version %s) is older and has no setup page.\n", info.Version)
		fprintln(stderr, "Restart it on this version (sheepdog restart), then run sheepdog setup again; or: sheepdog setup --terminal")
		return 1
	}
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
	if code := runInit(ctx, initArgs, stdin, stdout, stderr); code != 0 {
		return code
	}
	l, code, ok := loadLifecycle(ctx, "setup", configPath, envFile, stdout, stderr)
	if !ok {
		return code
	}
	if p := newPrompter(stdin, stdout, false); p.tty != nil && l.cfg.Web.IsLoopback() {
		fprintln(stdout, "")
		yes, err := p.yesNo("Let the iOS app reach sheepdog on your network (Wi-Fi, Tailscale), behind a long token?", false)
		if err == nil && yes {
			envPath := envFile
			if envPath == "" {
				envPath = initEnvPath(configPath)
			}
			created, err := onboard.SetPhoneAccess(envPath, l.cfg, true, false)
			if err != nil {
				fprintf(stderr, "sheepdog setup: %v\n", err)
				return 1
			}
			fprintf(stdout, "Phone access on: SHEEPDOG_WEB_BIND=%s in %s", onboard.PhoneBind, envPath)
			if created {
				fprint(stdout, " (and a new web token)")
			}
			fprintln(stdout, ". Scan the code from: sheepdog pair")
			if l, code, ok = loadLifecycle(ctx, "setup", configPath, envFile, io.Discard, stderr); !ok {
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

// initEnvPath is the env file init writes for configPath: .env next to
// the config file.
func initEnvPath(configPath string) string {
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	return filepath.Join(filepath.Dir(configPath), ".env")
}

func fprint(w io.Writer, s string) { _, _ = io.WriteString(w, s) }
