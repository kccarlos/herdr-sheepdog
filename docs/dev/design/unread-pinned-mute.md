# Unread, pinned and mute

Status: PRs 1 to 3 merged, 2026-11-03. Open questions decided (§12).

Three features for the agent list on the phone, in the herdr-app panel and
for the voice:

1. **Unread and pinned agents.** An agent that finished a turn nobody has
   looked at is *unread*. A pinned agent sorts first in its group. On
   herdr-core both marks are the terminal labels herdr-app already uses
   (herdr-app's `docs/labels.md`: `herdr.seen=<turn>` and
   `herdr.pinned=1`), so a pin set on the phone shows
   in every Herdr window and the other way round. herdr-tui has no labels,
   so the daemon keeps the marks in its own store. Opening an agent on the
   phone marks it seen. The phone can pin and unpin.
2. **Groups.** The list is grouped **Needs you**, **Working**, **Done**,
   with unread agents first within Done. Phone and panel show the same
   groups and order.
3. **Mute a space.** Muting a space's notifications stops push alerts,
   desktop notifications and the voice's unprompted announcements about its
   agents. It does not change the list, the badges or what the voice says
   when asked.

Words follow the [glossary](../glossary.md). "Mark" below means the seen
and pinned state of one terminal. "Space mute" is a different thing from
the voice mute (`POST /api/voice/mute`, which turns the microphone off). The
code, API and UI never call it just "mute".

## 1. Decisions

