package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// runHerdr dispatches the "herdr" command group.
func runHerdr(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		printHerdrUsage(stderr)
		return 2
	}
	switch args[0] {
	case "watch":
		return runHerdrWatch(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printHerdrUsage(stdout)
		return 0
	default:
		fprintf(stderr, "sheepdog herdr: unknown subcommand %q\n\n", args[0])
		printHerdrUsage(stderr)
		return 2
	}
}

func printHerdrUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog herdr watch [flags]")
	fprintln(w, "")
	fprintln(w, "Watches the herdr fleet, prints one JSON line per deduplicated, debounced")
	fprintln(w, "agent state transition on stdout, and persists the state table. Structured")
	fprintln(w, "herdr.* events go to stderr as JSON lines. Exits cleanly on SIGINT/SIGTERM.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH     config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --socket PATH     herdr socket (default from config)")
	fprintln(w, "  --state PATH      state table file (default from config)")
	fprintln(w, "  --debounce-ms N   how long `blocked` must persist (default from config)")
	fprintln(w, "  --once            print the persisted state table and exit")
}

// watchLine is what `herdr watch` prints per transition: the Transition
// plus the alias the state table gave the pane.
type watchLine struct {
	herdr.Transition
	Alias string `json:"alias,omitempty"`
}

func runHerdrWatch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("herdr watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printHerdrUsage(stderr) }
	configPath := fs.String("config", "", "config file")
	socket := fs.String("socket", "", "herdr socket path")
	statePath := fs.String("state", "", "state table file")
	debounceMS := fs.Int("debounce-ms", -1, "blocked debounce in milliseconds")
	once := fs.Bool("once", false, "print the persisted state table and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
		return 1
	}
	if *socket != "" {
		cfg.Herdr.Socket = *socket
	}
	if *statePath != "" {
		cfg.State.Path = *statePath
	}
	if *debounceMS >= 0 {
		cfg.DebounceMS = *debounceMS
	}

	store, err := state.Open(cfg.State.Path, state.Options{Aliases: cfg.Aliases, Warn: stderr})
	if err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
		return 1
	}

	if *once {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(store.Snapshot()); err != nil {
			fprintf(stderr, "sheepdog: %v\n", err)
			return 1
		}
		return 0
	}

	logger := herdr.NewJSONLLogger(stderr)
	client := herdr.NewClient(cfg.Herdr.Socket)
	client.Log = logger
	watcher := herdr.NewWatcher(client, herdr.WatcherConfig{
		Debounce:         time.Duration(cfg.DebounceMS) * time.Millisecond,
		SnapshotInterval: time.Duration(cfg.SnapshotIntervalS) * time.Second,
	}, logger)

	fprintf(stderr, "sheepdog herdr watch: %s\n", cfg)
	errc := make(chan error, 1)
	go func() { errc <- watcher.Run(ctx) }()

	enc := json.NewEncoder(stdout)
	for tr := range watcher.Transitions() {
		alias := ""
		if prev, ok := store.Get(tr.PaneID); ok {
			alias = prev.Alias
		}
		row, _, err := store.Apply(tr)
		if err != nil {
			fprintf(stderr, "sheepdog: persist: %v\n", err)
		}
		if row.Alias != "" {
			alias = row.Alias
		}
		if err := enc.Encode(watchLine{Transition: tr, Alias: alias}); err != nil {
			fprintf(stderr, "sheepdog: %v\n", err)
		}
	}
	if err := <-errc; err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
		return 1
	}
	return 0
}
