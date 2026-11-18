"""GitHub and merging: pull requests, checks, and finishing a merged task."""

import contextlib
import datetime
import fnmatch
import json
import os
import re
import shutil
import tempfile

from .base import Fail, GH, git, hc_json, now, run, STORE
from .agents import agent_running, detail, live_agents, live_helpers, mid_turn_agents, open_question, status_word


# ---- GitHub ---------------------------------------------------------------------------------

REPO_CMDS = {"pr", "issue", "run", "release", "secret", "variable", "workflow", "label", "ruleset", "cache", "attestation", "deploy-key"}


def _flag_repo(args):
    for i, a in enumerate(args):
        s = str(a)
        if s in ("--repo", "-R"):
            return str(args[i + 1]) if i + 1 < len(args) else ""
        if s.startswith("--repo=") or (s.startswith("-R") and len(s) > 2):
            return s.split("=", 1)[1] if s.startswith("--") else s[2:]
    return None


def _bad_api_arg(a):
    s = str(a).lstrip("/").split("?", 1)[0]
    segs = s.split("/")
    return "{owner}" in str(a) or "{repo}" in str(a) or (s.startswith("repos") and (s == "repos" or (segs[0] == "repos" and (len(segs) < 3 or not segs[1] or not segs[2]))))


def gh(*args, repo=None, cwd=None):
    # Every repo-targeting gh call names its repo explicitly, so an `upstream` remote or gh's default can't redirect it.
    if args and args[0] in REPO_CMDS and not (args[0] in ("secret", "variable") and any(f in args for f in ("--org", "-o", "--user", "-u"))):
        target = _flag_repo(args)
        if target is None and repo:
            args, target = (*args[:2], "--repo", repo, *args[2:]), repo
        if not target or "/" not in str(target):
            return False, "", "gh call is missing --repo <owner/name>"
    elif args[:2] == ("repo", "view"):
        if (len(args) <= 2 or str(args[2]).startswith("-")) and repo:
            args = (*args[:2], repo, *args[2:])
        if len(args) <= 2 or str(args[2]).startswith("-") or "/" not in str(args[2]):
            return False, "", "gh repo view is missing <owner/name>"
    elif args and args[0] == "api":
        if repo and "/" in str(repo):
            owner, _, name = str(repo).partition("/")
            args = tuple(str(a).replace("{owner}", owner).replace("{repo}", name) if not str(a).startswith("query=") else a for a in args)
        if any(_bad_api_arg(a) for i, a in enumerate(args[1:], 1) if not str(a).startswith("query=") and args[i - 1] not in ("-q", "--jq", "-f", "--raw-field")):
            return False, "", "gh api call has unresolved {owner}/{repo}"
    gh_bin = GH if GH != "gh" else (os.environ.get("HERDR_TASK_GH") or "gh")
    where = cwd if cwd and os.path.isdir(cwd) else (STORE if os.path.isdir(STORE) else tempfile.gettempdir())
    kw = {"cwd": where, **({"env": {k: v for k, v in os.environ.items() if k != "GH_REPO"}} if "GH_REPO" in os.environ else {})}
    # gh not installed (or not on this caller's PATH, as a background service's): it doesn't work here, said plainly
    try:
        r = run([gh_bin, *args], **kw)
    except FileNotFoundError:
        return False, "", "gh isn't installed here (GitHub CLI)"
    return r.returncode == 0, r.stdout.strip(), (r.stderr or r.stdout).strip()


def repo_from_url(url, explicit_host=False):
    """GitHub "owner/name" from a remote URL (HTTPS, SSH, or an SSH host alias for GitHub unless `explicit_host`), else None."""
    u = str(url or "").strip()
    ssh = not explicit_host and not u.startswith(("http://", "https://", "git://"))
    m = (re.match(r"^(?:https?|ssh|git)://(?:[^/@]+@)?([^/:]+)(?::\d+)?/(.+)$", u) if "://" in u
         else re.match(r"^(?:[^/@:]+@)?([A-Za-z0-9._-]{2,}):/?(.+)$", u))
    if not m:
        return None
    host, path = m.group(1).lower(), re.sub(r"\.git$", "", m.group(2).strip("/"))
    gh_host = (os.environ.get("GH_HOST") or "").strip().lower()
    if (any(x in host for x in ("gitlab", "bitbucket", "codeberg", "sr.ht", "sourcehut")) if ssh else not ("github" in host or (gh_host and host == gh_host))):
        return None
    parts = path.split("/")
    return path if len(parts) == 2 and all(re.fullmatch(r"[A-Za-z0-9._-]+", p) and p not in (".", "..") for p in parts) else None


def repo_from_pr_url(url):
    m = re.match(r"^https?://[^/]+/([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)/pull/\d+", str(url or "").strip())
    return m.group(1) if m else None


def push_remote(main, branch=None):
    """The remote `branch` (or `main`) pushes to; never `upstream` unless configured as the push remote."""
    if not (names := git(main, "remote")[1].split()):
        return None
    br = branch or git(main, "symbolic-ref", "--quiet", "--short", "HEAD")[1]
    cfg = lambda key: git(main, "config", "--get", key)[1]
    for r, allow_up in (((cfg(f"branch.{br}.pushRemote"), True),) if br else ()) + ((cfg("remote.pushDefault"), True),) + (((cfg(f"branch.{br}.remote"), False),) if br else ()):
        if r and r in names and (allow_up or r != "upstream"):
            return r
    return "origin" if "origin" in names else next((n for n in names if n != "upstream"), None)


def remote_of(main, branch=None):
    return push_remote(main, branch) or "origin"


def remote_repo(main, remote, explicit_host=False):
    """GitHub "owner/name" for `remote` in `main` (its pushurl first when set, else url), or None."""
    if not remote:
        return None
    if push_u := git(main, "config", "--get", f"remote.{remote}.pushurl")[1]:
        return repo_from_url(push_u, explicit_host=explicit_host) or (
            repo_from_url(git(main, "config", "--get", f"remote.{remote}.url")[1], explicit_host=explicit_host)
            if os.path.exists(push_u) else None)
    return repo_from_url(git(main, "config", "--get", f"remote.{remote}.url")[1], explicit_host=explicit_host)


def remote_slug(s, task=None, explicit_host=False):
    """GitHub "owner/name" from the task's or its base branch's push remote (without calling gh), else None."""
    if s.loc["kind"] != "repo":
        return None
    pr_r = ((task or {}).get("pr") or {}).get("repo")
    if pr_r and "/" in pr_r:
        return pr_r
    wt = (task or {}).get("worktree")
    folder = wt if wt and os.path.isdir(wt) else s.loc["folder"]
    if not os.path.isdir(folder):
        return None
    br = ((task or {}).get("pr") or {}).get("branch") or (task or {}).get("branch")
    base = (task or {}).get("base") or "main"
    for candidate in (br, base):
        if slug := remote_repo(folder, push_remote(folder, candidate), explicit_host=explicit_host):
            return slug
    return None


def _gh_bin():
    return GH if GH != "gh" else (os.environ.get("HERDR_TASK_GH") or "gh")


def github(s, task=None):
    """The repo on GitHub ("owner/name") from the remote the task (or folder) pushes to, when gh works
    there; asked again after an hour. None: tasks merge locally."""
    pr_r = ((task or {}).get("pr") or {}).get("repo")
    if s.loc["kind"] != "repo" or not shutil.which(_gh_bin()):
        return None
    if pr_r and "/" in pr_r:
        return pr_r
    if not (slug := remote_slug(s, task)):
        return None
    g = s.data.get("github") or {}
    if g and (g.get("from") or g.get("repo") or "").lower() == slug.lower() and now() - g.get("at", 0) < 3600000:
        return g.get("repo")
    ok, out, err = gh("repo", "view", slug, "--json", "nameWithOwner", "-q", ".nameWithOwner")
    if not ok and not settled(err):
        # #988: gh failing on a GitHub remote (a network blip, its sign-in lapsed) is never remembered as "not on
        # GitHub": None, with why in github_unreachable(s).
        s.github_error = err or "gh couldn't read the repo"
        return None
    try:
        repo = (json.loads(out).get("nameWithOwner") or slug) if ok and out.startswith("{") else (out.splitlines()[0].strip() if ok and out.strip() else (slug if ok else None))
    except ValueError:
        repo = slug
    s.data["github"] = {"repo": repo, "from": slug, "at": now()}
    s.dirty = True
    return repo


_github = github
# gh's answers that say for sure it won't work here (never signed in, no such repo or no access): tasks
# merge locally, as before; anything else (the network, GitHub's own errors) may pass.
SETTLED = ("auth login", "not logged in", "could not resolve to a repository", "none of the git remotes",
           "no git remotes", "not a git repository", "isn't installed")


def settled(err):
    return any(x in (err or "").lower() for x in SETTLED)


def on_github(folder):
    """A remote of the repo at folder points at github.com."""
    ok, out, _ = git(folder, "remote", "-v")
    return ok and bool(re.search(r"github\.com[:/]", out))


