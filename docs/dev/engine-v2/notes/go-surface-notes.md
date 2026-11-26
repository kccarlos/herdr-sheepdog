# Go surface notes (engine v2, Go rewrite) — researcher notes, 2026-11-18

Repo: the repository @ main 4df3a038. Read-only research.
Sources cited as path:line. UNVERIFIED = not opened.

## 1. Owner contract v1 (docs/dev/factory/owner-contract.md)

Boundary today: daemon -> `engine owner <cmd> --json` CLI (uv-managed Python), engine -> daemon loopback
`GET /api/factory/answer-proof/<ask_id>?proof=&option=` (owner-contract.md:27-31, 39-54, 363-367).
Every response has `"protocol": 1`; timestamps epoch ms int (:58-59). Error envelope {protocol, ok:false, code, error},
exit 1 / 2 on conflict; codes: not_found, already_closed, invalid_option, invalid_ask, unknown_kind, proof_required,
proof_rejected, not_delegable, invalid_rule, unexpected_key, not_ready (:60-85).

Reads (:43-50): projects, asks [--project][--open], digest [--project][--since], rules [--project], graph --project, alerts [--since].
Actions (:51-54): answer <ask> --option <idx|label> [--proof], say <project> "<text>", propose-rule, revoke-rule.

Project (:97-110): id, name, folder, prefix, machine, status enum working|waiting|idle|stopped (waiting iff open_asks>0;
stopped when held by alert no_pool/disk/consent_prompt/main_red or no live coordinator with open tasks), open_asks,
goal, active_milestone, coordinator{terminal,session,state working|idle|dead,lease_valid}|null, spent{tasks_merged,review_rounds,ci_runs},
pools[{pool claude|codex|antigravity-gemini|antigravity-claude-gpt, state ok|floor|unknown, rolling, five_hour_left, week_left, five_hour_reset, week_reset}].

Ask (:119-138): id ^[a-z0-9]{6,24}$, project|"everywhere", name, folder, kind (8: decision gate release paid-key delete install reset rule),
gated == kind!=decision, question 1..300 single line, options 2..9 distinct, recommend ∈ options, task, milestone, subject{}|null,
at, status open|answered|cancelled, answer, answered_at, answered_by owner|coordinator|rule|null, proof_id prf-…|rul-….
Asks never expire on a timer.

Digest (:148-157): generated_at, since, spoken_summary (code-built, no ids/paths), open_asks, alerts_active, pools,
projects[{id,name,status,milestone{id,title,state,merged,total},merged[{ref,title,pr,merged_at}],waiting[{ref,title,stage,why}],replans,open_asks,spent}].

Rules (:161-195): typed (merge_policy, review_rounds, auto_accept, design_gate fable|owner, delegation, notify_level asks_and_alerts|alerts_only|quiet,
project_priority) vs text (text, section, base_hash). Fields id rul-…, scope everywhere|machine|project, read_back, saved_by_ask, revoked_*.

Graph (provisional, :197-209): milestones[] state planned|active|validating|done (derived), tasks[] with `stage` from SDF-30 stage table, why_waiting, critical_path.

Answer flow (:215-237, 324-367): engine asks -> daemon registers PendingAction ID "fask-"+ask.id, PaneID "factory:"+project,
Command "kind · task", Options with Recommended/Default = recommend -> /api/state, /events pending.created, APNs KindApproval,
/ws CardKindApproval -> phone tap POST /api/pending/answer (paired ticket; deviceID != "" / "local") or live voice (InputVoice; typed refused)
-> railInputLocked -> mint AnswerProof prf-<12hex> in <data_dir>/factory_proofs.json (0600, 24h) -> daemon calls `owner answer --proof`
-> engine verifies via loopback GET -> unlock (decision: herdr-news to worker; gate: accept/merge; release: tag; rule: save/revoke).
Desktop panel: read-only for fask- (:353). Delegation rules answer gate/release in code with answered_by "rule" (:295-300).

