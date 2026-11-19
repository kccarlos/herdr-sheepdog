# Old engine (herdr_task, Python) structural critique — notes

Scope: the repository at main 4df3a038, factory/mods/tasks/bin/herdr_task/ (21,460 lines, 44 .py modules, `wc -l` 2026-11-18).
All paths below are relative to that folder unless stated. file:line = opened by me. UNVERIFIED = inferred.
Read-only critique; behaviours worth keeping are another team's job.

Inputs already known: docs/dev/factory/engine-audit.md (module table at 1a4e064e), scratchpad sdf54-split.md
(root cause of SDF-54: launch plan stored twice, re-derived in four places), task records SDF-67/69/70/73/74
(each ran 5/5 review rounds patching case by case; re-planned to "build one table first").

Not read: SDF-53's record and the body of the retro doc (only its headings). Notes written 2026-11-18, ~50 tool calls.

## Symptom 6 — is machine.py the state machine? REFUTED premise, CONFIRMED underlying fault
- machine.py is NOT a task state machine. It samples the host: CPU/memory/disk and CLI plan limits
  (machine.py:1-9 docstring; defs `sample` :186, `hold_reason` :208, `claude` :385, `codex` :549). Nothing in it
  touches a task.
- There is no transition interpreter anywhere. The stored `state` field has 6 words (todo, scheduled, queued,
  running, ready, done). Writes of `state` (any variable name, incl. tuple assignments): 15 sites in 4 modules —
  commands.py 11, tick.py 2, automations.py 1, github.py 1. autopilot.py writes none. Full list in "(b)" below.
- But `state` is only a fraction of the real state. The displayed status is DERIVED, "never stored"
  (agents.py:159-166 comment, STATUSES 8 words), by stage.py's ordered first-match table
  (stage.py:1, STAGE_TABLE :346, resolve_stage :364) from ~a dozen side fields: attention, recap, retro_run,
  review_runs, review_quota_wait, push_held, backup, device_wait, pr, retry, ended_at, done_at, archived…
  (read in tick.py:193, :402, :414, :470, :476, :583, :817).
- Count (grep, lower bound, only vars named `t`/`task`): 304 direct field writes/pops/setdefaults on the task
  record, 109 distinct field names, spread over 17 modules: commands 160, tick 51, retro 21, budget 17, github 16,
  news 6, settings 5, devices 5, automations 5, simulators 3, queues 3, checks 3, archive 3, plan 2, launch 2,
  takeover 1, ghnews 1. Fields written from >=3 modules: attention (5: automations, budget, commands, retro, tick),
  attention_at (4), queued_at, progress, pr, auto_merge (3 each).
- `attention` is a free-text string that doubles as a state flag: code tests it with string prefixes, e.g.
  `(t.get("attention") or "").startswith("Couldn't start:")` at commands.py:1828, :1883, :2114, :2039
  (+ REVIEW_RESUME_WAIT), REVIEWER_FAILED at :1819; budget.py:446 compares attention to recap title.
  Changing a sentence changes behaviour.
- Missing abstraction: one task state type (enum + typed sub-records) with one transition function; derived
  views read it, nothing else writes it. Evidence it hurts: SDF-67/74 (what a re-plan resets: "each round finding
  blockers in state a reset leaves behind") and SDF-69/73 (Fable round never starts) were both re-planned to
  "build one state table first" after 5/5 review rounds each (herdr-task show SDF-73, SDF-74).

## Symptom 2 — launch plan stored twice, re-derived in ~4 places: CONFIRMED (and one place forgets)
- Home 1: task fields agent/model/effort (+ reviewer/reviewer_model/reviewer_effort), set by set_opts/set_task.
- Home 2: `t["queue"] = {"argv": [...], "parent", "custom"?, "agent"?}` frozen at start time
  (commands.py:466 builds `plan`, :479 `t["queue"] = plan`; schedule() also takes the plan :472).
- Home 3: each launched agent's record keeps its own argv (`prev["argv"]`, used by resolve_again_argv
  commands.py:2003-2004 and revive.py:320).
