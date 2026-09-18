package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"slices"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

func printPromptsUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog prompts <export|show> [flags]")
	fprintln(w, "")
	fprintln(w, "  export [dir]   write the embedded default prompt files to dir (default")
	fprintln(w, "                 config prompts.dir, ~/.config/sheepdog/prompts)")
	fprintln(w, "  show <name>    print the effective text of one prompt file and its source")
	fprintln(w, "                 (embedded or override); name is one of:")
	for _, n := range prompts.WorkspaceFiles {
		fprintf(w, "                   %s\n", n)
	}
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH   config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --force         export: overwrite files that already exist")
}

// runPrompts dispatches the "prompts" command group.
func runPrompts(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		printPromptsUsage(stderr)
		return 2
	}
	switch args[0] {
	case "export":
		return runPromptsExport(ctx, args[1:], stdout, stderr)
	case "show":
		return runPromptsShow(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printPromptsUsage(stdout)
		return 0
	default:
		fprintf(stderr, "sheepdog prompts: unknown subcommand %q\n\n", args[0])
		printPromptsUsage(stderr)
		return 2
	}
}

// runPromptsExport implements `sheepdog prompts export [dir]`.
func runPromptsExport(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prompts export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printPromptsUsage(stderr) }
	configPath := fs.String("config", "", "config file")
	force := fs.Bool("force", false, "overwrite existing files")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	dir := fs.Arg(0)
	if dir == "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			fprintf(stderr, "sheepdog prompts export: %v\n", err)
			return 1
		}
		dir = cfg.Prompts.Dir
		if dir == "" {
			dir = config.DefaultPromptsDir()
		}
	}
	written, _, err := prompts.Export(dir, *force)
	for _, p := range written {
		fprintf(stdout, "wrote %s\n", p)
	}
	if err != nil {
		fprintf(stderr, "sheepdog prompts export: %v\n", err)
		return 1
	}
	if len(written) == 0 {
		fprintln(stdout, "nothing to write (all files already exist; pass --force to overwrite)")
	}
	return 0
}

// runPromptsShow implements `sheepdog prompts show <name>`.
func runPromptsShow(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prompts show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printPromptsUsage(stderr) }
	configPath := fs.String("config", "", "config file")
	promptsDir := fs.String("prompts-dir", "", "override directory (default config prompts.dir)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	name := fs.Arg(0)
	if name == "" {
		fprintln(stderr, "sheepdog prompts show: needs a name, one of:")
		for _, n := range prompts.WorkspaceFiles {
			fprintf(stderr, "  %s\n", n)
		}
		return 2
	}
	if !slices.Contains(prompts.WorkspaceFiles, name) {
		fprintf(stderr, "sheepdog prompts show: unknown prompt file %q (want one of: %s)\n", name, strings.Join(prompts.WorkspaceFiles, ", "))
		return 2
	}
	dir := *promptsDir
	if dir == "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			fprintf(stderr, "sheepdog prompts show: %v\n", err)
			return 1
		}
		dir = cfg.Prompts.Dir
	}
	pl := prompts.NewLoader(dir, nil)
	text, source := pl.Load(name)
	fprintf(stdout, "# %s (source: %s)\n\n%s\n", name, source, text)
	return 0
}