Quiet rule (:379-385): labels sheepdog.project / sheepdog.role (coordinator|worker|reviewer|checks|helper) / sheepdog.task;
herdr.IsFactoryQuiet in supervisor silences all non-coordinator factory panes: zero announce, voice push, cards, APNs.
Only owner asks, alerts (5 kinds: no_pool, disk, consent_prompt, main_red, ci_stalled; :391-397) and digests break silence.
Alert fields (:415-427). Daemon delivers each alert.id at most once across restarts; notify_level quiet holds speech; APNs EventID "factory-alert-"+id (:429-432).
Engine wakes daemon by setting hc label sheepdog.alert=<id> on coordinator terminal (:412).

## 1b. Approved UI (docs/dev/design/factory-addendum-draft.md, canvas boards)

Lifecycle board (FactoryLifecycle.dc.html, png/FactoryLifecycle.png) — one task's steps, NOT a stored enum:
Start (model and mode named) -> Work (status lines only) -> Review rounds (2, another model) -> Accept bar (in code, not a person)
-> Merge (by the engine) -> Drift check (every tick) -> Done (a number in the digest).
Side paths: Review -> "two rounds in a row with majors" -> Retro round (once: ship, fix, redesign) -> "one bounded fix" back to Work,
or -> Owner asked "drop it, or how to go on?" (only if task was split after its retro and a child draws a retro too).
Accept bar -> Owner asked "one card, one question" only for "a release, a paid key, a delete, or a rule that says designs wait for me".
Drift check -> Coordinator woken (judges and fixes) on "approved but not merged, red after approval, a stale ask".
Footer: each step writes one log line; each run and round records model and effort.

States board (FactoryStates.dc.html -> FactoryProject.dc.html script, state enum):
running | noask ("Nothing needs you") | planning | finished | stopped ("Stopped, with the fix") | ownerstop ("Stopped by you: start it again")
| loadfail ("Could not load") | end (Close space refused) | debug (later task).
Copy (FactoryProject.dc.html script `msg`): planning "The coordinator is turning your goal into milestones and tasks.";
finished "All 4 milestones are done. 41 tasks merged."; loadfail "Couldn't load the factory / The engine didn't answer. The agents below are still live." + "Try again";
ownerstop "Stopped by you / Nothing is running. The plan and the tasks are kept." + "Start the factory again…".
stopped alert banner: "Stopped: no model has quota left / It starts again by itself at 6:10 PM, when Claude resets." (alert title/body/fix).
Section content: ask card (running only), "Milestone 3 of 4 · 7 of 12 merged", "About 5 hours left, through SDF-53, SDF-54 and SDF-60."
(critical_path.remaining_text + tasks), layers "Layer N · after layer N-1", task rows {id, title, why, on_critical_path, live terminal?}
with why e.g. "Working · gpt-6.2-sol · round 0 of 2", "Waiting on CI, 18 min · sonnet", "After SDF-53", "After SDF-54 and 5 more".
Live row opens worker window; idle row not tappable. Agents block: "coordinator · working 2 min | idle 2 h | waiting for quota".
End: "Close space" refused: "A factory is running here. To close it, say "stop the factory" or run sheepdog factory stop."
=> engine must serve per task: ref, title, layer (dependency depth), on_critical_path, stage/why text, model, rounds done/of, terminal id (live?),
   waiting duration; per project: milestone index/total, merged/total, remaining_text, coordinator state+age, ownerstop flag, alert.

Ask card (FactoryAskCard.dc.html): aria "The <project> factory needs your answer"; "The herdr-sheepdog factory asks: <question>";
options as outlined buttons, recommend tagged "Recommended" (e.g. "Tag 0.1.31" Recommended / "Wait for one more QA pass" / "Not now").
Shown at head of factory section AND as project-level card under Needs you on Agents with badge (addendum :119).

