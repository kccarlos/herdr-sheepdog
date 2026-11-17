#!/usr/bin/env python3
"""Measure daily owner touches and outside ("babysitter") interventions across factory projects.

Read-only, Python standard library only. Reads local Shepherdr usage logs, task stores,
Autopilot state, and Claude Code / Codex session JSONL logs, and prints aggregate counts
only — never quoting, storing, or emitting message text or personal paths.

Usage:
    uv run --python 3 scripts/factory/baseline.py [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--json]
    python3 scripts/factory/baseline.py [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--json]
"""

from __future__ import annotations

import argparse
from collections import Counter, defaultdict
import datetime
import glob
import json
import os
import re
import statistics
import sys
from typing import Any

DEFAULT_SINCE = "2026-11-08"

TOUCHPOINTS = ("goal", "rule", "ask_answer", "release_approval", "digest", "other")
CHANNELS = (
    "assistant_chat",
    "worker_chat",
    "outside_orchestrator",
    "ui_accept",
    "ask_card",
    "settings",
)
INTERVENTION_TYPES = (
    "hc_send",
    "hc_keys",
    "hc_kill",
    "herdr_task_accept",
    "herdr_task_edit",
    "herdr_task_lifecycle",
    "herdr_task_admin",
    "herdr_app_do",
    "kill_process",
    "store_edit",
)

HT_LIFECYCLE_VERBS = frozenset({
    "merge", "done", "resume", "switch", "review-more", "reopen",
    "fix", "start", "add", "rm", "recap", "ready", "reviewed", "tell-assistant",
})
HT_ADMIN_VERBS = frozenset({
    "config", "autopilot", "scheduler", "tick", "project",
})
HT_READONLY_VERBS = frozenset({
    "list", "show", "where", "assistants", "machines", "home",
    "status", "asks", "star", "feedback", "--help", "-h",
})

TEMP_STORE_RE = re.compile(r"/(?:tmp|T)/(?:tmp\.|pytest-|herdr-test|sde-|sdp\d|qa\d)")

CLI_INJECTED_RE = re.compile(
    r"^\s*(<(local-command-|system-reminder|bash-|task-notification|user-memory-input|"
    r"user-prompt-submit-hook|command-message)|Caveat: The messages below|\[Request interrupted)"
)
CODEX_INJECTED_RE = re.compile(
    r"^\s*(# AGENTS\.md instructions|<(environment_context|user_instructions|user_shell_command|"
    r"turn_aborted|subagent_notification|skill|INSTRUCTIONS|permissions)\b)"
)
SYSTEM_PROSE_RE = re.compile(
    r"^(Carry on with what you were doing; the last turn hit an API error|"
    r"Your last turn was cut off by the model's provider \([^)]*\)|"
    r"Your background jobs? .* ended \(no longer running\); check |"
    r"Stop [A-Z]+-\d+: Herdr's loop-breaker says recap first \("
    r"|Your Assistant read the recap of [A-Z]+-\d+ and says go on:|"
    r"Herdr resumed you:|Herdr's review round \d+ of |Review round \d+ of |"
    r"Merge task [A-Z]+-\d+ \"|Task [A-Z]+-\d+ .* can't merge yet:|"
    r"You are taking over this tab from )"
)

RELEASE_RE = re.compile(
    r"\b(testflight|app\s*store|release\s+v?\d|ship\s+v?\d|tag\s+(?:ios-)?v\d|"
    r"(?:push|cut|approve)\s+(?:the\s+)?(?:release|tag)|ios-v\d+\.\d+|v\d+\.\d+\.\d+)\b",
    re.I,
)
RULE_RE = re.compile(
    r"\b(from now on|standing rule|by default|always\s+(?:use|run|ask|keep|set)|"
    r"never\s+(?:use|run|ask|merge|auto)|autopilot|max[_-]running|review\s+rounds?|"
    r"codexbar|quota\s+rule|pool\s+order|pace\s+the|delegate\s+(?:all|m\d|patch|design|accept))\b",
    re.I,
)
DIGEST_RE = re.compile(
    r"^\s*(how(?:'s| is| are)\b|what(?:'s| is)\s+(?:the\s+)?(?:status|progress|left|blocking|running|waiting|needing)|"
    r"what\s+needs\s+me|status\b|progress\b|update\b|digest\b|summary\b|where\s+are\s+we|"
    r"anything\s+(?:stuck|waiting|needing)|how\s+did\s+(?:last\s+)?night\s+go)",
    re.I,
)
GOAL_RE = re.compile(
    r"\b(build\b|implement\b|milestone\b|roadmap\b|phase\s+\d|requirements?\b|"
    r"next\s+(?:feature|milestone|release|phase)|start\s+a\s+project|new\s+feature|"
    r"design\s+and\s+roadmap|plan\s+(?:out|the|tasks))\b",
    re.I,
)
SHORT_ANSWER_RE = re.compile(
    r"^\s*(yes\b|yep\b|yeah\b|ok\b|okay\b|sure\b|go\s+ahead|approved?\b|accept\b|"
    r"ship\s+it|merge\s+it|do\s+it|sounds\s+good|looks\s+good|lgtm\b|no\b|nope\b|"
    r"hold\s+off|wait\b|skip\b|drop\s+it|option\s+\d|[1-9]\b)",
    re.I,
)


def parse_iso_ms(stamp: Any) -> int | None:
    if not stamp:
        return None
    try:
        s = str(stamp).strip()
        if s.endswith("Z"):
            s = s[:-1] + "+00:00"
        return int(datetime.datetime.fromisoformat(s).timestamp() * 1000)
    except (TypeError, ValueError):
        return None


def local_date_str(ms: int | float) -> str:
    return datetime.datetime.fromtimestamp(ms / 1000).strftime("%Y-%m-%d")


def date_range_days(since: str, until: str) -> list[str]:
    d0 = datetime.date.fromisoformat(since)
    d1 = datetime.date.fromisoformat(until)
    if d1 < d0:
        return []
    out = []
    cur = d0
    while cur <= d1:
        out.append(cur.isoformat())
        cur += datetime.timedelta(days=1)
    return out


def safe_json_load(path: str) -> Any:
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def is_temp_path(path: str) -> bool:
    return bool(TEMP_STORE_RE.search(path))


def load_projects(tasks_dir: str) -> tuple[dict[str, dict[str, Any]], dict[str, tuple[str, str]], dict[str, str]]:
    """Discover factory projects, terminal roles, and space mappings from task store JSON files."""
    projects: dict[str, dict[str, Any]] = {}
    term_map: dict[str, tuple[str, str]] = {}  # address -> (project_name, role)
    space_map: dict[str, str] = {}             # space_id -> project_name

    if not tasks_dir or not os.path.isdir(tasks_dir):
        return projects, term_map, space_map

    try:
        entries = sorted(os.listdir(tasks_dir))
    except OSError:
        return projects, term_map, space_map

    for fn in entries:
        if fn.startswith(".") or not fn.endswith(".json"):
            continue
        fp = os.path.join(tasks_dir, fn)
        if not os.path.isfile(fp):
            continue
        data = safe_json_load(fp)
        if not isinstance(data, dict):
            continue
        folder = str(data.get("folder") or "").strip()
        if not folder or is_temp_path(folder):
            continue
        norm_folder = os.path.normpath(os.path.expanduser(folder))
        name = os.path.basename(norm_folder.rstrip("/")) or fn[:-5]
        prefix = str(data.get("prefix") or name).upper()
        tasks = data.get("tasks") if isinstance(data.get("tasks"), list) else []

        proj = projects.setdefault(name, {
            "name": name,
            "prefix": prefix,
            "folders": set(),
            "slug": fn[:-5],
            "tasks": [],
        })
        proj["folders"].add(norm_folder)
        try:
            proj["folders"].add(os.path.realpath(norm_folder))
        except OSError:
            pass
        proj["tasks"].extend(t for t in tasks if isinstance(t, dict))

        for t in proj["tasks"]:
            sp = str(t.get("space") or "").strip()
            if sp:
                space_map[sp.split("/")[-1]] = name
            for a in t.get("agents") or []:
                if not isinstance(a, dict):
                    continue
                if a.get("address"):
                    term_map[str(a["address"])] = (name, "worker")
                if a.get("parent"):
                    term_map.setdefault(str(a["parent"]), (name, "assistant"))
            for r in t.get("review_runs") or []:
                if isinstance(r, dict) and r.get("address"):
                    term_map[str(r["address"])] = (name, "reviewer")

    # Map autopilot spaces to projects where possible
    ap = safe_json_load(os.path.join(tasks_dir, ".autopilot.json"))
    if isinstance(ap, dict) and isinstance(ap.get("spaces"), dict):
        for sp_id, rec in ap["spaces"].items():
            if not isinstance(rec, dict):
                continue
            sp_bare = sp_id.split("/")[-1]
            if sp_bare in space_map:
                continue
            sp_folder = os.path.normpath(os.path.expanduser(str(rec.get("folder") or "")))
            sp_name = str(rec.get("name") or "").strip().lower()
            for pname, p in projects.items():
                if sp_folder in p["folders"] or (sp_name and sp_name == pname.lower()):
                    space_map[sp_bare] = pname
                    break

    # Map ask terminals from asks/*.json
    asks_dir = os.path.join(tasks_dir, "asks")
    prefix_to_proj = {p["prefix"]: pname for pname, p in projects.items()}
    if os.path.isdir(asks_dir):
        try:
            ask_files = sorted(os.listdir(asks_dir))
        except OSError:
            ask_files = []
        for af in ask_files:
            if not af.endswith(".json"):
                continue
            ad = safe_json_load(os.path.join(asks_dir, af))
            if not isinstance(ad, dict):
                continue
            term = str(ad.get("terminal") or "").strip()
            pname = resolve_ask_project(ad, projects, prefix_to_proj, term_map, space_map)
            if term and pname:
                term_map[term] = (pname, "ask")

    return projects, term_map, space_map


