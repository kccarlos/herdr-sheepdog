"""SDF-16: one coordinator per project, named by a lease (design section 5). Field notes 53, 54 and 55 as tests."""
import home_guard  # noqa: F401  (first: every test on a throwaway home, never the real one)
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.path.join(ROOT, "mods", "tasks", "bin")
sys.path.insert(0, BIN)

import herdr_task.agents as agents  # noqa: E402
import herdr_task.assistant_settings as aset  # noqa: E402
import herdr_task.autopilot as autopilot  # noqa: E402
import herdr_task.base as base  # noqa: E402
import herdr_task.commands as commands  # noqa: E402
import herdr_task.launch as launch  # noqa: E402
import herdr_task.lease as lease  # noqa: E402
import herdr_task.news as news  # noqa: E402
import herdr_task.settings as st  # noqa: E402
import herdr_task.store as store  # noqa: E402
import herdr_task.watchdog as watchdog  # noqa: E402


class FakeHc:
    """hc on a fake machine: terminals by address; run starts one, kill ends one; every call is logged."""

    def __init__(self, clock):
        self.terms, self.calls, self.clock, self.n, self.down = {}, [], clock, 0, False

    def __call__(self, args, cwd=None, env=None, **kw):
        if args[0] != "hc":
            return subprocess.CompletedProcess(args, 1, "", "not here")
        self.calls.append(list(args))
        ok = lambda out="": subprocess.CompletedProcess(args, 0, out, "")
        cmd = args[1]
        if cmd == "ls":
            k, v = args[args.index("--label") + 1].split("=", 1) if "--label" in args else (None, None)
            return ok(json.dumps({"terminals": [t for t in self.terms.values() if t["state"] == "live" and (not k or t["labels"].get(k) == v)]}))
        if cmd == "get":
            t = self.terms.get(args[2]) or next((t for t in self.terms.values() if t.get("name") == args[2] and t["state"] == "live"), None)
            if self.down:  # hc not answering
                return subprocess.CompletedProcess(args, 1, "", "the core did not answer")
            return ok(json.dumps(t)) if t else subprocess.CompletedProcess(  # as hc says it
                args, 3, json.dumps({"error": {"kind": "not_found", "message": f"no terminal {args[2]!r}"}}), "")
        if cmd == "run":
            self.n += 1
            addr = f"n{self.n}"
            lbs = dict(args[i + 1].split("=", 1) for i, a in enumerate(args) if a == "--label")
            one = os.path.join(st.TEMPLATES, args[-1] + ".md")
            first = args[-1]
            if os.path.isfile(one):  # a one-off template: its words are the first message
                with open(one) as f:
                    first = f.read()
            self.terms[addr] = {"address": addr, "state": "live", "cwd": args[args.index("--cwd") + 1], "name": args[args.index("--name") + 1],
                                "labels": lbs, "status": {"state": "idle"}, "turn": 0, "created_ms": self.clock(), "argv": args,
                                "first": first, "foreground": {"name": "claude"}}
            return ok(json.dumps({"address": addr}))
        if cmd == "kill":
            if args[2] in self.terms:
                self.terms[args[2]]["state"] = "exited"
            return ok()
        if cmd == "label":
            for kv in args[3:]:
                k, v = kv.split("=", 1)
                self.terms[args[2]]["labels"][k] = v
            return ok()
        if cmd == "read":
            return ok(self.terms.get(args[2], {}).get("screen", ""))
        return ok()

    def term(self, addr, cwd, labels=None, status="idle", **extra):
        self.terms[addr] = {"address": addr, "state": "live", "cwd": cwd, "labels": labels or {}, "status": {"state": status},
                            "turn": 1, "created_ms": 1000, "foreground": {"name": "claude"}, **extra}
        return self.terms[addr]


