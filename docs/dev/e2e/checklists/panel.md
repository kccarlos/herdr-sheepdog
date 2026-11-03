# Panel acceptance checklist

Run against a private environment ([`../../e2e.md`](../../e2e.md)): `up.sh`,
raise the window, then tick each step. Shoot with `shot.sh` when a step says
"look". The panel is the Sheepdog block at the window's right edge.

## Settings fold and remember

1. Open the panel menu, Settings. Expect sections Voice, Orchestrator,
   Languages, Replies, Devices, each folded except the one you used last.
2. Open Languages, close the window's Settings, reopen it. Expect Languages
   still open (the state is saved in the shared key `sheepdog.settings.open`).
3. Fold everything, restart Herdr (`down.sh` and `up.sh` start clean; instead
   quit only the private window by pid and reopen it). Expect the same folds.

## Orchestrator switch refresh

1. Settings, Orchestrator: switch provider (Fake to Fake 2).
2. Expect the model list to reload for the new provider without reopening
   Settings, and `curl $E2E_URL/api/state | jq .brain` to show the new
   provider.

## Open Setup

1. `scenario.sh <dir> voice-off`. In Settings, press "Open Setup".
2. Expect the browser to open the private daemon's `/setup` page (port from
   `env.sh`), not `:7777`. A failure must show where to go instead.

## Languages

1. Settings, Languages: pick a language for the voice.
2. Expect the choice to stick after a refresh, and `/api/state` settings to
   show it.

## Composer: send is instant

1. Click the composer, type `hello`, press Enter.
2. Expect the line in the conversation at once (before any reply), the
   composer empty and still focused. Shift+Enter makes a new line instead.
3. With `daemon-down`, send a line: expect it kept with a plain failure note,
   not lost; after `daemon-up` it sends.

## Keys: Enter and Esc

1. With a question open (`approval`), focus the panel: Enter or a digit
   answers it per the key hints shown on the card; check `/api/state`
   `questions` empties.
2. Esc on the panel returns the keys to the pane that had them.
   Computer-use captures Escape globally: send key code 53 through System
   Events with computer-use idle, or read the `onKey` debug log.

## Sheepdog.app not running

1. With the daemon up and no voice client (`voice-client disconnected`), the
   voice area says "Sheepdog.app isn't running" with an "Open Sheepdog.app"
   action (or "isn't installed" with the install line and "Open Setup" when
   it is missing).
2. `voice-client connected`: the note goes away and the holder shows
   macOS. Do not press "Open Sheepdog.app" here: it would start your real app.

## Daemon down

1. `scenario.sh <dir> daemon-down`. Expect the panel to say the daemon is not
   reachable, with the fix, and `herdr-app status` to list no errors.
2. `daemon-up`: the panel recovers on its own within a few seconds, with the
   conversation back.

## Approvals show at once

1. `scenario.sh <dir> approval`. Expect the card ("auth asked", Answer, the
   "1 needs you" badge) and the conversation item within about a second,
   without a manual refresh.
2. Answer it from the panel; expect the card to clear and the agent to leave
   blocked in `/api/state`.

## Panel auto-update

TODO: once PR #534 lands, add steps (update available note, apply, panel
reload). Not testable before.
