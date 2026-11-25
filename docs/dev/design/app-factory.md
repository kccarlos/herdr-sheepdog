# The factory in the app and in voice: integration design

Status: step 1 of 2, revision 4 (2026-11-25), accepted after four rounds
of independent review. This page is the integration design only. No UI board is drawn
yet: the boards (Part 5 of the [design canvas](canvas/README.md)) come
after this page is accepted.

Words are the [glossary](../glossary.md)'s. "Owner" is the user. The
sample project is called `notes`.

## Decisions

| # | Decision |
|---|---|
| D1 | The owner sees three levels: **project** (headed by its coordinator), **task**, and the task's **worker or reviewer**. Ordinary agents stay as they are. |
| D2 | A project is named by the last part of its folder's path, everywhere: app, `/factory`, speech, pushes. The daemon computes it. On a tie the orchestrator asks which; a name never changes because another project appeared. |
| D3 | **The agent table is left alone.** On `main` it already holds no worker or reviewer, and the coordinator only while it is blocked. The app's grouping comes from the **engine** (the project view and the graph's live runs), not from the agent table. A factory terminal is read through the screen reads, which learn to read a pane the engine names. **Read-only.** In the app's first version a worker or reviewer is a line under its task; only the coordinator has a window. |
| D4 | The orchestrator's roster has **one row per project**, with a unique alias and a spoken name that may be shared. A blocked coordinator's real row merges into it. One helper in code says what each tool does on a project row. |
| D5 | The owner's instruction reaches the coordinator through **`Engine.Say`**, which exists and has no caller. No new voice tool and no new orchestrator tool: `prompt` on a project row is carried out as `Say`. A device route does the same for a text box on the phone. |
| D6 | `Say` carries **instructions**. Questions about a project are answered by the orchestrator from the board's facts: `read_screen` on a project row returns text built in code from the graph. |
| D7 | First version of the path: a spoken confirmation, asks arriving as cards, and questions answered from the board. A reply built from the log (`say_reply`) is a later PR. |
| D8 | Asks keep their routes and proofs. The phone gets **stop and resume** per project. Voice stop and start stay as they are (every project), and speak names. New goal, reopen, new project and settings stay on the Mac. Nothing gated can be reached through `Say`. |
| D9 | Per-worker narration stays off. A stuck worker is the engine's to handle and is not shown as a needs-you card. |

## Questions that remain for the owner

1. **Answered: the list is clean on v0.1.31** (step 0, section 10). The
   clutter the owner saw was v0.1.30 behaviour; updating the daemon
   removes it.
2. **Answers come from the board, not from the coordinator.** "Why is T8
   slow?" gets the task's stage and its waiting reason, as the engine
   records them. It does not get the coordinator's opinion. That comes
   with the later reply PR, if wanted.
3. **A text box on the project head.** It types straight to the
   coordinator through the device `say` route, so it also works with the
   orchestrator off. If the project head should be voice only, the route
   and the box are dropped.

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
  with Resume on a stopped one.

**The name (D2).** The project view gets a `name`: the last part of the
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
  several computers, it shows the computer's label, as it does for agents.

## 4. Where the grouping comes from (D3)

**The decision.** Two ways were on the table:

- A. Leave the agent table as it is. The app builds the project, its tasks
  and their runs from the engine's views. The screen reads learn a second
  lookup: a pane the engine names is readable without a table row.
- B. Put factory terminals back into the agent table and filter every
  reader of it (there are 21 `List()` call sites).

**A is chosen.** It is smaller, it cannot leak a worker into a reader
nobody listed, and it keeps the tests that say a quiet worker is never
stored. Revisions 1 and 2 of this page described B without knowing the
rows were gone.

**What the app reads:**

| What is shown | From | New in the daemon |
|---|---|---|
| The projects: name, goal, state, counts | `GET /api/factory/projects` | `name`; the coordinator's pane and whether it runs |
| A project's tasks, each with stage, waiting reason, steps, model and its live run | `GET /api/factory/projects/{id}/graph` (`TaskView`, `terminal`) | nothing known yet; the boards may ask for the run's role as a word |
| Open asks | the question cards, as today (`/api/state` `questions`, pane `factory:<id>`) | nothing |
| A stuck coordinator's dialog | its real row in `agents` while blocked, as today; the new app draws that card on its project, matched by the coordinator's pane | nothing |
| The coordinator's screen | the screen reads, with the second lookup below | the lookup |

