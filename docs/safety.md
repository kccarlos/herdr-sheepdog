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
- **Network:** loopback by default; off this computer, TLS and paired devices
  only (each with its own P-256 key). See [remote-access.md](remote-access.md).
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

## Factory

Unlike interactive agents you manage by voice above, the unattended [factory](factory.md) launches its coordinators and workers without interactive tool-approval prompts (`claude --permission-mode bypassPermissions`, `codex --dangerously-bypass-approvals-and-sandbox`, `agy --dangerously-skip-permissions`). Because there is no interactive approval prompt in front of each command, the safety boundary shifts to isolated git worktrees, read-only reviewers with clean-tree checks, GitHub CI and branch protection, role tokens, and owner-proof gated asks enforced in code:

- **Owner proof for gated asks.** Gated ask kinds (`gate`, `release`, `override`, `paid-key`, `delete`, `install`, `reset`, `rule`) cannot be answered by the coordinator, a worker, typed chat text, or a ticketless local HTTP request. Each answer requires a verified in-process owner proof (stored in `proofs.json` for 24 hours) minted only by a paired-device tap (a paired phone or a paired local browser on `/factory`) or a live voice session.
- **No lock-screen quick-reply on factory pushes.** A push notification for a factory ask carries no Approve or Decline button, so you cannot accidentally approve an override or gate from the lock screen. Tap it to open the full card and pick an option, or say the option in a live voice session.
- **Role separation (`SHEEPDOG_RUN_TOKEN`).** Every factory run receives a random per-run token in its environment (never on its command line, in a file, or in a log; only its SHA-256 hash is kept in `state.json`). The daemon checks the token against the role table so a worker cannot start or stop a project, apply plans, submit reviews, or open gated asks. Because runs share your OS user, the token stops agent mistakes rather than a hostile local process; GitHub branch protection and the factory's acceptance checks are the backstops.
- **Commit-pinned overrides.** Override cards are filled by the engine from live git and GitHub state (task, PR, the reported commit, CI check state, and unreported work) and recommend **No, wait**. An override always needs your confirmation — it cannot be auto-approved by a rule. An approved override covers only that exact commit (or clean base merges ahead of it), spends itself and stops merging if CI checks get worse than what the card showed, and still respects merge conflicts, unmet dependencies, and one-merge-at-a-time ordering.
- **Start checks, clean trees, and proofs.** `sheepdog factory start` runs all start checks first and refuses on any `FAIL` ([factory.md](factory.md#requirements)). Workers must report ready from a clean worktree with passing proofs for every outcome. Claude and Codex reviewers run in read-only modes (`dontAsk` with read-only tools for Claude, `-s read-only -a never` for Codex); every reviewer (including `agy`) is checked for a clean worktree on verdict, and a dirty worktree voids the round and stops the reviewer.

## Known limit

[herdr-tui](https://github.com/herdrdev/herdr) 0.9.0 does not report agy
(Antigravity CLI) prompts as blocked, so
sheepdog cannot tell you an agy agent is waiting.
