# Updates: the check, `sheepdog update` and the restart after an upgrade

Code: `internal/update` (the check, the cache, the installer, the watcher), `cmd/sheepdog/update.go`
(`sheepdog update`), `cmd/sheepdog/updater.go`
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

## `sheepdog update`

`runUpdate` checks the latest release (`update.Latest`, as the daemon does). When it is newer than a
released build, `update.Install` installs it for the install kind `detectInstall` reports, asking
nothing:

- **binary** (the installer, a manual download): `sheepdog_<tag>_<GOOS>_<GOARCH>.tar.gz` and
  `checksums.txt` from `/releases/download/<tag>/` (never `latest/`); the sha256 must match the line
  for that file, as in `scripts/install.sh`; the `sheepdog` file is staged in the executable's own
  directory, must answer `version` with the tag, and is renamed over the executable (a rename, never a
  copy into the old file, which would break the code signature on macOS). No signature check yet.
- **homebrew**: `brew update`, then `brew upgrade kccarlos/tap/sheepdog`.
- **herdr plugin**: `herdr plugin update --yes kccarlos.sheepdog` (without `--yes` herdr refuses a
  non-interactive stdin).
- **source** and a development build: the steps are printed; nothing is installed.

On macOS, Sheepdog.app is upgraded (`brew upgrade --cask kccarlos/tap/sheepdog-app`) only when
`brew list --cask kccarlos/tap/sheepdog-app` succeeds; otherwise brew is not run for it. A failed
cask upgrade is printed and does not stop the daemon's restart.

Then `finishUpdate` restarts a running daemon straight away with `sheepdog restart`'s code, whether
or not anyone is using it, when its `/api/hello` version differs from the tag just installed (or,
when there was nothing to download, from this CLI's version, so it finishes an upgrade made another
way). It ends with one line naming the version the daemon reports after the restart (exit 1 when that
is not the expected one), or the installed program's version when no daemon runs. `update.Install` does not restart, so the daemon can call it too.

`SHEEPDOG_RELEASE_API` replaces `https://api.github.com` and `SHEEPDOG_RELEASE_URL` the download
directory, for a test release server.

## The restart after an upgrade made another way

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
{
  "version": "v1.2.3",
  "files": { "main.js": "<sha256>", "model/store.js": "<sha256>" },
  "layout": "<normalized-sha256>",
  "previous_layout": "main",
  "keybinding": { "section": "global", "key": "cmd-shift-s", "command": "sheepdog.focus", "had_file": true }
}
```

`files` is every embedded file as written; `layout` is the normalized hash of `layouts/sheepdog.json`
(`panelsync.LayoutHash`, which strips runtime-mutated `size`, `hidden`, and `active` fields on layout
tree nodes before hashing) when setup wrote it (absent when the user's own layout was kept);
`previous_layout` is the layout `shared.json` showed before setup switched to `"sheepdog"`; and
`keybinding` records any opt-in `keys.json` shortcut setup installed (`previous` restores a replaced
binding on uninstall). If any mod file on disk differs from what the binary ships, no marker is
written. herdr-app tolerates the extra dotfile (`herdr-app check` passes). `version` is for display;
whether an update exists is decided by file hashes, so a development build (`dev`) works too.

**At startup** (`panelTracker.Sync`, a fresh process after the restart for an upgrade; `$HERDR_APP_HOME`
or `~/.config/herdr-app`), with `panel.auto_update` on:

- No `mods/sheepdog` folder: nothing happens, nothing is created.
- Marker present, every file listed in it still matches, and the binary's files differ: the folder is
  replaced (`panelsync.InstallPanel` uses the same staging and swap path). The new tree is built in a
  temporary directory in the herdr-app home (same file system), files that are not ours
  (`gpui-kit.d.ts`, `jsconfig.json`, `.gitignore`) are carried over, files the new version dropped are
  not, and a single-syscall atomic directory swap (`renamex_np(..., RENAME_SWAP)` on macOS,
  `renameat2(..., RENAME_EXCHANGE)` on Linux, falling back to two renames with rollback on filesystems
  that return `ENOSYS`/`ENOTSUP`/`EINVAL`) swaps it in without a missing-directory window.
  `panel.updated {from, to, files, layout, layout_sidecar}` is logged; a failure logs
  `panel.update_failed` and leaves the old panel.
- The layout is replaced atomically when its normalized hash (`LayoutHash`) still matches the marker's
  (so pane resizes or folds herdr-app saved at runtime do not block layout updates). If the user edited
  the layout's pane structure (`LayoutHash` differs from the marker) and the binary ships a structurally
  different layout, the user's `layouts/sheepdog.json` is preserved and the new template is written
  alongside it at `layouts/sheepdog.json.new` (`layout_sidecar: true`).
- A changed file, a missing file, no marker, a marker that does not parse, or a symlinked folder (a
  development checkout): nothing is written.
- **On `sheepdog uninstall`** (`panelsync.Uninstall`, with `--dry-run` preview): removes `mods/sheepdog`
  only if `.sheepdog-install.json` is present and every file still matches its hash; removes
  `layouts/sheepdog.json` if its normalized hash matches the marker or embedded layout (plus any
  `layouts/sheepdog.json.new` sidecar); removes or restores the recorded `keys.json` shortcut if still
  pointing at `"sheepdog.focus"`; and switches `shared.json` `"layout"` back to `previous_layout`
  (default `"main"`) only if `"layout"` is still `"sheepdog"`. Modified files are preserved and reported.

**`/api/state`** has `panel` `{installed, version, modified, update_available, auto_update}`
(re-read from disk at most every 10 s, so it follows a user's edits and `sheepdog setup`). The panel
shows "A newer panel is available" when `update_available && (modified || !auto_update)`. A
panel with no marker that is identical to the binary's reports `modified: false, update_available:
false`. `sheepdog status` prints the same as a `panel` line. The panel does not read its own version:
the daemon compares the files, which is the same fact and also covers a panel herdr-app has not
reloaded yet.

Tests never touch the real herdr-app: `internal/panelsync` tests use temp homes, and the `cmd/sheepdog`
`TestMain` points `HERDR_APP_HOME` at a temp directory.
