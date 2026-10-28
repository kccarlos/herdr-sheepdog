# Conversation timeline: what shows and how

*Proposal, 2026-10-27, approved by the CEO the same day (section 10).
Phases 1, 2 and 3 are built (sections 11 and 13). Code references are
`file:line` at commit `44d6ee33` (app 0.2.0), before phase 1. Log figures are from the
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
10. [Decisions by the CEO](#10-decisions-by-the-ceo)
11. [Phase 2: what was built, and where it differs](#11-phase-2-what-was-built-and-where-it-differs)
12. [The visual system](#12-the-visual-system)
13. [Phase 3: what was built, and where it differs](#13-phase-3-what-was-built-and-where-it-differs)
14. [When a question goes compact](#14-when-a-question-goes-compact)

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
| **B. Needs you** — open questions | 8 (open), 13 while its question is open | to act, now | always; never folded; the floating card is the place to answer | the one accent (amber); one copy only |
| **C. Agent outcomes** — what agents did | 9–12 (settled), 14, 15, 17, 18, 19 | to know what finished, what was decided, what you missed | yes; consecutive outcomes may fold, but only with each other | medium: a quiet card with the agent's name; its own icon (an agent glyph, not the bell) |
| **D. Your context** — where you are | 20, 21, 22, 23 | to know when the conversation starts over or changes computer | yes, as thin dividers | low: a centred caption |
| **E. System** — plumbing | 16, 24–36 | nothing, unless it changed what the voice heard or can do | no; a transient line where it needs action; full detail behind "Show technical events" | none in the timeline |
| **F. Screen helpers** | 37–40 | navigation | as needed | — |

Rules that follow from the table:

1. **A row is in the timeline only if it is something said, something to
   act on, or something an agent did.** Plumbing is not.
2. **Group only within a category.** "N updates" becomes "N agent
   updates" and holds category C only. One icon per category, so a
   glance tells an outcome from a missed one from a question; the bell is
   retired.
3. **One question, one card.** A question is shown once whether it is
   open or settled; its other copies fold into it.
4. **Something you missed is an outcome, not a deletion.** No
   strikethrough for content that was true.
5. **Plumbing that needs your hand goes where your hand is**: the voice
   bar or a banner, not the scrollback.
6. **Nothing is deleted.** Hidden rows stay in memory and on the daemon,
   and "Show technical events" brings every one back with its original text
   and time.

## 5. Handling per kind

"Debug" means: shown only with **Settings ▸ Health ▸ Show technical events** on
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

### 5.3 Agent outcomes (C)

| # | Kind | Handling |
|---|---|---|
| 15 | Held push | An outcome in transit, not a question: one quiet line, "Will tell you when you're back" (hourglass), not an amber dashed card. If the daemon restarts before a fate arrives, show it as kind 17 once the app learns of the restart (the push is gone). |
| 9 | Answered | One line: "You approved Codex — Yes · by tap · 12:47". The full question opens on tap. |
| 10–12 | Expired / cancelled / replaced | One muted line with the agent and the question's few words: "Codex's question (Finder access) — closed · 12:49". The whole line is one tap target that opens the full card: the question, how it ended, and what the voice said about it. "Closed" rather than "No longer needed" until the daemon sends the cancel reason (section 8); then "Answered on the computer" or "Codex moved on". **Settled-question dedupe:** the push that announced the question folds into this line; the voice's bubble that restated it folds too, but **only if you did not reply to it** (a voice line you answered is dialogue and stays). Same matching as `QuestionFocus`, kept after the question settles, within 2 minutes. |
| 14 | Push (news) | Quiet card: agent name first, then the line. Folds with neighbouring outcomes. |
| 17 | Dropped push | An update you missed, **not** struck through: a one-liner, "While you were away · forumind finished the design doc…", the full text on tap. A run folds into "6 updates while you were away" (below). One "Catch me up" chip on such a group asks the orchestrator for a summary, since the voice never said them (open question 3). |
| 18–19 | Cards | Unchanged. A gone card (19) shows only in a group, never alone at full size. |

**Grouping rule.** Fold a run of **three** or more consecutive
category-C rows (pushes, held and missed pushes, settled-question lines)
into one row; two one-liners read faster than a row and a tap. The
collapsed row shows the newest **two** lines and its label says what is
in it: "3 agent updates", "6 updates while you were away" when every
member was missed, "4 agent updates · 2 while you were away" when mixed.
A dialogue row, a question or a day divider ends the run. A restart
divider (23) does not: it shows inside the opened group, so a night away
reads as one "8 updates while you were away". Hidden system rows do not
end a run either, so a reconnect no longer splits two outcomes. Cards
and an open question never fold.

### 5.4 Your context (D)

| # | Kind | Handling |
|---|---|---|
| 20 | Day | Unchanged. |
| 21 | New conversation | Unchanged divider. |
| 22 | Talking to | Divider, not a card (the redesign already says "divider", [app-redesign.md](app-redesign.md) §3), with the name you gave the computer. When that name is the daemon's ComputerName and looks machine-made, the pairing screen offers to rename it (open question 4). Not grouped. |
| 23 | Sheepdog restarted | Keep one divider, merged with the "Voice session closed: shutdown." just before it: "Sheepdog restarted · 12:51", or "Sheepdog updated · 12:51" when it was an update, with the version on tap (needs `daemon.start`'s version, section 8). Inside a run of outcomes it moves into the group (above). |

### 5.5 System (E)

| # | Kind | Normal view | When it *is* worth showing |
|---|---|---|---|
| 24 | Voice closed: shutdown | hidden (merged into 23) | never on its own |
| 25 + 26 | Context lost + fresh reconnect | hidden; the pair is one event | **one** divider, "Voice restarted — it may not remember the last minute", only when you were mid-conversation: a dialogue row in the 5 minutes before the reconnect *and* an app attached. The voice bar shows "Reconnecting…" while it happens, as today |
| 27 | Resumed reconnect | hidden | never: nothing was lost |
| 28 | Unknown | debug only | — |
| 29 | Audio route | a small, persistent route glyph in the voice bar (speaker, headphones, AirPods, car) that changes with the route; VoiceOver announces the change. A 3-second line would be missed with the phone in a pocket | never in the list |
| 30–31 | Interruption, listening again | the voice bar's paused state (it exists, `onPausedChanged`) | never in the list |
| 32–35 | Audio needs your tap | a banner with the action ("Tap to resume listening"), cleared when audio runs again | never in the list |
| 36 | Voice off / closed | a banner (a state, not an event); "replaced" already is one | never in the list |
| 16 | Released push | debug only | — |

The 5-minute rule is from the log: on 10-27 one fresh reconnect in 34 met
even "attached and you spoke in the last 10 minutes"; the refresh waits
for a quiet moment, so the rule shows the rare mid-sentence failure and
costs no rows otherwise.

## 6. Nothing is lost: where each kind can still be found

The CEO's constraint: everything hidden from the normal view stays
reachable; nothing is deleted at the source; questions and outcomes are
never hidden.

**Show technical events** (Settings ▸ Health, next to Recent problems,
off by default, per phone; "system events" would read as iOS's): the
timeline shows every row of every kind, ungrouped, with the original text
and its exact time (seconds); technical rows keep the normal font in a
muted colour, so the page stays calm. A small "Technical events shown"
tag sits at the top of the conversation while it is on, so a screenshot
explains itself. A long press on any row copies its raw item or event as
JSON. Turning it off returns to the normal view; nothing is refetched.

| Kinds | Hidden or changed in the normal view | Still found |
|---|---|---|
| 1–12, 14, 17, 18, 21 | never hidden; 9–12 shortened, 17 reworded | full text on tap; the switch; the daemon's `GET /api/conversation`; `/debug` |
| 5 | folded into "Details" | tap; the switch; `/debug` |
| 13 (restating), and the voice's unanswered restatement of a settled question | folded into the question's line | tap the line; the switch; `GET /api/conversation` |
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
    ◷ While you were away · forumind finished the design doc…   12:44 ›
          ── Talking to Work Mac · 12:47 ──
    ◆ slingshot · stage Codex finished.                         12:48
    ⏱ Codex's question (Finder access) — closed                 12:49 ›
```

Four one-line rows in two styles, one divider. Gone from view: "Voice
context lost", both "Voice reconnected" lines, "Audio moved to Speaker."
(the voice bar's route glyph shows the speaker instead), and the voice's
unanswered restatement of the settled question (inside the question's
line). The "Talking to" divider uses the computer's own name. With fewer
than three outcomes nothing folds, so each is readable at a glance; the
missed one reads as news.

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
  ┌─────────────────────────────────────────────────────┐
  │ ◷ 8 updates while you were away   [Catch me up]  ⌄  │
  │   The two stuck waiting scripts are cleared…        │
  │   The local daemon is now the signed v0.1.9…        │
  └─────────────────────────────────────────────────────┘
```

One row. The two pushes orphaned by the restart count as missed; the
"Sheepdog updated · 12:51" divider sits inside the group when it is
opened. The reconnect pair and the shutdown notice are behind the
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
  "agent updates" or "updates while you were away"; the fold threshold
  becomes 3 (`minimumGroup`, `ConversationTimeline.swift:203`).
- `QuestionFocus` gains a settled mode (the pointer rule, kept after the
  question settles, 2-minute window, and only for a restatement you did
  not reply to).
- A `SettingsModel.showTechnicalEvents` flag (UserDefaults, per phone).
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
   "Show technical events" switch. Pure-function tests first.
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

- Hiding a reconnect that did matter. Mitigation: the 5-minute rule, the
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

Adopted from the review:

- Held pushes (15) move from B to C: only the floating question is
  amber, which is the brand rule.
- One icon per category; the bell, shared today by groups, pushes and
  missed pushes, is retired.
- "6 updates while you were away" instead of "things you missed"
  (blames less); mixed groups say both counts.
- Fold at three rows, not two, and show the newest two lines.
- A restart divider does not split a run of outcomes; it sits inside the
  opened group.
- The settled question is one tap target, not a line with an inline
  "Said aloud" link; "closed" until the daemon sends the cancel reason.
- Fold the voice's restatement of a settled question only when you did
  not reply to it, so rule A (dialogue never folded) holds.
- The reconnect window is 5 minutes, not 2: the log shows it costs no
  rows.
- A persistent route glyph in the voice bar instead of a 3-second line,
  which is missed with the phone in a pocket.
- The switch is "Show technical events", in Health, with a "Technical
  events shown" tag at the top of the conversation while on.
- A single missed push is a one-liner, and "Sheepdog updated" keeps the
  version behind a tap.
- The review's most valuable change: hide the reconnect pairs and
  shutdown notices (about 60 rows a day, 2 of 34 relevant) together with
  un-striking dropped pushes, "turning the page from a log into news".

## 10. Decisions by the CEO

The CEO approved all seven recommendations on 2026-10-27, as recommended.
The alternatives were not taken. Phase 1 (section 8) is built; the parts of
these decisions that belong to phases 2 and 3 are still to build.

1. **Reconnect row: when?** Decided: only when an app was attached and you
   or the voice spoke in the 5 minutes before; otherwise hidden. (Phase 1
   hides every reconnect row; the exception comes with phase 2, which pairs
   the rows.)
2. **Where does the switch live?** Decided: Settings ▸ Health, "Show
   technical events", off by default, with the footer "Adds reconnects,
   audio changes and other plumbing to the conversation, with exact times.
   For bug reports." (Built in phase 1.)
3. **"Catch me up" on missed updates?** Decided: yes, one chip on the
   group ("Catch me up") that asks the orchestrator for a summary of what
   you missed, since the voice never said them.
4. **Computer names like "M944QD4VY".** Decided: on pairing, if the
   ComputerName has no space and mixes letters and digits, suggest
   "Work Mac" and let you edit it; the divider uses your name.
5. **Settled questions: one line or the full card?** Decided: one line for
   every settled question, answered ones included ("You approved Codex —
   Yes"), the full card on tap.
6. **Orchestrator reply cards.** Decided: fold into the voice's bubble as
   "Details" when adjacent (section 5.1).
7. **Restart divider.** Decided: keep "Sheepdog restarted", and say
   "Sheepdog updated" when it was an update, the version on tap (needs a
   daemon field).


## 11. Phase 2: what was built, and where it differs

Built in the app, no daemon change. The context pass is `TimelineContext`
(`ConversationContext.swift`): one linear pass over the rows that decides
what the neighbours decide; `ConversationTimeline.layout` applies it, and
`classify` stays the one table of categories and flags.

- **Missed pushes.** Held and dropped pushes are outcomes that fold, shown
  as "While you were away · …" with no strikethrough. Both use that label
  (a held push has had no listener either); a group says "N updates while
  you were away", or "N agent updates · M while you were away" when mixed.
  "Catch me up" sends "Catch me up on what I missed." through the
  composer's path (`sendTyped`); there is no new endpoint.
- **Settled questions** are one line, tap for the full card with the push
  and the unanswered restatement under it (2-minute window; only the push
  with the question's words, or an interrupt from the same agent, folds).
  "Closed" replaces "No longer needed" everywhere. **Difference from 5.3:**
  settled-question lines do not fold into update groups, so a question is
  never folded away.
- **Reconnects.** Context lost is hidden and the fresh reconnect after it
  becomes the one "Voice restarted" row when dialogue (you or the voice)
  came in the 5 minutes before. The history does not say whether an app
  was attached; dialogue implies a client, which is the proxy used. The
  true signal is phase 3 (daemon field).
- **Restart.** "Voice session closed: shutdown." is plumbing, so the
  restart divider alone is shown; the version waits for phase 3.
- **Orchestrator replies** fold into a "Details" line under the voice's
  bubble (next or previous bubble within 2 minutes); pending or unvoiced
  replies stay cards.
- **Talking to** is a divider with the time; pairing suggests "Work Mac"
  for a machine-ID name (`ComputerNaming`), prefilled and editable.
- **Audio notices** are `LocalNotice` (words defined once). The route glyph
  is in the voice bar, a banner carries the ones that need a tap and clears
  when no longer true, and every notice is written to `os.Logger` (names
  redacted).

Left for phase 3 (built, section 13): notice `subkind`/`reason`, the cancel reason, the start
version ("Sheepdog updated"), and the daemon resolving held pushes at a
restart.

## 12. The visual system

Phases 1 and 2 decide what shows; this is how each row looks. Shape tells
the kinds apart and colour confirms. One place per concern:

- **What is a line of the ledger** is `RowClass.ledger` in the
  classification table; `ConversationTimeline.ledgerPositions` turns it into
  who joins the card above or below, and `gap` is the spacing (6 within an
  exchange, 14 divider to dialogue, 18 between categories, 0 in a run, 8
  next to a card, 6 in the technical view). The gutter's code is
  `TimelineCategory.code` (DLG, ASK, UPD, CTX, SYS, SCR).
- **How it looks** is in `ConversationRowStyle.swift` (`BubbleWidth`,
  `LedgerCard`, `LedgerLine`, `LedgerLook`, `ContextDivider`, `PlumbingLine`,
  `FloatingPill`); `ConversationRows.swift` only picks a look per kind.
- **Dialogue** is a bubble with one 6 pt corner (you right, 80 %; voice
  left, 86 %; iPad 70 %; whole width at the accessibility sizes). Orchestrator
  and Details are purple (`Thinking`, `ThinkingSoft`, `ThinkingText`, a 2 pt
  `ThinkingRule` when Details is open). **Needs you** is the only amber: a
  one-line pointer, the card above the voice bar answers. **Outcomes** have a
  28 pt round well (blue news with `terminal.fill`, `moon.zzz.fill` for
  missed, green approved, neutral declined with `xmark`, a hollow ring and a
  clock for closed); a card shown has a square well. **Context** is a hairline
  with a glyph; a notice this build does not know is a `Dash` outline.
- **Tokens added:** `ThinkingText`, `ThinkingRule`, `MutedOnFill` (captions
  inside `Fill` blocks), `Dash`, `Chevron`. `Danger` is for errors and
  destructive actions only.
- **Four behaviours** were added with the design, each small enough to revert
  alone: a declined answer is neutral (`LedgerLook.declined`); "Catch me up"
  shows "Asking…" until the voice answers or 20 s pass
  (`ConversationTimeline.voiceAnswered`); the floating question collapses to
  "Codex asked" and **Answer** from the third accessibility size (now one
  case of the rule in section 14); day headings are pinned section headers.
- **Differences from the mockups.** "Sheepdog wrote" is used for answers the
  text router gave (`via: text_router`); the "Typed" caption exists for items
  the history marks typed. A row's VoiceOver label is its visible words
  combined, not the mockup's sentence. The pointer's tap pulses the card with a
  short border flash; the card does not scroll.

## 13. Phase 3: what was built, and where it differs

Built: the small additive daemon change and the app using it. Nothing is
required of either side: an old app ignores the new fields, and a new app
works with an old daemon (it falls back to the words, as in phases 1 and
2). `/api/hello` has no capability list and the protocol number is not
bumped: the app reads each field when it is there.

- **`subkind` on every notice** (`ConversationItem.Subkind`,
  `internal/web/conversation.go`): `start` (`daemon.start`), `reset`,
  `closed` (`voice.closed`), `context_lost` (`voice.handle_dropped`),
  `fresh` and `resumed` (`voice.resume` without and with its context).
  `DaemonNotice` reads it first and the text only when it is absent or a
  value this build does not know.
- **`reason`** on the same item: a `closed` notice carries the event's
  reason (`shutdown`, ...), a `context_lost` one the handle drop's
  (`session_expired`, `resumed_twice`).
- **Why a question closed**, in the same `reason` field on an `approval`
  item: `expired`, `replaced`, or the cancel reason (`prompt_gone`,
  `agent_exited`, `pane_closed`, `closed`, `brain_interrupt`). A closed
  question's expanded card says it (`ConversationItem.closedReasonText`);
  no reason, or one this build does not know, keeps "It was closed on the
  computer." **Difference from section 8:** "No longer needed" had
  already become "Closed" (section 11), and `prompt_gone` is worded "It
  left the agent's screen, probably answered on the computer", because the
  daemon sees only that the prompt is gone, not who answered it.
- **`version`** on the `start` notice (from `daemon.start`'s existing
  `version`). The divider reads "Sheepdog updated to v0.9.1" when the
  version differs from the previous start in the loaded history, else
  "Sheepdog restarted" (or "Sheepdog started" for the first). The first
  start in a page has nothing to compare with, so it never says "updated".
- **Held pushes at a restart** (`internal/app/heldpushes.go`): right after
  `daemon.start` the daemon reads today's and yesterday's event files and
  logs `voice.push_dropped {reason: "restart", push_id, text, urgency,
  agent, age_ms}` for every non-silent `voice.push_held` without a
  released or dropped after it. The app already settles a held push on
  `voice.push_dropped`, so it shows "Dropped" instead of "Waiting for a
  listener". The app's wording ("nobody was listening for 5 minutes") is
  unchanged for this reason; reading the `reason` there is left for later.
  **Difference from section 8:** the true attached-client signal for the
  reconnect row (section 11) is not part of this change; the dialogue proxy
  stays.

## 14. When a question goes compact

An agent's multiple-choice question with three long options covered most of
a phone screen. The card had a height cap, so its question was cut off at the
top. Each option was a capsule with its label and description run together.
The recommended option was filled with amber, so it looked already chosen.

**The fix** has two parts.

- **The daemon.** The parser (`internal/herdr/prompt.go`, `parseOptions`)
  used to treat a line under an option as a description only when it started
  with a capital letter. Chinese has no capitals, so a Chinese description
  was joined to its label, and an English one was dropped. The parser now
  tells a wrapped label from a description by the pane's width: a line is a
  wrap only when its first word could not have fitted on the line above.
  The description reaches the app as its own field. "(Recommended)" or
  "(推荐)" at the end of a label becomes `recommended`, with `title`
  holding the label without it. All three fields are additive
  ([ws-contract.md](../ws-contract.md), "Approve by tap"). `label` is
  unchanged, so the voice and the orchestrator read what they read before.
- **The card.** Each option is a full-width row: the label in semibold, a
  small "Recommended" tag after it, and the description below in smaller,
  muted text. No option is filled, so all look the same until one is tapped.
  The question starts at its first word and shows at most three lines, then
  a fade and **More**.

**The rule.** The floating questions are either inline (the cards
themselves) or compact (one bar, "codex asked · Answer", which opens every
card in full in a scrolling sheet). One pure function decides:
`QuestionPresentation.decide` in SheepdogKit, unit-tested as a table.

1. **Accessibility 3 and larger: compact.** Even a short card leaves little
   of the conversation at those sizes, and the bar is the larger target.
   This was PR #468's rule; it is now one case of this function.
2. **Held: no change.** Nothing changes while the sheet is open or an answer
   is in flight or has just landed, so nothing moves under the user's
   finger. The sheet is attached to the container, not to the bar, so a
   change can never dismiss it.
3. **Not measured yet: no change.** Until both sizes are known, the current
   presentation stays (inline on first appearance).
4. **Otherwise: inline when the cards fit the budget.** The cards' natural
   height is their sum plus the gaps between them. The budget is
   `budgetShare` (0.55) of the area.
   - Coming back from compact, the cards must fit `returnShare` (0.85) of
     the budget. This hysteresis stops a size near the line, such as the
     keyboard or a rotation, from flipping the screen back and forth.

**What is measured.** Nothing is counted in characters. Each card is laid
out off screen, at the current width and text size and with its question
cut to three lines, whatever is showing. That copy is hidden from VoiceOver,
from taps and from UI tests. "More", an expanded command, a spinner or an
answer therefore never change the measured height.

The area is measured by the host on its root view, outside the floating
stack: the phone's screen, the iPad's conversation column, or the agent
window. It is the safe area above the keyboard. The cards' own size cannot
feed back into it, and it shrinks when the keyboard is up.

**Why 0.55.** It is about the old cap, half the screen, so every routine
approval that floated before still floats and stays one tap away. The rest
of the safe area keeps the capsule and the latest dialogue in view.

A first try at 0.4 sent Codex's real three-option command approval to the
bar on an iPhone. That is the most common prompt, so it would have cost an
extra tap every time. `QuestionsUITests.testCommandApprovalFloatsInline`
now guards it.

Measured on an iPhone 18 Pro in portrait and an iPad Air 11-inch in
portrait, at the default text size:

- **Inline:** a yes/no approval, and Codex's three-option command approval
  (testdata/herdr/codex-approval-prompt.screen.txt), whose middle option
  wraps. It measures 332 pt on the iPhone, against a budget of about 428 pt
  in a safe area of about 778 pt; 0.4 would have given about 311 pt.
- **Compact on a phone:** a question whose three options carry
  descriptions.
- **iPad:** the same described question fits in the taller column and stays
  inline. Codex's approval measures 291 pt in a 1180 pt window there.

**The cases.**

| Case | Outcome |
| --- | --- |
| Number of options, long descriptions | Each adds its measured height. Long ones go compact on a phone. |
| Free-text question (no options) | Measures small, stays inline. No special case. |
| Several agents asking at once | The stack must fit as a whole, else the bar says "2 questions" and the sheet lists them in `QuestionOrder`. |
| Keyboard up | The area shrinks. A yes/no card can stay, a command approval goes compact while typing, and it comes back when the keyboard goes. |
| Phone in landscape | The area is small, so cards are compact. |
| iPad, including split view | The column's own width and height are measured. A narrow split behaves like a phone. |
| Rotation, a Dynamic Type change, a second question | Each is a new input. The hysteresis and the held rule keep it from flip-flopping. |

**What stays.**

- The amber pointer row in the timeline stays in both presentations, and
  the bar never hides that a question is waiting.
- An answer from the sheet goes through the same `answer` path (the
  ActionRunner and its idempotency) as a card. Nothing auto-answers.
- VoiceOver reads the bar as "codex asked: first line of the question.
  Answer."
- Each question is announced once, however often the presentation changes.
- The bar and the rows are at least 44 pt tall.
- Reduce Motion turns the movement into a fade.
