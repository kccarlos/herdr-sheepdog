"""herdr-task: every module compiles, the add-on switch it reads, and (on CI only, where HOME is
a throwaway) its commands start. On a person's machine herdr-task never runs with another HOME."""
import json
import os
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.path.join(ROOT, "mods", "tasks", "bin")
CI = os.environ.get("CI") == "true"


class HerdrTask(unittest.TestCase):
    def test_compiles(self):
        files = [os.path.join(BIN, "herdr-task")] + [os.path.join(BIN, "herdr_task", f) for f in sorted(os.listdir(os.path.join(BIN, "herdr_task"))) if f.endswith(".py")]
        files += [os.path.join(ROOT, "mods", "artifacts", "bin", "herdr-artifact"), os.path.join(ROOT, "bin", "herdr-secret"),
                  os.path.join(ROOT, "bin", "sync-shared"), os.path.join(ROOT, "bin", "shepherdr")]
        for f in files:
            with open(f, "rb") as fh:
                compile(fh.read(), f, "exec")

    def test_addon_off(self):
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump({"everywhere": {"off": ["browser"]}}, f)
        code = ("import sys; sys.dont_write_bytecode = True; sys.path.insert(0, sys.argv[1]);"
                "from herdr_task.base import addon_off; print(addon_off('browser'), addon_off('voice'))")
        env = {**os.environ, "HERDR_TASK_SETTINGS": f.name}
        out = subprocess.run([sys.executable, "-c", code, BIN], capture_output=True, text=True, env=env)
        os.unlink(f.name)
        self.assertEqual(out.stdout.split(), ["True", "False"], out.stderr)

    def test_tell_takes_a_chat_mention(self):
        """A chat's #-mention (core/mentions.js) names its Assistant for tell-assistant: whole, its target, or #name."""
        code = ("import sys, json; sys.dont_write_bytecode = True; sys.path.insert(0, sys.argv[1]);"
                "from herdr_task.machines import _find;"
                "rows = [{'target': 'vm-demo/t3', 'route': 'vm-demo', 'space': 'sp-shop', 'space_name': 'shop'},"
                "        {'target': 't1', 'route': None, 'space': 'sp-web', 'space_name': 'web'}];"
                "print(json.dumps([[a['target'] for a in _find(rows, t)] for t in sys.argv[2:]]))")
        forms = ["[#shop](space:vm-demo/sp-shop)", "space:vm-demo/sp-shop", "vm-demo/sp-shop", "sp-shop", "#shop", "[#web](space:sp-web)", "#nope"]
        out = subprocess.run([sys.executable, "-c", code, BIN, *forms], capture_output=True, text=True)
        self.assertEqual(json.loads(out.stdout), [["vm-demo/t3"]] * 5 + [["t1"], []], out.stderr)

    @unittest.skipUnless(CI, "runs only on CI: herdr-task never runs with a changed HOME on a person's machine")
    def test_commands_start(self):
        home = tempfile.mkdtemp()
        env = {**os.environ, "HOME": home, "HERDR_TASK_STORE": os.path.join(home, "tasks")}
        for args in (["--help"], ["--version"], ["list", "--help"], ["machines", "sync", "--help"]):
            p = subprocess.run([sys.executable, os.path.join(BIN, "herdr-task"), *args], capture_output=True, text=True, env=env)
            self.assertEqual(p.returncode, 0, f"herdr-task {' '.join(args)}: {p.stderr}")


class Autopilot(unittest.TestCase):
    """Autopilot's Everywhere and each space's own (herdr_task/autopilot.py), in a throwaway store,
    heartbeat folder and settings file: herdr-app and hc are never run (the window's spaces are given)."""

    def setUp(self):
        import contextlib
        import io
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.autopilot as ap
        self.ap, self.io, self.redirect = ap, io, contextlib.redirect_stdout
        # real path: the code keys folders by it (store.locate), and /var is /private/var on macOS
        self.tmp = os.path.realpath(tempfile.mkdtemp())
        self.lists = []  # the task lists a wake looks at (waiting())
        self.folder = os.path.join(self.tmp, "tt-repo")
        os.makedirs(self.folder)
        store = os.path.join(self.tmp, "tasks")

        def never(*a, **k):
            raise AssertionError("ran a program")
        import herdr_task.settings as settings
        self.settings_file = os.path.join(self.tmp, "shepherdr.json")
        p = mock.patch.object(settings, "SETTINGS", self.settings_file)
        p.start()
        self.addCleanup(p.stop)
        for name, value in (("STORE", store), ("STATE", os.path.join(store, ".autopilot.json")),
                            ("HEARTBEATS", os.path.join(self.tmp, "beats")), ("run", never), ("hc_json", never),
                            ("space_info", lambda s: ("~", self.folder) if s == "sp1" else (None, None)),
                            ("window_spaces", lambda: None), ("ensure_scheduler", self.scheduler),
                            ("coordinator_in", lambda s: None), ("store_files", lambda: self.lists)):
            p = mock.patch.object(ap, name, value)
            p.start()
            self.addCleanup(p.stop)

    def scheduler(self, quiet=False):
        self.schedulers = getattr(self, "schedulers", 0) + 1
        return {}

    def cmd(self, sub, **kw):
        import argparse
        args = argparse.Namespace(autopilot=sub, json=True, space=None, everywhere=False, every=None, on=None, heartbeat=None,
                                  name=None, space_folder=None, here=False)
        for k, v in kw.items():
            setattr(args, k, v)
        out = self.io.StringIO()
        with self.redirect(out):
            self.ap.cmd_autopilot(args)
        return json.loads(out.getvalue())

    def test_the_rule_is_cores(self):
        """The cases core/autopilot.test.js checks pilotFor() on."""
        with open(os.path.join(ROOT, "core", "autopilot.cases.json")) as f:
            cases = json.load(f)["cases"]
        for c in cases:
            eff = self.ap.resolve(c["file"], c["file"]["spaces"].get(c["space"]), c["space"], c.get("settings", {}))
            got = {"on": eff["on"], "every": (eff["every"] or {}).get("text"), "events": eff["events"], "own": eff["own"]}
            self.assertEqual(got, c["expect"], c["name"])

    def test_a_spaces_old_settings_go(self):
        """Version 1's per-space schedule and events (and a switch on Everywhere) go on the next
        read; a space keeps its on/off and how its wakes went."""
        os.makedirs(self.ap.STORE)
        with open(self.ap.STATE, "w") as f:
            json.dump({"version": 1, "everywhere": {"on": True}, "spaces": {
                "on1": {"on": True, "every": self.ap.parse_every("every 30 minutes"), "events": ["ready"], "next_run": 5},
                "off1": {"on": False, "every": self.ap.parse_every("weekdays 9:00"), "events": []}}}, f)
        with self.ap.state():
            pass
        with open(self.ap.STATE) as f:
            d = json.load(f)
        self.assertEqual(d["version"], 2)
        self.assertNotIn("on", d["everywhere"])
        self.assertEqual(d["spaces"]["on1"]["on"], True)
        for a in d["spaces"].values():
            self.assertFalse({"every", "events"} & set(a))
        on1 = next(v for v in self.cmd("list")["spaces"] if v["space"] == "on1")
        self.assertEqual((on1["on"], on1["every"], on1["events"]), (True, "every 60 minutes", []))

    def test_everywhere_unless_a_space_sets_its_own(self):
        e = self.cmd("set", everywhere=True, every="every 30 minutes", on="ready")
        self.assertEqual((e["everywhere"], e["every"], e["events"]), (True, "every 30 minutes", ["ready"]))
        # kept in Shepherdr settings, with everything else there
        with open(self.settings_file) as f:
            self.assertEqual(json.load(f)["everywhere"]["autopilot"], {"every": "every 30 minutes", "events": ["ready"]})
        v = self.cmd("show", space="sp1")
        self.assertEqual((v["on"], v["every"], v["events"], v["own"]), (False, "every 30 minutes", ["ready"], []))
        # switched on in its header: Everywhere's schedule, a next wake
        v = self.cmd("on", space="sp1")
        self.assertEqual((v["on"], v["every"], v["heartbeat"]), (True, "every 30 minutes", self.ap.heartbeat_path()))
        self.assertTrue(v["next_run"])
        self.assertTrue(os.path.exists(self.ap.heartbeat_path()))
        # Everywhere's schedule changes reach it
        v0 = v["next_run"]
        self.cmd("set", everywhere=True, every="weekdays 9:07")
        v = self.cmd("show", space="sp1")
        self.assertEqual(v["every"], "weekdays 9:07")
        self.assertNotEqual(v["next_run"], v0)
        # its own schedule wins there (and gets its own next wake); its events still follow Everywhere's
        v = self.cmd("set", space="sp1", every="every 2 hours")
        self.assertEqual((v["every"], v["events"], v["own"]), ("every 2 hours", ["ready"], ["every"]))
        self.assertEqual(self.cmd("show", space="sp2")["every"], "weekdays 9:07")
        self.cmd("set", everywhere=True, every="every 60 minutes")
        self.assertEqual(self.cmd("show", space="sp1")["every"], "every 2 hours")
        # "default": back to Everywhere's
        v = self.cmd("set", space="sp1", every="default")
        self.assertEqual((v["every"], v["own"]), ("every 60 minutes", []))
        with open(self.settings_file) as f:
            self.assertNotIn("sp1", json.load(f).get("spaces", {}))
        # a schedule that isn't one is refused, and one written by hand is passed over
        with self.assertRaises(self.ap.Fail):
            self.cmd("set", space="sp1", every="whenever")
        self.assertEqual(self.ap.resolve({}, {}, "sp1", {"spaces": {"sp1": {"autopilot": {"every": "whenever"}}}})["every"]["text"], "every 60 minutes")
        # Everywhere can't switch spaces on or off
        for sub in ("on", "off", "run-now"):
            with self.assertRaises(self.ap.Fail):
                self.cmd(sub, everywhere=True)
        v = self.cmd("off", space="sp1")
        self.assertEqual((v["on"], v["next_run"]), (False, None))
        self.assertEqual([x["space"] for x in self.cmd("list")["spaces"]], ["sp1"])

    def test_heartbeats(self):
        """Everywhere's file from before, until Settings has one; a space's own wins there."""
        v = self.cmd("heartbeat")
        self.assertEqual((v["heartbeat"], v["heartbeat_from"], v["blank"]), (self.ap.heartbeat_path(), "file", False))
        with open(v["heartbeat"]) as f:
            self.assertEqual(f.read(), self.ap.STARTER)
        # the starter is a real heartbeat: a wake brings it as it is
        self.assertIn("Push the project forward", self.ap.message("sp1", {}, ["run now"]))
        with open(v["heartbeat"], "w") as f:
            f.write("Ship the docs.\n")
        self.assertIn("Ship the docs.", self.ap.message("sp1", {}, ["run now"]))
        self.cmd("set", everywhere=True, heartbeat="Keep the tests green.")
        self.cmd("set", space="sp1", heartbeat="Only the web app.")
        one, two = self.ap.message("sp1", {}, ["run now"]), self.ap.message("sp2", {}, ["run now"])
        self.assertIn("Only the web app.", one)
        self.assertIn("this space's own", one)
        self.assertIn("Keep the tests green.", two)
        self.assertNotIn("Ship the docs.", two)
        self.assertEqual(self.cmd("heartbeat", space="sp1")["heartbeat_from"], "space")
        self.assertEqual(self.cmd("show", space="sp1")["own"], ["heartbeat"])

    def test_old_starter_gets_the_new_one(self):
        """An earlier version's starter, never edited, stands for today's: the file is rewritten,
        Settings' words are read as it; an edited one is kept, an empty one stays blank."""
        path = self.ap.heartbeat_path()
        os.makedirs(os.path.dirname(path), exist_ok=True)
        old = self.ap.OLD_STARTERS[-1]  # the first starter, a form to fill in
        with open(path, "w") as f:
            f.write(old)
        self.cmd("list")
        with open(path) as f:
            self.assertEqual(f.read(), self.ap.STARTER)
        with open(path, "w") as f:
            f.write("  " + old.strip() + "\n\n")
        self.assertIn("Push the project forward", self.ap.message("sp1", {}, ["run now"]))
        # edited, even a little: kept as the person wrote it
        edited = old.replace("Priorities:\n-", "Priorities:\n- the docs")
        with open(path, "w") as f:
            f.write(edited)
        self.cmd("heartbeat")
        self.cmd("list")
        with open(path) as f:
            self.assertEqual(f.read(), edited)
        # in Settings, Everywhere's or a space's own: read as today's
        self.cmd("set", space="sp1", heartbeat=old)
        v = self.cmd("show", space="sp1")
        self.assertEqual((v["heartbeat_text"], v["blank"]), (self.ap.STARTER, False))
        self.assertIn("Push the project forward", self.ap.message("sp1", {}, ["run now"]))
        # empty: no heartbeat, and a wake says so
        self.cmd("set", space="sp1", heartbeat="")
        self.assertTrue(self.cmd("show", space="sp1")["blank"])
        self.assertIn("not written yet", self.ap.message("sp1", {}, ["run now"]))

    def test_the_last_starter_upgrades_with_a_backup(self):
        """A machine still on the starter that said "Don't invent work" gets today's, the old one kept
        as everywhere.md.bak; a file the person edited is left as it is, with no backup."""
        path = self.ap.heartbeat_path()
        os.makedirs(os.path.dirname(path), exist_ok=True)
        last = next(x for x in self.ap.OLD_STARTERS if "Don't invent work" in x)
        with open(path, "w") as f:
            f.write(last)
        self.cmd("list")
        with open(path) as f:
            self.assertEqual(f.read(), self.ap.STARTER)
        with open(path + ".bak") as f:
            self.assertEqual(f.read(), last)
        os.remove(path + ".bak")
        # today's starter: nothing to do, no backup
        self.cmd("list")
        self.assertFalse(os.path.exists(path + ".bak"))
        # edited: kept, no backup
        edited = last.replace("4. New tasks: only follow-ups I asked for", "4. New tasks: only what I ask for")
        with open(path, "w") as f:
            f.write(edited)
        self.cmd("list")
        with open(path) as f:
            self.assertEqual(f.read(), edited)
        self.assertFalse(os.path.exists(path + ".bak"))
        self.assertIn("4. New tasks: only what I ask for", self.ap.message("sp1", {}, ["run now"]))

    def test_the_asking_starter_upgrades_with_a_backup(self):
        """3.1.5's starter (which told it to ask) becomes today's deciding one, kept as everywhere.md.bak;
        an edited one stays as it is, with no backup."""
        path = self.ap.heartbeat_path()
        os.makedirs(os.path.dirname(path), exist_ok=True)
        last = next(x for x in self.ap.OLD_STARTERS if "ask me once, in one clear question" in x)
        with open(path, "w") as f:
            f.write(last)
        self.cmd("list")
        with open(path) as f:
            self.assertEqual(f.read(), self.ap.STARTER)
        with open(path + ".bak") as f:
            self.assertEqual(f.read(), last)
        os.remove(path + ".bak")
        edited = last.replace("At most 2 a wake", "At most 1 a wake")
        with open(path, "w") as f:
            f.write(edited)
        self.cmd("list")
        with open(path) as f:
            self.assertEqual(f.read(), edited)
        self.assertFalse(os.path.exists(path + ".bak"))

    def test_a_wake_decides_instead_of_asking(self):
        """The wake and the starter tell it to make the calls itself and never ask; only a take-over,
        for what's critical, goes to the person as an alert."""
        m = self.ap.message("sp1", {}, ["schedule"])
        for words in ("Never ask the person a question on a wake", "pick the best option", "Never ask me a question",
                      "Take over: the only time you stop for me", "herdr-task alert", "carry on with other work",
                      "answer it yourself"):
            self.assertIn(words, m)
        for words in ("ask me once", "Ask before anything", "Tell me in a line if it looks right"):
            self.assertNotIn(words, m)

    def test_a_wake_drives_the_project(self):
        """By default a wake asks for new tasks that push the project forward, toward the space's
        Direction when it has one (else the project's own), and a quiet reply only when there's
        truly nothing worth doing."""
        m = self.ap.message("sp1", {}, ["schedule"])
        for words in ("Push the project forward", "add and start the next most valuable", "Direction: none set",
                      "truly nothing worth doing", "Never merge, Accept"):
            self.assertIn(words, m)
        self.assertNotIn("Don't invent work", m)
        self.cmd("set", space="sp1", direction="  Launch the\n shop  ")
        v = self.cmd("show", space="sp1")
        self.assertEqual((v["direction"], v["own"]), ("Launch the shop", ["direction"]))
        self.assertIn("Direction (set by the person, Shepherdr settings > Autopilot): Launch the shop", self.ap.message("sp1", {}, ["schedule"]))
        self.assertIn("Direction: none set", self.ap.message("sp2", {}, ["schedule"]))
        self.cmd("set", space="sp1", direction="")
        self.assertEqual(self.cmd("show", space="sp1")["direction"], "")

    def test_check_and_pushed_copies(self):
        self.assertEqual(self.cmd("check", every="Weekdays 9:00")["every"], "Weekdays 9:00")
        self.assertEqual(self.cmd("check", every="off")["every"], None)
        with self.assertRaises(self.ap.Fail):
            self.cmd("check", every="whenever")
        # a copy the window pushed here: its machine holds the truth
        with open(self.settings_file, "w") as f:
            json.dump({"pushed_from": "herdr-app", "here": "vm"}, f)
        with self.assertRaises(self.ap.Fail):
            self.cmd("set", space="sp1", every="every 2 hours")

    def test_complete_turns_it_off(self):
        import argparse
        self.cmd("on", space="sp1")
        out = self.io.StringIO()
        with self.redirect(out):
            self.ap.cmd_complete(argparse.Namespace(space="sp1", evidence="All docs merged.", json=True))
        v = json.loads(out.getvalue())
        self.assertEqual((v["on"], v["completed"]["text"]), (False, "All docs merged."))

    def test_list_names_a_space_as_the_screens_do(self):
        self.cmd("on", space="sp1")
        args = __import__("argparse").Namespace(autopilot="list", json=False)
        out = self.io.StringIO()
        with self.redirect(out):
            self.ap.cmd_autopilot(args)
        lines = out.getvalue().splitlines()
        self.assertTrue(lines[0].startswith("Everywhere: Every hour"), lines)
        self.assertTrue(lines[1].startswith("tt-repo: Autopilot on"), lines)

    def test_on_starts_this_machines_scheduler(self):
        self.cmd("on", space="sp1")
        self.assertEqual(getattr(self, "schedulers", 0), 1)
        self.cmd("off", space="sp1")
        self.assertEqual(self.schedulers, 1)

    def test_a_folder_told_from_another_machines_window(self):
        # A VM has no window: the machine whose window lists the space says its name and folder.
        v = self.cmd("on", space="sp7", name="exedev", space_folder=self.folder)
        self.assertEqual((v["on"], v["name"], v["folder"]), (True, "exedev", self.folder))

    def remote(self, answers):
        """herdr-task on other machines, as cli.remote_run runs it: answers[machine](argv) -> its JSON out."""
        from unittest import mock
        import subprocess
        import herdr_task.cli as cli
        self.sent = []

        def run(machine, argv, timeout=None):
            self.sent.append((machine, argv))
            out = answers[machine](argv)
            return subprocess.CompletedProcess(argv, 0 if out is not None else 1, json.dumps(out) if out is not None else "", "" if out is not None else "hc: unreachable")
        p = mock.patch.object(cli, "remote_run", run)
        p.start()
        self.addCleanup(p.stop)

    def test_another_machines_space_is_switched_there(self):
        from unittest import mock
        win = [{"id": "sp1", "name": "~", "folder": self.folder},
               {"id": "vm/sp9", "machine": "vm", "name": "exedev", "folder": "/home/exedev"}]
        p = mock.patch.object(self.ap, "window_spaces", lambda: win)
        p.start()
        self.addCleanup(p.stop)
        self.remote({"vm": lambda argv: {"space": "sp9", "on": argv[1] == "on", "name": "exedev", "trigger": "Every hour"}})
        v = self.cmd("on", space="vm/sp9")
        self.assertEqual(self.sent, [("vm", ["autopilot", "on", "--space", "sp9", "--json", "--name", "exedev", "--space-folder", "/home/exedev"])])
        self.assertEqual((v["space"], v["machine"], v["on"]), ("vm/sp9", "vm", True))
        # Nothing here wakes it: no record of it on this machine, and this machine's scheduler isn't asked.
        self.assertFalse(os.path.exists(self.ap.STATE))
        self.assertEqual(getattr(self, "schedulers", 0), 0)
        self.cmd("run-now", space="vm/sp9")
        self.assertEqual(self.sent[-1][1][:4], ["autopilot", "run-now", "--space", "sp9"])

    def test_another_machines_refusal_is_said_here(self):
        self.remote({"vm": lambda argv: {"error": "Autopilot is off in this space: turn it on first."}})
        with self.assertRaises(self.ap.Fail) as e:
            self.cmd("run-now", space="vm/sp9")
        self.assertIn("turn it on first", str(e.exception))
        self.remote({"vm": lambda argv: None})
        with self.assertRaises(self.ap.Fail) as e:
            self.cmd("off", space="vm/sp9")
        self.assertIn("Couldn't reach vm", str(e.exception))

    def test_another_machines_space_is_set_here_by_its_full_id(self):
        self.remote({})
        v = self.cmd("set", space="vm/sp9", every="every 2 hours")
        with open(self.settings_file) as f:
            self.assertEqual(json.load(f)["spaces"]["vm/sp9"]["autopilot"], {"every": "every 2 hours"})
        self.assertEqual((v["space"], v["own"], self.sent), ("vm/sp9", ["every"], []))

    def test_list_holds_every_machines_spaces(self):
        from unittest import mock
        self.cmd("on", space="sp1")
        self.cmd("on", space="spgone")  # a space the window no longer lists
        win = [{"id": "sp1", "name": "~", "folder": self.folder}, {"id": "sp2", "name": "docs", "folder": "/tmp/docs"},
               {"id": "vm/sp9", "machine": "vm", "name": "exedev", "folder": "/home/exedev"},
               {"id": "vm/sp8", "machine": "vm", "name": "~", "folder": "/home/exedev/dev/game"},
               {"id": "mini/sp5", "machine": "mini", "name": "Home", "folder": "/Users/me"}]
        p = mock.patch.object(self.ap, "window_spaces", lambda: win)
        p.start()
        self.addCleanup(p.stop)
        self.remote({"vm": lambda argv: {"everywhere": {"trigger": "Every hour"},
                                         "spaces": [{"space": "sp9", "on": True, "trigger": "Every hour", "next_run": None, "own": []}]},
                     "mini": lambda argv: None})
        out = self.cmd("list")
        self.assertEqual(sorted(m for m, _ in self.sent), ["mini", "vm"])
        self.assertTrue(all(argv == ["autopilot", "list", "--here", "--json"] for _, argv in self.sent))
        got = {v["space"]: v for v in out["spaces"]}
        self.assertEqual(list(got), ["sp1", "sp2", "vm/sp9", "vm/sp8", "mini/sp5"])
        self.assertEqual([got[k]["on"] for k in ("sp1", "sp2", "vm/sp9", "vm/sp8")], [True, False, True, False])
        self.assertTrue(got["mini/sp5"].get("unreachable"))
        lines = [self.ap.line_of(v) for v in out["spaces"]]
        self.assertTrue(lines[2].startswith("exedev on vm: Autopilot on"), lines)
        self.assertTrue(lines[3].startswith("game on vm: Autopilot off"), lines)
        self.assertEqual(lines[4], "Home on mini: can't tell, mini didn't answer")
        # --here: this machine's only, nothing asked elsewhere
        self.sent = []
        self.assertEqual([v["space"] for v in self.cmd("list", here=True)["spaces"]], ["sp1", "sp2"])
        self.assertEqual(self.sent, [])

    def test_the_parking_starter_upgrades_with_a_backup(self):
        """SHEP-40: 3.1.7's starter (whose quiet wakes parked behind an Accept with a bare no-reply)
        becomes today's, kept as everywhere.md.bak; an edited one stays as it is."""
        path = self.ap.heartbeat_path()
        os.makedirs(os.path.dirname(path), exist_ok=True)
        last = self.ap.OLD_STARTERS[0]
        self.assertIn("Reply only <hc no-reply/> when there's truly nothing worth doing", last)
        self.assertIn("Never ask me a question on a wake", last)
        with open(path, "w") as f:
            f.write(last)
        self.cmd("list")
        with open(path) as f:
            self.assertEqual(f.read(), self.ap.STARTER)
        with open(path + ".bak") as f:
            self.assertEqual(f.read(), last)
        for words in ("Never park on a task waiting for my Accept", "never a limit of your own",
                      'end with <hc no-reply why="', "never a bare <hc no-reply/>"):
            self.assertIn(words, self.ap.STARTER)
        self.assertNotIn("Reply only <hc no-reply/>", self.ap.STARTER)
        # in Settings, as it was: read as today's
        self.cmd("set", space="sp1", heartbeat=last)
        self.assertEqual(self.cmd("show", space="sp1")["heartbeat_text"], self.ap.STARTER)
        # edited: kept, no backup
        os.remove(path + ".bak")
        edited = last.replace("At most 2 a wake", "At most 1 a wake")
        with open(path, "w") as f:
            f.write(edited)
        self.cmd("list")
        with open(path) as f:
            self.assertEqual(f.read(), edited)
        self.assertFalse(os.path.exists(path + ".bak"))

    def test_a_wake_never_parks_on_an_accept(self):
        """SHEP-40, tor-gameboy: TG-2 Ready, TG-3 queued after it and TG-4 after TG-3. The wake names
        what waits on the person, asks for work around it, a why on a quiet reply, and no self-made limits."""
        p = os.path.join(self.tmp, "tasks", "dev-tor-gameboy.json")
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "w") as f:
            json.dump({"folder": self.folder, "prefix": "TG", "tasks": [
                {"id": 1, "state": "done", "space": "sp1"},
                {"id": 2, "state": "ready", "space": "sp1"},
                {"id": 3, "state": "queued", "after": [2]},
                {"id": 4, "state": "queued", "after": [3]},
                {"id": 5, "state": "ready", "space": "sp1"},
                {"id": 6, "state": "ready", "space": "sp9"},
                {"id": 7, "state": "queued", "after": [6], "space": "sp9"}]}, f)
        self.lists = [p]
        a = {"folder": self.folder, "progress": {"text": "TG-3 queued behind it (Elias pace: one at a time).", "at": self.ap.now()}}
        self.assertEqual(self.ap.waiting("sp1", a), ["TG-2 is Ready, waiting on the person's Accept; TG-3 and TG-4 wait behind it"])
        self.assertEqual(self.ap.waiting("sp9", {}), ["TG-6 is Ready, waiting on the person's Accept; TG-7 waits behind it"])
        m = self.ap.message("sp1", a, ["schedule"])
        for words in ("Waiting on the person:\n- TG-2 is Ready, waiting on the person's Accept; TG-3 and TG-4 wait behind it",
                      "never parks you", "work that doesn't need it", "say in one line what waits on the person's Accept",
                      "never a limit or pace of your own", "doesn't apply", '<hc no-reply why="..."/>',
                      "never a bare <hc no-reply/>"):
            self.assertIn(words, m)
        # nothing waiting: no such section
        self.lists = []
        self.assertNotIn("Waiting on the person", self.ap.message("sp1", a, ["schedule"]))

    def wake(self, ago, turn=3):
        """sp1 on, with a wake delivered to t9 `ago` ms back (its turn then: `turn`)."""
        self.cmd("on", space="sp1")
        with self.ap.state() as d:
            a = d["spaces"]["sp1"]
            a.pop("pending", None)  # the first wake switching on queued: this one stands for it
            a["inflight"] = {"at": self.ap.now() - ago, "reason": "schedule", "to": "t9", "turn": turn}
            self.ap.log_wake(a, "schedule", "Delivered", a["inflight"]["at"])

    def settled(self, k):
        """settle() with the Assistant as `k` (hc get's): the wake left, if its turn ended."""
        from unittest import mock
        k = {"state": "live", "foreground": {"name": "claude"}, **k}
        with mock.patch.object(self.ap, "hc_json", lambda *a: k), self.ap.state() as d:
            self.ap.settle(d["spaces"]["sp1"])
            return d["spaces"]["sp1"]

    def test_a_wake_ends_with_its_turn(self):
        """SHEP-34: the header leaves "Waking" once the wake's turn ends, by its count or, should
        that miss it, the Assistant idle a while; else after the timeout."""
        done = {"turn": 4, "status": {"state": "done"}}
        self.wake(5000)
        self.assertIn("inflight", self.settled({"turn": 4, "status": {"state": "working"}}))
        a = self.settled(done)
        self.assertNotIn("inflight", a)
        self.assertEqual(a["last_wake"]["outcome"], "Turn ended, no progress saved")
        self.assertEqual(self.cmd("show", space="sp1")["inflight"], None)
        # its turn not counted: idle a while after the wake ends it, not at once
        self.wake(5000)
        self.assertIn("inflight", self.settled({"turn": 3, "status": {"state": "idle"}, "status_ms": self.ap.now() - 60000}))
        # SHEP-40: a one-second quiet turn the count missed: its status settled after the wake ends it at once
        self.wake(5000)
        a = self.settled({"turn": 3, "status": {"state": "done"}, "status_ms": self.ap.now() - 4000})
        self.assertNotIn("inflight", a)
        self.assertEqual(a["last_wake"]["outcome"], "Turn ended, no progress saved")
        self.assertEqual(self.cmd("show", space="sp1")["inflight"], None)
        # still working since the wake: not ended
        self.wake(5000)
        self.assertIn("inflight", self.settled({"turn": 3, "status": {"state": "working"}, "status_ms": self.ap.now() - 4000}))
        self.wake(self.ap.IDLE_AFTER_MS + 1000)
        self.assertNotIn("inflight", self.settled({"turn": 3, "status": {"state": "idle"}}))
        # asking the person something: still its wake, until the timeout
        self.wake(self.ap.IDLE_AFTER_MS + 1000)
        self.assertIn("inflight", self.settled({"turn": 3, "status": {"state": "blocked"}}))
        self.wake(self.ap.TURN_MAX_MS + 1000)
        self.assertEqual(self.settled({"turn": 3, "status": {"state": "working"}})["last_wake"]["outcome"],
                         "No turn end seen, no progress saved")
        # its agent quit (a shell left) or its terminal gone
        self.wake(5000)
        self.assertEqual(self.settled({"foreground": {"name": "bash"}, "turn": 3})["last_wake"]["outcome"],
                         "Assistant stopped, no progress saved")

    def test_switching_on_wakes_at_once(self):
        """SHEP-5: switched on, its first wake goes now (not one interval later); already on, no second one."""
        from unittest import mock
        v = self.cmd("on", space="sp1")
        # no Assistant running yet: queued, said so, and still a scheduled wake after it
        self.assertEqual(v["pending"]["reasons"], ["switched on"])
        self.assertTrue(v["next_run"])
        self.cmd("off", space="sp1")
        self.assertEqual(self.cmd("show", space="sp1")["pending"], None)
        k = {"state": "live", "foreground": {"name": "claude"}, "turn": 2, "status": {"state": "idle"}}
        sent = []
        with mock.patch.object(self.ap, "hc_json", lambda *a: k), mock.patch.object(self.ap, "coordinator_in", lambda s: "t9"), \
                mock.patch.object(self.ap, "typing", lambda *a: False), \
                mock.patch.object(self.ap, "news_send", lambda to, m, check=True: sent.append(m) or True):
            v = self.cmd("on", space="sp1")
            self.assertEqual((len(sent), v["inflight"]["reason"]), (1, "switched on"))
            self.assertIn('reason="switched on"', sent[0])
            self.cmd("on", space="sp1")
        self.assertEqual(len(sent), 1)

    def test_run_now_goes_after_a_wake_whose_turn_ended(self):
        """A wake the scheduler hasn't closed yet never holds Run now back."""
        from unittest import mock
        self.wake(5000)
        k = {"state": "live", "foreground": {"name": "claude"}, "turn": 4, "status": {"state": "done"}}
        sent = []
        with mock.patch.object(self.ap, "hc_json", lambda *a: k), mock.patch.object(self.ap, "coordinator_in", lambda s: "t9"), \
                mock.patch.object(self.ap, "typing", lambda *a: False), \
                mock.patch.object(self.ap, "news_send", lambda to, m, check=True: sent.append(to) or True):
            v = self.cmd("run-now", space="sp1")
        self.assertEqual(sent, ["t9"])
        self.assertEqual((v["inflight"]["reason"], v["inflight"]["turn"]), ("run now", 4))
        self.assertEqual([w["outcome"] for w in v["wakes"]], ["Turn ended, no progress saved", "Delivered"])


class RemoteInstall(unittest.TestCase):
    """herdr-task --on installs itself on the other machine first, never over a machine that holds its source."""

    def setUp(self):
        from unittest import mock
        import subprocess
        sys.path.insert(0, BIN)
        import herdr_task.cli as cli
        self.cli, self.store, self.ran, self.wrote = cli, tempfile.mkdtemp(), [], []
        self.source = False

        def run(argv, cwd=None, check=False):
            self.ran.append(argv)
            code = 0 if argv[:2] != ["hc", "exec"] or "test" not in argv else (0 if self.source else 1)
            return subprocess.CompletedProcess(argv, code, "{}" if "test" not in argv else "", "")

        def write(argv, **kw):
            self.wrote.append(argv)
            return subprocess.CompletedProcess(argv, 0, b"", b"")
        for name, value in (("run", run), ("STORE", self.store), ("sync_held", lambda: None), ("guard", lambda a: None),
                            ("install_secret", lambda m: None)):
            p = mock.patch.object(cli, name, value)
            p.start()
            self.addCleanup(p.stop)
        p = mock.patch.object(cli.subprocess, "run", write)
        p.start()
        self.addCleanup(p.stop)

    def test_a_machine_without_the_source_gets_this_copy(self):
        self.cli.remote_run("vm", ["autopilot", "list", "--json"])
        self.assertEqual(self.wrote[0][:5], ["hc", "file", "write", "--on", "vm"])

    def test_a_machine_holding_the_source_is_never_written_to(self):
        self.source = True
        self.cli.remote_run("mini", ["autopilot", "list", "--json"])
        self.cli.remote_run("mini", ["autopilot", "list", "--json"])
        self.assertEqual(self.wrote, [])
        self.assertEqual(sum(1 for a in self.ran if "test" in a), 1)  # asked once, then remembered
        self.assertEqual(self.ran[-1][-3:], ["autopilot", "list", "--json"])


