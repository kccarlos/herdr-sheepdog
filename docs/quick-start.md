# quick start

**From install to your first spoken answer, in about three minutes.**

You need
[herdr](https://github.com/herdrdev/herdr), macOS or Linux, a browser with a
microphone (on this computer, or over SSH: see below) and one free Gemini API
key (the setup page links to it; [models.md](models.md) lists the
alternatives). Windows is not supported. Getting the key from AI Studio is
the slowest step.

No herdr yet? Install it, then start it where your work lives and leave it
running:

```sh
curl -fsSL https://herdr.dev/install.sh | sh
herdr
```

Or `brew install herdr` instead of the curl line. Other ways (mise and
binaries) are on [herdr's releases page](https://github.com/herdrdev/herdr/releases).

Using herdr-app on herdr-core? Keep the conversation in its sidebar and use
Sheepdog.app for voice: [Sheepdog on your Mac](desktop.md).

## 1. Install sheepdog

For herdr-core (`hc`), herdr-app, or a Linux server, install the daemon
directly:

```sh
curl -fsSL https://raw.githubusercontent.com/kccarlos/herdr-sheepdog/main/scripts/install.sh | sh
```

The installer downloads the latest release for Apple silicon macOS or x86-64
Linux, verifies its sha256, and puts `sheepdog` in `~/.local/bin`. It offers
to run `sheepdog setup` when a terminal is available (otherwise it prints the
next step). For another platform, [build from source](#other-ways-to-install).

If you use old herdr, its plugin remains the easy install path:

```sh
herdr plugin install kccarlos/herdr-sheepdog
```

It needs herdr 0.9.4 or newer (`herdr --version`; update herdr the way you
installed it). herdr shows the build step before it runs it. The build step
downloads the `sheepdog` binary for your machine (Apple silicon Mac or x86-64
Linux), checks its sha256 and links it into `~/.local/bin`; herdr then prints:

```text
Installed kccarlos.sheepdog from kccarlos/herdr-sheepdog.
```

herdr does not show the build step's own messages when it succeeds. If
`sheepdog` is then not found, `~/.local/bin` is not on your PATH: add
`export PATH="$HOME/.local/bin:$PATH"` to your shell profile and open a new
terminal, or run `~/.local/bin/sheepdog` directly.

If the plugin has no release for your machine, it builds from source and
needs [Go](https://go.dev/dl/); without Go it stops and says so
([troubleshooting.md](troubleshooting.md)).

## 2. Set up in your browser

```sh
sheepdog setup
```

`sheepdog setup` detects whether old herdr, herdr-core or herdr-app is
installed and running. It can pin the backend, ask when the setup is
ambiguous, and offer to install the panel for herdr-app.

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

### Setting up in the terminal

`sheepdog setup --terminal` (or `sheepdog init`) asks the same questions as the
setup page's Models step: a starting point, the voice, then the orchestrator.
The starting points are the page's cards: **Recommended** (Gemini Live and
DeepSeek), **Claude Opus 5.5** (Gemini Live and Anthropic), **Free quick
start** (Gemini for both) and **Custom**, where you pick the voice (Gemini Live
on AI Studio or Vertex AI, or GPT-Live) and the orchestrator preset yourself.
On a fresh install Enter takes Recommended; otherwise it takes the card that
matches what you saved. Keys are typed without echo, checked live, and
nothing is written until they pass. Enter keeps the default at every model and
voice question. A short run, with the key lines as you would see them (the key
you type is not shown):

```text
$ sheepdog setup --terminal
Step 1 of 3. Starting point:
  1) Recommended: DeepSeek: 92 % understood in our tests, replies in about 3 s, and costs little. Needs a DeepSeek API key (paid).
  2) Claude Opus 5.5: 92 % understood in our tests, like DeepSeek, with fewer unsupported statements. Slower (about 7 s) and costs much more. Needs an Anthropic API key (paid).
  3) Free quick start: One free key for both. Good for trying; can be slow or busy. Upgrade any time.
  4) Custom: Pick each part yourself.
choice [1]:
Starting point: Recommended (voice: Gemini Live on AI Studio, orchestrator: DeepSeek)
Gemini API key (https://aistudio.google.com/apikey):
  ok    gemini key  valid (14 models); AI Studio does not expose the quota left
Live model:
  1) gemini-3.8-live (default)
Live model [gemini-3.8-live] (Enter keeps it; a number or a name picks another):
Voices: Puck, Kore, Charon, Aoede
Voice (Enter keeps the default) [Puck]:
DeepSeek API key (https://platform.deepseek.com/api_keys):
  ok    deepseek key  valid (2 models)

Summary: You talk to gemini-3.8-live (Google AI Studio). Your agents are handled by deepseek-flash (DeepSeek). Cost: free with daily limits · pay per use
sheepdog init: wrote ~/.config/sheepdog/config.toml (0600)
sheepdog init: wrote ~/.config/sheepdog/.env (0600): DEEPSEEK_API_KEY, GEMINI_API_KEY, SHEEPDOG_BRAIN_PROVIDER, ...
```

Choose **Custom** to pick an orchestrator preset: OpenRouter, OpenAI, Ollama and
LM Studio (found on this computer; the terminal says when the server is not
running) or any OpenAI-compatible URL. The model comes from a numbered list of
what the endpoint offers, with the models that call tools first. The summary
line is the page's, and the save is the page's too, so either one can change
what the other saved. Then it offers phone access and the login service, as
before.

For scripts, `sheepdog init --non-interactive` takes each choice as a flag and
each key from the environment; see [commands.md](commands.md#flags-of-sheepdog-init).

## If something stalls

| You see | Do |
|---|---|
| The setup page does not open | Open the URL `sheepdog setup` printed, or `sheepdog setup --no-open` to print it only. |
| "rejected the key" | Copy the whole key again from AI Studio. Nothing was saved. |
| Voice page says "Setup needed" | Run `sheepdog setup` and finish step 1. |
| No microphone prompt, or "blocked" | Allow your browser under System Settings, Privacy & Security, Microphone; permission is per address ([faq.md](faq.md)). |
| Anything else | `sheepdog doctor`, then [troubleshooting.md](troubleshooting.md). |

## Update

sheepdog checks for a new release once a day (one request to
`api.github.com`, nothing personal sent; [privacy.md](privacy.md)). When there
is one, `sheepdog status` and the setup and voice pages
say so, with the command for the way you installed. The voice never
speaks it. Update with the command for your install method:

| You installed with | Update with |
|---|---|
| One-line installer | Rerun the installer |
| herdr plugin | `herdr plugin update kccarlos.sheepdog` |
| Homebrew | `brew upgrade sheepdog` |
| From source | `git pull` in your clone, then `make install` |
| Manual release download | Download the new archive from the release page and replace the binary |

Then the running daemon has to start the new version:

- **It restarts itself when idle.** The daemon notices that its program was
  replaced and restarts into it as soon as nobody is using it: no voice
  client connected, no orchestrator turn running, no approval waiting, no
  demo running. It checks every minute and never interrupts a live session.
  Turn it off with `update.auto_restart = false` ([CONFIG.md](../CONFIG.md)).
- **Or restart it yourself:** `sheepdog restart`. Any command that finds the
  daemon older than the installed program says so and prints this.

`update.check = false` turns the daily check and its notices off.
`sheepdog update` checks right now and prints the steps.

## Other ways to install

The installer above is the supported path. The daemon is also a plain binary:

- **Homebrew:** `brew install kccarlos/tap/sheepdog` (macOS and Linux;
  the macOS binary is signed and notarized). `brew upgrade sheepdog` updates it.
- **From source** (Go, see `go.mod`): `git clone https://github.com/kccarlos/herdr-sheepdog.git`,
  then `make install` builds it and copies it to `~/.local/bin/`.
- **Release download:** each GitHub release carries
  `sheepdog_<tag>_darwin_arm64.tar.gz` and `sheepdog_<tag>_linux_amd64.tar.gz`
  with `checksums.txt`. Check the sha256, unpack, and put `sheepdog` on your
  PATH. The macOS binary is signed and notarized.
- **Sheepdog.app (Mac only):** the menu-bar app that holds the microphone,
  `brew install --cask kccarlos/tap/sheepdog-app` (signed, notarized, arm64,
  macOS 14 or newer). It talks to the daemon above, so install that too.
  `brew upgrade --cask sheepdog-app` updates it.

Then run `sheepdog setup` as above. `sheepdog update` prints the upgrade steps
for the way you installed.

Next: [pair your iPhone](iphone-app.md), [choose other models](models.md),
[use it away from home](remote-access.md).
