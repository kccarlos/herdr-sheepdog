package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

func printConfigUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog config <show|init> [flags]")
	fprintln(w, "")
	fprintln(w, "  show     print the effective config (file + env), secrets redacted")
	fprintln(w, "  init     write a commented config.toml template")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  show --config PATH   config file to load (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  init --path PATH     template destination (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  init --force         overwrite an existing file")
}

// runConfig dispatches the "config" command group.
func runConfig(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		printConfigUsage(stderr)
		return 2
	}
	switch args[0] {
	case "show":
		return runConfigShow(ctx, args[1:], stdout, stderr)
	case "init":
		return runConfigInit(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printConfigUsage(stdout)
		return 0
	default:
		fprintf(stderr, "sheepdog config: unknown subcommand %q\n\n", args[0])
		printConfigUsage(stderr)
		return 2
	}
}

// runConfigShow prints the fully-resolved config (file, then SHEEPDOG_*
// env overrides) via Config.String(), which never prints secret values
// (Gemini/LLM keys render as counts only).
func runConfigShow(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printConfigUsage(stderr) }
	configPath := fs.String("config", "", "config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fprintf(stderr, "sheepdog config show: %v\n", err)
		return 1
	}
	fprintln(stdout, cfg.String())
	return 0
}

// runConfigInit writes config.Template() to path (default
// config.DefaultPath()), refusing to overwrite an existing file unless
// --force is given.
func runConfigInit(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printConfigUsage(stderr) }
	path := fs.String("path", "", "template destination")
	force := fs.Bool("force", false, "overwrite an existing file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	dest := *path
	if dest == "" {
		dest = config.DefaultPath()
	}
	if !*force {
		if _, err := os.Stat(dest); err == nil {
			fprintf(stderr, "sheepdog config init: %s already exists; pass --force to overwrite\n", dest)
			return 1
		} else if !errors.Is(err, os.ErrNotExist) {
			fprintf(stderr, "sheepdog config init: %v\n", err)
			return 1
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		fprintf(stderr, "sheepdog config init: %v\n", err)
		return 1
	}
	if err := os.WriteFile(dest, []byte(config.Template()), 0o600); err != nil {
		fprintf(stderr, "sheepdog config init: %v\n", err)
		return 1
	}
	fprintf(stdout, "sheepdog config init: wrote %s\n", dest)
	return 0
}
