package hctest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// GitExec emulates machine.exec for project commands in a fixture checkout.
func GitExec(p hc.ExecParams) (hc.ExecResult, error) {
	dir, err := filepath.EvalSymlinks(p.Cwd)
	if err != nil {
		return hc.ExecResult{}, err
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return hc.ExecResult{}, err
	}
	rel, err := filepath.Rel(tmp, dir)
	if err != nil || !filepath.IsLocal(rel) || len(p.Argv) == 0 ||
		p.Timeout == nil || *p.Timeout <= 0 {
		return hc.ExecResult{}, fmt.Errorf("unexpected fixture git request in %s", dir)
	}
	prog := p.Argv[0]
	switch p.Argv[0] {
	case "/bin/sh":
		if len(p.Argv) < 4 || p.Argv[1] != "-c" || p.Argv[3] != "sheepdog-project-path" {
			return hc.ExecResult{}, fmt.Errorf("unexpected fixture shell request")
		}
	case "git":
		if p.Env["GIT_TERMINAL_PROMPT"] != "0" || p.Env["GIT_ASKPASS"] != "" {
			return hc.ExecResult{}, fmt.Errorf("fixture git did not disable prompts")
		}
	case "gh":
		bin, err := exec.LookPath("gh")
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
				return hc.ExecResult{}, &hc.Error{
					Code:    -32000,
					Kind:    "spawn_failed",
					Message: fmt.Sprintf("cannot start %s: No such file or directory (os error 2)", prog),
				}
			}
			return hc.ExecResult{}, err
		}
		bin, err = filepath.EvalSymlinks(bin)
		rel, relErr := filepath.Rel(tmp, bin)
		if err != nil || relErr != nil || !filepath.IsLocal(rel) ||
			p.Env["GH_PROMPT_DISABLED"] != "1" || p.Env["GH_NO_UPDATE_NOTIFIER"] != "1" {
			return hc.ExecResult{}, fmt.Errorf("fixture gh requires a temporary binary and disabled prompts")
		}
		p.Argv[0] = bin
	default:
		return hc.ExecResult{}, fmt.Errorf("unexpected fixture executable %q", p.Argv[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*p.Timeout*float64(time.Second)))
	defer cancel()
	cmd := exec.CommandContext(ctx, p.Argv[0], p.Argv[1:]...)
	cmd.Dir, cmd.WaitDelay = dir, time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + tmp, "TMPDIR=" + tmp, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_ALLOW_PROTOCOL=file"}
	for k, v := range p.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	// Fixtures map a synthetic GitHub remote to a local bare repository.
	// Expose its GitHub URL to identity checks while keeping file transport.
	if err == nil && len(p.Argv) > 2 && p.Argv[0] == "git" && p.Argv[1] == "remote" && p.Argv[2] == "get-url" {
		urls := strings.Split(stdout.String(), "\n")
		for i, target := range urls {
			if !filepath.IsAbs(target) {
				continue
			}
			config := exec.CommandContext(ctx, "git", "config", "--get", "url."+target+".insteadOf")
			config.Dir, config.Env = dir, cmd.Env
			if raw, err := config.Output(); err == nil && strings.TrimSpace(string(raw)) == "https://github.com/acme/demo.git" {
				urls[i] = strings.TrimSpace(string(raw))
			}
		}
		stdout.Reset()
		stdout.WriteString(strings.Join(urls, "\n"))
	}
	r := hc.ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), TimedOut: ctx.Err() != nil}
	if len(r.Stdout) > 8<<20 || len(r.Stderr) > 1<<20 {
		r.Stdout, r.Stderr, r.Truncated = r.Stdout[:min(len(r.Stdout), 8<<20)], r.Stderr[:min(len(r.Stderr), 1<<20)], true
	}
	if cmd.ProcessState == nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return r, &hc.Error{
				Code:    -32000,
				Kind:    "spawn_failed",
				Message: fmt.Sprintf("cannot start %s: No such file or directory (os error 2)", prog),
			}
		}
		return r, err
	}
	if status := cmd.ProcessState.Sys().(syscall.WaitStatus); status.Signaled() {
		signal := int(status.Signal())
		r.Signal = &signal
	} else {
		code := cmd.ProcessState.ExitCode()
		r.Code = &code
	}
	return r, nil
}