- Consumers: tick.py:449 `begin(s, t, q.get("argv") or worker_argv(s, t))`; cmd_start :462-463 same fallback.
- Re-derivations, each with different guard conditions:
  1. cmd_edit commands.py:1825-1827: refresh if `q.argv and not q.custom`.
  2. refresh_worker_plans commands.py:1877-1881: refresh only if state in (queued, scheduled) and not custom
     and not q.agent and not t.agent — a stricter, different rule.
  3. retarget commands.py:2111-2113: `q.argv and not q.custom`.
  4. plan amend plan.py:369-370: `set_task(old, k, str(v))` for every `set` key on NOT_STARTED tasks and
     never refreshes queue.argv → a queued task keeps its old model after plan amend. refresh_worker_plans is
     called only from commands.py:1940 and :1949; plan.py never calls it (grep).
  5. resolve_again_argv commands.py:1999-2005: uses `pick_at` vs `prev.at` timestamps to decide whether the
     task fields or the previous agent's argv win.
- "Couldn't start:" attention clearing is copy-pasted in all of 1-3 (commands.py:1828, :1883, :2114) and in
  start_again (:2039).
- Missing abstraction: store intent (role, cli, model, effort, perm) once; build argv at launch only, in one
  function. sdf54-split.md T1 says the same ("queue stores intent not argv").

### Symptom 6 detail
Writes of the stored `state` word, with enclosing function: commands.py (11): :183 scheduled (schedule), :195 todo
(unschedule), :410 running (begin), :477 queued (cmd_start), :1264 running (back_to_work), :1306 ready (finalize),
:1394 running (cmd_review), :1744 queued|todo (return_to_waiting), :1777 `= state` (cmd_state: done/reopen),
:2072 running (cmd_resume), :2697 ready (cmd_accept). Outside commands.py (4): automations.py:387 queued,
github.py:1348 done, tick.py:306 ready (settle_back), tick.py:328 queued (due_pass). (queues.py:89 writes an
after_ext dependency's cached state, not a task's.)
Each site also hand-pops its own set of side fields, e.g. begin() pops 7 fields (commands.py:416-417) while
automations.py:387-388 sets state+queue with none of begin's clean-up. Inline state-set tuples (`in ("running","ready")`
etc.): 47 occurrences; named sets duplicated: plan.py:20 NOT_STARTED, lease.py:77 OPEN, simulators.py:12 ENDED,
agents.py:162 STATUSES, base.py:98 RANK.

## Symptom 3 — launch paths: CONFIRMED, 11 distinct spawn paths, 4 different spawn mechanisms
Common core: launch() launch.py:770-830 (`hc run --bg` at :805). It branches on role and CLI internally:
worker perms only when no role (:776-777), own_argv for workers (:781-782), sandbox+trustedWorkspaces for
sandboxed reviewers (:783-784), own_argv with verbs for agy/claude reviewers (:785-787), Codex reviewer gets its
prompt typed later via send_when_clear (:788, :817-818), agy gets `-i` (:790), workers get `--keep`, reviewers not (:801).
Paths (argv builder → spawn):
 1. Worker first start: cmd_start commands.py:448 → begin :402 → launch :431. argv = `--argv` | worker_argv(agent) |
    frozen q.argv | worker_argv (:462-463). Message = brief().
 2. Queued/scheduled start by the tick: tick.py:449 begin(q.argv or worker_argv). Different source of truth than 1.
 3. Automation-started task: automations.py:383-390: worker_argv then begin, or writes queue itself (bypasses cmd_start).
 4. Resume / switch / start-again: start_again commands.py:2008 → resolve_again_argv :1999 (pick_at vs prev.at decides) →
    launch with resume argv (:2029, resume_argv launch.py:327) or fresh with handoff (:2037). Callers: cmd_resume
    :2075, cmd_switch :2139, :2290.
    Note: the fix round (cmd_fix commands.py:2743) is NOT a launch; it messages the running worker via tell() (:2756).
 5. Reviewer round (normal + Fable): commands.py:895-935. Per-CLI permission chosen inline (:901), argv via
    codex_review_argv (settings.py:401) or argv_for (:903); verdict protocol differs: sandboxed = parsed from terminal with
    a round code, else `herdr-task review` CLI (:911-928).
 6. Retro round: retro.py:231, argv_for({**FABLE,"perm":"dontAsk"}), role="reviewer", verbs=() (no verdict verb).
    Model hard-coded via FABLE constant settings.py:1144.
 7. Takeover/backup: takeover.py:219, backup_argv takeover.py:140 = CLI defaults ("its own model"), so a takeover
    ignores the task's model/effort pick entirely; failure swallowed into t["backup"].state (:220-222).
 8. Revive after crash/reboot: revive.py:320-322 rebuilds argv with its OWN resume_argv (revive.py:113, a second copy of
    launch.py:327) and its own flag tables (revive.py:41-48), and does NOT go through hc run: it types a shell line into
    the existing terminal with `hc send` (revive.py:358). Records kept in terminal labels (revive.py:172).
    revive.resume_argv (revive.py:113-117) wraps launch's (`launch_resume`) with a Claude-only exception: a second
    resume rule, not a copy.
 9. Assistant/coordinator: lease.py:246 `hc run ... tpl_name` from a one-off template file written to disk (:235-239)
    and deleted after (:251-252). No launch(), no labels helper.