Start entry (FactoryNewProject): row "Start a factory here" beside "Run a command…"; greyed with reason:
shelloff "Commands need the phone shell. Turn on Shell from your phone on your computer's setup page."
outside "Commands run only in your project folders. Add this folder to them on your computer."
Start shell (FactoryNewProjectChecks script): `$ sheepdog factory start` / "Checking this computer" / ok engine and Python / git repository, branch main /
claude and codex signed in / models: opus, gpt-6.2-sol / WARN gh is not signed in, fix: run gh auth login, until then: merges stay local /
quota unknown, no codexbar, fix: nothing needed / no factory runs here yet / notice (addendum :57) / "1 of 5  What should it build? One sentence, or a file path."
Fail variant: "no agent CLI is signed in / fix: at your Mac, run claude and sign in. codex or agy work too." "1 thing stops the start. Fix it, then press Return to check again. Control-C quits."
Five prompts (addendum :139-145): goal, delegated gates (None), releases (Ask me each time), notify level (Asks and alerts), design system.
Widening delegation -> rule ask, not saved directly (:149). Flags --goal, --goal-file, --yes (:151). Voice: fixed brain action, read-back,
`sheepdog factory start --goal-file <path> --yes` via RunLine in a space (POST /api/spaces first if none) (:153-159).

Terminal board (FactoryTerminal): `sheepdog factory log` records: time, by (engine|coordinator|owner), "<task> <verb>" (accept, merge, wake, add,
start, typed, plan-amend, review-round-1), wake: (event:ready, tick, tick:drift, event:drift, start, input:phone, input:owner, round),
why:, model: (model[, effort]). `sheepdog factory doctor`: "Checking this computer" (engine and Python, git repo/branch, CLIs signed in,
models, gh signed in + repo pinned, quota) + "Running now" (one coordinator N ago, main is green, <task> waits at a prompt N min [FAIL],
ask-N open on a done task [WARN], nothing approved and unmerged for over 30 min), summary "1 failed, 2 warnings."; 46-col, `fix:` lines.
Log record schema (addendum :83): at, project, by, wake, task, decision, why, model. One JSONL per project in store.

New contract needs from addendum (beyond owner-contract v1): actions `owner touch {terminal, device, kind, len}` (:95) and `stop` (:128,170);
graph tasks gain `terminal`, `model`, `rounds` (:170); space gains `factory: {project, status, open_asks, active_milestone}` in GET /api/spaces
and new GET /api/spaces/{id}/factory (graph + open asks) (:124); project record holds space id; owner-stop mark (:128);
`sheepdog.model` label (:116); muted reason beside `muted` (:117); coordinator done/idle silent, blocked speaks (:127); Close space refused while lease live (:125).

### Owner-visible task stage table (Python today, must be mirrored exactly)
factory/mods/tasks/bin/herdr_task/stage.py:346-361 STAGE_TABLE, first match wins, (name, owner inbox?, label):
owner_answer (inbox, "Owner answer") | done | owner_gate (inbox, "Owner gate") | blocked_or_failed | ci | waiting_on_dep ("Waiting on dependency") | queued | idle | working.
output.py:47 why_waiting = stage.why for owner_answer/owner_gate/waiting_on_dep. Fixture uses stage "ci"/"waiting_on_dep", why "Waiting on GitHub Actions (18m)", "Waiting on BCN-12".
Milestone states derived, never stored: planned|active|validating|done (milestones.md "What the engine adds").
Project status: working|waiting|idle|stopped (owner-contract.md:104).
Section states (board): running|noask|planning|finished|stopped|ownerstop|loadfail (+end, debug). Mapping proposal:
running = project has open ask (status waiting); noask = working, 0 asks; planning = coordinator active, no milestones yet (NO contract field today);
finished = all milestones done (derivable from graph); stopped = status stopped + active alert (banner = alert title/body); ownerstop = NEW owner-stop mark;
loadfail = daemon could not read engine (daemon-side). Lifecycle board steps (Start/Work/Review/Accept/Merge/Drift/Done/Retro) are a narrative,
they map loosely to stages working -> (review inside working/idle) -> ci -> done; there is no "review" or "merging" stage in STAGE_TABLE
although alert ci_stalled mentions "ready or merging task" (owner-contract.md:397).

## 2. How the Go daemon talks to the Python engine today

