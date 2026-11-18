"""After-the-fact checks on factory task facts."""
import home_guard  # noqa: F401
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(__file__)), "mods", "tasks", "bin"))
from herdr_task.stage import resolve_stage


class DriftTests(unittest.TestCase):
    def test_red_check_is_a_second_fact_in_every_stage(self):
        t = {"state": "done", "pr": {"head_sha": "abc", "failed_checks": ["test"]}}
        stage = resolve_stage(t)
        self.assertEqual(stage.stage, "done")
        self.assertEqual(stage.failed_checks, ("test",))

    def test_waiting_rows_name_the_party(self):
        self.assertEqual(resolve_stage({"state": "todo"}, deps=["TST-3"]).waits_on, ["TST-3"])
        self.assertEqual(resolve_stage({"state": "ready"}).waits_on, "owner")
        self.assertEqual(resolve_stage({"state": "queued"}).waits_on, "coordinator")
        self.assertFalse(resolve_stage({"state": "queued"}).inbox)

    def test_plain_stage_row_shows_wait_and_failure(self):
        from herdr_task.output import inbox_line, line
        row = {"stage": "owner_gate", "waits_on": "owner", "failed_checks": ["test"],
               "state": "ready", "status": "ready", "ref": "TST-1", "title": "Ship"}
        for render in (line, inbox_line):
            self.assertIn("waiting on owner · failing checks: test", render(row))


