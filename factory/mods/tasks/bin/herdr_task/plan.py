"""SDF-31: milestones and the plan file. The coordinator writes a plan (milestones, each with its
tasks); `plan apply` checks it as a whole and adds the tasks, `plan amend --reason` changes it
later and logs why. The engine adds each milestone's acceptance task and derives its state;
a split keeps the parent's place in the graph. docs/dev/factory/milestones.md."""

import copy
import json
import re

from .base import Fail, now
from .agents import live_agents, status_word
from .settings import SET_KEYS, set_task, worktrees_on

GATES = ("auto", "fable", "owner")
PLAN_MAX_LINES = 500  # a plan task's size estimate may not pass this (field notes 27, 42)
MID = re.compile(r"M\d+")
KEY = re.compile(r"[a-z][a-z0-9_-]*")
REF = re.compile(r"(?:[A-Za-z]+[0-9]*-)?[0-9]+")
TAG = re.compile(r"v\d+\.\d+\.\d+")
NOT_STARTED = ("todo", "scheduled", "queued")
M_KEYS = {"id", "title", "goal", "exit_test", "gate", "mandate", "ui", "release", "tasks"}
T_KEYS = {"key", "title", "prompt", "lines", "after", "kind", "set", "split_of", "retro"}


def load(path):
    try:
        with open(path) as f:
            plan = json.load(f)
    except OSError as e:
        raise Fail(f"Couldn't read {path}: {e.strerror}.")
    except ValueError as e:
        raise Fail(f"{path} isn't JSON: {e}.")
    if not isinstance(plan, dict) or set(plan) != {"milestones"} or not isinstance(plan["milestones"], list):
        raise Fail('A plan file is one JSON object: {"milestones": [...]}.')
    return plan


def milestones(s):
    return sorted(s.data.get("milestones") or [], key=lambda m: m["order"])


def state_of(s, m, first_open):
    """planned, active, validating or done: from its tasks' states, never stored."""
    tasks = [t for t in s.data["tasks"] if t.get("milestone") == m["id"]]
    acc = next((t for t in tasks if t.get("acceptance_of") == m["id"]), None)
    work = [t for t in tasks if t is not acc]
    if acc and acc["state"] == "done":
        return "done"
    known = {t["id"]: t for t in s.data["tasks"]}
    waits = [known[i] for i in (acc or {}).get("after") or [] if i in known] if acc else work
    if all(t["state"] == "done" for t in waits):
        return "validating"
    if m["id"] == first_open or any(t["state"] not in NOT_STARTED for t in tasks):
        return "active"
    return "planned"


def states(s):
    """Each milestone's state; the first one not done is active even before its tasks start."""
    done = {t["acceptance_of"] for t in s.data["tasks"] if t.get("acceptance_of") and t["state"] == "done"}
    first = next((m["id"] for m in milestones(s) if m["id"] not in done), None)
    return {m["id"]: state_of(s, m, first) for m in milestones(s)}


def agents_of(s, t):
    return live_agents(s.key(t))


def busy(s, t):
    """An agent of the task is mid-turn or waiting on a prompt (an idle one, as after a recap, ends when the
    task is split)."""
    return any(status_word(k) in ("working", "blocked") for k in agents_of(s, t))


def _resolve_ref(s, text):
    """A task of this folder by its ref ("SDF-4", "4"), or None."""
    m = re.fullmatch(r"(?:([A-Za-z]+[0-9]*)-)?([0-9]+)", text)
    if not m or (m.group(1) and m.group(1).upper() != s.prefix):
        return None
    return next((t for t in s.data["tasks"] if t["id"] == int(m.group(2))), None)


