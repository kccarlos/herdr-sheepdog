package web

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// upload posts one multipart file to the agent's attachments route.
func upload(t *testing.T, h *harness, pane, name string, data []byte) (int, AttachmentResponse, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(data)
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, h.http.URL+windowPath(pane, "attachments"), &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var raw bytes.Buffer
	_, _ = raw.ReadFrom(resp.Body)
	var out AttachmentResponse
	_ = json.Unmarshal(raw.Bytes(), &out)
	return resp.StatusCode, out, raw.String()
}

var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00")

// An upload lands in <data>/attachments/<pane>/ under a cleaned name with
// the sniffed type's extension, 0600, and the log has its size and type
// but never its name.
func TestAttachmentStored(t *testing.T) {
	data := t.TempDir()
	h := newHarness(t, harnessOpts{dataDir: data})
	trackWorking(t, h, "w1:p2", "auth", "hi")

	code, got, raw := upload(t, h, "w1:p2", "../../My Screen shot (2).HEIC", pngHeader)
	if code != http.StatusCreated {
		t.Fatalf("upload = %d %s", code, raw)
	}
	dir := filepath.Join(data, "attachments", "w1_p2")
	if filepath.Dir(got.Path) != dir {
		t.Fatalf("path %q not in %q", got.Path, dir)
	}
	if !strings.HasSuffix(got.Name, "-My-Screen-shot-2.png") || strings.ContainsAny(got.Name, " /()") || got.Type != "image/png" || got.Bytes != int64(len(pngHeader)) {
		t.Fatalf("got %+v", got)
	}
	info, err := os.Stat(got.Path)
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() != int64(len(pngHeader)) {
		t.Fatalf("stat = %v %v", info, err)
	}
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "web.attachment" {
			if strings.Contains(string(ev.Payload), "Screen") || !strings.Contains(string(ev.Payload), "image/png") {
				t.Fatalf("log payload = %s", ev.Payload)
			}
			return
		}
	}
	t.Fatal("no web.attachment event")
}

// Text keeps its own extension; a name with nothing usable becomes file.txt.
func TestAttachmentText(t *testing.T) {
	h := newHarness(t, harnessOpts{dataDir: t.TempDir()})
	trackWorking(t, h, "w1:p2", "auth", "hi")
	if code, got, raw := upload(t, h, "w1:p2", "main.go", []byte("package main\n")); code != http.StatusCreated || !strings.HasSuffix(got.Name, "-main.go") || got.Type != "text/plain" {
		t.Fatalf("go file = %d %s", code, raw)
	}
	if code, got, raw := upload(t, h, "w1:p2", "日志", []byte("hello\n")); code != http.StatusCreated || !strings.HasSuffix(got.Name, "-file.txt") {
		t.Fatalf("unicode name = %d %s", code, raw)
	}
}

// Refusals: an untracked pane, an unknown type, an empty file, a file over
// 20 MB (nothing left behind), and no data directory.
func TestAttachmentRefused(t *testing.T) {
	data := t.TempDir()
	h := newHarness(t, harnessOpts{dataDir: data})
	trackWorking(t, h, "w1:p2", "auth", "hi")
	if code, _, _ := upload(t, h, "w9:p9", "a.png", pngHeader); code != http.StatusNotFound {
		t.Fatalf("untracked = %d", code)
	}
	if code, _, _ := upload(t, h, "w1:p2", "a.zip", []byte("PK\x03\x04\x00\x00\x00\x00binary\x00\x01")); code != http.StatusUnsupportedMediaType {
		t.Fatalf("zip = %d", code)
	}
	if code, _, _ := upload(t, h, "w1:p2", "a.txt", nil); code != http.StatusBadRequest {
		t.Fatalf("empty = %d", code)
	}
	big := append(append([]byte{}, pngHeader...), make([]byte, MaxAttachmentBytes)...)
	if code, _, raw := upload(t, h, "w1:p2", "big.png", big); code != http.StatusRequestEntityTooLarge || !strings.Contains(raw, "20 MB") {
		t.Fatalf("big = %d %s", code, raw)
	}
	if files, _ := os.ReadDir(filepath.Join(data, "attachments", "w1_p2")); len(files) != 0 {
		t.Fatalf("left behind: %v", files)
	}

	none := newHarness(t, harnessOpts{})
	trackWorking(t, none, "w1:p2", "auth", "hi")
	if code, _, _ := upload(t, none, "w1:p2", "a.png", pngHeader); code != http.StatusServiceUnavailable {
		t.Fatalf("no data dir = %d", code)
	}
}

// Files older than 7 days are removed at start-up, with their empty
// folder; newer ones stay.
func TestAttachmentPrune(t *testing.T) {
	data := t.TempDir()
	oldDir := filepath.Join(data, "attachments", "w1_p1")
	keepDir := filepath.Join(data, "attachments", "w1_p2")
	for _, d := range []string{oldDir, keepDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := filepath.Join(oldDir, "old.png")
	keep := filepath.Join(keepDir, "new.png")
	_ = os.WriteFile(old, pngHeader, 0o600)
	_ = os.WriteFile(keep, pngHeader, 0o600)
	past := time.Now().Add(-8 * 24 * time.Hour)
	_ = os.Chtimes(old, past, past)

	newHarness(t, harnessOpts{dataDir: data})
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old folder still there: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("new file pruned: %v", err)
	}
}

func TestAttachmentNames(t *testing.T) {
	now := time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)
	for in, want := range map[string]string{
		"a.png":                           "-a.png",
		"/etc/passwd":                     "-passwd.png",
		`..\..\x.png`:                     "-x.png",
		"...":                             "-file.png",
		".hidden.png":                     "-hidden.png",
		strings.Repeat("a", 200) + ".png": "-" + strings.Repeat("a", 64) + ".png",
	} {
		got := attachmentName(in, ".png", now)
		if !strings.HasPrefix(got, "20261020-010203-") || !strings.HasSuffix(got, want) || strings.ContainsAny(got, "/\\ ") {
			t.Errorf("attachmentName(%q) = %q, want suffix %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"w1:p2": "w1_p2", "../..": "pane", "a/b": "a_b"} {
		if got := paneDirName(in); got != want {
			t.Errorf("paneDirName(%q) = %q, want %q", in, got, want)
		}
	}
}
