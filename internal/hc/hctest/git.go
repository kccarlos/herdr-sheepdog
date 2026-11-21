package hctest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// GitExec emulates machine.exec for Git in a fixture checkout.
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
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || len(p.Argv) == 0 ||
		p.Argv[0] != "git" || p.Timeout == nil || *p.Timeout <= 0 || p.Env["GIT_TERMINAL_PROMPT"] != "0" {
		return hc.ExecResult{}, fmt.Errorf("unexpected fixture git request in %s", dir)
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
	r := hc.ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), TimedOut: ctx.Err() != nil}
	if len(r.Stdout) > 8<<20 || len(r.Stderr) > 1<<20 {
		r.Stdout, r.Stderr, r.Truncated = r.Stdout[:min(len(r.Stdout), 8<<20)], r.Stderr[:min(len(r.Stderr), 1<<20)], true
	}
	if cmd.ProcessState == nil {
		return r, err
	}
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		signal := int(status.Signal())
		r.Signal = &signal
	} else {
		code := cmd.ProcessState.ExitCode()
		r.Code = &code
	}
	return r, nil
}
