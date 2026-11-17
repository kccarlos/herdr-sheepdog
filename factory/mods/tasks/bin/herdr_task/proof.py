"""A task's proof and what its Accept card shows: file-based proof (Markdown, images, recipe.json)
linked from the PR, and the evidence from Git, GitHub and those files, never the agent's own words
(only its summary)."""

import json
import os
import re

from .base import Fail, git
from .github import BAD, CANT_READ_CHECKS, NO_CI_RUN, PENDING, pr_view

# Files named on the card, and commits.
FEW = 3
MD_EXTS = (".md", ".markdown")
IMG_EXTS = (".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg")
PROOF_DIRS = ("docs/proof", "docs/proofs", "proof", "proofs", ".proof", ".sheepdog/proof")
REPORT_DIRS = ("docs/reports", "docs/report")
RECIPE_FILE_KEYS = ("cover", "file", "src", "path", "before", "after")
URL_RE = re.compile(r"^[a-zA-Z][a-zA-Z0-9+.-]*://")


def _task_root(s, t):
    wt = t.get("worktree")
    return wt if t.get("mode") == "worktree" and wt and os.path.isdir(wt) else s.loc["folder"]


def _rel_id(path, root):
    rp, rr = os.path.realpath(path), os.path.realpath(root)
    return os.path.relpath(rp, rr).replace(os.sep, "/") if rp == rr or rp.startswith(rr + os.sep) else rp


def _default_title(s, t):
    ref, name = s.ref(t), (t.get("title") or "").strip()
    return f"{ref}: {name} · Proof" if name else f"{ref} · Proof"


def _md_title(path):
    try:
        with open(path, encoding="utf-8") as f:
            text = f.read()
    except (OSError, UnicodeDecodeError) as e:
        raise Fail(f"Couldn't read proof Markdown {os.path.basename(path)}: {e}")
    if not text.strip():
        raise Fail(f"Proof Markdown {os.path.basename(path)} is empty.")
    m = re.search(r"(?m)^\s*#\s+(.+?)\s*$", text)
    return m.group(1).strip() if m else None


def _validate_recipe(recipe_path, root_dir, rel):
    try:
        with open(recipe_path, encoding="utf-8") as f:
            data = json.load(f)
    except (OSError, UnicodeDecodeError, ValueError) as e:
        raise Fail(f"Proof recipe {rel} isn't valid JSON: {e}")
    if not isinstance(data, dict) or not data:
        raise Fail(f"Proof recipe {rel} must be a non-empty JSON object.")
    files = data.get("files") or []
    if data.get("main") == "main.js" or (isinstance(files, list) and "main.js" in files):
        raise Fail(f"Your proof {rel} is a custom panel (main.js), which isn't supported. Use Markdown, images, or recipe.json, then run ready again.")
    if "blocks" in data and not isinstance(data["blocks"], list):
        raise Fail(f"Proof recipe {rel} field 'blocks' must be a list.")
    real_root = os.path.realpath(root_dir)
    refs = ([data["cover"]] if "cover" in data else []) + [
        item[k] for seq_k in ("blocks", "items", "steps", "media") if isinstance(data.get(seq_k), list)
        for item in data[seq_k] if isinstance(item, dict) for k in RECIPE_FILE_KEYS if k in item]
    for val in refs:
        if isinstance(val, str) and val.strip() and not URL_RE.match(val.strip()):
            target = os.path.realpath(os.path.join(real_root, val.strip()))
            if target != real_root and not target.startswith(real_root + os.sep):
                raise Fail(f"Proof recipe {rel} references {val!r} outside the proof folder.")
            if not os.path.isfile(target):
                raise Fail(f"Proof recipe {rel} references missing file {val!r}.")
    title = data.get("title")
    return title.strip() if isinstance(title, str) and title.strip() else None


def _dir_files(abs_dir, base_dir):
    found = []
    for cur, dirs, files in os.walk(abs_dir):
        dirs[:] = sorted(d for d in dirs if d not in (".git", "__pycache__") and not d.startswith("."))
        for fn in sorted(files):
            if fn != ".DS_Store" and not fn.endswith(".pyc"):
                p = os.path.join(cur, fn)
                found.append((p, fn, _rel_id(p, base_dir)))
    return found


