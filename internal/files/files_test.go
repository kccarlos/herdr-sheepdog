package files

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const pngHead = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

// tree is a test folder: files by slash path, and links by path to target.
type tree struct {
	files map[string]string
	links map[string]string
}

// build makes a root folder "shop" beside a folder "outside" holding a
// secret, from t's files and links.
func build(t *testing.T, tr tree) (*Root, string) {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "shop")
	write(t, filepath.Join(base, "outside", "secret.txt"), "secret")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for p, s := range tr.files {
		write(t, filepath.Join(dir, filepath.FromSlash(p)), s)
	}
	for p, target := range tr.links {
		link(t, target, filepath.Join(dir, filepath.FromSlash(p)))
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, dir
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

func names(l Listing) []string {
	var out []string
	for _, e := range l.Entries {
		out = append(out, e.Path)
	}
	return out
}

func TestSensitive(t *testing.T) {
	for name, want := range map[string]bool{
		".git": true, ".GIT": true, ".env": true, ".env.local": true, ".Env.Production": true,
		".env.example": false, ".env.sample": false, ".env.template": false, ".envrc": true, ".ENVRC": true, ".git-credentials": true, "id_dsa": true, "id_dsa.pub": true,
		".pgpass": true, "prod.tfvars": true, "PROD.TFVARS": true, "tfvars": false, "notes.tfvars.md": false,
		".ssh": true, ".aws": true, ".gnupg": true, ".netrc": true, ".npmrc": true, ".pypirc": true,
		".docker": true, ".kube": true, "id_rsa": true, "id_rsa.pub": true, "id_ed25519": true, "id_ecdsa_sk": true,
		"server.pem": true, "tls.KEY": true, "cert.p12": true, "cert.pfx": true, "release.keystore": true, "AuthKey.p8": true,
		"credentials": true, "credentials.json": true, "secrets.json": true, "secrets.yaml": true, "secrets.yml": true,
		"report.md": false, "env": false, "keys": false, "my.env": false, "secrets.md": false, "credentials.md": false,
	} {
		if got := Sensitive(name); got != want {
			t.Errorf("Sensitive(%q) = %v, want %v", name, got, want)
		}
	}
}

// Every rail, one escape per row: the path a client names and the error it
// gets, for a read and, where it is a folder, a list.
func TestEscapes(t *testing.T) {
	r, dir := build(t, tree{
		files: map[string]string{
			"docs/report.md": "# Report\n", ".env": "KEY=1\n", ".git/config": "[core]\n",
			"keys/id_rsa": "private", "keys/server.pem": "pem", "sub/.aws/credentials": "aws",
		},
		links: map[string]string{
			"up":         "../outside/secret.txt",
			"updir":      "../outside",
			"deep/up":    "../../outside",
			"notes":      ".env",
			"gitlink":    ".git",
			"inside":     "docs/report.md",
			"loop":       "loop",
			"docs/climb": "../../shop/docs/report.md",
		},
	})
	outside := filepath.Join(filepath.Dir(dir), "outside")
	link(t, filepath.Join(outside, "secret.txt"), filepath.Join(dir, "abs"))

	tests := []struct {
		name string
		path string
		want error
	}{
		{"dotdot", "../outside/secret.txt", ErrBadPath},
		{"dotdot inside", "docs/../../outside/secret.txt", ErrBadPath},
		{"dotdot alone", "..", ErrBadPath},
		{"dot segment", "docs/./report.md", ErrBadPath},
		{"empty segment", "docs//report.md", ErrBadPath},
		{"trailing slash", "docs/", ErrBadPath},
		{"absolute path", "/etc/passwd", ErrBadPath},
		{"absolute into the root", filepath.Join(dir, "docs/report.md"), ErrBadPath},
		{"NUL", "docs/report.md\x00", ErrBadPath},
		{"absolute symlink out", "abs", ErrOutside},
		{"relative symlink out", "up", ErrOutside},
		{"through a folder link out", "updir/secret.txt", ErrOutside},
		{"nested link out", "deep/up/secret.txt", ErrOutside},
		{"climbs out and back in", "docs/climb", ErrOutside},
		{".env", ".env", ErrSensitive},
		{".env any case", ".ENV", ErrSensitive},
		{"under .git", ".git/config", ErrSensitive},
		{"private key", "keys/id_rsa", ErrSensitive},
		{"pem", "keys/server.pem", ErrSensitive},
		{"under a sensitive folder", "sub/.aws/credentials", ErrSensitive},
		{"link to .env", "notes", ErrSensitive},
		{"through a link to .git", "gitlink/config", ErrSensitive},
		{"missing", "docs/gone.md", ErrNotFound},
		{"link loop", "loop", ErrDenied},
		{"a folder", "docs", ErrNotFile},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.Read(tc.path); !errors.Is(err, tc.want) {
				t.Errorf("Read(%q) = %v, want %v", tc.path, err, tc.want)
			}
		})
	}
	// The same rails for a list.
	for p, want := range map[string]error{
		"..": ErrBadPath, "/": ErrBadPath, outside: ErrBadPath, "updir": ErrOutside, "deep/up": ErrOutside,
		".git": ErrSensitive, "gitlink": ErrSensitive, "sub/.aws": ErrSensitive, "docs/report.md": ErrNotFound,
	} {
		if _, err := r.List(context.Background(), p); !errors.Is(err, want) {
			t.Errorf("List(%q) = %v, want %v", p, err, want)
		}
		if _, err := r.Search(context.Background(), p, "e"); !errors.Is(err, want) {
			t.Errorf("Search(%q) = %v, want %v", p, err, want)
		}
	}
	// A link inside the root is followed.
	if f, err := r.Read("inside"); err != nil || f.Text != "# Report\n" {
		t.Errorf("Read(inside) = %+v, %v", f, err)
	}
}

