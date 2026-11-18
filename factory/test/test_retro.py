"""SDF-49: the retro round. Review that does not converge starts one read-only Fable session; its
<retro> tail becomes task.retro and the engine acts on the verdict (SHIP, FIX, REDESIGN)."""
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
    lines = [body[i:i + width] for i in range(0, len(body), width)]
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
            self.assertEqual(autopilot.events_for("/tmp", state, []), ["event:retro SHEP-13"])
            self.assertEqual(autopilot.events_for("/tmp", state, []), [])
            self.task["retro_capped"] = 1  # the FIX cap: the coordinator hears again
            self.assertEqual(autopilot.events_for("/tmp", state, []), ["event:retro SHEP-13"])

    def test_replay_prompts_fill_every_slot_and_recorded_answers_parse(self):
        import re
        import replay_retro
        for ref in replay_retro.SITUATIONS:
            self.assertEqual(re.findall(r"\{[a-z_]+\}", replay_retro.prompt(ref).split("<retro>")[0]), [], ref)
            out = replay_retro.recorded(ref)
            if out is not None:
                rec, why = self.retro.parse_tail(out)
                self.assertIsNone(why, ref)


if __name__ == "__main__":
    unittest.main()