class Plan:
    """One apply or amend, worked out in memory: every problem first, then the store is changed."""

    def __init__(self, s, plan, amend):
        self.s, self.amend, self.problems, self.dry = s, amend, [], False
        self.by_key = {t["plan_key"]: t for t in s.data["tasks"] if t.get("plan_key")}
        self.old_ms = {m["id"]: m for m in s.data.get("milestones") or []}
        self.ms, self.tasks = [], []  # (milestone entry), (task entry, milestone id)
        self.check_shape(plan)
        if not self.problems:
            self.check_graph()

    def bad(self, text):
        self.problems.append(text)

    # ---- shape ----------------------------------------------------------------------------
    def check_shape(self, plan):
        seen_m, seen_k = set(), set()
        if not plan["milestones"]:
            self.bad("The plan has no milestones.")
        if not self.amend and self.old_ms:
            return self.bad("This folder has a plan already: change it with plan amend --reason.")
        for i, m in enumerate(plan["milestones"]):
            if not isinstance(m, dict):
                self.bad(f"Milestone {i + 1} isn't an object.")
                continue
            mid = m.get("id") if isinstance(m.get("id"), str) else f"#{i + 1}"
            if extra := set(m) - M_KEYS:
                self.bad(f"{mid}: unknown field(s) {', '.join(sorted(extra))} (a milestone's state is the engine's).")
            if not MID.fullmatch(mid):
                self.bad(f"{mid}: a milestone id is like M1.")
            elif mid in seen_m:
                self.bad(f"{mid} is in the plan twice.")
            seen_m.add(mid)
            for f in ("title", "goal", "exit_test", "mandate"):
                if not isinstance(m.get(f), str) or not m[f].strip():
                    self.bad(f"{mid}: {f} is missing.")
            if m.get("gate") not in GATES:
                self.bad(f"{mid}: gate is auto, fable or owner.")
            if "release" in m and not (isinstance(m["release"], str) and TAG.fullmatch(m["release"])):
                self.bad(f"{mid}: release is a tag like v1.2.0.")
            if not isinstance(m.get("ui", False), bool):
                self.bad(f"{mid}: ui is true or false.")
            old = self.old_ms.get(mid)
            if old and old["mandate"] != m.get("mandate"):
                self.bad(f"{mid}'s mandate is the owner's words as said and stays; wider scope is a new milestone.")
            self.ms.append(m)
            if not isinstance(m.get("tasks"), list):
                self.bad(f"{mid}: tasks is a list.")
                continue
            for j, t in enumerate(m["tasks"]):
                key = t.get("key") if isinstance(t, dict) else None
                if not isinstance(key, str) or not KEY.fullmatch(key):
                    self.bad(f"{mid} task {j + 1}: key is a short lowercase name like api-route.")
                    continue
                if key in seen_k:
                    self.bad(f"Task key {key} is in the plan twice.")
                seen_k.add(key)
                if extra := set(t) - T_KEYS:
                    self.bad(f"{key}: unknown field(s) {', '.join(sorted(extra))}.")
                self.tasks.append((t, mid))
                self.check_task(t, mid)

    def check_task(self, t, mid):
        key, old = t["key"], self.by_key.get(t["key"])
        if t.get("kind") not in (None, "design", "doc"):
            self.bad(f"{key}: kind is design or doc.")
        if not isinstance(t.get("after", []), list) or not all(isinstance(a, str) for a in t.get("after", [])):
            self.bad(f"{key}: after is a list of task keys or refs.")
        if not isinstance(t.get("split_of", ""), str) or (old and "split_of" in t):
            self.bad(f"{key}: split_of names the one task a new task replaces.")
        if not isinstance(t.get("set", {}), dict):
            self.bad(f"{key}: set is an object of --set keys.")
        else:
            for k, v in (t.get("set") or {}).items():
                field = SET_KEYS.get(str(k).strip().lower())
                if field in ("estimate_lines", "review_kind"):
                    self.bad(f"{key}: {'its size estimate is lines' if field == 'estimate_lines' else 'its kind is kind'}, not set.")
                    continue
                try:
                    set_task({}, k, str(v))
                except Fail as e:
                    self.bad(f"{key}: {e}")
        if not isinstance(t.get("retro", False), bool):
            self.bad(f"{key}: retro is true or false.")
        elif t.get("retro") and (old is None or old["state"] == "done"):
            self.bad(f"{key}: retro is for an open existing task, not a {'done' if old else 'new'} one.")
        if old and old["state"] not in NOT_STARTED:
            for f, have in (("title", old["title"]), ("prompt", old.get("plan_prompt")), ("lines", old.get("estimate_lines"))):
                if f in t and t[f] != have:
                    self.bad(f"{key} ({self.s.ref(old)}) has started: change its brief with herdr-task edit --amend, not the plan.")
                    break
            if old.get("milestone") != mid:
                self.bad(f"{key} ({self.s.ref(old)}) has started in {old.get('milestone')}; it can't move to {mid}.")
            if isinstance(t.get("after"), list):
                now_after = {(self.find(a) or {"id": a})["id"] for a in t["after"]}
                if now_after != set(old.get("plan_after") or []):
                    self.bad(f"{key} ({self.s.ref(old)}) has started: what it waits for can't change now.")
            return
        if old is None or "title" in t or "prompt" in t:
            for f in ("title", "prompt"):
                if not isinstance(t.get(f), str) or not t[f].strip():
                    self.bad(f"{key}: {f} is missing.")
        lines = t.get("lines", old.get("estimate_lines") if old else None)
        if not isinstance(lines, int) or isinstance(lines, bool) or lines <= 0:
            self.bad(f"{key}: lines (its size estimate in changed lines) is missing.")
        elif lines > PLAN_MAX_LINES:
            self.bad(f"{key}: {lines} lines is past the {PLAN_MAX_LINES}-line size limit; split it.")

    # ---- the graph ------------------------------------------------------------------------
    def check_graph(self):
        s, keys = self.s, {t["key"] for t, _ in self.tasks}
        listed = {m["id"] for m in self.ms}
        # Every not-done task of a milestone in this file is listed (drop one with herdr-task rm first).
        split_parents = {}
        for t, mid in self.tasks:
            if "split_of" in t:
                p = self.find(t["split_of"])
                if p is None:
                    self.bad(f"{t['key']}: split_of {t['split_of']} isn't a task of the plan.")
                elif p.get("milestone") != mid:
                    self.bad(f"{t['key']}: a split keeps the parent's place; {s.ref(p)} is in {p.get('milestone')}, not {mid}.")
                elif p.get("plan_key") in keys:
                    self.bad(f"{s.ref(p)} is split, so it can't stay in the plan as {p['plan_key']}.")
                elif p["state"] not in NOT_STARTED + ("running",) or p.get("acceptance_of") or (p["state"] == "running" and busy(s, p)):
                    self.bad(f"{s.ref(p)} can't be split now ({'its agent is at work' if p['state'] == 'running' else p['state']}).")
                else:
                    split_parents.setdefault(p["id"], []).append(t["key"])
        self.split_parents = split_parents
        for old in s.data["tasks"]:
            if (old.get("milestone") in listed and old.get("plan_key") and old["state"] != "done"
                    and old["plan_key"] not in keys and old["id"] not in split_parents):
                self.bad(f"{s.ref(old)} ({old['plan_key']}) is in {old['milestone']} but not in this file: list it, "
                         f"split it, or remove it with herdr-task rm first.")
        # Acceptance already started: no new work in that milestone, and its test and gate stay.
        for m in self.ms:
            acc = self.acceptance(m["id"])
            if acc and acc["state"] not in NOT_STARTED:
                old = self.old_ms[m["id"]]
                for f in ("exit_test", "gate", "release"):
                    if m.get(f) != old.get(f):
                        self.bad(f"{m['id']}'s acceptance ({s.ref(acc)}) has started: its {f} can't change now.")
                for t, mid in self.tasks:
                    old = self.by_key.get(t["key"])
                    if mid == m["id"] and (old is None or old.get("milestone") != mid):
                        self.bad(f"{m['id']}'s acceptance ({s.ref(acc)}) has started: put {t['key']} in a new milestone.")
        # Dangling refs, then loops over the plan's tasks and this folder's.
        edges = {}
        for t, mid in self.tasks:
            deps = []
            inherited = [str(i) for i in (self.find(t["split_of"]) or {}).get("plan_after") or []] if "split_of" in t else []
            for a in list(dict.fromkeys(inherited + t.get("after", self.old_after(t)))):
                o = None if a in keys else self.find(a)
                if a in keys:
                    deps.append(("k", a))
                elif o is not None and o["id"] in split_parents:
                    deps += [("k", k) for k in split_parents[o["id"]]]
                elif o is not None:
                    deps.append(("k", o["plan_key"]) if o.get("plan_key") in keys else ("i", o["id"]))
                else:
                    self.bad(f"{t['key']}: after {a} is neither a task key in this plan nor a task here.")
            edges[("k", t["key"])] = deps
        self.edges = edges
        self.check_loops(edges)  # an unknown id is left out of the edges, so the rest is still checked
        self.check_ui(edges)

    def old_after(self, t):
        """An existing task listed without "after": the after it was given by the plan before."""
        old = self.by_key.get(t["key"])
        return [str(i) for i in (old or {}).get("plan_after") or []] if old else []

    def find(self, text):
        if text in self.by_key:
            return self.by_key[text]
        return _resolve_ref(self.s, text) if REF.fullmatch(text) else None

    def acceptance(self, mid):
        return next((t for t in self.s.data["tasks"] if t.get("acceptance_of") == mid), None)

    def check_loops(self, edges):
        """No loop in the graph as it will be: the plan's own edges, this folder's tasks, and the edges the
        engine adds (a milestone's first tasks wait for the previous acceptance; an acceptance waits for
        its milestone's tasks and the previous acceptance)."""
        known = {t["id"]: t for t in self.s.data["tasks"]}
        ms_of = {t["key"]: mid for t, mid in self.tasks}
        order = [m["id"] for m in milestones(self.s)] + [m["id"] for m in self.ms if m["id"] not in self.old_ms]
        prev = dict(zip(order[1:], order))
        listed = set(ms_of)
        members = {mid: [("k", k) for k, m in ms_of.items() if m == mid] for mid in order}
        for t in self.s.data["tasks"]:
            if (t.get("milestone") in members and not t.get("acceptance_of") and t.get("plan_key") not in listed
                    and t["id"] not in self.split_parents):
                members[t["milestone"]].append(("i", t["id"]))

        def mid_of(n):
            return ms_of.get(n[1]) if n[0] == "k" else (known.get(n[1]) or {}).get("milestone")

        def nexts(n):
            if n[0] == "i" and (known.get(n[1]) or {}).get("plan_key") in listed:
                return nexts(("k", known[n[1]]["plan_key"]))  # one node per task, by key or by id
            if n[0] == "a":
                return members.get(n[1], []) + ([("a", prev[n[1]])] if n[1] in prev else [])
            if n[0] == "i" and (known.get(n[1]) or {}).get("acceptance_of"):
                return nexts(("a", known[n[1]]["acceptance_of"]))
            # A split parent an existing task waits for is replaced by its parts, as write() will do.
            deps = edges.get(n, []) if n[0] == "k" else [
                d for i in (known.get(n[1]) or {}).get("after") or []
                for d in ([("k", k) for k in self.split_parents[i]] if i in self.split_parents else [("i", i)])]
            if n[0] == "k" and ms_of[n[1]] in prev and not any(mid_of(d) == ms_of[n[1]] for d in deps):
                deps = deps + [("a", prev[ms_of[n[1]]])]
            return deps

        for start in edges:
            seen, todo = set(), list(nexts(start))
            while todo:
                n = todo.pop()
                if n[0] == "i" and (known.get(n[1]) or {}).get("plan_key") in listed:
                    n = ("k", known[n[1]]["plan_key"])
                if n == start:
                    self.bad(f"{start[1]} would end up waiting for itself (a loop; milestone order counts: "
                             f"a task can't wait for a later milestone's task).")
                    break
                if n not in seen:
                    seen.add(n)
                    todo.extend(nexts(n))

    def check_ui(self, edges):
        """A UI milestone starts with a design task: it has one, and its other tasks come after one."""
        for m in self.ms:
            if not m.get("ui"):
                continue
            mine = [t for t, mid in self.tasks if mid == m["id"]]
            design = {("k", t["key"]) for t in mine if self.kind(t) == "design"}
            if not design:
                self.bad(f"{m['id']} is a UI milestone: it needs a design task (kind design) first.")
                continue
            for t in mine:
                if ("k", t["key"]) in design:
                    continue
                seen, todo = set(), list(edges.get(("k", t["key"]), []))
                while todo and not (seen & design):
                    n = todo.pop()
                    if n not in seen:
                        seen.add(n)
                        todo.extend(edges.get(n, []))
                if not seen & design:
                    self.bad(f"{t['key']} in UI milestone {m['id']} must come after its design task.")

    def kind(self, t):
        old = self.by_key.get(t["key"])
        return t.get("kind", ((old or {}).get("set") or {}).get("review_kind"))

    # ---- changing the store ---------------------------------------------------------------
    def write(self, reason=None):
        s = self.s
        self.later = []
        self.replan_effects = []
        from .commands import set_base
        from .replan import has_replan_state, replan_task, require_replan_caller, select_retro_plan_targets
        if self.amend:
            require_replan_caller(s, action="amend the plan")
        had_new_tasks = any(t["key"] not in self.by_key for t, _ in self.tasks)
        log = {"at": now(), "action": "amend" if self.amend else "apply", "added": [], "changed": []}
        mode = "worktree" if worktrees_on(s) else "here"
        # Milestones: new ones go after the old in file order; old ones keep their order and mandate.
        top = max([m["order"] for m in self.old_ms.values()], default=0)
        for m in self.ms:
            rec = self.old_ms.get(m["id"])
            if rec is None:
                top += 1
                rec = {"id": m["id"], "order": top, "mandate": m["mandate"], "created": now()}
                s.data.setdefault("milestones", []).append(rec)
            rec.update({f: m[f] for f in ("title", "goal", "exit_test", "gate")}, ui=bool(m.get("ui")))
            rec.pop("release", None)
            if m.get("release"):
                rec["release"] = m["release"]
        # Tasks: new ones added, todo ones updated; then each one's after from keys to ids.
        for t, mid in self.tasks:
            old = self.by_key.get(t["key"])
            if old is None:
                old = s.new(t["title"].strip(), "", mode)
                old["plan_key"] = t["key"]
                if not self.dry:  # --check never touches git (a base only on the remote is fetched)
                    set_base(s, old, None, new=True)
                self.by_key[t["key"]] = old
                log["added"].append(s.ref(old))
            elif old["state"] in NOT_STARTED and any(f in t for f in ("title", "prompt", "lines", "kind", "set", "after")) \
                    or old.get("milestone") != mid:
                log["changed"].append(s.ref(old))
            if old["state"] in NOT_STARTED:
                if "title" in t:
                    old["title"] = t["title"].strip()
                if "prompt" in t:
                    old["plan_prompt"] = t["prompt"]
                if "lines" in t:
                    old["estimate_lines"] = t["lines"]
                if "kind" in t:
                    set_task(old, "review.kind", t["kind"] or "")
                for k, v in (t.get("set") or {}).items():
                    set_task(old, k, str(v))
            old["milestone"] = mid
            if "split_of" in t:
                old["split_of"] = s.ref(self.find(t["split_of"]))
        # A split: children take the parent's after, and whoever waited on the parent waits on them all.
        splits = {}
        for pid, kids in self.split_parents.items():
            parent = next(t for t in s.data["tasks"] if t["id"] == pid)
            ids = [self.by_key[k]["id"] for k in kids]
            for o in s.data["tasks"]:
                for f in ("after", "plan_after"):
                    if pid in (o.get(f) or []):
                        o[f] = list(dict.fromkeys(x for i in o[f] for x in (ids if i == pid else [i])))
            splits[s.ref(parent)] = [s.ref(self.by_key[k]) for k in kids]
            self.later.append(parent)  # its agent and simulator end only once the whole result passes
            s.data["tasks"].remove(parent)
        for t, mid in self.tasks:
            if "after" in t or "split_of" in t:
                ids = [self.by_key[n[1]]["id"] if n[0] == "k" else n[1] for n in self.edges[("k", t["key"])]]
                c = self.by_key[t["key"]]
                c["plan_after"] = list(dict.fromkeys(ids))
        log["added"] += self.link()
        for t in s.data["tasks"]:
            if t.get("plan_key") and t["state"] in NOT_STARTED and not t.get("acceptance_of"):
                t["prompt"] = brief(s, t)
        if self.amend:
            explicit_keys = {t["key"] for t, _ in self.tasks if t.get("retro")}
            targets = select_retro_plan_targets(
                s, self.tasks, self.by_key, reason,
                has_structural_changes=had_new_tasks or bool(self.split_parents),
            )
            replanned = []
            for target in targets:
                require_replan_caller(s, target, action="amend the plan or decide its recap")
                if target.get("plan_key") in explicit_keys or has_replan_state(target):
                    self.replan_effects.append(replan_task(
                        s, target, why=reason or "retro plan amend",
                        at=log["at"], dry=self.dry, defer_side_effects=True,
                    ))
                    ref = s.ref(target)
                    replanned.append(ref)
                    if ref not in log["changed"]:
                        log["changed"].append(ref)
            if replanned:
                log["replanned"] = replanned
        if splits:
            log["split"] = splits
        if reason:
            log["reason"] = reason
        s.data.setdefault("plan_log", []).append(log)
        s.dirty = True
        return log

    def finish(self):
        """After the written graph passed its last check: run deferred re-plan side effects, then end split parents."""
        from .commands import end_agent
        from .replan import cancel_queued_merge, clear_task_held_news, retire_reviewers
        from .simulators import release_task
        for eff in self.replan_effects:
            cancel_queued_merge(eff.store, eff.task, was_merge_queued=eff.was_merge_queued)
        for eff in self.replan_effects:
            retire_reviewers(eff.retiring)
            clear_task_held_news(eff.store, eff.task, eff.retiring, at=eff.at)
        for parent in self.later:
            for k in agents_of(self.s, parent) if parent["state"] == "running" else []:
                end_agent(self.s, parent, k["address"])
            release_task(self.s, parent)

    def link(self):
        """Each task's after: what the plan gave it, plus the previous milestone's acceptance for a
        milestone's first tasks; each milestone's acceptance task comes after all its own tasks."""
        s, mode = self.s, "worktree" if worktrees_on(self.s) else "here"
        from .commands import set_base
        prev, added = None, []
        for m in milestones(s):
            acc = self.acceptance(m["id"])
            if acc is None:
                acc = s.new(f"{m['id']} acceptance: {m['title']}", "", mode)
                acc.update(acceptance_of=m["id"], milestone=m["id"])
                if not self.dry:
                    set_base(s, acc, None, new=True)
                m["acceptance_task"] = acc["id"]
                added.append(s.ref(acc))
            work = [t for t in s.data["tasks"] if t.get("milestone") == m["id"] and t is not acc]
            ids = {t["id"] for t in work}
            for t in work:
                if t["state"] == "done" or not t.get("plan_key"):
                    continue
                own = [i for i in t.get("plan_after") or []]
                roots = prev and not any(i in ids for i in own)
                t["after"] = list(dict.fromkeys(own + ([prev["id"]] if roots else [])))
            if acc["state"] in NOT_STARTED:
                # It waits for the previous acceptance too, so an empty milestone keeps its place.
                acc["after"] = sorted(ids | ({prev["id"]} if prev else set()))
                acc["prompt"] = acceptance_brief(m)
                set_task(acc, "merge", "ask me first" if m.get("gate") == "owner" else "")
            prev = acc
        return added


