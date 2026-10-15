# Quick start

From install to your first spoken answer, about three minutes. You need
[herdr](https://github.com/herdrdev/herdr), macOS or Linux, a browser with a
microphone (on this computer, or over SSH: see below) and one free Gemini API
key (the setup page links to it; [models.md](models.md) lists the
alternatives). Getting the key from AI Studio is the slowest step. No herdr
yet? Install it from its
[releases page](https://github.com/herdrdev/herdr/releases) and start it with
`herdr` first.

## 1. Install the plugin

```sh
herdr plugin install kccarlos/herdr-sheepdog
```

herdr shows the build step before it runs it. It downloads the `sheepdog`
binary for your machine (Apple silicon Mac or x86-64 Linux), checks its
sha256, links it into `~/.local/bin` and ends with:

```text
Next: run `sheepdog setup` (or open the "Set up sheepdog" action in herdr).
```

If it warns that `~/.local/bin` is not on your PATH, add it as the message
says (then open a new terminal), or run the printed path directly.

Any other machine (an Intel Mac, ARM Linux), or a release that is not
published yet, builds from source instead and needs [Go](https://go.dev/dl/).
You then see `plugin-fetch: no release found at …` and `plugin-fetch:
building from source with go`; that takes about ten seconds on a fast
connection. Without Go the install stops and says so
([troubleshooting.md](troubleshooting.md)).

## 2. Set up in your browser

```sh
sheepdog setup
```

```text
Starting sheepdog…
sheepdog started in the background (pid 57577)
  voice page  http://127.0.0.1:7777/voice
  logs        sheepdog logs -f

Set up sheepdog in your browser:

    http://127.0.0.1:7777/setup

The page opened in your browser. It works on its own: you can close this terminal.
```

(Your port is 7777 unless you changed `web.port`. The last line appears only
when a browser could be opened; over SSH use the URL above, see
[Over SSH](#over-ssh-with-no-browser).) The page has five steps:

1. **Models.** Paste a free key from <https://aistudio.google.com/apikey>. It is
   checked on the spot ("Valid key"), then **Save and start the voice**.
   Nothing is saved until the key passes.
2. **Checks.** The same checks as `sheepdog doctor`, with the fix next to
   each problem.
3. **Start at login.** On by default. It installs a login service (a launchd
   agent on macOS, a `systemd --user` unit on Linux) that runs the daemon, so
   sheepdog comes back after a restart or a crash. `sheepdog service uninstall`
   removes it.
4. **Pair your phone.** Optional; see [iphone-app.md](iphone-app.md).
5. **Talk to it now.** Opens the voice page. Press Connect, allow the
   microphone, and ask "what's going on?".

Over SSH, with no display, `sheepdog setup` asks the same questions in the
terminal (`sheepdog setup --terminal`); see
[Over SSH, with no browser](#over-ssh-with-no-browser) for how to talk.

## 3. Talk

In herdr, open a pane and start an agent in it (`claude`, `codex` or `agy`;
never with an "approve everything" flag, see [safety.md](safety.md)). herdr
detects it and sheepdog follows. Then say:

- "What's going on?" It names who needs you first, then who finished.
- "Approve." It answers the approval prompt you were just told about, once
  you have said so in your own words within 30 seconds of its question
  (`supervisor.authorise_window_s`).
- "Why did the migration fail?" It reads the agent's screen and, if that is
  not enough, asks the agent itself.
- "Tell me when claude-auth is done." It remembers, and tells you.

## No agents yet? Use the demo

On step 5 of the setup page, **Try it with demo agents** starts three
scripted agents in the browser: one finishes, one asks to run the test suite
and then fails, one finishes later. It needs the key from step 1 saved
first. Or from a terminal, once setup has saved the key:

```sh
sheepdog demo
```

(Without setup: `GEMINI_API_KEY=your-key sheepdog demo`.)

It never touches herdr or real agents, runs on its own port (7778 or the next
free one) and is safe next to a running daemon. Press Connect on the page it
opens and talk. Ctrl-C ends it.

## Over SSH, with no browser

The setup page and the voice page only open on the computer sheepdog runs
on, and the voice needs a browser with a microphone, so from a server use an
SSH tunnel to your own computer:

```sh
ssh -N -L 7777:127.0.0.1:7777 you@the-server    # on your computer; use your web.port
```

Then open `http://127.0.0.1:7777/voice` (or `/setup`) in your own browser.
The tunnel may use another local port. The demo (below) listens on 7778 or the
next free port, so forward that one too. `sheepdog open` and the end of
`sheepdog setup --terminal` print this line for your port. On Linux, the
login service (`systemd --user`) may stop when your last session ends unless
lingering is on (`loginctl enable-linger $USER`); this is a systemd setting
that sheepdog does not change.

## If something stalls

| You see | Do |
|---|---|
| The setup page does not open | Open the URL `sheepdog setup` printed, or `sheepdog setup --no-open` to print it only. |
| "rejected the key" | Copy the whole key again from AI Studio. Nothing was saved. |
| Voice page says "Setup needed" | Run `sheepdog setup` and finish step 1. |
| No microphone prompt, or "blocked" | Allow your browser under System Settings, Privacy & Security, Microphone; permission is per address ([faq.md](faq.md)). |
| Anything else | `sheepdog doctor`, then [troubleshooting.md](troubleshooting.md). |

## Other ways to install

The plugin is the supported path. The daemon is also a plain binary:

- **Release download:** each GitHub release carries
  `sheepdog_<tag>_darwin_arm64.tar.gz` and `sheepdog_<tag>_linux_amd64.tar.gz`
  with `checksums.txt`. Check the sha256, unpack, and put `sheepdog` on your
  PATH. The macOS binary is signed and notarized.
- **From source** (Go, see `go.mod`): `git clone https://github.com/kccarlos/herdr-sheepdog.git`,
  then `make install` builds it and copies it to `~/.local/bin/`.
- **Homebrew:** not published yet.

Then run `sheepdog setup` as above. `sheepdog update` prints the upgrade steps
for the way you installed.

Next: [pair your iPhone](iphone-app.md), [choose other models](models.md),
[use it away from home](remote-access.md).
