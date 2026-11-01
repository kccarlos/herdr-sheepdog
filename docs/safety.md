# safety

**What sheepdog does on its own, and what it never does.**

- **Approvals need your words.** A worker's approval prompt is answered only on
  your own recorded words (or a tap) in that turn, with a key from the prompt's
  own options. The code enforces this rail, not a prompt.
- **Instructions go out without a confirmation step.** The orchestrator sends
  an instruction right after echoing it back, so the worker's own approval
  prompts are the safety gate.
- **Investigations are read-only.** Background questions run a separate,
  read-only invocation of the agent's CLI.
- **Stop works at once.** A bare "stop" (or "wait", "cancel") is acknowledged
  by the daemon and interrupts what is in flight. "Never mind" is not a stop.
- **Network:** loopback by default, a token for anything else, no TLS. See
  [remote-access.md](remote-access.md).
- **Measured, not assumed:** "Safe" (no action beyond your words) is one of four
  numbers every change is gated on ([dev/eval/README.md](dev/eval/README.md)).

## Never run workers fully auto-approve

The worker's own approval prompts are the only gate between what you say and
changed code. Do not point sheepdog at an agent started with one of these:

| Agent | Flags to avoid |
|---|---|
| Codex | `--full-auto`, `-a never` (or any config that disables approval prompts). `codex --sandbox read-only -a on-request` prompts for file changes and commands. |
| Claude Code | `--dangerously-skip-permissions`, `--permission-mode bypassPermissions` |
| agy (Antigravity CLI) | `--dangerously-skip-permissions`, `--mode accept-edits` |
| anything else herdr supports | its own "skip all approvals" flag |

## Known limit

[herdr-tui](https://github.com/herdrdev/herdr) 0.9.0 does not report agy
(Antigravity CLI) prompts as blocked, so
sheepdog cannot tell you an agy agent is waiting.