How often the app re-reads the project list is decided with the boards.
`Projects()` loads every project's state file on each call, so it must not
be called on every `/api/state` refetch.

**Reading a factory terminal.** Three reads start from the agent table
today: `Screen`, `ScreenANSI`, and the `/ws` screen subscription, which
calls those two. One helper in place of the table lookup in the first two
covers all three. It accepts a pane that is not in the table when the
engine names it: a project's coordinator run, or a task's live run.

- **What the read returns.** For a table row, the alias, spoken name,
  kind, state and state-since come from the row. For an engine-named pane
  they come from the engine: the project's name and the run's role (and
  the task, for a worker or reviewer) as the name, the run's CLI as the
  kind, and the run's state as the engine has it, or empty.
- **Cost.** The question "is this pane a run of a started project, and
  which" is asked on every screen poll. It must not load every project's
  state file each time, as `Projects()` does. The supervisor's `Factory`
  interface gets one read for it, answered from what the engine already
  holds in memory or from a small cache rebuilt on an engine change.
- **Read-only, by construction.** The routes that type into a pane, send
  keys, interrupt, close or rename it, and the live terminal attach, each
  do their own table lookup and are not changed. They keep refusing a pane
  with no row.
- **So a run's window is the snapshot window, not the live terminal.** On
  herdr-core the app's agent window is the live terminal, whose attach
  needs a row. The boards must not draw a live terminal for a factory run.
- **In the app's first version only the coordinator's window is offered.**
  The daemon lookup is the same code for any run, so worker and reviewer
  windows can follow without daemon work. The owner steers at project
  level, and the task list with its run line already answers "which
  workers belong to which coordinator".

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

`Agents()` and `Resolve` return one synthetic `AgentInfo` per project:
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
| `interrupt`, closing, renaming (`note` with a name), `run_cli`, `promise`, `investigate` | refused in code (`brain.rail_blocked`): "That works on an agent, not on a factory project." Stopping is "stop the factory", or Stop on the phone. |

Without the helper these tools would reach host methods with a pane that
is not a terminal. There is no rule for worker terminals, because a worker
never resolves.

### 5.3 `read_screen` on a project: the board as text

The orchestrator has no tool that reads a graph or a log, and the
coordinator's screen rarely says why a task is slow. So `read_screen` on a
project row returns text built in code from the graph: the project's state
and counts, each open task with its stage and its waiting reason, and the
open asks. No new tool. For a stopped project it is the only thing there
is to read.

