"""Starting agents: their argv, labels, brief and coordinator."""

import contextlib
import fcntl
import json
import os
import re
import secrets
import shlex
import shutil
import subprocess
import sys
import time

from .base import ENTRY, Fail, HOME, git, hc_json, kill_agent, label_of, now, PROJECT_LABEL, ROLE_LABEL, run, STORE, TASK_LABEL, WHERE, WORKTREES
from .settings import agent_key, ASSISTANT_TEMPLATES, by_layer, trust_roots, bare, BRANCH_PATTERN, branch_pattern_problem, gate_min, keep_review, notes, notes_part, read_settings, review_limits, settings_block, settings_for, sparse_exclude_of, sparse_exclude_pattern, worker_permissions
from .templates import instructions
from .agents import LINE_MAX, SUMMARY_MAX
from .github import remote_of


# ---- an agent's own herdr-task commands never ask -------------------------------------------
# At each launch the agent is allowed exactly this herdr-task's path, verb by verb, without
# touching the owner's CLI settings, through --settings on Claude Code and agy.

OWN = ["ready", "reviewed", "status", "alert", "ask", "simulator", "device-wait", "show", "list", "where"]
# herdr-secret's agent commands (~/.local/bin/herdr-secret): asking for a secret and
# using one by name never ask either, as Shepherdr's secret commands. Written by its full path or
# its bare name. Reviewers get none (launch() skips own_argv for them; herdr-secret refuses them).
SECRET = ["ask", "run", "env", "list"]
# A gate reviewer's shell (FAC-36): read-only commands, and the folder's check (review_reads).
REVIEW_READS = ["git log", "git diff", "git show", "git status", "git blame", "ls", "cat", "grep", "rg", "find", "sed -n",
                "head", "tail", "wc"]


def review_reads(check):
    """REVIEW_READS and each part of the folder's check as written: agy and Claude Code allow a
    compound line part by part (split at ;, &&, ||, | and line breaks outside quotes)."""
    parts = re.split(r"""(?:&&|\|\||[;|\n])(?=(?:[^'"]|'[^']*'|"(?:\\.|[^"\\])*")*$)""", check or "")
    return REVIEW_READS + [p.strip() for p in parts if p.strip()]


def secret_path():
    """herdr-secret on PATH (~/.local/bin, here or on another machine), else None."""
    return shutil.which("herdr-secret")


def own_path():
    """This herdr-task as agents write it: the one on PATH when that is this file, else this file."""
    me = ENTRY
    found = shutil.which("herdr-task")
    return found if found and os.path.realpath(found) == me else me


def own_rules(path, verbs=OWN):
    """Claude's allow rules for this exact path (verbs: a reviewer's are only its own verdict);
    a path a rule can't state literally gets none."""
    if not re.fullmatch(r"[\w@%+=:,./-]+", path):
        return []
    rules = [f"Bash({path} {verb}:*)" for verb in verbs]
    if verbs is not OWN:
        return rules
    sp = secret_path()
    for p in [sp, "herdr-secret"] if sp and re.fullmatch(r"[\w@%+=:,./-]+", sp) else []:
        rules += [f"Bash({p} {verb}:*)" for verb in SECRET]
    return rules


def claude_config_path():
    cfg = os.environ.get("CLAUDE_CONFIG_DIR")
    legacy, primary = os.path.join(cfg or os.path.join(HOME, ".claude"), ".config.json"), os.path.join(cfg or HOME, ".claude.json")
    try:
        return legacy if os.path.isfile(legacy) and os.path.getsize(legacy) > 0 and not os.path.isfile(primary) else primary
    except OSError:
        return primary


def _trusted(d, roots):
    p = d.get("projects") if isinstance(d, dict) else None
    return (isinstance(p, dict) and d.get("hasCompletedOnboarding") is True
            and all(isinstance(p.get(r), dict) and p[r].get("hasTrustDialogAccepted") is True for r in roots))


def claude_trust(folder):
    """Sets first-run onboarding and folder trust in Claude's .claude.json before launch."""
    roots = [r for r in trust_roots(folder) if r not in {"", os.sep, HOME, os.path.realpath(HOME)}]
    if not roots:
        return
    path = os.path.realpath(p) if os.path.islink(p := claude_config_path()) else p
    try:
        with open(path) as f:
            if _trusted(json.load(f), roots):
                return
    except FileNotFoundError:
        pass
    except (OSError, ValueError):
        return
    with contextlib.suppress(OSError):
        os.makedirs(os.path.dirname(path), exist_ok=True)
        os.makedirs(STORE, exist_ok=True)
    lk = None
    with contextlib.suppress(OSError):
        lk = open(os.path.join(STORE, ".claude-trust.lock"), "a")
    with lk or contextlib.nullcontext():
        if lk:
            fcntl.flock(lk, fcntl.LOCK_EX)
        mode, d = 0o600, {}
        try:
            mode = os.stat(path).st_mode & 0o777
            with open(path) as f:
                d = json.load(f)
        except FileNotFoundError:
            pass
        except (OSError, ValueError):
            return
        if not isinstance(d, dict) or (d.get("projects") is not None and not isinstance(d.get("projects"), dict)) or _trusted(d, roots):
            return
        d.setdefault("hasCompletedOnboarding", True)
        d["projects"] = {**(p := dict(d.get("projects") or {})), **{r: {**(p[r] if isinstance(p.get(r), dict) else {}), "hasTrustDialogAccepted": True} for r in roots}}
        tmp = f"{path}.{os.getpid()}.tmp"
        try:
            with os.fdopen(os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode or 0o600), "w") as f:
                json.dump(d, f, indent=2)
                f.write("\n")
            os.replace(tmp, path)
        except OSError:
            with contextlib.suppress(OSError):
                os.unlink(tmp)


