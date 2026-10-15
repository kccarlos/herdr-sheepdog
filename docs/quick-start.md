# Quick start

From install to your first spoken answer, about three minutes. You need
[herdr](https://github.com/herdrdev/herdr), macOS or Linux, and one free
Gemini API key (the setup page links to it; [models.md](models.md) lists the
alternatives).

## 1. Install the plugin

```sh
herdr plugin install kccarlos/herdr-sheepdog
```

herdr shows the build step before it runs it. It downloads the `sheepdog`
binary, checks its sha256, links it into `~/.local/bin` and ends with:

```text
Next: run `sheepdog setup` (or open the "Set up sheepdog" action in herdr).
```

If it warns that `~/.local/bin` is not on your PATH, add it as the message
says, or run the printed path directly.

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

(Your port is 7777 unless you changed `web.port`.) The page has five steps:

1. **Models.** Paste a free key from <https://aistudio.google.com/apikey>. It is
   checked on the spot ("Valid key"), then **Save and start the voice**.
   Nothing is saved until the key passes.
2. **Checks.** The same checks as `sheepdog doctor`, with the fix next to
   each problem.
3. **Start at login.** On by default; sheepdog then comes back after a
   restart or a crash.
4. **Pair your phone.** Optional; see [iphone-app.md](iphone-app.md).
5. **Talk to it now.** Opens the voice page. Press Connect, allow the
   microphone, and ask "what's going on?".

Over SSH, with no display, `sheepdog setup` asks the same questions in the
terminal (`sheepdog setup --terminal`).

## 3. Talk

Start some agents in herdr (Claude Code, Codex, agy), then say:

- "What's going on?" It names who needs you first, then who finished.
- "Approve." It answers the approval prompt you were just told about, once
  you have said so in your own words.
- "Why did the migration fail?" It reads the agent's screen and, if that is
  not enough, asks the agent itself.
- "Tell me when claude-auth is done." It remembers, and tells you.

## No agents yet? Use the demo

On step 5 of the setup page, **Try it with demo agents** starts three
scripted agents in the browser: one finishes, one asks to run the test suite
and then fails, one finishes later. Or from a terminal:

```sh
GEMINI_API_KEY=your-key sheepdog demo
```

It never touches herdr or real agents, runs on its own port (7778 or the next
free one) and is safe next to a running daemon. Press Connect on the page it
opens and talk. Ctrl-C ends it.

## If something stalls

| You see | Do |
|---|---|
| The setup page does not open | Open the URL `sheepdog setup` printed, or `sheepdog setup --no-open` to print it only. |
| "rejected the key" | Copy the whole key again from AI Studio. Nothing was saved. |
| Voice page says "Setup needed" | Run `sheepdog setup` and finish step 1. |
| Anything else | `sheepdog doctor`, then [troubleshooting.md](troubleshooting.md). |

Next: [pair your iPhone](iphone-app.md), [choose other models](models.md),
[use it away from home](remote-access.md).
