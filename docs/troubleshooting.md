# troubleshooting

**Find the fix for what went wrong, and gather what a bug report needs.**

Start with `sheepdog doctor`: one line per check, with the fix. Then
`sheepdog status` and `sheepdog logs`. For a bug report, run
`sheepdog doctor --report` (see the end of this page).

Paths below use `~`; sheepdog prints absolute ones. The port is 7777 unless
you changed `web.port`.

## Install and setup

| You see | Cause | Fix |
|---|---|---|
| `plugin-fetch: no release found at …` | No published release for your machine yet. | Install Go and run the install again (it builds from source), or build from a checkout: `make install`. |
| `plugin-fetch: no release to download and go is not on PATH. Install Go (…) and reinstall, or install a release by hand: …` | No release for your machine (Intel Mac, ARM Linux) or none published, and no Go. | Install Go from <https://go.dev/dl/> and run the install again, or download a release by hand ([quick-start.md](quick-start.md#other-ways-to-install)). |
| `plugin-fetch: warning: … is not on your PATH` | `~/.local/bin` is not on PATH. | Add `export PATH="$HOME/.local/bin:$PATH"` to your shell profile, or run the linked path it prints. |
| `plugin-fetch: … exists and is not a link; left it alone` | You already have a `sheepdog` file there. | Run `<plugin dir>/bin/sheepdog` directly, or remove the file and reinstall. |
| `port 7777 is in use by another program, so sheepdog cannot listen on http://127.0.0.1:7777.` followed by `Find it with: lsof -nP -iTCP:7777 -sTCP:LISTEN` | Another program has the port. | Stop it, or set `web.port` in `~/.config/sheepdog/config.toml`. `sheepdog setup` then ends with `The setup page needs the daemon. Fix the above, or set up in the terminal: sheepdog setup --terminal`. |
| After a restart sheepdog asks for setup again (`no Gemini API key`), or `sheepdog status` says `keys came from … last time; that file is gone or unreadable` | The keys were in a `.env` in some working directory (or exported in a shell) and the restart did not find them. `sheepdog restart` and the restart after an upgrade now reuse the env file the last run loaded, but a file that was moved or deleted cannot be found. | Put the keys where every start finds them: `cp /path/to/.env ~/.config/sheepdog/.env && chmod 600 ~/.config/sheepdog/.env` (or `sheepdog setup`), then `sheepdog restart`. Keys only exported in a shell are not remembered: put them in that file. `sheepdog doctor` warns about keys outside it. |
| `sheepdog is already running (pid N, http://127.0.0.1:7777)` | Not an error: one daemon at a time. | Use it: `sheepdog open`. `sheepdog restart` restarts it. |
| `sheepdog is already running (http://…; no pid file: started by an older sheepdog or with another --data-dir)` | An older sheepdog holds the port and left no pid file. | `sheepdog status` shows `pid unknown`. `sheepdog stop` will not guess the pid (`a sheepdog is running at … but left no pid file`): Ctrl-C it where it was started, or find it with `lsof -nP -iTCP:7777 -sTCP:LISTEN`, then `sheepdog start`. |
| `sheepdog pair: the daemon listens on 127.0.0.1 only, so a phone cannot reach it: turn on phone access in step 4 of the setup page …` | Phone access is off (the default). | `sheepdog setup`, step 4 **Turn on phone access**, then `sheepdog pair`. |
| `No browser here (SSH?): …` from `sheepdog open` | You are on SSH. | Run the printed `ssh -N -L …` on your own computer and open the URL there ([quick-start.md](quick-start.md#over-ssh-with-no-browser)). |
| `The setup page only opens on the computer sheepdog runs on.` (HTTP 403) | You opened `/setup` from the LAN or Tailscale. | Open `http://127.0.0.1:7777/setup` on the computer. |
| `sheepdog start: config: ~/.config/sheepdog/config.toml line 6, key brain.enabled: expected value but found "flase" instead` | A typo in the config. | Fix that line. Errors name the file, the line and the key; wrong types and invalid values read like `key web.port: wrong type: want an integer, got a string (in quotes)` and `key supervisor.chattiness: "loud" is not valid (want one of quiet, normal, chatty)`. |

## Keys and models

| You see | Fix |
|---|---|
| `Gemini (AI Studio) rejected the key (HTTP 400: API key not valid. Please pass a valid API key.)`; the setup page adds `nothing was saved: fix the key marked above` | Copy the whole key again, or create a new one at <https://aistudio.google.com/apikey>. |
| `Gemini (AI Studio) says the quota is used up (HTTP 429: …) — wait for the quota to reset or raise it` | Wait, add more keys (`GEMINI_API_KEY_1` … `_9` form a pool), or use another voice ([models.md](models.md)). |
| `Gemini (AI Studio) unreachable: … — check the network` | No route to Google. Check the connection or proxy. |
| `setup  no voice key yet, so the voice is off: run \`sheepdog setup\`` (from `sheepdog start`) and the voice page says "Setup needed" | Finish step 1 of `sheepdog setup`. |
| `sheepdog status` shows `NOT connected: The voice cannot connect (…). Retrying in 5s.` | The daemon stays up and retries (5 s, doubling to 5 min). Read the reason in the line; fix the key or network. |
| `orchestrator  off` in `sheepdog status` | `brain.enabled = false`, or no key for the chosen orchestrator. The daemon answers with its own short sentences only. |
| `WARN … model … answered without calling the probe tool`, or a FAIL that says to check the model supports tool calling | That model cannot call tools reliably. Pick one that can ([models.md](models.md)). |

## herdr

| You see | Fix |
|---|---|
| `FAIL  herdr-tui … no herdr-tui on PATH and no socket at ~/.config/herdr/herdr.sock — fix: install herdr-tui from https://github.com/herdrdev/herdr/releases and start it with herdr, then run sheepdog doctor again` | Run `herdr` to start [herdr-tui](https://github.com/herdrdev/herdr). Meanwhile `sheepdog demo` works. |
| `FAIL  herdr-core … herdr-core's socket … does not answer` | `herdr.backend` is `hc` but herdr-core is not running: start it (`hc server start`), or point `herdr.hc_socket` at its socket. |
| Agents never show up | `sheepdog status` shows whether the herdr backend is reachable (`herdr` line). Start it, then run `sheepdog doctor`. |

## Running it

| You see | Fix |
|---|---|
| `sheepdog is not running (start it with: sheepdog start)`, exit 3 | `sheepdog start`. |
| `No log yet: … does not exist, so sheepdog has not run here yet.` | `sheepdog start`, then `sheepdog logs -f`. |
| `sheepdog did not start: it exited (exit status 1)` followed by `It printed:` | The lines after it say why (for example an invalid key). The log: `~/.local/share/sheepdog/logs/sheepdog.log`. |
| `sheepdog stop` says `sheepdog is not running` | Fine, nothing to stop. |
| `WARN env file … is readable by other users (0644) — fix: chmod 600 ~/.config/sheepdog/.env` | Run the `chmod`. |
| The daemon keeps coming back after `kill` | The login service restarts it. Use `sheepdog stop`; it unloads the service. |
| `sheepdog doctor` says `project folders` needs access, or the phone lists no project folders | macOS has not let sheepdog into a folder such as Documents. Allow sheepdog in System Settings, Privacy & Security, Files and Folders (or Full Disk Access), then retry. Until you answer macOS's prompt the daemon skips that folder instead of waiting. |
| Browser blocks the microphone | See [faq.md](faq.md) (per origin: `127.0.0.1` and `localhost` are separate). |

## Phone

See [remote-access.md](remote-access.md) and [iphone-app.md](iphone-app.md).

## Update, uninstall, data

| You see | Meaning |
|---|---|
| `No published release found for kccarlos/herdr-sheepdog: the repository is private or has no release yet.` then `Nothing to update to; check again later.` | `sheepdog update`, exit 0: there is nothing newer. |
| `Could not check for a new release: …` then `Check your internet connection and try again.` | `sheepdog update`, exit 1: offline or rate-limited. |
| `Delete the event log (…) and the orchestrator's transcripts (…)? [y/N]` | `sheepdog data clear` asks first; `n` deletes nothing. Stop the daemon before running it. |
| `To remove the program: rm …` and `herdr plugin uninstall kccarlos.sheepdog` | The last lines of `sheepdog uninstall`: it never touches herdr-tui's plugin registry, so run that command yourself. |

## Report a bug

```sh
sheepdog doctor --report --out sheepdog-report.md
```

The report holds versions, OS, the config (secrets masked), the names (never the
values) of the env file's keys, the doctor results, `status` and the last 200
log lines. Every key and token is replaced by `[REDACTED]` and your home
directory shows as `~`. Read it, then attach it to a
[GitHub issue](https://github.com/kccarlos/herdr-sheepdog/issues/new/choose).
Agent screen text can appear in logs, so look before you post.
