# Conversation timeline: what shows and how

*Proposal, 2026-10-27. Nothing here is built yet. Code references are
`file:line` at commit `44d6ee33` (app 0.2.0). Log figures are from the
author's own daemon (`~/.local/share/sheepdog/events/`), read-only.*

The Conversation page reads cluttered. What you and the voice said, the
questions agents ask, what agents finished, and the voice's own plumbing
(reconnects, audio routes) all land in one list, at much the same weight,
and the "N updates" row folds them together without regard to kind. This
document lists everything that can appear in the timeline today, sorts it
into six categories, and proposes how each kind is shown. The changes are
mostly in the app, as the CEO asked: they decide what to show, what to
group and what to keep out of sight, and they lose nothing (section 6).

It builds on [app-redesign.md](app-redesign.md) (the conversation rows and
the floating question) and on
[ios-state-architecture.md](ios-state-architecture.md) (`ConversationModel`
and the per-computer conversation). How the timeline is assembled today is
in [ios/docs/dev/architecture.md](../../../ios/docs/dev/architecture.md);
the history API is in [ws-contract.md](../ws-contract.md) "History and
resume".

## Contents

1. [How the timeline is built today](#1-how-the-timeline-is-built-today)
2. [Inventory: every kind of row](#2-inventory-every-kind-of-row)
3. [How often each kind occurs](#3-how-often-each-kind-occurs)
4. [Categories](#4-categories)
5. [Handling per kind](#5-handling-per-kind)
6. [Nothing is lost: where each kind can still be found](#6-nothing-is-lost-where-each-kind-can-still-be-found)
7. [Before and after](#7-before-and-after)
8. [Implementation sketch (later)](#8-implementation-sketch-later)
9. [Usability review](#9-usability-review)
10. [Open questions for the CEO](#10-open-questions-for-the-ceo)

## 1. How the timeline is built today

Three sources feed one list:

- **History**: `ConversationItem`s, from `GET /api/conversation` (rebuilt
  by the daemon from its event log, `internal/web/conversation.go:247-353`)
  and folded live from `/events` by the same rules
  (`ConversationHistory.fold`, `ios/Sources/SheepdogKit/ConversationHistory.swift:297-361`).
  Kinds: `shepherd`, `dog`, `push`, `approval`, `card`, `notice`, plus
  `unknown`.
- **Activity**: orchestrator replies and held pushes, folded from `/events`
  only (`BrainActivity.fold`, `BrainActivity.swift:123-175`).
- **Live**: this session's id-less bubbles from `/ws` transcripts, and the
  app's own **local notices** (`ConversationReducer.apply(.notice)`,
  `ConversationReducer.swift:81-82`), plus cards delivered this session
  (`cardInbox.live`).

`ConversationTimeline.rows` merges them by time and adds day dividers
(`ConversationTimeline.swift:53-121`). `ConversationTimeline.grouped` then
folds every run of two or more "updates" into one **"N updates"** row
(`ConversationTimeline.swift:211-235`). An update is (`isUpdate`,
`:238-257`):

- any `notice` divider that is not a boundary (boundaries: "Sheepdog
  started" and "New conversation", `isBoundary`, `:262-266`);
- any local notice bubble (`.live` of kind `.notice`);
- any push that does not need the user (`urgency` other than `interrupt`).

Speech, approvals, orchestrator replies, held pushes, cards and day
dividers break a run. So today an "8 updates" row can hold a voice
reconnect, an agent's "finished", "Audio moved to Speaker." and "Talking
to Work Mac" side by side, while a held push between two of them splits
the run in two.

`QuestionFocus.pointerRowIDs` (`QuestionFocus.swift:11-87`) folds the
copies of an *open, pinned* question (the approval item, the push that
announced it, the voice's restatement) into one-line pointers. It returns
nothing once the question settles (`:12`), so a cancelled question comes
back at full size beside the voice's bubble that restated it.

Notices reach the app as text only: the daemon's `notice` items carry no
reason, so the app recognises "Sheepdog started." and the rest by string
(`isBoundary`, `dividerTitle`, `:149-157`).

## 2. Inventory: every kind of row

40 kinds, from code. "Group" says whether the kind folds into "N
updates" today.

### 2.1 Dialogue (history and live)

| # | Kind | Text | Produced | Trigger | Renders today | Group |
|---|---|---|---|---|---|---|
| 1 | You, by voice | your words | daemon `conversation.go:278-287` (`voice.transcript.in` merged per turn); live `/ws` transcript, `SessionViewModel.swift:2081-2085` | you speak | right bubble (`SpeechBubble`, `ConversationView.swift:586`); "…" while open | no |
| 2 | You, typed | your words | `conversation.go:288-296` (`voice.text`, `web.utterance`); `ConversationHistory.swift:325-329` | composer or the web page | right bubble | no |
| 3 | The voice | its words | `conversation.go:297-305` (`voice.transcript.out` merged); live `/ws` | the voice speaks, including a push it reads aloud | left bubble | no |
| 4 | Text-router answer | the orchestrator's `spoken` | `conversation.go:310-315`; `ConversationHistory.swift:330-333` | typed line on a daemon without a voice | left bubble | no |
| 5 | Orchestrator reply | "Orchestrator → Sheepdog", headline, options, Detail, raw JSON | `BrainActivity.swift:133-155` (`voice.tool_result` of `to_supervisor`) | every relay to the orchestrator | purple-tag card (`BrainReplyCard`, `ConversationView.swift:318`) | no |
| 6 | Orchestrator reply, compact | options and Detail under the voice's bubble | `ConversationTimeline.swift:131-136` | kind 5 that is also a kind-4 bubble | footer under the bubble | no |
| 7 | Orchestrator reply, pending | "Still working on it — the answer follows as a push." | `ConversationView.swift:336-340` | a reply that did not land in time | card, grey text | no |

### 2.2 Questions (approvals)

| # | Kind | Text | Produced | Trigger | Renders today | Group |
|---|---|---|---|---|---|---|
| 8 | Question, open | "Codex asked · 12:46" + the summary ("Codex is asking allow Computer Use to use Finder.", `internal/herdr/prompt.go:246`) + "Waiting for your answer — below" or "Not answered here" | `conversation.go:327-340`; `ConversationHistory.swift:337-345`; daemon `internal/supervisor/pending.go:116` | an agent shows a prompt | `ApprovalItemCard` (`ConversationView.swift:514`), amber border while pinned; a pointer row while it floats | no |
| 9 | Question, answered | "You approved — Yes · by tap/by voice" / "You declined — …" | `ConversationHistory.swift:143-165`; `pending.confirmed` (`supervisor/tools.go:428`) | you answer | same card, green check or red x | no |
| 10 | Question, expired | "Expired without an answer" | `pending.expired` (`supervisor/pending.go:198`) | `pending_ttl_s` passed | same card, grey clock | no |
| 11 | Question, cancelled | "No longer needed" | `pending.cancelled` (`supervisor/reprompt.go:216`, `supervisor/inputs.go:558`, reason `prompt_gone`) | the prompt left the agent's screen (answered in the terminal, or the agent moved on) | same card, grey clock | no |
| 12 | Question, replaced | "Replaced by a newer question" | `pending.replaced` (`supervisor/pending.go:88`) | the agent asked again | same card, grey clock | no |
| 13 | Interrupt push | e.g. a question the voice must say now; also the Stop reply "Nothing was in flight." (`supervisor/stop.go:298`) | `conversation.go:316-322`; `ConversationHistory.swift:334-336` (`urgency: interrupt`) | a push sent as interrupt | `SystemCard` with a bell; pointer while its question floats | no |

The pinned question itself floats above the voice capsule
(`FloatingQuestions.swift`), not in the list; the list keeps the record.

### 2.3 Agent outcomes

| # | Kind | Text | Produced | Trigger | Renders today | Group |
|---|---|---|---|---|---|---|
| 14 | Push (news) | "slingshot stage Codex finished." (`supervisor/transitions.go:259,272`) or the orchestrator's sentence | `conversation.go:316-322` (`voice.push`, not silent) | an agent finished, found something, needs a look; non-Gemini voices, `--voice=off` | `SystemCard` with a bell (`ConversationView.swift:630`) | **yes** |
| 15 | Held push | "Waiting for a listener — said when you connect" + the text | `BrainActivity.swift:157-164` (`voice.push_held`, `gemini/push.go:219`, `gptlive/loop.go:212`) | a spoken push with no app attached | dashed amber card (`HeldPushCard`, `ConversationView.swift:418`) | no (breaks a run) |
| 16 | Released push | (hidden) | `voice.push_released` (`gemini/push.go:446`) | an app attached; the push is then said | not shown: the voice's bubble says it (`BrainActivity.swift:100-103`) | — |
| 17 | Dropped push | "Dropped — nobody was listening for 5 minutes" + the text **struck through** | `voice.push_dropped` (`gemini/push.go:216`, `gptlive/loop.go:207`) | 5 minutes with no listener | dashed grey card, strikethrough (`ConversationView.swift:428-435`) | no (breaks a run) |
| 18 | Card | title, "Card · agent · 9:52", chevron | `card.shown` (`supervisor/cards.go:160`); `conversation.go:323-326`; live `cardInbox.live` | the orchestrator's `show` tool | `CardRow` (`CardViews.swift:71`), opens the sheet | no |
| 19 | Card, gone | "A card was shown on the screen; it is no longer stored." | `conversation.go:356` | the card store dropped it | grey `CardRow`, no chevron | no |

### 2.4 Your own context

| # | Kind | Text | Produced | Trigger | Renders today | Group |
|---|---|---|---|---|---|---|
| 20 | Day | "Today", "Yesterday", "Monday 26 October" | `ConversationTimeline.swift:108-119` | the day changes | centred caption (`DayDivider`) | no |
| 21 | New conversation | "New conversation · 9:12" | `conversation.go:256-258` (`conversation.reset`) | ⋯ ▸ New conversation, the web page or by voice | divider (boundary) | no |
| 22 | Talking to | "Talking to M944QD4VY" | app only, `SessionViewModel.swift:1935` | you switch the current computer | local notice → `SystemCard` with an info icon | **yes** |

"M944QD4VY" is the computer's `displayName`
(`DeviceCredentials.swift:47-52`): the name you gave it, else the
daemon's, which is the Mac's macOS ComputerName
(`internal/identity/identity.go:71-77`), here an asset tag.

### 2.5 System and plumbing

| # | Kind | Text | Produced | Trigger | Renders today | Group |
|---|---|---|---|---|---|---|
| 23 | Daemon (re)start | "Sheepdog started · t" / "Sheepdog restarted · t" | `conversation.go:253-255`; `ConversationTimeline.swift:149-157` | `daemon.start` (`internal/app/run.go:25`): launch, auto-update | divider (boundary) | no |
| 24 | Voice closed | "Voice session closed: shutdown." | `conversation.go:269-275` (`gemini/session.go:284`, `gptlive/session.go:238`) | daemon stopping; always just before kind 23 | divider | yes |
| 25 | Context lost | "Voice context lost · 12:46" | `conversation.go:266-268` (`gemini/run.go:25,89`) | the Gemini handle is dropped: the 54-minute refresh (`gemini/refresh.go:221`), a server 1011, session expiry | divider | yes |
| 26 | Fresh reconnect | "Voice reconnected with a fresh session; earlier context was lost." | `conversation.go:259-265` (`gemini/run.go:174`, `resumed: false`) | right after kind 25, 1–2 s later | divider | yes |
| 27 | Resumed reconnect | "Voice reconnected; the conversation continues." | same, `resumed: true` | a GoAway or drop that resumed | divider | yes |
| 28 | Unknown kind | its text, or "Notice" | `ConversationItem.Kind.unknown` | a newer daemon | divider | yes |
| 29 | Audio route | "Audio moved to Speaker." / "The audio route changed." | app, `AudioEngine.swift:635` | headphones in or out, AirPods, CarPlay, a call | local notice card | **yes** |
| 30 | Interruption | "Paused for a call or Siri." | `AudioEngine.swift:606` | iOS interruption began | local notice | yes |
| 31 | Resumed audio | "Listening again." | `AudioEngine.swift:644` | interruption ended | local notice | yes |
| 32 | Audio not given back | "iOS kept the audio for something else. Tap the orb to resume." | `AudioEngine.swift:615` | interruption ended without `shouldResume` | local notice | yes |
| 33 | Audio failed to resume | "Could not pick the audio back up (…). Tap the orb to try again." | `AudioEngine.swift:647` | reactivation threw | local notice | yes |
| 34 | Media services reset | "Audio was reset by iOS — reconnect to start listening again." | `AudioEngine.swift:590` | `mediaServicesWereReset` | local notice | yes |
| 35 | Audio stopped | "The microphone went away with the route change." / "Audio stopped after the route change (…)." | `AudioEngine.swift:693,711` → `SessionCore.swift:368-375` | the engine could not rebuild | local notice | yes |
| 36 | Voice off / closed | "Voice is off on the daemon (`--voice=off`)…" / "The daemon's voice session closed (…)." | `SessionCore.swift:557-571` | `/ws` status closed (not "replaced", which is a banner) | local notice | yes |

### 2.6 Rows the screen adds

| # | Kind | Text | Where | Renders today |
|---|---|---|---|---|
| 37 | Updates group | "8 updates" + the newest one's words | `ConversationTimeline.swift:211-235`, `UpdatesGroup` (`ConversationView.swift:248`) | bell row, opens in place |
| 38 | Question pointer | "Codex asked — answer below" or the folded line | `QuestionFocus.swift`, `QuestionPointer` (`ConversationView.swift:665`) | one muted line with an amber arrow |
| 39 | Load older | "Earlier conversation" | `OlderButton`, `ConversationView.swift:184` | button at the top |
| 40 | Empty | "Just talk" and two example chips | `EmptyConversation`, `ConversationView.swift:696` | mascot, listening |

**Not in the timeline** (for completeness): banners (`NoticeModel`,
`Banners.swift`), the "Card: …" corner label (`CardLabel.swift`), the
floating question, the voice bar status line, push notifications and the
Live Activity. The demo (`DemoWorld.swift`) produces kinds 1–3, 8–9, 14,
18 and 23 through the same reducers.

Two oddities found on the way:

- `ConversationReducer.Bubble.Kind.push` (`ConversationReducer.swift:26`)
  has no live producer any more; pushes arrive only as history items.
- A held push whose daemon restarts before the 5 minutes are up never gets
  a fate: on 2026-10-27 two pushes held at 19:49 and 19:50 UTC were
  followed by a restart at 19:51 and stay "Waiting for a listener — said
  when you connect" forever.

## 3. How often each kind occurs

From the event log, counted by event type (one row each unless noted).

| Event (kind) | 10-25 | 10-26 | 10-27 |
|---|---|---|---|
| `voice.handle_dropped` (25) | 5 | 27 | 27 |
| `voice.resume` fresh / resumed (26 / 27) | 5 / 0 | 30 / 1 | 34 / 2 |
| `voice.session_refreshed` (the 54-minute refresh) | 5 | 22 | 14 |
| `voice.push_held` (15) | 0 | 15 | 40 |
| `voice.push_dropped`, `no_listener` (17) | 0 | 15 | 37 |
| `voice.push` not silent (14, 13) | 0 | 0 | 2 |
| `voice.closed` + `daemon.start` (24 + 23) | 3 + 1 | 0 + 0 | 3 + 3 |
| `pending.created` answer prompts (8) | 0 | 0 | 4 |
| `card.shown` (18) | 0 | 0 | 2 |
| `voice.transcript.in` fragments (1) | — | 0 | 23 |

What this says:

- **Reconnect rows are the bulk of the timeline.** About 60 notice rows a
  day (kinds 25–27), roughly 2.5 an hour around the clock. Of the 34 fresh
  reconnects on 10-27, 26 came within 10 s after a "context lost" for the
  same cause, so they read as pairs. The reason was `session_refresh`
  15 times, `server_internal_error` 8, `session_expired` 3, `app` once.
- **They almost never touch a conversation.** An app was attached for 2
  of the 34 fresh reconnects, and you had spoken in the 10 minutes before
  only once. The refresh waits for a quiet moment by design
  (`gemini/refresh.go`, [timelines.md](../timelines.md) §1.4).
- **"Earlier context was lost" overstates it.** A fresh session is
  re-seeded with the recent conversation (`voice.reseeded`, 17 of 34 on
  10-27; the others had nothing recent to carry), and the orchestrator,
  which holds the real state, is untouched.
- **Dropped pushes are agent results you missed, not noise.** 37 on
  10-27. They are the orchestrator's summaries of finished work and of
  decisions waiting for you ("…has four things for you to decide…"),
  dropped only because no app was attached for 5 minutes. Today they are
  struck through, as if withdrawn.
- **Push cards are rare** on this daemon (2 on 10-27, against 66
  `silent` pushes). A silent push is context handed to the voice, which
  the conversation does not show (`conversation.go:318`); what the voice
  then says shows as its bubble. Push cards matter more for GPT-Live and
  `--voice=off`.
- **Local notices** (kinds 22, 29–36) are not logged by the daemon; they
  occur per phone, a few times a session (every headphone change, every
  call).

## 4. Categories

Six categories. Each has one rule: what you need from it, whether it
belongs in the timeline, and its visual weight.

| Category | Kinds | What you need | In the timeline? | Weight |
|---|---|---|---|---|
| **A. Dialogue** — you and the voice | 1–7 | to follow and scroll back the conversation | always; never folded | full: bubbles; the orchestrator reply as a light attachment |
| **B. Needs you** — open questions | 8 (open), 13 while its question is open, 15 | to act, now | always; never folded; the floating card is the place to answer | the one accent (amber); one copy only |
| **C. Agent outcomes** — what agents did | 9–12 (settled), 14, 17, 18, 19 | to know what finished, what was decided, what you missed | yes; consecutive outcomes may fold, but only with each other | medium: a quiet card with the agent's name |
| **D. Your context** — where you are | 20, 21, 22, 23 | to know when the conversation starts over or changes computer | yes, as thin dividers | low: a centred caption |
| **E. System** — plumbing | 16, 24–36 | nothing, unless it changed what the voice heard or can do | no; a transient line where it needs action; full detail behind "Show system events" | none in the timeline |
| **F. Screen helpers** | 37–40 | navigation | as needed | — |

Rules that follow from the table:

1. **A row is in the timeline only if it is something said, something to
   act on, or something an agent did.** Plumbing is not.
2. **Group only within a category.** "N updates" becomes "N agent
   updates" and holds category C only.
3. **One question, one card.** A question is shown once whether it is
   open or settled; its other copies fold into it.
4. **Something you missed is an outcome, not a deletion.** No
   strikethrough for content that was true.
5. **Plumbing that needs your hand goes where your hand is**: the voice
   bar or a banner, not the scrollback.
6. **Nothing is deleted.** Hidden rows stay in memory and on the daemon,
   and "Show system events" brings every one back with its original text
   and time.

## 5. Handling per kind

"Debug" means: shown only with **Settings ▸ Show system events** on
(section 6).

### 5.1 Dialogue (A)

| # | Kind | Handling |
|---|---|---|
| 1–4 | Speech | Unchanged. |
| 5 | Orchestrator reply | Keep, but lighter: today it is a separate purple card under almost every exchange. Show it as a one-line "Details" disclosure attached to the voice's bubble it belongs to when one is adjacent (as kind 6 already does), and as a card only when the voice did not say it (pending, or never voiced). |
| 6 | Compact reply | Unchanged. |
| 7 | Pending reply | Keep the line, muted; when the answer arrives (a push or the voice's bubble), collapse the pending line into it. |

### 5.2 Needs you (B)

| # | Kind | Handling |
|---|---|---|
| 8 | Open question | Unchanged: the floating card answers, the list shows a pointer. |
| 13 | Interrupt push | While its question is open: pointer (today). Otherwise it is news: treat as kind 14. The Stop reply "Nothing was in flight." is a reply to you: show it as the voice's line (dialogue), not a bell card. |
| 15 | Held push | One quiet line, "Will tell you when you're back" (hourglass), not an amber dashed card: it is not a question. If the daemon restarts before a fate arrives, show it as kind 17 once the app learns of the restart (the push is gone). |

### 5.3 Agent outcomes (C)

| # | Kind | Handling |
|---|---|---|
| 9 | Answered | One line: "You approved Codex — Yes · by tap · 12:47". The full question opens on tap. |
| 10–12 | Expired / cancelled / replaced | One muted line with the agent and the question's few words: "Codex's question (Finder access) — no longer needed". **Settled-question dedupe:** the push and the voice bubble that restated the question within 2 minutes before or after it fold into this line ("Said aloud · show"), the same rule as `QuestionFocus` but kept after the question settles. |
| 14 | Push (news) | Quiet card: agent name first, then the line. Folds with neighbouring outcomes. |
| 17 | Dropped push | **"Missed while you were away"** (bell-badge icon, not bell-slash), the text in full and **not** struck through, with the time. A run of them folds into "5 things you missed" with the newest line. Optional "Ask about this" chip that types "What happened with <agent>?" (open question 3). |
| 18–19 | Cards | Unchanged. A gone card (19) shows only in a group, never alone at full size. |

**Grouping rule.** Fold a run of two or more consecutive category-C rows
(pushes, dropped pushes, settled-question lines) into one row: "3 agent
updates", or "5 things you missed" when every member is a missed push,
with the newest line and the agent names ("slingshot, forumind"). A
dialogue row, a question, a day or a boundary divider ends the run.
Hidden system rows do **not** end a run, so a reconnect no longer splits
two outcomes. Cards and an open question never fold.

### 5.4 Your context (D)

| # | Kind | Handling |
|---|---|---|
| 20 | Day | Unchanged. |
| 21 | New conversation | Unchanged divider. |
| 22 | Talking to | Divider, not a card (the redesign already says "divider", [app-redesign.md](app-redesign.md) §3), with the name you gave the computer. When that name is the daemon's ComputerName and looks machine-made, the pairing screen offers to rename it (open question 4). Not grouped. |
| 23 | Sheepdog restarted | Keep one divider, merged with the "Voice session closed: shutdown." just before it. Say what you need to know: "Sheepdog restarted · 12:51" — and, when the restart was an update, "Sheepdog updated to v0.1.9 · 12:51" (needs `daemon.start`'s version, section 8). |

### 5.5 System (E)

| # | Kind | Normal view | When it *is* worth showing |
|---|---|---|---|
| 24 | Voice closed: shutdown | hidden (merged into 23) | never on its own |
| 25 + 26 | Context lost + fresh reconnect | hidden; the pair is one event | **one** divider, "Voice restarted — it may not remember the last minute", only when you were mid-conversation: a dialogue row in the 2 minutes before the reconnect *and* an app attached. The voice bar shows "Reconnecting…" while it happens, as today |
| 27 | Resumed reconnect | hidden | never: nothing was lost |
| 28 | Unknown | debug only | — |
| 29 | Audio route | the voice bar status line shows "Now on Speaker" for 3 s; VoiceOver announces it | never in the list |
| 30–31 | Interruption, listening again | the voice bar's paused state (it exists, `onPausedChanged`) | never in the list |
| 32–35 | Audio needs your tap | a banner with the action ("Tap to resume listening"), cleared when audio runs again | never in the list |
| 36 | Voice off / closed | a banner (a state, not an event); "replaced" already is one | never in the list |
| 16 | Released push | debug only | — |

The 2-minute rule is from the log: on 10-27 one fresh reconnect in 34 met
"attached and you spoke in the last 10 minutes"; the refresh waits for a
quiet moment, so the rule shows the rare mid-sentence failure and nothing
else.

## 6. Nothing is lost: where each kind can still be found

The CEO's constraint: everything hidden from the normal view stays
reachable; nothing is deleted at the source; questions and outcomes are
never hidden.

**Show system events** (Settings ▸ Conversation section, off by default,
per phone): the timeline shows every row of every kind, ungrouped, with
the original text and its exact time (seconds), system rows as small
monospaced lines in a muted colour so they are still easy to tell apart.
A long press on any row copies its raw item or event as JSON. Turning it
off returns to the normal view; nothing is refetched.

| Kinds | Hidden or changed in the normal view | Still found |
|---|---|---|
| 1–12, 14, 17, 18, 21 | never hidden; 9–12 shortened, 17 reworded | full text on tap; the switch; the daemon's `GET /api/conversation`; `/debug` |
| 5 | folded into "Details" | tap; the switch; `/debug` |
| 13 (restating), and the voice's restatement of a settled question | folded into the question's line | "Said aloud · show"; the switch; `GET /api/conversation` |
| 15, 17 | reworded, grouped | the group opens; Settings ▸ Health ▸ Recent problems (already lists held and dropped pushes, `DaemonHealth.swift:162-169`); the switch; `/events` |
| 16 | hidden | the switch; Health; `/events` |
| 23, 24 | merged | the switch shows both; `GET /api/conversation`; `/debug` |
| 25–28 | hidden or one divider | the switch; `GET /api/conversation` (the daemon still returns them); Health ▸ Recent problems for `voice.connect_error`; `/debug`; `events/*.jsonl` |
| 29–36 (local notices) | voice bar, banner | the switch reads them from the reducer, where they are kept as today (`ConversationReducer`, 400 bubbles, memory only); new: also written to the unified log (`SheepdogLog.audio`), so a sysdiagnose has them after a relaunch |
| 22 | divider | unchanged |

Local notices are the one place the app is the only record: they were
never on the daemon and were already lost on relaunch (`saveCache` writes
history only, `SessionViewModel.swift:1377-1398`). Writing them to the
unified log closes that gap; they are not added to the daemon.

Questions (kind 8) and their outcomes (9–12) are never hidden, in either
view. The approval rail is untouched: this is display only.

## 7. Before and after

### 7.1 The CEO's screenshots

Before (one screen, iPhone, about 12:46–12:52):

```
            ── Voice context lost · 12:46 ──
  ┌──────────────────────────────────────────────┐
  │ 🔔 8 updates                               ⌄ │
  │    Audio moved to Speaker.                   │
  └──────────────────────────────────────────────┘
     (opened: Voice reconnected with a fresh session;
      earlier context was lost · Voice reconnected;
      the conversation continues · Talking to M944QD4VY
      · slingshot stage Codex finished. · Audio moved
      to Speaker. · …)
  ┌ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ┐
    🔕 Dropped — nobody was listening for 5 minutes
    ~~forumind finished the design doc and is~~
    ~~waiting for your OK to start.~~
  └ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ┘
  ┌──────────────────────────────────────────────┐
  │ Codex asked · 12:49                          │
  │ Codex is asking allow Computer Use to use    │
  │ Finder.                                      │
  │ ⏱ No longer needed                           │
  └──────────────────────────────────────────────┘
  ┌────────────────────────────────────┐
  │ Codex is asking to allow Computer  │
  │ Use to use Finder. Allow it?       │
  └────────────────────────────────────┘
```

After:

```
          ── Talking to Work Mac · 12:47 ──
  ┌──────────────────────────────────────────────┐
  │ 🔔 2 agent updates                         ⌄ │
  │    slingshot: stage Codex finished.          │
  └──────────────────────────────────────────────┘
  ┌──────────────────────────────────────────────┐
  │ 🔔 Missed while you were away · 12:44        │
  │ forumind finished the design doc and is      │
  │ waiting for your OK to start.                │
  └──────────────────────────────────────────────┘
    ⏱ Codex's question (Finder access) — no longer
      needed · 12:49 · Said aloud ›
```

Gone from view: "Voice context lost", both "Voice reconnected" lines,
"Audio moved to Speaker." (shown for 3 s in the voice bar instead), and
the voice's restatement of the settled question (one tap away). The
"Talking to" divider uses the computer's own name. Two agent outcomes are
easy to see at a glance; the missed one reads as news.

### 7.2 A real hour from the log

2026-10-27, 19:00–20:05 UTC (12:00–13:05 local), no app attached most of
the hour, so no dialogue. Before (what the app shows on opening it):

```
            ── Voice context lost · 12:00 ──
  ── Voice reconnected with a fresh session; earlier context was lost · 12:01 ──
   [dropped, struck]  The sheepdog agent has finished. The slash command …
   [dropped, struck]  Both releases are underway. The daemon fix is wait…
   [dropped, struck]  The daemon fix built and is now waiting on you to …
   [dropped, struck]  Claude finished the forumind work and has publishe…
   [waiting forever]  It checked the app's icon files and there is nothi…
   [waiting forever]  The WordPress version of forumind is on TestFlight…
            ── Voice session closed: shutdown · 12:51 ──
            ── Sheepdog restarted · 12:51 ──
   [dropped, struck]  The local daemon is now the signed v0.1.9 …
   [dropped, struck]  The two stuck waiting scripts are cleared …
```

Ten rows: two notices, six struck-through cards, two cards that wait for
a listener who never comes, two more dividers. (The real lines are in
Chinese and English; shortened here.)

After:

```
  ┌──────────────────────────────────────────────┐
  │ 🔔 6 things you missed                      ⌄ │
  │    Claude finished the forumind work and has │
  │    published …                               │
  └──────────────────────────────────────────────┘
          ── Sheepdog updated to v0.1.9 · 12:51 ──
  ┌──────────────────────────────────────────────┐
  │ 🔔 2 things you missed                      ⌄ │
  │    The two stuck waiting scripts are cleared…│
  └──────────────────────────────────────────────┘
```

Three rows. The two pushes orphaned by the restart join the first group
as missed. The reconnect pair and the shutdown notice are behind the
switch.

## 8. Implementation sketch (later)

**Which layer decides.** The app (CEO priority: UI side). Phase 1 needs
no daemon change: the app already knows each row's kind, and the notice
texts are fixed strings. A small daemon change in phase 3 makes it robust:
the classification stays in the app.

**Data model (app).**

- `TimelineRow.category: TimelineCategory` (`dialogue`, `needsYou`,
  `outcome`, `context`, `system`), a pure function of the row in
  `SheepdogKit`, next to `isUpdate`, which it replaces.
- `ConversationTimeline.visibleRows(_:showSystem:)`: drops `system` rows
  (except the mid-conversation reconnect), merges shutdown + start, pairs
  context lost + reconnect (within 10 s), and applies the settled-question
  dedupe. Pure; the view gets `[ConversationDisplayRow]` as today.
- `grouped` folds only `outcome` runs; a new `.updates(kind:)` carries
  "agent updates" or "things you missed".
- `QuestionFocus` gains a settled mode (the pointer rule, kept after the
  question settles, 2-minute window).
- A `SettingsModel.showSystemEvents` flag (UserDefaults, per phone).
- Local notices: route kinds 29–36 to the voice bar / banner and also to
  `SheepdogLog`; keep appending them to the reducer so the switch shows
  them.

**Daemon (phase 3, small, additive).**

- `ConversationItem.Subkind` for every notice (the field exists,
  `conversation.go:48`): `start`, `closed`, `context_lost`, `resumed`,
  `fresh`, plus a `reason` (`session_refresh`, `server_internal_error`,
  …), so the app stops matching strings.
- An approval's cancel reason (`prompt_gone`, …) on the item: `resolve`
  drops it today (`conversation.go:376-381`), and "No longer needed" could
  then say "Answered on the computer".
- `daemon.start` carries the version, for "Sheepdog updated to v…".
- A held push without a fate at `daemon.start` gets a `voice.push_dropped
  {reason: restart}` so nothing waits forever.

**Phases.** One PR each, merged on green.

1. `feat(ios)`: categories, hide system rows, group by category, the
   "Show system events" switch. Pure-function tests first.
2. `feat(ios)`: missed pushes as outcomes, settled-question dedupe,
   Talking-to divider, audio notices to the voice bar. Screenshots light
   and dark.
3. `feat(web)` + `feat(ios)`: notice `subkind`/`reason`, cancel reason,
   start version, restart drops held pushes. The web `/debug` view is
   unchanged.

**Tests.**

- `SheepdogKitTests`: a table test of `category` for every kind in
  section 2; `visibleRows` on the two before/after fixtures of section 7
  (built from the log's event sequence, anonymised); grouping never mixes
  categories; with the switch on, every input row is in the output
  (count equality: the no-loss test); an open question is never dropped
  or folded.
- `TimelineGroupingTests` and `ConversationReducerTests` extended, not
  replaced.
- One UI test in the demo (`-sheepdogDemoSilent`, `-sheepdogNoAnimations`):
  toggling the switch shows a seeded reconnect row and hides it again.
- Go: `conversation_test.go` for the new `subkind`/`reason` fields.

**Risks.**

- Hiding a reconnect that did matter. Mitigation: the 2-minute rule, the
  voice bar's "Reconnecting…", and the switch.
- String matching on notice text until phase 3 (it already exists today).
- A view that differs between the switch on and off can confuse a bug
  report. Mitigation: the switch name says what it does, and the Feedback
  mark already captures the time.
- Safety rails: none move. The approval flow, the floating question and
  `/api/pending/answer` are untouched; this is display only.

## 9. Usability review

A draft of sections 4, 5 and 7 was given to a second reviewer (Fable)
for a usability opinion. Its points and what this document does with
them are below.

*(Filled in after the review; see the PR.)*

## 10. Open questions for the CEO

1. **Reconnect row: when?** Recommendation: only when an app was attached
   and you or the voice spoke in the 2 minutes before; otherwise hidden.
   Alternative: never in the timeline, voice bar only.
2. **Where does the switch live?** Recommendation: Settings ▸
   Conversation, "Show system events", off by default, with the footer
   "Adds reconnects, audio changes and other plumbing to the
   conversation, with exact times. For bug reports." Alternative: inside
   Health.
3. **"Ask about this" on a missed push?** Recommendation: yes, one chip
   on the group ("Catch me up") that asks the orchestrator for a summary
   of what you missed, since the voice never said them. Alternative: no
   chip; the group is read-only.
4. **Computer names like "M944QD4VY".** Recommendation: on pairing, if the
   ComputerName has no space and mixes letters and digits, suggest
   "Work Mac" and let you edit it; the divider uses your name.
   Alternative: always ask for a name on pairing.
5. **Settled questions: one line or the full card?** Recommendation: one
   line for expired, cancelled and replaced; the full card only for
   answered ones on tap. Alternative: keep today's full card for all.
6. **Orchestrator reply cards.** Recommendation: fold into the voice's
   bubble as "Details" when adjacent (section 5.1). Alternative: keep the
   separate purple card.
7. **Restart divider.** Recommendation: keep "Sheepdog restarted", and say
   "updated to vX" when it was an update (needs a daemon field).
   Alternative: hide restarts too, like reconnects.
