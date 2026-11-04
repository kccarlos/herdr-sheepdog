package files

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// A filesystem that folds case the Unicode way (APFS) opens the real file
// for a name spelled with U+017F (long s) or U+212A (Kelvin sign), so the
// rule folds the same way.
func TestSensitiveFolds(t *testing.T) {
	for name, want := range map[string]bool{
		"id_r\u017Fa": true, ".\u017Fsh": true, ".\u212Aube": true, "TLS.\u212AEY": true,
		"\u017Fecrets.json": true, ".ENV.LOCAL": true, "\u212Aey.pem": true, "a.p\u212Aey": false,
		"report.md": false, "\u017Fhop": false,
	} {
		if got := Sensitive(name); got != want {
			t.Errorf("Sensitive(%q) = %v, want %v", name, got, want)
		}
	}
	for p, want := range map[string]bool{
		"docs/report.md": false, "docs/.\u017Fsh/x": true, "a/b/id_r\u017Fa": true, "": false,
	} {
		if got := SensitivePath(p); got != want {
			t.Errorf("SensitivePath(%q) = %v, want %v", p, got, want)
		}
	}
}

// Every way in: read, list, search and a link's target refuse the folded
// spelling, and the list counts it as hidden.
func TestFoldedNamesRefused(t *testing.T) {
	r, _ := build(t, tree{
		files: map[string]string{
			"keys/id_r\u017Fa": "private", ".\u017Fsh/config": "x", "docs/report.md": "r",
		},
		links: map[string]string{"notes": ".\u017Fsh/config", "k": "keys/id_r\u017Fa"},
	})
	for _, p := range []string{"keys/id_r\u017Fa", ".\u017Fsh/config", "notes", "k"} {
		if _, err := r.Read(p); !errors.Is(err, ErrSensitive) {
			t.Errorf("Read(%q) = %v, want ErrSensitive", p, err)
		}
	}
	if _, err := r.List(context.Background(), ".\u017Fsh"); !errors.Is(err, ErrSensitive) {
		t.Errorf("List(.\u017Fsh) = %v", err)
	}
	l, err := r.List(context.Background(), "keys")
	if err != nil || len(l.Entries) != 0 || l.Hidden != 1 {
		t.Errorf("List(keys) = %v hidden %d %v", names(l), l.Hidden, err)
	}
	s, err := r.Search(context.Background(), "", "r")
	if err != nil || slices.ContainsFunc(names(s), func(p string) bool { return strings.Contains(p, "\u017F") }) || s.Hidden == 0 {
		t.Errorf("Search = %v hidden %d %v", names(s), s.Hidden, err)
	}
}

// node_modules is skipped under the same fold.
func TestSearchSkipsFoldedNodeModules(t *testing.T) {
	r, _ := build(t, tree{files: map[string]string{
		"NODE_MODULES/report/a.js": "", "node_module\u017F/report/b.js": "", "docs/report.md": "",
	}})
	l, err := r.Search(context.Background(), "", "report")
	if err != nil || !slices.Equal(names(l), []string{"docs/report.md"}) {
		t.Errorf("Search = %v %v", names(l), err)
	}
}

// A root inside a sensitive folder is refused, whatever the spelling.
func TestOpenRefusesSensitiveRoot(t *testing.T) {
	base := t.TempDir()
	for _, p := range []string{".git", ".ssh/keys", ".\u017Fsh", "x/.env.d/y"} {
		dir := filepath.Join(base, p)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); !errors.Is(err, ErrSensitive) {
			t.Errorf("Open(%s) = %v, want ErrSensitive", p, err)
		}
	}
	// A link to a sensitive folder is the folder.
	link(t, filepath.Join(base, ".git"), filepath.Join(base, "work"))
	if _, err := Open(filepath.Join(base, "work")); !errors.Is(err, ErrSensitive) {
		t.Errorf("Open(link to .git) = %v", err)
	}
}

// Whatever the system says, an error carries no host path: known ones map
// to a fixed reason, the rest to ErrFailed.
func TestOSErrCarriesNoPath(t *testing.T) {
	const host = "/home/someone/private/project"
	for _, tc := range []struct {
		errno syscall.Errno
		want  error
	}{
		{syscall.ENOENT, ErrNotFound}, {syscall.ENOTDIR, ErrNotFound},
		{syscall.EACCES, ErrDenied}, {syscall.EPERM, ErrDenied}, {syscall.ELOOP, ErrDenied},
		{syscall.ENXIO, ErrNotFile}, {syscall.EOPNOTSUPP, ErrNotFile}, {syscall.ENODEV, ErrNotFile},
		{syscall.EIO, ErrFailed}, {syscall.EMFILE, ErrFailed}, {syscall.ENFILE, ErrFailed}, {syscall.EINVAL, ErrFailed},
	} {
		err := osErr(&fs.PathError{Op: "open", Path: host, Err: tc.errno})
		if !errors.Is(err, tc.want) || strings.Contains(err.Error(), host) || strings.Contains(err.Error(), tc.errno.Error()) {
			t.Errorf("osErr(%v) = %v, want %v with no path", tc.errno, err, tc.want)
		}
	}
	if err := osErr(fmt.Errorf("odd failure at %s", host)); !errors.Is(err, ErrFailed) || strings.Contains(err.Error(), host) {
		t.Errorf("osErr(odd) = %v", err)
	}
}

// A socket, direct or behind a link, is not a file: 403 not_file, and the
// message names no path.
func TestSocketIsNotFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "sk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Skipf("unix socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	link(t, "s", filepath.Join(dir, "via"))
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	for _, p := range []string{"s", "via"} {
		_, err := r.Read(p)
		if !errors.Is(err, ErrNotFile) || strings.Contains(err.Error(), dir) {
			t.Errorf("Read(%s) = %v, want ErrNotFile with no path", p, err)
		}
	}
}

// 20,000 empty folders are 20,000 entries seen: the search stops at the
// limit instead of opening every one.
func TestSearchStopsInTheQueue(t *testing.T) {
	r, dir := build(t, tree{files: map[string]string{"a.txt": ""}})
	for i := range SearchSeenMax {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("d%05d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l, err := r.Search(context.Background(), "", "zzz")
	if err != nil || !l.Truncated {
		t.Errorf("Search = truncated %v, %v", l.Truncated, err)
	}
}

// A list that runs out of time answers what it has, truncated.
func TestListStopsOnContext(t *testing.T) {
	r, _ := build(t, tree{files: map[string]string{"a.txt": "", "b.txt": ""}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l, err := r.List(ctx, "")
	if err != nil || !l.Truncated || len(l.Entries) == 2 {
		t.Errorf("cancelled list = %v truncated %v, %v", names(l), l.Truncated, err)
	}
}