def loop_in(s):
    """A loop among the open tasks as written, as refs (first one found), or None. The checks above give
    the plain reasons; this one checks the graph exactly as it will be saved, whatever produced it."""
    open_ = {t["id"]: t for t in s.data["tasks"] if t["state"] != "done"}
    state = {}
    for root in open_:
        if state.get(root):
            continue
        path, stack = [], [(root, iter(open_[root].get("after") or []))]
        state[root] = 1
        path.append(root)
        while stack:
            node, deps = stack[-1]
            nxt = next((d for d in deps if d in open_ and state.get(d) != 2), None)
            if nxt is None:
                state[node] = 2
                stack.pop()
                path.pop()
            elif state.get(nxt) == 1:
                return [s.ref(open_[i]) for i in path[path.index(nxt):]] + [s.ref(open_[nxt])]
            else:
                state[nxt] = 1
                path.append(nxt)
                stack.append((nxt, iter(open_[nxt].get("after") or [])))
    return None


def brief(s, t):
    """A plan task's brief: its milestone first, then the prompt the plan gave it."""
    m = next((m for m in s.data.get("milestones") or [] if m["id"] == t.get("milestone")), None)
    head = f"Milestone {m['id']}: {m['title']}. Its goal: {m['goal']}\n\n" if m else ""
    return head + (t.get("plan_prompt") or "")


