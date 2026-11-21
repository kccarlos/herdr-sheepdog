package world

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func TestProjectPathProbes(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	present := filepath.Join(root, "present")
	alias := filepath.Join(root, "alias")
	locked := filepath.Join(root, "locked")
	loop := filepath.Join(root, "loop")
	for _, path := range []string{present, locked} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, dest := range map[string]string{alias: present, loop: loop, filepath.Join(root, "denied"): filepath.Join(locked, "child")} {
		if err := os.Symlink(dest, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	g := fixtureGit(t)
	for _, tc := range []struct {
		name, path, want string
		exists           bool
	}{
		{"present", present, present, true},
		{"present symlink", alias, present, true},
		{"absent suffix below symlink", alias + "//missing/child/", filepath.Join(present, "missing", "child"), false},
		{"denied parent", filepath.Join(locked, "child"), "", false},
		{"symlink into denied parent", filepath.Join(root, "denied"), "", false},
		{"loop", loop, "", false},
		{"child below loop", filepath.Join(loop, "child"), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if os.Geteuid() == 0 && strings.Contains(tc.name, "denied") {
				t.Skip("root can search the locked parent")
			}
			if exists, err := g.PathExists(ctx, root, tc.path); exists != tc.exists || (err != nil) != (tc.want == "") {
				t.Errorf("PathExists = %v, %v", exists, err)
			}
			if paths, err := g.CanonicalPaths(ctx, root, tc.path); paths[tc.path] != tc.want || (err != nil) != (tc.want == "") {
				t.Errorf("CanonicalPaths = %v, %v, want %q", paths, err, tc.want)
			}
		})
	}
}

func TestPathProbeUnknownHoldsRemovalAndSweep(t *testing.T) {
	for _, diagnostic := range []string{"Operation not permitted", "Input/output error", "RPC failure"} {
		t.Run(diagnostic, func(t *testing.T) {
			ctx := context.Background()
			repo := sizeRepo(t)
			path := filepath.Join(repo, "unavailable")
			srv := hctest.New(t)
			if diagnostic != "RPC failure" {
				bin := t.TempDir()
				stat := "#!/bin/sh\ncase \"$2\" in */unavailable) printf '%s\\n' 'stat: " + diagnostic + "' >&2; exit 1;; esac\nexec /usr/bin/stat \"$@\"\n"
				if err := os.WriteFile(filepath.Join(bin, "stat"), []byte(stat), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			}
			srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
				if p.Argv[0] == "/bin/sh" && diagnostic == "RPC failure" {
					return hc.ExecResult{}, errors.New(diagnostic)
				}
				if strings.Join(p.Argv, " ") == "git worktree prune" {
					t.Error("pruned after an unknown presence check")
				}
				return hctest.GitExec(p)
			}
			g := NewGit(hc.NewClient(srv.Path), nil, nil)
			if present, err := g.PathExists(ctx, repo, path); present || err == nil {
				t.Errorf("PathExists = %v, %v, want unknown", present, err)
			}
			if resolved, err := g.CanonicalPaths(ctx, repo, path); resolved != nil || err == nil {
				t.Errorf("CanonicalPaths = %v, %v, want unknown", resolved, err)
			}
			if removed, err := g.RemoveWorktree(ctx, repo, path); removed || err == nil {
				t.Errorf("RemoveWorktree = %v, %v, want hold", removed, err)
			}
			for _, probe := range g.Sweep(ctx, nil, repo, path, flow.State{Project: flow.Project{ID: "project"}}) {
				if (probe.Probe == "worktrees" || probe.Probe == "branches") && (probe.Unknown == "" || len(probe.Items) != 0) {
					t.Errorf("Sweep %s = %+v, want unknown", probe.Probe, probe)
				}
			}
		})
	}
}

func TestPathCommandsShareRepositoryBackoff(t *testing.T) {
	repo, worktree := t.TempDir(), t.TempDir()
	for _, first := range []string{"git", "path"} {
		t.Run(first, func(t *testing.T) {
			srv := hctest.New(t)
			srv.OnExec = func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{TimedOut: true}, nil
			}
			g := NewGit(hc.NewClient(srv.Path), nil, nil)
			ctx := GitRepository(context.Background(), repo)
			if first == "git" {
				_, _ = g.Run(ctx, repo, "status")
			} else {
				_, _ = g.PathExists(ctx, worktree, worktree)
			}
			_, gitErr := g.Run(ctx, repo, "status")
			_, pathErr := g.PathExists(ctx, worktree, worktree)
			if !errors.Is(gitErr, ErrGitSuppressed) || !errors.Is(pathErr, ErrGitSuppressed) || len(srv.Requests()) != 1 {
				t.Errorf("after %s stall: git=%v, path=%v, RPCs=%d; want both suppressed after one RPC", first, gitErr, pathErr, len(srv.Requests()))
			}
		})
	}
}
