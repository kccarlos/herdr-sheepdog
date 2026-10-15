package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// releaseRepo is the GitHub repository releases are published in.
const releaseRepo = "kccarlos/herdr-sheepdog"

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
func upgradeSteps(m installMethod, releaseURL string) []string {
	switch m.Kind {
	case "herdr plugin":
		return []string{"herdr plugin update kccarlos.sheepdog", "sheepdog restart"}
	case "homebrew":
		return []string{"brew upgrade sheepdog", "sheepdog restart"}
	case "source":
		root := filepath.Dir(filepath.Dir(m.Path))
		return []string{"git -C " + root + " pull && make -C " + root + " build", "sheepdog restart"}
	}
	return []string{"download it from " + releaseURL + " and replace " + m.Path, "sheepdog restart"}
}

// release is the part of GitHub's release JSON update reads.
type release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// errNoRelease: the repository is private or has no published release
// (GitHub answers 404 for both).
var errNoRelease = errors.New("no published release")

func latestRelease(ctx context.Context) (release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseAPI+"/repos/"+releaseRepo+"/releases/latest", nil)
	if err != nil {
		return release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := releaseClient.Do(req)
	if err != nil {
		return release{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return release{}, errNoRelease
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return release{}, fmt.Errorf("GitHub refused the request (%s; its rate limit for anonymous requests is 60 an hour)", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return release{}, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var r release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return release{}, err
	}
	if r.TagName == "" {
		return release{}, errNoRelease
	}
	return r, nil
}

// semver parses "v1.2.3" (a suffix after - or + is ignored); ok is false
// for anything else ("dev", a commit).
func semver(v string) (out [3]int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// newer reports whether version a is newer than b.
func newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// runUpdate implements `sheepdog update`: it checks the latest release
// and says how to upgrade; it never changes anything.
func runUpdate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fprintln(stderr, "usage: sheepdog update")
		fprintln(stderr, "")
		fprintln(stderr, "Checks the latest release on GitHub ("+releaseRepo+") and prints how to")
		fprintln(stderr, "upgrade for the way this sheepdog was installed. It changes nothing itself.")
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
	latest, okL := semver(r.TagName)
	cur, okC := semver(version)
	switch {
	case !okC:
		fprintf(stdout, "The latest release is %s; this is a development build (%s), which cannot be compared.\n", r.TagName, version)
	case okL && !newer(latest, cur):
		fprintf(stdout, "sheepdog %s is up to date (latest release %s).\n", version, r.TagName)
		return 0
	default:
		fprintf(stdout, "sheepdog %s is available (you have %s): %s\n", r.TagName, version, r.HTMLURL)
	}
	fprintln(stdout, "To upgrade:")
	for _, s := range upgradeSteps(m, r.HTMLURL) {
		fprintf(stdout, "  %s\n", s)
	}
	return 0
}
