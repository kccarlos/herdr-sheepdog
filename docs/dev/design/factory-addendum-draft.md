# sheepdog-factory design addendum: launch contract, quota probe, debug and phone (2026-11-17)

A proposal. Nothing was edited or run except `codex --help`, `codex features list` and read-only greps. Engine paths are under `factory/mods/tasks/bin/herdr_task/`; Go paths under `internal/`. Task-store facts come from a read of the running factory's task store (55 tasks) that printed counts only.

**Three findings that change the held tasks**

1. **The stuck Codex workers had no permission flags at all.** Stored worker argv is `codex --no-daemon [-m …] -c service_tier="default"` on all 11 Codex tasks: `argv_for` emits a sandbox or approval flag only when `perm` is set (`settings.py:366-371`), and the project had none. Sandbox, approval policy and (6 of 11) model all came from the owner's `~/.codex/config.toml`. Items 1 and 2 are one bug: the engine inherits what it does not state.
2. **No engine template contains `codexbar`.** It is only in the reader (`usage.py:51`), two docstrings (`commands.py:654,663`) and `doctor.go:1164`. It is in the `prompt` field of 55 of 55 tasks, and 50 carry one identical line ("Quota: before each heavy batch and about every 10 minutes run…"), so it was pasted in by whoever wrote the briefs, from the owner's standing text rule. *Unverified: whether the coordinator or the outside orchestrator pasted it, and whether a `start_notes` setting also carries it.* `docs/dev/factory/engine-audit.md:274` ("`launch.py:271` appends the codexbar line") is wrong today. SDF-55 as filed ("no codexbar in templates") would change nothing.
3. **Factory terminals are already listed on the phone, and already look muted.** `Snapshot` lists every live local terminal that is an agent and does not filter on space (`herdrcore/agents.go:107-113`); the app puts agents without a space in a last, unnamed group (`ios/Sources/SheepdogKit/Sidebar.swift:61-70`). The quiet rule is applied to speech, push, cards and banner counts (`supervisor/factory_quiet.go`, `web/activity.go:185,210`), not to the list. Not run end to end.

## 1. Covered already, and the gaps

| # | Today's problem | Already in the design or code | Gap |
|---|---|---|---|
| 1 | Codex dialogs | §5 "workers run with full permissions"; `ask` records only; a pane on a dialog is flagged (note 51). `perm=full` gives `--dangerously-bypass-approvals-and-sandbox` (`settings.py:367-368`); `project start` sets `perm: full` (`project.py:11`). Reviewers already run `-a never` (`settings.py:329,661`) | `perm` unset states nothing. Resume reuses the stored argv (`launch.py:455-456`, `settings.py:679-680`), so an old task keeps its old flags |
| 2 | Silent model | Retro B1 step 0 stamps reviewer CLI and model on a round | Fallback reviewer is `model: None` (`commands.py:686`). `review_runs` stores the requested model (`commands.py:897-898`: 2 of 9 are `None`); worker-run rounds store none (`commands.py:1052,1307`); effort is stored nowhere; nothing stores the model actually used |
| 3 | codexbar | `usage.read` already returns `unknown` pools when codexbar is missing or fails (`usage.py:49-54,31-47`); unknown never blocks a reviewer (`commands.py:662-666`); §6 says "run without quota facts" | Workers run a quota tool because their brief says so. `unknown` has no reason. `usage.pick` (`usage.py:76-89`) has no caller |
| 4 | Merge stall | `assistant.md:10-11` says accept is policy; `drift_pass` (SDF-48) raises "approved, not merged" at 30 min; SDF-50 fixes `main_red_hold` | **Nothing to add.** `drift_pass` is not in `tick.py` yet; it must land before SDF-15 |
| 5 | Debug space | Engine doctor exists (`internal/factory/doctor.go`); `web.agent_input` is logged (`web/window.go:95-98`) | No decision log. §3 says "no window" and "never types". Live typing is only byte-counted (`web/term.go:421-441`) |
| 6 | Phone | Agent window, list, quiet rule as above | No group name, no task or model on a row, no link from `/factory` (no `/factory` route exists yet) |