def github_unreachable(s):
    """Why GitHub couldn't be asked about a repo whose remote is there (github() said None), else None."""
    return getattr(s, "github_error", None) if s.loc["kind"] == "repo" and shutil.which(_gh_bin()) else None


def github_required(s, task=None):
    """True when the folder or its base branch tracks a GitHub remote (merges must go through a PR)."""
    return bool(remote_slug(s, task, explicit_host=True) or github_unreachable(s))


def github_refusal(s, task=None):
    """Why GitHub couldn't be reached/used for a GitHub-backed repo when github() returned None."""
    if why := github_unreachable(s):
        return why
    if not shutil.which(_gh_bin()):
        return "gh isn't installed here (GitHub CLI)"
    return getattr(s, "github_error", None) or "gh isn't signed in or couldn't access this GitHub repo (run `gh auth login`)"


def released(s):
    """The task list's lock let go around slow git work (Store.released(), #1007); a no-op elsewhere."""
    let_go = getattr(s, "released", None)
    return let_go() if let_go else contextlib.nullcontext()


def task_github(fn, s, task=None):
    return fn(s, task) if fn is _github else fn(s)


def pr_repo(task, s=None, fn=None):
    pr = (task or {}).get("pr") or {}
    repo = pr.get("repo") or repo_from_pr_url(pr.get("url")) or (task_github(fn or github, s, task) if s is not None else None)
    if repo and task and isinstance(task.get("pr"), dict) and not task["pr"].get("repo"):
        task["pr"]["repo"] = repo
    return repo


def _push_target(s, task):
    wt, br = task["worktree"], task["branch"]
    folder = wt if wt and os.path.isdir(wt) else s.loc["folder"]
    remote = remote_of(folder, br)
    if not remote_repo(folder, remote):
        base_r = push_remote(folder, task.get("base") or "main")
        if base_r and remote_repo(folder, base_r):
            remote = base_r
    # The pull request it continues (#828) gets its new commits on its own branch, never forced.
    to = (task.get("pr") or {}).get("branch") or br
    has, tip, _ = git(folder, "rev-parse", "--verify", "--quiet", f"refs/heads/{br}")
    seen, there, _ = git(folder, "ls-remote", remote, f"refs/heads/{to}")
    return folder, remote, br, to, has, tip, seen, there


def branch_needs_push(s, task):
    """Whether pushing/opening a PR for `task` would push a new commit to the remote."""
    if not (task.get("pr") or {}).get("number"):
        return True
    wt = task.get("worktree")
    folder = wt if wt and os.path.isdir(wt) else s.loc["folder"]
    if not folder or not os.path.isdir(folder):
        return bool(task.get("head") and task["pr"].get("head_sha") and task["pr"]["head_sha"] != task["head"])
    _, _, _, _, has, tip, seen, there = _push_target(s, task)
    return not (has and tip and seen and (there.split()[:1] == [tip] or tip == task.get("head") and synced_head(task, pr_repo(task), there.split()[0] if there.split() else "")))


def push_branch(s, task):
    """Pushes the task's branch to where its pull request reads it; Fail with git's error when it can't."""
    folder, remote, br, to, has, tip, seen, there = _push_target(s, task)
    # Already there at its exact tip (pushed by hand): no second push through its hooks (#795).
    if not (has and tip and seen and (there.split()[:1] == [tip] or tip == task.get("head") and synced_head(task, pr_repo(task), there.split()[0] if there.split() else ""))):
        # #1007: the task list's lock let go meanwhile (a pre-push hook can run for many minutes).
        with released(s):
            ok, out, err = git(folder, "push", "--force-with-lease", "-u", remote, br) if to == br else \
                git(folder, "push", remote, f"{br}:refs/heads/{to}")
        if not ok:
            raise Fail(f"Couldn't push {br}" + (f" to {to}" if to != br else "") + f": {push_error(out, err)}")


def pr_head_problem(s, task, repo, head, theirs=None):
    """FAC-88: a review of `head` must be of the commit GitHub has. None when the pull request's head is `head`
    (or can't be read); else pushes the branch when it is ahead and holds `head`, and says why when it still isn't."""
    pr = task.get("pr") or {}
    if not (head and pr.get("number") and repo):
        return None
    ask = lambda: gh("pr", "view", str(pr["number"]), "--repo", repo, "--json", "headRefOid", "-q", ".headRefOid")
    if not theirs:
        ok, theirs, _ = ask()
        if not ok:
            return None
    if not theirs or theirs == head or synced_head(task, repo, theirs):  # FAC-98: brought up to date with its base
        return None
    wt = task.get("worktree") or ""
    ahead = os.path.isdir(wt) and git(wt, "rev-parse", f"refs/heads/{task.get('branch')}")[1] == head \
        and git(wt, "merge-base", "--is-ancestor", theirs, head)[0]
    if ahead:
        from .replan import _last_new_recap_at
        reset_before = int(task.get("review_reset_at") or _last_new_recap_at(task) or 0)
        try:
            push_branch(s, task)
            if int(task.get("review_reset_at") or _last_new_recap_at(task) or 0) != reset_before or task.get("recap"):
                ref = s.ref(task) if s is not None and hasattr(s, "ref") else (task.get("branch") or "task")
                return f"{ref} was re-planned while pushing {task.get('branch') or ''}; its pull request was not updated.".replace("  ", " ")
        except Fail as e:
            return str(e)
        if isinstance(task.get("pr"), dict):
            task["pr"]["checked"] = 0  # the next tick reads its state again (a conflict the push cleared goes away)
        s.dirty = True
        ok, theirs, _ = ask()
        if not ok or theirs == head:
            return None
    return f"the PR's head isn't the reviewed commit: GitHub has {theirs[:7]}, reviewed {head[:7]}"


def pr_open(s, task, repo, summary=None):
    """Pushes the task's branch and opens its pull request (or keeps the open one, also one opened
    by hand or by its agent for the branch: SHEP-13's "already exists"). Its description follows
    each ready (#1004) when Herdr wrote it."""
    br = task["branch"]
    repo = pr_repo(task) or repo
    # Continuing a pull request already open on another branch, its work pushed there (#828): that
    # one is the task's, never a second with the same commits.
    if not (task.get("pr") or {}).get("number") and pr_at_tip(task, repo):
        if summary:
            pr_note(task, task["pr"].get("repo") or repo, summary)
        return task["pr"]
    from .replan import _last_new_recap_at
    reset_before = int(task.get("review_reset_at") or _last_new_recap_at(task) or 0)
    push_branch(s, task)
    if int(task.get("review_reset_at") or _last_new_recap_at(task) or 0) != reset_before or task.get("recap"):
        ref = s.ref(task) if s is not None and hasattr(s, "ref") else br
        raise Fail(f"{ref} was re-planned while pushing {br}; its pull request was not opened or updated.")
    pr = task.get("pr") or {}
    if pr.get("number"):
        pr["repo"] = repo
        ok, out, _ = gh("pr", "view", str(pr["number"]), "--repo", repo, "--json", "state", "-q", ".state")
        if ok and out == "OPEN":
            pr_describe(s, task, summary, repo=repo)
            if summary:
                pr_note(task, repo, summary)
            return pr
        if pr.get("branch"):
            # The other branch's pull request it continued was closed or merged: its own branch from here.
            task.pop("pr")
            return pr_open(s, task, repo, summary)
    if pr_adopt(task, repo):
        pr_describe(s, task, summary, repo=repo)
        if summary:
            pr_note(task, task["pr"].get("repo") or repo, summary)
        return task["pr"]
    # SHEP-59: never a second pull request for a branch that already had one merged (one at its tip
    # is done before this: already_merged()); commits made since go on a new branch.
    done = pr_merged(task, repo)
    if done:
        raise Fail(f"Pull request #{done['number']} for {br} is already merged, and {br} has commits since. "
                   f"Put them on a new branch (a new task) rather than a second pull request.")
    ok, out, err = gh("pr", "create", "--repo", repo, "--head", br, "--base", task.get("base") or "main",
                      "--title", pr_title(s, task), "--body", pr_body(s, task, summary, repo=repo))
    if not ok or not out:
        raise Fail(f"Couldn't open a pull request: {err}")
    url = out.split()[-1]
    task["pr"] = {"number": int(url.rstrip("/").split("/")[-1]), "url": url, "repo": repo}
    if summary and summary.strip():
        task["pr_note"] = {"number": task["pr"]["number"], "said": summary.strip()}  # its body says it already
    return task["pr"]


def push_error(out, err, lines=15):
    """What a failed push said, for ready's refusal (#963): a pre-push hook's own words (what failed,
    where its log is) often go to stdout, which git leaves there; both, its last lines only."""
    said = [x for x in (out + "\n" + err).splitlines() if x.strip()]
    return "\n".join(said[-lines:]) or "git push failed"