class AutomationMachine(unittest.TestCase):
    """auto list/show --json say whose an automation is: none here, the machine through --on (SHEP-43)."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.cli as cli
        self.cli, self.answer = cli, ""
        p = mock.patch.object(cli, "remote_run", lambda m, argv: subprocess.CompletedProcess(argv, 0, self.answer, ""))
        p.start()
        self.addCleanup(p.stop)

    def remote(self, argv, answer):
        from unittest import mock
        import io
        self.answer = json.dumps(answer)
        with mock.patch.object(sys, "stdout", io.StringIO()) as out:
            self.cli.remote("vm-b", argv)
        return json.loads(out.getvalue())

    def test_a_list_from_another_machine_names_it(self):
        got = self.remote(["auto", "list", "--json", "--all"], [{"ref": "X-A1", "folder": "/w", "machine": None}])
        self.assertEqual(got[0]["machine"], "vm-b")

    def test_one_from_another_machine_names_it(self):
        got = self.remote(["auto", "show", "--json", "X-A1"], {"ref": "X-A1", "folder": "/w", "machine": None})
        self.assertEqual(got["machine"], "vm-b")

    def test_a_feed_is_left_as_it_is(self):
        got = self.remote(["auto", "feed", "--json"], {"folder": "/w", "prefix": "X", "tasks": []})
        self.assertNotIn("machine", got)

    def test_here_it_names_none(self):
        import herdr_task.automations as au
        s = type("S", (), {"loc": {"folder": "/w"}, "prefix": "X", "data": {}})()
        with __import__("unittest").mock.patch.object(au, "settings_for", lambda s, sp: {"automations": {}}):
            v = au.auto_view(s, {"id": 1, "title": "t"})
        self.assertIsNone(v["machine"])


class SchedulerRound(unittest.TestCase):
    """The scheduler's round (automations.one_pass) and the window's or service's tick share one lock,
    both every 15 s. SHEP-34: a round that found the tick holding it skipped, and in step with the
    tick it skipped every time, so no autopilot wake was ever closed (the header stuck on "Waking")."""

    def test_a_round_waits_for_the_tick_and_runs(self):
        import fcntl
        import threading
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.automations as au
        import herdr_task.autopilot as ap
        import herdr_task.home as home
        store = tempfile.mkdtemp()
        ran = []
        patches = [mock.patch.object(au, "STORE", store), mock.patch.object(au, "SEEN", os.path.join(store, ".seen.json")),
                   mock.patch.object(au, "store_files", lambda *a: []), mock.patch.object(ap, "autopilot_pass", ran.append),
                   mock.patch.object(home, "home_pass", lambda r: None)]
        patches += [mock.patch.object(au, n, lambda *a, **k: None) for n in (
            "retry_pass", "archive_pass", "news_pass", "end_done_pass", "memory_guard", "backup_pass", "cli_check_pass", "machines_pass")]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)
        # a tick holds the lock as the round starts, and lets go a moment later
        tick = open(os.path.join(store, ".tick.lock"), "a")
        fcntl.flock(tick, fcntl.LOCK_EX)
        threading.Timer(0.5, lambda: (fcntl.flock(tick, fcntl.LOCK_UN), tick.close())).start()
        au.one_pass({})
        self.assertEqual(len(ran), 1, "the round gave way to the tick and skipped autopilot")
        self.assertTrue(os.path.exists(os.path.join(store, ".seen.json")))
        # a lock held past the wait: this round skips, the scheduler tries again in 15 s
        with open(os.path.join(store, ".tick.lock"), "a") as held:
            fcntl.flock(held, fcntl.LOCK_EX)
            with mock.patch.object(au, "LOCK_WAIT_S", 0.3):
                au.one_pass({})
        self.assertEqual(len(ran), 1)


class BackToWork(unittest.TestCase):
    """A Ready task that goes back to work is Working again (busy_again(), back_to_work()), and
    Accept retries a merge that failed once. In memory: hc, git and gh are never run."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.agents as agents
        import herdr_task.commands as commands
        import herdr_task.news as news
        import herdr_task.tick as tick
        self.agents, self.commands, self.news, self.tick, self.mock = agents, commands, news, tick, mock
        with open(os.path.join(ROOT, "core", "back-to-work.cases.json")) as f:
            self.cases = json.load(f)

        def never(*a, **k):
            raise AssertionError("ran a program")
        self.live = []  # the task's agents now (hc records)
        self.store = Store([])
        for mod, name, value in ((commands, "run", never), (commands, "git", never), (commands, "gh", never),
                                 (commands, "open_store", lambda args, write=True: self.store),
                                 (commands, "view", lambda s, t, live=None: dict(t)), (commands, "say", lambda *a: None),
                                 (commands, "live_agents", lambda key: self.live), (commands, "detail", lambda k: k),
                                 (commands, "refresh_sessions", lambda *a: None),
                                 (tick, "live_agents", lambda key: self.live), (tick, "detail", lambda k: k)):
            p = mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)

    def agent(self, a):
        """A case's agent as hc gives it."""
        return {"address": a["address"], "status": {"state": a["status"]}, "turn": a["turn"], "labels": {"herdr.role": a["role"]}}

    def ready(self, **more):
        t = {"id": 7, "title": "Web client", "state": "ready", "ready_at": 1, "ready_turns": {"t1": 5}, "agents": [], **more}
        self.store.data["tasks"] = [t]
        return t

    def args(self, **kw):
        import argparse
        return argparse.Namespace(id="SHEP-7", json=True, folder=None, **kw)

    def test_the_rule_is_cores(self):
        """The cases core/tasks.test.js checks busyAgain() on."""
        with self.mock.patch.object(self.agents, "now", lambda: self.cases["now"]):
            for c in self.cases["cases"]:
                self.assertEqual(self.agents.busy_again(c["task"], [self.agent(a) for a in c["live"]]), c["expect"], c["name"])

    def test_its_status_is_working_again(self):
        t = self.ready()
        self.assertEqual(self.agents.task_status(t, [self.agent({"address": "t1", "status": "working", "turn": 5, "role": "worker"})]),
                         ("working", None, "Working again"))
        self.assertEqual(self.agents.task_status(t, [self.agent({"address": "t1", "status": "working", "turn": 4, "role": "worker"})])[0], "ready")

    def test_a_problem_its_agent_is_on_reads_working(self):
        """SHEP-8: the cases core/tasks.test.js checks place() on (core/on-it.cases.json)."""
        with open(os.path.join(ROOT, "core", "on-it.cases.json")) as f:
            on = json.load(f)
        with self.mock.patch.object(self.agents, "now", lambda: on["now"]):
            for c in on["cases"]:
                why = self.agents.REASONS["question"] if c["expect"]["why"] == "QUESTION" else c["expect"]["why"]
                got = self.agents.task_status(dict(c["task"], id=7), [self.agent(a) for a in c["live"]])
                self.assertEqual(got, (c["expect"]["status"], c["expect"]["needs"], why), c["name"])

    def test_failing_checks_need_you_only_once_its_agent_stops_on_them(self):
        """SHEP-8 end to end: the tick finds the check failing on a Ready task; its agent at work on it
        (back at work, then waiting on CI with its own status) reads Working; idle with the check still
        failing once its line is stale, Needs you; the fix pushed and the check gone, the problem too."""
        t = self.ready(space="sp1", mode="worktree", worktree="/w", branch="b", pr={"number": 3, "repo": "o/r", "checked": 0})
        red = {"state": "OPEN", "mergeable": "MERGEABLE", "headRefOid": "a",
               "statusCheckRollup": [{"name": "check", "conclusion": "FAILURE"}]}
        views = [red]
        clock = [10_000_000]
        for mod, name, value in ((self.tick, "pr_view", lambda t: views[0]), (self.tick, "pr_news", lambda *a: None),
                                 (self.tick, "now", lambda: clock[0]), (self.agents, "now", lambda: clock[0]),
                                 (self.commands, "now", lambda: clock[0])):
            p = self.mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)
        status = lambda: self.agents.task_status(t, self.live)
        worker = lambda st, turn: [self.agent({"address": "t1", "status": st, "turn": turn, "role": "worker"})]
        # its agent is already at work when the failure is found: Working, not Needs you
        self.live = worker("working", 5)
        self.tick.facts(self.store, t, {"attention": []})
        self.assertEqual(t["attention"], "Checks failing: check.")
        self.assertEqual(status(), ("working", None, "Working again"))
        # the tick takes it back to work; it says what it does and waits on CI in the background
        self.tick.back_pass(self.store, t, {})
        self.assertEqual(t["state"], "running")
        clock[0] += 60_000
        self.commands.cmd_status(self.args(text="Re-running CI", done=95, left=None))
        self.live = worker("idle", 6)
        self.assertEqual(status(), ("working", None, None))
        # 40 minutes on, idle, the check still failing: nobody is on it
        clock[0] += 40 * 60_000
        t["pr"]["checked"] = 0
        self.tick.facts(self.store, t, {"attention": []})
        self.assertEqual(status(), ("needs", "attention", "Checks failing: check."))
        # its fix pushed, the failure gone: so is the problem
        views[0] = dict(red, statusCheckRollup=[{"name": "check", "status": "IN_PROGRESS"}])
        t["pr"]["checked"] = 0
        self.tick.facts(self.store, t, {"attention": []})
        self.assertNotIn("attention", t)

    def test_ready_keeps_each_agents_next_turn(self):
        turns = self.agents.ready_turns([{"address": "t1", "status": {"state": "working"}, "turn": 4},
                                         {"address": "t2", "status": "idle", "turn": 2}])
        self.assertEqual(turns, {"t1": 5, "t2": 2})

    def test_fix_takes_it_back_to_work(self):
        t = self.ready(mode="worktree", worktree="/w", branch="b", merge_requested=5)
        self.store.loc["folder"] = "/main"
        with self.mock.patch.object(self.commands, "tell", lambda s, t, msg, args: ("t1", False)):
            self.commands.cmd_fix(self.args(reason="Checks failing: test."))
        self.assertEqual(t["state"], "running")
        self.assertEqual(t["back"]["why"], "asked to fix: Checks failing: test.")
        self.assertEqual(t["progress"]["text"], "Fixing: Checks failing: test.")
        self.assertNotIn("ready_turns", t)
        self.assertNotIn("merge_requested", t)

    def test_resume_takes_it_back_to_work(self):
        t = self.ready(space="sp1")
        with self.mock.patch.object(self.commands, "start_again", lambda *a, **k: ({"address": "t2"}, True)):
            self.commands.cmd_resume(self.args(space=None, agent=None, argv=None, parent=None))
        self.assertEqual((t["state"], t["back"]["why"], t["progress"]["text"]), ("running", "resumed", "Working again"))

    def test_accept_refuses_a_task_back_at_work(self):
        t = self.ready()
        self.live = [self.agent({"address": "t1", "status": "working", "turn": 5, "role": "worker"})]
        with self.assertRaises(self.commands.Fail) as no:
            self.commands.cmd_accept(self.args())
        self.assertIn("is working again", str(no.exception))
        self.assertEqual(t["state"], "running")
        self.assertNotIn("accepted_at", t)

    def test_the_tick_takes_it_back_and_the_news_says_so(self):
        t = self.ready(space="sp1")
        report = {}
        self.tick.back_pass(self.store, t, report)
        self.assertEqual(t["state"], "ready")  # nothing at work
        self.live = [self.agent({"address": "t1", "status": "working", "turn": 5, "role": "worker"})]
        self.tick.back_pass(self.store, t, report)
        self.assertEqual((t["state"], report["working_again"]), ("running", ["SHEP-7"]))
        self.assertEqual(self.news.news_line(self.store, t, {}, "working-again"),
                         '<herdr-news task="SHEP-7" event="working-again">SHEP-7 is working again: its agent is at work again (Web client)</herdr-news>')

    # A merge that failed once (t75: "GraphQL: Something went wrong") never blocks the next Accept, and a pull
    # request merged meanwhile (by hand) makes the task done.
    def merging(self, views, merges):
        """Accept with GitHub giving these pull request views and merge results in turn."""
        views, merges = list(views), list(merges)
        done = []
        patches = [(self.commands, "pr_view", lambda t: views.pop(0)),
                   (self.commands, "gh", lambda *a, **k: merges.pop(0)),
                   (self.commands, "watch_main", lambda *a: None), (self.commands, "update_main", lambda *a: None),
                   (self.commands, "finish", lambda s, t, even_if_busy=False: (t.update(state="done"), done.append(t["id"])))]
        for mod, name, value in patches:
            p = self.mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)
        return done

    def pr_task(self):
        return self.ready(mode="worktree", worktree="/w", branch="b", pr={"number": 593, "repo": "o/r", "checked": 5})

    def test_accept_tries_a_failed_merge_again(self):
        t = self.pr_task()
        fine = {"state": "OPEN", "mergeable": "MERGEABLE", "statusCheckRollup": [], "headRefOid": "abc"}
        self.merging([fine, fine, fine], [(False, "", "GraphQL: Something went wrong"), (True, "", "")])
        with self.assertRaises(SystemExit):
            self.commands.cmd_accept(self.args())
        self.assertEqual((t["state"], t["attention"]), ("ready", "GitHub didn't merge it: GraphQL: Something went wrong"))
        self.assertEqual(t["pr"]["checked"], 0)  # the next tick reads it at once
        self.commands.cmd_accept(self.args())
        self.assertEqual((t["state"], t.get("merged")), ("done", True))

    def test_accept_finds_it_merged_by_hand(self):
        t = self.pr_task()
        t["attention"] = "GitHub didn't merge it: GraphQL: Something went wrong"
        self.merging([{"state": "MERGED", "mergeCommit": {"oid": "f00"}}], [])
        self.commands.cmd_accept(self.args())
        self.assertEqual((t["state"], t.get("merged")), ("done", True))
        self.assertNotIn("attention", t)

    def test_a_merge_that_errs_but_went_through_is_done(self):
        t = self.pr_task()
        fine = {"state": "OPEN", "mergeable": "MERGEABLE", "statusCheckRollup": [], "headRefOid": "abc"}
        self.merging([fine, {"state": "MERGED", "mergeCommit": {"oid": "f00"}}], [(False, "", "GraphQL: Something went wrong")])
        self.commands.cmd_accept(self.args())
        self.assertEqual((t["state"], t.get("merged")), ("done", True))

    # A pull request its agent opened itself (SHEP-13: "a pull request for branch ... already exists"):
    # ready and Accept adopt it, never fail on it.
    def github_has(self, prs):
        """GitHub with these open pull requests for the branch; opening another fails as gh does."""
        import herdr_task.github as github
        calls = []

        def fake(*a, **k):
            calls.append(a[:2])
            if a[:2] == ("pr", "list"):
                return True, json.dumps(prs), ""
            if a[:2] == ("pr", "create"):
                return False, "", "a pull request for branch \"b\" into branch \"main\" already exists"
            return True, "", ""
        for mod, name, value in ((github, "gh", fake), (github, "git", lambda *a: (True, "abc", ""))):
            p = self.mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)
        return github, calls

    def test_ready_adopts_the_open_pull_request(self):
        github, calls = self.github_has([{"number": 599, "url": "https://github.com/o/r/pull/599", "isCrossRepository": False, "baseRefName": "main", "headRefOid": "abc"}])
        t = self.ready(mode="worktree", worktree="/w", branch="b")
        self.assertEqual(github.pr_open(self.store, t, "o/r", "Done.")["number"], 599)
        self.assertEqual(t["pr"], {"number": 599, "url": "https://github.com/o/r/pull/599", "repo": "o/r"})
        self.assertNotIn(("pr", "create"), calls)
        self.assertIn(("pr", "comment"), calls)  # the summary still reaches it

    def test_ready_never_adopts_a_forks_or_another_ones(self):
        mine = {"number": 599, "url": "https://github.com/o/r/pull/599", "isCrossRepository": False, "baseRefName": "main", "headRefOid": "abc"}
        for other in ({"isCrossRepository": True}, {"baseRefName": "dev"}, {"headRefOid": "f00"}):
            github, calls = self.github_has([{**mine, **other}])
            t = self.ready(mode="worktree", worktree="/w", branch="b")
            with self.assertRaises(self.commands.Fail):
                github.pr_open(self.store, t, "o/r")
            self.assertNotIn("pr", t, other)

    def test_ready_never_opens_a_second_one_after_a_merged_one(self):
        # SHEP-59: the branch's pull request merged (squash), then more commits: no second one.
        import herdr_task.github as github
        calls = []

        def fake(*a, **k):
            calls.append(a[:2])
            if a[:2] == ("pr", "list"):
                state = a[a.index("--state") + 1]
                return True, json.dumps([] if state == "open" else [{"number": 704, "url": "u", "isCrossRepository": False,
                                                                     "baseRefName": "main", "headRefOid": "old"}]), ""
            return True, "", ""
        with self.mock.patch.object(github, "gh", fake), self.mock.patch.object(github, "git", lambda *a: (True, "abc", "")):
            t = self.ready(mode="worktree", worktree="/w", branch="b")
            with self.assertRaises(self.commands.Fail) as e:
                github.pr_open(self.store, t, "o/r")
        self.assertIn("#704", str(e.exception))
        self.assertNotIn(("pr", "create"), calls)

    def test_ready_opens_one_when_there_is_none(self):
        github, calls = self.github_has([])
        t = self.ready(mode="worktree", worktree="/w", branch="b")
        with self.assertRaises(self.commands.Fail):
            github.pr_open(self.store, t, "o/r")
        self.assertIn(("pr", "create"), calls)

    def test_accept_adopts_the_open_pull_request(self):
        self.github_has([{"number": 599, "url": "https://github.com/o/r/pull/599", "isCrossRepository": False, "baseRefName": "main", "headRefOid": "abc"}])
        t = self.ready(mode="worktree", worktree="/w", branch="b",
                       attention="Couldn't open a pull request: a pull request for branch \"b\" already exists")
        self.merging([{"state": "MERGED", "mergeCommit": {"oid": "f00"}}], [])
        with self.mock.patch.object(self.commands, "github", lambda s: "o/r"):
            self.commands.cmd_accept(self.args())
        self.assertEqual((t["state"], t["pr"]["number"]), ("done", 599))
        self.assertNotIn("attention", t)

    def test_the_tick_clears_a_failed_merge(self):
        t = self.pr_task()
        t["attention"], t["pr"]["checked"] = "GitHub didn't merge it: GraphQL: Something went wrong", 0
        fine = {"state": "OPEN", "mergeable": "MERGEABLE", "statusCheckRollup": [], "headRefOid": "abc"}
        with self.mock.patch.object(self.tick, "pr_view", lambda t: fine), self.mock.patch.object(self.tick, "pr_news", lambda *a: None):
            self.tick.facts(self.store, t, {"attention": [], "done": []})
        self.assertNotIn("attention", t)  # Ready, its Accept back


class OwnBackgroundWork(unittest.TestCase):
    """SHEP-53: an agent quiet with its own work still running (a background shell, a Monitor,
    Codex's background terminal), or whose fresh status line says it waits on CI or a review, is
    Working, not Needs you; asking the person, or quiet with nothing running, Needs you. Real logs
    in a temp folder; hc is never run."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.agents as agents
        self.agents, self.mock = agents, mock
        self.clock = 1_800_000_000_000
        p = mock.patch.object(agents, "now", lambda: self.clock)
        p.start()
        self.addCleanup(p.stop)
        self.hc = {}  # what hc ls --label herdr.task=<key> and hc get answer: {"ls": [...], "get": {address: record}}
        h = mock.patch.object(agents, "hc_json", lambda *a: ({"terminals": self.hc.get("ls", [])} if a[0] == "ls"
                                                               else self.hc.get("get", {}).get(a[1])))
        h.start()
        self.addCleanup(h.stop)
        self.dir = tempfile.mkdtemp()
        self.log = os.path.join(self.dir, "s.jsonl")
        self.lines = []
        self.task = {"id": 7, "title": "Fix", "state": "running", "started": self.clock - 3600_000,
                     "agents": [{"address": "t1", "at": self.clock - 3600_000, "log": self.log}]}
        self.idle = [{"address": "t1", "status": {"state": "idle"}, "labels": {"herdr.role": "worker"}}]
        self.stamp(-600_000, {"type": "user", "message": {"role": "user", "content": "Fix the bug"}})

    def stamp(self, ago, d):
        import datetime
        at = datetime.datetime.fromtimestamp((self.clock + ago) / 1000, datetime.timezone.utc).isoformat().replace("+00:00", "Z")
        self.lines.append(json.dumps(dict(d, timestamp=at)))
        with open(self.log, "w") as f:
            f.write("\n".join(self.lines) + "\n")
        self.agents._bg_cache.clear()

    def say(self, ago, text):
        self.stamp(ago, {"type": "assistant", "message": {"role": "assistant", "content": [{"type": "text", "text": text}]}})

    def background(self, ago, job="b1"):
        self.stamp(ago, {"type": "assistant", "message": {"role": "assistant", "content": [
            {"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": {"command": "gh run watch", "run_in_background": True}}]}})
        self.stamp(ago, {"type": "user", "toolUseResult": {"stdout": "", "backgroundTaskId": job}, "message": {"role": "user", "content": [
            {"tool_use_id": "toolu_1", "type": "tool_result", "content": f"Command running in background with ID: {job}. Output is being written to: /tmp/x"}]}})

    def finished(self, ago, job="b1"):
        self.stamp(ago, {"type": "queue-operation", "operation": "enqueue", "content":
                         f"<task-notification>\n<task-id>{job}</task-id>\n<status>completed</status>\n</task-notification>"})

    def status(self, live=None):
        return self.agents.task_status(self.task, self.idle if live is None else live)

    def test_idle_with_a_background_shell_is_working(self):
        self.background(-300_000)
        self.say(-200_000, "Review is running in the background; I'll pick it up when it ends.")
        self.assertEqual(self.status(), ("working", None, "Waiting on a background job"))
        # a Monitor counts the same
        self.lines[:] = self.lines[:1]
        self.stamp(-300_000, {"type": "user", "toolUseResult": {"taskId": "m1", "timeoutMs": 900000, "persistent": False},
                              "message": {"role": "user", "content": [{"tool_use_id": "toolu_2", "type": "tool_result",
                                                                       "content": "Monitor started (task m1, expires in 15m)"}]}})
        self.assertEqual(self.status(), ("working", None, "Waiting on a background job"))

    def test_idle_with_a_question_needs_you_even_with_a_background_shell(self):
        self.background(-300_000)
        self.say(-200_000, "Tests run in the background. Should I also update the docs?")
        self.assertEqual(self.status()[:2], ("needs", "reply"))
        # asking with its question tool or a permission: blocked, Needs you
        self.assertEqual(self.status([dict(self.idle[0], status={"state": "blocked"})])[:2], ("needs", "asks"))

    def test_idle_with_nothing_running_needs_you(self):
        self.say(-200_000, "Done with the first part.")
        self.assertEqual(self.status()[:2], ("needs", "reply"))
        # its background job ended and it went quiet again: Needs you as before
        self.background(-300_000)
        self.finished(-100_000)
        self.assertEqual(self.status()[:2], ("needs", "reply"))
        # a job started hours ago and never told ended no longer counts
        self.lines[:] = self.lines[:1]
        self.background(-4 * 3600_000)
        self.assertEqual(self.status()[:2], ("needs", "reply"))

    def test_its_status_line_says_it_waits_on_ci_or_review(self):
        self.say(-200_000, "Pushed.")
        for text, want in (("Waiting on review round 1", "working"), ("Rebased, waiting on GitHub check", "working"),
                           ("Codex reviewing the change", "working"), ("Waiting for your go on the merge", "needs"),
                           ("Fixed the menu", "needs")):
            self.task["progress"] = {"text": text, "set": self.clock - 120_000, "at": self.clock - 120_000}
            self.assertEqual(self.status()[0], want, text)
        # stale (over 30 minutes), set before its turn began, or said by Herdr: no longer counts
        self.task["progress"] = {"text": "Waiting on the GitHub check", "set": self.clock - 40 * 60_000}
        self.assertEqual(self.status()[0], "needs")
        self.task["progress"] = {"text": "Waiting on the GitHub check", "set": self.clock - 700_000}
        self.assertEqual(self.status()[0], "needs")
        self.task["progress"] = {"text": "Waiting on the GitHub check", "set": self.clock - 120_000, "by": "herdr"}
        self.assertEqual(self.status()[0], "needs")
        # a background job of its ended after it said so: it's back, then quiet
        self.task["progress"] = {"text": "Waiting on the GitHub check", "set": self.clock - 120_000}
        self.background(-110_000)
        self.finished(-60_000)
        self.assertEqual(self.status()[0], "needs")

    def test_codex_background_terminal(self):
        self.stamp(-300_000, {"type": "response_item", "payload": {"type": "function_call", "call_id": "c1", "name": "exec_command",
                                                                   "arguments": json.dumps({"cmd": "gh run watch", "yield_time_ms": 1000})}})
        self.stamp(-300_000, {"type": "response_item", "payload": {"type": "function_call_output", "call_id": "c1",
                                                                   "output": "Process running with session ID 42"}})
        self.assertEqual(self.status(), ("working", None, "Waiting on a background job"))
        self.stamp(-100_000, {"type": "response_item", "payload": {"type": "custom_tool_call_output", "call_id": "c2", "output": [
            {"type": "input_text", "text": json.dumps({"chunk_id": "a", "session_id": 42, "exit_code": 0, "output": ""})}]}})
        self.assertEqual(self.status()[:2], ("needs", "reply"))

    def test_the_tasks_panel_gets_it(self):
        """view() marks it (own_work), which news_pass keeps on the task for core/tasks.js place()."""
        import herdr_task.output as output
        self.background(-300_000)

        class S:
            write, loc, prefix = False, {"folder": "/f"}, "X"
            key = ref = lambda self, t: "X-7"
        with self.mock.patch.object(output, "waiting_for", lambda s, t: []), self.mock.patch.object(output, "waiting_ext", lambda t: []):
            v = output.view(S(), self.task, [dict(k, foreground={"name": "claude"}) for k in self.idle])
        self.assertEqual((v["status"], v["why"], v["own_work"]), ("working", "Waiting on a background job", True))
        self.assertIn("(Waiting on a background job)", output.line(v))

    def test_hc_get_foreground_and_status_msg_show_waiting_on_a_background_job(self):
        agy = {"id": 8, "title": "Build", "state": "running", "started": self.clock - 3600_000,
               "agents": [{"address": "t1", "at": self.clock - 3600_000}]}
        for fg in ({"name": "xcodebuild", "argv": ["xcodebuild", "test"]}, {"name": "sh", "argv": ["sh", "-c", "npm test"]}):
            k = {"address": "t1", "status": {"state": "done", "app": "agy"}, "foreground": fg, "labels": {"herdr.agent": "agy"}}
            self.assertTrue(self.agents.agent_running(k))
            self.assertEqual(self.agents.task_status(agy, [k]), ("working", None, "Waiting on a background job"))
        self.assertFalse(self.agents.agent_running({"foreground": {"name": "zsh", "argv": ["-zsh"]}}))
        for fg in ({"name": "agy"}, {"name": "2.1.319"}, {"name": "node", "argv": ["node", "/opt/agy/cli.js"]}):
            k = {"address": "t1", "status": {"state": "done"}, "foreground": fg}
            self.assertEqual(self.agents.task_status(agy, [k])[:2], ("needs", "reply"))
        for msg, want in (("Building in the background", "working"), ("Waiting on CI checks", "working"),
                          ("Waiting for your reply", "needs"), ("Implementing background-job status", "needs")):
            k = {"address": "t1", "status": {"state": "done", "msg": msg}, "status_ms": self.clock - 60_000, "foreground": {"name": "agy"}}
            self.assertEqual(self.agents.task_status(agy, [k])[0], want, msg)

    def test_news_and_automations_never_tell_needs_you_for_background_job(self):
        import herdr_task.automations as au, herdr_task.news as news, herdr_task.store as store
        repo, sdir = os.path.join(self.dir, "repo"), os.path.join(self.dir, "store")
        os.makedirs(repo); os.makedirs(sdir)
        term, sent = {"address": "t1", "state": "live", "status": {"state": "working"}, "foreground": {"name": "agy"}, "labels": {"herdr.agent": "agy"}}, []
        ps = [self.mock.patch.object(m, n, v) for m, n, v in (
            (store, "STORE", sdir), (news, "STORE", sdir), (news, "NEWS", os.path.join(sdir, ".news.json")),
            (news, "HELD", os.path.join(sdir, ".held.json")), (news, "now", lambda: self.clock),
            (news, "live_agents", lambda k: [term]), (news, "detail", lambda k: term), (news, "coordinator_in", lambda s: "c1"),
            (news, "hc_json", lambda *a: {"state": "live", "turn": 1}), (news, "news_send", lambda to, m, check=True: sent.append(m) or True),
            (au, "live_agents", lambda k: [term]), (au, "detail", lambda k: term))]
        for p in ps:
            p.start(); self.addCleanup(p.stop)
        with store.Store(store.locate(repo), write=True) as s:
            s.data.update(prefix="WUKO", tasks=[{"id": 1, "title": "Build", "state": "running", "space": "sp1",
                                                 "started": self.clock - 600_000, "agents": [{"address": "t1", "at": self.clock - 600_000}]}])
            s.dirty = True; term["labels"]["herdr.task"] = f"{s.slug}#1"
        seen = {}
        with store.Store(store.locate(repo), write=False) as s:
            self.assertEqual(au.events_in(s, seen), [])
        news.news_pass({}, repo)
        term.update(status={"state": "done"}, foreground={"name": "sh", "argv": ["sh", "-c", "xcodebuild test"]})
        for _ in range(2):
            self.clock += 10_000; news.news_pass({}, repo)
        self.assertEqual(sent, [])
        with store.Store(store.locate(repo), write=False) as s:
            self.assertTrue(s.data["tasks"][0].get("own_work"))
            self.assertEqual(au.events_in(s, seen), [])
        term["foreground"] = {"name": "agy"}
        for _ in range(2):
            self.clock += 10_000; news.news_pass({}, repo)
        self.assertIn('event="needs-you"', sent[0])
        with store.Store(store.locate(repo), write=False) as s:
            self.assertEqual([ev for ev, _ in au.events_in(s, seen)], ["needs-you"])


class WorkersOwnHelpers(OwnBackgroundWork):
    """SHEP-54: a worker runs its review as an hc child (herdr.task, herdr.role=reviewer, its
    terminal the parent). Never one of the task's agents (no second worker, nothing it says counts as
    the task's status); while it works, its quiet worker is Working; the Tasks panel lists it."""

    def setUp(self):
        super().setUp()
        self.idle = [dict(self.idle[0], labels={"herdr.task": "x#7"})]
        # idle here (SHEP-53's cases, inherited, hold with it), working in each case below
        self.reviewer = {"address": "t2", "parent": "t1", "state": "live", "status": "idle", "agent": "codex",
                         "labels": {"herdr.task": "x#7", "herdr.role": "reviewer", "herdr.agent": "codex"}, "created_ms": 5}
        self.worker = {"address": "t1", "state": "live", "status": "idle", "labels": {"herdr.task": "x#7"}}
        self.hc = {"ls": [self.worker, self.reviewer], "get": {"t2": self.reviewer}}

    def test_never_one_of_the_tasks_agents(self):
        self.reviewer["status"] = "working"
        self.assertEqual([k["address"] for k in self.agents.live_agents("x#7")], ["t1"])
        self.assertEqual([k["address"] for k in self.agents.live_helpers("x#7")], ["t2"])
        # hc ls gives no labels: its parent, the task's own terminal, says so
        self.hc["ls"] = [self.worker, dict(self.reviewer, labels={})]
        self.assertEqual([k["address"] for k in self.agents.live_agents("x#7")], ["t1"])
        self.assertEqual([k["address"] for k in self.agents.live_helpers("x#7")], ["t2"])
        # another machine's: its parent named there
        self.hc["ls"] = [dict(self.worker, address="vm/t1"), dict(self.reviewer, address="vm/t2", labels={})]
        self.assertEqual([k["address"] for k in self.agents.live_helpers("x#7")], ["vm/t2"])
        # one with no parent (an old herdr-task reviewer) stays the task's
        self.hc["ls"] = [self.worker, dict(self.reviewer, parent=None)]
        self.assertEqual([k["address"] for k in self.agents.live_agents("x#7")], ["t1", "t2"])

    def test_its_quiet_worker_is_working_while_it_reviews(self):
        self.reviewer["status"] = "working"
        self.say(-200_000, "Codex reviews it now.")
        self.assertEqual(self.status(), ("working", None, None))
        # asking the person still needs them
        self.say(-100_000, "Should I also update the docs?")
        self.assertEqual(self.status()[:2], ("needs", "reply"))
        # the review over: quiet again
        self.lines[:] = self.lines[:1]
        self.say(-100_000, "Done.")
        self.reviewer["status"] = "idle"
        self.assertEqual(self.status()[:2], ("needs", "reply"))
        # another worker's child doesn't count
        self.reviewer.update(status="working", parent="t9")
        self.assertEqual(self.status()[:2], ("needs", "reply"))

    def test_the_tasks_panel_lists_it(self):
        import herdr_task.output as output
        self.reviewer["status"] = "working"

        class S:
            write, loc, prefix = False, {"folder": "/f"}, "X"
            key = lambda self, t: "x#7"
            ref = lambda self, t: "X-7"
        with self.mock.patch.object(output, "waiting_for", lambda s, t: []), self.mock.patch.object(output, "waiting_ext", lambda t: []), \
                self.mock.patch.object(output, "detail", lambda k: self.hc["get"].get(k["address"], k)):
            v = output.view(S(), self.task, [dict(k, foreground={"name": "claude"}) for k in self.idle])
        self.assertEqual(v["status"], "working")
        self.assertEqual(v["subagents"], [{"address": "t2", "parent": "t1", "role": "reviewer", "agent": "codex", "name": None,
                                           "status": "working", "at": 5}])
        self.assertEqual([x["role"] for x in v["live"]], ["worker"])


class NewWorktrees(unittest.TestCase):
    """SHEP-74: a new task's worktree starts from its base fetched first (#574), on its own branch
    name or its folder's Branch names pattern (#570). Throwaway repos (a bare origin and a clone)
    and a throwaway settings file; hc and gh are never run."""

    def setUp(self):
        from unittest import mock
        import shutil
        sys.path.insert(0, BIN)
        import herdr_task.launch as launch
        import herdr_task.settings as settings
        from herdr_task.base import Fail
        self.launch, self.settings, self.Fail = launch, settings, Fail
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.origin, self.main, self.other = (os.path.join(self.root, x) for x in ("origin.git", "repo", "other"))
        self.git(self.root, "init", "-q", "--bare", "-b", "main", self.origin)
        self.git(self.root, "clone", "-q", self.origin, self.main)
        self.commit(self.main, "first")
        self.git(self.main, "push", "-q", "origin", "main")
        self.git(self.root, "clone", "-q", self.origin, self.other)
        self.settings_file = os.path.join(self.root, "shepherdr.json")
        for mod, name, value in ((launch, "WORKTREES", os.path.join(self.root, "wt")), (settings, "SETTINGS", self.settings_file)):
            p = mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)
        self.store = Store([])
        self.store.loc["folder"] = self.main

    def git(self, cwd, *args):
        return subprocess.run(["git", "-C", cwd, *args], capture_output=True, text=True, check=True).stdout.strip()

    def commit(self, cwd, words):
        self.git(cwd, "-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false",
                 "commit", "-q", "--allow-empty", "-m", words)
        return self.git(cwd, "rev-parse", "HEAD")

    def task(self, title="Check base", **kw):
        t = {"id": len(self.store.data["tasks"]) + 1, "title": title, "mode": "worktree", "state": "todo", **kw}
        self.store.data["tasks"].append(t)
        return t

    def new_worktree(self, t=None):
        t = t or self.task()
        return t, self.launch.folder_for(self.store, t)

    def test_starts_from_the_remote_base_when_the_folder_is_behind(self):
        theirs = self.commit(self.other, "merged on GitHub")
        self.git(self.other, "push", "-q", "origin", "main")
        before = self.git(self.main, "rev-parse", "main")
        t, path = self.new_worktree()
        self.assertEqual(self.git(path, "rev-parse", "HEAD"), theirs)
        self.assertEqual(t["start"], theirs)
        self.assertEqual(t["base"], "main")
        self.assertEqual(self.git(self.main, "rev-parse", "main"), before)  # the folder's own checkout stays as it was
        # no upstream: the task's branch never tracks origin/main
        self.assertEqual(subprocess.run(["git", "-C", path, "rev-parse", "--abbrev-ref", "@{u}"], capture_output=True).returncode, 128)

    def test_keeps_the_local_base_when_it_has_work_of_its_own(self):
        mine = self.commit(self.main, "merged here, not pushed")
        _, path = self.new_worktree()
        self.assertEqual(self.git(path, "rev-parse", "HEAD"), mine)

    def test_offline_starts_from_the_local_base(self):
        self.git(self.main, "remote", "set-url", "origin", os.path.join(self.root, "gone.git"))
        here = self.git(self.main, "rev-parse", "main")
        _, path = self.new_worktree()
        self.assertEqual(self.git(path, "rev-parse", "HEAD"), here)

    def test_default_branch_name_stays(self):
        t, _ = self.new_worktree(self.task("Fix the login page"))
        self.assertEqual(t["branch"], "task/1-fix-the-login-page")

    def write_settings(self, d):
        with open(self.settings_file, "w") as f:
            json.dump(d, f)

    def test_the_folders_pattern(self):
        self.write_settings({"everywhere": {"tasks": {"branch": "feat/{key}-{slug}"}},
                             "spaces": {"sp1": {"folder": self.main, "tasks": {"branch": "{key}"}}}})
        t, path = self.new_worktree(self.task("Fix login"))
        self.assertEqual(t["branch"], "SHEP-1")
        self.assertEqual(self.git(path, "rev-parse", "--abbrev-ref", "HEAD"), "SHEP-1")
        self.write_settings({"everywhere": {"tasks": {"branch": "feat/{key}-{slug}"}}})
        self.assertEqual(self.new_worktree(self.task("Fix login"))[0]["branch"], "feat/SHEP-2-fix-login")
        # one that can't be used (no {n} or {key}: every task would share it) falls back to the default
        self.write_settings({"everywhere": {"tasks": {"branch": "feat/{slug}"}}})
        self.assertEqual(self.new_worktree(self.task("Fix login"))[0]["branch"], "task/3-fix-login")

    def test_pattern_problems(self):
        problem = self.settings.branch_pattern_problem
        for ok in ("", "task/{n}-{slug}", "{key}", "users/me/{key}-{slug}"):
            self.assertIsNone(problem(ok), ok)
        for bad in ("feat/{slug}", "x/{n}/{bad}", "{n} x", "-{n}", "a..{n}", "{key}.lock"):
            self.assertIsNotNone(problem(bad), bad)

    def test_an_exact_branch(self):
        t = self.task("Fix login")
        self.launch.set_branch(self.store, t, "ABC-2137")
        _, path = self.new_worktree(t)
        self.assertEqual(t["branch"], "ABC-2137")
        self.assertEqual(self.git(path, "rev-parse", "--abbrev-ref", "HEAD"), "ABC-2137")
        self.assertTrue(path.endswith("/ABC-2137"))
        self.launch.set_branch(self.store, t, "ABC-2137")  # the same again: nothing to do
        with self.assertRaisesRegex(self.Fail, "can't change now"):
            self.launch.set_branch(self.store, t, "ABC-1")

    def test_an_exact_branch_is_refused_when_taken_or_invalid(self):
        set_branch, t = self.launch.set_branch, self.task()
        for bad in ("two words", "a..b", "-x", "x.lock"):
            with self.assertRaisesRegex(self.Fail, "isn't a valid branch name"):
                set_branch(self.store, t, bad)
        self.git(self.main, "branch", "here-already")
        with self.assertRaisesRegex(self.Fail, "already here"):
            set_branch(self.store, t, "here-already")
        self.git(self.other, "push", "-q", "origin", "HEAD:refs/heads/ABC-1")  # pushed by someone else, never fetched here
        with self.assertRaisesRegex(self.Fail, "origin already has"):
            set_branch(self.store, t, "ABC-1")
        set_branch(self.store, self.task(), "ABC-2")
        with self.assertRaisesRegex(self.Fail, "SHEP-2 already uses it"):
            set_branch(self.store, t, "ABC-2")
        self.assertNotIn("branch", t)
        set_branch(self.store, t, "ABC-3")
        set_branch(self.store, t, "")  # back to the pattern
        self.assertNotIn("branch", t)
        with self.assertRaisesRegex(self.Fail, "in a worktree"):
            set_branch(self.store, self.task(mode="here"), "ABC-4")


