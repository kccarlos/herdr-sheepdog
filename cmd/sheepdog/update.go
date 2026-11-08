package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/update"
)

// releaseRepo is the GitHub repository releases are published in.
const releaseRepo = update.Repo

const installScriptURL = "https://raw.githubusercontent.com/kccarlos/herdr-sheepdog/main/scripts/install.sh"

// releaseAPI is the GitHub API root (tests point it at a stub).
var releaseAPI = "https://api.github.com"

// releaseClient fetches the release (tests replace it).
var releaseClient = &http.Client{Timeout: 10 * time.Second}

// installMethod is how this binary was installed.
type installMethod struct {
	// Kind is "herdr plugin", "homebrew", "source" or "binary".
	Kind string
	// Path is the executable (symlinks resolved).
	Path string
}

// detectInstall classifies exe: a herdr plugin's bin/sheepdog (the
// plugin directory has herdr-plugin.toml), a Homebrew Cellar binary, a
// build in a source checkout (bin/ next to go.mod), or a plain binary.
// herdr's plugin install directory is not verified yet: a checkout with
// both files outside a herdr/plugins path is reported as "source", whose
// upgrade steps cover both cases.
func detectInstall(exe string) installMethod {
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	dir := filepath.Dir(exe)
	parent := filepath.Dir(dir)
	switch {
	case fileExists(filepath.Join(parent, "herdr-plugin.toml")) && fileExists(filepath.Join(parent, "go.mod")) && !strings.Contains(parent, "herdr"+string(filepath.Separator)+"plugins"):
		return installMethod{Kind: "source", Path: exe}
	case fileExists(filepath.Join(parent, "herdr-plugin.toml")):
		return installMethod{Kind: "herdr plugin", Path: exe}
	case strings.Contains(exe, "/Cellar/"):
		return installMethod{Kind: "homebrew", Path: exe}
	}
	return installMethod{Kind: "binary", Path: exe}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// upgradeSteps says how to upgrade for an install method.
func upgradeSteps(m installMethod) []string {
	if m.Kind == "source" {
		// A checkout with herdr-plugin.toml may also be what herdr
		// installed or linked as the plugin: give both ways.
		root := filepath.Dir(filepath.Dir(m.Path))
		return []string{"git -C " + root + " pull && make -C " + root + " build   (a checkout you cloned)", "herdr plugin update kccarlos.sheepdog   (if herdr installed it)"}
	}
	return []string{upgradeCommand(m)}
}

// updateDataDir is the data directory the update cache lives in ("" when
// the config does not load). Tests replace it.
var updateDataDir = func() string {
	if cfg, err := config.Load(""); err == nil {
		return cfg.DataDir
	}
	return ""
}

// release is a published release.
type release = update.Release

// errNoRelease: the repository is private or has no published release
// (GitHub answers 404 for both).
var errNoRelease = update.ErrNoRelease

// latestRelease is the newest v* release (ios-v* tags, drafts and
// prereleases are ignored).
// SHEEPDOG_RELEASE_API replaces the API root (a test release server).
func latestRelease(ctx context.Context) (release, error) {
	api := releaseAPI
	if v := os.Getenv("SHEEPDOG_RELEASE_API"); v != "" {
		api = v
	}
	return update.Latest(ctx, releaseClient, api)
}

// upgradeCommand is the one command that upgrades for m by hand.
func upgradeCommand(m installMethod) string {
	switch m.Kind {
	case "herdr plugin":
		return "herdr plugin update kccarlos.sheepdog"
	case "homebrew":
		return "brew upgrade sheepdog"
	case "source":
		root := filepath.Dir(filepath.Dir(m.Path))
		return "git -C " + root + " pull && make -C " + root + " build"
	}
	command := "curl -fsSL " + installScriptURL + " | sh"
	home, err := os.UserHomeDir()
	if err != nil {
		return command
	}
	if filepath.Clean(filepath.Dir(m.Path)) != filepath.Clean(filepath.Join(home, ".local", "bin")) {
		command = "SHEEPDOG_INSTALL_DIR=" + filepath.Dir(m.Path) + " " + command
	}
	return command
}

// runUpdate implements `sheepdog update`: it checks the latest release,
// installs it without asking (a source checkout gets the steps instead)
// and restarts a running daemon that is not on the installed version.
func runUpdate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fprintln(stderr, "usage: sheepdog update")
		fprintln(stderr, "")
		fprintln(stderr, "Checks the latest release on GitHub ("+releaseRepo+"), installs it the way this")
		fprintln(stderr, "sheepdog was installed (installer, Homebrew, herdr plugin; Sheepdog.app too when")
		fprintln(stderr, "Homebrew installed it) and restarts a running daemon. A source checkout gets the steps.")
	}
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	exe, _ := osExecutable()
	m := detectInstall(exe)
	fprintf(stdout, "sheepdog %s, installed as a %s (%s)\n", version, m.Kind, m.Path)
	r, err := latestRelease(ctx)
	switch {
	case errors.Is(err, errNoRelease):
		fprintf(stdout, "No published release found for %s: the repository is private or has no release yet.\n", releaseRepo)
		fprintln(stdout, "Nothing to update to; check again later.")
		return 0
	case err != nil:
		fprintf(stderr, "Could not check for a new release: %v\n", err)
		fprintln(stderr, "Check your internet connection and try again.")
		return 1
	}
	if dir := updateDataDir(); dir != "" {
		// Remember the answer for the notices (status, the pages).
		_ = update.WriteCache(dir, update.Cache{CheckedAt: time.Now().UTC(), Tag: r.TagName, URL: r.HTMLURL})
	}
	latest, okL := update.Semver(r.TagName)
	cur, okC := update.Semver(version)
	switch {
	case !okC:
		fprintf(stdout, "The latest release is %s; this is a development build (%s), which cannot be compared.\n", r.TagName, version)
	case okL && !update.Newer(latest, cur):
		fprintf(stdout, "sheepdog %s is up to date (latest release %s).\n", version, r.TagName)
		if m.Kind == "source" {
			return 0
		}
		return finishUpdate(ctx, version, stdout, stderr)
	case m.Kind != "source":
		fprintf(stdout, "Installing sheepdog %s (you have %s)\n", r.TagName, version)
		if err := update.Install(ctx, m.Kind, m.Path, r.TagName, stdout); err != nil {
			fprintf(stderr, "Could not update: %v\n", err)
			return 1
		}
		return finishUpdate(ctx, r.TagName, stdout, stderr)
	default:
		fprintf(stdout, "sheepdog %s is available (you have %s): %s\n", r.TagName, version, r.HTMLURL)
	}
	fprintln(stdout, "To upgrade:")
	for _, s := range upgradeSteps(m) {
		fprintf(stdout, "  %s\n", s)
	}
	return 0
}