def resolve_ask_project(
    ad: dict[str, Any],
    projects: dict[str, dict[str, Any]],
    prefix_to_proj: dict[str, str],
    term_map: dict[str, tuple[str, str]],
    space_map: dict[str, str],
) -> str:
    tref = str(ad.get("task") or "").strip().upper()
    m = re.match(r"^([A-Z]+)-\d+$", tref)
    if m and m.group(1) in prefix_to_proj:
        return prefix_to_proj[m.group(1)]
    to_addr = str(ad.get("to") or "").strip()
    if to_addr in term_map:
        return term_map[to_addr][0]
    orig = ad.get("origin") if isinstance(ad.get("origin"), dict) else {}
    orig_folder = str(orig.get("folder") or "").strip()
    if orig_folder:
        nf = os.path.normpath(os.path.expanduser(orig_folder))
        for pname, p in projects.items():
            if nf in p["folders"]:
                return pname
    orig_sp = str(orig.get("space") or "").strip().split("/")[-1]
    if orig_sp and orig_sp in space_map:
        return space_map[orig_sp]
    q = str(ad.get("question") or "")
    for pref, pname in prefix_to_proj.items():
        if re.search(rf"\b{re.escape(pref)}-\d+\b", q):
            return pname
    return "cross-project"


def match_project_by_cwd(cwd: str, projects: dict[str, dict[str, Any]]) -> str | None:
    if not cwd:
        return None
    nc = os.path.normpath(os.path.expanduser(cwd))
    for pname, p in projects.items():
        for pf in p["folders"]:
            if nc == pf or nc.startswith(pf + os.sep):
                return pname
        # Worktree paths contain `/worktrees/<slug-or-name>/task-...`
        if f"/{pname}/" in nc or nc.endswith(f"/{pname}") or f"-{pname}-" in nc:
            return pname
    return None


def infer_project_from_text(
    text: str,
    projects: dict[str, dict[str, Any]],
    prefix_to_proj: dict[str, str],
    term_map: dict[str, tuple[str, str]],
    space_map: dict[str, str],
) -> str | None:
    """Infer target factory project from task refs, terminals, spaces, or project names."""
    strong: set[str] = set()
    for pref, pname in prefix_to_proj.items():
        if re.search(rf"\b{re.escape(pref)}-\d+\b", text):
            strong.add(pname)
    for sp_id, pname in space_map.items():
        if len(sp_id) >= 6 and sp_id in text:
            strong.add(pname)
    for m in re.finditer(r"\b(t[0-9a-z]{1,6})\b", text):
        addr = m.group(1)
        if addr in term_map:
            strong.add(term_map[addr][0])
    if len(strong) == 1:
        return next(iter(strong))
    if len(strong) > 1:
        return None

    weak: set[str] = set()
    for pname in projects:
        if re.search(rf"\b{re.escape(pname)}\b", text, re.I):
            weak.add(pname)
    if len(weak) == 1:
        return next(iter(weak))
    return None


# ---- Shell command parser for session tool calls --------------------------------------------

def split_shell_segments(cmd: str) -> list[str]:
    """Split a shell command string on top-level ;, &&, ||, |, and newlines (outside quotes),
    stripping heredoc bodies so embedded Python/text is not mistaken for shell commands."""
    lines = cmd.splitlines()
    kept_lines: list[str] = []
    heredocs: list[str] = []
    i = 0
    while i < len(lines):
        ln = lines[i]
        if heredocs:
            tag = heredocs[0]
            if ln.strip() == tag:
                heredocs.pop(0)
            i += 1
            continue
        for m in re.finditer(r"<<-?\s*(?:'([^']+)'|\"([^\"]+)\"|([\w.-]+))", ln):
            heredocs.append(m.group(1) or m.group(2) or m.group(3))
        kept_lines.append(ln)
        i += 1

    cleaned = "\n".join(kept_lines)
    segs: list[str] = []
    cur: list[str] = []
    quote: str | None = None
    j = 0
    n = len(cleaned)
    while j < n:
        ch = cleaned[j]
        if quote:
            cur.append(ch)
            if ch == "\\" and quote == '"' and j + 1 < n:
                cur.append(cleaned[j + 1])
                j += 2
                continue
            if ch == quote:
                quote = None
            j += 1
            continue
        if ch in ("'", '"'):
            quote = ch
            cur.append(ch)
            j += 1
            continue
        if ch == "\\" and j + 1 < n:
            cur.append(ch)
            cur.append(cleaned[j + 1])
            j += 2
            continue
        if ch == "#" and (not cur or cur[-1].isspace()):
            while j < n and cleaned[j] != "\n":
                j += 1
            continue
        if ch in (";", "\n"):
            segs.append("".join(cur).strip())
            cur = []
            j += 1
            continue
        if ch in ("&", "|"):
            if j + 1 < n and cleaned[j + 1] == ch:
                segs.append("".join(cur).strip())
                cur = []
                j += 2
                continue
            if ch == "|":
                segs.append("".join(cur).strip())
                cur = []
                j += 1
                continue
        cur.append(ch)
        j += 1
    if cur:
        segs.append("".join(cur).strip())
    return [s for s in segs if s]


def tokenize_segment(seg: str) -> list[str]:
    """Lightweight shell word tokenizer that strips leading loop/env prefixes (`do`, `then`, `VAR=val`)."""
    tokens: list[str] = []
    w: list[str] = []
    quote: str | None = None
    i, n = 0, len(seg)
    while i < n:
        ch = seg[i]
        if quote:
            if ch == "\\" and quote == '"' and i + 1 < n:
                w.append(seg[i + 1])
                i += 2
                continue
            if ch == quote:
                quote = None
            else:
                w.append(ch)
            i += 1
            continue
        if ch in ("'", '"'):
            quote = ch
            i += 1
            continue
        if ch == "\\" and i + 1 < n:
            w.append(seg[i + 1])
            i += 2
            continue
        if ch.isspace():
            if w:
                tokens.append("".join(w))
                w = []
            i += 1
            continue
        w.append(ch)
        i += 1
    if w:
        tokens.append("".join(w))

    # Strip leading control keywords and env assignments
    while tokens:
        head = tokens[0].lstrip("(")
        if head in ("do", "then", "else", "elif", "!", "{", "time", "nohup", "env", "command"):
            tokens.pop(0)
        elif re.match(r"^[A-Za-z_]\w*=", head):
            tokens.pop(0)
        else:
            tokens[0] = head
            break
    return tokens