def pr_note(task, repo, summary):
    """Ready's summary on the task's open pull request: one comment, edited in place on a later ready, and
    nothing when it says the same as last time (#1058: each ready posted another copy under the person's
    name). task["pr_note"] = {number, id (the comment's), said}, for the pull request it was on."""
    summary = (summary or "").strip()
    number = task["pr"]["number"]
    note = task.get("pr_note") or {}
    if note.get("number") != number:
        note = {"number": number}
    if not summary or note.get("said") == summary:
        return
    if note.get("id"):
        ok, _, _ = gh("api", "-X", "PATCH", f"repos/{repo}/issues/comments/{note['id']}", "-f", f"body={summary}")
        if ok:
            task["pr_note"] = {**note, "said": summary}
            return
    # none yet, or it was deleted: a new one, its id (gh prints the comment's link, …#issuecomment-<id>) kept
    ok, out, _ = gh("pr", "comment", str(number), "--repo", repo, "--body", summary)
    if ok:
        m = re.search(r"#issuecomment-(\d+)", out or "")
        task["pr_note"] = {"number": number, "id": int(m.group(1)) if m else None, "said": summary}


def pr_describe(s, task, summary=None, repo=None):
    """#1004: an open pull request whose description Herdr wrote (pr_body(), "Task: SHEP-n" first)
    gets the one this ready says (its summary, details and proof); one written by a person or an
    agent, or another branch's it continues (#828), is left as it is."""
    pr = task.get("pr") or {}
    if not pr.get("number") or pr.get("branch"):
        return
    ok, body, _ = gh("pr", "view", str(pr["number"]), "--repo", pr["repo"], "--json", "body", "-q", ".body")
    want = pr_body(s, task, summary, repo=repo or pr.get("repo"))
    if ok and body.strip().splitlines()[:1] == [f"Task: {s.ref(task)}"] and body.strip() != want.strip():
        gh("pr", "edit", str(pr["number"]), "--repo", pr["repo"], "--body", want)


def pr_title(s, task):
    """The repo's commit titles read "SHEP-198: the task's title" (a squash merge adds "(#945)"): the
    task's ref first, so the pull request stays findable by it."""
    ref = s.ref(task)
    title = " ".join(str(task.get("title") or "").split())
    rest = re.sub(rf"^{re.escape(ref)}\b[:\s-]*", "", title, flags=re.I)
    return f"{ref}: {rest or ref}"


def pr_body(s, task, summary=None, repo=None):
    """Short: the task's ref, what ready said (summary), the details and links to its proof files.
    Never the brief: that stays with the task store."""
    lines = [f"Task: {s.ref(task)}"]
    if summary and summary.strip():
        lines += ["", "## Summary", "", summary.strip()]
    if (task.get("details") or "").strip():
        lines += ["", "## Details", "", task["details"].strip()]
    repo = repo or (task.get("pr") or {}).get("repo") or pr_repo(task, s, github)
    branch = task.get("branch")
    link = lambda p: f"https://github.com/{repo}/blob/{branch}/{p.lstrip('/')}" if repo and branch else p
    proof = task.get("proof") or {}
    title, path = proof.get("title"), proof.get("path")
    proof_lines = []
    if path:
        proof_lines.append(f"- [{title or path}]({link(path)})")
        for f in proof.get("files") or []:
            if f != path:
                proof_lines.append(f"  - [{f}]({link(f)})")
    elif title:
        proof_lines.append(f"Proof: {title}")
    if getattr(s, "loc", None) and s.loc.get("folder"):
        from .proof import task_artifacts
        for a in task_artifacts(s, task, proof or None):
            proof_lines.append(f"- [{a['title']}]({link(a['id'])})")
    if proof_lines:
        lines += ["", "## Proof", "", *proof_lines]
    return "\n".join(lines)


def pr_adopt(task, repo):
    """The open pull request for the task's branch, opened by hand or by its agent, recorded on the
    task; None when there is none (SHEP-13: ready failed with "already exists"). Only one from this
    repo (never a fork's branch of the same name), into the task's base, at the branch's tip here."""
    repo = repo or pr_repo(task)
    if not repo:
        return None
    ok, out, _ = gh("pr", "list", "--repo", repo, "--head", task["branch"], "--state", "open",
                    "--json", "number,url,isCrossRepository,baseRefName,headRefOid")
    tip_ok, tip, _ = git(task["worktree"], "rev-parse", task["branch"])
    try:
        found = next((p for p in json.loads(out) if not p.get("isCrossRepository")
                      and p.get("baseRefName") == (task.get("base") or "main")
                      and tip_ok and tip and p.get("headRefOid") == tip), None) if ok and out else None
    except (ValueError, TypeError, AttributeError):
        found = None
    if found:
        task["pr"] = {"number": found["number"], "url": found["url"], "repo": repo}
    return task.get("pr") if found else None


def pr_for_branch(task, repo):
    """The open pull request from this repo for the task's branch into its base, whatever its head
    (opened by its agent, ready never knowing: #988), as gh lists it, or None."""
    ok, out, _ = gh("pr", "list", "--repo", repo, "--head", task["branch"], "--state", "open",
                    "--json", "number,url,isCrossRepository,baseRefName,headRefOid")
    try:
        return next((p for p in json.loads(out) if not p.get("isCrossRepository")
                     and p.get("baseRefName") == (task.get("base") or "main")), None) if ok and out else None
    except (ValueError, TypeError, AttributeError):
        return None


def pr_at_tip(task, repo):
    """An open pull request from this repo, into the task's base, on another branch whose head is
    exactly the task branch's tip: recorded on the task, else None (#828)."""
    repo = repo or pr_repo(task)
    ok, tip, _ = git(task["worktree"], "rev-parse", "--verify", "--quiet", f"refs/heads/{task['branch']}")
    if not (repo and ok and tip):
        return None
    ok, out, _ = gh("pr", "list", "--repo", repo, "--state", "open", "--search", tip,
                    "--json", "number,url,isCrossRepository,baseRefName,headRefName,headRefOid")
    try:
        found = next((p for p in json.loads(out) if not p.get("isCrossRepository") and p.get("headRefOid") == tip
                      and p.get("headRefName") and p.get("headRefName") != task["branch"]
                      and p.get("baseRefName") == (task.get("base") or "main")), None) if ok and out else None
    except (ValueError, TypeError, AttributeError):
        found = None
    if found:
        task["pr"] = {"number": found["number"], "url": found["url"], "repo": repo, "branch": found["headRefName"]}
    return task.get("pr") if found else None


def pr_merged(task, repo):
    """The merged pull request for the task's branch (from this repo, into its base), or None
    (SHEP-59: a squash merge leaves the branch's commits out of the base)."""
    repo = repo or pr_repo(task)
    if not repo:
        return None
    ok, out, _ = gh("pr", "list", "--repo", repo, "--head", task["branch"], "--state", "merged",
                    "--json", "number,url,isCrossRepository,baseRefName,headRefOid,mergeCommit")
    try:
        return next((p for p in json.loads(out) if not p.get("isCrossRepository")
                     and p.get("baseRefName") == (task.get("base") or "main")), None) if ok and out else None
    except (ValueError, TypeError, AttributeError):
        return None


def pr_merged_at_tip(s, task, repo):
    """The merged pull request on GitHub whose head matches this branch's current tip, recorded on the task, else None."""
    main = s.loc["folder"]
    tip = (git(main, "rev-parse", "--verify", "--quiet", f"refs/heads/{task.get('branch')}")[1]
           if task.get("branch") else "") or task.get("head")
    found = pr_merged(task, repo) if tip else None
    if not found or found.get("headRefOid") != tip:
        return None
    task["pr"] = {"number": found["number"], "url": found["url"], "repo": repo}
    s.dirty = True
    return {**task["pr"], "mergeCommit": found.get("mergeCommit")}


def landed(s, task):
    """Its own work is in its base branch, though not as its commits (squash-merged, SHEP-59):
    merging its tip into the base, here or on its remote, would change nothing. Only a branch with
    work of its own (own_work()): one that just caught up with its base is never merged."""
    if not own_work(s, task):
        return False
    main, base = s.loc["folder"], task.get("base") or "main"
    ok, tip, _ = git(main, "rev-parse", "--verify", "--quiet", f"refs/heads/{task.get('branch')}")
    tip = tip if ok and tip else task.get("head")
    for ref in (base, f"refs/remotes/{remote_of(main, task.get('branch'))}/{base}"):
        ok, tree, _ = git(main, "merge-tree", "--write-tree", ref, tip)
        has, have, _ = git(main, "rev-parse", "--verify", "--quiet", f"{ref}^{{tree}}")
        if ok and has and tree and tree.split()[0] == have:
            return True
    return False