def acceptance_brief(m):
    lines = [f"# {m['id']} acceptance: {m['title']}", "",
             f"Milestone {m['id']} is built: every task in it is merged. You did not write its code; check it with fresh eyes.",
             "", f"Goal: {m['goal']}", f"Exit test: {m['exit_test']}", f"The owner's words: {m['mandate']}", "",
             "Run the exit test on the merged base branch as the owner would, and prove the result as your Proof line says. "
             "Change no product code here: a failure goes in your ready summary with what failed and how to see it."]
    if m.get("gate") == "owner":
        lines.append("This milestone's gate is the owner's: they accept it.")
    if m.get("release"):
        lines += ["", f"Release {m['release']}: once the exit test passes, ask before you say ready, never after "
                      f"(an ask after the merge is lost when the task closes): "
                      f"`herdr-task ask \"Release {m['release']}?\" --kind release --option Release --option Hold --recommend Release`."]
    return "\n".join(lines) + "\n"


def show(s):
    st = states(s)
    out = []
    for m in milestones(s):
        tasks = [t for t in s.data["tasks"] if t.get("milestone") == m["id"]]
        out.append({**{k: m.get(k) for k in ("id", "title", "goal", "order", "exit_test", "gate", "mandate", "ui", "release")},
                    "state": st[m["id"]], "acceptance_task": s.ref({"id": m["acceptance_task"]}) if m.get("acceptance_task") else None,
                    "tasks": [{"ref": s.ref(t), "key": t.get("plan_key"), "title": t["title"], "state": t["state"],
                               "after": [s.ref({"id": i}) for i in t.get("after") or []],
                               **({"split_of": t["split_of"]} if t.get("split_of") else {})} for t in tasks]})
    log = s.data.get("plan_log") or []
    return {"milestones": out, "replans": sum(1 for x in log if x["action"] == "amend"), "log": log}


