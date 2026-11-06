# Claude PermissionRequest hook: dropped

KC dropped this on 2026-11-05. A report-only hook reached 535 non-test lines against a 200 limit. To avoid showing the wrong command, a report could only be trusted when the whole command was already on screen, so the hook added nothing for Bash and still meant changing users' `~/.claude/settings.json`.

The real cause of cut-off commands was `promptTailLines = 40` in `internal/herdr/prompt.go`, which is fixed directly instead.

Reconsider only if Edit diffs or plans are needed on the phone and can't be read from the screen. The unmerged attempt is on branch `feat/permission-hook`.