class MergedOnlyWithOwnWork(unittest.TestCase):
    """SHEP-23: a task counts as merged only when work of its own landed (its pull request merged, or
    commits made on its branch now in the base), never when its branch just caught up with main, and
    never while its agent is mid-turn. A throwaway git repo; hc and gh are never run."""

    def setUp(self):
        from unittest import mock
        import shutil
        sys.path.insert(0, BIN)
        import herdr_task.commands as commands
        import herdr_task.tick as tick
        self.commands, self.tick = commands, tick
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.main, wt = os.path.join(self.root, "repo"), os.path.join(self.root, "wt")
        self.git(self.root, "init", "-q", "-b", "main", self.main)
        for k, v in (("user.name", "T"), ("user.email", "t@example.com"), ("commit.gpgsign", "false")):
            self.git(self.main, "config", k, v)
        self.commit(self.main, "base")
        self.git(self.main, "worktree", "add", "-q", "-b", "task/1", wt, "HEAD")
        self.live = []  # the task's agents now (hc records)
        self.store = Store([{"id": 1, "title": "Reactions", "state": "running", "mode": "worktree", "worktree": wt,
                             "branch": "task/1", "base": "main", "start": self.git(wt, "rev-parse", "HEAD"), "agents": []}])
        self.store.loc["folder"] = self.main
        self.task = self.store.data["tasks"][0]
        done = lambda s, t, even_if_busy=False: t.update(state="done")
        for mod, name, value in ((tick, "live_agents", lambda key: self.live), (tick, "finish", done),
                                 (tick, "watch_main", lambda *a: None), (commands, "finish", done),
                                 (commands, "open_store", lambda args, write=True: self.store),
                                 (commands, "view", lambda s, t, live=None: dict(t)), (commands, "say", lambda *a: None),
                                 (commands, "live_agents", lambda key: self.live), (commands, "detail", lambda k: k)):
            p = mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)

    def git(self, cwd, *args):
        return subprocess.run(["git", "-C", cwd, *args], capture_output=True, text=True, check=True).stdout.strip()

    def commit(self, cwd, words):
        self.git(cwd, "commit", "-q", "--allow-empty", "-m", words)
        return self.git(cwd, "rev-parse", "HEAD")

    def tick_once(self):
        self.tick.facts(self.store, self.task, {"attention": [], "done": []})
        return self.task["state"], bool(self.task.get("merged"))

    def working(self):
        self.live = [{"address": "t1", "status": {"state": "working"}}]

    def test_a_fresh_branch_is_not_merged(self):
        self.assertEqual(self.tick_once(), ("running", False))

    def test_a_branch_caught_up_with_main_is_not_merged(self):
        self.commit(self.main, "another task merged")
        self.git(self.task["worktree"], "merge", "-q", "--ff-only", "main")  # SHEP-17's agent
        self.assertEqual(self.tick_once(), ("running", False))
        self.assertNotIn("own", self.task)
        # Accepted so: done, nothing to merge, never "merged".
        self.task["state"] = "ready"
        import argparse
        self.commands.cmd_accept(argparse.Namespace(id="SHEP-1", json=True, folder=None))
        self.assertEqual(self.task["state"], "done")
        self.assertFalse(self.task.get("merged"))

    def test_a_branch_caught_up_with_origin_main_is_not_merged(self):
        self.git(self.main, "update-ref", "refs/remotes/origin/main", self.commit(self.main, "on GitHub"))
        self.git(self.main, "reset", "-q", "--hard", "HEAD~1")  # local main behind its remote
        self.git(self.task["worktree"], "merge", "-q", "--ff-only", "origin/main")
        self.assertEqual(self.tick_once(), ("running", False))
        self.git(self.main, "merge", "-q", "--ff-only", "origin/main")  # main pulled
        self.assertEqual(self.tick_once(), ("running", False))

    def test_its_commits_merged_by_hand_make_it_done(self):
        own = self.commit(self.task["worktree"], "reactions on the left")
        self.assertEqual(self.tick_once(), ("running", False))
        self.assertEqual(self.task["own"], own)
        self.git(self.main, "merge", "-q", "--ff-only", "task/1")
        self.working()
        self.assertEqual(self.tick_once(), ("running", False))  # never under a working agent
        self.live = []
        self.assertEqual(self.tick_once(), ("done", True))

    def test_its_merged_pull_request_makes_it_done(self):
        self.commit(self.task["worktree"], "reactions on the left")
        self.task.update(state="ready", pr={"number": 610, "repo": "o/r", "checked": 0})
        with self.mock_pr({"state": "MERGED", "mergeCommit": {"oid": "f00"}}):
            self.working()
            self.assertEqual(self.tick_once(), ("ready", False))
            self.live, self.task["pr"]["checked"] = [], 0
            self.assertEqual(self.tick_once(), ("done", True))

    def mock_pr(self, v):
        from unittest import mock
        return mock.patch.object(self.tick, "pr_view", lambda t: v)

    # SHEP-59: a pull request squash-merged (its commits never in main), then ready: done, never a
    # second pull request; nor when its work is in main some other way (nothing left to merge).
    def change(self, cwd, words):
        with open(os.path.join(cwd, "reactions.txt"), "w") as f:
            f.write(words)
        self.git(cwd, "add", "reactions.txt")
        return self.commit(cwd, words)

    def squash_into_main(self):
        self.git(self.main, "merge", "-q", "--squash", "task/1")
        return self.commit(self.main, "SHEP-1 Reactions (#704)")

    def github(self, merged):
        """GitHub for o/r with these merged pull requests for the branch (none open); the gh calls."""
        from unittest import mock
        import herdr_task.github as github
        calls = []

        def fake(*a, **k):
            calls.append(a[:2])
            if a[:2] == ("pr", "list"):
                return True, json.dumps(merged if a[a.index("--state") + 1] == "merged" else []), ""
            if a[:2] == ("pr", "create"):
                return True, "https://github.com/o/r/pull/705", ""
            return True, "", ""
        for mod, name, value in ((github, "gh", fake), (github, "github", lambda s: "o/r"), (self.commands, "github", lambda s: "o/r"),
                                 (github, "watch_main", lambda *a: None), (self.commands, "watch_main", lambda *a: None),
                                 (self.tick, "github", lambda s: "o/r")):
            p = mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)
        return calls

    def ready_now(self):
        import argparse
        from unittest import mock
        with mock.patch.object(self.commands, "review_refusal", lambda *a: None), \
                mock.patch.object(self.commands, "find_proof", lambda *a: None), \
                mock.patch.object(self.commands, "task_settings", lambda *a: []), \
                mock.patch.object(self.commands, "ready_turns", lambda *a: {}), \
                mock.patch.object(self.commands, "pr_open", lambda *a: (_ for _ in ()).throw(AssertionError("a pull request opened"))):
            self.commands.cmd_ready(argparse.Namespace(id="SHEP-1", json=True, folder=None, summary="Done.", details=None,
                                                       proof=None, agents_md=None))
        return self.task["state"], bool(self.task.get("merged"))

    def test_ready_after_its_pull_request_squash_merged_is_done(self):
        tip = self.change(self.task["worktree"], "reactions on the left")
        self.squash_into_main()
        self.commit(self.main, "another task merged after it")
        calls = self.github([{"number": 704, "url": "https://github.com/o/r/pull/704", "isCrossRepository": False,
                              "baseRefName": "main", "headRefOid": tip, "mergeCommit": {"oid": "f00"}}])
        self.assertEqual(self.ready_now(), ("done", True))
        self.assertEqual(self.task["pr"]["number"], 704)
        self.assertNotIn(("pr", "create"), calls)

    def test_ready_with_nothing_left_to_merge_is_done(self):
        self.change(self.task["worktree"], "reactions on the left")
        self.squash_into_main()  # by hand, no pull request
        calls = self.github([])
        self.assertEqual(self.ready_now(), ("done", True))
        self.assertNotIn("pr", self.task)
        self.assertNotIn(("pr", "create"), calls)

    # SHEP-96: a task with no commits beyond its base (research, an artifact only) still waits in
    # Ready for the person's Accept, its words kept; Accept just marks it done and cleans up.
    def ready_said(self, summary="Found three options.", details="- Read five sources"):
        import argparse
        from unittest import mock
        with mock.patch.object(self.commands, "review_refusal", lambda *a: None), \
                mock.patch.object(self.commands, "find_proof", lambda *a: None), \
                mock.patch.object(self.commands, "task_settings", lambda *a: []), \
                mock.patch.object(self.commands, "ready_turns", lambda *a: {}), \
                mock.patch.object(self.commands, "merge_policy", lambda *a: "auto"), \
                mock.patch.object(self.commands, "tell", lambda *a: (_ for _ in ()).throw(AssertionError("asked to merge"))), \
                mock.patch.object(self.commands, "pr_open", lambda *a: (_ for _ in ()).throw(AssertionError("a pull request opened"))):
            self.commands.cmd_ready(argparse.Namespace(id="SHEP-1", json=True, folder=None, summary=summary, details=details,
                                                       proof=None, agents_md=None))
        return self.task["state"]

    def accept_now(self):
        import argparse
        from unittest import mock
        import herdr_task.github as github
        with mock.patch.object(self.commands, "finish", github.finish):  # the real one: worktree and branch go
            self.commands.cmd_accept(argparse.Namespace(id="SHEP-1", json=True, folder=None))

    def test_ready_with_no_commits_waits_for_accept(self):
        calls = self.github([])
        self.assertEqual(self.ready_said(), "ready")
        self.assertEqual((self.task["summary"], self.task["details"]), ("Found three options.", "- Read five sources"))
        self.assertTrue(self.task["nothing_to_merge"])
        self.assertNotIn("auto_merge", self.task)
        self.assertNotIn("pr", self.task)
        self.assertNotIn(("pr", "create"), calls)
        self.assertTrue(os.path.isdir(self.task["worktree"]))
        # the tick leaves it for the person too
        self.assertEqual(self.tick_once(), ("ready", False))
        self.task["gh_checked"] = 0
        self.assertEqual(self.tick_once(), ("ready", False))
        # Accept: done, never merged, its worktree and branch cleaned up
        self.accept_now()
        self.assertEqual(self.task["state"], "done")
        self.assertFalse(self.task.get("merged"))
        self.assertTrue(self.task["nothing_to_merge"])
        self.assertFalse(os.path.isdir(self.task["worktree"]))
        self.assertEqual(self.git(self.main, "branch", "--list", "task/1"), "")

    def test_ready_only_caught_up_with_main_waits_for_accept(self):
        self.github([])
        self.commit(self.main, "another task merged")
        self.git(self.task["worktree"], "merge", "-q", "--ff-only", "main")
        self.assertEqual(self.ready_said(), "ready")
        self.assertTrue(self.task["nothing_to_merge"])
        self.accept_now()
        self.assertEqual(self.task["state"], "done")
        self.assertFalse(self.task.get("merged"))

    def test_commits_after_a_no_commit_ready_go_the_usual_way(self):
        self.github([])
        self.assertEqual(self.ready_said(), "ready")
        self.task["state"] = "running"  # back at work
        self.change(self.task["worktree"], "reactions on the left")
        with self.assertRaises(AssertionError):
            self.ready_said()  # on to pr_open
        self.assertNotIn("nothing_to_merge", self.task)

    def test_ready_with_work_left_opens_its_pull_request(self):
        self.change(self.task["worktree"], "reactions on the left")
        self.github([])
        with self.assertRaises(AssertionError):
            self.ready_now()  # on to pr_open

    def test_the_tick_finds_a_squash_merged_pull_request(self):
        tip = self.change(self.task["worktree"], "reactions on the left")
        self.assertEqual(self.tick_once(), ("running", False))
        self.squash_into_main()
        self.github([{"number": 704, "url": "u", "isCrossRepository": False, "baseRefName": "main", "headRefOid": tip}])
        self.working()
        self.assertEqual(self.tick_once(), ("running", False))  # never under a working agent
        self.live, self.task["gh_checked"] = [], 0  # GitHub asked at most once a minute
        self.assertEqual(self.tick_once(), ("done", True))
        self.assertEqual(self.task["pr"]["number"], 704)

    def test_the_tick_finds_a_ready_task_already_in_main(self):
        self.change(self.task["worktree"], "reactions on the left")
        self.tick_once()
        self.task["state"] = "ready"
        self.github([])
        self.squash_into_main()
        self.task["gh_checked"] = 0
        self.assertEqual(self.tick_once(), ("done", True))


class AcceptKeepsLaterCommits(unittest.TestCase):
    """FAC-55: Accept on a pull request never deletes a commit made after ready. A real throwaway git
    repo with the real merge_now and finish; only gh, pr_view and the main-branch upkeep are mocked."""

    setUp_repo, git, commit = MergedOnlyWithOwnWork.setUp, MergedOnlyWithOwnWork.git, MergedOnlyWithOwnWork.commit

    def setUp(self):
        self.setUp_repo()
        import herdr_task.github as github
        self.mock = __import__("unittest").mock
        self.ready_at = self.commit(self.task["worktree"], "ready")
        self.task.update(head=self.ready_at, state="ready", pr={"number": 7, "repo": "o/r", "checked": 5})
        self.merges = []
        view = {"state": "OPEN", "mergeable": "MERGEABLE", "statusCheckRollup": [], "headRefOid": self.ready_at}
        for mod, name, value in ((self.commands, "pr_view", lambda t: view), (self.commands, "finish", github.finish),
                                 (self.commands, "gh", lambda *a, **k: (self.merges.append(a), (True, "", ""))[1]),
                                 (self.commands, "watch_main", lambda *a: None), (self.commands, "update_main", lambda *a: None),
                                 (github, "live_agents", lambda key: [])):
            p = self.mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)

    def branches(self):
        return self.git(self.main, "branch", "--list", "task/1")

    def test_a_commit_after_ready_is_kept_and_nothing_merges(self):
        later = self.commit(self.task["worktree"], "after ready")
        self.assertEqual(self.commands.merge_now(self.store, self.task),
                         "task/1 has moved on since it said ready: its agent says ready again first.")
        self.assertEqual((self.merges, self.task["state"]), ([], "ready"))
        self.assertEqual(self.git(self.main, "rev-parse", "task/1"), later)
        self.assertTrue(os.path.isdir(self.task["worktree"]))

    def test_the_normal_case_merges_at_the_ready_commit(self):
        self.assertIsNone(self.commands.merge_now(self.store, self.task))
        self.assertEqual(self.merges[0][-2:], ("--match-head-commit", self.ready_at))
        self.assertEqual((self.task["state"], self.branches()), ("done", ""))


class TimeLeft(unittest.TestCase):
    """Every status gives the time left, a rough guess beats none (SHEP-13): the minutes show from
    the first one, one without --left asks for it, and every brief and template says so. In memory."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.commands as commands
        self.commands, self.said = commands, []
        self.store = Store([{"id": 7, "title": "Web client", "state": "running", "agents": []}])
        for name, value in (("open_store", lambda args, write=True: self.store), ("view", lambda s, t, live=None: {}),
                            ("live_agents", lambda key: []), ("say", lambda args, data, text: self.said.append(text))):
            p = mock.patch.object(commands, name, value)
            p.start()
            self.addCleanup(p.stop)

    def status(self, left=None, done=None):
        import argparse
        self.commands.cmd_status(argparse.Namespace(id="SHEP-7", json=False, folder=None, text="Starting", done=done, left=left))
        return self.said[-1]

    def test_the_first_status_shows_its_minutes(self):
        from herdr_task.output import report_of
        self.status("20m", 0)
        self.assertEqual(report_of(self.store.find(7), "working")["left"], 20)

    def test_a_status_without_left_asks_for_it(self):
        self.assertIn("Add --left", self.status())
        self.assertNotIn("Add --left", self.status("20m"))

    def test_every_brief_and_template_says_always(self):
        from herdr_task import templates
        launch = open(os.path.join(BIN, "herdr_task", "launch.py")).read()
        for words in (templates.WORKER_MD, open(os.path.join(ROOT, "templates", "worker.md")).read(), launch):
            self.assertNotIn("real estimate", words)
            self.assertIn("rough guess beats none", words)
        self.assertIn(templates.WORKER_MD_BEFORE_LEFT, templates.OLD_WORKERS)  # an unedited old one gets today's


class ReviewAsAHelperAgent(unittest.TestCase):
    """SHEP-54: workers run their review as an interactive hc child people see, never headless; an
    unedited earlier worker or reviewer template gets today's words."""

    def test_the_words(self):
        sys.path.insert(0, BIN)
        from herdr_task import templates
        with open(os.path.join(ROOT, "templates", "worker.md")) as f:
            worker = f.read()
        for words in (templates.WORKER_MD, worker):
            self.assertIn("--label herdr.role=reviewer", words)
            self.assertIn("herdr.task=$HERDR_BROWSER_TASK", words)
            self.assertIn("codex --sandbox read-only --ask-for-approval never", words)
            self.assertIn("claude --permission-mode plan", words)
            self.assertIn("hc wait <its name>", words)
            self.assertNotIn("< /dev/null", words)
        self.assertIn(templates.WORKER_MD_BEFORE_SUBAGENTS, templates.OLD_WORKERS)
        with open(os.path.join(ROOT, "templates", "reviewer.md")) as f:
            self.assertIn(templates.REVIEWER_MD.strip(), f.read())

    def test_an_unedited_old_reviewer_gets_todays(self):
        sys.path.insert(0, BIN)
        from unittest import mock
        from herdr_task import templates
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "reviewer.md"), "w") as f:
            f.write(templates.TEMPLATE_HEADS["reviewer"] + templates.REVIEWER_MD_BEFORE_SUBAGENT)
        with mock.patch.object(templates, "TEMPLATES", d):
            self.assertEqual(templates.instructions("reviewer"), templates.REVIEWER_MD.strip())
            with open(os.path.join(d, "reviewer.md"), "w") as f:
                f.write(templates.TEMPLATE_HEADS["reviewer"] + "My own words.")
            self.assertEqual(templates.instructions("reviewer"), "My own words.")


class Store:
    """Enough of herdr_task.store.Store for the commands, in memory."""
    def __init__(self, tasks):
        self.data, self.loc, self.dirty, self.write = {"tasks": tasks}, {"folder": "/f", "kind": "repo", "name": "f"}, False, True
        self.prefix, self.slug = "SHEP", "f"

    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False

    def find(self, ref):
        return next(t for t in self.data["tasks"] if t["id"] == int(str(ref).split("-")[-1]))

    def ref(self, t):
        return f"SHEP-{t['id']}"

    def key(self, t):
        return f"f#{t['id']}"


class GateReview(unittest.TestCase):
    """FAC-13: in a gate folder (review.min > 0) ready starts herdr-task's own reviewer for the exact
    head, and only that reviewer's terminal, on that unmoved head, records a round. A throwaway git
    repo; hc run, the worker's messages and GitHub are faked. Records go through the contract's validator."""

    def setUp(self):
        from unittest import mock
        import shutil
        sys.path.insert(0, BIN)
        sys.path.insert(0, os.path.join(ROOT, "test"))
        import herdr_task.commands as commands
        import herdr_task.launch as launch
        import herdr_task.templates as templates
        import herdr_task.settings as settings
        import contract_gate_evidence as contract
        self.commands, self.contract = commands, contract
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.main, self.wt = os.path.join(self.root, "repo"), os.path.join(self.root, "wt")
        self.git(self.root, "init", "-q", "-b", "main", self.main)
        for k, v in (("user.name", "T"), ("user.email", "t@example.com"), ("commit.gpgsign", "false")):
            self.git(self.main, "config", k, v)
        self.git(self.main, "commit", "-q", "--allow-empty", "-m", "base")
        self.git(self.main, "worktree", "add", "-q", "-b", "task/13", self.wt, "HEAD")
        start = self.git(self.wt, "rev-parse", "HEAD")
        self.head = self.change("say \"hi\" $HOME `date`")
        self.store = Store([{"id": 13, "title": "Gate", "state": "running", "mode": "worktree", "worktree": self.wt,
                             "branch": "task/13", "base": "main", "start": start, "agents": [{"address": "w1"}],
                             "prompt": "Make the gate.", "space": "sp1", "review_gate": True, "started": 1, "set": {"review_min": 2},
                             "reviewer": "agy", "reviewer_model": "gemini-4-argon-high"}])
        self.store.loc["folder"] = self.main
        self.task = self.store.data["tasks"][0]
        self.runs, self.told = [], []
        self.reviewer_term = {"state": "live", "agent": "agy"}  # what hc get says of a reviewer

        def hc_run(argv, **k):
            self.runs.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps({"address": f"term-{len(self.runs)}"}), "")
        tdir = tempfile.mkdtemp(dir=self.root)
        for mod, name, value in ((commands, "open_store", lambda args, write=True: self.store),
                                 (commands, "view", lambda s, t, live=None: dict(t)), (commands, "say", lambda *a: None),
                                 (commands, "find_proof", lambda *a: None), (commands, "ready_turns", lambda *a: {}),
                                 (commands, "live_agents", lambda key: []), (commands, "detail", lambda k: k),
                                 (commands, "github", lambda s: None), (commands, "already_merged", lambda *a: None),
                                 (commands, "own_work", lambda *a: None), (commands, "guard", lambda *a: None),
                                 (commands, "tell", lambda s, t, msg, args: self.told.append(msg) or ("w1", False)),
                                 (launch, "run", hc_run), (launch, "hc_json", lambda *a: None),
                                 (launch, "low_priority", lambda argv, name: argv), (templates, "TEMPLATES", tdir),
                                 (settings, "SETTINGS", os.path.join(tdir, "none.json")),
                                 (commands, "hc_json", lambda *a: self.reviewer_term),
                                 (commands, "sandboxes", lambda argv: False), (launch, "sandboxes", lambda argv: False),
                                 (commands, "subprocess", mock.Mock())):  # no real `hc kill` of a reviewer
            p = mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)
        self.env("HC_TERMINAL_ID", None)

    def env(self, k, v):
        from unittest import mock
        p = mock.patch.dict(os.environ, {k: v} if v else {})
        p.start()
        self.addCleanup(p.stop)
        if not v:
            os.environ.pop(k, None)

    def git(self, cwd, *args):
        return subprocess.run(["git", "-C", cwd, *args], capture_output=True, text=True, check=True).stdout.strip()

    def change(self, words):
        with open(os.path.join(self.wt, "gate.txt"), "w") as f:
            f.write(words)
        self.git(self.wt, "add", "gate.txt")
        self.git(self.wt, "commit", "-q", "-m", "gate")
        return self.git(self.wt, "rev-parse", "HEAD")

    def ready(self):
        import argparse
        self.commands.cmd_ready(argparse.Namespace(id="SHEP-13", json=True, folder=None, summary="The gate.", details=None,
                                                   proof=None, agents_md=None))

    def verdict(self, by, passed=True, changes=None, must_fix=0):
        import argparse
        self.env("HC_TERMINAL_ID", by)
        self.commands.cmd_review(argparse.Namespace(id="SHEP-13", json=True, folder=None, passed=passed, changes=changes,
                                                    changes_file=None, notes=None, must_fix=must_fix))

    def valid(self):
        for r in self.task.get("review_runs") or []:
            self.contract.validate_review_run(r)
        for r in self.task.get("reviews") or []:
            self.contract.validate_review(r)

    def test_ready_starts_a_reviewer_for_the_head(self):
        self.ready()
        self.assertEqual(self.task["state"], "running")  # not Ready until the gate's rounds pass
        self.assertEqual(len(self.runs), 1)
        argv = self.runs[0]
        self.assertIn("herdr.role=reviewer", argv)
        self.assertEqual(argv[argv.index("--cwd") + 1], self.wt)
        cmd = argv[argv.index("--") + 1:]
        self.assertIn("agy", [os.path.basename(a) for a in cmd])
        self.assertEqual(cmd[cmd.index("--model") + 1], "gemini-4-argon-high")
        self.assertEqual(cmd[cmd.index("--mode") + 1], "plan")
        prompt = cmd[cmd.index("-i") + 1]
        from herdr_task.templates import REVIEWER_MD
        for words in (REVIEWER_MD.strip()[:60], "Make the gate.", 'say "hi" $HOME `date`', "major", self.head,
                      "review SHEP-13", "--must-fix"):
            self.assertIn(words, prompt)
        settings = json.loads(cmd[cmd.index("--settings") + 1])
        self.assertEqual(settings["trustedWorkspaces"], [self.wt])
        self.assertTrue(any(" review" in r for r in settings["permissions"]["allow"]))
        self.assertFalse(any(" ready" in r or " reviewed" in r for r in settings["permissions"]["allow"]))
        runs = self.task["review_runs"]
        self.assertEqual([(r["round"], r["head"], r["address"], r["cli"], r["model"]) for r in runs],
                         [(1, self.head, "term-1", "agy", "gemini-4-argon-high")])
        self.valid()
        self.ready()  # again while its reviewer works: no second one
        self.assertEqual(len(self.runs), 1)

    def test_the_reviewer_may_run_reads_and_the_check_and_nothing_that_writes(self):
        # FAC-36: agy's --settings list replaces the person's own (which here may write); the check is allowed part by part
        from unittest import mock
        import herdr_task.launch as launch
        import herdr_task.settings as settings
        home = tempfile.mkdtemp(dir=self.root)
        os.makedirs(os.path.join(home, ".gemini", "antigravity-cli"))
        with open(os.path.join(home, ".gemini", "antigravity-cli", "settings.json"), "w") as f:
            json.dump({"permissions": {"allow": ["command(sed)", "command(npm ci)", "command(git diff)"]}}, f)
        conf = os.path.join(home, "shepherdr.json")
        check = 'export PATH="$HOME/n/bin:$PATH"; bin/sync-shared --check && python3 -m unittest discover -s test'
        with open(conf, "w") as f:
            json.dump({"spaces": {"sp1": {"folder": self.main, "tasks": {"check": check}}}}, f)
        with mock.patch.object(launch, "HOME", home), mock.patch.object(settings, "SETTINGS", conf):
            self.ready()
        cmd = self.runs[0][self.runs[0].index("--") + 1:]
        allow = json.loads(cmd[cmd.index("--settings") + 1])["permissions"]["allow"]
        self.assertEqual([r for r in allow if "herdr-task" not in r],
                         [f"command({c})" for c in launch.REVIEW_READS]
                         + ['command(export PATH="$HOME/n/bin:$PATH")', "command(bin/sync-shared --check)",
                            "command(python3 -m unittest discover -s test)"])
        self.assertEqual([r.split()[-1] for r in allow if "herdr-task" in r], ["review)"])
        self.assertIn(check, cmd[cmd.index("-i") + 1])  # and the prompt says what runs without asking
        # a worker still gets the person's own list; a claude reviewer gets the same commands as Bash rules
        with mock.patch.object(launch, "HOME", home):
            worker = json.loads(launch.own_argv(["agy"])[2])["permissions"]["allow"]
            claude = launch.own_argv(["claude", "--settings", '{"fastMode": false}'], verbs=("review",), reads=["git diff", "sed -n"])
        self.assertIn("command(sed)", worker)
        got = json.loads(claude[2])
        self.assertEqual(got["fastMode"], False)
        self.assertEqual(got["permissions"]["allow"][1:], ["Bash(git diff:*)", "Bash(sed -n:*)"])

    def test_a_verdict_from_another_terminal_is_refused(self):
        self.ready()
        for by in ("term-9", "w1", None):
            with self.assertRaises(self.commands.Fail):
                self.verdict(by)
        self.assertNotIn("reviews", self.task)
        self.assertEqual(self.task["state"], "running")

    def test_a_verdict_after_head_moved_or_on_a_dirty_tree_is_refused(self):
        self.ready()
        with open(os.path.join(self.wt, "gate.txt"), "a") as f:
            f.write("more")
        with self.assertRaises(self.commands.Fail):
            self.verdict("term-1")
        self.change("moved")
        with self.assertRaises(self.commands.Fail):
            self.verdict("term-1")
        self.assertNotIn("reviews", self.task)

    def test_a_pass_needs_no_must_fix(self):
        self.ready()
        with self.assertRaises(self.commands.Fail):
            self.verdict("term-1", must_fix=1)
        self.assertNotIn("reviews", self.task)

    def test_pass_with_min_2_starts_round_2_then_ready(self):
        self.ready()
        self.verdict("term-1")
        self.assertEqual(self.task["state"], "running")
        self.assertEqual(len(self.runs), 2)
        self.assertEqual([(r["round"], r["head"], r["address"]) for r in self.task["review_runs"]],
                         [(1, self.head, "term-1"), (2, self.head, "term-2")])
        with self.assertRaises(self.commands.Fail):
            self.verdict("term-1")  # round 1's reviewer can't record round 2
        self.verdict("term-2")
        self.assertEqual(self.task["state"], "ready")
        self.assertEqual([(r["round"], r["by"], r["verdict"], r["started_by"]) for r in self.task["reviews"]],
                         [(1, "term-1", "pass", "herdr-task"), (2, "term-2", "pass", "herdr-task")])
        self.valid()

    def test_changes_go_to_the_worker_and_the_next_ready_reviews_the_new_head(self):
        self.ready()
        self.verdict("term-1", passed=False, changes="gate.txt says hi twice", must_fix=1)
        self.assertEqual(self.task["state"], "running")
        self.assertIn("gate.txt says hi twice", self.told[-1])
        self.assertEqual(len(self.runs), 1)
        new = self.change("fixed")
        self.ready()
        self.assertEqual([(r["round"], r["head"]) for r in self.task["review_runs"]], [(1, self.head), (2, new)])
        self.valid()

    def test_ready_again_without_fixing_the_changes_starts_none(self):
        self.ready()
        self.verdict("term-1", passed=False, changes="gate.txt says hi twice", must_fix=1)
        self.ready()  # the same head: no fresh reviewer for unfixed code
        self.assertEqual(len(self.runs), 1)
        self.assertEqual(self.task["state"], "running")

    def prompt(self, i):
        cmd = self.runs[i][self.runs[i].index("--") + 1:]
        return cmd[cmd.index("-i") + 1]

    def test_round_2_after_a_fix_carries_only_the_delta_and_the_earlier_findings(self):
        # FAC-53: round 1 has the whole change, a later round what changed since the last reviewed head
        self.ready()
        self.assertIn("gate.txt", self.prompt(0))
        self.assertIn("say \"hi\"", self.prompt(0))
        self.verdict("term-1", passed=False, changes="needs a second line", must_fix=1)
        new = self.change("say \"hi\" $HOME `date`\nsecond line")
        self.ready()
        p = self.prompt(1)
        self.assertIn(f"git diff {self.head}..{new}", p)
        self.assertIn("+second line", p)
        self.assertIn("round 1", p)
        self.assertIn("changes: needs a second line", p)
        self.verdict("term-2")  # round 3 is another pass of the same head: still the delta, still the earlier rounds
        p = self.prompt(2)
        self.assertIn(f"git diff {self.head}..{new}", p)
        self.assertIn("changes: needs a second line", p)

    def test_after_a_rebase_only_the_rebase_delta(self):
        self.ready()
        self.verdict("term-1", passed=False, changes="fix it", must_fix=1)
        self.git(self.main, "checkout", "-q", "main")
        with open(os.path.join(self.main, "other.txt"), "w") as f:
            f.write("moved base")
        self.git(self.main, "add", "other.txt")
        self.git(self.main, "commit", "-q", "-m", "base moved")
        self.git(self.wt, "rebase", "-q", "main")
        self.change("fixed")
        self.ready()
        p = self.prompt(1)
        self.assertIn("base moved", p)
        self.assertNotIn("other.txt", p)  # the base's own commit isn't in the delta
        self.assertIn("+fixed", p)

    def test_after_the_base_is_merged_in_the_delta_has_none_of_its_commits(self):
        self.ready()
        self.verdict("term-1", passed=False, changes="fix it", must_fix=1)
        with open(os.path.join(self.main, "other.txt"), "w") as f:
            f.write("moved base")
        self.git(self.main, "add", "other.txt")
        self.git(self.main, "commit", "-q", "-m", "base moved")
        self.git(self.wt, "merge", "-q", "--no-edit", "main")
        self.change("fixed")
        self.ready()
        p = self.prompt(1)
        self.assertIn("+fixed", p)
        self.assertNotIn("+moved base", p)

    def test_the_base_is_the_remotes_when_it_is_ahead_of_the_local_one(self):
        self.git(self.main, "update-ref", "refs/remotes/origin/main", self.git(self.main, "rev-parse", "main"))
        self.git(self.main, "remote", "add", "origin", self.main)
        self.git(self.main, "checkout", "-q", "-b", "up", "main")
        with open(os.path.join(self.main, "up.txt"), "w") as f:
            f.write("upstream pr")
        self.git(self.main, "add", "up.txt")
        self.git(self.main, "commit", "-q", "-m", "pr")
        self.git(self.main, "update-ref", "refs/remotes/origin/main", self.git(self.main, "rev-parse", "up"))
        self.git(self.main, "checkout", "-q", "main")
        self.git(self.wt, "reset", "-q", "--hard", "origin/main")  # a worktree made from the remote's base
        self.change("mine")
        self.ready()
        self.assertIn("+mine", self.prompt(0))
        self.assertNotIn("upstream pr", self.prompt(0))

    def test_earlier_findings_are_kept_whole_and_an_amend_on_the_same_base_is_a_plain_delta(self):
        self.ready()
        report = "1. [major] a.txt: " + "long " * 60 + "2. [major] b.txt: second finding"
        self.verdict("term-1", passed=False, changes=report, must_fix=2)
        self.git(self.wt, "commit", "-q", "--amend", "-m", "amended")
        self.change("amended again")
        self.ready()
        p = self.prompt(1)
        self.assertIn("2. [major] b.txt: second finding", p)
        self.assertNotIn("base moved", p)

    def test_a_rebase_delta_names_its_files(self):
        self.ready()
        self.verdict("term-1", passed=False, changes="fix it", must_fix=1)
        self.git(self.main, "checkout", "-q", "main")
        with open(os.path.join(self.main, "other.txt"), "w") as f:
            f.write("moved base")
        self.git(self.main, "add", "other.txt")
        self.git(self.main, "commit", "-q", "-m", "base moved")
        self.git(self.wt, "rebase", "-q", "main")
        self.change("fixed")
        self.ready()
        self.assertIn("+++ now/gate.txt", self.prompt(1))

    def test_an_idle_reviewer_of_an_older_head_doesnt_block_the_next(self):
        self.ready()
        self.change("moved on")
        self.reviewer_term = {"state": "live", "agent": "agy", "foreground": {"name": "agy"}, "status": {"state": "idle"}}
        self.ready()
        self.assertEqual(len(self.runs), 2)

    def test_no_second_reviewer_while_one_is_live_even_for_an_older_head(self):
        self.ready()
        self.change("moved on")  # a new head, round 1's reviewer still alive and unfinished
        self.reviewer_term = {"state": "live", "agent": "agy", "foreground": {"name": "agy"}}
        self.ready()
        self.assertEqual(len(self.runs), 1)
        self.assertEqual(len(self.task["review_runs"]), 1)

    def test_a_reviewer_gone_without_a_verdict_doesnt_stop_the_next(self):
        self.ready()
        self.reviewer_term = {"state": "live", "agent": "zsh", "foreground": {"name": "zsh"}}  # its CLI exited, the kept shell stays
        self.ready()
        self.assertEqual([r["round"] for r in self.task["review_runs"]], [1, 2])

    def test_findings_below_must_fix_never_block(self):
        self.ready()
        with self.assertRaises(self.commands.Fail):
            self.verdict("term-1", passed=False, changes="nitpick: rename a variable", must_fix=0)
        self.assertNotIn("reviews", self.task)

    def test_review_more_and_show_in_a_gate_folder(self):
        import argparse
        from unittest import mock
        self.task["set"]["review_rounds"] = 1
        self.ready()
        self.verdict("term-1")
        self.assertNotIn("progress", self.task)  # its "reviewer is on it" line is gone: it waits for a round
        sent = []
        with mock.patch("herdr_task.news.send_or_hold", lambda to, msg: sent.append(msg) or True), \
                mock.patch.object(self.commands, "workers", lambda s, t: [{"address": "w1"}]), \
                mock.patch.object(self.commands, "caller_labels", lambda: {}):
            self.commands.cmd_review_more(argparse.Namespace(id="SHEP-13", json=True, folder=None))
        self.assertIn(" ready SHEP-13", sent[0])
        self.assertNotIn(" reviewed ", sent[0])
        self.assertEqual(self.commands.rounds_used(self.task, True), 1)
        from herdr_task import settings
        self.assertEqual(settings.review_limits(self.store, {"id": 4, "title": "x", "mode": "worktree", "agents": [],
                                                             "set": {"review_min": 5}}), (True, 5))

    def test_a_new_commit_on_a_ready_task_is_never_left_mergeable(self):
        self.ready()
        self.verdict("term-1")
        self.verdict("term-2")
        self.assertEqual(self.task["state"], "ready")
        self.change("unreviewed")
        self.ready()
        self.assertEqual(self.task["state"], "running")  # back at work until its rounds pass
        self.assertEqual(len(self.runs), 3)

    def test_no_verdict_on_a_done_task_and_no_crash_without_a_worktree(self):
        self.ready()
        self.task["state"] = "done"
        with self.assertRaises(self.commands.Fail):
            self.verdict("term-1")
        self.assertEqual(self.task["state"], "done")
        self.task.update(state="todo", worktree=os.path.join(self.root, "gone"))
        with self.assertRaises(self.commands.Fail):
            self.ready()

    def test_rounds_never_below_the_minimum(self):
        from herdr_task import settings
        with open(settings.SETTINGS, "w") as f:
            json.dump({"everywhere": {"tasks": {"review_rounds": 2, "review_min": 3}}}, f)
        self.assertEqual(settings.review_limits(self.store, {"id": 5, "title": "x", "mode": "worktree", "agents": []}), (True, 3))

    def test_its_reviewer_booting_is_not_a_wait_for_the_person(self):
        from herdr_task import agents
        self.task["started"] = 1
        self.ready()
        self.assertTrue(agents.starting(self.task))

    def test_a_codex_reviewer_is_told_to_escalate_its_verdict(self):
        self.task["reviewer"], self.task["reviewer_model"] = "codex", None
        self.ready()
        cmd = self.runs[0][self.runs[0].index("--") + 1:]
        self.assertIn("read-only", cmd)
        self.assertIn("require_escalated", cmd[-1])

    def test_a_claude_reviewer_runs_in_auto_mode_so_its_verdict_can_run(self):
        self.task["reviewer"], self.task["reviewer_model"] = "claude", None
        self.ready()
        cmd = self.runs[0][self.runs[0].index("--") + 1:]
        self.assertEqual(cmd[cmd.index("--permission-mode") + 1], "auto")  # plan mode blocks `herdr-task review`

    def test_out_of_rounds_starts_none(self):
        self.task["set"]["review_rounds"] = 1
        self.ready()
        self.verdict("term-1")
        self.assertEqual(len(self.runs), 1)
        self.assertEqual(self.task["state"], "running")
        self.assertTrue(self.task.get("review_out"))

    def test_the_workers_own_reviewed_is_refused(self):
        import argparse
        with self.assertRaises(self.commands.Fail):
            self.commands.cmd_reviewed(argparse.Namespace(id="SHEP-13", json=True, folder=None, report="fine"))
        self.assertNotIn("reviews", self.task)

    def test_outside_a_gate_folder_nothing_changes(self):
        del self.task["set"]
        self.task["set"] = {"review": "on"}
        with self.assertRaises(self.commands.Fail):  # its own reviewed round is still what ready waits for
            self.ready()
        self.assertEqual(self.runs, [])
        import argparse
        self.commands.cmd_reviewed(argparse.Namespace(id="SHEP-13", json=True, folder=None, report="fine"))
        self.ready()
        self.assertEqual((self.task["state"], self.runs), ("ready", []))

    # ---- FAC-49: an agy reviewer on a Mac runs under sandbox-exec; its verdict is read from its terminal ----

    def sandbox(self):
        from unittest import mock
        import herdr_task.launch as launch
        for mod in (self.commands, launch):
            p = mock.patch.object(mod, "sandboxes", lambda argv: os.path.basename(argv[0]) == "agy")
            p.start()
            self.addCleanup(p.stop)

    def screen(self, text):
        from unittest import mock
        read = []
        p = mock.patch.object(self.commands, "run", lambda argv, **k: read.append(argv) or subprocess.CompletedProcess(argv, 0, text, ""))
        p.start()
        self.addCleanup(p.stop)
        return read

    def test_a_sandboxed_reviewer_skips_permissions_inside_sandbox_exec(self):
        import herdr_task.launch as launch
        self.sandbox()
        self.ready()
        argv = self.runs[0]
        self.assertIn("herdr.sandbox=1", argv)  # revive never brings it back outside its sandbox
        cmd = argv[argv.index(launch.SANDBOX_SH) - 2:]  # inside the launch's usual wraps (clean env, the person's shell)
        self.assertEqual(cmd[:3], ["sh", "-c", launch.SANDBOX_SH])
        with open(os.path.join(BIN, "herdr_task", "reviewer.sb")) as f:
            self.assertEqual(cmd[5], f.read())
        real = os.path.realpath
        self.assertEqual(cmd[6:8], [real(self.wt), real(os.path.join(self.main, ".git"))])
        agy = cmd[10:]
        self.assertEqual(agy[0], "agy")
        self.assertIn("--dangerously-skip-permissions", agy)
        self.assertNotIn("--mode", agy)
        self.assertEqual(json.loads(agy[agy.index("--settings") + 1]), {"trustedWorkspaces": [self.wt]})  # no allow list
        prompt = agy[agy.index("-i") + 1]
        code = self.commands.round_code(self.task["review_runs"][0])
        self.assertIn(f"This round's code is {code}.", prompt)
        self.assertNotIn("review SHEP-13", prompt)
        self.assertIsNone(self.commands.read_verdict(prompt, code))  # its echoed prompt is never a verdict
        self.valid()

    def test_the_unsandboxed_flag_runs_the_agy_reviewer_as_itself(self):
        from unittest import mock
        import herdr_task.launch as launch
        flag = os.path.join(self.wt, "agy-review-unsandboxed")
        with mock.patch.object(launch, "UNSANDBOXED_FLAG", flag):
            self.assertEqual(launch.sandboxed(["agy", "-i", "hi"], self.wt)[:3], ["sh", "-c", launch.SANDBOX_SH])
            open(flag, "w").close()
            self.assertEqual(launch.sandboxed(["agy", "-i", "hi"], self.wt), ["agy", "-i", "hi"])

    def test_its_verdict_is_read_from_its_own_terminal(self):
        self.sandbox()
        self.ready()
        rn = self.task["review_runs"][0]
        code = self.commands.round_code(rn)
        read = self.screen(f"> Review commit ... This round's code is {code}.\n  Working\n  {code} begin\n  - major: gate.txt says hi"
                           f"\n    twice\n  {code} changes 1\n\n>\n")
        self.reviewer_term = {"state": "live", "agent": "agy", "status": {"app": "antigravity", "state": "working"}}
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(read, [])  # never read mid-turn
        self.reviewer_term = {"state": "live", "agent": "agy", "status": {"app": "antigravity", "state": "done"}}
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(read, [["hc", "read", rn["address"], "--lines", "400"]])  # that run's terminal only
        self.assertEqual([(r["round"], r["by"], r["verdict"], r["must_fix"], r["report"]) for r in self.task["reviews"]],
                         [(1, rn["address"], "changes", 1, "- major: gate.txt says hi\ntwice")])
        self.assertIn("gate.txt says hi", self.told[-1])
        self.assertTrue(self.commands.subprocess.Popen.called)  # that reviewer ends
        self.commands.gate_reads(self.store, self.task)  # a recorded round is never read again
        self.assertEqual(len(self.task["reviews"]), 1)
        self.valid()

    def test_a_read_pass_starts_the_next_round_and_a_moved_head_records_none(self):
        self.sandbox()
        self.ready()
        code = self.commands.round_code(self.task["review_runs"][0])
        self.screen(f"**{code} pass**\n")
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual([r["verdict"] for r in self.task["reviews"]], ["pass"])
        self.assertEqual(len(self.task["review_runs"]), 2)  # min 2: round 2 started
        code2 = self.commands.round_code(self.task["review_runs"][1])
        self.assertNotEqual(code, code2)
        self.assertEqual(self.commands.read_verdict(f"`{code2} changes 0`\n", code2), (True, 0, ""))  # only minor findings: a pass
        self.assertEqual(self.commands.read_verdict(f"- minor: x\n{code2} pass", code2), (True, 0, "- minor: x"))  # no begin line
        self.screen(f"{code} pass\n")  # round 1's code on round 2's screen: no verdict
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(len(self.task["reviews"]), 1)
        with open(os.path.join(self.wt, "gate.txt"), "a") as f:  # a dirty tree: nothing recorded, its reviewer kept for a later try
            f.write("more")
        self.commands.subprocess.Popen.reset_mock()
        self.screen(f"{code2} pass\n")
        with self.assertRaises(self.commands.Fail):
            self.commands.gate_reads(self.store, self.task)
        self.assertEqual(len(self.task["reviews"]), 1)
        self.assertFalse(self.commands.subprocess.Popen.called)
        from unittest import mock
        self.git(self.wt, "checkout", "gate.txt")
        with mock.patch.object(self.commands, "gate_step", side_effect=RuntimeError("hc run failed")):  # starting the next round fails
            with self.assertRaises(RuntimeError):
                self.commands.gate_reads(self.store, self.task)
        self.assertEqual(len(self.task["reviews"]), 1)  # rolled back, read again next tick
        self.assertEqual(self.task["progress"]["text"], "Herdr's reviewer is on it")  # all of it, its progress too
        self.assertFalse(self.commands.subprocess.Popen.called)
        self.change("moved")  # its head moved on: no verdict, and that reviewer ends
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(len(self.task["reviews"]), 1)
        self.assertTrue(self.commands.subprocess.Popen.called)
        self.reviewer_term = {"state": "gone"}  # nor from a terminal no longer live
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(len(self.task["reviews"]), 1)

    def test_the_sweep_removes_dirs_of_ended_launchers_only(self):
        from unittest import mock
        import herdr_task.launch as launch
        root = tempfile.mkdtemp(dir=self.root)
        p = subprocess.Popen(["true"])
        p.wait()
        for n in (f"{os.getpid()}.live", f"{p.pid}.gone", "other"):
            os.makedirs(os.path.join(root, n, "x"))
        with mock.patch.object(launch, "SANDBOX_ROOT", root):
            launch.sweep_sandboxes()
        self.assertEqual(sorted(os.listdir(root)), [f"{os.getpid()}.live", "other"])

    @unittest.skipUnless(sys.platform == "darwin" and os.path.exists("/usr/bin/sandbox-exec"), "sandbox-exec is macOS's")
    def test_the_launcher_really_confines_it_and_removes_its_temp_dir(self):
        from unittest import mock
        import herdr_task.launch as launch
        real = os.path.realpath
        root, home, bindir = (real(tempfile.mkdtemp(dir=self.root)) for _ in range(3))
        g = os.path.join(home, ".gemini", "antigravity-cli")
        os.makedirs(os.path.join(g, "cache"))
        for f in ("antigravity-oauth-token", "cache/onboarding.json", "settings.json"):
            with open(os.path.join(g, f), "w") as fh:
                fh.write("{}")
        fake = os.path.join(bindir, "agy")
        with open(fake, "w") as f:  # stands in for agy: where it runs, what it can read and write
            f.write('#!/bin/sh\necho "home=$HOME"; ls -A "$HOME/.gemini/antigravity-cli" "$HOME/.gemini/antigravity-cli/cache"\n'
                    'touch "$HOME/ok" && echo wrote-tmp\ntouch gate2.txt 2>/dev/null || echo no-write-worktree\n'
                    'cat gate.txt >/dev/null && echo read-worktree\ngit log -1 --format=%s\n'
                    f'cat {g}/settings.json 2>/dev/null || echo no-read-home\n'
                    'cat .env 2>/dev/null || echo no-read-env\n')
        os.chmod(fake, 0o755)
        with open(os.path.join(self.wt, ".env"), "w") as f:  # herdr-secret env writes one: never readable in there
            f.write("TOKEN=x\n")
        with mock.patch.object(launch, "SANDBOX_ROOT", os.path.join(root, "r")), mock.patch.object(launch, "HOME", home):
            argv = launch.sandboxed([fake, "-i", "hi"], self.wt)
        out = subprocess.run(argv, cwd=self.wt, capture_output=True, text=True, timeout=60,
                             env={**os.environ, "HOME": home, "PATH": bindir + ":/usr/bin:/bin:/usr/sbin"}).stdout
        self.assertRegex(out, rf"home={os.path.join(root, 'r')}/\d+\.\w+\n")
        for words in ("antigravity-oauth-token", "onboarding.json", "wrote-tmp", "no-write-worktree", "read-worktree", "gate",
                      "no-read-home", "no-read-env"):
            self.assertIn(words, out)
        self.assertNotIn("settings.json", out)  # only the sign-in and onboarding are copied
        self.assertFalse(os.path.exists(os.path.join(self.wt, "gate2.txt")))
        self.assertEqual(os.listdir(os.path.join(root, "r")), [])  # its trap removed the run's temp dir

    def test_the_setting_and_its_words(self):
        from herdr_task import settings
        from herdr_task.launch import closing_line
        t = {"id": 2, "title": "x", "mode": "worktree", "agents": []}
        settings.set_task(t, "review.min", "2")
        self.assertEqual(t["set"], {"review_min": 2})
        with self.assertRaises(settings.Fail):
            settings.set_task(t, "review.min", "eleven")
        block = settings.settings_block(self.store, t)
        self.assertIn("herdr-task", block.split("- Review:")[1].split("\n")[0])
        words = closing_line(self.store, t)
        self.assertNotIn(" reviewed ", words)
        self.assertEqual(settings.review_limits(self.store, t), (True, 3))
        plain = {"id": 3, "title": "y", "mode": "worktree", "agents": [], "set": {"review": "on"}}
        self.assertNotIn("herdr-task", settings.settings_block(self.store, plain).split("- Review:")[1].split("\n")[0])
        self.assertIn(" reviewed ", closing_line(self.store, plain))


