"""SDF-11: the role templates' standing rules (moved from test_repo.py), and the built-in worker and reviewer
defaults equal to the bundled templates/*.md, so a clean install gets the same words as an installed file."""
import home_guard  # noqa: F401  (first: every test on a throwaway home, never the real one)
import os
import re
import sys
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
J = lambda *p: os.path.join(ROOT, *p)
sys.path.insert(0, J("mods", "tasks", "bin"))


class Templates(unittest.TestCase):
    def test_the_built_in_defaults_are_the_bundled_files(self):
        import herdr_task.templates as T
        for role, words in (("worker", T.WORKER_MD), ("reviewer", T.REVIEWER_MD)):
            with open(J("templates", f"{role}.md"), encoding="utf-8") as fh:
                self.assertEqual(T.FRONT.sub("", fh.read()).strip(), words.strip(), role)

    def test_an_unedited_default_from_before_the_trim_gets_todays(self):
        import tempfile
        from unittest import mock
        import herdr_task.templates as T
        d = tempfile.mkdtemp()
        with mock.patch.object(T, "TEMPLATES", d):
            for role, old, new in (("worker", T.WORKER_MD_BEFORE_TRIM, T.WORKER_MD), ("reviewer", T.REVIEWER_MD_BEFORE_TRIM, T.REVIEWER_MD)):
                with open(os.path.join(d, f"{role}.md"), "w") as f:
                    f.write(T.TEMPLATE_HEADS[role] + old)
                self.assertEqual(T.instructions(role), new.strip(), role)
                with open(os.path.join(d, f"{role}.md"), "w") as f:
                    f.write(T.TEMPLATE_HEADS[role] + old + "- My own rule.\n")
                self.assertTrue(T.instructions(role).endswith("- My own rule."), role)  # an edited one is the person's

    def test_assistant_decide_yourself_rules(self):
        """FAC-57: templates/assistant.md's Decide yourself section keeps its existing standing rules
        and carries the in-worktree prompt approval rule and PR CI ownership + loop-breaker rule."""
        with open(J("templates", "assistant.md"), encoding="utf-8") as fh:
            text = fh.read()
        sec = text.split("## Decide yourself / Ask only for\n", 1)[1].split("\n## ", 1)[0]
        for words in (
            "review-more",
            "accept <id> --owner-approved",
            '"stop and ask" at a size overrun',
            "hc read <t> --lines 80",
            "hc keys <t>",
            "$TMPDIR",
            "read-only",
            "folder's check",
            "credentials, keychains, settings or other machines goes to the person",
            "Never approve a delete (`rm`)",
            "gh run view <run> --log-failed",
            "gh run rerun <run> --failed",
            "3 failed CI runs",
            "1 hour of CI",
            "Recap required",
            "herdr-task recap --decide",
            "docs/factory.md section 9",
            "Release asks belong before merge, not after",
        ):
            self.assertIn(words, sec)

    def test_assistant_inherited_suite_rule(self):
        """FAC-74: measure an inherited suite's flakiness on the base and turn on retries first; gate a new fork's release workflow."""
        with open(J("templates", "assistant.md"), encoding="utf-8") as fh:
            sec = fh.read().split("## Decide yourself / Ask only for\n", 1)[1].split("\n## ", 1)[0]
        for words in ("Inherited suite (a fork)", "measure its flakiness on the base first", "turn on test retries in CI before tasks start",
                      "a new fork's release workflow stays gated (manual dispatch or tags) until signing is proven"):
            self.assertIn(words, sec)

    def test_assistant_unattended_night_checklist_rule(self):
        with open(J("templates", "assistant.md"), encoding="utf-8") as fh:
            rules = fh.read().split("\nRules:\n", 1)[1].split("\n## ", 1)[0]
        self.assertIn("Before an unattended night, run `herdr-task project checklist` and don't start milestone work while an item is missing.", rules)

    def test_assistant_design_and_doc_review_kind_rule(self):
        with open(J("templates", "assistant.md"), encoding="utf-8") as fh:
            text = fh.read()
        for words in ("review.kind=design|doc", "--set review.kind=design", "--set review.kind=doc",
                      "one review round", "wait in Ready for the person's accept", "never on design or doc tasks"):
            self.assertIn(words, text)

    def test_assistant_checks_apple_agreements_before_a_release(self):
        """FAC-77 (field note 36): an expired Apple agreement is a take-over for the person, never a re-run."""
        with open(J("templates", "assistant.md"), encoding="utf-8") as fh:
            text = fh.read()
        rules = text.split("\nRules:\n", 1)[1].split("\n## ", 1)[0]
        for words in ("Before an iOS release run", "HTTP 403 from App Store Connect", "`asc apps list`",
                      "agreement is missing or has expired", "take-over for the person", "never a re-run"):
            self.assertIn(words, rules)

    def test_assistant_claude_ai_artifact_export_rules(self):
        """FAC-71: templates/assistant.md tells the Assistant to export claude.ai artifacts/docs into
        docs/<project>/... before a non-Claude worker starts, and design tasks deliver repo files as Done."""
        with open(J("templates", "assistant.md"), encoding="utf-8") as fh:
            text = fh.read()
        rules = text.split("\nRules:\n", 1)[1].split("\n## ", 1)[0]
        for words in (
            "only Claude agents can read claude.ai",
            "Before a non-Claude worker starts a task that depends on a claude.ai artifact or doc",
            "docs/<project>/…",
            "Markdown for text; images or a written spec for designs",
            "source URL linked at the top",
            "briefs point at the repo copy, never only at a claude.ai link",
            "marked `to export`",
        ):
            self.assertIn(words, rules)
        workers = text.split("## Start the workers (one per task)\n", 1)[1].split("\n## ", 1)[0]
        for words in (
            "deliver the canvas as repo files too under `docs/<project>/design/…`",
            "as part of Done",
            "repo files shown in your proof",
            "approved repo files under `docs/<project>/design/…`, never only at a claude.ai link",
        ):
            self.assertIn(words, workers)

    def test_no_engine_prompt_or_brief_mentions_tasks_panel_or_herdr_app(self):
        import tempfile
        from unittest import mock
        import herdr_task.assistant_settings as aset
        import herdr_task.commands as commands
        import herdr_task.launch as launch
        import herdr_task.revive as revive
        import herdr_task.settings as settings
        import herdr_task.templates as T
        from test_herdr_task import Store
        tdir = tempfile.mkdtemp()
        with mock.patch.object(T, "TEMPLATES", tdir), mock.patch.object(revive, "TEMPLATES", tdir), \
                mock.patch.object(settings, "SETTINGS", os.path.join(tdir, "none.json")), \
                mock.patch.object(launch, "own_path", lambda: "/usr/local/bin/herdr-task"):
            s = Store([
                {"id": 4, "title": "Fix bug", "state": "running", "mode": "worktree", "worktree": "/wt/4", "branch": "task/4", "base": "main", "prompt": "Fix it."},
                {"id": 5, "title": "Local", "state": "running", "mode": "here", "prompt": "Check it."},
            ])
            wt, here = s.data["tasks"]
            rs = aset.rows("/f", {})
            samples = [
                T.instructions("worker"),
                T.instructions("reviewer"),
                revive.template_of("assistant")[1],
                aset.block(rs),
                aset.notice(rs),
                launch.brief(s, wt),
                launch.brief(s, here),
                launch.handoff(s, wt, {"agent": "claude"}),
                settings.settings_block(s, wt),
                settings.settings_block(s, here),
                commands.finalize(s, {**here, "set": {}}),
                commands.finalize(s, {**wt, "set": {"merge": "ask"}}),
                commands.RESUME_NUDGE,
                commands.SWITCH_NUDGE,
            ]
            for label, cfg_merge in (("assistant_settings", aset.block(rs)), ("settings_block", settings.settings_block(s, wt)),
                                     ("finalize_local", commands.finalize(s, {**here, "set": {}})),
                                     ("finalize_wt", commands.finalize(s, {**wt, "set": {"merge": "ask"}}))):
                self.assertIn("by voice or on the phone's ask card", cfg_merge, label)
            for idx, text in enumerate(samples):
                low = text.lower()
                self.assertNotIn("tasks panel", low, f"sample {idx}: {text[:120]}")
                self.assertNotIn("herdr-app", low, f"sample {idx}: {text[:120]}")
                self.assertNotIn("shepherdr", low, f"sample {idx}: {text[:120]}")


if __name__ == "__main__":
    unittest.main()
