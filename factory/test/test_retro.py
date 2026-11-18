"""SDF-49: the retro round. Review that does not converge starts one read-only Fable session; its
<retro> tail becomes task.retro and the engine acts on the verdict (SHIP, FIX, REDESIGN)."""
import home_guard  # noqa: F401
import argparse
import json
import os
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import test_herdr_task as T  # noqa: E402
ROOT = T.ROOT

TAIL = {"verdict": "SHIP", "majors": [], "cause": "patch-shaped-fix", "second_cause": "reviewer-narrow",
        "evidence": ["round 3: one more loop case"], "coordinator_change": "Name the class in the brief.",
        "worker_change": "Check the written graph, not each case.", "replan": "none", "replan_note": "",
        "worker_model": "keep", "reviewer": "class-pass", "confidence": "high"}
MAJOR = {"file": "plan.py", "line": 7, "what": "a loop through a split child passes", "repro": "plan amend on a split"}


def screen(rec, width=60):
    """A reviewer's screen: the prompt's own example tail first, then the answer's tail wrapped as a terminal does."""
    body = "<retro>" + json.dumps(rec) + "</retro>"
    import textwrap
    lines = textwrap.wrap(body, width, break_on_hyphens=False)  # as Claude Code wraps: at spaces
    return '  <retro>{"verdict":"SHIP|FIX|REDESIGN"}</retro>\n⏺ Findings: none.\n' + "\n".join("  " + x for x in lines)


