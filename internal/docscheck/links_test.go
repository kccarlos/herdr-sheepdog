package docscheck

import (
	"io/fs"
	"net/url"
	"os"
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

// TestMarkdownLinks fails for every relative link in a tracked Markdown file
// whose target file or directory does not exist, and, for the README and the
// user docs, whose #fragment names no heading. External URLs are skipped.
func TestMarkdownLinks(t *testing.T) {
	root := repoRoot(t)
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
	cache := map[string]map[string]bool{}
	bad := 0
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
			path, frag, _ := strings.Cut(tgt, "#")
			if i := strings.IndexByte(path, '?'); i >= 0 {
				path = path[:i]
			}
			if u, err := url.PathUnescape(path); err == nil {
				path = u
			}
			dest := filepath.ToSlash(filepath.Join(filepath.Dir(rel), path))
			if path == "" {
				dest = rel
			} else if strings.HasPrefix(path, "/") {
				dest = strings.TrimPrefix(path, "/")
			}
			if _, err := os.Stat(filepath.Join(root, dest)); err != nil {
				t.Errorf("%s: link %q: %s does not exist", rel, tgt, dest)
				bad++
				continue
			}
			userDoc := dest == "README.md" || (strings.HasPrefix(dest, "docs/") && !strings.HasPrefix(dest, "docs/dev/")) || dest == "CONFIG.md" || dest == "CONTRIBUTING.md"
			if frag == "" || !userDoc || !strings.HasSuffix(dest, ".md") {
				continue
			}
			set, ok := cache[dest]
			if !ok {
				b, err := os.ReadFile(filepath.Join(root, dest))
				if err != nil {
					t.Fatal(err)
				}
				set = anchors(string(b))
				cache[dest] = set
			}
			if !set[strings.ToLower(frag)] {
				t.Errorf("%s: link %q: no heading #%s in %s", rel, tgt, frag, dest)
				bad++
			}
		}
	}
	t.Logf("checked %d Markdown files, %d broken links", len(files), bad)
}