| # | Decision | Why |
|---|---|---|
| D1 | Marks belong to the **terminal**, not to the agent row or the agent's session. They survive an agent exiting and a new agent (or a resume) starting in the same shell. They end when the terminal closes. | Same as the labels on hc. `state.Agent` rows are deleted on `Released`, so the marks cannot live on the row. A resume in a *new* terminal starts with no marks, as in herdr-app. |
| D2 | One Go interface, `herdr.Marker`. hc implements it over labels. A sheepdog store (`internal/attention.Local`) implements it for herdr-tui and `--herdr=mock`. On hc the store also covers a terminal that has no `herdr.seen` label yet. | Upstream says "A missing label is not `0`: use your own seen rules". |
| D3 | The **turn** is the backend's own counter of finished turns: hc's `turn`; herdr-tui's `completion_seq`. herdr-tui's `state_change_seq` is **not** a turn, because it goes up on every state change. | A blocked→idle change without new work must not make an agent unread. |
| D4 | Without a stored seen mark, the backend's own word decides: `done` is unread and `idle` is read. herdr-tui reports `done` until someone focuses the pane in a TUI client (its server-side seen state), so a pane looked at in the TUI reads as seen without extra work. | This is the only seen signal herdr 0.9.5 offers. Its only write is `agent.focus`, which would move the user's TUI view. Sheepdog never calls it for this. |
| D5 | The daemon computes `group`, `unread` and `pinned` once (`attention.Classify`, pure Go). Clients only apply the order (§5). Older daemons send none of it, so clients fall back to grouping by state. | Phone, panel and voice must agree. A shared fixture keeps the three sort implementations in step. |
| D6 | Pinned agents sort first **within** their group. There is no separate Pinned section. | `labels.md`: "Pinned agents go first, in the client's usual order." A pinned agent that blocks still has to show under Needs you. |
| D7 | `seen` only goes up, and never above the turn the client actually showed. The one lower write is **Mark as unread** (`turn − 1`, a no-op at turn 0). | Follows `labels.md`. Without the client's turn, a turn that ends between drawing the list and the tap would be marked seen without anyone seeing it. |
| D8 | Space mute is stored by the daemon, keyed by the space's **folder path**, in `<data_dir>/space_mutes.json`. It reaches clients through `/api/state` and an event. | herdr-tui workspace ids (`w5`) are positional and come back after a close or a herdr restart. hc space ids are labels. The folder is stable on both backends, the same reason wrap-up commands are stored by folder (`wrapups.json`). |
| D9 | Mute is a code ceiling next to chattiness, not a prompt instruction. A kept promise ("tell me when auth finishes") and the finish of the user's own instruction still speak and still push. | Rails stay in code (AGENTS.md). A request the user made explicitly is newer intent than the mute. |
| D10 | New features are named `attention` and `space-mute` in `bot-ready`. The panel never opens `/ws`, so it checks whether the `/api/state` fields are present. `/ws` protocol stays 1. | [ws-contract.md](../ws-contract.md#protocol-version-and-handshake): an optional part of the contract is a feature, not a new protocol. |

## 2. Data model

### 2.1 Marks (per terminal)

```go
// internal/herdr/marks.go
type Marks struct {
    Turn   uint64  // finished turns of this terminal (D3); 0 = none yet
    Seen   *uint64 // last turn a person looked at; nil = no record (D4)
    Pinned bool
    Word   State   // the backend's own word: done vs idle decides when Seen is nil
    Error  bool    // hc status word "error": counts as needs you (labels.md)
    Shared bool    // true when the marks are hc labels every Herdr client shares
}

// Marker is optional; app wiring always supplies one (hc backend, else attention.Local).
type Marker interface {
    AllMarks(ctx context.Context) (map[string]Marks, error) // by pane id
    MarkSeen(ctx context.Context, paneID string, turn uint64) (Marks, error)   // raise-only, clamped to Turn
    MarkUnread(ctx context.Context, paneID string) (Marks, error)              // seen = Turn-1; no-op at 0
    SetPinned(ctx context.Context, paneID string, pinned bool) (Marks, error)
    MarkChanges() <-chan string // pane ids whose marks changed outside these calls
}
```

`herdr.AgentInfo.StateChangeSeq` keeps its meaning. hc already fills it with
`turn`, and the TUI sends `state_change_seq` there. Code that wants a turn
reads `Marks.Turn`, never `StateChangeSeq`.

**hc (`internal/herdrcore`).**

- `Turn` is `Terminal.Turn`. `Seen` is `labels["herdr.seen"]` parsed as an
  integer. A missing or unparseable label falls through to the local store
  for that terminal (D2).
- `Pinned` is a non-empty `labels["herdr.pinned"]`.
- The backend keeps one `events.subscribe` connection with no filter and
  caches marks per local terminal. It applies `turn` and `labels` events and
  sends the address on `MarkChanges`. A `missed` event or a reconnect
  re-reads the snapshot.
- Writes use a new `hc.Client.Label(ctx, terminal, map[string]string)`
  (`terminal.label`): only the keys that change, never the whole map.
  - `MarkSeen` writes `herdr.seen=<min(turn, Turn)>`, and only when that is
    above the cached value or the label is missing.
  - `SetPinned(false)` writes `herdr.pinned=` (removes it).
- `StartAgent` and the phone shell pass `Labels{"herdr.seen":"0"}` in
  `RunParams` ("A terminal a client starts gets `herdr.seen=0`").
- Remote terminals (`mac/t7`) are not panes for sheepdog (`isLocal`) and get
  no marks.

**herdr-tui and mock (`internal/attention.Local`).**

- `herdr.Client` decodes `completion_seq` (nullable) into a new
  `AgentInfo.CompletionSeq *uint64`. `Turn` is the highest `completion_seq`
  seen for the terminal, kept in the store because the field is null while
  the agent works.
- Store file: `<data_dir>/agent_marks.json`, mode 0600, written atomically
  the way `state.json` is:

  ```json
  {"version": 1,
   "terminals": {
     "term_65bb68107f52a2": {"pane_id": "w5:p1", "turn": 7, "seen": 6,
                              "seen_at": "2026-11-03T10:00:00Z", "pinned": true}
   }}
  ```

- Key: the TUI's `terminal_id` (`term_…`, unique). `w5:p1` is positional and
  is reused. A pane with no terminal id (an injected mock pane) uses
  `pane:<pane_id>`.
- The store refreshes from `ListAgents` after every Watcher transition and
  every periodic snapshot. `/api/state` reads only the cache.
- A `done→idle` with no `working` in between means someone focused the pane
  in a TUI client. The store records `seen = turn` and emits a change. That
  is how "seen in the TUI" reaches the phone.
- **Counter reset.** If herdr restarts, its counters start again. When a
  terminal's observed `completion_seq` falls below the stored `turn`, the
  store drops `turn` and `seen` for it (D4 takes over).
- **Pruning.** `Closed` removes the entry. After the first successful
  snapshot following a daemon start, entries whose terminal id is not in
  the snapshot are removed.
- On hc the same store holds only the terminals whose label is missing.
  The first phone "seen" writes the label, and the store entry is dropped.

**What sheepdog cannot do on herdr-tui (documented, not hidden).** A phone
"seen" does not clear the TUI's own Done badge, because herdr 0.9.5 has no
API for it other than focusing the pane. Pins exist only in sheepdog: the
TUI does not show them.

### 2.2 Attention (computed, per agent)

`attention.Classify(state herdr.State, m herdr.Marks) Attention`, pure:

| Condition (first match) | `group` | `unread` |
|---|---|---|
| state `blocked`, or `m.Error` | `needs_you` | false |
| state `working` | `working` | false |
| state `idle` or `done`, `m.Seen != nil` | `done` | `m.Turn > *m.Seen` |
| state `idle` or `done`, `m.Seen == nil` | `done` | `m.Word == done` |
| anything else (`unknown`) | `not_running` | false |

`pinned` is `m.Pinned` in every group. `muted` is set when the agent's
space folder is in the mute store (§2.3).

### 2.3 Space mutes

`<data_dir>/space_mutes.json`, mode 0600:

```json
{"version": 1,
 "spaces": {"/Users/u/code/auth": {"muted_at": "2026-11-03T10:00:00Z",
                                   "until": "2026-11-03T11:00:00Z", "via": "phone"}}}
```

- `until` is absent for "until I unmute". An expired entry is removed on
  load and by a timer, which logs `space.muted` with `muted: false, via:
  "expired"`.
- An agent maps to a space folder through the supervisor's existing space
  resolver (the `path` of `GET /api/spaces`): the checkout of a worktree
  space, else the folder of its first pane. An agent with no resolvable
  space is never muted.
- Two spaces in one folder share a mute. This is rare, because a new agent
  in a folder joins that folder's space.
- An entry whose folder no longer exists is removed at start.

## 3. API

All new routes are device-authenticated (phones with their ticket; the panel
over loopback), take an `Idempotency-Key`, and use the existing JSON error
shape and codes: `400` a bad body, `404` an agent or space the daemon does
not track, `502` herdr refused the write. Each call logs metadata only.

### 3.1 `/api/state`

Every agent gains `attention`. An embedding view type adds it, so
`state.Agent` and `state.json` do not change:

```json
{"pane_id": "w5:p1", "alias": "auth", "state": "done", "...": "...",
 "attention": {"group": "done", "unread": true, "pinned": false,
               "turn": 7, "seen": 6, "muted": false, "shared": true}}
```

`seen` is absent when there is no record. `shared` is true when the marks
are hc labels; the phone uses it for the "Shared with Herdr" footnote.
Top level, new:

```json
"attention_counts": {"needs_you": 1, "working": 2, "done_unread": 1},
"space_mutes": [{"path": "/Users/u/code/auth", "name": "auth",
                 "until": "2026-11-03T11:00:00Z"}]
```

### 3.2 Routes

| Route | Body | Answer |
|---|---|---|
| `POST /api/agents/{pane_id}/seen` | `{"turn": 7}` | `{"ok": true, "attention": {…}}` |
| `POST /api/agents/{pane_id}/seen` | `{"unread": true}` | `{"ok": true, "attention": {…}}` |
| `POST /api/agents/{pane_id}/pin` | `{"pinned": true}` | `{"ok": true, "attention": {…}}` |
| `POST /api/spaces/{id}/mute` | `{"muted": true, "until"?: "RFC 3339"}` | `{"ok": true, "space": {"id", "path", "muted": true, "until"?}}` |

- **`seen` with `turn`.** The turn the client showed. The daemon writes
  `min(turn, Marks.Turn)` only when that raises it (D7). A lower value is
  not an error: the answer just shows the current marks. `turn` above the
  current turn is clamped, not refused. A missing `turn` is `400`.
- **`seen` with `unread`.** Mark as unread (`Turn − 1`). At turn 0 it
  answers `{"ok": true}` with the marks unchanged.
- **`mute`.** The space `id` is resolved to its folder. `muted: false`
  removes the entry. `until` in the past is `400`.
- `GET /api/spaces` `Space` gains `"muted": bool, "until"?`.

### 3.3 Events (event log, `/events`)

| Type | Fields | Who reacts |
|---|---|---|
| `agent.attention` | `pane_id, group, unread, pinned, turn, seen?, via` (`phone`, `panel`, `herdr`, `tui_focus`) | phone and panel re-read `/api/state` (debounced, like `herdr.transition`). Also logged when an agent's state change moves its group or unread, so the event never lags `/api/state` (not for a pane's first transition, which only sets the baseline) |
| `space.muted` | `path, name, muted, until?, via` (`phone`, `expired`) | phone, panel, Sheepdog.app |

