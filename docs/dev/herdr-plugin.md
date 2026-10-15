# sheepdog as a herdr plugin

The repository root is a [herdr plugin](https://herdr.dev/docs/plugins/)
(`herdr-plugin.toml`, id `kccarlos.sheepdog`). It adds four actions to
herdr and downloads the `sheepdog` binary for you. It needs herdr 0.9.5 or
newer (macOS or Linux).

```sh
herdr plugin install kccarlos/herdr-sheepdog
```

herdr shows the build step before it runs it. It is
[`scripts/plugin-fetch.sh`](../../scripts/plugin-fetch.sh), about 80 lines of
POSIX sh.

## What you get

Four actions (`herdr plugin action list --plugin kccarlos.sheepdog`), each
opening a popup:

| Action | Runs | Popup |
|---|---|---|
| Set up sheepdog (`setup`) | `sheepdog init` | 90×30. Asks for a key; at the end offers `sheepdog service install`. |
| Pair your phone (`pair`) | `sheepdog pair --compact` | 72×42, sized for the QR code with three addresses. |
| Check setup (`doctor`) | `sheepdog doctor` | 100×30 |
| Logs (`logs`) | `sheepdog logs -f` | 90%×80%. Follows the log; Ctrl-C closes it. |

A popup closes when its command exits, so
[`scripts/plugin-pane.sh`](../../scripts/plugin-pane.sh) waits for Enter after
`init`, `pair` and `doctor`. Bind the actions to keys with herdr's
`[[keys.command]]` (`type = "plugin_action"`).

## First run

1. Run **Set up sheepdog**, paste a Gemini key, and say yes to the login
   service. Or from a shell: `sheepdog init`.
2. **Check setup** should show no FAIL. Start herdr's agents as usual.
3. Open <http://127.0.0.1:7777/voice>, or run **Pair your phone** for the
   iOS app.

## What the plugin does and does not do

- **Build** (`herdr plugin install` only; `herdr plugin link` skips it, so
  run `sh scripts/plugin-fetch.sh` yourself when developing): downloads
  `sheepdog_<tag>_<os>_<arch>.tar.gz` (darwin arm64, linux amd64) and
  `checksums.txt` from the latest GitHub release and checks the sha256.
  A mismatch is an error. With no release for your machine and `go` on
  PATH it runs `go build`; with neither it stops and says so. The binary
  goes in `bin/` of the plugin directory. `SHEEPDOG_RELEASE_URL` replaces
  the download directory (tests use a `file://` one).
- **Startup hook:** `sheepdog service start`. If the login service is
  installed and not running, it asks launchd or `systemd --user` to start
  it, then exits. If it is not installed it prints one line and does
  nothing. The plugin never installs the service on its own (only you do,
  through the setup popup or `sheepdog service install`), and it starts no
  process of its own: the service manager owns the daemon.
- **Config and keys** stay where the CLI keeps them,
  `~/.config/sheepdog/` (`config.toml`, `.env`), so a plugin install and a
  Homebrew or `make install` one share a setup. The plugin writes nothing
  to its own config or state directories.
- The service runs `<plugin dir>/bin/sheepdog`. `herdr plugin update`
  replaces that file in place; run `sheepdog service install` again to
  restart the daemon on the new binary.

## Tests

- `sh scripts/plugin-fetch_test.sh`: the fetch script against a fake
  release directory (good download, tampered download, no release with and
  without Go).
- `shellcheck -s sh scripts/plugin-*.sh`
- Go: `service start` (`internal/service`, `cmd/sheepdog/service_test.go`),
  init's service offer, and `pair --compact` fitting its popup.
