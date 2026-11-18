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
| 4 | Merge stall | `assistant.md:10-11` says accept is policy; `drift_pass` (SDF-48) raises "approved, not merged" at 30 min; SDF-50 fixes `main_red_hold` | **Nothing to add.** `drift_pass` is not in `tick.py` yet; it must land before the switch, SDF-60 |
| 5 | Debug space | Engine doctor exists (`internal/factory/doctor.go`); `web.agent_input` is logged (`web/window.go:95-98`) | No decision log. §3 says "no window" and "never types". Live typing is only byte-counted (`web/term.go:421-441`) |
| 6 | Phone | Agent window, list, quiet rule as above | No group name, no task or model on a row, no factory view in the app |

## 2. Agent launch contract

**Rule (code, in `argv_for` and `resume_argv`): every start, resume and reviewer argv states permission mode, model and effort. The engine never relies on a CLI's config file, and never writes one.**

| CLI | Worker (perm `full`, the only factory value) | Reviewer | Notes |
|---|---|---|---|
| Codex | `--dangerously-bypass-approvals-and-sandbox -m <model> -c model_reasoning_effort="<e>"` | worker-run: `-s read-only -a never`; gate: `-s workspace-write -a never` in a scratch folder | Both worker flags exist on `codex resume` (checked in `--help`, 0.169.0). These reviewers cannot write the worktree |
| Claude Code | `--dangerously-skip-permissions --model --effort` (`settings.py:343-353`) | worker-run: `--permission-mode plan`; **engine-started gate reviewer: the worker's own mode, auto** (`commands.py:865-868`) | The gate reviewer is told not to change files; nothing stops it |
| agy | `--dangerously-skip-permissions --model --effort` (`settings.py:376-383`) | `--mode plan`, and the factory default turns its sandbox **off** (`project.py:12-14`) | Told not to change files; not prevented |

So "reviewers run read-only" is true only of the two Codex reviewers. The design says what is true: **reviewers are told not to change files**, and the run record's `perm` holds the real mode of each run.

**Why bypass and not `--approve-for-me` / `approvals_reviewer`.** The owner decided "full permissions as the user, and that is all" (11-17); the worker sandbox was cancelled on 11-08. Bypass is the one mode in which Codex has no dialog to stop on, it is what Claude and agy workers already get, and it deletes code: the PermissionRequest hook (`launch.py:189-196`, `cmd_allow` `:328-343`), the `OWN`/`GIT`/`helper_hc` matching for Codex (`:258-325`) and the "require_escalated" brief text (`:608-612`) are dead under it. `--approve-for-me` keeps a sandbox, adds a second model whose denial behaviour I could not verify (failure to the model, or a dialog?), and needs the `guardian_approval` feature. Field evidence, 2026-11-17: with `approvals_reviewer = "auto_review"` set in the owner's config, a Codex worker (SDF-45) restarted at about 17:00 still stopped on a dialog for `gh run view … --log-failed`, so automatic review does not cover every escalation (a network command, here). *Not verified by me: reported by the factory's handoff terminal.* Widening `OWN` is a hand-kept list. If the owner wants the guard anyway, it is question 1.

**Model and effort.** Resolution order: task → project setting for the role and CLI. Nothing else: reading the CLI's own default would be the same inheritance that failed today, and a doctor WARN is not something a voice-only owner sees. So a missing model is refused early, where the coordinator is told at once: `add`, `plan apply` and `project start` fail with "no model set for codex workers in this project". **Where the pick comes from:** today the ids are hard-coded (`project.py:11-12`, `commands.py:686`) and `codex_models()` only validates, so on a stranger's first run every `add` would be refused. Instead the start check picks them and prints one line saying which (it is not a prompt): for Codex, the first id its own `models_cache.json` offers (`settings.py:586-596`); for Claude Code and agy, the CLI's own alias (`opus`; *unverified what agy offers as an alias*). A pick changes only by a project setting or a `--model` flag on `factory start`. The hard-coded ids go. No model list is kept in sheepdog. The reviewer fallback chain (`commands.py:686`) uses the project's saved pick for each CLI and skips a CLI that has none. Nothing can then reach `launch()` without a model; if it does, that is a bug with a test. The alternative is question 3.

