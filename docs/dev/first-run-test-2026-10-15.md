# First-run test, 2026-10-15

A synthetic first-time-user test of sheepdog before launch. The user is a
stranger from the herdr plugin marketplace. The metric is time to first value
(TTFV): install to the first spoken answer, target median under 3 minutes.

## Method and limits

- **A, naive readers.** Ten `claude -p --model haiku` runs (5 personas, 2 runs
  each). Each got only `README.md` and the user docs (`docs/*.md`, `CONFIG.md`,
  no `docs/dev`), and role-played the persona. Raw outputs are not committed.
- **B, executed walkthrough.** The README followed literally in throwaway HOMEs
  (`env -i`, own XDG dirs, own herdr server, ports 7790 to 7795, never 7777),
  every step timed with `date +%s` and captured with `script`. Personas 1
  (herdr user), 2 (no agents, no herdr server) and 3 (SSH, `setup --terminal`).
  Personas 4 and 5 are method A only.
- **Substitutions.** `herdr plugin install` cannot work yet (private repo, no
  release): I used `herdr plugin link` on the worktree and ran the manifest's
  build step, `sh scripts/plugin-fetch.sh`, by hand. The setup page was driven
  over its HTTP API (`/api/setup/state`, `validate`, `keys`, `checks`, `demo`),
  not in a browser: the page's labels were only compared with
  `web/setup/index.html`. The real key came from the repo `.env` and was never
  printed; it was written to the throwaway `.env` files, which were deleted.
  Ports were set to non-7777 in the throwaway `config.toml`, which a stranger
  would not do. No real agents ran (the throwaway HOME has no Claude login).
- **Limits.** Synthetic users miss feelings, visual polish and real networks.
  Haiku readers over-report "confusions" and its minute estimates are noisy.
  The machine is fast, with a warm network. Not measured at all: creating the
  AI Studio key, installing herdr, typing, the browser microphone prompt,
  `herdr plugin install` itself, a plugin action popup, a real browser, a phone.
  The measured times below are the program's time; the human gap is in the
  Method A estimates and is dominated by prerequisites and comprehension.
- **Gemini use.** Three scripted Live sessions (`audio-e2e`, one question
  each). The daemons also reported `voice: connected` right after the key was
  saved, so each may hold an idle session: the cap of 4 may have been exceeded
  by one or two idle sessions. No other model calls.
- **Incident.** The first `sheepdog setup --terminal` run answered Enter to
  "Install sheepdog as a login service now? [Y/n]" (the default). That loaded a
  launchd job named `io.github.kccarlos.sheepdog` into the real user's launchd
  from the throwaway HOME. I removed it with `sheepdog service uninstall` about
  a minute later; the real `launchctl list` has only the bridge and livekit
  jobs, and the real daemon on :7777 was never touched. The label is global per
  user, so a second sheepdog install on one account collides with it.

## TTFV

### Measured (B), program time in seconds

| Step | P1 herdr user | P2 no agents | P3 SSH terminal |
|---|---|---|---|
| `herdr plugin link` (stands in for install) | 0 | 0 | not run |
| Build step (no release, `go build`, cold caches, network) | 9 | 10 | not run |
| `sheepdog setup --no-open` (daemon start, prints URL) | 0 | 0 | |
| Key check (`validate`) and save, daemon restarts | 1 + 0 (+ about 13 s to reload, in the log) | 0 | |
| `sheepdog setup --terminal`, scripted input | | | 17 (about 12 s are my scripted sleeps) |
| Demo start (setup page button, `/api/setup/demo`) | | 0 | |
| First spoken answer, `audio-e2e --only status-question` | run 1: no audio in 21 s (the answer was ready at about 20 s); run 2: first audio 2.0 s after the end of speech | demo: first audio 4.7 s after the end of speech ("Let me check the status of your agents for you."), 12 s run | not run (Live budget) |
| **Total, program time** | about 12 s to 33 s | about 25 s | about 17 s to a configured daemon |

P1 run 1 evidence (event log): `brain.step dur_ms 16638` for a 65-token reply
on `gemini-3.5-flash`, then `voice.tool_result` `"detail": "Still working on
it; the answer follows on its own."`, `"spoken": ""`, `"scheduling": "silent"`.
For about 17 s the user hears nothing. Run 2 answered at once, without a tool
call: "As I mentioned, no agents are running right now and you don't have any
active agent sessions." (n=1 each; the scenario marks run 2 FAIL because it
expects a `to_supervisor` call.) The FAQ says "a status question in about 2 s".

### Estimated (A), minutes to the first spoken answer, from the readers

Before the fixes (docs at `origin/main`), after the fixes (this PR). Where a
reader gave a range the last one is shown; blank means the reader gave none.