def _inspect_proof(path, base_dir, s, t):
    abs_path = os.path.realpath(path)
    rel = _rel_id(abs_path, base_dir)
    if os.path.isdir(abs_path):
        entries = _dir_files(abs_path, base_dir)
        if any(fn.lower() == "main.js" for _, fn, _ in entries):
            raise Fail(f"Your proof {rel} is a custom panel (contains main.js), which isn't supported. Use Markdown, images, or recipe.json, then run ready again.")
        recipes = [p for p, fn, _ in entries if fn.lower() == "recipe.json" or fn.lower().endswith(".recipe.json")]
        mds = [p for p, fn, _ in entries if fn.lower().endswith(MD_EXTS)]
        imgs = [p for p, fn, _ in entries if fn.lower().endswith(IMG_EXTS)]
        if not recipes and not mds and not imgs:
            raise Fail(f"Proof folder {rel} has no Markdown, image, or recipe.json files.")
        titles = [_validate_recipe(rp, os.path.dirname(rp), _rel_id(rp, base_dir)) for rp in recipes] + [_md_title(mp) for mp in mds]
        title = next((x for x in titles if x), None) or _default_title(s, t)
        return {"id": rel, "title": title, "path": rel, "files": [r for _, _, r in entries], "kind": "recipe" if recipes else "dir"}
    if not os.path.isfile(abs_path):
        raise Fail(f"No proof file or folder {rel} found.")
    low = os.path.basename(abs_path).lower()
    if low == "main.js" or low.endswith(".js"):
        raise Fail(f"Your proof {rel} is a JavaScript file (custom panel), which isn't supported. Use Markdown, images, or recipe.json, then run ready again.")
    if low == "recipe.json" or low.endswith(".recipe.json"):
        title, kind = _validate_recipe(abs_path, os.path.dirname(abs_path), rel) or _default_title(s, t), "recipe"
    elif low.endswith(MD_EXTS):
        title, kind = _md_title(abs_path) or _default_title(s, t), "markdown"
    elif low.endswith(IMG_EXTS):
        if os.path.getsize(abs_path) <= 0:
            raise Fail(f"Proof image {rel} is empty.")
        title, kind = _default_title(s, t), "image"
    else:
        raise Fail(f"Proof {rel} must be a Markdown file, image, recipe.json, or a folder containing them.")
    return {"id": rel, "title": title, "path": rel, "files": [rel], "kind": kind}


def _changed_files(base_dir, s, t):
    if t.get("mode") != "worktree" or s.loc["kind"] != "repo":
        return []
    base = t.get("base") or "main"
    since = base if git(base_dir, "rev-parse", "--verify", "--quiet", base)[0] else t.get("start")
    paths = set()
    if since:
        ok, out, _ = git(base_dir, "diff", "--name-only", f"{since}...HEAD")
        if ok and out:
            paths.update(line.strip() for line in out.splitlines() if line.strip())
    ok, st_out, _ = git(base_dir, "status", "--porcelain")
    if ok and st_out:
        paths.update(line[3:].strip().split(" -> ")[-1].strip('"') for line in st_out.splitlines()
                     if len(line) > 3 and line[:2].strip() != "D")
    return sorted(p for p in paths if p)


