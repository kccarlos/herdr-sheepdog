package cli

import (
	"path/filepath"
	"testing"
)

func TestDetectAbsolutePaths(t *testing.T) {
	fake := fakeBinPath(t)
	d := Detect(Config{CodexBin: fake, ClaudeBin: filepath.Join(t.TempDir(), "no-such-claude")}, nil)
	if !d.Codex {
		t.Error("codex should be detected via an absolute path")
	}
	if d.Claude {
		t.Error("claude should not be detected: binary does not exist")
	}
}

func TestDetectLogsEvent(t *testing.T) {
	fake := fakeBinPath(t)
	log := &recLog{}
	Detect(Config{CodexBin: fake, ClaudeBin: fake}, log)
	events := log.ofType("cli.detected")
	if len(events) != 1 {
		t.Fatalf("cli.detected events = %d, want 1", len(events))
	}
	payload, ok := events[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", events[0].Payload)
	}
	if payload["codex"] != true || payload["claude"] != true {
		t.Errorf("payload = %+v", payload)
	}
}

func TestDetectViaTempPATH(t *testing.T) {
	dir := t.TempDir()
	writeFakeBin(t, dir, "codex")
	t.Setenv("PATH", dir)
	d := Detect(Config{}, nil) // default CodexBin/ClaudeBin: "codex"/"claude", looked up on PATH
	if !d.Codex {
		t.Error("codex should be found on PATH")
	}
	if d.Claude {
		t.Error("claude should not be found: not on PATH")
	}
}

func TestDetectNopLogger(t *testing.T) {
	// Detect(nil log) must not panic (logging.NopLogger fallback).
	Detect(Config{CodexBin: fakeBinPath(t)}, nil)
}