def text_of(v):
    if not v["milestones"]:
        return "No plan here yet: write a plan file and run herdr-task plan apply <file>."
    rows = []
    for m in v["milestones"]:
        rows.append(f"{m['id']} {m['title']} ({m['state']}, gate {m['gate']}): {m['goal']}")
        for t in m["tasks"]:
            after = f" after {', '.join(t['after'])}" if t["after"] else ""
            rows.append(f"  {t['ref']}  {t['state']:<9} {t['title']}{after}")
    rows.append(f"replans: {v['replans']}")
    return "\n".join(rows)


def cmd_plan(args):
    from .output import say
    from .store import open_store
    with open_store(args, write=args.plan != "show" and not getattr(args, "check", False)) as s:
        if args.plan == "show":
            v = show(s)
            return say(args, v, text_of(v))
        if args.plan == "amend" and not (args.reason or "").strip():
            raise Fail("plan amend needs --reason: why the plan changes, in a line.")
        p = Plan(s, load(args.file), args.plan == "amend")
        if p.problems:
            raise Fail("The plan isn't applied:\n- " + "\n- ".join(p.problems))
        saved = copy.deepcopy(s.data) if args.check else None
        p.dry = bool(args.check)
        log = p.write((args.reason or "").strip() or None)
        if loop := loop_in(s):
            raise Fail("The plan isn't applied: it would make a loop: " + " waits for ".join(loop) + ".")  # nothing saved
        if args.check:
            s.data.clear()
            s.data.update(saved)
            s.dirty = False
            return say(args, {"ok": True}, "The plan passes its checks; nothing was changed.")
        p.finish()
        v = show(s)
    added = f"added {', '.join(log['added'])}" if log["added"] else "added nothing"
    say(args, {**v, "applied": log}, f"Plan {log['action']}: {added}." + "".join(f" Split {p} into {', '.join(k)}." for p, k in (log.get("split") or {}).items()) + "\n" + text_of(v))