```
notes: running. 3 working, 1 waiting on you, 9 of 14 done.
T7 feat: search — review, round 2 of 2.
T8 feat: tags — working, codex, 31 min.
T9 feat: export to PDF — waiting on others: after T8.
T10 feat: import — waiting on you: "Delete the old notes table in the migration?"
```

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
| Finished | "notes is finished. Give it a new goal on your Mac." | `Say` refuses; nothing is queued |
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
- **Refusals the app shows:** finished (`closed`: "notes is finished. Give
  it a new goal on your Mac."), empty text, too long, project not found,
  no paired-device identity.

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
prompt sentence of 6.4, and one eval scenario (section 10, step 3).

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

### 6.5 Questions are not relayed (D6)

"How is notes?", "why is T8 slow?", "what is it waiting for?" are answered
by the orchestrator from the roster row and the board text of 5.3. They
are not sent through `Say`. This is prompt guidance for the orchestrator,
which is acceptable because it is not a safety property: a question that
does get relayed is still only an instruction that changes nothing.

### 6.6 Later: a reply built from the log (`say_reply`)

Not in the first version. When it is built, after the `Say` path has run
on a real project:

- **Text:** the log lines with `by == coordinator` and a sequence number
  after the line that records the delivery of the owner wakes, up to the
  end of that turn. Worded as what the coordinator did in the turn that
  read the words. With no such line: "The notes coordinator read it and
  changed nothing."
- **One stored value:** the sequence number at delivery.
- **When:** only in the branch of `reconcileCoordWakes` where the
  coordinator is idle and its turn advanced; that function returns true
  for an ended run too, and the two are told apart by the run's state. Its
  requeue branch produces no reply; a requeued wake gets its reply after
  it is delivered again.
- **Delivery:** `Changes()` can drop, so the reply text is stored in the
  project's state and the supervisor re-reads it, as it re-reads asks.
- **To prove first:** that the coordinator's turn counter advances alike
  for a Claude, a Codex and an agy coordinator.
- **The coordinator's own sentence,** if wanted then: one read of its
  screen at the end of the turn.
- No push for it.

## 7. What is narrated and what stays quiet (D9)

| Narrated (voice, and a conversation row) | Quiet (on the board and in the log only) |
|---|---|
| An open ask for the owner, with its options and recommendation (exists) | A worker or reviewer starting, working, finishing, failing or getting stuck |
| An override result (exists) | Review rounds and verdicts, retro rounds, plan review |
| The coordinator stuck on a dialog (exists) | CI passing, a PR opening, a merge |
| The confirmation that an instruction was passed on (new) | The coordinator's routine wakes, recaps and decisions |
| Anything, when asked: "how is the notes factory?" (new: roster row and board text) | A model handoff, a retry, a rotation; heartbeats and sweeps |

## 8. Asks, stop and resume (D8)

The owner asked for stop and resume on the phone in the first version, and
did not ask for new goal there.

| Action | Phone | Voice | Authority, in code |
|---|---|---|---|
| Answer an ask | tap an option on the question card (exists) | say the option (exists) | answer proof from a paired-device tap or a live voice answer |
| Stop a project | `POST /api/factory/projects/stop` with the project, behind a confirmation | "stop the factory": every running project, as today | phone: the paired device's identity (the route refuses without it). Voice: live voice input only, as today |
| Resume | `POST /api/factory/projects/start` with the folder and goal the project view sent | "start the factory": every stopped project, as today | same |
| New goal, reopen, new project, settings | on the Mac | no | — |

- **Voice stop and start stay as they are.** "Stop the factory" stops
  every running project and "start the factory" resumes every stopped one.
  With one project that is exact; with several, stopping all is a safe
  first version, and the phone stops or resumes one. What is spoken back
  uses names, not ids. Matching a project name in these phrases is left
  for when two projects at once is common. When it is built, a tie on the
  name has no way to be told apart by voice: it stops or starts neither
  and points to the phone.
- **A failed start check on resume.** Start runs the start checks and can
  refuse with their results. The phone shows the failing lines and their
  fixes as the daemon wrote them, and says they are fixed on the Mac.
- Refusals the phone shows plainly: no paired-device identity (pair this
  phone again), project gone, and the factory not available on this
  computer (it needs herdr-core).

## 9. Reused, new, left out

**Reused as it is:** the agent table and the quiet rule, untouched; the
ask bridge, answer proofs, question cards, override results; held wakes;
the project log; the device routes for projects, graph, log, asks, start
and stop; `ProjectView`, `GraphView`, `TaskView`; the words and stage
names of `/factory`; `to_supervisor`, `prompt`, `read_screen`,
`list_agents`, `send_keys` and the name matcher; the rule that action
tools run only in the owner's turn.

**New**

| Piece | Where |
|---|---|
| The project name, and its use in ask lines, the pseudo-pane's name, the blocked coordinator's name, what voice stop and start say back, push titles | `internal/factory/view.go`, `internal/supervisor` |
| The coordinator's pane on the project view; one read on the `Factory` interface: is this pane a run of a started project | `internal/factory`, `internal/supervisor/factory.go` |
| The screen reads (`Screen`, `ScreenANSI`, and through them the `/ws` subscription) accept a pane the engine names, read-only | `internal/supervisor/window.go` |
| The muted marker on factory-quiet transitions | `internal/app/mutelog.go` |
| A fake `Factory` in the eval simulator | `internal/inject`, `internal/eval` (neither has any factory code today) |
| The synthetic project row; the blocked coordinator merged into it; the helper of 5.2; the board text of 5.3 | `internal/supervisor/brainhost.go`, `internal/brain/tools.go` |
| `prompt` on a project as `Say`; the heard-words parameter; owner wakes first; the device `say` route; one sentence in `coordinator.md` | `internal/supervisor`, `internal/factory`, `internal/web` |
| The app: projects in Agents, the board, the task view, the coordinator's snapshot window, the text box, stop, resume, the states | `ios/`; drawn in step 2 |

**Left out, or later**

- Any filter on the agent table, and any factory row in it.
- A needs-you card for a stuck worker; worker and reviewer windows in the
  app; input on any run's window.
- Reading the coordinator's terminal through the orchestrator.
- A project name in voice stop and start.
- `say_reply` (6.6) and the coordinator's own sentence.
- New goal and reopen on the phone or by voice.
- A `factory` entry in `/api/state`, until the boards say it is needed.
- The project log screen on the phone; the task graph on the iPhone (the
  iPad gets list and graph).
- Earlier boards this design replaces, to be listed and removed in step 2
  after the new ones are accepted: `FactoryAgents`, `FactoryAgentsDark`,
  `FactoryIPad`, `FactoryIPadDark`, `FactoryIPadSidebar` (the flat list),
  and the space-page factory section of Part 3 (`FactoryPage` and its
  states), which predates `/factory`.

## 10. Plan

| Step | What | Checks |
|---|---|---|
| 0 | **Reproduce on `main`.** A daemon built from `main`, a project running, the phone's Agents list. Expected: clean. No PR. **Result (2026-11-25): clean.** A daemon built from `main` at v0.1.31 ran a two-task project on the test repository with agy coordinator, workers and reviewers; `GET /api/state` was sampled every 15 s through implementing and two review rounds per task. No worker or reviewer ever appeared in `agents`; the coordinator appeared in one sample, while blocked. The flat list the owner saw is v0.1.30 behaviour. | the list, on the device; if workers show, stop and revise this page |
| 1 | The project name everywhere, the blocked coordinator's included; the muted marker; the coordinator's pane on the project view; the screen reads' read-only lookup | `make test`, with a test that a task's `terminal` from the graph is the pane id the screen read takes on herdr-core; the conformance suite on both herdr backends; an installed app build against the new daemon |
| 2a | The eval simulator learns the factory: a fake `Factory` | `make test`; no behaviour change |
| 2 | The project row with its unique alias, the merged blocked coordinator, the helper, the board text | `make test` with the golden test of 5.1 and a resolver test for two projects with one name; `make eval` with a project's row in the roster, including at least 15 scale-ten runs: the last roster wording change was held to that bar ([eval/README.md](../eval/README.md), standing safety lesson and the 11-03 row) |
| 3 | `prompt` on a project as `Say` with the heard words; owner wakes first; the `coordinator.md` sentence; the device `say` route; a test for `Say` on a stopped project | `make test`; `make eval` scenarios for one project, two projects with one name, stopped, finished, and a daemon-started turn that must not `Say`, and an open ask that the owner answers in free words the fast path does not match (the orchestrator must point to the card or the spoken option, and must not `prompt` the project with the answer); factory-side evidence for the prompt sentence; an end-to-end run on a test project |
| 3b | `say_reply` (6.6), after step 3 has run on a real project | `make test`; the turn counter proved on Claude, Codex and agy coordinators |
| 5, 6 | The app, after the boards | unit and UI tests with a stub daemon; the accessibility checks |
| 7 | Docs: [factory.md](../../factory.md), [iphone-app.md](../../iphone-app.md), [safety.md](../../safety.md), [ws-contract.md](../ws-contract.md), [roles.md](../roles.md), [ios architecture](../../../ios/docs/dev/architecture.md) | `go test ./internal/docscheck` |

Steps 0 to 3 do not depend on the boards.

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

## 12. Not verified

- **No daemon built from `main` was run with a project.** Section 1 rests
  on the code and the tests. Step 0 is that run.
- Whether a task's `terminal` in the graph is the pane id the screen read
  takes on a real herdr-core. By reading it is: the run's address, which a
  local herdr-core uses as the pane id unchanged. Step 1 pins it with a
  test.
- How a coordinator behaves on an "owner said" wake while an ask is open
  (6.3). That is from the prompt text and the ask kinds, not from a run.
- Where the owner saw the flat list. The second review found the live
  daemon on v0.1.30; nobody looked at the phone.
- That the coordinator's turn counter advances alike for Claude, Codex and
  agy coordinators (needed only for step 3b).
- `Say` on a stopped project end to end: the code keeps the wake; there is
  no test yet.