class LoopBreaker(GateReview):
    """FAC-54: a task whose herdr-task rounds don't converge, or that is far over its estimate, stops
    as Needs you "Recap required"; ready refuses until the Assistant decides (continue once)."""

    def rounds(self, *rows, since=0):
        self.task["reviews"] = [{"round": i + 1, "head": "h", "started_by": "herdr-task", "verdict": v, "must_fix": n,
                                 "report": rep, "at": since + i + 1} for i, (v, n, rep) in enumerate(rows)]

    def why(self):
        from herdr_task.budget import triggers
        return triggers(self.store, self.task)

    def test_review_triggers_and_their_edges(self):
        self.rounds(("changes", 3, "a.py"))
        self.assertEqual(self.why(), [])  # one round of findings is normal
        self.rounds(("changes", 3, "a.py"), ("pass", 0, ""))
        self.assertEqual(self.why(), [])
        self.rounds(("changes", 3, "x/a.py:4"), ("changes", 2, "b.ts"))
        self.assertEqual(len(self.why()), 1)
        self.assertIn("round 2 still has 2", self.why()[0])
        self.rounds(("changes", 2, "x/a.py:4"), ("changes", 2, "x/a.py:9"))
        self.assertEqual(len(self.why()), 3)
        self.assertTrue(any("didn't drop" in w for w in self.why()) and any("x/a.py" in w for w in self.why()))
        self.rounds(("changes", 2, "x/a.py"), ("pass", 0, ""), ("changes", 1, "c.md"))  # same file only once each, dropping
        self.assertEqual([w[:12] for w in self.why()], ["review round"])
        self.rounds(("changes", 2, "a.py"), ("pass", 0, ""), ("changes", 1, "a.py"))
        self.assertTrue(any("same file" in w for w in self.why()))
        # an agent's own `reviewed` rounds and rounds before the last decision don't count
        self.rounds(("changes", 2, "a.py"), ("changes", 2, "a.py"))
        self.task["reviews"][0]["started_by"] = "agent"
        self.assertEqual(self.why(), [])
        self.rounds(("changes", 2, "a.py"), ("changes", 2, "a.py"))
        self.task["recaps"] = [{"decision": "continue", "why": "w", "at": 1}]
        self.assertEqual(self.why(), [])

    def test_time_trigger_edge(self):
        from unittest import mock
        import herdr_task.budget as budget
        self.task.update(started=1000, estimate_time=60000)
        with mock.patch.object(budget, "now", lambda: 1000 + 120000):
            self.assertEqual(self.why(), [])
        with mock.patch.object(budget, "now", lambda: 1000 + 120001):
            self.assertIn("time estimate (2m of 1m)", self.why()[0])
        with mock.patch.object(budget, "now", lambda: 1000 + 10 ** 7):
            self.task["state"] = "ready"  # waiting for the person's Accept: no time counts
            self.assertEqual(self.why(), [])
            self.task["state"] = "running"
            self.task["recaps"] = [{"decision": "continue", "why": "w", "at": 1000 + 10 ** 7 - 120000}]
            self.assertEqual(self.why(), [])  # counts from the decision, not the start
        self.task.pop("estimate_time")
        with mock.patch.object(budget, "now", lambda: 10 ** 12):
            self.assertEqual(self.why(), [])

    def test_token_sum_from_a_transcript_and_its_edge(self):
        d = os.path.join(self.root, ".claude", "projects", "wt")
        os.makedirs(d)
        log = os.path.join(d, "s.jsonl")
        use = {"input_tokens": 100, "cache_creation_input_tokens": 400, "cache_read_input_tokens": 1000, "output_tokens": 60}
        line = lambda mid: json.dumps({"type": "assistant", "message": {"id": mid, "usage": use}}) + "\n"
        with open(log, "w") as f:  # one response logged twice (two content blocks), a user line, a half-written line
            f.write(line("m1") + line("m1") + json.dumps({"type": "user", "message": {"content": "hi"}}) + "\n" + line("m2")[:30])
        self.task["agents"] = [{"address": "w1", "log": log}, {"address": "g", "log": "/elsewhere/gemini.json"}]
        self.task["estimate_tokens"] = 500  # 100 + 500 + 100 + 300 = 1000 per response
        self.assertEqual(self.why(), [])  # 1000 is not over 2 x 500
        self.assertEqual(self.task["tokens"][log]["sum"], 1000)
        with open(log, "a") as f:
            f.write(line("m2")[30:])
        self.assertIn("token estimate (2k of 500)", self.why()[0])
        self.assertEqual(self.task["tokens"][log]["sum"], 2000)
        self.task["recaps"] = [{"decision": "continue", "why": "w", "at": 1, "tokens": 1500}]
        self.assertEqual(self.why(), [])  # 500 since the decision
        self.task.pop("recaps")
        self.task["agents"].append({"address": "w2", "log": log})  # a resumed session: the same log, counted once
        self.assertIn("(2k of 500)", self.why()[0])
        from herdr_task.settings import set_task
        set_task(self.task, "estimate.tokens", "3M")
        set_task(self.task, "estimate.time", "1h30m")
        self.assertEqual((self.task["estimate_tokens"], self.task["estimate_time"]), (3000000, 5400000))
        with self.assertRaises(self.commands.Fail):
            set_task(self.task, "estimate.tokens", "lots")

    def test_a_ready_task_goes_back_to_work_and_the_recap_stays_first(self):
        from herdr_task.budget import budget_pass
        from herdr_task.agents import task_status
        self.rounds(("changes", 2, "a.py"), ("changes", 2, "a.py"))
        self.task.update(state="ready", auto_merge="checks", pr={"number": 7})
        self.assertNotIn("recap", self.task)
        with __import__("unittest").mock.patch.object(self.commands, "workers", lambda s, t: []):
            self.assertTrue(budget_pass(self.store, self.task))
        self.assertEqual(self.task["state"], "running")  # never mergeable while (or after) a recap is open
        self.assertNotIn("auto_merge", self.task)
        self.task["attention"] = "Checks failing: a check."
        budget_pass(self.store, self.task)
        self.assertTrue(self.task["attention"].startswith("Recap required"))
        # its worker said a status line and went idle: still Needs you, not Working
        self.task["progress"] = {"set": self.task["attention_at"] + 1, "left": 3600000, "text": "Wrote RECAP.md"}
        idle = [{"address": "w1", "status": {"state": "idle"}, "labels": {}}]
        self.assertEqual(task_status(self.task, idle)[0], "needs")

    def test_a_reviewer_in_flight_when_a_recap_opens_never_passes_it_on(self):
        from unittest import mock
        import herdr_task.budget as budget
        self.ready()
        self.task.update(started=1000, estimate_time=60000)
        with mock.patch.object(budget, "now", lambda: 200000), mock.patch.object(self.commands, "workers", lambda s, t: []):
            budget.budget_pass(self.store, self.task)  # over its time while round 1 is being reviewed
        self.verdict("term-1", passed=True)
        self.assertEqual(len(self.task["review_runs"]), 1)  # no next round started
        self.assertEqual(self.task["state"], "running")
        self.task.update(state="ready", auto_merge="checks")  # however it got there
        budget.budget_pass(self.store, self.task)
        self.assertEqual(self.task["state"], "running")
        self.assertNotIn("auto_merge", self.task)

    def recap(self, decide=None, why="w"):
        import argparse
        from unittest import mock
        import herdr_task.store as store
        import herdr_task.launch as launch
        import herdr_task.output as output
        out = []
        with mock.patch.object(store, "open_store", lambda args, write=True: self.store), \
                mock.patch.object(launch, "caller_labels", lambda: {}), \
                mock.patch.object(output, "say", lambda args, v, msg: out.append(msg)):
            from herdr_task.budget import cmd_recap
            cmd_recap(argparse.Namespace(id="SHEP-13", json=False, folder=None, decide=decide, why=why))
        return out[-1]

    def test_second_must_fix_round_stops_ready_until_continue_then_again(self):
        from unittest import mock
        self.ready()
        self.verdict("term-1", passed=False, changes="major: gate.txt says hi", must_fix=1)
        self.assertNotIn("recap", self.task)
        self.assertIn("Fix these", self.told[-1])
        self.head = self.change("better")
        with mock.patch.object(self.commands, "workers", lambda s, t: [{"address": "w1"}]):
            self.ready()
            self.verdict("term-2", passed=False, changes="major: gate.txt still says hi", must_fix=1)
        self.assertTrue(self.task["attention"].startswith("Recap required: review round 2 still has 1"))
        self.assertIn("RECAP.md", self.told[-1])
        self.assertIn("Don't fix anything more", self.told[-1])
        self.assertIn("gate.txt still says hi", self.told[-1])  # the findings, for the recap
        self.assertEqual(self.task["recap"]["round"], 2)
        with self.assertRaises(self.commands.Fail) as e:
            self.ready()
        self.assertIn("Assistant decides", str(e.exception))
        self.assertIn("no RECAP.md yet", self.recap())
        with open(os.path.join(self.wt, "RECAP.md"), "w") as f:
            f.write("Goal: a gate.")
        self.assertIn("Goal: a gate.", self.recap())
        with self.assertRaises(self.commands.Fail):
            self.recap("continue", why=" ")
        self.assertIn("decided: continue", self.recap("continue", why="one more, the premise was right"))
        self.assertNotIn("recap", self.task)
        self.assertNotIn("attention", self.task)
        self.assertEqual(self.why(), [])  # the count starts afresh
        # two more non-converging rounds: Recap required again, and continue is refused
        at = self.task["recaps"][-1]["at"]
        self.rounds(("changes", 1, "a.py"), ("changes", 1, "b.py"), since=at)
        from herdr_task.budget import budget_pass
        self.assertTrue(budget_pass(self.store, self.task))
        self.assertEqual(self.task["attention"], "Recap required again: change direction")
        with self.assertRaises(self.commands.Fail) as e:
            self.recap("continue")
        self.assertIn("one continue", str(e.exception))
        self.assertIn("decided: split", self.recap("split", why="two tasks"))
        self.assertEqual([d["decision"] for d in self.task["recaps"]], ["continue", "split"])

    def test_ci_triggers_window_and_recap_reset(self):
        from unittest import mock
        import herdr_task.budget as budget
        run = lambda sha, rid, failed, at: {"sha": sha, "run": rid, "failed": failed, "at": at}
        clock = [5000]
        with mock.patch.object(budget, "now", lambda: clock[0]), \
                mock.patch.object(self.commands, "workers", lambda s, t: [{"address": "w1"}]):
            self.task["ci_runs"] = [run("a", "1", True, 1000)]
            self.assertEqual(self.why(), [])
            self.task["ci_runs"].append(run("a", "1", True, 2000))
            self.assertEqual(self.why(), [])  # 2 failed runs don't trigger
            self.task["ci_runs"].append(run("b", "2", False, 3000))
            self.assertEqual(self.why(), [])  # passing runs don't count as failures
            self.task["ci_runs"] = [run("a", "1", True, 1000), run("a", "1", True, 2000), run("b", "2", True, 3000)]
            self.assertEqual(self.why(), ["3 failed CI runs"])
            # 1h window from the first CI run in the current window while CI is red (or still pending after a failure)
            self.task["ci_runs"] = [run("a", "1", True, 1000)]
            clock[0] = 1000 + 3599999
            self.assertEqual(self.why(), [])
            clock[0] = 1000 + 3600000
            self.assertEqual(self.why(), ["1h on CI"])
            self.task["ci_runs"].append(run("b", "2", False, 2000))  # green on latest sha: waiting for Accept isn't a CI loop
            clock[0] = 1000 + 10 ** 7
            self.task["state"] = "ready"
            self.assertEqual(self.why(), [])
            self.task["state"] = "running"
            self.assertEqual(self.why(), [])
            self.task["ci_pending"] = True  # another workflow on sha b still running past 1h
            self.assertEqual(self.why(), ["1h on CI"])
            self.task.pop("ci_pending")
            # a recap decision resets both the failed-run count and the 1h window
            self.task["ci_runs"] = [run("a", "1", True, 1000), run("a", "1", True, 2000), run("a", "1", True, 3000)]
            clock[0] = 1000 + 3600000
            self.assertTrue(budget.budget_pass(self.store, self.task))
            self.assertEqual(self.task["attention"], "Recap required: 3 failed CI runs")
            self.assertIn("decided: continue", self.recap("continue", why="turn on test retries"))
            self.assertEqual(self.why(), [])
            at = self.task["recaps"][-1]["at"]
            self.task["ci_runs"] += [run("b", "3", True, at + 10), run("b", "3", True, at + 20)]
            self.assertEqual(self.why(), [])  # 2 after the decision: still under 3
            self.task["ci_runs"].append(run("b", "3", True, at + 30))
            self.assertEqual(self.why(), ["3 failed CI runs"])

    def test_pr_ci_recorded_from_pr_view_counts_reruns_and_stops_for_recap(self):
        from unittest import mock
        import herdr_task.budget as budget
        import herdr_task.github as github
        import herdr_task.tick as tick
        clock, gh_calls, rollup = [1_000_000], [], []
        self.task.update(state="running", pr={"number": 7, "repo": "o/r", "checked": 0})
        url = lambda rid, job: f"https://github.com/o/r/actions/runs/{rid}/job/{job}"

        def gh_run(argv, **kw):
            gh_calls.append(argv)
            if argv[1:3] == ["pr", "view"]:
                body = {"state": "OPEN", "mergeable": "MERGEABLE", "headRefOid": self.head, "statusCheckRollup": rollup, "mergeCommit": None}
                return subprocess.CompletedProcess(argv, 0, json.dumps(body), "")
            return subprocess.CompletedProcess(argv, 0, "[]" if argv[1] == "api" else "failed test log", "")

        def step(dt=60_000):
            clock[0] += dt
            report = {"done": [], "attention": []}
            tick.facts(self.store, self.task, report)
            budget.budget_pass(self.store, self.task)

        worker = [{"address": "w1", "state": "live", "agent": "agy", "foreground": {"name": "agy"},
                   "status": {"state": "idle"}, "labels": {"herdr.role": "worker"}}]
        with mock.patch.object(github, "run", gh_run), mock.patch.object(tick, "now", lambda: clock[0]), \
                mock.patch.object(budget, "now", lambda: clock[0]), mock.patch.object(self.commands, "now", lambda: clock[0]), \
                mock.patch.object(self.commands, "live_agents", lambda key: worker):
            # cancelled runs (including a fast green job beside a cancelled one) don't count
            rollup[:] = [{"name": "lint", "conclusion": "SUCCESS", "detailsUrl": url(500, 1)},
                         {"name": "test", "conclusion": "CANCELLED", "detailsUrl": url(500, 2)}]
            step()
            self.assertNotIn("ci_runs", self.task)
            self.assertEqual(len(gh_calls), 1)  # one gh pr view per tick, nothing extra
            # attempt 1 on run 501 fails; looking again with nothing changed doesn't count it twice
            rollup[:] = [{"name": "test", "conclusion": "FAILURE", "detailsUrl": url(501, 1), "completedAt": "t1"}]
            step()
            step()
            self.assertEqual([(r["run"], r["failed"]) for r in self.task["ci_runs"]], [("501", True)])
            self.assertEqual(len(gh_calls), 3)
            # `gh run rerun 501 --failed`: goes pending, then fails again -> attempt 2 on the same run id
            rollup[:] = [{"name": "test", "status": "IN_PROGRESS", "detailsUrl": url(501, 1)}]
            step()
            self.assertTrue(self.task.get("ci_pending"))
            rollup[:] = [{"name": "test", "conclusion": "FAILURE", "detailsUrl": url(501, 1), "completedAt": "t1"}]
            step()
            self.assertNotIn("ci_pending", self.task)
            self.assertEqual(len(self.task["ci_runs"]), 2)
            self.assertNotIn("recap", self.task)
            # fast re-run of 501 on a Ready task finishes between ticks (new job/completedAt): attempt 3 -> Recap required
            self.task.update(state="ready", auto_merge="checks", ready_at=clock[0])
            rollup[:] = [{"name": "test", "conclusion": "FAILURE", "detailsUrl": url(501, 2), "completedAt": "t2"}]
            step(dt=150_000)
            self.assertEqual(len(self.task["ci_runs"]), 3)
            self.assertEqual(self.task["state"], "running")
            self.assertNotIn("auto_merge", self.task)
            self.assertNotIn("news", self.task["pr"])
            self.assertEqual(self.task["attention"], "Recap required: 3 failed CI runs")
            for words in ("3 failed CI runs", "Don't fix anything more or re-run CI.", "CI checks failed", "RECAP.md"):
                self.assertIn(words, self.told[-1])
            # after `recap --decide continue`, the still-unchanged failed check isn't counted again in the new window
            self.recap("continue", why="retry flaky tests in workflow")
            step()
            self.assertNotIn("recap", self.task)
            self.assertEqual(self.why(), [])


for _name in [n for n in vars(GateReview) if n.startswith("test_")]:
    setattr(LoopBreaker, _name, None)  # its setUp only: GateReview's own tests run once, there


class SandboxedReviewer(unittest.TestCase):
    """FAC-49: which reviewers run under sandbox-exec (GateReview patches this choice)."""

    def setUp(self):
        sys.path.insert(0, BIN)

    def test_only_agy_on_a_mac_is_sandboxed(self):
        from unittest import mock
        import herdr_task.launch as launch
        with mock.patch.object(launch.sys, "platform", "linux"):
            self.assertFalse(launch.sandboxes(["agy"]))
        with mock.patch.object(launch.sys, "platform", "darwin"), mock.patch.object(launch.os.path, "exists", lambda p: True):
            self.assertTrue(launch.sandboxes(["/x/agy", "--model", "m"]))
            self.assertFalse(launch.sandboxes(["claude"]))
        with mock.patch.object(launch.sys, "platform", "darwin"), mock.patch.object(launch.os.path, "exists", lambda p: False):
            self.assertFalse(launch.sandboxes(["agy"]))

    def test_its_launcher_counts_as_its_agent_running(self):
        from herdr_task.agents import agent_running
        self.assertTrue(agent_running({"foreground": {"name": "bash", "argv": ["sh", "-c", "root=$1 ...", "herdr-sandbox", "/r"]}}))
        self.assertFalse(agent_running({"foreground": {"name": "bash", "argv": ["bash"]}}))

    def test_revive_never_brings_a_sandboxed_reviewer_back(self):
        from herdr_task.revive import watched
        labels = {"herdr.task": "dev#6", "herdr.role": "reviewer"}
        self.assertTrue(watched({"labels": labels}))
        self.assertFalse(watched({"labels": {**labels, "herdr.sandbox": "1"}}))