## 2. Agent launch contract

**Rule (code, in `argv_for` and `resume_argv`): every start, resume and reviewer argv states permission mode, model and effort. The engine never relies on a CLI's config file, and never writes one.**

| CLI | Worker (perm `full`, the only factory value) | Reviewer | Notes |
|---|---|---|---|
| Codex | `--dangerously-bypass-approvals-and-sandbox -m <model> -c model_reasoning_effort="<e>"` | unchanged: `-s read-only -a never` (worker-run), `-s workspace-write -a never` in a scratch folder (gate) | Both flags exist on `codex resume` (checked in `--help`, 0.169.0) |
| Claude Code | `--dangerously-skip-permissions --model --effort` (`settings.py:343-353`) | `--permission-mode plan` | Resume re-states the mode already (`launch.py:450-454`) |
| agy | `--dangerously-skip-permissions --model --effort` (`settings.py:376-383`) | sandboxed profile, as today | No change |

**Why bypass and not `--approve-for-me` / `approvals_reviewer`.** The owner decided "full permissions as the user, and that is all" (11-17); the worker sandbox was cancelled on 11-08. Bypass is the one mode in which Codex has no dialog to stop on, it is what Claude and agy workers already get, and it deletes code: the PermissionRequest hook (`launch.py:189-196`, `cmd_allow` `:328-343`), the `OWN`/`GIT`/`helper_hc` matching for Codex (`:258-325`) and the "require_escalated" brief text (`:608-612`) are dead under it. `--approve-for-me` keeps a sandbox, adds a second model whose denial behaviour I could not verify (failure to the model, or a dialog?), and needs the `guardian_approval` feature. Widening `OWN` is a hand-kept list. If the owner wants the guard anyway, it is question 1.

**Model and effort.** Resolution order: task → project setting for the role and CLI. Nothing else: reading the CLI's own default would be the same inheritance that failed today, and a doctor WARN is not something a voice-only owner sees. So a missing model is refused early, where the coordinator is told at once: `add`, `plan apply` and `project start` fail with "no model set for codex workers in this project". `project start` saves a pick per signed-in CLI and role from its probe, and `codex_models()` (`settings.py:586-596`) validates Codex ids, so no model list is kept. The reviewer fallback chain (`commands.py:686`) uses the project's saved pick for each CLI and skips a CLI that has none. Nothing can then reach `launch()` without a model; if it does, that is a bug with a test. The alternative is question 3.

**Run record** (added to `task.agents[]`, `task.reviewers[]` and `review_runs[]`, written in `launch()` `launch.py:916`):

| Field | Meaning |
|---|---|
| `agent`, `argv`, `at`, `address` | exist |
| `model`, `effort` | what the argv asked for; never null after this change |
| `model_source` | `task` \| `project` |
| `perm` | `full` \| `read-only` |
| `model_used` | read from the CLI's session file by `_track`; `null` means unknown, never a guess. *Unverified per CLI* |
| `resumed_from` | address of the run this one resumed |

**Round record** (`reviews[]`): `reviewer_agent`, `reviewer_model`, `reviewer_effort`, `worker_model`, `worker_effort`, copied by the engine from the two runs. This belongs to the retro's "structured rounds" task (B5 task 1), not to SDF-54: one owner for the schema. `show` prints both.

**Resume.** `resume_argv` rebuilds flags from the current rule and the recorded `model`/`effort`, and keeps only the session id from the old run. A task started before the change resumes with full flags. A resume on another model is a new run with `resumed_from`.

**First-run notice** (printed by `factory start`, spoken once):

> Factory agents work without asking. Each worker runs as you, with full access to this Mac, limited only by its brief and its own worktree. That is not a sandbox. Reviewers run read-only. Sheepdog names the model for every agent and does not read or change your Claude, Codex or agy settings. Say "stop the factory" at any time.

