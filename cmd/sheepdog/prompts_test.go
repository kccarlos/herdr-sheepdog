package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptsExportAndShow(t *testing.T) {
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"prompts", "export", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("export: code=%d stderr=%s", code, stderr.String())
	}
	for _, rel := range []string{"assistant.md", "USER.md", filepath.Join("svp", "SOUL.md"), filepath.Join("svp", "RULES.md")} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("export did not write %s: %v", rel, err)
		}
	}

	// A second export without --force must refuse and change nothing.
	before, err := os.ReadFile(filepath.Join(dir, "assistant.md"))
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"prompts", "export", dir}, &stdout, &stderr); code == 0 {
		t.Error("second export without --force should fail")
	}
	after, err := os.ReadFile(filepath.Join(dir, "assistant.md"))
	if err != nil || !bytes.Equal(before, after) {
		t.Error("refused export must not modify the existing file")
	}

	// show, embedded (no override dir configured).
	stdout.Reset()
	if code := run(context.Background(), []string{"prompts", "show", "--prompts-dir", "", "assistant.md"}, &stdout, &stderr); code != 0 {
		t.Fatalf("show embedded: code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "source: embedded") {
		t.Errorf("show embedded: %s", stdout.String())
	}

	// Edit the exported override and show again: must report it.
	if err := os.WriteFile(filepath.Join(dir, "assistant.md"), []byte("edited by the CEO"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := run(context.Background(), []string{"prompts", "show", "--prompts-dir", dir, "assistant.md"}, &stdout, &stderr); code != 0 {
		t.Fatalf("show override: code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "source: override") || !strings.Contains(stdout.String(), "edited by the CEO") {
		t.Errorf("show override: %s", stdout.String())
	}

	// Unknown name is rejected.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"prompts", "show", "nope.md"}, &stdout, &stderr); code == 0 {
		t.Error("unknown prompt name should be rejected")
	}
}