def parse_bash_actions(
    cmd: str,
    var_env: dict[str, str],
) -> list[dict[str, Any]]:
    """Extract factory-relevant command invocations from one Bash tool call."""
    actions: list[dict[str, Any]] = []
    # Track simple VAR=path assignments across segments in this command
    local_vars = dict(var_env)
    for m in re.finditer(r"(?:^|[;\n]\s*)([A-Za-z_]\w*)=([^\s;|&\"']+|\"[^\"]*\"|'[^']*')", cmd):
        local_vars[m.group(1)] = m.group(2).strip("\"'")

    for seg in split_shell_segments(cmd):
        toks = tokenize_segment(seg)
        if not toks:
            continue
        prog_raw = toks[0]
        if prog_raw.startswith("$"):
            vname = prog_raw.lstrip("$").strip("{}")
            prog_raw = local_vars.get(vname, prog_raw)
        prog = os.path.basename(prog_raw)

        # 1. hc send / keys / kill / rm
        if prog == "hc" and len(toks) >= 2:
            verb = toks[1]
            if verb in ("send", "keys", "kill", "rm"):
                # Find first positional arg after verb (skipping flags)
                rest = toks[2:]
                target = None
                j = 0
                while j < len(rest):
                    tk = rest[j]
                    if tk in ("--if", "--timeout", "--on", "--session"):
                        j += 2
                        continue
                    if tk.startswith("-"):
                        j += 1
                        continue
                    target = tk
                    break
                if target and target.startswith("$"):
                    target = local_vars.get(target.lstrip("$").strip("{}"), target)
                actions.append({"kind": f"hc_{verb}", "verb": verb, "target": target, "raw": cmd})

        # 2. herdr-task (or python -m herdr_task / $HT)
        elif prog == "herdr-task" or prog_raw in ("$HT", "${HT}"):
            rest = toks[1:]
            verb = None
            folder_arg = None
            j = 0
            while j < len(rest):
                tk = rest[j]
                if tk in ("--folder", "--on") and j + 1 < len(rest):
                    if tk == "--folder":
                        folder_arg = rest[j + 1]
                    j += 2
                    continue
                if tk.startswith("-"):
                    if tk in ("--help", "-h"):
                        verb = tk
                        break
                    j += 1
                    continue
                verb = tk
                break
            if verb:
                actions.append({
                    "kind": "herdr_task",
                    "verb": verb,
                    "folder": folder_arg,
                    "raw": cmd,
                })

        # 2b. Helper wrapper script invoking accept on a task ref (e.g. `bash verify.sh FAC-44 ... accept`)
        elif "accept" in toks[1:] and any(re.fullmatch(r"[A-Z]{2,8}-\d+", t) for t in toks[1:]) and prog not in ("echo", "printf", "grep", "rg", "git"):
            actions.append({
                "kind": "herdr_task",
                "verb": "accept",
                "folder": None,
                "raw": cmd,
            })

        # 3. herdr-app do <action>
        elif prog == "herdr-app" and len(toks) >= 3 and toks[1] == "do":
            actions.append({
                "kind": "herdr_app_do",
                "verb": toks[2],
                "raw": cmd,
            })

        # 4. kill / pkill / killall
        elif prog in ("kill", "pkill", "killall"):
            if re.search(r"\b(agy|antigravity|claude|codex|herdr|hc)\b|kill\s+-9\s+\d+", seg):
                actions.append({"kind": "kill_process", "verb": prog, "raw": cmd})

        # 5. Direct python/jq/shell writes to shepherdr/tasks store
        if ("shepherdr/tasks" in seg or ".herdr/tasks" in seg) and prog not in ("herdr-task", "ls", "cat", "head", "tail", "wc", "stat", "grep", "rg"):
            if re.search(r"json\.dump|write_text|\.write\(|os\.replace|mv\s+|cp\s+|rm\s+|(?<!2)>\s*\S", seg):
                actions.append({"kind": "store_edit", "verb": "shell_write", "raw": cmd})

        # Aux signals used for touchpoint classification on assistant responses
        if prog == "git" and len(toks) >= 2 and toks[1] == "tag" and not any(x in ("-l", "--list", "-d") for x in toks[2:]):
            actions.append({"kind": "aux_release", "verb": "git_tag", "raw": cmd})
        elif prog == "gh" and len(toks) >= 3 and toks[1] == "workflow" and toks[2] == "run":
            actions.append({"kind": "aux_release", "verb": "gh_workflow_run", "raw": cmd})

    var_env.update(local_vars)
    return actions


# ---- Session log reader & turn classifier ---------------------------------------------------

def clean_owner_message(
    entry: dict[str, Any],
    is_first: bool,
    role: str,
    session_kind: str,
) -> tuple[str | None, list[str]]:
    """Return (cleaned_owner_text_or_None, setting_changed_keys).

    Never emitted anywhere: `cleaned_owner_text_or_None` stays in local memory solely
    to classify the turn into one of the 6 touchpoint buckets, then is discarded.
    """
    setting_keys: list[str] = []

    if session_kind == "claude":
        if entry.get("isSidechain") or entry.get("isMeta") or entry.get("isCompactSummary"):
            return None, setting_keys
        if entry.get("turnOrigin") == "scheduled" or entry.get("promptSource") == "system":
            return None, setting_keys
        orig = entry.get("origin")
        if isinstance(orig, dict) and orig.get("kind") == "task-notification":
            return None, setting_keys
        if entry.get("type") != "user":
            return None, setting_keys
        content = (entry.get("message") or {}).get("content")
        if isinstance(content, list):
            if any(isinstance(x, dict) and x.get("type") == "tool_result" for x in content):
                return None, setting_keys
            has_img = any(isinstance(x, dict) and x.get("type") == "image" for x in content)
            raw = "\n".join(str(x.get("text") or "") for x in content if isinstance(x, dict) and x.get("type") == "text")
        elif isinstance(content, str):
            has_img = False
            raw = content
        else:
            return None, setting_keys
    else:  # codex
        tp = entry.get("type")
        payload = entry.get("payload") if isinstance(entry.get("payload"), dict) else {}
        if tp == "response_item" and payload.get("role") == "user":
            c = payload.get("content")
            raw = "\n".join(str(x.get("text") or "") for x in c if isinstance(x, dict)) if isinstance(c, list) else str(c or "")
            if CODEX_INJECTED_RE.match(raw):
                return None, setting_keys
            has_img = False
        else:
            return None, setting_keys

    s = raw.strip()
    if not s:
        return ("[image]" if has_img else None), setting_keys

    # Extract <herdr-setting name="<key>"> change notices (name != "all") before stripping tags
    if role == "assistant" and "<herdr-setting" in s:
        stripped_for_tags = re.sub(r"</?pasted_content\b[^>]*>\n?", "", s).strip()
        if stripped_for_tags.startswith("<herdr-setting"):
            for m in re.finditer(r'<herdr-setting\s+name="([^"]+)"', stripped_for_tags):
                k = m.group(1)
                if k and k != "all":
                    setting_keys.append(k)

    if is_first:
        if role in ("worker", "reviewer"):
            return None, setting_keys
        if role == "assistant":
            if "wait for the person." in s:
                s = s.split("wait for the person.", 1)[1].strip()
            else:
                return None, setting_keys

    if not s:
        return None, setting_keys
    if CLI_INJECTED_RE.match(s) or CODEX_INJECTED_RE.match(s):
        return None, setting_keys

    s = re.sub(r"<system-reminder>[\s\S]*?</system-reminder>", "", s)
    s = re.sub(r"</?pasted_content\b[^>]*>\n?", "", s)
    s = re.sub(r"\[Image #\d+\]\s*", "", s).strip()
    if not s:
        return ("[image]" if has_img else None), setting_keys

    if re.match(r"^\s*<herdr-(autopilot|assistant)\b", s):
        return None, setting_keys

    # Strip task mention badges (<hc task="...">) and herdr-* system tags
    s = re.sub(r'<hc\s+task="[^"]*"[^>]*>[\s\S]*?</hc>\n?', "", s)
    s = re.sub(r"<(herdr-[\w-]+)\b[^>]*>[\s\S]*?</\1\s*>\n?", "", s)
    s = re.sub(r"<herdr-[\w-]+\b[^>]*/>\n?", "", s).strip()

    # Strip reaction lines (<hc reaction="..." .../>)
    lines = [ln for ln in s.splitlines() if not re.match(r'^\s*<hc\b[^>]*\breaction(?:-removed)?="', ln)]
    s = "\n".join(lines).strip()
    if not s:
        return None, setting_keys

    # Whole-message <hc ...>...</hc> is an inter-terminal send unless from="artifact"
    m_hc = re.match(r"^<hc\b([^>]*)>([\s\S]*?)</hc>$", s)
    if m_hc:
        m_from = re.search(r'\bfrom="([^"]*)"', m_hc.group(1))
        if m_from and m_from.group(1) == "artifact":
            return (m_hc.group(2).strip() or "[artifact]"), setting_keys
        return None, setting_keys

    # Any single XML tag wrapping the entire message is program-generated
    if re.match(r"^<([a-z][\w-]*)\b[^>]*>[\s\S]*</\1>$", s):
        return None, setting_keys

    # Strip <hc reply-to="..."/> header on chat replies
    s = re.sub(r'^\s*<hc\b[^>]*\breply-to="[^"]*"\s*/>\s*', "", s).strip()
    if not s or SYSTEM_PROSE_RE.match(s):
        return None, setting_keys

    return s, setting_keys


