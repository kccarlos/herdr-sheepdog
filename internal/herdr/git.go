package herdr

import (
	"os"
	"path/filepath"
	"strings"
)

// GitCheckout is what GitInfo reads.
type GitCheckout struct {
	Root, Repo, Branch string
}

// GitInfo finds the git checkout dir is in (walking up to a .git) and
// its branch from HEAD, without running git. ok is false outside one.
func GitInfo(dir string) (GitCheckout, bool) {
	for d := dir; d != "" && d != string(filepath.Separator) && d != "."; d = filepath.Dir(d) {
		dotgit := filepath.Join(d, ".git")
		fi, err := os.Stat(dotgit)
		if err != nil {
			continue
		}
		g := GitCheckout{Root: d, Repo: filepath.Base(d)}
		gitdir := dotgit
		if !fi.IsDir() {
			// A linked worktree: ".git" is a file "gitdir: <path>".
			b, err := os.ReadFile(dotgit)
			if err != nil {
				return g, true
			}
			p := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
			if !filepath.IsAbs(p) {
				p = filepath.Join(d, p)
			}
			gitdir = p
		}
		if head, err := os.ReadFile(filepath.Join(gitdir, "HEAD")); err == nil {
			h := strings.TrimSpace(string(head))
			if ref, ok := strings.CutPrefix(h, "ref: refs/heads/"); ok {
				g.Branch = ref
			} else if len(h) >= 7 {
				g.Branch = h[:7]
			}
		}
		return g, true
	}
	return GitCheckout{}, false
}