10. Ask pane: ask.py:136 `hc run --bg --outlive-parent --name ask-<id>`.
11. Check runner: checks.py:97 `hc run --bg --no-parent` in a temp dir; cross-machine actions link.py:98 `hc run --rm`.
Recap is NOT a launch: open_recap tells the running worker to stop and write RECAP.md (budget.py:470-476 docstring).
Count: 11 spawn paths if worker-start, queued-start and automation-start are counted apart (they all end in begin);
7 distinct argv builders (worker_argv, argv_for, codex_review_argv, backup_argv, resolve_again_argv, revive plan,
lease template). Missing abstraction: one Launch(role, intent) → (argv, env, labels, prompt delivery, verdict protocol) table; one spawner.

## Symptom 4 — size recap never firing: CONFIRMED, three separate causes
- Where it lives: size_triggers budget.py:430-442, called only by size_pass budget.py:479-484, called from ONE place:
  cmd_ready commands.py:535. The tick's budget_pass (tick.py:376, budget.py:470) uses triggers() budget.py:398-427 which
  checks non-convergence, CI, time and tokens — NOT size. So size is evaluated only when the worker itself runs `ready`.
- Cause A (path): a task that keeps working, or whose rounds go via review/fix without a `ready`, is never size-checked.
  Folder (non-worktree) tasks never: change_sizes returns None without a live worktree (budget.py:216-218).
- Cause B (estimate parse, the decisive hole): size_estimate = `estimate_lines` else brief_lines(prompt)
  (budget.py:64-66). BRIEF_LINES (budget.py:21) matches only a number IMMEDIATELY followed by whitespace + "lines".
  Tested with the same regex: "About 150 Python lines plus tests." → no match; "about 150-300 lines" → (150, 300).
  So "150 Python lines", "150 lines of Go"-style phrasing… any word between number and "lines" → no estimate →
  `if not est: return []` (budget.py:437): the size check is silently off. It is also the max of all matches
  anywhere in the brief when there is no "Size:" line (:58-61), so a stray "500 lines" elsewhere inflates it.
- Cause C (one continue = forever): after a recap decided `continue` with no reasons, or with any "twice its size" reason,
  or recorded at >2x, size_triggers returns [] for the rest of that decision (budget.py:437-439) — no 4x/10x/20x rung.
  Only the "small continue" carries a bound (:433-436).
- Also: budget_pass is wrapped in `except Exception` that only appends to a report dict (tick.py:375-378).
- Missing abstraction: a size budget evaluated on every head change (event), from an explicit estimate field, with an
  escalation ladder; not a regex over prose run on one CLI verb.

### Symptom 4 — field evidence from the live store (herdr-task show --json, 2026-11-18)
- SDF-54: brief says "About 150 Python lines plus tests" → regex does NOT match → no estimate. Grew to ~+2,841/-379
  (journal 2026-11-18-sheepdog-factory.md:122). Record has no `size` key and no recaps despite 5 reviews, so cmd_ready's
  size block (commands.py:531-535) also never stored a size (why: UNVERIFIED).
