// Package world holds the engine's executors and observers over herdr-core and
// git. It makes no policy decisions (docs/dev/engine-v2/design.md 9.4, 13).
package world

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// RunSpec describes one terminal run to launch in herdr-core.
type RunSpec struct {
	Name, Cwd, Prompt, ClaudeConfig string
	Argv                            []string
	Labels                          map[string]string
}

// ClaudeArgv returns the launch argv for Claude Code across the three roles,
// stating model, effort and permission mode explicitly, skipping the bypass
// confirmation dialog in bypass modes, and disabling the auto-updater
// (KB-LCH-2, KB-LCH-4, KB-LCH-9, KB-LCH-11).
func ClaudeArgv(role, model, effort, permissionMode string) []string {
	model, effort, permissionMode = strings.TrimSpace(model), strings.TrimSpace(effort), strings.TrimSpace(permissionMode)
	if (role != flow.RoleCoordinator && role != flow.RoleWorker && role != flow.RoleReviewer) ||
		model == "" || effort == "" || permissionMode == "" {
		return nil
	}
	argv := []string{"env", "DISABLE_AUTOUPDATER=1", "claude", "--model", model, "--effort", effort, "--permission-mode", permissionMode}
	if permissionMode == "bypassPermissions" {
		argv = append(argv, "--settings", `{"skipDangerousModePermissionPrompt":true}`)
	}
	return argv
}

// StartRun launches spec in herdr-core and returns its terminal address.
func StartRun(ctx context.Context, client *hc.Client, spec RunSpec) (string, error) {
	if client == nil || strings.TrimSpace(spec.Cwd) == "" || len(spec.Argv) == 0 {
		return "", errors.New("world: StartRun requires hc client, Cwd and Argv")
	}
	if spec.Name != "" {
		if t, err := client.Get(ctx, spec.Name); err == nil {
			if t.State == "live" {
				return t.Address, nil
			}
			if _, err := client.Remove(ctx, t.Address); err != nil && !errors.Is(err, hc.ErrNotFound) {
				return "", err
			}
		}
	}
	if isClaudeArgv(spec.Argv) {
		if err := preAcceptClaudeTrust(spec.ClaudeConfig, spec.Cwd); err != nil {
			return "", err
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
		NoParent: true, NoNotify: true,
	})
	return t.Address, err
}

func isClaudeArgv(argv []string) bool {
	for i, a := range argv {
		if (i == 0 && filepath.Base(a) == "env") || (strings.Contains(a, "=") && !strings.HasPrefix(a, "-")) {
			continue
		}
		return filepath.Base(a) == "claude"
	}
	return false
}

func defaultClaudeConfig() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	if home, _ := os.UserHomeDir(); home != "" {
		return filepath.Join(home, ".claude.json")
	}
	return ""
}

func preAcceptClaudeTrust(configPath, cwd string) error {
	path := cmp.Or(strings.TrimSpace(configPath), defaultClaudeConfig())
	if path == "" {
		return nil
	}
	if testing.Testing() {
		absCfg, err := filepath.Abs(path)
		if rel, rerr := filepath.Rel(os.TempDir(), absCfg); err != nil || rerr != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("world: %s is outside the temp dir under go test", path)
		}
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil
	}
	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if !json.Valid(data) || dec.Decode(&root) != nil || root == nil {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return nil
	}
	projects, ok1 := childMap(root, "projects")
	entry, ok2 := childMap(projects, absCwd)
	if !ok1 || !ok2 {
		return nil
	}
	entry["hasTrustDialogAccepted"] = true
	projects[absCwd] = entry
	root["projects"], root["hasCompletedOnboarding"] = projects, true
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".claude.json.tmp*")
	if err != nil {
		return nil
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(append(data, '\n')); err == nil && f.Chmod(0o600) == nil && f.Sync() == nil && f.Close() == nil {
		_ = os.Rename(f.Name(), path)
	} else {
		_ = f.Close()
	}
	return nil
}

func childMap(parent map[string]any, key string) (map[string]any, bool) {
	raw, ok := parent[key]
	if !ok || raw == nil {
		return map[string]any{}, true
	}
	m, ok := raw.(map[string]any)
	return m, ok
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
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
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
