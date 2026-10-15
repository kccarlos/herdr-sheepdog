package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

func printUninstallUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog uninstall [--yes | --keep-data] [--config PATH]")
	fprintln(w, "")
	fprintln(w, "Stops the daemon, removes the login service and the ~/.local/bin/sheepdog")
	fprintln(w, "link when it points to this sheepdog, then asks before deleting your config and")
	fprintln(w, "keys (~/.config/sheepdog) and data (~/.local/share/sheepdog: transcripts, logs).")
	fprintln(w, "It never changes herdr's plugin registry: it prints the herdr command to finish.")
	fprintln(w, "")
	fprintln(w, "  --yes        delete config and data without asking")
	fprintln(w, "  --keep-data  keep config and data, ask nothing")
}

// runUninstall implements `sheepdog uninstall`.
func runUninstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printUninstallUsage(stderr) }
	configPath, envFile := lifecycleFlags(fs)
	yes := fs.Bool("yes", false, "delete without asking")
	keep := fs.Bool("keep-data", false, "keep config and data")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	if *yes && *keep {
		fprintln(stderr, "sheepdog uninstall: --yes or --keep-data, not both")
		return 2
	}
	l, code, ok := loadLifecycle(ctx, "uninstall", *configPath, *envFile, stdout, stderr)
	if !ok {
		return code
	}
	failed := false

	// 1. The daemon.
	if code := l.stop(ctx); code != 0 {
		fprintln(stderr, "sheepdog uninstall: the daemon is still running; stop it, then run this again.")
		return 1
	}

	// 2. The login service.
	if !l.serviceInstalled() {
		fprintln(stdout, "login service: not installed")
	} else {
		l.mgr.Out = stdout
		if err := l.mgr.Uninstall(ctx); err != nil {
			fprintf(stderr, "sheepdog uninstall: login service: %v\n", err)
			failed = true
		}
	}

	// 3. ~/.local/bin/sheepdog, when it is a link to this binary.
	exe, _ := osExecutable()
	if msg := removeLocalBinLink(exe); msg != "" {
		fprintln(stdout, msg)
	}

	// 4. Config and data, after asking. The default config directory
	// goes as a whole; a config given with --config elsewhere is removed
	// as a file (with the --env-file given), never its directory.
	type target struct {
		paths []string
		show  string
		what  string
		dir   bool
	}
	var targets []target
	if *configPath == "" || filepath.Clean(filepath.Dir(*configPath)) == filepath.Dir(config.DefaultPath()) {
		d := filepath.Dir(config.DefaultPath())
		targets = append(targets, target{paths: []string{d}, show: d, what: "your config and API keys", dir: true})
	} else {
		files := []string{*configPath}
		if *envFile != "" {
			files = append(files, *envFile)
		}
		targets = append(targets, target{paths: files, show: strings.Join(files, " and "), what: "your config and API keys"})
	}
	if err := checkDataDir(l.cfg.DataDir); err == nil {
		targets = append(targets, target{paths: []string{l.cfg.DataDir}, show: l.cfg.DataDir, what: "your data: transcripts, the agent table, logs", dir: true})
	} else if fileExists(l.cfg.DataDir) {
		fprintf(stdout, "left %s: %v\n", l.cfg.DataDir, err)
	}
	for _, d := range targets {
		var present []string
		for _, p := range d.paths {
			if fileExists(p) {
				present = append(present, p)
			}
		}
		if len(present) == 0 {
			continue
		}
		switch {
		case *keep:
			fprintf(stdout, "kept %s (%s)\n", d.show, d.what)
			continue
		case !*yes && !confirm(stdin, stdout, "Delete "+d.show+" ("+d.what+")?"):
			fprintf(stdout, "kept %s\n", d.show)
			continue
		}
		for _, p := range present {
			var err error
			if d.dir {
				err = os.RemoveAll(p)
			} else {
				err = os.Remove(p)
			}
			if err != nil {
				fprintf(stderr, "sheepdog uninstall: %v\n", err)
				failed = true
				continue
			}
			fprintf(stdout, "deleted %s\n", p)
		}
	}

	// 5. What only the user can do.
	fprintln(stdout, "")
	switch detectInstall(exe).Kind {
	case "homebrew":
		fprintln(stdout, "To remove the program: brew uninstall sheepdog")
	case "binary":
		fprintf(stdout, "To remove the program: rm %s\n", exe)
	}
	fprintln(stdout, "If you installed sheepdog as a herdr plugin, finish with:")
	fprintln(stdout, "  herdr plugin uninstall kccarlos.sheepdog")
	if failed {
		return 1
	}
	return 0
}

// localBin is ~/.local/bin/sheepdog (tests use the temp HOME).
func localBin() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "bin", "sheepdog")
}

// removeLocalBinLink removes ~/.local/bin/sheepdog when it is a symlink
// that resolves to exe, and says what it did. A regular file there (a
// `make install` copy) or a link to another sheepdog is left alone.
func removeLocalBinLink(exe string) string {
	link := localBin()
	st, err := os.Lstat(link)
	if err != nil {
		return ""
	}
	if st.Mode()&os.ModeSymlink == 0 {
		if same(link, exe) {
			return "left " + link + ": it is this program itself, not a link (remove it with: rm " + link + ")"
		}
		return ""
	}
	if !same(link, exe) {
		target, _ := os.Readlink(link)
		return "left " + link + ": it points to another sheepdog (" + target + ")"
	}
	if err := os.Remove(link); err != nil {
		return "could not remove " + link + ": " + err.Error()
	}
	return "removed the link " + link
}

// same reports whether a and b are the same file (symlinks followed).
func same(a, b string) bool {
	if b == "" {
		return false
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}
