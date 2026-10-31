# Sheepdog panel for herdr-app

A [herdr-app](https://herdr.dev) mod for [sheepdog](../../../README.md), the daemon that lets you manage your coding agents by voice. It shows the conversation you also see on the phone: what you and the voice said, the questions that need you, what your agents did. You can type to Sheepdog, answer its questions and tap its suggested replies from it; talking (the microphone) is a later version. The design is [docs/dev/design/desktop-client.md](../../../docs/dev/design/desktop-client.md) (sections 6 to 8).

- **The rail** docks on the right (200 px; drag it under 160 px for a compact column). It says whether the voice is listening, muted or off, and who holds it ("Voice on iPhone"), how many agents need you, and, when it has to, that Sheepdog is not running or needs setting up. Click it, or press **Open panel**, to open the panel.
- **The panel** is a window over your layout: it drags, it remembers where you dropped it and it comes back after a restart. It lists the conversation newest first, with older history added as you reach the bottom. **Technical** adds the plumbing (reconnects, restarts) the timeline hides. At the bottom is the composer: **Enter** sends, **Shift+Enter** starts a new line. Your line shows at once, then the reply; if Sheepdog cannot take it (the orchestrator is off, nothing answers) the reason shows under the box and your text stays. An agent's question floats above the composer with a button per option (when they would take too much of the window it is one bar, "Codex asked · Answer", that opens them). The orchestrator's suggested next steps are buttons above the box while they still answer something. A card the orchestrator showed (a table, a list) opens when you press its row, with a button that shows the agent in herdr.

## Install

Needs the sheepdog daemon on the same computer (`sheepdog setup`; default `http://127.0.0.1:7777`) and a herdr-app with mods. Copy this folder to `~/.config/herdr-app/mods/sheepdog/` (leave `README.md`, `RECIPE.md` and the `*.test.mjs` files out if you like), then `herdr-app check ~/.config/herdr-app/mods/sheepdog`. herdr-app docks it the first time it sees the folder, into a layout you own; in a shipped layout use `mod.open`. Written and checked with the runtime `herdr-app check` reports as gpui-shell 0.7.1.

To point it at another address, set `url` in the layout block's `props` or the shared key `sheepdog.url`. Only plain `http://host[:port]` works (the stream is read over a raw socket).

## What it does on your computer

It only talks to the daemon address:

- `GET /api/state` and `GET /api/conversation?limit=&before=` with `fetch`, and a `GET /events?kinds=…` stream over a raw TCP socket. No token and no cookie: the daemon treats this computer's loopback address as the local device. It never opens `/ws`, because a second voice client would replace your phone.
- Two `POST`s with `fetch`, each with an `Idempotency-Key`: `/api/turn` with a line you typed (or a suggested reply you tapped), and `/api/pending/answer` with the option you tapped.
- The **Run it in a tab** and **Run sheepdog setup** buttons run `sheepdog setup` in a new terminal tab (`terminal.open`). **Show … in herdr** on a card runs herdr-app's own `agent.focus` for that agent's terminal.
- It reads no files and writes none: herdr-app reloads a mod when its folder changes, so nothing is ever written inside it.

## Develop

`net/` and `model/` import nothing from herdr, so plain node tests them: `make test-panel` from the repository root (it is part of `make test-web`, which CI runs). The composer's rules, the outbox, the questions' phases and compact rule, and the suggested replies' liveness are in `model/` for that reason. The timeline's classification is shared with the iPhone app through `ios/Tests/Fixtures/timeline-classification.json`: change `model/classify.js` or `ConversationTimeline.classify` and that file together. `herdr-app check herdr-app/mods/sheepdog` loads the mod and renders the rail once, with no daemon and no network; it writes `gpui-kit.d.ts` and `jsconfig.json` into the folder, which `.gitignore` leaves out. The panel and its rows are only drawn when you open them, so look at them in herdr-app too.

[RECIPE.md](RECIPE.md) is the brief to build this mod from scratch.