class Scheduled(unittest.TestCase):
    """APP-38: a task that starts by itself at a time (--at "fri 8:15", --in 6h). A real task list on
    disk in a throwaway store and a throwaway git repo; only starting an agent, hc and the clock are faked."""

    def setUp(self):
        import argparse
        import contextlib
        import io
        import shutil
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.commands as commands
        import herdr_task.queues as queues
        import herdr_task.settings as settings
        import herdr_task.store as store
        import herdr_task.tick as tick
        from herdr_task.base import Fail
        self.commands, self.tick, self.store_mod, self.Fail = commands, tick, store, Fail
        self.argparse, self.io, self.redirect = argparse, io, contextlib.redirect_stdout
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.folder = os.path.join(self.tmp, "app")
        os.makedirs(self.folder)
        subprocess.run(["git", "init", "-q", "-b", "main", self.folder], check=True)
        subprocess.run(["git", "-C", self.folder, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q",
                        "--allow-empty", "-m", "base"], check=True)
        self.started, self.clock = [], None

        def begin(s, t, argv, space, parent=None):
            t["state"], t["started"] = "running", 1
            for k in ("queue", "queued_at", "held", "scheduled_for"):
                t.pop(k, None)
            s.dirty = True
            self.started.append((s.ref(t), argv, space, parent))
            return {"address": "t9", "agent": "claude"}

        later = lambda: self.clock if self.clock is not None else int(__import__("time").time() * 1000)
        patches = [mock.patch.object(store, "STORE", os.path.join(self.tmp, "tasks")),
                   mock.patch.object(settings, "SETTINGS", os.path.join(self.tmp, "settings.json")),
                   mock.patch.dict(os.environ, {"HC_TERMINAL_ID": ""}),
                   mock.patch.object(commands, "begin", begin), mock.patch.object(tick, "begin", begin),
                   mock.patch.object(commands, "ensure_scheduler", lambda quiet=False: {}),
                   mock.patch.object(commands, "default_space", lambda: "sp1"),
                   mock.patch.object(tick, "default_space", lambda: "sp1"),
                   mock.patch.object(commands, "caller_space", lambda: None),
                   mock.patch.object(commands, "worker_argv", lambda s, t, space, key=None: ["claude"]),
                   mock.patch.object(tick, "worker_argv", lambda s, t, space, key=None: ["claude"]),
                   mock.patch.object(commands, "live_agents", lambda key: []),
                   mock.patch.object(queues, "live_agents", lambda key: []),
                   mock.patch.object(queues, "machine_hold", lambda: None),
                   mock.patch.object(tick, "machine_hold", lambda: None),
                   mock.patch.object(tick, "now", later)]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)

    def run_cmd(self, fn, **kw):
        base = dict(folder=self.folder, json=True, id=None, title=None, prompt=None, prompt_file=None, here=False,
                    worktree=False, after=None, space=None, at=None, in_=None, agent=None, model=None, effort=None,
                    argv=None, parent=None, now=False, base=None, start_notes=None, finish_notes=None, set=None)
        args = self.argparse.Namespace(**{**base, **kw})
        out = self.io.StringIO()
        with self.redirect(out):
            fn(args)
        return json.loads(out.getvalue())

    def add(self, title, **kw):
        return self.run_cmd(self.commands.cmd_add, title=title, **kw)

    def tick_at(self, ms):
        """One tick at that moment, the list read fresh from disk (as after a restart)."""
        self.clock = ms
        report = {"started": [], "done": [], "attention": [], "fired": []}
        with self.store_mod.Store(self.store_mod.locate(self.folder)) as s:
            self.tick.tick_store(s, report)
        return [x["ref"] for x in report["started"]]

    def task(self, n):
        with self.store_mod.Store(self.store_mod.locate(self.folder), write=False) as s:
            return dict(s.find(n))

    def test_parsing_absolute_relative_and_refused(self):
        import datetime
        from herdr_task.when import parse_at, parse_in, when_from
        thu = int(datetime.datetime(2026, 11, 5, 10, 0).timestamp() * 1000)  # a Thursday, 10:00
        at = lambda words: datetime.datetime.fromtimestamp(parse_at(words, thu) / 1000).strftime("%a %Y-%m-%d %H:%M")
        self.assertEqual(at("2026-11-06 09:00"), "Fri 2026-11-06 09:00")
        self.assertEqual(at("tomorrow 9:00"), "Fri 2026-11-06 09:00")
        self.assertEqual(at("fri 8:15"), "Fri 2026-11-06 08:15")
        self.assertEqual(at("thu 9:00"), "Thu 2026-11-12 09:00")  # passed today: next week's
        self.assertEqual(at("today 18:00"), "Thu 2026-11-05 18:00")
        self.assertEqual(at("9:00"), "Fri 2026-11-06 09:00")  # a bare time: the next one
        self.assertEqual(at("next friday at 5:30 pm"), "Fri 2026-11-06 17:30")
        self.assertEqual([parse_in(x) // 60000 for x in ("6h", "90m", "2 days", "6 hours", "1h 30m", "in 6 hours")],
                         [360, 90, 2880, 360, 90, 360])
        for words in ("soon", "tomorrow", "blah 9:00", "25:00", "2026-02-30 9:00", "2026-11-06"):
            with self.assertRaises(self.Fail, msg=words) as e:
                parse_at(words, thu)
            self.assertRegex(str(e.exception), r"tomorrow 9:00|No such date|2026-11-06 09:00")
        for words in ("6", "soon", "6 parsecs", ""):
            with self.assertRaises(self.Fail, msg=words) as e:
                parse_in(words)
            self.assertTrue("6h" in str(e.exception) or "how long" in str(e.exception), str(e.exception))
        with self.assertRaises(self.Fail):
            when_from("2020-01-01 09:00", None)  # passed
        self.assertEqual(when_from("in 6 hours", None, thu), thu + 6 * 3600000)  # a span where a time goes: from now
        with self.assertRaises(self.Fail) as e:
            when_from("whenever", None, thu)
        self.assertIn('"tomorrow 9:00"', str(e.exception))
        with self.assertRaises(self.Fail):
            self.add("Both", at="18:00", in_="6h")  # one or the other (argparse refuses it too)

    def test_automations_once_takes_the_same_words(self):
        import datetime
        from herdr_task.automations import parse_every
        r = parse_every("once tomorrow 9:00")
        tomorrow = datetime.date.today() + datetime.timedelta(days=1)
        self.assertEqual(r, {"text": "once tomorrow 9:00", "kind": "once", "at": f"{tomorrow:%Y-%m-%d} 09:00"})
        self.assertEqual(parse_every("once 2026-11-05 9:00")["at"], "2026-11-05 09:00")
        with self.assertRaises(self.Fail):
            parse_every("once whenever")

    def test_it_starts_on_time_not_before(self):
        import time
        v = self.add("Nightly cleanup", in_="6h")
        self.assertEqual(v["status"], "scheduled")
        self.assertEqual(v["status_text"], "Scheduled")
        self.assertRegex(v["why"], r"^Starts (Today|Tomorrow) \d+:\d\d \(in (5h \d+m|6h)\)$")
        due = v["scheduled_for"]
        self.assertAlmostEqual(due, time.time() * 1000 + 6 * 3600000, delta=5000)
        self.assertEqual(self.tick_at(due - 60000), [])
        self.assertEqual(self.task(1)["state"], "scheduled")
        self.assertEqual(self.tick_at(due + 1000), ["APP-1"])
        self.assertEqual(self.started, [("APP-1", ["claude"], "sp1", None)])  # the space it was added in
        t = self.task(1)
        self.assertEqual(t["state"], "running")
        self.assertNotIn("scheduled_for", t)
        self.assertEqual(self.tick_at(due + 20000), [])  # once

    def test_a_time_passed_while_the_machine_was_off_starts_once(self):
        v = self.add("Report", at="2099-01-01 09:00")
        self.assertEqual(v["status"], "scheduled")
        # The machine was off on the day: the first tick after (a fresh read of the list) starts it.
        a_day_late = v["scheduled_for"] + 86400000
        self.assertEqual(self.tick_at(a_day_late), ["APP-1"])
        self.assertEqual(self.tick_at(a_day_late + 15000), [])
        self.assertEqual(len(self.started), 1)

    def test_no_room_then_it_queues(self):
        from unittest import mock
        import herdr_task.queues as queues
        self.add("Busy one")
        self.run_cmd(self.commands.cmd_start, id="1", now=True)
        v = self.add("Later", in_="1h")
        with mock.patch.object(self.store_mod.Store, "max_running", lambda s: 1), \
                mock.patch.object(queues, "working", lambda s, t: t["state"] == "running"):
            self.assertEqual(self.tick_at(v["scheduled_for"] + 1000), [])
            self.assertEqual(self.task(2)["state"], "queued")

    def four_tasks(self):
        """FAC-10's folder: two tasks went ready then done, one still works, the fourth is queued."""
        import time
        for title in ("One", "Two", "Three", "Four"):
            self.add(title)
        with self.store_mod.Store(self.store_mod.locate(self.folder)) as s:
            for i in (1, 2):
                s.find(i)["state"] = "done"
            s.find(3).update(state="running", agents=[{"address": "t3", "at": int(time.time() * 1000)}])
            s.find(4).update(state="queued", queued_at=1)
            s.dirty = True

    def test_room_after_two_finish_starts_the_queued_one(self):
        """FAC-10: max_running 3, two of three done and one working: the queued one starts on the next tick
        (the queue's own rule was right; what failed was that no round ran, below)."""
        from unittest import mock
        self.four_tasks()
        with mock.patch.object(self.store_mod.Store, "max_running", lambda s: 3):
            self.assertEqual(self.tick_at(None), ["APP-4"])

    def test_a_tick_stuck_in_git_doesnt_freeze_the_queue(self):
        """FAC-10: the service's tick hung in `git rev-parse` in the folder (macOS kept it from the
        folder) while holding the tick lock, so every scheduler round gave up after its wait and the
        queued task stayed Queued with no reason. The folder's git call now gives up, the tick ends."""
        import threading
        import time
        from unittest import mock
        import herdr_task.automations as au
        import herdr_task.autopilot as ap
        import herdr_task.home as home
        self.four_tasks()
        store = os.path.join(self.tmp, "tasks")
        fakebin, hung, block = os.path.join(self.tmp, "bin"), os.path.join(self.tmp, "hung"), os.path.join(self.tmp, "block")
        os.makedirs(fakebin)
        real = subprocess.run(["which", "git"], capture_output=True, text=True).stdout.strip()
        with open(os.path.join(fakebin, "git"), "w") as f:  # rev-parse hangs while `block` exists
            f.write(f'#!/bin/sh\ncase "$*" in *rev-parse*) [ -e {block} ] && touch {hung} && exec sleep 8;; esac\nexec {real} "$@"\n')
        os.chmod(os.path.join(fakebin, "git"), 0o755)
        open(block, "w").close()
        quiet = lambda *a, **k: None
        patches = [mock.patch.dict(os.environ, {"PATH": fakebin + os.pathsep + os.environ["PATH"]}),
                   mock.patch.object(self.store_mod.Store, "max_running", lambda s: 3),
                   mock.patch.object(self.store_mod, "GIT_WAIT_S", 1, create=True),
                   mock.patch.object(self.tick, "STORE", store), mock.patch.object(au, "STORE", store),
                   mock.patch.object(au, "SEEN", os.path.join(store, ".seen.json")), mock.patch.object(au, "LOCK_WAIT_S", 4),
                   mock.patch.object(ap, "autopilot_pass", quiet), mock.patch.object(home, "home_pass", quiet),
                   mock.patch.object(au, "automations_pass", quiet)]
        patches += [mock.patch.object(self.tick, n, quiet) for n in (
            "revive_pass", "retry_pass", "archive_pass", "news_pass", "end_done_pass", "memory_guard", "lost_logs_pass", "browser_pass", "say")]
        patches += [mock.patch.object(au, n, quiet) for n in (
            "retry_pass", "archive_pass", "news_pass", "end_done_pass", "memory_guard", "lost_logs_pass", "backup_pass",
            "cli_check_pass", "machines_pass")]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)
        # The service's tick, where git can't get into the folder
        args = self.argparse.Namespace(folder=None, json=True, machine=None, hold=None)
        service = threading.Thread(target=self.tick.cmd_tick, args=(args,))
        service.start()
        self.addCleanup(service.join)
        for _ in range(100):
            if os.path.exists(hung):
                break
            time.sleep(0.05)
        self.assertTrue(os.path.exists(hung), "the service's tick never reached git")
        service.join(5)
        self.assertFalse(service.is_alive(), "the service's tick still holds the lock")
        # the same folder again within the minute fails at once (a tick finds it several times)
        began = time.monotonic()
        with self.assertRaises(self.Fail):
            self.store_mod.locate(self.folder)
        self.assertLess(time.monotonic() - began, 0.5)
        os.remove(block)  # the scheduler (its own process, no memory of the hang) gets in
        self.store_mod._hung.clear()
        report = {"started": [], "done": [], "attention": [], "fired": []}
        au.one_pass(report)
        self.assertEqual([x["ref"] for x in report["started"]], ["APP-4"], "the scheduler's round never got the tick lock")

    def test_after_and_a_time_start_when_both_hold(self):
        self.add("First")
        v = self.add("Second", in_="1h", after="1")
        due = v["scheduled_for"]
        self.assertEqual(v["status"], "scheduled")
        self.assertEqual(self.tick_at(due + 1000), [])  # its time came, SHEP-1 isn't done
        self.assertEqual(self.task(2)["state"], "queued")
        with self.store_mod.Store(self.store_mod.locate(self.folder)) as s:
            s.find(1)["state"] = "done"
            s.dirty = True
        self.assertEqual(self.tick_at(due + 20000), ["APP-2"])
        # the other way round: done first, then its time
        self.add("Third")
        v = self.add("Fourth", in_="2h", after="3")
        with self.store_mod.Store(self.store_mod.locate(self.folder)) as s:
            s.find(3)["state"] = "done"
            s.dirty = True
        self.assertEqual(self.tick_at(v["scheduled_for"] - 60000), [])
        self.assertEqual(self.tick_at(v["scheduled_for"] + 1000), ["APP-4"])

    def test_start_at_now_reschedule_and_unschedule(self):
        self.add("Deploy")
        v = self.run_cmd(self.commands.cmd_start, id="1", at="2099-01-01 09:00", agent=None)
        self.assertEqual((v["status"], v["queue"]["space"]), ("scheduled", "sp1"))
        v = self.run_cmd(self.commands.cmd_edit, id="1", in_="3h")
        self.assertEqual(v["status"], "scheduled")
        self.assertEqual(v["queue"]["argv"], ["claude"])  # what start would use, kept
        v = self.run_cmd(self.commands.cmd_edit, id="1", at="")
        self.assertEqual(v["status"], "todo")
        self.assertNotIn("scheduled_for", v)
        self.run_cmd(self.commands.cmd_edit, id="1", at="tomorrow 9:00")
        self.assertEqual(self.task(1)["state"], "scheduled")
        v = self.run_cmd(self.commands.cmd_start, id="1", now=True)
        self.assertEqual(v["state"], "running")
        self.assertEqual([x[0] for x in self.started], ["APP-1"])
        with self.assertRaises(self.Fail):
            self.run_cmd(self.commands.cmd_edit, id="1", in_="1h")  # started: too late
        with self.assertRaises(self.Fail):
            self.add("Typo", at="fry 9:00")


class FastOffIsSaid(unittest.TestCase):
    """SHEP-41: a worker with Fast off runs standard even when config.toml sets service_tier."""

    def test_codex_fast_on_and_off(self):
        sys.path.insert(0, BIN)
        from herdr_task.settings import argv_for
        off = argv_for({"agent": "codex", "model": "gpt-x", "fast": False})
        self.assertIn('service_tier="default"', off)
        self.assertNotIn('service_tier="fast"', off)
        self.assertIn('service_tier="fast"', argv_for({"agent": "codex", "fast": True}))
        self.assertIn('{"fastMode": false}', argv_for({"agent": "claude", "fast": False}))


class FolderTrustQuestion(unittest.TestCase):
    """FAC-2: a Claude Code worker started without the window in a new worktree can sit at its
    folder-trust question, reporting no status: Needs you with the reason, not Working forever.
    The screen is Claude Code 2.1.319's real one (hc read, a throwaway core); hc read is faked."""

    SCREEN = ("─" * 80 + "\n Accessing workspace:\n /tmp/wt\n Quick safety check: Is this a project you created or one"
              " you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a"
              " moment to review what's in this\n folder first.\n Claude Code'll be able to read, edit, and execute files"
              " here.\n Security guide\n ❯ No, exit\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel\n")

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.agents as agents
        self.agents = agents
        self.clock = 1_800_000_000_000
        p = mock.patch.object(agents, "now", lambda: self.clock)
        p.start()
        self.addCleanup(p.stop)
        self.screen = self.SCREEN
        self.reads = []

        def read(argv, **kw):
            self.reads.append(argv)
            return subprocess.CompletedProcess(argv, 0, self.screen, "")
        r = mock.patch.object(agents, "run", read)
        r.start()
        self.addCleanup(r.stop)
        # Started 10 minutes ago: past STARTING_MS, so only its agents' status speaks.
        self.task = {"id": 2, "title": "Fix", "state": "running", "started": self.clock - 600_000,
                     "agents": [{"address": "t1", "at": self.clock - 600_000}]}
        # As hc get shows it: the agent runs, has reported nothing and taken no turn.
        self.live = [{"address": "t1", "status": None, "turn": 0, "reported": False, "agent": "env",
                      "foreground": {"name": "2.1.319"}, "labels": {"herdr.role": "worker", "herdr.agent": "claude"}}]

    def check(self):
        self.agents.trust_check(self.task, self.live)
        return self.agents.task_status(self.task, self.live)

    def test_the_trust_question_needs_you(self):
        st, needs, why = self.check()
        self.assertEqual((st, needs), ("needs", "attention"))
        self.assertIn("trust", why)
        # Claude Code's older wording too
        self.task.pop("attention")
        self.screen = " Do you trust the files in this folder?\n ❯ 1. Yes, proceed\n   2. No, exit\n"
        self.assertEqual(self.check()[:2], ("needs", "attention"))

    def test_answered_it_works_again(self):
        self.check()
        self.screen = "❯ \n ⏵⏵ auto mode on\n"
        self.live[0].update(status={"state": "working"}, turn=0)
        self.assertEqual(self.check(), ("working", None, None))
        self.assertNotIn("attention", self.task)

    def test_only_an_agent_that_has_not_started(self):
        # Reported a status or took a turn: its screen is never read (its own output may quote the question).
        for change in ({"status": {"state": "idle"}}, {"turn": 1}):
            self.reads.clear()
            k = dict(self.live[0], **change)
            self.agents.trust_check(self.task, [k])
            self.assertEqual(self.reads, [], change)
            self.assertNotIn("attention", self.task)
        # Another CLI's worker (agy, Codex): its screen is never read either.
        self.reads.clear()
        self.agents.trust_check(self.task, [dict(self.live[0], labels={"herdr.role": "worker", "herdr.agent": "agy"})])
        self.assertEqual(self.reads, [])
        self.assertNotIn("attention", self.task)
        # Another problem already shown stays as it is.
        self.task["attention"] = "Checks failing"
        self.agents.trust_check(self.task, self.live)
        self.assertEqual(self.task["attention"], "Checks failing")

    def test_automations_hear_needs_you_not_a_failure(self):
        from herdr_task.automations import diff_events
        seen = {}
        facts = lambda: [{"id": 2, "state": "running", "status": "idle", "needs": self.agents.task_status(self.task, self.live)[1],
                          "attention": self.task.get("attention"), "merged": False, "automation": None, "starting": False}]
        diff_events(facts(), seen)
        self.agents.trust_check(self.task, self.live)
        self.assertEqual([e for e, _ in diff_events(facts(), seen)], ["needs-you"])
        self.assertEqual([e for e, _ in diff_events(facts(), seen)], [])  # said once


class AgyWorker(unittest.TestCase):
    """Antigravity CLI (agy) as a task's agent: its argv, own commands, resume and conversation id."""

    def setUp(self):
        sys.path.insert(0, BIN)
        self.home = tempfile.mkdtemp()

    def test_its_argv(self):
        from herdr_task.settings import argv_for, agent_argv
        self.assertEqual(argv_for({"agent": "agy", "model": "gemini-4-argon-medium", "effort": "high", "perm": "full"}),
                         ["agy", "--model", "gemini-4-argon-medium", "--effort", "high", "--dangerously-skip-permissions"])
        self.assertEqual(argv_for({"agent": "agy", "perm": "plan"}), ["agy", "--mode", "plan"])
        self.assertEqual(argv_for({"agent": "agy", "perm": "auto"}), ["agy", "--mode", "accept-edits"])
        self.assertEqual(agent_argv("agy"), ["agy"])

    def test_own_commands_join_the_global_allow_list(self):
        from unittest import mock
        from herdr_task import launch
        d = os.path.join(self.home, ".gemini", "antigravity-cli")
        os.makedirs(d)
        with open(os.path.join(d, "settings.json"), "w") as f:
            json.dump({"permissions": {"allow": ["command(go test)"]}}, f)
        with mock.patch.object(launch, "HOME", self.home), \
                mock.patch.object(launch, "own_path", lambda: "/x/herdr-task"), \
                mock.patch.object(launch, "secret_path", lambda: None), mock.patch.object(launch, "browser_path", lambda: None):
            out = launch.own_argv(["agy", "--mode", "accept-edits"])
            self.assertEqual(out[:2], ["agy", "--settings"])
            allow = json.loads(out[2])["permissions"]["allow"]
            self.assertEqual(allow[0], "command(go test)")
            self.assertIn("command(/x/herdr-task status)", allow)
            self.assertEqual(out[3:], ["--mode", "accept-edits"])
            # an --settings already there: its allow list, then the global one, then its own
            mine = launch.own_argv(["agy", "--settings", json.dumps({"permissions": {"allow": ["command(ls)"]}, "x": 1})])
            self.assertEqual(mine.count("--settings"), 1)
            merged = json.loads(mine[2])
            self.assertEqual(merged["permissions"]["allow"][:2], ["command(ls)", "command(go test)"])
            self.assertIn("command(/x/herdr-task status)", merged["permissions"]["allow"])
            self.assertEqual(merged["x"], 1)
            os.unlink(os.path.join(d, "settings.json"))  # missing file: only its own rules
            allow = json.loads(launch.own_argv(["agy"])[2])["permissions"]["allow"]
            self.assertNotIn("command(go test)", allow)
            self.assertIn("command(/x/herdr-task status)", allow)

    def test_its_first_message_is_a_prompt_not_an_argument(self):
        from types import SimpleNamespace
        from unittest import mock
        import herdr_task.launch as L
        ran = []

        def run(argv, *a, **k):
            ran.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps({"address": "t9"}), "")
        s = SimpleNamespace(key=lambda t: "dev#6", dirty=False)
        with mock.patch.object(L, "run", run), mock.patch.object(L, "folder_for", lambda s, t: "/tmp"), \
                mock.patch.object(L, "agent_name", lambda *a: "dev-6-fake"), mock.patch.object(L, "track", lambda *a: None), \
                mock.patch.object(L, "coordinator_tab", lambda a: None), mock.patch.object(L, "own_argv", lambda a, *_, **__: a), \
                mock.patch.object(L, "in_shell", lambda a: a), mock.patch.object(L, "low_priority", lambda a, n: a):
            L.launch(s, {"id": 6, "title": "fake", "agents": []}, ["agy", "--mode", "plan"], "sp1", "Your task")
            L.launch(s, {"id": 7, "title": "fake", "agents": []}, ["claude"], "sp1", "Your task")
        self.assertEqual(ran[0][-4:], ["--mode", "plan", "-i", "Your task"])  # agy reads prompts only from -i
        self.assertEqual(ran[1][-2:], ["claude", "Your task"])

    def test_its_reviewer_is_another_installed_cli(self):
        from unittest import mock
        import herdr_task.settings as settings
        for have, want in (({"claude", "codex"}, "claude"), ({"codex"}, "codex"), (set(), "agy")):
            with mock.patch.object(settings.shutil, "which", lambda n: "/bin/" + n if n in have else None):
                self.assertEqual(settings.other_cli("agy"), want)
                self.assertEqual(settings.other_cli("claude"), "codex" if "codex" in have else "claude")

    def test_an_settings_file_is_merged_into(self):
        from unittest import mock
        from herdr_task import launch
        f = os.path.join(self.home, "s.json")
        with open(f, "w") as fh:
            json.dump({"permissions": {"allow": ["command(ls)"]}}, fh)
        with mock.patch.object(launch, "HOME", self.home), mock.patch.object(launch, "own_path", lambda: "/x/herdr-task"), \
                mock.patch.object(launch, "secret_path", lambda: None), mock.patch.object(launch, "browser_path", lambda: None):
            out = launch.own_argv(["agy", "--settings", f])
        self.assertEqual(out.count("--settings"), 1)
        allow = json.loads(out[out.index("--settings") + 1])["permissions"]["allow"]
        self.assertEqual(allow[0], "command(ls)")
        self.assertIn("command(/x/herdr-task status)", allow)

    def test_an_agy_settings_relative_file_and_equals_form_are_merged(self):
        from unittest import mock
        from herdr_task import launch
        folder = os.path.join(self.home, "task")
        os.makedirs(folder)
        with open(os.path.join(folder, "role.json"), "w") as fh:
            json.dump({"permissions": {"allow": ["command(ls)"]}}, fh)
        for flag in (["--settings", "role.json"], ["--settings=role.json"], ["--settings", '{"x": 1}'], ['--settings={"x": 1}']):
            with mock.patch.object(launch, "HOME", self.home), mock.patch.object(launch, "own_path", lambda: "/x/herdr-task"), \
                    mock.patch.object(launch, "secret_path", lambda: None), mock.patch.object(launch, "browser_path", lambda: None):
                out = launch.own_argv(["agy"] + flag, "/wt/x", cwd=folder)  # the test's own cwd is not the folder
            self.assertEqual(sum(a.startswith("--settings") for a in out), 1, flag)
            merged = json.loads(out[out.index("--settings") + 1])
            self.assertEqual(merged["trustedWorkspaces"], ["/wt/x"], flag)
            self.assertIn("command(/x/herdr-task status)", merged["permissions"]["allow"], flag)
            if "role" in flag[-1]:
                self.assertEqual(merged["permissions"]["allow"][0], "command(ls)")
        # several --settings become one that keeps all their rules, and the worktree is trusted once
        with mock.patch.object(launch, "HOME", self.home), mock.patch.object(launch, "own_path", lambda: "/x/herdr-task"), \
                mock.patch.object(launch, "secret_path", lambda: None), mock.patch.object(launch, "browser_path", lambda: None):
            out = launch.own_argv(["agy", "--settings=role.json", "--settings", '{"trustedWorkspaces": ["/wt/x"], "permissions": {"allow": ["command(pwd)"]}}', "-m", "x"], "/wt/x", cwd=folder)
        self.assertEqual(sum(a.startswith("--settings") for a in out), 1)
        self.assertEqual(out[-2:], ["-m", "x"])
        merged = json.loads(out[out.index("--settings") + 1])
        self.assertEqual(merged["trustedWorkspaces"], ["/wt/x"])
        self.assertEqual(merged["permissions"]["allow"][:2], ["command(ls)", "command(pwd)"])

    def test_a_switched_task_does_not_read_the_old_agents_log(self):
        from herdr_task.agents import agent_log
        t = {"agents": [{"address": "t1", "agent": "claude", "session": "c1", "log": "/old.jsonl"},
                        {"address": "t2", "agent": "agy", "session": "5b9e", "log": None}]}
        self.assertIsNone(agent_log(t))
        self.assertIsNone(agent_log(t, "t2"))
        self.assertEqual(agent_log(t, "t1"), "/old.jsonl")

    def test_a_resumed_agent_keeps_its_sessions_log_until_its_own_is_found(self):
        from herdr_task.agents import agent_log
        t = {"agents": [{"address": "t1", "agent": "claude", "session": "c1", "log": "/c1.jsonl"},
                        {"address": "t2", "agent": "claude", "session": "c1", "log": None}]}
        self.assertEqual(agent_log(t), "/c1.jsonl")
        t["agents"][1]["log"] = "/c1b.jsonl"
        self.assertEqual(agent_log(t), "/c1b.jsonl")

    def test_a_resumed_agent_at_its_address_falls_back_to_its_sessions_log(self):
        from herdr_task.agents import agent_log
        t = {"agents": [{"address": "w1", "agent": "claude", "session": "S1", "log": "/s1.jsonl"},
                        {"address": "w2", "agent": "claude", "session": "S1", "log": None}]}
        self.assertEqual(agent_log(t, "w2"), "/s1.jsonl")
        t["agents"].append({"address": "w3", "agent": "agy", "session": "5b9e", "log": None})
        self.assertIsNone(agent_log(t, "w3"))

    def test_a_resumed_agent_has_its_session_from_the_start(self):
        from types import SimpleNamespace
        from unittest import mock
        import herdr_task.commands as C
        from herdr_task.agents import agent_log
        t = {"id": 1, "agents": [{"address": "w1", "agent": "claude", "argv": ["claude"], "at": 1, "session": "S", "log": "/S.jsonl"}]}

        def launch(s, task, argv, space, message=None, parent=None, resume=None, role=None):
            a = {"address": "w2", "agent": "claude", "argv": argv, "at": 2}
            task["agents"].append(a)
            return a
        with mock.patch.object(C, "launch", launch), mock.patch.object(C, "unarchive", lambda s, t: None), \
                mock.patch.object(C, "parent_for", lambda p, sp: p):
            a, resumed = C.start_again(SimpleNamespace(), t, "sp1")
        self.assertTrue(resumed)
        self.assertEqual(a["session"], "S")
        self.assertEqual(agent_log(t), "/S.jsonl")

    def test_it_trusts_the_worktree_made_for_the_task_only(self):
        from types import SimpleNamespace
        from unittest import mock
        import herdr_task.launch as L
        ran = []

        def run(argv, *a, **k):
            ran.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps({"address": "t9"}), "")
        s = SimpleNamespace(key=lambda t: "dev#6", dirty=False)
        with mock.patch.object(L, "run", run), mock.patch.object(L, "folder_for", lambda s, t: "/wt/x" if t["mode"] == "worktree" else "/main"), \
                mock.patch.object(L, "agent_name", lambda *a: "dev-6-fake"), mock.patch.object(L, "track", lambda *a: None), \
                mock.patch.object(L, "coordinator_tab", lambda a: None), mock.patch.object(L, "HOME", self.home), \
                mock.patch.object(L, "in_shell", lambda a: a), mock.patch.object(L, "low_priority", lambda a, n: a), \
                mock.patch.object(L, "sandboxes", lambda a: False):  # FAC-36's path, as off a Mac
            for mode, role in (("worktree", None), ("here", None), ("worktree", "reviewer")):
                L.launch(s, {"id": 6, "mode": mode, "agents": []}, ["agy"], "sp1", role=role)
        trusted = lambda hc: json.loads(hc[hc.index("--settings") + 1]).get("trustedWorkspaces") if "--settings" in hc else None
        self.assertEqual(trusted(ran[0]), ["/wt/x"])
        self.assertIsNone(trusted(ran[1]))
        # an agy reviewer in a worktree: that worktree trusted, and of herdr-task only its verdict (FAC-13), none of the worker's own rules
        self.assertEqual(trusted(ran[2]), ["/wt/x"])
        self.assertEqual([r.split()[-1] for r in json.loads(ran[2][ran[2].index("--settings") + 1])["permissions"]["allow"]
                          if "herdr-task" in r], ["review)"])
        # an argv's own --settings keeps the folders it already trusts
        with mock.patch.object(L, "HOME", self.home):
            out = L.own_argv(["agy", "--settings", '{"trustedWorkspaces": ["/code"]}'], "/wt/x")
        self.assertEqual(trusted(out), ["/code", "/wt/x"])

    def test_the_brief_gives_an_agy_reviewer_i_and_its_worktree_trusted(self):
        from herdr_task.templates import WORKER_MD
        with open(os.path.join(ROOT, "templates", "worker.md")) as f:
            installed = f.read()
        for words in (WORKER_MD, installed):  # the installed template is what workers get
            self.assertIn("after `-i`", words)
            self.assertIn("trustedWorkspaces", words)
            self.assertIn("agy rejects a positional prompt", words)
            self.assertIn("herdr.agent=<codex, claude or agy>", words)

    def test_resume_by_conversation(self):
        from herdr_task.launch import resume_argv
        self.assertEqual(resume_argv(["agy", "--mode", "plan"], "abc"), ["agy", "--mode", "plan", "--conversation", "abc"])

    def test_its_conversation_is_the_db_it_holds_open(self):
        from unittest import mock
        import herdr_task.sessions as sessions
        cid = "0199aaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
        base = "/Users/me/.gemini/antigravity-cli/conversations/" + cid
        lsof = "\n".join("n" + base + x for x in (".db", ".db-wal", ".db-shm"))
        with mock.patch.object(sessions, "HOME", self.home), mock.patch.object(sessions, "kids", lambda pid: []), \
                mock.patch.object(sessions.shutil, "which", lambda n: "/usr/sbin/lsof"), \
                mock.patch.object(sessions, "run", lambda argv: mock.Mock(stdout=lsof)), \
                mock.patch.object(sessions.os.path, "isdir", lambda p: False):
            self.assertEqual(sessions.session_of(42), (cid, None))
            # its log is not Claude's: not found by session, nor by looking in a Claude folder
            self.assertIsNone(sessions.find_log({"agent": "agy", "session": cid}, "ELIA-5", ["/Users/me"]))
            self.assertIsNone(sessions.find_log({"agent": "agy", "at": 1}, "ELIA-5", ["/Users/me"]))


