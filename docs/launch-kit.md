# Launch kit

Storyboard for the hero video, plus draft launch posts. The owner records the
video on a real phone; nothing here is recorded yet. Every claim in the lines
and captions must match what the real app does on the day of recording; the
checks are in "Before you record".

## Hero video (45–60 s, vertical 9:16)

**Message:** talk to your Herdr agents, from anywhere: hear who's stuck,
approve with a word, ask what broke, right from your phone.
**Look:** handheld phone footage, real lock screen, no screen mockups. Show
the screen only while it matters; keep the desk and the street in frame.
**Sound:** earbud-mic audio for the user's lines, the app's voice from the
phone speaker or a clip-on recorder. No music under speech; a quiet bed
between scenes.

| # | Time | Shot | On-screen caption | Line (exact) |
|---|---|---|---|---|
| 1 | 0–5 s | Desk, laptop with herdr: three panes (Claude Code, Codex, agy) working. User puts in earbuds, stands up. | Three agents. Zero babysitting. | — |
| 2 | 5–12 s | User walks out of the door; hold on the closing door, then outdoors. | Walk away from your desk. | — |
| 3 | 12–22 s | Phone in hand, lock screen: a sheepdog notification, "api needs your approval". Thumb stays off the phone. The app's voice speaks over the earbuds. | An agent needs you. You hear it first. | **Sheepdog:** "Codex in api needs your approval to run the migration." |
| 4 | 22–30 s | Close-up of the earbud (or the lock screen with the Live Activity), the user speaks. | Approve by voice. | **You:** "What will the migration touch?" **Sheepdog:** "An index on orders, no data changes." **You:** "Approve." **Sheepdog:** "Approved." |
| 5 | 30–42 s | Walking, wider shot; the user raises a hand to the earbud. | Ask what is going on. | **You:** "What's going on?" **Sheepdog:** "Codex is migrating, Claude Code is writing tests, agy is idle. I'll tell you when Codex is done." |
| 6 | 42–52 s | Back at the desk: laptop shows the migration done and tests passing. Earbud voice over the cut. | Come back to finished work. | **Sheepdog:** "Codex finished the migration; auth's tests pass." |
| 7 | 52–60 s | End card on the phone: the logo and the install line. | herdr plugin install kccarlos/herdr-sheepdog | — |

Notes:

- The lines in shot 3–6 are the spoken script; the real model will phrase
  things its own way. Record real takes, and if the wording drifts, keep the
  take and fix the caption to match (never dub).
- Shot 3 needs push configured (`[push.apns]`, [push.md](push.md)) on the
  daemon the app talks to, with the phone locked.
- Shot 6 can be shot first and re-used: stage a short real task in each pane
  so the finish lands while you are out.

### Before you record (check on the day)

- The earbud remote, the Live Activity and the lock-screen notification work
  on your device ([status.md](status.md) lists them as not yet checked on a
  device); drop a shot rather than fake one.
- `sheepdog doctor` is clean and chattiness is `chatty` (the default).
- The migration approval is a real approval prompt from a real agent.
- Use staging projects only, no real secrets on screen. Blur the token if the
  pairing screen shows.

### Stills for the README

`assets/readme/hero.gif` is a 12 s loop of its own: the iOS app's offline
demo on the left, the conversation as chat bubbles on the right
(`scripts/readme-media/bubbles/`, lines in its `lines.tsv`).
Record `status.gif` (ask "what's going on", shot 5), `phone.gif` (approve
from the phone, shot 4) and `install.gif` (the three install steps in
herdr, once the plugin ships). Keep each GIF under ~5 MB.

## Launch post for X (draft, under 280 characters)

```text
Talk to your Herdr agents. From anywhere. Hear who's stuck, approve with a word, ask what broke, right from your phone. Sheepdog is a voice supervisor for Claude Code and Codex in herdr, open source. [video] github.com/kccarlos/herdr-sheepdog
```

(242 characters counted literally, so under 280 even before link shortening.)

## Launch post for the herdr community (draft)

**Title:** sheepdog: supervise your herdr agents by voice, away from your desk

I run several coding agents in herdr and kept coming back to find one had
been blocked on an approval for an hour. sheepdog fixes that by watching
herdr's agent state and talking to me.

What it is:

- An iPhone app plus earbuds. When an agent blocks, I get a lock-screen
  notification and hear it. I say "approve", or "what's going on?", and it
  answers for the whole fleet, blocked ones first.
- Two models: a realtime voice (Gemini Live or GPT-Live) talks to me; behind
  it a stateful orchestrator reads the panes and the agents' session records,
  asks a read-only fork of the worker when a screen is not enough, remembers
  "tell me when auth is done", and comes back to questions it could not
  answer.
- A hard rail in code: a worker's approval prompt is answered only on my own
  words in that turn. The voice model has no hands.
- It is installed as a herdr plugin: `herdr plugin install
  kccarlos/herdr-sheepdog`, run "Set up sheepdog" (one Gemini key), then
  "Pair your phone" (QR).
- Every prompt and harness change goes through an eval suite with confidence
  intervals; the index, including the experiments that lost, is in
  `docs/eval/README.md`.

Try it without herdr: `sheepdog demo` (three scripted agents in your browser)
or the iOS app's demo mode.

Honest limits: the iOS app is TestFlight-only for now; push notifications
need your own APNs key; herdr does not yet report agy approval prompts as
blocked. Feedback welcome, especially on what you would want it to say first.

Repo: https://github.com/kccarlos/herdr-sheepdog
