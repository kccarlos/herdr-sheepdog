# Milestone 1 acceptance (SDF-13)

Phase 1's exit test from the design: *the whole Python suite passes with no herdr-app installed; on a Linux VM and a clean Tart VM with only hc and one CLI, a task goes from `add` to a merged PR.* An agent that wrote none of milestone 1 ran it on 2026-11-17 against `main` at `4cacc91a` (after SDF-10, SDF-11 and SDF-12). This page records what was run, what passed and the gaps found. Background: [engine-audit.md](engine-audit.md) section 6.

**Status: met on the Linux test VM (SDF-43).** The Python suite passed without `herdr-app` on all three machines, local-merge mode reached `main` on both VMs, and the GitHub pull-request leg (section 3) is now proven on the Linux test VM with [`scripts/factory/m1-pr-acceptance.sh`](../../../scripts/factory/m1-pr-acceptance.sh) (no macOS test VM image was present in `tart list` during SDF-43). Until SDF-43 ran section 3, this exit test was half met, not passed.

## Machines

| Machine | What is installed | Agent CLI |
|---|---|---|
| Mac (host) | Everything, so `herdr-app` was taken off `PATH` for the run; the suite's own throwaway `HOME` hides `~/.config/herdr-app` | — |
| Linux VM (`ssh oci`, Ubuntu 24.04, arm64) | hc 0.1.17, claude, codex, uv 0.12.1, git, Python 3.12. No herdr-app, no gh, no make | claude (Haiku) |
| Tart VM (a clone of `factory-base-2026-11-08`, macOS 26.6) | Stripped to hc 0.1.18 and agy: herdr-app, the Herdr window, `~/.config/herdr-app`, claude and codex removed from the clone. Homebrew git, gh (signed in) and Python 3.15 stay | agy (Gemini 4 Carbon) |

The `sheepdog-oci` ssh name in the brief is stale; the Linux test VM is `oci`. Unlike the stripped macOS test VM clone (which had only `hc` and `agy`), the Linux test VM had both `claude` and `codex` installed, not "only hc and one CLI". The original VM images were not changed: the Tart work ran on a clone.

## 1. The suite without herdr-app

The command is `make test-factory` on the Mac. On the VMs it is the same `uv run` line without `make` (Linux has no make).

| Machine | Result |
|---|---|
| Mac, `herdr-app` off `PATH` | 739 tests, OK, 34 skipped |
| Linux VM (no herdr-app at all) | 739 tests, 2 failures, then OK with 33 skipped after the fix below |
| Tart VM (stripped clone) | 739 tests, OK, 33 skipped |

Skips, all for a stated reason:

- On every machine: 28 tests whose Shepherdr window, web or vm files are outside `factory/`, and 1 test that runs only on CI.
- On macOS: 3 bwrap (Linux sandbox) tests and 1 "no `/proc`". On the Mac host only, the hc ask-pane end-to-end test also skipped: hc was not found on the test's `PATH`. It ran and passed on the Tart VM.
- On Linux: 1 sandbox-exec (macOS) test, 1 "no working node", and the 2 `make`-dependent tests below. No skip hides a herdr-app dependency. The test files mention herdr-app only to keep the person's real `~/.config/herdr-app` out of reach (`home_guard.py`, `test_repo.py`), to set `HERDR_APP_HOME` for `restart.sh` (`test_restart.py`) and to list words the templates must not contain (`test_replay.py`).

**Fixed here:** `SyncBehindBase.test_empty_check_derives_from_makefile_test_target` and `…_falls_back_while_merge_regression_fails` derive `make test` from a Makefile and failed with exit 127 on a machine without `make`. They now skip when `make` is missing.

## 2. From `add` to merged, by hand on each VM

Recipe on each VM:

1. Copy a `sheepdog` built from this commit and run `sheepdog factory doctor` in a fresh git repo. Fix what it names: on Tart, install uv and run `hc server start`; on Linux, put a newer uv first on `PATH`.
2. Put `herdr-task` on `PATH` by hand (finding 4): a two-line `sh` wrapper that execs the managed Python, `~/.local/share/sheepdog/factory/python/cpython-3.14.8-*/bin/python3`, on the engine's `~/.local/share/sheepdog/factory/engine/<version>/mods/tasks/bin/herdr-task`. On Tart there is a second wrapper for `bin/herdr-secret`.
3. Run `herdr-task config --prefix DEMO --set worker.perm=full`. On Tart this setting was added only after the stall in finding 3.
4. Add a one-line task, `herdr-task add "Add HELLO.md" --prompt "Create HELLO.md … then run herdr-task ready"`. On Linux it also had `--model haiku`.
5. Run `herdr-task start DEMO-1`. On Tart it took `herdr-task switch DEMO-1 --agent agy` (finding 1). Wait for Ready, then `herdr-task accept DEMO-1`, with the tester playing the owner.

| Step | Linux (claude) | Tart (agy) |
|---|---|---|
| `factory doctor` | All ok after installing a newer uv; warns about gh and codexbar | Fails until uv is installed and `hc server start` is run, then all ok |
| `start` → worker runs | Needed 2 key presses in the worker's terminal (finding 2) | Started `claude`, which is not installed (finding 1). After `switch --agent agy`, it stopped on a permission prompt (finding 3). Ran once `worker.perm=full` was set |
| Worker commits and runs `ready` | Yes, with a proof file | Yes |
| `accept` | "merged into main; done" | "merged into main; done" |

Both VMs reached a commit on `main` in local-merge mode: there is no GitHub remote, so no pull request. The pull-request leg is in section 3.

## 3. Pull request on GitHub

