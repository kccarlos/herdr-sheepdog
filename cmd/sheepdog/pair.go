package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"

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
	fprintln(w, "  --compact         fewer lines around the code, to fit a small window or popup")
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
	compact := fs.Bool("compact", false, "fewer lines around the code")
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
	if *compact {
		return printPairCompact(stdout, code, addrs, cfg.Web.Token != "")
	}
	fprintln(stdout, "Scan this in the iOS app (Settings → Scan pairing code):")
	fprintln(stdout, "")
	_, _ = io.WriteString(stdout, code)
	fprintln(stdout, "")
	if !tty {
		fprintln(stdout, "Does not scan? Try --invert (for a light background).")
		fprintln(stdout, "")
	}
	fprintln(stdout, pair.Explain(addrs))
	fprintln(stdout, "The code holds these addresses (the app keeps the first that answers):")
	for _, a := range addrs {
		fprintf(stdout, "  %-9s %s\n", a.Kind, a.URL)
	}
	if cfg.Web.Token == "" {
		fprintln(stdout, "and no token: web.token is not set, so anyone on these networks can use the daemon (sheepdog setup sets one).")
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

// printPairCompact is the code with a one-line title and
// the addresses, one per line.
func printPairCompact(w io.Writer, code string, addrs []pair.Address, hasToken bool) int {
	fprintln(w, "Scan in the iOS app (Settings → Scan pairing code):")
	_, _ = io.WriteString(w, code)
	fprintln(w, wrapText(pair.Explain(addrs), 68))
	for _, a := range addrs {
		fprintln(w, a.URL)
	}
	if hasToken {
		fprintln(w, "The code holds the web token (not printed): treat it like a password.")
	} else {
		fprintln(w, "No web.token is set: anyone on these networks can use the daemon (sheepdog setup sets one).")
	}
	return 0
}

// wrapText breaks s at spaces into lines of at most width runes.
func wrapText(s string, width int) string {
	var out, line strings.Builder
	for _, w := range strings.Fields(s) {
		if line.Len() > 0 && line.Len()+1+len(w) > width {
			out.WriteString(line.String() + "\n")
			line.Reset()
		}
		if line.Len() > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(w)
	}
	return out.String() + line.String()
}
