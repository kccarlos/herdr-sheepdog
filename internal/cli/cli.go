package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// Backend names one of the two supported read-only query tools.
type Backend string

// Supported backends.
const (
	Codex  Backend = "codex"
	Claude Backend = "claude"
)

// Errors callers can branch on with errors.Is.
var (
	// ErrUnavailable is returned when no backend is detected (or the one
	// forced by --backend / cli.prefer is not on PATH).
	ErrUnavailable = errors.New("cli: no query backend available")
	// ErrBusy is returned when a query is already running for the same
	// workspace; the caller should not block waiting for it.
	ErrBusy = errors.New("cli: a query is already running for this workspace")
	// ErrNotForked is returned when a fork run reported the session it
	// was asked to fork: the backend continued the worker's own session
	// instead of copying it.
	ErrNotForked = errors.New("cli: the backend did not fork the session")
	// ErrTimedOut is wrapped by the error of a run killed at its time
	// limit.
	ErrTimedOut = errors.New("timed out")
	// ErrBudget matches a run claude stopped at its --max-budget-usd
	// (subtype error_max_budget_usd).
	ErrBudget = errors.New("cli: the run reached its cost cap")
)

// ExitError is a run whose process exited non-zero, or (Code 0) one
// whose output reported an error.
type ExitError struct {
	Backend Backend
	Code    int
	Stderr  string
	// Subtype and Reported are what the backend's JSON output said went
	// wrong (claude: subtype, errors, result): claude exits 1 with an
	// empty stderr when it stops at its budget, so without them the
	// error read "cli: claude: exit 1: " (2026-10-08 00:17).
	Subtype  string
	Reported string
}

func (e *ExitError) Error() string {
	why := e.Stderr
	switch {
	case why == "":
		why = e.Reported
	case e.Reported != "":
		why = e.Reported + "; stderr: " + why
	}
	if e.Code == 0 {
		return fmt.Sprintf("cli: %s reported an error: %s", e.Backend, why)
	}
	return fmt.Sprintf("cli: %s: exit %d: %s", e.Backend, e.Code, why)
}

// Is matches ErrBudget for a run stopped at its cost cap.
func (e *ExitError) Is(target error) bool {
	return target == ErrBudget && e.Subtype == "error_max_budget_usd"
}

// ExitCode is the process's exit code.
func (e *ExitError) ExitCode() int { return e.Code }

// Default config values (plan §3.5, docs/dev/cli-query-tools.md §3).
const (
	DefaultTimeout        = 60 * time.Second
	DefaultMaxOutputBytes = 16 << 10
	// DefaultClaudeModel is Haiku: cheap and fast enough for a bounded,
	// read-only "why did the tests fail" style question (WP4 cost rule).
	DefaultClaudeModel    = "claude-haiku-4-5-20251001"
	DefaultCodexReasoning = "low"
	DefaultPrefer         = "auto"
)

// defaultCodexCmd and defaultClaudeCmd are the verified argv templates from
// docs/dev/cli-query-tools.md §3. {question}, {cwd}, {model} and {reasoning}
// are substituted; nothing is ever passed through a shell. Both backends
// keep their session (codex writes it under ~/.codex/sessions, claude
// under ~/.claude/projects), so an investigation's follow-up can resume
// the same sub-agent through the resume templates below; the session id
// comes out of the JSON output either way.
var (
	defaultCodexCmd = []string{
		"codex", "exec",
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--color", "never",
		"-c", "model_reasoning_effort={reasoning}",
		"-C", "{cwd}",
		"--json",
		"{question}",
	}
	defaultClaudeCmd = []string{
		"claude", "-p", "{question}",
		"--model", "{model}",
		"--tools", "Read,Grep,Glob,WebSearch",
		"--allowedTools", "Read,Grep,Glob,WebSearch",
		"--strict-mcp-config",
		"--permission-mode", "dontAsk",
		"--permission-prompts", "none",
		"--output-format", "json",
		"--max-budget-usd", "0.50",
	}
	// The resume templates take {session} as well. `codex exec resume`
	// has no --sandbox, -C or --color of its own (verified 2026-09-21),
	// so the sandbox is set through -c and the working directory by the
	// child's cwd, which Runner sets anyway.
	defaultCodexResumeCmd = []string{
		"codex", "exec", "resume", "{session}", "{question}",
		"-c", "sandbox_mode=read-only",
		"-c", "model_reasoning_effort={reasoning}",
		"--skip-git-repo-check",
		"--json",
	}
	defaultClaudeResumeCmd = []string{
		"claude", "-p", "{question}",
		"--resume", "{session}",
		"--model", "{model}",
		"--tools", "Read,Grep,Glob,WebSearch",
		"--allowedTools", "Read,Grep,Glob,WebSearch",
		"--strict-mcp-config",
		"--permission-mode", "dontAsk",
		"--permission-prompts", "none",
		"--output-format", "json",
		"--max-budget-usd", "0.50",
	}
)