def own_argv(argv, folder=None, verbs=OWN, cwd=None, reads=None):
    """An agent's argv with its own herdr-task commands allowed (Claude Code and agy), with folder
    trust and bypass acceptance recorded where needed. verbs: the herdr-task verbs allowed;
    cwd: the folder the agent runs in; reads: a reviewer's commands (review_reads)."""
    path, out = own_path(), list(argv)
    k = agent_key(out)
    if k == "claude":
        claude_trust(folder)
        rules = own_rules(path, verbs) + [f"Bash({r}:*)" for r in reads or []]
        bypass = (any(a in out for a in ("--allow-dangerously-skip-permissions", "--dangerously-skip-permissions",
                                         "--permission-mode=bypassPermissions"))
                  or any(a == "--permission-mode" and b == "bypassPermissions" for a, b in zip(out, out[1:])))
        if not rules and verbs is not OWN and not bypass:
            return out
        cur, i = {}, None
        if "--settings" in out[:-1]:
            i = out.index("--settings")
            try:
                cur = json.loads(out[i + 1])
            except ValueError:
                try:
                    with open(os.path.expanduser(out[i + 1])) as f:
                        cur = json.load(f)
                except (OSError, ValueError):
                    cur = None
            if not isinstance(cur, dict):
                cur, i = {}, None
        merged = dict(cur)
        if rules:
            perms = dict(cur.get("permissions") or {})
            perms["allow"] = list(perms.get("allow") or []) + [r for r in rules if r not in (perms.get("allow") or [])]
            merged["permissions"] = perms
        if verbs is OWN:
            # FAC-107: no idle compaction (it wakes a Ready task's worker); a value the argv sets wins
            merged.setdefault("idleCompaction", False)
        if bypass:
            merged.setdefault("skipDangerousModePermissionPrompt", True)
        if i is not None:
            out[i + 1] = json.dumps(merged)
            return out
        return out[:1] + ["--settings", json.dumps(merged)] + out[1:]
    if k == "codex":
        roots = [] if any(str(a).startswith("projects") for a in out) else trust_roots(folder)
        trust = ["-c", "projects={" + ",".join(f'{json.dumps(r)}={{trust_level="trusted"}}' for r in roots) + "}"] if roots else []
        return out[:1] + trust + out[1:]
    if k == "agy":
        # agy's --settings allow list replaces the person's own (~/.gemini/antigravity-cli): it carries
        # the argv's own --settings rules, then the person's, then these.
        own = [re.sub(r":\*\)$", ")", r.replace("Bash(", "command(", 1)) for r in own_rules(path, verbs)]
        try:
            with open(os.path.join(HOME, ".gemini", "antigravity-cli", "settings.json")) as f:
                theirs = list(json.load(f)["permissions"]["allow"])
        except (OSError, ValueError, KeyError, TypeError):
            theirs = []
        if reads is not None:  # a reviewer: none of the person's rules, which may write (sed, npm ci)
            theirs = [f"command({r})" for r in reads]
        trusted = {"trustedWorkspaces": [folder]} if folder else {}  # skips agy's "Trust this folder?" for it
        cur, spans = None, []  # every `--settings X` / `--settings=X` becomes one, at the first one's place
        for j, a in enumerate(out):
            if a == "--settings" and j + 1 < len(out):
                n, val = 2, out[j + 1]
            elif a.startswith("--settings="):
                n, val = 1, a[len("--settings="):]
            else:
                continue
            spans.append((j, n))
            try:
                got = json.loads(val)
            except ValueError:
                try:
                    with open(os.path.join(cwd or "", os.path.expanduser(val))) as f:
                        got = json.load(f)
                except (OSError, ValueError):
                    continue
            if isinstance(got, dict):  # a later one wins per key, but its allow and trust lists add to the earlier ones
                old = cur or {}
                allow = list((old.get("permissions") or {}).get("allow") or []) + list((got.get("permissions") or {}).get("allow") or [])
                cur = {**old, **got, "permissions": {**(old.get("permissions") or {}), **(got.get("permissions") or {}), "allow": allow},
                       "trustedWorkspaces": list(old.get("trustedWorkspaces") or []) + list(got.get("trustedWorkspaces") or [])}
                if not cur["trustedWorkspaces"]:
                    del cur["trustedWorkspaces"]
        if isinstance(cur, dict):
            perms = dict(cur.get("permissions") or {})
            allow = list(perms.get("allow") or [])
            perms["allow"] = allow + [rule for rule in theirs + own if rule not in allow]
            if folder:
                trusted = {"trustedWorkspaces": list(dict.fromkeys(list(cur.get("trustedWorkspaces") or []) + [folder]))}
            for j, n in reversed(spans[1:]):
                del out[j:j + n]
            i, n = spans[0]
            out[i:i + n] = ["--settings", json.dumps({**cur, **trusted, "permissions": perms})]
            return out
        return out[:1] + ["--settings", json.dumps({**trusted, "permissions": {"allow": theirs + own}})] + out[1:]
    return out


# ---- starting agents ------------------------------------------------------------------------


