# The factory in the app and in voice: integration design

Status: step 1 of 2, revision 10 (2026-11-25). Revision 4 was accepted
after four rounds of independent review; revisions 5 and 6 applied the
owner's answers and a fifth review; revisions 7 to 9 follow the board
reviews and D10; revision 10 settles the gaps the implementation split
found and orders the PRs (sections 10 and 11). This page is the
integration design only; the boards are Part 3 of the
[design canvas](canvas/README.md).

Words are the [glossary](../glossary.md)'s. "Owner" is the user. The
sample project is called `notes`.

## Decisions

| # | Decision | Owner |
|---|---|---|
| D1 | The owner sees three levels: **project** (headed by its coordinator), **task**, and the task's **worker or reviewer**. Ordinary agents stay as they are. | OK |
| D2 | A project is named by the last part of its folder's path, everywhere. The daemon computes it. On a tie the orchestrator asks which; a name never changes because another project appeared. | — |
| D3 | **The project head is a permanent contact.** As long as a project exists (running, stopped or finished), its head is in the app's list. It is where the owner asks questions and passes on decisions, as with the orchestrator today. The head is the **project**, not a terminal: coordinators are replaced, the project stays. In the orchestrator's roster, running and stopped projects are rows; a finished project is not listed but is still found by name. The agent table is left alone; the head and its tasks come from the engine. | asked for |
| D4 | In the roster the head is one row per project, with a unique alias and a spoken name that may be shared. A blocked coordinator's real row merges into it. One helper in code says what each tool does on it. | — |
| D5 | **Decisions and instructions** reach the coordinator through `Engine.Say`, which exists and has no caller: `prompt` on the project row, and a device route for a text box on the phone. | OK |
| D6 | **Questions have two layers, and neither touches the coordinator.** Status ("how is notes?", "what is T8 waiting for?") is answered from the board's facts, which carry each task's last decision and its why. Judgement ("why did you split T8?", "would a different design be better?") goes through the existing ask-an-agent tool, `investigate` with its default source: a fresh read-only agent in the project's folder, pointed at the project's log and board. **It answers from what the coordinator recorded, not from the coordinator's memory.** Asking a copy of the coordinator's own session is a described later step; the engine cannot do it today. | OK, with this condition |
| D7 | No separate reply mechanism. `say_reply` is dropped: after an instruction the owner hears a confirmation, sees asks as cards, and asks "what did it do, and why?" through D6. | OK |
| D8 | **The phone can do everything `/factory` can**: stop, resume, new goal, reopen, settings with models per role, and new project. Every route it needs is already open to a paired device, and every proof and read-back the daemon requires stays in code. | asked for |
| D9 | Per-worker narration stays off. A stuck worker is the engine's to handle and is not shown as a needs-you card. | OK |
| D10 | **The coordinator's window is live and the owner can type in it** (owner, 2026-11-25: "this one is a must"), so the owner can ask the coordinator a clarifying question before answering its ask. It is the live terminal with an input box, as for an ordinary agent, offered only while a coordinator runs. Typing there is the owner's own hands: it is never an ask's answer, which still needs a proved tap or a live voice answer. Worker and reviewer windows are not in the app's first version (section 4). **Later (wanted, not v1):** a "clarify" box on the ask card itself, with the coordinator's answer written onto the card and spoken (section 6.7). | asked for |
| D11 | **A finished project's settings stay editable on the phone**, with the same read-back ask as a running one: the owner sometimes gives a finished project a new goal and may change models or parallelism first. `Engine.ProposeSetting` already accepts a proposal on a stopped or finished project. | decided |
| D12 | **Another computer's project opens its board directly**, against that computer's connection, with no "Talk to <computer>" step. | decided |

## Questions that remain for the owner

1. **Records, not memory.** A judgement question is answered by a fresh
   read-only agent that reads the project's log, plan and code. Every
   coordinator act is in that log with its reason, so "why did you split
   T8?" has an answer. What it cannot give is what the coordinator was
   thinking and did not write down. Getting that means asking a copy of
   the coordinator's own session, which needs engine work first (6.6) and
   replays a long Opus session on your Claude quota for each question. Is
   the first version enough to start with?

Settled in this revision, as the review recommends: a question never
starts a stopped project; a new project on the phone picks its folder from
the folders sheepdog knows and does not type one; finished projects stay,
folded into the closed "Done" group. **A "remove" (or archive) for
projects follows later**: nothing else ever shrinks the list.

## 1. The problem, as it is on `main`

The owner's report: with factory mode on, every herdr-core agent shows up
on the phone, and the list is unusable.

What the code says:

- Since #751 (2026-11-22), a transition of a factory terminal that the
  quiet rule silences never writes a row to the agent table, and closes
  the row if one exists. That is every worker, reviewer, retro and plan
  reviewer in every state, and the coordinator in every state but
  `blocked`. Tests pin it, for a blocked worker too.
- v0.1.30 does not contain #751. v0.1.31, tagged today, does. The second
  review found the live daemon answering as v0.1.30, with agents that
  carry no labels.

So the flat list is most likely what v0.1.30 does, and updating the daemon
removes it. There is nothing to filter on `main`.

What `main` does **not** give the owner is the other half of the ask:

- **Which workers belong to which coordinator.** After #751 the phone
  shows nothing of a project except its asks and, while it is stuck, its
  coordinator. No project, no tasks, no workers.
- **The voice talks to the coordinator.** The orchestrator has no row for
  a project and no way to pass on an instruction.
- **Stop and resume on the phone.** The routes exist; the app does not
  use them.

That is what this design is for.

## 2. What exists today (read in code on `main`, 2026-11-25)

| Fact | Where |
|---|---|
| Every factory terminal carries labels `sheepdog.project`, `sheepdog.role`, `sheepdog.task`, `sheepdog.model`, `sheepdog.effort`, `sheepdog.run`. | `internal/factory/engine.go` |
| A factory-quiet transition returns before the agent row is written and closes an existing row. The only factory row that can be in the table is a **blocked coordinator**, and it is removed on its next transition. | `internal/supervisor/transitions.go` (`HandleTransition`), `internal/herdr/factory.go`, `internal/supervisor/factory_snapshot_test.go` |
| So the roster, `/api/state`, `/api/agents`, the Live Activity and the attention counts hold no worker. The orchestrator cannot resolve one, and no tool reaches one. | `internal/supervisor/brainhost.go` (`Agents`, `Resolve`) |
| The screen and window routes start from the agent table and answer "no such agent" otherwise. So no worker's window opens on the phone today, and the coordinator's only while it is blocked. | `internal/supervisor/window.go` (`Screen`) |
| The watcher still logs `herdr.transition` for every pane, and the Mac app turns working→done into a "finished" notification for any pane. | `internal/herdr/watcher.go`, `ios/Sources/SheepdogMac/NotificationService.swift`, `internal/app/mutelog.go` |
| A project's id is `proj-<8 hex>`. Its pseudo-pane is `factory:<id>`, spoken "`<id>` factory". An ask is spoken by its task id alone, or by the project id when it has no task. Voice stop and start speak the id. | `internal/factory/world/world.go`, `internal/supervisor/factory.go`, `names.go` |
| `/factory` shows a project by the last part of its folder's path, and falls back to the id. | `web/factory/overview.js` (`projectName`) |
| The engine knows each project's coordinator run and each task's live run. The graph sends a task's `terminal`; the project view does not send the coordinator's. The supervisor's `Factory` interface cannot ask for either. | `internal/factory/flow/records.go`, `view.go`, `internal/supervisor/factory.go` |
| The supervisor bridges the engine: open gated asks become question cards and spoken lines; a live voice answer or a paired-device tap mints the answer proof; override results are spoken and pushed. | `internal/supervisor/factory.go` |
| Voice has two factory commands, matched as bare phrases from live voice input only: "stop the factory" stops **every** running project, "start the factory" resumes **every** stopped one. They work with the orchestrator off. | `factoryControl`, `factoryVoiceFastPath` |
| With the orchestrator off, `to_supervisor` only reports status. | `internal/supervisor/tools.go` |
| `Engine.Say(project, text)` queues the owner's words as a coordinator wake (`owner_say`), writes a log line and emits a `say` change. It refuses a finished project only; on a stopped project the wake is kept and delivered after a resume. Its project argument is also tried as a folder. **Nothing calls it.** | `internal/factory/asks.go`, `engine.go` (`findProjectLocked`), `flow/project.go` |
| Wakes are delivered in order, at most 20 in one typed message, only when the coordinator run exists and is idle. | `internal/factory/wake.go` |
| The coordinator's prompt says: "when they ask a question or say wait, answer and do nothing else until they say go", and "quote the owner's words in each task's brief, never paraphrase them". | `internal/factory/coordinator.md` |
| The orchestrator's action tools, `prompt` included, already run only in a turn the owner's own words started. Its tools are `read_screen`, `run_cli`, `list_agents`, `recall`, `prompt`, `send_keys`, `interrupt`, `promise`, `note`, `investigate`: none reads a graph or a log. Two places resolve an agent and then act: the general one and the rename branch of `note`. | `internal/brain/tools.go` |
| Every coordinator act needs a `--why` and is one log line with `by` and a sequence number. `Changes()` drops a change when a subscriber's buffer is full. | `internal/factory/store.go`, `asks.go` |
| Paired devices can read projects, graph, log, settings and asks, answer asks, and call `POST /api/factory/projects/start` and `/stop`, which refuse a request with no paired-device identity. | `internal/web/server.go`, `internal/web/factory.go` |