class LeaseCase(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.store = os.path.join(self.tmp, "store")
        self.repo = os.path.join(self.tmp, "shop")
        os.makedirs(self.store)
        os.makedirs(self.repo)
        self.now = 1_800_000_000_000
        self.hc = FakeHc(lambda: self.now)
        self.slug, self.list = store.slug_of(self.repo), None
        self.list = os.path.join(self.store, self.slug + ".json")
        clock = lambda: self.now
        noop = lambda *a, **kw: None
        patches = [mock.patch.object(subprocess, "run", self.hc),
                   *(mock.patch.object(m, "now", clock) for m in (base, agents, news, commands, watchdog, lease, autopilot)),
                   *(mock.patch.object(m, "STORE", self.store) for m in (store, watchdog, news)),
                   mock.patch.object(news, "HELD", os.path.join(self.store, ".held.json")),
                   mock.patch.object(news, "NEWS", os.path.join(self.store, ".news.json")),
                   mock.patch.object(news, "screen_raw", lambda *_: ""), mock.patch.object(news, "POLL_DELAYS", (0,)),
                   mock.patch.object(watchdog, "WATCHDOG", os.path.join(self.store, ".watchdog.json")),
                   mock.patch.object(watchdog, "template_info", lambda: ("tpl", 0)),
                   mock.patch.object(autopilot, "STATE", os.path.join(self.store, ".autopilot.json")),
                   mock.patch.object(st, "TEMPLATES", os.path.join(self.tmp, "templates")),
                   mock.patch.object(lease, "probe_volume", lambda *_: None),
                   *(mock.patch.object(m, "guard", noop) for m in (base, news, watchdog)),
                   mock.patch.object(aset, "settings_pass", noop)]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)

    def save(self, tasks, coordinator=None, kind="repo"):
        d = {"version": 1, "folder": self.repo, "kind": kind, "prefix": "SHOP", "tasks": tasks,
             **({"coordinator": coordinator} if coordinator else {})}
        with open(self.list, "w") as f:
            json.dump(d, f)

    def data(self):
        with open(self.list) as f:
            return json.load(f)

    def started(self):
        return [c for c in self.hc.calls if c[1] == "run"]

    def first(self, n=0):
        """What the n-th coordinator started heard first."""
        return self.hc.terms[f"n{n + 1}"]["first"]


class Lease(LeaseCase):
    def test_the_holder_is_the_coordinator_and_a_second_claimant_is_refused(self):
        self.hc.term("c1", self.repo)
        self.hc.term("c2", self.repo)
        self.save([])
        self.assertTrue(lease.claim(self.repo, "c1"))
        self.assertEqual(launch.coordinator_in(self.repo), "c1")
        self.assertFalse(lease.claim(self.repo, "c2"))  # c1 still lives
        self.assertEqual(lease.lease_of(self.repo)["address"], "c1")
        self.assertTrue(lease.claim(self.repo, "c2", replacing="c1"))  # a restart hands it on
        self.assertEqual((launch.coordinator_in(self.repo), lease.handoffs_of(lease.lease_of(self.repo))[0]["from"]), ("c2", "c1"))
        self.hc.terms["c2"]["state"] = "exited"
        self.assertIsNone(launch.coordinator_in(self.repo))
        self.assertTrue(lease.claim(self.repo, "c1"))  # the holder is gone: anyone the engine started may take it

    def test_note_55_a_terminal_that_looks_like_an_assistant_is_not_the_coordinator(self):
        # (a) a hand-started claude in a tab renamed "Assistant", and one with the coordinator labels someone set
        self.hc.term("t2gt", self.repo, name="assistant")
        self.hc.term("t15g", self.repo, {"sheepdog.role": "coordinator", "herdr.role": "coordinator", "sheepdog.project": self.slug})
        self.save([{"id": 1, "title": "Auth", "state": "running"}])
        self.assertIsNone(launch.coordinator_in(self.repo))
        report = {}
        lease.reconcile_pass(report)
        new = report["coordinators"][0]["to"]
        # the engine starts its own and says the look-alike is not the coordinator; it never touches it
        self.assertEqual(launch.coordinator_in(self.repo), new)
        self.assertIn({"address": "t15g", "cwd": self.repo}, report["not_coordinator"])
        self.assertFalse([c for c in self.hc.calls if c[1] in ("kill", "label") and c[2] in ("t2gt", "t15g")])
        # revive and the settings notices know only the holder
        import herdr_task.revive as revive
        self.assertEqual((revive.watched(self.hc.terms["t15g"]), revive.watched(self.hc.terms[new])), (False, True))
        self.assertEqual(lease.live_holders(), [(new, self.repo)])