class AcceptCardArtifacts(unittest.TestCase):
    """SHEP-96: the Accept card links the task's other artifacts (a research report) beside its proof."""

    def test_its_other_artifacts_since_it_started(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.proof as proof
        arts = [{"id": "report-0003", "title": "SHEP-1: Three options", "task": "SHEP-1", "at": 300},
                {"id": "proof-0002", "title": "SHEP-1: Reactions · Proof", "task": "SHEP-1", "at": 200, "files": ["recipe.json"]},
                {"id": "other-0004", "title": "SHEP-2: Elsewhere", "task": "SHEP-2", "at": 400},
                {"id": "old-0001", "title": "SHEP-1: Before it started", "task": "SHEP-1", "at": 50}]
        s = Store([{"id": 1, "title": "Reactions", "state": "ready", "started": 100, "agents": []}])
        t = s.data["tasks"][0]
        with mock.patch.object(proof, "artifacts_of", lambda folder: arts):
            self.assertEqual(proof.task_artifacts(s, t, {"id": "proof-0002"}), [{"id": "report-0003", "title": "SHEP-1: Three options"}])
            self.assertEqual([a["id"] for a in proof.task_artifacts(s, t)], ["report-0003", "proof-0002"])


if __name__ == "__main__":
    unittest.main()


class CleanEnv(unittest.TestCase):
    """SHEP-86: herdr-core's holder hands every terminal the environment it was started with, so a holder started from
    inside Claude Code would hand each agent that session's markers (transcripts off, no folder-trust prompt). A task's
    agent launched through launch() runs without them; the person's own Claude Code config stays. Fake values only:
    hc is never run, the launch's own argv runs for real with `env` as its agent."""
    MARKERS = {"CLAUDECODE": "1", "CLAUDE_PID": "4242", "CLAUDE_CODE_CHILD_SESSION": "1", "CLAUDE_CODE_SESSION_ID": "fake-session",
               "CLAUDE_CODE_ENTRYPOINT": "cli", "CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/fake.sock", "CLAUDE_CODE_MESSAGING_TOKEN": "fake-token"}
    KEPT = {"CLAUDE_CONFIG_DIR": "/tmp/fake-claude", "CLAUDE_CODE_USE_BEDROCK": "1", "CLAUDE_CODE_DISABLE_AUTOUPDATER": "1", "FAKE_OTHER": "x"}

    def agent_env(self, argv):
        """What the agent at the end of a launch argv sees, started from a parent environment with the markers."""
        out = subprocess.run(argv, env={"PATH": os.environ["PATH"], **self.MARKERS, **self.KEPT}, capture_output=True, text=True, timeout=20)
        self.assertEqual(out.returncode, 0, out.stderr)
        return dict(l.split("=", 1) for l in out.stdout.splitlines() if "=" in l)

    def test_a_task_agent_starts_without_a_parent_claude_session(self):
        from types import SimpleNamespace
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.launch as L
        ran = []

        def run(argv, *a, **k):
            ran.append(argv)
            return subprocess.CompletedProcess(argv, 0, json.dumps({"address": "t9"}), "")
        s = SimpleNamespace(key=lambda t: "dev#6", dirty=False)
        task = {"id": 6, "title": "fake", "agents": []}
        with mock.patch.object(L, "run", run), mock.patch.object(L, "folder_for", lambda s, t: "/tmp"), \
                mock.patch.object(L, "agent_name", lambda *a: "dev-6-fake"), mock.patch.object(L, "track", lambda *a: None), \
                mock.patch.object(L, "coordinator_tab", lambda a: None), mock.patch.object(L, "own_argv", lambda a, *_, **__: a), \
                mock.patch.object(L, "in_shell", lambda a: a), mock.patch.object(L, "low_priority", lambda a, n: a):
            L.launch(s, task, ["env"], "sp1")
        hc = ran[0]
        self.assertEqual(hc[:2], ["hc", "run"])
        env = self.agent_env(hc[hc.index("--") + 1:])
        for k in self.MARKERS:
            self.assertNotIn(k, env)
        for k, v in self.KEPT.items():
            self.assertEqual(env.get(k), v)
        self.assertEqual(env.get("HERDR_BROWSER_TASK"), "dev#6")  # what the launch sets on purpose stays

    def test_the_same_line_everywhere(self):
        """herdr-task, the window and the service (core/clean-env.js) and restart.sh clear the same names."""
        sys.path.insert(0, BIN)
        import herdr_task.launch as L
        js = subprocess.run(["sh", "-c", 'grep -o "export const CLEAN_ENV = .*" "$1"', "x", os.path.join(ROOT, "core", "clean-env.js")],
                            capture_output=True, text=True).stdout.strip()
        self.assertEqual(js.split("`")[1].replace("\\\\", "\\"), L.CLEAN_ENV)
        restart = os.path.join(ROOT, "mods", "coordinator", "restart.sh")
        line = subprocess.run(["bash", "-c", 'eval "$(grep "^clean=" "$1")"; printf %s "$clean"', "x", restart], capture_output=True, text=True).stdout
        self.assertEqual(line, L.CLEAN_ENV)
        self.assertEqual(self.agent_env(["sh", "-c", line + ' exec env']).keys() & self.MARKERS.keys(), set())


class Revive(unittest.TestCase):
    """SHEP-30:an Assistant or a task's agent the core gave a plain shell (a crash, a power loss, a
    reboot) gets its agent typed back into that shell, on its own session. A fake core in memory:
    hc and the agents' session files are never touched."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.revive as revive
        self.r, self.mock = revive, mock
        self.dir = tempfile.mkdtemp()
        self.log = os.path.join(self.dir, "S1.jsonl")
        open(self.log, "w").close()
        self.terms = {}
        self.sent, self.screens, self.sessions = [], {}, {}

        def hc(args):
            out, code = "", 0
            if args[:2] == ["hc", "label"]:
                k, v = args[3].split("=", 1)
                self.terms[args[2]].setdefault("labels", {})[k] = v
            elif args[:2] == ["hc", "send"]:
                # each line first clears a parent Claude Code session's markers (SHEP-86)
                self.assertTrue(args[3].startswith(revive.CLEAN_ENV + " "), args[3])
                self.sent.append((args[2], args[3][len(revive.CLEAN_ENV) + 1:]))
            elif args[:2] == ["hc", "read"]:
                out = self.screens.get(args[2], "")
            else:
                raise AssertionError(f"ran {args}")
            return subprocess.CompletedProcess(args, code, out, "")

        def hc_json(*args):
            if args == ("ls",):
                return {"terminals": [{"address": a, "state": t["state"]} for a, t in self.terms.items()]}
            if args[0] == "get":
                return json.loads(json.dumps(self.terms[args[1]]))
            raise AssertionError(f"hc {args}")
        self.tasks = {"dev#6": {"id": 6, "state": "running", "agents": []}}
        for name, value in (("run", hc), ("hc_json", hc_json), ("session_of", lambda pid: self.sessions.get(pid, (None, None))),
                            ("task_of", lambda k: self.tasks.get((k.get("labels") or {}).get("herdr.task")))):
            p = mock.patch.object(revive, name, value)
            p.start()
            self.addCleanup(p.stop)

    def assistant(self, addr="t1", pid=100, agent_pid=101, status="done"):
        self.terms[addr] = {"address": addr, "state": "live", "pid": pid, "cwd": "/home/me", "template": "assistant",
                            "argv": ["claude", "--model", "opus", "You are the coordinator..."], "status": {"state": status},
                            "foreground": {"pid": agent_pid, "name": "claude", "argv": ["/opt/claude", "--model", "opus", "You are..."], "cwd": "/home/me/app"},
                            "labels": {"herdr.role": "coordinator", "herdr.chat": "p1"}}
        self.sessions[agent_pid] = ("S1", self.log)

    def crash(self, addr, pid=200):
        """What the core does with a kept terminal whose program was lost: a shell, labels kept."""
        t = self.terms[addr]
        t.update(pid=pid, argv=["/bin/bash"], template=None, status=None, foreground={"pid": pid, "name": "bash", "argv": ["/bin/bash"], "cwd": t["cwd"]})

    def tick(self):
        report = {}
        self.r.revive_pass(report)
        return report

    def test_the_assistant_resumes_its_own_session(self):
        self.assistant()
        self.tick()
        rec = json.loads(self.terms["t1"]["labels"]["herdr.resume"])
        self.assertEqual((rec["session"], rec["argv"], rec["pid"], rec["shell"]), ("S1", ["claude", "--model", "opus"], 100, False))
        self.assertEqual(self.sent, [])
        self.crash("t1")
        report = self.tick()
        self.assertEqual(report["revived"], [{"terminal": "t1", "how": "resume", "session": "S1"}])
        self.assertEqual(self.sent, [("t1", "cd /home/me/app && command claude --model opus --resume S1")])
        # Once: the next tick types nothing again, even while the agent starts.
        self.tick()
        self.assertEqual(len(self.sent), 1)

    def test_a_working_agent_carries_on(self):
        self.assistant(status="working")
        self.tick()
        self.crash("t1")
        self.tick()
        self.assertIn("--resume S1 'Herdr restarted", self.sent[0][1])

    def test_an_agent_that_quit_at_its_shell_stays_there(self):
        """The same process (no crash): a person who quit the agent keeps their shell."""
        self.assistant()
        self.tick()
        t = self.terms["t1"]
        t["foreground"] = {"pid": 100, "name": "bash", "argv": ["/bin/bash"]}
        self.tick()
        self.assertEqual(self.sent, [])

    def test_a_gone_session_starts_a_new_assistant_that_says_so(self):
        self.assistant()
        self.tick()
        os.unlink(self.log)
        self.crash("t1")
        with self.mock.patch.object(self.r, "template_of", lambda name="assistant": ("claude", "---\ncommand: claude\n---\nYou are the Assistant.")):
            self.tick()
        line = self.sent[0][1]
        self.assertTrue(line.startswith("cd /home/me/app && command claude --model opus '<restart>Your earlier session is gone"), line)
        self.assertIn("You are the Assistant.", line)
        self.assertNotIn("--resume", line)

    def test_a_resume_the_cli_refuses_starts_anew_once(self):
        self.assistant()
        self.tick()
        self.crash("t1")
        self.tick()
        self.screens["t1"] = "$ claude --resume S1\nNo conversation found with session ID: S1\n$ "
        with self.mock.patch.object(self.r, "template_of", lambda name="assistant": ("claude", None)):
            report = self.tick()
            self.tick()
        self.assertEqual(report["revived"][0]["how"], "fresh")
        self.assertEqual(len(self.sent), 2)
        self.assertIn("<restart>", self.sent[1][1])

    def test_never_a_second_copy_of_a_running_session(self):
        self.assistant()
        self.assistant("t2", pid=300, agent_pid=301)
        self.tick()
        self.crash("t1")
        report = self.tick()
        self.assertEqual(self.sent, [])
        self.assertEqual(report["revived"][0]["how"], "running")

    def test_a_worker_resumes_with_its_task_browser(self):
        self.terms["t5"] = {"address": "t5", "state": "live", "pid": 500, "cwd": "/wt", "name": "shep-6-web",
                            "argv": ["sh", "-c", "low", "herdr-low", "env", "HERDR_BROWSER_TASK=dev#6", "/bin/bash", "-ic", "x", "/bin/bash",
                                     "claude", "--settings", '{"a":1}', "Brief..."],
                            "status": {"state": "working"}, "foreground": {"pid": 501, "name": "claude", "argv": ["claude"], "cwd": "/wt"},
                            "labels": {"herdr.task": "dev#6", "herdr.agent": "claude"}}
        self.sessions[501] = ("S1", self.log)
        self.tick()
        self.crash("t5")
        self.tick()
        self.assertTrue(self.sent[0][1].startswith("""cd /wt && HERDR_BROWSER_TASK='dev#6' claude --settings '{"a":1}' --resume S1 'Herdr restarted"""), self.sent[0][1])

    def agy_worker(self, cid="0199aaaa-bbbb-cccc-dddd-eeeeeeeeeeee"):
        """A kept agy worker (FAC-7) in a temp home: its conversation is a db no chat reads, so no log."""
        import herdr_task.launch as launch
        for mod in (self.r, launch):
            p = self.mock.patch.object(mod, "HOME", self.dir)
            p.start()
            self.addCleanup(p.stop)
        conv = os.path.join(self.dir, ".gemini", "antigravity-cli", "conversations")
        os.makedirs(conv, exist_ok=True)
        self.db = os.path.join(conv, cid + ".db")
        open(self.db, "w").close()
        self.terms["t5"] = {"address": "t5", "state": "live", "pid": 500, "cwd": "/wt", "name": "shep-6-web",
                            "argv": ["sh", "-c", "low", "herdr-low", "env", "HERDR_BROWSER_TASK=dev#6", "/bin/zsh", "-lic", "x", "/bin/zsh",
                                     "agy", "--settings", '{"trustedWorkspaces":["/wt"]}', "--mode", "accept-edits", "-i", "Brief..."],
                            "status": {"state": "working"}, "foreground": {"pid": 501, "name": "agy", "argv": ["agy"], "cwd": "/wt"},
                            "labels": {"herdr.task": "dev#6", "herdr.agent": "agy"}}
        self.tasks["dev#6"].update(mode="worktree", worktree="/wt",
                                   agents=[{"address": "t5", "agent": "agy", "argv": ["agy", "--mode", "accept-edits"], "session": cid, "log": None}])
        self.sessions[501] = (cid, None)
        return cid

    def test_an_agy_worker_resumes_its_own_conversation(self):
        """Its exact conversation (never agy -c, which another agy in the folder can take), trusting its worktree."""
        cid = self.agy_worker()
        self.tick()
        rec = json.loads(self.terms["t5"]["labels"]["herdr.resume"])
        self.assertEqual((rec["kind"], rec["session"], rec["argv"]),
                         ("agy", cid, ["agy", "--settings", '{"trustedWorkspaces":["/wt"]}', "--mode", "accept-edits"]))
        self.crash("t5")
        report = self.tick()
        self.assertEqual(report["revived"], [{"terminal": "t5", "how": "resume", "session": cid}])
        self.assertEqual(self.sent, [("t5", "cd /wt && HERDR_BROWSER_TASK='dev#6' agy --settings '{\"trustedWorkspaces\":[\"/wt\"]}' "
                                            f"--mode accept-edits --conversation {cid} -i 'Herdr restarted (a crash, power loss or reboot) "
                                            "while you were working, and your session is resumed. Carry on where you left off.'")])

    def test_an_agy_worker_lost_before_any_record_resumes_trusting_its_worktree(self):
        cid = self.agy_worker()
        self.crash("t5")
        self.tick()
        line = self.sent[0][1]
        self.assertIn(f"--conversation {cid} -i 'Herdr restarted", line)
        settings = json.loads(shlex.split(line)[shlex.split(line).index("--settings") + 1])
        self.assertEqual(settings["trustedWorkspaces"], ["/wt"])

    def test_an_agy_worker_whose_conversation_is_gone_starts_anew(self):
        self.agy_worker()
        self.tick()
        os.unlink(self.db)
        self.crash("t5")
        self.tick()
        line = self.sent[0][1]
        self.assertNotIn("--conversation", line)
        self.assertIn("--mode accept-edits -i '<restart>Your earlier session is gone", line)

    def test_an_assistant_lost_before_any_record_starts_anew(self):
        """Lost before this change (or before its first record): a new Assistant, never a bare shell."""
        self.assistant()
        self.crash("t1")
        with self.mock.patch.object(self.r, "template_of", lambda name="assistant": ("claude", "---\ncommand: claude\n---\nRole.")):
            self.tick()
            self.tick()
        self.assertEqual(len(self.sent), 1)
        self.assertTrue(self.sent[0][1].startswith("cd /home/me && command claude '<restart>"), self.sent[0][1])

    def test_other_terminals_are_left_alone(self):
        self.terms["t9"] = {"address": "t9", "state": "live", "pid": 9, "argv": ["/bin/bash"], "foreground": {"pid": 9, "name": "bash"}, "labels": {}}
        self.tick()
        self.assertEqual((self.sent, self.terms["t9"]["labels"]), ([], {}))

    def test_one_typed_into_a_shell_comes_back_the_same_way(self):
        """An Assistant the person restarted by hand in its shell (claude -c): its process's flags, through the shell again."""
        self.assistant()
        t = self.terms["t1"]
        t.update(argv=["/bin/bash"], foreground={"pid": 101, "name": "claude", "argv": ["/opt/claude", "--dangerously-skip-permissions", "-c"], "cwd": "/home/me"})
        self.tick()
        self.crash("t1")
        self.tick()
        self.assertEqual(self.sent, [("t1", "cd /home/me && claude --dangerously-skip-permissions --resume S1")])

    def test_a_finished_tasks_worker_stays_a_shell(self):
        self.terms["t5"] = {"address": "t5", "state": "live", "pid": 500, "cwd": "/wt", "name": "shep-6-web", "argv": ["claude", "Brief"],
                            "foreground": {"pid": 501, "name": "claude", "argv": ["claude"], "cwd": "/wt"}, "labels": {"herdr.task": "dev#6"}}
        self.sessions[501] = ("S1", self.log)
        self.tick()
        self.tasks["dev#6"]["state"] = "done"
        self.crash("t5")
        self.tick()
        self.assertEqual(self.sent, [])

    def test_a_worker_lost_before_any_record_resumes_its_tasks_session(self):
        self.tasks["dev#6"]["agents"] = [{"address": "t5", "argv": ["claude"], "session": "S1", "log": self.log}]
        self.terms["t5"] = {"address": "t5", "state": "live", "pid": 500, "cwd": "/wt", "name": "shep-6-web", "argv": ["claude"],
                            "labels": {"herdr.task": "dev#6"}}
        self.crash("t5")
        with self.mock.patch("herdr_task.launch.own_argv", lambda argv, *_, **__: argv):
            self.tick()
        self.assertTrue(self.sent[0][1].startswith("cd /wt && HERDR_BROWSER_TASK='dev#6' claude --resume S1 'Herdr restarted"), self.sent)


class AutomationFromAnotherMachine(unittest.TestCase):
    """SHEP-14: an automation on machine A hears task events in a folder on machine B (--from). Two
    linked machines, simulated: B is a real herdr-task process on its own task store (asked as
    `herdr-task --on vmb auto feed` would ask it, its hc a fake that knows no terminals); A runs the
    scheduler's real round on another store. Only starting agents and the other passes are mocked."""

    def setUp(self):
        import argparse
        import contextlib
        import io
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.automations as au
        import herdr_task.store as store
        import herdr_task.settings as settings
        import herdr_task.machines as machines
        import herdr_task.home as home
        import herdr_task.autopilot as ap
        self.au, self.argparse, self.io, self.redirect = au, argparse, io, contextlib.redirect_stdout
        self.tmp = os.path.realpath(tempfile.mkdtemp())
        self.a_store, self.b_store = os.path.join(self.tmp, "a-tasks"), os.path.join(self.tmp, "b-tasks")
        self.a_folder, self.b_folder = os.path.join(self.tmp, "a-app"), os.path.join(self.tmp, "shepherdr")
        for f in (self.a_folder, self.b_folder):
            os.makedirs(f)
            subprocess.run(["git", "init", "-q", f], check=True)
        os.makedirs(self.b_store)
        fakebin = os.path.join(self.tmp, "bin")
        os.makedirs(fakebin)
        with open(os.path.join(fakebin, "hc"), "w") as f:
            f.write("#!/bin/sh\nexit 1\n")
        os.chmod(os.path.join(fakebin, "hc"), 0o755)
        self.b_env = {**os.environ, "HERDR_TASK_STORE": self.b_store, "PATH": fakebin + os.pathsep + os.environ.get("PATH", ""),
                      "HERDR_TASK_SETTINGS": os.path.join(self.tmp, "b-settings.json")}
        self.b_tasks, self.b_up, self.started = [], True, []
        self.write_b()
        real_run = subprocess.run

        def run(argv, **kw):
            # A's `herdr-task --on vmb ...`: answered by B's own herdr-task, or not at all while B is down.
            if "--on" in argv and argv[argv.index("--on") + 1] == "vmb":
                if not self.b_up:
                    return subprocess.CompletedProcess(argv, 1, "", "hc: vmb: connection refused\n")
                rest = argv[argv.index("--on") + 2:]
                return real_run([sys.executable, os.path.join(BIN, "herdr-task"), *rest], env=self.b_env, **kw)
            return real_run(argv, **kw)

        def begin(s, t, argv, space, parent=None):
            t["state"], t["started"] = "running", 1
            self.started.append({"ref": s.ref(t), "prompt": t["prompt"]})

        patches = [mock.patch.object(store, "STORE", self.a_store), mock.patch.object(au, "STORE", self.a_store),
                   mock.patch.object(au, "SEEN", os.path.join(self.a_store, ".seen.json")),
                   mock.patch.object(settings, "SETTINGS", os.path.join(self.tmp, "a-settings.json")),
                   mock.patch.object(au.subprocess, "run", run), mock.patch.object(au, "guard", lambda a: None),
                   mock.patch.object(au, "begin", begin), mock.patch.object(au, "queue_reason", lambda s, t: None),
                   mock.patch.object(au, "worker_argv", lambda s, t, space: ["claude"]),
                   mock.patch.object(au, "ensure_scheduler", lambda quiet=False: {}),
                   mock.patch.object(au, "default_space", lambda: "sp1"),
                   mock.patch.object(au, "live_agents", lambda key: []),
                   mock.patch.object(machines, "connected_machines", lambda: [{"name": "vmb", "state": "connected"}]),
                   mock.patch.object(ap, "autopilot_pass", lambda r: None), mock.patch.object(home, "home_pass", lambda r: None),
                   mock.patch.dict(os.environ, {"HC_TERMINAL_ID": ""})]
        patches += [mock.patch.object(au, n, lambda *a, **k: None) for n in (
            "tick_store", "retry_pass", "archive_pass", "news_pass", "end_done_pass", "memory_guard", "backup_pass",
            "cli_check_pass", "machines_pass")]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)

    def write_b(self):
        """B's task list as its own herdr-task keeps it."""
        from_b = {"version": 1, "folder": self.b_folder, "kind": "repo", "name": "shepherdr", "prefix": "SHEP",
                  "next": len(self.b_tasks) + 1, "tasks": self.b_tasks}
        slug = self.b_folder.strip("/").replace("/", "-")
        with open(os.path.join(self.b_store, slug + ".json"), "w") as f:
            json.dump(from_b, f)

    def b_task(self, title, state="todo", **kw):
        self.b_tasks.append({"id": len(self.b_tasks) + 1, "title": title, "prompt": "", "mode": "worktree", "state": state,
                             "created": 1, "agents": [], **kw})
        self.write_b()

    def b_merge(self, n):
        t = self.b_tasks[n - 1]
        t.update(state="done", merged=True, done_at=2)
        self.write_b()

    def auto(self, sub, **kw):
        args = self.argparse.Namespace(auto=sub, folder=self.a_folder, json=True, id=None, title=None, prompt=None,
                                       prompt_file=None, every=None, on=None, match=None, agent=None, space=None,
                                       parent=None, here=False, worktree=False, from_=None)
        for k, v in kw.items():
            setattr(args, k, v)
        out = self.io.StringIO()
        with self.redirect(out):
            self.au.cmd_auto(args)
        return json.loads(out.getvalue())

    def round(self):
        """One scheduler round on A; each round reads all it knows from disk (a restart loses nothing)."""
        report = {"started": [], "done": [], "attention": [], "fired": []}
        self.au.one_pass(report)
        return report["fired"]

    def test_a_merge_on_b_starts_one_run_on_a(self):
        self.b_task("Fix the scheduler")
        self.b_task("Write docs")
        a = self.auto("add", title="Update Shepherdr everywhere", prompt="Update every machine.", on="merged",
                      from_=f"vmb:{self.b_folder}")
        self.assertEqual(a["from"], f"vmb:{self.b_folder}")
        self.assertIn(f"When a task is merged in vmb:{self.b_folder}", a["trigger"])
        self.assertNotIn("warning", a)
        self.assertEqual(self.round(), [])  # nothing happened on B yet
        self.b_merge(1)
        fired = self.round()
        self.assertEqual([f["why"] for f in fired], ["merged: vmb:SHEP-1"])
        self.assertEqual(len(self.started), 1)
        brief = self.started[0]["prompt"]
        self.assertTrue(brief.startswith("Update every machine."))
        for words in ("SHEP-1", "Fix the scheduler", "vmb"):
            self.assertIn(words, brief)
        # Rounds again, and A's scheduler restarted (a round keeps nothing in memory): no second run.
        self.assertEqual(self.round() + self.round(), [])
        self.assertEqual(len(self.started), 1)
        # auto show and list say where it listens
        shown = self.auto("show", id="1")
        self.assertEqual(shown["from"], f"vmb:{self.b_folder}")
        self.assertEqual(shown["runs"][-1]["from"], f"vmb:{self.b_folder}")

    def test_missed_while_down_is_caught_up_once(self):
        self.b_task("Fix one")
        self.auto("add", title="Update", prompt="Go.", on="merged", from_=f"vmb:{self.b_folder}")
        self.round()
        # B can't be reached: its merges meanwhile wait, and nothing fires
        self.b_up = False
        self.b_merge(1)
        self.b_task("Fix two", state="done", merged=True)  # added and merged while unseen
        self.assertEqual(self.round(), [])
        self.assertIn("Couldn't ask vmb", self.auto("show", id="1")["last_error"]["message"])
        # B is back: each merge fires once, the error goes
        self.b_up = True
        self.assertEqual(sorted(f["why"] for f in self.round()), ["merged: vmb:SHEP-1", "merged: vmb:SHEP-2"])
        self.assertEqual(self.round(), [])
        self.assertIsNone(self.auto("show", id="1")["last_error"])
        # A was down (no rounds) while B merged another: once, on A's next round
        self.b_task("Fix three")
        self.round()
        self.b_merge(3)
        self.assertEqual([f["why"] for f in self.round()], ["merged: vmb:SHEP-3"])
        self.assertEqual(self.round(), [])
        self.assertEqual(len(self.started), 3)

    def test_match_still_narrows(self):
        self.b_task("Fix the scheduler")
        self.b_task("Write docs")
        self.auto("add", title="Update", prompt="Go.", on="merged", match="fix", from_=f"vmb:{self.b_folder}")
        self.b_merge(2)
        self.assertEqual(self.round(), [])
        self.b_merge(1)
        self.assertEqual([f["why"] for f in self.round()], ["merged: vmb:SHEP-1"])

    def test_other_events_too(self):
        self.auto("add", title="Look", prompt="Look.", on="added,ready", from_=f"vmb:{self.b_folder}")
        self.b_task("New thing")
        self.assertEqual([f["why"] for f in self.round()], ["added: vmb:SHEP-1"])
        self.b_tasks[0]["state"] = "ready"
        self.write_b()
        self.assertEqual([f["why"] for f in self.round()], ["ready: vmb:SHEP-1"])

    def test_unknown_or_unreachable_machine(self):
        with self.assertRaises(self.au.Fail) as e:
            self.auto("add", title="Update", prompt="Go.", on="merged", from_="vm-nope:/home/x/app")
        self.assertIn("No machine vm-nope", str(e.exception))
        with self.assertRaises(self.au.Fail):
            self.auto("add", title="Update", prompt="Go.", from_=f"vmb:{self.b_folder}")  # no --on
        self.b_up = False
        a = self.auto("add", title="Update", prompt="Go.", on="merged", from_=f"vmb:{self.b_folder}")
        self.assertIn("couldn't ask vmb", a["warning"])
        # its first answer is its first look: from then on every merge is news
        self.b_task("Fix")
        self.b_up = True
        self.assertEqual(self.round(), [])
        self.b_merge(1)
        self.assertEqual(len(self.round()), 1)


class LostLogs(unittest.TestCase):
    """SHEP-26: a task agent that ended before any pass saw its session (a short automation run) gets
    its log found afterwards, so its chat reads back once its terminal is gone. A throwaway HOME
    with Claude Code and Codex logs; nothing is run."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.sessions as sessions
        import herdr_task.tick as tick
        self.sessions, self.tick = sessions, tick
        self.home = tempfile.mkdtemp()
        p = mock.patch.object(sessions, "HOME", self.home)
        p.start()
        self.addCleanup(p.stop)
        self.folder = "/Users/me"
        self.at = 1793873842495  # the agent's launch (ms)

    def stamp(self, ms):
        import datetime
        return datetime.datetime.fromtimestamp(ms / 1000, datetime.timezone.utc).isoformat().replace("+00:00", "Z")

    def claude_log(self, sid, at, first, folder=None):
        d = self.sessions.project_dir(folder or self.folder)
        os.makedirs(d, exist_ok=True)
        f = os.path.join(d, sid + ".jsonl")
        with open(f, "w") as fh:
            fh.write(json.dumps({"type": "mode", "mode": "normal", "sessionId": sid}) + "\n")
            fh.write(json.dumps({"type": "user", "timestamp": self.stamp(at), "sessionId": sid, "message": {"role": "user", "content": first}}) + "\n")
            fh.write(json.dumps({"type": "assistant", "timestamp": self.stamp(at + 2000), "message": {"role": "assistant", "content": [{"type": "text", "text": "Done."}]}}) + "\n")
        return f

    def store(self, task):
        class S:
            dirty = False
            loc = {"folder": self.folder}
            def ref(self, t): return "ELIA-5"
        return S()

    def test_an_ended_agent_without_a_session_gets_the_log_that_names_its_task(self):
        # the Home Assistant's own session in the same folder (started earlier), another task's run after, and its own
        self.claude_log("assistant", self.at - 3600000, "You are the coordinator of one space in Herdr")
        self.claude_log("other", self.at + 5000, "Your task, ELIA-50:\nSomething else")
        mine = self.claude_log("mine", self.at + 1500, [{"type": "text", "text": "You work on one Herdr task.\n\nYour task, ELIA-5:\nSay hi"}])
        t = {"id": 5, "state": "done", "done_at": self.at + 14268, "ended_at": self.at + 81555,
             "agents": [{"address": "t1h", "agent": "claude", "argv": ["claude"], "at": self.at, "parent": "t13", "ended": self.at + 81555}]}
        self.assertTrue(self.tick.lost_log(t, t["agents"][0]))
        s = self.store(t)
        self.sessions.find_lost_logs(s, t)
        self.assertEqual(t["agents"][0]["log"], mine)
        self.assertTrue(s.dirty)
        # looked for once: never again
        self.assertFalse(self.tick.lost_log(t, t["agents"][0]))

    def test_by_its_session_when_it_has_one(self):
        mine = self.claude_log("S9", self.at, "anything", folder="/elsewhere")
        t = {"id": 5, "state": "done", "agents": [{"address": "t1", "agent": "claude", "session": "S9", "at": self.at, "ended": self.at + 1}]}
        self.sessions.find_lost_logs(self.store(t), t)
        self.assertEqual(t["agents"][0]["log"], mine)

    def test_a_codex_rollout(self):
        import datetime
        day = datetime.date.fromtimestamp(self.at / 1000)
        d = os.path.join(self.home, ".codex", "sessions", f"{day:%Y}", f"{day:%m}", f"{day:%d}")
        os.makedirs(d)
        f = os.path.join(d, "rollout-2026-11-05T10-00-00-0199aaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
        with open(f, "w") as fh:
            fh.write(json.dumps({"timestamp": self.stamp(self.at + 800), "type": "session_meta", "payload": {"id": "x", "timestamp": self.stamp(self.at + 800), "cwd": self.folder}}) + "\n")
            fh.write(json.dumps({"timestamp": self.stamp(self.at + 900), "type": "response_item", "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "<environment_context>…</environment_context>"}]}}) + "\n")
            fh.write(json.dumps({"timestamp": self.stamp(self.at + 900), "type": "response_item", "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Your task, ELIA-5:\nSay hi"}]}}) + "\n")
        t = {"id": 5, "state": "done", "agents": [{"address": "t1", "agent": "codex", "at": self.at, "ended": self.at + 60000}]}
        self.sessions.find_lost_logs(self.store(t), t)
        self.assertEqual(t["agents"][0]["log"], f)

    def test_no_log_left_is_looked_for_once_and_stays_none(self):
        self.claude_log("before", self.at - 7200000, "Your task, ELIA-5:\nan older run")  # started long before this agent
        t = {"id": 5, "state": "done", "agents": [{"address": "t1", "agent": "claude", "at": self.at, "ended": self.at + 60000}]}
        self.sessions.find_lost_logs(self.store(t), t)
        self.assertNotIn("log", t["agents"][0])
        self.assertTrue(t["agents"][0]["log_looked"])
        self.assertFalse(self.tick.lost_log(t, t["agents"][0]))

    def test_a_running_agent_is_left_to_the_session_pass(self):
        t = {"id": 5, "state": "running", "agents": [{"address": "t1", "agent": "claude", "at": self.at}]}
        self.assertFalse(self.tick.lost_log(t, t["agents"][0]))


class AgentReactions(unittest.TestCase):
    """SHEP-48: Reactions for every agent: a task worker, an automation's run and a plain agent tab are
    told by their space at the start (no settings block), and a running one its hook taught gets one
    <herdr-setting name="reactions"> line when it changes, telling it to carry on. A throwaway settings
    file and state; hc is faked (its terminals are dicts), nothing is sent."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.settings as settings
        import herdr_task.reactions as reactions
        import herdr_task.assistant_settings as aset
        import herdr_task.news as news
        self.settings, self.reactions, self.aset = settings, reactions, aset
        self.dir = tempfile.mkdtemp()
        self.file = os.path.join(self.dir, "shepherdr.json")
        self.terminals, self.sent = {}, []

        def hc_json(*args):
            if args[0] == "ls":  # hc ls --label herdr.reactions=<v>
                k, v = args[2].split("=", 1)
                return {"terminals": [{"address": a, "state": t["state"]} for a, t in self.terminals.items()
                                      if (t.get("labels") or {}).get(k) == v]}
            return self.terminals.get(args[1]) if args[0] == "get" else None

        def run(args, **_):
            if args[:2] == ["hc", "label"]:
                k, v = args[3].split("=", 1)
                t = self.terminals[args[2]]
                t["labels"] = {**(t.get("labels") or {}), k: v}
            return subprocess.CompletedProcess(args, 0, "", "")

        for target, name, value in [
            (settings, "SETTINGS", self.file), (reactions, "SETTINGS", self.file),
            (reactions, "STATE", os.path.join(self.dir, ".reactions.json")),
            (reactions, "hc_json", hc_json), (aset, "hc_json", hc_json), (reactions, "run", run), (aset, "run", run),
            (reactions, "addon_off", lambda name: False),
            (news, "news_send", lambda addr, text: self.sent.append((addr, text)) or True),
        ]:
            p = mock.patch.object(target, name, value)
            p.start()
            self.addCleanup(p.stop)

    def write(self, d):
        with open(self.file, "w") as f:
            json.dump(d, f)
        self.saves = getattr(self, "saves", 0) + 1
        os.utime(self.file, (1700000000 + self.saves, 1700000000 + self.saves))

    def worker(self, addr="t9", space="sp1", turn=3, **labels):
        """A task's agent (a worker, or an automation's run): its task's space on its terminal."""
        self.terminals[addr] = {"state": "live", "turn": turn, "cwd": "/wt/task-1", "labels": {"herdr.task": "dev#1", "herdr.task.space": space, **labels}}

    def test_every_agent_type_is_told_by_its_space(self):
        """A task worker and an automation's run (their task's space), a plain agent tab (the space whose folder
        holds its folder, else Everywhere's): the Assistant's Reactions words, on and off, without its block."""
        self.worker("t9", "sp2")
        self.worker("t10", "sp1", **{"herdr.task": "dev#auto-3"})
        self.terminals["t11"] = {"state": "live", "cwd": "/repos/shop/web", "labels": {}}
        self.terminals["t12"] = {"state": "live", "cwd": "/elsewhere"}
        self.write({"spaces": {"sp2": {"agents": {"reactions": False}}, "sp4": {"folder": "/repos/shop", "agents": {"reactions": False}}}})
        notes = {a: self.aset.session_note(a) for a in ("t9", "t10", "t11", "t12")}
        from herdr_task.feedback import AGENT_NOTE
        fb = "\n" + AGENT_NOTE  # Feedback offers (SHEP-78), on by default
        self.assertEqual(notes, {"t9": self.reactions.OFF + fb, "t10": self.reactions.GUIDE + fb, "t11": self.reactions.OFF + fb, "t12": self.reactions.GUIDE + fb})
        self.assertEqual([self.terminals[a]["labels"]["herdr.reactions"] for a in ("t9", "t10", "t11", "t12")], ["off", "on", "off", "on"])
        self.write({"everywhere": {"agents": {"reactions": False}}})
        for a in ("t9", "t10", "t11", "t12"):
            self.assertIn("never react", self.aset.session_note(a))
        for words in ("👀", "👍", "✅", "😂", "🙏", "<hc react=", "never more than one", "never instead of an answer", "<hc no-reply/>", "In to=, leave out double quotes",
                      "most specific, delightful or funny emoji", "not a generic 👍 or 👀 by default", "🚀", "🔥", "🐛", "🧹", "🫡", "🤌", "🍿",
                      "Keep it tasteful", "most messages get none"):
            self.assertIn(words, self.reactions.GUIDE)
        # the turned-on notice carries the same guidance
        self.assertIn("most specific, delightful or funny emoji", self.reactions.TURNED_ON)

    def test_running_agents_of_every_type_are_told_once(self):
        """Workers, automation runs and agent tabs their hook taught are told once when it changes for their
        space, and told to carry on; one never taught (started before) and Assistants (the settings pass
        tells them) are left alone."""
        self.worker("t9", "sp1", **{"herdr.reactions": "on"})
        self.worker("t10", "sp2", **{"herdr.reactions": "on", "herdr.task": "dev#auto-3"})
        self.terminals["t11"] = {"state": "live", "turn": 2, "cwd": "/repos/shop", "labels": {"herdr.reactions": "on"}}
        self.worker("t12", "sp1")  # started before: no label
        self.terminals["t5"] = {"state": "live", "turn": 3, "labels": {"herdr.role": "coordinator", "herdr.space": "sp1", "herdr.reactions": "on"}}
        self.write({"spaces": {"sp1": {"agents": {"reactions": False}}, "sp4": {"folder": "/repos/shop", "agents": {"reactions": False}}}})
        report = {}
        self.reactions.reactions_pass(report, {})
        self.assertEqual(sorted(a for a, _ in self.sent), ["t11", "t9"])
        for _, w in self.sent:
            self.assertRegex(w, r'^<herdr-setting name="reactions" value="off" scope="space">Reactions are now off[^<]*'
                                r'<hc react> lines\)\. Nothing to answer: carry on with what you were doing; if nothing, reply only <hc no-reply/>\.</herdr-setting>$')
        self.assertEqual(self.terminals["t9"]["labels"]["herdr.reactions"], "off")
        self.assertEqual(self.terminals["t10"]["labels"]["herdr.reactions"], "on")
        self.assertNotIn("herdr.reactions", self.terminals["t12"]["labels"])
        self.assertEqual(self.terminals["t5"]["labels"]["herdr.reactions"], "on")
        self.reactions.reactions_pass({}, {})  # the file didn't change: nothing again
        self.write({"spaces": {"sp1": {"agents": {"reactions": False}}, "sp4": {"folder": "/repos/shop", "agents": {"reactions": False}}}, "tasks": {}})
        self.reactions.reactions_pass({}, {})
        self.assertEqual(len(self.sent), 2)
        self.write({})  # back on: each told again, with the how
        self.reactions.reactions_pass({}, {})
        self.assertEqual(sorted(a for a, _ in self.sent[2:]), ["t11", "t9"])
        self.assertIn('scope="everywhere">Reactions are now on', self.sent[2][1])
        self.assertIn("👀", self.sent[2][1])


class ReviewerFlagsSet(unittest.TestCase):
    """add/edit's --reviewer-model and --reviewer-effort flags set the task's fields (they used to fail)."""

    def test_flags(self):
        import argparse
        sys.path.insert(0, BIN)
        import herdr_task.settings as settings
        t = {}
        args = argparse.Namespace(reviewer="claude", reviewer_model="opus", reviewer_effort="high", set=None)
        settings.set_opts(t, args)
        self.assertEqual((t["reviewer"], t["reviewer_model"], t["reviewer_effort"]), ("claude", "opus", "high"))
        settings.set_task(t, "reviewer.model", "sonnet")
        self.assertEqual(t["reviewer_model"], "sonnet")