def already_merged(s, task):
    """SHEP-59: its work is already in its base (its pull request merged, squash too, or nothing left
    to merge): ready and the tick mark it done instead of opening a pull request. Returns the merged
    pull request ({} when merged without one), or None. Its pull request is recorded on the task."""
    if merged_here(s, task):
        return {}
    repo = task_github(github, s, task)
    tip = task.get("head")
    if repo:
        main = s.loc["folder"]
        git(main, "fetch", "--quiet", remote_of(main, task.get("branch")), task.get("base") or "main")
        tip = git(main, "rev-parse", "--verify", "--quiet", f"refs/heads/{task.get('branch')}")[1] or tip
        if task.get("pr"):
            pr_repo(task, s, github)
            v = pr_view(task)
            if v and v.get("state") == "MERGED" and v.get("headRefOid") == tip:
                return {**task["pr"], "mergeCommit": v.get("mergeCommit")}
        found = pr_merged(task, repo)
        if found and found.get("headRefOid") == tip:
            task["pr"] = {"number": found["number"], "url": found["url"], "repo": repo}
            return {**task["pr"], "mergeCommit": found.get("mergeCommit")}
    return {} if landed(s, task) else None


BAD = {"FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE"}
PENDING = {"QUEUED", "IN_PROGRESS", "PENDING", "WAITING", "REQUESTED", "EXPECTED"}


VIEW = "state,mergeable,headRefOid,mergeCommit,reviewDecision,mergeStateStatus,isCrossRepository"
# SHEP-179: a token that may read a pull request but not its checks (a fine-grained one without Checks: read, as
# the exe.dev GitHub integration's) gets GitHub's "Resource not accessible" for them: a permission, never a blip.
DENIED = ("resource not accessible", "http 403", "must have", "not have permission", "insufficient scope")
CANT_READ_CHECKS = "Can't read GitHub checks here: check them on GitHub."
CHECKS_PERMISSION = "Checks: read and Commit statuses: read"


def denied(err):
    return any(x in (err or "").lower() for x in DENIED)


def pr_view(task):
    """The pull request as GitHub has it (gh pr view's fields), or None when GitHub couldn't be asked. Its checks
    (statusCheckRollup) unreadable here (SHEP-179): the rest still read, the checks asked another way (check_rows());
    neither works: "checks_unknown" set, never a wait for checks nobody can see."""
    pr = task["pr"]
    repo = pr_repo(task)
    if not repo:
        return None
    view = lambda fields: gh("pr", "view", str(pr["number"]), "--repo", repo, "--json", fields)
    ok, out, err = view(VIEW + ",statusCheckRollup")
    rest = not ok and denied(err)
    if rest:
        ok, out, _ = view(VIEW)
    try:
        v = json.loads(out) if ok else None
    except ValueError:
        return None
    if not isinstance(v, dict):
        return v
    if rest:
        rows, whole = check_rows(repo, v.get("headRefOid"))
        v["statusCheckRollup"] = rows
        if not whole:
            v["checks_unknown"] = True
    if not rest and not v.get("statusCheckRollup") and v.get("headRefOid"):
        # FAC-115: no rollup row, yet a run of any event (push, workflow_dispatch) may sit on the head commit
        v["statusCheckRollup"] = check_rows(repo, v["headRefOid"])[0]
    if v.get("statusCheckRollup"):
        v["statusCheckRollup"] = latest_checks(v["statusCheckRollup"])
    if v.get("state") == "OPEN" and not pr_pending(v) and runs_pending(pr["repo"], v.get("headRefOid")):
        # #1044: a workflow run at its head not finished, its jobs not all shown as checks yet (a job
        # that waits on another's needs, a run waiting for a runner): its checks still run.
        v["runs_pending"] = True
    return v


def runs_pending(repo, sha):
    """GitHub Actions has a run at commit sha not finished yet (queued, waiting, in progress). False
    when none, or GitHub couldn't say."""
    if not sha:
        return False
    ok, out, _ = gh("api", f"repos/{repo}/actions/runs?head_sha={sha}&per_page=50", "-q", "[.workflow_runs[].status]")
    try:
        states = json.loads(out) if ok else []
    except ValueError:
        return False
    return any(st != "completed" for st in states if isinstance(st, str))


def check_rows(repo, sha):
    """A commit's checks from GitHub's REST API (check runs and commit statuses), as statusCheckRollup's rows:
    (rows, whole). whole: both could be read. A token with one permission but not the other still sees part."""
    rows, whole = [], bool(sha)
    if not sha:
        return rows, False
    ok, out, _ = gh("api", f"repos/{repo}/commits/{sha}/check-runs?per_page=100")
    try:
        for c in json.loads(out).get("check_runs") or [] if ok else []:
            rows.append({"name": c.get("name"), "status": (c.get("status") or "").upper(),
                         "conclusion": (c.get("conclusion") or "").upper(), "startedAt": c.get("started_at") or "",
                         "completedAt": c.get("completed_at") or "",
                         "workflowName": ((c.get("app") or {}).get("slug") or "")})
    except (ValueError, AttributeError):
        ok = False
    whole = whole and ok
    ok, out, _ = gh("api", f"repos/{repo}/commits/{sha}/status")
    try:
        for c in json.loads(out).get("statuses") or [] if ok else []:
            rows.append({"context": c.get("context"), "state": (c.get("state") or "").upper(),
                         "startedAt": c.get("updated_at") or c.get("created_at") or ""})
    except (ValueError, AttributeError):
        ok = False
    return rows, whole and ok


def note_checks(s, task, v):
    """Remembers on the folder's task list whether its GitHub checks could be read (`shepherdr doctor` warns while
    they can't), and on the task's pull request (the Accept card says so)."""
    pr = task.get("pr") or {}
    if v.get("checks_unknown"):
        if not pr.get("checks_unknown"):
            pr["checks_unknown"] = True
        s.data["github_checks"] = {"repo": pr.get("repo"), "at": now(), "denied": True}
        s.dirty = True
    else:
        if pr.pop("checks_unknown", None) or (s.data.get("github_checks") or {}).get("repo") == pr.get("repo"):
            s.data.pop("github_checks", None)
            s.dirty = True


def latest_checks(rows):
    """Each check by its newest run (#781): a run that a newer run of the same check superseded (a
    workflow's cancel-in-progress leaves it CANCELLED beside the one that passed) is left out."""
    newest = {}
    for i, c in enumerate(rows):
        name = c.get("name") or c.get("context")
        key = (c.get("workflowName") or "", name) if name else i
        when = c.get("startedAt") or c.get("completedAt") or ""
        if key not in newest or when >= newest[key][0]:
            newest[key] = (when, c)
    return [c for _, c in newest.values()]


BEHIND = "It is behind its base branch, which must be up to date to merge."


def pr_problem(v):
    """What keeps a pull request from merging, for the person; None when nothing does."""
    if v.get("state") == "CLOSED":
        return "The pull request was closed."
    if v.get("mergeable") == "CONFLICTING":
        return "It conflicts with the base branch."
    checks = v.get("statusCheckRollup") or []
    bad = [c.get("name") or c.get("context") or "a check" for c in checks if word(c) in BAD]
    if any(word(c) in BAD - STOPPED for c in checks):  # a real failure: as ever, stopped ones listed too
        return "Checks failing: " + ", ".join(bad[:3]) + (" and more." if len(bad) > 3 else ".")
    if stopped := stopped_checks(v):
        return stop_words({}, stopped[0])  # FAC-102: CI, not code (task_problem() re-runs it once)
    # #743: the base requires it up to date with itself (GitHub says BEHIND only under that rule).
    if v.get("mergeStateStatus") == "BEHIND":
        return BEHIND
    return None


# FAC-98 (note 52): two pull requests each green on its own can break their base together. Before Herdr merges one
# whose head lacks the base's tip, GitHub's update-branch merges the base in (a merge commit, never a rebase or force
# push) and the merge waits for the checks on that new head. Merge-only: no review round, its agent isn't woken.
SYNC_GRACE_MS = 2 * 60000  # update-branch is asynchronous: the old head may show this long after it was asked
SYNC_CHECKS_MS = 5 * 60000  # the updated head's checks may take this long to show; none by then: none will
DRIFTED = (" They failed after Herdr brought the branch up to date with its base (the base moved on since): "
           "pull the branch first (git pull), fix, then say ready again.")
UPDATE_CONFLICT = "Herdr couldn't bring it up to date with its base branch (a conflict?)"


def main_red_hold(s, base):
    """FAC-113: why merges (and update-branch) into `base` are held while its newest finished GitHub run is red
    (the watch main_check keeps: ghnews.py), else None. Resumes by itself once main_check sees it green."""
    red = ((s.data.get("main_watch") or {}).get(base) or {}).get("red")
    return f"{base} is red since {red[:7]}: merges held until it's green." if red else None


def pr_behind(task, repo, head, unknown=0):
    """FAC-98: how many of its base branch's commits the pull request's head lacks (GitHub's compare), or 0 when
    GitHub can't say, unless `unknown` is supplied."""
    ok, out, _ = gh("api", f"repos/{repo}/compare/{task.get('base') or 'main'}...{head}", "--jq", ".behind_by")
    try:
        return int(out) if ok else unknown
    except ValueError:
        return unknown