**Why `perm` was unset today.** Likely `worker_pick`: it carries `perm` over to a task whose agent differs only when the project's worker setting has one (`settings.py:396,403`), so a Codex task in a project with no `perm` gets none (field note 56; I did not re-run it). The rule above removes the dependence either way.

**Run record** (added to `task.agents[]`, `task.reviewers[]` and `review_runs[]`, written in `launch()` `launch.py:916`):

| Field | Meaning |
|---|---|
| `agent`, `argv`, `at`, `address` | exist |
| `model`, `effort` | what the argv asked for; never null after this change |
| `model_source` | `task` \| `project` |
| `perm` | the real mode of that run: `full`, `auto`, `plan`, `read-only` or `workspace-write` |
| `model_used` | read from the CLI's session file by `_track`; `null` means unknown, never a guess. *Unverified per CLI* |
| `resumed_from` | address of the run this one resumed |

**Round record** (`reviews[]`): `reviewer_agent`, `reviewer_model`, `reviewer_effort`, `worker_model`, `worker_effort`, copied by the engine from the two runs. This belongs to the retro's "structured rounds" task (B5 task 1), not to SDF-54: one owner for the schema. `show` prints both.

**Resume.** `resume_argv` rebuilds flags from the current rule and the recorded `model`/`effort`, and keeps only the session id from the old run. A task started before the change resumes with full flags. A resume on another model is a new run with `resumed_from`.

**First-run notice** (printed by `factory start`, spoken once):

> Factory agents work without asking. Each worker runs as you, with full access to this Mac, limited only by its brief and its own worktree. That is not a sandbox. Reviewers are told not to change files. Sheepdog names the model for every agent and does not read or change your Claude, Codex or agy settings. To stop it: `sheepdog factory stop`.

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
| (c) debug section | **Trim.** A collapsed row at the foot of the factory section of the space page (§5); it shows the last 20 log lines and the doctor lines. Model and rounds are fields on the task row. No embedded terminal: a row opens the Agent window |
| (d) `factory doctor` | **Merge** into the existing command as a "running" part. It prints stage and `drift_pass` results and computes nothing new: one lease holder per project, panes on a dialog, pools, main red, stale asks |

**Log line** (one record per decision, stored as JSONL: `at`, `project`, `by`, `wake`, `task`, `decision`, `why`, `model`). The engine writes all of them: its own decisions, and one for each engine verb the coordinator runs (`accept`, `start`, `add`, `recap --decide`, `plan amend`, `review-more`), with that verb's reason as `why`. Free text such as `progress` is not parsed. `sheepdog factory log` prints each record in lines of at most 46 columns, so it reads in a phone terminal:

