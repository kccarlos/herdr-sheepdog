package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

// pairAddrs lists this machine's interface addresses (tests replace it).
var pairAddrs = pair.SystemAddrs

func printPairUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog pair [flags]")
	fprintln(w, "")
	fprintln(w, "Prints a QR code for the iOS app (Settings → Scan pairing code): every LAN and")
	fprintln(w, "Tailscale address the daemon listens on, and its web.token, as")
	fprintln(w, "sheepdog://pair?u=<url>&u=<url>…&t=<token>. The app picks the first address")
	fprintln(w, "that answers. The token is in the code only; it is not printed. /debug (State")
	fprintln(w, "tab) shows the same code.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH     config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH   env file (default ./.env, else ~/.config/sheepdog/.env, as serve)")
	fprintln(w, "  --plain           no colour codes (the default when stdout is not a terminal)")
	fprintln(w, "  --invert          with --plain (or piped): for a light terminal background")
}

// runPair implements `sheepdog pair`.
func runPair(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printPairUsage(stderr) }
	configPath := fs.String("config", "", "config file")
	envFile := fs.String("env-file", "", "env file")
	invert := fs.Bool("invert", false, "for a light terminal")
	plain := fs.Bool("plain", false, "no colours")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if code, ok := loadServeEnvFile(*envFile, stderr); !ok {
		return code
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fprintf(stderr, "sheepdog pair: %v\n", err)
		return 1
	}
	sys, err := pairAddrs()
	if err != nil {
		fprintf(stderr, "sheepdog pair: %v\n", err)
		return 1
	}
	addrs, err := pair.Addresses(cfg.Web.Bind, cfg.Web.Port, sys)
	if err != nil {
		fprintf(stderr, "sheepdog pair: %v\n", err)
		return 1
	}
	link := pair.URL(addrs, cfg.Web.Token)
	tty := isTerminal(stdout) && !*plain
	var code string
	if tty {
		code, err = pair.ANSI(link)
	} else {
		code, err = pair.Terminal(link, *invert)
	}
	if err != nil {
		fprintf(stderr, "sheepdog pair: %v\n", err)
		return 1
	}
	fprintln(stdout, "Scan this in the iOS app (Settings → Scan pairing code):")
	fprintln(stdout, "")
	_, _ = io.WriteString(stdout, code)
	fprintln(stdout, "")
	if !tty {
		fprintln(stdout, "Does not scan? Try --invert (for a light background).")
		fprintln(stdout, "")
	}
	fprintln(stdout, "It holds these addresses (the app uses the first that answers):")
	for _, a := range addrs {
		fprintf(stdout, "  %-9s %s\n", a.Kind, a.URL)
	}
	if cfg.Web.Token == "" {
		fprintln(stdout, "and no token: web.token is not set, so anyone on these networks can use the daemon (sheepdog init sets one).")
	} else {
		fprintln(stdout, "and the web token (not printed). Treat the code like a password.")
	}
	return 0
}

// isTerminal reports whether w is a terminal (a character device).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