def proof_files_of(s, t):
    """Proof and report files on disk for this task, primary proof candidates first, newest first."""
    base_dir = _task_root(s, t)
    if not base_dir or not os.path.isdir(base_dir):
        return []
    ref, seen = s.ref(t).lower(), {}

    def add(abs_path, is_proof=True):
        real = os.path.realpath(abs_path)
        if not os.path.exists(real):
            return
        rel = _rel_id(real, base_dir)
        if rel in seen:
            seen[rel]["is_proof"] = seen[rel]["is_proof"] or is_proof
            return
        try:
            title = _inspect_proof(real, base_dir, s, t)["title"]
        except Fail:
            title = _default_title(s, t)
        seen[rel] = {"id": rel, "abs": real, "title": title, "is_proof": is_proof,
                     "mtime": os.path.getmtime(real) if os.path.exists(real) else 0}

    for d in PROOF_DIRS:
        dp = os.path.join(base_dir, d)
        for fn in sorted(os.listdir(dp)) if os.path.isdir(dp) else []:
            low, p = fn.lower(), os.path.join(dp, fn)
            if (low == ref and os.path.isdir(p)) or low in (f"{ref}.md", f"{ref}.markdown", f"{ref}.recipe.json") or (
                    low.startswith(ref + ".") and low.endswith(IMG_EXTS)):
                add(p, is_proof=True)
            elif low.startswith((ref + "-", ref + "_")) and low.endswith((*MD_EXTS, *IMG_EXTS, ".json")):
                add(p, is_proof=False)
    for d in REPORT_DIRS:
        dp = os.path.join(base_dir, d)
        for fn in sorted(os.listdir(dp)) if os.path.isdir(dp) else []:
            if (fn.lower() == ref or fn.lower().startswith((ref + "-", ref + "_", ref + "."))) and not fn.endswith(".pyc"):
                add(os.path.join(dp, fn), is_proof=False)
    for fn in sorted(os.listdir(base_dir)):
        if fn.lower() in (f"{ref}-proof.md", f"{ref}.proof.md", f"proof-{ref}.md"):
            add(os.path.join(base_dir, fn), is_proof=True)
    for rel_p in _changed_files(base_dir, s, t):
        norm, full = rel_p.replace(os.sep, "/"), os.path.join(base_dir, rel_p)
        low, bn = norm.lower(), os.path.basename(norm).lower()
        if not os.path.exists(full):
            continue
        if low in ("proof.md", "recipe.json"):
            add(full, is_proof=True)
        elif any(low.startswith(d + "/") for d in PROOF_DIRS):
            parent = os.path.dirname(full)
            if os.path.dirname(norm) not in PROOF_DIRS and (bn in ("recipe.json", "main.js") or any(
                    os.path.isfile(os.path.join(parent, m)) for m in ("recipe.json", "main.js"))):
                add(parent, is_proof=True)
            elif bn.endswith((*MD_EXTS, *IMG_EXTS, ".recipe.json")):
                add(full, is_proof=True)
        elif any(low.startswith(d + "/") for d in REPORT_DIRS) and bn.endswith((*MD_EXTS, *IMG_EXTS, ".json")):
            add(full, is_proof=False)
    return sorted(seen.values(), key=lambda r: (not r["is_proof"], -r["mtime"], r["id"]))


def find_proof(s, t, given=None):
    """The task's proof, {id, title, path, files, kind}: the file or folder given (ready --proof <path>),
    else the task's discovered proof file/folder (Markdown, image, or recipe.json). None: none found.
    A custom panel (main.js) is refused."""
    base_dir, folder = _task_root(s, t), s.loc["folder"]
    if given is not None:
        raw = given.strip()
        if not raw:
            raise Fail("Give a proof file or folder path after --proof.")
        cand = raw if os.path.isabs(raw) else os.path.join(base_dir, raw)
        if not os.path.isabs(raw) and not os.path.exists(cand) and folder != base_dir and os.path.exists(os.path.join(folder, raw)):
            cand, base_dir = os.path.join(folder, raw), folder
        if not os.path.exists(cand):
            raise Fail(f"No proof file or folder {raw} in {base_dir}.")
        return _inspect_proof(cand, base_dir, s, t)
    mine = [r for r in proof_files_of(s, t) if r["is_proof"]]
    return _inspect_proof(mine[0]["abs"], base_dir, s, t) if mine else None


def task_artifacts(s, t, proof=None):
    """The other files its agent wrote for this task (a research task's report), [{id, title}]:
    linked on its Accept card beside its proof (SHEP-96)."""
    skip = (proof or {}).get("id")
    return [{"id": r["id"], "title": r["title"]} for r in proof_files_of(s, t) if r["id"] and r["id"] != skip]


def numstat(out, files):
    """git --numstat lines added into files {path: [added, removed]} (binary files count 0)."""
    for line in out.splitlines():
        parts = line.split("\t")
        if len(parts) == 3:
            a, r, path = parts
            cur = files.setdefault(path, [0, 0])
            cur[0] += int(a) if a.isdigit() else 0
            cur[1] += int(r) if r.isdigit() else 0