class Reconcile(LeaseCase):
    def test_note_54_a_coordinator_that_exited_is_replaced_in_the_repo_folder_with_its_workers(self):
        home = os.path.dirname(self.repo)
        self.hc.term("t1pj", home, {"sheepdog.role": "coordinator"})  # it ran from the wrong folder (note 55b)
        w1 = self.hc.term("w1", self.repo, {"sheepdog.task": f"{self.slug}#1"}, parent="t1pj")
        self.hc.term("w2", self.repo, {"sheepdog.task": f"{self.slug}#2"}, parent="t1pj")
        self.save([{"id": 1, "title": "Auth", "state": "ready", "agents": [{"address": "w1", "parent": "t1pj"}]},
                   {"id": 2, "title": "Pay", "state": "running", "agents": [{"address": "w2", "parent": "t1pj"}]},
                   {"id": 3, "title": "Old", "state": "done", "agents": [{"address": "w0", "parent": "t1pj", "ended": 1}]}],
                  coordinator={"address": "t1pj", "created_ms": 1000, "log": None})
        with news.held_messages() as held:
            held["t1pj"] = [{"text": "SHOP-1 is ready", "how": "news", "at": self.now}]
        self.hc.terms["t1pj"]["state"] = "exited"  # a disk stall took it
        report = {}
        lease.reconcile_pass(report)
        self.assertEqual(len(self.started()), 1)
        run = self.started()[0]
        new = report["coordinators"][0]["to"]
        # exactly one, started in the repo folder, labelled for the project, kept, with a handoff naming the workers
        self.assertEqual((run[run.index("--cwd") + 1], launch.coordinator_in(self.repo)), (self.repo, new))
        self.assertIn("--keep", run)
        self.assertIn(f"sheepdog.project={self.slug}", run)
        self.assertIn("w1 (SHOP-1 Auth)", self.first())
        self.assertIn("w2 (SHOP-2 Pay)", self.first())
        # workers re-parented; a finished one is left as it was
        d = self.data()
        self.assertEqual([t["agents"][0]["parent"] for t in d["tasks"]], [new, new, "t1pj"])
        self.assertEqual(report["coordinators"][0]["workers"], 2)
        # their parent_for and the news route reach the new one; the held news goes to it
        self.assertEqual(launch.parent_for(w1["parent"], self.repo), new)
        with news.held_messages() as held:
            self.assertEqual((held.get("t1pj"), [x["text"] for x in held[new]]), (None, ["SHOP-1 is ready"]))
        news.held_pass(r := {})
        self.assertIn([new, "SHOP-1 is ready"], [c[2:] for c in self.hc.calls if c[1] == "send"])
        # the next tick finds one and starts none
        lease.reconcile_pass(report := {})
        self.assertEqual((len(self.started()), report.get("coordinators")), (1, None))

    def test_news_typed_into_a_busy_coordinator_that_died_goes_to_the_new_one(self):
        self.save([{"id": 1, "title": "Auth", "state": "running"}], coordinator={"address": "c1", "created_ms": 1000})
        with news.held_messages() as held:
            held["c1"] = [{"text": "SHOP-1 is ready", "how": "news", "at": self.now, "pending": 3}]
        lease.reconcile_pass(report := {})
        new = report["coordinators"][0]["to"]
        with news.held_messages() as held:
            self.assertEqual(held[new], [{"text": "SHOP-1 is ready", "how": "news", "at": self.now}])

    def test_a_start_that_loses_the_lease_moves_no_worker_and_ends(self):
        self.hc.term("w1", self.repo, parent="c1")
        self.save([{"id": 1, "title": "Auth", "state": "running", "agents": [{"address": "w1", "parent": "c1"}]}],
                  coordinator={"address": "c1", "created_ms": 1000})
        with mock.patch.object(lease, "claim", lambda *a, **kw: False):
            lease.reconcile_pass(report := {})
        self.assertEqual(report["coordinators"][0]["action"], "refused")
        self.assertEqual((self.hc.terms["n1"]["state"], self.data()["tasks"][0]["agents"][0]["parent"]), ("exited", "c1"))

    def test_a_recovery_cut_short_still_moves_the_workers(self):
        self.hc.term("n9", self.repo)
        self.hc.term("w1", self.repo, parent="c1")
        self.save([{"id": 1, "title": "Auth", "state": "running", "agents": [{"address": "w1", "parent": "c1"}]}],
                  coordinator={"address": "n9", "created_ms": 1000})  # the lease moved, then the tick stopped
        lease.reconcile_pass(report := {})
        self.assertEqual((self.started(), report["coordinators"][0]["action"]), ([], "re-parented"))
        self.assertEqual(self.data()["tasks"][0]["agents"][0]["parent"], "n9")
        lease.reconcile_pass(report := {})
        self.assertIsNone(report.get("coordinators"))  # nothing left to move: no write, no report

    def test_a_recovery_cut_short_still_moves_the_held_news(self):
        self.hc.term("n9", self.repo)
        self.save([{"id": 1, "title": "Auth", "state": "running"}], coordinator={"address": "n9", "created_ms": 1000, "handoffs": [{"from": "c1"}]})
        with news.held_messages() as held:
            held["c1"] = [{"text": "SHOP-1 is ready", "how": "news", "at": self.now}]  # the tick stopped before it moved
        lease.reconcile_pass(report := {})
        with news.held_messages() as held:
            self.assertEqual((held.get("c1"), [x["text"] for x in held["n9"]]), (None, ["SHOP-1 is ready"]))
        lease.reconcile_pass(report := {})
        self.assertIsNone(report.get("coordinators"))

    def test_a_restart_cut_short_ends_the_replaced_one_and_moves_its_news(self):
        self.hc.term("c1", self.repo)
        self.hc.term("n9", self.repo)
        self.hc.term("w1", self.repo, parent="c1")
        self.save([{"id": 1, "title": "Auth", "state": "running", "agents": [{"address": "w1", "parent": "c1"}]}],
                  coordinator={"address": "n9", "created_ms": 1000,
                               "handoffs": [{"from": "c1", "created_ms": 1000, "pid": 4242, "at": self.now}]})  # claimed, then the tick stopped
        with news.held_messages() as held:
            held["c1"] = [{"text": "SHOP-1 is ready", "how": "news", "at": self.now}]
        with mock.patch.object(lease, "end_group") as eg, mock.patch.object(lease, "pid_started_before", lambda pid, at: True):
            lease.reconcile_pass(report := {})
        self.assertEqual((self.started(), report["coordinators"][0]["ended"], self.hc.terms["c1"]["state"]), ([], "c1", "exited"))
        self.assertEqual(eg.call_args_list, [mock.call(4242)])  # its process group too, as a whole restart does
        self.assertEqual(lease.handoffs_of(lease.lease_of(self.repo)), [])
        self.assertEqual(self.data()["tasks"][0]["agents"][0]["parent"], "n9")
        with news.held_messages() as held:
            self.assertEqual((held.get("c1"), [x["text"] for x in held["n9"]]), (None, ["SHOP-1 is ready"]))

    def test_a_later_terminal_at_the_replaced_address_is_left_alone(self):
        self.hc.term("c1", self.repo, created_ms=2000)  # not the one replaced (that one started at 1000)
        self.hc.term("n9", self.repo)
        self.save([{"id": 1, "title": "Auth", "state": "running"}],
                  coordinator={"address": "n9", "created_ms": 1000, "handoffs": [{"from": "c1", "created_ms": 1000}]})
        with news.held_messages() as held:
            held["c1"] = [{"text": "its own", "how": "send", "at": self.now}]
        lease.reconcile_pass(report := {})
        self.assertEqual((self.hc.terms["c1"]["state"], report["coordinators"][0].get("ended")), ("live", None))
        with news.held_messages() as held:
            self.assertEqual((held.get("c1"), held.get("n9")), ([{"text": "its own", "how": "send", "at": self.now}], None))

    def test_round_4_a_finished_handoff_leaves_nothing_to_redo(self):
        # a whole restart, then the old address taken by a later terminal with news of its own: nothing moves
        old = self.hc.term("c1", self.repo, foreground={"name": "claude", "pid": 4242})
        self.save([{"id": 1, "title": "Auth", "state": "running"}], coordinator={"address": "c1", "created_ms": 1000})
        with mock.patch.object(lease, "end_group"), mock.patch.object(lease, "agent_pid", lambda a: 4242):
            new = lease.restart(self.repo, old)
        self.assertEqual(lease.handoffs_of(lease.lease_of(self.repo)), [])
        self.hc.term("c1", self.repo, created_ms=5000)
        with news.held_messages() as held:
            held["c1"] = [{"text": "its own", "how": "send", "at": self.now}]
        lease.reconcile_pass(report := {})
        with news.held_messages() as held:
            self.assertEqual((held.get("c1")[0]["text"], held.get(new), report.get("coordinators")), ("its own", None, None))

    def test_revive_never_takes_a_later_terminal_at_the_lease_address_for_the_coordinator(self):
        import herdr_task.revive as revive
        self.hc.term("c1", self.repo, created_ms=2000)
        self.save([], coordinator={"address": "c1", "created_ms": 1000})
        self.assertFalse(revive.watched(self.hc.terms["c1"]))
        self.hc.terms["c1"]["created_ms"] = 1000
        self.assertTrue(revive.watched(self.hc.terms["c1"]))

    def test_round_5_a_tick_that_stopped_after_the_kill_still_ends_the_group(self):
        self.hc.term("c1", self.repo)["state"] = "finished"  # killed, then the tick stopped before end_group
        self.hc.term("n9", self.repo)
        self.save([{"id": 1, "title": "Auth", "state": "running"}],
                  coordinator={"address": "n9", "created_ms": 1000, "handoffs": [{"from": "c1", "created_ms": 1000, "pid": 4242, "at": self.now}]})
        with mock.patch.object(lease, "end_group") as eg, mock.patch.object(lease, "pid_started_before", lambda pid, at: True):
            lease.reconcile_pass({})
        self.assertEqual(eg.call_args_list, [mock.call(4242)])
        self.assertIn(["hc", "rm", "c1"], self.hc.calls)
        self.assertEqual(lease.handoffs_of(lease.lease_of(self.repo)), [])

    def test_round_5_a_second_replacement_keeps_the_unfinished_handoff(self):
        self.hc.term("c1", self.repo)  # the first one replaced, still running: its handoff never finished
        self.hc.term("w1", self.repo, parent="c1")
        self.save([{"id": 1, "title": "Auth", "state": "running", "agents": [{"address": "w1", "parent": "c1"}]}],
                  coordinator={"address": "n9", "created_ms": 1000, "handoffs": [{"from": "c1", "created_ms": 1000, "pid": 4242, "at": self.now}]})
        with news.held_messages() as held:
            held["c1"] = [{"text": "SHOP-1 is ready", "how": "news", "at": self.now}]
        # n9 is gone before any tick: the next one starts a third and must finish both handoffs
        with mock.patch.object(lease, "end_group") as eg, mock.patch.object(lease, "pid_started_before", lambda pid, at: True):
            lease.reconcile_pass(report := {})
        new = report["coordinators"][0]["to"]
        self.assertEqual((self.hc.terms["c1"]["state"], self.data()["tasks"][0]["agents"][0]["parent"]), ("exited", new))
        self.assertEqual(eg.call_args_list[0], mock.call(4242))
        with news.held_messages() as held:
            self.assertEqual((held.get("c1"), [x["text"] for x in held[new]]), (None, ["SHOP-1 is ready"]))
        self.assertEqual(lease.handoffs_of(lease.lease_of(self.repo)), [])

    def test_fable_a_stale_record_never_ends_a_recycled_pid(self):
        self.hc.term("n9", self.repo)
        self.save([{"id": 1, "title": "Auth", "state": "running"}],
                  coordinator={"address": "n9", "created_ms": 1000, "handoffs": [{"from": "c1", "created_ms": 1000, "pid": 4242, "at": self.now}]})
        seen = []
        with mock.patch.object(lease, "end_group") as eg, \
                mock.patch.object(lease, "pid_started_before", lambda pid, at: seen.append((pid, at)) or False):  # 4242 is someone else's now
            lease.reconcile_pass({})
        self.assertEqual((eg.call_args_list, seen), ([], [(4242, self.now)]))
        self.assertEqual(lease.handoffs_of(lease.lease_of(self.repo)), [])

    def test_fable_hc_not_answering_replaces_no_holder_and_keeps_the_handoff(self):
        self.hc.term("n9", self.repo)
        self.hc.term("c1", self.repo)
        self.save([{"id": 1, "title": "Auth", "state": "running"}],
                  coordinator={"address": "n9", "created_ms": 1000, "handoffs": [{"from": "c1", "created_ms": 1000}]})
        self.hc.down = True
        lease.reconcile_pass(report := {})
        self.assertEqual((self.started(), report.get("coordinators")), ([], None))
        self.assertEqual(lease.finish_handoff(self.repo, "n9"), (0, []))
        self.assertEqual((len(lease.handoffs_of(lease.lease_of(self.repo))), self.hc.terms["c1"]["state"]), (1, "live"))
        self.hc.down = False
        lease.reconcile_pass({})
        self.assertEqual((lease.handoffs_of(lease.lease_of(self.repo)), self.hc.terms["c1"]["state"]), ([], "exited"))

    def test_fable_a_start_whose_claim_fails_ends(self):
        self.save([{"id": 1, "title": "Auth", "state": "running"}])
        def boom(*a, **kw):
            raise lease.Fail("the list is gone")
        with mock.patch.object(lease, "claim", boom):
            lease.reconcile_pass(report := {})
        self.assertEqual((report["coordinators"][0]["action"], self.hc.terms["n1"]["state"]), ("refused", "exited"))
        self.assertIn(["hc", "rm", "n1"], self.hc.calls)

    def test_fable_a_handoff_from_the_holder_itself_never_ends_it(self):
        self.hc.term("n9", self.repo)
        self.save([{"id": 1, "title": "Auth", "state": "running"}], coordinator={"address": "n9", "handoffs": [{"from": "n9"}]})
        lease.finish_handoff(self.repo, "n9")
        self.assertEqual((self.hc.terms["n9"]["state"], lease.handoffs_of(lease.lease_of(self.repo))), ("live", []))

    def test_fable_the_pid_is_kept_only_for_the_terminal_it_was_read_from(self):
        self.hc.term("c2", self.repo)
        self.hc.term("n9", self.repo)
        self.save([], coordinator={"address": "c2", "created_ms": 1000})  # the lease moved on since c1's pid was read
        lease.claim(self.repo, "n9", replacing="c1", pid=4242)
        self.assertEqual(lease.handoffs_of(lease.lease_of(self.repo)), [])  # refused: c2 lives
        self.hc.terms["c2"]["state"] = "exited"
        lease.claim(self.repo, "n9", replacing="c1", pid=4242)
        self.assertEqual([(h["from"], h["pid"]) for h in lease.handoffs_of(lease.lease_of(self.repo))], [("c2", None)])

    def test_one_starts_before_any_owner_message_and_only_for_a_repo_with_open_tasks(self):
        self.save([{"id": 1, "title": "Auth", "state": "done"}])
        lease.reconcile_pass({})
        self.assertEqual(self.started(), [])
        self.save([{"id": 1, "title": "Auth", "state": "queued"}], kind="folder")  # the home folder is no project
        lease.reconcile_pass({})
        self.assertEqual(self.started(), [])
        self.save([{"id": 1, "title": "Auth", "state": "queued"}])
        lease.reconcile_pass(report := {})
        self.assertEqual((len(self.started()), report["coordinators"][0]["from"]), (1, None))
        self.assertIn("open tasks", self.first())

    def test_a_coordinator_that_keeps_dying_is_started_three_times_an_hour_then_left(self):
        self.save([{"id": 1, "title": "Auth", "state": "running"}])
        for _ in range(3):
            lease.reconcile_pass({})
            self.hc.terms[lease.lease_of(self.repo)["address"]]["state"] = "exited"
            self.now += 5 * 60000
            lease.reconcile_pass({})  # inside the grace: no new start yet
            self.now += lease.START_GRACE_MS
        lease.reconcile_pass(report := {})
        self.assertEqual((len(self.started()), report["coordinators"][0]["action"]), (3, "gave-up"))
        self.now += lease.HOUR_MS
        lease.reconcile_pass({})
        self.assertEqual(len(self.started()), 4)

    def test_a_stalled_disk_starts_nothing(self):
        self.save([{"id": 1, "title": "Auth", "state": "running"}])
        with mock.patch.object(lease, "probe_volume", lambda *_: "the disk did not answer in 10 s"):
            lease.reconcile_pass(report := {})
        self.assertEqual((self.started(), report["coordinators"][0]["action"]), ([], "disk-stalled"))

    def test_a_kept_terminal_waiting_for_revive_is_not_replaced_until_it_stays_a_shell(self):
        self.hc.term("c1", self.repo, foreground={"name": "zsh"})  # hc gave it a shell; revive types its agent back
        self.save([{"id": 1, "title": "Auth", "state": "running"}], coordinator={"address": "c1", "created_ms": 1000})
        lease.reconcile_pass({})
        self.now += lease.SHELL_MS - 60000
        lease.reconcile_pass({})
        self.assertEqual(self.started(), [])
        self.now += 60000  # still a bare shell: its agent never came back, or exited again
        lease.reconcile_pass(report := {})
        new = report["coordinators"][0]["to"]
        self.assertEqual((launch.coordinator_in(self.repo), self.hc.terms["c1"]["state"]), (new, "exited"))
        self.assertIn(["hc", "rm", "c1"], self.hc.calls)

    def test_a_holder_whose_agent_came_back_is_left_alone(self):
        self.hc.term("c1", self.repo, foreground={"name": "zsh"})
        self.save([{"id": 1, "title": "Auth", "state": "running"}], coordinator={"address": "c1", "created_ms": 1000})
        lease.reconcile_pass({})
        self.hc.terms["c1"]["foreground"] = {"name": "claude"}  # revive typed it back
        self.now += 5 * 60000
        lease.reconcile_pass({})
        self.assertNotIn("no_agent_since", lease.lease_of(self.repo))
        self.hc.terms["c1"]["foreground"] = {"name": "zsh"}  # a later exit starts a new count
        self.now += lease.SHELL_MS - 60000
        lease.reconcile_pass({})
        self.assertEqual(self.started(), [])

    def test_note_54_wakes_are_keyed_by_folder_and_reach_the_new_holder(self):
        self.hc.term("c1", self.repo)
        self.save([{"id": 1, "title": "Auth", "state": "running"}], coordinator={"address": "c1", "created_ms": 1000})
        self.hc.terms["c1"]["state"] = "exited"
        lease.reconcile_pass(report := {})
        new = report["coordinators"][0]["to"]
        a = {"folder": self.repo, "pending": {"at": self.now, "reasons": ["schedule"]}}
        with mock.patch.object(autopilot, "message", lambda *a: "wake"), mock.patch.object(autopilot, "typing", lambda *a: None):
            self.assertEqual(autopilot.deliver(self.repo, a), "delivered")
        self.assertEqual(a["inflight"]["to"], new)