def classify_message_touchpoint(
    msg_text: str,
    prev_assistant_asked: bool,
    prev_assistant_release_ask: bool,
    turn_actions: list[dict[str, Any]],
    turn_edited_rule_file: bool,
    turn_wrote_any_file: bool,
    is_opening_assistant_msg: bool,
) -> str:
    """Classify one owner message into one of the five design touchpoints or 'other'."""
    ht_verbs = {a["verb"] for a in turn_actions if a["kind"] == "herdr_task"}
    has_aux_release = any(a["kind"] == "aux_release" for a in turn_actions)
    has_mutating_action = bool(
        turn_wrote_any_file
        or has_aux_release
        or any(a["kind"] in ("hc_send", "hc_keys", "hc_kill", "herdr_app_do", "kill_process", "store_edit") for a in turn_actions)
        or (ht_verbs - HT_READONLY_VERBS)
    )

    # 1. Release approval
    if has_aux_release or RELEASE_RE.search(msg_text):
        if prev_assistant_release_ask or SHORT_ANSWER_RE.match(msg_text) or has_aux_release or re.search(r"\b(tag|ship|release|approve|yes|go)\b", msg_text, re.I):
            return "release_approval"
    if prev_assistant_release_ask and SHORT_ANSWER_RE.match(msg_text) and len(msg_text) <= 240:
        return "release_approval"

    # 2. Ask answer (answering a preceding question or explicit approval/choice)
    if prev_assistant_asked and (SHORT_ANSWER_RE.match(msg_text) or len(msg_text) <= 200):
        return "ask_answer"
    if SHORT_ANSWER_RE.match(msg_text) and len(msg_text) <= 120 and ("accept" in ht_verbs or "merge" in ht_verbs):
        return "ask_answer"

    # 3. Standing rule / policy / setting
    if ("config" in ht_verbs or "autopilot" in ht_verbs or turn_edited_rule_file) and not ("add" in ht_verbs):
        return "rule"
    if RULE_RE.search(msg_text) and "add" not in ht_verbs:
        return "rule"

    # 4. Digest / status check (read-only question/inquiry)
    if not has_mutating_action and (DIGEST_RE.search(msg_text) or msg_text.rstrip().endswith("?")):
        return "digest"

    # 5. Goal / feature / milestone direction
    if is_opening_assistant_msg or "add" in ht_verbs or "project" in ht_verbs or GOAL_RE.search(msg_text):
        return "goal"

    # 6. Everything else (troubleshooting, nudging stuck tasks, babysitting instructions)
    return "other"


def detect_initial_role(first_user_raw: str, cwd: str, is_worktree_dir: bool) -> str:
    head = first_user_raw[:1500]
    if "You work on one Herdr task." in head[:500]:
        return "worker"
    if "You review one Herdr task" in head[:500] or "reviewer.md" in head:
        return "reviewer"
    if (
        "You are the coordinator of one space in Herdr" in head[:700]
        or "herdr-assistant" in head[:700]
        or "You coordinate project" in head[:500]
    ):
        return "assistant"
    if cwd and ("herdr-review-" in cwd or cwd.endswith("-review")):
        return "reviewer"
    if is_worktree_dir:
        return "worker"
    return "candidate_outside"