def pr_update_branch(task, repo, head):
    """FAC-98: GitHub's update-branch for its pull request at `head` (only while that is still its head). The pending
    merge stays; the tick merges once the new head's checks pass. Returns (ok, error)."""
    ok, _, err = gh("api", "-X", "PUT", f"repos/{repo}/pulls/{task['pr']['number']}/update-branch",
                    "-f", f"expected_head_sha={head}")
    if ok:
        task["pr"]["synced"] = {"from": task.get("head"), "asked": head, "at": now()}
        task["pr"]["checked"] = 0
    return ok, err


def synced_head(task, repo, theirs):
    """FAC-98: `theirs` when it is the merge commit the last update-branch made (its first parent the head that was
    asked: the ready head, or an earlier update's), "" when GitHub couldn't say, else None."""
    pr, head = task.get("pr") or {}, task.get("head")
    sy = pr.get("synced") or {}
    if not (head and theirs and repo) or theirs == head or sy.get("from") != head:
        return None
    if sy.get("to") == theirs:
        return theirs
    ok, out, _ = gh("api", f"repos/{repo}/commits/{theirs}", "--jq", "[.parents[].sha] | join(\" \")")
    if not ok:
        return ""
    if out.split()[:1] == [sy.get("asked")]:
        sy["to"] = theirs
        return theirs
    return None


# FAC-102 (WukongMind): a check GitHub cancelled or timed out, not superseded (latest_checks()), is CI, not code: no
# test failed. Re-run once per head, but only a check cancelled from outside (runner lost, superseded). FAC-112: one that
# ran to its own timeout-minutes (conclusion TIMED_OUT, or CANCELLED after about the workflow file's timeout-minutes:
# GitHub reports a job's own timeout as cancelled) mostly hits it again, so it goes to the Assistant at once, no re-run.
STOPPED = {"CANCELLED", "TIMED_OUT"}
CI_STOPPED = 'CI "'  # such a reason's start
RERUN_SHOW_MS = 10 * 60000  # a re-run whose jobs don't show by then won't


def word(c):
    return (c.get("conclusion") or c.get("state") or "").upper()


def stopped_checks(v):
    return [c for c in v.get("statusCheckRollup") or [] if word(c) in STOPPED]