## 3. What the owner sees (D1, D2)

```
computer
├── agents                      the interactive agents, from the agent table, as today
│   ├── auth-fix
│   └── api-docs
└── factory                     from the engine, not from the agent table
    └── project "notes"         goal, state, counts, open asks
        ├── coordinator         one terminal; the only one the voice talks to
        ├── T7  feat: search    stage: review
        │   └── reviewer        claude · round 2 of 2      (a line under its task)
        ├── T8  feat: tags      stage: working
        │   └── worker          codex · 31 min             (a line under its task)
        └── T9 … T14            no terminal now: on the project's board only
```

- A **project** is one unit on the phone, in the roster and in speech.
- The **coordinator** is the project's head.
- **Workers and reviewers** are shown only inside their project, under
  their task, as one line: role, model, how long. They are not addressed
  by voice. They get no window in the app's first version.
- A stopped or finished project has no terminals but is still a project,
  and its head is still there (3.1).

### 3.1 The head is a permanent contact (D3)

The owner's model is the orchestrator: one contact that answers questions
and takes decisions, always there. A project gets the same.

**What is natural from the engine's side.** The engine runs exactly one
coordinator per project. The coordinator owns the plan, holds the context,
and is the only role that takes the owner's words (`Say`, delivered as a
wake). But a coordinator *terminal* is not permanent: the engine gives the
coordinator a fresh session at idle moments, replaces it after a failure,
and stops it with the project. What is permanent is the project and its
stored state. So the contact is the **project head**, backed by whichever
coordinator run is current, and by the stored plan and log when none is.
A row tied to the coordinator's terminal would vanish on every rotation.

**What the head does in each state:**

| Project | A decision or instruction | A status question | A judgement question |
|---|---|---|---|
| Running | `Say`: delivered when the coordinator is idle | the board's facts | a fresh read-only agent reads the log, the board and the code (6.5) |
| Coordinator being replaced | `Say`: held, delivered to the next one | the same | the same |
| Stopped | `Say`: held until Resume; the owner is told so | the same | the same. **A question never starts a project**: starting spends quota and launches workers; only Resume or a new goal does. |
| Finished | refused: "notes is finished. Give it a new goal." A new goal is the decision that reopens it (section 8) | the same | the same |

Questions work alike in every state and for every coordinator CLI, because
they read stored state and never need a terminal.

**The list only grows.** Nothing removes a project, and the engine lists
every project folder it has. So:

- in the orchestrator's roster, running and stopped projects are rows; a
  finished project is not listed (it would be prompt text on every turn)
  but still resolves by name, so "ask the notes factory why …" and "give
  notes a new goal" still find it;
- in the app, finished projects fold into the "Done" group, closed by
  default;
- a remove or archive follows later.

In the app the head is always in the Agents list. The sections are, in
order: **Needs you, Working, Stopped, Done.**

- Running: under Working. A head whose coordinator is stuck on a dialog
  is under Needs you. An open ask is its own card under Needs you; the
  head stays under Working and shows "1 waiting on you", so it never jumps
  sections beside its own card.
- Stopped: under **Stopped**, with its Resume button. The section is shown
  only when a project is stopped. It exists because Done is closed by
  default and would hide Resume, and Working would be untrue.
- Finished: in the closed Done group.

The head says only what the daemon sends: the name, the goal, the state
word and the counts. It does not say who stopped a project or when one
finished; the project view carries neither.

### 3.2 The name (D2)

**The name.** The project view gets a `name`: the last part of the
folder's path, the id when there is no folder. One project's own state is
enough to compute it. It is the rule `/factory` already uses in the page;
it moves into the daemon so every surface agrees.

