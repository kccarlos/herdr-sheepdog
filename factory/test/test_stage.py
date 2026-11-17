"""SDF-30: one ordered stage table per task (first match wins); owner-waiting rows are the inbox."""

import home_guard  # noqa: F401  (first: every test on a throwaway home, never the real one)
import argparse
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
BIN = os.path.join(ROOT, "mods", "tasks", "bin")
sys.path.insert(0, BIN)


class OrderedStageTable(unittest.TestCase):
    def setUp(self):
        import herdr_task.agents as agents
        import herdr_task.ask as ask
        import herdr_task.automations as automations
        import herdr_task.base as base
        import herdr_task.cli as cli
        import herdr_task.commands as commands
        import herdr_task.output as output
        import herdr_task.settings as settings
        import herdr_task.stage as stage
        import herdr_task.store as store
        self.agents, self.ask, self.automations = agents, ask, automations
        self.base, self.cli, self.commands = base, cli, commands
        self.output, self.settings, self.stage, self.store = output, settings, stage, store

        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="stage-test-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.store_dir = os.path.join(self.tmp, "tasks")
        self.asks_dir = os.path.join(self.store_dir, "asks")
        self.settings_file = os.path.join(self.tmp, "settings.json")
        self.repo = os.path.join(self.tmp, "repo")
        os.makedirs(self.store_dir)
        os.makedirs(self.repo)
        subprocess.run(["git", "init", "-q", "-b", "main", self.repo], check=True)
        for k, v in (("user.name", "T"), ("user.email", "t@x"), ("commit.gpgsign", "false")):
            subprocess.run(["git", "-C", self.repo, "config", k, v], check=True)
        subprocess.run(["git", "-C", self.repo, "commit", "-q", "--allow-empty", "-m", "init"], check=True)
        with open(self.settings_file, "w") as f:
            json.dump({"projects": {self.repo: {"tasks": {"auto_accept": False}}}}, f)

        self.clock = 1_800_000_000_000
        self.live_by_key = {}
        for p in (
            mock.patch.object(base, "STORE", self.store_dir),
            mock.patch.object(store, "STORE", self.store_dir),
            mock.patch.object(ask, "ASKS", self.asks_dir),
            mock.patch.object(settings, "SETTINGS", self.settings_file),
            mock.patch.object(agents, "now", lambda: self.clock),
            mock.patch.object(output, "now", lambda: self.clock),
            mock.patch.object(agents, "live_agents", lambda key: list(self.live_by_key.get(key, []))),
            mock.patch.object(agents, "detail", lambda k: k),
            mock.patch.object(agents, "live_helpers", lambda key: []),
            mock.patch.object(output, "subagents", lambda key: []),
            mock.patch.object(commands, "live_agents", lambda key: list(self.live_by_key.get(key, []))),
            mock.patch.object(commands, "detail", lambda k: k),
            mock.patch.object(commands, "refresh_sessions", lambda *a: None),
            mock.patch.object(automations, "live_agents", lambda key: list(self.live_by_key.get(key, []))),
            mock.patch.object(automations, "detail", lambda k: k),
        ):
            p.start()
            self.addCleanup(p.stop)

    def add_task(self, title, state="todo", **extra):
        with self.store.Store(self.store.locate(self.repo), write=True) as s:
            s.data["prefix"] = "TST"
            t = s.new(title, "", "worktree")
            t["state"] = state
            t.update(extra)
            s.dirty = True
            return s, dict(t)

    def open_store(self, write=False):
        return self.store.Store(self.store.locate(self.repo), write=write)

    def test_table_covers_all_stages_in_order(self):
        self.assertEqual(
            self.stage.STAGES,
            ("owner_answer", "done", "owner_gate", "blocked_or_failed", "ci", "waiting_on_dep", "queued", "idle", "working"),
        )
        self.assertEqual(self.stage.OWNER_STAGES, frozenset({"owner_answer", "owner_gate"}))
        for name in self.stage.STAGES:
            self.assertEqual(self.stage.is_inbox_stage(name), name in ("owner_answer", "owner_gate"))

    def test_first_match_wins_precedence(self):
        # Task with open owner ask, ready state, attention, and running CI: owner_answer wins first.
        with self.open_store(write=True) as s:
            s.data["prefix"] = "TST"
            t = s.new("Ship release", "", "worktree")
            t.update(state="ready", pr={"number": 1, "wait": "Checks running"}, attention="It conflicts.", attention_at=self.clock - 60_000)
            s.dirty = True
            ref = s.ref(t)
        self.ask.save({"id": "ask001", "kind": "release", "gated": True, "question": "Tag v1.0.0?",
                       "options": ["Yes", "No"], "recommend": "Yes", "task": ref, "to": "coord-1", "at": 1})
        with self.open_store() as s:
            res = self.stage.resolve_stage(s.find("1"), [], store=s)
            self.assertEqual((res.stage, res.inbox, res.why), ("owner_answer", True, "Tag v1.0.0?"))

        # Open owner ask on a done task (note 31) still stays in owner_answer / inbox.
        with self.open_store(write=True) as s:
            s.find("1")["state"] = "done"
            s.dirty = True
        with self.open_store() as s:
            res = self.stage.resolve_stage(s.find("1"), [], store=s)
            self.assertEqual((res.stage, res.inbox), ("owner_answer", True))

        # Answering the ask lets done win over stale attention/pr wait.
        self.ask.save({"id": "ask001", "kind": "release", "gated": True, "question": "Tag v1.0.0?",
                       "options": ["Yes", "No"], "recommend": "Yes", "task": ref, "to": "coord-1", "at": 1, "answer": "Yes"})
        with self.open_store() as s:
            res = self.stage.resolve_stage(s.find("1"), [], store=s)
            self.assertEqual((res.stage, res.inbox), ("done", False))

    def test_coordinator_decision_ask_is_not_owner_inbox(self):
        with self.open_store(write=True) as s:
            s.data["prefix"] = "TST"
            t = s.new("Pick format", "", "worktree")
            t.update(state="running", started=self.clock - 120_000, agents=[{"address": "w1", "at": self.clock - 120_000}])
            s.dirty = True
            ref = s.ref(t)
        self.ask.save({"id": "dec001", "kind": "decision", "gated": False, "question": "SQLite or JSON?",
                       "options": ["SQLite", "JSON"], "recommend": "SQLite", "task": ref, "to": "w1", "at": 1})
        live = [{"address": "w1", "state": "live", "status": {"state": "working"}, "foreground": {"name": "claude"}}]
        with self.open_store() as s:
            res = self.stage.resolve_stage(s.find("1"), live, store=s)
            self.assertEqual((res.stage, res.inbox), ("working", False))

    def test_note_49_out_of_rounds_never_hidden_behind_working_or_bg_job(self):
        with self.open_store(write=True) as s:
            s.data["prefix"] = "TST"
            code_t = s.new("Code out of rounds", "", "worktree")
            code_t.update(state="running", review_out=True, started=self.clock - 120_000,
                          progress={"text": "Waiting on review", "set": self.clock - 10_000, "at": self.clock - 10_000},
                          agents=[{"address": "w1", "at": self.clock - 120_000}])
            design_t = s.new("Design out of rounds", "", "worktree")
            design_t.update(state="running", review_out=True, set={"review_kind": "design"}, started=self.clock - 120_000,
                            agents=[{"address": "w2", "at": self.clock - 120_000}])
            s.dirty = True

        busy_w1 = [{"address": "w1", "state": "live", "status": {"state": "working"}, "foreground": {"name": "claude"}}]
        busy_w2 = [{"address": "w2", "state": "live", "status": {"state": "working"}, "foreground": {"name": "claude"}}]
        with self.open_store() as s:
            r_code = self.stage.resolve_stage(s.find("1"), busy_w1, store=s)
            self.assertEqual((r_code.stage, r_code.inbox, r_code.why),
                             ("blocked_or_failed", False, "Out of review rounds: wants one more"))
            r_design = self.stage.resolve_stage(s.find("2"), busy_w2, store=s)
            self.assertEqual((r_design.stage, r_design.inbox, r_design.why),
                             ("owner_gate", True, "Out of review rounds: for the person"))

    def test_note_26_background_build_is_working_not_needs_you_or_inbox(self):
        log = os.path.join(self.tmp, "bg.jsonl")
        with open(log, "w") as f:
            f.write(json.dumps({"type": "user", "toolUseResult": {"backgroundTaskId": "bg1"},
                                "message": {"role": "user", "content": [{"tool_use_id": "t1", "type": "tool_result",
                                                                         "content": "Command running in background with ID: bg1"}]}}) + "\n")
        with self.open_store(write=True) as s:
            s.data["prefix"] = "TST"
            t = s.new("Background build", "", "worktree")
            t.update(state="running", started=self.clock - 300_000,
                     agents=[{"address": "w1", "at": self.clock - 300_000, "log": log}])
            s.dirty = True
        idle_w1 = [{"address": "w1", "state": "live", "status": {"state": "idle"}, "status_ms": self.clock - 60_000,
                    "foreground": {"name": "claude"}}]
        with self.open_store() as s:
            res = self.stage.resolve_stage(s.find("1"), idle_w1, store=s)
            self.assertEqual((res.stage, res.inbox, res.why), ("working", False, "Waiting on a background job"))

    def test_ready_stages_owner_gate_vs_ci_vs_blocked_vs_fable(self):
        with self.open_store(write=True) as s:
            s.data["prefix"] = "TST"
            t_gate = s.new("Ready gate", "", "worktree")
            t_gate.update(state="ready")
            t_ci = s.new("Ready ci", "", "worktree")
            t_ci.update(state="ready", auto_merge="checks", pr={"number": 2, "wait": "Checks running"})
            t_conflict = s.new("Ready conflict", "", "worktree")
            t_conflict.update(state="ready", attention="It conflicts with the base branch.", attention_at=self.clock - 60_000)
            t_fable = s.new("Design waiting Fable", "", "worktree")
            t_fable.update(state="ready", set={"review_kind": "design", "auto_accept": "on"})
            s.dirty = True

        with self.open_store() as s:
            r1 = self.stage.resolve_stage(s.find("1"), [], store=s)
            self.assertEqual((r1.stage, r1.inbox), ("owner_gate", True))
            r2 = self.stage.resolve_stage(s.find("2"), [], store=s)
            self.assertEqual((r2.stage, r2.inbox), ("ci", False))
            r3 = self.stage.resolve_stage(s.find("3"), [], store=s)
            self.assertEqual((r3.stage, r3.inbox), ("blocked_or_failed", False))
            r_fable = self.stage.resolve_stage(s.find("4"), [], store=s)
            self.assertEqual((r_fable.stage, r_fable.inbox), ("working", False))

    def test_dependencies_and_queue_stages(self):
        with self.open_store(write=True) as s:
            s.data["prefix"] = "TST"
            t1 = s.new("First", "", "worktree")
            t1.update(state="queued")
            t2 = s.new("Second", "", "worktree")
            t2.update(state="queued", after=[t1["id"]])
            t3 = s.new("Failed start", "", "worktree")
            t3.update(state="queued", after=[t1["id"]], attention="Couldn't start: missing CLI")
            s.dirty = True

        with self.open_store() as s:
            r1 = self.stage.resolve_stage(s.find("1"), [], store=s)
            self.assertEqual((r1.stage, r1.inbox), ("queued", False))
            r2 = self.stage.resolve_stage(s.find("2"), [], store=s)
            self.assertEqual((r2.stage, r2.inbox, r2.why), ("waiting_on_dep", False, "Waiting on TST-1"))
            r3 = self.stage.resolve_stage(s.find("3"), [], store=s)
            self.assertEqual((r3.stage, r3.inbox), ("blocked_or_failed", False))

        with self.open_store(write=True) as s:
            s.find("1")["state"] = "done"
            s.dirty = True
        with self.open_store() as s:
            r2_after = self.stage.resolve_stage(s.find("2"), [], store=s)
            self.assertEqual((r2_after.stage, r2_after.inbox), ("queued", False))

    def test_idle_and_stopped_are_not_in_inbox(self):
        with self.open_store(write=True) as s:
            s.data["prefix"] = "TST"
            t_idle = s.new("Quiet worker", "", "worktree")
            t_idle.update(state="running", started=self.clock - 120_000, agents=[{"address": "w1", "at": self.clock - 120_000}])
            t_stopped = s.new("Stopped worker", "", "worktree")
            t_stopped.update(state="running", started=self.clock - 120_000, agents=[{"address": "w2", "at": self.clock - 120_000}])
            s.dirty = True

        idle_w1 = [{"address": "w1", "state": "live", "status": {"state": "idle"}, "status_ms": self.clock - 60_000,
                    "foreground": {"name": "claude"}}]
        with self.open_store() as s:
            r_idle = self.stage.resolve_stage(s.find("1"), idle_w1, store=s)
            self.assertEqual((r_idle.stage, r_idle.inbox), ("idle", False))
            r_stop = self.stage.resolve_stage(s.find("2"), [], store=s)
            self.assertEqual((r_stop.stage, r_stop.inbox), ("blocked_or_failed", False))

    def test_inbox_and_cli_show_only_owner_waiting_rows(self):
        with self.open_store(write=True) as s:
            s.data["prefix"] = "TST"
            t_gate = s.new("Needs accept", "", "worktree")
            t_gate.update(state="ready")
            t_ask = s.new("Needs answer", "", "worktree")
            t_ask.update(state="running", started=self.clock - 120_000, agents=[{"address": "w1", "at": self.clock - 120_000}])
            t_ci = s.new("Running CI", "", "worktree")
            t_ci.update(state="ready", auto_merge="checks", pr={"number": 3, "wait": "Checks running"})
            t_dep = s.new("Blocked on dep", "", "worktree")
            t_dep.update(state="queued", after=[t_ci["id"]])
            t_idle = s.new("Idle worker", "", "worktree")
            t_idle.update(state="running", started=self.clock - 120_000, agents=[{"address": "w2", "at": self.clock - 120_000}])
            t_dev = s.new("Device wait", "", "worktree")
            t_dev.update(state="running", started=self.clock - 120_000, device_wait={"at": self.clock - 10_000, "why": "unlock iPhone"},
                         agents=[{"address": "w3", "at": self.clock - 120_000}])
            s.dirty = True
            ask_ref = s.ref(t_ask)

        self.ask.save({"id": "ask002", "kind": "paid-key", "gated": True, "question": "Allow key?",
                       "options": ["Allow", "Deny"], "recommend": "Allow", "task": ask_ref, "to": "coord-1", "at": 1})
        self.live_by_key[f"{self.open_store().slug}#2"] = [
            {"address": "w1", "state": "live", "status": {"state": "working"}, "foreground": {"name": "claude"}}]
        self.live_by_key[f"{self.open_store().slug}#5"] = [
            {"address": "w2", "state": "live", "status": {"state": "idle"}, "status_ms": self.clock - 60_000,
             "foreground": {"name": "claude"}}]
        self.live_by_key[f"{self.open_store().slug}#6"] = [
            {"address": "w3", "state": "live", "status": {"state": "idle"}, "status_ms": self.clock - 60_000,
             "foreground": {"name": "claude"}}]

        def run_cli(*argv):
            out = io.StringIO()
            with mock.patch.object(sys, "argv", ["herdr-task", *argv]), contextlib.redirect_stdout(out):
                rc = self.cli.main()
            return rc, out.getvalue()

        with mock.patch("herdr_task.commands.queue_reason", lambda s, t: "after TST-3"):
            rc, raw = run_cli("inbox", "--folder", self.repo, "--json")
            self.assertEqual(rc, 0)
            data = json.loads(raw)
            # Only owner_answer (TST-2, TST-6) and owner_gate (TST-1) appear, in table order!
            self.assertEqual([(x["ref"], x["stage"], x["inbox"]) for x in data["tasks"]], [
                ("TST-2", "owner_answer", True),
                ("TST-6", "owner_answer", True),
                ("TST-1", "owner_gate", True),
            ])

            rc, raw_list = run_cli("list", "--folder", self.repo, "--inbox", "--json")
            self.assertEqual(rc, 0)
            self.assertEqual([x["ref"] for x in json.loads(raw_list)["tasks"]], ["TST-2", "TST-6", "TST-1"])

            rc, txt = run_cli("inbox", "--folder", self.repo)
            self.assertEqual(rc, 0)
            self.assertIn("TST-2", txt)
            self.assertIn("TST-6", txt)
            self.assertIn("TST-1", txt)
            self.assertNotIn("TST-3", txt)
            self.assertNotIn("TST-4", txt)
            self.assertNotIn("TST-5", txt)

            rc, show_raw = run_cli("show", "TST-4", "--folder", self.repo, "--json")
            self.assertEqual(rc, 0)
            show_v = json.loads(show_raw)
            self.assertEqual((show_v["stage"], show_v["inbox"], show_v["why_waiting"]),
                             ("waiting_on_dep", False, "Waiting on TST-3"))

        # Empty inbox prints plain message.
        self.ask.save({"id": "ask002", "kind": "paid-key", "gated": True, "question": "Allow key?",
                       "options": ["Allow", "Deny"], "recommend": "Allow", "task": ask_ref, "to": "coord-1", "at": 1, "answer": "Allow"})
        with self.open_store(write=True) as s:
            s.find("1")["state"] = "done"
            s.find("6").pop("device_wait", None)
            s.dirty = True
        rc, empty_txt = run_cli("inbox", "--folder", self.repo)
        self.assertEqual((rc, empty_txt.strip()), (0, "Inbox is empty."))

        # stage.inbox_tasks and inbox --all --json agree and stay empty when nothing waits on the owner.
        with self.open_store() as s:
            self.assertEqual(self.stage.inbox_tasks(s), [])
            f3 = self.automations.task_facts(s, s.find("3"))
            self.assertEqual((f3["stage"], f3["inbox"]), ("ci", False))
        rc, all_raw = run_cli("inbox", "--all", "--json")
        self.assertEqual((rc, json.loads(all_raw)["tasks"]), (0, []))