def _ms(text):
    try:
        return datetime.datetime.strptime(str(text), "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=datetime.timezone.utc).timestamp() * 1000
    except ValueError:
        return None


def job_timeout(folder, job):
    """The timeout-minutes a workflow file in folder gives the job (by its key or name), or None. A line scan, no YAML."""
    if not folder:
        return None
    d = os.path.join(folder, ".github", "workflows")
    try:
        files = sorted(f for f in os.listdir(d) if f.endswith((".yml", ".yaml")))
    except OSError:
        return None
    job = re.sub(r" \(.*\)$", "", job or "")  # a matrix job: "test (ubuntu)"
    for f in files:
        try:
            with open(os.path.join(d, f)) as fh:
                lines = fh.read().splitlines()
        except OSError:
            continue
        jobs, on, ind, cur = [], False, None, None
        for line in lines:
            text = line.strip()
            if not text or text.startswith("#"):
                continue
            at = len(line) - len(line.lstrip())
            if at == 0:
                on, cur = text.startswith("jobs:"), None
            elif on and at == (ind := ind or at):
                cur = {"key": text.rstrip(":").strip()}
                jobs.append(cur)
            elif on and cur is not None and at == cur.setdefault("at", at):
                m = re.match(r"(name|timeout-minutes):\s*['\"]?([^'\"#]*?)['\"]?\s*(#.*)?$", text)
                if m:
                    cur[m.group(1)] = m.group(2)
        for j in jobs:
            if job in (j["key"], j.get("name")) and str(j.get("timeout-minutes", "")).isdigit():
                return int(j["timeout-minutes"])
    return None


def hit_limit(task, c):
    """(hit its own time limit, minutes it ran, the job's timeout-minutes or None): TIMED_OUT, or it ran to within a
    minute of the timeout-minutes its workflow file (in the task's worktree) gives it."""
    name = c.get("name") or c.get("context") or "a check"
    a, b = _ms(c.get("startedAt")), _ms(c.get("completedAt"))
    took = int(b - a) // 60000 if a and b and b >= a else None
    limit = job_timeout(task.get("worktree"), name)
    reached = bool((limit or 360) and took is not None and took >= (limit or 360) - 1)  # GitHub's default is 360
    return word(c) == "TIMED_OUT" or reached, took, limit


def stop_words(task, c):
    """'CI "check" timed out after 46m: …', with the job's timeout when its workflow file (in the task's worktree) says."""
    name = c.get("name") or c.get("context") or "a check"
    timed, took, limit = hit_limit(task, c)
    reached = bool(timed and limit and took is not None and took >= limit - 1)
    text = f'CI "{name}" ' + ("timed out" if timed else "was cancelled")
    if took is not None:
        text += " after " + (f"{took // 60}h {took % 60}m" if took >= 60 else f"{took}m")
    why = (f"its timeout-minutes is {limit}, so the job's timeout was reached" if reached
           else "likely the job's timeout" if timed else "cancelled on GitHub or its runner was lost")
    return f"{text} ({why}), not a failing test."


def ci_stopped(task, v, repo):
    """FAC-102: its stopped checks re-run once per head (gh run rerun --failed), shown as still running while that
    shows; a stop again on that head (or no re-run possible): the CI-infrastructure reason. None while it waits."""
    stopped, pr, head = stopped_checks(v), task.setdefault("pr", {}), v.get("headRefOid") or ""
    sig = lambda c: f"{c.get('detailsUrl') or c.get('targetUrl') or ''}|{c.get('completedAt') or ''}"

    def hold():
        for c in stopped:
            c.update(status="IN_PROGRESS", conclusion="")
    slow = [c for c in stopped if hit_limit(task, c)[0]]
    if slow:  # FAC-112: its own time limit, not a hiccup: a re-run would mostly take as long again
        name, _, limit = (slow[0].get("name") or slow[0].get("context") or "a check"), 0, hit_limit(task, slow[0])[2]
        return (f"{stop_words(task, slow[0])} Job \"{name}\" hit its {limit}-min limit: too long, not a hiccup, so Herdr "
                "did not re-run it. A CI problem in " + ", ".join(f'"{c.get("name") or c.get("context")}"' for c in slow[:3]) +
                ": add a fix task to speed up or split the job (or raise its timeout-minutes) instead of sending code back."
                if limit else
                f"{stop_words(task, slow[0])} Job \"{name}\" hit its time limit: too long, not a hiccup, so Herdr did not "
                "re-run it. A CI problem: add a fix task to speed up or split the job instead of sending code back.")
    rr = pr.get("ci_rerun") or {}
    if rr.get("head") != head:
        if pr_pending(v):  # gh refuses a run still in progress: the one re-run waits for it
            return hold()
        runs = sorted({m.group(1) for c in stopped if (m := re.search(r"/actions/runs/(\d+)", c.get("detailsUrl") or ""))})
        ok = bool(runs) and all(gh("run", "rerun", r, "--failed", "--repo", repo)[0] for r in runs)
        rr = pr["ci_rerun"] = {"head": head, "runs": runs, "rows": [sig(c) for c in stopped], "at": now(), "ok": ok}
        if ok:
            return hold()
    elif rr.get("ok") and now() - (rr.get("at") or 0) < RERUN_SHOW_MS and all(sig(c) in rr.get("rows", []) for c in stopped):
        return hold()  # the stop the re-run was asked for still shows
    names = ", ".join(f'"{c.get("name") or c.get("context")}"' for c in stopped[:3])
    again = "Herdr re-ran it once and it stopped again" if rr.get("ok") else "Herdr couldn't re-run it"
    return (f"{stop_words(task, stopped[0])} {again}: a CI-infrastructure problem in {names}, "
            "not a code failure. Add a fix task for the CI job instead of sending code back.")


def task_problem(task, v, repo):
    """pr_problem(), saying when its checks failed on the head Herdr brought up to date with its base (FAC-98); a
    cancelled or timed-out check re-run once (FAC-102)."""
    p = pr_problem(v)
    if p and p.startswith(CI_STOPPED):
        p = ci_stopped(task, v, repo)
    if p and p.startswith("Checks failing") and synced_head(task, repo, v.get("headRefOid")):
        p += DRIFTED
    return p


def pr_pending(v):
    if v.get("runs_pending"):
        return True
    return any((c.get("status") or c.get("state") or "").upper() in PENDING and not (c.get("conclusion") or "")
               for c in v.get("statusCheckRollup") or [])


def needs_approval(v):
    """#743: the base requires an approving review that hasn't landed (or changes were asked for):
    a merge can't go through yet, and that is nobody's problem to fix here; it waits calmly."""
    return v.get("reviewDecision") in ("REVIEW_REQUIRED", "CHANGES_REQUESTED")


QUEUE_QUERY = """query($owner: String!, $name: String!, $n: Int!) { repository(owner: $owner, name: $name) {
  pullRequest(number: $n) { id state isInMergeQueue autoMergeRequest { enabledAt }
    timelineItems(last: 1, itemTypes: [REMOVED_FROM_MERGE_QUEUE_EVENT]) { nodes { ... on RemovedFromMergeQueueEvent { reason } } } } } }"""


def queue_entry(task):
    """Its pull request in GitHub's merge queue (#802): {"id": node id, "in": in the queue, "auto": set to merge by
    itself (it joins the queue once its checks pass), "reason": why the queue last removed it}, or
    None when GitHub couldn't say."""
    pr = task["pr"]
    repo = pr_repo(task)
    if not repo or "/" not in repo:
        return None
    owner, _, name = repo.partition("/")
    ok, out, _ = gh("api", "graphql", "-f", f"query={QUEUE_QUERY}", "-f", f"owner={owner}", "-f", f"name={name}",
                    "-F", f"n={pr['number']}")
    try:
        p = json.loads(out)["data"]["repository"]["pullRequest"] if ok else None
    except (ValueError, TypeError, KeyError):
        p = None
    if not isinstance(p, dict):
        return None
    removed = ((p.get("timelineItems") or {}).get("nodes") or [{}])[-1] or {}
    return {"id": p.get("id"), "in": bool(p.get("isInMergeQueue")), "auto": bool(p.get("autoMergeRequest")),
            "reason": removed.get("reason"), "state": p.get("state")}


def drop_branch(s, task):
    """After GitHub's merge queue merged it: its branch on GitHub goes, as --delete-branch would have
    (gh refuses that flag on a queue, #801). Already gone (the repo deletes merged branches): fine."""
    if task.get("branch"):
        main = s.loc["folder"]
        # --no-verify: a pre-push hook (a typecheck) checks what is pushed; a deletion has nothing to
        # check, and the hook would hold the task list's lock for minutes (#1007).
        git(main, "push", "--quiet", "--no-verify", remote_of(main, task["branch"]), "--delete", task["branch"])


def queue_merge(task, head):
    """Asks GitHub to merge its pull request (squash) at exactly head: merged now, or queued in its
    merge queue. Never --delete-branch (#1045: drop_branch() once it merged). (ok, err)."""
    pr = task["pr"]
    ok, _, err = gh("pr", "merge", str(pr["number"]), "--repo", pr["repo"], "--squash", "--match-head-commit", head or "")
    return ok, err


# #1044, #1018: a pull request just pushed shows no checks until GitHub starts them, which can take
# minutes. No checks yet is never "none set up" while its repo says some will come: its workflows at
# its head run on pull requests, or GitHub blocks the merge with none there (a required check).
NO_CHECKS_MS = 30000  # no workflow of its own: this long for checks from elsewhere (another CI's statuses)
MAYBE_CHECKS_MS = 5 * 60000  # a workflow whose filters (paths, branches, a push) may leave it out: this long
CHECKS_START_MS = 20 * 60000  # one that runs on it: this long, then "they never started" (no merge by itself)
# SHEP-179: checks that can't be read here, while GitHub blocks the merge: Working this long at most, then Ready.
UNKNOWN_CHECKS_MS = 20 * 60000
CHECKS_RUNNING, GITHUB_LOOKS, BACK_TO_AGENT = "Checks running", "Waiting for GitHub", "Back to its agent to fix"
# FAC-105: a base whose workflows run on its pull requests, yet none ran on this one this long after ready:
# never read as passing (Actions disabled on the repo, or not triggered); it waits for the person.
NO_CI_RUN_MS = 5 * 60000
NO_CI_RUN = "No CI run appeared for this PR; GitHub Actions may be disabled or not triggered"
_ON = re.compile(r"""^(?:on|"on"|'on'|true)\s*:\s*(.*)$""")
_CI_SEEN = {}  # "<base commit>:<base>" -> whether its workflows run on a pull request to it


def _indent(line):
    return len(line) - len(line.lstrip())
CHECKS_NEVER = "Its checks never started on GitHub."
WORKFLOWS = ".github/workflows"
PR_EVENTS = ("pull_request", "pull_request_target")


def _block(lines, i):
    """The lines nested under lines[i] (more indented), and where they end."""
    ind = len(lines[i]) - len(lines[i].lstrip())
    j = i + 1
    while j < len(lines) and len(lines[j]) - len(lines[j].lstrip()) > ind:
        j += 1
    return lines[i + 1:j], j


def _key(line):
    k, colon, rest = line.strip().partition(":")
    return (k.strip().strip("'\""), rest.strip()) if colon else (None, line.strip())


def workflow_checks(text, base="main"):
    """One workflow file's say on a task's pull request into base: "yes" (it runs on pull requests,
    unfiltered or for base), "maybe" (only on a push, or filtered by paths or other branches), or None
    (never on one). Its "on:" read by indentation (no YAML parser here)."""
    lines = [ln.split(" #")[0].rstrip() for ln in text.splitlines()]
    lines = [ln for ln in lines if ln.strip() and not ln.lstrip().startswith("#")]
    for i, ln in enumerate(lines):
        if ln[:1].isspace() or _key(ln)[0] not in ("on", "true"):
            continue
        inline = _key(ln)[1]
        words = set(re.findall(r"[\w-]+", inline))
        if inline:
            return "yes" if words & set(PR_EVENTS) else "maybe" if "push" in words else None
        body, _ = _block(lines, i)
        say = None
        j = 0
        while j < len(body):
            k, rest = _key(body[j].lstrip("- ").rstrip()) if body[j].lstrip().startswith("-") else _key(body[j])
            sub, end = _block(body, j)
            j = end if end > j else j + 1
            if k in PR_EVENTS or rest in PR_EVENTS:
                keys = {}
                m = 0
                while m < len(sub):
                    sk, sv = _key(sub[m])
                    inner, e = _block(sub, m)
                    keys[sk] = sv + " " + " ".join(x.strip() for x in inner)
                    m = e
                if keys.keys() & {"paths", "paths-ignore", "branches-ignore"}:
                    say = say or "maybe"
                elif "branches" in keys and not re.search(rf"(?<![\w/.-]){re.escape(base)}(?![\w/.-])|\*", keys["branches"]):
                    say = say or "maybe"
                else:
                    return "yes"
            elif k == "push" or rest == "push":
                say = say or "maybe"
        return say
    return None


def expected_checks(s, task, sha):
    """Whether checks will come for its pull request at commit sha, from its repo's workflows there:
    "yes", "maybe" or None (workflow_checks()). Kept on the pull request per commit."""
    pr = task.get("pr") or {}
    seen = pr.get("expects") or {}
    if sha and seen.get("sha") == sha:
        return seen.get("is")
    main = s.loc["folder"]
    ref = sha if sha and git(main, "cat-file", "-e", f"{sha}^{{commit}}")[0] else (task.get("branch") or task.get("head"))
    ok, out, _ = git(main, "ls-tree", "--name-only", f"{ref}:{WORKFLOWS}") if ref else (False, "", "")
    found = None
    for name in out.split() if ok else []:
        if name.endswith((".yml", ".yaml")):
            said = workflow_checks(git(main, "show", f"{ref}:{WORKFLOWS}/{name}")[1], task.get("base") or "main")
            found = "yes" if "yes" in (found, said) else said or found
    if sha:
        pr["expects"] = {"sha": sha, "is": found}
        s.dirty = True
    return found


def checks_since(s, task, v):
    """Since when its pull request's head has been what it is (ms): when it said ready, or when a newer
    commit was first seen there (#1018: a push after ready, its checks start again)."""
    pr = task.get("pr") or {}
    sha = v.get("headRefOid")
    if sha and pr.get("head_sha") != sha:
        pr["head_at"] = now() if pr.get("head_sha") else (task.get("ready_at") or now())
        pr["head_sha"] = sha
        s.dirty = True
    return max(task.get("ready_at") or 0, pr.get("head_at") or 0)


def checks_missing(v, since, expect=None):
    """No checks on its pull request yet (#1044): None when it has some or none are coming; "waiting"
    while they may still start; "never" when they should have and didn't (CHECKS_START_MS)."""
    if v.get("statusCheckRollup"):
        return None
    gone = now() - (since or 0)
    if v.get("checks_unknown"):
        # Checks that can't be read here (SHEP-179): a moment for GitHub, then pr_wait() decides.
        return "waiting" if gone < NO_CHECKS_MS else None
    blocked = v.get("mergeStateStatus") == "BLOCKED" and not needs_approval(v)
    if expect == "yes" or blocked:
        return "waiting" if gone < CHECKS_START_MS else "never"
    return "waiting" if gone < (MAYBE_CHECKS_MS if expect == "maybe" else NO_CHECKS_MS) else None


def _items(lines, i):
    """The list a `key:` at lines[i] holds: inline ([a, b] or a) or the `- a` lines under it (or at its own indent)."""
    rest = lines[i].split(":", 1)[1].strip()
    if rest:
        return [x.strip(" '\"") for x in rest.strip("[]").split(",") if x.strip()]
    out = []
    for line in lines[i + 1:]:
        if _indent(line) < _indent(lines[i]) or _indent(line) == _indent(lines[i]) and not line.lstrip().startswith("-"):
            break
        if line.strip().startswith("-"):
            out.append(line.strip()[1:].strip(" '\""))
    return out


def runs_on_pr(text, base):
    """Whether a workflow file's `on:` runs it on a pull request to `base`: pull_request present, its branches
    absent or matching base, branches-ignore not. A small reader of the usual forms, no YAML library."""
    lines = [x for x in (re.sub(r"\s+#.*$", "", x).rstrip() for x in text.splitlines()) if x.strip() and not x.lstrip().startswith("#")]
    for i, line in enumerate(lines):
        m = _indent(line) == 0 and _ON.match(line)
        if not m:
            continue
        if m.group(1):  # on: pull_request · on: [push, pull_request]
            return "pull_request" in re.findall(r"[\w-]+", m.group(1))
        block = []
        for x in lines[i + 1:]:
            if _indent(x) == 0 and not x.startswith("-"):  # on:\n- pull_request: a list at on's own indent
                break
            block.append(x)
        for j, b in enumerate(block):
            if b.strip().lstrip("-").strip().split(":")[0].strip(" '\"") != "pull_request":
                continue
            sub = []
            for x in block[j + 1:]:
                if _indent(x) <= _indent(b):
                    break
                sub.append(x)
            keys = {x.strip().split(":")[0]: k for k, x in enumerate(sub) if ":" in x}
            match = lambda key: any(fnmatch.fnmatchcase(base, p) for p in _items(sub, keys[key]) if not p.startswith("!"))
            if "branches-ignore" in keys and match("branches-ignore"):
                return False
            return "branches" not in keys or match("branches")
        return False
    return False


def base_commit(s, task):
    """The task's base branch as fetched here (its remote's copy, else the local branch): (commit, base) or (None, base)."""
    main, base = s.loc["folder"], task.get("base") or "main"
    for ref in (f"{remote_of(main, task.get('branch'))}/{base}", base):
        ok, sha, _ = git(main, "rev-parse", "--verify", "-q", f"{ref}^{{commit}}")
        if ok and sha:
            return sha, base
    return None, base


def ci_expected(s, task):
    """FAC-105: the task's base branch (as fetched here: its remote's copy, else the local branch) has a workflow
    in .github/workflows that runs on a pull request to it. Cached per base commit."""
    main = s.loc["folder"]
    sha, base = base_commit(s, task)
    if not sha:
        return False
    key = f"{sha}:{base}"
    if key not in _CI_SEEN:
        ok, names, _ = git(main, "ls-tree", "--name-only", sha, ".github/workflows/")
        _CI_SEEN[key] = ok and any(runs_on_pr(git(main, "show", f"{sha}:{n}")[1], base)
                                   for n in names.splitlines() if n.endswith((".yml", ".yaml")))
    return _CI_SEEN[key]


STARTED_CI = "Started CI by hand: no run appeared"
_DISPATCH = re.compile(r"(?m)^\s*(?:-\s*)?workflow_dispatch\b|^(?:on|true)\s*:.*\bworkflow_dispatch\b")


def dispatch_file(s, task):
    """FAC-115: a workflow file (name) on the base that has workflow_dispatch and runs on pull requests to it."""
    main = s.loc["folder"]
    sha, base = base_commit(s, task)
    if not sha:
        return None
    ok, names, _ = git(main, "ls-tree", "--name-only", sha, ".github/workflows/")
    for n in names.splitlines() if ok else []:
        if n.endswith((".yml", ".yaml")):
            text = git(main, "show", f"{sha}:{n}")[1]
            if _DISPATCH.search(text) and runs_on_pr(text, base):
                return os.path.basename(n)
    return None


def ci_dispatch(s, task, v):
    """FAC-115: no run appeared on the head: start its workflow once per head commit (gh workflow run <file> --ref
    <head branch>) when the repo has one with workflow_dispatch that runs on pull requests to the base and Actions
    are enabled. Never for a fork's head. Returns STARTED_CI while that run is awaited, else None (FAC-105's wait)."""
    pr, head = task.setdefault("pr", {}), v.get("headRefOid") or ""
    done = pr.get("ci_dispatched") or {}
    if done.get("head") == head:
        return STARTED_CI if done.get("ok") and now() - (done.get("at") or 0) < UNKNOWN_CHECKS_MS else None
    branch, repo = pr.get("branch") or task.get("branch") or "", pr_repo(task)
    if not (head and branch and repo) or v.get("isCrossRepository"):
        return None
    ok, out, _ = gh("api", f"repos/{repo}/actions/permissions")
    try:
        enabled = ok and bool(json.loads(out).get("enabled"))
    except (ValueError, AttributeError):
        enabled = False
    name = dispatch_file(s, task) if enabled else None
    if not name:
        return None
    ok = gh("workflow", "run", name, "--repo", repo, "--ref", branch)[0]
    pr["ci_dispatched"] = {"head": head, "file": name, "at": now(), "ok": ok}
    s.dirty = True
    return STARTED_CI if ok else None


def ci_awaited(s, task, v):
    """FAC-105: no check on its pull request (v) yet, and its base's workflows say one should run."""
    return not v.get("statusCheckRollup") and not v.get("checks_unknown") and ci_expected(s, task)


def ci_runs_on(task, v):
    """FAC-105: GitHub Actions has a run for its pull request's head commit (its checks just don't show yet)."""
    ok, out, _ = gh("run", "list", "--repo", pr_repo(task), "--branch", (task["pr"].get("branch") or task.get("branch") or ""),
                    "--json", "headSha", "--limit", "20")
    try:
        return ok and any(r.get("headSha") == v.get("headRefOid") for r in json.loads(out))
    except (ValueError, TypeError, AttributeError):
        return False


CI_CAP_WAIT = "Waiting for CI runner slot"
PR_RUN_ACTIVE = {"QUEUED", "IN_PROGRESS", "WAITING", "PENDING", "REQUESTED"}
PR_RUN_EVENTS = {"pull_request", "pull_request_target"}


def ci_runner_cap(s):
    """Configured PR-in-CI runner cap for this project (0 or None: unlimited)."""
    from .settings import settings_for
    raw = (settings_for(s)["tasks"].get("ci_runner_cap") if s is not None else 0)
    try:
        return max(0, int(raw or 0))
    except (TypeError, ValueError):
        return 0


def prs_in_ci(s, repo=None, skip_task=None):
    """Counts other PRs currently running CI (store tasks with open pending PRs + active GitHub PR runs)."""
    seen = set()
    skip_id = skip_task.get("id") if isinstance(skip_task, dict) else None
    skip_brs = {skip_task.get("branch"), (skip_task.get("pr") or {}).get("branch")} - {None, ""} if isinstance(skip_task, dict) else set()
    base = (skip_task or {}).get("base") or "main"
    for other in (s.data.get("tasks") or []) if s is not None and isinstance(getattr(s, "data", None), dict) else ():
        if other is skip_task or (skip_id is not None and other.get("id") == skip_id):
            continue
        if other.get("state") not in ("running", "ready") or other.get("merged") or other.get("push_held"):
            continue
        opr = other.get("pr") or {}
        if not opr.get("number") or opr.get("no_ci"):
            continue
        if repo and opr.get("repo") and opr["repo"].lower() != repo.lower():
            continue
        if other.get("ci_pending") or opr.get("wait") in (CHECKS_RUNNING, STARTED_CI, "Checks are still running.") or other.get("merge_queued"):
            seen.add(opr.get("branch") or other.get("branch") or f"pr#{opr['number']}")
    if repo:
        ok, out, _ = gh("run", "list", "--repo", repo, "--json", "status,headBranch,event", "--limit", "50")
        try:
            runs = json.loads(out) if ok and out else []
        except ValueError:
            runs = []
        if isinstance(runs, list):
            for r in runs:
                if not isinstance(r, dict) or (r.get("status") or "").upper() not in PR_RUN_ACTIVE:
                    continue
                ev = (r.get("event") or "").lower()
                br = r.get("headBranch") or ""
                if br and br in skip_brs:
                    continue
                if ev in PR_RUN_EVENTS or (ev in ("", "workflow_dispatch") and br and br != base):
                    seen.add(br or f"run#{len(seen)}")
    return len(seen)


def ci_cap_hold(s, task, repo=None):
    """Reason to hold pushing another PR/commit into CI when PRs in CI >= runner cap, else None."""
    cap = ci_runner_cap(s)
    if cap <= 0:
        return None
    in_ci = prs_in_ci(s, repo, skip_task=task)
    if in_ci >= cap:
        return f"{CI_CAP_WAIT} ({in_ci} of {cap} PRs in CI)"
    return None


def pr_wait(v, since, expect=None, ci=False):
    """SHEP-175: what a Ready task's pull request (v: pr_view's answer) still waits on before the
    person has anything to do, in a few words, or None once its checks are done and GitHub knows it
    can merge. A problem (pr_problem()) goes back to its agent: Working still, never Ready first.
    expect: expected_checks(); since: checks_since(). ci: a check is expected from its base's workflows
    (ci_awaited()), so none waits NO_CI_RUN_MS and the tick takes over after (FAC-105)."""
    if v.get("state") != "OPEN":
        return None
    if pr_problem(v):
        return BACK_TO_AGENT
    if pr_pending(v):
        return CHECKS_RUNNING
    if (v.get("mergeable") or "").upper() not in ("MERGEABLE",):
        return GITHUB_LOOKS
    if (not v.get("statusCheckRollup") and now() - (since or 0) < NO_CI_RUN_MS) if ci else checks_missing(v, since, expect) == "waiting":
        return CHECKS_RUNNING
    if v.get("checks_unknown") and v.get("mergeStateStatus") == "BLOCKED" and not needs_approval(v) \
            and now() - (since or 0) < UNKNOWN_CHECKS_MS:
        # SHEP-179: its checks can't be read here, but GitHub blocks the merge (a required check not passed yet):
        # Working for a while, then Ready anyway (the Accept card says the checks couldn't be read; GitHub still
        # refuses a merge its checks block). CLEAN: GitHub says they passed.
        return CHECKS_RUNNING
    return None


def update_main(s, task):
    """After a merge on GitHub: the base branch here catches up when it's checked out and clean."""
    main, base = s.loc["folder"], task.get("base") or "main"
    remote = remote_of(main, task.get("branch"))
    git(main, "fetch", remote, base)
    ok, cur, _ = git(main, "symbolic-ref", "--short", "HEAD")
    if ok and cur == base and not git(main, "status", "--porcelain", "--untracked-files=no")[1]:
        git(main, "merge", "--ff-only", f"{remote}/{base}")
    elif not (ok and cur == base):
        # A base not checked out here (a task's own --base): its branch fast-forwards (git refuses
        # anything else, and a branch checked out in another worktree).
        git(main, "fetch", remote, f"{base}:{base}")


MAIN_WATCH_MS = 3 * 3600000  # how long main is watched after a task merges through its pull request


def watch_main(s, task, sha=None):
    """A task just merged through its pull request: its base branch on GitHub is watched for a few
    hours (ghnews.main_pass), so the project's coordinator hears once if it goes red."""
    if not task.get("pr"):
        return
    repo = pr_repo(task, s, github)
    if not repo:
        return
    base = task.get("base") or "main"
    folder = (getattr(s, "loc", None) or {}).get("folder")
    w = s.data.setdefault("main_watch", {}).setdefault(base, {})
    w.pop("space", None)
    w.update(repo=repo, until=now() + MAIN_WATCH_MS, folder=folder or w.get("folder"))
    merged = [m for m in w.get("merged") or [] if now() - (m.get("at") or 0) < 24 * 3600000]
    merged.append({"ref": s.ref(task), "title": task["title"], "pr": task["pr"]["number"], "at": now(), **({"sha": sha} if sha else {})})
    w["merged"] = merged[-30:]
    s.dirty = True


def merged_here(s, task):
    """Its own work is in the base branch here (merged by anyone, without a PR): the newest commit
    its branch held that the base didn't (own_work()) is in the base now. A branch that only caught
    up with its base (SHEP-23: `git merge --ff-only main` mid-work, its tip the base's) has no work
    of its own, so it never counts as merged."""
    own = own_work(s, task)
    return bool(own) and git(s.loc["folder"], "merge-base", "--is-ancestor", own, task["base"])[0]


def own_work(s, task):
    """The newest commit of the task's own seen on its branch, or None: its tip whenever the branch
    holds commits its base branch doesn't, here or on its remote (kept on the task as "own"; the
    tick and ready look), else the last one kept."""
    main, br, base, start = s.loc["folder"], task.get("branch"), task.get("base"), task.get("start")
    if not (br and base and start):
        return None
    ok, tip, _ = git(main, "rev-parse", "--verify", "--quiet", f"refs/heads/{br}")
    tip = tip if ok and tip else task.get("head")
    if tip and tip != start and tip != task.get("own") and not any(
            git(main, "merge-base", "--is-ancestor", tip, ref)[0] for ref in (base, f"refs/remotes/{remote_of(main, br)}/{base}")):
        task["own"] = tip
        s.dirty = True
    return task.get("own")


def mid_turn(s, task):
    """Its agent, or a helper of its own, is mid-turn (working, or asking), or has an open question
    waiting to be rescued: its folder stays until the turn ends (SHEP-188, FAC-70). Even when the
    merge came from Herdr itself (even_if_busy)."""
    key = s.key(task)
    agents = live_agents(key)
    return bool(mid_turn_agents(agents + live_helpers(key), detail)) or any(open_question(task, k) for k in agents)


def finish(s, task, even_if_busy=False):
    """Done, its branch merged: removes the worktree and branch when nothing is left uncommitted
    there and no agent still works in it: none mid-turn (mid_turn()), and (unless the person merged
    from Herdr) none running at all. Else tidy_done() removes them once its agents ended."""
    from .simulators import release_task
    from .settings import accepted_word, remove_build_dir
    if task.get("auto_merge") and not task.get("accepted_by"):
        task["accepted_by"] = accepted_word(task)
    task["state"], task["done_at"] = "done", now()
    for k in ("attention", "merge_requested", "queue", "queued_at", "merge_queued", "kept_open", "review_followup"):
        task.pop(k, None)
    release_task(s, task)
    remove_build_dir(s, task)
    s.dirty = True
    if not task.get("worktree") or mid_turn(s, task) or (not even_if_busy and any(agent_running(k) for k in live_agents(s.key(task)))):
        return
    remove_worktree(s, task)


def remove_worktree(s, task):
    """A finished task's worktree removed when nothing is uncommitted there, then its branch. True
    once the worktree is gone."""
    wt, br, main = task.get("worktree"), task.get("branch"), s.loc["folder"]
    if os.path.isdir(wt):
        if os.path.realpath(wt) == os.path.realpath(main):
            return False
        ok, out, _ = git(wt, "status", "--porcelain", "--untracked-files=no")
        if not ok or out or not git(main, "worktree", "remove", "--force", wt)[0]:
            return False
    if br:
        git(main, "branch", "-D", br)
    return True


# A Done task's worktree left on disk (its agent was still open when it finished, SHEP-161): removed
# as finish() would have, once nothing runs in it; one that can't go yet (uncommitted work, a
# terminal there) is looked at again after TIDY_AGAIN_MS. Old leftovers go the same way on the next
# pass. A task made Done by hand with its work unmerged keeps its worktree (the person's to remove).
TIDY_AGAIN_MS = 3600000


def tidy_due(t):
    """A Done task, merged or with nothing to merge, whose worktree is still on disk and may be looked at now."""
    wt = t.get("worktree")
    return bool(t.get("state") == "done" and t.get("mode") == "worktree" and wt and (t.get("merged") or t.get("nothing_to_merge"))
                and now() - (t.get("worktree_kept") or 0) >= TIDY_AGAIN_MS and os.path.isdir(wt))


def tidy_done(s, t, report=None):
    """Removes a Done task's leftover worktree and branch (remove_worktree()) when no terminal is
    live in it (neither the task's own nor any other with its folder there) and its branch has
    nothing new since its work merged. True when removed."""
    if not tidy_due(t):
        return False
    wt, br, main = t["worktree"], t.get("branch"), s.loc["folder"]
    terms = hc_json("ls")
    real = os.path.realpath(wt)
    inside = lambda p: bool(p) and (os.path.realpath(p) + os.sep).startswith(real + os.sep)
    busy = terms is None or live_agents(s.key(t)) or live_helpers(s.key(t)) or any(
        k.get("state") == "live" and inside(k.get("cwd")) for k in terms.get("terminals") or [])
    ok, tip, _ = git(main, "rev-parse", "--verify", "--quiet", f"refs/heads/{br}") if br else (False, "", "")
    base = t.get("base") or "main"
    newer = ok and tip and tip != t.get("own") and not any(
        git(main, "merge-base", "--is-ancestor", tip, ref)[0] for ref in (base, f"refs/remotes/{remote_of(main, br)}/{base}"))
    if busy or newer or not remove_worktree(s, t):
        t["worktree_kept"] = now()
        s.dirty = True
        return False
    t.pop("worktree_kept", None)
    t["worktree_removed_at"] = now()
    s.dirty = True
    if report is not None:
        report.setdefault("worktrees_removed", []).append({"task": s.ref(t), "path": wt})
    return True