```
14:05 coordinator  SDF-57 add
      wake: event:drift
      why: fix main, watchdog crash
      model: claude-opus-5-5
14:09 owner        typed
      wake: input:phone
      why: to coordinator, 34 chars
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
| Listing and order | **None.** Keep the one Needs you / Working / Done sort shared with the daemon. No Factory group in the Agents list. Owner decision, 2026-11-17: a per-project group with a jump button on its header was considered and deferred; the smallest version keeps the factory in one place, the space page. Revisit if three taps to reach it proves too deep in trial use |
| Role, task and model on a row | **New, small:** Swift decodes `labels`; the card's kind line reads "codex · worker", and a third line shows the model from a new `sheepdog.model` label stamped in `launch()` (`launch.py:891-893`) |
| The crossed bell | Keep the glyph; change the words for a factory row to "Factory, not announced" (VoiceOver too). The daemon sends the reason beside `muted`, so the app does not guess from labels |
| Blocked at a prompt | **New copy only:** the card says it waits at a prompt and offers Open window. Under the launch contract of §2 this state should be rare; it is the one `doctor` points to |
| Factory ask on Agents | **New:** the stock approval card (`ApprovalCardView`: every option outlined, a Recommended tag, never a fill, `ApprovalCard.swift:360`) shown as a project-level card under Needs you, and the badge counts it. The same card heads the factory section. Drawn once, as one component |
| Conversation | **No new row kinds.** A digest is the voice bubble with Details; an alert is the agent-update row |
| Agent window | Unchanged, plus one `NoticeBar` line: "Factory" and the model. No marks inside the terminal |
| Owner input logged | §4; daemon only |
| The factory view | **Owner decision: the factory section of the space page**, not a separate `/factory` web page. A space without a factory looks exactly as today plus one row, "Start a factory here". A space with one shows a Factory section at the top: the ask card, the milestone, and tasks in dependency layers with the critical path in ink and the model on each row. A row with a live terminal opens its window; a row without one is not tappable. Titles wrap. No Debug row until task E exists |
| Detecting a factory | **Derived, no list, by label.** A herdr-core space's path is its first pane's folder (`supervisor/spaces.go:218-236`) and workers start in their worktrees (`launch.py:904`), so matching a space's path to a project folder breaks: the section would vanish when the start terminal closes, and two spaces in one folder would both match. Instead: a space has a factory when one of its panes carries `sheepdog.project=<slug>`; the daemon looks that slug up in the engine's `projects` (`internal/factory/contract.go:242-253`) and adds an optional `factory: {project, status, open_asks, active_milestone}` to the space in `GET /api/spaces`. Detail comes from one new route, `GET /api/spaces/{id}/factory` (the engine's `graph` and open asks), beside `/worktrees` (`internal/web/server.go:528-532`). The app never reads labels to decide |
| A space for the project | Factory terminals carry no `herdr.space` label today (`launch.py:891-897`, `lease.py:217`), and herdr-core builds a space only from that label (`herdrcore/spaces.go:33,53`). **New, engine:** `factory start` records the id of the space it runs in **in the project record**, and every start of a coordinator, worker or reviewer stamps that id, so restarts and re-parenting keep them in the space. **Started from a plain terminal with no space:** `factory start` asks the daemon to open a space for the folder (`POST /api/spaces`, `server.go:529`) and records its id; with no daemon running it starts without one, says so in one line, and adopts a space the first time the folder is opened as one. **Rail, daemon:** Close space is refused while the project's coordinator lease is live |
| iPad | The space page exists only inside the Spaces form sheet (`ios/Sources/Sheepdog/SpacesView.swift:7,52,270`, `MainView.swift:664`), and sidebar space names are plain text (`IPadRootView.swift:244`). So the factory section is drawn **inside that sheet, in the phone layout**, light and dark. No new detail-pane screen and no new control on the sidebar's space names. All agents shows the ask card and factory rows in its two-column sections; the sidebar lists a factory's terminals under its space once they carry the space label |
| The coordinator | It is not silenced today (`internal/herdr/factory.go:28`), which contradicts "only asks, alerts and digests". **Decision:** its done and idle turns are silent like a worker's; its blocked turn still speaks, because a blocked coordinator stops the factory |
| Stopping a factory | **New, one action:** `sheepdog factory stop` and the same by voice (contract action `stop`). Today the contract has only answer, say, propose_rule and revoke_rule (`internal/factory/client.go:413-496`) and no stop verb. Setting the project `stopped` is not enough: that status already means held by an alert or no live coordinator with open tasks (`docs/dev/factory/owner-contract.md:104`), and reconcile would start a coordinator at the next tick. So `stop` writes an **owner-stop mark** in the project record. Reconcile, the watchdog and revive honour it and start nothing; the project reports its own reason, `stopped by the owner`, so no alert is spoken. The section shows "Stopped by you" with one fix, the same start row. A second `factory start` in that folder clears the mark and resumes; doctor does not call it "already running". "Running" for the Close-space rail means a live coordinator lease |
| Words | Do not call the factory rule "quiet" in the app: say "not announced" |

Swift work: decode `labels` and the space's `factory` field, the row lines and words, the project-level ask card and badge, the notice line, the factory section and the one start row. No new screen.

**Starting a factory (owner decision: no new UI beyond the entry point).** The entry is one row on a space page that has no factory, "Start a factory here", beside "Run a command…". It runs `sheepdog factory start` through the existing `RunLine` (`internal/supervisor/spaces.go:765`): a new tab of that space. Opening that tab's window so the owner can type in it is new work: today the app discards the pane `runLine` returns (`ios/Sources/Sheepdog/SpacesView.swift:684-686`) and its window route is for agents. The row is greyed, with the reason under it, when the phone shell is off or the folder is outside the project folders (`internal/web/shell.go:54-80`). Everything else happens in that terminal:

1. Doctor runs first and prints its output as it is: one short line per check, and under each failing or warning line a plain `fix:` line, at most 46 columns. A failure waits for Return to check again; nothing is asked until it passes. Three things stop the start: no signed-in CLI, a folder that is not a git repository, a factory already running there. `gh` not signed in is a warning ("until then: merges stay local"). The same pass prints the model picks of §2.
2. The permissions notice of §2, in full, once.
3. At most five prompts, one at a time. Return accepts the recommended answer:

| # | Prompt | Recommended answer | Saved how |
|---|---|---|---|
| 1 | What should it build? | none; it must be given (`--goal`) | directly |
| 2 | Which gates do you delegate? | **None.** Designs and plans go to the Fable gate, which is the product default and not a delegation | directly |
| 3 | Releases | **Ask me each time** | directly |
| 4 | Notify level | **Asks and alerts** | directly |
| 5 | Design system (§9) | from the probe: use what was found; else make one for a UI goal; else skip | directly |

4. The plan summary, then it starts. The space page now shows the factory section.

**Rail for prompts 2 and 3.** A `rule` is a gated kind (`settings.py:994`), and §4 says input to a pane never answers a gated ask; a full-permission worker can type into this terminal too. So an answer that would **widen** delegation (delegate a gate, or releases) is only proposed: the engine raises one `rule` ask, and it takes effect when that ask is answered by a proven tap or proven words. The recommended answers delegate nothing and are saved directly.

**Flags, for voice and scripts.** `sheepdog factory start --goal "<words>" --yes` takes the recommended answers and asks nothing; `--goal-file <path>` gives the goal without putting it on the command line. `--yes` never delegates.

**By voice.** The brain cannot type into a shell: its only typing tool is `prompt` to a roster alias (`internal/brain/tools.go:95-140`, `spawn.go:24`), a shell is not an agent (`herdrcore/mapping.go:76`), and terminal prompts never become questions. So voice is one fixed-command brain action, with three rules in code:

- **Read back first.** Main §9 says goals are read back with the project name before they are sent. The action says "Start a factory in blog-engine to build …?" and runs only on a yes.
- **The goal never enters the shell line.** `runInSpace` types the line and Enter (`internal/supervisor/spaces.go:782-803`) and `checkWrapUp` only limits length and newlines (`:643-655`), so a quote, a backtick, `$(…)` or `!` in the spoken goal would be run by the shell. The daemon writes the goal to a file and the line is exactly `sheepdog factory start --goal-file <path> --yes`. `--goal "<words>"` stays for a person typing at a terminal.
- **A space first.** `RunLine` needs a space id; when the folder has none open, the action opens one through the existing `POST /api/spaces`, then runs the line.

It speaks one line if doctor fails ("It can't start: no agent CLI is signed in on your Mac"). Prompts in the terminal are typed, never spoken. The action changes the brain's tools, so it needs `make eval`. `runInSpace` today logs `via: user` and tells the brain "the user ran … from the phone"; for this action both must say it was run by voice.

Nothing here needs a sheet, a checklist screen or an iPad variant.

## 6. Changes to the main design

| Section | Replace or add |
|---|---|
| §2 after "Never narrated" | Add: "Silenced means no speech, no push and no question card for that terminal. It never hides a terminal: every factory terminal is in the phone's agent list, in its usual section, and opens in the Agent window. In the app the words are 'Factory, not announced', never 'muted' or 'quiet'." |
| §2 touchpoints | Add a sixth, optional row: "Look in or step in: open any factory terminal on the phone; typing is logged as the owner's." |
| §3 Coordinator | Replace "in an hc terminal, no window" with "in an hc terminal, listed on the phone like every factory terminal". Replace "The owner never types in its pane" with "The owner never needs to type in its pane. When they do, the input goes through the daemon, is logged as the owner's, and never counts as an answer to a gated ask." |
| §3 Owner contract | Add actions `owner touch` and `stop`; `graph` tasks gain `terminal`, `model`, `rounds`. The daemon adds `factory` to a space, joined by the project label |
| §2 "Never narrated" and alerts | Add: "The coordinator's done and idle turns are silent too. Its blocked turn speaks." |
| §5 Worker permissions, reviewers | Replace "Reviewers stay in the macOS sandbox profile" with "Reviewers are told not to change files. Only the Codex reviewers are prevented from writing the worktree; the run record holds each run's real mode." |
| §5 Worker permissions | Add: "The engine's argv states permission mode, model and effort on every start, resume and review. Nothing is inherited from, or written to, the owner's CLI config." |
| §5 Quota and models | Replace "(share left, time to reset, from `codexbar`)" with "(share left, time to reset, from the quota probe; a pool it cannot read is `unknown` with its reason)". Add: "Every run and every round records model and effort." |
| §6 first run, step 3 | Replace "confirm the pools found" with "the design system: use an existing one, make one first, or skip (§9)". Pools found are stated in the plan summary, not asked |
| §6 cannot-be-bundled, `codexbar` row | "Needed for: quota facts (optional). Without it: pools read `unknown`; switch only on a provider error." |
| §4 surfaces | Replace the row "One read-only page, `/factory`, served by the daemon" with "The factory section of the space page, on the phone and iPad: first what needs the owner, then the milestone, tasks in dependency layers, the critical path marked, one line of why each task waits, and a collapsed debug row. A task row opens its worker's window. A space without a factory looks as it does today." Everywhere else, "the `/factory` page" becomes "the factory section of the space page" (§4 "No board…", §7 Go estimate, §8 phase 4, §9 decision 5 and the unverified list). "A native iOS view only if the page gets used" is deleted |
| §6 first run, steps 2 and 3 | Replace with: "Start a factory from a space page ('Start a factory here') or by voice. The row runs `sheepdog factory start` in a new tab of the space. Doctor runs first and prints its output; then at most five prompts are asked in that terminal, each with a recommended answer that Return accepts. An answer that widens delegation takes effect only through a `rule` ask with a proven answer. By voice, one fixed command runs it with the goal and the recommended answers." |
| §4 Plan row | Add: "Tasks of a UI milestone are split after its design passes, and each names the boards it implements." |
| §5 Designs and plans row | Add: "Visual design is drawn on the project's canvas, exported read-only into the repo with a PNG per board, and reviewed by a session that did not draw it. After two change requests the retro ladder runs before the owner is asked." |
| §8 Do not build | Add: "a second terminal UI; a debug dashboard beyond that section; a quota reader that drives a CLI's screen" |
| §9 Unverified | Add the list below |

**Unverified, in one place:** `--approve-for-me` denial behaviour; that Codex's bypass mode shows no first-use confirmation; who pasted the codexbar line; `model_used` from each CLI's session file; Claude and agy default-model lookup; `isAgent` for wrapped worker commands; the ask pane and gated kinds; `hc` input source; app deep link; why `perm` was unset on the running project (its settings were not read).

## 7. Tasks and order

Ids are the store's. **The switch is SDF-60** (after SDF-53, 55, 54, 58, 18, 19 and 48); SDF-15 is the embed task and is done. Everything in the first table lands before SDF-60, so the switch does not carry today's failures over.

**Before the switch**

| # | Task | Scope | Size | Done |
|---|---|---|---|---|
| A | **SDF-53** (running) | Permission mode always stated for start and resume; factory default `full` when unset; resume rebuilds flags; first-run notice. Reviewer scope **deleted** (already `-a never`). Then delete the Codex hook path and its brief text, keeping the trust roots of `own_argv` (`launch.py:193-194`) | Python ~80, minus ~120 | With a `CODEX_HOME` that holds only the sign-in, and again with one whose `config.toml` sets a weak model, `on-request` approvals and a read-only sandbox, a Codex worker runs `make test`, a socket test and a network command (`gh run view <id> --log-failed`) with no dialog and on the named model, on a fresh start and again after a resume; a task started before the change resumes with the flags; no test reads the real config |
| B | **SDF-54** | Model and effort stated on every argv, with `model_source`; run record fields; `model_used` from `_track`; `show`. Fallback reviewer no longer `None`. Round fields move to the structured-rounds task; the model picks line in the start check (§2) replaces the hard-coded ids | Python ~150 | No argv without a model in the suite; `show` prints requested and used model per run; `add` without a resolvable model is refused with the fix; a fresh `CODEX_HOME` with only a sign-in reaches a first `add` with a model, with no id typed |
| C | **SDF-55** (running) | Retitled "quota unknown is a value": `reason` on pools, pool table wording, the brief line, digest wording, delete `usage.pick`. No template removal (there is none) | Python ~60 | With no codexbar on PATH: a task goes from `add` to merged, the wake shows `unknown (not installed)`, no agent runs a quota tool in the replay set |
| D | **SDF-58** | JSONL log, the CLI view, the contract action, `web.term.input`, watchdog hold, the gated-ask refusal test | Python ~120, Go ~100 | Each of the three line kinds appears in a scripted run; phone typing into an ask pane of kind `release` is refused |
| S | **SDF-18** `sheepdog factory start` | It owns the whole command: doctor first, output at 46 columns with a `fix:` line per failing item, the model picks line, the notice, the five prompts with their recommended answers, `--goal`, `--goal-file` and `--yes`, the plan, the start; recording the space id; **`sheepdog factory stop`** and the contract `stop` action | Go ~250, Python ~120 | In a 46-column terminal no line wraps; each of the three stopping states prints one line and one fix; Return alone accepts every recommended answer and delegates nothing; **a typed answer that delegates a gate or releases creates no rule until a `rule` ask is answered with daemon proof (a refusal test)**; `--goal … --yes` starts with no prompt; `stop` ends the coordinator and workers and writes the owner-stop mark; **the project stays stopped across a tick and a daemon restart, with no alert; a second `factory start` resumes it instead of failing doctor's "already running"** |
| — | SDF-19, SDF-48 | Unchanged; SDF-48 is already among SDF-60's blockers | — | — |

**After the switch.** G is the design loop. F1, F2, F3, E and H are owner-facing; they wait on **this design review passing**, not on G.

| # | Task | Scope | Size | Done |
|---|---|---|---|---|
| G | **SDF-59** | the design-system question and `design.system`/`design.canvas` settings, export folder with PNGs and hash check, Claude-only rule for visual design, independent reviewer check, `boards` on tasks of a UI milestone, brief lines, designer and design-review template lines with two replay situations | Python ~200, templates ~25 lines | A UI milestone's task with no `boards` is refused by `plan apply`; a visual design task cannot go ready without a fresh export; a hand edit in the export folder is refused; a non-Claude CLI or the drawing session is refused as its reviewer |
| F1 | New: daemon and engine for the space | The space id in the project record and its label on every factory terminal; `factory` on a space by the label join; `GET /api/spaces/{id}/factory`; Close space refused while the coordinator lease is live; the coordinator's done and idle turns silenced, blocked kept | Go ~150, Python ~50 | After the start terminal closes and after a coordinator restart, the space still reports its factory and lists its terminals; two spaces in one folder do not both report it; Close space is refused with the reason; a coordinator finishing a turn causes no speech or push |
| F2 | New: Agents-list wiring | Swift decodes `labels`; role, task and model on the card; "Factory, not announced" words (VoiceOver too); the blocked-at-prompt copy; the project-level ask card on Agents with the badge; the window's one line | Swift ~200, Go ~30 | On the fixture daemon a factory worker is listed in its state section with role, task and model; VoiceOver says "Factory, not announced"; a factory ask shows on Agents and raises the badge; no speech or push in the run |
| F3 | **SDF-35**, retitled from "M4: The /factory page" to "M4: The factory section of the space page" | The section on the phone and inside the iPad Spaces sheet, in every drawn state (running, nothing needs you, planning, finished, stopped, stopped by you, could not load); rows with a live terminal open its window; the largest text size; dark | Swift ~300 | A space with no project shows no section and one start row; a space with a running project shows the section; every drawn state has a test; titles wrap at the largest size |
| E | New: doctor's running part and the Debug row | Doctor prints the existing checks at 46 columns; the Debug row at the foot of the section shows the last log lines and doctor lines | Go ~80, Swift ~60 | A pane left on a dialog and a stale ask each show as one doctor line with a fix; the Debug row is absent until this task ships |
| H | New: the start row and the voice action | The one row on the space page that runs the command through `RunLine`, greyed with its reason when the shell is off or the folder is outside the project folders; **opening the new tab's window and accepting typing in it**; the one fixed-command brain action for voice: read-back, goal by file, a space opened first when there is none, one spoken line on a doctor failure, and logging that says it was run by voice | Swift ~80, Go ~120 | The row opens the terminal running the command and the owner can type answers in it; "start a factory in <project>" reads back the project and goal and runs only on a yes; the line run is exactly `sheepdog factory start --goal-file <path> --yes`; **a goal containing quotes, a backtick, `$(` and `!` reaches the engine unchanged and runs nothing (a test)**; with no space open, one is opened first; the log and the brain's note say voice, not "the user ran … from the phone"; `make eval` shows no Safe regression. No new screen exists |

**Store changes:** cancel SDF-34 (a separate page is no longer built); SDF-35 is retitled as above and becomes F3; SDF-37 (M4 acceptance) checks the factory section of the space page, not a `/factory` page; SDF-60 keeps SDF-48 among its blockers.

## 8. Questions for the owner: decided

KC approved the design and asked for implementation, so the three recommended answers stand:

1. Codex workers run with full bypass, not `--approve-for-me`.
2. Owner typing is logged by length, not by text.
3. A role with no model is refused at planning time; the start check supplies the picks (§2).

## 9. Design loop before implementation (owner requirement, 2026-11-17)

**Why.** This factory was built from a text-only design. Owner-facing work (asks as cards in SDF-22, the quiet rule in SDF-23, the planned factory page) was merged or planned with no drawing, so nobody could see that "no window" removed the owner's only way to look in.

**Source.** This is the practice another of the owner's projects already runs by hand (its design README, "Where design output lives" and "Who reviews what"), made product behaviour: technical design in docs; visual design changed only in the artifacts; the repo's design folder a read-only export with a PNG per board; visual design tasks on Claude; milestone design reviewed by Fable before the milestone's tasks are split.

**Fit.** It is the existing design task made stricter, not a second mechanism. `plan apply` already refuses a UI milestone without a design task that the other tasks come after (`plan.py:305-318`, milestone key `ui`, `plan.py:21`); a Fable round already judges design tasks (FAC-99, `commands.py:909`) with a cap of two change requests (`FABLE_CHANGES`, `settings.py:995`). FAC-94's "wait for the person" stays the opt-in typed rule.

| Step | Rule (in code unless marked) |
|---|---|
| 0. Two kinds of design | **Technical** design (architecture, task split, acceptance) is a document under `docs/`; any model. **Visual** design lives only in the project's artifacts, named once in a project setting `design.canvas` (and `design.system` if there is one) |
| 1. Draw | A UI milestone's design task is drawn by a design agent on the canvas: every new screen as an artboard, with empty, error and dark states, plus flows and diagrams. Nobody edits visual design in the repo |
| 2. Export | `docs/<project>/design/` is a **read-only export** of the artifacts: board sources, a PNG of every new or changed artboard, and `boards.json` (`id`, `title`, `file`, `png`, `canvas`). Re-exported after every artifact change, never edited by hand: a hand edit there fails `ready` (the export is compared with its recorded hash). Codex and agy workers and reviewers check the UI against the PNG. `ready` on a visual design task refuses without a fresh export. *Unverified: how an export is produced unattended. Both repos already hold exports made by hand or by an agent (the other project's canvas and design-system exports; sheepdog's `docs/dev/design/app-redesign/*.jpg`), so the folder shape is proven and the automation is not. It is G's first Done criterion* |
| 3. Claude only | Visual design tasks, and their reviewer, run on Claude, because only Claude can open artifacts. The engine refuses another CLI for the design task of a `ui` milestone in a project with `design.canvas`; a technical design task stays on any model. One reviewer is enough |
| 4. Independent review | A fresh Fable session that did not draw, told not to change files: the engine refuses a reviewer whose session or parent is the designer's (run records, §2). It checks every owner-facing surface is drawn, every state present, the brand followed, no rail moved into a prompt, and that nothing costs the owner more attention than it gives |
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