class PidCheck(unittest.TestCase):
    """The real ps, no fake: the handoff's pid is ended only when that process started before the record."""

    def test_the_pid_check_reads_the_real_process_start(self):
        self.assertTrue(lease.pid_started_before(os.getpid(), (time.time() + 5) * 1000))
        self.assertFalse(lease.pid_started_before(os.getpid(), (time.time() - 3600) * 1000))  # it started later: a recycled pid
        self.assertFalse(lease.pid_started_before(None, time.time() * 1000))


class EngineRestart(LeaseCase):
    def test_restart_starts_in_the_repo_folder_hands_on_the_lease_and_ends_the_old_one(self):
        old = self.hc.term("c1", os.path.dirname(self.repo), screen="Checking SHOP-1 again\n", foreground={"name": "claude", "pid": 4242})
        self.hc.term("w1", self.repo, parent="c1")
        self.save([{"id": 1, "title": "Auth", "state": "running", "agents": [{"address": "w1", "parent": "c1"}]}],
                  coordinator={"address": "c1", "created_ms": 1000})
        with mock.patch.object(lease, "end_group") as eg, mock.patch.object(lease, "agent_pid", lambda a: 4242), \
                mock.patch.object(lease, "pid_started_before", lambda pid, at: True):
            new = lease.restart(self.repo, old)
        self.assertEqual((launch.coordinator_in(self.repo), self.hc.terms["c1"]["state"], eg.call_args_list), (new, "exited", [mock.call(4242)]))
        run = self.started()[0]
        self.assertEqual(run[run.index("--cwd") + 1], self.repo)
        self.assertIn("restarted by the watchdog", self.first())
        saved = [f for f in os.listdir(os.path.join(self.store, "handoff"))]
        self.assertEqual(len(saved), 1)
        self.assertEqual(self.data()["tasks"][0]["agents"][0]["parent"], new)

    def test_the_template_and_settings_reach_the_new_one_with_the_handoff(self):
        os.makedirs(st.TEMPLATES)
        with open(os.path.join(st.TEMPLATES, "assistant.md"), "w") as f:
            f.write("---\nname: assistant\ncommand: exec claude --model opus\n---\nCoordinate.\n")
        seen = {}
        real = self.hc.__call__

        def spy(args, **kw):
            if args[:2] == ["hc", "run"]:
                with open(os.path.join(st.TEMPLATES, args[-1] + ".md")) as f:
                    seen["tpl"] = f.read()
            return real(args, **kw)
        self.save([])
        with mock.patch.object(subprocess, "run", spy):
            lease.start(self.repo, "SHOP", "You are taking over.")
        self.assertIn("DISABLE_AUTOUPDATER=1 exec claude --model opus", seen["tpl"])
        self.assertIn(launch.CLEAN_ENV, seen["tpl"])
        self.assertTrue(seen["tpl"].rstrip().endswith("You are taking over."))
        self.assertEqual(os.listdir(st.TEMPLATES), ["assistant.md"])  # the one-off template is gone