## 3. Quota probe

Keep `usage.read` as the probe; do not build a second one.

| Part | Proposal |
|---|---|
| Interface | `usage.read(cache_dir) -> [pool]`, pool = `{name, state: ok\|unknown, reason, five, week, five_reset, week_reset}`. `reason` is new: `not installed`, `timed out`, `no data`, `not signed in` |
| Detection | `shutil.which("codexbar")` at each read; one reader today. A second reader is added only when a CLI offers a non-interactive usage command. Never an interactive screen, never a prompt: it runs in the tick, outside any agent's sandbox |
| Unknown | A value, not an error. Rails: never start on a pool **known** empty; an unknown pool starts, and a provider quota screen (`QUOTA_SCREEN`, `commands.py:590`) is the fallback signal, as §6 already says |
| Coordinator | The pool table in every wake prints `codex: unknown (codexbar not installed)`. Built-in brief line, replacing the owner's rule in briefs: "Quota is watched for you. Do not run quota tools." The owner's text rule stays the coordinator's, and is not copied into briefs |
| Digest | "Claude 40% left" when known; "quota unknown" once per digest when any pool in use is unknown; nothing per worker |
| Delete | `usage.pick` (no caller). Doctor's codexbar line stays a WARN |

Limit accepted: code cannot stop a coordinator from typing a quota command into a brief without a word list. Under bypass it no longer prompts; the brief line and the replay set cover the rest.

## 4. Debug and owner intervention

| Proposal | Verdict |
|---|---|
| (a) terminals viewable and typable | **Keep.** No new UI: the existing Agent window |
| (b) `factory log` | **Keep.** One append-only JSONL per project in the store; `sheepdog factory log [--project X] [-f]` prints it |
| (c) debug section on `/factory` | **Trim.** Collapsed; it shows the last 20 log lines and the doctor lines. Model and rounds are fields on the task row. No embedded terminal: a row links to the Agent window |
| (d) `factory doctor` | **Merge** into the existing command as a "running" part. It prints stage and `drift_pass` results and computes nothing new: one lease holder per project, panes on a dialog, pools, main red, stale asks |