- SDF-73 and SDF-74: no "<n> lines" phrase in either brief → size_estimate None → `if not est: return []`
  (budget.py:437). SDF-74 has size.changed = 2,298 with no estimate key; the check was silently off. The size check is
  opt-in by prose, and nothing says it is off.
- So all three 10-20x tasks escaped the size rung through the SAME hole: no parsed estimate → check off, silently.
  Even with an estimate, the check runs only on `ready` (cause A) and only once per continue (cause C).

## Symptom 1 — non-convergence across homes: CONFIRMED
One fact, several homes, no reconciler that owns it:
- Launch plan: task fields vs queue.argv vs agents[-1].argv (see symptom 2). plan amend updates one (plan.py:369-370).
- Liveness of an agent: task.agents[] records with `ended` (tick.py:476) vs hc's live terminal list (agents.py:41
  `k.get("state") == "live"`) vs revive records stored in terminal labels (revive.py:172) vs lease record for
  coordinators (lease.py:3-8) vs screen scraping (`hc read` in 9 modules, 14 sites: agents, checks, lease, launch, news,
  machines, commands, revive, watchdog).
- PR: t["pr"] cache refreshed by polling every PR_EVERY=60s (base.py:102; tick.py:120) or 20s while checks run
  (tick.py:49), plus ghnews.py polling reviews/comments, plus github.py merge path setting done (github.py:1348).
- Dependencies: `after` vs `plan_after` (31 references) and cached other-project states in after_ext[].state polled
  every EXT_EVERY (queues.py:76-90).
- Brief: `prompt` vs `plan_prompt` (22 refs) + `amendments` + review_followup + retro_parent folded in at different times
  (commands.py:427-428, plan.py:367, :378-379, :400-401).
- Model actually used vs requested: SDF-54 had to add `model_used` (sdf54-split.md T2) because nothing records it.
- Missing abstraction: a single owned record per entity (task, run, PR, lease) with one writer; external worlds (hc, gh)
  observed by events into that record, never cached ad hoc in many fields.

## Symptom 5 — hand-maintained lists: CONFIRMED
- Model families: settings.py:398 FOREIGN_MODEL `(?:claude|gemini)-`; settings.py:743 CLAUDE_MODEL
  `^(claude-|opus|sonnet|haiku|fable)`. sdf54-split.md M5 flags that SDF-54 added more.
- Model names hard-coded: settings.py:1144 FABLE `claude-fable-5-2`; commands.py:682 ARGON `gemini-4-argon-high` and
  :719 fallback list with `"opus"`; project.py:11-12 FACTORY_WORKER `gemini-4-carbon-high`, FACTORY_REVIEWER argon.
  These go stale every model release.
- Effort words: settings.py:613 EFFORTS (9 words across all CLIs). Perm words: settings.py:190 WORKER_PERMS.
- CLI binaries/flags: settings.py:17 AGENTS (codex needs `--no-daemon`); agents.py:69 AGENT_BINS; revive.py:41-48
  CLAUDE_VALUE / CODEX_VALUE / AGY_VALUE (which flags take a value, per CLI, to re-parse argv).
- Screen text: commands.py:621 QUOTA_SCREEN regex of CLI quota banners; provider.py classifier (audit).
- Status/state words: 47 inline tuples + 5 named sets (see symptom 6). Automation events automations.py:43.
- Brief prose: budget.py:20-21 SIZE_LINE / BRIEF_LINES; generated-file names budget.py:69-71; doc suffixes :102-104.
- Missing abstraction: ask the CLI (its own model list / probe) and store what it said; argv is built, never re-parsed.

## Other faults found (not on the owner's list)
F1. Settings resolution has ~8 sources for "which model": DEFAULTS → everywhere → machine → project (+legacy `spaces`
    merged in, settings.py:87-90) (settings_for :360-375) → task fields (worker_pick :521-536) → `start --agent` →
    previous agent's argv (worker_argv :539-546 keeps the OLD argv, only permissions replaced) → queue.argv → custom
    `--argv`. Fallback silently picks the first installed CLI (default_worker_agent settings.py:24-26, used :530, :551).