- Spoken and shown: "notes", "the notes factory". The id is never spoken.
- **A tie has one rule: the orchestrator asks which, by goal.** For that
  to hold, a project row's **alias is unique**: it always carries the
  id's short form (for example `notes-factory-1a2b`), even for the first
  project with that name, and only the **spoken name** ("the notes
  factory") is shared. The name matcher's first stage returns the first row whose alias
  matches, without counting; only its spoken-name stage returns nothing on
  a tie. With one alias for two projects, "the notes factory" would go to
  whichever came first. A unit test on the resolver pins this: with two
  projects named notes, "the notes factory" resolves to neither, including
  the one started first.
- **A listed project wins.** A finished project is found by name but not
  listed in the roster (3.1). It is matched only when no listed row
  matches, so a finished `notes` beside a running `notes` is not a tie the
  roster cannot show. The resolver test covers this case too.
- The app shows the folder under the name, so two cards with one name can
  be told apart. No project is renamed because a second one appeared.
- Ask lines say the project's name and the task ("notes, T1: …"). Today
  they say the task id alone, and two projects both have a T1.
- The other places that speak the id change too: the pseudo-pane's spoken
  name, voice stop and start, push titles (today "proj-1a2b3c4d factory"),
  and **a pane labelled as a project's coordinator**. While blocked, the
  coordinator's real row is announced by its terminal name (`coord-<hex>`),
  because the spoken name is read from the row first; it must be announced
  as "the notes factory".
- Names are unique per computer at best. Where the app lists projects of
  several computers, it shows the computer's label, as it does for agents,
  and a tap opens that project's board directly (D12).

## 4. Where the grouping comes from (D3)

**The decision.** Two ways were on the table:

- A. Leave the agent table as it is. The app builds the project, its tasks
  and their runs from the engine's views. The screen reads learn a second
  lookup: a project's current coordinator pane is readable without a table row.
- B. Put factory terminals back into the agent table and filter every
  reader of it (there are 21 `List()` call sites).

**A is chosen.** It is smaller, it cannot leak a worker into a reader
nobody listed, and it keeps the tests that say a quiet worker is never
stored. Revisions 1 and 2 of this page described B without knowing the
rows were gone.

**What the app reads:**

| What is shown | From | New in the daemon |
|---|---|---|
| The heads on Agents, kept fresh | **a small `factory` list in `GET /api/state`**, keys pinned below | the list |
| A project's page: goal and the rest of the project view | `GET /api/factory/projects` | `name`; the coordinator's pane; the open alert's cause and fix |
| A project's tasks, each with stage, waiting reason, steps, model and its live run | `GET /api/factory/projects/{id}/graph` (`TaskView`, `terminal`) | the run's role, CLI and start time and the last decision (the new `TaskView` keys below; the selection of 5.3) |
| Open asks | the question cards, as today (`/api/state` `questions`, pane `factory:<id>`) | the `factory` object on a `fask-` card (below) |
| A stuck coordinator's dialog | its real row in `agents` while blocked, as today; the new app draws that card on its project, matched by the coordinator's pane | nothing |
| The coordinator's screen | the screen reads, with the second lookup below | the lookup |

**The `factory` list in `/api/state`** (this settles what earlier
revisions left to the boards). One entry per project, with these keys and
no others: `id`, `name`, `goal`, `state` (the project view's word:
running, stopped or finished), `counts` (the project view's map),
`folder`, `coordinator_pane` (left out when no coordinator runs) and
`alert` (`{kind, cause, fix}`, left out when none is open). A project can
hold several alerts (`flow.Project.Alerts` is a map); the entry shows one:
the stopping alert (`flow.StoppingAlert`), else one of kind
`agents_down` (its keys vary: `agents_down`, or `agents_down@j6` and
`@j10`, built in `internal/factory/alerts.go` and `engine.go`), else the
first by key. The same `alert` goes on the project view. The keys are
written into [ws-contract.md](../ws-contract.md) with the code.

- It is built from the same in-memory read the screen lookup below needs,
  **not** from `Projects()`, which loads every project's state file on
  each call. `/api/state` is refetched on every event burst.
- So the app never polls for heads: they arrive with the state it already
  refetches. The project's page and its graph are read when opened.
- **What makes the app refetch.** The app re-reads `/api/state` on any
  event whose type starts with `herdr.`, `supervisor.`, `investigation.`,
  `promise.`, `update.` or `web.inject`, on `daemon.start`
  (`ios/Sources/Sheepdog/SessionViewModel.swift`), and on the two types in
  `AttentionEvents.refreshesState`, `agent.attention` and `space.muted`
  (`ios/Sources/SheepdogKit/AttentionActions.swift`). The engine logs
  none of these when a project starts, stops, finishes, replaces its
  coordinator, moves a task or opens or clears an alert. So the engine
  logs one new event, `factory.changed` `{project}`, and the app adds
  that one type to `refreshesState`. **It fires on a change, not on a
  write**: the engine writes state on a timer while a plan review or any
  run is live, and an event per write would make every phone and Mac
  refetch each pass and push real events out of the event log's
  2,000-event ring. It is logged (a) when the project's log gets a line
  (`commitLocked`, the one place `Seq` moves: every decision, stage move,
  ask and alert), and (b) after a save with no log line, when the
  project's `factory` entry differs from the last one announced (a
  coordinator pane that changes without a log line). A bookkeeping save
  logs nothing. There is no rate limit: log lines come at decision speed.
  It is logged by the engine (`logEvent`), not sent on `Changes()`,
  whose buffer drops when full and must keep carrying asks.
  It is not a notification: only `pending.created` and `herdr.transition`
  make one (`internal/app/mutelog.go`, the Mac's notification service),
  and nothing pushes it.
- Installed app builds ignore the unknown key.

**What a row shows, and what it does not:**

- **A run line is one format on phone and iPad:** role, CLI, how long:
  "worker · codex · 31 min". The role and the start time come from the
  run record, which has both (`Role`, `StartedAt`). **Model and effort are
  not on rows**; they are in the task view, where `TaskView.model` already
  serves the model.
- **No "idle" or "working" for the coordinator.** The head says running or
  not running; the live window shows the rest. The engine's view sends
  no such word today, and none is added.
- **The Coordinator button shows only when a coordinator runs**, that is
  when the entry has a coordinator pane. A stopped or finished project has
  no terminal and the screen read accepts only a pane the engine names, so
  there is no window to open. If the coordinator goes while its window is
  open, the window shows why in place of the terminal, with no key bar
  and no box (board `FactoryWindowStates`, wording
  [slice14-wording.md](canvas/slice14-wording.md)): replaced or not
  running, the heading `app.coord.off` ("The notes coordinator is not
  running"), the open alert's cause and fix, and `app.win.off.body`;
  stopped, "notes is stopped" with Resume on the project's page; finished,
  "notes is finished" with reopen on the project's page.
- **The box while the coordinator is blocked on its own dialog.** The
  window's box posts through `WindowPrompt`, which refuses a blocked pane
  ("…is waiting at a prompt; answer it first",
  `internal/supervisor/window.go`). The app shows `app.win.blocked` under
  the box and keeps the text; the keys still reach the dialog. While a
  factory ask is open the coordinator is idle, not blocked, and the box
  works.
- **Why the coordinator is not running.** The engine already records an
  alert with a cause and a fix (`flow.Alert`; for example cause "claude
  coordinator is signed out", fix "sign in to claude at this Mac"; also no
  quota pool, a full disk, a consent prompt). The open alert's cause and
  fix go on the project view and are shown as the daemon wrote them. "Being
  replaced" has no alert and needs no fix: it keeps the sentence of 6.1.
- **Last decision and why in the task view** come from the same selection
  as the board text (5.3), exposed on `TaskView`, so the phone and the
  orchestrator share one piece of code.
- **The new `TaskView` keys.** `TaskView` has `model` and `terminal` but
  no CLI. It gains `run_role`, `run_cli` and `run_started_at` (from the
  run record, left out when no run is live) and `last_actor_decision`
  `{by, decision, why, at}`, where `at` is the log line's time, which the
  task view shows ("Today, 11:05"). The existing `last_rule` and
  `last_decision` stay for `/factory`. Role, CLI and start time are on
  the run record (`flow.RunRecord`, `internal/factory/flow/runs.go`).
- **The kind and task on a factory ask card.** A `fask-` question card
  carries only the question, the options and the command today. It
  gains an optional `factory` object, `{project, task, kind}`. It
  is a new optional field on `state.PendingAction`, set by the bridge
  from the `flow.Ask` it already reads (`syncFactoryAsks` in
  `internal/supervisor/factory.go`), so `PendingView`, which embeds the
  action, carries it with no second lookup; it is left out for every other
  card. `kind` is the ask's own word; `flow.Ask` has nine. The boards
  draw four (`delete`, `release`, `rule` as the read-back,
  `override`); any other kind is the plain question card with its
  "notes · T10" line. That is smaller than having the app join
  `GET /api/factory/asks`. The read-back's new and current values are in
  the question text already (`flow.RuleQuestion`).
- **The blocked coordinator is shown once.** Its real row stays in
  `agents` for older app builds. The new app leaves out of the ordinary
  agents any row whose pane is a `factory` entry's `coordinator_pane`,
  draws its dialog on that head, and counts it once in Needs you and the
  badge. **A surface leaves the row out only where it draws the head**:
  the iPad's sidebar and All agents list `Flock.running` and draw no head
  until PR 13, so they keep the row until then. The test runs on the iPad
  as well as the iPhone.
- **Badge and Live Activity: unchanged in the first version.** The Agents
  badge counts question cards and blocked agents as today (an open ask and
  a stuck coordinator are already among them). Projects add nothing to the
  Live Activity.

**Reading the coordinator's terminal.** Three reads start from the agent
table today: `Screen`, `ScreenANSI`, and the `/ws` screen subscription,
which calls those two. One helper in place of the table lookup in the
first two covers all three. It accepts a pane that is not in the table
when the engine names it as a project's current coordinator, and no other
run: worker and reviewer windows are not in the first version, and the
orchestrator reads the board as text (5.2).

- **What the read returns.** For a table row, the alias, spoken name,
  kind, state and state-since come from the row. For the coordinator's
  pane they come from the engine: the project's name and "coordinator" as
  the name, the run's CLI as the kind, and the run's state as the engine
  has it, or empty.
- **Cost.** The question "is this pane a project's current coordinator,
  and which" is asked on every screen poll. It must not load every project's
  state file each time, as `Projects()` does. The supervisor's `Factory`
  interface gets one read for it, answered from what the engine already
  holds in memory or from a small cache rebuilt on an engine change.
- **Workers and reviewers stay out.** Every route that reads, types
  into, attaches to, interrupts, closes or renames a pane keeps refusing a
  worker or reviewer pane, which has no row.
- **The coordinator's window is the exception (D10): live, with input.**
  The live attach and the input route accept the project's current
  coordinator pane, found through the same engine lookup (the project
  entry's coordinator pane), and nothing else the engine names. Close,
  rename and interrupt stay refused for it: the engine owns its life.
  **They are refused in code, by one rail, not by the missing row.** A
  blocked coordinator has a row in `state.Store` (`IsFactoryQuiet` is
  false for a coordinator going to `blocked`), and on `main` nothing else
  stops either route: the user routes (`UserRename`, `UserInterrupt`,
  `UserClose` in `useractions.go`) check only the store, and so do the
  orchestrator's (`brainHost.Rename`, `brainHost.Interrupt`,
  `closeAgent`). One function refuses a pane labelled `sheepdog.project`
  or named by the engine, blocked or not. It is called where the two
  routes meet: `s.rename` (`names.go`), `brainHost.Interrupt` (both
  routes interrupt through it), `closeAgent` (`close.go`) and
  `UserClose`. Tests pin a blocked coordinator through the user route and
  through the orchestrator route. The 5.2 helper still answers for a
  project row; this rail is what holds for the coordinator's own pane.
- **What still interrupts.** The window leaves out the Stop button and the
  Interrupt row of More keys. Esc and the Ctrl latch stay, because the
  coordinator's own dialogs need them, so **Esc, or Ctrl then c, still
  interrupts the coordinator's turn**, as on the Mac. That is the owner's
  own hands and not a rail: refusing bytes on `/term` would mean parsing
  the stream. What protects the factory is in code: a gated ask needs an
  answer proof whoever answers, and only a coordinator opens one. PR 2's
  done-when proves the engine recovers (section 10).
  Typed text is the owner's own hands, as in an ordinary agent's window;
  it is not logged as an owner wake and can never answer an ask.
- **In the app's first version only the coordinator's window is offered.**
  The owner steers at project level, and the task list with its run line
  already answers "which workers belong to which coordinator". `/factory`
  offers no worker window either.

**Not shown: a stuck worker.** It has no row and no card on `main`, and
this design does not add one. The engine owns a stuck run: it ends a run
that is blocked too long, and a consent prompt raises a stopping alert.
Bringing the engine's alerts to push and voice is later work in
[engine-v2/design.md](../engine-v2/design.md) (12.2).

**One loose end from the flat era.** A factory-quiet transition gets the
marker a muted space already gets in the event log, so the Mac app makes
no "finished" notification for a worker.

## 5. The orchestrator: one row per project (D4, D6)

### 5.1 The row

`Resolve` returns one synthetic `AgentInfo` per project, and `Agents()`
lists those of running and stopped projects (3.1):
pane `factory:<id>`, alias and spoken name from section 3, state and task
line from the project view and the open asks. It exists whether or not any
terminal does, so a stopped project can still be told something. It goes
through the same name matcher as every agent, so there is no second
matcher.

What the model reads for it, in the roster's existing shape:

```
notes factory: running. Goal: "A notes page with search".
3 working, 1 waiting on you, 9 of 14 done. Coordinator idle.
Open ask: "Delete the old notes table in the migration?"
```

With no factory project, the roster and the standing context are
byte-identical to today's. A golden test holds that.

**The blocked coordinator merges into the row.** While it is blocked, the
coordinator has a real row with its own alias and a pending prompt. Then:

- the real row is left out of `Agents()`;
- its pending prompt is shown on the project row;
- its alias resolves to the project row, and it is spoken as the project
  (section 3);
- `send_keys` maps `factory:<id>` to the coordinator's pane, which the
  engine supplies.

**A dialog option that asks for free text** (Claude's "No, and tell Claude
what to do differently") is answered as the key it is: "No". Nothing on
the head, and nothing the orchestrator does, types into the coordinator's
terminal. What to do instead goes in the project's text box (6.2) or is
said by voice, and the coordinator gets it as an owner wake when it is
next free; or the owner types it in the coordinator's live window (D10,
section 4), which is the owner's own hands and not a wake.
The card says so in one line. A factory ask has no free-text answer
either: an ask is a question with a fixed list of options (`flow.Ask`),
and an answer must be one of them.

**Only the coordinator's own dialog is ever the row's pending prompt.** A
gated ask is text on the row, as in the example above, never `Pending`. The
proof rail does not depend on this (an ask is answered only through
`MintProof`), but it keeps `send_keys` from ever being offered an ask's
options.

`brain.AgentInfo` has no labels, so this mapping lives in the host
(`brainHost`), not in the model's view.

### 5.2 What each tool does on a project row

One helper decides, and **both** places that resolve an agent and then act
call it: the general one, and the rename branch of `note`, which resolves
by itself and returns early.

| Tool on a project row | What happens |
|---|---|
| `prompt` | carried out as `Say` (section 6) |
| `read_screen` | text built in code from the graph (5.3). `source: session` is refused. There is no way to read the coordinator's terminal through the orchestrator in the first version: a stuck dialog's question and options are already on the row as its pending prompt, and questions are answered from the board. A third `source` value would change the tool's declaration and need its own eval. |
| `send_keys` | while the coordinator is blocked on a dialog: to its terminal, under the one rail, as today. **When it is not blocked: refused, nothing sent.** The orchestrator's own check lets a call through when a row has no pending prompt and leaves it to the host; the host must refuse here, because there is no row and no prompt to revive. |
| `investigate` | the default source (`cli`): a fresh read-only agent in the project's folder, with the board text itself and the path of the project's log in its question (6.5). `source: worker` is refused in the first version: "the notes coordinator's own session cannot be asked yet; ask without source". `investigate` starts from an agent row today (workspace, alias, session, remote check), so it needs a target that is not a row. |
| `interrupt`, closing, renaming (`note` with a name), `run_cli`, `promise` | refused in code (`brain.rail_blocked`): "That works on an agent, not on a factory project." Stopping is "stop the factory", or Stop on the phone. |

Without the helper these tools would reach host methods with a pane that
is not a terminal. There is no rule for worker terminals, because a worker
never resolves.

### 5.3 `read_screen` on a project: the board as text

The orchestrator has no tool that reads a graph or a log, and the
coordinator's screen rarely says why a task is slow. So `read_screen` on a
project row returns text built in code from the graph: the project's state
and counts, the last few project-level lines the coordinator wrote, each
task with its stage and its waiting reason, and the open asks. Dropped and
closed tasks are shown too: they are where "why" is asked most. No new tool. For a stopped project it is the only thing there
is to read.

```
notes: running. 3 working, 1 waiting on you, 9 of 14 done.
Coordinator, plan amended: "T11 added: sync was missing from the goal".
T6 feat: storage — split; why: "two changes over 400 lines".
T7 feat: search — review, round 2 of 2.
T8 feat: tags — working, codex, 31 min. Last: sent back; why: "the tag index must be case-insensitive".
T9 feat: export to PDF — dropped; why: "owner said to skip the export".
T10 feat: import — waiting on you: "Delete the old notes table in the migration?"
T11 feat: sync — waiting on others: after T8.
```

Each task line carries its **last decision and its why**: the last log
line for that task whose `by` is the coordinator or the owner, with its
decision and why. This is a new, small selection over the log. The
existing `LastDecision` field is not it: that keeps only lines of the
engine's own task rules ("start a reviewer"), and a coordinator's act is
logged under its verb (`task drop`, `task send-back`, `recap decide`). A
plan-level act such as `plan amend` carries no task, so it appears under
the header, not on a task's line; a split decided at a recap is on the
parent task's line. Many "why" questions are then answered from this text with no model on the factory
side.

The text goes back inside the same untrusted-text wrapper every screen
read gets (`wrapWorker`): task titles and ask questions are written by
models.

This is what makes D6 honest: a question gets the board's facts.

## 6. How the owner's words reach the coordinator (D5, D7)

### 6.1 The path

```
owner            voice            orchestrator            engine              coordinator
  │ "tell the notes │                  │                     │                     │
  │  factory to skip│                  │                     │                     │
  │  the export     │                  │                     │                     │
  │  task"          │                  │                     │                     │
  ├────────────────►│ to_supervisor    │                     │                     │
  │                 ├─────────────────►│ prompt(notes        │                     │
  │                 │                  │  factory, "skip the │                     │
  │                 │                  │  export task")      │                     │
  │                 │                  │ existing rule: only │                     │
  │                 │                  │ in a turn the owner │                     │
  │                 │                  │ started             │                     │
  │                 │                  ├────────────────────►│ Say(project, text,  │
  │                 │                  │                     │     heard words)    │
  │                 │                  │                     │ wake owner_say held │
  │                 │                  │                     │ log: owner said …   │
  │  "Told the      │◄─────────────────┤◄────────────────────┤                     │
  │◄─notes factory."│                  │                     │ coordinator idle:   │
  │                 │                  │                     │ owner wakes first   │
  │                 │                  │                     ├────────────────────►│ acts: factory task
  │                 │                  │                     │◄────────────────────┤ drop T9 --why "…"
  │                 │                  │                     │ log: dropped T9 …   │
  │  later: "what happened to the export task?"              │                     │
  │◄─ read_screen(notes factory): the board as text ─────────┤                     │
```

- **Why `Say` and not typed keys.** `Say` is one line in the project log,
  is delivered when the coordinator is idle and not into the middle of its
  turn, is held while the project is stopped or the coordinator is being
  replaced, and reaches the next coordinator after a rotation. Typed keys
  are none of these, and off a blocked state the coordinator has no row to
  type into anyway.
- **No new rail.** `prompt` is an action tool, and action tools already
  run only in a turn the owner's own words started. A turn the daemon
  started cannot `Say`. Typed chat may `Say`: it gates nothing.
- **Whose words.** The coordinator is told to quote the owner and never
  paraphrase. So `Say` gets one more parameter, the words the daemon
  recorded (the way a voice answer to an ask already carries what was
  heard), and the wake says which is which:
  `- project: owner said, relayed by the orchestrator: <text>. Heard: "<words>".`
  The coordinator quotes the heard words.
- **Order.** Owner wakes are delivered **first, together**, before held
  task wakes. The owner does not wait behind up to 20 wakes, and three
  instructions in a row cost one coordinator turn, not three.
- **No trailer.** The "end with three short lines" trailer of `prompt` is
  not added.
- **One line, capped.** The wake message writes one wake per line, so the
  text and the heard words are each made one line and capped in length
  (6.2), on this path as on the device route.
- **Which project.** One project: no name needed. Several: the owner names
  it. A tie or no name: the orchestrator asks which.

What is said at once, by case:

| Case | Spoken now | Then |
|---|---|---|
| Running | "Told the notes factory." | the coordinator acts; an ask arrives as a card |
| Stopped | "notes is stopped. It gets this when you resume it." | the wake is held |
| Coordinator not running (being replaced, signed out) | "The notes coordinator is not running. It gets this when it is back." | the wake is held |
| Finished | "notes is finished. Give it a new goal." | `Say` refuses; nothing is queued |
| No such project | "No factory project called that. You have notes and recipes." | nothing |

### 6.2 From the phone, and with the orchestrator off

With the orchestrator off, the voice can still answer asks and stop or
start the factory; it cannot `Say`. So one device route is added for a
text box on the project head: `POST /api/factory/projects/{id}/say`.

- **Access:** a paired device, with the identity check that stop and start
  use. No answer proof: it gates nothing and answers no ask.
- **The id only.** `Say` also tries its argument as a folder. The route
  takes a project id and checks it the way the read routes do: no path
  characters, an existing project.
- **Wording.** This text is the owner's own typing, so the wake has no
  relay and no "heard": `- project: owner said (typed): "<text>".`
- **One line, capped.** The text goes into a typed message with one wake
  per line. Line breaks become spaces, and the text is capped (proposed:
  1,000 characters) and refused when longer. The same holds for the voice
  path.
- **One confirmation, by voice and on the phone.** The box is labelled
  "Tell the notes factory", and on success the app shows "Told the notes
  factory.", the sentence the voice says (6.1). The head is the project,
  not a terminal, so neither says "coordinator" here. The other sentences
  of 6.1's table are shown as written there: stopped ("notes is stopped.
  It gets this when you resume it.") and coordinator not running ("The
  notes coordinator is not running. It gets this when it is back."). In
  both the text was accepted and is held.
- **Refusals the app shows**, each as a sentence under the box, with the
  text kept so it is not lost:

  | Refusal | Shown |
  |---|---|
  | Finished (`closed`) | "notes is finished. Give it a new goal." |
  | Empty text | the send button is off; nothing is sent |
  | Too long | a counter appears near the cap; over it: "Too long. Keep it under 1,000 characters." |
  | Project not found | "This project is gone." and the page closes to Agents |
  | No paired-device identity | "Not sent: Work Mac no longer knows this phone. Pair this phone again in Settings ▸ Work Mac, then send again." The same pattern as Stop on the boards ("Not stopped: … then press Stop again."): it names the computer and says where to pair. |

- **Strings this page adds.** The page keeps no wording list of its own;
  the app's strings go into the wording table
  ([slice14-wording.md](canvas/slice14-wording.md)) with the board export.
  These four are new here: `1 waiting on you` (on a head under Working);
  `Not sent: <computer> no longer knows this phone. Pair this phone again
  in Settings ▸ <computer>, then send again.`; `Too long. Keep it under
  1,000 characters.`; `This project is gone.`
- **Where a question goes.** The box sends instructions. One sentence
  under it says so: "A question? Ask by voice, or ask Sheepdog in
  Conversation." A question typed into the box is a `Say`, which changes
  nothing and returns no answer (6.5).

### 6.3 Why `Say` cannot pass a gate

```
owner: "tell the notes factory to tag the release"
   │
   ▼
Say(notes, "tag the release")            answers nothing, gates nothing
   │
   ▼
coordinator: factory ask --kind release "Tag 0.2.0 now?" --option … --recommend …
   │                                     a coordinator cannot answer a gated kind
   ▼
engine: open gated ask ──► question card on the phone, spoken with its options
   │
   ▼
owner: taps an option on a paired device, or says it in a live voice session
   │
   ▼
daemon: MintProof (device tap or live voice) ──► Answer(ask, option, proof)
   │                                     typed chat, the orchestrator and agents cannot mint one
   ▼
engine acts
```

The instruction and the approval are two steps, and only the second
carries authority. That is the existing rule of
[safety.md](../../safety.md#factory). No rail moves into a prompt.

**To be exact about what the engine enforces.** An answer to any gated ask
needs the owner's proof; that holds for every kind. What happens *after*
the answer differs:

| Ask kind | Who acts on the answer | What holds the coordinator back before it |
|---|---|---|
| `gate`, `override`, `rule` | the engine: it moves the task, merges, or changes the setting only on a proved answer | code |
| `release`, `paid-key`, `delete`, `install`, `reset` | nobody in the engine: these exist as ask kinds only (read in `flow/asks.go` and their uses) | the coordinator's prompt: "Never spend money, publish, tag a release or delete data on the owner's behalf" |

That is true today and not this design's doing. What this design adds is a
channel that arrives labelled "owner said" through a model: with a release
ask open, a relayed "yes, go ahead" could be read as the answer. Three
things close most of it: the heard words attached by code (6.1), the
prompt sentence of 6.4, and one eval scenario: an ask is open and the
owner answers in free words the fast path does not match; the
orchestrator must point to the card or the spoken option, and must not
`prompt` the project with the answer.

### 6.4 The coordinator's prompt must change

The prompt's "when they ask a question or say wait, answer and do nothing
else until they say go" was written for the owner typing in the terminal.
Through `Say` it can park a project silently: the coordinator answers in
prose that nobody hears, then waits for a "go" the owner does not know to
send.

So `coordinator.md` gets one sentence: an "owner said" wake is an
instruction to act on with the usual commands, not a conversation to wait
in; when it cannot be acted on, open an ask; and it is **never the answer
to an open ask**: only the card answers an ask. That is a prompt change and
needs its own evidence on the factory side, like every line of that file.
The "owner said" marking is built in code and does not depend on it.

### 6.5 Questions: two layers (D6)

**Status, and the common "why", from the board.** "How is notes?", "what
is T8 waiting for?", "why was T9 dropped?" are answered from the roster
row and the board text of 5.3. Fast, no model on the factory side, the
same in every project state.

**Judgement, from the record, through the tool that exists.** The
orchestrator already has a tool that asks an agent a question in the
background: `investigate`. Its default source starts a fresh read-only
agent in a folder. On a project row that folder is the project's, and the
question carries the board text itself (it is built in code and has no
file) and the path of the project's log. The
daemon already points a plain read-only agent at a file in this way, when
a worker's session cannot be asked.

```
owner: "ask the notes factory why it split T8"
   │
   ▼
orchestrator: investigate(notes factory, "Why was T8 split …")
   │   says at once: "Looking into notes, about half a minute."
   ▼
daemon: a fresh read-only agent in the project's folder
   │   reads: the project log (every coordinator act, with its why),
   │          the board text, the plan, the code
   │   the coordinator is not involved: not woken, not asked, not copied
   ▼
its answer ──► investigation result ──► event turn ──► spoken at the next pause
                                         follow-ups resume the same agent
```

- **The owner's condition.** A question cannot make the coordinator act,
  because the coordinator never sees it. That the answering agent cannot
  act on the factory either rests on two different things. On the Claude
  backend it has `Read`, `Grep`, `Glob` and `WebSearch` only: no shell.
  On the Codex backend, which the default source picks when Codex is
  installed, it is a shell in a read-only sandbox, and a local command
  with no run token is treated as the owner's CLI (6.6). This is how every
  investigation of an ordinary agent already runs. **A test in this step
  pins it:** from a default-source investigation on the Codex backend,
  `sheepdog factory stop` does not reach the daemon. If it does, project
  investigations are pinned to the Claude backend, which the
  investigation query can already name.
- **It works everywhere:** project running, stopped or finished; Claude,
  Codex or agy coordinator. It costs the small cap of an ordinary
  investigation.
- **How the answer returns.** As for any agent: a background job, an
  acknowledgement with a rough time, the result as an event turn the voice
  speaks at the next pause, tagged to the request, kept in the ledger
  `recall` searches. One live investigation per project; "stop" cancels
  it. The answer is a model's prose and goes back inside the
  untrusted-text wrapper.
- **What the owner must know: records, not memory.** The answer comes from
  what the coordinator **recorded**: each act and its why in the log, the
  plan, the briefs, the code. It does not come from the coordinator's own
  memory. For "why did you split T8?" the log line says why. For "would a
  different design be better?" the agent reasons from the plan and the
  code like a well-informed colleague; it is not the coordinator
  answering, and the voice should say "from the project's record".
- **A decision is not a question.** "Tell it to use a different design" is
  `prompt`, which is `Say`. "Would a different design be better?" is
  `investigate`. Choosing between them is orchestrator guidance, so it is
  in the eval list beside the `Say` scenarios. A wrong choice is harmless
  one way (a question sent as `Say` changes nothing, by the prompt
  sentence of 6.4) and visible the other (the read-only agent cannot act).

### 6.6 Later: asking the coordinator's own session

This is what gives "what the coordinator itself was thinking". It is the
`source: worker` form of `investigate`, which forks a session read-only.
For a coordinator it does not exist today. Before it can:

| Missing today | In code |
|---|---|
| **No coordinator session id is recorded, for any CLI.** | A Claude coordinator is launched with an empty session, so no `--session-id` is passed and the run record's session stays empty. Codex and agy sessions are discovered after launch for workers only. Recording one is engine work on the launch path and on how a run is adopted after a restart. |
| **Nothing survives a stop or a rotation.** | `Stop` clears the whole coordinator run record; a rotation replaces it. The project needs its own "last coordinator session" value that neither clears. |
| **A Codex coordinator's session is in a private home.** | Every Codex run of the engine gets its own `CODEX_HOME`. The daemon's fork runs with the daemon's environment and would not find the session; the runner cannot set that home today. A Claude fork must likewise read the Claude config directory the engine launched with. |
| **agy** | keeps no transcript the daemon can read: never forkable. |

**Why a fork would be read-only, stated correctly.** What protects is the
fork's **tools**, not a missing run token. The Claude fork is given
`Read`, `Grep`, `Glob` and `WebSearch` and nothing else: it has no shell
and cannot run any command. The Codex fork runs in a read-only sandbox but
has a shell. A local command with **no** run token is treated as the
owner's own CLI: the loopback start and stop routes act on it with no
proof. So a process with a shell and no token could stop or start the
factory. Revision 5 of this page had this backwards. The test that
matters before any Codex coordinator is forked: from inside a Codex fork,
`sheepdog factory stop` does not reach the daemon. If it does, a Codex
coordinator is not forkable under the owner's condition. An answer to a
gated ask stays safe either way: it needs a proof no loopback route gives.

**Cost.** The Claude fork has no model flag: it continues with the
session's own model, capped at 10 USD per run. A coordinator is Opus by
default and its session is long, so each question replays that context on
the owner's Claude quota. If this step is built, it needs a stated bound,
and the existing fallback (on hitting the cap, ask a plain read-only agent
pointed at the transcript) applies.

**`say_reply` stays dropped (D7).**

| Need | Served by |
|---|---|
| "Did it get my instruction?" | the spoken confirmation of 6.1 |
| "What did it do, and why?" | the board text (5.3): each task's last decision and its why |
| A longer "why" | `investigate` on the project (6.5), from the log |
| It needs a decision from me | an ask, as a card |

What `say_reply` would have added is an *unprompted* report after each
instruction: narration the owner did not ask for, on new engine state.

### 6.7 Later: clarify on the ask card (wanted, not v1)

The owner wants to ask a clarifying question on an ask card itself, by
typing or by voice, and hear the coordinator's answer without opening its
terminal. That needs one engine action the engine lacks today: the
coordinator adds a note to an open ask (shown on the card, spoken once,
kept in the log), and an owner wake that names the ask it is about. It is
its own design round after the first version ships; until then the live
coordinator window (D10) covers clarification.

## 7. What is narrated and what stays quiet (D9)

| Narrated (voice, and a conversation row) | Quiet (on the board and in the log only) |
|---|---|
| An open ask for the owner, with its options and recommendation (exists) | A worker or reviewer starting, working, finishing, failing or getting stuck |
| An override result (exists) | Review rounds and verdicts, retro rounds, plan review |
| The coordinator stuck on a dialog (exists) | CI passing, a PR opening, a merge |
| The confirmation that an instruction was passed on (new) | The coordinator's routine wakes, recaps and decisions |
| Anything, when asked: status from the roster row and board text; judgement from a read-only agent on the project's record (new) | A model handoff, a retry, a rotation; heartbeats and sweeps |

## 8. Everything `/factory` does, on the phone (D8)

Every route `/factory` uses for these is already open to a paired device
(`accessDevice` in `internal/web/server.go`). The phone needs **no new
daemon route** for parity. What the daemon requires today stays as it is.

| Action | Routes (all exist, all device access) | What the daemon requires, in code |
|---|---|---|
| Answer an ask | `POST /api/factory/asks/{id}/answer`; the question card (exists in the app) | an answer proof, minted from a paired-device tap or a live voice answer |
| Stop | `POST /api/factory/projects/stop` with the project | a paired-device identity (`proof_required` without it). The app adds a confirmation. |
| Resume | `POST /api/factory/projects/start` with the folder and the stored goal | the same identity; the start checks run and can refuse with their results |
| New goal | the same route with the folder and a new goal: the engine treats a different goal on a known folder as a new goal and wakes the coordinator with it | the same |
| Reopen a finished project | the same route with a new goal. The same goal on a finished project is refused (`closed`). | the same |
| Read settings, models per role included | `GET /api/factory/projects/{id}/settings`: each row with its value and default, and for model rows the CLIs to choose from | — |
| Change a setting or a role's models | `POST /api/factory/projects/{id}/settings` with name and value. This changes nothing by itself: it opens a `rule` ask that reads back the new and current value ("Save" / "Keep current"). The change is made only when that ask is answered. | the identity to propose; an answer proof to save. A `rule` ask can never be answered by a delegation rule. The read-back ask reaches the phone as a question card like any gated ask. |
| New project: pick the folder | `GET /api/spaces` (the folders sheepdog knows on that computer, each marked git or not), plus the folders of existing projects, as the page does | — |
| New project: defaults | `GET /api/factory/settings` | — |
| New project: check | `POST /api/factory/projects/check` with the folder and chosen settings: runs the start checks **on the daemon's computer** and returns each result with its fix | — |
| New project: start | `POST /api/factory/projects/start` with folder, goal and chosen settings | the identity; the start checks run again and a failing check refuses the start. **The chosen settings apply at start with the device identity alone, with no read-back**, exactly as on `/factory`. The read-back is for changing a setting of a project that exists: there the start route refuses any value that differs from the stored one. |

Notes:

- **New project starts from the `+` menu**, beside New agent: it is the
  same kind of act. A folder that is already a project cannot be picked
  there; its row reads "already a project: open it to give it a new goal",
  because a start on a known folder is a new goal and the start route
  refuses a setting that differs from the stored one.
- **On a new project nothing is read back.** The models and settings
  chosen in the form apply at start. So the editor opened from the New
  project form says "Used when you start the project", its button is
  "Done", and it returns to the form. The read-back wording ("Nothing
  changes yet … saved when you answer Save") belongs only to an existing
  project's settings.
- **Folders are picked, not typed** in the first version
  ([owner question 2](#questions-that-remain-for-the-owner)). The routes
  accept any folder string from a paired device today, as they do from the
  page, so typing would add no authority; it is left out because it is
  error-prone on a phone. When herdr cannot be reached the list is empty;
  the app says so and points to the Mac.
- **Start-check fixes are text.** A failing check shows its fix as the
  daemon wrote it. Fixes that are commands (`run: …`) are run in a
  terminal on the Mac; the phone offers Copy and "Check again", not a Run
  button.
- **Settings stay editable on a stopped or finished project (D11)**, with
  the same read-back ask.
- **The models rule stays in code:** reviewers must differ from the
  worker, and a CLI a role cannot launch is refused, by the daemon, with
  its sentence shown under the row.
- **Voice** keeps what it has: answering asks, and "stop the factory" and
  "start the factory" for every project, with names spoken instead of ids.
  New goal, settings and new project by voice are not designed here: a
  misheard goal re-plans a project. Matching one project's name in the
  stop and start phrases is later; on a name tie it would stop or start
  neither and point to the phone.
- **Refusals the phone shows plainly:** no paired-device identity (pair
  this phone again), project gone, finished with the same goal, a setting
  the daemon refuses, a failed start check, and the factory not available
  on this computer (it needs herdr-core).
- The words on the phone are the words of `/factory`
  ([slice14-wording.md](canvas/slice14-wording.md)).

## 9. Reused, new, left out

**Reused as it is:** the agent table and the quiet rule, untouched; the
ask bridge, answer proofs, question cards, override results; held wakes;
the project log; every device route of `/factory` (projects, check, start,
stop, graph, log, settings, asks) and `GET /api/spaces`; the settings
read-back ask; investigations with their default source, their ledger and
how their answers are spoken; `ProjectView`, `GraphView`, `TaskView`; the words and stage
names of `/factory`; `to_supervisor`, `prompt`, `read_screen`,
`list_agents`, `send_keys`, `investigate` and the name matcher; the rule that action
tools run only in the owner's turn.

**New**

| Piece | Where |
|---|---|
| The project name, and its use in ask lines, the pseudo-pane's name, the blocked coordinator's name, what voice stop and start say back, push titles | `internal/factory/view.go`, `internal/supervisor` |
| The coordinator's pane on the project view; one read on the `Factory` interface: is this pane a project's current coordinator | `internal/factory`, `internal/supervisor/factory.go` |
| The screen reads (`Screen`, `ScreenANSI`, and through them the `/ws` subscription), the live attach and the input accept the coordinator's pane | `internal/supervisor/window.go`, `internal/web/term.go` |
| One rail: rename, interrupt and close refuse every factory pane, blocked or not, on the user and the orchestrator route | `internal/supervisor` (`names.go`, `brainhost.go`, `close.go`, `useractions.go`) |
| The `factory` object on a `fask-` card | `internal/state`, `internal/supervisor/factory.go` |
| The `factory.changed` event | `internal/factory`; the app's `AttentionEvents` |
| The muted marker on factory-quiet transitions | `internal/app/mutelog.go` |
| A fake `Factory` in the eval simulator | `internal/inject`, `internal/eval` (neither has any factory code today) |
| The synthetic project row; the blocked coordinator merged into it; the helper of 5.2; the board text of 5.3 | `internal/supervisor/brainhost.go`, `internal/brain/tools.go` |
| `prompt` on a project as `Say`; the heard-words parameter; owner wakes first; the device `say` route; one sentence in `coordinator.md` | `internal/supervisor`, `internal/factory`, `internal/web` |
| `investigate` on a project row: a target that is not an agent row (the project's folder, name and log path from the engine); each task's last decision and why in the board text | `internal/supervisor/investigate.go`, `internal/supervisor/factory.go`, `internal/factory/view.go` |
| The `factory` list in `/api/state`; the run's role, CLI and start time and the last decision on `TaskView`; the open alert on the project view; the `factory` object on a `fask-` card | `internal/web`, `internal/factory/view.go`, `internal/supervisor/factory.go` |
| The app: the permanent project head in Agents, the board, the task view, the coordinator's live window, the text box, stop, resume, new goal, reopen, settings and models, new project, the states | `ios/`; drawn in step 2 |

**Left out, or later**

- Any filter on the agent table, and any factory row in it.
- A needs-you card for a stuck worker; worker and reviewer windows in the
  app, and input on them.
- Reading the coordinator's terminal through the orchestrator.
- A project name in voice stop and start.
- `say_reply`, dropped. Asking the coordinator's own session (6.6), later.
- Removing or archiving a project: follows; until then finished projects
  are folded on the phone and unlisted in the roster.
- New goal, settings and new project by voice; a typed folder path on the
  phone; a Run button for start-check fixes.
- Model and effort on a run's row; an idle or working word for the
  coordinator; "stopped by" and a finished time on the head.
- Projects in the Agents badge count and in the Live Activity.
- The project log screen on the phone; the task graph on the iPhone (the
  iPad gets list and graph).
- The space-page factory section of Part 3 (`FactoryPage` and its
  states), which predates `/factory`. The flat-list boards
  (`FactoryAgents`, `FactoryIPad`, `FactoryIPadSidebar`) were redrawn
  under the same names for this design, so they are replaced in place,
  not removed.

## 10. Plan

Reproduced on `main` first (2026-11-25): a daemon built at v0.1.31 ran a
two-task project with agy in every role, and no worker or reviewer ever
appeared in `agents`; the flat list the owner saw is v0.1.30 behaviour.

The PRs, in order. Each one updates the docs its code changes, so there
is no docs PR at the end. Sizes are net lines without tests.

| # | PR | After | Size |
|---|---|---|---|
| 1a | `feat(factory): project names, the factory list in /api/state, factory.changed, and the muted marker` (keys into ws-contract.md) | — | ~180 |
| 1b | `feat(factory): the TaskView run and decision keys, and the factory object on ask cards` (keys into ws-contract.md) | 1a | ~90 |
| 2 | `feat(window): the coordinator's live window, and one rail refusing rename, interrupt and close on factory panes` | 1a | ~120 |
| 3 | `test(eval): a fake factory in the eval simulator` | 1a | ~220 |
| 4 | `feat(brain): project rows, the tool helper, and board text in read_screen` (golden zero-project roster; scale-ten bar) | 1a, 3 | ~250 |
| 5 | `feat(factory): Say behind prompt, the device say route, owner wakes first, and the coordinator prompt sentence` | 4 | ~210 |
| 6 | `feat(supervisor): investigate on a project from its record` (with the Codex loopback check) | 4, 5 | ~150 |
| 7 | `feat(ios): factory models and routes in SheepdogKit` | 1a, 1b; 5's route | ~260 |
| 8 | `feat(ios): project heads in Agents, factory ask cards, and the Stopped section` | 7 | ~260 |
| 9 | `feat(ios): the project board, the text box, stop and resume, and the coordinator's window` | 2, 5, 8 | ~275 |
| 10 | `feat(ios): the task view, new goal and reopen` | 9 | ~200 |
| 11 | `feat(ios): settings and models per role` | 9 | ~290 |
| 12 | `feat(ios): new project from the + menu, with start checks` | 8, 11 | ~280 |
| 13 | `feat(ios): the iPad list and graph` | 9, 10 | ~270 |

Every PR has a hard cap of 300 net lines; one that would pass it stops
and asks. 2 and 3 run side by side after 1a; 7 runs beside 3 to 6 once
1a's and 1b's keys are in ws-contract.md. 13 is in the first version and
comes last.

**PR 2 is done when** a test shows the engine still handles, or delivers
again, the wakes in flight after (a) an Esc that interrupts a coordinator
turn and (b) a turn the owner started by typing in the window. The second
is likely to break today: the engine clears `CoordWakeAt` once `Run.Turn`
passes `CoordWakeTurn` (`engine.go`), and `Turn` is read from the
terminal's own status (`world/world.go`, `flow/runs.go`), so an owner's
question probably moves it and switches off the silent-coordinator watch
(`flow/project.go`) while the delivered wakes were never acted on. This is
engine state: a state table first, then the code.

**Deleted or deferred**, after asking of each PR what can go:

- The split's separate docs PR: each PR carries its own docs.
- The worker and reviewer snapshot sheet in 10: no worker window in v1.
- In 1a, the in-memory snapshot cache, unless the first check of section 12
  finds that the engine does not hold project states in memory.
- Later, each on its own: the coordinator session fork (6.6), clarify on
  the ask card (6.7), a remove or archive for projects.

## 11. The reviews, and what changed

**Review 1** (revision 2): the name is the id; `prompt` cannot resolve a
project; the fresh-input rule exists; no `Say` with the orchestrator off;
a question through `Say` can park the project; wake order; the eval
harness has no factory; `say_reply` narrowed and deferred; "start by
project id" deleted. All kept in this revision.

**Review 2** (this revision):

| Finding | Answer |
|---|---|
| On `main`, workers are never in the agent table and the coordinator only while blocked | True (`HandleTransition`, the snapshot tests). Sections 1 and 2 are rewritten on that basis. This page's first two revisions had read the early return and missed what it does to the row. |
| The filter PR filters rows that do not exist | Deleted, with the reader table. Step 0 reproduces instead. The Mac notification marker is kept. |
| "The coordinator row stays", "a blocked worker stays" | Deleted. The first is a fact about `blocked` only; the second would be a new feature (D9). |
| The worker half of the tool check has nothing to refuse | Deleted. 5.2 is the project row's rules only. |
| The real decision is A (leave the table, read by engine pane) or B (add rows, filter) | A, in section 4. |
| The project row needs the coordinator's pane from the engine | Section 4 and 5.1; step 1. |
| The check must cover the rename branch; say how the blocked coordinator merges | 5.1 and 5.2. |
| The device route: wording, id only, refusals, length cap | 6.2. |
| The name rule: no parent tie-break; ask lines need the project | Section 3. |
| The orchestrator cannot read a graph | 5.3. |
| "First and alone" should be "first, together" | 6.1. |
| Owner questions | New goal: the owner did not ask for it; it stays on the Mac and the question is gone. Questions not relayed: yes, with 5.3. The text box and its route: kept. A worker's window: read-only. A stuck worker: not shown. |

**Review 3** (revision 4): the structure stands. Five edits, each checked
in code:

| Finding | Answer |
|---|---|
| The name matcher's first stage does not count hits, so a shared alias goes to the first project | True (`findAlias`). Section 3: unique alias, shared spoken name, a resolver test. Section 8 says what a voice tie does. |
| The terminal form of `read_screen` has no mechanism | True: `source` is `screen` or `session`. Deleted for the first version (5.2); the board text keeps the untrusted-text wrapper (5.3). |
| The merged coordinator: its spoken name, `send_keys` when not blocked, an ask as pending prompt | Sections 3, 5.1 and 5.2. |
| The screen lookup: which reads, what they return, the cost, live terminal or snapshot | Section 4. |
| 6.3 claimed more than the engine enforces | 6.3 names the kinds; 6.4 extends the prompt sentence; step 3 gets the scenario. This page counts `reset` among the kinds that only the prompt holds back, by their uses in `internal/factory`. |
| The say route: cap the voice path too, one line | 6.1 and 6.2. |
| Defer the project name in voice stop and start; leave run windows out of the app's first version | Done (sections 4, 8, 10). |

**The owner's answers** (revision 5):

| Answer | What changed |
|---|---|
| D1, D5, D7, D9: OK | — |
| "Shouldn't the coordinator always be in the list? Who do I go to with a question or a design change?" | D3 and 3.1: the project head is permanent in the app in every project state; in the roster running and stopped projects are listed and a finished one is found by name. |
| Questions: OK, but the orchestrator already has a tool that asks an agent | D6 and 6.5: judgement questions go through `investigate` (in revision 6: its default source, from the project's record). 6.6: `say_reply` is dropped, because that one mechanism serves both. |
| "Can the phone do all of it?" | D8 and section 8: parity with `/factory`. Every route was checked to be device access already. |
| Settle the points before the plan | Section 10 is an outline, marked provisional. |

**Review 5** (revision 6):

| Finding | Answer |
|---|---|
| The coordinator session fork does not exist: no session id for any CLI, a stop clears the run, Codex has a private home | True, each read in code. 6.5 is now `investigate` with its default source; the fork is 6.6, later, with what the engine must record first. |
| "No run token" was the wrong reason: a local command without a token is the owner's CLI | True (`internal/web/factory.go`, the loopback start and stop). 6.6 states the tools as the protection and names the test that matters. |
| The fork's cost was not mentioned | 6.6. |
| The board text can carry the last decision and its why | 5.3. |
| Finished projects as roster rows only grow | 3.1: unlisted in the roster, still resolved by name; folded on the phone; a remove follows. |
| On a new project the settings apply at start without a read-back | Section 8. |
| The three owner questions | Adopted. One short question is left: records, not memory. |

The tables of reviews 1 to 3 above describe earlier revisions; where they
differ from sections 1 to 10, the owner's answers and review 5 supersede
them.

**The first review of the boards** (revision 7): the gaps that were the
spec's, each checked in code.

| Gap | Answer |
|---|---|
| A "Stopped" section in Agents | 3.1. |
| Two confirmation wordings; the typed route's refusals not specified as sentences; nowhere says where a question goes | 6.2. |
| New project's entry point; the models editor promising a read-back on a new project; a folder that is already a project | Section 8. |
| Data no route serves: heads kept fresh, the run's role and start time, the last decision in the task view | Section 4: the `factory` list in `/api/state`, and the fields on the graph. |
| Model and effort on rows; coordinator idle or working; "stopped by", "finished yesterday" | Cut (sections 3.1, 4, 9): the views carry none of them. |
| Why a coordinator is not running has no plain fix | Section 4: the engine's alert already has a cause and a fix (`internal/factory/alerts.go`). |
| The Coordinator button opens a window that cannot exist | Section 4: shown only when a coordinator runs. |
| A dialog option that needs typed text | 5.1: answered as its key; the text goes in the project's text box. An ask has fixed options only. |
| Badge and Live Activity | Section 4: unchanged in the first version. |

**The implementation split** (revision 10): the gaps another model found
while splitting the work into PRs, each checked in code.

| Gap | Answer |
|---|---|
| Stale read-only wording; header revision; the board drew model and effort | Header, D10, 4, 5.1, 9; the boards: `FactoryWindow` redrawn, `FactoryWindowStates` added. |
| Nothing makes the app refetch on a factory change | Section 4: `factory.changed`, logged by the engine, no notification. |
| The `factory` list's keys, `goal` missing, which alert | Section 4: the keys pinned, `goal` added, the alert order. |
| `TaskView` has no CLI and the decision no time | Section 4: `run_role`, `run_cli`, `run_started_at`, `last_actor_decision.at`. |
| A factory ask card has no kind or task | Section 4: `factory` on `PendingView`. |
| The blocked coordinator twice in the app | Section 4: shown once, on its head. |
| Rename, interrupt and close accept a blocked coordinator today | Section 4: refused in code for every factory pane. |
| Finished projects' settings; another computer's project | D11, D12. |
| The PR order | Section 10. |

**Design review D10-1** (revision 10, second pass): twelve changes, all
taken.

| Change | Answer |
|---|---|
| 1 `factory.changed` fired on every state write | Section 4: on a log line, or a changed `factory` entry after a bare save; no rate limit. |
| 2 The refetch citation | Section 4 says what `SessionViewModel` and `AttentionEvents` hold. |
| 3 The gap-7 rail only on the user routes | Section 4: one function where the user and orchestrator routes meet; both routes tested. |
| 4 Esc and the Ctrl latch still interrupt | Section 4 says so; PR 2's done-when proves recovery after an Esc and after an owner-started turn (section 10). |
| 5 "13 can wait" | Deleted; 13 is in v1. The iPad keeps the blocked row until it draws heads (section 4). |
| 6 Where the ask-card object lives; other kinds | Section 4: on `state.PendingAction`; other kinds draw the plain card. |
| 7–9 Dynamic Type, dark, the strip's label | Boards `FactoryWindow` and `FactoryWindowStates`. |
| 10 Three not-running wordings; finished; blocked | Section 4 cites `app.coord.off`; finished and the blocked box added. |
| 11 A read lookup for every run | Section 4 and PR 2: the coordinator's pane only. |
| 12 PR 1 over its size | Section 10: 1a and 1b, and a 300-line cap on every PR. |

## 12. Not verified

- Whether the engine holds every project's state in memory between
  passes, or the `factory` list needs its own small cache rebuilt on an
  engine change. Either way it must not read state files per refetch.
- Which engine alerts are open while a coordinator is not running, beyond
  the signed-out one read for this page.

- Whether a Codex read-only sandbox can reach the daemon on loopback.
  This matters for the first version (6.5), where the test sits.
- Whether a Claude fork finds a session under the engine's Claude config
  directory (the later step of 6.6 only).
- How large a real project's log gets, and whether a read-only agent finds
  a task's lines in it quickly.
- What a `rule` read-back ask looks like as a question card in the app
  today.
- What `GET /api/spaces` returns when herdr-core runs but no space is
  open.
- Whether the coordinator run's address is the pane id the screen read and
  the attach take on a real herdr-core. By reading it is: a local
  herdr-core uses the run's address as the pane id unchanged. A test pins
  it.
- How a coordinator behaves on an "owner said" wake while an ask is open
  (6.3). That is from the prompt text and the ask kinds, not from a run.
- Where the owner saw the flat list. The second review found the live
  daemon on v0.1.30; nobody looked at the phone.
- `Say` on a stopped project end to end: the code keeps the wake; there is
  no test yet.
