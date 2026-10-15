# FAQ

**Do I need herdr?** For real use, yes: sheepdog watches the agents herdr runs
(Claude Code, Codex, agy). To try it without herdr, use `sheepdog demo` or the
iPhone app's demo ([quick-start.md](quick-start.md)).

**Do I need an iPhone?** No. The voice page works in a desktop browser:
`sheepdog open`. The app adds the lock screen, earbuds and being away from your
desk. There is no Android app.

**What does it cost?** sheepdog is free (MIT). One free Gemini key runs both
models. GPT-Live is $0.05 per minute of open session; DeepSeek is paid per
token. See [models.md](models.md).

**Is it safe to let it approve things?** A worker's approval prompt is answered
only on your own words in that turn, with a key from the prompt's own options,
and code enforces that, not a prompt. See [safety.md](safety.md) for what you
must still avoid.

**Where do my words go?** Audio to the voice provider, text and agent screens to
the orchestrator provider, nowhere else. There is no telemetry.
[privacy.md](privacy.md).

**How talkative is it?** Pick `quiet`, `normal` or `chatty` (the default) in the
`/debug` header or set `supervisor.chattiness`:

| Level | Announced |
|---|---|
| `quiet` | An agent's question, an agent exiting or losing its pane while busy, and promises you asked for. |
| `normal` | `quiet`, plus informational blocks and the finish of any agent you dealt with recently. |
| `chatty` | `normal`, plus every finish and "started on…" for your instructions. |

**Which language?** Set your languages in the app's Settings, Languages (most
preferred first); the voice and the orchestrator answer in the language you use.

**How fast is it?** About 5 s from herdr reporting a block to hearing it; a
status question in about 2 s when the voice answers alone (when it waits for the orchestrator, one cold test took about 20 s); an instruction delivered in about 4 s; an
investigation in about 30 s (acknowledged at once). From a 2026-10-02 phone run.

**The microphone does not work in the browser.** macOS: System Settings,
Privacy & Security, Microphone, enable your browser. Browsers ask per origin, so
`127.0.0.1:7777` and `localhost:7777` are separate.

**How do I update or remove it?** `sheepdog update` prints the upgrade steps;
`sheepdog uninstall` removes it ([commands.md](commands.md)).

**It broke. What do I send?** `sheepdog doctor --report` and the
[troubleshooting.md](troubleshooting.md) table first; then open an issue with
the report.