| Persona | Before (run 1, run 2) | After (run 1, run 2) | Where they stall |
|---|---|---|---|
| 1 herdr user, macOS | 7 to 10, 7 to 10 | 6 to 8, – | Go in the install path, PATH file, "Start at login" side effect |
| 2 curious, no agents | 5 to 10, – (one said 12 to 20 via demo) | 15 to 20, – | herdr not installable from the docs, how to open a pane |
| 3 Linux over SSH | about 20 to 30, about 30 | –, 10 to 12 | no way to talk from a headless box (before); demo port and linger (after) |
| 4 skims, non-native | 15 to 20, – | 12 to 15, 10 to 12 | Go, PATH, pairing vs "optional" |
| 5 iPhone-first | 30 to 40, 8 to 12 | –, 10 to 15 | TestFlight dead link (before); no install path but a source build (after) |

The medians are far from the 3-minute target in the readers' minds (6 to 20
minutes), while the program time is under a minute. The gap is the
prerequisites (herdr, key, Go on unsupported machines) and unclear steps, not
the binary. Haiku's after-estimates for P2 went up because it now sees the Go
and herdr caveats it ignored before; treat the estimates as ordinal only.

### Confusions before and after (10 runs each, keyword tally of the outputs)

| Theme | Before | After |
|---|---|---|
| TestFlight placeholder link | 6 | 1 |
| `sheepdog pair` step 3 vs "optional" vs loopback bind | 8 | 4 |
| "How do I start agents in herdr" | 6 | 2 |
| No path to talk over SSH (tunnel, no browser) | 2 (and every P3 estimate over 20 min) | 2, now about the demo port and linger, both documented after the re-run |
| Go needed / build from source | 9 | 8 (now documented, so the count is mentions, not surprise) |
| herdr install not explained | 7 | 9 (not fixed beyond one sentence) |
| "Start at login" surprise | 9 | 10 (one sentence added after the re-run, not re-tested) |

Quotes (before): "Troubleshooting later says "install Go and run the
install again (it builds from source)," which I hadn't been told." (P1);
"README install says "`sheepdog pair` # 3. show the QR code for your phone
(also in the wizard)" as a normal step. Quick start says "Pair your phone.
Optional."" (P2); "the URL is literally `testflight.apple.com/join/TESTFLIGHT_LINK`.
This is a placeholder. Dead end." (P5); "The page opened in your browser. ...
There is no browser here." (P3); "I don't know what words count, or what I must
say first." (P5, on "Approve"); "No SSH tunnel guidance for a headless
server." (P3).

## Issues

Fixed ones are marked **fixed**. Lines are at `origin/main` before this PR.

### P0 (blocks)

1. **Dead TestFlight link presented as the install.** `README.md:19-20,58`,
   `docs/iphone-app.md:10-11` link to `.../join/TESTFLIGHT_LINK`; 6 of 10
   readers hit it, P5 gives up. **Fixed:** the links are gone, the text says
   the link is coming and points to the source build; a comment keeps the
   restore steps. The CEO decides when to restore.
2. **No release, so the install needs Go; Intel Macs and ARM Linux never get a
   binary.** Evidence (B, P1 and P2):
   `curl: (56) ... 404`, `plugin-fetch: no release found at
   https://github.com/kccarlos/herdr-sheepdog/releases/latest/download`,
   `plugin-fetch: building from source with go`,
   `sheepdog dev (commit none, built unknown)`. Without Go: `plugin-fetch: no
   release to download and go is not on PATH. Install Go ...` (not in
   troubleshooting). `scripts/plugin-fetch.sh:41-43` supports only
   `darwin_arm64` and `linux_amd64`. **Docs fixed** (README, quick start,
   troubleshooting row). **Not fixed, launch gate:** publish a release before
   launch; consider `darwin_amd64` and `linux_arm64` assets.
3. **Over SSH there was no documented way to a spoken answer.** The terminal
   wizard ends with "`sheepdog open`  the voice page, on this computer". I
   verified an `ssh -N -L` tunnel works, even to another local port
   (`Host: 127.0.0.1:8080` and `localhost:8080` give 200 on
   `/api/setup/state`; `evil.example` gives 403). **Fixed:** quick-start
   section "Over SSH, with no browser"; `sheepdog open` over SSH prints the
   tunnel line instead of running `open` on the server
   (`cmd/sheepdog/lifecycle.go`); `setup --terminal` ends with the same line
   (`cmd/sheepdog/setup_cmd.go`).
4. **README install step 3, `sheepdog pair`, fails on a fresh install.**
   Evidence: `sheepdog pair: the daemon listens on 127.0.0.1 only, so a phone
   cannot reach it: set web.bind = "0.0.0.0" ... (sheepdog init can write
   both)`. **Fixed:** the README block has two steps; the message
   (`internal/pair/pair.go:42`) points to step 4 of the setup page; a
   troubleshooting row.

### P1 (painful)

5. **Fix texts say `sheepdog init`, the docs say `sheepdog setup`.** Doctor,
   `demo`, `pair`, service warning: `internal/onboard/onboard.go:190,245,412,464`,
   `internal/onboard/doctor.go:52,125`, `cmd/sheepdog/pair.go:96,124`,
   `cmd/sheepdog/demo.go:192`, `cmd/sheepdog/service.go:102`. The setup page
   shows these fixes too. **Fixed** (the `init` command itself is unchanged).
