# Sheepdog panel for herdr-app

A [herdr-app](https://herdr.dev) mod for [sheepdog](../../../README.md), the daemon that lets you manage your coding agents by voice. It shows the conversation you also see on the phone: what you and the voice said, the questions that need you, what your agents did. This first version is read-only: you cannot send, answer or talk from it yet. The design is [docs/dev/design/desktop-client.md](../../../docs/dev/design/desktop-client.md) (sections 6 to 8).

- **The rail** docks on the right (200 px; drag it under 160 px for a compact column). It says whether the voice is listening, muted or off, and who holds it ("Voice on iPhone"), how many agents need you, and, when it has to, that Sheepdog is not running or needs setting up. Click it, or press **Open panel**, to open the panel.
- **The panel** is a window over your layout: it drags, it remembers where you dropped it and it comes back after a restart. It lists the conversation newest first, with older history added as you reach the bottom. **Technical** adds the plumbing (reconnects, restarts) the timeline hides.

## Install

Needs the sheepdog daemon on the same computer (`sheepdog setup`; default `http://127.0.0.1:7777`) and a herdr-app with mods. Copy this folder to `~/.config/herdr-app/mods/sheepdog/` (leave `README.md`, `RECIPE.md` and the `*.test.mjs` files out if you like), then `herdr-app check ~/.config/herdr-app/mods/sheepdog`. herdr-app docks it the first time it sees the folder, into a layout you own; in a shipped layout use `mod.open`. Written and checked with the runtime `herdr-app check` reports as gpui-shell 0.7.1.

To point it at another address, set `url` in the layout block's `props` or the shared key `sheepdog.url`. Only plain `http://host[:port]` works (the stream is read over a raw socket).

## What it does on your computer

It only talks to the daemon address, and sends nothing:

- `GET /api/state` and `GET /api/conversation?limit=&before=` with `fetch`, and a `GET /events?kinds=…` stream over a raw TCP socket. No token and no cookie: the daemon treats this computer's loopback address as the local device. It never opens `/ws`, because a second voice client would replace your phone.
- The **Run it in a tab** and **Run sheepdog setup** buttons run `sheepdog setup` in a new terminal tab (`terminal.open`).
- It reads no files and writes none: herdr-app reloads a mod when its folder changes, so nothing is ever written inside it.

## Develop

`net/` and `model/` import nothing from herdr, so plain node tests them: `make test-panel` from the repository root (it is part of `make test-web`, which CI runs). The timeline's classification is shared with the iPhone app through `ios/Tests/Fixtures/timeline-classification.json`: change `model/classify.js` or `ConversationTimeline.classify` and that file together. `herdr-app check herdr-app/mods/sheepdog` loads the mod and renders the rail once, with no daemon and no network; it writes `gpui-kit.d.ts` and `jsconfig.json` into the folder, which `.gitignore` leaves out. The panel and its rows are only drawn when you open them, so look at them in herdr-app too.

[RECIPE.md](RECIPE.md) is the brief to build this mod from scratch.