class StuckWorking(LeaseCase):
    """Note 53: a coordinator in one "working" turn with no child process and no output for 20 minutes."""

    def setUp(self):
        super().setUp()
        self.k = self.hc.term("c1", self.repo, status="working", screen="✻ Checking SHOP-125 again… (3m · ↓ 1k tokens · esc to interrupt)\n",
                              foreground={"name": "claude", "pid": 777})
        self.save([{"id": 1, "title": "Auth", "state": "running"}], coordinator={"address": "c1", "created_ms": 1000})
        self.kids = {"777": []}
        p = mock.patch("herdr_task.sessions.kids", lambda pid: self.kids.get(str(pid), []))
        p.start()
        self.addCleanup(p.stop)
        self.restarts = []
        p = mock.patch.object(watchdog, "restart_assistant", lambda k, folder: self.restarts.append(k["address"]) or None)
        p.start()
        self.addCleanup(p.stop)

    def pass_(self, minutes, spinner=None):
        self.now += minutes * 60000
        if spinner:
            self.hc.terms["c1"]["screen"] = f"✻ Checking SHOP-125 again… ({spinner} · ↓ 2k tokens · esc to interrupt)\n"
        watchdog.watchdog_pass(report := {})
        return report

    def interrupts(self):
        return [c for c in self.hc.calls if c[1] == "interrupt"]

    def test_esc_once_then_a_fresh_session(self):
        self.pass_(0)
        self.pass_(19, spinner="19m")  # the timer ticks: no output of its own
        self.assertEqual((self.interrupts(), self.restarts), ([], []))
        r = self.pass_(1, spinner="20m")
        self.assertEqual((len(self.interrupts()), r["watchdog"][0]["action"]), (1, "interrupted"))
        self.pass_(1)
        self.assertEqual(self.restarts, [])  # it gets a moment to stop
        self.pass_(1)
        self.assertEqual((len(self.interrupts()), self.restarts), (1, ["c1"]))

    def test_an_esc_that_only_repaints_still_leads_to_a_fresh_session(self):
        self.pass_(0)
        self.pass_(20)
        self.assertEqual(len(self.interrupts()), 1)
        self.hc.terms["c1"]["screen"] = "Interrupted\n✻ Checking SHOP-125 again… (21m · esc to interrupt)\n"  # repainted, still working
        self.pass_(2)
        self.assertEqual((len(self.interrupts()), self.restarts), (1, ["c1"]))

    def test_output_in_its_log_after_the_esc_clears_it(self):
        log = os.path.join(self.tmp, "session.jsonl")
        with open(log, "w") as f:
            f.write("{}\n")
        os.utime(log, (self.now / 1000, self.now / 1000))
        with mock.patch("herdr_task.sessions.session_of", lambda pid: ("s1", log)):
            self.pass_(0)
            self.pass_(20)
            self.assertEqual(len(self.interrupts()), 1)
            os.utime(log, ((self.now + 1000) / 1000, (self.now + 1000) / 1000))  # it wrote again
            self.pass_(2)
            self.assertEqual(self.restarts, [])

    def test_a_child_at_work_or_new_output_keeps_it(self):
        self.hc.terms["c1"]["status"] = {"state": "idle"}
        self.kids["777"] = ["800"]  # an MCP server, there while idle too
        self.pass_(0)
        self.hc.terms["c1"]["status"] = {"state": "working"}
        self.pass_(1)
        self.kids["777"] = ["800", "901"]  # a command it runs now
        self.pass_(25)
        self.assertEqual(self.interrupts(), [])
        self.kids["777"] = ["800"]
        self.hc.terms["c1"]["screen"] = "Read 3 files\n✻ Checking SHOP-125 again… (26m · esc to interrupt)\n"  # output of its own
        self.pass_(1)
        self.pass_(15)
        self.assertEqual(self.interrupts(), [])
        self.pass_(5)  # 20 min since its last output, no child but the MCP server
        self.assertEqual(len(self.interrupts()), 1)

    def test_a_helper_terminal_at_work_is_a_child(self):
        self.hc.term("h1", self.repo, status="working", parent="c1")
        self.pass_(0)
        self.pass_(30)
        self.assertEqual(self.interrupts(), [])


if __name__ == "__main__":
    unittest.main()