// The list shows links, marks the ones that leave the root, hides the
// sensitive names and counts them.
func TestList(t *testing.T) {
	r, _ := build(t, tree{
		files: map[string]string{
			"b.md": "b", "A.md": "a", ".env": "x", ".env.example": "KEY=", "id_ed25519": "k",
			"zeta/x": "", "alpha/y": "", ".git/HEAD": "ref",
		},
		links: map[string]string{"out": "../outside", "in": "b.md", "secret": ".env"},
	})
	l, err := r.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "zeta", ".env.example", "A.md", "b.md", "in", "out", "secret"}
	if !slices.Equal(names(l), want) || l.Hidden != 3 || l.Truncated || l.RootName != "shop" || l.Path != "" {
		t.Fatalf("list = %v hidden %d truncated %v root %q", names(l), l.Hidden, l.Truncated, l.RootName)
	}
	byName := map[string]Entry{}
	for _, e := range l.Entries {
		byName[e.Name] = e
	}
	if e := byName["b.md"]; e.Kind != KindFile || e.Size != 1 || e.Modified.IsZero() || e.Modified.Location().String() != "UTC" {
		t.Errorf("b.md = %+v", e)
	}
	if e := byName["alpha"]; e.Kind != KindDir || e.Size != 0 {
		t.Errorf("alpha = %+v", e)
	}
	if !byName["out"].Blocked || byName["out"].Kind != KindLink || byName["in"].Blocked || byName["secret"].Blocked {
		t.Errorf("links = %+v %+v %+v", byName["out"], byName["in"], byName["secret"])
	}
	sub, err := r.List(context.Background(), "alpha")
	if err != nil || !slices.Equal(names(sub), []string{"alpha/y"}) || sub.Path != "alpha" {
		t.Errorf("List(alpha) = %v %v", names(sub), err)
	}
}

