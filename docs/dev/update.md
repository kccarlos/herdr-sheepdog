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

## The panel after an upgrade

Code: `internal/panelsync` (the logic), `cmd/sheepdog/panel.go` (the daemon's tracker),
`cmd/sheepdog/setup_cmd.go` (the marker), `mods/sheepdog/model/panel-update.js` (the notice). The
user's view is [desktop.md](../desktop.md#updating-the-panel); the key is `panel.auto_update` in
[CONFIG.md](../../CONFIG.md#panel).

The daemon binary carries the panel and the layout it was built with: the root package's
`herdrsheepdog.Panel` (an `embed.FS` of `mods/sheepdog`, runtime files only) and
`herdrsheepdog.Layout` (`sheepdog-layout/layouts/sheepdog.json`). `mods/sheepdog` is the one source;
`make sync-layout` still produces the copy herdr.dev lists, and `TestEmbeddedPanelMatchesTheModFolder`
fails if the embed and the folder disagree or a runtime file is not embedded.

**The marker.** `sheepdog setup` (when it installs the panel, or finds it identical to its own) writes
`<herdr-app home>/mods/sheepdog/.sheepdog-install.json`:

```json
{ "version": "v1.2.3", "files": { "main.js": "<sha256>", "model/store.js": "<sha256>" }, "layout": "<sha256>" }
```

`files` is every embedded file as written; `layout` is the hash of `layouts/sheepdog.json` when setup
wrote it (absent when the user's own layout was kept). If any file on disk differs from what the
binary ships, no marker is written. herdr-app tolerates the extra dotfile (`herdr-app check` passes).
`version` is for display; whether an update exists is decided by file hashes, so a development build
(`dev`) works too.

**At startup** (`panelTracker.Sync`, a fresh process after the restart for an upgrade; `$HERDR_APP_HOME`
or `~/.config/herdr-app`), with `panel.auto_update` on:

- No `mods/sheepdog` folder: nothing happens, nothing is created.
- Marker present, every file listed in it still matches, and the binary's files differ: the folder is
  replaced. The new tree is built in a temporary directory in the herdr-app home (same file system),
  files that are not ours (`gpui-kit.d.ts`, `jsconfig.json`, `.gitignore`) are carried over, files the
  new version dropped are not, and two renames swap it in (the old folder is put back if the second
  fails). `panel.updated {from, to, files, layout}` is logged; a failure logs `panel.update_failed`
  and leaves the old panel.
- The layout is replaced the same way (a temporary file and a rename) when its hash is still the
  marker's; a layout the user edited is left alone, with no notice, and the marker's `layout` hash
  follows a replacement.
- A changed file, a missing file, no marker, a marker that does not parse, or a symlinked folder (a
  development checkout): nothing is written.

**`/api/state`** has `panel` `{installed, version, modified, update_available, auto_update}`
(re-read from disk at most every 10 s, so it follows a user's edits and `sheepdog setup`). The panel
shows "A newer panel is available" when `update_available && (modified || !auto_update)`. A
panel with no marker that is identical to the binary's reports `modified: false, update_available:
false`. `sheepdog status` prints the same as a `panel` line. The panel does not read its own version:
the daemon compares the files, which is the same fact and also covers a panel herdr-app has not
reloaded yet.

Tests never touch the real herdr-app: `internal/panelsync` tests use temp homes, and the `cmd/sheepdog`
`TestMain` points `HERDR_APP_HOME` at a temp directory.
