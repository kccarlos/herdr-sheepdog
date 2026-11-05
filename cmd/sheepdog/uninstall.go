package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/panelsync"
)

func printUninstallUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog uninstall [--yes | --keep-data] [--dry-run] [--config PATH]")
	fprintln(w, "")
	fprintln(w, "Stops the daemon, removes the login service, the ~/.local/bin/sheepdog link")
	fprintln(w, "when it points to this sheepdog, and unchanged herdr-app panel/layout files")
	fprintln(w, "(plus any opt-in keys.json shortcut setup added), then asks before deleting")
	fprintln(w, "your config and keys (~/.config/sheepdog) and data (~/.local/share/sheepdog).")
	fprintln(w, "It never changes herdr-tui's plugin registry: it prints the herdr-tui command to finish.")
	fprintln(w, "")
	fprintln(w, "  --yes        delete config and data without asking")
	fprintln(w, "  --keep-data  keep config and data, ask nothing")
	fprintln(w, "  --dry-run    print what uninstall would remove; change nothing")
}

// runUninstall implements `sheepdog uninstall`.
func runUninstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printUninstallUsage(stderr) }
	configPath, envFile := lifecycleFlags(fs)
	yes := fs.Bool("yes", false, "delete without asking")
	keep := fs.Bool("keep-data", false, "keep config and data")
	dryRun := fs.Bool("dry-run", false, "print what would change, do nothing")
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
	if *dryRun {
		if _, _, running := l.running(ctx); running {
			fprintln(stdout, "would stop the running sheepdog daemon")
		} else {
			fprintln(stdout, "sheepdog is not running")
		}
	} else {
		if code := l.stop(ctx); code != 0 {
			fprintln(stderr, "sheepdog uninstall: the daemon is still running; stop it, then run this again.")
			return 1
		}
	}

	// 2. The login service.
	if !l.serviceInstalled() {
		fprintln(stdout, "login service: not installed")
	} else if *dryRun {
		fprintf(stdout, "would stop and remove login service (%s)\n", l.mgr.Spec.UnitPath())
	} else {
		l.mgr.Out = stdout
		if err := l.mgr.Uninstall(ctx); err != nil {
			fprintf(stderr, "sheepdog uninstall: login service: %v\n", err)
			failed = true
		}
	}

	// 3. ~/.local/bin/sheepdog, when it is a link to this binary.
	exe, _ := osExecutable()
	if msg := removeOrPlanLocalBinLink(exe, *dryRun); msg != "" {
		fprintln(stdout, msg)
	}

	// 4. herdr-app panel, layout, opt-in shortcut and active layout.
	panelPlan, err := panelsync.Uninstall(panelHome(), panelSource(), *dryRun)
	if err != nil {
		fprintf(stderr, "sheepdog uninstall: herdr-app panel: %v\n", err)
		failed = true
	}
	for _, line := range panelPlan.Actions {
		fprintln(stdout, line)
	}
	for _, line := range panelPlan.Kept {
		fprintln(stdout, line)
	}

	// 5. Config and data, after asking. The default config directory
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
		if *dryRun {
			if *keep {
				fprintf(stdout, "would keep %s (%s)\n", d.show, d.what)
			} else {
				fprintf(stdout, "would delete %s (%s)\n", d.show, d.what)
			}
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

	// 6. What only the user can do.
	fprintln(stdout, "")
	if panelPlan.RestoredLayout != "" && !*dryRun {
		fprintln(stdout, "If herdr-app is open on the Sheepdog layout, switch the live window:")
		fprintf(stdout, "  herdr-app do layout.switch '{\"name\":%q}'\n", panelPlan.RestoredLayout)
	}
	switch detectInstall(exe).Kind {
	case "homebrew":
		fprintln(stdout, "To remove the program: brew uninstall sheepdog")
	case "binary":
		fprintf(stdout, "To remove the program: rm %s\n", exe)
	}
	fprintln(stdout, "If you installed sheepdog as a herdr-tui plugin, finish with:")
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
	return removeOrPlanLocalBinLink(exe, false)
}

func removeOrPlanLocalBinLink(exe string, dryRun bool) string {
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
	if dryRun {
		return "would remove the link " + link
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