class AssistantSettings(unittest.TestCase):
    """SHEP-42/SHEP-49: a space's Assistant is told its settings (Reactions among them): the block its
    session gets at the start, and the one message a running one gets when some change. A throwaway
    settings file and state; hc is faked (its terminals are dicts), nothing is sent."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.settings as settings
        import herdr_task.assistant_settings as aset
        import herdr_task.news as news
        self.settings, self.aset, self.news = settings, aset, news
        self.dir = tempfile.mkdtemp()
        self.file = os.path.join(self.dir, "shepherdr.json")
        self.terminals, self.labelled, self.sent = {}, [], []
        self.auto = {}  # space -> (on, schedule, direction)

        def hc_json(*args):
            return self.terminals.get(args[1]) if args[0] == "get" else None

        def run(args, **_):
            if args[:2] == ["hc", "label"]:
                k, v = args[3].split("=", 1)
                self.terminals[args[2]].setdefault("labels", {})[k] = v
                self.labelled.append((args[2], args[3]))
            return subprocess.CompletedProcess(args, 0, "", "")

        for target, name, value in [
            (settings, "SETTINGS", self.file), (aset, "SETTINGS", self.file),
            (aset, "STATE", os.path.join(self.dir, ".assistant-settings.json")), (aset, "hc_json", hc_json),
            (aset, "run", run), (aset.reactions, "run", run), (aset, "addon_off", lambda name: False),
            (aset, "coordinators", lambda: list(self.terminals)),
            (aset, "autopilot_of", lambda space, cfg: self.auto.get(space, (False, "every 60 minutes", ""))),
            (news, "news_send", lambda addr, text: self.sent.append((addr, text)) or True),
        ]:
            p = mock.patch.object(target, name, value)
            p.start()
            self.addCleanup(p.stop)

    def write(self, d):
        with open(self.file, "w") as f:
            json.dump(d, f)
        # a change the pass looks at at once (the file's time), as the window's or the web's save
        self.saves = getattr(self, "saves", 0) + 1
        os.utime(self.file, (1700000000 + self.saves, 1700000000 + self.saves))

    def assistant(self, addr="t5", space="sp1", turn=3, **labels):
        self.terminals[addr] = {"state": "live", "turn": turn, "labels": {"herdr.role": "coordinator", "herdr.space": space, **labels}}

    def line(self, note, label):
        return next(x for x in note.split("\n") if x.startswith(f"- {label}: "))

    def test_reactions_on_by_default_everywhere_and_a_space_sets_its_own(self):
        self.assertTrue(self.settings.DEFAULTS["agents"]["reactions"])
        self.assertTrue(self.settings.reactions_on("sp1"))
        self.write({"everywhere": {"agents": {"reactions": False}}, "spaces": {"sp2": {"agents": {"reactions": True}}}})
        self.assertFalse(self.settings.reactions_on("sp1"))
        self.assertTrue(self.settings.reactions_on("vm/sp2"))
        self.assertFalse(self.settings.reactions_on(None))

    def test_the_assistants_reactions_from_before_still_apply(self):
        """SHEP-48: SHEP-42 kept it as assistant.reactions: read where a layer has no agents.reactions (the
        window and the web app move it on their next change), in reactions_on and the Assistant's block."""
        self.write({"everywhere": {"assistant": {"reactions": False}}, "spaces": {"sp2": {"assistant": {"reactions": True}},
                                                                                 "sp3": {"assistant": {"reactions": True}, "agents": {"reactions": False}}}})
        self.assertFalse(self.settings.reactions_on("sp1"))
        self.assertTrue(self.settings.reactions_on("sp2"))
        self.assertFalse(self.settings.reactions_on("sp3"))
        self.assistant("t5", "sp2")
        self.assertIn("- Reactions: on", self.aset.session_note("t5"))
        self.assistant("t6", "sp3")
        self.assertIn("- Reactions: off (never react to the person's messages); set for this space", self.aset.session_note("t6"))
        self.write({"everywhere": {"assistant": {"reactions": False}, "agents": {"reactions": True}}})
        self.assertTrue(self.settings.reactions_on("sp1"))

    def test_the_block_by_default(self):
        self.assistant()
        note = self.aset.session_note("t5")
        self.assertTrue(note.startswith("Your settings: "))
        for label, words in [("Review", "off"), ("Merge", "ask the person first"), ("Proof", "on"), ("Task agent", "claude"),
                             ("Base branch", "the folder's checked-out branch"), ("Worktrees", "on"), ("Tasks at once", "10 per folder"),
                             ("Task notes", "none"), ("Autopilot", "off"), ("Reactions", "on"),
                             ("Feedback offers", "on (offer once to file a Shepherdr problem as an issue on eliasstravik/shepherdr")]:
            self.assertTrue(self.line(note, label).startswith(f"- {label}: {words}"), self.line(note, label))
        # nothing differs from Herdr's default: no line says who set it
        self.assertNotIn("; set for", note)
        self.assertNotIn("; your setting", note)
        self.assertEqual(len(note.split("\n")), 13)  # head, 11 lines, the Reactions guide

    def test_the_block_for_a_space_with_its_own(self):
        self.write({"everywhere": {"tasks": {"review_rounds": 2, "merge": "checks"}, "assistant": {"reactions": True}},
                    "spaces": {"sp1": {"tasks": {"base": "dev", "start_notes": "bun install", "max_running": 3, "worktrees": False},
                                       "worker": {"agent": "codex", "model": "gpt-5", "effort": "high"},
                                       "reviewer": {"agent": "claude", "model": "opus"},
                                       "assistant": {"reactions": False}},
                               "sp2": {"tasks": {"merge": "auto"}}}})
        self.auto["sp1"] = (True, "every 30 minutes", "Ship the beta")
        self.assistant()
        note = self.aset.session_note("t5")
        self.assertEqual(self.line(note, "Review"), "- Review: on, at most 2 rounds, must fix major and up (blocker, major), reviewer claude, model opus; set for this space")
        self.assertEqual(self.line(note, "Merge"), "- Merge: after checks (Herdr merges a ready task once its checks pass); your setting everywhere")
        self.assertEqual(self.line(note, "Proof"), "- Proof: on")
        self.assertEqual(self.line(note, "Task agent"), "- Task agent: codex, model gpt-5, effort high; set for this space")
        self.assertIn("- Base branch: dev ", note)
        self.assertIn("- Worktrees: off (tasks work in the folder itself); set for this space", note)
        self.assertIn("- Tasks at once: 3 per folder", note)
        self.assertIn("- Task notes: start notes set", note)
        self.assertEqual(self.line(note, "Autopilot"), "- Autopilot: on, every 30 minutes, direction: Ship the beta; set for this space")
        self.assertIn("- Reactions: off", note)
        self.assertIn("never react", note.split("\n")[-1])
        self.assertNotIn("👀", note)
        labels = self.terminals["t5"]["labels"]
        self.assertEqual(labels["herdr.reactions"], "off")
        self.assertEqual(sorted(self.aset.parse(labels["herdr.settings"])),
                         sorted(["review", "merge", "proof", "agent", "base", "worktrees", "max_running", "notes", "autopilot", "reactions", "feedback"]))
        # another space inherits Everywhere's, with its own merge
        self.assistant("t6", "sp2")
        other = self.aset.session_note("t6")
        self.assertIn("- Merge: on its own (Herdr merges a ready task); set for this space", other)
        self.assertIn("- Reactions: on", other)
        self.assertIn("👀", other)

    def test_only_assistants_get_the_block_other_agents_reactions_only(self):
        self.terminals["t9"] = {"state": "live", "labels": {"herdr.task": "SHEP-1"}}
        note = self.aset.session_note("t9")
        self.assertNotIn("Your settings", note)
        from herdr_task.feedback import AGENT_NOTE
        self.assertEqual(note, self.aset.reactions.GUIDE + "\n" + AGENT_NOTE)  # and Feedback (SHEP-78), on by default
        self.assertEqual(self.terminals["t9"]["labels"], {"herdr.task": "SHEP-1", "herdr.reactions": "on"})
        self.assertEqual(self.aset.session_note("nope"), "")  # not a terminal here

    def test_the_hook_text_is_json_ready(self):
        import contextlib
        import io
        self.assistant()
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.aset.cmd_session_note(type("A", (), {"terminal": "t5"})())
        ctx = json.loads('{"c":"base.' + out.getvalue() + '"}')["c"]
        self.assertTrue(ctx.startswith("base. Your settings: "))
        self.assertIn("\n- Review: off\n", ctx)
        self.assertIn('<hc react="👀"', ctx)

    def test_a_running_assistant_is_told_once_what_changed_in_one_message(self):
        self.assistant()
        self.aset.session_note("t5")
        quiet = {}
        self.aset.settings_pass({}, quiet)
        self.assertEqual(self.sent, [])  # told at its start already
        self.write({"everywhere": {"tasks": {"review_rounds": 3}}, "spaces": {"sp1": {"assistant": {"reactions": False}}}})
        report = {}
        self.aset.settings_pass(report, quiet)
        self.assertEqual(len(self.sent), 1)
        lines = self.sent[0][1].split("\n")
        self.assertEqual(len(lines), 2)
        self.assertRegex(lines[0], r'^<herdr-setting name="review" value="on" scope="everywhere">Review is now on, at most 3 rounds[^<]*'
                                   r'; your setting everywhere\. Tasks you add from now on get it; running ones keep their brief\.</herdr-setting>$')
        self.assertRegex(lines[1], r'^<herdr-setting name="reactions" value="off" scope="space">Reactions are now off[\s\S]*<hc no-reply/>\.</herdr-setting>$')
        self.assertEqual(report["settings"], [{"to": "t5", "changed": ["review", "reactions"]}])
        self.assertEqual(self.terminals["t5"]["labels"]["herdr.reactions"], "off")

    def test_a_save_that_changes_nothing_for_it_says_nothing(self):
        self.write({"everywhere": {"tasks": {"merge": "checks"}}})
        self.assistant()
        self.aset.session_note("t5")
        # the same values saved again
        self.write({"everywhere": {"tasks": {"merge": "checks"}}})
        self.aset.settings_pass({}, {})
        # another space's change, and Everywhere's where this space sets its own
        self.write({"everywhere": {"tasks": {"merge": "checks"}}, "spaces": {"sp2": {"tasks": {"merge": "auto"}}}})
        self.aset.settings_pass({}, {})
        self.write({"everywhere": {"tasks": {"merge": "checks"}, "worker": {"agent": "codex"}},
                    "spaces": {"sp1": {"worker": {"agent": "claude", "model": "opus"}}}})
        self.aset.settings_pass({}, {})
        self.assertEqual(len(self.sent), 1)  # only the last: sp1's own agent
        self.assertIn('name="agent" value="claude/opus" scope="space"', self.sent[0][1])
        self.write({"everywhere": {"tasks": {"merge": "checks"}, "worker": {"agent": "codex"}},
                    "spaces": {"sp1": {"worker": {"agent": "claude", "model": "opus"}}}})
        self.aset.settings_pass({}, {})
        self.assertEqual(len(self.sent), 1)

    def test_back_to_the_default_says_so(self):
        self.write({"everywhere": {"tasks": {"proof": False}}})
        self.assistant()
        self.aset.session_note("t5")
        self.write({})
        self.aset.settings_pass({}, {})
        self.assertIn('<herdr-setting name="proof" value="on" scope="everywhere">Proof is now on; Herdr\'s default.', self.sent[0][1])

    def test_one_started_before_gets_the_whole_block_once_it_has_had_a_turn(self):
        self.assistant("t5", "sp1", **{"herdr.reactions": "on"})
        self.assistant("t6", "sp2")
        self.assistant("t7", "sp1", turn=0)  # just started: its hook tells it
        self.write({"spaces": {"sp2": {"assistant": {"reactions": True}}}})
        self.aset.settings_pass({}, {})
        self.assertEqual([a for a, _ in self.sent], ["t5", "t6"])
        self.assertTrue(self.sent[0][1].startswith('<herdr-setting name="all">Your settings: '))
        self.assertNotIn("👀", self.sent[0][1])  # taught reactions already
        self.assertIn("👀", self.sent[1][1])
        self.assertNotIn("herdr.settings", self.terminals["t7"]["labels"])
        self.write({"spaces": {"sp2": {"assistant": {"reactions": True}}}})
        self.aset.settings_pass({}, {})
        self.assertEqual(len(self.sent), 2)

    def test_busy_with_the_person_tries_again_next_time(self):
        from unittest import mock
        self.assistant()
        self.aset.session_note("t5")
        self.write({"everywhere": {"tasks": {"merge": "auto"}}})
        with mock.patch.object(self.news, "news_send", lambda addr, text: False):
            self.aset.settings_pass({}, {})
        self.write({"everywhere": {"tasks": {"merge": "auto"}}})
        self.aset.settings_pass({}, {})
        self.assertEqual(len(self.sent), 1)
        self.assertIn('name="merge" value="auto"', self.sent[0][1])

    def test_chat_tags_hide_the_message(self):
        """Every line is one <herdr-…> tag: the chat shows nothing of it (core/chat-log.js strips Herdr's tags)."""
        rs = self.aset.rows("sp1", {}, (False, "", ""))
        for line in self.aset.notice(rs).split("\n"):
            self.assertRegex(line, r'^<herdr-setting name="\w+" value="[^"]*" scope="\w+">[^\n]*</herdr-setting>$')


class StarPrompt(unittest.TestCase):
    """SHEP-79: the one-time "Enjoying Shepherdr?" card (herdr_task/star.py), in throwaway settings files and task
    lists: herdr-artifact is faked, and nothing else may run."""

    def setUp(self):
        import contextlib
        import io
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.settings as settings
        import herdr_task.star as star
        self.star, self.io, self.redirect, self.mock = star, io, contextlib.redirect_stdout, mock
        self.tmp = tempfile.mkdtemp()
        self.use(os.path.join(self.tmp, "a.json"))
        self.done = 3
        self.runs = []
        for obj, name, value in ((star, "done_count", lambda: self.done), (star.subprocess, "run", self.fake_run)):
            p = mock.patch.object(obj, name, value)
            p.start()
            self.addCleanup(p.stop)
        self.settings = settings

    def use(self, path):
        """One machine's settings file (each machine reads its own copy)."""
        import herdr_task.settings as settings
        if hasattr(self, "_p"):
            self._p.stop()
        self._p = self.mock.patch.object(settings, "SETTINGS", path)
        self._p.start()
        self.addCleanup(self._p.stop)
        self.path = path

    def fake_run(self, argv, **kw):
        # only herdr-artifact publish, with the card's recipe
        self.assertEqual(argv[:2], ["herdr-artifact", "publish"], argv)
        with open(os.path.join(argv[2], "recipe.json")) as f:
            self.runs.append(json.load(f))
        return subprocess.CompletedProcess(argv, 0, json.dumps({"id": f"a{len(self.runs)}"}), "")

    def cmd(self, sub, **kw):
        import argparse
        args = argparse.Namespace(star=sub, json=True, folder=None, **kw)
        out = self.io.StringIO()
        with self.redirect(out):
            self.star.cmd_star(args)
        return json.loads(out.getvalue())

    def saved(self):
        with open(self.path) as f:
            return json.load(f)["everywhere"]["star_prompt"]

    def test_asks_once_ever_and_records_before_showing(self):
        self.assertTrue(self.cmd("check")["can"])
        seen = {}
        real_publish = self.star.publish

        def publish(folder):
            seen["asked_first"] = bool(self.saved().get("asked"))
            return real_publish(folder)
        with self.mock.patch.object(self.star, "publish", publish):
            r = self.cmd("ask")
        self.assertEqual(r, {"asked": True, "artifact": "a1"})
        self.assertTrue(seen["asked_first"], "the ask is kept before the card shows")
        self.assertEqual(len(self.runs), 1)
        # a restart, or another Assistant: never again, whatever the answer
        with self.assertRaises(self.star.Fail):
            self.cmd("ask")
        self.cmd("answer", answer="no")
        with self.assertRaises(self.star.Fail):
            self.cmd("ask")
        self.assertFalse(self.cmd("check")["can"])
        self.assertEqual(len(self.runs), 1)
        self.assertEqual(self.saved()["answer"], "no")

    def test_another_machine_never_asks_again(self):
        self.cmd("ask")
        # the settings file reaches the other machine (the web host's sync, the window's push): the same Everywhere
        with open(self.path) as f:
            synced = json.load(f)
        other = os.path.join(self.tmp, "b.json")
        with open(other, "w") as f:
            json.dump(synced, f)
        self.use(other)
        self.assertFalse(self.cmd("check")["can"])
        with self.assertRaises(self.star.Fail):
            self.cmd("ask")
        self.assertEqual(len(self.runs), 1)

    def test_a_pushed_copy_never_asks(self):
        with open(self.path, "w") as f:
            json.dump({"pushed_from": "herdr-app", "here": "vm-b", "everywhere": {}}, f)
        with self.assertRaises(self.star.Fail):
            self.cmd("ask")
        self.assertEqual(self.runs, [])

    def test_off_switch_and_too_early(self):
        with open(self.path, "w") as f:
            json.dump({"everywhere": {"star_prompt": {"off": True}}}, f)
        with self.assertRaises(self.star.Fail):
            self.cmd("ask")
        with open(self.path, "w") as f:
            json.dump({}, f)
        self.done = 2
        with self.assertRaises(self.star.Fail):
            self.cmd("ask")
        self.assertEqual(self.runs, [])

    def test_never_stars_for_the_person(self):
        """The card is a plain link to the repo; nothing calls GitHub (no gh, no API, no starring)."""
        self.cmd("ask")
        blocks = self.runs[0]["blocks"]
        items = next(b for b in blocks if b["type"] == "buttons")["items"]
        self.assertEqual(items[0]["href"], "https://github.com/eliasstravik/shepherdr")
        self.assertEqual([x["label"] for x in items], ["Open Shepherdr on GitHub", "No thanks"])
        with open(os.path.join(BIN, "herdr_task", "star.py")) as f:
            src = f.read()
        for word in ("api.github.com", "user/starred", '"gh"', "urllib", "PUT"):
            self.assertNotIn(word, src)

    def test_the_card_is_a_valid_recipe(self):
        """herdr-artifact's own check takes the card (buttons with an https href)."""
        import importlib.machinery
        import importlib.util
        path = os.path.join(ROOT, "mods", "artifacts", "bin", "herdr-artifact")
        loader = importlib.machinery.SourceFileLoader("herdr_artifact_mod", path)
        spec = importlib.util.spec_from_loader("herdr_artifact_mod", loader)
        art = importlib.util.module_from_spec(spec)
        loader.exec_module(art)
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "recipe.json"), "w") as f:
            json.dump(self.star.card(), f)
        self.assertEqual(art.recipe_problems(d, ["recipe.json"]), [])
        bad = {"recipe": 1, "blocks": [{"type": "buttons", "items": [{"label": "x", "href": "javascript:alert(1)"}]}]}
        with open(os.path.join(d, "recipe.json"), "w") as f:
            json.dump(bad, f)
        self.assertTrue(art.recipe_problems(d, ["recipe.json"]))


class Feedback(unittest.TestCase):
    """SHEP-78: an agent's report of a problem in Shepherdr itself returns at once and reaches its
    Assistant as a feedback news line; the Assistant offers once per problem, remembers the
    person's No, searches for a duplicate before filing and files a redacted issue. A throwaway
    store and settings file; hc and gh are faked, nothing is sent or filed. Made-up names only."""

    def setUp(self):
        import io
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.cli as cli
        import herdr_task.feedback as fb
        import herdr_task.launch as launch
        import herdr_task.settings as settings
        self.cli, self.fb, self.io, self.mock = cli, fb, io, mock
        self.queue = fb.queue_news  # the real one (faked below)
        self.dir = tempfile.mkdtemp()
        self.file = os.path.join(self.dir, "shepherdr.json")
        self.held, self.gh, self.issues = [], [], []
        self.caller = {"labels": {"herdr.task": "dev#7", "herdr.task.space": "sp1", "herdr.agent": "codex"}, "name": "shep-7-fix"}
        with open(os.path.join(self.dir, "dev.json"), "w") as f:
            json.dump({"prefix": "SHEP", "tasks": [{"id": 7, "title": "Fix login"}]}, f)

        def run(args, **_):
            self.gh.append(args)
            if args[1:3] == ["issue", "list"]:
                return subprocess.CompletedProcess(args, 0, json.dumps(self.issues), "")
            if "--body-file" in args:
                with open(args[args.index("--body-file") + 1]) as f:
                    self.body = f.read()
                return subprocess.CompletedProcess(args, 0, "https://github.com/acme/shep/issues/42\n", "")
            return subprocess.CompletedProcess(args, 1, "", "unexpected")

        for target, name, value in [
            (settings, "SETTINGS", self.file), (fb, "STATE", os.path.join(self.dir, ".feedback.json")), (fb, "STORE", self.dir),
            (fb, "me", lambda: ("t9", self.caller)), (fb, "run", run), (fb, "cli_version", lambda agent: "0.9.1"),
            (fb, "private_words", lambda: ["Jane Doe", "Jane", "janes-box"]), (fb, "shepherdr_version", lambda: "3.1.12"),
            (launch, "caller_space", lambda: (self.caller.get("labels") or {}).get("herdr.task.space") or (self.caller.get("labels") or {}).get("herdr.space")),
            (launch, "coordinator_in", lambda space: "t5" if space == "sp1" else None),
            (fb, "queue_news", lambda to, text: self.held.append((to, text, "news"))),
        ]:
            p = mock.patch.object(target, name, value)
            p.start()
            self.addCleanup(p.stop)

    def settings(self, d):
        with open(self.file, "w") as f:
            json.dump(d, f)

    def assistant(self):
        self.caller = {"labels": {"herdr.role": "coordinator", "herdr.space": "sp1", "herdr.agent": "claude"}, "name": "assistant"}

    def say(self, *argv):
        import contextlib
        out, err = self.io.StringIO(), self.io.StringIO()
        with self.mock.patch.object(sys, "argv", ["herdr-task", "feedback", *argv]), contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = self.cli.main()
        return code, out.getvalue() + err.getvalue()

    def test_a_report_returns_at_once_and_reaches_the_assistant(self):
        import time
        t = time.time()
        code, out = self.say("The Accept card shows an empty diff", "--details", "Goal: accept SHEP-7. Expected: its 3 files.")
        self.assertLess(time.time() - t, 2)
        self.assertEqual(code, 0)
        self.assertIn("Carry on", out)
        self.assertEqual(len(self.held), 1)
        to, text, how = self.held[0]
        self.assertEqual((to, how), ("t5", "news"))
        self.assertRegex(text, r'^<herdr-news task="SHEP-7" event="feedback" key="[0-9a-f]{10}">SHEP-7 feedback: The Accept card shows an empty diff · '
                               r'Goal: accept SHEP-7\. Expected: its 3 files\. \(Fix login\)</herdr-news>$')
        # the same problem again (other small words): nothing more is sent
        code, out = self.say("the accept card shows the empty diff")
        self.assertIn("already has this one", out)
        self.assertEqual(len(self.held), 1)

    def test_the_chat_reads_it_as_a_news_row(self):
        self.say("Status lines vanish after a restart")
        js = ("const m = await import(process.argv[1]); const t = process.argv[2];"
            "const r = [...t.matchAll(/<herdr-news\\b([^>]*)>([\\s\\S]*?)<\\/herdr-news\\s*>/g)].length;"
            "const w = (await import(process.argv[3])).newsWords({ ref: 'SHEP-7', event: 'feedback', text: t.replace(/<[^>]*>/g, '') }, { title: 'Fix login' });"
            "console.log(JSON.stringify([r, w]));")
        out = subprocess.run(["node", "--input-type=module", "-e", js, os.path.join(ROOT, "core", "chat-log.js"), self.held[0][1],
                              os.path.join(ROOT, "core", "chat-view.js")], capture_output=True, text=True)
        if out.returncode:
            self.skipTest(out.stderr[-300:])
        n, words = json.loads(out.stdout)
        self.assertEqual(n, 1)
        self.assertEqual(words, {"mark": "other", "what": "feedback", "text": "Status lines vanish after a restart"})

    def test_off_in_settings_sends_nothing(self):
        self.settings({"spaces": {"sp1": {"agents": {"feedback": False}}}})
        code, out = self.say("Status lines vanish after a restart")
        self.assertEqual((code, self.held), (0, []))
        self.assertIn("off in Settings", out)
        self.assistant()
        self.assertIn("off in Settings", self.say("Status lines vanish after a restart")[1])
        self.assertIn("off in Settings", self.say("Status lines vanish after a restart", "--file", "--new")[1])
        self.assertEqual(self.gh, [])
        # Everywhere off, the space on: on there
        self.settings({"everywhere": {"agents": {"feedback": False}}, "spaces": {"sp1": {"agents": {"feedback": True}}}})
        self.assertTrue(self.fb.feedback_on("sp1"))
        self.assertFalse(self.fb.feedback_on("sp2"))

    def test_offered_once_and_no_is_remembered(self):
        self.assistant()
        code, out = self.say("Ready refuses a reviewed commit")
        self.assertIn('"Leave feedback for Shepherdr about Ready refuses a reviewed commit?"', out)
        self.assertIn("Already offered", self.say("ready refuses the reviewed commit")[1])
        self.assertIn("Remembered", self.say("Ready refuses a reviewed commit", "--no")[1])
        self.assertIn("said no", self.say("Ready refuses a reviewed commit")[1])
        self.caller = {"labels": {"herdr.task": "dev#7", "herdr.task.space": "sp1"}}
        self.assertIn("said no", self.say("Ready refuses a reviewed commit")[1])
        self.assertEqual(self.held, [])

    def test_only_an_assistant_files(self):
        code, out = self.say("Ready refuses a reviewed commit", "--file")
        self.assertEqual(code, 1)
        self.assertIn("Only an Assistant", out)
        self.assertEqual(self.gh, [])

    def test_a_duplicate_is_offered_before_filing(self):
        self.assistant()
        self.issues = [{"number": 12, "title": "Ready refuses reviewed commits", "url": "https://github.com/acme/shep/issues/12"}]
        code, out = self.say("Ready refuses a reviewed commit", "--file", "--happened", "It said no round was recorded.")
        self.assertIn("#12 Ready refuses reviewed commits", out)
        self.assertIn("--comment", out)
        self.assertEqual([a[1:3] for a in self.gh], [["issue", "list"]])
        self.assertIn("eliasstravik/shepherdr", self.gh[0])  # the default repo
        self.assertEqual(self.gh[0][self.gh[0].index("--search") + 1], "ready refuses reviewed commit")
        code, out = self.say("Ready refuses a reviewed commit", "--file", "--comment", "12", "--happened", "Again on 3.1.12.")
        self.assertEqual(self.gh[-1][:4], ["gh", "issue", "comment", "12"])
        self.assertIn("Commented: https://github.com/acme/shep/issues/42", out)
        self.assertIn("Already filed", self.say("Ready refuses a reviewed commit")[1])

    def test_the_issue_is_redacted_and_complete(self):
        self.assistant()
        self.settings({"everywhere": {"agents": {"feedback_repo": "https://github.com/acme/shep.git"}}})
        code, out = self.say(
            "Tasks panel loses a task's status line", "--file", "--surface", "web",
            "--goal", "Watch SHEP-7 from my phone.",
            "--happened", "Jane Doe saw it blank on janes-box.example.ts.net (100.101.1.2); log at /home/jane/.herdr/tasks/dev.json, "
                          "token ghp_abcdefghijklmnopqrstuvwxyz012345, GH_TOKEN=s3cr3t, mail jane@example.com\n" + "\n".join(f"log line {i}" for i in range(60)),
            "--tried", "Reloaded the page.", "--expected", "Its last status line.", "--fix", "Keep it in the store.",
            "--repro", "1. Start a task\n2. Restart the service")
        self.assertEqual(code, 0, out)
        self.assertIn("Filed: https://github.com/acme/shep/issues/42", out)
        create = self.gh[-1]
        self.assertEqual(create[:5], ["gh", "issue", "create", "--repo", "acme/shep"])
        self.assertEqual(create[create.index("--title") + 1], "Tasks panel loses a task's status line")
        b = self.body
        for head in ("## Goal", "## What happened", "## What was tried", "## Expected", "## Suggested fix", "## Steps to reproduce", "## Environment"):
            self.assertIn(head, b)
        for words in ("- Shepherdr: 3.1.12", "- Agent CLI: Claude Code 0.9.1", "- Used from: the web app", "1. Start a task", "… (cut)"):
            self.assertIn(words, b)
        for secret in ("Jane", "janes-box", "100.101.1.2", "/home/jane", "ghp_", "s3cr3t", "jane@example.com", "log line 40"):
            self.assertNotIn(secret, b)
        self.assertIn("~/.herdr/tasks/dev.json", b)
        self.assertIn("GH_TOKEN=<redacted>", b)

    def test_the_search_never_carries_a_secret(self):
        """The duplicate search leaves the machine too: its words are redacted first."""
        self.assistant()
        self.say("Login fails with GH_TOKEN=supersecret123 for Jane", "--file")
        q = self.gh[0][self.gh[0].index("--search") + 1]
        self.assertEqual(q, "login fails token")
        for w in ("supersecret123", "jane", "redacted", "name"):
            self.assertNotIn(w, q.lower())

    def test_filing_holds_no_lock_while_gh_runs(self):
        """A worker's report while the Assistant's gh call is slow: it still returns at once."""
        import fcntl
        self.assistant()
        locked = []

        def gh(args, **_):
            with open(self.fb.STATE + ".lock", "a") as lock:
                try:
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    locked.append(False)
                    fcntl.flock(lock, fcntl.LOCK_UN)
                except OSError:
                    locked.append(True)
            return subprocess.CompletedProcess(args, 0, "[]" if args[1:3] == ["issue", "list"] else "https://github.com/acme/shep/issues/9\n", "")
        with self.mock.patch.object(self.fb, "run", gh):
            code, out = self.say("Status lines vanish after a restart", "--file")
        self.assertIn("Filed: https://github.com/acme/shep/issues/9", out)
        self.assertTrue(locked and not any(locked), locked)  # every search and the create ran with the lock free
        self.assertIn("Already filed: https://github.com/acme/shep/issues/9", self.say("Status lines vanish after a restart")[1])

    def test_never_filed_twice(self):
        """Filed (or a comment added): another --file of the same problem files nothing; one under way
        holds a second off; one that found duplicates or failed leaves it as it was."""
        self.assistant()
        self.issues = [{"number": 12, "title": "Status lines vanish", "url": "https://github.com/acme/shep/issues/12"}]
        self.say("Status lines vanish after a restart", "--file")  # duplicates: nothing filed, nothing held
        self.assertNotIn("filing", open(self.fb.STATE).read())
        self.say("Status lines vanish after a restart", "--file", "--comment", "12")
        n = len(self.gh)
        for again in (["--new"], ["--comment", "12"]):
            code, out = self.say("Status lines vanish after a restart", "--file", *again)
            self.assertIn("Already filed", out)
        self.assertEqual(len(self.gh), n)
        with self.fb.problems() as ps:
            ps[self.fb.key_of("Ready hangs")] = {"line": "Ready hangs", "state": "filing", "at": self.fb.now()}
        self.assertIn("being filed right now", self.say("Ready hangs", "--file", "--new")[1])
        # a report or an offer while it's filed leaves it marked: a second --file still files nothing
        self.assertIn("filing this one right now", self.say("Ready hangs")[1])
        self.caller = {"labels": {"herdr.task": "dev#7", "herdr.task.space": "sp1"}}
        self.assertIn("filing this one right now", self.say("Ready hangs")[1])
        self.assistant()
        self.assertIn("being filed right now", self.say("Ready hangs", "--file", "--comment", "12")[1])
        self.assertEqual((len(self.gh), self.held), (n, []))

    def test_a_report_queues_in_the_background(self):
        """The news line goes to a detached herdr-task (_hold-news): a report never waits on the held messages' lock."""
        calls = []
        with self.mock.patch.object(self.fb.subprocess, "Popen", lambda args, **kw: calls.append((args, kw))):
            self.queue("t5", "<herdr-news …>")
        (args, kw), = calls
        self.assertEqual(args[-3:], ["_hold-news", "t5", "<herdr-news …>"])
        self.assertTrue(kw["start_new_session"])

    def test_a_workers_details_and_cli_go_in_the_issue(self):
        self.say("Status lines vanish after a restart", "--details", "Restarted the service; every status line went blank.")
        self.assistant()
        self.say("Status lines vanish after a restart", "--file", "--new", "--dry-run")
        self.assertEqual(self.gh, [])
        code, out = self.say("Status lines vanish after a restart", "--file", "--new")
        self.assertIn("every status line went blank", self.body)
        self.assertIn("- Agent CLI: Codex 0.9.1", self.body)

    def test_autopilot_notes_it_for_later(self):
        self.assistant()
        self.assertIn("Noted", self.say("Status lines vanish after a restart", "--later")[1])
        self.assertIn("later  Status lines vanish after a restart", self.say("--list")[1])

    def test_every_role_is_told(self):
        from herdr_task import templates, reactions, autopilot
        with open(os.path.join(ROOT, "templates", "assistant.md")) as f:
            a = f.read()
        for words in ("## Feedback for Shepherdr", "Leave feedback for Shepherdr about", "--no", "--later", "duplicate", "--comment", "in the middle of something urgent"):
            self.assertIn(words, a)
        with open(os.path.join(ROOT, "templates", "worker.md")) as f:
            self.assertIn(templates.FEEDBACK_RULE.strip(), f.read())
        self.assertIn(templates.FEEDBACK_RULE, templates.WORKER_MD)
        self.assertIn(templates.WORKER_MD_BEFORE_FEEDBACK, templates.OLD_WORKERS)
        with open(os.path.join(ROOT, "templates", "reviewer.md")) as f:
            self.assertIn(templates.REVIEWER_FEEDBACK.strip(), f.read())
        self.assertIn("herdr-task feedback", self.fb.AGENT_NOTE)
        with open(os.path.join(BIN, "herdr_task", "autopilot.py")) as f:
            self.assertIn("herdr-task feedback", f.read())
        self.assertIn("feedback", __import__("herdr_task.launch", fromlist=["OWN"]).OWN)


class CodexAsksOnTheCard(unittest.TestCase):
    """SHEP-91: a Codex worker's PermissionRequest hook (`herdr-task _allow`) allows its own commands, and hands anything
    else to the chat's card (ask-hook.sh, marked HERDR_ASK_CHAINED), waiting as long as the card does."""

    def run_allow(self, command, hook):
        app = tempfile.mkdtemp()
        if hook is not None:
            os.makedirs(os.path.join(app, "mods", "chat"))
            with open(os.path.join(app, "mods", "chat", "ask-hook.sh"), "w") as f:
                f.write(hook)
        code = ("import sys, types, argparse; sys.dont_write_bytecode = True; sys.path.insert(0, sys.argv[1]);"
                "from herdr_task.launch import cmd_allow; cmd_allow(argparse.Namespace(command='/x/herdr-task'))")
        req = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}, "turn_id": "t1"})
        env = {**os.environ, "HERDR_APP_HOME": app}
        env.pop("HERDR_ASK_CHAINED", None)
        return subprocess.run([sys.executable, "-c", code, BIN], input=req, capture_output=True, text=True, env=env)

    def test_its_own_command_never_asks(self):
        out = self.run_allow("/x/herdr-task status SHEP-1 hi", 'echo "should not run"')
        self.assertEqual(json.loads(out.stdout)["hookSpecificOutput"]["decision"], {"behavior": "allow"}, out.stderr)

    def test_anything_else_goes_to_the_card(self):
        card = ('[ "$HERDR_ASK_CHAINED" = 1 ] || exit 0; grep -q "rm -rf build" || exit 0; '
                'echo \'{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"no"}}}\'')
        out = self.run_allow("rm -rf build", card)
        self.assertEqual(json.loads(out.stdout)["hookSpecificOutput"]["decision"], {"behavior": "deny", "message": "no"}, out.stderr)
        # no card answer (answered in the terminal, or no chat hook installed): no decision, Codex asks itself
        self.assertEqual(self.run_allow("rm -rf build", "exit 0").stdout, "")
        self.assertEqual(self.run_allow("rm -rf build", None).stdout, "")

    def test_codex_waits_as_long_as_the_card(self):
        sys.path.insert(0, BIN)
        from herdr_task import launch
        argv = launch.own_argv(["codex", "--sandbox", "workspace-write"])
        self.assertIn(f"timeout={launch.ASK_TIMEOUT}", argv[2])
        self.assertEqual(launch.ASK_TIMEOUT, 1800)


class Asks(unittest.TestCase):
    """FAC-9: herdr-task ask keeps the ask and starts an ask pane that reports a question over OSC
    7501; its answer goes into the record and reaches the asker as held news. A throwaway store;
    the end-to-end test runs a throwaway herdr-core, never the person's live session."""

    def setUp(self):
        self.store = tempfile.mkdtemp()
        # No hc reaches a core here: a fake one that refuses everything, and no terminal of ours.
        fake = os.path.join(self.store, "bin")
        os.makedirs(fake)
        with open(os.path.join(fake, "hc"), "w") as f:
            f.write("#!/bin/sh\nexit 1\n")
        os.chmod(os.path.join(fake, "hc"), 0o755)
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("HC_")}
        self.env.update(HERDR_TASK_STORE=self.store, PATH=f"{fake}:/usr/bin:/bin")
        self.entry = os.path.join(BIN, "herdr-task")

    def record(self, ask_id):
        with open(os.path.join(self.store, "asks", f"{ask_id}.json")) as f:
            return json.load(f)

    def held(self):
        with open(os.path.join(self.store, ".held.json")) as f:
            return json.load(f)

    def test_pane_keeps_the_answer_and_holds_news(self):
        os.makedirs(os.path.join(self.store, "asks"))
        with open(os.path.join(self.store, "asks", "ab12cd.json"), "w") as f:
            json.dump({"id": "ab12cd", "question": "Ship <it> tonight?", "options": ["Yes", "No"], "recommend": "Yes",
                       "task": "FAC-3", "to": "t5", "at": 1}, f)
        p = subprocess.run([sys.executable, self.entry, "_ask-pane", "ab12cd"], input="\n2\n", capture_output=True, text=True, env=self.env)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("❯ 1. Yes  (recommended)\n  2. No\nAnswer with a number or your own words", p.stdout)
        self.assertIn("\x1b]7501;state=blocked:kind=question:msg=U2hpcCA8aXQ+IHRvbmlnaHQ/\x07", p.stdout)
        self.assertEqual(self.record("ab12cd")["answer"], "No")
        [msg] = self.held()["t5"]
        self.assertEqual(msg["how"], "news")
        self.assertEqual(msg["text"], '<herdr-news task="FAC-3" event="answer" key="ab12cd">'
                                      'Ask ab12cd answered: No (asked: Ship ‹it› tonight?)</herdr-news>')
        # Words of one's own are an answer too (a digit hc can't count is words); no hc on PATH: held all the same.
        p = subprocess.run([sys.executable, self.entry, "_ask-pane", "ab12cd"], input="² maybe later\n", capture_output=True, text=True,
                           env={**self.env, "PATH": "/usr/bin:/bin", "HC_TERMINAL_SOCKET": "/nonexistent.sock"})
        self.assertEqual(self.record("ab12cd")["answer"], "² maybe later", p.stderr)
        self.assertEqual(len(self.held()["t5"]), 2)
        # A pane whose HC_SESSION isn't its socket's session (the guard's case) sends nothing: held.
        log = os.path.join(self.store, "hc.log")
        with open(os.path.join(self.store, "bin", "hc"), "w") as f:
            f.write(f'#!/bin/sh\necho "$@" >> {log}\nexit 1\n')
        p = subprocess.run([sys.executable, self.entry, "_ask-pane", "ab12cd"], input="1\n", capture_output=True, text=True,
                           env={**self.env, "HC_SESSION": "other", "HC_TERMINAL_SOCKET": "/nonexistent/default/default.sock"})
        self.assertEqual((p.returncode, os.path.exists(log), len(self.held()["t5"])), (0, False, 3), p.stderr)
        # The ask saves the pane's terminal after the pane read the record: answering keeps it.
        pane = subprocess.Popen([sys.executable, self.entry, "_ask-pane", "ab12cd"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, env=self.env)
        pane.stdout.readline()  # the question: the record is read
        rec = {**self.record("ab12cd"), "terminal": "t9"}
        with open(os.path.join(self.store, "asks", "ab12cd.json"), "w") as f:
            json.dump(rec, f)
        pane.communicate("1\n")
        self.assertEqual((self.record("ab12cd")["answer"], self.record("ab12cd")["terminal"]), ("Yes", "t9"))
        p = subprocess.run([sys.executable, self.entry, "ask", "--show", "ab12cd", "--json"], capture_output=True, text=True, env=self.env)
        self.assertEqual(json.loads(p.stdout)["answer"], "Yes", p.stderr)

    def test_recommend_must_be_an_option(self):
        p = subprocess.run([sys.executable, self.entry, "ask", "Ship?", "--option", "Yes", "--option", "No", "--recommend", "Maybe"],
                           capture_output=True, text=True, env={**self.env, "HC_TERMINAL_ID": "t5"})
        self.assertEqual(p.returncode, 1)
        self.assertIn("--recommend must be one of the options: Yes, No", p.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.store, "asks")))
        for opts in (["--option", "  ", "--option", "No", "--recommend", "No"], ["--option", "Yes", "--recommend", "Yes"],
                     ["--option", "Yes", "--option", "No"], ["--option", "Yes", "--option", " Yes ", "--recommend", "Yes"]):
            p = subprocess.run([sys.executable, self.entry, "ask", "Ship?", *opts], capture_output=True, text=True, env={**self.env, "HC_TERMINAL_ID": "t5"})
            self.assertEqual(p.returncode, 1, opts)
        # hc refusing the pane leaves no ask behind.
        p = subprocess.run([sys.executable, self.entry, "ask", "Ship?", "--option", " Yes ", "--option", "No", "--recommend", " Yes"],
                           capture_output=True, text=True, env={**self.env, "HC_TERMINAL_ID": "t5"})
        self.assertIn("couldn't start the ask pane: hc failed", p.stderr)
        self.assertEqual(os.listdir(os.path.join(self.store, "asks")), [])
        # Outside a terminal there's nobody to tell the answer: refused.
        p = subprocess.run([sys.executable, self.entry, "ask", "Ship?", "--option", "Yes", "--option", "No", "--recommend", "Yes"],
                           capture_output=True, text=True, env=self.env)
        self.assertIn("Run it from an agent's own terminal", p.stderr)

    @unittest.skipUnless(shutil.which("hc") and not CI, "needs herdr-core (hc) on this machine")
    def test_ask_pane_end_to_end(self):
        import pwd
        import time
        session = f"fac9-ask-{os.getpid()}"
        base = {"HOME": pwd.getpwuid(os.getuid()).pw_dir, "PATH": f"/usr/bin:/bin:{os.path.dirname(shutil.which('hc'))}"}
        throwaway = {**base, "HC_SESSION": session}

        def hc(*a):
            return subprocess.run(["hc", *a], capture_output=True, text=True, env=throwaway)

        def get(t):
            return json.loads(hc("get", t, "--json").stdout or "{}")

        def until(test, what):
            for _ in range(200):
                if test():
                    return
                time.sleep(0.1)
            self.fail(what)

        state = []  # the throwaway core's folder, once known: removed after it stops

        def stop():
            hc("server", "stop")
            for d in state:
                shutil.rmtree(d, True)

        self.addCleanup(stop)
        # The asker (the Assistant) is a terminal of the throwaway core; its socket is how
        # herdr-task inside it reaches that core, with HC_SESSION unset (base.not_real allows it).
        sock_file = os.path.join(self.store, "sock")
        asker = json.loads(hc("run", "--bg", "--json", "--name", "asker", "--", "sh", "-c",
                              f'echo "$HC_TERMINAL_SOCKET" > {sock_file}; exec sleep 120').stdout)["address"]
        until(lambda: os.path.getsize(sock_file) if os.path.exists(sock_file) else 0, "the asker didn't start")
        with open(sock_file) as f:
            sock = f.read().strip()
        if os.path.basename(os.path.dirname(sock)) == session:
            state.append(os.path.dirname(sock))
        inside = {**base, "HC_SOCKET": sock, "HC_TERMINAL_SOCKET": sock, "HC_TERMINAL_ID": asker, "HERDR_TASK_STORE": self.store}
        ls = json.loads(subprocess.run(["hc", "ls", "--json"], capture_output=True, text=True, env=inside).stdout)
        self.assertEqual([k["name"] for k in ls["terminals"]], ["asker"], "not the throwaway core: stop")

        p = subprocess.run([sys.executable, self.entry, "ask", "Ship it tonight?", "--option", "Wait", "--option", "Ship",
                            "--recommend", "Ship", "--task", "FAC-9", "--json"], capture_output=True, text=True, env=inside)
        self.assertEqual(p.returncode, 0, p.stderr)
        out = json.loads(p.stdout)
        t = out["terminal"]
        until(lambda: (get(t).get("status") or {}).get("state") == "blocked", f"the pane didn't report blocked: {get(t)}")
        self.assertEqual(get(t)["status"], {"kind": "question", "msg": "Ship it tonight?", "state": "blocked"})
        self.assertEqual(sheepdog_parse(hc("read", t).stdout), (["Wait", "Ship  (recommended)"], 1))
        self.assertEqual(hc("keys", t, "2").returncode, 0)  # a tap, as sheepdog sends it: no Enter
        until(lambda: get(t).get("state") == "finished", f"the pane didn't exit: {get(t)}")
        self.assertEqual(get(t)["exit_code"], 0)
        rec = self.record(out["id"])
        self.assertEqual((rec["answer"], rec["to"], rec["terminal"]), ("Ship", asker, t))
        # The news for the asker: held, as herdr-task's guard refuses hc in a throwaway core's terminals
        # (they carry its HC_SESSION); on the person's own core it is sent at once (news.send_or_hold).
        [msg] = self.held()[asker]
        self.assertEqual(msg["text"], f'<herdr-news task="FAC-9" event="answer" key="{out["id"]}">'
                                      f'Ask {out["id"]} answered: Ship (asked: Ship it tonight?)</herdr-news>')