6. **`init`'s last line hardcodes the port and says `sheepdog serve`.**
   Evidence (port 7792): `sheepdog init: next: sheepdog doctor, then sheepdog
   serve, and open http://127.0.0.1:7777/voice`, followed by setup's own block
   with `sheepdog start`. `cmd/sheepdog/onboard.go:334`. **Fixed.**
7. **Silent 17 to 20 s on a cold first question** (P1 run 1, above). The voice
   says nothing while the orchestrator works (`scheduling: silent`).
   **Not fixed** (needs a voice-prompt or harness change with eval evidence,
   `docs/dev/eval/README.md`). Proposal: an immediate short acknowledgement when
   `reply_pending` exceeds about 3 s.
8. **herdr is never an install step.** 7 of 10 readers before, 9 of 10 after.
   **Partly fixed:** one sentence and the releases link in the quick start. A
   real fix needs herdr's own install command, which I did not verify.
9. **"Start at login: on by default" does not say what it installs.** 9 and 10
   of 10. The terminal wizard installed a real launchd job on Enter (incident
   above). **Fixed in docs** (launchd agent or systemd user unit, how to
   remove). Not re-tested. Linux lingering is stated as untested here.
10. **plugin-fetch noise and a wrong "Next".** A `curl: (56)` line before the
    "no release" message, and `Next: run sheepdog setup` even when the link was
    refused (an existing non-link file). `scripts/plugin-fetch.sh:41,74-90`.
    **Fixed** (curl quiet for the probe; Next names the binary path when not
    linked; `scripts/plugin-fetch_test.sh` passes).
11. **"Approve" is vague.** P5: "I don't know what words count, or what I must
    say first." The 30 s window is only in CONFIG.md. **Fixed:** the quick start
    states the window. The accepted words are not documented; `docs/faq.md:15`
    says "with a key from the prompt's own options".

### P2 (polish)

12. **`CONFIG.md` `[state]` table was corrupted** (the `state.path` row held the
    brain-transcript row, plus two stray rows), found by two readers.
    **Fixed.**
13. **Version string of a source build** is `dev (commit none, built unknown)`
    and `sheepdog update` says "installed as a source". Fix: pass `-ldflags -X
    main.version=...` in `build_from_source` (about 5 lines, needs a tag
    source). Not done.
14. **`sheepdog status` on a stopped daemon without a key** shows `voice gemini,
    from config`, no setup hint. Not done.
15. **Demo before setup.** The page button answers 409 `the demo needs the voice
    key first: save it in step 1`; the CLI needs the key too. README said "No
    herdr yet? `sheepdog demo`". **Fixed** in the README and quick start.
16. **Voice answered "As I mentioned" to a first question** (P1 run 2, n=1),
    with no agents. Not investigated.
17. **A real browser was never driven.** Visual state, the microphone prompt and
    the popup actions (`herdr plugin action`) are untested. The startup hook ran
    and exited 0 (`sheepdog service: not ...`, log truncated).

## Docs and behaviour disagree

| # | Doc says | Real behaviour | State |
|---|---|---|---|
| 1 | quick start step 1: downloads the binary, checks the sha256 | With no release: 404, build from source, `dev` version | docs fixed |
| 2 | README step 3 `sheepdog pair` | Fails until phone access is on | fixed |
| 3 | README "No herdr yet? `sheepdog demo`" | Needs the key (button: 409, CLI: error) | fixed |
| 4 | quick start output ends "The page opened in your browser." | Only when a browser could be opened; absent with `--no-open` and over SSH | docs fixed |
| 5 | FAQ "a status question in about 2 s" | 2.0 s once, about 20 s once (cold orchestrator) | hedged |
| 6 | `CONFIG.md` `state.path` row | Row held another table's text | fixed |
| 7 | docs say `sheepdog setup` | doctor, demo, pair texts said `sheepdog init` | fixed |
| 8 | troubleshooting lists the install errors | The no-Go message and the `pair` loopback message were missing | rows added |
| 9 | `sheepdog open` opens the voice page | Over SSH it ran `open`/`xdg-open` on the server | fixed |
| 10 | (init text) open `http://127.0.0.1:7777/voice` | Port is `web.port` | fixed |

Matched exactly (checked against real output): port in use (three lines),
`sheepdog is already running`, `sheepdog is not running`, `No log yet`, the
three config-typo messages, `rejected the key` plus `nothing was saved`, doctor
`FAIL herdr ... no herdr on PATH`, env file 0644 `WARN`, the `/setup` 403 from
the LAN, the PATH warning and the non-link message of `plugin-fetch`,
`sheepdog update` and `sheepdog uninstall` prompts, the voice page and setup
page labels (`web/setup/index.html`).

## Left for a bigger change

- Publish a release and add `darwin_amd64` and `linux_arm64` (launch gate).
- Acknowledge slow orchestrator turns by voice (issue 7), with eval evidence.
- A real browser run of `/setup`, the microphone prompt and the plugin popups.
- Stamp the version in the source-build path (issue 13).
- Restore the TestFlight link when it exists (issue 1).
- A herdr install command in the quick start (issue 8).
