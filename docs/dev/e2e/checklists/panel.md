# Panel acceptance checklist

Run against a private environment ([`../../e2e.md`](../../e2e.md)): `up.sh`,
raise the window, then tick each step. Shoot with `shot.sh` when a step says
"look". The panel is the Sheepdog block at the window's right edge.

## Settings fold and remember

1. Open the panel menu, Settings. Expect sections Voice, Paired devices,
   Orchestrator, Languages, Replies. First time, Voice and Paired devices are
   open and the rest folded; sections fold independently (not one at a time).
2. Open Languages, close the window's Settings, reopen it. Expect Languages
   still open (the state is saved in the shared key `sheepdog.settings.open`
   in `<app home>/shared.json`).
3. Fold Voice, restart Herdr (`down.sh` and `up.sh` start clean; instead
   quit only the private window by pid and reopen it). Expect the same folds.

## Orchestrator switch refresh

1. Settings, Orchestrator: open Fake 2 and pick a model (deepseek-flash).
2. Expect "Orchestrator changed.", the model list reloaded without reopening
   Settings, and `curl $E2E_URL/api/state | jq .brain` to show provider
   `fake2`.

## Open Setup

1. `scenario.sh <dir> voice-off`. In Settings, press "Open Setup".
2. Expect "Setup opened in your browser." and the browser on the private
   daemon's `/setup` page (port from `env.sh`), not `:7777`. A failure must
   show where to go instead. The browser is not in the kit's window, so the
   URL is only checked by the panel's unit test.

## Languages

1. Settings, Languages: pick a language for the voice.
2. Expect "Languages changed.", the choice to stick after Refresh, and
   `GET $E2E_URL/api/settings` `languages` to show it.

## Composer: send is instant

1. Click the composer, type `hello`, press Enter.
2. Expect the line in the conversation at once with "Sending..." (before any
   reply), then "Typed" and the canned reply, the composer empty and still
   focused. Shift+Enter makes a new line instead.
3. With `daemon-down` the panel replaces the composer with the daemon-down
   state (see below), so there is nothing to type into; the outbox path is
   covered by `model/outbox.test.mjs`.

## Keys: Enter and Esc

1. With a question open (`approval`), click "Answer", then press `1`:
   "Yes, proceed" is sent (the card shows no key hints); `/api/state`
   `questions` empties and the conversation shows "You approved".
2. Esc in the composer goes to the panel, Esc on the panel returns the keys
   to the pane that had them. `herdr-app status` `focused_pane` does not
   change with panel focus, so it cannot show this; watch where a digit
   lands with a question open. Computer-use captures Escape globally: send
   key code 53 through System Events with computer-use idle.

## Sheepdog.app not running

1. With the daemon up and no voice client (`voice-client disconnected`), the
   voice area says "Sheepdog.app isn't running" with an "Open Sheepdog.app"
   action (or "isn't installed" with the install line and "Open Setup" when
   it is missing).
2. `voice-client connected` (needs `voice-on`): the note goes away, the
   status shows "Listening" and `/api/state` `voice.client.platform` is
   `macos`. Do not press "Open Sheepdog.app" here: it would start your real app.

## Daemon down

1. `scenario.sh <dir> daemon-down`. Expect "The Sheepdog daemon isn't
   running" with `sheepdog start`, "Start Sheepdog" and "Run setup", and
   `herdr-app status` to list no errors.
2. `daemon-up`: the panel recovers on its own within a few seconds, with the
   conversation back.

## Approvals show at once

1. `scenario.sh <dir> approval`. Expect the card ("auth asked", Answer, the
   "1 needs you" badge) and the conversation item within about a second,
   without a manual refresh.
2. Answer it from the panel (Answer, then `1`) or with `POST
   /api/pending/answer {"id":…,"option":N}`; expect the card to clear and
   `agents[0].last_block.answered` to be true. The mock pane stays `blocked`.

## Panel auto-update

1. Build the daemon from a tree whose embedded panel has `DEFAULT_URL` set to
   the private port, and `up.sh --port N --panel-from <older checkout>`.
2. Write `mods/sheepdog/.sheepdog-install.json` in the app home (version, a
   sha256 per panel file as on disk, and the layout hash), then restart the
   daemon (`voice-off`). Expect `/api/state` `panel` to go from
   `update_available: true, modified: false` to `update_available: false`,
   the log line "updated the herdr-app panel", `model/panel-update.js` on
   disk and `herdr-app status` `errors` empty.
3. Edit any panel file in the app home. Within about 10 s expect
   `modified: true` and the window to show "A newer panel is available" with
   "Run sheepdog setup" (this proves the replaced panel is the one running).