// A file that was listed and then swapped for a link out of the root is
// refused, and so is a folder swapped the same way.
func TestSwapAfterListing(t *testing.T) {
	r, dir := build(t, tree{files: map[string]string{"a.txt": "fine", "sub/b.txt": "fine"}})
	l, err := r.List(context.Background(), "")
	if err != nil || len(l.Entries) != 2 {
		t.Fatalf("list = %v %v", names(l), err)
	}
	outside := filepath.Join(filepath.Dir(dir), "outside")
	for _, p := range []string{"a.txt", "sub"} {
		if err := os.RemoveAll(filepath.Join(dir, p)); err != nil {
			t.Fatal(err)
		}
	}
	link(t, filepath.Join(outside, "secret.txt"), filepath.Join(dir, "a.txt"))
	link(t, "../outside", filepath.Join(dir, "sub"))
	if f, err := r.Read("a.txt"); !errors.Is(err, ErrOutside) {
		t.Errorf("Read(a.txt) after the swap = %+v, %v", f, err)
	}
	if f, err := r.Read("sub/secret.txt"); !errors.Is(err, ErrOutside) {
		t.Errorf("Read(sub/secret.txt) after the swap = %+v, %v", f, err)
	}
	if l, err := r.List(context.Background(), "sub"); !errors.Is(err, ErrOutside) {
		t.Errorf("List(sub) after the swap = %v, %v", names(l), err)
	}
	if _, _, _, err := r.Image("a.txt"); !errors.Is(err, ErrOutside) {
		t.Errorf("Image(a.txt) after the swap = %v", err)
	}
	// os.Root itself refuses the open if the swap lands after the check.
	if _, _, err := r.openAt("a.txt"); err == nil {
		t.Error("openAt followed a link out of the root")
	}
}

func TestRead(t *testing.T) {
	big := strings.Repeat("a", TextMax-1) + "é" + "tail"
	r, _ := build(t, tree{files: map[string]string{
		"notes.md":   "# Notes\nhello",
		"empty.txt":  "",
		"logo.svg":   `<svg xmlns="http://www.w3.org/2000/svg"></svg>`,
		"logo.png":   pngHead + "rest",
		"photo.jpg":  "\xff\xd8\xff\xe0rest",
		"anim.gif":   "GIF89a....",
		"pic.webp":   "RIFF\x00\x00\x00\x00WEBPVP8 ",
		"tool.bin":   "\x7fELF\x00\x00\x01",
		"latin1.txt": "caf\xe9",
		"big.txt":    big,
	}})
	tests := []struct {
		path, kind, text string
		truncated        bool
	}{
		{"notes.md", KindText, "# Notes\nhello", false},
		{"empty.txt", KindText, "", false},
		{"logo.svg", KindText, `<svg xmlns="http://www.w3.org/2000/svg"></svg>`, false},
		{"logo.png", KindImage, "", false},
		{"photo.jpg", KindImage, "", false},
		{"anim.gif", KindImage, "", false},
		{"pic.webp", KindImage, "", false},
		{"tool.bin", KindBinary, "", false},
		{"latin1.txt", KindBinary, "", false},
	}
	for _, tc := range tests {
		f, err := r.Read(tc.path)
		if err != nil || f.Kind != tc.kind || f.Text != tc.text || f.Truncated != tc.truncated || f.Path != tc.path {
			t.Errorf("Read(%s) = %+v, %v", tc.path, f, err)
		}
	}
	// Over the text limit: cut at TextMax, never in the middle of a rune.
	f, err := r.Read("big.txt")
	if err != nil || f.Kind != KindText || !f.Truncated || f.Size != int64(len(big)) {
		t.Fatalf("big = %v %v %v %d", f.Kind, f.Truncated, err, f.Size)
	}
	if len(f.Text) != TextMax-1 || !utf8.ValidString(f.Text) {
		t.Errorf("big text = %d bytes, valid %v", len(f.Text), utf8.ValidString(f.Text))
	}
}