class HandoffsAtTheFloor(unittest.TestCase):
    """FAC-23: when a usage pool reaches its floor, each running task whose worker is on it gets a
    handoff from git and its chat, saved under the store. A throwaway git repo and store; hc never runs."""

    def setUp(self):
        import shutil
        sys.path.insert(0, BIN)
        import herdr_task.handoff as handoff
        self.handoff = handoff
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.store_dir = os.path.join(self.root, "store")
        main, self.wt = os.path.join(self.root, "repo"), os.path.join(self.root, "wt")
        git = lambda cwd, *a: subprocess.run(["git", "-C", cwd, *a], capture_output=True, text=True, check=True).stdout.strip()
        git(self.root, "init", "-q", "-b", "main", main)
        for k, v in (("user.name", "T"), ("user.email", "t@example.com"), ("commit.gpgsign", "false")):
            git(main, "config", k, v)
        git(main, "commit", "-q", "--allow-empty", "-m", "base")
        git(main, "worktree", "add", "-q", "-b", "task/1", self.wt, "HEAD")
        start = git(self.wt, "rev-parse", "HEAD")
        git(self.wt, "commit", "-q", "--allow-empty", "-m", "Add the pool picker")
        with open(os.path.join(self.wt, "half-done.py"), "w") as f:
            f.write("x = 1\n")
        log = os.path.join(self.root, "chat.jsonl")
        with open(log, "w") as f:
            for d in ({"type": "user", "message": {"content": "the brief"}},
                      {"type": "user", "message": {"content": "Use the soonest weekly reset"}},
                      {"type": "assistant", "message": {"content": [{"type": "text", "text": "Picker half done"}]}}):
                f.write(json.dumps(d) + "\n")
        worker = lambda i, argv, **kw: dict({"id": i, "title": f"T{i}", "state": "running", "mode": "worktree", "worktree": self.wt,
                                             "branch": "task/1", "base": "main", "start": start,
                                             "agents": [{"address": f"a{i}", "agent": argv[0], "argv": argv, "log": log}]}, **kw)
        self.store = Store([worker(1, ["claude", "--model", "opus"]), worker(2, ["codex", "--no-daemon", "-m", "gpt-5.5"]),
                            worker(3, ["agy", "--model", "claude-x", "--model", "gemini-4-argon-high"]), worker(4, ["agy", "--model", "claude-sonnet-5-5-medium"]),
                            worker(5, ["claude"], state="ready"), worker(6, ["agy"])])
        self.store.loc["folder"], self.store.path = main, os.path.join(self.store_dir, "f.json")

    def test_only_the_pools_running_tasks_get_one(self):
        got = {p: [h["task"] for h in self.handoff.handoffs(self.store, p, self.store.data["tasks"])]
               for p in ("claude", "codex", "antigravity-gemini", "antigravity-claude-gpt")}
        self.assertEqual(got, {"claude": ["SHEP-1"], "codex": ["SHEP-2"], "antigravity-gemini": ["SHEP-3"],
                               "antigravity-claude-gpt": ["SHEP-4"]})

    def test_the_handoff_has_the_work_and_chat_and_is_saved_per_task(self):
        [h] = self.handoff.handoffs(self.store, "claude", self.store.data["tasks"])
        for words in ("Add the pool picker", "half-done.py", "person: Use the soonest weekly reset",
                      "agent: Picker half done", "the claude pool reached its floor", self.wt):
            self.assertIn(words, h["text"])
        self.assertNotIn("the brief", h["text"])
        self.assertEqual(h["path"], os.path.join(self.store_dir, "handoffs", "f-1.md"))
        with open(h["path"]) as f:
            self.assertEqual(f.read(), h["text"] + "\n")

    def test_agy_on_its_own_default_has_no_pool_and_no_pool_gets_none(self):
        self.assertIsNone(self.handoff.pool_of({"agent": "agy", "argv": ["agy"]}))
        self.assertEqual(self.handoff.handoffs(self.store, None, self.store.data["tasks"]), [])
        self.assertFalse(os.path.exists(os.path.join(self.store_dir, "handoffs")))


class Checks(unittest.TestCase):
    """FAC-14: the gate runs tasks.check itself at a Ready task's exact head, in a throwaway worktree.
    A real throwaway git repo; hc is a fake that really runs the command, gh is not reached."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        sys.path.insert(0, os.path.join(ROOT, "test"))
        import herdr_task.checks as checks
        self.c, self.mock = checks, mock
        self.repo = tempfile.mkdtemp()
        self.git("init", "-q")
        self.commit("one")
        self.terms, self.cwds = {}, []
        self.cfg, self.min, self.clock = "echo ran; test -f f.txt", 1, [1000]

        def hc(args, **kw):
            out, code = "", 0
            if args[:2] == ["hc", "run"]:
                cwd = args[args.index("--cwd") + 1]
                self.cwds.append(cwd)
                p = subprocess.run(args[args.index("--") + 1:], cwd=cwd, capture_output=True, text=True)
                self.terms[f"t{len(self.terms) + 1}"] = {"state": "finished", "exit_code": p.returncode, "out": p.stdout,
                                                         "labels": [a for a in args if a.startswith("herdr.")]}
                out = json.dumps({"address": f"t{len(self.terms)}"})
            elif args[:2] == ["hc", "read"]:
                out = self.terms[args[2]]["out"]
            elif args[:2] == ["hc", "kill"]:
                self.terms[args[2]]["state"] = "finished"
            elif args[:2] == ["hc", "rm"]:
                self.terms.pop(args[2], None)
            return subprocess.CompletedProcess(args, code, out, "")
        for name, value in (("run", hc), ("hc_json", lambda *a: dict(self.terms[a[1]]) if a[1] in self.terms else None),
                            ("settings_for", lambda s, sp=None: {"tasks": {"check": self.cfg}}),
                            ("gate_min", lambda s, t, sp=None: self.min)):
            p = mock.patch.object(checks, name, value)
            p.start()
            self.addCleanup(p.stop)
        p = mock.patch.object(checks, "now", lambda: self.clock[0])
        p.start()
        self.addCleanup(p.stop)
        self.s = type("S", (), {"dirty": False, "key": lambda self, t: "dev#14", "loc": {"folder": self.repo}})()
        self.s.repo = self.repo
        self.t = {"id": 14, "state": "ready", "worktree": self.repo}

    def git(self, *a):
        return subprocess.run(["git", "-C", self.repo, "-c", "user.name=t", "-c", "user.email=t@t", *a],
                              capture_output=True, text=True, check=True).stdout.strip()

    def commit(self, name):
        with open(os.path.join(self.repo, "f.txt"), "w") as f:
            f.write(name)
        self.git("add", "."), self.git("commit", "-qm", name)
        return self.git("rev-parse", "HEAD")

    def tick(self):
        self.c.checks_pass(self.s, self.t)

    def test_a_ready_task_gets_one_run_at_its_head_and_a_valid_record(self):
        from contract_gate_evidence import validate_check
        head = self.git("rev-parse", "HEAD")
        self.tick()
        self.assertEqual(len(self.terms), 1)
        self.assertNotEqual(self.cwds[0], self.repo)
        self.assertFalse(os.path.exists(os.path.join(self.repo, "ran")))
        self.assertIn("herdr.role=checks", self.terms["t1"]["labels"])
        self.assertIn("herdr.checks=dev#14", self.terms["t1"]["labels"])
        self.assertNotIn("herdr.task=dev#14", self.terms["t1"]["labels"])
        self.assertIn("running", self.c.checks_line(self.t))
        self.tick()  # finished: recorded, cleaned up
        rec = self.t["checks"][0]
        validate_check(rec)
        self.assertEqual((rec["sha"], rec["exit"], rec["output_tail"].strip()), (head, 0, "ran"))
        self.assertFalse(os.path.exists(self.cwds[0]))
        self.assertEqual(self.terms, {})
        self.tick()  # same head, has a record: no new run
        self.assertEqual(len(self.t["checks"]), 1)
        self.assertEqual(len(self.cwds), 1)
        self.assertIn("passed", self.c.checks_line(self.t))

    def test_a_failing_command_gives_a_valid_record(self):
        from contract_gate_evidence import validate_check
        self.cfg = "echo boom; exit 3"
        self.tick(), self.tick()
        validate_check(self.t["checks"][0])
        self.assertEqual(self.t["checks"][0]["exit"], 3)
        self.assertIn("failed (exit 3)", self.c.checks_line(self.t))

    def test_a_moved_head_gets_a_new_run(self):
        self.tick(), self.tick()
        new = self.commit("two")
        self.tick(), self.tick()
        self.assertEqual([r["sha"] for r in self.t["checks"]][1], new)
        self.assertEqual(len(self.cwds), 2)

    def test_not_a_gate_folder_not_ready_or_no_command_get_none(self):
        self.min = 0
        self.tick()
        self.min = 1
        self.t["state"] = "running"
        self.tick()
        self.t["state"], self.cfg = "ready", ""
        self.tick()
        self.assertEqual(self.terms, {})

    def test_a_check_past_the_limit_is_killed_and_recorded_failed(self):
        from contract_gate_evidence import validate_check
        self.tick()
        self.terms["t1"]["state"] = "live"
        self.clock[0] += self.c.LIMIT_MS + 1
        self.tick()  # killed; recorded once it has exited
        self.assertFalse(self.t.get("checks"))
        self.tick()
        self.assertEqual(self.terms, {})
        rec = self.t["checks"][0]
        validate_check(rec)
        self.assertNotEqual(rec["exit"], 0)
        self.assertTrue(rec["output_tail"].endswith("Killed: ran longer than 20 minutes."))

    def test_a_run_whose_head_moved_or_task_left_ready_is_dropped_without_a_record(self):
        self.tick()
        self.terms["t1"]["state"] = "live"
        tmp = self.cwds[0]
        self.commit("two")
        self.tick(), self.tick()  # kill, then drop
        self.assertEqual((self.t.get("check_run"), self.t.get("checks"), self.terms), (None, None, {}))
        self.assertFalse(os.path.exists(tmp))
        self.tick()  # the new head gets its run
        self.assertEqual(len(self.cwds), 2)

    def test_a_pending_ci_state_is_refreshed(self):
        self.t["pr"] = {"repo": "o/r", "url": "https://x/1"}
        states = iter(["pending", "none", "success"])
        self.mock.patch.object(self.c, "ci_of", lambda t, sha: {"state": next(states), "url": "https://x/1"}).start()
        self.addCleanup(self.mock.patch.stopall)
        self.tick(), self.tick()
        self.assertEqual(self.t["checks"][0]["ci"]["state"], "pending")
        self.tick()
        self.assertEqual(self.t["checks"][0]["ci"]["state"], "none")  # GitHub may not have registered runs yet
        self.tick()
        self.assertEqual(self.t["checks"][0]["ci"]["state"], "success")

    def test_an_hc_that_does_not_answer_never_makes_a_failed_record(self):
        self.tick()
        self.terms["t1"]["state"] = "live"
        real = self.terms
        self.c.hc_json = lambda *a: None
        self.tick()
        self.assertEqual((self.t.get("checks"), bool(self.t.get("check_run")), os.path.exists(self.cwds[0])), (None, True, True))
        self.clock[0] += self.c.LIMIT_MS + 1  # past the limit but hc still silent: waits, never a failed record or a rerun
        self.tick()
        self.assertEqual((self.t.get("checks"), bool(self.t.get("check_run"))), (None, True))
        self.clock[0] += self.c.LIMIT_MS  # gone for good: dropped, no record, run again
        self.tick()
        self.assertEqual((self.t.get("checks"), self.t.get("check_run")), (None, None))
        self.assertFalse(os.path.exists(self.cwds[0]))

    def test_a_run_killed_as_stale_is_never_recorded_even_if_ready_again(self):
        self.tick()
        self.terms["t1"]["state"] = "live"
        self.t["state"] = "running"
        self.tick()  # killed
        self.t["state"] = "ready"
        self.tick()  # reaped: dropped, then run again at the same head
        self.assertIsNone(self.t.get("checks"))
        self.tick()
        self.assertEqual(len(self.cwds), 2)


def sheepdog_parse(screen):
    """A copy of sheepdog's ParsePrompt for numbered menus (herdr/prompt.go findOptions/parseOptions at cf9c7337):
    options numbered from 1, exactly one with a cursor marker. Returns (labels, cursor index) or None."""
    import re
    rx = re.compile(r"^\s*([›❯>])?\s*(\d+)\.\s+(.+?)\s*$")
    lines = screen.split("\n")[-40:]
    for i in range(len(lines) - 1, -1, -1):
        m = rx.match(lines[i])
        if not m or m.group(2) != "1":
            continue
        labels, cursor, cursors = [], -1, 0
        for line in lines[i:]:
            m = rx.match(line)
            if m and m.group(2) == str(len(labels) + 1):
                if m.group(1):
                    cursor, cursors = len(labels), cursors + 1
                labels.append(m.group(3))
            else:
                break  # the pane's own text under the options ends the block
        if len(labels) >= 2 and cursors == 1:
            return labels, cursor
    return None


class AskPane(unittest.TestCase):
    """FAC-28: the ask pane is a question card in sheepdog (cursor marker) and a tapped digit answers it."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="ask-pane-")
        self.addCleanup(shutil.rmtree, self.tmp, True)
        os.makedirs(os.path.join(self.tmp, "asks"))
        self.env = {**os.environ, "HERDR_TASK_STORE": self.tmp}

    def pane(self, feed):
        """Runs the real pane on a pseudo-terminal, sends `feed` once its prompt is up; returns (screen, record)."""
        import pty, select, time
        with open(os.path.join(self.tmp, "asks", "ab1.json"), "w") as f:  # fresh each run: no answer left over
            json.dump({"id": "ab1", "question": "Ship it?", "options": ["Yes, ship", "Wait for review", "Drop it"],
                       "recommend": "Wait for review"}, f)
        pid, fd = pty.fork()
        if pid == 0:
            os.execve(sys.executable, [sys.executable, os.path.join(BIN, "herdr-task"), "_ask-pane", "ab1"], self.env)
        out, sent, end = b"", False, time.time() + 15
        while time.time() < end:
            if select.select([fd], [], [], 0.2)[0]:
                try:
                    chunk = os.read(fd, 4096)
                except OSError:
                    break
                if not chunk:
                    break
                out += chunk
            if not sent and b"> " in out.split(b"\x07")[-1]:
                for part in feed if isinstance(feed, tuple) else (feed,):
                    time.sleep(part) if isinstance(part, float) else os.write(fd, part)
                sent = True
        try:
            for _ in range(20):  # the pane ends by itself once answered
                done, status = os.waitpid(pid, os.WNOHANG)
                if done:
                    break
                time.sleep(0.1)
            else:
                os.kill(pid, 9)  # a pane that never answered (the old one) must not hang the test
                os.waitpid(pid, 0)
                self.fail("the pane did not end")
        finally:
            os.close(fd)
        self.assertEqual(status, 0, "the pane crashed")
        with open(os.path.join(self.tmp, "asks", "ab1.json")) as f:
            return out.decode(errors="replace"), json.load(f)

    def test_the_screen_parses_as_a_question_card_on_the_recommended_option(self):
        screen, _ = self.pane(b"1")
        self.assertEqual(sheepdog_parse(screen.replace("\r", "")),
                         (["Yes, ship", "Wait for review  (recommended)", "Drop it"], 1))

    def test_a_tapped_digit_answers_with_or_without_enter(self):
        for keys in (b"3", b"3\r"):
            self.assertEqual(self.pane(keys)[1]["answer"], "Drop it", keys)

    def test_typed_words_still_answer(self):
        self.assertEqual(self.pane(b"2 weeks, then ship\r")[1]["answer"], "2 weeks, then ship")

    def test_input_typed_ahead_and_a_lone_escape_are_not_lost(self):
        self.assertEqual(self.pane(b"\r2\r")[1]["answer"], "Wait for review")
        self.assertEqual(self.pane(b"\r2")[1]["answer"], "Wait for review")
        import time
        self.assertEqual(self.pane((b"\x1b", 0.2, b"1"))[1]["answer"], "Yes, ship")

    def test_typing_edits_like_a_terminal(self):
        for keys, want in ((b"\xc2\xb2 maybe\r", "\xb2 maybe"), ("\u6539\u5929\r".encode(), "\u6539\u5929"),
                           (b"n\x7f1\r", "Yes, ship"), (b"\x7f2\r", "Wait for review"),
                           (b"\x1b[B3\r", "Drop it"), (b"\x1b[B\x1b[A1", "Yes, ship")):
            self.assertEqual(self.pane(keys)[1]["answer"], want, keys)

    def test_a_recommended_no_needs_a_yes_to_be_tappable(self):
        import argparse
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.ask as ask
        def args(opts, rec):
            return argparse.Namespace(show=None, question="Ship?", option=opts, recommend=rec, task=None)
        # Never a real pane or the real store: hc and the asks folder are replaced, whatever the code does.
        with mock.patch.object(ask, "run", side_effect=AssertionError("started hc")) as run, \
                mock.patch.object(ask, "ASKS", os.path.join(self.tmp, "asks")), \
                mock.patch.dict(os.environ, {"HC_TERMINAL_ID": ""}):  # empty: stops at the terminal check
            with self.assertRaisesRegex(ask.Fail, "starts with"):
                ask.cmd_ask(args(["Wait for review", "Ship now"], "Wait for review"))
            with self.assertRaisesRegex(ask.Fail, "own terminal"):
                ask.cmd_ask(args(["Wait for review", "Yes, ship"], "Wait for review"))
            with self.assertRaisesRegex(ask.Fail, "own terminal"):  # "Do it" is no negative, "Do not" is
                ask.cmd_ask(args(["Do it now", "Hold"], "Do it now"))
            with self.assertRaisesRegex(ask.Fail, "starts with"):
                ask.cmd_ask(args(["Do not ship", "Ship"], "Do not ship"))
            with self.assertRaisesRegex(ask.Fail, "short word in parentheses"):
                ask.cmd_ask(args(["Switch (v2)", "Ship"], "Ship"))
            run.assert_not_called()


class Resume(unittest.TestCase):
    """FAC-61: herdr-task resume rebuilds argv from current settings and sends a continue line."""

    def setUp(self):
        import argparse
        import contextlib
        import io
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.base as base
        import herdr_task.commands as commands
        import herdr_task.launch as launch
        import herdr_task.settings as settings
        import herdr_task.store as store
        import herdr_task.templates as templates
        self.argparse, self.io, self.redirect = argparse, io, contextlib.redirect_stdout
        self.commands, self.launch, self.settings, self.store_mod = commands, launch, settings, store
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="resume-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.repo = os.path.join(self.tmp, "repo")
        os.makedirs(self.repo)
        subprocess.run(["git", "-C", self.repo, "init", "-q", "-b", "main"], check=True)
        for k, v in (("user.name", "T"), ("user.email", "t@example.com"), ("commit.gpgsign", "false")):
            subprocess.run(["git", "-C", self.repo, "config", k, v], check=True)
        subprocess.run(["git", "-C", self.repo, "commit", "-q", "--allow-empty", "-m", "init"], check=True)
        self.settings_file = os.path.join(self.tmp, "shepherdr.json")
        self.runs, self.live = [], []

        def fake_hc(argv, *a, **kw):
            if argv[:2] == ["hc", "run"]:
                self.runs.append(argv)
                return subprocess.CompletedProcess(argv, 0, json.dumps({"address": f"t{len(self.runs)}"}), "")
            return subprocess.CompletedProcess(argv, 0, "{}", "")

        patches = [
            (settings, "SETTINGS", self.settings_file),
            (base, "STORE", os.path.join(self.tmp, "tasks")),
            (store, "STORE", os.path.join(self.tmp, "tasks")),
            (launch, "WORKTREES", os.path.join(self.tmp, "worktrees")),
            (launch, "HOME", self.tmp),
            (launch, "own_path", lambda: "/bin/herdr-task"),
            (commands, "own_path", lambda: "/bin/herdr-task"),
            (launch, "secret_path", lambda: None),
            (launch, "browser_path", lambda: None),
            (launch, "in_shell", lambda a: a),
            (launch, "low_priority", lambda a, n: a),
            (launch, "clean_env", lambda a: a),
            (launch, "track", lambda *a: None),
            (launch, "hc_json", lambda *a: None),
            (launch, "run", fake_hc),
            (commands, "run", fake_hc),
            (commands, "live_agents", lambda key: list(self.live)),
            (commands, "detail", lambda k: k),
            (commands, "ensure_scheduler", lambda quiet=False: {}),
            (templates, "TEMPLATES", os.path.join(ROOT, "templates")),
        ]
        for mod, name, val in patches:
            p = mock.patch.object(mod, name, val)
            p.start()
            self.addCleanup(p.stop)

    def write_worker(self, worker):
        with open(self.settings_file, "w") as f:
            json.dump({"spaces": {"sp1": {"folder": self.repo, "worker": worker}}}, f)

    def call(self, fn, **kw):
        out = self.io.StringIO()
        with self.redirect(out):
            fn(self.argparse.Namespace(folder=self.repo, json=True, **kw))
        return json.loads(out.getvalue())

    def launched_cmd(self, idx=-1):
        hc = self.runs[idx]
        env_idx = hc.index("env")
        return hc[env_idx + 2:]  # after ["env", "HERDR_BROWSER_TASK=..."]

    def started_task(self, worker, session="0199-sid", **add_kw):
        self.write_worker(worker)
        add_defaults = dict(title="Build loop", prompt="Ship it.", prompt_file=None, here=False, worktree=True,
                            space="sp1", base=None, branch=None, start_notes=None, finish_notes=None, after=None,
                            at=None, in_=None, agent=None, model=None, effort=None, reviewer=None,
                            reviewer_model=None, reviewer_effort=None, set=None)
        add_defaults.update(add_kw)
        v = self.call(self.commands.cmd_add, **add_defaults)
        ref = f"REPO-{v['id']}"
        self.call(self.commands.cmd_start, id=ref, here=False, worktree=False, branch=None, space="sp1",
                  agent=None, argv=None, parent=None, at=None, in_=None, now=True)
        with self.store_mod.open_store(self.argparse.Namespace(folder=self.repo, id=ref)) as s:
            if session:
                s.find(ref)["agents"][-1]["session"] = session
                s.dirty = True
        return ref

    def test_resume_rebuilds_argv_from_settings_and_sends_continue_line(self):
        ref = self.started_task({"agent": "agy", "model": "gemini-4-argon-high", "perm": "auto"})
        self.assertIn("accept-edits", self.launched_cmd())
        # Space settings change to perm: full (--dangerously-skip-permissions).
        self.write_worker({"agent": "agy", "model": "gemini-4-argon-high", "perm": "full"})
        out = self.call(self.commands.cmd_resume, id=ref, space=None, agent=None, argv=None, parent=None)
        self.assertTrue(out["resumed"])
        cmd = self.launched_cmd()
        self.assertIn("--dangerously-skip-permissions", cmd)
        self.assertNotIn("accept-edits", cmd)
        self.assertIn("--conversation", cmd)
        self.assertEqual(cmd[cmd.index("--conversation") + 1], "0199-sid")
        self.assertEqual(cmd[-2], "-i")
        self.assertIn(f"Herdr resumed you: carry on with {ref} where you left off", cmd[-1])
        self.assertIn(f"`/bin/herdr-task ready {ref} --folder ", cmd[-1])
        with self.store_mod.open_store(self.argparse.Namespace(folder=self.repo, id=ref), write=False) as s:
            last = s.find(ref)["agents"][-1]
            self.assertEqual(last["argv"], ["agy", "--model", "gemini-4-argon-high", "--dangerously-skip-permissions"])
            self.assertEqual(last["session"], "0199-sid")

    def test_fresh_handoff_when_session_missing_or_cli_changed_without_duplicate_continue(self):
        # Same CLI, no recorded session: fresh start with new argv and handoff, continue line not sent twice.
        ref = self.started_task({"agent": "agy", "perm": "auto"}, session=None)
        self.write_worker({"agent": "agy", "perm": "full"})
        out = self.call(self.commands.cmd_resume, id=ref, space=None, agent=None, argv=None, parent=None)
        self.assertFalse(out["resumed"])
        cmd = self.launched_cmd()
        self.assertIn("--dangerously-skip-permissions", cmd)
        self.assertNotIn("--conversation", cmd)
        self.assertEqual(cmd[-2], "-i")
        self.assertIn(f"You are taking over task {ref}", cmd[-1])
        self.assertNotIn("Herdr resumed you:", cmd[-1])
        self.assertEqual(cmd[-1].count(f"/bin/herdr-task ready {ref}"), 1)
        # Settings switch CLI from agy to claude: fresh claude agent with handoff, no agy conversation.
        ref2 = self.started_task({"agent": "agy", "perm": "full"}, session="0199-sid2")
        self.write_worker({"agent": "claude", "model": "opus", "perm": "bypassPermissions"})
        out2 = self.call(self.commands.cmd_resume, id=ref2, space=None, agent=None, argv=None, parent=None)
        self.assertFalse(out2["resumed"])
        cmd2 = self.launched_cmd()
        self.assertEqual(cmd2[0], "claude")
        self.assertIn("--dangerously-skip-permissions", cmd2)
        self.assertNotIn("--resume", cmd2)
        self.assertIn(f"You are taking over task {ref2}", cmd2[-1])
        self.assertNotIn("Herdr resumed you:", cmd2[-1])
        # And if handoff ever omitted the ready command, resume still appends continue_line once.
        from unittest import mock
        with mock.patch.object(self.commands, "handoff", lambda *a, **kw: "Short handoff."):
            self.call(self.commands.cmd_resume, id=ref2, space=None, agent=None, argv=None, parent=None)
        self.assertIn(f"Short handoff.\n\nHerdr resumed you: carry on with {ref2}", self.launched_cmd()[-1])

    def test_agent_and_argv_flags_still_win(self):
        ref = self.started_task({"agent": "agy", "model": "gemini-4-argon-high", "perm": "full"},
                                agent="agy", model="gemini-4-argon-high")
        # --agent claude overrides both task's agy+model and space's agy worker (without passing agy's model to claude).
        out = self.call(self.commands.cmd_resume, id=ref, space=None, agent="claude", argv=None, parent=None)
        self.assertFalse(out["resumed"])
        cmd = self.launched_cmd()
        self.assertEqual(cmd[0], "claude")
        self.assertNotIn("gemini-4-argon-high", cmd)
        # Record a claude session on that agent; resuming with --agent claude while space is agy still resumes claude.
        with self.store_mod.open_store(self.argparse.Namespace(folder=self.repo, id=ref)) as s:
            s.find(ref)["agents"][-1]["session"] = "claude-s1"
            s.dirty = True
        out2 = self.call(self.commands.cmd_resume, id=ref, space=None, agent="claude", argv=None, parent=None)
        self.assertTrue(out2["resumed"])
        cmd2 = self.launched_cmd()
        self.assertEqual(cmd2[0], "claude")
        self.assertEqual(cmd2[cmd2.index("--resume") + 1], "claude-s1")
        self.assertIn(f"Herdr resumed you: carry on with {ref}", cmd2[-1])
        # --argv wins over settings too, and still sends the continue line on a resumed session.
        out3 = self.call(self.commands.cmd_resume, id=ref, space=None, agent=None,
                         argv=json.dumps(["claude", "--model", "sonnet"]), parent=None)
        self.assertTrue(out3["resumed"])
        cmd3 = self.launched_cmd()
        self.assertEqual(cmd3[cmd3.index("--model") + 1], "sonnet")
        self.assertEqual(cmd3[cmd3.index("--resume") + 1], "claude-s1")
        self.assertIn(f"Herdr resumed you: carry on with {ref}", cmd3[-1])
        # Conversely, --agent agy on a claude task picks up the space's agy worker settings (perm: full, model).
        ref2 = self.started_task({"agent": "agy", "model": "gemini-4-argon-high", "perm": "full"},
                                 agent="claude", model="sonnet")
        self.call(self.commands.cmd_resume, id=ref2, space=None, agent="agy", argv=None, parent=None)
        cmd4 = self.launched_cmd()
        self.assertEqual(cmd4[0], "agy")
        self.assertIn("--dangerously-skip-permissions", cmd4)
        self.assertEqual(cmd4[cmd4.index("--model") + 1], "gemini-4-argon-high")

    def test_switch_and_tell_keep_their_existing_behaviour(self):
        ref = self.started_task({"agent": "agy", "perm": "auto"}, session="0199-sid")
        self.write_worker({"agent": "agy", "perm": "full"})
        # switch without --agent keeps prev["argv"] and sends no continue line.
        out = self.call(self.commands.cmd_switch, id=ref, space=None, agent=None, argv=None, parent=None, keep=False)
        self.assertTrue(out["resumed"])
        cmd = self.launched_cmd()
        self.assertIn("accept-edits", cmd)
        self.assertNotIn("-i", cmd)
        # tell with no live worker starts again with tell's own message once, not continue_line.
        with self.store_mod.open_store(self.argparse.Namespace(folder=self.repo, id=ref)) as s:
            t = s.find(ref)
            self.commands.tell(s, t, "Please rebase onto main.", self.argparse.Namespace(space=None, agent=None, argv=None))
        cmd2 = self.launched_cmd()
        self.assertEqual(cmd2[-2:], ["-i", "Please rebase onto main."])


class BusyWorkerDelivery(unittest.TestCase):
    """FAC-60: messages herdr-task sends a busy agy worker follow hc send with alt+m when its turn
    offers it, or re-deliver via held_pass once its turn ends if still pending; typing holds and
    other CLIs keep their behaviour. hc is mocked; no real terminals."""

    def setUp(self):
        import shutil
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.commands as commands
        import herdr_task.news as news
        self.commands, self.news, self.mock = commands, news, mock
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.clock = 1_800_000_000_000
        self.term = {"address": "t1", "state": "live", "status": {"state": "working"}, "turn": 2,
                     "labels": {"herdr.agent": "agy"}}
        self.screens, self.raw, self.calls = [""], "", []

        def fake_run(argv, **_):
            self.calls.append(list(argv))
            out = self.screens.pop(0) if argv[:2] == ["hc", "read"] and len(self.screens) > 1 else self.screens[0]
            return subprocess.CompletedProcess(argv, 0, out if argv[:2] == ["hc", "read"] else "", "")

        for obj, name, val in ((news, "STORE", self.tmp), (news, "HELD", os.path.join(self.tmp, ".held.json")),
                               (news, "POLL_DELAYS", (0,)), (news, "now", lambda: self.clock),
                               (news, "guard", lambda *_: None), (news, "hc_json", lambda *_: dict(self.term)),
                               (news, "screen_raw", lambda *_: self.raw), (news, "run", fake_run),
                               (news.subprocess, "run", fake_run)):
            p = mock.patch.object(obj, name, val)
            p.start()
            self.addCleanup(p.stop)

    def held(self):
        try:
            with open(self.news.HELD) as f:
                return json.load(f)
        except OSError:
            return {}

    def test_busy_agy_worker_gets_alt_m_when_its_turn_offers_it(self):
        import argparse
        self.screens = ["▸ Rebase onto main  (alt+m to send immediately)\n  Press up to edit queued messages\n", "> Rebase onto main\n"]
        s = type("S", (), {"dirty": False, "ref": lambda _, t: "FAC-1"})()
        t = {"id": 1, "title": "Fix", "state": "running"}
        with self.mock.patch.object(self.commands, "workers", lambda *_: [self.term]):
            self.assertEqual(self.commands.tell(s, t, "Rebase onto main", argparse.Namespace(space=None)), ("t1", False))
        self.assertIn(["hc", "send", "t1", "Rebase onto main"], self.calls)
        self.assertIn(["hc", "keys", "t1", "alt+m"], self.calls)
        self.assertEqual(self.held(), {})

    def test_busy_agy_worker_without_alt_m_redelivers_once_turn_ends(self):
        self.screens = ["Working on step 3…\n"]
        self.assertEqual(self.news.send_or_hold("t1", "Contract updated in a1b2c3"), "sent")
        self.assertNotIn(["hc", "keys", "t1", "alt+m"], self.calls)
        self.assertEqual(self.held()["t1"][0]["pending"], 3)
        self.calls.clear()
        # Still in turn 2: waits.
        self.news.held_pass({})
        self.assertEqual(self.calls, [])
        # Turn 2 ends without agy starting turn 3: re-delivered once.
        self.term.update(status={"state": "done"}, turn=3)
        report = {}
        self.news.held_pass(report)
        self.assertEqual(self.calls, [["hc", "send", "t1", "Contract updated in a1b2c3"]])
        self.assertEqual(report["held"], [{"to": "t1", "messages": 1}])
        self.assertEqual(self.held(), {})

    def test_pending_dropped_when_agy_already_started_the_next_turn(self):
        self.screens = ["Working…\n"]
        self.news.send_or_hold("t1", "Check contract")
        self.calls.clear()
        self.term.update(status={"state": "working"}, turn=3)
        self.news.held_pass({})
        self.assertEqual(self.calls, [])
        self.assertEqual(self.held(), {})

    def test_held_while_typing_and_other_clis_unchanged(self):
        self.term["labels"] = {"herdr.agent": "agy", self.news.TYPING: str(self.clock - 1000)}
        self.assertEqual(self.news.send_or_hold("t1", "Pointer to docs/x.md"), "held")
        self.assertEqual(self.calls, [])
        # Typing stops while agy is still busy and offers alt+m: held_pass sends and presses alt+m.
        self.term["labels"] = {"herdr.agent": "agy"}
        self.screens = ["▸ Pointer to docs/x.md  (alt+m to send immediately)\n", "> Pointer to docs/x.md\n"]
        self.news.held_pass({})
        self.assertIn(["hc", "send", "t1", "Pointer to docs/x.md"], self.calls)
        self.assertIn(["hc", "keys", "t1", "alt+m"], self.calls)
        self.assertEqual(self.held(), {})
        # Idle agy and busy claude: no alt+m; claude news_send keeps ctrl+enter.
        self.calls.clear()
        self.term.update(status={"state": "done"})
        self.news.send_or_hold("t1", "Continue")
        self.assertEqual(self.calls, [["hc", "send", "t1", "Continue"]])
        self.calls.clear()
        self.term.update(status={"state": "working"}, labels={"herdr.agent": "claude"})
        self.screens = ["ctrl+enter to send now\n"]
        self.assertTrue(self.news.news_send("t1", "<herdr-news/>"))
        self.assertIn(["hc", "keys", "t1", "ctrl+enter"], self.calls)