class Comparisons(unittest.TestCase):
    def setUp(self):
        from types import SimpleNamespace
        from unittest.mock import patch
        from herdr_task import drift
        self.drift = drift
        self.now = 10_000_000
        self.store = SimpleNamespace(data={"tasks": []}, loc={"folder": "/tmp"}, dirty=False)
        self.clock = patch.object(drift, "now", lambda: self.now)
        self.clock.start()
        self.addCleanup(self.clock.stop)
        self.t = {"id": 1, "state": "ready", "head": "abc", "ready_at": self.now, "pr": {"repo": "a/b", "number": 1}}
        self.v = {"state": "OPEN", "headRefOid": "abc", "statusCheckRollup": []}

    def inspect(self):
        self.drift.inspect(self.store, self.t, self.v)
        return self.t["pr"]["drift"]

    def test_done_unmerged_and_agreement(self):
        self.t["state"] = "done"
        self.assertIn("done", self.inspect())
        self.v["state"] = "MERGED"
        self.assertNotIn("done", self.inspect())

    def test_stored_head_mismatch_and_agreement(self):
        self.v["headRefOid"] = "other"
        self.assertIn("head", self.inspect())
        self.v["headRefOid"] = "abc"
        self.assertNotIn("head", self.inspect())

    def test_accepted_open_after_half_hour(self):
        self.t["accepted_at"] = self.now - self.drift.WAIT_MS
        self.assertIn("unmerged", self.inspect())
        self.v["state"] = "MERGED"
        self.assertNotIn("unmerged", self.inspect())

    def test_red_outside_ci_reruns_once_and_clears(self):
        from unittest.mock import patch
        self.t["state"] = "done"
        self.t["accepted_at"] = self.now
        self.v["statusCheckRollup"] = [{"name": "test", "conclusion": "FAILURE", "detailsUrl": "https://example.test/actions/runs/123"}]
        with patch.object(self.drift, "gh", return_value=(True, "", "")) as gh:
            self.assertIn("red", self.inspect())
            self.inspect()
            self.assertEqual(gh.call_count, 1)
        self.v["statusCheckRollup"][0]["conclusion"] = "SUCCESS"
        self.assertNotIn("red", self.inspect())

    def test_red_retry_waits_for_pending_run_and_only_records_success(self):
        from unittest.mock import patch
        self.t.update(state="done", accepted_at=self.now)
        url = "https://example.test/actions/runs/4"
        self.v["statusCheckRollup"] = [
            {"name": "test", "conclusion": "FAILURE", "detailsUrl": url},
            {"name": "lint", "status": "IN_PROGRESS", "detailsUrl": url},
        ]
        with patch.object(self.drift, "gh", side_effect=[(False, "", "retry later"), (True, "", "")]) as gh:
            self.assertIn("red", self.inspect())
            gh.assert_not_called()
            self.v["statusCheckRollup"][1].update(status="COMPLETED", conclusion="SUCCESS")
            self.inspect()
            self.inspect()
            self.inspect()
            self.assertEqual(gh.call_count, 2)

    def test_ready_green_hold_waits_half_hour(self):
        self.v["statusCheckRollup"] = [{"name": "test", "conclusion": "SUCCESS"}]
        self.t["auto_merge"] = "checks"
        self.store.data["main_watch"] = {"main": {"red": "abcdef0123"}}
        self.assertNotIn("hold", self.inspect())
        self.now += self.drift.WAIT_MS
        self.assertIn("main is red", self.inspect()["hold"])
        self.store.data["main_watch"] = {}
        self.assertNotIn("hold", self.inspect())

    def test_local_base_ahead_and_agreement(self):
        from unittest.mock import patch
        self.store.data["tasks"] = [self.t]
        with patch.object(self.drift, "git", return_value=(True, "1", "")):
            self.drift.base_drift(self.store)
        self.assertEqual(self.store.data["drift_bases"], ["main"])
        with patch.object(self.drift, "git", return_value=(True, "0", "")):
            self.drift.base_drift(self.store)
        self.assertEqual(self.store.data["drift_bases"], [])

    def test_drift_wakes_coordinator_once_even_without_other_events(self):
        from contextlib import nullcontext
        from unittest.mock import patch
        from herdr_task import autopilot
        self.store.slug = "test"
        self.store.ref = lambda t: "TST-1"
        self.store.data["tasks"] = [self.t]
        self.t["pr"]["drift"] = {"red": "CI red on a reviewed head: flaky test or real?"}
        state = {"folder": "/tmp"}
        with patch.object(autopilot, "store_files", return_value=["store"]), \
             patch.object(autopilot, "read_list", return_value={"folder": "/tmp"}), \
             patch.object(autopilot, "locate", return_value={}), \
             patch.object(autopilot, "Store", return_value=nullcontext(self.store)), \
             patch.object(autopilot, "events_in", return_value=[]):
            reasons = autopilot.events_for("/tmp", state, [])
            self.assertEqual(reasons, ["event:drift TST-1 CI red on a reviewed head: flaky test or real?"])
            self.assertEqual(autopilot.events_for("/tmp", state, []), [])

    def test_red_in_ci_stage_stays_quiet(self):
        self.t["auto_merge"] = "checks"
        self.v["statusCheckRollup"] = [{"name": "test", "conclusion": "FAILURE"}]
        self.assertNotIn("red", self.inspect())

    def test_timeout_in_failed_run_is_not_rerun(self):
        from unittest.mock import patch
        self.t["state"] = "done"
        self.t["accepted_at"] = self.now
        url = "https://example.test/actions/runs/123"
        self.v["statusCheckRollup"] = [{"name": "test", "conclusion": "FAILURE", "detailsUrl": url},
                                        {"name": "slow", "conclusion": "TIMED_OUT", "detailsUrl": url}]
        with patch.object(self.drift, "gh") as gh:
            self.assertIn("red", self.inspect())
            gh.assert_not_called()

    def test_old_accepted_branch_uses_existing_update_path(self):
        from unittest.mock import patch
        self.t["accepted_at"] = self.now - self.drift.WAIT_MS
        with patch("herdr_task.commands.uses_queue", return_value=False), \
             patch("herdr_task.checks.sync_plan", return_value=("full", None)), \
             patch.object(self.drift, "ci_cap_hold", return_value=None), \
             patch.object(self.drift, "pr_behind", return_value=1), \
             patch.object(self.drift, "pr_update_branch") as update:
            self.assertIn("unmerged", self.inspect())
            update.assert_called_once_with(self.t, "a/b", "abc")

    def test_working_new_head_is_not_a_claim_or_reviewed_red(self):
        from unittest.mock import patch
        self.t["state"] = "running"
        self.v["headRefOid"] = "new"
        self.v["statusCheckRollup"] = [{"name": "test", "conclusion": "FAILURE", "detailsUrl": "https://example.test/actions/runs/4"}]
        with patch.object(self.drift, "gh") as gh:
            self.assertNotIn("head", self.inspect())
            self.assertNotIn("red", self.inspect())
            gh.assert_not_called()

    def test_accepted_open_less_than_half_hour_is_quiet(self):
        self.t["accepted_at"] = self.now - self.drift.WAIT_MS + 1
        self.assertNotIn("unmerged", self.inspect())

    def test_stopped_check_reuses_retry_and_respects_timeout(self):
        from unittest.mock import patch
        self.t["accepted_at"] = self.now - self.drift.WAIT_MS
        self.v["statusCheckRollup"] = [{"name": "test", "conclusion": "CANCELLED"}]
        with patch.object(self.drift, "ci_stopped") as retry, patch("herdr_task.commands.uses_queue", return_value=True):
            self.inspect()
            retry.assert_called_once()

    def test_done_audit_is_bounded_and_rotates(self):
        from unittest.mock import patch
        self.store.data["tasks"] = [{"id": i, "state": "done", "pr": {"number": i, "drift_checked": 1}} for i in range(1, 4)]
        def observe(s, t, at):
            t["pr"]["drift_checked"] = at
        with patch.object(self.drift, "drift_pass", side_effect=observe) as probe:
            self.drift.audit_done(self.store, 10)
            self.assertEqual(probe.call_count, 1)
            self.drift.audit_done(self.store, 11)
            self.assertEqual([call.args[1]["id"] for call in probe.call_args_list], [1, 2])

    def test_findings_review_while_worker_fixes_is_quiet(self):
        from unittest.mock import patch
        self.t.update(state="running", reviews=[{"head": "abc", "result": "report"}])
        self.v["statusCheckRollup"] = [{"name": "test", "conclusion": "FAILURE", "detailsUrl": "https://example.test/actions/runs/9"}]
        with patch.object(self.drift, "gh") as gh:
            self.assertNotIn("red", self.inspect())
            gh.assert_not_called()

    def test_held_push_is_not_a_head_mismatch(self):
        self.t["push_held"] = {"reason": "CI capacity"}
        self.v["headRefOid"] = "old"
        self.assertNotIn("head", self.inspect())

    def test_verified_merged_pr_is_not_polled_again(self):
        from unittest.mock import patch
        self.t["state"] = "done"
        self.store.data["tasks"] = [self.t]
        self.v["state"] = "MERGED"
        self.inspect()
        with patch.object(self.drift, "drift_pass") as probe:
            self.drift.audit_done(self.store, self.now)
            probe.assert_not_called()
