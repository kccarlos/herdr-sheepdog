package factory

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

func createManagedPythonStub(t *testing.T, dataDir, version string) string {
	t.Helper()
	homeDir := filepath.Join(PythonInstallDir(dataDir), "cpython-"+version+"-test")
	binDir := filepath.Join(homeDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pyBin := filepath.Join(binDir, "python3")
	if err := os.WriteFile(pyBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return homeDir
}

func TestInspectPythonRejectsSystemPythonAndSymlinks(t *testing.T) {
	ctx := context.Background()
	pinned := PinnedPythonVersion()

	t.Run("rejects symlinked python3 pointing outside data_dir", func(t *testing.T) {
		dataDir := t.TempDir()
		outsideDir := t.TempDir()
		fakeSysPy := filepath.Join(outsideDir, "system-python3")
		if err := os.WriteFile(fakeSysPy, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(PythonInstallDir(dataDir), "cpython-"+pinned+"-darwin", "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(fakeSysPy, filepath.Join(binDir, "python3")); err != nil {
			t.Fatal(err)
		}

		executed := false
		_, err := InspectPython(ctx, PythonOptions{
			DataDir:       dataDir,
			PinnedVersion: pinned,
			TempDir:       t.TempDir(),
			RunCommand: func(context.Context, string, []string, ...string) CommandOutput {
				executed = true
				return CommandOutput{Stdout: pinned + "\n/usr\n"}
			},
		})
		if err == nil || !strings.Contains(err.Error(), "outside") {
			t.Fatalf("expected outside-dir rejection, got err=%v", err)
		}
		if executed {
			t.Fatal("external symlinked python3 must be rejected before execution")
		}
	})

	t.Run("rejects symlinked factory/python root", func(t *testing.T) {
		dataDir := t.TempDir()
		outside := t.TempDir()
		if err := os.MkdirAll(FactoryDir(dataDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, PythonInstallDir(dataDir)); err != nil {
			t.Fatal(err)
		}
		_, err := InspectPython(ctx, PythonOptions{
			DataDir:       dataDir,
			PinnedVersion: pinned,
			TempDir:       t.TempDir(),
		})
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("expected symlink error, got %v", err)
		}
	})

	t.Run("rejects wrong version and external base_prefix", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := createManagedPythonStub(t, dataDir, pinned)

		// Wrong version (e.g. macOS system 3.9.6).
		_, err := InspectPython(ctx, PythonOptions{
			DataDir:       dataDir,
			PinnedVersion: pinned,
			TempDir:       t.TempDir(),
			RunCommand: func(context.Context, string, []string, ...string) CommandOutput {
				return CommandOutput{Stdout: "3.9.6\n" + homeDir + "\n"}
			},
		})
		if err == nil || !strings.Contains(err.Error(), "3.9.6") {
			t.Fatalf("expected version mismatch error, got %v", err)
		}

		// External sys.base_prefix.
		_, err = InspectPython(ctx, PythonOptions{
			DataDir:       dataDir,
			PinnedVersion: pinned,
			TempDir:       t.TempDir(),
			RunCommand: func(context.Context, string, []string, ...string) CommandOutput {
				return CommandOutput{Stdout: pinned + "\n/usr/local\n"}
			},
		})
		if err == nil || !strings.Contains(err.Error(), "base_prefix") {
			t.Fatalf("expected base_prefix rejection, got %v", err)
		}
	})
}

func TestCheckPythonRepairAndFailureModes(t *testing.T) {
	ctx := context.Background()
	pinned := PinnedPythonVersion()

	t.Run("healthy python passes without invoking uv", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := createManagedPythonStub(t, dataDir, pinned)
		pyExec := filepath.Join(homeDir, "bin", "python3")

		env := newFakeEnv() // uv is NOT even installed
		env.handlers[pyExec+" -I"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stdout: pinned + "\n" + homeDir + "\n"}
		}

		r := CheckPython(ctx, DoctorOptions{
			DataDir:     dataDir,
			TempDir:     t.TempDir(),
			LookPath:    env.lookPath,
			RunCommand:  env.run,
			AuthTimeout: time.Second,
		})
		if r.Status != onboard.OK || strings.Contains(r.Detail, "installed with uv") {
			t.Fatalf("healthy CheckPython = %+v", r)
		}
	})

	t.Run("missing python is installed with uv --reinstall --no-bin --managed-python", func(t *testing.T) {
		dataDir := t.TempDir()
		env := newFakeEnv("uv")
		var uvArgs []string

		env.handlers["/usr/local/bin/uv python"] = func(_ string, extra []string, argv []string) CommandOutput {
			uvArgs = append([]string(nil), argv...)
			if !slices.Contains(extra, "UV_NO_PROGRESS=1") {
				t.Errorf("uv extra env = %v, want UV_NO_PROGRESS=1", extra)
			}
			homeDir := createManagedPythonStub(t, dataDir, pinned)
			pyExec := filepath.Join(homeDir, "bin", "python3")
			env.mu.Lock()
			env.handlers[pyExec+" -I"] = func(string, []string, []string) CommandOutput {
				return CommandOutput{Stdout: pinned + "\n" + homeDir + "\n"}
			}
			env.mu.Unlock()
			return CommandOutput{Exit: 0}
		}

		r := CheckPython(ctx, DoctorOptions{
			DataDir:              dataDir,
			TempDir:              t.TempDir(),
			LookPath:             env.lookPath,
			RunCommand:           env.run,
			AuthTimeout:          time.Second,
			PythonInstallTimeout: time.Second,
		})
		if r.Status != onboard.OK || !strings.Contains(r.Detail, "installed with uv") {
			t.Fatalf("repaired CheckPython = %+v", r)
		}
		for _, wantFlag := range []string{"--reinstall", "--install-dir", PythonInstallDir(dataDir), "--no-bin", "--managed-python", "--no-config", pinned} {
			if !slices.Contains(uvArgs, wantFlag) {
				t.Errorf("uv args %v missing %q", uvArgs, wantFlag)
			}
		}
	})

	t.Run("broken python installation is repaired with uv --reinstall", func(t *testing.T) {
		dataDir := t.TempDir()
		homeDir := createManagedPythonStub(t, dataDir, pinned)
		pyExec := filepath.Join(homeDir, "bin", "python3")
		repaired := false

		env := newFakeEnv("uv")
		env.handlers[pyExec+" -I"] = func(string, []string, []string) CommandOutput {
			if !repaired {
				return CommandOutput{Exit: 1, Stderr: "ImportError: corrupted stdlib"}
			}
			return CommandOutput{Stdout: pinned + "\n" + homeDir + "\n"}
		}
		env.handlers["/usr/local/bin/uv python"] = func(_ string, _ []string, argv []string) CommandOutput {
			if !slices.Contains(argv, "--reinstall") {
				t.Errorf("repair must pass --reinstall so uv replaces broken dir: %v", argv)
			}
			repaired = true
			return CommandOutput{Exit: 0}
		}

		r := CheckPython(ctx, DoctorOptions{
			DataDir:              dataDir,
			TempDir:              t.TempDir(),
			LookPath:             env.lookPath,
			RunCommand:           env.run,
			AuthTimeout:          time.Second,
			PythonInstallTimeout: time.Second,
		})
		if r.Status != onboard.OK || !repaired {
			t.Fatalf("broken repair = %+v, repaired=%v", r, repaired)
		}
	})

	t.Run("missing python with NoRepair fails without calling uv", func(t *testing.T) {
		dataDir := t.TempDir()
		env := newFakeEnv("uv")
		r := CheckPython(ctx, DoctorOptions{
			DataDir:     dataDir,
			NoRepair:    true,
			TempDir:     t.TempDir(),
			LookPath:    env.lookPath,
			RunCommand:  env.run,
			AuthTimeout: time.Second,
		})
		if r.Status != onboard.Fail || len(env.calls) != 0 {
			t.Fatalf("NoRepair = %+v, calls = %v", r, env.calls)
		}
	})

	t.Run("missing python and missing uv prints uv install fix", func(t *testing.T) {
		dataDir := t.TempDir()
		env := newFakeEnv()
		r := CheckPython(ctx, DoctorOptions{
			DataDir:     dataDir,
			TempDir:     t.TempDir(),
			LookPath:    env.lookPath,
			RunCommand:  env.run,
			AuthTimeout: time.Second,
		})
		if r.Status != onboard.Fail || !strings.Contains(r.Fix, "astral.sh/uv/install.sh") {
			t.Fatalf("missing uv = %+v", r)
		}
	})

	t.Run("uv exit 0 without valid python still fails verification", func(t *testing.T) {
		dataDir := t.TempDir()
		env := newFakeEnv("uv")
		env.handlers["/usr/local/bin/uv python"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Exit: 0}
		}
		r := CheckPython(ctx, DoctorOptions{
			DataDir:              dataDir,
			TempDir:              t.TempDir(),
			LookPath:             env.lookPath,
			RunCommand:           env.run,
			AuthTimeout:          time.Second,
			PythonInstallTimeout: time.Second,
		})
		if r.Status != onboard.Fail || !strings.Contains(r.Detail, "verification failed") {
			t.Fatalf("unverified uv install = %+v", r)
		}
	})
}
