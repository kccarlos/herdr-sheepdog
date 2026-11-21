package factory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func fixtureGit(t *testing.T) *world.Git {
	t.Helper()
	srv := hctest.New(t)
	srv.OnExec = hctest.GitExec
	return world.NewGit(hc.NewClient(srv.Path), nil)
}

func newGitTestEngine(t *testing.T, paths Paths, client *hc.Client) *Engine {
	t.Helper()
	e := New(paths, client)
	srv := hctest.New(t)
	srv.OnExec = hctest.GitExec
	e.git = world.NewGit(hc.NewClient(srv.Path), func(folder string) {
		e.logEvent("factory.git_suppressed", map[string]string{"folder": folder})
	})
	return e
}

func TestGitRPCFailureLeavesTaskFactsUnknown(t *testing.T) {
	l := newLanding(t)
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if p.Cwd == l.wt && slices.Contains(p.Argv, "diff") {
			return hc.ExecResult{}, errors.New("fixture lost the machine.exec reply")
		}
		return hctest.GitExec(p)
	}
	l.eng.git = world.NewGit(hc.NewClient(srv.Path), nil)
	l.pass()
	task := l.state().Tasks[0]
	if task.LastRow != "" || len(task.Recaps) != 0 || len(l.eng.tips) != 0 || l.eng.git.Suppressed(l.wt) {
		t.Fatalf("failed RPC changed task facts: row=%q, recaps=%v, tips=%v", task.LastRow, task.Recaps, l.eng.tips)
	}
	if !strings.Contains(l.log.String(), "fixture lost the machine.exec reply") {
		t.Fatalf("missing RPC failure log: %s", l.log)
	}
}

func TestHungGitDoesNotSkipHealthyProjectInSameTick(t *testing.T) {
	hung := newLanding(t)
	healthy := newLanding(t)
	eng := hung.eng
	for _, item := range []struct {
		id    string
		state flow.State
	}{
		{"a-hung", hung.state()},
		{"b-healthy", healthy.state()},
	} {
		state := item.state
		state.Project.ID = item.id
		if item.id == "b-healthy" {
			state.Tasks[0].Attempts = nil
		}
		if err := SaveState(eng.Paths.State(item.id), state); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(eng.Paths.State(hung.id)); err != nil {
		t.Fatal(err)
	}

	bin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte("#!/bin/sh\n"+
		"if [ -f .fixture-hung ]; then exec sleep 20; fi\nexec "+strconv.Quote(bin)+" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hung.wt, ".fixture-hung"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
	oldTimeout := lockGitTimeout
	t.Cleanup(func() { lockGitTimeout = oldTimeout })
	lockGitTimeout = 1200 * time.Millisecond

	eng.PassInterval, eng.wake = time.Hour, nil
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		eng.Run(ctx)
	}()
	defer func() { cancel(); <-done }()

	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("healthy project did not start an attempt on the first tick")
		case <-tick.C:
			s, err := LoadState(eng.Paths.State("b-healthy"))
			if err == nil && len(s.Tasks[0].Attempts) == 1 && s.Tasks[0].LastRow == "N11" {
				cancel()
				<-done
				if !eng.git.Suppressed(hung.wt) || strings.Count(hung.log.String(), "factory.git_suppressed") != 1 ||
					strings.Count(hung.log.String(), "git child timed out") != 1 {
					t.Fatalf("timeout or suppression missing: %s", hung.log)
				}
				eng.passProject(context.Background(), "a-hung", false)
				if strings.Count(hung.log.String(), "factory.git_suppressed") != 1 {
					t.Fatalf("suppression logged again: %s", hung.log)
				}
				return
			}
		}
	}
}
