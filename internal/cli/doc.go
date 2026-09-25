// Package cli implements the read-only CLI query tool of plan §3.5 (WP4):
// detecting codex/claude on PATH and running one of them, non-interactively
// and read-only, to answer a question about an agent's workspace.
//
// Both backends keep their own session, so a follow-up can resume the
// same sub-agent (Query.Session and the resume templates): the backend
// is the persistence layer, not sheepdog (docs/roles.md
// "Investigations and explanations"). The session id comes out of the
// JSON output — codex's thread.started, claude's session_id.
//
// A worker's own session is asked through a fork (Query.Fork): claude
// --resume --fork-session, codex exec fork. Both answer from the
// worker's memory and write only a new session of their own, never into
// the one the worker's TUI has open (docs/cli-query-tools.md §2c); the
// fork's id is what a follow-up resumes.
//
// Runner implements supervisor.Querier. It is deliberately not a
// supervisor: the only path that changes a worker's files is
// herdr.Client.Prompt / SendKeys against a worker pane (decision D8). The
// only guarantee that a CLI invocation does not write to the workspace is
// the CLI's own sandboxing (read-only mode for codex, a reduced tool list
// for claude) — Runner does not and cannot enforce this itself; it only
// arranges the invocation per docs/cli-query-tools.md and bounds it with a
// timeout and an output cap.
//
// Both backends are launched with a scrubbed environment (no HERDR_* and
// no CLAUDECODE / CLAUDE_CODE_* variables, so the child's own herdr/Claude
// Code integration hooks do not report a bogus session for the daemon's
// own pane) and in their own process group, so a timeout kills the whole
// tree rather than leaving orphaned children behind.
package cli