// The fork templates ask a worker's *own* session a side question
// without writing into it (docs/dev/cli-query-tools.md §2c, verified
// 2026-09-25): claude's --fork-session copies the history under a new
// session id, `codex exec fork` starts a new thread that reads the
// worker's rollout up to a byte offset. `codex exec resume` would append
// to the worker's live rollout, so it is never used on a worker. The
// argv is fixed, not configurable: the fork is only safe in this form.
//
// The claude fork has no --model: it continues the worker's
// conversation, which a long day makes larger than Haiku's window, so it
// runs on the user's default model; its budget cap is raised for the
// same reason: the first turn pays for the whole context, and with the
// fork's reduced tool list the prompt-cache prefix is not the worker's,
// so it is a cold cache write. Measured 2026-10-08 on a 13 h worker
// session (360 k tokens): the first turn cost about $2.8–3.0 (notional, on
// a subscription), past the old $2.00 cap, and claude, which checks the
// cap after a turn, threw away the finished answer and exited 1
// (error_max_budget_usd). $10 covers a cold turn on a full 1 M window
// plus a few tool rounds; it is a runaway guard, the time limit
// (brain.investigate_max_s) bounds the run as well, and a fork that
// still hits it falls back to a plain look (supervisor). A follow-up
// resumes the fork's own id through the resume templates above.
var (
	defaultClaudeForkCmd = []string{
		"claude", "-p", "{question}",
		"--resume", "{session}", "--fork-session",
		"--tools", "Read,Grep,Glob,WebSearch",
		"--allowedTools", "Read,Grep,Glob,WebSearch",
		"--strict-mcp-config",
		"--permission-mode", "dontAsk",
		"--permission-prompts", "none",
		"--output-format", "json",
		"--max-budget-usd", "10.00",
	}
	defaultCodexForkCmd = []string{
		"codex", "exec", "fork", "{session}", "{question}",
		"-c", "sandbox_mode=read-only",
		"-c", "model_reasoning_effort={reasoning}",
		"--skip-git-repo-check",
		"--json",
	}
)

// Config controls Runner. Zero values take the defaults above.
type Config struct {
	// Prefer picks the backend: "codex", "claude", or "auto" (codex if
	// detected, else claude, else ErrUnavailable). Default "auto".
	Prefer string
	// CodexBin / ClaudeBin are the executables looked up on PATH (or an
	// absolute path); they also replace argv[0] of the matching template.
	CodexBin, ClaudeBin string
	// CodexCmd / ClaudeCmd are argv templates; {question}, {cwd}, {model}
	// and {reasoning} are substituted. No shell is invoked.
	CodexCmd, ClaudeCmd []string
	// CodexResumeCmd / ClaudeResumeCmd are the argv templates of a
	// follow-up in an existing session; {session} is substituted as well.
	CodexResumeCmd, ClaudeResumeCmd []string
	// ClaudeModel fills {model} in ClaudeCmd.
	ClaudeModel string
	// CodexReasoning fills {reasoning} in CodexCmd.
	CodexReasoning string
	// Timeout bounds one invocation; the whole process group is killed on
	// expiry. Default 60s.
	Timeout time.Duration
	// MaxOutputBytes caps captured stdout; the tail is kept. Default 16 KiB.
	MaxOutputBytes int
	// Log receives cli.detected and cli.run events; nil discards.
	Log logging.Logger
}

func (c Config) withDefaults() Config {
	if c.Prefer == "" {
		c.Prefer = DefaultPrefer
	}
	if c.CodexBin == "" {
		c.CodexBin = "codex"
	}
	if c.ClaudeBin == "" {
		c.ClaudeBin = "claude"
	}
	if len(c.CodexCmd) == 0 {
		c.CodexCmd = defaultCodexCmd
	}
	if len(c.ClaudeCmd) == 0 {
		c.ClaudeCmd = defaultClaudeCmd
	}
	if len(c.CodexResumeCmd) == 0 {
		c.CodexResumeCmd = defaultCodexResumeCmd
	}
	if len(c.ClaudeResumeCmd) == 0 {
		c.ClaudeResumeCmd = defaultClaudeResumeCmd
	}
	if c.ClaudeModel == "" {
		c.ClaudeModel = DefaultClaudeModel
	}
	if c.CodexReasoning == "" {
		c.CodexReasoning = DefaultCodexReasoning
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if c.Log == nil {
		c.Log = logging.NopLogger{}
	}
	return c
}

// Detection is which backends were found on PATH at startup.
type Detection struct {
	Codex  bool
	Claude bool
}

// Detect looks up cfg.CodexBin and cfg.ClaudeBin (config can override the
// binary path, e.g. an absolute path) and logs cli.detected. Call it once
// at startup; Runner does not re-detect per query.
func Detect(cfg Config, log logging.Logger) Detection {
	cfg = cfg.withDefaults()
	if log == nil {
		log = logging.NopLogger{}
	}
	d := Detection{Codex: onPath(cfg.CodexBin), Claude: onPath(cfg.ClaudeBin)}
	log.Log("cli", "cli.detected", "", map[string]any{"codex": d.Codex, "claude": d.Claude})
	return d
}

func onPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