Wiring: internal/app/wire.go:43-66 — only when opts.Herdr == HerdrSocket: NewTickSupervisor + NewClient (DaemonURL = loopback), NewProofStore always.
Importers of internal/factory (non-test): cmd/sheepdog/factory.go, internal/app/{app,wire}.go, internal/web/{server,factory}.go,
internal/supervisor/{factory,supervisor,inputs}.go. No brain/voice/iOS/panel import.
Daemon USES only: Client.AsksPartial(open) polled every 2 s (supervisor/supervisor.go:549-565, supervisor/factory.go:26 defaultFactoryPoll)
and Client.AnswerIndex (supervisor/factory.go:571). Client.Projects/Digest/Rules/Graph/Alerts/Say/ProposeRule/RevokeRule exist
(internal/factory/client.go:179-506) but have NO caller in the daemon (grep). => alerts->APNs/voice, digest speech, brain `say`, rules
read-back, graph/space section are NOT wired in Go today (contract §6.3, SDF-24..27 Go side unbuilt).

| Go file | lines | job today | verdict |
|---|---|---|---|
| internal/factory/contract.go | 1-1385 | v1 types, enums (ask kinds :19-55, rule types :58-84, alert kinds :87-109, codes :113-123), strict decoders+Validate, GraphResponse provisional (:486-505) | REPLACE: types/enums/validators become the engine's domain model; strict decode-from-CLI layer and Go/Python fixture parity go |
| internal/factory/client.go | 1-728 | exec `engine owner … --json` via managed Python, 15 s read / 30 s action timeouts (:24-25), asks cache keyed by store digest (:216-340), env passing HERDR_TASK_STORE/SETTINGS/TEMPLATES, SHEEPDOG_DAEMON_URL (:602-636) | DELETE (keep its method set as the Go interface the supervisor calls) |
| internal/factory/engine.go | 1-423 | unpack embedded factory/ per version+digest into <data_dir>/factory/engine/<v>/, marker .sheepdog-engine.json | DELETE |
| factory_embed.go (root) | :13-21 | go:embed factory/ tree + .python-version | DELETE |
| internal/factory/python.go | 1-368 | uv-managed pinned CPython in <data_dir>/factory/python, probe imports sqlite3/ssl (:17), EngineEnv | DELETE |
| internal/factory/tick.go | 1-834 | keeps Python `herdr-task scheduler` alive in hc terminal `sheepdog-factory-tick` (labels sheepdog.role=tick, sheepdog.engine, sheepdog.python, sheepdog.store :24-35); re-check every 10 s, backoff 1-30 s, healthy 30 s (:37-41); wakes on hc events finished/lost/removed/restarted/foreground/labels/missed (:400-411); off until a project is started (HasStartedProject :208); path resolution ResolveFactoryPaths/defaultFactoryLocations (:126-196) keeps test daemons out of ~/.config, ~/.local/share | REPLACE: the tick becomes a daemon goroutine; KEEP the event-wake set, the "off until a project exists" rule and the path-isolation logic |
| internal/factory/proof.go | 1-314 | ProofStore: Mint/Verify/Delete, <data_dir>/factory_proofs.json 0600, 24 h (:16-23, :203-275) | KEEP (rail). Verification becomes an in-process call |
| internal/factory/doctor.go | 1-1700 | `sheepdog factory doctor`: CheckEngine :195, CheckPython :219, CheckHerdrCore :270, project volume probe in hc + TCC consent naming :300-510, agent CLI canaries claude/codex/agy :538-905, gh + repo pin :965-1160, codexbar :1166, sync :1208, worktree sparse :1245, tart :1274; reads factory settings.json layers :1343-1475 | KEEP most; DELETE CheckEngine/CheckPython; ADD "Running now" part (FactoryTerminal board) |
| internal/web/factory.go + server.go:600 | 1-83 | `GET /api/factory/answer-proof/{ask_id}` accessLoopback, for the Python engine to verify proofs | DELETE (only exists because engine is another process) |
| internal/supervisor/factory.go | 1-796 | asks -> PendingAction fask-<id>, pane factory:<project> (:29-45, :233-270), notify (:332), stillAsking via contract (:371), mint proof + answer (:465-650), brain sees asks as agents (:663-760) | KEEP; swap Client for engine interface; replace 2 s poll by engine change notifications |
| internal/supervisor/factory_quiet.go, internal/herdr/factory.go | 50 / 40 | quiet rule by labels; coordinator returns false (herdr/factory.go:20-33) | KEEP; change coordinator rule per addendum :127 |
| cmd/sheepdog/factory.go | :42-60 | subcommands doctor, python-version, python-dir only | KEEP doctor; DELETE python-*; ADD start/stop/log |
| Makefile:44-48 test-factory, .github/workflows/ci.yml:130-140 factory job | | uv python install + unittest | DELETE |
| testdata/factory/owner-contract-v1.json | | shared fixture | KEEP as golden for whatever JSON the phone/web read; drop Python half |
Release/Homebrew: no factory/python step in release.yml (grep found none); engine ships embedded in the binary.