// finishUpdate restarts a running daemon that is not on version want
// right away, then says which version runs, as the daemon (or, when none
// runs, the installed program) reports it.
func finishUpdate(ctx context.Context, want string, stdout, stderr io.Writer) int {
	l, code, ok := loadLifecycle(ctx, "restart", "", "", io.Discard, stderr)
	if !ok {
		return code
	}
	info, _, running := l.running(ctx)
	if !running {
		exe, _ := osExecutable()
		installed, _ := update.ProbeVersion(ctx, update.LaunchPath(os.Args[0], exe, exec.LookPath))
		fprintf(stdout, "sheepdog %s is installed; the daemon is not running (sheepdog start).\n", update.VersionOf(installed))
		return 0
	}
	if h, err := daemon.GetHello(ctx, info.URL); err != nil || h.Version != want {
		if code := runRestart(ctx, nil, stdout, stderr); code != 0 {
			return code
		}
		info, _, _ = l.running(ctx)
	}
	h, err := daemon.GetHello(ctx, info.URL)
	if err != nil {
		fprintf(stderr, "The daemon does not answer at %s: %v\n", info.URL, err)
		return 1
	}
	if h.Version != want {
		fprintf(stderr, "sheepdog %s is still running, not %s; see sheepdog doctor.\n", h.Version, want)
		return 1
	}
	fprintf(stdout, "sheepdog %s is running.\n", h.Version)
	return 0
}
