package web

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// gitT runs git in dir with no user or system config.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func writeT(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// trackIn tracks pane with its working directory at cwd.
func trackIn(t *testing.T, h *harness, pane, cwd string) {
	t.Helper()
	trackWorking(t, h, pane, "a"+strings.NewReplacer(":", "").Replace(pane), "hi")
	if _, err := h.store.Update(pane, func(a *state.Agent) { a.Cwd = cwd }); err != nil {
		t.Fatal(err)
	}
}

func changesHarness(t *testing.T) (*harness, string) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gitT(t, dir, "init", "-q")
	writeT(t, filepath.Join(dir, "a.go"), "package a\n\nfunc A() {}\n")
	writeT(t, filepath.Join(dir, "gone.txt"), "bye\n")
	writeT(t, filepath.Join(dir, "old.txt"), strings.Repeat("same line\n", 20))
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-qm", "init")
	gitT(t, dir, "switch", "-qc", "fix-login")
	// Modified, deleted, renamed, untracked in a subfolder.
	writeT(t, filepath.Join(dir, "a.go"), "package a\n\nfunc A() { b() }\n\nfunc b() {}\n")
	_ = os.Remove(filepath.Join(dir, "gone.txt"))
	gitT(t, dir, "mv", "old.txt", "new.txt")
	writeT(t, filepath.Join(dir, "sub", "note.md"), "one\ntwo\n")
	h := newHarness(t, harnessOpts{})
	trackIn(t, h, "w1:p2", filepath.Join(dir, "sub"))
	return h, dir
}

// The list has every changed file with its counts and the branch, from
// a working directory below the repository's top level.
func TestChangesList(t *testing.T) {
	h, dir := changesHarness(t)
	var got ChangesResponse
	if code := h.getJSON(windowPath("w1:p2", "changes"), &got); code != http.StatusOK {
		t.Fatalf("changes = %d", code)
	}
	root, _ := filepath.EvalSymlinks(dir)
	if !got.Repo || got.Branch != "fix-login" || got.Root != root {
		t.Fatalf("got %+v (root %q)", got, root)
	}
	byPath := map[string]ChangedFile{}
	for _, f := range got.Files {
		byPath[f.Path] = f
	}
	want := map[string]ChangedFile{
		"a.go":        {Path: "a.go", Status: "modified", Added: 3, Deleted: 1},
		"gone.txt":    {Path: "gone.txt", Status: "deleted", Deleted: 1},
		"new.txt":     {Path: "new.txt", OldPath: "old.txt", Status: "renamed"},
		"sub/note.md": {Path: "sub/note.md", Status: "untracked", Added: 2},
	}
	if len(byPath) != len(want) {
		t.Fatalf("files = %+v", got.Files)
	}
	for p, w := range want {
		if byPath[p] != w {
			t.Errorf("%s = %+v, want %+v", p, byPath[p], w)
		}
	}
	if got.Added != 5 || got.Deleted != 2 {
		t.Fatalf("totals = +%d -%d", got.Added, got.Deleted)
	}
}

// A listed file's diff is served; anything else is 404, so a client
// cannot read outside the repository.
func TestChangesDiff(t *testing.T) {
	h, dir := changesHarness(t)
	var d DiffResponse
	if code := h.getJSON(windowPath("w1:p2", "changes/a.go"), &d); code != http.StatusOK {
		t.Fatalf("diff = %d", code)
	}
	if d.Status != "modified" || !strings.Contains(d.Diff, "+func A() { b() }") || !strings.Contains(d.Diff, "-func A() {}") {
		t.Fatalf("diff = %+v", d)
	}
	var u DiffResponse
	if code := h.getJSON(windowPath("w1:p2", "changes/sub/note.md"), &u); code != http.StatusOK || !strings.Contains(u.Diff, "+two") {
		t.Fatalf("untracked diff = %d %+v", code, u)
	}
	var r DiffResponse
	if code := h.getJSON(windowPath("w1:p2", "changes/new.txt"), &r); code != http.StatusOK || r.OldPath != "old.txt" {
		t.Fatalf("rename diff = %d %+v", code, r)
	}

	// A symlink out of the repository is listed (untracked) but never read.
	outside := t.TempDir()
	writeT(t, filepath.Join(outside, "secret"), "secret\n")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	var l DiffResponse
	if code := h.getJSON(windowPath("w1:p2", "changes/link"), &l); code != http.StatusOK || strings.Contains(l.Diff, "+secret") || !l.Binary {
		t.Fatalf("symlink = %d %+v", code, l)
	}
	for _, p := range []string{"..%2F..%2Fetc%2Fpasswd", url.PathEscape("/etc/passwd"), "old.txt", "README", "a.go%2F..%2F..%2Fx"} {
		if code := h.getJSON(windowPath("w1:p2", "changes/")+p, nil); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", p, code)
		}
	}
	if code := h.getJSON(windowPath("w9:p9", "changes"), nil); code != http.StatusNotFound {
		t.Errorf("untracked pane = %d", code)
	}
}

