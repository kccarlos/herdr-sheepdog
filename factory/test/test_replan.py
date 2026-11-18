"""SDF-74: one table for what a re-plan resets, keeps, and archives (`replan.py` and `docs/dev/factory/replan.md`),
with 1-to-1 doc parity, one test per row, caller authorization, target selection, and `--check` invariants."""

import home_guard  # noqa: F401  (first: every test on a throwaway home, never the real one)
import argparse
import contextlib
import copy
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
REPO = os.path.dirname(ROOT)
sys.path.insert(0, os.path.join(ROOT, "mods", "tasks", "bin"))
sys.path.insert(0, os.path.join(ROOT, "test"))


def plan_task(key, lines=200, **kw):
    return {"key": key, "title": key.replace("-", " ").title(), "prompt": f"Build {key}.", "lines": lines, **kw}


def milestone(mid, tasks, **kw):
    return {
        "id": mid, "title": f"Milestone {mid}", "goal": f"goal of {mid}",
        "exit_test": f"exit test of {mid}", "gate": "auto",
        "mandate": f"owner mandate for {mid}", "tasks": tasks, **kw,
    }


def parse_doc_table(md_path):
    with open(md_path, encoding="utf-8") as f:
        lines = f.read().splitlines()
    in_section = False
    rows = []
    for line in lines:
        if line.startswith("## "):
            in_section = line.strip() == "## State table"
            continue
        if not in_section or not line.strip().startswith("|"):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) < 4 or cells[0] == "Row" or set(cells[0]) <= {"-"}:
            continue
        rows.append((cells[0].strip("`"), cells[1], cells[2].strip("`"), cells[3]))
    return rows