def checks_of(t):
    """GitHub's checks on its pull request, in words: {state, text}; failed and running say so,
    so a green one is never assumed."""
    if not t.get("pr"):
        return {"state": "none", "text": "No GitHub checks here"}
    v = pr_view(t)
    if not v:
        return {"state": "unknown", "text": "Couldn't ask GitHub"}
    rows = v.get("statusCheckRollup") or []
    word = lambda c: (c.get("conclusion") or c.get("state") or "").upper()
    bad = [c.get("name") or c.get("context") or "a check" for c in rows if word(c) in BAD]
    running = [c for c in rows if (c.get("status") or c.get("state") or "").upper() in PENDING and not c.get("conclusion")]
    if bad:
        return {"state": "failed", "text": f"{len(bad)} failing: " + ", ".join(bad[:FEW])}
    if running:
        return {"state": "running", "text": f"{len(running)} of {len(rows)} still running"}
    if v.get("checks_unknown"):
        # SHEP-179: this machine's GitHub token can't read its checks: never said to pass
        return {"state": "unknown", "text": CANT_READ_CHECKS}
    if not rows and t["pr"].get("no_ci"):
        return {"state": "unknown", "text": NO_CI_RUN}  # FAC-105: CI is set up, but nothing ran
    if not rows:
        return {"state": "none", "text": "No checks on its pull request"}
    return {"state": "passed", "text": f"All {len(rows)} passed"}


def evidence(s, t):
    """What an Accept card shows: {summary, details, commits: {count, list}, diff: {files, added, removed,
    list} or None, git (False: not a git folder), scope, checks, proof, artifacts (its others), merges (Accept
    merges its branch first: an open pull request or commits past its base; the button says so)}."""
    main, out = s.loc["folder"], {"summary": t.get("summary"), "details": t.get("details"), "proof": t.get("proof") or find_proof(s, t)}
    out["artifacts"] = task_artifacts(s, t, out["proof"])
    commits, files = [], {}
    fmt = "--format=%h%x09%s"
    if s.loc["kind"] != "repo":
        out.update(git=False, commits={"count": 0, "list": []}, diff=None, checks=checks_of(t), merges=False)
        return out
    if t.get("mode") == "worktree" and t.get("branch"):
        # Its branch past where it meets its base: exactly what Accept merges.
        ok, tip, _ = git(main, "rev-parse", "--verify", "--quiet", f"refs/heads/{t['branch']}")
        tip = tip if ok and tip else t.get("head")
        base = t.get("base") or "main"
        since = base if git(main, "rev-parse", "--verify", "--quiet", base)[0] else t.get("start")
        if tip and since:
            ok, log, _ = git(main, "log", fmt, f"{since}..{tip}")
            commits = [line.split("\t", 1) for line in log.splitlines() if "\t" in line] if ok else []
            ok, diff, _ = git(main, "diff", "--numstat", f"{since}...{tip}")
            if ok:
                numstat(diff, files)
            else:
                files = None
        out["scope"] = f"{t['branch']} into {base}"
    else:
        # In the folder itself: the commits made here while it worked (other work committed here
        # meanwhile counts too, so the card says "while it worked").
        start, end = (t.get("started") or 0) // 1000, ((t.get("ready_at") or 0) // 1000 + 60) or None
        rng = [f"--since=@{start}"] + ([f"--until=@{end}"] if end else [])
        ok, log, _ = git(main, "log", fmt, *rng)
        commits = [line.split("\t", 1) for line in log.splitlines() if "\t" in line] if ok else []
        ok, diff, _ = git(main, "log", "--numstat", "--format=", *rng)
        if ok:
            numstat(diff, files)
        else:
            files = None
        out["scope"] = "committed here while it worked"
    out["git"] = True
    out["commits"] = {"count": len(commits), "list": [{"sha": c[0], "subject": c[1]} for c in commits[:FEW]]}
    out["diff"] = None if files is None else {
        "files": len(files), "added": sum(v[0] for v in files.values()), "removed": sum(v[1] for v in files.values()),
        "list": [{"path": p, "added": v[0], "removed": v[1]} for p, v in sorted(files.items(), key=lambda x: -(x[1][0] + x[1][1]))[:FEW]]}
    out["checks"] = checks_of(t)
    # What Accept does (cmd_accept): a worktree task's branch with work of its own is merged first; else only done.
    out["merges"] = bool(t.get("mode") == "worktree" and t.get("branch") and (t.get("pr") or commits))
    return out