// Not a repository, a missing folder, a repository with no commit yet and
// a clean one.
func TestChangesEdges(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	h := newHarness(t, harnessOpts{})

	plain := t.TempDir()
	trackIn(t, h, "w1:p1", plain)
	var got ChangesResponse
	if code := h.getJSON(windowPath("w1:p1", "changes"), &got); code != http.StatusOK || got.Repo || got.Reason != "not_repo" || got.Files == nil {
		t.Fatalf("plain = %d %+v", code, got)
	}

	trackIn(t, h, "w1:p3", filepath.Join(plain, "missing"))
	got = ChangesResponse{}
	if h.getJSON(windowPath("w1:p3", "changes"), &got); got.Reason != "no_cwd" {
		t.Fatalf("missing = %+v", got)
	}

	fresh := t.TempDir()
	gitT(t, fresh, "init", "-q")
	writeT(t, filepath.Join(fresh, "x.txt"), "x\n")
	gitT(t, fresh, "add", "x.txt")
	trackIn(t, h, "w1:p4", fresh)
	got = ChangesResponse{}
	if code := h.getJSON(windowPath("w1:p4", "changes"), &got); code != http.StatusOK || !got.Repo || len(got.Files) != 1 || got.Files[0].Status != "added" || got.Files[0].Added != 1 {
		t.Fatalf("fresh = %d %+v", code, got)
	}
	var d DiffResponse
	if code := h.getJSON(windowPath("w1:p4", "changes/x.txt"), &d); code != http.StatusOK || !strings.Contains(d.Diff, "+x") {
		t.Fatalf("fresh diff = %d %+v", code, d)
	}

	gitT(t, fresh, "-c", "user.name=T", "-c", "user.email=t@example.invalid", "commit", "-qm", "x")
	got = ChangesResponse{}
	if code := h.getJSON(windowPath("w1:p4", "changes"), &got); code != http.StatusOK || len(got.Files) != 0 || got.Files == nil {
		t.Fatalf("clean = %d %+v", code, got)
	}
	// The view never leaves a lock behind for the agent's own git.
	if _, err := os.Stat(filepath.Join(fresh, ".git", "index.lock")); !os.IsNotExist(err) {
		t.Fatalf("index.lock: %v", err)
	}
}

func TestParseStatusV2(t *testing.T) {
	out := "# branch.oid abc\x00# branch.head (detached)\x001 .M N... 100644 100644 100644 a a a.go\x002 R. N... 100644 100644 100644 a a R100 new name.txt\x00old name.txt\x00u UU N... 1 1 1 1 a b c both.go\x00? new dir/x\x001 .M N..."
	files, branch := parseStatusV2([]byte(out), true)
	if branch != "" || len(files) != 4 {
		t.Fatalf("files = %+v, branch %q", files, branch)
	}
	if files[1] != (ChangedFile{Path: "new name.txt", OldPath: "old name.txt", Status: "renamed"}) || files[2].Status != "conflicted" || files[3].Path != "new dir/x" {
		t.Fatalf("files = %+v", files)
	}
}
