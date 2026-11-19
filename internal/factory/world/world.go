// Package world holds the engine's executors and observers over herdr-core and
// git. It makes no policy decisions (docs/dev/engine-v2/design.md 9.4, 13).
package world

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// RunSpec describes one terminal run to launch in herdr-core.
type RunSpec struct {
	Name, Cwd, Prompt string
	Argv              []string
	Labels            map[string]string
}

// ClaudeArgv returns the launch argv for Claude Code across the three roles,
// stating model, effort and permission mode explicitly and disabling the
// auto-updater (KB-LCH-2, KB-LCH-4, KB-LCH-11).
func ClaudeArgv(role, model, effort, permissionMode string) []string {
	model, effort, permissionMode = strings.TrimSpace(model), strings.TrimSpace(effort), strings.TrimSpace(permissionMode)
	if (role != flow.RoleCoordinator && role != flow.RoleWorker && role != flow.RoleReviewer) ||
		model == "" || effort == "" || permissionMode == "" {
		return nil
	}
	return []string{"env", "DISABLE_AUTOUPDATER=1", "claude", "--model", model, "--effort", effort, "--permission-mode", permissionMode}
}

// StartRun launches spec in herdr-core and returns its terminal address.
func StartRun(ctx context.Context, client *hc.Client, spec RunSpec) (string, error) {
	if client == nil || strings.TrimSpace(spec.Cwd) == "" || len(spec.Argv) == 0 {
		return "", errors.New("world: StartRun requires hc client, Cwd and Argv")
	}
	if spec.Name != "" {
		if t, err := client.Get(ctx, spec.Name); err == nil && t.State == "live" {
			return t.Address, nil
		}
	}
	argv := append([]string(nil), spec.Argv...)
	if spec.Prompt != "" {
		argv = append(argv, spec.Prompt)
	}
	labels := map[string]string{"herdr.space": randID(), "herdr.tab": randID(), "herdr.seen": "0"}
	maps.Copy(labels, spec.Labels)
	t, err := client.Run(ctx, hc.RunParams{
		Name: spec.Name, Cwd: spec.Cwd, Argv: argv, Labels: labels,
		Env: map[string]string{"DISABLE_AUTOUPDATER": "1"}, NoParent: true, NoNotify: true,
	})
	return t.Address, err
}

// SendRun pastes text into terminal id and submits it.
func SendRun(ctx context.Context, client *hc.Client, id, text string) error {
	if client == nil || id == "" {
		return errors.New("world: SendRun requires hc client and terminal id")
	}
	_, err := client.Send(ctx, id, text, false)
	return err
}

// StopRun kills terminal id, removes it, and verifies both the terminal and its
// OS process group are gone (design.md 9.4).
func StopRun(ctx context.Context, client *hc.Client, id string) error {
	if id == "" {
		return nil
	}
	t, err := getTerminal(ctx, client, id)
	if errors.Is(err, hc.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	pid := 0
	if t.PID != nil && *t.PID > 1 && *t.PID != os.Getpid() && *t.PID != syscall.Getpgrp() {
		pid = *t.PID
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
	if _, err := client.Kill(ctx, id); err != nil && !errors.Is(err, hc.ErrNotFound) && !errors.Is(err, hc.ErrFinished) {
		return err
	}
	for i := 0; i <= 80; i++ {
		if _, rerr := client.Remove(ctx, id); rerr != nil && !errors.Is(rerr, hc.ErrNotFound) && !errors.Is(rerr, hc.ErrLive) {
			return rerr
		}
		_, gerr := client.Get(ctx, id)
		procGone := pid == 0 || (syscall.Kill(pid, 0) != nil && syscall.Kill(-pid, 0) != nil)
		if errors.Is(gerr, hc.ErrNotFound) && procGone {
			return nil
		}
		if i == 4 && pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("world: %s or pid %d still alive after stop", id, pid)
}

// RunState maps herdr-core's terminal state and OSC 7501 status to flow's
// idle | working | gone.
func RunState(ctx context.Context, client *hc.Client, id string) (flow.RunState, error) {
	if id == "" {
		return flow.RunGone, nil
	}
	t, err := getTerminal(ctx, client, id)
	if errors.Is(err, hc.ErrNotFound) || (err == nil && t.State != "live") {
		return flow.RunGone, nil
	} else if err != nil {
		return "", err
	}
	if t.Status != nil && (t.Status.State == "idle" || t.Status.State == "done" || t.Status.State == "error") {
		return flow.RunIdle, nil
	}
	return flow.RunWorking, nil
}

func getTerminal(ctx context.Context, client *hc.Client, id string) (hc.Terminal, error) {
	if client == nil {
		return hc.Terminal{}, errors.New("world: no hc client")
	}
	return client.Get(ctx, id)
}

// RepoProject resolves folder's git top-level directory and its project id in
// local git config, minting and writing one when init is true (design.md 3).
func RepoProject(ctx context.Context, folder string, init bool) (string, string, error) {
	root, err := gitCmd(ctx, folder, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("not a git repository (%s): %w", folder, err)
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	id, _ := gitCmd(ctx, root, "config", "--local", "--get", "sheepdog.projectId")
	if id == "." || id == ".." || filepath.Base(id) != id {
		id = ""
	}
	if id == "" && init {
		id = "proj-" + randID()
		_, err = gitCmd(ctx, root, "config", "--local", "sheepdog.projectId", id)
	}
	return root, id, err
}

func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(gctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func randID() string { return fmt.Sprintf("%08x", rand.Uint32()) }
