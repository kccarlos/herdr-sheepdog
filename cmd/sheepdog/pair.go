package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

func printPairUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog pair [flags]")
	fprintln(w, "")
	fprintln(w, "Asks the running daemon for a new pairing code and prints it as a QR code. Scan it")
	fprintln(w, "in the iOS app, or with any phone's camera to use sheepdog in the browser. The code")
	fprintln(w, "works once and expires after 10 minutes; showing a new one (here or on the setup")
	fprintln(w, "page) replaces it. It holds the computer's addresses, its certificate's")
	fprintln(w, "fingerprint and the one-time code.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH     config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH   env file (default ./.env, else ~/.config/sheepdog/.env, as serve)")
	fprintln(w, "  --plain           no colour codes (the default when stdout is not a terminal)")
	fprintln(w, "  --compact         fewer lines around the code, to fit a small window or popup")
	fprintln(w, "  --invert          with --plain (or piped): for a light terminal background")
}

// pairResponse is the daemon's GET /api/pair.
type pairResponse struct {
	Link      string         `json:"link"`
	Expires   *time.Time     `json:"expires"`
	Addresses []pair.Address `json:"addresses"`
	Error     string         `json:"error"`
}

// runPair implements `sheepdog pair`: the daemon mints the one-time code
// (only it can check it later), so this asks the running daemon on
// loopback for a new one.
func runPair(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
	var p pairResponse
	if err := getJSON(ctx, daemon.LocalURL(cfg.Web.Bind, cfg.Web.Port), "/api/pair?new=1", &p); err != nil {
		fprintf(stderr, "sheepdog pair: the daemon is not answering on port %d (%v): start it with sheepdog start\n", cfg.Web.Port, err)
		return 1
	}
	if p.Link == "" {
		fprintf(stderr, "sheepdog pair: %s\n", p.Error)
		return 1
	}
	tty := isTerminal(stdout) && !*plain
	var code string
	if tty {
		code, err = pair.ANSI(p.Link)
	} else {
		code, err = pair.Terminal(p.Link, *invert)
	}
	if err != nil {
		fprintf(stderr, "sheepdog pair: %v\n", err)
		return 1
	}
	expires := ""
	if p.Expires != nil {
		expires = p.Expires.Local().Format("15:04")
	}
	if *compact {
		return printPairCompact(stdout, code, p.Addresses, expires)
	}
	fprintln(stdout, "Scan this in the Sheepdog app, or with your phone's camera:")
	fprintln(stdout, "")
	_, _ = io.WriteString(stdout, code)
	fprintln(stdout, "")
	if !tty {
		fprintln(stdout, "Does not scan? Try --invert (for a light background).")
		fprintln(stdout, "")
	}
	fprintln(stdout, pair.Explain(p.Addresses))
	fprintln(stdout, "The code holds these addresses (the app keeps the first that answers):")
	for _, a := range p.Addresses {
		fprintf(stdout, "  %-10s %s\n", a.Kind, pair.HostPort(a.URL))
	}
	fprintf(stdout, "It works once and expires at %s. Every device already paired is told when a new one pairs.\n", expires)
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
func printPairCompact(w io.Writer, code string, addrs []pair.Address, expires string) int {
	fprintln(w, "Scan in the Sheepdog app or with your phone's camera:")
	_, _ = io.WriteString(w, code)
	fprintln(w, wrapText(pair.Explain(addrs), 68))
	for _, a := range addrs {
		fprintln(w, pair.HostPort(a.URL))
	}
	fprintf(w, "Works once, until %s.\n", expires)
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