`via: "herdr"` covers every change that came in on `MarkChanges` (herdr-app,
another `hc` client, the other phone through a label). Events from the
daemon's own writes carry the client's `via`. The orchestrator gets **no**
transcript line for seen or pin, because they do not concern it. It gets a
line for a space mute ("the user muted auth's notifications from the
phone").

### 3.4 Versioning and back-compat

- `bot-ready` features: `attention` (the field, the seen and pin routes,
  `agent.attention`) and `space-mute` (the route, `space_mutes`,
  `space.muted`). Both are always listed by a daemon that has them. Add
  them to the features table in [ws-contract.md](../ws-contract.md), plus a
  new "Attention" section and a row in "Spaces".
- **New phone, old daemon.** No feature, no `attention` field. Kit groups by
  state only: blocked → Needs you, working → Working, idle and done → Done,
  with no unread dot and no pin or mute controls. Nothing is written.
- **Old phone, new daemon.** It ignores the new fields and never writes
  seen. Unread then clears only from another client: herdr-app, the TUI, or
  a newer phone.
- **Panel.** It checks whether `attention` is present in `/api/state`
  (D10). An older daemon gives the panel exactly what it has today.

## 4. Backend behaviour and the conformance suite

Every scenario below goes into `internal/herdr/conformance` and runs on both
drivers ([herdr-core-backend.md](herdr-core-backend.md)). The wording is
chosen so that neither backend skips a scenario. The `Driver` gains:

```go
Marker() herdr.Marker                       // hc: the backend; old: attention.Local over the fake, temp store
EndTurn(ctx, paneID string) error           // working → idle/done with one finished turn
OtherClientSeen(ctx, paneID string) error   // hc: herdr.seen=<turn> from another client; old: done → idle (TUI focus)
OtherClientPin(ctx, paneID string, on bool) error // hc: herdr.pinned from another client; old: Marker().SetPinned (TUI has no pin)
RestartDaemon(t) Driver                     // a fresh Marker over the same fake and the same store file
```

Fakes: `hctest` gains `terminal.label`, `turn` and `labels` events, and
`Terminal.Turn`/`Labels` in the snapshot. `herdrtest` gains
`completion_seq` on `agent.list`/`session.snapshot` (null while working)
and a `Focus(paneID)` helper that turns `done` into `idle`.

| Scenario | Assertion |
|---|---|
| a finished turn is unread until seen | `EndTurn` makes it `done`/unread. `MarkSeen(turn)` makes it read. Another `EndTurn` makes it unread again. |
| seen never goes down | After `MarkSeen(2)`, `MarkSeen(1)` leaves seen at 2 and does not write (hc: no `terminal.label` request). |
| seen for an older turn leaves newer work unread | At turn 2, `MarkSeen(1)` leaves it unread. `MarkSeen(5)` is clamped to 2. |
| mark unread | `MarkUnread` after seen makes it unread (seen = turn − 1). At turn 0 it changes nothing. |
| another client's seen clears unread | `OtherClientSeen` makes it read and fires `MarkChanges`. |
| no record falls back to the backend's word | A fresh agent with no seen record is unread exactly when the backend says `done`. |
| blocked and working are never unread | Blocked is `needs_you` and working is `working`, whatever the marks say. Unread comes back once it is idle again. |
| a blocked→idle with no work keeps the turn | No new unread after an approval that ended without work (hc: no `turn` event; TUI: `completion_seq` unchanged). |
| pin survives the agent, not the terminal | Pin, agent exits (`Released`), a different agent starts in the same shell: still pinned. Close the terminal: a new terminal in the same folder is not pinned. |
| a resumed session in the same terminal keeps its marks | `claude --resume` / `codex resume` in the pinned terminal: pinned, seen kept. |
| marks survive a daemon restart | `RestartDaemon`: same seen and pinned. |
| another client's pin shows up | `OtherClientPin` changes `AllMarks` and fires `MarkChanges`. |
| sheepdog-started agents start seen | An agent from `StartAgent` has `seen == 0`, so it is not unread before its first turn. |

Backend-only unit tests (they test mechanics, not behaviour):

- hc: label parsing (`herdr.seen=abc` counts as missing); `error` maps to
  `needs_you`; label writes send only changed keys; resubscribe after
  `missed`.
- TUI: counter reset drops seen; pruning after the first snapshot;
  `term_…` keys; atomic file writes; mode 0600.

## 5. Order (shared by Go, Kit and the panel)

1. Groups: `needs_you`, `working`, `done`, then `not_running` (folded, as
   today's "Not running (N)").
2. Within a group: pinned first.
3. Within `done`: unread before read.
4. Then by spoken name, case-insensitive, as today. There is no time sort,
   so rows never jump while you look at them.

`ios/Tests/Fixtures/agent-attention.json` holds cases of `{state, marks} →
{group, unread}` and lists `→` the expected order. Go
(`internal/attention`), `SheepdogKitTests` and `node --test`
(`mods/sheepdog/model/agents.test.mjs`) all read it, following the
`timeline-classification.json` pattern.

## 6. iPhone app

The logic lives in SheepdogKit with unit tests. `SessionViewModel` only
forwards calls ([ios-state-architecture.md](ios-state-architecture.md)).

**Kit.**

- `Agent.attention: AgentAttention?` (Codable, mirrors §3.1).
  `StateSnapshot` gains `attentionCounts` and `spaceMutes`. Decode tests use
  the literal JSON from §3.
- `AgentGroups` replaces `Flock`'s sort. It has sections
  `needsYou/working/done/notRunning` and applies §5. It falls back to
  state-only grouping when `attention` is nil. `waitingCount` stays as it is
  (blocked only), so the badge does not change. Fixture tests run here.
- `SeenTracker` (pure) gets "agent X at turn T is visible: app active, window
  or expanded card on screen" and returns the writes to make: once per turn
  while visible, never for a turn already written, nothing while in the
  background.
- `AttentionOverlay` holds optimistic seen, pin and mute changes until
  `/api/state` shows them or 5 s pass. Two phones converge on the daemon's
  answer.
- `DaemonClient.markSeen(paneID:turn:)`, `markUnread(paneID:)`,
  `setPinned(paneID:_:)`, `setSpaceMute(spaceID:muted:until:)`.
- `AgentChanges`/event routing: `agent.attention` and `space.muted` trigger
  the debounced `/api/state` refetch.
- `DemoDaemon` serves attention fields: one unread done agent, one pinned
  agent, one muted space. The demo then shows the feature, and UI tests need
  no daemon.

**Views.**

- `FlockView` and `AllAgentsView`: section headers "Needs you", "Working",
  "Done". An empty section is not shown. With no agents, the existing
  sleeping mascot.
- Unread row: an accent dot before the name, semibold name, VoiceOver value
  "Unread".
- Pinned row: a pin glyph, VoiceOver value "Pinned".
- Swipe and context menu: Pin / Unpin, Mark as unread (only for a read done
  agent).
- Opening the agent window or expanding the card marks it seen (through
  `SeenTracker`).
- The agent strip keeps its order rule (blocked first) but adds the unread
  dot.
- Spaces (`SpacesView`, space detail): "Mute notifications" with "For 1
  hour", "Until tomorrow morning" and "Until I turn them back on"; "Unmute" when muted. A `bell.slash`
  glyph shows on the space and its agents' rows.
- Footer text in the mute sheet: "Sheepdog won't notify your phone or speak
  up about this space. Its agents still show here, and the voice still
  answers when you ask."
- On a TUI backend (`shared == false`), the agent details show "Seen and
  pinned on this computer only" once.
- Error state: a failed write rolls the overlay back and shows the usual
  inline error ("Couldn't pin auth. Try again."). Check Dynamic Type at
  accessibility sizes (no truncation) and dark mode.
- **Live Activity:** no change in content. Its "done" count stays as it is.
- **Sheepdog.app (macOS)** `NotificationService`: a Kit `NotificationGate`
  drops `pending.created` and `herdr.transition` notifications for an agent
  whose `attention.muted` is true in the cached state. Daemon events also
  carry `muted: true` (§7), so the gate does not depend on a fresh state
  read.

## 7. Space mute: what it silences

| Channel | Muted space | Unchanged |
|---|---|---|
| Voice announcements (block, finish) | `ceilingForbidsAll` is also true when the agent's space is muted. No orchestrator turn, logged `supervisor.push_quiet` with `reason: "space_muted"`. The worker event still reaches the orchestrator's window as context. | A kept promise and the finish of the user's own instruction (owed) still speak (D9). |
| APNs alerts (`approval`, `blocked`) | Not sent: `notify` follows the voice's decision, as today ([push.md](../push.md#when-a-notification-is-sent)). | Kept-promise pushes still go. |
| Live Activity | A new waiting agent in a muted space goes at priority 5, not 10, so it does not alert. | Rows and counts. |
| Sheepdog.app notifications | Dropped by `NotificationGate`. `pending.created` and `herdr.transition` events gain `muted: true` for such agents. | Voice-error notices. |
| Phone haptics for `pending.created` | Off for muted agents. | — |
| Lists, groups, unread dots, Agents badge, panel amber, question cards, answering by tap or voice, "what needs me" | — | All unchanged. |

With no space muted, the code path is byte-for-byte today's, so this needs
no eval run (§9).

## 8. herdr-app panel

The panel has no agent list (Q1). herdr-app's own sidebar already shows
labels-based done and pins on hc, from the same labels, so a second list
would only repeat it.

- `model/agents.js`: `attentionCounts(state)`. When the `attention` field is
  missing it falls back to counting by state.
- The status line reads "1 needs you · 2 working · 1 done". The rail badge
  and the amber stay as they are (needs you only, Q3).
- `net/live.js`: `agent.attention` and `space.muted` re-read `/api/state`.

## 9. Voice and orchestrator

No prompt file changes. Two code changes, both in the last PR because they
need eval evidence ([eval/README.md](../eval/README.md): status ordering and
wording changes have moved Safe before):

- `composeBriefing` (`internal/supervisor/briefing.go`), the code-built
  status answer: sort by §5 instead of `stateRank`. This decides which
  three agents are named first.
- `renderRoster` (`internal/brain/prompt.go`): the same order. **Arm B**
  also appends `; finished, not looked at yet` to an unread agent and
  `; notifications muted` to a muted one. Arm A is order only.
- Gate: tier 1 workday ×5 at `chatty`, plus ≥ 15 `workday-scale-ten` runs,
  per arm against main, with `bench northstar --vs`. Merge the best arm only
  without a Safe regression. If neither passes, drop it. PRs 1–4 do not
  depend on it.
- Not in scope: a voice command to pin or mute. It would be an orchestrator
  tool, which needs its own design and eval.

## 10. Edge cases

| Case | Behaviour |
|---|---|
| Agent exits, the shell stays (`Released`) | The row goes, the marks stay on the terminal. A new agent there inherits pin and seen (D1). |
| `claude --resume` / `codex resume` in the same terminal | Same terminal, so the marks are kept and the turn continues. |
| Resume in a new terminal | No marks (upstream rule). |
| Terminal closed | hc: labels end with it. TUI: entry removed on `Closed`. |
| herdr-app writes `herdr.seen` while the phone does | Raise-only writes of `min(client turn, turn)`. Last writer wins (labels.md). The cache follows `labels` events. |
| Two phones | Both write through the daemon. Each optimistic overlay settles on the `agent.attention` event. |
| Phone opens the window while the agent works | Seen = the last finished turn. The new turn will be unread when it ends unless the window is still open (`SeenTracker` writes again). |
| Phone in background with the window open | No writes (labels.md "really in front of someone"). |
| Daemon restart | hc: labels. TUI: `agent_marks.json`. |
| herdr-tui restart | New counters: a lower `completion_seq` drops seen. New terminal ids: old entries are pruned after the first snapshot. |
| hc core restart, kept terminal | The terminal and its labels survive (`restarted` event). |
| hc `error` | Needs you. Today's mapping to `done` stays for the Watcher. |
| Mute expires while the daemon is down | Removed on load. |
| Muted space closed or worktree removed | The entry stays until its folder no longer exists (checked at start). Reopening the folder is still muted, which is the intent. |
| `--herdr=mock`, `/api/inject/*`, `sheepdog demo` | `attention.Local` over the mock, so e2e and demo exercise the fallback. |

## 11. Work breakdown

Each PR merges on its own and keeps `make test`, `make test-web` and
`make vet lint` green. Every scenario name below is what QA looks for in
`go test -v` output.

### PR 1. `feat(herdr): seen and pinned marks on both backends`

`herdr.Marks`/`Marker`; `hc.Client.Label`; the hc marks cache and
subscription; `completion_seq` in the TUI client; `internal/attention`
(`Local`, `Classify`, order, file store); wiring in `internal/app` (hc
backend, else `Local`; mock uses `Local`); `herdr.seen=0` on spawn; fake
upgrades; conformance scenarios (§4); the shared fixture with its Go test.
No API yet.

QA:

- `go test -race ./internal/herdr/... ./internal/herdrcore/... ./internal/hc/... ./internal/attention/...`
  passes, and `-v` lists every §4 scenario under both
  `TestBackendConformance` runs (old and hc).
- A test asserts the store file is mode 0600 and that tests use `t.TempDir()`
  only.

### PR 2. `feat(api): agent attention and space mute`

`/api/state` `attention`, `attention_counts`, `space_mutes`; the seen, pin
and mute routes; `space_mutes.json`; `agent.attention` / `space.muted`
events; the mute ceiling in `announce.go` and `notify.go`; Live Activity
priority; `muted` on `pending.created` and `herdr.transition`; features
`attention` and `space-mute`; [ws-contract.md](../ws-contract.md) and
[push.md](../push.md) updates.

QA:

- Unit tests: route bodies and errors as in §3; the ceiling (a muted block
  gives no orchestrator turn and no push; a kept promise in a muted space
  still pushes and speaks; no mutes means the decisions are unchanged on the
  existing `announce_test.go` table); mute expiry timer.
- New `scripts/e2e/scenario.sh` scenarios:
  - `agent-unread`: inject an agent, working → done; `/api/state` shows
    `group: done, unread: true`.
  - `agent-seen`: POST seen with the turn; unread false, and an
    `agent.attention` event appears on `/events`.
  - `agent-pin`: pinned true, and it survives `daemon-down` / `daemon-up`.
  - `space-mute`: mute, then `approval`; no `push.sent` and no
    `voice.push` in the event log; the question card still appears in
    `/api/state` `questions`.
- `up.sh --herdr hc` (new): the daemon uses the private core
  (`SHEEPDOG_HERDR_BACKEND=hc`, the private `HC_SOCKET`). The scenario
  `label-sync` pins through the API and checks `hc ls --json` shows
  `herdr.pinned=1`. It then sets `herdr.seen` with
  `hc label` and checks `/api/state` follows within 2 s.
- A TUI e2e run needs a private `herdr --session` whose socket is provably
  outside the user's runtime directory. Until that is verified, the TUI
  path is covered by conformance and the mock. QA must not attach to the
  user's herdr.

### PR 3. `feat(ios): agents grouped by needs you, working and done, with unread, pins and space mute`

Kit types and logic (§6), views, `NotificationGate` in Sheepdog.app,
demo data.

QA:

- `SheepdogKitTests`: decode the §3 JSON; `AgentGroups` on the shared
  fixture and on the old-daemon fallback; `SeenTracker` (once per turn,
  none in the background); `AttentionOverlay` rollback and two-writer
  settle; `NotificationGate`.
- UI test `AgentAttentionUITests` (demo, `-sheepdogDemoSilent
  -sheepdogNoAnimations`, `PaneSwitch` helpers):
  - section headers in order;
  - the unread agent's VoiceOver value is "Unread", and it reads otherwise
    after opening its window and coming back;
  - Pin from the context menu moves the agent to the top of its section;
  - muting a space shows `bell.slash` on its agents.
- Run on the iPhone 18 Pro and iPad Air 11 (M4) simulators, then shut both
  down.
- Screenshots in light and dark mode and at an accessibility text size,
  with no truncated row.

### PR 4. `feat(panel): attention counts in the status line`

§8.

QA:

- `make test-panel`: `attentionCounts` on the shared fixture, the fallback
  without `attention`, and `live.js` refetch on both events.
- `herdr-app check mods/sheepdog`.
- e2e with `up.sh --herdr hc`: the counts change as an agent starts,
  finishes and blocks. Add this to
  [`e2e/checklists/panel.md`](../e2e/checklists/panel.md).

### PR 5. `feat(supervisor): status in attention order` (eval-gated)

§9. QA: unit tests for the new order in `composeBriefing` and
`renderRoster`. The eval report goes in `docs/dev/eval/` with its row in
the index, and the decision follows the gate in §9.

## 12. Decisions

KC decided these on 2026-11-03.

- **Q1. No agent list in the panel.** Only the counts, in the status line
  (§8). herdr-app's sidebar already shows done and pins.
- **Q2. Hearing the voice announce a finish does not count as seen.** Only a
  screen in front of someone does, so the agent stays unread under Done.
- **Q3. The Agents badge and the panel amber count needs you only.** Unread
  shows as dots.
- **Q4. Three mute durations:** 1 hour, until tomorrow morning (08:00 local)
  and until unmuted.
