// Package cli implements the read-only CLI query tool: detection of `codex`
// and/or `claude` on PATH, and read-only, non-interactive, bounded
// invocation (cwd set to the agent's workspace, a timeout, and an output
// cap) whose result is passed through the cheap LLM's spoken_answer
// template. It may read a repo, diffs, logs, and the internet, but must
// never write; the only path that changes code is `herdr send` to a worker
// pane.
package cli