## 3. internal/herdr / internal/hc / internal/herdrcore — what the engine gets

Two backends behind herdr.Backend (internal/herdr/backend.go:11-34: Snapshot, Subscribe, SubscribeFleet, Resync, Ping, ListAgents, Read, Prompt, SendKeys, Interrupt):
(a) internal/herdr.Client = old herdr TUI socket (doc.go), (b) internal/herdrcore.Backend over internal/hc (herdr-core JSON-RPC; terminals only, no workspaces; herdrcore/doc.go).
Optional caps by type assertion: StartAgent(pane, name, kind, args, timeout) (herdr/client.go:354, herdrcore/spawn.go:99), CreateWorkspace, SplitPane, ClosePane, ProcessInfo (herdrcore/spawn.go:71).
States: idle|working|blocked|done|unknown (herdr/protocol.go:10-18), mapped from hc Status in herdrcore/mapping.go:105; watcher Transition{PaneID, From, To, At, Agent, Name, Workspace*, Closed, Released, Labels} (herdr/watcher.go:10-28) with blocked debounce.
Prompt detection: herdr.ParsePrompt(screen) -> Prompt{options, question, command} (prompt.go:113-224), EndsInQuestion. Unsent input: herdr.TypedInput (typing.go) — "unsent desktop input blocks prompts" scenario.
Raw herdr-core client internal/hc/client.go: Hello, List(all), Get, Read(lines), Send(text, force), Keys, Interrupt(watch), Run(RunParams{Argv, Line, Cwd, Name, Env, Labels, Keep, NoParent, NoNotify, Rm, Size}) (types.go:107-120),
Label, Kill, Remove, Exec(machine.exec); Subscribe(filter) -> Notifications (events.go). Terminal{Address, Name, Argv, Cwd, State, Status{state, app, kind, msg, title, progress}, Turn, Labels, Foreground{pid,name,argv,cwd}, PID, ExitCode, Reason, CreatedMS, FinishedMS, StatusMS, Keep, Parent} (types.go:10-32).
Conformance: internal/herdr/conformance RunAll (conformance.go:181) run by internal/herdr/conformance_test.go:17 and internal/herdrcore/conformance_test.go:19; scenarios: plain shell never agent, claude/codex detected fresh+resumed, wrapped agent survives restart, exited agent -> shell, different agent same shell, phone shell reuse, new agent joins space, split size, blocked question parsed, quiet snapshot, foreground tree identifies session, unsent input blocks prompts, plus marks scenarios.
Gaps for the engine: (1) herdr.Backend.StartAgent has no labels/env/keep/no-parent — factory must use hc.Run directly (as Python does: `hc run --keep --no-parent --cwd`, coordinator.md "Starting one"), so the factory is herdr-core-only; old-herdr backend has no labels at all (architecture.md:64 attention.Local). (2) No "send or hold" (deliver when idle) primitive; Python engine does it. (3) No model_used / session-id reader per CLI (addendum :48 unverified). (4) hc cannot re-parent (coordinator.md reconcile step 3) — store parent in engine. (5) No conformance scenarios for labelled run, keep/no-parent, interrupt-with-watch, label events waking the tick; addendum F1 needs space label stamping (herdrcore/spaces.go builds a space only from herdr.space label, addendum :125). (6) owner-touch needs a daemon event per typing burst (web/term.go:421-441 byte-counted only).