In SDF-13, neither VM could make or merge a pull request because the Linux test VM lacked `gh` and credentials, and the owner's login on the macOS test VM was left untouched. Running it needed:

- a private throwaway repo for the test, and
- a fine-grained token scoped to that repo only (Contents and Pull requests, read and write), given through `herdr-secret` and removed after the run, plus `gh` on the Linux test VM.

**Proven on the Linux test VM (SDF-43, 2026-11-17 against `main` at `5f712f72`):** with `gh` (`2.45.0`), `uv` (`0.13.9`), and `sheepdog` from `5f712f72` on the Linux test VM, [`scripts/factory/m1-pr-acceptance.sh`](../../../scripts/factory/m1-pr-acceptance.sh) cloned a private throwaway repo (`kccarlos/test_repo`) using a repo-scoped fine-grained `GH_TOKEN` passed via `herdr-secret`, ran steps 1–5 unattended (`claude` Haiku, `worker.perm=full`, 0 key presses after #646), accepted `DEMO-1` once PR #2 was open, waited for `herdr-task`'s `tick` to merge it after the 30s no-CI window, and verified the merged state on GitHub:

```json
{"mergeCommit":{"oid":"e8a40ace5155a1e560400fc95798df21f3f8dbec"},"mergedAt":"2026-11-17T17:06:08Z","number":2,"state":"MERGED","url":"https://github.com/kccarlos/test_repo/pull/2"}
```

No macOS test VM image was present in `tart list` on the host Mac during SDF-43, so section 3 was not run on the macOS test VM.

## Findings

**Fixed since:** findings 1 and 2 fixed by #646 (SDF-41); findings 6 and 7 fixed by #645 (SDF-42).

Severity levels follow the reviewer template: **major** means someone would hit it, **minor** means it is rarely hit or cosmetic.

1. **Major: the default worker CLI is `claude` even where it is not installed.** On a machine with only agy, `start` says "DEMO-1 started", the worker terminal prints `command not found: claude`, and the task sits in Working. Doctor already knows which CLIs are ready (`agent clis: 1 ready (agy)`). Not fixed here. Picking an installed CLI as the default broke `switch --all --from claude` and other code that reads "no agent set" as claude (`agents.py`, `settings.py`). It needs a decision, probably in phase 2: doctor or `project start` writes the worker setting, or `start` refuses a CLI that is not on `PATH`.
2. **Major: a Claude worker on a fresh machine stops on two first-run dialogs.** The first is the folder-trust dialog; the engine sees it and shows "Asks to trust this folder", but nobody answers it. The second is the one-time Bypass Permissions confirmation; during it the engine still shows Working. Codex gets its trust through `-c projects=…` and agy through `--settings`, but Claude gets nothing. Unattended, the task waits for ever. Here the tester pressed the keys, 2 step-ins.
3. **Major: plain `add`/`start` use the person-facing defaults, not the factory's.** An agy worker in accept-edits mode stops on every command (field note 13). Only `project start` writes the factory defaults (`perm: full` and the agy worker), and it also starts a coordinator whose template runs `claude`, so on an agy-only machine it cannot finish. Here `herdr-task config --set worker.perm=full` was set by hand.
4. **Major (phase 2): doctor unpacks the engine but puts neither `herdr-task` nor `herdr-secret` on `PATH`.** Both start with `#!/usr/bin/env python3`, which is the system Python (3.15 on Tart, 3.12 on Linux), not the pinned 3.14.8 that doctor installed. Workers call `herdr-task` by its full engine path, so the run used a two-line wrapper that execs the managed Python. This belongs to phase 2's one install.
5. **Minor: prerequisites beyond "hc and one CLI".** uv was missing on Tart. uv 0.12.1 on Linux cannot download CPython 3.14.8 (`No download found`); 0.13.9 can. git and, for a pull request, gh are also needed. Doctor names uv and gh with a fix line; it does not check the uv version.
6. **Minor: with Review off, auto-accept still waits for a review round.** `show` says "waits for the person's Accept: 0 of 1 passing review rounds" and the Settings block says Review: off. The default merge setting asks the person first anyway, so nothing merges by itself.
7. **Minor: Shepherdr words remain.** The worker brief's Merge line and `commands.py` still say "they accept it in the Tasks panel" (`assistant_settings.MERGE_SHORT`). `templates.py`'s `REVIEWER_SANDBOX` line names `herdr-app check`. `mods/coordinator/restart.sh` reads `HERDR_APP_HOME`/`~/.config/herdr-app`, a known phase 2 item (design section 5). These are prompt and template texts, so changing them needs the replay evidence from [template-replay.md](template-replay.md). They are left for a follow-up.
8. **Minor: herdr-task refuses a private hc session.** With `HC_SESSION` set to anything other than empty or `default` (here `sdf13`), it says "refusing to run hc". This is by design (`base.not_real`), but it means a test on a shared machine has to use the default hc session. The Linux run did that and removed only its own terminals.
9. **Info: older hc on the VMs.** The VMs run hc 0.1.17 and 0.1.18, the Mac 0.1.21. Both worked for this path. On Tart, `hc server start` printed "run hc update to finish installing Herdr Core".

## Not proven

- **The merged pull request on the macOS test VM** (SDF-43 proved section 3 on the Linux test VM; `tart list` had no macOS test VM image available on the host Mac).
- Unattended runs on the macOS test VM (`agy` / `project start`): SDF-43's Linux PR run was unattended after #646, while the earlier SDF-13 runs had the tester answering dialogs, changing one setting, and accepting as the owner. Phase 2 (doctor, coordinator lease) removes the remaining step-ins.
- `project start` with a coordinator on either VM.
- Review rounds (Review was off) and CI checks (the demo repos have no CI).