F2. 48 `open_store`/`Store(` sites (commands 25, automations 6, tick 4, simulators 3, …); some readers bypass the
    Store lock and read the JSON file directly (commands.py:1236 `open(...)`, autopilot.py:302 and
    assistant_settings.py:42 `read_list(...)`; whether read_list locks: UNVERIFIED).
F3. Time-based polling everywhere: tick loop (needs_tick tick.py:813, cmd_tick :823), PR every 60s, checks 20s, external
    deps every EXT_EVERY, watchdog/revive scrape screens. No event source; facts are as old as the last pass.
F4. Error swallowing: ~319 `except` clauses; 98 are followed directly by pass/continue/return. tick_store wraps each
    pass in `except Exception … # never stops the tick` (tick.py:343-346, :375-378, :379-382, plus retro/checks passes
    ~:403-420) and only appends to a report dict. takeover failure → t["backup"].state (takeover.py:220-222).
F5. Import tangle: 122 function-local `from .x import` (cycle workarounds), most in retro 16, settings 12, budget 12,
    stage 10, commands 9. budget→commands (budget.py:219), agents→stage (agents.py:176), retro↔budget↔commands↔settings.
F6. Two resume rules (launch.py:327 and revive.py:113, the latter wrapping the former with a Claude exception);
    argv re-parsing (revive.py:57 base_argv, settings.py:555 argv_model, flag tables revive.py:41-48) because argv is
    the stored form of intent.
F7. Behaviour keyed on free text: `attention` string prefixes (commands.py:1819 REVIEWER_FAILED, :1828, :2039, :2114; budget.py:446);
    size reasons matched by substring "twice its size" (budget.py:438); NON_CONVERGENCE sentence used as a key
    (budget.py:381, :391).
