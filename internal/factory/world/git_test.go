package world

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func fixtureGit(t *testing.T) *Git {
	t.Helper()
	srv := hctest.New(t)
	srv.OnExec = hctest.GitExec
	return NewGit(hc.NewClient(srv.Path), nil)
}

func TestGitLargePatchAndTruncation(t *testing.T) {
	repo := sizeRepo(t)
	tip := commitFile(t, repo, "large.txt", strings.Repeat("a line of patch data\n", 8192))
	args := []string{"diff", "--no-color", tip + "^", tip}
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	want, err := cmd.Output()
	if err != nil || len(want) <= 8<<10 {
		t.Fatalf("fixture patch size %d: %v", len(want), err)
	}
	got, err := fixtureGit(t).Run(context.Background(), repo, args...)
	if err != nil || !bytes.Equal(got.Stdout, want) {
		t.Fatalf("captured patch size %d, want %d: %v", len(got.Stdout), len(want), err)
	}

	srv := hctest.New(t)
	srv.OnExec = func(hc.ExecParams) (hc.ExecResult, error) {
		code := 0
		return hc.ExecResult{Code: &code, Stdout: []byte("partial patch"), Truncated: true}, nil
	}
	g := NewGit(hc.NewClient(srv.Path), nil)
	out, err := g.gitCmd(context.Background(), repo, args...)
	var bad *GitError
	if out != "" || !errors.As(err, &bad) || !bad.Result.Truncated || bad.RPC != nil {
		t.Fatalf("truncation = %q, %v", out, err)
	}
}
