"""herdr_task.usage: codexbar readings (recorded in test/fixtures/codexbar, account details replaced) into pools."""
import json
import os
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.dont_write_bytecode = True
sys.path.insert(0, os.path.join(ROOT, "mods", "tasks", "bin"))
from herdr_task import usage  # noqa: E402


def fixture(provider):
    with open(os.path.join(ROOT, "test", "fixtures", "codexbar", provider + ".json")) as f:
        return f.read()


def pool(name, five, week, reset="2026-11-10T00:00:00Z", state="ok"):
    return {"name": name, "state": state, "five": five, "week": week, "five_reset": None, "week_reset": reset}


class Parse(unittest.TestCase):
    def test_normal(self):
        (c,) = usage.parse("claude", fixture("claude"))
        self.assertEqual((c["name"], c["state"], c["five"], c["week"]), ("claude", "ok", 90, 25))
        self.assertEqual((c["five_reset"], c["week_reset"]), ("2026-11-08T18:29:59Z", "2026-11-09T04:59:59Z"))
        (x,) = usage.parse("codex", fixture("codex"))  # weekly window only
        self.assertEqual((x["state"], x["five"], x["week"]), ("ok", None, 0.0))
        pools = {p["name"]: p for p in usage.parse("antigravity", fixture("antigravity"))}
        self.assertEqual(sorted(pools), ["antigravity-claude-gpt", "antigravity-gemini"])
        self.assertEqual((pools["antigravity-gemini"]["five"], pools["antigravity-gemini"]["week"]), (97.1, 76.3))
        self.assertEqual(pools["antigravity-claude-gpt"]["week_reset"], "2026-11-11T08:02:36Z")

    def test_a_model_scoped_tertiary_window_doesnt_replace_the_weekly_one(self):
        d = json.loads(fixture("claude"))
        d[0]["usage"]["secondary"]["usedPercent"] = 99.5
        d[0]["usage"]["tertiary"] = {"windowMinutes": 10080, "usedPercent": 15, "resetsAt": "2026-11-11T00:00:00Z"}
        (c,) = usage.parse("claude", json.dumps(d))
        self.assertEqual(c["week"], 0.5)
        self.assertIsNone(usage.pick([c], {"claude"}, False))

    def test_a_failing_provider_is_unknown(self):
        for text in ("", "not json", "[]", '[{"error":"x"}]'):
            (p,) = usage.parse("claude", text)
            self.assertEqual((p["name"], p["state"], p["five"], p["week"]), ("claude", "unknown", None, None))
        self.assertEqual([p["state"] for p in usage.parse("antigravity", "")], ["unknown", "unknown"])


class Read(unittest.TestCase):
    def test_cached_for_a_minute(self):
        calls, now = [], [1000.0]
        def fetch(p):
            calls.append(p)
            return fixture(p) if p != "codex" else ""
        with tempfile.TemporaryDirectory() as d:
            pools = usage.read(d, fetch, lambda: now[0])
            self.assertEqual(calls, ["claude", "codex", "antigravity"])
            self.assertEqual({p["name"]: p["state"] for p in pools}["codex"], "unknown")
            now[0] += 30
            self.assertEqual(usage.read(d, fetch, lambda: now[0]), pools)
            self.assertEqual(len(calls), 3)
            now[0] += 31
            usage.read(d, fetch, lambda: now[0])
            self.assertEqual(len(calls), 6)


class Pick(unittest.TestCase):
    def test_soonest_weekly_reset_wins(self):
        pools = [pool("a", 80, 50, "2026-11-12T00:00:00Z"), pool("b", 80, 50, "2026-11-09T00:00:00Z"),
                 pool("c", 80, 50, "2026-11-08T00:00:00Z")]
        self.assertEqual(usage.pick(pools, {"a", "b"}, False)["name"], "b")

    def test_pool_at_its_floor_is_skipped(self):
        pools = [pool("a", 5, 50, "2026-11-08T00:00:00Z"), pool("b", 80, 1, "2026-11-08T00:00:00Z"), pool("c", 40, 40)]
        self.assertEqual(usage.pick(pools, {"a", "b", "c"}, False)["name"], "c")
        self.assertIsNone(usage.pick(pools, {"a", "b"}, False))

    def test_long_job_refused_on_a_low_5h_window(self):
        pools = [pool("a", 15, 50)]
        self.assertEqual(usage.pick(pools, {"a"}, False)["name"], "a")
        self.assertIsNone(usage.pick(pools, {"a"}, True))

    def test_unknown_only_for_short_jobs_and_last(self):
        unk = pool("u", None, None, None, "unknown")
        self.assertEqual(usage.pick([unk, pool("a", 50, 50)], {"u", "a"}, False)["name"], "a")
        self.assertEqual(usage.pick([unk], {"u"}, False)["name"], "u")
        self.assertIsNone(usage.pick([unk], {"u"}, True))
        self.assertIsNone(usage.pick([pool("a", 50, 50)], set(), False))


if __name__ == "__main__":
    unittest.main()