# ---- agy reviewers run sandboxed (FAC-49) ---------------------------------------------------
# agy matches allow rules by literal prefix, so a reviewer's every new command would prompt. On a Mac
# it runs instead with --dangerously-skip-permissions under sandbox-exec (reviewer.sb): it reads only
# its worktree, the repo's git dir and the system's tools, writes only its run's temp dir, and can't
# reach herdr-core's sockets. That dir is its HOME, holding all of agy's state for the run (a copy of
# its sign-in and onboarding only); the launcher's trap removes it, sweep_sandboxes() what a killed
# one left. It can't run herdr-task: the gate reads its verdict from its terminal (gate_reads).
SANDBOX_ROOT = f"/private/tmp/herdr-review-{os.getuid()}" if hasattr(os, "getuid") else ""
SANDBOX_SH = r"""root=$1 profile=$2 wt=$3 git=$4 nvm=$5 core=$6; shift 6
mkdir -p -m 700 "$root" && d=$(mktemp -d "$root/$$.XXXXXX") || exit 1
trap 'rm -rf "$d"' EXIT; trap 'exit 1' INT; trap 'exit 0' HUP TERM
g="$HOME/.gemini/antigravity-cli" a="$d/.gemini/antigravity-cli"; mkdir -p "$a/cache"
cp -p "$g/antigravity-oauth-token" "$a/"; cp -p "$g/cache/onboarding.json" "$a/cache/"
dev=$(xcode-select -p 2>/dev/null || echo /Library/Developer/CommandLineTools); agy=$(realpath "$(command -v "$1")")
PATH="$dev/usr/bin:$PATH" HOME="$d" TMPDIR="$d/" GIT_OPTIONAL_LOCKS=0 sandbox-exec -p "$profile" -D TMP="$d" -D WORKTREE="$wt" \
  -D GITDIR="$git" -D AGY="$agy" -D DEVDIR="$dev" -D NVM="$nvm" -D CORE="$core" "$@"
"""


def sandboxes(argv):
    """An agy reviewer here runs sandboxed: agy on a Mac with sandbox-exec. Elsewhere it keeps plan
    mode and its allow list (never skipped permissions without the sandbox)."""
    return agent_key(argv) == "agy" and sys.platform == "darwin" and os.path.exists("/usr/bin/sandbox-exec")


# The person's opt-out: with this file, an agy reviewer runs with skipped permissions as itself, outside
# sandbox-exec, so it signs in as the person's agy does (agy keeps its sign-in in the keychain, which
# the sandbox can't reach). Its brief still says read only; the rest (verdict from its terminal) is the same.
UNSANDBOXED_FLAG = os.path.join(WHERE.roots["config"], "agy-review-unsandboxed")


def reviewer_sandbox(s, space=None):
    """(on, by): do agy reviewers here run sandboxed? Settings' Tasks > Reviewer sandbox (tasks.reviewer_sandbox: on
    unless set off, per folder, machine or everywhere; by says which), and UNSANDBOXED_FLAG still counts as off (by "flag")."""
    if os.path.exists(UNSANDBOXED_FLAG):
        return False, "flag"
    on = settings_for(s)["tasks"].get("reviewer_sandbox") is not False
    return on, by_layer(s, None, "tasks", "reviewer_sandbox", on, True)


