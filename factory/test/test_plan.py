"""SDF-31: milestones, plan apply and plan amend; a split keeps the parent's place."""

import home_guard  # noqa: F401  (first: every test on a throwaway home, never the real one)
import contextlib
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "mods", "tasks", "bin"))


def task(key, lines=200, **kw):
    return {"key": key, "title": key.replace("-", " ").title(), "prompt": f"Do {key}.", "lines": lines, **kw}


def milestone(mid, tasks, **kw):
    return {"id": mid, "title": f"Milestone {mid}", "goal": f"goal of {mid}", "exit_test": f"exit test of {mid}",
            "gate": "auto", "mandate": f"the owner's words for {mid}", "tasks": tasks, **kw}


class Plans(unittest.TestCase):
    def setUp(self):
        import herdr_task.base as base
        import herdr_task.cli as cli
        import herdr_task.plan as plan
        import herdr_task.settings as settings
        import herdr_task.store as store
        self.cli, self.plan, self.store = cli, plan, store
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="plan-test-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.store_dir = os.path.join(self.tmp, "tasks")
        self.repo = os.path.join(self.tmp, "repo")
        os.makedirs(self.store_dir)
        subprocess.run(["git", "init", "-q", "-b", "main", self.repo], check=True)
        for k, v in (("user.name", "T"), ("user.email", "t@x"), ("commit.gpgsign", "false")):
            subprocess.run(["git", "-C", self.repo, "config", k, v], check=True)
        subprocess.run(["git", "-C", self.repo, "commit", "-q", "--allow-empty", "-m", "init"], check=True)
        settings_file = os.path.join(self.tmp, "settings.json")
        with open(settings_file, "w") as f:
            json.dump({}, f)
        self.live = {}  # task id: its live agents
        self.ended = []
        for p in (mock.patch.object(base, "STORE", self.store_dir), mock.patch.object(store, "STORE", self.store_dir),
                  mock.patch.object(settings, "SETTINGS", settings_file),
                  mock.patch.object(plan, "agents_of", lambda s, t: self.live.get(t["id"], [])),
                  mock.patch("herdr_task.commands.end_agent", lambda s, t, a: self.ended.append(a))):
            p.start()
            self.addCleanup(p.stop)
        with self.open(True) as s:
            s.data["prefix"] = "TST"
            s.dirty = True

    def open(self, write=False):
        return self.store.Store(self.store.locate(self.repo), write=write)

    def run_cli(self, *argv, plan=None):
        if plan is not None:
            path = os.path.join(self.tmp, "plan.json")
            with open(path, "w") as f:
                json.dump(plan, f)
            argv = [argv[0], argv[1], path, *argv[2:]]
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(sys, "argv", ["herdr-task", *argv, "--folder", self.repo, "--json"]), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = self.cli.main()
        return rc, json.loads(out.getvalue() or "null")

    def tasks(self):
        with self.open() as s:
            return {t.get("plan_key") or f"acc-{t.get('acceptance_of')}": t for t in s.data["tasks"]}

    def first_plan(self):
        return {"milestones": [
            milestone("M1", [task("design", kind="design"), task("api", after=["design"]), task("page", after=["design", "api"])],
                      ui=True, gate="owner"),
            milestone("M2", [task("ship", lines=120)], release="v1.2.0"),
        ]}

    def test_apply_adds_tasks_acceptance_and_order(self):
        rc, v = self.run_cli("plan", "apply", plan=self.first_plan())
        self.assertEqual(rc, 0, v)
        t = self.tasks()
        self.assertEqual(t["api"]["after"], [t["design"]["id"]])
        self.assertEqual(t["page"]["after"], [t["design"]["id"], t["api"]["id"]])
        self.assertEqual(t["acc-M1"]["after"], sorted([t["design"]["id"], t["api"]["id"], t["page"]["id"]]))
        # The next milestone's first task waits for this one's acceptance; its acceptance for its own tasks.
        self.assertEqual(t["ship"]["after"], [t["acc-M1"]["id"]])
        self.assertEqual(t["acc-M2"]["after"], sorted([t["ship"]["id"], t["acc-M1"]["id"]]))
        self.assertEqual(t["design"]["set"]["review_kind"], "design")
        self.assertEqual(t["api"]["estimate_lines"], 200)
        self.assertTrue(t["api"]["prompt"].startswith("Milestone M1: Milestone M1. Its goal: goal of M1\n\nDo api."))
        # Owner gate: its acceptance merges only after the owner; a release asks before ready.
        self.assertEqual(t["acc-M1"]["set"]["merge"], "ask")
        self.assertIn("exit test of M1", t["acc-M1"]["prompt"])
        self.assertIn("--kind release", t["acc-M2"]["prompt"])
        self.assertIn("ask before you say ready", t["acc-M2"]["prompt"])
        self.assertNotIn("--kind release", t["acc-M1"]["prompt"])
        ms = {m["id"]: m for m in v["milestones"]}
        self.assertEqual((ms["M1"]["state"], ms["M2"]["state"]), ("active", "planned"))
        self.assertEqual(ms["M1"]["mandate"], "the owner's words for M1")
        self.assertEqual(v["replans"], 0)
        # Applying again is refused: a plan changes through amend.
        rc, v = self.run_cli("plan", "apply", plan=self.first_plan())
        self.assertEqual(rc, 1)
        self.assertIn("plan amend", v["error"])

    def test_checks_refuse_the_whole_plan_and_change_nothing(self):
        bad = {"milestones": [
            milestone("M1", [task("a", after=["b"]), task("b", after=["a"]), task("big", lines=900), task("nolines", lines=None),
                             task("c", after=["SDF-99"])]),
            milestone("M2", [task("ui-work")], ui=True, state="active", gate="maybe", release="1.2"),
            milestone("M3", [task("draw", kind="design"), task("screen")], ui=True),
        ]}
        rc, v = self.run_cli("plan", "apply", plan=bad)
        self.assertEqual(rc, 1)
        err = v["error"]
        for words in ("unknown field(s) state", "gate is auto, fable or owner", "release is a tag", "900 lines is past the 500-line",
                      "nolines: lines (its size estimate"):
            self.assertIn(words, err)
        self.assertEqual(self.tasks(), {})
        # The graph checks, once the shape is right.
        bad["milestones"][0]["tasks"][3]["lines"] = 100
        bad["milestones"][0]["tasks"][2]["lines"] = 100
        bad["milestones"][1] = milestone("M2", [task("ui-work")], ui=True)
        rc, v = self.run_cli("plan", "apply", plan=bad)
        err = v["error"]
        self.assertIn("after SDF-99 is neither", err)
        bad["milestones"][0]["tasks"][4]["after"] = []
        rc, v = self.run_cli("plan", "apply", plan=bad)
        err = v["error"]
        self.assertIn("would end up waiting for itself", err)
        self.assertIn("M2 is a UI milestone: it needs a design task", err)
        self.assertIn("screen in UI milestone M3 must come after its design task", err)
        self.assertEqual(self.tasks(), {})

    def test_review_round_1_findings(self):
        # A loop through the engine's own edges: M1's task waiting for M2's would wait for M1's acceptance.
        plan = {"milestones": [milestone("M1", [task("a", after=["b"])]), milestone("M2", [task("b")])]}
        rc, v = self.run_cli("plan", "apply", plan=plan)
        self.assertIn("a would end up waiting for itself", v["error"])
        # The size limit can't be passed through set.
        plan = {"milestones": [milestone("M1", [task("a", lines=10, set={"estimate.lines": "900"})])]}
        rc, v = self.run_cli("plan", "apply", plan=plan)
        self.assertIn("its size estimate is lines, not set", v["error"])
        self.assertEqual(self.tasks(), {})
        # An empty milestone keeps its place: its acceptance waits for the previous one.
        plan = {"milestones": [milestone("M1", [task("a")]), milestone("M2", [])]}
        rc, v = self.run_cli("plan", "apply", plan=plan)
        self.assertEqual(rc, 0, v)
        t = self.tasks()
        self.assertEqual(t["acc-M2"]["after"], [t["acc-M1"]["id"]])
        self.assertEqual({m["id"]: m["state"] for m in v["milestones"]}, {"M1": "active", "M2": "planned"})
        # A gate changed by amend reaches the acceptance task, both ways.
        plan["milestones"][1]["gate"] = "owner"
        rc, v = self.run_cli("plan", "amend", "--reason", "the owner checks M2", plan=plan)
        self.assertEqual(self.tasks()["acc-M2"]["set"]["merge"], "ask")
        plan["milestones"][1]["gate"] = "auto"
        rc, v = self.run_cli("plan", "amend", "--reason", "delegated", plan=plan)
        self.assertNotIn("merge", self.tasks()["acc-M2"].get("set") or {})
        # A started task's malformed after is a plan problem, not a crash.
        with self.open(True) as s:
            s.find(str(t["a"]["id"]))["state"] = "running"
            s.dirty = True
        plan["milestones"][0]["tasks"] = [{"key": "a", "after": 42}]
        rc, v = self.run_cli("plan", "amend", "--reason", "x", plan=plan)
        self.assertIn("a: after is a list", v["error"])

    def test_check_only_changes_nothing(self):
        rc, v = self.run_cli("plan", "apply", "--check", plan=self.first_plan())
        self.assertEqual((rc, v), (0, {"ok": True}))
        self.assertEqual(self.tasks(), {})

    def test_amend_needs_a_reason_logs_it_and_keeps_the_mandate(self):
        self.run_cli("plan", "apply", plan=self.first_plan())
        plan = self.first_plan()
        plan["milestones"].append(milestone("M3", [task("polish", lines=80)]))
        rc, v = self.run_cli("plan", "amend", plan=plan)
        self.assertEqual(rc, 1)
        self.assertIn("--reason", v["error"])
        rc, v = self.run_cli("plan", "amend", "--reason", "the owner asked for polish", plan=plan)
        self.assertEqual(rc, 0, v)
        t = self.tasks()
        self.assertEqual(t["polish"]["after"], [t["acc-M2"]["id"]])
        self.assertEqual(v["replans"], 1)
        self.assertEqual(v["log"][-1]["reason"], "the owner asked for polish")
        self.assertEqual(v["applied"]["added"], ["TST-%d" % t["polish"]["id"], "TST-%d" % t["acc-M3"]["id"]])
        self.assertEqual([m["order"] for m in v["milestones"]], [1, 2, 3])
        # The owner's words stay as said.
        plan["milestones"][0]["mandate"] = "something wider"
        rc, v = self.run_cli("plan", "amend", "--reason", "x", plan=plan)
        self.assertIn("mandate is the owner's words", v["error"])

    def test_amend_refuses_dropping_or_rewriting_started_work(self):
        self.run_cli("plan", "apply", plan=self.first_plan())
        plan = self.first_plan()
        plan["milestones"][0]["tasks"].pop(1)  # api, still to do
        plan["milestones"][0]["tasks"][1]["after"] = ["design"]
        rc, v = self.run_cli("plan", "amend", "--reason", "x", plan=plan)
        self.assertIn("(api) is in M1 but not in this file", v["error"])
        with self.open(True) as s:
            next(t for t in s.data["tasks"] if t.get("plan_key") == "design")["state"] = "running"
            s.dirty = True
        plan = self.first_plan()
        plan["milestones"][0]["tasks"][0]["prompt"] = "Something else."
        rc, v = self.run_cli("plan", "amend", "--reason", "x", plan=plan)
        self.assertIn("has started: change its brief with herdr-task edit --amend", v["error"])
        # A started task listed by its key alone is fine, and a to-do task's brief changes.
        plan["milestones"][0]["tasks"][0] = {"key": "design"}
        plan["milestones"][0]["tasks"][1]["prompt"] = "Do api better."
        rc, v = self.run_cli("plan", "amend", "--reason", "clearer api brief", plan=plan)
        self.assertEqual(rc, 0, v)
        self.assertTrue(self.tasks()["api"]["prompt"].endswith("Do api better."))

    def test_split_keeps_the_parents_place(self):
        self.run_cli("plan", "apply", plan=self.first_plan())
        before = self.tasks()
        api = before["api"]
        plan = self.first_plan()
        m1 = plan["milestones"][0]["tasks"]
        m1[1:2] = [task("api-read", split_of="api"), task("api-write", split_of="TST-%d" % api["id"], after=["api-read"])]
        rc, v = self.run_cli("plan", "amend", "--reason", "api was two jobs", plan=plan)
        self.assertEqual(rc, 0, v)
        t = self.tasks()
        self.assertNotIn("api", t)
        d, r, w = t["design"]["id"], t["api-read"]["id"], t["api-write"]["id"]
        self.assertEqual(t["api-read"]["after"], [d])  # the parent's own after
        self.assertEqual(t["api-write"]["after"], [d, r])
        self.assertEqual(t["page"]["after"], [d, r, w])  # whoever waited on the parent waits on both
        self.assertEqual(t["acc-M1"]["after"], sorted([d, r, w, t["page"]["id"]]))
        self.assertEqual(t["api-read"]["milestone"], "M1")
        self.assertEqual(t["api-read"]["split_of"], "TST-%d" % api["id"])
        self.assertEqual(v["log"][-1]["split"], {"TST-%d" % api["id"]: ["TST-%d" % r, "TST-%d" % w]})

    def test_split_refused_in_another_milestone_or_while_at_work(self):
        self.run_cli("plan", "apply", plan=self.first_plan())
        api = self.tasks()["api"]
        plan = self.first_plan()
        plan["milestones"][0]["tasks"].pop(1)
        plan["milestones"][0]["tasks"][1]["after"] = ["design"]
        plan["milestones"][1]["tasks"].append(task("api-b", split_of="api"))
        rc, v = self.run_cli("plan", "amend", "--reason", "x", plan=plan)
        self.assertIn("a split keeps the parent's place", v["error"])
        with self.open(True) as s:
            s.find(str(api["id"]))["state"] = "running"
            s.dirty = True
        self.live[api["id"]] = [{"address": "w1", "state": "live", "status": {"state": "working"}, "foreground": {"name": "claude"}}]
        plan["milestones"][1]["tasks"].pop()
        plan["milestones"][0]["tasks"].append(task("api-b", split_of="api"))
        rc, v = self.run_cli("plan", "amend", "--reason", "x", plan=plan)
        self.assertIn("can't be split now (its agent is at work)", v["error"])
        # After a recap its agent sits idle: the split ends it and goes on.
        self.live[api["id"]][0]["status"] = {"state": "idle"}
        rc, v = self.run_cli("plan", "amend", "--reason", "recap said split", plan=plan)
        self.assertEqual(rc, 0, v)
        self.assertEqual(self.ended, ["w1"])
        t = self.tasks()
        self.assertNotIn("api", t)
        self.assertEqual(t["api-b"]["after"], [t["design"]["id"]])
        self.assertIn(t["api-b"]["id"], t["acc-M1"]["after"])

    def test_states_are_derived_and_acceptance_closes_the_milestone(self):
        self.run_cli("plan", "apply", plan=self.first_plan())
        t = self.tasks()

        def states():
            return {m["id"]: m["state"] for m in self.run_cli("plan", "show")[1]["milestones"]}

        with self.open(True) as s:
            for k in ("design", "api", "page"):
                s.find(str(t[k]["id"]))["state"] = "done"
            s.find(str(t["design"]["id"]))["archived"] = 1  # archived done tasks still count as done
            s.dirty = True
        self.assertEqual(states(), {"M1": "validating", "M2": "planned"})
        # No new work in a milestone whose acceptance has started.
        with self.open(True) as s:
            s.find(str(t["acc-M1"]["id"]))["state"] = "running"
            s.dirty = True
        plan = self.first_plan()
        plan["milestones"][0]["tasks"].append(task("late"))
        rc, v = self.run_cli("plan", "amend", "--reason", "x", plan=plan)
        self.assertIn("acceptance (TST-%d) has started" % t["acc-M1"]["id"], v["error"])
        with self.open(True) as s:
            s.find(str(t["acc-M1"]["id"]))["state"] = "done"
            s.dirty = True
        self.assertEqual(states(), {"M1": "done", "M2": "active"})


if __name__ == "__main__":
    unittest.main()