## 4. Design loop and coordinator (approved)
Design loop (FactoryDesignLoop board + addendum §9 :222-256): runs at project start and before each UI milestone. Steps: 0 design-system question once
(use one / make one / skip; probe recommends) -> 1 Draw artboards on the canvas (Claude only) -> 2 Export read-only folder docs/<project>/design/ with PNGs + boards.json {id,title,file,png,canvas}, hash-checked; hand edit fails ready -> 3 Claude-only design task + reviewer -> 4 Review by a fresh Fable session that did not draw (engine refuses designer's session/parent) -> 5 findings back to designer, at most 2 change requests; then retro ladder (retro round, recap) and only at retro step 7 the owner -> 6 Split by plan amend, each task carries boards:[ids] or [] with reason; UI tasks cannot start before the pass. Gate ask to owner only if typed rule design_gate=owner. Owner hears nothing otherwise.
Coordinator (coordinator.md): one Opus per project; identity = lease in <data_dir>/tasks/<slug>.json `coordinator` {address, created_ms, template hash, session id, log, handoffs[]}; only engine writes lease (project start, reconcile, watchdog restart); started `hc run --keep --no-parent --cwd <repo>` with assistant template + settings block + handoff (tasks/handoff/); reconcile each tick for projects with open tasks (bare shell >15 min replaced; disk probe first; 10 min between starts; 3 starts/h -> gave-up); re-parent workers, move held news, finish_handoff kills old pid group with 30 s START_MARGIN; watchdog: working w/o child/output 20 min -> interrupt, 90 s -> restart, again within 2 h -> alert; unchanged screen 30 min -> restart. Coordinator plans via plan apply/amend (milestones.md), answers decision asks, judges recaps.

## 5. Conventions and storage
Packages under internal/<name> with doc.go; architecture.md module table must list a new package (architecture.md:51-58). `make test` = `go test -race ./...` (Makefile:41-42) includes internal/docscheck (links + real `sheepdog …` commands in user docs); `make vet lint`; `make eval` if brain tools change (voice start action). New config keys -> CONFIG.md; data files listed in CONFIG.md data table (:555-575). Tests must not touch real ~/.config/sheepdog or ~/.local/share/sheepdog (AGENTS.md; ResolveFactoryPaths keeps an overridden data dir self-contained, tick.go:150-158). Eventlog types documented in architecture.md:311 (factory.* events).
Storage today: NO SQLite in the daemon (grep: only python.go probe string). All state is JSON files in <data_dir> (~/.local/share/sheepdog): state.json, agent_marks.json, space_mutes.json, wrapups.json, devices.json, push_devices.json, factory_proofs.json (0600, atomic rename), JSONL logs events/YYYY-MM-DD.jsonl, cards.jsonl, notes.jsonl, brain/<run>.jsonl. Atomic write helper exists e.g. internal/web/tlsid.go:352 writeFileAtomic (no shared package found).
Engine store today (Python): <data_dir>/tasks/<slug>.json (task list incl. coordinator lease), <store_dir>/asks/<id>.json, <store_dir>/alerts.json, settings at ~/.config/sheepdog/settings.json (layers everywhere/machine/project; doctor.go:1343-1475 reads it), usage-pools.json cache. Env overrides SHEEPDOG_FACTORY_HOME / SHEEPDOG_HOME / SHEPHERDR_HOME / XDG_* / HERDR_TASK_STORE / HERDR_TASK_SETTINGS (tick.go:126-145, client.go:602-636). CONFIG.md data table does NOT list tasks/ or the asks/alerts store (only factory/ and factory_proofs.json) — doc gap.

## 6. Awkward in the old engine / contradictions
- Alerts, digest, graph, say, rules: client methods exist, no daemon caller -> contract §6.3 delivery (at-most-once alert ids, APNs factory-alert-<id>, voice) unbuilt in Go.
- Asks reach the daemon by a 2 s CLI poll spawning Python each time (cache by store digest mitigates); engine wakes daemon via hc label sheepdog.alert (contract :412) — awkward cross-process signalling; in-process engine can push.
- Loopback proof round-trip (engine -> daemon HTTP) only exists because of the process boundary; residual "local process runs `owner say`" (contract :372) disappears with no CLI, but a `sheepdog factory …` CLI must not reintroduce gated actions.
- Boards FactoryNewProjectChecks + FactoryTerminal print "ok engine and Python": contradicts the Go-only decision; copy must change (owner-facing text change on approved boards).
- Coordinator quiet: contract §6.1 + code (herdr/factory.go:28) keep coordinator loud; addendum :127/:171 decides done/idle silent, blocked speaks.
- Lifecycle board "Owner asked … only a release, a paid key, a delete, or a rule that says designs wait for me" vs contract §4.1: also install, reset, rule asks, and `gate` for out-of-rounds tasks (field note 47); board sends "two rounds with majors" to the retro round first. Board's "drop it, or how to go on?" ask has no clear kind (decision is coordinator-answerable, gated kinds don't fit).
- Project status `stopped` overloaded; addendum owner-stop mark + reason "stopped by the owner" has no contract field; section state `planning` and `finished` have no contract field (derive from graph).
- Pool `reason` (contract text :112-113, addendum §3) is not a key in the Pool shape (:110) — strict decoders would refuse it.
- Graph tasks lack terminal, model, rounds, layer (addendum :170; FactoryProject rows need them); graph still provisional (:197-209).
- Contract still names a `/factory` web page and "/events SSE + /factory" (:410, :487 SDF-34/35); addendum replaces it with the space-page section and cancels SDF-34.
- Contract lists no `stop` or `owner touch` action; addendum adds both.
- Factory ask currently shows only on Conversation, badge 0 (addendum :107); approved: also project-level card on Agents with badge.
- Alert ci_stalled says "ready or merging task" but STAGE_TABLE has no ready/merging stage.
- Drift check "every tick" on the lifecycle board: drift_pass not in tick.py yet (addendum :18).
UNVERIFIED (not opened): docs/dev/architecture.md beyond grep hits, glossary.md (grep found no factory/owner terms), m1-acceptance.md body, internal/herdr/{events,bridge,watcher}.go bodies, herdrcore/spaces.go, supervisor/inputs.go, iOS sources, FactoryVoice/FactoryAsk/FactoryTask/FactoryAgents/FactoryIPad* templates beyond extracted strings, PNGs other than Lifecycle.

