package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/files"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

const testPNG = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01"

// filesFolder makes a folder "shop" with a report, a .env, an image, a
// huge image and a link to a secret outside it.
func filesFolder(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "shop")
	writeT(t, filepath.Join(base, "outside", "secret.txt"), "secret\n")
	writeT(t, filepath.Join(dir, "docs", "report.md"), "# Report\n")
	writeT(t, filepath.Join(dir, ".env"), "KEY=1\n")
	writeT(t, filepath.Join(dir, "logo.png"), testPNG)
	writeT(t, filepath.Join(dir, "huge.png"), testPNG)
	if err := os.Truncate(filepath.Join(dir, "huge.png"), files.ImageMax+1); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(dir, "out")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fileReads returns the files.read events' payloads, with the pane.
func fileReads(t *testing.T, h *harness) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "files.read" {
			continue
		}
		var pl map[string]any
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		pl["pane"] = ev.PaneID
		out = append(out, pl)
	}
	return out
}

// An agent's folder: the list, a search, a text file, an image's bytes,
// and the feature in bot-ready.
func TestFilesAgent(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	trackIn(t, h, "w1:p2", filesFolder(t))
	if !slices.Contains(h.srv.features(), wsproto.FeatureFiles) {
		t.Errorf("features %v lack files", h.srv.features())
	}

	var list files.Listing
	if code := h.getJSON(windowPath("w1:p2", "files"), &list); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	var got []string
	for _, e := range list.Entries {
		got = append(got, e.Path+":"+e.Kind)
	}
	if !slices.Equal(got, []string{"docs:dir", "huge.png:file", "logo.png:file", "out:link"}) || list.RootName != "shop" || list.Hidden != 1 || !list.Entries[3].Blocked {
		t.Fatalf("list = %v %+v", got, list)
	}
	list = files.Listing{}
	if code := h.getJSON(windowPath("w1:p2", "files?path=docs&q=REPORT"), &list); code != http.StatusOK || len(list.Entries) != 1 || list.Entries[0].Path != "docs/report.md" {
		t.Fatalf("search = %d %+v", code, list)
	}

	var f files.File
	if code := h.getJSON(windowPath("w1:p2", "files/docs/report.md"), &f); code != http.StatusOK || f.Kind != files.KindText || f.Text != "# Report\n" || f.Size != 9 {
		t.Fatalf("read = %d %+v", code, f)
	}
	f = files.File{}
	if code := h.getJSON(windowPath("w1:p2", "files/huge.png"), &f); code != http.StatusOK || f.Kind != files.KindImage || f.Text != "" {
		t.Fatalf("huge as JSON = %d %+v", code, f)
	}

	resp, err := http.Get(h.http.URL + windowPath("w1:p2", "files/logo.png?raw=1"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != testPNG || resp.Header.Get("Content-Type") != "image/png" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("raw = %d %q %v", resp.StatusCode, body, resp.Header)
	}
}

// Every refusal: its status and reason.
func TestFilesErrors(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	dir := filesFolder(t)
	trackIn(t, h, "w1:p2", dir)
	trackWorking(t, h, "w1:p3", "nocwd", "hi")
	trackIn(t, h, "w1:p4", filepath.Join(dir, "removed"))
	abs := url.PathEscape(filepath.Join(dir, "docs", "report.md"))
	tests := []struct {
		path   string
		code   int
		reason string
	}{
		{windowPath("w9:p9", "files"), 404, "no_agent"},
		{windowPath("w9:p9", "files/docs/report.md"), 404, "no_agent"},
		{windowPath("w1:p3", "files"), 404, "no_cwd"},
		{windowPath("w1:p4", "files"), 404, "not_found"},
		{windowPath("w1:p2", "files/gone.md"), 404, "not_found"},
		{windowPath("w1:p2", "files?path=docs%2Freport.md"), 404, "not_found"},
		{windowPath("w1:p2", "files/..%2F..%2Fetc%2Fpasswd"), 400, "bad_path"},
		{windowPath("w1:p2", "files?path=..%2Foutside"), 400, "bad_path"},
		{windowPath("w1:p2", "files?path=%2Fetc&q=passwd"), 400, "bad_path"},
		{windowPath("w1:p2", "files/") + abs, 400, "bad_path"},
		{windowPath("w1:p2", "files/out/secret.txt"), 403, "outside"},
		{windowPath("w1:p2", "files?path=out"), 403, "outside"},
		{windowPath("w1:p2", "files/.env"), 403, "sensitive"},
		{windowPath("w1:p2", "files/.env?raw=1"), 403, "sensitive"},
		{windowPath("w1:p2", "files?path=.env&q=key"), 403, "sensitive"},
		{windowPath("w1:p2", "files/docs"), 403, "not_file"},
		{windowPath("w1:p2", "files/huge.png?raw=1"), 413, "too_large"},
		{windowPath("w1:p2", "files/docs/report.md?raw=1"), 415, "not_image"},
	}
	for _, tc := range tests {
		var body map[string]string
		if code := h.getJSON(tc.path, &body); code != tc.code || body["reason"] != tc.reason || body["error"] == "" {
			t.Errorf("GET %s = %d %v, want %d %s", tc.path, code, body, tc.code, tc.reason)
		}
	}
	// GET only.
	for _, p := range []string{windowPath("w1:p2", "files"), windowPath("w1:p2", "files/docs/report.md"), "/api/spaces/w3/files/a"} {
		resp, err := http.Post(h.http.URL+p, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", p, resp.StatusCode)
		}
	}
}

// Each read is audited with its path and size, or why it was refused,
// never its contents; a list is not.
func TestFilesAudit(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	trackIn(t, h, "w1:p2", filesFolder(t))
	h.getJSON(windowPath("w1:p2", "files"), nil)
	h.getJSON(windowPath("w1:p2", "files/docs/report.md"), nil)
	h.getJSON(windowPath("w1:p2", "files/.env"), nil)
	resp, err := http.Get(h.http.URL + windowPath("w1:p2", "files/logo.png?raw=1"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	reads := fileReads(t, h)
	if len(reads) != 3 {
		t.Fatalf("files.read = %v", reads)
	}
	if r := reads[0]; r["pane"] != "w1:p2" || r["path"] != "docs/report.md" || r["size"] != float64(9) || r["raw"] != false {
		t.Errorf("text read = %v", r)
	}
	if r := reads[1]; r["path"] != ".env" || r["reason"] != "sensitive" || r["size"] != nil {
		t.Errorf("refused read = %v", r)
	}
	if r := reads[2]; r["path"] != "logo.png" || r["raw"] != true || r["size"] != float64(len(testPNG)) {
		t.Errorf("raw read = %v", r)
	}
	for _, ev := range h.log.Tail(0) {
		if strings.Contains(string(ev.Payload), "Report") || strings.Contains(string(ev.Payload), "KEY=1") {
			t.Errorf("%s carries a file's contents: %s", ev.Type, ev.Payload)
		}
	}
}

// stubSpaceFolder answers a space's folder without herdr.
type stubSpaceFolder struct {
	*supervisor.Supervisor
	dirs map[string]string
	errs map[string]error
}

func (s *stubSpaceFolder) SpaceFolder(_ context.Context, id string) (string, error) {
	if err, ok := s.errs[id]; ok {
		return "", err
	}
	dir, ok := s.dirs[id]
	if !ok {
		return "", supervisor.ErrNoSuchSpace
	}
	return dir, nil
}

// A space's folder is served the same way; an unknown space, one with no
// folder, and a herdr that cannot list spaces each say so.
func TestFilesSpace(t *testing.T) {
	dir := filesFolder(t)
	h := newHarness(t, harnessOpts{wrap: func(s *supervisor.Supervisor) Supervisor {
		return &stubSpaceFolder{Supervisor: s, dirs: map[string]string{"w3": dir, "w4": ""}}
	}})
	var list files.Listing
	if code := h.getJSON("/api/spaces/w3/files?path=docs", &list); code != http.StatusOK || len(list.Entries) != 1 || list.Path != "docs" {
		t.Fatalf("space list = %d %+v", code, list)
	}
	var f files.File
	if code := h.getJSON("/api/spaces/w3/files/docs/report.md", &f); code != http.StatusOK || f.Text != "# Report\n" {
		t.Fatalf("space read = %d %+v", code, f)
	}
	if reads := fileReads(t, h); len(reads) != 1 || reads[0]["space"] != "w3" || reads[0]["pane"] != "" {
		t.Errorf("files.read = %v", reads)
	}
	for p, want := range map[string]string{"/api/spaces/w9/files": "no_space", "/api/spaces/w4/files": "no_cwd", "/api/spaces/w3/files/.env": "sensitive"} {
		var body map[string]string
		if h.getJSON(p, &body); body["reason"] != want {
			t.Errorf("GET %s = %v, want %s", p, body, want)
		}
	}

	plain := newHarness(t, harnessOpts{})
	if code := plain.getJSON("/api/spaces/w1/files", nil); code != http.StatusInternalServerError {
		t.Errorf("space list on mock herdr = %d, want 500", code)
	}
}

// An agent or space whose folder is itself sensitive is refused, and an
// error the daemon did not expect says so in fixed words: no path, no
// system text, status 500 failed.
func TestFilesRefusalsNamePaths(t *testing.T) {
	dir := filesFolder(t)
	if err := os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, harnessOpts{wrap: func(s *supervisor.Supervisor) Supervisor {
		return &stubSpaceFolder{Supervisor: s, dirs: map[string]string{"w3": filepath.Join(dir, ".git")}, errs: map[string]error{
			"w5": fmt.Errorf("herdr: stat /Users/someone/private: %w", syscall.EIO),
		}}
	}})
	trackIn(t, h, "w1:p2", filepath.Join(dir, ".git", "hooks"))
	for _, tc := range []struct {
		path   string
		code   int
		reason string
	}{
		{windowPath("w1:p2", "files"), 403, "sensitive"},
		{windowPath("w1:p2", "files/x"), 403, "sensitive"},
		{"/api/spaces/w3/files", 403, "sensitive"},
		{"/api/spaces/w5/files", 500, "failed"},
		{"/api/spaces/w5/files/a.txt", 500, "failed"},
	} {
		resp, err := http.Get(h.http.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var body map[string]string
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		if resp.StatusCode != tc.code || body["reason"] != tc.reason || strings.Contains(string(raw), "/Users") || strings.Contains(string(raw), dir) {
			t.Errorf("GET %s = %d %s, want %d %s with no path", tc.path, resp.StatusCode, raw, tc.code, tc.reason)
		}
	}
}
