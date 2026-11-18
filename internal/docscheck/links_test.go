package docscheck

import (
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot walks up from the test's directory to the one holding go.mod.
func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

var (
	fenceRe      = regexp.MustCompile("(?s)(^|\n)(```|~~~).*?\n(```|~~~)[^\n]*")
	inlineCodeRe = regexp.MustCompile("`[^`\n]*`")
	mdLinkRe     = regexp.MustCompile(`!?\[[^\]\n]*\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	htmlRefRe    = regexp.MustCompile(`(?i)\b(?:href|src|srcset)="([^"]+)"`)
	headingRe    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*#*\s*$`)
)

// skipDirs are not scanned: vendored code, VCS data, and recorded run
// output whose links belong to the run, not to the docs.
func skipDir(rel string) bool {
	switch {
	case rel == ".git", rel == "node_modules", strings.HasSuffix(rel, "/node_modules"):
		return true
	case rel == "docs/dev/bench/results", rel == "docs/dev/research/live-resume-context-results":
		return true
	}
	return false
}

// slug is GitHub's heading anchor: lowercase, punctuation dropped, spaces to dashes.
func slug(h string) string {
	h = inlineCodeRe.ReplaceAllStringFunc(h, func(s string) string { return strings.Trim(s, "`") })
	h = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`).ReplaceAllString(h, "$1")
	var b strings.Builder
	for _, r := range strings.ToLower(h) {
		switch {
		case r == ' ' || r == '-':
			b.WriteRune('-')
		case r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r > 127 && r != ' ':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func anchors(md string) map[string]bool {
	out := map[string]bool{}
	for _, m := range headingRe.FindAllStringSubmatch(fenceRe.ReplaceAllString(md, ""), -1) {
		out[slug(m[1])] = true
	}
	return out
}

type sparseSet struct {
	files map[string]bool
	dirs  map[string]bool
}

func loadSparseExcluded(root string) sparseSet {
	out := sparseSet{files: map[string]bool{}, dirs: map[string]bool{}}
	enabled, err := exec.Command("git", "-C", root, "config", "--bool", "--get", "core.sparseCheckout").Output()
	if err != nil || strings.TrimSpace(string(enabled)) != "true" {
		return out
	}
	listed, err := exec.Command("git", "-C", root, "ls-files", "-t").Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(listed), "\n") {
		if !strings.HasPrefix(line, "S ") {
			continue
		}
		p := strings.TrimPrefix(line, "S ")
		out.files[p] = true
		for dir := path.Dir(p); dir != "." && dir != "/"; dir = path.Dir(dir) {
			out.dirs[dir] = true
		}
	}
	return out
}

func (s sparseSet) has(dest string) bool {
	clean := strings.TrimSuffix(filepath.ToSlash(dest), "/")
	return s.files[clean] || s.dirs[clean]
}

// TestMarkdownLinks fails for every relative link in a tracked Markdown file
// whose target file or directory does not exist, and, for the README and the
// user docs, whose #fragment names no heading. External URLs are skipped.
func TestMarkdownLinks(t *testing.T) {
	root := repoRoot(t)
	files, errs := checkMarkdownLinks(t, root)
	for _, e := range errs {
		t.Error(e)
	}
	t.Logf("checked %d Markdown files, %d broken links", files, len(errs))
}

func checkMarkdownLinks(t testing.TB, root string) (int, []string) {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skipDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		// CHANGELOG.md is generated from commit subjects (scripts/changelog.sh).
		if strings.HasSuffix(rel, ".md") && rel != "CHANGELOG.md" {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sparse := loadSparseExcluded(root)
	cache := map[string]map[string]bool{}
	var errs []string
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		text := inlineCodeRe.ReplaceAllString(fenceRe.ReplaceAllString(string(raw), "\n"), "")
		var targets []string
		for _, m := range mdLinkRe.FindAllStringSubmatch(text, -1) {
			targets = append(targets, m[1])
		}
		for _, m := range htmlRefRe.FindAllStringSubmatch(text, -1) {
			targets = append(targets, m[1])
		}
		for _, tgt := range targets {
			if strings.Contains(tgt, "://") || strings.HasPrefix(tgt, "mailto:") || strings.HasPrefix(tgt, "tel:") || strings.HasPrefix(tgt, "data:") {
				continue
			}
			linkPath, frag, _ := strings.Cut(tgt, "#")
			if i := strings.IndexByte(linkPath, '?'); i >= 0 {
				linkPath = linkPath[:i]
			}
			if u, err := url.PathUnescape(linkPath); err == nil {
				linkPath = u
			}
			dest := filepath.ToSlash(filepath.Join(filepath.Dir(rel), linkPath))
			if linkPath == "" {
				dest = rel
			} else if strings.HasPrefix(linkPath, "/") {
				dest = strings.TrimPrefix(linkPath, "/")
			}
			onDisk := true
			if _, err := os.Stat(filepath.Join(root, dest)); err != nil {
				onDisk = false
				if !sparse.has(dest) {
					errs = append(errs, rel+": link "+tgt+": "+dest+" does not exist")
					continue
				}
			}
			userDoc := dest == "README.md" || (strings.HasPrefix(dest, "docs/") && !strings.HasPrefix(dest, "docs/dev/")) || dest == "CONFIG.md" || dest == "CONTRIBUTING.md" || dest == "ios/README.md"
			if frag == "" || !userDoc || !strings.HasSuffix(dest, ".md") {
				continue
			}
			set, ok := cache[dest]
			if !ok {
				var b []byte
				if onDisk {
					b, err = os.ReadFile(filepath.Join(root, dest))
				} else {
					b, err = exec.Command("git", "-C", root, "show", ":"+strings.TrimSuffix(dest, "/")).Output()
				}
				if err != nil {
					t.Fatal(err)
				}
				set = anchors(string(b))
				cache[dest] = set
			}
			if !set[strings.ToLower(frag)] {
				errs = append(errs, rel+": link "+tgt+": no heading #"+frag+" in "+dest)
			}
		}
	}
	return len(files), errs
}

func TestMarkdownLinksSparseCheckout(t *testing.T) {
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	tmp := t.TempDir()
	main := filepath.Join(tmp, "main")
	wt := filepath.Join(tmp, "wt")
	if err := os.MkdirAll(filepath.Join(main, "docs/dev/bench/results/run1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "docs/dev/bench/results/run1/report.md"), []byte("# Run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "docs/guide.md"), []byte("# Guide\nSee [bench](dev/bench/results/run1/) and [report](dev/bench/results/run1/report.md).\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "docs/other.md"), []byte("# Other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(main, "init", "-b", "main")
	runGit(main, "config", "user.email", "test@example.com")
	runGit(main, "config", "user.name", "Test")
	runGit(main, "add", ".")
	runGit(main, "commit", "--no-gpg-sign", "-m", "init")
	runGit(main, "worktree", "add", "--no-checkout", "-b", "wt-branch", wt, "main")
	runGit(wt, "sparse-checkout", "set", "--no-cone", "/*", "!/docs/dev/bench")
	runGit(wt, "checkout")

	if _, err := os.Stat(filepath.Join(wt, "docs/dev/bench")); !os.IsNotExist(err) {
		t.Fatalf("expected docs/dev/bench to be absent in sparse worktree")
	}
	_, errs := checkMarkdownLinks(t, wt)
	if len(errs) != 0 {
		t.Fatalf("expected no broken links for sparse-excluded targets, got: %v", errs)
	}

	// A genuinely broken link still fails even with sparse-checkout enabled.
	if err := os.WriteFile(filepath.Join(wt, "docs/guide.md"), []byte("# Guide\nSee [missing](dev/bench/missing.md) and [other](other.md).\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(wt, "docs/other.md")); err != nil {
		t.Fatal(err)
	}
	_, errs = checkMarkdownLinks(t, wt)
	if len(errs) != 2 {
		t.Fatalf("expected 2 broken links (nonexistent sparse target + deleted non-excluded file), got: %v", errs)
	}
}