## Corrections after review (supersede earlier lines)
- Task state has TWO engine layers: stored task status (agents.py:162-163 STATUSES todo|scheduled|queued|working|needs|ready|merging|done;
  lease.py:77 OPEN also says "running") and the derived 9-row stage (stage.py:346). So the "ci_stalled ready/merging but no such stage"
  contradiction is WITHDRAWN. The new engine's lifecycle table must map onto the boards (section states + lifecycle steps), not copy the Python table.
- "No daemon caller" re-verified by name grep over internal/ and cmd/ for Projects/Rules/ProposeRule/RevokeRule/Digest/Alerts/Graph/Say(ctx: none outside internal/factory.
- hc.Send(text, force) returns SendResult{Started, Status, Turn} (hc/types.go:82-86): a force flag exists; whether hc holds a non-forced send until idle is UNVERIFIED, so "no send-or-hold primitive" is UNVERIFIED.
- Board ask "drop it, or how to go on?": review-rounds.md "The retro round" not opened -> kind UNVERIFIED, not a firm contradiction.
- Ask/answer path cited from the contract doc; supervisor/factory.go:233-270, :526-650 bodies not read. Behaviours pinned by tests (supervisor/factory_test.go) that the new engine must keep:
  transport failure rolls back proof and input; slow unlock past timeout still confirms recorded answer; a bad sibling ask does not hide valid asks;
  unknown kind creates no card or proof; local tap / WindowKeys / typed input / negative speech refused; redacted option label still verifies.
- Surfaces: desktop panel has factory-ask code (mods/sheepdog/ui/questions.js, model/questions.js; read-only for fask-). web/ no hits. Menu bar UNVERIFIED.
  iOS: 5 grep hits not inspected; addendum :103 says the app has no factory code and does not decode labels.
- FactoryTask board title still says "opened from a /factory row" (another /factory leftover).
- Cadences: engine's own tick 15 s (owner-contract.md:21); tick.go 10 s is only the liveness re-check of the tick terminal; daemon ask poll 2 s.
- DELETE also (UNVERIFIED, not opened): factory/ tree, root-package Python-import parity test (architecture.md:58), uv dependency (python.go:46 UVMissingError), any Homebrew/uninstall/data-clear handling of <data_dir>/factory.
- Glossary has no owner/coordinator/factory terms (grep): owner-contract.md says owner == user; a new engine doc must add terms or use "user".