class Retro(unittest.TestCase):
    git, change, env = T.GateReview.git, T.GateReview.change, T.GateReview.env

    def setUp(self):
        T.GateReview.setUp(self)
        import herdr_task.retro as retro
        import herdr_task.budget as budget
        self.retro, self.budget = retro, budget
        self.task["set"] = {"review": "on", "review_rounds": 2}  # worker-run rounds, no gate folder
        self.task["head"] = self.head
        self.screen, self.asks = "", []
        self.reviewer_term = {"state": "live", "status": {"state": "idle"}, "turn": 1}
        for mod, name, value in ((retro, "hc_json", lambda *a: self.reviewer_term),
                                 (self.commands, "screen_of", lambda address, lines: self.screen),
                                 (self.commands, "workers", lambda s, t: [])):
            p = mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)

    def rounds(self, *rows):
        self.task["reviews"] = [{"round": i + 1, "head": self.head, "result": "report", "verdict": v, "majors": n,
                                 "report": f"round {i + 1}", "at": 10 + i} for i, (v, n) in enumerate(rows)]

    def tick(self, rec=None):
        if rec is not None:
            self.screen = screen(rec)
        self.retro.retro_pass(self.store, self.task)

    def recap(self, decide):
        import herdr_task.store as store
        import herdr_task.launch as launch
        import herdr_task.output as output
        with mock.patch.object(store, "open_store", lambda args, write=True: self.store), \
                mock.patch.object(launch, "caller_labels", lambda: {}), mock.patch.object(output, "say", lambda *a: None):
            self.budget.cmd_recap(argparse.Namespace(id="SHEP-13", json=False, folder=None, decide=decide, why="w", small=False))

    def started(self):
        self.rounds(("pass", 0), ("changes", 2), ("changes", 1))
        self.tick()
        return self.task["retro_run"]

    def test_template_is_the_bundled_file_and_fills_every_slot(self):
        with open(os.path.join(ROOT, "templates", "retro.md"), encoding="utf-8") as f:
            self.assertEqual(f.read(), self.retro.RETRO_MD)
        self.assertLessEqual(len(self.retro.RETRO_MD.splitlines()), 40)
        self.started()
        prompt = self.runs[-1][-1]
        import re
        self.assertEqual(re.findall(r"\{[a-z_]+\}", prompt), [])
        self.assertIn("SHEP-13 \"Gate\"", prompt)
        self.assertIn("3 rounds, majors in the last 2", prompt)
        self.assertIn("Make the gate.", prompt)

    def test_trigger_starts_one_retro_and_a_size_continue_does_not_hide_it(self):
        self.rounds(("pass", 0), ("changes", 2))
        self.tick()
        self.assertNotIn("retro_run", self.task)
        self.task["reviews"].append({"round": 3, "head": self.head, "result": "report", "verdict": "changes", "majors": 1, "at": 30})
        self.task["recaps"] = [{"decision": "continue", "at": 20, "reasons": ["change is 900 lines, over twice its size (300)"]}]
        self.reviewer_term = {"state": "live", "status": {"state": "working"}}
        self.tick()
        self.assertIn(self.budget.NON_CONVERGENCE, self.task["recap"]["reasons"])
        n = len(self.runs)
        self.assertIn("claude-fable-5-2", " ".join(self.runs[-1]))
        self.tick()  # still at work: no second session
        self.assertEqual(len(self.runs), n)

    def test_folder_task_draws_the_retro_too(self):
        self.task.update(mode="here", worktree=None)
        self.assertTrue(self.started())
        self.assertIn("recap", self.task)

    def test_tail_parsed_into_task_retro_and_ship_counts_as_the_passing_round(self):
        rn = self.started()
        self.tick(TAIL)
        r = self.task["retro"]
        self.assertEqual((r["verdict"], r["cause"], r["head"], r["by"]), ("SHIP", "patch-shaped-fix", self.head, rn["address"]))
        self.assertNotIn("recap", self.task)
        self.assertEqual(self.commands.gate_need(self.store, self.task), 1)
        self.assertEqual(self.commands.gate_passes(self.task, self.head), 1)
        self.assertIsNone(self.commands.review_refusal(self.store, self.task, self.wt))
        self.assertFalse(self.budget.triggers(self.store, self.task))
        self.assertIn("passed", self.told[-1])
        n = len(self.runs)
        self.tick()  # one retro per task
        self.assertEqual(len(self.runs), n)

    def test_a_bad_tail_is_a_failed_round_and_keeps_the_retro(self):
        self.started()
        self.tick({**TAIL, "cause": "bad-luck"})
        self.assertNotIn("retro", self.task)
        self.assertIn("outside the set", self.task["retro_failures"][0]["why"])
        self.assertIn("retro_run", self.task)  # tried again
        self.screen = "no tail at all"
        self.tick()
        self.tick()
        self.assertEqual(len(self.task["retro_failures"]), 3)
        self.assertIn("Retro round failed 3 times", self.task["attention"])

    def test_fix_is_one_bounded_fix_checked_by_fable_then_capped(self):
        self.started()
        self.tick({**TAIL, "verdict": "FIX", "majors": [MAJOR]})
        self.assertEqual(self.task["recaps"][-1]["small"], True)
        self.assertIn("Check the written graph", self.told[-1])
        self.assertTrue(self.retro.fixing(self.task))
        self.assertEqual(self.commands.gate_need(self.store, self.task), 1)
        from herdr_task.settings import fable_due
        self.assertTrue(fable_due(self.store, self.task))
        self.task["review_runs"] = [{"round": 1, "head": self.head, "address": "fable-1", "model": "claude-fable-5-2", "at": 1}]
        self.task["reviews"].append({"round": 5, "head": self.head, "by": "fable-1", "started_by": "herdr-task",
                                     "verdict": "changes", "majors": 1, "at": self.task["retro"]["at"] + 1})
        self.tick()
        self.assertIn(self.retro.RETRO_CAP, self.task["recap"]["reasons"])
        self.assertFalse(self.retro.fixing(self.task))
        with self.assertRaisesRegex(self.commands.Fail, "plan amend --reason retro:SHEP-13"):
            self.recap("continue")

    def test_redesign_refuses_continue_and_leaves_split(self):
        self.started()
        self.tick({**TAIL, "verdict": "REDESIGN", "majors": [MAJOR], "cause": "task-too-big", "replan": "split"})
        self.assertIn(self.retro.RETRO_REDESIGN, self.task["recap"]["reasons"])
        with self.assertRaisesRegex(self.commands.Fail, "REDESIGN"):
            self.recap("continue")
        self.recap("split")
        self.assertEqual(self.task["recaps"][-1]["decision"], "split")

    def test_low_confidence_is_stored_not_acted_on(self):
        self.started()
        self.tick({**TAIL, "confidence": "low"})
        self.assertEqual(self.task["retro"]["confidence"], "low")
        self.assertIn("recap", self.task)
        self.assertEqual(self.told, [])
        self.assertIsNone(self.retro.gate_rounds(self.task))

    def test_split_children_carry_the_change_and_a_second_retro_asks_the_owner(self):
        from herdr_task.launch import task_text
        self.task["retro_parent"] = {"task": "SHEP-9", "worker_change": "Check the written graph."}
        self.assertIn("From the retro of SHEP-9, which this task splits: Check the written graph.", task_text(self.task))
        self.rounds(("pass", 0), ("changes", 2), ("changes", 1))
        import herdr_task.ask as ask
        with mock.patch.object(ask, "cmd_ask", lambda a: self.asks.append(a) or {"id": "ab12"}):
            self.tick()
            self.tick()
        self.assertEqual(len(self.asks), 1)
        self.assertEqual((self.asks[0].kind, self.asks[0].task), ("decision", "SHEP-13"))
        self.assertNotIn("retro_run", self.task)
        self.assertIn("recap", self.task)

    def test_each_outcome_wakes_the_coordinator_once(self):
        from contextlib import nullcontext
        from herdr_task import autopilot
        self.store.slug = "test"
        self.started()
        self.tick({**TAIL, "verdict": "FIX", "majors": [MAJOR]})
        state = {"folder": "/tmp"}
        with mock.patch.object(autopilot, "store_files", return_value=["store"]), \
                mock.patch.object(autopilot, "read_list", return_value={"folder": "/tmp"}), \
                mock.patch.object(autopilot, "locate", return_value={}), \
                mock.patch.object(autopilot, "Store", return_value=nullcontext(self.store)), \
                mock.patch.object(autopilot, "events_in", return_value=[]), mock.patch.object(os.path, "isdir", return_value=True):
            first = autopilot.events_for("/tmp", state, [])
            self.assertEqual(first, ["event:retro SHEP-13 FIX, cause patch-shaped-fix, replan none, confidence high: "
                                     "Name the class in the brief."])
            self.assertEqual(autopilot.events_for("/tmp", state, []), [])
            self.task["retro_capped"] = 1  # the FIX cap: the coordinator hears again
            self.assertIn("FIX (FIX cap)", autopilot.events_for("/tmp", state, [])[0])
        a = {"pending": {"reasons": [f"event:x{i}" for i in range(6)], "at": 1}}
        for i in range(7):
            autopilot.queue(a, f"event:retro T-{i}")
        self.assertEqual(len([x for x in a["pending"]["reasons"] if x.startswith("event:retro")]), 7)

    def test_replay_prompts_fill_every_slot_and_recorded_answers_parse(self):
        import re
        import replay_retro
        for ref in replay_retro.SITUATIONS:
            self.assertEqual(re.findall(r"\{[a-z_]+\}", replay_retro.prompt(ref).split("<retro>")[0]), [], ref)
            out = replay_retro.recorded(ref)
            if out is not None:
                rec, why = self.retro.parse_tail(out)
                self.assertIsNone(why, ref)

    def test_review_findings_round_1(self):
        """Codex round 1: a folder task is ticked; a moved head, bad majors or review off never use the retro;
        a FIX after a spent small continue is the cap; a split after a retro needs its reason."""
        from herdr_task.tick import needs_tick
        self.task.update(mode="here", worktree=None)
        self.rounds(("pass", 0), ("changes", 2), ("changes", 1))
        self.assertTrue(needs_tick({"tasks": [self.task]}))
        self.task.update(mode="worktree", worktree=self.wt)
        self.tick()
        self.change("moved on")
        self.tick(TAIL)
        self.assertNotIn("retro", self.task)
        self.assertIn("checkout moved on", self.task["retro_failures"][-1]["why"])
        self.assertIn("majors is not", self.retro.parse_tail(screen({**TAIL, "verdict": "FIX", "majors": [0]}))[1])
        self.task["set"]["review"] = "off"
        self.assertFalse(self.retro.retro_due(self.task))
        self.task["set"]["review"] = "on"
        self.task.pop("retro_run", None)
        self.task["recaps"] = [{"decision": "continue", "small": True, "at": 1, "reasons": ["3 failed CI runs"]}]
        self.tick()
        self.tick({**TAIL, "verdict": "FIX", "majors": [MAJOR]})
        self.assertTrue(self.task["retro_capped"])
        self.assertIn(self.retro.RETRO_CAP, self.task["recap"]["reasons"])
        self.assertEqual(self.told, [])

    def test_a_split_after_a_retro_needs_its_reason(self):
        import herdr_task.plan as plan
        self.task["retro"] = dict(TAIL)
        fake = mock.Mock(problems=[], tasks=[({"key": "a", "split_of": "SHEP-13"}, "m1")], find=lambda ref: self.task)
        with mock.patch.object(plan, "Plan", return_value=fake), mock.patch.object(plan, "load", return_value={}), \
                mock.patch("herdr_task.store.open_store", lambda args, write=True: self.store):
            with self.assertRaisesRegex(self.commands.Fail, "retro:SHEP-13"):
                plan.cmd_plan(argparse.Namespace(plan="amend", reason="smaller", file="p.json", check=True, json=True, folder=None))

    def test_review_findings_round_2(self):
        """Codex round 2: contradictory tails fail; SHIP keeps an unrelated CI recap; a folder FIX needs its own
        passing round after the retro; new or drop after REDESIGN needs the replan on record."""
        self.assertIn("SHIP verdict names majors", self.retro.parse_tail(screen({**TAIL, "majors": [MAJOR]}))[1])
        self.assertIn("file and what", self.retro.parse_tail(screen({**TAIL, "verdict": "FIX", "majors": [{}]}))[1])
        self.started()
        self.task["recap"]["reasons"].append("3 failed CI runs")
        self.tick(TAIL)
        self.assertEqual(self.task["recap"]["reasons"], ["3 failed CI runs"])
        self.assertFalse(self.budget.review_nonconverging(self.task))

    def test_folder_fix_needs_its_own_passing_round(self):
        self.task.update(mode="here", worktree=None)
        self.task["review_gate"] = True
        self.started()
        self.tick({**TAIL, "verdict": "FIX", "majors": [MAJOR]})
        self.assertEqual(self.task["review_rounds_added"], 1)
        self.assertIn("one more round is added", self.told[-1])
        self.assertIn("isn't reviewed", self.commands.review_refusal(self.store, self.task, None))
        row = {"round": 5, "result": "report", "verdict": "pass", "majors": 0, "at": self.task["retro"]["at"] + 1, "head": "0" * 40}
        self.task["reviews"].append(row)
        if self.store.loc.get("kind") == "repo":  # a round on an older commit is no review of the fix
            self.assertIn("isn't reviewed", self.commands.review_refusal(self.store, self.task, None))
        row["head"] = self.git(self.store.loc["folder"], "rev-parse", "HEAD")
        self.assertIsNone(self.commands.review_refusal(self.store, self.task, None))

    def test_new_after_redesign_needs_the_replan_on_record(self):
        self.started()
        self.tick({**TAIL, "verdict": "REDESIGN", "majors": [MAJOR], "cause": "design-needed-first"})
        self.store.data["plan_log"] = [{"at": 1, "action": "apply"}]
        with self.assertRaisesRegex(self.commands.Fail, "record the replan first"):
            self.recap("new")
        self.store.data["plan_log"].append({"at": self.task["retro"]["at"], "action": "amend", "reason": "retro:SHEP-13"})
        self.recap("new")

    def test_review_findings_round_3(self):
        """Codex round 3: a FIX is passed only by Fable's round after it; retro rows use no worker round; a FIX
        beside a CI stop leaves the bound to the coordinator's continue."""
        self.rounds(("pass", 0), ("changes", 2), ("changes", 1))
        self.task["reviews"][0]["started_by"] = "herdr-task"  # an earlier pass on this same head
        self.tick()
        self.task["recap"]["reasons"].append("3 failed CI runs")
        self.tick({**TAIL, "verdict": "FIX", "majors": [MAJOR]})
        self.assertEqual(self.commands.gate_passes(self.task, self.head), 0)
        self.assertEqual(self.commands.rounds_used(self.task), 3)
        self.assertEqual((self.task["recap"]["reasons"], self.told), (["3 failed CI runs"], []))
        self.recap("continue")
        self.assertTrue(self.task["recaps"][-1]["small"])
        self.assertNotIn("retro_fix_pending", self.task)

    def test_folder_retro_reads_from_where_the_task_started(self):
        self.task.update(mode="here", worktree=None, folder_start=self.git(self.wt, "rev-parse", "HEAD~1"))
        self.store.loc["folder"] = self.wt
        self.started()
        self.assertIn(f"Diff: {self.task['folder_start'][:12]}..", self.runs[-1][-1])

    def test_review_findings_round_4(self):
        """Codex round 4: a verdict after the coordinator decided is stored, not acted on; the FIX cap keeps a CI stop;
        a judgement task's earlier Fable pass doesn't pass a FIX."""
        self.started()
        self.recap("new")
        self.tick(TAIL)
        self.assertEqual((self.task["retro"]["acted"], self.told), (False, []))
        self.assertIsNone(self.retro.gate_rounds(self.task))
        self.task["retro"] = {**TAIL, "verdict": "FIX", "majors": [MAJOR], "at": 100, "head": self.head}
        self.task["recap"] = {"at": 1, "reasons": ["3 failed CI runs"], "title": "Recap required"}
        self.task["review_runs"] = [{"address": "fable-1", "model": "claude-fable-5-2", "head": self.head, "round": 1, "at": 1}]
        self.task["reviews"].append({"round": 9, "head": self.head, "by": "fable-1", "started_by": "herdr-task",
                                     "verdict": "pass", "majors": 0, "at": 50})
        from herdr_task.settings import accept_bar
        self.task["set"]["review_kind"] = "doc"
        self.assertEqual(accept_bar(self.store, self.task)[0], None)
        self.task["reviews"].append({"round": 10, "head": self.head, "by": "fable-1", "started_by": "herdr-task",
                                     "verdict": "changes", "majors": 1, "at": 150})
        self.assertTrue(self.retro.cap(self.store, self.task))
        self.assertEqual(self.task["recap"]["reasons"], ["3 failed CI runs", self.retro.RETRO_CAP])

    def test_a_folder_fix_that_fails_its_round_is_the_cap(self):
        self.task.update(mode="here", worktree=None)
        self.started()
        self.tick({**TAIL, "verdict": "FIX", "majors": [MAJOR]})
        self.task["reviews"].append({"round": 5, "result": "report", "verdict": "changes", "majors": 1, "at": self.task["retro"]["at"] + 1})
        self.tick()
        self.assertIn(self.retro.RETRO_CAP, self.task["recap"]["reasons"])

    def test_review_findings_round_5(self):
        """Codex round 5: a retro row never closes a Fable run; CI failures that came during the recap survive SHIP;
        a superseded REDESIGN restricts nothing."""
        self.started()
        self.task["ci_runs"] = [{"sha": self.head, "run": str(i), "failed": True, "at": 40 + i} for i in range(3)]
        self.tick(TAIL)
        self.assertIn("3 failed CI runs", self.task["recap"]["reasons"])
        self.task["review_runs"] = [{"round": len(self.task["reviews"]), "head": self.head, "address": "fable-1",
                                     "model": "claude-fable-5-2", "at": 1}]
        self.assertEqual([r["address"] for r in self.commands.open_runs(self.task)], ["fable-1"])
        self.task["retro"] = {**TAIL, "verdict": "REDESIGN", "acted": False}
        self.assertIsNone(self.retro.blocks_continue(self.task))

    def test_folder_ship_needs_a_round_on_the_current_commit_and_a_folder_fix_is_bounded(self):
        self.task.update(mode="here", worktree=None, review_gate=True)
        self.store.loc.update(folder=self.wt, kind="repo")
        self.started()
        self.tick(TAIL)
        self.assertIsNone(self.commands.review_refusal(self.store, self.task, None))
        old = self.task["retro"]["head"]
        self.change("x\n" * 80)
        self.assertIsNotNone(self.commands.review_refusal(self.store, self.task, None))
        self.task["retro"] = {**TAIL, "verdict": "FIX", "majors": [MAJOR], "head": old, "at": 10 ** 13}
        self.assertTrue(self.retro.cap(self.store, self.task))

    def test_outside_check_findings(self):
        """Outside Fable check of e40cd3b2: the retro terminal can't record a review verdict, nothing does while a recap
        is open; a judgement task's SHIP is only the code pass; the tail must end the last message on a real screen."""
        self.started()
        self.assertNotIn(" review:*)", " ".join(self.runs[-1]))  # launched with no verdict verb
        args = argparse.Namespace(id="SHEP-13", json=True, folder=None, passed=True, changes=None, changes_file=None, notes=None)
        self.env("HC_TERMINAL_ID", self.task["retro_run"]["address"])
        with self.assertRaisesRegex(self.commands.Fail, "retro round records no verdict"):
            self.commands.cmd_review(args)
        self.env("HC_TERMINAL_ID", "other")
        with self.assertRaisesRegex(self.commands.Fail, "recap open"):
            self.commands.cmd_review(args)
        self.tick(TAIL)
        self.task["set"]["review_kind"] = "design"
        self.assertEqual(self.commands.gate_need(self.store, self.task), 2)
        from herdr_task.settings import accept_bar
        self.assertEqual(accept_bar(self.store, self.task)[0], None)
        with open(os.path.join(ROOT, "test", "fixtures", "fac78_claude_reviewer_tail.txt"), encoding="utf-8") as f:
            real = f.read().splitlines()
        cut = next(i for i, x in enumerate(real) if x.startswith("✻"))
        tail = screen(TAIL).splitlines()[1:]
        self.assertEqual(self.retro.parse_tail("\n".join(real[:cut] + tail + real[cut:]))[0]["verdict"], "SHIP")
        late = "\n".join(real[:cut] + tail + ["⏺ One more thought after the tail."] + real[cut:])
        self.assertIn("not at the end", self.retro.parse_tail(late)[1])


if __name__ == "__main__":
    unittest.main()
