# Sheepdog

A docked rail and a floating panel for [sheepdog](https://github.com/kccarlos/herdr-sheepdog), the daemon that lets you manage your coding agents by voice. The rail says whether the voice is listening, muted or off, who holds it ("Voice on iPhone"), and how many agents need you. Clicking it opens the panel: a window with the conversation you also see on the phone, read-only. (A `preview.png` is added with the screenshots; there is none yet.)

## What you see

**The rail**, docked on the right at 200 px on the `chrome` background. At 160 px or wider it is a column with 12 px padding and 10 px gaps:

1. The colour logo (22 px) and "Sheepdog" (13 px semibold).
2. The state, one of:
   - Looking for the daemon: "Looking for Sheepdog…" in `dim`.
   - Daemon down: "Sheepdog isn't running" (13 px semibold), "Start it with:", the command `sheepdog setup` in a `raised` box with a 1 px `border`, and "Looking again every 3 seconds." in `dim`.
   - Setup needed: "Setup needed" in `blocked`, then the daemon's own words (its voice `problem`, or "Run sheepdog setup to add a voice key.").
   - Otherwise: a 9 px dot and the voice's words in 13 px semibold, an optional `muted` note, and either "N agents need you" (`blocked`, 12 px medium) or "Nothing is waiting on you." (`dim`).
3. When the daemon answers but its event stream is down: a dashed 1 px `border` line, "Live updates paused", in `muted`.
4. An **Open panel** button (28 px high, 1 px `border`; `solid` with `solid_foreground` while an agent needs you).

Under 160 px it is a compact column: the logo (26 px), the voice dot, and, when agents need you, a `blocked` badge with the count; the whole column is the button and its tooltip says the state. The dot is `done` while listening, `idle` while muted, `working` while connecting, speaking or waiting for the voice, `blocked` in setup needed and `dim` otherwise.

The voice's words, from the daemon's `voice` in `/api/state`: "Voice off", "Connecting", "Voice not connected", "Waiting for the voice", "Ready" (nobody holds the voice), "Listening", "Muted", "Speaking" (this Mac holds it), and "Voice on iPhone" (or "…on a browser", else the client's name), with ", muted" or ", speaking" added, when another device holds it. This Mac is the holder whose `voice.client.platform` is `macos`.

**The panel**, a 420 × 640 window on the `chrome` background:

1. A header, 12 px padding, a 1 px `border` under it: the logo (20 px), "Sheepdog" (13 px semibold) and under it the voice dot (7 px) and words (11.5 px, `muted`); a `blocked` pill ("1 needs you", "2 need you") with `solid_foreground` text when agents need you; a **Technical** text button (11.5 px, `muted`); and a ✕ that closes the window. These stop the press so the rest of the window drags.
2. A banner when it applies: the daemon-down text as the whole body (title, the reason when something other than sheepdog answered, "Start it with:", `sheepdog setup`, a primary **Run it in a tab** button, "Looking again every 3 seconds."); the setup-needed block under the header (title in `blocked`, the daemon's words, a primary **Run sheepdog setup** button); the dashed "Live updates paused" line.
3. The timeline, newest first, with a scrollbar. Each day has a heading, "Today", "Yesterday" or "Monday 21 September" (with the year when it is not this one), between two hairlines, above that day's rows. Times are 24 h local, "17:42". Rows by category (docs/dev/design/conversation-timeline.md):
   - **Dialogue**: bubbles, 14 px radius with one 6 px corner, at most 4/5 of the width. You are on the right (`accent`, `accent_foreground`), the voice on the left (`raised`, `foreground`). A 10.5 px `dim` caption says "Typed" under your typed lines and "Sheepdog wrote" under a reply that was written.
   - **Needs you**, the only amber: an open question, or a push that interrupts, as a pointer: `blocked` at 12% fill, 1 px `blocked` at 33% border, 10 px radius, a 8 px `blocked` dot and "Codex asked — waiting for your answer" (the agent's name, else "An agent"), the question's words under it in `muted`.
   - **Outcomes**, lines of a ledger: consecutive lines share one `raised` card with a 1 px `border` and 10 px corners at the ends of the run. A 28 px well leads each: a round one with `>_` (`working` on `working` at 15%) for an agent's news, `✓` (`done` on `done` at 15%) for an approved question, `×` (`foreground` on `hover`) for a declined one, a hollow `border` ring with `–` (`muted`) for one that expired, was replaced or closed; a square well with `▢` for a card. The line is the agent's name (11 px, semibold, `muted`) over the words (12.5 px) over the time (10.5 px, `dim`). A settled question reads "You approved — Yes, proceed · by tap" or "Codex's question (allow Computer Use to use Finder) — closed".
   - **Context**: a hairline with a centred 11 px `muted` caption: a day, "New conversation · 17:42", "Sheepdog restarted · 17:42", "Sheepdog updated to v0.9.1 · 17:42", and "Voice restarted — it may not remember the last minute · 17:42" when the voice lost its context within 5 minutes of dialogue.
   - **System** (reconnects, a closed voice session, kinds this build does not know): not listed, except under **Technical**, where every row shows with its three-letter code (DLG, ASK, UPD, CTX, SYS) in a gutter and a dashed 1 px `border` box for the plumbing.
   - The last row says "Earlier messages", "Loading earlier…" or "This is the start of the conversation" in a dashed box.
4. With no history: "Nothing has been said yet." centred in `dim`.

Every colour is a `theme()` name, so dark and light both work; the only computed colours are `blocked` and `working` and `done` with an alpha suffix. The logo is `logo.svg` with its cream sticker outline, drawn with `image()` (which keeps colour; `svg()` would tint it to one).

## How it behaves

- The data layer asks the daemon, never writes: `GET /api/state` and `GET /api/conversation?limit=50` (and `&before=<next_before>` for older pages) with `fetch`, and `GET /events?kinds=card,conversation,daemon,pending,voice,web&since=<newest item id>` over a raw TCP socket (`net.connect`), because `fetch` is buffered. The mod reads the chunked HTTP body itself and parses `id:`, `event:` and `data:`. Every record, the `: keepalive` comments the daemon sends every 15 s included, proves the stream is alive; after 40 s without one it reconnects, waiting 1 s, then 2, 4, 8 and 15 s while that fails, and sends `Last-Event-ID`.
- An event that can change what is shown (a question, a push, an utterance, a reset, a restart, who holds the voice, mute, speaking) makes it ask `/api/state` and `/api/conversation` again, at most four times a second; transcript fragments wait for 1.5 s of quiet. Other events (audio stats, playback, usage) change nothing. Items merge by id, the server's copy winning.
- While the stream is down it asks every 3 seconds instead and shows "Live updates paused". If nothing answers the daemon is down, with the same 3 s retry.
- The list pages back when its last row is drawn: it asks for the next older page (`before`), and the rows are added below, so nothing moves under you.
- **Technical** shows every row the daemon sent; without it only what was said, what needs you, what agents did and where you are.
- Clicking the rail (or **Open panel**) opens one window with `key: "sheepdog-panel"` and `keep: true`: opening it again replaces it, and it returns after a restart. The ✕ closes it for good. `mod.open` opens the same panel as a dialog without that key.
- The buttons **Run it in a tab** and **Run sheepdog setup** run `terminal.open` with `argv: ["sheepdog", "setup"]` in a tab.
- It sends nothing: no POST, no `/ws`, no keys, no files. Nothing is written inside the mod folder at run time (herdr-app reloads a mod when its folder changes).

## Built from

- `main.js`: one `View`, `Sheepdog`. An overlay copy (`props().overlay` is set) is the panel; the docked block is the rail. The rail's copy asks only for the state. Imports `View` from `gpui-kit`, `connect` from `net`, and from `herdr`: `closeOverlay`, `openOverlay`, `props`, `run`, `shared`, `theme`. It starts the data layer in a `cx.spawn` task and stops it in `close()`; `cx.timer.every(250, …)` pages back; `.on_size_change` switches the rail between wide and compact.
- `mod.json`: `{ "place": "right", "size": 200, "title": "Sheepdog", "overlay": { "width": 420, "height": 640 } }`.
- `net/`: `config.js` (the base URL, default `http://127.0.0.1:7777`, from the block's `props.url` or the shared key `sheepdog.url`; only plain `http`), `http.js` (the two GETs), `sse.js` (chunked decoder, SSE parser, the client with its 1 s silence check, and the backoff), `live.js` (events and the 3 s poll drive the store).
- `model/`: `classify.js` (the phone's classification table, its notices and the settled-question words), `timeline.js` (day headings, dividers, the reconnect and settled-question rules, ledger joins, spacing, newest-first layout), `history.js` (pages merged by id), `store.js`, `status.js` (the voice's words, the states), `events.js` (which events matter, and the `kinds`). None imports `herdr`.
- `ui/`: `rail.js`, `panel.js` (`list()` with a `Scrollbar.vertical` of the same id), `rows.js`, `widgets.js`. They use `div`, `image` and `list` from `gpui-kit`, `h_flex`, `v_flex` and `Scrollbar` from `gpui-base`.
- `logo.svg`, drawn with `image("mods/sheepdog/logo.svg")`.
- Network and programs, to say before installing: `fetch` and one raw TCP socket to the daemon address only (default `127.0.0.1:7777`, this computer; nothing else is contacted, no token is sent), and `run("terminal.open", { argv: ["sheepdog", "setup"], place: "tab" })` when you press a setup button. It reads no files and writes none.

## Watch out

- **Newest first, on purpose.** `list()` ignores `track_scroll` (`herdr-app check` warns), so code cannot scroll it to the bottom; only `v_virtual_list` takes a scroll handle and it wants each row's height. Newest first needs no scrolling.
- The rows of a `list()` may carry no handlers: nothing in the timeline is clickable yet, and the phone's "N agent updates" group (it opens on a tap) is not drawn; every update is its own line.
- `herdr-app check` renders only the docked rail, with no daemon. The panel, its rows and the daemon states are only drawn when opened: look at them in herdr-app.
- `fetch` cannot stream (buffered, 8 MiB, 30 s) and the mod runtime has no `setTimeout`: the stream is `net.connect` plus `cx.sleep`, and a read that never returns cannot be given a timeout, so a watchdog task checks the silence every second. Closing the socket may not wake a pending read; the client races the read against the watchdog and `stop()`.
- No `TextDecoder` is assumed: `net/sse.js` decodes UTF-8 by hand. No `Intl` either: times are `HH:MM`.
- Each open copy of the mod has its own state, stream and poll: the rail and the panel are two connections. A copy hidden in a column keeps its stream; it asks nothing while the daemon is quiet.
- The panel lists only what `GET /api/conversation` has: items with server ids. The orchestrator's replies and held pushes come over `/ws`, which this mod never opens.
- Only plain `http://host[:port]` is read: the raw socket does not speak TLS.
- The rail docks only into a layout you own; in a shipped layout use `mod.open`.

## Done when

1. `herdr-app check herdr-app/mods/sheepdog` passes with no daemon running, and `make test-panel` passes.
2. With no daemon on the address the rail says "Sheepdog isn't running" with `sheepdog setup`, and the panel says the same with a **Run it in a tab** button; starting the daemon turns both into the voice state within about 3 seconds, with no restart.
3. With a daemon the rail's voice words match `/api/state` (`voice.mode`, `status`, `muted`, `speaking`, `client`): with the phone holding the voice it says "Voice on iPhone".
4. A blocked agent adds a `blocked` count to the rail within about a second and a pointer to the panel; answering it elsewhere turns the pointer into a settled line.
5. The panel's timeline matches the phone's for the same daemon: the same categories, settled questions as one line, the reconnect rule, restarts as dividers.
6. Reaching the bottom of the list loads the next older page until "This is the start of the conversation".
7. Stopping the daemon shows the daemon-down state; restarting it brings the timeline back without reopening the window. "Live updates paused" shows only while `/api/state` answers and `/events` does not, and goes when the stream is up.
8. Reloading the mod twice leaves one panel window; closing it with ✕ and restarting herdr-app does not bring it back.
9. Nothing was written inside the mod folder and no request other than the GETs above reached the daemon.
