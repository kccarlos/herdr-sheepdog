# Launch kit

Draft launch posts and the README stills. The launch video's script,
storyboard and recording checklist are in [launch-video.md](launch-video.md).

## README media

`assets/readme/hero.gif` is a 12 s loop of its own: the iOS app's offline
demo on the left, the conversation as chat bubbles on the right
(`scripts/readme-media/bubbles/`, lines in its `lines.tsv`).
Record `status.gif` (ask "what's going on", shot 5 of the launch video),
`phone.gif` (approve from the phone, shot 4) and `install.gif` (the three install steps in
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
  `docs/dev/eval/README.md`.

Try it without herdr: `sheepdog demo` (three scripted agents in your browser)
or the iOS app's demo mode.

Honest limits: the iOS app is TestFlight-only for now; push notifications
need your own APNs key; herdr does not yet report agy approval prompts as
blocked. Feedback welcome, especially on what you would want it to say first.

Repo: https://github.com/kccarlos/herdr-sheepdog
