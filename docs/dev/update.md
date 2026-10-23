# Update notices and the restart after an upgrade

Code: `internal/update` (the check, the cache, the watcher), `cmd/sheepdog/updater.go`
(wiring, CLI notices, the restart), `cmd/sheepdog/serve.go` (the restart branch of the serve
loop), `web/static/update-banner.js` (the pages). The user's view is
[quick-start.md](../quick-start.md#update); the keys are `update.check` and
`update.auto_restart` in [CONFIG.md](../../CONFIG.md).

## The check

The daemon asks `GET api.github.com/repos/kccarlos/herdr-sheepdog/releases?per_page=30` 30 s
after it starts and then every 24 h (`update.Run`). `update.Pick` keeps plain `vX.Y.Z` tags only:
`ios-v*` tags (the iPhone app's releases), drafts and prereleases are ignored, so the newest daemon
release is found even when an app release is the newest overall. The answer goes to
`<data_dir>/update-check.json`; a cache younger than a day is used as it is, so a daemon that
restarts often asks GitHub at most once a day. A failed check keeps the last answer.
`update.Compute` compares with the running version: a development build (`dev`) or a pseudo
version never reports an update.

`/api/state` has the result as `update` ([ws-contract.md](ws-contract.md)); `sheepdog status` reads
it from there (from the cache when the daemon is down). Commands that end with a notice (`setup`,
`init`, `doctor`, `pair`, `start`, `open`) print one line about a newer release at most once a day
(`<data_dir>/update-notice.json`), read from the cache: only the daemon and `sheepdog update` ask
GitHub. They also print, every time, "The running sheepdog is vA; vB is installed" when
`/api/hello` shows a daemon older than the CLI.

## The restart

`update.Watch` snapshots the program the daemon was started from at startup: the user-facing path
(argv[0] made absolute, or looked up on PATH), its symlinks resolved, with device, inode, size and
mtime. A Homebrew upgrade retargets the `bin/sheepdog` link to a new Cellar directory; a herdr plugin
update installs into a new folder that `~/.local/bin/sheepdog` links to; both change the resolved
identity. A path that does not resolve at the moment (an upgrade in progress) is not a change.

`update.Restarter` checks every minute. On a change it runs `<launch path> version`, shows
`restart_pending` (and `restart_to`) in `/api/state`, and, with `update.auto_restart` on, restarts
once `App.Idle()` and the setup demo say the daemon is idle. Idle means none of: a voice client
(browser or phone) connected to `/ws`; an orchestrator turn or its after-turn work running
(`brain.Busy`); an approval question open (`pending_action`); the setup page's demo running.

How it restarts (`restartForUpdate`):

- **Under the login service** (`XPC_SERVICE_NAME` is the launchd label, or systemd's
  `INVOCATION_ID` is set): shut down cleanly and exit with code 75. launchd's `KeepAlive` restarts
  after any exit (10 s throttle); systemd's `Restart=on-failure` needs a non-zero exit, hence 75
  (EX_TEMPFAIL). The unit files did not change, so installed services keep working.
- **Otherwise** (`sheepdog start`, `sheepdog serve` in a terminal): release the single-instance lock,
  flush the log and `syscall.Exec` the launch path with `serve`, `--env-file <absolute path of the
  env file this run loaded>` (an `--env-file` already in the arguments wins) and the same other
  arguments, in the same working directory, with the original environment. The explicit path means a
  `./.env` that was found implicitly is not looked up again. Same pid. A terminal run prints a line first. `daemon.restart {from, to,
  via}` is logged when the restart is asked for.

No loop: `<data_dir>/update-restart.json` records the target; a daemon never restarts into the same
`version` line again within 30 minutes. A new program that reports the same version line as the
running one, or that cannot report one after five tries, or an `exec` that fails (the daemon takes the
lock back and carries on), leaves only the notice.