def sandboxed(argv, worktree, setting=True):
    """argv (agy's) run under reviewer.sb in a temp dir of its own (SANDBOX_SH); as it is with the Reviewer
    sandbox setting off (setting=False) or UNSANDBOXED_FLAG."""
    if not setting or os.path.exists(UNSANDBOXED_FLAG):
        return list(argv)
    import pkgutil
    common = git(worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")[1] or worktree
    real = os.path.realpath
    return ["sh", "-c", SANDBOX_SH, "herdr-sandbox", SANDBOX_ROOT, pkgutil.get_data(__package__, "reviewer.sb").decode(),
            real(worktree), real(common), real(os.path.join(HOME, ".nvm", "versions")),
            real(os.path.join(os.environ.get("XDG_STATE_HOME") or os.path.join(HOME, ".local", "state"), "herdr-core")), *argv]


def sweep_sandboxes():
    """Removes the temp dirs of sandboxed reviewers whose launcher (the pid each is named by) is gone."""
    for n in os.listdir(SANDBOX_ROOT) if SANDBOX_ROOT and os.path.isdir(SANDBOX_ROOT) else []:
        try:
            os.kill(int(n.split(".")[0]), 0)
        except ProcessLookupError:
            shutil.rmtree(os.path.join(SANDBOX_ROOT, n), ignore_errors=True)
        except (ValueError, OSError):
            pass


def claude_mode(argv, folder=None):
    """The permission mode a new Claude Code session would start in (#1056): argv's own flag, else the defaultMode of
    its --settings, the folder's .claude/settings.local.json and .claude/settings.json, then the person's
    (CLAUDE_CONFIG_DIR or ~/.claude), as Claude Code ranks them; "default" when none says."""
    for i, a in enumerate(argv):
        if a == "--dangerously-skip-permissions":
            return None
        if a == "--permission-mode" and i + 1 < len(argv):
            return argv[i + 1]
        if a.startswith("--permission-mode="):
            return a.split("=", 1)[1]
    found = []
    for i, a in enumerate(argv[:-1]):
        if a == "--settings":
            found.append(argv[i + 1])
    home = os.environ.get("CLAUDE_CONFIG_DIR") or os.path.join(HOME, ".claude")
    files = ([os.path.join(folder, ".claude", "settings.local.json"), os.path.join(folder, ".claude", "settings.json")] if folder else []) \
        + [os.path.join(home, "settings.json")]
    for src in found + files:
        try:
            if src.lstrip().startswith("{"):
                d = json.loads(src)
            else:
                with open(os.path.expanduser(src)) as f:
                    d = json.load(f)
        except (OSError, ValueError):
            continue
        mode = ((d.get("permissions") or {}) if isinstance(d, dict) else {}).get("defaultMode")
        if isinstance(mode, str) and mode:
            return mode
    return "default"


def resume_argv(argv, session, folder=None, *, worker=True):
    """Resume a session with the current worker argv and the recorded session id. Claude Code brings a resumed
    session back in the permission mode it ended in (#1056): the mode a new one would get is said, unless argv
    already says one (worker argv always does)."""
    if worker:
        argv = worker_permissions(argv)
    k = agent_key(argv)
    if k == "claude":
        mode = claude_mode(argv, folder)
        has = mode is None or any(a in ("--dangerously-skip-permissions", "--allow-dangerously-skip-permissions", "--permission-mode")
                                  or a.startswith("--permission-mode=") for a in argv)
        return argv + ([] if has else ["--permission-mode", mode]) + ["--resume", session]
    if k == "codex":
        return [argv[0], "resume", *argv[1:], session]
    if k == "agy":
        return argv + ["--conversation", session]
    return None


# ---- agents never inherit a parent Claude Code session (SHEP-86) ------------------------------
# herdr-core's holder keeps the environment of whatever started it, and every terminal gets it: a
# holder started from inside Claude Code hands each agent CLAUDECODE, CLAUDE_PID and
# CLAUDE_CODE_CHILD_SESSION, SESSION_ID, MESSAGING_*... Claude Code then saves no transcript (the
# web Chat can't find the conversation) and skips the folder-trust prompt. hc can only add
# variables, so the launch unsets them first: CLAUDECODE, CLAUDE_PID and CLAUDE_CODE_*, keeping the
# person's own Claude Code config (_USE_*, _SKIP_*, _DISABLE_*, _OAUTH_*..., CLAUDE_CONFIG_DIR).
# The same line is core/clean-env.js CLEAN_ENV's (test_repo keeps
# them equal).
CLEAN_ENV = ("for v in $(env | sed -nE 's/^(CLAUDECODE|CLAUDE_PID|CLAUDE_CODE_[A-Za-z0-9_]+)=.*/\\1/p' | "
             "grep -vE '^CLAUDE_CODE_(USE|SKIP|DISABLE|ENABLE|MAX|OAUTH|CLIENT|API)_'); do unset \"$v\"; done;")


# ---- agents never update themselves mid-run (FAC-87, field note 40) ----------------------------
# A self-updated binary makes macOS ask on the desktop whether it may use a removable volume, and
# every new agent there hangs until someone clicks Allow. Updating stays the owner's job. Only
# Claude Code has a switch (DISABLE_AUTOUPDATER); Codex and agy list none in --help or config, so
# they get nothing.
NO_UPDATE = {"claude": ["DISABLE_AUTOUPDATER=1"]}


def no_update_env(argv):
    """The env words that stop argv's agent CLI auto-updating (and carry CLAUDE_CONFIG_DIR when set)."""
    out = list(NO_UPDATE.get(agent_key(argv), []))
    if agent_key(argv) == "claude" and (cfg := os.environ.get("CLAUDE_CONFIG_DIR")):
        out.append(f"CLAUDE_CONFIG_DIR={cfg}")
    return out


def clean_env(argv):
    """argv run without a parent Claude Code session's markers (CLEAN_ENV)."""
    return ["sh", "-c", CLEAN_ENV + ' exec "$@"', "herdr-clean", *argv]


def in_shell(argv):
    """Runs the agent in the person's interactive shell, as Herdr does: their functions and
    aliases apply (claude() adding flags), and the shell stays when the agent exits."""
    sh = os.environ.get("SHELL") or "/bin/sh"
    name = os.path.basename(sh)
    flag = "-lic" if sys.platform == "darwin" else "-ic"
    if name in ("bash", "zsh", "sh", "ksh", "dash"):
        return [sh, flag, '"$@"; exec "$0" -i', sh] + argv
    if name == "fish":
        return [sh, flag, " ".join(shlex.quote(a) for a in argv) + "; exec fish -i"]
    return argv


def reviewer_shell(argv):
    """in_shell() for a reviewer: no interactive shell stays when its CLI exits, and end_reviewer's
    TERM/HUP stops it with exit code 0 instead of a hangup exit 1."""
    out = in_shell(argv)
    if len(out) >= 3 and out[2] == '"$@"; exec "$0" -i':
        return [*out[:2], 'trap "exit 0" HUP TERM; "$@"', *out[3:]]
    if len(out) == 3 and out[2].endswith("; exec fish -i"):
        return [*out[:2], "function _end --on-signal HUP --on-signal TERM; exit 0; end; " + out[2][:-len("; exec fish -i")]]
    return out


# ---- workers run at low priority (HERD-61) ---------------------------------------------------
# So agents never take the machine down: a task's worker and reviewer start below everything else
# (Settings > Machines "Workers run at low priority", on unless turned off): CPU nice 10, IO
# best-effort 7, and an OOM score of +500, so under memory pressure the kernel ends a worker
# first, never herdr-core's holder or an Assistant. Where systemd's user manager runs, each worker
# also gets its own scope (with the optional per-worker memory cap as MemoryHigh), so oomd, which
# ends whole cgroups, ends that worker rather than the holder that keeps every terminal. On a Mac:
# nice 10 and the utility QoS clamp (taskpolicy). Anything missing is skipped, never fatal.

LOW = ('{ echo 500 > /proc/self/oom_score_adj; } 2>/dev/null; '
       'command -v ionice >/dev/null 2>&1 && ionice -c2 -n7 -p $$ >/dev/null 2>&1; '
       'command -v taskpolicy >/dev/null 2>&1 && exec nice -n 10 taskpolicy -c utility "$@"; '
       'exec nice -n 10 "$@"')
_scope = {}


def scope_argv():
    """systemd-run's words for a scope of its own under the user manager, or None where there's
    none. systemd 254+ expands $ in the command unless told not to: checked once, by running."""
    if "argv" in _scope:
        return _scope["argv"]
    _scope["argv"] = None
    if sys.platform.startswith("linux") and shutil.which("systemd-run"):
        for extra in (["--expand-environment=no"], []):
            argv = ["systemd-run", "--user", "--scope", "--quiet", "--collect", *extra]
            try:
                r = subprocess.run(argv + ["--", "sh", "-c", 'echo "$0"', "herdr-ok"], stdout=subprocess.PIPE,
                                   stderr=subprocess.DEVNULL, universal_newlines=True, timeout=10)
            except (OSError, subprocess.SubprocessError):
                break
            if r.returncode == 0 and r.stdout.strip() == "herdr-ok":
                _scope["argv"] = argv
                break
    return _scope["argv"]


def low_priority(argv, name):
    """argv started low (LOW), in a scope of its own where there is one; as is when the person
    turned it off."""
    from .settings import machine_option
    if machine_option("low_priority", True) is False or sys.platform.startswith("win"):
        return argv
    out = ["sh", "-c", LOW, "herdr-low", *argv]
    scope = scope_argv()
    if scope:
        cap = machine_option("worker_memory")
        unit = re.sub(r"[^A-Za-z0-9_.-]+", "-", f"herdr-{name}-{secrets.token_hex(3)}")
        out = [*scope, f"--unit={unit}", *([f"--property=MemoryHigh={int(cap)}G"] if isinstance(cap, (int, float)) and cap > 0 else []),
               "--", *out]
    return out


def caller_labels():
    me = os.environ.get("HC_TERMINAL_ID")
    k = hc_json("get", me) if me else None
    return (k or {}).get("labels") or {}


def closing_line(s, task, parent=None):
    """How the worker finishes: every task, worktree or in the folder, says ready with a summary.
    It names this herdr-task by its full path: only that form runs without asking (own_argv)."""
    done = "finished and committed" if task.get("mode") == "worktree" else "finished"
    ht = own_path()
    text = (f"When the work is {done}, run `{ht} ready {s.ref(task)} --folder {shlex.quote(s.loc['folder'])} "
            f"--summary \"<what the person now sees or can do, plain words, at most {SUMMARY_MAX} characters>\"` (optionally "
            f"`--details \"<a few short bullets in plain words, one per line: what changed for the person first, then how it was checked>\"`; with `--agents-md \"<one line>\"` when you learned something "
            f"about setting up or testing this repo that its AGENTS.md lacks). The person accepts it from there; never mark it done yourself.")
    on, rounds = review_limits(s, task)
    if gate_min(s, task):
        text += (f" Review is herdr-task's own: after ready it starts the reviewer for your final commit and tells you what it "
                 f"finds; `reviewed` doesn't count here.")
    elif on:
        text += (f" Review is on: after each review round, record it on the commit it reviewed with `{ht} reviewed {s.ref(task)} "
                 f"--folder {shlex.quote(s.loc['folder'])} --verdict pass|changes --majors N --report \"<its findings in a line>\"` (add `--commit <sha>` if you committed fixes since; at most {rounds} rounds); "
                 f"ready refuses a final commit no round was recorded on.")
    text += (f" At the start, at each natural step and at least every 5 minutes while you work, say how it's going: `{ht} status {s.ref(task)} --folder {shlex.quote(s.loc['folder'])} "
             f"\"<what you're doing now, at most {LINE_MAX} characters>\" --done <% of the whole task> --left <your best guess of how long is left, like 20m>`: "
             f"always give `--left`, from your very first status on, and revise it honestly each time from how the work is really going; "
             f"a rough guess beats none.")
    text += (f" Only for what truly can't wait, `{ht} alert \"<what, a few words>\" --body \"<why>\"` reaches the person at once; "
             f"a question goes through `{ht} ask` instead.")
    text += f" Use the full path {ht} for {', '.join(OWN)}. Run each as one plain command."
    from .simulators import simulator_line
    text += simulator_line(s, task)
    if secret_path():
        text += (" Secrets (keys, tokens, passwords) are used by name, never seen: `herdr-secret ask NAME --why \"…\"` "
                 "(then end your turn), `herdr-secret run NAME -- <command>`, or `herdr-secret list`.")
    # No "report to your coordinator": Herdr tells the Assistant itself (news_pass).
    return text


def where_line(task):
    if not task.get("worktree"):
        return ""
    base = f", from {task['base']}, which it merges into" if task.get("base") else ""
    return f"You work in {task['worktree']} on branch {task['branch']}{base}."


CHANGES_HEAD = "Brief changes (newest last, these win over the brief above):"


def stamp(ms):
    return time.strftime("%Y-%m-%d %H:%M", time.localtime(ms / 1000))


def task_text(task):
    """The task's brief as given, then its dated amendments (FAC-109, `edit --amend`): what workers and reviewers judge by."""
    text = (task.get("prompt") or "").strip() or task["title"]
    changes = [f"- {stamp(a['at'])}: {a['text']}" for a in task.get("amendments") or []]
    return "\n\n".join([text, CHANGES_HEAD + "\n" + "\n".join(changes)]) if changes else text


QUOTA_RULE = "Quota is watched for you. Do not run quota tools."


def brief(s, task, parent=None, argv=None):
    instructions("reviewer")  # the file its review's reviewer gets (WORKER_MD)
    keep_review(s, task)
    start = notes_part("Before you start", notes(s, task, "start"))
    if start and task.get("worktree"):
        start += f"\n(The main folder is {s.loc['folder']}.)"
    body = "Your task, " + s.ref(task) + ":\n" + task_text(task)
    finish = notes_part("Before you say ready", notes(s, task, "finish"))
    return "\n\n".join(x for x in [instructions("worker"), start, body, where_line(task), finish, closing_line(s, task, parent),
                                    QUOTA_RULE, settings_block(s, task, argv=argv, live=True)] if x)


def handoff(s, task, prev, parent=None, argv=None):
    lines = [f"You are taking over task {s.ref(task)} \"{task['title']}\" from another agent.", instructions("worker")]
    lines.append(where_line(task))
    lines.append("The task:\n" + task_text(task))
    from .handoff import work_context
    lines.append(work_context(s, task, prev))
    if prev and prev.get("log"):
        lines.append(f"The previous agent ({prev.get('agent') or 'agent'}) kept its session log at {prev['log']}: read the recent part to pick up where it left off.")
    elif prev:
        lines.append(f"The previous agent ({prev.get('agent') or 'agent'}) left no session log; check the folder's state (git status, git log) before you continue.")
    lines.append(notes_part("Before you continue, if it isn't done yet", notes(s, task, "start")))
    lines.append(notes_part("Before you say ready", notes(s, task, "finish")))
    lines.append(closing_line(s, task, parent))
    lines.append(QUOTA_RULE)
    lines.append(settings_block(s, task, argv=argv, live=True))
    return "\n\n".join(x for x in lines if x)


def current_base(main):
    """The folder's base: the branch checked out in it (else its commit)."""
    ok, base, _ = git(main, "symbolic-ref", "--short", "HEAD")
    return base if ok else git(main, "rev-parse", "HEAD")[1]


def base_ref(main, base):
    """A task's own base branch (--base), ready to branch from: the local branch, else the remote's
    (fetched; a local branch is made to track it). Fails when neither has it."""
    if not re.fullmatch(r"[\w./@+-]+", base) or base.startswith("-"):
        raise Fail(f"{base!r} isn't a branch name.")
    if git(main, "show-ref", "--verify", "--quiet", f"refs/heads/{base}")[0]:
        return base
    names = git(main, "remote")[1].split()
    remote = remote_of(main, base)
    if remote in names:
        git(main, "fetch", remote, base)
        if git(main, "show-ref", "--verify", "--quiet", f"refs/remotes/{remote}/{base}")[0]:
            ok, _, err = git(main, "branch", "--track", base, f"{remote}/{base}")
            if not ok:
                raise Fail(err or f"Couldn't make a local {base}.")
            return base
        raise Fail(f"There is no branch {base} here or on {remote}.")
    raise Fail(f"There is no branch {base} here.")


def git_quick(cwd, *args, timeout=15):
    """git that may reach the network (fetch, ls-remote): never asks for a password and gives up
    after `timeout` seconds, so an offline or slow remote costs a moment, not a hang."""
    env = {**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_SSH_COMMAND": os.environ.get("GIT_SSH_COMMAND") or "ssh -o BatchMode=yes"}
    try:
        r = subprocess.run(["git", "-C", cwd, *args], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                           universal_newlines=True, env=env, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        return False, "", "timed out"
    return r.returncode == 0, r.stdout.strip(), r.stderr.strip()


def fresh_start(main, base):
    """Where a new worktree on `base` starts (#574): the base fetched from the remote first, so a
    task branches from the latest even when others merge on GitHub. The remote's when it holds
    everything the local branch has (the local one is only behind); else the local branch (it has
    work of its own, or the fetch failed: offline, no remote, no such branch there). The folder's
    own checkout is never moved."""
    remote = remote_of(main, base)
    if remote not in git(main, "remote")[1].split() or not git(main, "show-ref", "--verify", "--quiet", f"refs/heads/{base}")[0]:
        return base
    git_quick(main, "fetch", "--quiet", "--no-tags", remote, f"+refs/heads/{base}:refs/remotes/{remote}/{base}")
    theirs = f"refs/remotes/{remote}/{base}"
    if git(main, "show-ref", "--verify", "--quiet", theirs)[0] and git(main, "merge-base", "--is-ancestor", f"refs/heads/{base}", theirs)[0]:
        return f"{remote}/{base}"
    return base


def branch_problem(main, name):
    """Why `name` can't be a branch (git check-ref-format's rules), or None."""
    if not name or name.startswith("-") or not git(main, "check-ref-format", "--branch", name)[0]:
        return f"{name!r} isn't a valid branch name."
    return None


def branch_taken(s, t, name):
    """Who has branch `name` already: another task here, this repo, or its remote (asked now when
    it answers within seconds, else as git last fetched it). None: it's free."""
    for o in s.data.get("tasks") or []:
        if o is not t and o.get("branch") == name:
            return f"{s.ref(o)} already uses it"
    main = s.loc["folder"]
    if git(main, "show-ref", "--verify", "--quiet", f"refs/heads/{name}")[0]:
        return "a branch by that name is already here"
    remote = remote_of(main, name)
    if remote in git(main, "remote")[1].split():
        ok, out, _ = git_quick(main, "ls-remote", "--heads", remote, f"refs/heads/{name}", timeout=10)
        if (ok and out) or (not ok and git(main, "show-ref", "--verify", "--quiet", f"refs/remotes/{remote}/{name}")[0]):
            return f"{remote} already has a branch by that name"
    return None


def set_branch(s, t, name):
    """--branch: the task's exact branch name (#570), e.g. a ticket's ABC-2137, instead of its
    folder's pattern. Checked as git does, and refused when that branch exists already (here, on
    the remote, or another task's): never a silent suffix. "" goes back to the pattern."""
    if name is None:
        return
    name = name.strip()
    if t.get("worktree"):
        if name == t.get("branch"):
            return
        raise Fail(f"{s.ref(t)} already has its worktree on {t.get('branch')}; its branch can't change now.")
    if not name:
        t.pop("branch", None)
        return
    if t.get("mode") != "worktree":
        raise Fail("--branch is for tasks in a worktree; this one works in the folder itself.")
    if name == t.get("branch"):
        return
    problem = branch_problem(s.loc["folder"], name)
    if problem:
        raise Fail(problem)
    taken = branch_taken(s, t, name)
    if taken:
        raise Fail(f"Branch {name}: {taken}. Pick another name.")
    t["branch"] = name


def branch_for(s, task):
    """A task's branch: its own (--branch), else its folder's pattern (settings > tasks.branch:
    {n} its number, {key} its id, {slug} its title's words), else task/{n}-{slug}."""
    if task.get("branch"):
        return task["branch"]
    words = re.sub(r"[^a-z0-9]+", "-", task["title"].lower()).strip("-")[:40].strip("-") or "task"
    pattern = str(settings_for(s)["tasks"].get("branch") or "").strip()
    if not pattern or branch_pattern_problem(pattern):
        pattern = BRANCH_PATTERN
    name = pattern.replace("{n}", str(task["id"])).replace("{key}", s.ref(task)).replace("{slug}", words)
    if branch_problem(s.loc["folder"], name):
        name = BRANCH_PATTERN.replace("{n}", str(task["id"])).replace("{slug}", words)
    return name


def folder_for(s, task):
    if task.get("mode") != "worktree":
        return s.loc["folder"]
    if task.get("worktree") and os.path.isdir(task["worktree"]):
        return task["worktree"]
    if s.loc["kind"] != "repo":
        raise Fail("Worktrees need a git repo. Use --here.")
    main = s.loc["folder"]
    branch = branch_for(s, task)
    # New worktrees under WORKTREES (or tasks.worktree_root);
    # a task that has one keeps it wherever it is (git keeps its absolute path).
    root = os.path.expanduser(str(settings_for(s)["tasks"].get("worktree_root") or "").strip()) or WORKTREES
    path = os.path.join(root, os.path.basename(main), re.sub(r"[^A-Za-z0-9._-]+", "-", branch).strip("-"))
    # Its own base (--base) branches from that branch; else from what the folder has checked out.
    own = task.get("base") if task.get("base_set") else None
    base = base_ref(main, own) if own else current_base(main)
    have = registered_worktree(main, path)
    if have:
        # #1023: its worktree is still there, registered with this repo, though the task no longer
        # names it (its agent may have switched it to another branch): it goes on there, on the branch
        # checked out now, never a failed "already exists".
        br = have.get("branch") or branch
        base = task.get("base") or base
        start = task.get("start") or git(main, "merge-base", base, have.get("head") or br)[1] or have.get("head")
        task.update(worktree=path, branch=br, base=base, start=start)
        return path
    exists = git(main, "show-ref", "--verify", "--quiet", f"refs/heads/{branch}")[0]
    exclude = sparse_exclude_of(s)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    no_checkout = ["--no-checkout"] if exclude else []
    if exists:
        add = ["worktree", "add", *no_checkout, path, branch]
    else:
        # From the base's latest (fetched first); a detached folder (base is a commit) from HEAD.
        branchy = git(main, "show-ref", "--verify", "--quiet", f"refs/heads/{base}")[0]
        add = ["worktree", "add", *no_checkout, "--no-track", "-b", branch, path, fresh_start(main, base) if branchy else (own or "HEAD")]
    ok, _, err = git(main, *add)
    if not ok:
        raise Fail(err or "git worktree add failed")
    if exclude:
        def _rollback():
            git(main, "worktree", "remove", "--force", path)
            if not exists:
                git(main, "branch", "-D", branch)

        ok, _, err = git(path, "sparse-checkout", "set", "--no-cone", "/*", *[sparse_exclude_pattern(p) for p in exclude])
        if not ok:
            _rollback()
            raise Fail(err or "git sparse-checkout set failed")
        ok, _, err = git(path, "checkout")
        if not ok:
            _rollback()
            raise Fail(err or "git checkout failed")
    # Where the branch starts: work past it that turns up in the base branch means merged.
    start = git(path, "rev-parse", "HEAD")[1] if not exists else task.get("start")
    task.update(worktree=path, branch=branch, base=task.get("base") or base, start=start)
    return path


# Codex's "Trust this folder?" screen: whatever opens the reviewer's prompt first must wait it out, or the prompt
# typed while it shows is lost (FAC-80). Only a reviewer's own task repository ever shows it.
CODEX_TRUST = re.compile(r"Trust this folder|Do you trust the contents|Yes, continue", re.I)
CODEX_READY = re.compile(r"^\s*[›>]\s|\bcontext left\b|\? for shortcuts", re.I | re.M)  # its input box, once startup is over


def send_when_clear(address, message, tries=60):
    """Sends a Codex reviewer its prompt once its screen shows the input box and no trust dialog (answering a
    dialog with enter, once). Never sent blind: if that doesn't come within `tries` seconds the reviewer is
    ended and the launch fails (the gate tries its second CLI)."""
    answered = False
    for _ in range(tries):
        screen = run(["hc", "read", address, "--lines", "40"]).stdout or ""
        if CODEX_TRUST.search(screen):
            if not answered:
                run(["hc", "keys", address, "enter"])
                answered = True
        elif CODEX_READY.search(screen):
            return run(["hc", "send", address, message])
        time.sleep(1)
    kill_agent(address, run)
    raise Fail("the Codex reviewer never showed its prompt box (trust screen or startup stuck)")


def registered_worktree(main, path):
    """The worktree git has registered at path for the repo at main ({path, head, branch}: branch
    None when detached), when its folder is there; else None."""
    if not os.path.isdir(path):
        return None
    ok, out, _ = git(main, "worktree", "list", "--porcelain")
    want = os.path.realpath(path)
    for block in out.split("\n\n") if ok else []:
        rows = dict((ln.split(" ", 1) + [""])[:2] for ln in block.splitlines() if ln.strip())
        if rows.get("worktree") and os.path.realpath(rows["worktree"]) == want and "prunable" not in rows:
            ref = rows.get("branch") or ""
            return {"path": path, "head": rows.get("HEAD"),
                    "branch": ref[len("refs/heads/"):] if ref.startswith("refs/heads/") else None}
    return None


def launch(s, task, argv, message=None, parent=None, resume=None, role=None, reads=None):
    """Starts one agent for the task as an hc child of `parent` (the project's coordinator).
    Stamps sheepdog.project, sheepdog.task, sheepdog.role and sheepdog.agent (plus herdr.*
    compatibility labels); reads: a reviewer's read-only commands (review_reads). Returns its record."""
    key = s.key(task)
    slug = getattr(s, "slug", None) or key.partition("#")[0]
    if not role:
        argv = worker_permissions(argv, settings_for(s)["worker"].get("perm"))
    cmd, folder = list(resume or argv), folder_for(s, task)
    sand = role == "reviewer" and sandboxes(cmd)
    trust = folder if task.get("mode") == "worktree" or agent_key(cmd) != "agy" else None
    if not role:
        cmd = own_argv(cmd, trust, cwd=folder)
    elif sand:  # its fresh HOME trusts no folder yet
        cmd = cmd + ["--settings", json.dumps({"trustedWorkspaces": [folder]})]
    elif role == "reviewer" and agent_key(cmd) in ("agy", "claude"):
        # a read-only reviewer: its worktree trusted, its verdict (herdr-task review) and `reads` allowed
        cmd = own_argv(cmd, trust, verbs=("review",), cwd=folder, reads=reads)
    held = message if role == "reviewer" and agent_key(cmd) == "codex" else None
    if message and not held:
        cmd = cmd + (["-i", message] if agent_key(cmd) == "agy" else [message])  # agy reads prompts only from -i
    if sand:
        cmd = sandboxed(cmd, folder, reviewer_sandbox(s)[0])
    labels = ["--label", f"{PROJECT_LABEL}={slug}", "--label", f"{TASK_LABEL}={key}",
              "--label", f"{ROLE_LABEL}={role or 'worker'}", "--label", f"sheepdog.agent={agent_key(argv)}",
              "--label", f"herdr.task={key}", "--label", f"herdr.agent={agent_key(argv)}"]
    if role:
        labels += ["--label", f"herdr.role={role}"]
    if sand:  # never revived (revive.watched): it would come back unsandboxed
        labels += ["--label", "sheepdog.sandbox=1", "--label", "herdr.sandbox=1"]
    rel = ["--parent", parent, "--outlive-parent", *(["--no-notify"] if role == "reviewer" else [])] if parent else ["--no-parent"]
    keep = [] if role == "reviewer" else ["--keep"]
    sh = reviewer_shell(cmd) if role == "reviewer" else in_shell(cmd)
    name = agent_name(s, task, "review" if role else None)
    # HERDR_BROWSER_TASK stays beside SHEEPDOG_TASK until SDF-8/SDF-11 convert revive.py and worker.md.
    r = run(["hc", "run", "--bg", "--json", *keep, "--cwd", folder, "--name", name,
             *labels, *rel, "--", *low_priority(clean_env(["env", f"SHEEPDOG_TASK={key}", f"HERDR_BROWSER_TASK={key}", *no_update_env(cmd), *sh]), name)])
    try:
        out = json.loads(r.stdout)
    except ValueError:
        out = {}
    if r.returncode != 0 or not out.get("address"):
        raise Fail((r.stderr or r.stdout).strip() or "hc run failed")
    if held:
        send_when_clear(out["address"], held)
    a = {"address": out["address"], "agent": agent_key(argv), "argv": argv, "at": now()}
    if parent:
        a["parent"] = parent
    s.dirty = True
    if role:
        task.setdefault("reviewers", []).append(a)
        return a
    task["agents"].append(a)
    track(s, task, out["address"])
    return a


def is_coordinator(k):
    return bool(k) and (label_of(k.get("labels"), "role") == "coordinator" or k.get("template") in ASSISTANT_TEMPLATES)


def coordinators():
    """The live coordinators' addresses on this machine (without labels: hc get each)."""
    # Only coordinators get an hc get (labels): there can be thousands of terminals.
    found = [k for k in (hc_json("ls") or {}).get("terminals", []) if k.get("template") in ASSISTANT_TEMPLATES]
    found += (hc_json("ls", "--label", f"{ROLE_LABEL}=coordinator") or {}).get("terminals", [])
    found += (hc_json("ls", "--label", "herdr.role=coordinator") or {}).get("terminals", [])
    return list(dict.fromkeys(k["address"] for k in found if k.get("state") == "live" and "/" not in k["address"]))


def coordinator_in(project):
    """The project's live coordinator on this machine: its lease holder (lease.py), or None. A terminal without the
    lease is never the coordinator, whatever its name or labels (note 55)."""
    from .lease import holder
    return holder(project)


def parent_for(parent, project=None):
    """A task agent's parent: `parent` while it's live, else the project's coordinator."""
    return live_parent(parent) or coordinator_in(project)


def agent_name(s, task, suffix=None):
    """The agent's hc name: its id and title as hc allows names (hat-4-fix-login, at most 32),
    with -2, -3 while another live terminal has it (an agent taking over beside the old one)."""
    base = re.sub(r"[^a-z0-9]+", "-", f"{s.ref(task)} {task['title']}".lower()).strip("-")[:32 - (len(suffix) + 1 if suffix else 0)].rstrip("-")
    if suffix:
        base += f"-{suffix}"
    for n in range(1, 20):
        name = base if n == 1 else f"{base[:32 - len(str(n)) - 1].rstrip('-')}-{n}"
        k = hc_json("get", name)
        if not k or k.get("state") != "live":
            return name
    return f"{base[:24].rstrip('-')}-{secrets.token_hex(3)}"


def track(s, task, address):
    """Watches the agent in the background and records its session as it changes, so a stopped
    task can be resumed later."""
    me = ENTRY
    subprocess.Popen([sys.executable, me, "_track", str(task["id"]), address, "--folder", s.loc["folder"]],
                     stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                     start_new_session=True)


def live_parent(addr):
    """A coordinator to report to, if it's still there (one that restarted has a new address)."""
    k = hc_json("get", addr) if addr else None
    return addr if k and k.get("state") == "live" else None