F8. Heritage: 407 ticket refs (FAC-/SHEP-/SDF-/HERD-/#nnn) in comments; 15 "As core/*.js" mirrors of the old TS app;
    `space` parameters still threaded (settings_for(s, space=None) :360, worker_pick :521, begin :402);
    HERDR_BROWSER_TASK env still set on every launch (launch.py:804-806) though browsers.py was dropped.
F9. Features with thin or no user in the sheepdog product: machines.py 983 (multi-machine sync/reach; used only by
    automations+cli), link.py 202 (cross-machine actions; cli only), devices.py 116, simulators.py 222 (imported by 9
    modules: deep coupling for an iOS-only feature), automations.py 822 + autopilot.py 795 (a second scheduler with its
    own natural-language time parser automations.py:47-93 next to when.py), watchdog.py 346, sandbox.py 162.
    Whether any live project uses machines/devices/simulators/automations: UNVERIFIED (~/.config/sheepdog/settings.json
    does not exist; the live store is under ~/.local/share/shepherdr/tasks, not checked). The sheepdog iOS app does use
    simulators, so treat simulators/devices as "plugin later", not proven dead.
F10. Per-feature JSON side-files beside the task store (see store table): 20+ files, each its own lock or none.
F11. Every review/verdict path has two protocols: CLI verb `herdr-task review` vs a code-fenced verdict parsed off the
    terminal for sandboxed agy (commands.py:911-928); the retro uses a third (<retro>{…}</retro> tail, retro.py:1-4).
F12. Resets are hand-kept pop lists per path: reopen pops 25 named fields (commands.py:1771), begin pops 7
    (commands.py:416-417); recap --decide new keeps rounds and the review-more grant (the SDF-67/74 bug, per the task
    records; its code path not opened: UNVERIFIED).
    A new review field must be added to every list by hand or it leaks across a reset.

## (b) Task states and where each is set
Stored `state` (one field, 6 words; created "todo" at store.py:311):
| state | set at |
|---|---|
| todo | store.py:311 (new), commands.py:195 (unschedule), :1744 (return_to_waiting), :1777 (cmd_state) |
| scheduled | commands.py:183 (schedule) |
| queued | commands.py:477 (cmd_start), :1744 (return_to_waiting), automations.py:387, tick.py:328 (scheduled time reached) |
| running | commands.py:410 (begin), :1264 (back_to_work), :1394 (cmd_review), :1777 (cmd_state), :2072 (cmd_resume) |
| ready | commands.py:1306 (finalize), :2697 (cmd_accept), tick.py:306 (settle_back auto) |
| done | commands.py:1777 (cmd_state), github.py:1348 (merge seen) |
Derived, never stored: 8 STATUSES (agents.py:162: todo, scheduled, queued, working, needs, ready, merging, done) and 9
stages (stage.py:346-356: owner_answer, done, owner_gate, blocked_or_failed, ci, waiting_on_dep, queued, idle,
working), each derived from `state` + side fields (attention, recap, retro_run, review_runs, review_quota_wait,
push_held, backup, device_wait, pr.wait, retry, merge_requested, ended_at/done_at) + live hc agent status.
Sub-state machines with their own `state` words: t["backup"].state starting/started/failed (takeover.py:215-224),
after_ext[].state (queues.py:89), run/CI `state` mirrors of GitHub (proof.py:290-292), hc agent live/gone (lease.py:44).

## (c) Persistent files the engine writes
STORE = WHERE.path("tasks") (base.py:73); store.py:1 says ~/.local/share/sheepdog/tasks but the live files are in
~/.local/share/shepherdr/tasks (ls 2026-11-18). Writer column = the module holding the path constant; the "holds"
column for side-files is from names/comments, mostly UNVERIFIED.
| path | format | writer | holds (overlap) |
|---|---|---|---|
| STORE/<slug>.json | JSON | store.py Store (48 open sites) | tasks[], config, milestones, plan_log, coordinator lease (lease.py:3), queue argv inside tasks |
| STORE/<slug>.archive.json | JSON | archive.py | old done tasks |
| STORE/.<slug>.json.lock | flock | store.py:189 | — |
| STORE/.stops.jsonl | JSONL | base.py:117 stop_note | why agents were stopped |
| STORE/.news.json, .held.json | JSON | news.py:32, :309 | news queue, held messages per terminal address |
| STORE/.autopilot.json, .autopilot.lock | JSON | autopilot.py:28, :239 | wake state |
| config/autopilot/ (HEARTBEATS) | files | autopilot.py:29 | heartbeat text |
| STORE/.assistant-settings.json | JSON | assistant_settings.py:16 | last settings told to Assistant (copy of settings) |
| STORE/.seen.json, .scheduler.json, .tick.lock, .scheduler.lock | JSON/locks | automations.py:642, :756, :666, :742 | automation events seen, scheduler identity |
| STORE/.retries.json | JSON | tick.py:665 | retry rows (overlaps task `retry` field) |
| STORE/.memory-guard.json | JSON | tick.py:504 | alert throttle |
| STORE/.watchdog.json | JSON | watchdog.py:17 | coordinator stall state (overlaps lease) |
| STORE/.machines.json, .reach.json, .found.json, .machines-sync.lock, .reach.lock, .remote/<m>[.secret] | JSON/marks | machines.py:78, :581, :693, :482, :613; cli.py:44,63 | multi-machine directory |
| STORE/.usage/ | cache | usage.py (commands.py:689) | codexbar reading |
| STORE/asks/ | JSON per ask | ask.py:28 | open questions (stage.py reads) |
| STORE/.claude-trust.lock | lock | launch.py:103 | — |
| beside_store machine-hold.json, cli-check.json | JSON | queues.py:207, takeover.py:21 | start holds, CLI health |
| WHERE state codex-sandbox.json | JSON | sandbox.py:21 | probe cache |
| config agy-review-unsandboxed | flag file | launch.py:258 | reviewer sandbox switch |
| ~/.config/sheepdog/settings.json | JSON | settings.py:49 | everywhere/machine/projects(+legacy spaces) |
| ~/.config/herdr-core/templates/*.md (+one-off herdr-assistant-<pid>.md) | MD | templates.py, lease.py:235 | worker/reviewer/assistant prompts |
| hc terminal labels | JSON in label | revive.py:172, commands.py:1249, tick.py:656, watchdog.py:263 | revive record (copy of agent argv/session), alerts, template hash |
| worktrees dir | git | launch.py/base.py:75 | — |
One fact with several homes: model/argv (settings.json, task fields, queue.argv, agents[].argv, revive label);
coordinator liveness (lease in <slug>.json, .watchdog.json, hc labels); retries (task.retry, .retries.json);
settings (settings.json, .assistant-settings.json copy, brief text rendered into the prompt).

## (a) Module table (lines from wc -l 2026-11-18; "real job" for modules I did not open comes from engine-audit.md)
| module | lines | real job | verdict |
|---|---:|---|---|
| commands.py | 2760 | every CLI verb + most state writes (160 field writes, 11 of 15 state writes) | DROP (re-split by entity: task transitions, run launch, review) |
| github.py | 1413 | gh PR/CI/merge engine, sets done | KEEP-IDEA (as an event-fed PR observer) |
| settings.py | 1367 | settings layers, argv building, model checks, auto-accept rules | MERGE-INTO-config (layers) + launch (argv); DROP model regexes |
| agents.py | 1344 | hc agent status, transcript tailing, derived status words | MERGE-INTO-run observer |
| machines.py | 983 | multi-machine sync/reach/directory | DROP |
| tick.py | 889 | poll loop running ~15 passes (15s cadence per earlier audit, UNVERIFIED) | DROP (replace by event loop) |
| launch.py | 878 | hc run spawner, brief, own_argv, sandbox | KEEP-IDEA (one spawner) |
| automations.py | 822 | timed/event task re-adds + second scheduler | DROP (or tiny MERGE-INTO-scheduler) |
| autopilot.py | 795 | wake coordinator on events/timers | MERGE-INTO-news/events |
| project.py | 756 | project start, CLI probe picks | KEEP-IDEA (probe), DROP rest |
| budget.py | 634 | loop breaker, size/time/token/CI recap | KEEP-IDEA (ladder), rewrite as event rule |
| machine.py | 618 | host health + CLI plan limits | KEEP-IDEA (limits probe) |
| news.py | 555 | event router to coordinator | MERGE-INTO-events |
| plan.py | 555 | plan apply/amend graph | KEEP-IDEA |
| lease.py | 489 | one coordinator per project | KEEP-IDEA |
| stage.py | 408 | derived stage table (read side) | KEEP-IDEA (derived view of one state) |
| retro.py | 379 | retro round | MERGE-INTO-review |
| revive.py | 366 | restore dead terminals via hc send + labels | DROP (daemon owns runs; resume via launch) |
| store.py | 355 | locked JSON store | DROP (daemon store) |
| proof.py | 350 | proof gate | KEEP-IDEA |
| watchdog.py | 346 | coordinator stall restart | MERGE-INTO-lease |
| ghnews.py | 320 | PR review/comment polling | MERGE-INTO-github observer |
| base.py | 298 | paths, shell, kill helpers | DROP |
| queues.py | 260 | after-deps, max_running, ext deps | KEEP-IDEA (deps), DROP ext/machines |
| sessions.py | 254 | CLI session/transcript discovery | KEEP-IDEA |
| templates.py | 241 | prompt templates + upgrades | MERGE-INTO-launch |
| ask.py | 241 | blocking questions | KEEP-IDEA |
| assistant_settings.py | 240 | settings block told to Assistant | DROP |
| takeover.py | 236 | CLI failover backup | MERGE-INTO-launch (as a launch intent) |
| simulators.py | 222 | iOS sim pool | DROP from engine core (plugin later) |
| checks.py | 206 | local check runner | KEEP-IDEA |
| link.py | 202 | cross-machine actions | DROP |
| when.py | 168 | time parser | MERGE-INTO-scheduler (one parser, not two) |
| sandbox.py | 162 | Linux codex bwrap probe | KEEP-IDEA (minor) |
| output.py | 152 | JSON/table views | DROP |
| drift.py | 142 | after-the-fact comparisons | MERGE-INTO-review |
| devices.py | 116 | physical iPhone wait | DROP |
| usage.py | 112 | codexbar reader | KEEP-IDEA |
| provider.py | 102 | provider error classifier | KEEP-IDEA |
| archive.py | 80 | move old done tasks | MERGE-INTO-store |
| handoff.py | 73 | handoff text on CLI switch | MERGE-INTO-launch |
| __init__.py | 39 | docstring | DROP |
| scheduler.py | 12 | tick loop identity | DROP |
| cli.py | 520 | argparse + --on forwarder | DROP |