**Log line** (one per decision; the engine writes all of them: its own decisions, and one line for each engine verb the coordinator runs (`accept`, `start`, `recap --decide`, `plan amend`, `review-more`), with that verb's reason as `why`. Free text such as `progress` is not parsed):

```
2026-11-17T14:02:11Z SDF  by=engine       wake=event:ready      task=SDF-41 decision=accept        why="2 rounds passed, CI green"         model=-
2026-11-17T14:05:40Z SDF  by=coordinator  wake=event:drift      task=SDF-38 decision=open-fix-task why="CI red on a reviewed head"         model=claude-opus-5-5
2026-11-17T14:09:02Z SDF  by=owner        wake=input:phone      task=SDF-38 decision=typed         why="prompt, 41 chars, iPhone"          model=-
```

**Owner typing.** The mark comes from the path, never from terminal content. The phone reaches a pane only through the daemon, so the daemon is the witness: on a prompt, a key or a typing burst into a terminal with `sheepdog.*` labels it calls one new contract action, `owner touch {terminal, device, kind, len}`. The engine writes the `by=owner` line, appends `task.owner_touches[]` (the baseline's "owner touches" counts them) and holds the watchdog's Esc and restart on that pane for 10 minutes. Length is logged, not text (question 2). New in Go: one `web.term.input` event per burst in `term.go`. **Rail, in code:** input to any pane, including an ask pane, never answers a gated ask; only the proven-answer route does (`web/server.go:600`). *Unverified: that `ask.py`'s pane refuses gated kinds; it is a Done criterion.* Typing at the Mac through `hc` is not seen by the daemon and stays unmarked; *unverified whether `hc events` names an input source.*

## 5. Phone wiring (revised after a full read of the app)

**What the app does today with factory terminals** (code read; nothing run):

| Fact | Evidence |
|---|---|
| The iPhone app has no factory code and its `Agent` model does not decode `labels`, though the daemon sends them | no `factory` or `labels` field under `ios/Sources`; `internal/state/table.go:86` |
| Factory terminals are listed, in the app's normal sections (Needs you, Working, Done) by state. The iPhone has no grouping by space; the iPad sidebar puts a terminal with no space under "Other" | `attention/classify.go:49`, `Sidebar.swift:61`, `IPadRootView.swift:244` |
| Workers, reviewers, checks and helpers already show the crossed bell, because the daemon sets `muted` for a quiet row; VoiceOver then says "Muted space", which is wrong. The coordinator is not quiet | `supervisor/attention.go` (`muted = isFactoryQuietRow \|\| rowMuted`), `FlockView.swift:916`, `herdr/factory.go:28` |
| A quiet worker stuck at a CLI prompt gets **no question**: `onBlocked` returns before a pending action is made. Its card sits under Needs you with no answer buttons, and it still raises the tab badge | `supervisor/transitions.go:413-416` |
| A factory ask already arrives as a normal question (`fask-<id>`, pane `factory:<project>`) through the existing approval card, which has a "Recommended" tag. It has no agent, and floating questions are hidden on the Agents pane, so it shows **only on Conversation** and the badge stays 0 | `supervisor/factory.go:22-24`, `MainView.swift:220`, `ApprovalCard.swift:335-363` |
| Owner input: a prompt is logged as `web.agent_input` with its length; typing mode and key-bar bytes over `/term` are not logged; nothing reaches the Conversation; the terminal draws only the agent's own frames | `web/window.go:95-98`, `web/term.go:421-441` |
| "Quiet" is already a Dock mode (a question is open) and a notify level | `Dock` board, owner contract §6.3 |

**What changes, smallest first**

| Need | Change |
|---|---|
| Listing and order | **None.** Keep the one Needs you / Working / Done sort shared with the daemon. No separate Factory group (my first draft had one; it broke that sort) |
| Role, task and model on a row | **New, small:** Swift decodes `labels`; the card's kind line reads "codex · worker", and a third line shows the model from a new `sheepdog.model` label stamped in `launch()` (`launch.py:891-893`) |
| The crossed bell | Keep the glyph; change the words for a factory row to "Factory, not announced" (VoiceOver too). The daemon sends the reason beside `muted`, so the app does not guess from labels |
| Blocked at a prompt | **New copy only:** the card says it waits at a prompt and offers Open window. Under the launch contract of §2 this state should be rare; it is the one `doctor` points to |
| Factory ask on Agents | **New:** a project-level approval card under Needs you (the stock `ApprovalCardView`) and the badge counts it. Copy: "a typed answer doesn't count" |
| Conversation | **No new row kinds.** A digest is the voice bubble with Details; an alert is the agent-update row |
| Agent window | Unchanged, plus one `NoticeBar` line ("Factory terminal, not announced" and the model). No marks inside the terminal |
| Owner input logged | §4; daemon only |
| `/factory` | A web page served by the daemon, opened in the browser from the paired phone; the app has no web views. A row links to the agent's window. *Unverified: the app's deep-link form.* Alternative to decide later: a section on the existing space page |
| iPad | No new layout. Factory terminals appear under "Other" in the sidebar and in All agents with its filters |
| Words | Do not call the factory rule "quiet" in the app: say "not announced" |

Swift work: decode `labels`, the row lines and words, the project-level ask card and badge, the notice line. No new screen.

## 6. Changes to the main design

| Section | Replace or add |
|---|---|
| §2 after "Never narrated" | Add: "Silenced means no speech, no push and no question card for that terminal. It never hides a terminal: every factory terminal is in the phone's agent list, in its usual section, and opens in the Agent window. In the app the words are 'Factory, not announced', never 'muted' or 'quiet'." |
| §2 touchpoints | Add a sixth, optional row: "Look in or step in: open any factory terminal on the phone; typing is logged as the owner's." |
| §3 Coordinator | Replace "in an hc terminal, no window" with "in an hc terminal, listed on the phone like every factory terminal". Replace "The owner never types in its pane" with "The owner never needs to type in its pane. When they do, the input goes through the daemon, is logged as the owner's, and never counts as an answer to a gated ask." |
| §3 Owner contract | Add action `owner touch`; `graph` tasks gain `terminal`, `model`, `rounds` |
| §5 Worker permissions | Add: "The engine's argv states permission mode, model and effort on every start, resume and review. Nothing is inherited from, or written to, the owner's CLI config." |
| §5 Quota and models | Replace "(share left, time to reset, from `codexbar`)" with "(share left, time to reset, from the quota probe; a pool it cannot read is `unknown` with its reason)". Add: "Every run and every round records model and effort." |
| §6 first run, step 3 | Replace "confirm the pools found" with "the design system: use an existing one, make one first, or skip (§9)". Pools found are stated in the plan summary, not asked |
| §6 cannot-be-bundled, `codexbar` row | "Needed for: quota facts (optional). Without it: pools read `unknown`; switch only on a provider error." |
| §4 surfaces, `/factory` | Add: "A collapsed debug section: the last log lines and doctor's running checks. A task row opens its worker's window." |
| §4 Plan row | Add: "Tasks of a UI milestone are split after its design passes, and each names the boards it implements." |
| §5 Designs and plans row | Add: "Visual design is drawn on the project's canvas, exported read-only into the repo with a PNG per board, and reviewed by a session that did not draw it. After two change requests the retro ladder runs before the owner is asked." |
| §8 Do not build | Add: "a second terminal UI; a debug dashboard beyond that section; a quota reader that drives a CLI's screen" |
| §9 Unverified | Add the list below |

**Unverified, in one place:** `--approve-for-me` denial behaviour; that Codex's bypass mode shows no first-use confirmation; who pasted the codexbar line; `model_used` from each CLI's session file; Claude and agy default-model lookup; `isAgent` for wrapped worker commands; the ask pane and gated kinds; `hc` input source; app deep link; why `perm` was unset on the running project (its settings were not read).

## 7. Tasks and order

All of A to D land **before SDF-15**: the switch must not carry today's four failures over. E and F follow it.

| # | Task | Scope (amended) | Size | Done |
|---|---|---|---|---|
| A | **SDF-53** | Permission mode always stated for start and resume; factory default `full` when unset; resume rebuilds flags; first-run notice. Reviewer scope **deleted** (already `-a never`). Then delete the Codex hook path and its brief text, keeping the trust roots of `own_argv` (`launch.py:193-194`) | Python ~80, minus ~120 | With a `CODEX_HOME` that holds only the sign-in, and again with one whose `config.toml` sets a weak model, `on-request` approvals and a read-only sandbox, a Codex worker runs `make test` and a socket test with no dialog and on the named model; a task started before the change resumes with the flags; no test reads the real config |
| B | **SDF-54** | Model and effort stated on every argv, with `model_source`; run record fields; `model_used` from `_track`; `show`. Fallback reviewer no longer `None`. Round fields move to the structured-rounds task | Python ~150 | No argv without a model in the suite; `show` prints requested and used model per run; `add` without a resolvable model is refused with the fix |
| C | **SDF-55** | Retitled "quota unknown is a value": `reason` on pools, pool table wording, the brief line, digest wording, delete `usage.pick`. No template removal (there is none) | Python ~60 | With no codexbar on PATH: a task goes from `add` to merged, the wake shows `unknown (not installed)`, no agent runs a quota tool in the replay set |
| D | New: `factory log` and `owner touch` | JSONL log, the CLI view, the contract action, `web.term.input`, watchdog hold, the gated-ask refusal test | Python ~120, Go ~100 | Each of the three line kinds appears in a scripted run; phone typing into an ask pane of kind `release` is refused |
| — | SDF-48, SDF-50, SDF-52 | Unchanged; before SDF-15 | — | — |
| E | New: doctor running part and the debug section | Doctor prints the existing checks; the section is added inside the `/factory` page task | Go ~80, JS ~60 | A pane left on a dialog and a stale ask each show as one doctor line with a fix |
| F | New: phone wiring | Swift decodes `labels`; role, task and model on the card; "Factory, not announced" words; the blocked-at-prompt copy; a project-level ask card on Agents with the badge; the window notice line; `terminal` on graph tasks and the row link | Go ~60, Swift ~200 | On the fixture daemon a factory worker is listed in its state section with role, task and model, VoiceOver says "Factory, not announced", a factory ask shows on Agents and raises the badge, and a `/factory` row opens the same window; no speech or push in the run |
| G | New: design loop (§9) | the design-system question and `design.system`/`design.canvas` settings, export folder with PNGs and hash check, Claude-only rule for visual design, independent reviewer check, `boards` on tasks of a UI milestone, brief lines, designer and design-review template lines with two replay situations | Python ~200, templates ~25 lines | A UI milestone's task with no `boards` is refused by `plan apply`; a visual design task cannot go ready without a fresh export; a hand edit in the export folder is refused; a non-Claude CLI or the drawing session is refused as its reviewer |

G is not needed for the switch, so it lands after SDF-15, and before E and F. It applies to them: both are owner-facing, so they start only after the Part 3 boards are exported into the repo and pass an independent review.

## 8. Questions for the owner

1. **Codex workers: bypass (recommended) or `--approve-for-me`?** Bypass matches your 11-17 decision and removes code. The other keeps Codex's own guard at the cost of a second model and one unverified path.
2. **Owner typing: log length only (recommended) or the text?** Text makes the log a better debug record and may put a pasted secret in it.
3. **No model set for a role: refuse at planning time (recommended), or start on the CLI's default with a warning?** Refusing at `add` tells the coordinator at once and cannot stall a running night. The CLI default is what silently ran gpt-6.1-terra today.

## 9. Design loop before implementation (owner requirement, 2026-11-17)

**Why.** This factory was built from a text-only design. Owner-facing work (asks as cards in SDF-22, the quiet rule in SDF-23, the planned `/factory` page) was merged or planned with no drawing, so nobody could see that "no window" removed the owner's only way to look in.

**Source.** This is the practice another of the owner's projects already runs by hand (its design README, "Where design output lives" and "Who reviews what"), made product behaviour: technical design in docs; visual design changed only in the artifacts; the repo's design folder a read-only export with a PNG per board; visual design tasks on Claude; milestone design reviewed by Fable before the milestone's tasks are split.

**Fit.** It is the existing design task made stricter, not a second mechanism. `plan apply` already refuses a UI milestone without a design task that the other tasks come after (`plan.py:305-318`, milestone key `ui`, `plan.py:21`); a Fable round already judges design tasks (FAC-99, `commands.py:909`) with a cap of two change requests (`FABLE_CHANGES`, `settings.py:995`). FAC-94's "wait for the person" stays the opt-in typed rule.

| Step | Rule (in code unless marked) |
|---|---|
| 0. Two kinds of design | **Technical** design (architecture, task split, acceptance) is a document under `docs/`; any model. **Visual** design lives only in the project's artifacts, named once in a project setting `design.canvas` (and `design.system` if there is one) |
| 1. Draw | A UI milestone's design task is drawn by a design agent on the canvas: every new screen as an artboard, with empty, error and dark states, plus flows and diagrams. Nobody edits visual design in the repo |
| 2. Export | `docs/<project>/design/` is a **read-only export** of the artifacts: board sources, a PNG of every new or changed artboard, and `boards.json` (`id`, `title`, `file`, `png`, `canvas`). Re-exported after every artifact change, never edited by hand: a hand edit there fails `ready` (the export is compared with its recorded hash). Codex and agy workers and reviewers check the UI against the PNG. `ready` on a visual design task refuses without a fresh export. *Unverified: how an export is produced unattended. Both repos already hold exports made by hand or by an agent (the other project's canvas and design-system exports; sheepdog's `docs/dev/design/app-redesign/*.jpg`), so the folder shape is proven and the automation is not. It is G's first Done criterion* |
| 3. Claude only | Visual design tasks, and their reviewer, run on Claude, because only Claude can open artifacts. The engine refuses another CLI for the design task of a `ui` milestone in a project with `design.canvas`; a technical design task stays on any model. One reviewer is enough |
| 4. Independent review | A fresh Fable session, read-only, that did not draw: the engine refuses a reviewer whose session or parent is the designer's (run records, §2). It checks every owner-facing surface is drawn, every state present, the brand followed, no rail moved into a prompt, and that nothing costs the owner more attention than it gives |
| 5. Revise | Findings go back to the designer, who revises and re-exports. Rounds are structured records. Cap: the existing two change requests. **One change from today:** after the second, `fable_due` goes false and the task waits for the person (`settings.py:995,1028-1033`). For design tasks the retro ladder comes first (retro round, recap without `continue`), and the owner is asked only at retro step 7. No new cap or alert |
| 6. Split | The milestone's implementation tasks are split **after** the pass, by `plan amend`. Each carries `boards: [ids]` found in `boards.json`, or `boards: []` with a one-line reason ("engine only, no surface"). The brief's Settings block prints the board PNG paths; the reviewer judges the result against them. A task of a UI milestone cannot start before the design task is done: the graph's ready rule, now with a named test |

**Project start.** Milestones run in order, each ending in its acceptance task (main design §4). When a milestone is `ui`, its first plan holds only the design task and non-UI groundwork; the rest arrives by `plan amend` after the pass. "Owner-facing" is the milestone's `ui` flag the coordinator already sets, not a new list. `doctor` warns when a `ui` milestone exists and no `design.canvas` is set or no Claude CLI is signed in.

**The design-system question (owner decision, 2026-11-17).** At project start the factory asks one structured question, in the first-run manner of main design §6: probe first, one recommended answer that one word accepts.

| Answer | What happens |
|---|---|
| (a) Use an existing one | The owner gives its link: a Design System artifact, a repo folder or a brand doc. Saved as `design.system`. Every design brief and design review names it |
| (b) Make one first | The first plan gains a design-system task, drawn and reviewed like any design (steps 1 to 5), before the first screens |
| (c) Skip | Design starts at once; the designer sets a look in the first boards. `design.system` stays empty and the question is not asked again |

The probe decides the recommendation: a brand doc or exported boards in the repo (a `brand.md`, a `design/` folder with images, a `tokens.json`) → "I found a brand doc and exported mockups in this repo. Use them?" with (a) recommended; nothing found and a `ui` goal → (b); a goal with no surface → the question is skipped. The answer is saved as a typed project setting, not a prompt.

**It stays within "at most five".** It replaces "confirm the pools found": pools and models are probe-answered, so they are stated once in the plan summary ("Using Claude and Codex; quota unknown") and asked only when the probe finds no signed-in CLI. The five are then: the goal, which gates you delegate, releases, notify level, design system.

**For sheepdog the answer is (a).** Its design system is what already exists: `docs/dev/brand.md` (palette, type, rules), the `docs/dev/design/` docs with their exported mockup images, and the iOS canvas's spec and shared boards (`ChatSpec`, `TopBar`, `AgentCard`, `Dock`). `design.system` lists the three; `design.canvas` is the iOS canvas, where Part 3 (the factory boards) was drawn today. **No separate design-system artifact now.** `ChatSpec` specifies one surface, the conversation, and should not be stretched into a whole system; the gap it leaves is small (the light and dark token sets are copied into each board), and the export closes it by writing them once as `tokens.json`. Revisit when a second canvas needs the same brand.

**What the owner hears.** Nothing. The plan summary says "design drawn and reviewed" as one clause; the digest counts design rounds with the other rounds. Only if their typed rule says designs wait for them do they get one `gate` ask after the reviewer's pass, with the board link.
