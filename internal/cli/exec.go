package cli

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// stderrCapBytes bounds the stderr captured for the log/error message,
// independent of the (usually much larger) stdout cap.
const stderrCapBytes = 4 << 10

// stdoutHeadBytes is how much of the *start* of stdout is kept besides
// the tail. Codex announces the session on its first line and then
// streams every command's output, so a talkative run pushes
// thread.started out of the tail: without the head a follow-up could
// never resume the session (docs/dev/cli-query-tools.md §2b).
const stdoutHeadBytes = 4 << 10

// execResult is the raw outcome of one child process, before any
// backend-specific parsing.
type execResult struct {
	Stdout string
	// StdoutHead is the first stdoutHeadBytes of stdout, kept even when
	// the tail dropped them.
	StdoutHead   string
	StdoutCapped bool
	Stderr       string
	StderrCapped bool
	Exit         int  // -1 when the process never reported an exit code
	Killed       bool // the timeout (or ctx) fired and the process group was killed
	StartErr     error
}

// runCommand starts argv[0] with argv[1:] in dir, stdin closed, env
// scrubbed of HERDR_* / CLAUDECODE / CLAUDE_CODE_*, in its own process
// group. It blocks until the process exits or ctx is done, in which case
// the whole process group is killed (SIGKILL) so no grandchild survives.
func runCommand(ctx context.Context, argv []string, dir string, maxStdoutBytes int) execResult {
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv is built from config templates, not a shell
	cmd.Dir = dir
	cmd.Env = scrubEnv(os.Environ())
	cmd.Stdin = nil // exec.Cmd connects a nil Stdin to the null device
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	out := &tailBuffer{max: maxStdoutBytes, headMax: stdoutHeadBytes}
	errOut := &tailBuffer{max: stderrCapBytes}
	cmd.Stdout = out
	cmd.Stderr = errOut

	if err := cmd.Start(); err != nil {
		return execResult{StartErr: err, Exit: -1}
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var (
		waitErr error
		killed  bool
	)
	select {
	case <-ctx.Done():
		killed = true
		killGroup(cmd.Process.Pid)
		<-done // reap; ignore the (now-expected) killed error
	case waitErr = <-done:
	}

	res := execResult{
		Stdout:       out.String(),
		StdoutHead:   out.Head(),
		StdoutCapped: out.capped,
		Stderr:       errOut.String(),
		StderrCapped: errOut.capped,
		Killed:       killed,
		Exit:         -1,
	}
	if ps := cmd.ProcessState; ps != nil {
		res.Exit = ps.ExitCode()
	}
	if !killed && waitErr != nil && res.Exit < 0 {
		res.StartErr = waitErr
	}
	return res
}

// killGroup sends SIGKILL to the whole process group of pid (Setpgid made
// the child its own group leader, so -pid addresses the group).
func killGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// scrubEnv drops HERDR_*, CLAUDECODE and CLAUDE_CODE_* so the child's own
// herdr / Claude Code integration hooks do not see the daemon's session
// (docs/dev/cli-query-tools.md, WP4 environment rules).
func scrubEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if strings.HasPrefix(key, "HERDR_") || key == "CLAUDECODE" || strings.HasPrefix(key, "CLAUDE_CODE_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// renderArgv substitutes {name} placeholders (question, cwd, model,
// reasoning) in every element of tmpl. No shell is involved, so a
// question containing shell metacharacters is passed through literally.
func renderArgv(tmpl []string, vars map[string]string) []string {
	out := make([]string, len(tmpl))
	for i, a := range tmpl {
		for k, v := range vars {
			a = strings.ReplaceAll(a, "{"+k+"}", v)
		}
		out[i] = a
	}
	return out
}

// tailBuffer keeps the last max bytes written to it (newest data wins),
// so an unbounded child process cannot grow the daemon's memory and the
// most relevant (most recent) output survives truncation. With headMax
// set it also keeps the first headMax bytes, for the lines a backend
// only prints once, at the start (codex's thread id).
type tailBuffer struct {
	max     int
	headMax int
	head    []byte
	buf     []byte
	capped  bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := t.headMax - len(t.head); room > 0 {
		t.head = append(t.head, p[:min(room, n)]...)
	}
	t.buf = append(t.buf, p...)
	if t.max > 0 && len(t.buf) > t.max {
		t.capped = true
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return n, nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

// Head returns the first headMax bytes written, whether or not the tail
// still holds them.
func (t *tailBuffer) Head() string { return string(t.head) }

// elideRunes truncates s to at most n runes, appending "…" when it did
// (used to keep the logged question short — plan: "question elided to 80
// chars").
func elideRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
