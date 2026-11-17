package factory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func minimalEngineFS(pyVer, body string) fstest.MapFS {
	return fstest.MapFS{
		"factory/.python-version":                       {Data: []byte(pyVer + "\n")},
		"factory/NOTICE":                                {Data: []byte("MIT Notice\n")},
		"factory/bin/herdr-secret":                      {Data: []byte("#!/usr/bin/env python3\nprint('secret')\n")},
		"factory/mods/tasks/bin/herdr-task":             {Data: []byte("#!/usr/bin/env python3\n" + body + "\n")},
		"factory/mods/tasks/bin/herdr_task/__init__.py": {Data: []byte("# pkg\n")},
		"factory/mods/tasks/bin/herdr_task/reviewer.sb": {Data: []byte("(version 1)\n")},
		"factory/templates/assistant.md":                {Data: []byte("# assistant\n")},
		"factory/templates/reviewer.md":                 {Data: []byte("# reviewer\n")},
		"factory/templates/worker.md":                   {Data: []byte("# worker\n")},
	}
}

func TestEnsureEnginePerVersionAndIdempotent(t *testing.T) {
	dataDir := t.TempDir()

	engV1, err := EnsureEngine(dataDir, EngineSource{Version: "v0.2.0"})
	if err != nil {
		t.Fatalf("EnsureEngine v0.2.0: %v", err)
	}
	if !engV1.Unpacked {
		t.Fatal("first EnsureEngine should report Unpacked=true")
	}
	if engV1.VersionKey != "v0.2.0" {
		t.Fatalf("VersionKey = %q, want v0.2.0", engV1.VersionKey)
	}
	if engV1.PythonVersion != PinnedPythonVersion() {
		t.Fatalf("PythonVersion = %q, want %q", engV1.PythonVersion, PinnedPythonVersion())
	}

	// Executable wrappers must have 0755; plain files must stay 0644.
	for _, p := range []string{engV1.HerdrTask, engV1.HerdrSecret} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if st.Mode().Perm() != 0o755 {
			t.Errorf("%s mode = %o, want 0755", p, st.Mode().Perm())
		}
	}
	for _, p := range []string{engV1.ReviewerSandbox, filepath.Join(engV1.Dir, "NOTICE")} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if st.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %o, want 0644", p, st.Mode().Perm())
		}
	}

	// Second call on the same version is a fast no-op.
	again, err := EnsureEngine(dataDir, EngineSource{Version: "v0.2.0"})
	if err != nil {
		t.Fatalf("EnsureEngine repeat: %v", err)
	}
	if again.Unpacked {
		t.Fatal("repeat EnsureEngine should report Unpacked=false")
	}

	// A new release unpacks beside the old one without overwriting v0.2.0.
	engV2, err := EnsureEngine(dataDir, EngineSource{Version: "v0.2.1"})
	if err != nil {
		t.Fatalf("EnsureEngine v0.2.1: %v", err)
	}
	if !engV2.Unpacked || engV2.Dir == engV1.Dir {
		t.Fatalf("v0.2.1 = %+v, want separate unpacked directory from %s", engV2, engV1.Dir)
	}
	if _, err := InspectEngine(dataDir, EngineSource{Version: "v0.2.0"}); err != nil {
		t.Fatalf("v0.2.0 should remain intact beside v0.2.1: %v", err)
	}
}

func TestEnsureEngineDevDigestAndSelfHeal(t *testing.T) {
	dataDir := t.TempDir()
	fsA := minimalEngineFS("3.14.8", "print('a')")
	fsB := minimalEngineFS("3.14.8", "print('b')")

	engA, err := EnsureEngine(dataDir, EngineSource{FS: fsA, Version: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	engB, err := EnsureEngine(dataDir, EngineSource{FS: fsB, Version: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(engA.VersionKey, "dev-") || engA.Dir == engB.Dir {
		t.Fatalf("dev builds with different content collided: %s vs %s", engA.Dir, engB.Dir)
	}

	// Corrupting a file or stripping executable permissions makes InspectEngine
	// fail and EnsureEngine repair it atomically.
	if err := os.Chmod(engA.HerdrTask, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectEngine(dataDir, EngineSource{FS: fsA, Version: "dev"}); err == nil {
		t.Fatal("InspectEngine should reject stripped executable bit")
	}
	repaired, err := EnsureEngine(dataDir, EngineSource{FS: fsA, Version: "dev"})
	if err != nil || !repaired.Unpacked {
		t.Fatalf("repaired = %+v, err = %v", repaired, err)
	}
	st, err := os.Stat(engA.HerdrTask)
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("HerdrTask mode after repair = %v, err = %v", st, err)
	}

	// Tampering with content is also repaired.
	if err := os.WriteFile(filepath.Join(engA.Dir, "NOTICE"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectEngine(dataDir, EngineSource{FS: fsA, Version: "dev"}); err == nil {
		t.Fatal("InspectEngine should reject modified file")
	}
	repaired, err = EnsureEngine(dataDir, EngineSource{FS: fsA, Version: "dev"})
	if err != nil || !repaired.Unpacked {
		t.Fatalf("content repair = %+v, err = %v", repaired, err)
	}

	// Refuses to overwrite a symlinked engine folder.
	extDir := t.TempDir()
	symTarget := filepath.Join(EnginesDir(dataDir), "v9.9.9")
	if err := os.Symlink(extDir, symTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureEngine(dataDir, EngineSource{FS: fsA, Version: "v9.9.9"}); err == nil {
		t.Fatal("EnsureEngine should refuse symlinked engine folder")
	}
}

func TestUnpackedEngineRunsHelp(t *testing.T) {
	py3, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	dataDir := t.TempDir()
	eng, err := EnsureEngine(dataDir, EngineSource{Version: "v0.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	scratchHome := t.TempDir()
	env := EngineEnv(
		[]string{
			"HOME=" + scratchHome,
			"XDG_CONFIG_HOME=" + filepath.Join(scratchHome, ".config"),
			"XDG_DATA_HOME=" + filepath.Join(scratchHome, ".local", "share"),
			"XDG_STATE_HOME=" + filepath.Join(scratchHome, ".local", "state"),
			"PATH=/usr/bin:/bin",
		},
		ManagedPython{BinDir: filepath.Dir(py3)},
		eng,
	)
	out := SystemRunCommand(context.Background(), scratchHome, env, eng.HerdrTask, "--help")
	if out.Err != nil || out.Exit != 0 || !strings.Contains(out.Stdout, "herdr-task") {
		t.Fatalf("unpacked herdr-task --help failed: exit=%d err=%v stdout=%q stderr=%q", out.Exit, out.Err, out.Stdout, out.Stderr)
	}
}