class ReplanTableTests(unittest.TestCase):
    def setUp(self):
        import herdr_task.agents as agents
        import herdr_task.base as base
        import herdr_task.budget as budget
        import herdr_task.cli as cli
        import herdr_task.commands as commands
        import herdr_task.drift as drift
        import herdr_task.github as github
        import herdr_task.launch as launch
        import herdr_task.news as news
        import herdr_task.plan as plan
        import herdr_task.replan as replan
        import herdr_task.settings as settings
        import herdr_task.store as store
        import herdr_task.tick as tick

        self.agents = agents
        self.base = base
        self.budget = budget
        self.cli = cli
        self.commands = commands
        self.drift = drift
        self.github = github
        self.launch = launch
        self.news = news
        self.plan = plan
        self.replan = replan
        self.settings = settings
        self.store_mod = store
        self.tick = tick

        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="replan-test-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.store_dir = os.path.join(self.tmp, "tasks")
        self.repo = os.path.join(self.tmp, "repo")
        self.wt = os.path.join(self.tmp, "wt")
        os.makedirs(self.store_dir)

        subprocess.run(["git", "init", "-q", "-b", "main", self.repo], check=True)
        for k, v in (("user.name", "T"), ("user.email", "t@x"), ("commit.gpgsign", "false")):
            subprocess.run(["git", "-C", self.repo, "config", k, v], check=True)
        with open(os.path.join(self.repo, "app.py"), "w") as f:
            f.write("x = 1\n")
        subprocess.run(["git", "-C", self.repo, "add", "app.py"], check=True)
        subprocess.run(["git", "-C", self.repo, "commit", "-q", "-m", "init"], check=True)
        self.base_sha = self.git(self.repo, "rev-parse", "HEAD")
        subprocess.run(["git", "-C", self.repo, "worktree", "add", "-q", "-b", "task/1-api", self.wt, "main"], check=True)
        self.head1 = self.commit_file("app.py", "x = 2\n", "attempt 1")

        self.settings_file = os.path.join(self.tmp, "settings.json")
        self.write_settings({"tasks": {"review": True, "review_rounds": 3, "merge": "ask", "auto_accept": True, "proof": False}})
        self.held_file = os.path.join(self.store_dir, ".held.json")
        self.news_file = os.path.join(self.store_dir, ".news.json")

        self.hc_terminals = {}
        self.live_by_key = {}
        self.ended_reviewers = []
        self.ended_agents = []
        self.sent = []
        self.gh_calls = []
        self.queue_state = {"id": "PR_node_1", "state": "OPEN", "in": False, "auto": False}

        def fake_hc_json(*argv):
            if len(argv) >= 2 and argv[0] == "get":
                val = self.hc_terminals.get(argv[1])
                if isinstance(val, Exception):
                    raise val
                return val
            return {}

        def fake_gh(*argv):
            self.gh_calls.append(argv)
            return True, "{}", ""

        patches = [
            mock.patch.object(base, "STORE", self.store_dir),
            mock.patch.object(store, "STORE", self.store_dir),
            mock.patch.object(commands, "STORE", self.store_dir),
            mock.patch.object(settings, "SETTINGS", self.settings_file),
            mock.patch.object(news, "HELD", self.held_file),
            mock.patch.object(news, "NEWS", self.news_file),
            mock.patch.object(base, "hc_json", fake_hc_json),
            mock.patch.object(commands, "hc_json", fake_hc_json),
            mock.patch.object(launch, "hc_json", fake_hc_json),
            mock.patch.object(agents, "hc_json", fake_hc_json),
            mock.patch.object(news, "hc_json", fake_hc_json),
            mock.patch.object(agents, "_live", lambda key=None: list(self.live_by_key.get(key, []))),
            mock.patch.object(commands, "end_reviewer", lambda addr: self.ended_reviewers.append(addr)),
            mock.patch.object(commands, "end_agent", lambda s, t, addr: self.ended_agents.append(addr)),
            mock.patch.object(news, "send_or_hold", lambda addr, msg, **kw: (self.sent.append((addr, msg)), True)[1]),
            mock.patch.object(github, "github", lambda folder: "acme/demo"),
            mock.patch.object(github, "gh", fake_gh),
            mock.patch.object(github, "queue_entry", lambda t: copy.deepcopy(self.queue_state)),
            mock.patch.object(commands, "branch_needs_push", lambda s, t: False),
        ]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)

        with self.open(True) as s:
            s.data["prefix"] = "TST"
            s.dirty = True
        rc, out = self.run_cli("plan", "apply", plan={"milestones": [
            milestone("M1", [plan_task("api"), plan_task("api-v2", after=["api"])]),
        ]})
        self.assertEqual(rc, 0, out)
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t.update(
                state="running", mode="worktree", worktree=self.wt, branch="task/1-api",
                base="main", start=self.base_sha, head=self.head1, started=1000, review_gate=True,
                agents=[{"address": "w1", "agent": "claude", "argv": ["claude"], "at": 1000}],
            )
            s.dirty = True

    def write_settings(self, data):
        with open(self.settings_file, "w") as f:
            json.dump({"version": 1, "everywhere": data}, f)

    def git(self, cwd, *args):
        return subprocess.run(["git", "-C", cwd, *args], check=True, capture_output=True, text=True).stdout.strip()

    def commit_file(self, rel, content, msg):
        full = os.path.join(self.wt, rel)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w") as f:
            f.write(content)
        subprocess.run(["git", "-C", self.wt, "add", rel], check=True)
        subprocess.run(["git", "-C", self.wt, "commit", "-q", "-m", msg], check=True)
        return self.git(self.wt, "rev-parse", "HEAD")

    def open(self, write=False):
        return self.store_mod.Store(self.store_mod.locate(self.repo), write=write)

    def find_key(self, s, key):
        return next(t for t in s.data["tasks"] if t.get("plan_key") == key)

    def run_cli(self, *argv, plan=None):
        if plan is not None:
            p = os.path.join(self.tmp, "plan.json")
            with open(p, "w") as f:
                json.dump(plan, f)
            argv = [argv[0], argv[1], p, *argv[2:]]
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(sys, "argv", ["herdr-task", *argv, "--folder", self.repo, "--json"]), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = self.cli.main()
        raw = out.getvalue().strip()
        return rc, (json.loads(raw) if raw else None)

    def run_text(self, *argv):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(sys, "argv", ["herdr-task", *argv, "--folder", self.repo]), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = self.cli.main()
        return rc, out.getvalue(), err.getvalue()

    def current_plan(self, **api_extra):
        return {"milestones": [
            milestone("M1", [{"key": "api", **api_extra}, plan_task("api-v2", after=["api"])]),
        ]}

    # ---- parity check ---------------------------------------------------------------------------

    def test_table_matches_doc_and_every_row_has_a_test(self):
        doc_path = os.path.join(REPO, "docs", "dev", "factory", "replan.md")
        doc_rows = parse_doc_table(doc_path)
        self.assertGreaterEqual(len(doc_rows), 11)
        self.assertEqual(
            [(r.key, r.disposition) for r in self.replan.REPLAN_TABLE],
            [(k, disp) for k, _, disp, _ in doc_rows],
        )
        for row in self.replan.REPLAN_TABLE:
            self.assertIn(row.disposition, self.replan.DISPOSITIONS)
            self.assertTrue(row.why.strip())
            self.assertTrue(
                hasattr(self, f"test_row_{row.key}"),
                f"Missing dedicated row test method test_row_{row.key}",
            )

    # ---- one test per row in REPLAN_TABLE -------------------------------------------------------

    def test_row_live_reviewers(self):
        self.write_settings({"tasks": {"review": True, "review_rounds": 3, "review_min": 1, "merge": "ask"}})
        with self.open(True) as s:
            t = self.find_key(s, "api")
            key = s.key(t)
            t["review_runs"] = [{"round": 1, "head": self.head1, "address": "rv-gate", "cli": "codex", "model": None, "at": 1100, "majors_required": True}]
            t["recap"] = {"at": 1200, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required: 3 failed CI runs"}
            s.dirty = True
        self.hc_terminals["rv-gate"] = {"address": "rv-gate", "state": "live", "foreground": {"name": "codex"}, "status": {"state": "working"}}
        self.hc_terminals["rv-sub"] = {"address": "rv-sub", "state": "live", "labels": {"sheepdog.role": "reviewer"}, "foreground": {"name": "codex"}}
        self.live_by_key[key] = [
            (self.hc_terminals["rv-gate"], "t"),
            (self.hc_terminals["rv-sub"], "t"),
        ]

        rc, _ = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "start over")
        self.assertEqual(rc, 0)
        self.assertEqual(self.ended_reviewers, ["rv-gate", "rv-sub"])
        with self.open() as s:
            t = self.find_key(s, "api")
            self.assertEqual(t.get("retiring_reviewers"), ["rv-gate", "rv-sub"])

        # While a retiring reviewer is still live (or hc get fails transiently), no second reviewer starts.
        head2 = self.commit_file("app.py", "x = 3\n", "attempt 2")
        launched = []
        with mock.patch.object(self.commands, "launch", lambda *a, **kw: (launched.append(a), {"address": "rv-new"})[1]):
            with self.open(True) as s:
                t = self.find_key(s, "api")
                t["head"] = head2
                msg = self.commands.gate_step(s, t)
                self.assertIn("still exiting", msg)
                self.assertEqual(launched, [])
            # Transient hc failure fails closed (keeps hold).
            self.hc_terminals["rv-gate"] = OSError("socket busy")
            self.hc_terminals["rv-sub"] = {"address": "rv-sub", "state": "finished", "exit_code": 0}
            with self.open(True) as s:
                t = self.find_key(s, "api")
                msg = self.commands.gate_step(s, t)
                self.assertIn("still exiting", msg)
                self.assertEqual(t.get("retiring_reviewers"), ["rv-gate"])
                self.assertEqual(launched, [])
            # Once rv-gate exits, gate_step clears retiring_reviewers and starts round 1 cleanly.
            self.hc_terminals["rv-gate"] = {"address": "rv-gate", "state": "finished", "exit_code": 0}
            with self.open(True) as s:
                t = self.find_key(s, "api")
                msg = self.commands.gate_step(s, t)
                self.assertIn("started review round 1", msg)
                self.assertNotIn("retiring_reviewers", t)
                self.assertEqual(len(launched), 1)

    def test_row_helper_terminals(self):
        with self.open(True) as s:
            t = self.find_key(s, "api")
            key = s.key(t)
            t["agents"] = [{"address": "w1", "agent": "claude", "argv": ["claude"], "at": 1000}]
            t["reviewers"] = [{"address": "rv-old", "agent": "codex", "argv": ["codex"], "at": 1100, "role": "reviewer"}]
            t["recap"] = {"at": 1200, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required"}
            s.dirty = True
        self.live_by_key[key] = [
            ({"address": "w1", "state": "live", "labels": {"herdr.role": "worker"}, "foreground": {"name": "claude"}}, "t"),
            ({"address": "hlp-1", "state": "live", "labels": {"herdr.role": "helper"}, "foreground": {"name": "claude"}}, "t"),
            ({"address": "rv-live", "state": "live", "labels": {"herdr.role": "reviewer"}, "foreground": {"name": "codex"}}, "t"),
        ]
        rc, _ = self.run_cli("plan", "amend", "--reason", "retro: new approach for api", plan=self.current_plan(retro=True))
        self.assertEqual(rc, 0)
        self.assertEqual(self.ended_reviewers, ["rv-live"])
        self.assertEqual(self.ended_agents, [])
        with self.open() as s:
            t = self.find_key(s, "api")
            self.assertEqual([a["address"] for a in t["agents"]], ["w1"])
            self.assertEqual([a["address"] for a in t["reviewers"]], ["rv-old"])
            self.assertEqual(t.get("retiring_reviewers"), ["rv-live"])

    def test_row_review_rounds(self):
        from contract_gate_evidence import validate_review, validate_review_run

        # Worker-reviewed flow: exhaust all 3 rounds + review-more, then re-plan and record round 1 again.
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["reviews"] = [
                {"round": 1, "result": "report", "verdict": "changes", "majors": 1, "must_fix": None, "report": "r1", "text": "r1", "head": self.head1, "at": 1100, "by": "w1", "cli": "codex", "model": "gpt"},
                {"round": 2, "result": "report", "verdict": "changes", "majors": 0, "must_fix": None, "report": "r2", "text": "r2", "head": self.head1, "at": 1200, "by": "w1", "cli": "codex", "model": "gpt"},
                {"round": 3, "result": "report", "verdict": "changes", "majors": 1, "must_fix": None, "report": "r3", "text": "r3", "head": self.head1, "at": 1300, "by": "w1", "cli": "codex", "model": "gpt"},
            ]
            t["review_runs"] = [
                {"round": 1, "head": self.head1, "address": "rv1", "cli": "codex", "model": None, "at": 1100, "majors_required": True},
            ]
            t["review_failures"] = [
                {"round": 1, "head": self.head1, "address": f"f{i}", "cli": cli_name, "why": "exited (1)", "at": 1050 + i, "tail": "old crash"}
                for i, cli_name in enumerate(("codex", "agy", "claude"))
            ]
            t["review"] = {"round": 3}
            t["review_out"] = 1350
            t["review_wait"] = {"address": "rv1", "nudged_at": 1310}
            t["review_quota_wait"] = {"head": self.head1, "until": 9999999999999, "text": "All reviewers are out of quota"}
            t["merge_only_runs"] = ["rv1"]
            t["attention"] = f"{self.agents.REVIEWER_FAILED} codex (exited (1))"
            t["attention_at"] = 1360
            t["recap"] = {"at": 1400, "reasons": ["3 failed CI runs"], "round": 3, "title": "Recap required: 3 failed CI runs"}
            s.dirty = True
        self.hc_terminals["rv1"] = {"address": "rv1", "state": "finished", "exit_code": 0}

        rc, _ = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "fresh design")
        self.assertEqual(rc, 0)

        rc, shown, _ = self.run_text("show", "TST-1")
        self.assertEqual(rc, 0)
        self.assertIn("review rounds: 0 of 3 used (reset; 3 before re-plan)", shown)
        self.assertNotIn("old crash", shown)

        rc, show_json = self.run_cli("show", "TST-1")
        self.assertEqual(rc, 0)
        self.assertEqual(show_json["review_rounds"]["used"], 0)
        self.assertEqual(show_json["review_rounds"]["before_replan"], 3)
        self.assertTrue(show_json["review_rounds"]["reset_at"])
        for k in ("review", "review_out", "review_wait", "review_quota_wait", "merge_only_runs", "attention"):
            self.assertNotIn(k, show_json)
        self.assertTrue(all(r.get("before_replan") is True for r in show_json["reviews"]))
        self.assertTrue(all(r.get("before_replan") is True for r in show_json["review_runs"]))
        self.assertTrue(all(f.get("before_replan") is True for f in show_json["review_failures"]))

        # New commit can record round 1 via `reviewed`, and old rounds remain marked before_replan.
        head2 = self.commit_file("app.py", "x = 10\n", "new approach")
        rc, rev_out = self.run_cli("reviewed", "TST-1", "--verdict", "pass", "--majors", "0", "--report", "clean")
        self.assertEqual(rc, 0, rev_out)
        self.assertEqual(rev_out["round"], 1)
        with self.open() as s:
            t = self.find_key(s, "api")
            self.assertEqual(len(t["reviews"]), 4)
            self.assertEqual([r.get("before_replan", False) for r in t["reviews"]], [True, True, True, False])
            self.assertEqual(t["reviews"][-1]["round"], 1)
            self.assertEqual(t["reviews"][-1]["head"], head2)

        # Gate contract validator accepts archived + post-replan round 1 records, and old failures don't block round 1.
        self.write_settings({"tasks": {"review": True, "review_rounds": 3, "review_min": 1, "merge": "ask"}})
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["reviews"] = [
                {"round": 1, "head": self.head1, "by": "rv1", "started_by": "herdr-task", "verdict": "changes", "must_fix": 1, "majors": 1, "report": "old", "cli": "codex", "model": None, "at": 1150, "before_replan": True},
            ]
            t["retiring_reviewers"] = []
            t.pop("retiring_reviewers", None)
            t["head"] = head2
            s.dirty = True
            # Archived review_runs alone do not trigger gate_reads to launch a reviewer before ready.
            self.assertEqual(self.replan.active_review_runs(t), [])
            launched = []
            with mock.patch.object(self.commands, "launch", lambda *a, **kw: (launched.append(kw), {"address": "rv-fresh"})[1]):
                self.commands.gate_reads(s, t)
                self.assertEqual(launched, [])
                msg = self.commands.gate_step(s, t)
                self.assertIn("started review round 1", msg)
                self.assertEqual(len(launched), 1)
                self.commands.record_verdict(s, t, t["review_runs"][-1], True, 0, "lgtm", 0)
            for r in t["review_runs"]:
                validate_review_run(r)
            for r in t["reviews"]:
                validate_review(r)
            # agents.review_running / starting never mistake pre-replan round 1 for an unfinished new round 1.
            self.assertFalse(self.agents.review_running(t))

    def test_row_fable_runs(self):
        self.write_settings({"tasks": {"review": True, "review_rounds": 3, "review_min": 1, "merge": "checks", "auto_accept": True, "proof": False}})
        fable_model = self.settings.FABLE["model"]
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["set"] = {"review_kind": "design"}
            t["review_runs"] = [
                {"round": 1, "head": self.head1, "address": "fb1", "cli": "claude", "model": fable_model, "at": 1100, "majors_required": True},
                {"round": 2, "head": self.head1, "address": "fb2", "cli": "claude", "model": fable_model, "at": 1200, "majors_required": True},
            ]
            t["reviews"] = [
                {"round": 1, "head": self.head1, "by": "fb1", "started_by": "herdr-task", "verdict": "changes", "must_fix": 1, "majors": 1, "report": "f1", "cli": "claude", "model": fable_model, "at": 1150},
                {"round": 2, "head": self.head1, "by": "fb2", "started_by": "herdr-task", "verdict": "changes", "must_fix": 1, "majors": 1, "report": "f2", "cli": "claude", "model": fable_model, "at": 1250},
            ]
            t["recap"] = {"at": 1300, "reasons": ["3 failed CI runs"], "round": 2, "title": "Recap required"}
            self.assertEqual(self.settings.fable_said(t, "changes"), 2)
            self.assertFalse(self.settings.fable_due(s, t))
            s.dirty = True

        rc, _ = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "rethink UX")
        self.assertEqual(rc, 0)
        with self.open() as s:
            t = self.find_key(s, "api")
            self.assertEqual(self.settings.fable_runs(t), set())
            self.assertEqual(self.settings.fable_said(t, "changes"), 0)
            self.assertTrue(self.settings.fable_due(s, t))
            self.assertEqual(self.commands.gate_need(s, t), 2)

    def test_row_review_more(self):
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["review_more_granted"] = True
            t["review_rounds_added"] = 3
            t["review_auto_grants"] = [{"at": 1100, "head": self.head1, "after_round": 1}, {"at": 1200, "head": self.head1, "after_round": 2}]
            t["reviews"] = [
                {"round": i, "result": "report", "verdict": "changes", "majors": 0, "report": "nit", "text": "nit", "head": self.head1, "at": 1000 + i}
                for i in range(1, 7)
            ]
            t["recap"] = {"at": 1700, "reasons": ["3 failed CI runs"], "round": 6, "title": "Recap required"}
            s.dirty = True

        rc, _ = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "fresh budget")
        self.assertEqual(rc, 0)
        with self.open(True) as s:
            t = self.find_key(s, "api")
            for k in ("review_more_granted", "review_rounds_added", "review_auto_grants"):
                self.assertNotIn(k, t)
            self.assertEqual(self.settings.review_limits(s, t), (True, 3))
            # Use all 3 base rounds on the new approach, then confirm review-more is available again.
            at0 = t["recaps"][-1]["at"]
            for i in range(1, 4):
                t["reviews"].append({"round": i, "result": "report", "verdict": "changes", "majors": 0, "report": "nit", "text": "nit", "head": self.head1, "at": at0 + i})
            s.dirty = True
        rc, more = self.run_cli("review-more", "TST-1")
        self.assertEqual(rc, 0, more)
        self.assertEqual(more["rounds"], 4)

    def test_row_consecutive_majors(self):
        # 1. `recap --decide continue` preserves a consecutive-majors streak across the recap.
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["reviews"] = [
                {"round": 1, "result": "report", "verdict": "pass", "majors": 0, "report": "ok", "head": self.head1, "at": 1100},
                {"round": 2, "result": "report", "verdict": "changes", "majors": 1, "report": "m1", "head": self.head1, "at": 1200},
            ]
            t["recap"] = {"at": 1250, "reasons": ["3 failed CI runs"], "round": 2, "title": "Recap required"}
            s.dirty = True
        rc, _ = self.run_cli("recap", "TST-1", "--decide", "continue", "--why", "fix flaky test")
        self.assertEqual(rc, 0)
        with self.open(True) as s:
            t = self.find_key(s, "api")
            at_cont = t["recaps"][-1]["at"]
            t["reviews"].append({"round": 3, "result": "report", "verdict": "changes", "majors": 1, "report": "m2", "head": self.head1, "at": at_cont + 10})
            self.assertTrue(self.budget.review_nonconverging(t))
            self.assertTrue(self.budget.budget_pass(s, t))
            self.assertIn(self.budget.NON_CONVERGENCE, t["recap"]["reasons"])
            s.dirty = True

        # 2. `recap --decide new` resets the streak; rounds 1 and 2 of the new approach don't fire, round 3 does.
        rc, _ = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "new architecture")
        self.assertEqual(rc, 0)
        with self.open(True) as s:
            t = self.find_key(s, "api")
            at_new = t["recaps"][-1]["at"]
            self.assertFalse(self.budget.review_nonconverging(t))
            t["reviews"].append({"round": 1, "result": "report", "verdict": "changes", "majors": 1, "report": "n1", "head": self.head1, "at": at_new + 1})
            self.assertFalse(self.budget.review_nonconverging(t))
            t["reviews"].append({"round": 2, "result": "report", "verdict": "changes", "majors": 1, "report": "n2", "head": self.head1, "at": at_new + 2})
            self.assertFalse(self.budget.review_nonconverging(t))
            t["reviews"].append({"round": 3, "result": "report", "verdict": "changes", "majors": 1, "report": "n3", "head": self.head1, "at": at_new + 3})
            self.assertTrue(self.budget.review_nonconverging(t))
            # Legacy `"new"` recap without `before_replan` also bounds `all_rounds`.
            for r in t["reviews"]:
                r.pop("before_replan", None)
            t["recaps"].append({"decision": "new", "why": "legacy", "at": at_new + 100})
            t["reviews"].append({"round": 1, "result": "report", "verdict": "changes", "majors": 1, "report": "l1", "head": self.head1, "at": at_new + 101})
            self.assertFalse(self.budget.review_nonconverging(t))

    def test_row_open_recap(self):
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["recap"] = {"at": 1200, "reasons": ["over twice its time estimate"], "round": 1, "title": "Recap required: over twice its time estimate"}
            t["attention"] = "Recap required: over twice its time estimate"
            t["attention_at"] = 1200
            s.dirty = True
        # Blocked before retro plan amend:
        rc, bad = self.run_cli("reviewed", "TST-1", "--verdict", "pass", "--majors", "0", "--report", "ok")
        self.assertEqual(rc, 1)
        self.assertIn("RECAP.md", bad["error"])

        rc, _ = self.run_cli("plan", "amend", "--reason", "retro: simplify api", plan=self.current_plan())
        self.assertEqual(rc, 0)
        with self.open() as s:
            t = self.find_key(s, "api")
            self.assertNotIn("recap", t)
            self.assertNotIn("attention", t)
        rc, ok = self.run_cli("reviewed", "TST-1", "--verdict", "pass", "--majors", "0", "--report", "ok")
        self.assertEqual(rc, 0, ok)

    def test_row_recaps(self):
        # Retro plan amend without an open recap still appends decision="new" to recaps, superseding continue --small.
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["estimate_time"] = 60_000
            t["started"] = 1000
            t["tokens"] = {"/tmp/.claude/x.jsonl": {"off": 10, "sum": 5000}}
            t["ci_runs"] = [{"sha": self.head1, "run": "1", "failed": True, "at": 1100}, {"sha": self.head1, "run": "2", "failed": True, "at": 1200}]
            t["recaps"] = [
                {"decision": "continue", "why": "first", "at": 1300},
                {"decision": "continue", "small": True, "max_rounds": 1, "max_lines": 5, "why": "nit", "at": 1400},
            ]
            s.dirty = True

        rc, _ = self.run_cli("plan", "amend", "--reason", "retro: replace api implementation", plan=self.current_plan(retro=True))
        self.assertEqual(rc, 0)
        with self.open() as s:
            t = self.find_key(s, "api")
            self.assertEqual([d["decision"] for d in t["recaps"]], ["continue", "continue", "new"])
            self.assertFalse(t["recaps"][-1].get("small", False))
            self.assertEqual(self.budget.ci_since(t), [])
            self.assertEqual(self.budget.size_triggers(t), [])
            at_new = t["recaps"][-1]["at"]
            t["reviews"] = [{"round": 1, "result": "report", "verdict": "changes", "majors": 0, "head": self.head1, "at": at_new + 1}]
            self.assertNotIn("small continue", " ".join(self.budget.triggers(s, t)))

        # And after [continue, continue, new], a subsequent open recap cannot choose `continue` again.
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["recaps"] = [{"decision": "continue", "why": "first", "at": 1300}, {"decision": "new", "why": "replan", "at": 2000}]
            t["recap"] = {"at": 2500, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required again: change direction"}
            s.dirty = True
        for extra in ([], ["--small"]):
            rc, bad = self.run_cli("recap", "TST-1", "--decide", "continue", "--why", "more", *extra)
            self.assertEqual(rc, 1)

    def test_row_verdict_told(self):
        self.write_settings({"tasks": {"review": True, "review_rounds": 3, "review_min": 2, "merge": "ask"}})
        self.live_by_key["repo#1"] = []
        with self.open(True) as s:
            t = self.find_key(s, "api")
            key = s.key(t)
            t["verdict_told"] = [1, 2]
            t["recap"] = {"at": 1200, "reasons": ["3 failed CI runs"], "round": 2, "title": "Recap required"}
            s.dirty = True
        worker = {"address": "w1", "state": "live", "labels": {"herdr.role": "worker"}, "foreground": {"name": "claude"}, "status": {"state": "idle"}}
        self.live_by_key[key] = [(worker, False)]
        self.hc_terminals["w1"] = worker

        rc, _ = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "fresh start")
        self.assertEqual(rc, 0)
        with self.open(True) as s:
            t = self.find_key(s, "api")
            self.assertNotIn("verdict_told", t)
            t.setdefault("review_runs", []).append({"round": 1, "head": self.head1, "address": "rv-new", "cli": "codex", "model": None, "at": t["recaps"][-1]["at"] + 5, "majors_required": True})
            self.sent.clear()
            with mock.patch.object(self.commands, "gate_step", lambda s2, t2: "round 2 started"):
                self.commands.record_verdict(s, t, t["review_runs"][-1], True, 0, "ok", 0)
            self.assertEqual(len(self.sent), 1)
            self.assertIn("review round 1 of TST-1 passed", self.sent[0][1])

    def test_row_merge_queued(self):
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["pr"] = {"number": 42, "url": "https://example.invalid/pr/42", "repo": "acme/demo"}
            t["merge_queued"] = 1500
            t["requeued"] = 1
            t["attention"] = f"{self.tick.QUEUE_REMOVED} check failed"
            t["recap"] = {"at": 1600, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required"}
            s.dirty = True

        # 1. If PR already merged on GitHub, re-plan is refused and task state stays intact.
        self.queue_state = {"id": "PR_node_42", "state": "MERGED", "in": False, "auto": False}
        rc, bad = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "too late")
        self.assertEqual(rc, 1)
        self.assertIn("already merged", bad["error"])
        with self.open() as s:
            self.assertEqual(self.find_key(s, "api").get("merge_queued"), 1500)

        # 2. If dequeue fails and PR stays in merge queue, plan amend aborts without saving or retiring reviewers.
        self.queue_state = {"id": "PR_node_42", "state": "OPEN", "in": True, "auto": True}
        with mock.patch.object(self.github, "gh", lambda *a: (False, "", "graphql boom")):
            rc, bad = self.run_cli("plan", "amend", "--reason", "retro: redo api", plan=self.current_plan(retro=True))
        self.assertEqual(rc, 1)
        self.assertIn("merge queue", bad["error"])
        with self.open() as s:
            t = self.find_key(s, "api")
            self.assertEqual(t.get("merge_queued"), 1500)
            self.assertIn("recap", t)

        # 3. When GitHub dequeues and disables auto-merge, merge_queued and requeued clear.
        self.gh_calls.clear()
        rc, ok = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "dequeue and redo")
        self.assertEqual(rc, 0, ok)
        self.assertTrue(any("dequeuePullRequest" in " ".join(c) for c in self.gh_calls))
        self.assertTrue(any("--disable-auto" in c for c in self.gh_calls))
        with self.open() as s:
            t = self.find_key(s, "api")
            self.assertNotIn("merge_queued", t)
            self.assertNotIn("requeued", t)
            self.assertNotIn("attention", t)

    def test_row_back_auto(self):
        with self.open(True) as s:
            t = self.find_key(s, "api")
            key = s.key(t)
            t["state"] = "running"
            t["back"] = {"at": 1000, "why": "its agent is at work again", "auto": True}
            t["settled_at"] = 1000
            t["auto_merge"] = "checks"
            t["pr"] = {"number": 7, "url": "https://example.invalid/pr/7", "repo": "acme/demo"}
            s.dirty = True
        worker = {"address": "w1", "state": "live", "labels": {"herdr.role": "worker"}, "foreground": {"name": "claude"}, "status": {"state": "idle"}}
        self.live_by_key[key] = [(worker, False)]
        self.hc_terminals["w1"] = worker

        rc, _ = self.run_cli("plan", "amend", "--reason", "retro: rework api", plan=self.current_plan(retro=True))
        self.assertEqual(rc, 0)
        with self.open(True) as s:
            t = self.find_key(s, "api")
            self.assertFalse((t.get("back") or {}).get("auto", False))
            self.assertNotIn("settled_at", t)
            self.assertNotIn("auto_merge", t)
            # tick.settle_back must NOT return the task to Ready after a re-plan.
            with mock.patch.object(self.tick, "now", lambda: 999999999):
                self.tick.settle_back(s, t, [worker])
            self.assertEqual(t["state"], "running")

    def test_row_ready_and_auto_accept(self):
        self.write_settings({"tasks": {"review": True, "review_rounds": 3, "review_min": 1, "merge": "checks", "auto_accept": True, "proof": False}})
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t.update(
                state="ready", ready_at=1500, ready_head=self.head1, ready_turns={"w1": 2}, stray_since=1510,
                auto_merge="checks", merge_requested=1520, accepted_by="engine", accepted_at=1530,
                auto_accepted={"by": "engine", "at": 1530, "head": self.head1, "rounds": [1]},
                accept_wait="no CI", owner_approved={"by": "Assistant", "at": 1540, "note": "ok"},
                head_moved_at=1550, push_held={"reason": "ci cap", "at": 1560},
                pr={"number": 9, "url": "https://example.invalid/pr/9", "repo": "acme/demo", "wait": "Checks are still running.", "no_ci": True, "drift_hold": "drift"},
                review_runs=[{"round": 1, "head": self.head1, "address": "rv1", "cli": "codex", "model": None, "at": 1100, "majors_required": True}],
                reviews=[{"round": 1, "head": self.head1, "by": "rv1", "started_by": "herdr-task", "verdict": "pass", "must_fix": 0, "majors": 0, "report": "ok", "cli": "codex", "model": None, "at": 1200}],
            )
            self.hc_terminals["rv1"] = {"address": "rv1", "state": "finished", "exit_code": 0}
            self.assertEqual(self.commands.gate_passes(t, self.head1), 1)
            self.assertEqual(self.settings.accept_bar(s, t), ("engine", None))
            t["nothing_to_merge"] = True
            s.dirty = True

        rc, _ = self.run_cli("plan", "amend", "--reason", "retro: redo api", plan=self.current_plan(retro=True))
        self.assertEqual(rc, 0)
        doc_head = self.commit_file("README.md", "# Doc note\n", "docs commit")
        with self.open() as s:
            t = self.find_key(s, "api")
            self.assertEqual(t["state"], "running")
            for k in ("ready_at", "ready_head", "ready_turns", "stray_since", "auto_merge", "merge_requested",
                      "accepted_by", "accepted_at", "auto_accepted", "accept_wait", "owner_approved",
                      "nothing_to_merge", "head_moved_at", "push_held"):
                self.assertNotIn(k, t)
            for k in ("wait", "no_ci", "drift_hold"):
                self.assertNotIn(k, t["pr"])
            # Pre-replan passing round never satisfies gate_passes, docs-only carry-forward, accept_bar, or drift.
            self.assertEqual(self.commands.gate_passes(t, self.head1), 0)
            self.assertEqual(self.commands.gate_passes_for(s, t, doc_head, need=1), 0)
            self.assertEqual(self.settings.reviewed_for_head(s, t, self.head1), [])
            self.assertFalse(self.settings.accept_bar(s, t)[0])
            red_view = {"state": "OPEN", "headRefOid": self.head1, "statusCheckRollup": [{"name": "test", "conclusion": "FAILURE"}]}
            self.assertNotIn("red", self.drift.inspect(s, t, red_view) or {})

    def test_row_held_news(self):
        with self.open(True) as s:
            t = self.find_key(s, "api")
            slug = s.slug
            key = f"{slug}#1"
            t["review_followup"] = "Old review findings"
            t["attention"] = f"{self.commands.REVIEW_RESUME_WAIT} Can't resume"
            t["pr"] = {"number": 5, "url": "https://example.invalid/pr/5", "repo": "acme/demo", "news": {"key": "ci-red", "at": 1100}}
            t["review_runs"] = [{"round": 1, "head": self.head1, "address": "rv-retire", "cli": "codex", "model": None, "at": 1100, "majors_required": True}]
            t["recap"] = {"at": 1200, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required"}
            s.dirty = True
        self.hc_terminals["rv-retire"] = {"address": "rv-retire", "state": "live", "foreground": {"name": "codex"}}

        with open(self.held_file, "w") as f:
            json.dump({
                "rv-retire": [{"text": "Your check has finished: record your verdict now.", "at": 1150}],
                "w1": [
                    {"text": "Herdr's review round 1 of TST-1 found what must be fixed", "at": 1160},
                    {"text": '<herdr-news task="TST-1" event="answer">Use SQLite</herdr-news>', "at": 1170},
                    {"text": "The brief of TST-1 changed: support pagination\nWork to it from now on.", "at": 1180},
                ],
                "coord-1": [{"how": "news", "text": (
                    '<herdr-news task="TST-1" state="needs" event="recap">TST-1 needs a recap</herdr-news>\n'
                    '<herdr-news task="TST-2" state="ready" event="ready">TST-2 is ready</herdr-news>'
                ), "at": 1190}],
            }, f)
        with open(self.news_file, "w") as f:
            json.dump({
                "seen": {slug: {"1": {"status": "needs", "recap": "Recap required", "held_ready_at": 1100, "reviewer_failed": "x"}}},
                "outbox": [
                    {"key": key, "ref": "TST-1", "event": "recap", "at": 1190, "line": '<herdr-news task="TST-1" event="recap">old</herdr-news>'},
                    {"key": f"{slug}#2", "ref": "TST-2", "event": "ready", "at": 1195, "line": '<herdr-news task="TST-2" event="ready">keep</herdr-news>'},
                ],
                "delivered": {key: {"event": "recap", "at": 1190}},
            }, f)

        rc, _ = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "clear stale news")
        self.assertEqual(rc, 0)
        with self.open() as s:
            t = self.find_key(s, "api")
            self.assertNotIn("review_followup", t)
            self.assertNotIn("attention", t)
            self.assertNotIn("news", t["pr"])
        with open(self.held_file) as f:
            held = json.load(f)
        self.assertNotIn("rv-retire", held)
        self.assertEqual(len(held["w1"]), 2)
        self.assertIn('event="answer"', held["w1"][0]["text"])
        self.assertIn("The brief of TST-1 changed:", held["w1"][1]["text"])
        self.assertEqual(held["coord-1"][0]["text"], '<herdr-news task="TST-2" state="ready" event="ready">TST-2 is ready</herdr-news>')
        with open(self.news_file) as f:
            nstate = json.load(f)
        self.assertEqual([x["ref"] for x in nstate["outbox"]], ["TST-2"])
        self.assertNotIn(key, nstate["delivered"])
        # news_pass also drops any stale pre-reset outbox item via holds().
        with open(self.news_file, "w") as f:
            json.dump({
                "seen": {slug: {"1": {"status": "working"}}},
                "outbox": [{"key": key, "project": self.repo, "folder": "repo", "event": "ready", "status": "working", "stall": None, "text": "old", "at": 1100}],
                "delivered": {},
            }, f)
        self.news.news_pass({})
        with open(self.news_file) as f:
            self.assertEqual(json.load(f)["outbox"], [])

    def test_row_task_context(self):
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["amendments"] = [{"at": 1050, "text": "Keep backwards compatibility"}]
            t["pr"] = {"number": 11, "url": "https://example.invalid/pr/11", "repo": "acme/demo"}
            t["asks"] = [{"id": "ask1", "terminal": "w1", "question": "Which port?"}]
            t["simulator"] = {"name": "iPhone 17 Pro", "udid": "SIM-1"}
            t["device_wait"] = {"kind": "ios", "at": 1060}
            t["tokens"] = {"/tmp/.claude/s.jsonl": {"off": 5, "sum": 1200}}
            t["ci_runs"] = [{"sha": self.head1, "run": "99", "failed": True, "at": 1100}]
            t["recap"] = {"at": 1200, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required"}
            prompt_before = t["prompt"]
            title_before = t["title"]
            s.dirty = True

        rc, _ = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "keep context")
        self.assertEqual(rc, 0)
        with self.open() as s:
            t = self.find_key(s, "api")
            self.assertEqual(t["title"], title_before)
            self.assertEqual(t["prompt"], prompt_before)
            self.assertEqual(t["amendments"], [{"at": 1050, "text": "Keep backwards compatibility"}])
            self.assertEqual(t["worktree"], self.wt)
            self.assertEqual(t["branch"], "task/1-api")
            self.assertEqual(t["base"], "main")
            self.assertEqual(t["pr"], {"number": 11, "url": "https://example.invalid/pr/11", "repo": "acme/demo"})
            self.assertEqual(t["asks"], [{"id": "ask1", "terminal": "w1", "question": "Which port?"}])
            self.assertEqual(t["simulator"], {"name": "iPhone 17 Pro", "udid": "SIM-1"})
            self.assertEqual(t["device_wait"], {"kind": "ios", "at": 1060})
            self.assertEqual(t["ci_runs"], [{"sha": self.head1, "run": "99", "failed": True, "at": 1100}])
            self.assertEqual(self.budget.ci_since(t), [])

    # ---- caller authorization, target selection, and --check invariants --------------------------

    def test_who_may_trigger_replan(self):
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["recap"] = {"at": 1200, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required"}
            s.data["coordinator"] = {"address": "coord-live", "created_ms": 5000}
            s.dirty = True

        # 1. Worker env vars (`SHEEPDOG_TASK`, `HERDR_BROWSER_TASK`) refuse even with HC_TERMINAL_ID unset.
        for env_k in ("SHEEPDOG_TASK", "HERDR_BROWSER_TASK"):
            with mock.patch.dict(os.environ, {env_k: "repo#1"}, clear=False):
                rc, bad = self.run_cli("plan", "amend", "--reason", "retro: sneaky worker", plan=self.current_plan(retro=True))
                self.assertEqual(rc, 1)
                self.assertIn("can't amend the plan", bad["error"])
                rc, bad = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "sneaky")
                self.assertEqual(rc, 1)
                self.assertIn("can't decide its recap", bad["error"])

        # 2. Failed or invalid `hc get` when HC_TERMINAL_ID is set fails closed.
        self.hc_terminals["mystery"] = None
        with mock.patch.dict(os.environ, {"HC_TERMINAL_ID": "mystery"}, clear=False):
            rc, bad = self.run_cli("plan", "amend", "--reason", "retro: unknown caller", plan=self.current_plan(retro=True))
            self.assertEqual(rc, 1)
            self.assertIn("Couldn't verify the caller terminal", bad["error"])

        # 3. Worker, reviewer, helper, task-listed terminal, or child of task agent is refused.
        for term_id, info in (
            ("w1", {"address": "w1", "state": "live", "labels": {}}),
            ("child-hlp", {"address": "child-hlp", "state": "live", "parent": "w1", "labels": {}}),
            ("rev-x", {"address": "rev-x", "state": "live", "labels": {"sheepdog.role": "reviewer"}}),
            ("hlp-x", {"address": "hlp-x", "state": "live", "labels": {"herdr.role": "helper"}}),
            ("other-w", {"address": "other-w", "state": "live", "labels": {"herdr.task": "repo#2"}}),
            ("spoof-c", {"address": "spoof-c", "state": "live", "created_ms": 5000, "labels": {"herdr.role": "coordinator"}}),
            ("stale-c", {"address": "coord-live", "state": "live", "created_ms": 9999, "labels": {"herdr.role": "coordinator"}}),
        ):
            self.hc_terminals[term_id] = info
            with mock.patch.dict(os.environ, {"HC_TERMINAL_ID": term_id}, clear=False):
                rc, bad = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "try")
                self.assertEqual(rc, 1, f"{term_id} should have been refused")

        # 4. Lease-holding coordinator is allowed.
        self.hc_terminals["coord-live"] = {"address": "coord-live", "state": "live", "created_ms": 5000, "labels": {"herdr.role": "coordinator"}}
        with mock.patch.dict(os.environ, {"HC_TERMINAL_ID": "coord-live"}, clear=False):
            rc, ok = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "coordinator approved")
            self.assertEqual(rc, 0, ok)

        # 5. Owner (outside hc, no task env vars) is allowed.
        with self.open(True) as s:
            self.find_key(s, "api")["recap"] = {"at": 2000, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required"}
            s.dirty = True
        rc, ok = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "owner approved")
        self.assertEqual(rc, 0, ok)

    def test_plan_amend_retro_target_selection(self):
        # Set up two open tasks: `api` has an open recap; `api-v2` is running with 1 round used.
        with self.open(True) as s:
            t1 = self.find_key(s, "api")
            t2 = self.find_key(s, "api-v2")
            t1["reviews"] = [{"round": 1, "result": "report", "verdict": "changes", "majors": 1, "head": self.head1, "at": 1100}]
            t1["recap"] = {"at": 1200, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required"}
            t2["state"] = "running"
            t2["reviews"] = [{"round": 1, "result": "report", "verdict": "changes", "majors": 0, "head": self.head1, "at": 1100}]
            s.dirty = True

        # 1. Hyphenated `api-v2` in `--reason` re-plans ONLY `api-v2`, never `api` (`\b` bug in SDF-67).
        rc, _ = self.run_cli("plan", "amend", "--reason", "retro on api-v2", plan=self.current_plan())
        self.assertEqual(rc, 0)
        with self.open() as s:
            self.assertIn("recap", self.find_key(s, "api"))
            self.assertTrue(self.find_key(s, "api-v2")["reviews"][0].get("before_replan"))

        # 2. Unresolved task ref (`TST-99`) or done/removed key (`API-v2`) does NOT fall back to resetting `api`.
        rc, _ = self.run_cli("plan", "amend", "--reason", "retro after TST-99", plan=self.current_plan())
        self.assertEqual(rc, 0)
        with self.open(True) as s:
            self.assertIn("recap", self.find_key(s, "api"))
            self.find_key(s, "api-v2")["state"] = "done"
            s.dirty = True
        plan_only_api = {"milestones": [milestone("M1", [{"key": "api"}])]}
        rc, _ = self.run_cli("plan", "amend", "--reason", "retro follow-up from API-v2", plan=plan_only_api)
        self.assertEqual(rc, 0)
        with self.open() as s:
            self.assertIn("recap", self.find_key(s, "api"))

        # 3. Structural addition with `retro` in reason does NOT touch unrelated open recap task `api`.
        plan_with_new = {"milestones": [milestone("M1", [{"key": "api"}, plan_task("extra", lines=50)])]}
        rc, _ = self.run_cli("plan", "amend", "--reason", "retro: add extra helper task", plan=plan_with_new)
        self.assertEqual(rc, 0)
        with self.open() as s:
            self.assertIn("recap", self.find_key(s, "api"))

        # 4. `rm` records `removed_plan_keys`, so naming a removed key in `--reason` won't fall back to `api` either.
        with self.open() as s:
            extra_id = self.find_key(s, "extra")["id"]
        rc, _ = self.run_cli("rm", f"TST-{extra_id}")
        self.assertEqual(rc, 0)
        rc, _ = self.run_cli("plan", "amend", "--reason", "retro after extra", plan=plan_only_api)
        self.assertEqual(rc, 0)
        with self.open() as s:
            self.assertIn("recap", self.find_key(s, "api"))

        # 5. Hyphenated English prose (`copy-on-write`, `UTF-8`) with no structural change DOES fall back to `api`!
        rc, _ = self.run_cli("plan", "amend", "--reason", "retro: switch to copy-on-write for UTF-8", plan=plan_only_api)
        self.assertEqual(rc, 0)
        with self.open() as s:
            self.assertNotIn("recap", self.find_key(s, "api"))

        # 6. Multiple open recap/review_out tasks without naming one refuses ambiguously; validation rejects bad `retro`.
        rc, _ = self.run_cli("plan", "amend", "--reason", "add second", plan={"milestones": [milestone("M1", [{"key": "api"}, plan_task("second")])]})
        self.assertEqual(rc, 0)
        with self.open(True) as s:
            self.find_key(s, "api")["recap"] = {"at": 3000, "reasons": ["x"], "round": 1, "title": "Recap required"}
            t_sec = self.find_key(s, "second")
            t_sec["state"] = "running"
            t_sec["review_out"] = 3000
            s.dirty = True
        both_plan = {"milestones": [milestone("M1", [{"key": "api"}, {"key": "second"}])]}
        rc, bad = self.run_cli("plan", "amend", "--reason", "retro: try again", plan=both_plan)
        self.assertEqual(rc, 1)
        self.assertIn("Multiple tasks need a recap", bad["error"])

        bad_new = {"milestones": [milestone("M1", [{"key": "api"}, {"key": "second"}, plan_task("brand-new", retro=True)])]}
        rc, bad = self.run_cli("plan", "amend", "--reason", "x", plan=bad_new)
        self.assertEqual(rc, 1)
        self.assertIn("retro is for an open existing task", bad["error"])

    def test_plan_amend_check_touches_nothing(self):
        project_id_path = os.path.join(self.repo, ".sheepdog", "project_id")
        if os.path.exists(project_id_path):
            os.remove(project_id_path)
        with self.open(True) as s:
            s.data.pop("project_id", None)
            t = self.find_key(s, "api")
            t["pr"] = {"number": 55, "url": "https://example.invalid/pr/55", "repo": "acme/demo"}
            t["merge_queued"] = 1500
            t["review_runs"] = [{"round": 1, "head": self.head1, "address": "rv-check", "cli": "codex", "model": None, "at": 1100, "majors_required": True}]
            t["recap"] = {"at": 1600, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required"}
            store_path = s.path
            s.dirty = True
        self.hc_terminals["rv-check"] = {"address": "rv-check", "state": "live", "foreground": {"name": "codex"}}
        self.queue_state = {"id": "PR_node_55", "state": "OPEN", "in": True, "auto": True}
        with open(self.held_file, "w") as f:
            json.dump({"rv-check": [{"text": "nudge", "at": 1}]}, f)
        with open(self.news_file, "w") as f:
            json.dump({"outbox": [{"key": "repo#1", "event": "recap"}]}, f)

        with open(store_path, "rb") as f:
            before_store = f.read()
        with open(self.held_file, "rb") as f:
            before_held = f.read()
        with open(self.news_file, "rb") as f:
            before_news = f.read()
        self.gh_calls.clear()
        self.ended_reviewers.clear()
        self.ended_agents.clear()

        rc, res = self.run_cli("plan", "amend", "--check", "--reason", "retro: dry run", plan=self.current_plan(retro=True))
        self.assertEqual((rc, res), (0, {"ok": True}))
        self.assertFalse(os.path.exists(project_id_path))
        self.assertEqual(self.gh_calls, [])
        self.assertEqual(self.ended_reviewers, [])
        self.assertEqual(self.ended_agents, [])
        with open(store_path, "rb") as f:
            self.assertEqual(f.read(), before_store)
        with open(self.held_file, "rb") as f:
            self.assertEqual(f.read(), before_held)
        with open(self.news_file, "rb") as f:
            self.assertEqual(f.read(), before_news)

    def test_round_1_review_findings(self):
        import herdr_task.devices as devices

        # 1. Legacy reviewer in t["review"] is retired on re-plan and cannot pass the task via cmd_review.
        with self.open(True) as s:
            t = self.find_key(s, "api")
            t["review"] = {"reviewer": "rv-legacy", "round": 1, "of": 3, "at": 1100}
            t["recap"] = {"at": 1200, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required"}
            s.dirty = True
        self.hc_terminals["rv-legacy"] = {"address": "rv-legacy", "state": "live", "foreground": {"name": "claude"}}
        self.ended_reviewers.clear()
        rc, _ = self.run_cli("recap", "TST-1", "--decide", "new", "--why", "retire legacy reviewer")
        self.assertEqual(rc, 0)
        self.assertIn("rv-legacy", self.ended_reviewers)
        with mock.patch.dict(os.environ, {"HC_TERMINAL_ID": "rv-legacy"}, clear=False):
            rc, bad = self.run_cli("review", "TST-1", "--pass")
        self.assertEqual(rc, 1)
        self.assertIn("no active review round", bad["error"])
        with self.open() as s:
            self.assertEqual(self.find_key(s, "api")["state"], "running")

        # 2. collect_live_reviewers fetches detail when hc ls omits role labels, retiring reviewer and keeping helper.
        with self.open() as s:
            t = self.find_key(s, "api")
            key = s.key(t)
            t.pop("retiring_reviewers", None)
            self.live_by_key[key] = [
                ({"address": "sub-rev", "state": "live", "labels": {"herdr.task": key}}, True),
                ({"address": "sub-hlp", "state": "live", "labels": {"herdr.task": key}}, True),
            ]
            self.hc_terminals["sub-rev"] = {"address": "sub-rev", "state": "live", "labels": {"herdr.task": key, "herdr.role": "reviewer"}}
            self.hc_terminals["sub-hlp"] = {"address": "sub-hlp", "state": "live", "labels": {"herdr.task": key, "herdr.role": "helper"}}
            found = self.replan.collect_live_reviewers(s, t)
            self.assertEqual(found, ["sub-rev"])

        # 3. retiring_live production subprocess parser keeps hold on inconclusive hc get and clears only on confirmed exit/absence.
        with mock.patch.object(self.commands, "hc_json", self.replan._DEFAULT_HC_JSON):
            for proc, want_hold in (
                (subprocess.CompletedProcess(["hc"], 0, '{"error":"service unavailable"}', ""), True),
                (subprocess.CompletedProcess(["hc"], 0, "{}", ""), True),
                (subprocess.CompletedProcess(["hc"], 1, "", "dependency socket not found"), True),
                (subprocess.CompletedProcess(["hc"], 0, '{"address":"rv-p","state":"finished","exit_code":0}', ""), False),
                (subprocess.CompletedProcess(["hc"], 1, "", "terminal rv-p not found"), False),
            ):
                dummy = {"retiring_reviewers": ["rv-p"]}
                with mock.patch.object(self.commands, "run", lambda argv, p=proc: p):
                    got = self.replan.retiring_live(None, dummy)
                self.assertEqual(got, want_hold, proc)

        # 4. news_pass race: clear_task_held_news after snapshot and before delivery neither sends nor restores stale recap.
        if os.path.exists(self.news_file):
            os.remove(self.news_file)
        with self.open(True) as s:
            t = self.find_key(s, "api")
            slug = s.slug
            key = f"{slug}#1"
            t.pop("retiring_reviewers", None)
            t.pop("review_reset_at", None)
            t["recap"] = {"at": 2000, "reasons": ["3 failed CI runs"], "round": 1, "title": "Recap required: 3 failed CI runs"}
            t["attention"] = "Recap required: 3 failed CI runs"
            s.dirty = True
        t0 = self.base.now() - self.news.NEWS_SETTLE_MS - 1000
        with open(self.news_file, "w") as f:
            json.dump({
                "seen": {slug: {"_init": True, "1": {"status": "needs", "recap": True}}},
                "outbox": [{"key": key, "project": self.repo, "folder": "repo", "event": "recap", "status": "needs",
                            "stall": None, "text": '<herdr-news task="TST-1" event="recap">stale recap</herdr-news>', "at": t0}],
                "delivered": {key: {"to": "coord-1", "turn": 1, "at": t0, "since": t0, "event": "ready", "status": "needs", "stall": None}},
            }, f)
        delivered_msgs = []
        hook_ran = []

        def replan_during_lookup(project):
            hook_ran.append(project)
            with self.open(True) as s2:
                t2 = self.find_key(s2, "api")
                self.replan.replan_task(s2, t2, why="race replan", at=t0 + 500)
            return "coord-1"

        self.hc_terminals["coord-1"] = {"address": "coord-1", "state": "live", "turn": 2, "status": {"state": "idle"}}
        with mock.patch.object(self.news, "coordinator_in", replan_during_lookup), \
                mock.patch.object(self.news, "news_send", lambda to, txt, **kw: (delivered_msgs.append(txt), "sent")[1]):
            self.news.news_pass({})
        self.assertEqual(hook_ran, [self.repo])
        self.assertEqual(delivered_msgs, [])
        with open(self.news_file) as f:
            after_news = json.load(f)
        self.assertEqual(after_news["outbox"], [])
        self.assertEqual(after_news["delivered"], {})
        self.assertNotIn("recap", after_news["seen"][slug]["1"])

        # 5. Held device CARRY_ON wake-up survives clear_task_held_news while old review/recap items are removed.
        with open(self.held_file, "w") as f:
            json.dump({
                "w1": [
                    {"text": devices.CARRY_ON, "at": 3000},
                    {"text": "Stop TST-1: Herdr's loop-breaker says recap first.", "at": 3001},
                ],
            }, f)
        with self.open() as s:
            t = self.find_key(s, "api")
            self.replan.clear_task_held_news(s, t, at=6000)
        with open(self.held_file) as f:
            self.assertEqual(json.load(f), {"w1": [{"text": devices.CARRY_ON, "at": 3000}]})

        # 6. plan amend --check runs no git on task worktrees, and Store(write=False) creates no lock file or STORE dir.
        with mock.patch.object(self.budget, "git", side_effect=AssertionError("worktree git called in --check")):
            rc, res = self.run_cli("plan", "amend", "--check", "--reason", "retro: check boundary", plan=self.current_plan(retro=True))
        self.assertEqual((rc, res), (0, {"ok": True}))

        fresh_store_dir = os.path.join(self.tmp, "missing-store")
        with mock.patch.object(self.store_mod, "STORE", fresh_store_dir):
            loc = self.store_mod.locate(self.repo)
            with self.store_mod.Store(loc, write=False):
                pass
        self.assertFalse(os.path.exists(fresh_store_dir))


if __name__ == "__main__":
    unittest.main()