func TestImage(t *testing.T) {
	r, dir := build(t, tree{files: map[string]string{"logo.png": pngHead + "rest", "notes.md": "# hi", "huge.png": pngHead}})
	if err := os.Truncate(filepath.Join(dir, "huge.png"), ImageMax+1); err != nil {
		t.Fatal(err)
	}
	f, ctype, body, err := r.Image("logo.png")
	if err != nil || ctype != "image/png" || f.Kind != KindImage || f.Size != int64(len(pngHead)+4) {
		t.Fatalf("Image = %+v %q %v", f, ctype, err)
	}
	_ = body.Close()
	if _, _, _, err := r.Image("notes.md"); !errors.Is(err, ErrNotImage) {
		t.Errorf("Image(notes.md) = %v", err)
	}
	if _, _, _, err := r.Image("huge.png"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Image(huge.png) = %v", err)
	}
	// The JSON read still describes it, with no bytes.
	if f, err := r.Read("huge.png"); err != nil || f.Kind != KindImage || f.Size != ImageMax+1 {
		t.Errorf("Read(huge.png) = %+v %v", f, err)
	}
}

func TestSearch(t *testing.T) {
	r, _ := build(t, tree{
		files: map[string]string{
			"Report.md": "", "docs/report-2.md": "", "docs/deep/old-report.txt": "", "docs/other.md": "",
			"node_modules/report/index.js": "", ".git/report": "", ".env.report": "", "keys/report.pem": "",
			".env.example": "",
		},
		links: map[string]string{"reports": "../outside"},
	})
	l, err := r.Search(context.Background(), "", "REPORT")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/deep/old-report.txt", "docs/report-2.md", "Report.md", "reports"}
	if !slices.Equal(names(l), want) || l.Hidden != 2 || l.Truncated {
		t.Fatalf("search = %v hidden %d truncated %v", names(l), l.Hidden, l.Truncated)
	}
	if l, _ := r.Search(context.Background(), "docs", "report"); !slices.Equal(names(l), []string{"docs/deep/old-report.txt", "docs/report-2.md"}) || l.Path != "docs" {
		t.Errorf("search under docs = %v", names(l))
	}
	if l, _ := r.Search(context.Background(), "", "env"); !slices.Equal(names(l), []string{".env.example"}) || l.Hidden != 1 {
		t.Errorf("search env = %v hidden %d", names(l), l.Hidden)
	}
	// Out of time: what was found so far, truncated.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l, err := r.Search(ctx, "", "report"); err != nil || !l.Truncated {
		t.Errorf("cancelled search = %v %v", l.Truncated, err)
	}
}

// A large folder: the list stops at ListMax, folders first; a search stops
// at SearchMatchMax.
func TestLargeFolder(t *testing.T) {
	r, dir := build(t, tree{files: map[string]string{"zz-dir/x": "", ".env": ""}})
	for i := range ListMax + 500 {
		write(t, filepath.Join(dir, fmt.Sprintf("f%05d.txt", i)), "")
	}
	l, err := r.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != ListMax || !l.Truncated || l.Hidden != 1 || l.Entries[0].Name != "zz-dir" || l.Entries[1].Name != "f00000.txt" {
		t.Fatalf("list = %d entries, truncated %v, hidden %d, first %v", len(l.Entries), l.Truncated, l.Hidden, names(l)[:2])
	}
	s, err := r.Search(context.Background(), "", ".txt")
	if err != nil || len(s.Entries) != SearchMatchMax || !s.Truncated {
		t.Fatalf("search = %d entries, truncated %v, %v", len(s.Entries), s.Truncated, err)
	}
}

// A folder that is gone, or not a folder, is not found; so is a file
// removed while its root is open.
func TestGone(t *testing.T) {
	r, dir := build(t, tree{files: map[string]string{"a.txt": "a"}})
	for _, p := range []string{filepath.Join(t.TempDir(), "missing"), filepath.Join(dir, "a.txt")} {
		if _, err := Open(p); !errors.Is(err, ErrNotFound) {
			t.Errorf("Open(%s) = %v", p, err)
		}
	}
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read("a.txt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Read after removal = %v", err)
	}
}
