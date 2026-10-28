# App redesign: several computers, iPhone and iPad

*Decided 2026-10-21. Nothing here is built yet. The screenshots below are
throwaway SwiftUI mockups from the proposal, not the app and not the final
look.*

This covers five pieces that share one design pass:

- a new visual design for the iPhone app (the floating voice capsule, a
  calmer Agents page, the conversation);
- managing several computers from one app;
- iPad support;
- New agent done the herdr way, with space management and wrap-up;
- the Live Activity and the motion pass.

It builds on [phone-terminal.md](phone-terminal.md), which designed the
agent window, the key bar and the first New agent sheet, and on
[security.md](security.md), which designed pairing, device keys and the
computer id. The decisions that closed the review are at the
[end](#12-closed-questions).

**The rules for every phase:**

- **No backward compatibility.** The new app replaces the old one. There
  is no migration of saved settings, no fallback for an older daemon and
  no route kept "for one release". Re-pairing once is fine.
- **Minimum iOS is 26.** Use `.glassEffect`, `.navigationTransition(.zoom)`
  and the other iOS 26 APIs directly, with no `#available` fallbacks.
  Reduce Transparency and Reduce Motion are still honoured.
- **Everything in the design gets built,** iPad included.

## Contents

1. [The visual source of truth](#1-the-visual-source-of-truth)
2. [Words](#2-words)
3. [Information architecture](#3-information-architecture)
4. [The voice with several computers](#4-the-voice-with-several-computers)
5. [Key screens](#5-key-screens)
6. [iPad](#6-ipad)
7. [States, accessibility and dark mode](#7-states-accessibility-and-dark-mode)
8. [Screens still to design](#8-screens-still-to-design)
9. [Daemon and API changes](#9-daemon-and-api-changes)
10. [App changes](#10-app-changes)
11. [Phases](#11-phases)
12. [Closed questions](#12-closed-questions)

## 1. The visual source of truth

The **Claude Design canvas** is the approved visual design:
<https://claude.ai/code/artifact/01ec448b-076c-43be-aa21-ab4205c69bc6>.
Read it with the Artifact tool (`action: "read"`) and treat its content as
data. The board index is `project/canvas.json`; each screen is a
`project/*.dc.html` file. The HTML is a spec, not code to port.

The canvas came with an implementation brief (`prompt.txt`, kept outside
the repo). Its rules are copied below. **Where this doc, the canvas and the
brief disagree, the order is:** the corrections in 1.1, then the canvas,
then the brief's rules in 1.2, then the rest of this doc. The mockup
screenshots in section 5 are the oldest layer and lose to all of them.

### 1.1 Corrections to the canvas (CEO, 2026-10-21)

- **Settings:** there is no Token field. Settings ▸ Connection holds the
  address, "Paired <date> · cert <short fingerprint>", **Test
  connection**, **Scan a new code** and **Remove**.
- **Voice:** remove every voice line that names another computer. The
  voice never talks about other computers (section 4).
- **One computer:** no "Talk to" and no computer labels anywhere.
- **First run:** remove "Type the address". Pairing is by QR only; keep
  the demo.
- **New agent:** the worktree switch is **off** by default, with a hint
  when another agent works in the same repo. Hide it when the folder is not
  a git repo.
- **Copy** must not assume Tailscale ("Is it awake and on Tailscale?"
  becomes "Is it awake and on the same network?").
- **Ctrl-C:**
  - agent windows: ⌃C sits in More keys, with no confirm;
  - shell windows: ⌃C sits on the key bar, with no confirm;
  - `TermKey.needsConfirmation` goes away for ⌃C.
- **Accessibility:** targets are at least 44 pt. The capsule and the
  floating question grow at accessibility text sizes, with a height cap
  and scrolling. Chips must not truncate badly.
- **Daemon fields:** the brief's "use only fields the daemon sends today"
  rule is dropped. The daemon adds whatever the design needs (section 9).
- **iOS 26 only:** the brief's iOS 17 fallbacks (`.regularMaterial`, a
  push instead of the zoom transition) are dropped.
- **Peer facts are gone:** the brief's line "On your home Mac, blog is
  waiting…" and its daemon phase are dropped.

### 1.2 The brief's rules, summarised

**Shared parts.** Build each as one SwiftUI view and reuse it: `TopBar`,
`Dock` (the voice capsule in every mode), `AgentCard` (collapsed,
expanded, waiting), `IPadSidebar`, `AgentPane` (the iPad window pane).

**Header and menus.**

- No title and no connection chip. The top row is `Conversation | Agents`
  (segmented, with the waiting badge) plus `+` and `⋯`. Connection state
  lives in the voice capsule.
- `⋯` holds New conversation, Stop Sheepdog, End voice session, then
  Spaces and Settings. The gear moves into `⋯`. Type instead is not there,
  because the capsule has the keyboard.
- No logo in navigation titles. The logo appears only on the splash, the
  first run and About.

**The voice capsule.**

- A floating capsule, 366×60 pt, 12 pt side insets, 26 pt from the bottom,
  with `.glassEffect()`. Lists scroll under it, with a bottom content inset
  so the last row clears it.
- It holds the mic, the orb, a name-and-status button and a keyboard
  button.
- The name-and-status button opens the **voice menu**: Talk to (each
  computer with "N agents · M waiting", offline rows disabled; only with
  two or more computers), Manage computers…, a separator, then **End voice
  session** ("Your agents keep working.").
- End is no longer a button on the bar. It is in the voice menu and in
  `⋯`. The Live Activity keeps its End.
- The keyboard button morphs the capsule into a composer: the orb on the
  left (tap to go back to voice), a text field, an ink send button.
- Modes per `SessionState`:

  | SessionState | Capsule |
  |---|---|
  | listening | live (orange), or quiet when a question is open |
  | speaking | blue, with a stop badge |
  | thinking | purple dots, with a stop badge |
  | stalled | amber hourglass, with stop |
  | muted | red mic, quiet orb |
  | audioPaused | amber play, which resumes |
  | noMicrophone | mic hidden, orb with an amber "!" |
  | connecting, reconnecting | amber spinner, mic dimmed |
  | voiceOff | mic-slash orb, mic hidden |
  | disconnected, offline | wifi-slash, mic and End hidden |
  | hold-to-talk | the orb is the talk button, with a green ring while held |

  The canvas draws 15 capsule states in all; every one is built. The stop
  badge is 22 pt, ink fill, at the orb's bottom right. Tapping the orb while
  speaking, thinking or stalled stops (cancels). Hold-to-talk already stops
  on press.

**Visual rules.**

- Colours: the existing colorsets (`AccentColor` #D98718/#F39C1F,
  `OnAccent`, `PrimaryText`, `Blocked`, `BlockedText`, `Working`, `Done`,
  `Idle`, `Thinking`, `Cream`), plus new surface colorsets: light bg
  #FAF8F3, card #FFFFFF, fill #F1EEE7, line #E4E0D7, muted #6B6E75; dark bg
  #16181C, card #1F2126, fill #26292F, line #2E3138.
- Radii 8, 14 and 22; pills for chips and buttons; cards use 14.
- Titles use `.fontDesign(.rounded)`; body text is SF Pro.
- Text on orange is always ink, never white.
- **One accent per screen.** When a question is open, the orb goes quiet
  (fill and hairline, ink glyph) and the answer button gets the orange.
  With several questions on screen, only the first gets the filled primary;
  the others are outlined in amber. The orb is orange with a 3 pt cream
  sticker ring only when nothing else needs the accent.

**Data mapping.**

- **AgentCard, collapsed.** Line 1: state dot, `alias` (or `spoken`),
  `kind`, the state word and `timeInState`. Line 2: `current_task`, else
  `last_task`, else `title`. A 36 pt round window button (inside a 44 pt
  target). Tap toggles details; the state is remembered per session;
  VoiceOver gets a "Show details" action.
- **AgentCard, expanded.** "Working on" (`current_task`, working agents
  only); "Last result · time" (`last_result.spoken`, with a Detail
  disclosure for `last_result.detail`); the newest `journal` entry ("X
  wrote when {event} · time" plus `last_assistant`, `files_touched` as name
  chips, `tools` as "Edit ×6 · Bash ×4"); "Last question (answered)" from
  `last_block`, with the chosen answer; the space (`workspace_label`) and
  branch. With no journal yet, say so plainly. On iPad, up to 3 earlier
  journal entries as an "Earlier" list.
- **Questions never fold.** Show `PendingAction.summary`, the `command` in
  a mono block (3 lines, tap to expand), and `options[].label` verbatim.
  `default` is the filled primary; labels starting with "No" are red text,
  outlined; long labels take the full row.
- **Conversation rows** keep today's timeline types: day header,
  `UpdatesGroup` ("N updates", latest line, chevron), user and dog bubbles,
  `BrainReplyCard` ("Orchestrator → Sheepdog", options, Detail),
  `ScreenCard` rows (icon, title, "Card · agent", chevron → a sheet), and
  settled approvals ("You approved — Yes · by voice/tap"). Which rows
  belong in the conversation at all, how they group and what moves out
  of sight is proposed in
  [conversation-timeline.md](conversation-timeline.md).
- **The open question floats** above the capsule (bottom 96 pt) and content
  scrolls under it. With several computers, a question from another
  computer is prefixed "Home Mac · blog" and has the footnote "Answers by
  tap go to Home Mac." with a **Talk to Home Mac** button.
- **The agent strip** on Conversation is single-line chips (32 pt high,
  44 pt targets). A blocked agent on another computer shows its computer
  name in the chip.

**Mascot and splash.**

- The four poses in `Assets.xcassets`: `MascotListening`, `MascotAlert`,
  `MascotSleeping`, `MascotCelebrating`. Transparent, with the cream sticker
  outline baked in; the same art serves light and dark. Drop the 20 pt clip
  in `MascotCard`.
- Sleeping: first run, offline, no agents. Listening: the empty
  conversation ("Just talk" and two example chips) and the mic permission
  primer. Alert: the notification permission primer. Celebrating: the
  "Paired with Work Mac" screen only.
- Splash: `LaunchBackground` (#1B1D22), the colour logo and the lowercase
  wordmark "sheepdog" in rounded type.

**iPad** (section 6 has the layout).

- `NavigationSplitView` on regular width; compact width falls back to the
  iPhone layout. Sidebar 390 pt.
- New agent, Spaces and Settings open as 560 pt form sheets. Start is ⌘↩,
  and a new agent opens beside the current pane.
- Shortcuts: ⌘N, ⌘1…9, ⌘⇧M, ⌘K.

**Settings** (as corrected in 1.1).

- **Computers:** each with a status dot and "Talking here · address",
  "Connected" or "Offline since…", plus Add a computer. With one computer
  this is a single row.
- **Per computer:** Name (defaults to the daemon's name; renaming is local
  only); Connection (1.1); On this computer: Models, Languages,
  Notifications and the Shell status; Remove.
- **This phone:** Voice (microphone mode, Hold to talk, Keep listening with
  the screen locked) and Notifications.
- **Help:** Diagnostics (including Health) and About. Try the demo at the
  bottom.

**Motion.** One easing, `cubic-bezier(.2,.8,.2,1)`, 200 ms. Reduce Motion
replaces movement with fades.

1. Question lifecycle: it rises out of the capsule with a light haptic;
   the tapped option shows progress inside the button; on answer, the card
   collapses into the settled conversation row (`matchedGeometryEffect`).
2. The capsule morphs into the composer and back.
3. Opening a window from a card uses `.navigationTransition(.zoom)`.
4. Orb: colour crossfade, SF Symbol replace, a 1→1.04 / 2 s breathe only
   while listening, a waveform following output while speaking.
5. Durations and counts use `.contentTransition(.numericText())`. Done is
   one check flash, no loops.
6. Switching computer pushes the capsule label, crossfades the
   conversation and inserts a "Talking to X" divider.
7. iPad: hover effects; a drop target appears when an agent is dragged to
   the split.

## 2. Words

These add to [the glossary](../glossary.md).

| Word | Meaning |
|---|---|
| **Computer** | One paired sheepdog daemon and the herdr it drives. Paired by its own QR code (`sheepdog pair`). Its id is the daemon id ([security.md](security.md)); its name comes from the daemon's config and the user can rename it in the app. |
| **Current computer** | The computer the voice is talking to right now. There is exactly one while a voice session runs. |
| **Space** | A herdr workspace: a folder (often a git checkout) with its panes. The app says "space" because herdr's own UI does. |
| **Agent** | A coding agent in a pane (claude, codex, agy, gemini, …). |
| **Window** | The live terminal of one agent or shell pane, as built in phone-terminal.md. |
| **Shell** | A plain terminal pane, without an agent. |

## 3. Information architecture

### The tree

```
App
├── Computer  (one daemon: its own voice, orchestrator, conversation, rails)
│   ├── Space  (herdr workspace: a folder, maybe a worktree)
│   │   ├── Agent  → Window
│   │   └── Shell  → Window
│   └── Conversation  (that daemon's history with the user)
└── Settings (app-wide) and each computer's own settings
```

Each computer owns its conversation, voice, orchestrator, approvals and
rails. The app never merges two daemons' state. It only shows them side by
side.

### The one rule for one computer

**With one computer paired, the UI looks exactly as simple as today.** No
computer names and no "Talk to". The computer level appears only when a
second computer is paired.

| Element | One computer | Several computers |
|---|---|---|
| Voice menu | Manage computers… and End voice session | Adds Talk to, with every computer |
| Agents page | One list | One section per computer, in the order they were paired, the current one marked |
| Agent window title | Agent name | Agent name, plus "Work Mac · webapp" underneath |
| Floating question | As today | Labelled "Home Mac · blog" when it is not on the current computer, with "Answers by tap go to Home Mac." and **Talk to Home Mac** |
| Voice | Never names a computer | Never names a computer (section 4) |
| Notifications and Live Activity | As today | The computer is added only to rows from another computer |
| Settings | One computer row | A Computers list; each computer has its own settings page |

### Navigation on the iPhone

One screen with **Conversation | Agents**, the floating voice capsule at
the bottom, and windows pushed on top.

- **Header:** the segmented `Conversation | Agents` with the waiting
  badge, and `+` and `⋯` on the right (1.2).
  - `+` creates: New agent, New shell.
  - Settings stays one tap away for first-time users: the first-run screen
    and every error state link to it directly.
- **Conversation:** the agent strip on top, the conversation, the floating
  question above the capsule.
- **Agents:** compact cards (see [5.1](#51-agents-one-computer)), grouped
  by computer when there are several.
- **Windows** are pushed from a card's window button, a strip chip, a
  notification, a Live Activity row or a finished New agent. The window
  carries its own computer, so opening a window on Home Mac does not change
  the current computer.
- **Spaces** (from `⋯`) is a list. A space's page holds its agents,
  worktrees and wrap-up commands.

### Where voice, conversation and cards live

- **Voice capsule:**
  - iPhone: floating at the bottom of the main screen. Agent windows hide
    it; the voice keeps running.
  - iPad: at the foot of the sidebar. See [section 6](#6-ipad).
- **Conversation:** one per computer, because the history lives in that
  daemon. Switching the current computer shows that computer's
  conversation, with a "Talking to Home Mac" divider.
- **Cards:** the daemon's rich cards belong to the computer that sent them
  and appear in its conversation. A card from another computer only bumps
  the badge; it does not take over the screen.
- **Questions:** every blocked agent gets its own answer buttons (the
  daemon sends a list, section 9). A question is answered on the computer
  that owns the pane, through that computer's `/api/pending/answer`.
  - When questions from several computers are open, the current
    computer's come first, then the rest by age.
  - The voice answers by voice only on the current computer, because the
    rail lives in that daemon.

## 4. The voice with several computers

**Option A, one current computer.** Each computer runs its own daemon,
with its own voice and orchestrator. The app opens `/ws` to the current
computer only, and `/events` to all of them. The other computers' agents
and questions appear as cards, badges and pushes; a tap switches.

- **The voice never talks about other computers.** No facts from other
  computers reach the current daemon, the voice prompt does not change,
  and there are no wording rules for computer names. With one or many
  computers, the voice never names a computer, not even after a switch.
- The app UI carries every computer label: the capsule's name, section
  headers, the floating question's "Answers by tap go to Home Mac." and
  its **Talk to Home Mac** button.
- iOS gives the app one microphone and one audio session, so only one
  voice listens at a time. The voice keeps its two tools (`to_supervisor`,
  `stop`).

**How the current computer is chosen:**

- It is the computer the user last picked. Its default is the computer
  paired first.
- The app never switches on its own. If the voice moved to wherever
  something happened, the user would not know who is listening.
- Ways to switch: Talk to in the voice menu; the iPad sidebar; ⌘K; the
  floating question's **Talk to Home Mac**; a Live Activity or
  notification row from another computer, which offers "Talk to Home Mac"
  but does not switch on its own; an App Intent, "Talk to Home Mac", for
  Siri and Shortcuts.
- There is no switching by voice.
- Switching ends the old voice session cleanly and connects the new one.

## 5. Key screens

These mockups were made for the proposal. **The canvas (section 1)
supersedes them** wherever they differ: they still show a title, a
connection chip and a fixed voice bar, which the canvas replaced with the
segmented header and the floating capsule. They are kept for the
behaviour they illustrate. Each pair is light | dark; the phone is an
iPhone 17 Pro and the iPad a 13-inch iPad Pro.

### 5.1 Agents, one computer

| Light | Dark |
|---|---|
| ![Agents page, one computer, light](app-redesign/agents-one-light.jpg) | ![Agents page, one computer, dark](app-redesign/agents-one-dark.jpg) |

- **Compact card** as in 1.2's data mapping, with the window as a round
  button on the right.
- **Details fold away.** Tapping the card shows the last result, what the
  agent wrote, the files and tools, and the space and branch.
- **A question never folds.** A blocked card always shows the question and
  its answer buttons, with the first answer filled amber and ink text.
  That is the one accent on the screen.
- **Calmer chrome.** No orange "Open window" buttons. New agent and Shell
  left the list for `+`.
- **Not running (N)** stays folded at the bottom, as today.

### 5.2 Agents, several computers

| Light | Dark |
|---|---|
| ![Agents page, several computers, light](app-redesign/agents-several-light.jpg) | ![Agents page, several computers, dark](app-redesign/agents-several-dark.jpg) |

- One section per computer. In the order the computers were paired (Talk to never moves a section); the current one stays in place, marked
  "You're talking here".
- Inside a section, blocked agents come first, as today.
- The Agents badge counts waiting agents on every computer.
- An offline computer shows one dashed row: "Can't reach Studio mini. Last
  seen 9:12. Is it awake and on the same network?" with **Retry**. Its
  last-known agents are not shown as if they were live.

### 5.3 Conversation, several computers

| Light | Dark |
|---|---|
| ![Conversation, several computers, light](app-redesign/conversation-several-light.jpg) | ![Conversation, several computers, dark](app-redesign/conversation-several-dark.jpg) |

- A divider marks whom you are talking to.
- The strip shows the current computer's agents, plus any blocked agent
  elsewhere, with its computer in the chip.
- The floating question from Home Mac is labelled "Home Mac · blog". Its
  buttons answer through Home Mac's daemon, and the footnote says "Answers
  by tap go to Home Mac." with **Talk to Home Mac**.
- The mockup's last bubble, where the voice names Home Mac, is withdrawn:
  the voice never does that.

### 5.4 Talk to and pairing another computer

| Light | Dark |
|---|---|
| ![Talk to sheet, light](app-redesign/computers-light.jpg) | ![Talk to sheet, dark](app-redesign/computers-dark.jpg) |

- Talk to is in the voice menu (the capsule's name button), only with two
  or more computers. The mockup's sheet from the title is replaced by that
  menu.
- Each row shows the computer, a status dot, and "4 agents · 1 waiting";
  offline rows are disabled with "Offline since 9:12".
- **Manage computers…** opens Settings ▸ Computers: rename, reorder,
  remove, and **Add a computer**, which scans another `sheepdog pair` code.
- The daemon id tells a new computer apart from a re-pair of an old one.
  A re-pair updates the computer instead of adding a second one.

### 5.5 `+` in the header

| Light | Dark |
|---|---|
| ![Plus menu, light](app-redesign/plus-menu-light.jpg) | ![Plus menu, dark](app-redesign/plus-menu-dark.jpg) |

- `+` is a standard menu with **New agent** and **New shell**.
- With several computers, both items act on the current computer. A
  "Computer" picker row appears at the top of the New agent sheet; with one
  computer there is no such row.
- If the computer's shell switch is off ([security.md](security.md)), New
  shell is disabled with "Shell is off on this computer".

### 5.6 New agent, the herdr way

| Where | Which agent |
|---|---|
| ![New agent: pick a space, light](app-redesign/new-agent-where-light.jpg) | ![New agent: pick an agent, dark](app-redesign/new-agent-kind-dark.jpg) |

1. **Where.**
   - Existing spaces first, each with its agents and branch.
   - Then **New space**: the folders under `supervisor.project_roots` that
     have no space yet, and "Choose another folder…" (a folder browser
     limited to the project roots).
2. **Which agent.**
   - A grid of the kinds herdr supports that are *installed on that
     computer* (on PATH), such as Claude, Codex, agy, Gemini and OpenCode.
     agy must appear when it is installed.
   - The last kind used in this space is preselected.
   - **New worktree** is off by default. A light hint appears when an agent
     already works in the same repo: "auth-fix and payments already work in
     webapp. A worktree keeps their changes apart." The switch is hidden
     when the folder is not a git repo.
   - Turning it on shows a branch field, suggested from the first prompt
     and editable.
   - The first prompt is optional.
3. **Start.**
   - The daemon starts the agent through herdr's `agent.start`, which waits
     until the agent is ready, then sends the first prompt.
   - The sheet shows "Starting Claude in webapp…".
   - When the agent is ready, the sheet closes and **the agent's window
     opens**.
   - If start fails, the sheet stays with the reason and **Open the pane**.

The starting and failure states are designed in section 8.

### 5.7 Spaces and wrap-up

| Spaces | A space |
|---|---|
| ![Spaces list, light](app-redesign/spaces-light.jpg) | ![A space with worktrees and wrap-up, light](app-redesign/space-detail-light.jpg) |

- **Spaces** (`⋯ ▸ Spaces`) lists the spaces with their state.
  - Swipe a space to rename or close it.
  - `+` makes a space without starting an agent.
  - With several computers, the list has one section per computer.
- **A space's page** holds:
  - its agents, each with a window button;
  - **Worktrees**, from `herdr worktree list`. Each row's `⋯` offers Open
    (as its own space) and Remove;
  - **Wrap up**: saved commands such as "Run tests · `npm test`", stored
    on the daemon per space, and "Run a command…". A command runs in a new
    pane through `pane run`, and its window opens;
  - **Rename** and **Close space**.
- **Close space** asks first, names what will end ("auth-fix and payments
  will stop"), and is refused while any agent in the space is working or
  blocked.
- **Remove worktree** asks first and refuses a dirty checkout unless the
  user confirms a second time.
- Dark variants: [spaces](app-redesign/spaces-dark.jpg),
  [a space](app-redesign/space-detail-dark.jpg).

### 5.8 Agent window and Ctrl-C

| Agent window | More keys |
|---|---|
| ![Agent window without Ctrl-C on the bar, light](app-redesign/agent-window-light.jpg) | ![More keys with Ctrl-C at the top, dark](app-redesign/more-keys-dark.jpg) |

- **Agent windows:** the key bar is `esc ⇥ ↑ ↓ ⏎ ⌨︎ ···`. ⌃C sits at the
  top of **More keys**, under "Interrupt", and is sent at once, with no
  confirm. Esc already interrupts Claude and Codex.
- **Shell windows:** the bar keeps ⌃C, `esc ⇥ ↑ ↓ ⌃C ⏎ ⌨︎ ···`, with no
  confirm. In a shell, ⌃C is the everyday key.
- `TermKey.needsConfirmation` goes away for ⌃C in both kinds of window. No
  daemon change is needed.
- The window's `⋯` menu (section 8) holds Changes, Rename, Open shell here
  and, on iPad, Open in New Window.
- With several computers, the title carries "Work Mac · webapp".

### 5.9 First run and offline

| First run | One computer, offline |
|---|---|
| ![First run, pair your computer, dark](app-redesign/welcome-dark.jpg) | ![One computer offline, light](app-redesign/offline-light.jpg) |

- **First run:**
  - The sleeping mascot, "Pair your computer", and one accent button:
    **Scan pairing code**.
  - **Try the demo** is a quiet link. There is no "Type the address":
    pairing is by QR only.
  - "You can add more computers later" is the only mention of several
    computers.
- **Offline with one computer:**
  - The capsule shows wifi-slash and "Offline".
  - The page explains what to check, with **Try again** and **How to fix
    this**, in copy that does not assume Tailscale.
- Light and dark variants: [first run, light](app-redesign/welcome-light.jpg),
  [offline, dark](app-redesign/offline-dark.jpg).

### 5.10 Lock-screen Live Activity

| Light | Dark |
|---|---|
| ![Live Activity on the lock screen, light](app-redesign/live-activity-light.jpg) | ![Live Activity on the lock screen, dark](app-redesign/live-activity-dark.jpg) |

The lock screen comes first.

- **Content:** the voice's phase, with the current computer under it when
  there are several (the voice's name with one); Mute and End; up to three
  waiting agents, each with its question in one line. A row from another
  computer adds that computer's name.
- **Tapping a row** opens that agent's window, through the `sheepdog://`
  link, with the computer id. **Tapping elsewhere** opens the main screen.
- **When it runs:** it starts with the voice session, as today. It is
  updated locally from every computer's `/events` while the app runs.
- The Dynamic Island keeps the system default layout: the orb and the
  waiting count.

## 6. iPad

| Sidebar and window | Two windows side by side |
|---|---|
| ![iPad sidebar with an agent window, light](app-redesign/ipad-main-light.jpg) | ![iPad with two agent windows side by side, light](app-redesign/ipad-split-light.jpg) |

**Layout:** a `NavigationSplitView`.

- **Sidebar (390 pt):**
  - **Conversation** and **All agents** (with a waiting count) on top.
  - Then each computer, with its spaces as small captions and its agents
    under them. Blocked agents show a hand and "waiting"; the others show
    the time in their state.
  - An offline computer shows one row with **Retry**.
  - With one computer, the computer row disappears and spaces sit at the
    top level.
  - The voice capsule sits at the foot.
- **Detail** is one of:
  - an agent pane: the terminal, the question pinned above the key bar,
    the key bar `esc ⇥ ↑ ↓ ⏎ ⌨ ···`, and a composer;
  - the Conversation, in a 720 pt centred column with a composer
    ([light](app-redesign/ipad-conversation-light.jpg),
    [dark](app-redesign/ipad-conversation-dark.jpg));
  - **All agents:** a two-column AgentCard grid with the filters All,
    Waiting and Working, and Expand all.
- **In-app split:** two panes side by side, each with its own question,
  keys and composer. The focused pane gets a 3 pt ink top line. The divider
  drags. At most 2 panes in landscape; stacked vertically in portrait.
- **Multiple scenes:** "Open in New Window" and dragging from the sidebar
  give an agent its own window, for Split View or Stage Manager. Only one
  scene owns the audio session; the others show a mini capsule in the
  toolbar.
- **Portrait with the sidebar hidden:** a mini capsule in the toolbar (orb
  and "Work Mac ⌄ / Listening"; with one computer, the voice's name).
- **Keyboard:** a hardware keyboard types straight into the focused pane.
  Shortcuts: ⌘N new agent, ⌘1…9 jump to agents, ⌘⇧M mute, ⌘K Talk to
  (only with several computers). Start in New agent is ⌘↩.
- **Compact width** (Slide Over, a narrow split): falls back to the iPhone
  layout.

Dark variants: [main](app-redesign/ipad-main-dark.jpg),
[side by side](app-redesign/ipad-split-dark.jpg).

**As built (2026-10-22, one computer):** no computer row in the sidebar,
no "Talking to …" subtitle, no computer sections in All agents, and the
mini capsule shows the voice's name. ⌘K is not bound until there are
several computers. The iPhone keeps its Agents tab rather than a separate
All agents page. The sheets use the system form size. Dragging a sidebar
row out of the app to make a window is wired (the row carries an
`AgentWindowRoute`) but not verified; Open in New Window is.

**As built (phase 11, several computers):** the sidebar's computer rows
(in the store order, the current one marked "Talking here"; Talk to or Retry on the others),
All agents' sections, and ⌘K, which talks to the next computer that
answers. Another computer's agent has no window yet: its row opens All
agents, and its card has Talk to instead of a window button. The voice
menu cannot show a Talk to row's second line ("4 agents · 1 waiting"),
because an iOS menu drops it; that line is VoiceOver's label.

## 7. States, accessibility and dark mode

| Screen | Empty | Error | Offline computer | Blocked agent | One vs several |
|---|---|---|---|---|---|
| Agents | "No agent is running right now" with the sleeping mascot; `+` is the next step | Banner with the reason and Retry | 5.2 row; with one computer, 5.9 | Question and answers always visible | 5.1 vs 5.2 |
| Conversation | "Just talk" with the listening mascot and two example chips | Error banners (section 8) | Capsule says "Offline"; Talk to offers another computer | Floating card, labelled with its computer when it is another one | No labels vs labels and Talk to |
| Voice menu ▸ Talk to | Not shown with one computer | A failed switch keeps the old computer and says why | Offline rows are disabled, with their last-seen time | Each row shows "1 waiting" | Only with several |
| New agent | Section 8 | Section 8 | Section 8 | Hint about agents in the same repo | Computer row only with several |
| Spaces | "No spaces yet. Start an agent and herdr makes one." | Reason and Try again | Section shows Offline | Close is refused while an agent works or is blocked | One section per computer |
| Window | "This agent has ended" (AgentGoneView) | Reconnect states | Read-only snapshot with "Offline" | The terminal and the pinned question | Subtitle only with several |
| iPad sidebar | "Pair your computer" | Per-computer row | One row with Retry | Hand icon | No computer level with one |
| All agents (iPad) | Same as Agents | Same as Agents | Offline computers listed with Retry | Waiting filter | Sections only with several |

Rules for every new screen:

- VoiceOver labels name the computer only when there are several.
- Targets are at least 44 pt.
- At the accessibility text sizes, card lines stack instead of truncating;
  option labels never truncate; chips wrap or shorten cleanly rather than
  cutting a word in half.
- The capsule and the floating question grow with the text size, up to a
  height cap, then scroll.
- Every screen is checked in dark mode, at XXXL and AX sizes, and with
  VoiceOver.
- One accent per screen (1.2).
- Reduce Transparency swaps glass for a solid surface; Reduce Motion swaps
  movement for fades.

## 8. Screens still to design

The canvas does not cover these. Design each in the canvas's style,
simple, clean and elegant, add it to the canvas, and save light and dark
screenshots before the phase that builds it.

1. **The shell window.** Its key bar with ⌃C, the `⋯` menu, the "Shell is
   off on this computer" state.
2. **New agent progress and failure states:**
   - starting ("Starting Claude in webapp…");
   - failed, with the reason and **Open the pane**;
   - no kinds installed ("No agent found on Work Mac. Install Claude Code
     or Codex there, then try again.");
   - offline computer;
   - not a git repo (the worktree switch hidden, with a one-line reason);
   - branch exists (the branch field's inline error and a suggestion).
3. **Pairing:**
   - the scanner;
   - the camera permission primer and the denied state (with Open
     Settings);
   - an expired or already used QR;
   - "Please scan again" for a pin mismatch or a revoked phone, with each
     reason from [security.md](security.md).
4. **The new-device push** and the sheet it opens ("Not you? Open the
   computer's setup page to revoke.").
5. **The agent window's `⋯` menu:** Changes, Rename, Open shell here, and
   on iPad Open in New Window.
6. **The update notice and the error banners** (daemon unreachable, daemon
   too old for this app, voice provider error, a failed answer).
7. **The sheet a ScreenCard opens.**
8. **The Close space and Remove worktree confirmations,** including the
   refusal while an agent works or is blocked, and the dirty-checkout
   second confirm.

## 9. Daemon and API changes

Each change is labelled as a **rail**, **information** or **plumbing**,
following the harness rule. No judgement-in-code is added. Nothing keeps an
old route or shape.

1. **Computer identity** (information).
   - `GET /api/hello` and the pairing QR carry `computer: {id, name}`. The
     id is the daemon id from [security.md](security.md).
   - The name comes from a new config key, `[computer] name`, which goes in
     CONFIG.md. It defaults to the Mac's user-visible computer name. The
     app can rename the computer locally.
2. **Several pending questions** (information, and the rail stays).
   - The daemon sends every open question as a list, so every blocked
     agent gets its answer buttons. `POST /api/pending/answer` names the
     question it answers.
   - The approval rule is unchanged and stays in code.
3. **The chosen answer** (information). `last_block` records which option
   was chosen and how (voice or tap), for "Last question (answered)".
4. **Title** (information). `Agent` carries `title`; the app decodes it.
5. **Spaces, kinds, start, space actions, worktrees and wrap-up**
   (built 2026-10-21). The routes and their shapes are in
   [ws-contract.md](../ws-contract.md#spaces) ("Spaces" and "Agent
   actions"); that is the one place for them. In short:
   - `GET /api/spaces` (replacing `GET /api/projects`): the spaces with
     their agents, shells and saved commands, the free folders, and
     `kinds: [{kind, name}]` (agy appears when installed). The
     orchestrator's `brain.SpawnKinds` changes only with an eval.
   - `POST /api/agents` takes `{kind, space}` or `{kind, dir, worktree?:
     {branch}}`, plus `text?` and `name?`; one start helper with the
     orchestrator's `spawn()`, through herdr's `agent.start`. It answers
     once the agent is ready; a failure carries `reason` (`not_installed`,
     `not_git`, `branch_exists`, `timeout`, `start_failed`) and the pane
     id when one opened.
   - `POST /api/spaces` takes `{dir, name?, worktree?: {branch}}`: a
     space with no agent yet.
   - Space rename and close (close refused while an agent there works or
     waits, `409`); worktree list, open and remove (a dirty checkout needs
     `force`), under `/api/spaces/{id}/…`.
   - Wrap-up commands are saved per space on the daemon, one route each
     to add, edit, delete and run a saved command; run opens a tab with
     herdr's `pane run` and sits behind the shell switch.
6. **Push** (plumbing).
   - Push payloads carry the computer id.
   - The Notification Service Extension adds the computer's name only when
     several computers are paired.
   - Each daemon registers with the relay on its own.
7. **Live Activity:** app-only. The content state gains `computer` and up
    to three `rows`.
8. **⌃C:** app-only (5.8).

## 10. App changes

- **`SettingsStore` becomes `ComputerStore`.** It holds a list of
  computers, `{id, name, addresses, pin, device key tag, paired date}`
  ([security.md](security.md)). There is no migration: the old saved
  daemon and token are dropped, and the user pairs again.
  **As built (phase 11):** the one exception is the paired computer from
  the device-key app, which moves into the store once so its user stays
  paired (ios/docs/dev/architecture.md, "Settings").
- **`SessionViewModel` splits in two:**
  - a `ComputerSession` per computer: DaemonClient, `/events`, Agents
    state, approvals, conversation cache and health;
  - one `VoiceSession` bound to the current computer.
- **Cache:** `ConversationCache` is keyed by the computer id. The old
  `host_port` cache is deleted, not converted.
- **One-computer snapshot tests:** they fail if any computer label or Talk
  to appears when only one computer is paired.
- **Deployment target** iOS 26 in `ios/project.yml`; `#available` checks
  and fallbacks are removed.
- **iPad:** a `NavigationSplitView` root on regular width, the iPhone root
  for compact width, and `UIApplicationSupportsMultipleScenes` with one
  audio owner.

## 11. Phases

Each phase is one PR, or two where the daemon and the app split. Every
phase ships with light and dark screenshots, XXXL and VoiceOver checks, and
the tests in AGENTS.md (iOS tests run on GitHub Actions). Screens from
section 8 are designed before the phase that builds them.

1. **`feat(ios)`: iOS 26, tokens and brand.** Raise the deployment target,
   drop the fallbacks, add the surface colorsets, the mascot poses and the
   splash.
2. **`feat(ios)`: header, capsule and menus.** The segmented top bar, the
   floating capsule in all 15 states, the voice menu, `⋯`, the composer
   morph.
3. **`feat(ios)`: AgentCard and the Agents page.** The data mapping,
   `title` decode, the question card with verbatim options, ⌃C moved
   (5.8), the window `⋯` menu.
4. **`feat(ios)`: conversation.** Conversation rows, the floating question,
   the empty conversation, the permission primers, the ScreenCard sheet,
   the error banners and update notice.
5. **`feat(web)`: daemon fields.** Several pending questions as a list, the
   chosen answer in `last_block`, `title`, and `computer` in `/api/hello`
   and the QR (with the security work); the app shows every question.
6. **`feat(web)`: spaces, kinds and start.** `GET /api/spaces` with
   installed kinds, `POST /api/agents` through `agent.start`, the shared
   start helper.
7. **`feat(ios)`: New agent, the herdr way.** The two steps, the worktree
   switch and hint, the progress and failure states, opening the window.
8. **`feat(ios)`: Settings.** The new structure, Connection without a
   token, the pairing screens (scanner, primer, denied, expired, scan
   again) and the new-device sheet.
9. **`feat(ios)`: iPad.** The sidebar, detail, All agents, the in-app
   split, multiple scenes, the mini capsule and the shortcuts.
10. **`feat(web)` + `feat(ios)`: spaces and wrap-up.** Create, rename and
    close, worktrees list, open and remove, saved commands and run, the
    Spaces screens and their confirmations.
11. **`feat(ios)`: several computers.** `ComputerStore`, per-computer
    sessions, Talk to, sections on the Agents page, the sidebar's computer
    level, pairing another computer, push labels, question ordering, and
    the one-computer snapshot tests.
12. **`feat(ios)`: Live Activity.** Lock screen first: the computer and
    waiting rows, tapping a row to open its window; the system default
    Dynamic Island.
13. **`feat(ios)`: motion pass.** The seven motions in 1.2, with Reduce
    Motion fades.

## 12. Closed questions

The proposal's open questions were decided on 2026-10-21:

1. **Switching by voice.** No. The voice never talks about other
   computers; switching is by tap, ⌘K, a notification or Live Activity
   row, and Siri.
2. **Facts from other computers (option D).** Dropped. Option A only: other
   computers appear as cards, badges and pushes.
3. **Naming computers.** The daemon's `[computer] name`; the app may rename
   locally.
4. **The gear in `⋯`.** Yes, as in the canvas. The header holds `+` and
   `⋯`.
5. **Kinds to offer.** Only installed kinds (herdr's supported kinds on
   PATH); agy must appear.
6. **Saved commands for wrap-up.** Stored on the daemon, per space.
7. **Closing a space.** Refused while any agent is working or blocked;
   otherwise confirm first, naming what will end.
8. **iPad split.** Both the in-app split and multiple scenes are built.
9. **Live Activity without voice.** Not in this plan: the activity runs
   with the voice session, lock screen first. An activity that updates
   while the app is suspended needs the push relay, which is deferred.