def scan_session_file(
    fp: str,
    session_kind: str,
    is_worktree_dir: bool,
    projects: dict[str, dict[str, Any]],
    prefix_to_proj: dict[str, str],
    term_map: dict[str, tuple[str, str]],
    space_map: dict[str, str],
) -> dict[str, Any] | None:
    """Parse one Claude or Codex JSONL session into privacy-safe structural records."""
    role: str | None = None
    cwd: str = ""
    first_user_seen = False
    saw_herdr_coord_tags = False
    saw_reviewer_verdict = False
    saw_factory_target = False

    autopilot_wakes: list[tuple[int, str]] = []          # (ms, space_or_empty)
    setting_notices: list[tuple[int, int]] = []          # (ms, count_of_keys)
    project_hints: Counter[str] = Counter()

    # Turn-level tracking (no message words retained after scan_session_file returns!)
    turns: list[dict[str, Any]] = []
    cur_turn: dict[str, Any] | None = None
    prev_assistant_asked = False
    prev_assistant_release_ask = False

    pending_tools: dict[str, dict[str, Any]] = {}
    all_tool_actions: list[dict[str, Any]] = []
    var_env: dict[str, str] = {}

    try:
        fh = open(fp, encoding="utf-8", errors="replace")
    except OSError:
        return None

    with fh:
        for raw_line in fh:
            raw_line = raw_line.strip()
            if not raw_line:
                continue
            try:
                o = json.loads(raw_line)
            except ValueError:
                continue
            if not isinstance(o, dict):
                continue

            if session_kind == "claude":
                if not cwd and o.get("cwd"):
                    cwd = str(o["cwd"])
                if o.get("isSidechain") or o.get("isMeta"):
                    continue
                ms = parse_iso_ms(o.get("timestamp"))
                tp = o.get("type")

                if tp == "user":
                    content = (o.get("message") or {}).get("content")
                    # Close any pending tool_use windows on tool_result
                    if isinstance(content, list):
                        for b in content:
                            if isinstance(b, dict) and b.get("type") == "tool_result":
                                tid = str(b.get("tool_use_id") or "")
                                if tid in pending_tools and ms:
                                    pending_tools[tid]["end_ms"] = max(ms, pending_tools[tid]["start_ms"])
                        if any(isinstance(x, dict) and x.get("type") == "tool_result" for x in content):
                            continue

                    raw_txt = (
                        content if isinstance(content, str)
                        else "\n".join(str(x.get("text") or "") for x in (content or []) if isinstance(x, dict) and x.get("type") == "text")
                    )
                    if not raw_txt.strip():
                        continue

                    is_first = not first_user_seen
                    first_user_seen = True
                    if role is None:
                        role = detect_initial_role(raw_txt, cwd, is_worktree_dir)

                    # Detect coordinator tags & autopilot wakes (only top-level tags sent by herdr-task)
                    stripped_paste = re.sub(r"</?pasted_content\b[^>]*>\n?", "", raw_txt).strip()
                    m_ap = re.match(r'^<herdr-autopilot\s+reason="[^"]*"\s+space="([^"]*)"', stripped_paste)
                    if m_ap and not is_first:
                        saw_herdr_coord_tags = True
                        if ms:
                            autopilot_wakes.append((ms, m_ap.group(1)))
                    if re.match(r'^<herdr-news\s+task="[A-Z]+-\d+"\s+event="', stripped_paste) and not is_first:
                        for pref, pname in prefix_to_proj.items():
                            hits = len(re.findall(rf"\b{re.escape(pref)}-\d+\b", stripped_paste))
                            if hits:
                                project_hints[pname] += hits
                    if re.match(r'^<herdr-setting\s+name="', stripped_paste) and not is_first:
                        saw_herdr_coord_tags = True
                    m_hc_from = re.match(r'^<hc\b[^>]*\bfrom="([^"]*)"', stripped_paste)
                    if m_hc_from:
                        sender = m_hc_from.group(1).split()[0]
                        if sender in term_map:
                            saw_factory_target = True

                    msg_text, set_keys = clean_owner_message(o, is_first, role, "claude")
                    if set_keys and ms:
                        setting_notices.append((ms, len(set_keys)))

                    if msg_text is not None and ms:
                        inferred_proj = infer_project_from_text(msg_text, projects, prefix_to_proj, term_map, space_map)
                        cur_turn = {
                            "ms": ms,
                            "msg_text": msg_text,
                            "is_opening": is_first and role == "assistant",
                            "prev_asked": prev_assistant_asked,
                            "prev_release_ask": prev_assistant_release_ask,
                            "inferred_proj": inferred_proj,
                            "actions": [],
                            "edited_rule_file": False,
                            "wrote_any_file": False,
                            "acted_intervention": False,
                        }
                        turns.append(cur_turn)
                    else:
                        cur_turn = {
                            "ms": ms or 0,
                            "msg_text": None,
                            "is_opening": False,
                            "prev_asked": False,
                            "prev_release_ask": False,
                            "inferred_proj": None,
                            "actions": [],
                            "edited_rule_file": False,
                            "wrote_any_file": False,
                            "acted_intervention": False,
                        }
                        turns.append(cur_turn)
                    prev_assistant_asked = False
                    prev_assistant_release_ask = False

                elif tp == "assistant":
                    blocks = (o.get("message") or {}).get("content") or []
                    prose_parts: list[str] = []
                    for b in blocks:
                        if not isinstance(b, dict):
                            continue
                        btype = b.get("type")
                        if btype == "text" and b.get("text"):
                            prose_parts.append(str(b["text"]))
                        elif btype == "tool_use":
                            tname = str(b.get("name") or "")
                            inp = b.get("input") if isinstance(b.get("input"), dict) else {}
                            if tname == "AskUserQuestion":
                                prev_assistant_asked = True
                                q_str = json.dumps(inp)
                                if RELEASE_RE.search(q_str):
                                    prev_assistant_release_ask = True
                            elif tname in ("Edit", "Write", "MultiEdit"):
                                fpath = str(inp.get("file_path") or inp.get("notebook_path") or "")
                                if cur_turn is not None:
                                    cur_turn["wrote_any_file"] = True
                                    if re.search(r"(?:AGENTS|CLAUDE)\.md$|settings\.json$|autopilot|heartbeat", fpath):
                                        cur_turn["edited_rule_file"] = True
                                if ("shepherdr/tasks" in fpath or ".herdr/tasks" in fpath) and ms:
                                    act = {
                                        "kind": "store_edit",
                                        "verb": tname.lower(),
                                        "start_ms": ms,
                                        "end_ms": ms,
                                        "proj": infer_project_from_text(fpath, projects, prefix_to_proj, term_map, space_map),
                                        "refs": [],
                                        "turn_idx": len(turns) - 1,
                                    }
                                    all_tool_actions.append(act)
                                    saw_factory_target = True
                                    if cur_turn is not None:
                                        cur_turn["actions"].append(act)
                            elif tname == "Bash" and ms:
                                cmd = str(inp.get("command") or "")
                                tid = str(b.get("id") or "")
                                parsed = parse_bash_actions(cmd, var_env)
                                for pa in parsed:
                                    if pa["kind"] == "herdr_task" and pa["verb"] == "review" and ("--pass" in cmd or "--changes" in cmd):
                                        saw_reviewer_verdict = True
                                    proj = None
                                    if pa.get("target") and pa["target"] in term_map:
                                        proj = term_map[pa["target"]][0]
                                        saw_factory_target = True
                                    if pa.get("folder"):
                                        proj = proj or match_project_by_cwd(pa["folder"], projects)
                                    proj = proj or infer_project_from_text(cmd, projects, prefix_to_proj, term_map, space_map)
                                    if pa["kind"] in ("herdr_task", "store_edit"):
                                        saw_factory_target = True
                                    refs = re.findall(r"\b(?:[A-Z]{2,8}-\d+|\d+)\b", cmd)
                                    rec = {
                                        "kind": pa["kind"],
                                        "verb": pa["verb"],
                                        "target": pa.get("target"),
                                        "start_ms": ms,
                                        "end_ms": ms + (300_000 if inp.get("run_in_background") else 30_000),
                                        "proj": proj,
                                        "refs": refs,
                                        "turn_idx": len(turns) - 1,
                                    }
                                    all_tool_actions.append(rec)
                                    if tid:
                                        pending_tools[tid] = rec
                                    if cur_turn is not None:
                                        cur_turn["actions"].append(rec)

                    if prose_parts:
                        joined = re.sub(r"<hc\b[^>]*/>", "", "\n".join(prose_parts)).strip()
                        if joined.endswith("?") or re.search(r"\?\s*$", joined):
                            prev_assistant_asked = True
                            if RELEASE_RE.search(joined[-400:]):
                                prev_assistant_release_ask = True

            else:  # codex JSONL
                ms = parse_iso_ms(o.get("timestamp"))
                tp = o.get("type")
                payload = o.get("payload") if isinstance(o.get("payload"), dict) else {}
                if tp in ("session_meta", "turn_context") and not cwd and payload.get("cwd"):
                    cwd = str(payload["cwd"])
                elif tp == "response_item" and payload.get("role") == "user":
                    c = payload.get("content")
                    raw_txt = "\n".join(str(x.get("text") or "") for x in c if isinstance(x, dict)) if isinstance(c, list) else str(c or "")
                    if CODEX_INJECTED_RE.match(raw_txt) or not raw_txt.strip():
                        continue
                    is_first = not first_user_seen
                    first_user_seen = True
                    if role is None:
                        role = detect_initial_role(raw_txt, cwd, is_worktree_dir)
                    msg_text, _ = clean_owner_message(o, is_first, role, "codex")
                    cur_turn = {
                        "ms": ms or 0,
                        "msg_text": msg_text if ms else None,
                        "is_opening": is_first and role == "assistant",
                        "prev_asked": prev_assistant_asked,
                        "prev_release_ask": prev_assistant_release_ask,
                        "inferred_proj": infer_project_from_text(msg_text, projects, prefix_to_proj, term_map, space_map) if msg_text else None,
                        "actions": [],
                        "edited_rule_file": False,
                        "wrote_any_file": False,
                        "acted_intervention": False,
                    }
                    turns.append(cur_turn)
                    prev_assistant_asked = False
                    prev_assistant_release_ask = False
                elif tp == "response_item" and payload.get("role") == "assistant":
                    c = payload.get("content") or []
                    txt = "\n".join(str(x.get("text") or "") for x in c if isinstance(x, dict) and x.get("type") in ("output_text", "text"))
                    if txt.strip().endswith("?"):
                        prev_assistant_asked = True
                        if RELEASE_RE.search(txt[-400:]):
                            prev_assistant_release_ask = True
                elif tp == "response_item" and payload.get("type") in ("function_call", "custom_tool_call") and ms:
                    tname = str(payload.get("name") or "")
                    raw_args = payload.get("arguments") or payload.get("input") or ""
                    cmd = ""
                    if isinstance(raw_args, str):
                        try:
                            ad = json.loads(raw_args)
                            if isinstance(ad, dict):
                                c_val = ad.get("cmd") or ad.get("command") or ad.get("input") or ""
                                cmd = " ".join(str(x) for x in c_val) if isinstance(c_val, list) else str(c_val)
                            else:
                                cmd = raw_args
                        except ValueError:
                            cmd = raw_args
                    if tname == "apply_patch" and cur_turn is not None:
                        cur_turn["wrote_any_file"] = True
                    elif cmd:
                        for pa in parse_bash_actions(cmd, var_env):
                            if pa["kind"] == "herdr_task" and pa["verb"] == "review" and ("--pass" in cmd or "--changes" in cmd):
                                saw_reviewer_verdict = True
                            proj = None
                            if pa.get("target") and pa["target"] in term_map:
                                proj = term_map[pa["target"]][0]
                                saw_factory_target = True
                            proj = proj or infer_project_from_text(cmd, projects, prefix_to_proj, term_map, space_map)
                            if pa["kind"] in ("herdr_task", "store_edit"):
                                saw_factory_target = True
                            rec = {
                                "kind": pa["kind"],
                                "verb": pa["verb"],
                                "target": pa.get("target"),
                                "start_ms": ms,
                                "end_ms": ms + 30_000,
                                "proj": proj,
                                "refs": re.findall(r"\b(?:[A-Z]{2,8}-\d+|\d+)\b", cmd),
                                "turn_idx": len(turns) - 1,
                            }
                            all_tool_actions.append(rec)
                            if cur_turn is not None:
                                cur_turn["actions"].append(rec)

    # Finalize session role from structural evidence
    has_non_reviewer_actions = any(
        a["kind"] in ("hc_send", "hc_keys", "hc_kill", "herdr_app_do", "store_edit")
        or (a["kind"] == "herdr_task" and a["verb"] not in ("review", "show", "list", "where", "--help", "-h"))
        for a in all_tool_actions
    )
    if saw_reviewer_verdict and role not in ("assistant", "worker") and not has_non_reviewer_actions and len(turns) <= 4:
        role = "reviewer"
    elif role == "candidate_outside" and saw_herdr_coord_tags and not has_non_reviewer_actions:
        role = "assistant"
    elif role == "candidate_outside":
        role = "outside_orchestrator" if saw_factory_target else "unrelated"
    elif role is None:
        role = "unrelated"

    if role == "unrelated":
        return None

    # Determine session's primary project (for assistant / worker / reviewer)
    session_proj = match_project_by_cwd(cwd, projects)
    if not session_proj and autopilot_wakes:
        for _, sp in autopilot_wakes:
            sp_b = sp.split("/")[-1]
            if sp_b in space_map:
                session_proj = space_map[sp_b]
                break
    if not session_proj and project_hints:
        session_proj = project_hints.most_common(1)[0][0]

    # Classify owner turns into touchpoints now and discard msg_text immediately!
    owner_turns: list[dict[str, Any]] = []
    for t_idx, tr in enumerate(turns):
        msg = tr.get("msg_text")
        if msg is None or not tr.get("ms"):
            continue
        tp_name = classify_message_touchpoint(
            msg_text=msg,
            prev_assistant_asked=tr["prev_asked"],
            prev_assistant_release_ask=tr["prev_release_ask"],
            turn_actions=tr["actions"],
            turn_edited_rule_file=tr["edited_rule_file"],
            turn_wrote_any_file=tr["wrote_any_file"],
            is_opening_assistant_msg=tr["is_opening"],
        )
        # Determine project for this turn
        t_proj = session_proj if role in ("assistant", "worker") else None
        if not t_proj:
            t_proj = tr["inferred_proj"]
        if not t_proj:
            act_projs = {a["proj"] for a in tr["actions"] if a.get("proj")}
            if len(act_projs) == 1:
                t_proj = next(iter(act_projs))
        owner_turns.append({
            "ms": tr["ms"],
            "touchpoint": tp_name,
            "project": t_proj or "cross-project",
            "turn_idx": t_idx,
        })
        tr["msg_text"] = None  # Explicitly discard raw text

    return {
        "role": role,
        "project": session_proj or "cross-project",
        "owner_turns": owner_turns,
        "tool_actions": all_tool_actions,
        "autopilot_wakes": autopilot_wakes,
        "setting_notices": setting_notices,
    }


