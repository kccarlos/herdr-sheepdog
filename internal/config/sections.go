package config

// Prompts configures the prompt workspace (docs/dev/roles.md "Prompt files"):
// user overrides of the embedded assistant/SVP/USER prompt files.
type Prompts struct {
	// Dir is the override directory: <dir>/<same relative path as in the
	// prompts/ package> overrides the matching embedded file; a missing
	// file or directory is not an error, the embedded default is used.
	// Re-read on SIGHUP and POST /api/prompts/reload, not just at start.
	Dir string `toml:"dir"`
}

// Supervisor tunes how proactive the voice side is (docs/faq.md "How talkative is it?").
type Supervisor struct {
	// Chattiness is the initial push policy: "quiet" (agent questions and
	// exits only), "normal" (plus finishes of agents you dealt with
	// recently) or "chatty" (every finish and every pick-up of an
	// instruction). It can be changed at runtime from /debug or
	// PUT /api/settings; that choice is kept in the state file and wins
	// over this value on restart.
	Chattiness string `toml:"chattiness"`
	// RecentMinutes is the "normal" level's window: a finish is announced
	// when the agent was blocked, was sent something, was asked about or
	// was named in a spoken briefing within this many minutes.
	RecentMinutes int `toml:"recent_minutes"`
	// PromiseTTLS is how long a promise ("tell me when it's done") waits
	// for its event before it expires silently (default 7200, 2 h).
	PromiseTTLS int `toml:"promise_ttl_s"`
	// CancelWindowS is how soon after an instruction went to a working
	// agent a "stop" still interrupts it with Ctrl-C (docs/dev/roles.md
	// "Stop", phase 3; default 30).
	CancelWindowS int `toml:"cancel_window_s"`
	// RepromptPollS is how long the post-answer poll watches a pane after
	// keys go to it, in case it is already showing a different prompt
	// within herdr's own poll window with no state change in between
	// (docs/dev/roles.md "blocked -> blocked is a block"; default 10).
	RepromptPollS int `toml:"reprompt_poll_s"`
	// RepeatWindowS is how long an unchanged roster sentence counts as
	// "already reported": a status question inside it gets a "no change
	// since I last checked" note instead of the roster repeated in full
	// (docs/dev/history/issues-2026-09-18.md "Smaller items"; default 60).
	RepeatWindowS int `toml:"repeat_window_s"`
	// AuthoriseWindowS is how recent the user's own words (a voice
	// transcript, typed text or a tap) must be to authorise answering a
	// worker's approval prompt (docs/dev/roles.md "The one rail"; default 30).
	AuthoriseWindowS int `toml:"authorise_window_s"`
	// ProjectRoots are the directories a new agent may be started in
	// (docs/dev/roles.md "Starting an agent"): any immediate subdirectory of
	// one of them, or the directory of an existing herdr workspace. A
	// leading "~/" is the home directory (default ["~/Documents"]).
	ProjectRoots []string `toml:"project_roots"`
}

// ChattinessLevels are the accepted values of supervisor.chattiness.
var ChattinessLevels = []string{"quiet", "normal", "chatty"}

// DefaultChattiness is the built-in supervisor.chattiness.
const DefaultChattiness = "chatty"

// validChattiness reports whether v is one of ChattinessLevels.
func validChattiness(v string) bool {
	for _, l := range ChattinessLevels {
		if v == l {
			return true
		}
	}
	return false
}

// CLI configures the read-only CLI query tool (plan §3.5, WP4): codex
// and/or claude, invoked non-interactively and read-only to answer
// questions the orchestrator cannot answer from a screen alone, and to run a
// background investigation in a worker's repository. CodexCmd and
// ClaudeCmd are argv templates ({question}, {cwd}, {model} and
// {reasoning} are substituted), the *ResumeCmd ones take {session} too;
// no shell is invoked. See docs/dev/cli-query-tools.md for why each flag is
// there.
type CLI struct {
	// Prefer picks the backend: "codex", "claude" or "auto" (codex if
	// detected on PATH, else claude, else the deep-ask path is skipped).
	Prefer string `toml:"prefer"`
	// CodexBin / ClaudeBin are looked up on PATH, or may be an absolute
	// path; they also become argv[0] of the matching *Cmd template.
	CodexBin  string `toml:"codex_bin"`
	ClaudeBin string `toml:"claude_bin"`
	// CodexCmd / ClaudeCmd are the argv templates; TOML-only (like
	// [aliases], not a single scalar an env var could sanely hold).
	CodexCmd  []string `toml:"codex_cmd"`
	ClaudeCmd []string `toml:"claude_cmd"`
	// CodexResumeCmd / ClaudeResumeCmd are the argv templates of an
	// investigation's follow-up: the same read-only invocation with
	// {session} — the session id the first run reported — substituted as
	// well (docs/dev/cli-query-tools.md §3).
	CodexResumeCmd  []string `toml:"codex_resume_cmd"`
	ClaudeResumeCmd []string `toml:"claude_resume_cmd"`
	// ClaudeModel fills {model} in ClaudeCmd (default a cheap Haiku id).
	ClaudeModel string `toml:"claude_model"`
	// CodexReasoning fills {reasoning} in CodexCmd (default "low").
	CodexReasoning string `toml:"codex_reasoning"`
	// TimeoutS bounds one invocation; the process group is killed on
	// expiry (default 60).
	TimeoutS int `toml:"timeout_s"`
	// MaxOutputKB caps captured stdout; the tail is kept (default 16).
	MaxOutputKB int `toml:"max_output_kb"`
}

