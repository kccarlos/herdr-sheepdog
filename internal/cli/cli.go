package cli

import (
	"errors"
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
)

// Default config values (plan §3.5, docs/cli-query-tools.md §3).
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
// docs/cli-query-tools.md §3. {question}, {cwd}, {model} and {reasoning}
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