def read_usage_events(usage_dirs: list[str]) -> list[dict[str, Any]]:
    """Read deduplicated usage.1.jsonl + usage.jsonl events across configured service dirs."""
    seen_files: set[str] = set()
    events: list[dict[str, Any]] = []
    for d in usage_dirs:
        if not d:
            continue
        exp = os.path.expanduser(d)
        if not os.path.isdir(exp):
            continue
        for name in ("usage.1.jsonl", "usage.jsonl"):
            fp = os.path.join(exp, name)
            if not os.path.isfile(fp):
                continue
            try:
                rp = os.path.realpath(fp)
            except OSError:
                rp = fp
            if rp in seen_files:
                continue
            seen_files.add(rp)
            try:
                with open(fp, encoding="utf-8", errors="replace") as f:
                    for ln in f:
                        ln = ln.strip()
                        if not ln:
                            continue
                        try:
                            ev = json.loads(ln)
                        except ValueError:
                            continue
                        if isinstance(ev, dict) and isinstance(ev.get("at"), (int, float)):
                            events.append(ev)
            except OSError:
                continue
    return events


def measure(
    since: str,
    until: str,
    usage_dirs: list[str],
    tasks_dir: str,
    claude_dir: str,
    codex_dir: str,
) -> dict[str, Any]:
    """Run the full read-only baseline measurement and return aggregate counts."""
    days = date_range_days(since, until)
    day_set = set(days)

    projects, term_map, space_map = load_projects(tasks_dir)
    prefix_to_proj = {p["prefix"]: pname for pname, p in projects.items()}
    project_names = sorted(projects.keys()) + ["cross-project"]

    # Cutoff mtime for fast skipping of ancient session files
    try:
        since_epoch = datetime.datetime.fromisoformat(since).timestamp() - 86400
    except ValueError:
        since_epoch = 0.0

    sessions: list[dict[str, Any]] = []

    # 1. Scan Claude Code sessions
    exp_claude = os.path.expanduser(claude_dir) if claude_dir else ""
    if exp_claude and os.path.isdir(exp_claude):
        try:
            proj_dirs = sorted(os.listdir(exp_claude))
        except OSError:
            proj_dirs = []
        for d in proj_dirs:
            if any(skip in d for skip in ("private-tmp", "scratch-workspaces", "ClaudeProbe")):
                continue
            dp = os.path.join(exp_claude, d)
            if not os.path.isdir(dp):
                continue
            is_wt = "worktrees-" in d
            for fp in sorted(glob.glob(os.path.join(dp, "*.jsonl"))):
                try:
                    if os.path.getmtime(fp) < since_epoch:
                        continue
                except OSError:
                    continue
                parsed = scan_session_file(fp, "claude", is_wt, projects, prefix_to_proj, term_map, space_map)
                if parsed:
                    sessions.append(parsed)

    # 2. Scan Codex sessions
    exp_codex = os.path.expanduser(codex_dir) if codex_dir else ""
    if exp_codex and os.path.isdir(exp_codex):
        for fp in sorted(glob.glob(os.path.join(exp_codex, "*", "*", "*", "rollout-*.jsonl"))):
            try:
                if os.path.getmtime(fp) < since_epoch:
                    continue
            except OSError:
                continue
            parsed = scan_session_file(fp, "codex", False, projects, prefix_to_proj, term_map, space_map)
            if parsed:
                sessions.append(parsed)

    # Collect agent action windows for deduplicating sends, ask answers, and CLI accepts
    outside_send_windows: list[tuple[int, int, str | None]] = []
    all_agent_send_windows: list[tuple[int, int, str | None]] = []
    agent_keys_windows: list[tuple[int, int, str | None]] = []
    cli_accept_windows: list[tuple[int, int, str, list[str]]] = []

    # Initialize daily & per-project aggregators
    touches_by_day_tp: dict[str, Counter[str]] = {d: Counter() for d in days}
    touches_by_day_ch: dict[str, Counter[str]] = {d: Counter() for d in days}
    touches_by_day_proj: dict[str, Counter[str]] = {d: Counter() for d in days}
    touches_by_proj_tp: dict[str, Counter[str]] = {p: Counter() for p in project_names}

    interv_by_day_type: dict[str, Counter[str]] = {d: Counter() for d in days}
    interv_by_day_proj: dict[str, Counter[str]] = {d: Counter() for d in days}
    interv_by_proj_type: dict[str, Counter[str]] = {p: Counter() for p in project_names}
    interv_episodes_by_day: Counter[str] = Counter()
    autopilot_wakes_by_day: Counter[str] = Counter()

    def record_touch(day: str, proj: str, tp: str, ch: str) -> None:
        if day not in day_set:
            return
        p = proj if proj in touches_by_proj_tp else "cross-project"
        touches_by_day_tp[day][tp] += 1
        touches_by_day_ch[day][ch] += 1
        touches_by_day_proj[day][p] += 1
        touches_by_proj_tp[p][tp] += 1

    def record_intervention(day: str, proj: str, itype: str) -> None:
        if day not in day_set:
            return
        p = proj if proj in interv_by_proj_type else "cross-project"
        interv_by_day_type[day][itype] += 1
        interv_by_day_proj[day][p] += 1
        interv_by_proj_type[p][itype] += 1

    # Pass 1 over sessions: collect deduplication windows + count outside interventions + autopilot wakes
    seen_ap_wakes: set[tuple[int, str]] = set()
    for sess in sessions:
        srole = sess["role"]
        for ms, sp in sess["autopilot_wakes"]:
            key = (ms // 1000, sp)
            if key not in seen_ap_wakes:
                seen_ap_wakes.add(key)
                d_str = local_date_str(ms)
                if d_str in day_set:
                    autopilot_wakes_by_day[d_str] += 1

        acted_turns_per_day: dict[str, set[int]] = defaultdict(set)

        for act in sess["tool_actions"]:
            kind = act["kind"]
            verb = act["verb"]
            s_ms, e_ms = act["start_ms"], act["end_ms"]

            if kind == "hc_send" or (kind == "herdr_app_do" and verb == "chat.send"):
                # Delivery happens at invocation start (`hc send` is often chained with `&& hc wait`)
                all_agent_send_windows.append((s_ms - 2000, s_ms + 10000, act.get("proj")))
                if srole == "outside_orchestrator":
                    outside_send_windows.append((s_ms - 2000, s_ms + 10000, act.get("proj")))
            elif kind == "hc_keys":
                agent_keys_windows.append((s_ms - 2000, s_ms + 10000, act.get("target")))
            elif kind == "herdr_task" and verb == "accept" and srole in ("outside_orchestrator", "assistant"):
                cli_accept_windows.append((s_ms - 5000, max(e_ms, s_ms + 300_000), srole, act["refs"]))

            # Outside interventions: actions by outside_orchestrator on factory tasks/agents/stores
            if srole != "outside_orchestrator":
                continue

            d_str = local_date_str(s_ms)
            if d_str not in day_set:
                continue

            itype: str | None = None
            if kind == "hc_send":
                itype = "hc_send"
            elif kind == "hc_keys":
                itype = "hc_keys"
            elif kind in ("hc_kill", "hc_rm"):
                itype = "hc_kill"
            elif kind == "herdr_task":
                if verb in HT_READONLY_VERBS or verb == "review":
                    itype = None
                elif verb == "accept":
                    itype = "herdr_task_accept"
                elif verb == "edit":
                    itype = "herdr_task_edit"
                elif verb in HT_LIFECYCLE_VERBS:
                    itype = "herdr_task_lifecycle"
                elif verb in HT_ADMIN_VERBS:
                    itype = "herdr_task_admin"
            elif kind == "herdr_app_do":
                itype = "herdr_app_do"
            elif kind == "kill_process":
                itype = "kill_process"
            elif kind == "store_edit":
                itype = "store_edit"

            if itype:
                proj = act.get("proj") or "cross-project"
                record_intervention(d_str, proj, itype)
                if act.get("turn_idx", -1) >= 0:
                    acted_turns_per_day[d_str].add(act["turn_idx"])

        for d_str, tset in acted_turns_per_day.items():
            interv_episodes_by_day[d_str] += len(tset)

    # Also fold in any autopilot wakes from .autopilot.json not already seen in session logs
    if tasks_dir and os.path.isdir(tasks_dir):
        ap_data = safe_json_load(os.path.join(tasks_dir, ".autopilot.json"))
        if isinstance(ap_data, dict) and isinstance(ap_data.get("spaces"), dict):
            for sp_id, rec in ap_data["spaces"].items():
                if not isinstance(rec, dict):
                    continue
                for w in rec.get("wakes") or []:
                    if isinstance(w, dict) and isinstance(w.get("at"), (int, float)):
                        ms = int(w["at"])
                        d_str = local_date_str(ms)
                        if d_str in day_set and not any(abs(ms // 1000 - s_sec) <= 5 and sp == sp_id for s_sec, sp in seen_ap_wakes):
                            seen_ap_wakes.add((ms // 1000, sp_id))
                            autopilot_wakes_by_day[d_str] += 1

    # Read usage.jsonl events
    usage_events = read_usage_events(usage_dirs)
    usage_sends: list[tuple[int, str]] = []
    usage_setting_times: list[int] = []
    surface_counts: Counter[str] = Counter()

    for ev in usage_events:
        at = int(ev["at"])
        d_str = local_date_str(at)
        kind = ev.get("kind")
        surf = str(ev.get("from") or "unknown")
        if kind == "send":
            usage_sends.append((at, surf))
            if d_str in day_set:
                surface_counts[surf] += 1

    # Pass 2 over sessions: count owner message touches (deduplicated against outside/agent sends)
    for sess in sessions:
        srole = sess["role"]
        if srole == "reviewer":
            continue
        for ot in sess["owner_turns"]:
            ms = ot["ms"]
            d_str = local_date_str(ms)
            if d_str not in day_set:
                continue
            if srole == "assistant":
                if any(lo <= ms <= hi and (p is None or p == ot["project"]) for lo, hi, p in outside_send_windows):
                    continue
                ch = "assistant_chat"
            elif srole == "worker":
                if any(lo <= ms <= hi and (p is None or p == ot["project"]) for lo, hi, p in all_agent_send_windows):
                    continue
                ch = "worker_chat"
            elif srole == "outside_orchestrator":
                ch = "outside_orchestrator"
            else:
                continue
            record_touch(d_str, ot["project"], ot["touchpoint"], ch)

    # Task store UI Accepts (excluding engine auto-accepts and CLI accepts by outside/assistant)
    store_accept_times: list[tuple[int, str]] = []
    for pname, p in projects.items():
        prefix = p["prefix"]
        for t in p["tasks"]:
            acc_at = t.get("accepted_at")
            if not isinstance(acc_at, (int, float)):
                continue
            acc_ms = int(acc_at)
            d_str = local_date_str(acc_ms)
            if d_str not in day_set:
                continue
            ab = t.get("accepted_by")
            if ab in ("auto", "engine", "assistant") or t.get("auto_accepted"):
                continue
            ref = f"{prefix}-{t.get('id')}"
            tid_str = str(t.get("id"))
            if any(
                lo <= acc_ms <= hi and (ref in refs or tid_str in refs)
                for lo, hi, _, refs in cli_accept_windows
            ):
                continue
            title_prompt = f"{t.get('title') or ''} {(t.get('prompt') or '')[:300]}"
            tp = "release_approval" if RELEASE_RE.search(title_prompt) else "other"
            record_touch(d_str, pname, tp, "ui_accept")
            store_accept_times.append((acc_ms, ref))

    # Ask records answered by the owner (excluding those answered by agent hc keys)
    ask_answer_times: list[int] = []
    asks_dir = os.path.join(tasks_dir, "asks") if tasks_dir else ""
    if asks_dir and os.path.isdir(asks_dir):
        try:
            ask_files = sorted(os.listdir(asks_dir))
        except OSError:
            ask_files = []
        for af in ask_files:
            if not af.endswith(".json"):
                continue
            ad = safe_json_load(os.path.join(asks_dir, af))
            if not isinstance(ad, dict) or not isinstance(ad.get("answered_at"), (int, float)):
                continue
            ans_ms = int(ad["answered_at"])
            d_str = local_date_str(ans_ms)
            if d_str not in day_set:
                continue
            ask_term = str(ad.get("terminal") or "").strip()
            if any(lo <= ans_ms <= hi and (not tgt or tgt == ask_term) for lo, hi, tgt in agent_keys_windows):
                continue
            pname = resolve_ask_project(ad, projects, prefix_to_proj, term_map, space_map)
            akind = str(ad.get("kind") or "").lower()
            tp = "release_approval" if (akind == "release" or RELEASE_RE.search(str(ad.get("question") or ""))) else "ask_answer"
            record_touch(d_str, pname, tp, "ask_card")
            ask_answer_times.append(ans_ms)

    # Usage log events for accepts, closed asks, and settings changes not already recorded
    for ev in usage_events:
        at = int(ev["at"])
        d_str = local_date_str(at)
        if d_str not in day_set:
            continue
        kind = ev.get("kind")
        sp_b = str(ev.get("space") or "").split("/")[-1]
        t_field = str(ev.get("task") or "").upper()
        m_pref = re.match(r"^([A-Z]+)-\d+", t_field)
        proj = (
            prefix_to_proj.get(m_pref.group(1)) if m_pref and m_pref.group(1) in prefix_to_proj
            else space_map.get(sp_b, "cross-project")
        )
        if kind == "act":
            if ev.get("key"):
                record_touch(d_str, proj, "rule", "settings")
                usage_setting_times.append(at)
            elif ev.get("type") == "accept":
                if not any(abs(at - s_ms) <= 30_000 for s_ms, _ in store_accept_times):
                    record_touch(d_str, proj, "other", "ui_accept")
        elif kind == "ask":
            if not any(abs(at - a_ms) <= 10_000 for a_ms in ask_answer_times) and not any(
                lo <= at <= hi for lo, hi, _ in agent_keys_windows
            ):
                record_touch(d_str, proj, "ask_answer", "ask_card")

    # Historical setting change notices (<herdr-setting name="<key>">) not already in usage.jsonl
    # Deduplicate Everywhere notices arriving across multiple space Assistants within 60s
    all_setting_notices: list[tuple[int, str, int]] = []
    for sess in sessions:
        if sess["role"] == "assistant":
            for ms, n_keys in sess["setting_notices"]:
                all_setting_notices.append((ms, sess["project"], n_keys))
    all_setting_notices.sort(key=lambda x: x[0])
    dedup_notice_times: list[int] = []
    for ms, proj, _n_keys in all_setting_notices:
        d_str = local_date_str(ms)
        if d_str not in day_set:
            continue
        if any(abs(ms - u_ms) <= 120_000 for u_ms in usage_setting_times):
            continue
        if any(abs(ms - prev_ms) <= 60_000 for prev_ms in dedup_notice_times):
            continue
        dedup_notice_times.append(ms)
        record_touch(d_str, proj, "rule", "settings")

    # Assemble summary stats (per day, median across all calendar days and active days)
    per_day: list[dict[str, Any]] = []
    touch_totals_all: list[int] = []
    interv_totals_all: list[int] = []
    episode_totals_all: list[int] = []

    for d in days:
        t_tot = sum(touches_by_day_tp[d].values())
        i_tot = sum(interv_by_day_type[d].values())
        ep_tot = interv_episodes_by_day[d]
        touch_totals_all.append(t_tot)
        interv_totals_all.append(i_tot)
        episode_totals_all.append(ep_tot)
        per_day.append({
            "date": d,
            "owner_touches": t_tot,
            "touches_by_touchpoint": {k: touches_by_day_tp[d][k] for k in TOUCHPOINTS if touches_by_day_tp[d][k]},
            "touches_by_channel": {k: touches_by_day_ch[d][k] for k in CHANNELS if touches_by_day_ch[d][k]},
            "touches_by_project": {k: touches_by_day_proj[d][k] for k in project_names if touches_by_day_proj[d][k]},
            "outside_interventions": i_tot,
            "intervention_episodes": ep_tot,
            "interventions_by_type": {k: interv_by_day_type[d][k] for k in INTERVENTION_TYPES if interv_by_day_type[d][k]},
            "interventions_by_project": {k: interv_by_day_proj[d][k] for k in project_names if interv_by_day_proj[d][k]},
            "autopilot_wakes": autopilot_wakes_by_day[d],
        })

    active_days = [r for r in per_day if r["owner_touches"] > 0 or r["outside_interventions"] > 0 or r["autopilot_wakes"] > 0]

    def med(vals: list[int]) -> float:
        return float(statistics.median(vals)) if vals else 0.0

    per_project: dict[str, Any] = {}
    for p in project_names:
        t_p = sum(touches_by_proj_tp[p].values())
        i_p = sum(interv_by_proj_type[p].values())
        if p == "cross-project" and t_p == 0 and i_p == 0:
            continue
        per_project[p] = {
            "prefix": projects[p]["prefix"] if p in projects else None,
            "tasks_total": len(projects[p]["tasks"]) if p in projects else 0,
            "owner_touches": t_p,
            "touches_by_touchpoint": {k: touches_by_proj_tp[p][k] for k in TOUCHPOINTS if touches_by_proj_tp[p][k]},
            "outside_interventions": i_p,
            "interventions_by_type": {k: interv_by_proj_type[p][k] for k in INTERVENTION_TYPES if interv_by_proj_type[p][k]},
        }

    return {
        "since": since,
        "until": until,
        "days_count": len(days),
        "active_days_count": len(active_days),
        "totals": {
            "owner_touches": sum(touch_totals_all),
            "touches_by_touchpoint": {
                k: sum(touches_by_day_tp[d][k] for d in days)
                for k in TOUCHPOINTS
                if sum(touches_by_day_tp[d][k] for d in days)
            },
            "touches_by_channel": {
                k: sum(touches_by_day_ch[d][k] for d in days)
                for k in CHANNELS
                if sum(touches_by_day_ch[d][k] for d in days)
            },
            "usage_log_send_surfaces": dict(surface_counts),
            "outside_interventions": sum(interv_totals_all),
            "intervention_episodes": sum(episode_totals_all),
            "interventions_by_type": {
                k: sum(interv_by_day_type[d][k] for d in days)
                for k in INTERVENTION_TYPES
                if sum(interv_by_day_type[d][k] for d in days)
            },
            "autopilot_wakes": sum(autopilot_wakes_by_day.values()),
        },
        "median_per_day": {
            "owner_touches": med(touch_totals_all),
            "outside_interventions": med(interv_totals_all),
            "intervention_episodes": med(episode_totals_all),
        },
        "median_per_active_day": {
            "owner_touches": med([r["owner_touches"] for r in active_days]),
            "outside_interventions": med([r["outside_interventions"] for r in active_days]),
            "intervention_episodes": med([r["intervention_episodes"] for r in active_days]),
        },
        "per_day": per_day,
        "per_project": per_project,
    }


def fmt_num(v: float) -> str:
    return str(int(v)) if v == int(v) else f"{v:.1f}"


def render_text(report: dict[str, Any]) -> str:
    lines: list[str] = []
    lines.append(
        f"Factory baseline ({report['since']} .. {report['until']}, "
        f"{report['days_count']} calendar days, {report['active_days_count']} active factory days)"
    )
    lines.append("")
    lines.append("Per day:")
    hdr = (
        f"{'Date':<10}  {'Touches':>7}  "
        f"{'goal':>4} {'rule':>4} {'ask':>4} {'rel':>4} {'dig':>4} {'oth':>4}  "
        f"{'Interv':>6} {'(Turns)':>7}  {'Autopilot':>9}"
    )
    lines.append(hdr)
    lines.append("-" * len(hdr))
    for r in report["per_day"]:
        tp = r["touches_by_touchpoint"]
        lines.append(
            f"{r['date']:<10}  {r['owner_touches']:>7}  "
            f"{tp.get('goal', 0):>4} {tp.get('rule', 0):>4} {tp.get('ask_answer', 0):>4} "
            f"{tp.get('release_approval', 0):>4} {tp.get('digest', 0):>4} {tp.get('other', 0):>4}  "
            f"{r['outside_interventions']:>6} {r['intervention_episodes']:>7}  {r['autopilot_wakes']:>9}"
        )
    lines.append("-" * len(hdr))
    tot = report["totals"]
    ttp = tot["touches_by_touchpoint"]
    lines.append(
        f"{'Total':<10}  {tot['owner_touches']:>7}  "
        f"{ttp.get('goal', 0):>4} {ttp.get('rule', 0):>4} {ttp.get('ask_answer', 0):>4} "
        f"{ttp.get('release_approval', 0):>4} {ttp.get('digest', 0):>4} {ttp.get('other', 0):>4}  "
        f"{tot['outside_interventions']:>6} {tot['intervention_episodes']:>7}  {tot['autopilot_wakes']:>9}"
    )
    m_all = report["median_per_day"]
    m_act = report["median_per_active_day"]
    lines.append(
        f"{'Median/day':<10}  {fmt_num(m_all['owner_touches']):>7}  "
        f"{'':>29}  {fmt_num(m_all['outside_interventions']):>6} {fmt_num(m_all['intervention_episodes']):>7}"
    )
    lines.append(
        f"{'Med/active':<10}  {fmt_num(m_act['owner_touches']):>7}  "
        f"{'':>29}  {fmt_num(m_act['outside_interventions']):>6} {fmt_num(m_act['intervention_episodes']):>7}"
    )

    lines.append("")
    lines.append("Per project:")
    phdr = f"{'Project':<18} {'Prefix':<6} {'Tasks':>5}  {'Touches':>7}  {'Interv':>6}"
    lines.append(phdr)
    lines.append("-" * len(phdr))
    for pname, p in report["per_project"].items():
        pref = p["prefix"] or "-"
        lines.append(
            f"{pname:<18} {pref:<6} {p['tasks_total']:>5}  {p['owner_touches']:>7}  {p['outside_interventions']:>6}"
        )

    lines.append("")
    lines.append("Owner touches by channel:")
    for ch, cnt in tot["touches_by_channel"].items():
        lines.append(f"  {ch:<22} {cnt:>5}")
    if tot["usage_log_send_surfaces"]:
        surf_str = ", ".join(f"{k}={v}" for k, v in sorted(tot["usage_log_send_surfaces"].items()))
        lines.append(f"  (usage.jsonl send surfaces kept: {surf_str})")

    lines.append("")
    lines.append("Outside interventions by type:")
    for itype, cnt in tot["interventions_by_type"].items():
        lines.append(f"  {itype:<24} {cnt:>5}")

    return "\n".join(lines)


def default_tasks_dir() -> str:
    primary = os.path.expanduser("~/.local/share/shepherdr/tasks")
    if os.path.isdir(primary):
        return primary
    legacy = os.path.expanduser("~/.herdr/tasks")
    return legacy if os.path.isdir(legacy) else primary


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    ap = argparse.ArgumentParser(
        description="Measure daily owner touches and outside interventions across factory projects."
    )
    ap.add_argument("--since", default=DEFAULT_SINCE, help=f"Start date YYYY-MM-DD (default: {DEFAULT_SINCE})")
    ap.add_argument(
        "--until",
        default=datetime.date.today().isoformat(),
        help="End date YYYY-MM-DD inclusive (default: today)",
    )
    ap.add_argument("--json", action="store_true", help="Emit JSON instead of a formatted table")
    ap.add_argument(
        "--usage-dir",
        action="append",
        dest="usage_dirs",
        help="Override Shepherdr service usage log directory (repeatable)",
    )
    ap.add_argument("--tasks-dir", default=None, help="Override Shepherdr task store directory")
    ap.add_argument("--claude-dir", default="~/.claude/projects", help="Override Claude projects log directory")
    ap.add_argument("--codex-dir", default="~/.codex/sessions", help="Override Codex sessions log directory")
    return ap.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    for label, val in (("--since", args.since), ("--until", args.until)):
        try:
            datetime.date.fromisoformat(val)
        except ValueError:
            print(f"baseline.py: invalid {label} date {val!r} (expected YYYY-MM-DD)", file=sys.stderr)
            return 2

    usage_dirs = args.usage_dirs if args.usage_dirs is not None else [
        "~/.local/state/shepherdr/service",
        "~/.herdr/shepherdr",
    ]
    tasks_dir = os.path.expanduser(args.tasks_dir) if args.tasks_dir is not None else default_tasks_dir()

    report = measure(
        since=args.since,
        until=args.until,
        usage_dirs=usage_dirs,
        tasks_dir=tasks_dir,
        claude_dir=args.claude_dir,
        codex_dir=args.codex_dir,
    )
    if args.json:
        print(json.dumps(report, indent=2))
    else:
        print(render_text(report))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