// Log configures the daemon's runtime log, <data_dir>/logs/sheepdog.log
// (CONFIG.md "What sheepdog stores"). Secrets are always redacted.
type Log struct {
	// Level is debug, info (default), warn or error: which events are
	// mirrored into the runtime log as text lines (logging.EventMirror).
	Level string `toml:"level"`
	// MaxSizeMB is the size at which the log is rotated (default 10).
	MaxSizeMB int `toml:"max_size_mb"`
	// MaxFiles is how many rotated files are kept next to it (default 3).
	MaxFiles int `toml:"max_files"`
}

// Events configures the event log, <data_dir>/events/YYYY-MM-DD.jsonl,
// which holds transcripts of what the user said.
type Events struct {
	// RetentionDays is how many days of event files the daemon keeps; older
	// day files are deleted at startup and once a day. 0 keeps them
	// forever. Default 14.
	RetentionDays int `toml:"retention_days"`
}

// Update configures the daily update check and the restart after an
// upgrade (docs/dev/update.md).
type Update struct {
	// Check turns on the check for a newer release once a day: one GET to
	// api.github.com, nothing personal sent. Default true.
	Check bool `toml:"check"`
	// AutoRestart restarts the daemon by itself when its executable on
	// disk changed (after an upgrade) and nobody is using it. Default true.
	AutoRestart bool `toml:"auto_restart"`
}

// Computer names this computer for the apps (GET /api/hello).
type Computer struct {
	// Name is what the app calls this computer; empty means the
	// system's computer name (macOS ComputerName, else the host name).
	Name string `toml:"name"`
}

// LogLevels are the valid log.level values.
var LogLevels = []string{"debug", "info", "warn", "error"}

// Herdr configures the herdr socket client.
type Herdr struct {
	// Socket is the path of the herdr unix socket.
	Socket string `toml:"socket"`
	// Bin is the herdr binary the phone's terminal windows run
	// (`herdr terminal session`); empty finds `herdr` on PATH, then
	// ~/.local/bin/herdr.
	Bin string `toml:"bin"`
}

// State configures the persisted state table.
type State struct {
	// Path is the JSON file the state table is persisted to.
	Path string `toml:"path"`
	// FullFsync turns on the hardware-durable fsync pass after every
	// persist (debounced; see internal/state). Off by default: on macOS
	// the durable path is F_FULLFSYNC, which is dramatically slower than
	// the plain fsync used otherwise. Turn it on only if you need the
	// state file to survive a power loss, not just a process crash.
	FullFsync bool `toml:"full_fsync"`
}

// defaultCLI mirrors the argv templates verified in
// docs/dev/cli-query-tools.md §3. {question}, {cwd}, {model} and {reasoning}
// are substituted by internal/cli; no shell is invoked.
func defaultCLI() CLI {
	return CLI{
		Prefer:    "auto",
		CodexBin:  "codex",
		ClaudeBin: "claude",
		CodexCmd: []string{
			"codex", "exec",
			"--sandbox", "read-only",
			"--skip-git-repo-check",
			"--color", "never",
			"-c", "model_reasoning_effort={reasoning}",
			"-C", "{cwd}",
			"--json",
			"{question}",
		},
		ClaudeCmd: []string{
			"claude", "-p", "{question}",
			"--model", "{model}",
			"--tools", "Read,Grep,Glob,WebSearch",
			"--allowedTools", "Read,Grep,Glob,WebSearch",
			"--strict-mcp-config",
			"--permission-mode", "dontAsk",
			"--permission-prompts", "none",
			"--output-format", "json",
			"--max-budget-usd", "0.50",
		},
		CodexResumeCmd: []string{
			"codex", "exec", "resume", "{session}", "{question}",
			"-c", "sandbox_mode=read-only",
			"-c", "model_reasoning_effort={reasoning}",
			"--skip-git-repo-check",
			"--json",
		},
		ClaudeResumeCmd: []string{
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
		},
		ClaudeModel:    "claude-haiku-4-5-20251001",
		CodexReasoning: "low",
		TimeoutS:       60,
		MaxOutputKB:    16,
	}
}
