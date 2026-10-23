# Launch video: script and storyboard

The plan for re-recording sheepdog's launch video, for the owner's review. Its synthetic cut is
made (see [The video we have now](#the-video-we-have-now)); a filmed cut is not. It replaces the storyboard that was in [launch-kit.md](launch-kit.md), which still
holds the launch posts.

> **Decision (2026-10-21): we make it ourselves, synthetic, as the old one was, after the
> product work.** The redesign, the security work and the several-computers support land first,
> so the video shows the app as it will ship. Then we follow this storyboard with simulator
> recordings and the offline demo wherever possible, TTS voices as in the old pipeline
> ([scripts/readme-media/launch/](../../scripts/readme-media/launch/README.md)), and the new brand
> and logo. Shots that need a real phone (the lock screen, the Live Activity) are recorded on a
> device or drawn as before. The "film well" product changes in section 5 are done if there is
> time. The filming notes below stay as the reference for anything recorded live.

Every caption has to match what the real app does on the day of recording. Check each shot
against the app, not against this page, and drop a shot rather than fake it.

## The video we have now

The README plays a GitHub user-attachment of the first launch video (36 s, old brand). The
repository's `assets/readme/launch.mp4` (66 s, 1920×1080), `launch-vertical.mp4` (1080×1920) and
`launch-poster.png` are the synthetic re-make of this storyboard, in the new brand. How it is made
is in [scripts/readme-media/launch/README.md](../../scripts/readme-media/launch/README.md). In
short:

- **Synthetic, not filmed.**
  - The phone is the iOS Simulator running the offline demo in dark mode, driven by `LaunchRecordingUITests` and filmed with `simctl io recordVideo`.
  - The voices are Gemini TTS lines (`lines.tsv`; Puck plays the user and Kore plays the voice), not a live voice session. There is no waveform, and a footnote says the voices are text-to-speech.
  - `compose.py` draws the title, captions, setup steps and end card in the brand (ink, cream, one orange command; Outfit and Inter).
- **What it shows** (shots 2, 4 in-app, 5, 6, 7, 12 and 13): the title lockup; four agents at work; codex-payments' question with the chime and the voice; Approve; "What's going on?"; "Show me the agents" and its card; claude-auth's live terminal; Changes and a diff; the three setup commands; the end card.
- **What it leaves out**, because the demo cannot show it honestly: the lock screen and the Live Activity (a real device), and the attachment, New agent, the shell and the update notice (a real daemon, or the demo changes in [section 5](#5-what-to-prepare-and-what-would-need-code)). The README link still points at the old attachment; replacing it is the owner's call.

## 1. Goal, audience, length

**Goal.** In under a minute and a half, show that you can leave your desk and still run your
agents: you hear when one needs you, you answer, you read its diff and you start the next one,
all from the phone. Then show that setup is three commands and a QR code.

**Audience.**

1. herdr users who already run several agents and come back to find one blocked for an hour.
2. Developers who use Claude Code or Codex and scroll past on X, LinkedIn or 小红书 / Bilibili.
3. Reviewers and interviewers reading the README.

**Cuts.**

| Cut | Length | Frame | Where |
|---|---|---|---|
| Main | 75–85 s (target 80 s) | 16:9, 1920×1080 master | README (as a smaller file, see [Export](#export)), X, LinkedIn, YouTube |
| Vertical | the same edit, reframed | 9:16, 1080×1920 | Reels, Shorts, TikTok, 小红书, X mobile |
| Teaser | 15 s | 9:16 and 1:1 | social posts, the herdr community post, a looping README GIF if wanted |

**Sound.** Real voice takes where you can get them: the user's line on a lav or earbud mic, and
the voice's reply from a real voice session (Gemini Live or GPT-Live, with DeepSeek as the
orchestrator). The real model words things its own way. Keep the take and change the caption to
match it. Never dub. If a shot has to be built from the demo or TTS, the edit must not pass it off
as a live session: no live-session waveform over a TTS line.

## 2. Storyboard

The story is "away from the desk". The user sets four agents going, leaves for a walk, gets told
that one is blocked, approves it, asks what is going on, reads a diff on the phone, sends a
follow-up with a screenshot and starts a new agent. Then they come back to finished work, and the
video shows how to set it up.

The agent names follow the app's offline demo (the `shop` workspace: `claude-auth`,
`codex-payments`, `claude-migration` and `codex-docs`), so real-daemon shots and demo shots cut
together. The "Source" line says where each shot can come from (see
[Demo mode or a real daemon](#demo-mode-or-a-real-daemon)).

### Timeline

| # | Time | Beat | Screen |
|---|---|---|---|
| 1 | 0:00–0:04 | Four agents, then leave | desk, Mac |
| 2 | 0:04–0:08 | Walk out, title | street, title overlay |
| 3 | 0:08–0:15 | An agent blocks: you hear it first | phone lock screen |
| 4 | 0:15–0:21 | Approve from the lock screen | phone lock screen |
| 5 | 0:21–0:28 | "What's going on?" | phone, conversation |
| 6 | 0:28–0:33 | The orchestrator puts a card on screen | phone, card |
| 7 | 0:33–0:42 | The live terminal, then the diff | phone, agent window |
| 8 | 0:42–0:48 | Prompt with a screenshot | phone, composer |
| 9 | 0:48–0:56 | Start a new agent | phone, New agent |
| 10 | 0:56–1:02 | And: the shell, the update notice | phone montage |
| 11 | 1:02–1:08 | Back at the desk, work done | desk, Mac |
| 12 | 1:08–1:16 | Set up in three steps | Mac terminal, browser, phone |
| 13 | 1:16–1:22 | End card | brand card |

### Shots

Captions are English, with a Chinese line under it for the Chinese-language posts (burn only the
English into the README cut; ship the Chinese as a subtitle file or a separate export).

**1. Four agents, then leave (0:00–0:04)**

- **Screen:** the desk. The Mac shows herdr with four panes in the `shop` workspace, all working: two Claude Code, two Codex. The user puts in earbuds, picks up the phone and stands up.
- **Caption:** "Four agents working. You have a walk to take." / 「四个 agent 在干活，你出门走走。」
- **You:** none. **Voice:** none.
- **Source:** real herdr on the demo Mac.

**2. Walk out, title (0:04–0:08)**

- **Screen:** the user walks out of the door; outside, a wide shot, the phone in their pocket. Over it fades in the title lockup: the colour logo (`sheepdog-logo-color.svg`, with its cream sticker outline) on an ink card, **sheepdog** in Outfit, and the tagline under it.
- **Caption (the lockup):** "Manage your Herdr agents by voice." / 「用语音管理你的 Herdr agents。」
- **You:** none. **Voice:** none.
- **Source:** live action, plus a graphic.

**3. An agent blocks: you hear it first (0:08–0:15)**

- **Screen:**
  - Close-up of the earbud, then the phone taken out.
  - The lock screen shows a sheepdog notification: "codex-payments wants to run npm install p-retry. Approve?".
  - Corner insert, for one second: the alert pose cropped from `mascot-states-sheet.jpg`.
- **Caption:** "An agent is blocked. You hear it first." / 「agent 卡住了，你第一时间听到。」
- **You:** none.
- **Voice:** "codex-payments wants to run npm install p-retry. Approve?"
- **Source:** **real daemon and a real device only.** The notification comes through the shared push relay, and the voice comes from a running voice session.

**4. Approve from the lock screen (0:15–0:21)**

- **Screen:** the user long-presses the notification. **Approve** and **Decline** appear. They tap Approve, and Face ID (or the passcode) confirms. Show the Face ID moment: it is the rail, and it makes the point that approvals are guarded.
- **Caption:** "Approve from the lock screen, or just say it." / 「锁屏上直接批准，或者说一声。」
- **You:** none on screen. The alternative take is "Approve.", said aloud.
- **Voice:** "Approved."
- **Source:** real device and daemon. The approval card in the app (the voice take) works in the offline demo.

**5. "What's going on?" (0:21–0:28)**

- **Screen:**
  - The app open on the conversation. The listening orb (orange, the one accent) breathes while the user talks.
  - The reply appears as the voice speaks it.
  - Optional, for half a second: the Live Activity in the Dynamic Island.
- **Caption:** "Ask what's going on. Who needs you comes first." / 「问一句进展，最需要你的先说。」
- **You:** "What's going on?"
- **Voice:** "claude-migration failed: the column users.plan already exists. claude-auth is done, and codex-docs is still writing."
- **Source:** a real daemon for the real voice. The offline demo has the same agents and an on-device voice, which is good for blocking out the shot.

**6. The orchestrator puts a card on screen (0:28–0:33)**

- **Screen:** a card opens by itself: "Your agents", a table of the four agents and their state. The user taps claude-auth's row into its window.
- **Caption:** "Ask to see it, and it shows up on screen." / 「想看，它就显示在屏幕上。」
- **You:** "Show me the agents."
- **Voice:** a short spoken summary (whatever the take says).
- **Source:** the offline demo's "Show me the agents" (`-sheepdogDemoCard`), or a real daemon. A card opens only while the conversation is on screen.

**7. The live terminal, then the diff (0:33–0:42)**

- **Screen:**
  - claude-auth's live terminal in the agent's own colours, sized to the phone. The user pinches to a readable size.
  - The key bar shows (esc, tab, the arrows, ⌃C, return, ⌨︎).
  - ⋯ ▸ **Changes**: three files, +31 −4. A tap on `internal/auth/token.go` opens its diff, with red and green lines.
- **Caption (terminal):** "The agent's live terminal, on your phone." / 「agent 的实时终端，就在手机上。」
- **Caption (diff):** "Read the diff before you say yes." / 「先看 diff，再点头。」
- **You:** none. **Voice:** none.
- **Source:** the offline demo covers the window, the key bar and Changes/diff (`DemoChanges`), with the same `/Users/demo/code/shop` paths as the real shoot.

**8. Prompt with a screenshot (0:42–0:48)**

- **Screen:** in the same window, 📎 **Attach**, then a staged screenshot of a login error (a fake staging page). The user types "This is the error from staging. Handle it too." and sends. The agent picks it up in the terminal.
- **Caption:** "Send a prompt, a photo or a log." / 「发一句话、一张图或一段日志。」
- **You:** none (typed). **Voice:** none.
- **Source:** **a real daemon.** The demo accepts the attachment but answers the send with "This is the demo: … nothing was sent".

**9. Start a new agent (0:48–0:56)**

- **Screen:**
  - Back to Agents, then **New agent**. Pick the project `shop`, Claude Code and **a fresh git worktree on a new branch**.
  - First prompt: "Fix the migration: users.plan already exists. Make it idempotent." Then Start.
  - The new agent appears in the list as working.
- **Caption:** "Start the next agent from the sidewalk." / 「在路边就能开一个新 agent。」
- **You:** none (typed). It can also be said: "Start a Claude in shop to fix the migration."
- **Voice:** a short confirmation that the agent started (whatever the take says).
- **Source:** **a real daemon.** The demo shows the sheet but answers "This is the demo: its agents are scripted, so no agent was started."

**10. And: the shell, the update notice (0:56–1:02)**

A three-beat montage, about 2 s each, with one caption each.

- **10a.** ⋯ ▸ **Open shell here**: "Shell · shop" runs `git log --oneline -3`.
  - **Caption:** "A real shell when you need one." / 「需要时，还有一个真正的 shell。」
  - **Source:** a real daemon (the shell is on by default once the security work lands). The demo's shell prints "demo: nothing runs here", so don't use it on camera.
- **10b.** Settings, the top line: "sheepdog vX.Y.Z is available. To update, run this on your computer: `brew upgrade sheepdog`".
  - **Caption:** "It tells you when there's an update." / 「有新版本，它会告诉你。」
  - **Source:** this line shows only when the daemon knows of a newer release: film the offline demo with `-sheepdogDemoUpdate` in the simulator, or film just after a release with an older daemon.
- **10c (optional).** The Live Activity on the lock screen with Mute and End.
  - **Caption:** "Mute from your earbuds." / 「耳机上一键静音。」
  - **Source:** real device only.

**11. Back at the desk, work done (1:02–1:08)**

- **Screen:**
  - The user sits down. herdr on the Mac shows codex-payments done, and the new agent's pane on its branch with tests passing.
  - Corner insert: the celebrating pose.
  - Note: while an agent window is open on the phone, that pane on the Mac takes the phone's size. Close the window before the desk shot.
- **Caption:** "Come back to finished work." / 「回来时，活已经干完了。」
- **You:** none.
- **Voice:** "codex-payments is done, and its tests pass."
- **Source:** real herdr. Shoot it first, or stage it, and reuse it.

**12. Set up in three steps (1:08–1:16)**

Three quick cuts with a step number in the corner.

- **12a.** A terminal: `brew install kccarlos/tap/sheepdog`. A small line under it reads "or the herdr plugin".
- **12b.** `sheepdog setup`, then the browser wizard: the Models step with "Valid key", and Checks all green.
- **12c.** The wizard's **Pair your phone** QR (a dummy one), then the phone app's **Scan pairing code**, then "Paired". Then the TestFlight icon.
- **Caption:** "Install. Run setup. Scan the code." / 「安装，运行 setup，扫码配对。」
- **You:** none. **Voice:** none.
- **Source:** Mac screen recording (the setup wizard can come from `install-gif.sh`, which already types a placeholder key and swaps the QR for a dummy), plus a phone recording.

**13. End card (1:16–1:22)**

- **Screen:** the ink card (`#16181C`), the colour logo centred with its sticker outline, then:
  - **sheepdog**, with the tagline under it in cream.
  - `brew install kccarlos/tap/sheepdog` in mono, the one orange element.
  - "or: herdr plugin install kccarlos/herdr-sheepdog" in slate.
  - "iPhone app: testflight.apple.com/join/bjCaFYgK" and `github.com/kccarlos/herdr-sheepdog`.
- **Caption:** none; the card is the caption. In the Chinese export, add 「iPhone 应用：TestFlight 公测」.
- **You:** none. **Voice:** none.

### The 15-second teaser

| Time | Screen | Caption | Audio |
|---|---|---|---|
| 0:00–0:03 | Walking; the lock-screen notification | "An agent is blocked." | **Voice:** "codex-payments wants to run npm install p-retry. Approve?" |
| 0:03–0:05 | Face ID on Approve | "Approve from anywhere." | **Voice:** "Approved." |
| 0:05–0:09 | Agent window, then the diff | "Read the diff on your phone." | quiet bed |
| 0:09–0:12 | New agent, Start | "Start the next one." | quiet bed |
| 0:12–0:15 | End card: logo, tagline, brew command | "Manage your Herdr agents by voice." | |

### Brand on screen

Follow [brand.md](brand.md). Its sources of truth are
[assets/brand/colors.json](../../assets/brand/colors.json) and `web/static/tokens.css`.

- **Cards and lower thirds:**
  - Ink surfaces: `#16181C` for the background and `#1F2126` for caption pills, radius 14.
  - Cream text (`#F5F2EA`); slate (`#8E929A`) for secondary lines.
  - No gradients, and never pure black or white.
- **One accent per frame.**
  - Orange `#F39C1F` appears only where the product listens or needs you: the orb, the Approve button, the command on the end card.
  - Text on orange is ink.
  - Captions are cream, not orange.
- **Type:** Outfit for titles and the wordmark, Inter (or SF Pro) for captions, system mono for commands. Lowercase **sheepdog** in prose; SHEEPDOG in caps only on a hero lockup.
- **Logo and mascot.**
  - The colour logo on dark (`assets/brand/logo/sheepdog-logo-color.svg`, or the 1024 px sticker PNG for video).
  - Mascot poses only as crops from `mascot-states-sheet.jpg`: alert in shot 3, celebrating in shot 11.
  - Never recolour, flip or put text over the dog.
- **Motion:** 200 ms cuts and fades with `cubic-bezier(.2,.8,.2,1)`. No bounces or zooms on text.
- **The phone in dark mode** throughout (the canonical icon is the dark one), with the default accent.

## 3. Recording checklist

### Demo data

- **A separate macOS user account named `demo`,** so every path on screen reads `/Users/demo/…`.
  - It has its own `~/.config/sheepdog` and its own daemon. Your own daemon, keys and history stay out of frame.
  - Set `git config --global user.name "Demo"` and `user.email demo@example.com` there; `git log` and diffs show the author.
- **One fake repository, `~/code/shop`:** a tiny web shop with a Go `internal/auth`, a `migrations/` folder and a `package.json`. Add a few commits so `git log` has something to show. No real project, client or company names.
- **One herdr session, workspace `shop`, four panes, started with plain `claude` and `codex` (no approve-everything flags):**
  - `claude-auth`: a small task that finishes during the shoot ("validate empty and over-long tokens in internal/auth and add tests").
  - `codex-payments`: a task that needs `npm install p-retry`. Codex in its default approval mode stops and asks.
  - `claude-migration`: a migration that adds `users.plan` when the column already exists, so it fails.
  - `codex-docs`: a long docs task that is still writing.
  - Name the panes so the voice says those names. Real agents are not deterministic: plan two or three takes per beat.
- **`supervisor.project_roots`** lists `~/code` only, so New agent offers `shop` and nothing private.
- **Keys:** a Gemini key for the voice (or OpenAI for GPT-Live) and DeepSeek for the orchestrator, all in the demo user's `.env`. Run `sheepdog doctor` once and make sure it is clean, with chattiness `chatty`.
- **Push:** the default shared relay. Check on the device the day before that a notification arrives with the app closed and that Approve works from the lock screen.
- **Never on screen:**
  - API keys: the wizard's key field shows a placeholder, or blur it.
  - The pairing token: Settings, Daemon.
  - The real pairing QR: use `scripts/readme-media/launch/dummyqr.go` or blur. Press **New token** on the setup page after the shoot anyway.
  - Tailscale names and LAN addresses: the old wizard script rewrites them to `192.168.1.20`.
  - Other apps' notifications.
  - The Mac's menu bar and hostname: hide the menu bar or crop.
  - Contacts and real email addresses.

### Devices

- **iPhone:**
  - A Focus mode named "Filming" that silences everything but allows Sheepdog (its notifications are time-sensitive). Plain Do Not Disturb would hide shot 3.
  - Battery at 100 % or charging; Wi-Fi on, with Tailscale connected for the shell.
  - A plain wallpaper and a home screen page with only Sheepdog, TestFlight and Camera.
  - Dark mode. Text Size one step above the default, and check that nothing truncates. The terminal is pinched to about 40 columns.
  - The current TestFlight build, so the screens match the captions.
- **Clean status bar:**
  - The simulator: `xcrun simctl status_bar booted override --time 9:41 --batteryState charged --batteryLevel 100 --wifiBars 3 --cellularBars 4 --operatorName ''`.
  - A real iPhone has no override. A QuickTime recording over the cable usually shows a cleaned status bar; check it on a dry run. Otherwise cover the status bar with an ink strip in the edit for the screen shots.
- **Mac:** a clean desktop, a hidden Dock and menu bar, the terminal at 18 pt in a dark theme close to ink, and the browser in full-screen with no bookmarks bar or extensions.

### Capture

- **iPhone screen:**
  - (a) QuickTime Player on the Mac: New Movie Recording, then pick the iPhone as the camera and the microphone. It captures the screen and the phone's audio together, so this is the first choice for the voice shots.
  - (b) iOS Screen Recording from Control Center. Do a dry run to check whether it records the app's audio during a voice session.
  - Film the live-action shots (3, 4, 11) with a second camera and use the screen recording as inserts.
- **The voice's audio:** take it from the QuickTime capture (phone speaker route). If you use earbuds on camera, record a second take on the speaker for the sound and keep the earbud take for the picture.
- **Your voice:** a lav mic, or the second camera's mic, recorded separately and synced by clap.
- **Mac screen:** QuickTime or ⌘⇧5 at 60 fps for herdr and the wizard. The terminal half of the install can be re-rendered with `vhs` (`scripts/readme-media/launch/install.tape`).
- **Simulator** (only for demo-mode shots): `xcrun simctl io booted recordVideo --codec h264 shot.mp4`. Do not lock the simulator mid-take: since iOS 27 the recorder freezes after a lock (see the old pipeline's README).

### Demo mode or a real daemon

The app's offline demo (**Try the demo**, or `-sheepdogDemoSilent` in the simulator) uses the same
agents and the app's real views. It is good for blocking out and rehearsing every phone shot, and
for some final shots.

| Shot | Offline demo | Needs a real daemon |
|---|---|---|
| 3. Lock-screen notification | no | yes, on a device, via the shared relay |
| 4. Approve: lock screen | no | yes, on a device |
| 4. Approve: in-app card | yes | |
| 5. Status, with the real voice | the words yes; the voice is on-device speech | yes, for Gemini Live or GPT-Live |
| 5. Live Activity | no (the simulator does not draw it) | yes, on a device |
| 6. Card "Your agents" | yes (`-sheepdogDemoCard`) | or real |
| 7. Live terminal and key bar | yes | or real |
| 7. Changes and diff | yes | or real |
| 8. Attach and send a prompt | no (it says "This is the demo") | yes |
| 9. New agent | no (it says "no agent was started") | yes |
| 10a. Shell | no (it says "demo: nothing runs here") | yes |
| 10b. Update notice | yes (`-sheepdogDemoUpdate`) | or an older daemon |
| 11, 12 | | real herdr, real setup |

## 4. Editing notes

- **Music:** one quiet, warm bed (acoustic or soft electronic, no drops), only between the spoken lines.
  - Duck it to about −20 dB under speech, and out completely under the voice's lines.
  - Use a track with a licence that allows commercial social use, and keep the licence file with the project.
  - The two-tone chime from the old pipeline (an ffmpeg sine) can stay as the "needs you" sound in shot 3.
- **Captions:**
  - Burned in for the README and the vertical cut, because both autoplay muted. Inter Semibold, cream on an ink pill at 85 % opacity, in the bottom third and clear of the phone's UI. At most two lines.
  - The spoken lines are shown as said, labelled "You" and "Voice" only where it helps.
  - Also export `.srt` files, one in English and one in Chinese, for YouTube, LinkedIn and Bilibili.
- **Real takes:** write the final captions from the takes, not from this page. If the voice says it differently, the caption follows the voice.
- **End card:** see shot 13. Hold it for at least 4 s and keep the brew command large enough to read on a phone (about 1/20 of the frame height). Use the exact TestFlight link from the README: <https://testflight.apple.com/join/bjCaFYgK>.
- **Poster:** a new `assets/readme/launch-poster.png` from shot 7's diff frame or the title lockup, in the brand palette.

### Export

| Target | Format |
|---|---|
| GitHub README (user-attachment) | MP4, H.264 High, yuv420p, AAC, `+faststart`, **under 10 MB**. At 80 s that is about 1 Mbit/s in total, so export 1280×720 at 30 fps, two-pass. |
| X | MP4 H.264 + AAC, 16:9 1920×1080 or 9:16 1080×1920 |
| LinkedIn | MP4, 16:9 or 1:1 (1080×1080) |
| YouTube | the 1080p master, plus the vertical cut as a Short |
| Reels, Shorts, TikTok, 小红书 | 9:16 1080×1920, captions inside the safe area (clear of the bottom 20 % and the right edge) |
| Bilibili | 16:9 1080p with the Chinese subtitle file |

Check each platform's current length and size limits when you upload; they change.

The README bitrate is `video kbps = 9.5 × 8192 / seconds − 96` (9.5 MB to leave headroom, with
96 kbps of AAC). For 80 s that is about 870 kbps:

```sh
ffmpeg -y -i master.mov -vf scale=1280:-2 -c:v libx264 -preset slow -b:v 870k -pass 1 -an -f mp4 /dev/null
ffmpeg -i master.mov -vf scale=1280:-2 -c:v libx264 -preset slow -b:v 870k -pass 2 \
  -pix_fmt yuv420p -c:a aac -b:a 96k -movflags +faststart launch.mp4
```

Then upload it to a GitHub issue or PR comment to get a `user-attachments` URL, and replace the
link in the README.

## 5. What to prepare, and what would need code

### The owner can prepare these without code

- The `demo` macOS user, the `shop` repository with its commits, and the herdr session with the four agents and their tasks (above).
- The keys (Gemini or OpenAI, and DeepSeek) in the demo user's `.env`, and a clean `sheepdog doctor`.
- The shell switch on the setup page left on (it is on by default).
- On the device: a push test, the Filming Focus, the wallpaper and home screen, dark mode, the text size and the current TestFlight build.
- The staged "login error" screenshot for shot 8 (a fake page, made in any image editor).
- The graphics: the title lockup and the end card, from `assets/brand/`.
- A music track and its licence.
- A dry run of every phone shot in the offline demo, to time the cut before filming the real ones.
- After the shoot: revoke the demo devices on the setup page (or **Reset pairing**).

### Product changes that would help filming

Items 1, 5 and 7 are built; the rest are not. Each one is small, and each one only matters if the shot cannot be
filmed on a real daemon.

1. **Demo update notice.** Done: under `-sheepdogDemoUpdate` (debug builds) the demo's `/api/state` reports sheepdog v0.1.17 as available, so the main screen shows the update notice and shot 10b can be filmed offline.
2. **Demo new agent.** In the demo, Start would add a scripted, working agent to the list, instead of answering "no agent was started". This makes shot 9 filmable offline.
3. **Demo sends without the "This is the demo" replies.** The composer, attachments and agent actions would answer as a real daemon does, behind a recording-only flag. This covers shot 8.
4. **A demo shell with a few scripted commands** (`git log --oneline -3`, `git status`, `make test`) instead of "demo: nothing runs here". This covers shot 10a.
5. **Consistent demo paths.** Done: the demo's agents, Changes and New agent folders all use `shop` under `/Users/demo/code` (agents in `~/code/shop`, Changes on branch `fix-login`), the same paths as the `demo` macOS user above.
6. **More demo cards.** Today the demo only answers "Show me the agents". A "show me what claude-auth changed" card would back shot 6 with something closer to the diff.
7. **The recording pipeline (scripts and a test, not the app).** Done: `LaunchRecordingUITests`, `phone.sh`, `compose.py` and `lines.tsv` make the synthetic cut described in [The video we have now](#the-video-we-have-now).
8. **(Optional) A local notification in the demo** with the daemon's push text, to rehearse shot 3 on a phone without a daemon. Filming shot 3 still needs a real device, because a locked simulator stops the recorder.
