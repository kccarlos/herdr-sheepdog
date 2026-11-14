"""herdr-task: every module compiles, the add-on switch it reads, and (on CI only, where HOME is
a throwaway) its commands start. On a person's machine herdr-task never runs with another HOME."""
import home_guard  # noqa: F401  (first: every test on a throwaway home, never the real one)
import json
import os
import shlex
import shutil
import subprocess
import sys
import tempfile
import time
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

    def test_an_unknown_prefix_outside_a_repo_asks_for_the_folder(self):
        """FAC-93: `accept ZZZ-1` from a non-repo shell never guesses the current folder's list."""
        code = ("import sys, argparse; sys.dont_write_bytecode = True; sys.path.insert(0, sys.argv[1]);"
                "import herdr_task.store as st;"
                "st.prefixes_in_use = lambda skip=None: {};"
                "st.locate = lambda p: {'folder': '/x', 'kind': 'folder', 'name': 'x', 'common': None};"
                "\ntry:\n st.open_store(argparse.Namespace(id='ZZZ-1', folder=None))\nexcept st.Fail as e: print(e)")
        out = subprocess.run([sys.executable, "-c", code, BIN], capture_output=True, text=True)
        self.assertIn("which folder? pass --folder", out.stdout, out.stderr)

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
            "retry_pass", "archive_pass", "news_pass", "watchdog_pass", "end_done_pass", "memory_guard", "backup_pass", "cli_check_pass", "machines_pass")]
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
        # still finishing the turn it said ready in: not Working again, but not Ready yet either (SHEP-148)
        self.assertEqual(self.agents.task_status(t, [self.agent({"address": "t1", "status": "working", "turn": 4, "role": "worker"})]),
                         ("working", None, None))
        self.assertEqual(self.agents.task_status(t, [self.agent({"address": "t1", "status": "idle", "turn": 5, "role": "worker"})])[0], "ready")

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

    def test_ready_again_when_its_own_turn_changed_nothing(self):
        """SHEP-133: a background job's notice (or a question in its chat) wakes a Ready task's agent;
        it answers and goes quiet with nothing new. Ready again, not Needs you."""
        wt = tempfile.mkdtemp()
        said = {"text": "Proof published", "at": 1, "set": 1}
        t = self.ready(mode="worktree", worktree=wt, branch="b", head="abc", progress=dict(said))
        repo = {"head": "abc", "dirty": ""}
        p = self.mock.patch.object(self.tick, "git", lambda cwd, *a: (True, repo["head"] if a[0] == "rev-parse" else repo["dirty"], ""))
        p.start()
        self.addCleanup(p.stop)
        worker = lambda st, turn: [dict(self.agent({"address": "t1", "status": st, "turn": turn, "role": "worker"}), agent="claude")]
        # woken: Working again (its Assistant hears it), Herdr's line
        self.live = worker("working", 5)
        self.tick.tick_store(self.store, {"started": []})
        self.assertEqual((t["state"], t["back"]["auto"], t["progress"]["by"]), ("running", True, "herdr"))
        # still at work: stays so
        self.tick.tick_store(self.store, {"started": []})
        self.assertEqual(t["state"], "running")
        # its turn over, nothing new: Ready again, its own line back, the next turn new work again
        self.live = worker("idle", 6)
        report = {"started": []}
        self.tick.tick_store(self.store, report)
        self.assertEqual((t["state"], t["ready_turns"], t["progress"], report["ready_again"]), ("ready", {"t1": 6}, said, ["SHEP-7"]))
        self.assertNotIn("back", t)
        self.assertEqual(self.agents.task_status(t, self.live)[0], "ready")
        # woken again, and this time it commits, or leaves changes, or asks: stays Working (Needs you after the grace)
        for change in ({"head": "def"}, {"dirty": " M a.py"}):
            t.update(state="ready", ready_turns={"t1": 6})
            repo.update({"head": "abc", "dirty": "", **change})
            self.live = worker("working", 6)
            self.tick.tick_store(self.store, {"started": []})
            self.live = worker("idle", 7)
            self.tick.tick_store(self.store, {"started": []})
            self.assertEqual(t["state"], "running", change)

    def test_a_fix_asked_for_is_never_undone(self):
        """herdr-task fix or resume took it back: Working until its agent says ready, idle or not."""
        t = self.ready(mode="worktree", worktree=tempfile.mkdtemp(), branch="b", head="abc")
        p = self.mock.patch.object(self.tick, "git", lambda cwd, *a: (True, "abc" if a[0] == "rev-parse" else "", ""))
        p.start()
        self.addCleanup(p.stop)
        self.commands.back_to_work(self.store, t, "the person asked for a fix")
        self.assertNotIn("auto", t["back"])
        self.live = [dict(self.agent({"address": "t1", "status": "idle", "turn": 6, "role": "worker"}), agent="claude")]
        self.tick.tick_store(self.store, {"started": []})
        self.assertEqual(t["state"], "running")

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

    def test_resume_nudges_the_resumed_agent(self):
        """SHEP-189 / FAC-61: a resumed session comes back with no message; resume rebuilds argv from
        settings and sends continue_line so it doesn't sit idle until someone writes to it."""
        t = self.ready(space="sp1")
        got = {}

        def fake(s, t, space, *a, **k):
            got.update(k)
            return {"address": "t2"}, True
        with self.mock.patch.object(self.commands, "start_again", fake):
            self.commands.cmd_resume(self.args(space=None, agent=None, argv=None, parent=None))
        self.assertTrue(got.get("resuming"))
        self.assertIn("carry on with SHEP-7 where you left off", self.commands.continue_line(self.store, t))

    def test_accept_refuses_a_task_back_at_work(self):
        t = self.ready()
        self.live = [self.agent({"address": "t1", "status": "working", "turn": 5, "role": "worker"})]
        with self.assertRaises(self.commands.Fail) as no:
            self.commands.cmd_accept(self.args())
        self.assertIn("is working again", str(no.exception))
        self.assertEqual(t["state"], "running")
        self.assertNotIn("accepted_at", t)

    def out_task(self):
        t = self.pr_task()
        t.update(state="running", review_out=5)
        return t

    def test_accept_out_of_rounds_refuses_and_names_the_flag(self):
        t = self.out_task()
        with self.assertRaises(self.commands.Fail) as no:
            self.commands.cmd_accept(self.args())
        self.assertIn('out of review rounds: accept --owner-approved "<who/where>" once the owner approved it', str(no.exception))
        self.assertNotIn("owner_approved", t)
        with self.assertRaises(self.commands.Fail):  # a flag on a task that isn't out of rounds changes nothing
            self.pr_task().update(state="running", review_out=None) or self.commands.cmd_accept(self.args(owner_approved="x"))

    def test_accept_owner_approved_merges_and_records_it(self):
        t = self.out_task()
        fine = {"state": "OPEN", "mergeable": "MERGEABLE", "statusCheckRollup": [], "headRefOid": "abc"}
        self.merging([fine, {"state": "MERGED", "mergeCommit": {"oid": "f00"}}], [(True, "", "")])
        self.commands.cmd_accept(self.args(owner_approved="owner in chat, 2026-11-13"))
        self.assertEqual((t["state"], t.get("merged")), ("done", True))
        self.assertEqual((t["owner_approved"]["note"], bool(t["owner_approved"]["at"])), ("owner in chat, 2026-11-13", True))

    def test_accept_owner_approved_waiting_on_checks_is_ready_for_the_tick(self):
        t = self.out_task()
        pending = {"state": "OPEN", "mergeable": "MERGEABLE", "headRefOid": "abc",
                   "statusCheckRollup": [{"name": "check", "status": "IN_PROGRESS"}]}
        self.merging([pending], [])
        self.commands.cmd_accept(self.args(owner_approved="owner said so"))
        self.assertEqual((t["state"], t.get("auto_merge")), ("ready", "checks"))
        self.assertEqual(t["owner_approved"]["note"], "owner said so")

    def test_accept_owner_approved_still_refuses_a_red_check(self):
        t = self.out_task()
        red = {"state": "OPEN", "mergeable": "MERGEABLE", "headRefOid": "abc",
               "statusCheckRollup": [{"name": "check", "conclusion": "FAILURE"}]}
        done = self.merging([red, red], [])
        with self.assertRaises(SystemExit):
            self.commands.cmd_accept(self.args(owner_approved="owner said so"))
        self.assertEqual((t["state"], done), ("ready", []))  # a retry is a plain accept
        self.assertIn("Checks failing", t["attention"])

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
        self.merging([fine, fine, fine, {"state": "MERGED", "mergeCommit": {"oid": "f00"}}],
                     [(False, "", "GraphQL: Something went wrong"), (True, "", "")])
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

    # GitHub's merge queue (#742, #801, #802): gh refuses --delete-branch there; the pull request joins the
    # queue and the task stays Merging until GitHub merged it, then its branch there goes.
    QUEUE_ERR = "Cannot use `-d` or `--delete-branch` when merge queue enabled"
    FINE = {"state": "OPEN", "mergeable": "MERGEABLE", "statusCheckRollup": [], "headRefOid": "abc"}

    def gh_merges(self, views, merges):
        """merging(), with each gh call kept."""
        calls, merges = [], list(merges)
        done = self.merging(views, [])
        p = self.mock.patch.object(self.commands, "gh", lambda *a, **k: (calls.append(a), merges.pop(0))[1])
        p.start()
        self.addCleanup(p.stop)
        dropped = []
        p = self.mock.patch.object(self.commands, "drop_branch", lambda s, t: dropped.append(t["id"]))
        p.start()
        self.addCleanup(p.stop)
        return calls, done, dropped

    def test_a_merge_queue_queues_it_and_it_is_not_done(self):
        t = self.pr_task()
        calls, done, dropped = self.gh_merges([self.FINE, self.FINE], [(False, "", self.QUEUE_ERR), (True, "", "")])
        self.commands.cmd_accept(self.args())
        self.assertEqual(len(calls), 2)
        self.assertIn("--delete-branch", calls[0])
        self.assertNotIn("--delete-branch", calls[1])
        self.assertEqual(calls[1][calls[1].index("--match-head-commit") + 1], "abc")
        self.assertEqual((t["state"], bool(t.get("merged")), done), ("ready", False, []))
        self.assertTrue(t["merge_queued"])
        self.assertNotIn("attention", t)
        self.assertEqual(self.agents.task_status(t, []), ("working", None, "In GitHub's merge queue"))
        # the queue is remembered for its base: the next merge goes straight to it
        t2 = dict(self.pr_task(), id=8)
        self.store.data["tasks"].append(t2)
        calls2, _, _ = self.gh_merges([self.FINE, self.FINE], [(True, "", "")])
        self.assertEqual(self.commands.merge_now(self.store, t2), self.commands.IN_QUEUE)
        self.assertNotIn("--delete-branch", calls2[0])
        self.assertEqual(dropped, [])

    def test_merge_again_while_queued_asks_github_nothing_more(self):
        t = self.pr_task()
        t["merge_queued"] = 1
        calls, done, _ = self.gh_merges([self.FINE], [])
        self.commands.cmd_merge(self.args())
        self.assertEqual((calls, done, t["state"]), ([], [], "ready"))

    def test_a_repo_without_a_queue_merges_as_before(self):
        t = self.pr_task()
        calls, done, dropped = self.gh_merges([self.FINE, {"state": "MERGED", "mergeCommit": {"oid": "f00"}}], [(True, "", "")])
        self.commands.cmd_accept(self.args())
        self.assertEqual(len(calls), 1)
        self.assertIn("--delete-branch", calls[0])
        self.assertEqual((t["state"], t.get("merged"), dropped), ("done", True, []))

    def queue_tick(self, v, entry=None, clock=10_000_000):
        """One tick pass over the task with GitHub saying v (and entry for its queue)."""
        got = {"done": [], "dropped": [], "asked": 0}

        def ask(t):
            got["asked"] += 1
            return entry
        for name, value in (("pr_view", lambda t: v), ("pr_news", lambda *a: None), ("now", lambda: clock),
                            ("queue_entry", ask), ("watch_main", lambda *a: None), ("update_main", lambda *a: None),
                            ("drop_branch", lambda s, t: got["dropped"].append(t["id"])),
                            ("finish", lambda s, t, even_if_busy=False: (t.update(state="done"), got["done"].append(t["id"])))):
            p = self.mock.patch.object(self.tick, name, value)
            p.start()
            self.addCleanup(p.stop)
        t = self.store.data["tasks"][0]
        t["pr"]["checked"] = 0
        report = {"attention": [], "done": []}
        self.tick.facts(self.store, t, report)
        return got, report

    def test_a_queued_pull_request_is_done_once_github_merged_it(self):
        t = self.pr_task()
        t["merge_queued"] = 10_000_000 - 1000
        got, _ = self.queue_tick(self.FINE, clock=10_000_000)
        self.assertEqual((t["state"], got["asked"]), ("ready", 0))  # just queued: not asked yet
        got, _ = self.queue_tick(self.FINE, {"in": True, "auto": False, "reason": None}, clock=20_000_000)
        self.assertEqual((t["state"], t.get("merge_queued"), got["asked"]), ("ready", 10_000_000 - 1000, 1))
        got, report = self.queue_tick({"state": "MERGED", "mergeCommit": {"oid": "f00"}})
        self.assertEqual((t["state"], t.get("merged"), got["dropped"], report["done"]), ("done", True, [7], [7]))

    def test_the_queue_removing_it_needs_you(self):
        t = self.pr_task()
        t["merge_queued"] = 1
        _, report = self.queue_tick(self.FINE, {"in": False, "auto": False, "reason": "The merge commit's checks failed"})
        self.assertNotIn("merge_queued", t)
        self.assertEqual(t["attention"], "GitHub's merge queue removed it: The merge commit's checks failed.")
        self.assertEqual(report["attention"], [7])
        # the next pass leaves that alone (nothing GitHub says about the pull request itself is wrong)
        self.queue_tick(self.FINE, clock=20_000_000)
        self.assertEqual(t["attention"], "GitHub's merge queue removed it: The merge commit's checks failed.")

    # #743: a base that requires an approving review: no merge tried, no Needs you; it merges once approved.
    def test_merge_after_checks_waits_calmly_for_an_approval(self):
        t = self.pr_task()
        t["auto_merge"] = "checks"
        waiting = dict(self.FINE, reviewDecision="REVIEW_REQUIRED")
        self.queue_tick(waiting)
        self.assertFalse(t.get("_pr_fresh"))
        self.assertNotIn("attention", t)
        self.assertEqual(self.agents.task_status(t, []), ("working", None, "Merges once it's approved"))
        calls, done, _ = self.gh_merges([waiting], [])
        self.assertEqual(self.commands.merge_now(self.store, t), self.commands.WAITS_APPROVAL)
        self.assertEqual((calls, done), ([], []))
        self.assertNotIn("attention", t)
        # approved: fresh again, and it merges as before
        self.queue_tick(dict(self.FINE, reviewDecision="APPROVED"))
        self.assertTrue(t.get("_pr_fresh"))
        self.assertNotIn("needs_approval", t["pr"])

    def test_accept_waiting_for_an_approval_merges_once_approved(self):
        t = self.pr_task()
        t["auto_merge"] = None
        calls, done, _ = self.gh_merges([dict(self.FINE, reviewDecision="REVIEW_REQUIRED")], [])
        self.commands.cmd_accept(self.args())
        self.assertEqual((t["state"], t["auto_merge"], calls, done), ("ready", "checks", [], []))

    def test_a_branch_behind_under_an_up_to_date_rule_says_so(self):
        import herdr_task.github as github
        self.assertEqual(github.pr_problem(dict(self.FINE, mergeStateStatus="BEHIND")), github.BEHIND)
        t = self.pr_task()
        self.queue_tick(dict(self.FINE, mergeStateStatus="BEHIND"))
        self.assertEqual(t["attention"], github.BEHIND)
        self.queue_tick(dict(self.FINE, mergeStateStatus="CLEAN"), clock=20_000_000)
        self.assertNotIn("attention", t)

    # #781: a run that a newer run of the same check superseded (cancel-in-progress) never counts.
    def test_a_superseded_cancelled_run_is_not_failing(self):
        import herdr_task.github as github
        rollup = [{"__typename": "CheckRun", "name": "check", "workflowName": "Check", "status": "COMPLETED",
                   "conclusion": "CANCELLED", "startedAt": "2026-11-09T10:00:00Z"},
                  {"__typename": "CheckRun", "name": "check", "workflowName": "Check", "status": "COMPLETED",
                   "conclusion": "SUCCESS", "startedAt": "2026-11-09T10:00:05Z"},
                  {"__typename": "StatusContext", "context": "ci/other", "state": "SUCCESS", "startedAt": "2026-11-09T10:00:00Z"}]
        out = json.dumps(dict(self.FINE, statusCheckRollup=rollup))
        with self.mock.patch.object(github, "gh", lambda *a, **k: (True, out, "")):
            v = github.pr_view(self.pr_task())
        self.assertEqual([c.get("conclusion") or c.get("state") for c in v["statusCheckRollup"]], ["SUCCESS", "SUCCESS"])
        self.assertIsNone(github.pr_problem(v))
        # a cancelled run with no newer one still fails
        lone = [dict(rollup[0], name="lint")] + rollup[1:]
        self.assertEqual(github.pr_problem({"statusCheckRollup": github.latest_checks(lone)}), "Checks failing: lint.")

    # #802, #844: a merge GitHub reports never closes a task under its live agent, nor one reopened after it.
    def test_a_working_task_whose_pull_request_merged_stays_open_while_its_agent_is_there(self):
        t = self.pr_task()
        t["state"] = "running"
        self.live = [{"address": "t1", "status": {"state": "idle"}, "agent": "claude"}]
        merged = {"state": "MERGED", "mergeCommit": {"oid": "f00"}}
        self.queue_tick(merged)
        self.assertEqual((t["state"], t.get("merged")), ("running", None))
        self.live = []
        self.queue_tick(merged, clock=20_000_000)
        self.assertEqual((t["state"], t.get("merged")), ("done", True))

    def test_reopened_after_a_merge_it_stays_open_and_takes_status(self):
        t = self.pr_task()
        t.update(state="done", merged=True, started=1)
        self.commands.cmd_state(self.args(), "reopen")
        self.assertEqual((t["state"], t.get("kept_open")), ("running", True))
        self.queue_tick({"state": "MERGED", "mergeCommit": {"oid": "f00"}})
        self.assertEqual(t["state"], "running")
        self.commands.cmd_status(self.args(text="Releasing", done=50, left="10m"))
        self.assertEqual(t["progress"]["text"], "Releasing")

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
        self.hc = {}  # what hc ls --label herdr.task=<key>, hc get and hc read answer
        h = mock.patch.object(agents, "hc_json", lambda *a: ({"terminals": self.hc.get("ls", [])} if a[0] == "ls"
                                                               else self.hc.get("get", {}).get(a[1])))
        r = mock.patch.object(agents, "run", lambda a, **_: subprocess.CompletedProcess(
            a, 0, self.hc.get("read", {}).get(a[2], "") if a[:2] == ["hc", "read"] else "", ""))
        h.start(); r.start()
        self.addCleanup(h.stop); self.addCleanup(r.stop)
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

    def test_a_plain_question_needs_you_once_nothing_runs(self):
        """SHEP-148: a plain question with its own work still running is no ask yet (asking for real
        is its question tool or a permission: blocked); once nothing runs, it is."""
        self.background(-300_000)
        self.say(-200_000, "Tests run in the background. Should I also update the docs?")
        self.assertEqual(self.status(), ("working", None, "Waiting on a background job"))
        # asking with its question tool or a permission: blocked, Needs you at once
        self.assertEqual(self.status([dict(self.idle[0], status={"state": "blocked"})])[:2], ("needs", "asks"))
        # its shell ended: the question is the person's now
        self.finished(-100_000)
        self.assertEqual(self.status()[:2], ("needs", "reply"))

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

    def test_agy_footer_tasks_count_shows_waiting_on_a_background_job(self):
        import herdr_task.output as output
        agy = {"id": 8, "title": "Checks", "state": "running", "started": self.clock - 3600_000, "agents": [{"address": "t1", "at": self.clock - 3600_000}]}
        k = {"address": "t1", "status": {"state": "done", "app": "antigravity", "msg": "Dispatched CI checks."},
             "status_ms": self.clock - 60_000, "foreground": {"name": "agy"}, "labels": {"herdr.agent": "agy"}}
        rule, survey = "─" * 100, "  How's the CLI experience so far? Help us improve:  \n  [1] Good  [2] Fine  [3] Bad  [0] Skip"
        for body in ("> Auto mode: edits & safe commands auto-approved (shift+tab to cycle)", survey):
            self.hc["read"] = {"t1": "\n".join(["─" * 60, "> Re-run the checks", "  Checks run in the background.", rule, body, rule,
                                                "? for shortcuts                                      auto · Gemini 4 Carbon · high · 1 task(s) · /tasks"])}
            self.assertEqual(self.agents.task_status(agy, [k]), ("working", None, "Waiting on a background job"))
        s = type("S", (), {"write": False, "loc": {"folder": "/f"}, "prefix": "X", "key": lambda *_: "X-8", "ref": lambda *_: "X-8"})()
        with self.mock.patch.object(output, "waiting_for", lambda *_: []), self.mock.patch.object(output, "waiting_ext", lambda *_: []):
            self.assertTrue(output.view(s, agy, [k])["own_work"])
        self.hc["read"] = {"t1": "\n".join(["  Finished 1 task(s) cleanly.", "", rule, "> Auto mode: edits & safe commands auto-approved (shift+tab to cycle)",
                                            rule, "? for shortcuts                                                       auto · Gemini 4 Carbon · high"])}
        self.assertEqual(self.agents.task_status(agy, [k])[:2], ("needs", "reply"))

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


class CodexCodeMode(unittest.TestCase):
    """SHEP-207: Codex 0.163+ code mode runs its commands from a script (one exec call with
    tools.exec_command and tools.write_stdin inside Promise.allSettled). A poll's result drops its
    session id once the command exited, so each result is matched to the session its call polled,
    and a Codex job counts only while its agent's process tree runs something besides the code-mode
    host. The items are a real run's (fixtures/codex-code-mode); the process tree is faked (ps)."""

    setUp = OwnBackgroundWork.setUp
    stamp, status = OwnBackgroundWork.stamp, OwnBackgroundWork.status

    def setUp_ps(self, ps=""):
        """The `ps` output codex_working() reads (the rest of the test's processes: none)."""
        from types import SimpleNamespace
        if not hasattr(self, "ps"):
            p = self.mock.patch.object(self.agents, "run", lambda args, **kw: SimpleNamespace(stdout=self.ps), create=True)
            p.start()
            self.addCleanup(p.stop)
        self.ps = ps

    def replay(self, n=None):
        with open(os.path.join(ROOT, "test", "fixtures", "codex-code-mode", "rollout-items.jsonl")) as f:
            items = [json.loads(line) for line in f if line.strip()]
        for i, item in enumerate(items[:n]):
            self.stamp(-300_000 + i * 1000, item)

    def codex(self, ps_pid=100):
        return [{"address": "t1", "status": {"state": "idle"}, "labels": {"herdr.role": "worker"},
                 "foreground": {"pid": ps_pid, "name": "codex"}}]

    def test_each_poll_ends_its_own_session(self):
        self.setUp_ps()
        self.replay(2)  # both commands started: their results carry their session ids
        self.assertEqual(sorted(self.agents.background_jobs(self.log)[0]), ["codex:50495", "codex:69674"])
        self.replay(4)  # the script polls both in one call: "Script running with cell ID 2"
        self.assertEqual(sorted(self.agents.background_jobs(self.log)[0]), ["codex:50495", "codex:69674"])
        self.replay()  # wait on cell 2: alpha's result has no session id (exited), beta still runs
        jobs, ended = self.agents.background_jobs(self.log)
        self.assertEqual(list(jobs), ["codex:50495"])
        self.assertGreater(ended, 0)

    def test_a_result_without_its_id_ends_the_session_its_place_polled(self):
        self.setUp_ps()
        self.stamp(-300_000, {"type": "response_item", "payload": {"type": "custom_tool_call", "call_id": "p1", "name": "exec",
                              "input": "await Promise.allSettled([tools.write_stdin({session_id:7, chars:''}), tools.write_stdin({session_id:8, chars:''})])"}})
        self.stamp(-299_000, {"type": "response_item", "payload": {"type": "function_call_output", "call_id": "p1", "output": [
            {"type": "input_text", "text": "[{\"status\":\"fulfilled\",\"value\":{\"exit_code\":0,\"output\":\"\"}},"
                                         "{\"status\":\"fulfilled\",\"value\":{\"session_id\":8,\"output\":\"\"}}]"}]}})
        self.assertEqual(list(self.agents.background_jobs(self.log)[0]), ["codex:8"])

    def test_a_code_mode_job_with_no_process_is_over(self):
        """The bug: the log still counts beta, but its agent is idle and no command runs under it."""
        self.setUp_ps("100 1 codex --no-daemon\n101 100 /usr/local/lib/codex/codex-code-mode-host\n")
        self.replay(4)
        live = self.codex()
        self.assertEqual(self.status(live)[:2], ("needs", "reply"))
        self.assertFalse(self.agents.own_work(self.task, live, ready=True))

    def test_a_running_command_under_the_agent_is_still_work(self):
        self.setUp_ps("100 1 codex --no-daemon\n101 100 /usr/local/lib/codex/codex-code-mode-host\n"
                      "102 101 /bin/bash -lc sleep 60\n")
        self.replay(4)
        live = self.codex()
        self.assertEqual(self.status(live)[:2], ("working", None))
        self.assertTrue(self.agents.own_work(self.task, live, ready=True))

    def test_an_agent_without_a_pid_keeps_the_log_s_word(self):
        self.setUp_ps()
        self.replay(4)
        self.assertEqual(self.status()[:2], ("working", None))

    def test_the_old_write_stdin_format_still_ends(self):
        self.setUp_ps()
        self.stamp(-300_000, {"type": "response_item", "payload": {"type": "function_call", "call_id": "c1", "name": "exec_command",
                              "arguments": json.dumps({"cmd": "gh run watch"})}})
        self.stamp(-300_000, {"type": "response_item", "payload": {"type": "function_call_output", "call_id": "c1",
                              "output": "Process running with session ID 42"}})
        self.stamp(-200_000, {"type": "response_item", "payload": {"type": "function_call", "call_id": "c2", "name": "write_stdin",
                              "arguments": json.dumps({"session_id": 42, "chars": ""})}})
        self.stamp(-100_000, {"type": "response_item", "payload": {"type": "function_call_output", "call_id": "c2",
                              "output": "Process exited with code 0\nOutput:\nok"}})
        self.assertEqual(self.agents.background_jobs(self.log)[0], {})

    def test_codex_working_reads_the_process_tree(self):
        codex = {"foreground": {"pid": 100}}
        self.setUp_ps("100 1 codex\n101 100 codex-code-mode-host\n")
        self.assertFalse(self.agents.codex_working(codex))
        self.setUp_ps("100 1 codex\n101 100 codex-code-mode-host\n102 101 sleep 9\n")
        self.assertTrue(self.agents.codex_working(codex))
        self.assertTrue(self.agents.codex_working({}))  # no pid: its log decides


class NoFalseNeedsYou(unittest.TestCase):
    """SHEP-133: no Needs you for a few seconds that moves on by itself. A quiet agent or a problem
    Herdr found needs the person only after a grace (GRACE_MS); asking needs them at once; Herdr's
    own "Working again" is never the reason; a background job started before megabytes of
    screenshots still counts. Real logs in a temp folder; hc is never run."""

    setUp = OwnBackgroundWork.setUp
    stamp, say, background, finished, status = (OwnBackgroundWork.stamp, OwnBackgroundWork.say, OwnBackgroundWork.background,
                                                OwnBackgroundWork.finished, OwnBackgroundWork.status)

    def test_the_rule_is_cores(self):
        """The cases core/tasks.test.js checks place() on (core/grace.cases.json)."""
        with open(os.path.join(ROOT, "core", "grace.cases.json")) as f:
            g = json.load(f)
        self.assertEqual(self.agents.GRACE_MS, 20_000)
        agent = lambda a: {"address": a["address"], "status": {"state": a["status"]}, "turn": a["turn"],
                           "labels": {"herdr.role": a["role"]}, **({"status_ms": a["since"]} if "since" in a else {})}
        with self.mock.patch.object(self.agents, "now", lambda: g["now"]), \
                self.mock.patch.object(self.agents, "held_ask", lambda address: None):
            for i, c in enumerate(g["cases"]):
                task = dict(c["task"], id=7)
                if "log" in c:
                    # SHEP-148: an agent's real state, its session log written as Claude Code writes it
                    log = os.path.join(self.dir, f"case{i}.jsonl")
                    with open(log, "w") as f:
                        f.write("".join(json.dumps(d, ensure_ascii=False, separators=(",", ":")) + "\n" for d in c["log"]))
                    task["agents"] = [dict(a, log=log) for a in task["agents"]]
                live = [agent(a) for a in c["live"]]
                got = self.agents.task_status(task, live)
                self.assertEqual(got[:2], (c["expect"]["status"], c["expect"]["needs"]), c["name"])
                # core reads task.own_work: what herdr-task's scheduler keeps from that same log
                if "own_work" in c["task"]:
                    self.assertEqual(self.agents.own_work(task, live, ready=task["state"] == "ready"), c["task"]["own_work"], c["name"])

    def test_a_quiet_agent_needs_you_once_the_grace_is_over(self):
        self.say(-3_000, "Done with the first part.")
        quiet = lambda ago, st="idle": [dict(self.idle[0], status={"state": st}, status_ms=self.clock - ago)]
        self.assertEqual(self.status(quiet(3_000)), ("working", None, None))
        self.assertEqual(self.status(quiet(19_000)), ("working", None, None))
        self.assertEqual(self.status(quiet(21_000))[:2], ("needs", "reply"))
        # a question or a permission: at once
        self.assertEqual(self.status(quiet(1_000, "blocked"))[:2], ("needs", "asks"))

    def test_herdrs_own_words_are_never_its_reason(self):
        """SHEP-126 read "Needs you · Working again": a background job's notice woke it while Ready
        (back_to_work() set Herdr's line), it said it was still ready and went quiet."""
        self.say(-60_000, "That was an old file search finishing. Nothing changes: the task is still ready.")
        self.task["progress"] = {"text": "Working again", "at": self.clock - 70_000, "set": self.clock - 70_000, "by": "herdr"}
        self.assertIsNone(self.agents.own_line(self.task))
        self.assertEqual(self.agents.last_words(self.task), "Nothing changes: the task is still…")
        # its own line said this turn still is the reason
        self.task["progress"] = {"text": "Waiting for your go on the menu", "at": self.clock - 50_000, "set": self.clock - 50_000}
        self.assertEqual(self.agents.own_line(self.task), "Waiting for your go on the menu")

    def test_a_job_before_megabytes_of_screenshots_still_counts(self):
        """HOME-1 read Needs you while its Monitor ran: 4.5 MB of image results after the Monitor's
        start pushed it out of the 1 MB the log's end was read for."""
        self.background(-300_000)
        image = {"type": "image", "source": {"type": "base64", "data": "A" * 700_000}}
        for i in range(4):
            self.stamp(-200_000 + i, {"type": "user", "message": {"role": "user", "content": [
                {"tool_use_id": f"toolu_r{i}", "type": "tool_result", "content": [image]}]}})
        self.say(-100_000, "Four look clean. Waiting for the rest.")
        self.assertGreater(os.path.getsize(self.log), 2_500_000)
        self.assertEqual(self.status(), ("working", None, "Waiting on a background job"))
        # it ends: quiet with nothing left, Needs you
        self.finished(-50_000)
        self.assertEqual(self.status()[:2], ("needs", "reply"))

    def test_a_long_command_in_the_foreground_is_work(self):
        """HOME-1 read Needs you twice mid-turn: a 50 s and a 110 s Bash running in the foreground,
        Claude Code read idle meanwhile. Its log has the call open: Working. A permission for it
        held, Needs you; done, or open past FG_MAX_MS, quiet as before."""
        held = {}
        p = self.mock.patch.object(self.agents, "held_ask", lambda address: held.get(address))
        p.start()
        self.addCleanup(p.stop)
        self.say(-60_000, "Passes. Finishing all 10, then the gallery.")
        # Claude Code's own compact lines, as its log writes them
        self.lines.append('{"parentUuid":"a","isSidechain":false,"message":{"role":"assistant","content":[{"type":"tool_use",'
                          '"id":"toolu_9","name":"Bash","input":{"command":"./finish.sh"}}]},"type":"assistant","timestamp":"%s"}'
                          % self.iso(-50_000))
        self.write()
        self.assertEqual(self.status(), ("working", None, "Waiting on a background job"))
        self.assertEqual(self.agents.open_tool(self.log)[0], "Bash")
        held["t1"] = {"tool_name": "Bash", "tool_input": {"command": "./finish.sh"}}
        self.assertEqual(self.status()[:2], ("needs", "reply"))
        held.clear()
        # its result in: quiet, Needs you
        self.lines.append('{"parentUuid":"b","isSidechain":false,"type":"user","message":{"role":"user","content":[{"tool_use_id":'
                          '"toolu_9","type":"tool_result","content":"done"}]},"timestamp":"%s"}' % self.iso(-10_000))
        self.write()
        self.assertEqual(self.status()[:2], ("needs", "reply"))
        # a subagent's call (its side chain) or a question tool is no work of its own; one open 15 minutes, none
        for line in ('{"isSidechain":true,"message":{"content":[{"type":"tool_use","id":"toolu_a","name":"Bash"}]},"type":"assistant","timestamp":"%s"}' % self.iso(-5_000),
                     '{"isSidechain":false,"message":{"content":[{"type":"tool_use","id":"toolu_b","name":"AskUserQuestion"}]},"type":"assistant","timestamp":"%s"}' % self.iso(-5_000),
                     '{"isSidechain":false,"message":{"content":[{"type":"tool_use","id":"toolu_c","name":"Bash"}]},"type":"assistant","timestamp":"%s"}' % self.iso(-16 * 60_000)):
            self.lines.append(line)
            self.write()
            self.assertEqual(self.status()[:2], ("needs", "reply"), line)
            self.lines.pop()

    def iso(self, ago):
        import datetime
        return datetime.datetime.fromtimestamp((self.clock + ago) / 1000, datetime.timezone.utc).isoformat().replace("+00:00", "Z")

    def write(self):
        with open(self.log, "w") as f:
            f.write("\n".join(self.lines) + "\n")
        self.agents._bg_cache.clear()

    def test_the_log_is_read_as_it_grows(self):
        self.background(-300_000)
        self.assertEqual(list(self.agents.background_jobs(self.log)[0]), ["b1"])
        line = json.dumps({"type": "queue-operation", "operation": "enqueue", "timestamp": "2027-01-15T08:00:00Z",
                           "content": "<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n</task-notification>"})
        # half a line still being written: not read yet
        with open(self.log, "a") as f:
            f.write(line[:40])
        self.assertEqual(list(self.agents.background_jobs(self.log)[0]), ["b1"])
        with open(self.log, "a") as f:
            f.write(line[40:] + "\n")
        jobs, ended = self.agents.background_jobs(self.log)
        self.assertEqual((jobs, bool(ended)), ({}, True))
        # a new log in its place (same name): read afresh
        os.remove(self.log)
        with open(self.log, "w") as f:
            f.write("\n".join(self.lines) + "\n")
        self.assertEqual(list(self.agents.background_jobs(self.log)[0]), ["b1"])


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
        # a plain question while its reviewer still works: no ask yet (SHEP-148), Working
        self.say(-100_000, "Should I also update the docs?")
        self.assertEqual(self.status(), ("working", None, None))
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


class DoneWorktreesGo(unittest.TestCase):
    """SHEP-161: a Done task whose agent was still open when it finished keeps its worktree only until
    its agents are gone; leftovers from before go on the next pass. Never with uncommitted work, never
    a folder a terminal is live in. A throwaway git repo; hc is never run."""

    def setUp(self):
        from unittest import mock
        import shutil
        sys.path.insert(0, BIN)
        import herdr_task.github as github
        import herdr_task.tick as tick
        self.github, self.tick = github, tick
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.main, wt = os.path.join(self.root, "repo"), os.path.join(self.root, "wt")
        self.git(self.root, "init", "-q", "-b", "main", self.main)
        for k, v in (("user.name", "T"), ("user.email", "t@example.com"), ("commit.gpgsign", "false")):
            self.git(self.main, "config", k, v)
        self.git(self.main, "commit", "-q", "--allow-empty", "-m", "base")
        self.git(self.main, "worktree", "add", "-q", "-b", "task/1", wt, "HEAD")
        self.git(wt, "commit", "-q", "--allow-empty", "-m", "reactions")
        self.git(self.main, "merge", "-q", "--ff-only", "task/1")
        self.live, self.others = [], []  # the task's live terminals; every live terminal (hc ls)
        self.store = Store([{"id": 1, "title": "Reactions", "state": "running", "mode": "worktree", "worktree": wt,
                             "branch": "task/1", "base": "main", "merged": True, "agents": []}])
        self.store.loc["folder"] = self.main
        self.task = self.store.data["tasks"][0]
        live = lambda key: list(self.live)
        for mod, name, value in ((github, "live_agents", live), (github, "live_helpers", lambda key: []),
                                 (github, "hc_json", lambda *a: {"terminals": self.live + self.others}),
                                 (tick, "live_agents", live), (tick, "detail", lambda k: k),
                                 (tick, "refresh_sessions", lambda *a: None), (tick, "quiet_hc", self.end),
                                 (tick, "store_files", lambda folder=None: ["/f.json"]),
                                 (tick, "read_list", lambda p: {"folder": self.main, "tasks": self.store.data["tasks"]}),
                                 (tick, "locate", lambda f: self.store.loc), (tick, "Store", lambda loc: self.store),
                                 (tick.time, "sleep", lambda n: None)):
            p = mock.patch.object(mod, name, value, create=True)  # create: the test runs on the code before too
            p.start()
            self.addCleanup(p.stop)

    def git(self, cwd, *args):
        return subprocess.run(["git", "-C", cwd, *args], capture_output=True, text=True, check=True).stdout.strip()

    def end(self, verb, k):
        self.live = [x for x in self.live if x is not k]
        return subprocess.CompletedProcess([], 0)

    def agent_open(self):
        self.live = [{"address": "t1", "state": "live", "agent": "claude", "status": "idle", "cwd": self.task["worktree"],
                      "labels": {"herdr.role": "worker"}}]

    def later(self, ms):
        for k in ("done_at", "ended_at", "worktree_kept"):
            if self.task.get(k):
                self.task[k] -= ms

    def pass_(self):
        report = {}
        self.tick.end_done_pass(report)
        return report

    def test_its_worktree_goes_once_its_agent_ended(self):
        self.agent_open()
        self.github.finish(self.store, self.task)
        self.assertTrue(os.path.isdir(self.task["worktree"]))  # its agent still open: kept for now
        self.later(self.tick.DONE_GRACE_MS + 1000)
        self.pass_()                                             # the agent ends
        self.assertEqual(self.live, [])
        report = self.pass_()                                    # nothing left of it: its worktree goes
        self.assertFalse(os.path.isdir(self.task["worktree"]))
        self.assertEqual(self.git(self.main, "branch", "--list", "task/1"), "")
        self.assertEqual(report["worktrees_removed"], [{"task": "SHEP-1", "path": self.task["worktree"]}])
        self.assertEqual(self.pass_(), {})                       # then never looked at again

    def test_a_leftover_from_before_goes_on_the_next_pass(self):
        self.task.update(state="done", done_at=1000, ended_at=2000)
        self.pass_()
        self.assertFalse(os.path.isdir(self.task["worktree"]))

    def test_uncommitted_work_keeps_it(self):
        self.task.update(state="done", done_at=1000, ended_at=2000)
        with open(os.path.join(self.task["worktree"], "a.txt"), "w") as f:
            f.write("x")
        self.git(self.task["worktree"], "add", "a.txt")
        self.pass_()
        self.assertTrue(os.path.isdir(self.task["worktree"]))
        self.assertIn("worktree_kept", self.task)
        self.git(self.task["worktree"], "reset", "-q", "--hard")
        self.pass_()                                             # not looked at again at once
        self.assertTrue(os.path.isdir(self.task["worktree"]))
        self.later(3600000)  # TIDY_AGAIN_MS
        self.pass_()                                             # clean since: it goes
        self.assertFalse(os.path.isdir(self.task["worktree"]))

    def test_new_commits_after_the_merge_keep_it(self):
        self.task.update(state="done", done_at=1000, ended_at=2000)
        self.git(self.task["worktree"], "commit", "-q", "--allow-empty", "-m", "after the merge")
        self.pass_()
        self.assertTrue(os.path.isdir(self.task["worktree"]))
        self.assertEqual(self.git(self.main, "branch", "--list", "--format=%(refname:short)", "task/1"), "task/1")

    def test_a_terminal_in_it_keeps_it(self):
        self.task.update(state="done", done_at=1000, ended_at=2000)
        self.others = [{"address": "t9", "state": "live", "cwd": os.path.join(self.task["worktree"], "web")}]
        self.pass_()
        self.assertTrue(os.path.isdir(self.task["worktree"]))
        self.others = [{"address": "t9", "state": "live", "cwd": self.task["worktree"] + "-other"}]
        self.later(3600000)  # TIDY_AGAIN_MS
        self.pass_()
        self.assertFalse(os.path.isdir(self.task["worktree"]))

    def test_done_by_hand_unmerged_keeps_it(self):
        self.git(self.main, "reset", "-q", "--hard", "HEAD~1")  # its work never merged
        self.task.update(state="done", done_at=1000, ended_at=2000, merged=False)
        self.pass_()
        self.assertTrue(os.path.isdir(self.task["worktree"]))
        self.assertEqual(self.git(self.main, "branch", "--list", "--format=%(refname:short)", "task/1"), "task/1")

    def auto_merge(self, status):
        """The tick's auto-merge of a Ready task whose pull request GitHub merged: merge_now() with its
        agent at the given status (SHEP-188). Nothing but GitHub and git is stubbed."""
        from unittest import mock
        import herdr_task.commands as commands
        wt = self.task["worktree"]
        self.live = [{"address": "t1", "state": "live", "agent": "claude", "status": status, "cwd": wt,
                      "labels": {"herdr.role": "worker"}}]
        self.task.update(state="ready", merged=False, auto_merge="checks", pr={"number": 5, "repo": "o/r"})
        merged = {"state": "MERGED", "mergeCommit": {"oid": "f00"}}
        for name, value in (("pr_view", lambda t: merged), ("note_checks", lambda s, t, v: None),
                            ("watch_main", lambda *a: None), ("update_main", lambda *a: None)):
            p = mock.patch.object(commands, name, value)
            p.start()
            self.addCleanup(p.stop)
        self.assertIsNone(commands.merge_now(self.store, self.task))
        self.assertEqual(self.task["state"], "done")
        return os.path.isdir(wt)

    def test_an_auto_merge_keeps_the_folder_while_its_agent_is_mid_turn(self):
        self.assertTrue(self.auto_merge("working"))              # its turn after ready: not pulled out
        self.live = []                                           # its agent ended (end_done after the grace)
        self.later(self.tick.DONE_GRACE_MS + 1000)
        self.pass_()
        self.assertFalse(os.path.isdir(self.task["worktree"]))   # then its worktree goes

    def test_an_auto_merge_while_its_agent_asks_keeps_the_folder(self):
        self.assertTrue(self.auto_merge("blocked"))

    def test_an_auto_merge_with_its_agent_idle_still_removes_the_folder(self):
        self.assertFalse(self.auto_merge("idle"))                # as before: nothing mid-turn, gone at once


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
        with mock.patch.object(self.commands, "finish", github.finish), \
                mock.patch.object(github, "live_agents", lambda key: self.live), \
                mock.patch.object(github, "live_helpers", lambda key: []):  # the real one: worktree and branch go
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

    def test_ready_rebased_onto_origin_main_waits_for_accept(self):
        """#737: no commits of its own, its branch rebased onto origin/main while the local main is
        behind: nothing to merge and nothing to review, never an empty pull request."""
        from unittest import mock
        calls = self.github([])
        self.git(self.main, "update-ref", "refs/remotes/origin/main", self.commit(self.main, "on GitHub"))
        self.git(self.main, "reset", "-q", "--hard", "HEAD~1")  # local main behind its remote
        self.git(self.task["worktree"], "rebase", "-q", "origin/main")
        self.task["head"] = self.git(self.task["worktree"], "rev-parse", "HEAD")
        self.task["review_gate"] = True
        with mock.patch.object(self.commands, "review_limits", lambda *a: (True, 2)):
            self.assertIsNone(self.commands.review_refusal(self.store, self.task, self.task["worktree"]))
        self.assertEqual(self.ready_said(), "ready")
        self.assertTrue(self.task["nothing_to_merge"])
        self.assertNotIn(("pr", "create"), calls)

    # SHEP-117: a research task runs in the folder itself (--here), commits nothing and ends with an
    # artifact: ready waits for Accept with no branch or pull request, whatever the merge setting.
    def test_research_in_the_folder_itself_goes_ready_then_done(self):
        calls = self.github([])
        for k in ("worktree", "branch", "start"):
            self.task.pop(k)
        self.task["mode"] = "here"
        self.assertEqual(self.ready_said(), "ready")
        self.assertEqual(self.task["summary"], "Found three options.")
        self.assertNotIn("auto_merge", self.task)
        self.assertNotIn("pr", self.task)
        self.assertNotIn(("pr", "create"), calls)
        self.accept_now()
        self.assertEqual(self.task["state"], "done")
        self.assertFalse(self.task.get("merged"))
        self.assertEqual(self.git(self.main, "rev-list", "--count", "HEAD"), "1")

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
        merged_view = {"state": "MERGED", "mergeCommit": {"oid": self.ready_at}}
        for mod, name, value in ((self.commands, "pr_view", lambda t: merged_view if self.merges else view),
                                 (self.commands, "finish", github.finish),
                                 (self.commands, "gh", lambda *a, **k: (self.merges.append(a), (True, "", ""))[1]),
                                 (self.commands, "watch_main", lambda *a: None), (self.commands, "update_main", lambda *a: None),
                                 (github, "live_agents", lambda key: []), (github, "live_helpers", lambda key: [])):
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

    def test_a_status_at_least_every_5_minutes_its_time_left_revised_honestly(self):
        """SHEP-214: the system prompt and the brief's footer both ask for a status at least every 5
        minutes and an honest --left each time; an unedited earlier template gets today's words."""
        from herdr_task import templates
        with open(os.path.join(BIN, "herdr_task", "launch.py")) as f:
            launch = f.read()
        with open(os.path.join(ROOT, "templates", "worker.md")) as f:
            worker = f.read()
        for words in (templates.WORKER_MD, worker, launch):
            self.assertIn("at least every 5 minutes while you work", words)
            self.assertIn("revise it honestly", words)
            self.assertNotIn("update it at every step", words)
        with open(os.path.join(ROOT, "templates", "worker.md")) as f:
            line = [x for x in f.read().splitlines() if x.startswith("- Say how it's going")]
        self.assertEqual(line, [x for x in templates.WORKER_MD.splitlines() if x.startswith("- Say how it's going")])
        self.assertIn(templates.WORKER_MD_BEFORE_OFTEN, templates.OLD_WORKERS)
        self.assertNotEqual(templates.WORKER_MD_BEFORE_OFTEN, templates.WORKER_MD)


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
            self.assertIn("codex --no-daemon --sandbox read-only", words)
            self.assertIn("claude --permission-mode plan", words)
            self.assertIn("exactly as the Reviewer line gives it", words)
            self.assertIn("hc wait <its name>", words)
            self.assertNotIn("< /dev/null", words)
        self.assertIn(templates.WORKER_MD_BEFORE_SUBAGENTS, templates.OLD_WORKERS)
        self.assertIn(templates.WORKER_MD_BEFORE_COMMAND, templates.OLD_WORKERS)
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

    def test_the_reviewer_says_which_checks_its_sandbox_cant_run(self):
        """#840: herdr-app check hangs and doctor can't reach the service in Codex's read-only sandbox."""
        sys.path.insert(0, BIN)
        from unittest import mock
        from herdr_task import templates
        with open(os.path.join(ROOT, "templates", "reviewer.md")) as f:
            words = f.read()
        for w in (words, templates.REVIEWER_MD):
            self.assertIn("`herdr-app check` hangs there", w)
            self.assertIn("Not run in my sandbox:", w)
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "reviewer.md"), "w") as f:
            f.write(templates.TEMPLATE_HEADS["reviewer"] + templates.REVIEWER_MD_BEFORE_SANDBOX)
        with mock.patch.object(templates, "TEMPLATES", d):
            self.assertEqual(templates.instructions("reviewer"), templates.REVIEWER_MD.strip())



class ReviewerLine(unittest.TestCase):
    """#745: the brief's Reviewer line gives the reviewer's whole command (Codex's on its own app
    server, --no-daemon), and a CLI the sign-in check says is signed out here never reviews: the
    task's own CLI does, and the line says how to bring the other back."""

    def setUp(self):
        sys.path.insert(0, BIN)
        from unittest import mock
        from herdr_task import settings, takeover
        self.settings = settings
        d = tempfile.mkdtemp()
        self.checks = os.path.join(d, "cli-check.json")
        with open(os.path.join(d, "settings.json"), "w") as f:
            json.dump({"everywhere": {"tasks": {"review_rounds": 2}}}, f)
        for p in (mock.patch.object(settings, "SETTINGS", os.path.join(d, "settings.json")),
                  mock.patch.object(takeover, "CLI_CHECK", self.checks),
                  mock.patch.object(settings.shutil, "which", lambda name: f"/usr/bin/{name}")):
            p.start()
            self.addCleanup(p.stop)

    def line(self, codex):
        with open(self.checks, "w") as f:
            json.dump({"machines": {"": {"claude": "ready", "codex": codex}}}, f)
        t = {"id": 1, "title": "x", "mode": "worktree"}
        return next(x for x in self.settings.task_settings(Store([t]), t, argv=["claude"]) if x["label"] == "Reviewer")

    def test_codex_runs_on_its_own_server(self):
        r = self.line("ready")
        self.assertEqual(r["value"]["agent"], "codex")
        self.assertTrue(r["value"]["command"].startswith("codex --no-daemon --sandbox read-only --ask-for-approval never"))
        self.assertIn("(the other CLI); start it as `codex --no-daemon", r["text"])

    def test_signed_out_codex_never_reviews(self):
        r = self.line("signed-out")
        self.assertEqual(r["value"]["agent"], "claude")
        self.assertTrue(r["value"]["command"].startswith("claude --permission-mode plan"))
        self.assertIn("Codex isn't signed in here", r["text"])
        self.assertIn("`codex login`", r["text"])

    def test_an_unknown_check_keeps_codex(self):
        self.assertEqual(self.line(None)["value"]["agent"], "codex")

    def test_codex_skips_its_trust_and_hooks_screens(self):
        """#798, #753: a reviewer in a task's worktree never stops on "Trust this folder?" (its repo's
        root and the worktree trusted for this run, as a table: the path has dots) or "Hooks need review"."""
        import shlex
        d = os.path.realpath(tempfile.mkdtemp())
        main, wt = os.path.join(d, "re.po"), os.path.join(d, ".local", "wt")
        subprocess.run(["git", "init", "-q", main], check=True)
        subprocess.run(["git", "-C", main, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q",
                        "--allow-empty", "-m", "x"], check=True)
        subprocess.run(["git", "-C", main, "worktree", "add", "-q", wt, "-b", "t1"], check=True)
        cmd = shlex.split(self.settings.reviewer_command("codex", folder=wt))
        self.assertIn("--disable", cmd)
        self.assertEqual(cmd[cmd.index("--disable") + 1], "hooks")
        trust = cmd[cmd.index("-c") + 1]
        self.assertEqual(trust, "projects={%s={trust_level=\"trusted\"},%s={trust_level=\"trusted\"}}"
                         % (json.dumps(wt), json.dumps(main)))



class ReadOnlyStore(unittest.TestCase):
    """#841: in a read-only sandbox (a Codex reviewer's) show and list read the task list (write="try");
    a change is refused with a clear message, never a traceback."""

    def test_reads_without_a_writable_lock(self):
        import builtins
        import errno
        from unittest import mock
        sys.path.insert(0, BIN)
        from herdr_task import store
        d = tempfile.mkdtemp()
        folder = os.path.join(d, "proj")
        os.makedirs(folder)
        loc = {"folder": folder, "kind": "folder", "name": "proj", "common": None}
        with mock.patch.object(store, "STORE", os.path.join(d, "tasks")):
            with store.Store(loc) as s:
                s.data["tasks"].append({"id": 1, "title": "x", "state": "running"})
                s.dirty = True
            real = builtins.open

            def ro(path, mode="r", *a, **k):
                if any(c in mode for c in "wax+"):
                    raise OSError(errno.EROFS, "Read-only file system", path)
                return real(path, mode, *a, **k)
            with mock.patch.object(store, "open", ro, create=True):
                for write in ("try", False):
                    with store.Store(loc, write=write) as s:
                        self.assertEqual([t["id"] for t in s.data["tasks"]], [1])
                        s.dirty = True  # what show learns is kept only where it can be
                with self.assertRaises(store.Fail) as e:
                    with store.Store(loc):
                        pass
                self.assertIn("read-only", str(e.exception))


class ReadOnlyAutopilot(unittest.TestCase):
    """#900: autopilot list and show read the autopilot state in a read-only sandbox (a Codex
    reviewer's) without a writable lock; a change is refused with a clear message, never a traceback."""

    def test_reads_without_a_writable_lock(self):
        import builtins
        import errno
        from unittest import mock
        sys.path.insert(0, BIN)
        from herdr_task import autopilot, store
        d = tempfile.mkdtemp()
        stash = os.path.join(d, "tasks")
        os.makedirs(stash)
        state_file = os.path.join(stash, ".autopilot.json")
        with open(state_file, "w") as f:
            json.dump({"version": 2, "spaces": {"sp1": {"on": True}}}, f)
        real = builtins.open

        def ro(path, mode="r", *a, **k):
            if any(c in mode for c in "wax+"):
                raise OSError(errno.EROFS, "Read-only file system", path)
            return real(path, mode, *a, **k)
        with mock.patch.object(autopilot, "STORE", stash), mock.patch.object(autopilot, "STATE", state_file), \
                mock.patch.object(store, "STORE", stash), mock.patch.object(store, "open", ro, create=True), \
                mock.patch.object(autopilot, "open", ro, create=True):
            # No lock file yet (read-only, so it can't be made): a read still works.
            with autopilot.state(write=False) as st:
                self.assertTrue(st["spaces"]["sp1"]["on"])
            # With a lock file there, a read takes it shared.
            open(os.path.join(stash, ".autopilot.lock"), "w").close()
            with autopilot.state(write=False) as st:
                self.assertTrue(st["spaces"]["sp1"]["on"])
            with self.assertRaises(autopilot.Fail) as e:
                with autopilot.state(write=True):
                    pass
            self.assertIn("read-only", str(e.exception))


class PrLooks(unittest.TestCase):
    """A Ready task's pull request as the tick sees it. A fake gh (what HERDR_TASK_GH points herdr-task at:
    github.GH here, its store a throwaway) answers each look (gh_script())."""

    def gh_script(self):
        return ("#!/usr/bin/env python3\nimport json, sys\n"
                f"open({self.gh_log!r}, 'a').write(json.dumps(sys.argv[1:]) + '\\n')\n"
                "if sys.argv[1:3] == ['pr', 'view']:\n"
                f"    print(open({os.path.join(self.dir, 'view.json')!r}).read())\n"
                "elif sys.argv[1] == 'api':\n    print('[]')\n")

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.agents as agents
        import herdr_task.commands as commands
        import herdr_task.ghnews as ghnews
        import herdr_task.github as github
        import herdr_task.news as news
        import herdr_task.tick as tick
        self.agents, self.commands, self.ghnews, self.news, self.tick = agents, commands, ghnews, news, tick
        self.dir = tempfile.mkdtemp()
        self.gh_log = os.path.join(self.dir, "gh.log")
        gh = os.path.join(self.dir, "gh")
        with open(gh, "w") as f:
            f.write(self.gh_script())
        os.chmod(gh, 0o755)
        self.clock = [50_000_000]
        self.live = [{"address": "t1", "agent": "claude", "status": {"state": "idle"}, "turn": 6, "status_ms": 1,
                      "labels": {"herdr.role": "worker"}}]
        self.sent = []
        self.store = Store([])
        for mod, name, value in ((github, "GH", gh), (github, "STORE", self.dir),
                                 (tick, "now", lambda: self.clock[0]), (agents, "now", lambda: self.clock[0]),
                                 (github, "now", lambda: self.clock[0]), (ghnews, "now", lambda: self.clock[0]),
                                 (commands, "now", lambda: self.clock[0]),
                                 (commands, "live_agents", lambda key: self.live), (commands, "detail", lambda k: k),
                                 (tick, "live_agents", lambda key: self.live), (tick, "detail", lambda k: k),
                                 (ghnews, "news_send", lambda to, text: self.sent.append((to, text)) or True)):
            p = mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)

    def said_ready(self, **more):
        """A task its agent just said ready for: its pull request opened (as finalize leaves it)."""
        t = {"id": 9, "title": "Login", "state": "running", "space": "sp1", "agents": [], "mode": "worktree",
             "worktree": self.dir, "branch": "b", "set": {"merge": "ask"}, **more}
        self.store.data["tasks"] = [t]
        opened = {"number": 41, "url": "https://github.com/o/r/pull/41", "repo": "o/r"}
        with unittest.mock.patch.object(self.commands, "github", lambda s: "o/r"), \
                unittest.mock.patch.object(self.commands, "pr_open", lambda s, t, repo, summary=None: t.setdefault("pr", dict(opened))):
            msg = self.commands.finalize(self.store, t)
        self.assertIn("shows Working until its checks pass", msg)
        return t

    def github_says(self, mergeable="MERGEABLE", checks=()):
        with open(os.path.join(self.dir, "view.json"), "w") as f:
            json.dump({"state": "OPEN", "mergeable": mergeable, "headRefOid": "abc", "statusCheckRollup": list(checks)}, f)

    def look(self, t, after=60_000):
        """A tick's look at the task, after this long; (status, needs, why) and the news event then."""
        self.clock[0] += after
        t["pr"]["checked"] = 0
        self.tick.facts(self.store, t, {"attention": []})
        v = {**dict(zip(("status", "needs", "why"), self.agents.task_status(t, self.live))), "own_work": False}
        with unittest.mock.patch.object(self.news, "view", lambda s, t, live=None: v):
            event = self.news.news_state(self.store, t, self.live)[1]
        return v["status"], v["why"], event



class ChecksBeforeReady(PrLooks):
    """SHEP-175: a task said ready in a GitHub repo is Working (never Ready nor Needs you, no ready
    news) until its pull request's checks are done and GitHub knows it can merge."""

    RUNNING = {"name": "check", "status": "IN_PROGRESS", "conclusion": ""}
    PASSED = {"name": "check", "status": "COMPLETED", "conclusion": "SUCCESS"}
    FAILED = {"name": "check", "status": "COMPLETED", "conclusion": "FAILURE", "detailsUrl": ""}

    def test_working_while_checks_run_then_ready(self):
        t = self.said_ready()
        self.assertEqual(self.agents.task_status(t, self.live), ("working", None, "Checks running"))
        self.github_says(checks=[self.RUNNING])
        self.assertEqual(self.look(t), ("working", "Checks running", None))
        self.github_says(checks=[self.PASSED])
        self.assertEqual(self.look(t), ("ready", None, "ready"))
        with open(self.gh_log) as f:
            self.assertTrue(any(a[:2] == ["pr", "view"] for a in map(json.loads, f)))

    def test_github_still_working_out_mergeable_is_working(self):
        t = self.said_ready()
        self.github_says(mergeable="UNKNOWN", checks=[self.PASSED])
        self.assertEqual(self.look(t), ("working", "Waiting for GitHub", None))

    def test_a_failing_check_goes_back_to_its_agent_never_ready_first(self):
        t = self.said_ready()
        self.github_says(checks=[self.FAILED])
        self.assertEqual(self.look(t, after=1000), ("working", "Back to its agent to fix", None))
        self.assertEqual(t["attention"], "Checks failing: check.")
        # handed to its resting agent at once (pr_news, pr_deliver): it is on it, still Working past the grace
        self.tick.pr_deliver(self.store, t, {})
        self.assertEqual(self.sent[-1][0], "t1")
        self.assertIn('Check \\"check\\" failed', json.dumps(self.sent[-1][1]))
        self.clock[0] += 30_000
        self.assertEqual(self.agents.task_status(t, self.live)[0], "working")
        # it never fixes it and stays quiet: Needs you, as only the person can act now
        self.clock[0] += 10 * 60_000
        self.assertEqual(self.agents.task_status(t, self.live), ("needs", "attention", "Checks failing: check."))

    def test_a_conflict_goes_back_to_its_agent(self):
        t = self.said_ready(base="main")
        self.github_says(mergeable="CONFLICTING", checks=[self.PASSED])
        self.assertEqual(self.look(t, after=1000), ("working", "Back to its agent to fix", None))
        self.tick.pr_deliver(self.store, t, {})
        self.assertIn("It conflicts with main: rebase onto main", self.sent[-1][1])
        self.assertEqual(self.agents.task_status(t, self.live)[0], "working")

    def test_no_checks_is_ready_once_github_had_time_to_start_them(self):
        t = self.said_ready()
        self.github_says(checks=[])
        self.assertEqual(self.look(t, after=5000), ("working", "Checks running", None))
        self.assertEqual(self.look(t, after=30_000), ("ready", None, "ready"))

    def test_merging_by_itself_is_working_then_done(self):
        t = self.said_ready()
        t["auto_merge"] = "checks"
        self.assertEqual(self.agents.task_status(t, self.live), ("working", None, "Merges once its checks pass"))
        t["merge_requested"] = 1
        self.assertEqual(self.agents.task_status(t, self.live), ("working", None, "Merging"))


class ChecksUnreadable(PrLooks):
    """SHEP-179: a GitHub token that reads the pull request but not its checks (the exe.dev GitHub integration's):
    gh pr view with statusCheckRollup fails with GitHub's own GraphQL error, and the REST check runs with a 403. The
    task is never stuck Working on checks nobody can see, and Accept merges (GitHub refuses what its checks block)."""

    ERR = ("GraphQL: Resource not accessible by personal access token (repository.pullRequest.statusCheckRollup.nodes.0"
           ".commit.statusCheckRollup.contexts.nodes.0), Resource not accessible by personal access token "
           "(repository.pullRequest.statusCheckRollup.nodes.0.commit.statusCheckRollup.contexts.nodes.1)")
    DENIED = ('{"message":"Resource not accessible by personal access token","status":"403"}'
              "gh: Resource not accessible by personal access token (HTTP 403)")

    def gh_script(self):
        d = self.dir
        return ("#!/usr/bin/env python3\nimport json, os, sys\n"
                f"a = sys.argv[1:]\nopen({self.gh_log!r}, 'a').write(json.dumps(a) + '\\n')\n"
                f"def file(n):\n    p = os.path.join({d!r}, n)\n    return open(p).read() if os.path.exists(p) else None\n"
                "if a[:2] == ['pr', 'view'] and 'statusCheckRollup' in a[-1]:\n"
                f"    sys.stderr.write({self.ERR!r}); sys.exit(1)\n"
                "elif a[:2] == ['pr', 'view']:\n    print(file('view.json'))\n"
                "elif a[:2] == ['pr', 'merge']:\n"
                f"    open(os.path.join({d!r}, 'view.json'), 'w').write(json.dumps({{'state': 'MERGED', 'mergeCommit': {{'oid': 'f00'}}}}))\n"
                "elif a[0] == 'api' and a[1].endswith('/status'):\n"
                "    print(file('status.json') or json.dumps({'state': 'pending', 'statuses': []}))\n"
                "elif a[0] == 'api' and '/check-runs' in a[1] and file('runs.json'):\n    print(file('runs.json'))\n"
                "elif a[0] == 'api':\n"
                f"    print({self.DENIED!r}); sys.stderr.write('gh: Resource not accessible by personal access token (HTTP 403)'); sys.exit(1)\n")

    def github_says(self, merge_state="CLEAN", mergeable="MERGEABLE", runs=None):
        with open(os.path.join(self.dir, "view.json"), "w") as f:
            json.dump({"state": "OPEN", "mergeable": mergeable, "headRefOid": "abc", "mergeStateStatus": merge_state,
                       "reviewDecision": ""}, f)
        if runs is not None:
            with open(os.path.join(self.dir, "runs.json"), "w") as f:
                json.dump({"check_runs": runs}, f)

    def test_the_view_reads_the_rest_and_says_checks_are_unknown(self):
        import herdr_task.github as github
        self.github_says()
        v = github.pr_view({"pr": {"number": 7, "repo": "o/r"}})
        self.assertEqual((v["state"], v["mergeable"], v["statusCheckRollup"], v["checks_unknown"]), ("OPEN", "MERGEABLE", [], True))
        with open(self.gh_log) as f:
            calls = [json.loads(x) for x in f]
        self.assertIn(["api", "repos/o/r/commits/abc/check-runs?per_page=100"], calls)

    def test_ready_with_a_note_when_github_says_clean(self):
        t = self.said_ready()
        self.github_says("CLEAN")
        self.assertEqual(self.look(t, after=5000)[:2], ("working", "Checks running"))  # GitHub may not have started them
        self.assertEqual(self.look(t, after=30_000), ("ready", None, "ready"))
        self.assertTrue(t["pr"]["checks_unknown"])
        self.assertEqual(self.store.data["github_checks"]["repo"], "o/r")
        import herdr_task.proof as proof
        self.assertEqual(proof.checks_of(t), {"state": "unknown", "text": "Can't read GitHub checks here: check them on GitHub."})

    def test_blocked_waits_a_while_then_is_ready_anyway(self):
        t = self.said_ready()
        self.github_says("BLOCKED")
        self.assertEqual(self.look(t, after=60_000)[:2], ("working", "Checks running"))
        self.assertEqual(self.look(t, after=10 * 60_000)[:2], ("working", "Checks running"))
        self.assertEqual(self.look(t, after=10 * 60_000), ("ready", None, "ready"))

    def test_rest_check_runs_still_count_when_graphql_refuses(self):
        t = self.said_ready()
        self.github_says("BLOCKED", runs=[{"name": "test", "status": "in_progress", "conclusion": None}])
        self.assertEqual(self.look(t)[:2], ("working", "Checks running"))
        self.github_says("CLEAN", runs=[{"name": "test", "status": "completed", "conclusion": "success"}])
        self.assertEqual(self.look(t), ("ready", None, "ready"))
        self.assertNotIn("checks_unknown", t["pr"])  # REST read them all: known
        self.assertNotIn("github_checks", self.store.data)
        self.github_says("BLOCKED", runs=[{"name": "test", "status": "completed", "conclusion": "failure"}])
        self.assertEqual(self.look(t, after=1000)[:2], ("working", "Back to its agent to fix"))
        self.assertEqual(t["attention"], "Checks failing: test.")

    def test_accept_merges_without_check_access(self):
        t = self.said_ready()
        t["state"] = "ready"
        self.github_says("CLEAN")
        for name in ("watch_main", "update_main"):
            p = unittest.mock.patch.object(self.commands, name, lambda *a, **k: None)
            p.start()
            self.addCleanup(p.stop)
        p = unittest.mock.patch.object(self.commands, "finish", lambda s, t, even_if_busy=False: t.update(state="done"))
        p.start()
        self.addCleanup(p.stop)
        self.assertIsNone(self.commands.merge_now(self.store, t))
        self.assertEqual((t["state"], t.get("merged")), ("done", True))
        with open(self.gh_log) as f:
            self.assertTrue(any(a[:2] == ["pr", "merge"] for a in map(json.loads, f)))

    def test_a_network_blip_is_not_taken_for_a_permission(self):
        import herdr_task.github as github
        self.assertFalse(github.denied("error connecting to api.github.com"))
        self.assertTrue(github.denied(self.ERR))
        self.assertTrue(github.denied(self.DENIED))


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
        import herdr_task.agents as agents
        import herdr_task.commands as commands
        import herdr_task.launch as launch
        import herdr_task.templates as templates
        import herdr_task.settings as settings
        import contract_gate_evidence as contract
        self.commands, self.contract, self.real_tell = commands, contract, commands.tell
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
                                 (commands, "tell", lambda s, t, msg, args, start=True: self.told.append(msg) or ("w1", False)),
                                 (launch, "run", hc_run), (launch, "hc_json", lambda *a: None),
                                 (launch, "low_priority", lambda argv, name: argv), (templates, "TEMPLATES", tdir),
                                 (settings, "SETTINGS", os.path.join(tdir, "none.json")),
                                 (launch, "UNSANDBOXED_FLAG", os.path.join(tdir, "none")),
                                 (commands, "hc_json", lambda *a: self.reviewer_term), (agents, "hc_json", lambda *a: None),
                                 (commands, "run", lambda argv, **k: subprocess.CompletedProcess(argv, 0, "", "")),
                                 (agents, "run", lambda argv, **k: subprocess.CompletedProcess(argv, 0, "", "")),
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
        self.assertEqual([(r["round"], r["cli"]) for r in self.task["review_runs"]], [(1, "agy"), (1, "claude")])
        self.assertEqual(self.commands.rounds_used(self.task, True), 1)
        self.valid()

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
        from herdr_task import agents, output
        self.task["started"] = 1
        self.ready()
        self.assertTrue(agents.starting(self.task))
        # FAC-66: past STARTING_MS with its worker resting after ready, an open review round for head stays Working.
        self.task["review_runs"][0]["at"] = self.task["progress"]["at"] = agents.now() - 5 * 60_000
        self.assertFalse(agents.starting(self.task))
        idle = [{"address": "w1", "state": "live", "status": {"state": "done"}, "foreground": {"name": "agy"},
                 "labels": {"herdr.agent": "agy", "herdr.role": "worker"}}]
        self.assertEqual(agents.task_status(self.task, idle), ("working", None, None))
        v = output.view(self.store, self.task, idle)
        self.assertEqual((v["status"], v["needs"], v["own_work"]), ("working", None, False))
        self.assertIn("(Herdr's reviewer is on it)", output.line(v))
        # Once the round records changes, an idle worker that hasn't taken them up is Needs you again.
        self.verdict("term-1", passed=False, changes="fix edge case", must_fix=1)
        self.assertEqual(agents.task_status(self.task, idle)[:2], ("needs", "reply"))

    def codex_run(self, model):
        """Starts a Codex gate reviewer; returns (its argv, the prompt held for it)."""
        from unittest import mock
        import herdr_task.launch as launch
        sent = []
        self.task["reviewer"], self.task["reviewer_model"] = "codex", model
        with mock.patch.object(launch, "send_when_clear", lambda address, message: sent.append(message)):
            self.ready()
        return self.runs[0][self.runs[0].index("--") + 1:], sent

    def test_a_codex_reviewer_runs_unattended_on_the_worktree(self):
        """FAC-80: its worktree and repo root trusted, nothing asks (never, workspace-write so the check and
        `herdr-task review` run), and it is told not to escalate."""
        cmd, sent = self.codex_run("gpt-5.5")
        self.assertEqual(cmd[cmd.index("-a") + 1], "never")
        self.assertEqual(cmd[cmd.index("-s") + 1], "workspace-write")
        self.assertEqual(cmd[cmd.index("--disable") + 1], "hooks")
        trust = next(a for a in cmd if a.startswith("projects="))
        scratch = cmd[cmd.index("-C") + 1]  # not the worktree: that stays read-only to it
        self.assertNotEqual(scratch, self.wt)
        for root in (self.wt, os.path.realpath(self.main), scratch):  # the repo's root as git resolves it
            self.assertIn(json.dumps(root) + '={trust_level="trusted"}', trust)
        self.assertEqual(cmd[cmd.index("-m") + 1], "gpt-5.5")
        self.assertIn(self.head, sent[0])
        self.assertIn("never with sandbox_permissions", sent[0])
        self.assertNotIn(sent[0], cmd)  # not on the command line: it waits for the trust screen

    def test_another_clis_model_is_not_passed_to_codex(self):
        cmd, _ = self.codex_run("claude-sonnet-5-5-high")
        self.assertNotIn("-m", cmd)
        reviewer = next(x for x in self.settings_rows() if x["label"] == "Reviewer")
        self.assertIn("claude-sonnet-5-5-high is another CLI's", reviewer["text"])
        self.assertIsNone(reviewer["value"]["model"])

    def settings_rows(self):
        from herdr_task import settings
        return settings.task_settings(self.store, self.task)

    def test_the_prompt_waits_for_codex_trust_screen(self):
        from unittest import mock
        import herdr_task.launch as launch
        screens = iter(["", "Starting Codex...", "Trust this folder? 1. Yes, continue", "Trust this folder? 1. Yes, continue",
                        "› Ask Codex"])
        calls = []

        def hc(argv, **k):
            calls.append(argv[1])
            return subprocess.CompletedProcess(argv, 0, next(screens) if argv[1] == "read" else "", "")
        with mock.patch.object(launch, "run", hc), mock.patch.object(launch.time, "sleep", lambda n: None):
            launch.send_when_clear("t1", "THE PROMPT")
        self.assertEqual(calls, ["read", "read", "read", "keys", "read", "read", "send"])  # answered once, sent last

    def test_the_prompt_is_never_sent_while_the_trust_screen_stays(self):
        from unittest import mock
        import herdr_task.launch as launch
        calls = []

        def hc(argv, **k):
            calls.append(argv[1])
            return subprocess.CompletedProcess(argv, 0, "Trust this folder?" if argv[1] == "read" else "", "")
        with mock.patch.object(launch, "run", hc), mock.patch.object(launch.time, "sleep", lambda n: None):
            with self.assertRaises(launch.Fail):
                launch.send_when_clear("t1", "THE PROMPT", tries=3)
        self.assertNotIn("send", calls)
        self.assertEqual(calls[-1], "kill")

    def test_a_claude_reviewer_runs_in_auto_mode_so_its_verdict_can_run(self):
        self.task["reviewer"], self.task["reviewer_model"] = "claude", None
        self.ready()
        cmd = self.runs[0][self.runs[0].index("--") + 1:]
        self.assertEqual(cmd[cmd.index("--permission-mode") + 1], "auto")  # plan mode blocks `herdr-task review`

    def test_a_claude_reviewer_never_auto_updates_but_a_codex_one_has_no_switch(self):
        """FAC-87: Claude has DISABLE_AUTOUPDATER; Codex has none to set."""
        self.task["reviewer"], self.task["reviewer_model"] = "claude", None
        self.ready()
        self.assertIn("DISABLE_AUTOUPDATER=1", self.runs[0])

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

    # ---- FAC-76: whether agy reviewers run sandboxed is a setting (tasks.reviewer_sandbox), the flag file counts as off ----

    def box_settings(self, **layers):
        """Writes the settings file (a patched path): layers like everywhere={...} or space={...} hold tasks values."""
        from herdr_task import settings
        d = {"everywhere": {"tasks": layers["everywhere"]}} if "everywhere" in layers else {}
        if "space" in layers:
            d["spaces"] = {"sp1": {"folder": self.main, "tasks": layers["space"]}}
        with open(settings.SETTINGS, "w") as f:
            json.dump(d, f)

    def box_row(self):
        from herdr_task import settings
        row = {x["label"]: x for x in settings.task_settings(self.store, self.task)}["Reviewer sandbox"]
        block = [x for x in settings.settings_block(self.store, self.task).splitlines() if x.startswith("- Reviewer sandbox")]
        return row["value"], row["by"], block[0]

    def reviewer_is_sandboxed(self):
        import herdr_task.launch as launch
        self.runs.clear()
        self.ready()
        return launch.SANDBOX_SH in self.runs[0]

    def test_the_reviewer_sandbox_is_on_by_default(self):
        self.sandbox()
        self.assertEqual(self.box_row()[:2], (True, None))
        self.assertIn("on: the agy reviewer runs in a sandbox", self.box_row()[2])
        self.assertTrue(self.reviewer_is_sandboxed())

    def test_the_setting_off_runs_the_agy_reviewer_as_itself(self):
        self.sandbox()
        self.box_settings(space={"reviewer_sandbox": False})
        value, by, text = self.box_row()
        self.assertEqual((value, by), (False, "folder"))
        self.assertTrue(text.endswith("outside the sandbox (this folder's setting)"), text)
        self.assertFalse(self.reviewer_is_sandboxed())
        self.assertIn("agy", self.runs[0])
        self.assertIn("--dangerously-skip-permissions", self.runs[0])

    def test_the_setting_everywhere_off(self):
        self.sandbox()
        self.box_settings(everywhere={"reviewer_sandbox": False})
        self.assertEqual(self.box_row()[:2], (False, "all"))
        self.assertTrue(self.box_row()[2].endswith("(your setting everywhere)"))
        self.assertFalse(self.reviewer_is_sandboxed())

    def test_a_folders_own_on_wins_over_everywhere_off(self):
        self.sandbox()
        self.box_settings(everywhere={"reviewer_sandbox": False}, space={"reviewer_sandbox": True})  # the default says nothing
        self.assertEqual(self.box_row()[:2], (True, None))
        self.assertTrue(self.reviewer_is_sandboxed())

    def test_the_flag_file_counts_as_off_and_says_so(self):
        from unittest import mock
        import herdr_task.launch as launch
        self.sandbox()
        flag = os.path.join(self.root, "agy-review-unsandboxed")
        open(flag, "w").close()
        self.box_settings(space={"reviewer_sandbox": True})  # the flag still wins: tonight's machines run unchanged
        with mock.patch.object(launch, "UNSANDBOXED_FLAG", flag):
            self.assertEqual(self.box_row()[:2], (False, "flag"))
            self.assertTrue(self.box_row()[2].endswith("(the flag file ~/.herdr/agy-review-unsandboxed)"))
            self.assertFalse(self.reviewer_is_sandboxed())

    def test_no_reviewer_sandbox_line_for_a_reviewer_that_is_never_sandboxed(self):
        from herdr_task import settings
        self.task["reviewer"] = "claude"
        self.assertNotIn("Reviewer sandbox", [x["label"] for x in settings.task_settings(self.store, self.task)])

    def test_a_sandboxed_sign_in_failure_names_the_setting_to_turn_off(self):
        self.sandbox()
        self.ready()
        self.screen("  │  Sign in required                              │  \n")
        self.reviewer_term = {"state": "live", "agent": "agy", "status": {"state": "blocked", "kind": "auth"}, "turn": 0}
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(self.task["review_failures"][0]["why"], "Sign in required: turn Reviewer sandbox off")

    def test_the_sign_in_failure_of_an_unsandboxed_reviewer_has_no_such_hint(self):
        self.sandbox()
        self.box_settings(space={"reviewer_sandbox": False})
        self.ready()
        self.screen("  │  Sign in required                              │  \n")
        self.reviewer_term = {"state": "live", "agent": "agy", "status": {"state": "blocked", "kind": "auth"}, "turn": 0}
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(self.task["review_failures"][0]["why"], "Sign in required")

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

    def test_a_pass_tells_the_worker_starts_the_next_round_and_final_pass_tells_merging(self):
        from unittest import mock
        from herdr_task import agents
        self.ready()
        self.verdict("term-1")
        self.assertEqual(len(self.task["review_runs"]), 2)
        self.assertEqual(self.told, [
            "Herdr's review round 1 of SHEP-13 passed (1 of 2 passing). Round 2 has started; its findings come to you as a message, nothing to do meanwhile."
        ])
        self.commands.tell_pass(self.store, self.task, 1, "duplicate")
        self.assertEqual(len(self.told), 1)  # never sent twice for the same round
        # Final needed pass: tells merging (with what finalize did) in one plain line, and its ack turn doesn't mark Ready as Working again.
        idle = [{"address": "w1", "status": "idle", "turn": 6, "labels": {}}]
        with mock.patch.object(self.commands, "live_agents", lambda key: idle), \
                mock.patch.object(self.commands, "ready_turns", agents.ready_turns):
            self.verdict("term-2")
        self.assertEqual(self.task["state"], "ready")
        self.assertEqual(len(self.told), 2)
        self.assertNotIn("\n", self.told[-1])
        self.assertTrue(self.told[-1].startswith("Herdr's review round 2 of SHEP-13 passed: SHEP-13 goes on to merging (2 of 2 passing). SHEP-13 is ready."))
        self.assertIn("Tasks panel", self.told[-1])
        self.commands.tell_pass(self.store, self.task, 2, "duplicate")
        self.assertEqual(len(self.told), 2)
        self.assertFalse(agents.busy_again(self.task, [{"address": "w1", "status": "working", "turn": 6, "labels": {}}]))
        self.assertTrue(agents.busy_again(self.task, [{"address": "w1", "status": "working", "turn": 7, "labels": {}}]))

    def test_a_pass_never_starts_a_worker_when_none_is_live(self):
        from unittest import mock
        started = []
        self.sandbox()
        self.ready()
        with mock.patch.object(self.commands, "tell", self.real_tell), \
                mock.patch.object(self.commands, "workers", lambda s, t: []), \
                mock.patch.object(self.commands, "start_again",
                                  lambda *a, **k: (started.append(k.get("message")), ({"address": "w2"}, True))[1]):
            for i in (0, 1):  # neither an intermediate pass nor the final pass starts a worker when none is live
                code = self.commands.round_code(self.task["review_runs"][i])
                self.screen(f"{code} pass\n")
                self.commands.gate_reads(self.store, self.task)
            self.assertEqual(self.task["state"], "ready")
            self.assertEqual(started, [])
            # Round 1 changes, by contrast, still starts a worker when none is live.
            self.change("another")
            for k in ("reviews", "review_runs", "verdict_told"):
                self.task.pop(k, None)
            self.ready()
            code3 = self.commands.round_code(self.task["review_runs"][0])
            self.screen(f"fix x\n{code3} changes 1\n")
            self.commands.gate_reads(self.store, self.task)
            self.assertEqual(len(started), 1)
            self.assertIn("Fix these", started[0])

    def test_end_reviewer_exits_0_without_exit_1(self):
        import time
        from types import SimpleNamespace
        from unittest import mock
        import herdr_task.launch as launch
        with mock.patch.object(self.commands, "workers", lambda s, t: [{"address": "w1"}]):
            self.ready()
        hc_argv = self.runs[0]
        self.assertIn("--no-notify", hc_argv)
        self.assertNotIn("--keep", hc_argv)
        self.assertTrue(any('trap "exit 0" HUP TERM' in a for a in hc_argv))
        self.assertFalse(any('exec "$0" -i' in a for a in hc_argv))

        real = os.path.realpath
        bindir, home, sb_root = (real(tempfile.mkdtemp(dir=self.root)) for _ in range(3))
        ready_file, log_file, target_file = (os.path.join(bindir, n) for n in ("ready", "hc.jsonl", "pid"))
        cli = os.path.join(bindir, "agy")
        with open(cli, "w") as f:
            f.write(f"#!/usr/bin/env python3\nimport time\nopen({ready_file!r}, 'w').write('ok')\ntime.sleep(30)\n")
        os.chmod(cli, 0o755)
        fake_hc = os.path.join(bindir, "hc")
        with open(fake_hc, "w") as f:
            f.write(f"""#!/usr/bin/env python3
import json, os, signal, sys
with open({target_file!r}) as fh:
    p = int(fh.read())
alive = True
try:
    os.kill(p, 0)
except ProcessLookupError:
    alive = False
with open({log_file!r}, "a") as fh:
    fh.write(json.dumps([sys.argv[1:], os.environ.get("HC_TERMINAL_ID"), alive]) + "\\n")
if sys.argv[1] == "rm":
    sys.exit(1 if alive else 0)
if sys.argv[1] == "kill" and alive:
    import subprocess as sp
    os.kill(p, signal.SIGHUP)
    for c in sp.run(["pgrep", "-P", str(p)], capture_output=True, text=True).stdout.split():
        try:
            os.killpg(int(c), signal.SIGHUP)
        except OSError:
            pass
    os.killpg(p, signal.SIGHUP)
""")
        os.chmod(fake_hc, 0o755)
        fast_sh = self.commands.END_REVIEWER_SH.replace("sleep 4", "sleep 0.05").replace("sleep 2", "sleep 0.1")
        env = {**os.environ, "HOME": home, "SHELL": "/bin/sh", "PATH": bindir + ":" + os.environ["PATH"]}

        def run_case(argv, give_pid=True):
            for p in (ready_file, log_file):
                if os.path.exists(p):
                    os.remove(p)
            proc = subprocess.Popen(argv, cwd=self.wt, env=env, start_new_session=True,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            self.addCleanup(lambda p=proc: p.poll() is None and p.kill())
            with open(target_file, "w") as fh:
                fh.write(str(proc.pid))
            for _ in range(50):
                if os.path.exists(ready_file):
                    break
                time.sleep(0.05)
            self.assertTrue(os.path.exists(ready_file))
            self.reviewer_term = {"state": "live", "parent": "w1", **({"pid": proc.pid} if give_pid else {})}
            popens = []
            real_sub = SimpleNamespace(DEVNULL=subprocess.DEVNULL,
                                       Popen=lambda *a, **k: (popens.append(subprocess.Popen(*a, **k)), popens[-1])[1])
            with mock.patch.object(self.commands, "END_REVIEWER_SH", fast_sh), \
                    mock.patch.object(self.commands, "subprocess", real_sub), \
                    mock.patch.dict(os.environ, env, clear=True):
                self.commands.end_reviewer("term-1")
            self.assertEqual(proc.wait(timeout=5), 0)  # clean exit 0, never exit 1 or signal death
            popens[0].wait(timeout=5)
            with open(log_file) as fh:
                return [json.loads(l) for l in fh]

        with mock.patch.dict(os.environ, {"SHELL": "/bin/sh"}):
            sh_argv = launch.reviewer_shell([cli])
        # 1) Local pid stopped with TERM: exits 0 on its own before the second hc rm, no hc kill needed.
        calls = run_case(sh_argv, give_pid=True)
        self.assertEqual(calls, [[["rm", "term-1"], "w1", True], [["rm", "term-1"], "w1", False]])
        # 2) Without a local pid (mock/remote hc), fallback hc kill sends SIGHUP: trap still exits 0, as parent w1.
        calls = run_case(sh_argv, give_pid=False)
        self.assertEqual([c[:2] for c in calls], [[["rm", "term-1"], "w1"], [["kill", "term-1"], "w1"], [["rm", "term-1"], "w1"]])
        # 3) Sandboxed agy reviewer on macOS: exits 0 and cleans up its sandbox temp dir.
        if sys.platform == "darwin" and os.path.exists("/usr/bin/sandbox-exec"):
            g = os.path.join(home, ".gemini", "antigravity-cli")
            os.makedirs(os.path.join(g, "cache"))
            for fn in ("antigravity-oauth-token", "cache/onboarding.json"):
                with open(os.path.join(g, fn), "w") as fh:
                    fh.write("{}")
            # Inside sandbox-exec, write the ready marker into $TMPDIR (worktree/bindir are read-only there).
            with open(cli, "w") as f:
                f.write("#!/bin/sh\necho ok > \"$TMPDIR/ready\"\nsleep 30\n")
            with mock.patch.object(launch, "SANDBOX_ROOT", sb_root), mock.patch.object(launch, "HOME", home), \
                    mock.patch.dict(os.environ, {"SHELL": "/bin/sh"}):
                sb_argv = launch.reviewer_shell(launch.sandboxed([cli, "-i", "hi"], self.wt))
            proc = subprocess.Popen(sb_argv, cwd=self.wt, env=env, start_new_session=True,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            self.addCleanup(lambda p=proc: p.poll() is None and p.kill())
            with open(target_file, "w") as fh:
                fh.write(str(proc.pid))
            for _ in range(60):
                if os.listdir(sb_root) and os.path.exists(os.path.join(sb_root, os.listdir(sb_root)[0], "ready")):
                    break
                time.sleep(0.05)
            self.assertEqual(len(os.listdir(sb_root)), 1)
            self.reviewer_term = {"state": "live", "parent": "w1", "pid": proc.pid}
            popens = []
            real_sub = SimpleNamespace(DEVNULL=subprocess.DEVNULL,
                                       Popen=lambda *a, **k: (popens.append(subprocess.Popen(*a, **k)), popens[-1])[1])
            with mock.patch.object(self.commands, "END_REVIEWER_SH", fast_sh), \
                    mock.patch.object(self.commands, "subprocess", real_sub), \
                    mock.patch.dict(os.environ, env, clear=True):
                self.commands.end_reviewer("term-1")
            self.assertEqual(proc.wait(timeout=5), 0)
            popens[0].wait(timeout=5)
            self.assertEqual(os.listdir(sb_root), [])

    # ---- FAC-59: a dead reviewer uses no round and falls back to the other CLI; two failures stop ----

    def test_a_dead_reviewer_uses_no_round_and_falls_back_to_the_other_cli(self):
        self.task["set"]["review_rounds"] = 2  # exact review_min: a dead run must not cost a round
        self.sandbox()
        self.ready()
        self.reviewer_term = {"state": "finished", "exit_code": 1}  # closed without a verdict (field note 22)
        self.commands.gate_reads(self.store, self.task)  # tick advances it without waiting on the worker
        self.assertEqual(self.told, [])
        self.assertTrue(self.commands.subprocess.Popen.called)
        self.assertEqual([(f["round"], f["address"], f["cli"], f["why"]) for f in self.task["review_failures"]],
                         [(1, "term-1", "agy", "exited (1)")])
        self.assertEqual([(r["round"], r["address"], r["cli"], r["model"]) for r in self.task["review_runs"]],
                         [(1, "term-1", "agy", "gemini-4-argon-high"), (1, "term-2", "claude", "opus")])
        cmd = self.runs[1][self.runs[1].index("--") + 1:]
        self.assertEqual(cmd[cmd.index("--permission-mode") + 1], "auto")
        self.assertEqual(self.commands.rounds_used(self.task, True), 1)
        with self.assertRaises(self.commands.Fail):
            self.verdict("term-1")  # the failed reviewer can never record a late verdict
        self.reviewer_term = {"state": "live", "agent": "agy"}
        self.verdict("term-2")  # round 1 passes on the fallback; round 2 still fits inside review_rounds=2
        self.assertEqual([(r["round"], r["cli"]) for r in self.task["review_runs"]],
                         [(1, "agy"), (1, "claude"), (2, "agy")])
        code3 = self.commands.round_code(self.task["review_runs"][2])
        self.screen(f"{code3} pass\n")
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(self.task["state"], "ready")
        self.assertEqual([(r["round"], r["by"], r["verdict"]) for r in self.task["reviews"]],
                         [(1, "term-2", "pass"), (2, "term-3", "pass")])
        self.assertEqual(self.commands.rounds_used(self.task, True), 2)
        self.valid()

    def fail_on(self, screen, status):
        self.sandbox()
        self.ready()
        self.screen(screen)
        self.reviewer_term = {"state": "live", "agent": "agy", "status": status, "turn": 1}
        self.commands.gate_reads(self.store, self.task)
        return self.task.get("review_failures") or []

    def test_a_quota_reviewer_fails_at_once_with_its_reset_time(self):
        for text, why in [
            ("⚠ Individual quota reached. Please upgrade your subscription. Resets in 10h55m44s.\n", "quota reached (resets in 10h55m44s)"),
            ("Claude usage limit reached. Your limit will reset at 3pm (Europe/Berlin).\n", "quota reached (resets at 3pm (Europe/Berlin))"),
            ("■ You've hit your usage limit. Try again at 3:04 PM.\n", "quota reached (resets at 3:04 PM)"),
            ("■ You've hit your usage limit.\n", "quota reached"),
        ]:
            with self.subTest(text=text):
                for k in ("review_runs", "review_failures", "reviews"):
                    self.task.pop(k, None)
                f = self.fail_on(text, {"state": "error"})
                self.assertEqual([(x["cli"], x["why"]) for x in f], [("agy", why)])
                self.assertIn("usage limit" if "usage" in text else "quota", f[0]["tail"])
                self.assertEqual(self.task["review_runs"][-1]["cli"], "claude")  # the next reviewer, no round used
                self.assertEqual(self.commands.rounds_used(self.task, True), 1)  # the fallback run counts, the failed one not

    def test_the_real_error_status_fails_at_once(self):
        msg = "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 10h55m44s."
        f = self.fail_on("idle prompt\n", {"state": "error", "msg": msg})
        self.assertEqual([x["why"] for x in f], ["quota reached (resets in 10h55m44s)"])

    def test_a_reviewer_in_error_status_fails_at_once(self):
        f = self.fail_on("something broke\n", {"state": "error"})
        self.assertEqual([x["why"] for x in f], ["reviewer error"])

    def test_a_working_reviewer_is_not_failed(self):
        f = self.fail_on("Reviewing the diff... usage limit handling looks fine\n", {"state": "working"})
        self.assertEqual(f, [])

    def test_a_passing_mention_of_usage_limit_is_no_quota_failure(self):
        for status in ({"state": "idle"}, {"state": "blocked", "kind": "permission"}):
            with self.subTest(status=status):
                for k in ("review_runs", "review_failures", "reviews"):
                    self.task.pop(k, None)
                f = self.fail_on("Allow the command grep -n usage limit commands.py?\n"
                                 "+QUOTA_SCREEN = re.compile(r\"(?i)individual quota reached|usage limit reached\")\n"
                                 "agy \"Individual quota reached\", Claude Code \"usage limit reached\" / \"limit will reset\"\n"
                                 "Bash(grep -n \"limit will reset\" mods/tasks/bin/herdr_task/commands.py)\n", status)
                self.assertNotIn("quota", " ".join(x["why"] for x in f))

    def test_sign_in_required_and_claude_fallback_to_agy(self):
        from herdr_task import settings
        self.sandbox()
        self.ready()
        self.screen("  │  Sign in required                              │  \n")
        self.reviewer_term = {"state": "live", "agent": "agy", "status": {"state": "blocked", "kind": "auth"}, "turn": 0}
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(self.told, [])
        self.assertEqual(self.task["review_failures"][0]["why"], "Sign in required: turn Reviewer sandbox off")
        self.assertEqual([(r["round"], r["cli"], r["model"]) for r in self.task["review_runs"]],
                         [(1, "agy", "gemini-4-argon-high"), (1, "claude", "opus")])
        # Reverse direction: when claude ends its turn with no verdict, fallback runs agy with the space's agy model.
        with open(settings.SETTINGS, "w") as f:
            json.dump({"spaces": {"sp1": {"folder": self.main, "reviewer": {"agent": "agy", "model": "gemini-4-argon-high"}}}}, f)
        self.task.update(reviewer="claude", reviewer_model="opus")
        for k in ("review_runs", "review_failures", "reviews"):
            self.task.pop(k, None)
        self.runs.clear()
        self.screen("")
        self.ready()
        self.reviewer_term = {"state": "live", "agent": "claude", "status": {"state": "idle"}, "turn": 1}
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(self.task["review_failures"][0]["why"], "ended without a verdict")
        self.assertEqual([(r["round"], r["cli"], r["model"]) for r in self.task["review_runs"]],
                         [(1, "claude", "opus"), (1, "agy", "gemini-4-argon-high")])
        self.assertIn("herdr.sandbox=1", self.runs[1])
        self.valid()

    def test_two_failures_on_one_round_stop_and_tell_the_assistant(self):
        import argparse
        from unittest import mock
        import herdr_task.agents as agents
        import herdr_task.assistant_settings as aset
        import herdr_task.home as home
        import herdr_task.news as news
        import herdr_task.reactions as reactions
        import herdr_task.store as store
        import herdr_task.tick as tick
        self.ready()
        self.reviewer_term = {"state": "gone"}
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual([r["cli"] for r in self.task["review_runs"]], ["agy", "claude"])
        self.reviewer_term = {"state": "live", "agent": "claude", "status": {"state": "done"}, "turn": 1}
        rep = {}
        with mock.patch.object(agents, "hc_json", lambda *a: {}), mock.patch.object(tick, "github", lambda s: None):
            tick.tick_store(self.store, rep)
        self.assertEqual(len(self.runs), 2)  # no third reviewer
        self.assertEqual(self.commands.rounds_used(self.task, True), 0)
        self.assertEqual(self.told, [])  # the worker isn't asked anything (field note 16)
        self.assertEqual(rep.get("attention"), [13])
        why = "Reviewer failed twice: agy (terminal gone), claude (ended without a verdict)"
        self.assertEqual(self.task["attention"], why)
        worker = [{"address": "w1", "state": "live", "agent": "agy", "status": {"state": "working"}, "labels": {}}]
        self.assertEqual(agents.task_status(self.task, worker), ("needs", "attention", why))
        # Neither a later tick nor ready on the same head loops.
        self.commands.gate_reads(self.store, self.task)
        self.ready()
        self.assertEqual(len(self.runs), 2)
        self.assertEqual(self.task["attention"], why)
        # news_pass tells the space's Assistant once via needs-you, even if the task had already gone quiet.
        sdir, sent, clock = tempfile.mkdtemp(dir=self.root), [], [10_000]
        ps = [mock.patch.object(m, n, v) for m, n, v in (
            (agents, "hc_json", lambda *a: {}), (store, "STORE", sdir), (news, "STORE", sdir),
            (news, "NEWS", os.path.join(sdir, ".news.json")), (news, "HELD", os.path.join(sdir, ".held.json")),
            (home, "NEWSLOG", os.path.join(sdir, ".newslog.json")), (aset, "settings_pass", lambda *a: None),
            (reactions, "reactions_pass", lambda *a: None), (news, "now", lambda: clock[0]),
            (news, "live_agents", lambda k: worker), (news, "detail", lambda k: k),
            (news, "coordinator_in", lambda sp: "c1"), (news, "hc_json", lambda *a: {"state": "live", "turn": 1}),
            (news, "news_send", lambda to, m, check=True: sent.append((to, m)) or True))]
        for p in ps:
            p.start()
        try:
            with store.Store(store.locate(self.main), write=True) as s:
                s.data.update(prefix="FAC", tasks=[{k: v for k, v in self.task.items() if k != "attention"}])
                s.dirty = True
            news.news_pass({}, self.main)
            with store.Store(store.locate(self.main), write=True) as s:
                s.data["tasks"][0].update(attention=why, attention_at=clock[0])
                s.dirty = True
            for _ in range(2):
                clock[0] += 10_000
                news.news_pass({}, self.main)
        finally:
            for p in ps:
                p.stop()
        self.assertEqual([to for to, _ in sent], ["c1"])
        self.assertIn('event="needs-you"', sent[0][1])
        self.assertIn(why, sent[0][1])
        # Editing --reviewer clears the block so the next tick starts the newly chosen reviewer.
        self.reviewer_term = {"state": "live", "agent": "claude"}
        with mock.patch.object(self.commands, "view", lambda s, t, live=None: {**t, "status": "running"}), \
                mock.patch.object(self.commands, "task_tabs", lambda *a: []), mock.patch.object(self.commands, "name_tabs", lambda *a: []):
            self.commands.cmd_edit(argparse.Namespace(**{k: None for k in (
                "folder", "title", "prompt", "prompt_file", "agent", "model", "effort", "reviewer_effort", "set", "base",
                "start_notes", "finish_notes", "after", "at", "in_", "branch")},
                id="SHEP-13", json=True, reviewer="claude", reviewer_model="opus", here=False, worktree=False))
        self.assertNotIn("attention", self.task)
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(len(self.runs), 3)
        self.valid()

    # ---- FAC-75: a failed reviewer's last screen is kept, shown and told ----

    def test_a_reviewer_that_ends_without_a_verdict_keeps_its_last_screen(self):
        import argparse
        from unittest import mock
        import herdr_task.commands as commands
        busy = "\n\n".join(f"line {i}" for i in range(100))
        self.sandbox()
        self.ready()
        self.screen(f"{busy}\n│  ╭───╮  │\n  Error: model refused the request  \n\n")
        self.reviewer_term = {"state": "live", "agent": "agy", "status": {"state": "idle"}, "turn": 1}
        self.commands.gate_reads(self.store, self.task)
        fail = self.task["review_failures"][0]
        self.assertEqual(fail["why"], "ended without a verdict")
        lines = fail["tail"].splitlines()
        self.assertEqual(len(lines), commands.TAIL_LINES)
        self.assertTrue(all(x.strip() for x in lines))
        self.assertEqual(lines[-1].strip(), "Error: model refused the request")
        self.assertLessEqual(len(fail["tail"].encode()), commands.TAIL_BYTES)
        self.assertNotIn("line 0", fail["tail"])
        self.assertEqual(self.task["review_runs"][-1]["cli"], "claude")  # the fallback went on as before
        self.assertEqual(commands.screen_tail("x" * 10_000).encode().__len__(), commands.TAIL_BYTES)
        # A finished (closed) terminal is read before end_reviewer closes it.
        order = []
        self.task.pop("review_failures"), self.task.pop("review_runs")
        self.runs.clear()
        self.ready()
        self.screen("Segmentation fault\n")
        self.reviewer_term = {"state": "finished", "exit_code": 139}
        with mock.patch.object(commands, "end_reviewer", lambda a: order.append(("closed", len(order)))), \
                mock.patch.object(commands, "fail_run", lambda s, t, rn, why, screen=None: order.append((why, screen))):
            self.commands.inspect_runs(self.store, self.task)
        self.assertEqual(order, [("exited (139)", "Segmentation fault\n"), ("closed", 1)])

    def test_show_and_the_needs_you_message_carry_the_reviewers_last_screen(self):
        import argparse
        import io
        from contextlib import redirect_stdout
        from unittest import mock
        import herdr_task.output as output
        fails = [{"round": 1, "address": "term-1", "cli": "agy", "why": "ended without a verdict",
                  "tail": "  │ Error: 429 model is overloaded │\nmore"},
                 {"round": 1, "address": "term-2", "cli": "claude", "why": "terminal gone"}]
        self.ready()
        self.task["review_failures"] = [{**f, "head": self.head, "at": self.commands.now() + 10 + i} for i, f in enumerate(fails)]
        self.task["review_runs"] = [{"round": 1, "head": self.head, "address": f["address"], "cli": f["cli"], "at": 1} for f in fails]
        self.commands.gate_reads(self.store, self.task)  # two failures on one round: the Assistant is told
        why = self.task["attention"]
        self.assertTrue(why.startswith("Reviewer failed twice: agy (ended without a verdict), claude (terminal gone)"), why)
        self.assertIn("agy saw: Error: 429 model is overloaded", why)  # the first meaningful line, one each
        self.assertNotIn("claude saw", why)
        self.assertEqual(self.commands.tail_gist("│ ─── │\n\n"), None)
        with mock.patch.object(self.commands, "view", output.view), mock.patch.object(self.commands, "say", output.say), \
                mock.patch.object(self.commands, "settings_block", lambda s, t: ""):
            for js in (False, True):
                out = io.StringIO()
                with redirect_stdout(out):
                    self.commands.cmd_show(argparse.Namespace(id="SHEP-13", json=js, folder=None, evidence=False))
                if js:
                    self.assertEqual(json.loads(out.getvalue())["review_failures"][0]["tail"], fails[0]["tail"])
                else:
                    lines = out.getvalue().splitlines()
                    i = next(i for i, x in enumerate(lines) if x.startswith("needs the person: Reviewer failed twice"))
                    self.assertEqual(lines[i + 1:i + 4], ["  agy reviewer's last screen (ended without a verdict):",
                                                          "      │ Error: 429 model is overloaded │", "    more"])

    def test_a_reviewer_screen_that_cant_be_read_stores_no_tail_and_the_gate_goes_on(self):
        from unittest import mock
        self.sandbox()
        self.ready()
        for broken in (lambda argv, **k: subprocess.CompletedProcess(argv, 1, "garbage", "no such terminal"),
                       mock.Mock(side_effect=OSError("hc is gone"))):
            self.task.pop("review_failures", None), self.task.pop("review_runs", None)
            self.runs.clear()
            self.reviewer_term = {"state": "live", "agent": "agy", "status": {"state": "idle"}, "turn": 1}
            with mock.patch.object(self.commands, "run", broken):
                self.ready()
                self.commands.gate_reads(self.store, self.task)
            self.assertEqual(self.task["review_failures"][0]["why"], "ended without a verdict")
            self.assertNotIn("tail", self.task["review_failures"][0])
            self.assertEqual([r["cli"] for r in self.task["review_runs"]], ["agy", "claude"])

    def test_a_live_reviewer_still_working_is_left_alone(self):
        from unittest import mock
        import herdr_task.agents as agents
        self.sandbox()
        self.ready()
        self.commands.subprocess.Popen.reset_mock()
        # 1) Mid-turn working: never even read.
        read = self.screen('+why = "Sign in required"\n> 1. ... or the screen shows "Sign in required".\n')
        self.reviewer_term = {"state": "live", "agent": "agy", "status": {"state": "working"}}
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(read, [])
        # 2) Live with echoed diff/prompt containing "Sign in required" in quotes/diff, or brief auth startup, or turn 0 booting.
        for term in (
            {"state": "live", "agent": "agy"},
            {"state": "live", "agent": "agy", "status": {"state": "blocked", "kind": "auth"}},
            {"state": "live", "agent": "agy", "status": {"state": "idle"}, "turn": 0},
        ):
            self.reviewer_term = term
            self.commands.gate_reads(self.store, self.task)
            self.ready()
            self.assertNotIn("review_failures", self.task)
            self.assertEqual(len(self.runs), 1)
            self.assertFalse(self.commands.subprocess.Popen.called)
        # 3) Stuck at turn 0 past STARTING_MS without ever starting a turn: marked failed and falls back.
        later = self.task["review_runs"][0]["at"] + agents.STARTING_MS
        with mock.patch.object(self.commands, "now", lambda: later):
            self.commands.gate_reads(self.store, self.task)
        self.assertEqual(self.task["review_failures"][0]["why"], "ended without a verdict")
        self.assertEqual([r["cli"] for r in self.task["review_runs"]], ["agy", "claude"])
        self.valid()


    # ---- FAC-79: a reviewer whose own background job still runs isn't failed ----

    def fixture(self, name):
        with open(os.path.join(ROOT, "test", "fixtures", f"fac78_{name}_reviewer_tail.txt")) as f:  # FAC-78's real last screens
            return f.read()

    def gate_at(self, clock, nudges):
        from unittest import mock
        import herdr_task.news as news
        with mock.patch.object(self.commands, "now", lambda: clock), \
                mock.patch.object(news, "send_or_hold", lambda a, m, how="send": nudges.append((a, m)) or "sent"):
            self.commands.gate_reads(self.store, self.task)

    def test_a_reviewer_done_with_a_shell_still_running_is_not_failed_and_nudged_once_past_the_limit(self):
        c = self.commands
        self.task.update(reviewer="claude", reviewer_model="opus")
        self.ready()
        self.screen(self.fixture("claude"))  # "Cooked for 2m 0s · done 3:17 AM · 1 shell still running" and "· 1 shell ·"
        self.reviewer_term = {"state": "live", "agent": "claude", "status": {"state": "done"}, "turn": 3}
        t0, nudges = self.task["review_runs"][0]["at"] + 600_000, []
        self.gate_at(t0, nudges)
        self.gate_at(t0 + c.BG_WAIT_MS - 1, nudges)
        self.assertEqual((nudges, self.task.get("review_failures"), len(self.runs)), ([], None, 1))
        self.gate_at(t0 + c.BG_WAIT_MS, nudges)  # shown too long: one nudge, still no failure
        self.gate_at(t0 + c.BG_WAIT_MS + c.NUDGE_GRACE_MS - 1, nudges)
        self.assertEqual(nudges, [("term-1", c.NUDGE_TEXT)])
        self.assertNotIn("review_failures", self.task)
        self.gate_at(t0 + c.BG_WAIT_MS + c.NUDGE_GRACE_MS, nudges)  # no verdict after the grace: failed as before
        self.assertEqual(len(nudges), 1)
        fail = self.task["review_failures"][0]
        self.assertEqual(fail["why"], "ended without a verdict")
        self.assertIn("1 shell still running", fail["tail"])  # the screen kept for FAC-75
        self.assertEqual(self.task["review_runs"][-1]["cli"], "agy")  # the fallback goes on
        self.valid()
        # Claude starts a turn by itself when the job ends: working again clears the clocks, nothing was sent.
        self.task.pop("review_failures"), self.task.pop("review_runs"), self.runs.clear()
        self.ready()
        self.gate_at(t0, nudges)
        self.assertIn("review_wait", self.task)
        self.reviewer_term["status"] = {"state": "working"}
        self.gate_at(t0 + c.BG_WAIT_MS * 3, nudges)
        self.assertEqual((len(nudges), self.task.get("review_failures")), (1, None))
        self.assertNotIn("bg_at", self.task["review_wait"])

    def test_a_reviewer_stuck_on_a_shell_for_over_an_hour_gets_exactly_one_nudge_then_fails(self):
        c = self.commands
        self.task.update(reviewer="claude", reviewer_model="opus")
        self.ready()
        self.screen(self.fixture("claude"))
        done = {"state": "live", "agent": "claude", "status": {"state": "done"}, "turn": 3}
        self.reviewer_term, nudges, t0 = dict(done), [], self.task["review_runs"][0]["at"] + 600_000
        failed_at = None
        for minute in range(0, 80):
            # A nudged reviewer works for a minute (as the real one did), then ends its turn on the same shell again.
            worked = nudges and minute == (c.BG_WAIT_MS // 60_000) + 1
            self.reviewer_term = {**done, "status": {"state": "working" if worked else "done"}}
            self.gate_at(t0 + minute * 60_000, nudges)
            if self.task.get("review_failures") and failed_at is None:
                failed_at = minute
        self.assertEqual(len(nudges), 1)  # never a second one, however long it goes on
        self.assertEqual(failed_at, c.BG_WAIT_MS // 60_000 + c.NUDGE_GRACE_MS // 60_000)  # 3 min after the nudge
        self.assertEqual(self.task["review_failures"][0]["why"], "ended without a verdict")
        self.assertEqual([r["cli"] for r in self.task["review_runs"]], ["claude", "agy"])  # the fallback went on
        self.valid()

    def test_a_refused_nudge_is_tried_again_and_the_grace_starts_when_it_is_delivered(self):
        from unittest import mock
        import herdr_task.news as news
        c, sends, ok = self.commands, [], [False]
        self.task.update(reviewer="claude", reviewer_model="opus")
        self.ready()
        self.screen(self.fixture("claude"))
        self.reviewer_term = {"state": "live", "agent": "claude", "status": {"state": "done"}, "turn": 3}
        t0 = self.task["review_runs"][0]["at"] + 600_000

        def at(off, send=None):
            with mock.patch.object(c, "now", lambda: t0 + off), mock.patch.object(news, "send_or_hold", send or (lambda a, m, how="send": sends.append(m) or ("sent" if ok[0] else None))):
                c.gate_reads(self.store, self.task)
        at(0)
        at(c.BG_WAIT_MS)  # due, hc refuses it
        at(c.BG_WAIT_MS + 60_000)  # tried again, refused again
        at(c.BG_WAIT_MS + 2 * 60_000, mock.Mock(side_effect=OSError("hc is gone")))  # raising is a refusal too
        self.assertEqual((len(sends), self.task.get("review_failures"), self.task["review_wait"].get("nudged_at")), (2, None, None))
        ok[0] = True
        late = c.BG_WAIT_MS + 2 * 60_000 + 30_000
        at(late)  # delivered: the grace starts now, not at the first try
        at(late + c.NUDGE_GRACE_MS - 1)
        self.assertEqual((len(sends), self.task.get("review_failures")), (3, None))
        at(late + c.NUDGE_GRACE_MS)
        self.assertEqual((len(sends), self.task["review_failures"][0]["why"]), (3, "ended without a verdict"))
        # hc refusing for good: it fails once the grace passed since the first try, never waits for ever.
        self.task.pop("review_failures"), self.task.pop("review_runs"), self.task.pop("review_wait"), self.runs.clear()
        self.ready()
        t0 = self.task["review_runs"][0]["at"] + 600_000
        ok[0] = False
        for off in (0, c.BG_WAIT_MS, c.BG_WAIT_MS + c.NUDGE_GRACE_MS - 1):
            at(off)
        self.assertNotIn("review_failures", self.task)
        at(c.BG_WAIT_MS + c.NUDGE_GRACE_MS)
        self.assertIn("review_failures", self.task)

    def test_an_agy_reviewer_whose_job_is_gone_gets_one_nudge_then_fails(self):
        from unittest import mock
        c, job = self.commands, [True]
        self.sandbox()
        self.ready()
        self.screen(self.fixture("agy"))  # real agy last screen: no task count in its footer
        self.reviewer_term = {"state": "live", "agent": "agy", "status": {"state": "idle"}, "turn": 2}
        t0, nudges = self.task["review_runs"][0]["at"] + 600_000, []
        with mock.patch.object(c, "agy_bg_tasks", lambda address: job[0]):
            self.gate_at(t0, nudges)  # "1 task(s)" in its footer: waits
            job[0] = False  # the indicator is gone, the reviewer idle without a verdict
            self.gate_at(t0 + 1, nudges)
            self.gate_at(t0 + c.QUIET_MS, nudges)
            self.assertEqual((nudges, self.task.get("review_failures")), ([], None))
            self.gate_at(t0 + 1 + c.QUIET_MS, nudges)
            self.gate_at(t0 + 1 + c.QUIET_MS + c.NUDGE_GRACE_MS - 1, nudges)
            self.assertEqual((nudges, self.task.get("review_failures")), ([("term-1", c.NUDGE_TEXT)], None))
            self.gate_at(t0 + 1 + c.QUIET_MS + c.NUDGE_GRACE_MS, nudges)
        self.assertEqual((len(nudges), self.task["review_failures"][0]["why"]), (1, "ended without a verdict"))
        self.valid()

    def test_an_idle_reviewer_with_no_job_and_no_verdict_fails_at_once_without_a_nudge(self):
        for cli, tail in (("claude", self.fixture("claude").replace("1 shell still running", "done").replace("1 shell", "no shells")), ("agy", self.fixture("agy"))):
            self.task.pop("review_failures", None), self.task.pop("review_runs", None), self.runs.clear()
            self.task.update(reviewer=cli, reviewer_model=None)
            self.screen(tail)
            self.ready()
            self.reviewer_term = {"state": "live", "agent": cli, "status": {"state": "idle"}, "turn": 2}
            nudges = []
            self.gate_at(self.task["review_runs"][0]["at"] + 600_000, nudges)
            self.assertEqual((nudges, self.task["review_failures"][0]["why"]), ([], "ended without a verdict"), cli)
            self.assertNotIn("review_wait", self.task)

    def test_the_claude_footer_counts_only_at_the_bottom_of_the_screen(self):
        from herdr_task.agents import claude_bg_shells
        for text, want in (("a\n✻ Cooked for 2m · 1 shell still running\n❯\n", True), ("x\n⏵⏵ auto mode on · 2 shells · ↓ to manage\n", True),
                           ("⏵⏵ auto mode on · 0 shells · ↓\n", False),
                           ("✻ Crunched for 2m 55s · done 9:38 AM · 2 shells, 1 monitor still running\n", True), ("⏵⏵ auto mode on · 2 shells, 1 monitor · ↓ to manage\n", True),
                           ("✻ Cooked for 1m · 1 monitor still running\n", True), ("⏵⏵ auto mode on · 1 monitor · ↓ to manage\n", True), ("⏵⏵ auto mode on · 0 shells, 0 monitors · ↓\n", False),
                           ("⏺ The test reports 1 shell still running, which I reproduced as a false alarm.\n❯\n⏵⏵ auto mode on · ↓ to manage\n", False), ("2 shells still running\n" + "\n".join("l" for _ in range(6)), False), ("", False)):
            self.assertEqual(claude_bg_shells(text), want, text)


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

    def test_brief_size_estimate_and_set_override(self):
        from herdr_task.budget import brief_lines, size_estimate
        from herdr_task.settings import set_task
        for text, want in (("One change of about 150-300 lines.", 300), ("Keep to ~300 lines.", 300),
                           ("At most 300 lines total.", 300), ("About 150–300 lines or 150 to 300 lines.", 300),
                           ("Field note: 7,191 lines over 27 files.\n- Size: about 150-300 lines. Stop past ~400.", 300),
                           ("Say why in 1 line; hc read --lines 400.", None)):
            self.assertEqual(brief_lines(text), want, text)
        self.task["prompt"] = "Keep this change to about 150-300 lines."
        self.assertEqual(size_estimate(self.task), 300)
        set_task(self.task, "estimate.lines", "80")
        self.assertEqual(size_estimate(self.task), 80)  # --set wins over the brief
        for bad in ("0", "-10", "lots"):
            with self.assertRaises(self.commands.Fail):
                set_task(self.task, "estimate.lines", bad)
        set_task(self.task, "estimate.lines", "default")
        self.assertNotIn("estimate_lines", self.task)
        self.assertEqual(size_estimate(self.task), 300)

    def test_ready_measures_diff_ignores_locks_refuses_past_twice_size_until_continue(self):
        import argparse
        from unittest import mock
        from herdr_task.budget import budget_lines, change_lines
        from herdr_task.settings import set_task

        def write_all(root, files, msg):
            for rel, body in files.items():
                p = os.path.join(root, rel)
                os.makedirs(os.path.dirname(p), exist_ok=True)
                with open(p, "w") as f:
                    f.write(body)
            self.git(root, "add", ".")
            self.git(root, "commit", "-q", "-m", msg)
            return self.git(root, "rev-parse", "HEAD")

        mb = write_all(self.main, {"src.py": "".join(f"old {i}\n" for i in range(5))}, "seed")
        write_all(self.main, {"other.py": "x\n" * 40}, "later on main")  # past merge-base: never counted against the task
        self.git(self.wt, "reset", "--hard", mb)
        self.task["start"] = mb
        self.task["prompt"] = "Field note: 7,191 lines.\n- Size: about 5-10 lines."
        # 5 deleted + 15 added = 20 lines (at 2x of 10); lock and generated files don't count
        noise = "lock\n" * 50
        self.head = write_all(self.wt, {"src.py": "".join(f"new {i}\n" for i in range(15)), "package-lock.json": noise,
                                        "bun.lockb": noise, "Cargo.lock": noise, "web/generated/plugins.ts": noise}, "at 2x")
        self.assertEqual(change_lines(self.task), 20)
        self.ready()
        self.assertNotIn("recap", self.task)
        # 21 lines (> 2x of 10): opens a recap and ready refuses with recap_refusal's words
        self.head = write_all(self.wt, {"src.py": "".join(f"new {i}\n" for i in range(16))}, "over 2x")
        self.assertEqual(change_lines(self.task), 21)
        with mock.patch.object(self.commands, "workers", lambda s, t: [{"address": "w1"}]):
            with self.assertRaises(self.commands.Fail) as e:
                self.ready()
        for words in ("change is 21 lines (plus 200 generated), over twice its size (10)", "write RECAP.md (untracked)", "Assistant decides"):
            self.assertIn(words, str(e.exception))
        self.assertEqual(self.task["recap"]["reasons"], ["change is 21 lines (plus 200 generated), over twice its size (10)"])
        self.assertEqual(self.task["attention"], "Recap required: change is 21 lines (plus 200 generated), over twice its size (10)")
        self.assertIn("change is 21 lines (plus 200 generated), over twice its size (10)", self.told[-1])
        # show's budget lines include the size estimate and current size
        from herdr_task.output import view as real_view
        shown = []
        with mock.patch.object(self.commands, "view", real_view), \
                mock.patch.object(self.commands, "say", lambda args, v, text: shown.append(text)):
            self.commands.cmd_show(argparse.Namespace(id="SHEP-13", json=False, folder=None, evidence=False))
        self.assertIn("estimate: 10 lines (current size: 21 lines (plus 200 generated))", budget_lines(self.task))
        self.assertIn("estimate: 10 lines (current size: 21 lines (plus 200 generated))", shown[-1])
        # --set wins over the brief when no recap is open; continue lets >2x through without firing again
        self.assertIn("decided: continue", self.recap("continue", why="one cohesive change"))
        self.ready()
        self.head = write_all(self.wt, {"src.py": "".join(f"new {i}\n" for i in range(18))}, "review fix")
        self.ready()  # doesn't fire again for that continue decision
        self.assertNotIn("recap", self.task)
        self.task["recaps"] = [{"decision": "split", "why": "two tasks", "at": 1, "reasons": ["change is 21 lines, over twice its size (10)"]}]
        with self.assertRaises(self.commands.Fail):
            self.ready()  # split does not waive the 2x size limit
        self.task.pop("recap", None)
        self.task.pop("recaps", None)
        set_task(self.task, "estimate.lines", "20")
        self.ready()  # 23 <= 2 * 20: --set wins over the 10-line brief
        self.assertNotIn("recap", self.task)
        set_task(self.task, "estimate.lines", "default")
        self.task["prompt"] = "Make the gate."  # no size phrase in brief: no limit
        self.ready()
        self.assertNotIn("recap", self.task)

    def test_size_check_counts_source_lines_and_reports_generated_ones_apart(self):
        """FAC-82: a WukongMind-like diff (project.pbxproj, .xcstrings, a repo-marked file, source) counts only source."""
        from herdr_task.budget import change_lines, change_sizes, size_triggers
        with tempfile.TemporaryDirectory() as d:
            def commit(files, msg):
                for rel, body in files.items():
                    os.makedirs(os.path.dirname(os.path.join(d, rel)) or d, exist_ok=True)
                    with open(os.path.join(d, rel), "w") as f:
                        f.write(body)
                self.git(d, "add", ".")
                self.git(d, "commit", "-q", "-m", msg)
            self.git(d, "init", "-q", "-b", "main")
            self.git(d, "config", "user.email", "t@example.com")
            self.git(d, "config", "user.name", "t")
            commit({"App/App.swift": "old\n", ".gitattributes": "*.snap linguist-generated=true\nkeep.snap -linguist-generated\n"}, "seed")
            start = self.git(d, "rev-parse", "HEAD")
            self.git(d, "checkout", "-q", "-b", "task")
            commit({"App/App.swift": "".join(f"line {i}\n" for i in range(120)),
                    "App.xcodeproj/project.pbxproj": "obj\n" * 700, "App/Localizable.xcstrings": "{}\n" * 300,
                    "Tests/a.snap": "snap\n" * 40, "keep.snap": "mine\n" * 5,
                    "Package.resolved": "pin\n" * 20, "plain.txt": "text\n" * 3}, "work")
            t = {"worktree": d, "mode": "worktree", "base": "main", "start": start, "prompt": "Size: about 60 lines."}
            # 120 source + 1 deleted + 5 keep.snap (unset again) + 3 plain; 700+300 generated
            self.assertEqual(change_sizes(t), (129, 1060))
            self.assertEqual(change_lines(t), 129)
            self.assertEqual(size_triggers(t), ["change is 129 lines (plus 1,060 generated), over twice its size (60)"])
            t["prompt"] = "Size: about 65 lines."  # 129 <= 2 * 65: the generated lines alone never trip it
            self.assertEqual(size_triggers(t), [])
            commit({"gen.lock": "x\n"}, "lock")
            self.assertEqual(change_sizes(t), (129, 1061))
            t["prompt"] = "Make it."
            self.assertEqual(size_triggers(t), [])


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
        from types import SimpleNamespace
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
            "revive_pass", "retry_pass", "archive_pass", "news_pass", "watchdog_pass", "end_done_pass", "memory_guard", "lost_logs_pass", "browser_pass", "say")]
        patches += [mock.patch.object(au, n, quiet) for n in (
            "retry_pass", "archive_pass", "news_pass", "watchdog_pass", "end_done_pass", "memory_guard", "lost_logs_pass", "backup_pass",
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
    """#755 / FAC-2: a Claude Code worker started without the window in a new worktree can sit at its
    folder-trust question, reporting no status: Needs you (asks) with the reason, not Working forever.
    The screen is Claude Code 2.1.319's real one (hc read, a throwaway core); hc read is faked."""

    SCREEN = ("─" * 80 + "\n Accessing workspace:\n /tmp/wt\n Quick safety check: Is this a project you created or one"
              " you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a"
              " moment to review what's in this\n folder first.\n Claude Code'll be able to read, edit, and execute files"
              " here.\n Security guide\n ❯ No, exit\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel\n")

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.agents as agents
        import herdr_task.base as base
        self.agents = agents
        agents._trust_seen.clear()
        self.addCleanup(agents._trust_seen.clear)
        self.clock = 1_800_000_000_000
        p = mock.patch.object(agents, "now", lambda: self.clock)
        p.start()
        self.addCleanup(p.stop)
        self.screen = self.SCREEN
        self.reads = []

        def read(argv, **kw):
            self.reads.append(argv)
            return subprocess.CompletedProcess(argv, 0, self.screen, "")
        r = mock.patch.object(base, "run", read)
        r.start()
        self.addCleanup(r.stop)
        # Started 10 minutes ago: past STARTING_MS, so only its agents' status speaks.
        self.task = {"id": 2, "title": "Fix", "state": "running", "started": self.clock - 600_000,
                     "agents": [{"address": "t1", "at": self.clock - 600_000}]}
        # As hc get shows it: the agent runs, has reported nothing and taken no turn.
        self.live = [{"address": "t1", "status": None, "turn": 0, "reported": False, "agent": "env",
                      "foreground": {"name": "2.1.319"}, "labels": {"herdr.role": "worker", "herdr.agent": "claude"}}]

    def check(self):
        return self.agents.task_status(self.task, self.live)

    def test_the_trust_question_needs_you(self):
        st, needs, why = self.check()
        self.assertEqual((st, needs), ("needs", "asks"))
        self.assertIn("trust", why)
        # Claude Code's older wording too
        self.clock += 6000
        self.screen = " Do you trust the files in this folder?\n ❯ 1. Yes, proceed\n   2. No, exit\n"
        self.assertEqual(self.check()[:2], ("needs", "asks"))

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
            self.assertIsNone(self.agents.trust_asked([k]), change)
            self.assertEqual(self.reads, [], change)
        # Another CLI's worker (agy, Codex) or a reviewer: its screen is never read either.
        self.reads.clear()
        self.assertIsNone(self.agents.trust_asked([dict(self.live[0], labels={"herdr.role": "worker", "herdr.agent": "agy"})]))
        self.assertIsNone(self.agents.trust_asked([dict(self.live[0], labels={"herdr.role": "reviewer", "herdr.agent": "claude"})]))
        self.assertEqual(self.reads, [])
        # Another problem already shown past GRACE_MS stays as the task's reason.
        self.task["attention"], self.task["attention_at"] = "Checks failing", self.clock - 60_000
        self.assertEqual(self.check(), ("needs", "attention", "Checks failing"))

    def test_automations_hear_needs_you_not_a_failure(self):
        from herdr_task.automations import diff_events
        seen = {}
        self.screen = ""
        facts = lambda: [{"id": 2, "state": "running", "status": "idle", "needs": self.agents.task_status(self.task, self.live)[1],
                          "attention": self.task.get("attention"), "merged": False, "automation": None, "starting": False}]
        diff_events(facts(), seen)
        self.clock += 6000
        self.screen = self.SCREEN
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


class ReviewerFlags(unittest.TestCase):
    """#803: add/edit's --reviewer-model and --reviewer-effort set the task's reviewer, as --set does."""

    def test_the_flags_set_the_reviewer(self):
        import argparse
        sys.path.insert(0, BIN)
        from herdr_task.settings import set_opts
        t = {}
        set_opts(t, argparse.Namespace(reviewer="codex", reviewer_model="gpt-5", reviewer_effort="xhigh"))
        self.assertEqual((t["reviewer"], t["reviewer_model"], t["reviewer_effort"]), ("codex", "gpt-5", "xhigh"))
        set_opts(t, argparse.Namespace(set=["reviewer-effort=high", "reviewer_model=default"]))
        self.assertEqual(t["reviewer_effort"], "high")
        self.assertNotIn("reviewer_model", t)


class MachineHoldFollowsSettings(unittest.TestCase):
    """#768: limits a tick kept in machine-hold.json (--machine/--hold) never shadow Settings >
    Machines saved after them; a newer --hold wins again. Throwaway files; nothing is sampled."""

    def setUp(self):
        import shutil
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.machine as machine
        import herdr_task.queues as queues
        import herdr_task.settings as settings
        self.queues, self.tmp = queues, tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.settings = os.path.join(self.tmp, "shepherdr.json")
        busy = {"memory": 40, "load": 0.1, "cores": 4, "cpu": 3, "disk": 96}
        for mod, name, value in ((settings, "SETTINGS", self.settings), (queues, "HOLD_FILE", os.path.join(self.tmp, "machine-hold.json")),
                                 (machine, "sample", lambda: busy), (queues, "_hold", {})):
            p = mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)

    def hold(self):
        self.queues._hold.clear()
        return self.queues.machine_hold()

    def save_settings(self, disk, at):
        with open(self.settings, "w") as f:
            json.dump({"machines": {"vm-demo": {"machine": {"start_disk": disk}}}}, f)
        os.utime(self.settings, (at, at))

    def test_settings_saved_after_the_hold_win(self):
        self.queues.set_hold("vm-demo", {"start_load": 1.0, "start_memory": 80, "start_disk": 90})
        os.utime(self.queues.HOLD_FILE, (1000, 1000))
        self.assertEqual(self.hold(), "vm-demo's disk is 96% full")
        self.save_settings(100, 2000)  # raised in Settings > Machines
        self.assertIsNone(self.hold())
        self.queues.set_hold("vm-demo", {"start_load": 1.0, "start_memory": 80, "start_disk": 90})  # a newer tick's
        self.assertEqual(self.hold(), "vm-demo's disk is 96% full")

    def test_no_settings_file_keeps_the_hold(self):
        self.queues.set_hold("vm-demo", {"start_disk": 90})
        self.assertEqual(self.hold(), "vm-demo's disk is 96% full")


class MacMemoryIsWhatMacOSCallsFree(unittest.TestCase):
    """SHEP-199 / GitHub #922: on a Mac the memory hold reads macOS's own free share
    (kern.memorystatus_level, the figure memory_pressure prints), not a sum of vm_stat pages that
    leaves out the cache macOS counts as free. Only sysctl and vm_stat are faked; the numbers are
    the report's: vm_stat's sum says 81% used, macOS says 39% free (61% used)."""

    TOTAL = 16 * 2 ** 30  # 16 GB, 16 KB pages: 1048576 pages
    VM_STAT = """Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                 40000.
Pages active:                              300000.
Pages inactive:                            120000.
Pages speculative:                           9000.
Pages throttled:                                0.
Pages wired down:                          150000.
Pages purgeable:                            30000.
File-backed pages:                          90000.
Anonymous pages:                           250000.
Pages stored in compressor:                 80000.
Pages occupied by compressor:               40000.
"""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.machine as machine
        self.machine = machine
        self.sysctl_level = "39\n"
        fake = {"sysctl -n hw.memsize": "%d\n" % self.TOTAL, "vm_stat": self.VM_STAT}

        def run(argv, timeout=10):
            key = " ".join(argv)
            if key == "sysctl -n kern.memorystatus_level":
                return self.sysctl_level
            return fake.get(key, "")
        for name, value in (("MAC", True), ("_run", run)):
            p = mock.patch.object(machine, name, value)
            p.start()
            self.addCleanup(p.stop)

    def test_uses_the_free_share_macos_reports(self):
        total, used = self.machine._memory()
        self.assertEqual(total, self.TOTAL)
        self.assertEqual(round(used * 100 / total), 61)  # 100 - 39, not vm_stat's 81
        self.assertEqual(self.machine.sample()["memory"], 61)

    def test_without_the_sysctl_counts_vm_stat_pages(self):
        self.sysctl_level = ""
        total, used = self.machine._memory()
        # free + inactive + speculative + purgeable = 199000 of 1048576 pages: 81% used
        self.assertEqual(round(used * 100 / total), 81)


class ReviewedNamesItsCommit(unittest.TestCase):
    """#797: `reviewed --commit <sha>` records a round on the commit it reviewed, fixes committed
    since; only a commit on the task's branch. A throwaway git repo; hc and gh are never run."""

    def setUp(self):
        import argparse
        import shutil
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.commands as commands
        self.commands, self.argparse = commands, argparse
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        main, self.wt = os.path.join(self.root, "repo"), os.path.join(self.root, "wt")
        self.git(self.root, "init", "-q", "-b", "main", main)
        for k, v in (("user.name", "T"), ("user.email", "t@example.com"), ("commit.gpgsign", "false")):
            self.git(main, "config", k, v)
        self.git(main, "commit", "-q", "--allow-empty", "-m", "base")
        self.git(main, "commit", "-q", "--allow-empty", "-m", "elsewhere")
        self.other = self.git(main, "rev-parse", "HEAD")
        self.git(main, "worktree", "add", "-q", "-b", "task/1", self.wt, "HEAD~1")
        self.store = Store([{"id": 1, "title": "Reactions", "state": "running", "mode": "worktree", "worktree": self.wt,
                             "branch": "task/1", "base": "main", "start": self.git(self.wt, "rev-parse", "HEAD"), "agents": [],
                             "review_gate": True}])
        self.store.loc["folder"] = main
        self.task = self.store.data["tasks"][0]
        for name, value in (("open_store", lambda args, write=True: self.store), ("view", lambda s, t, live=None: dict(t)),
                            ("say", lambda *a: None), ("review_limits", lambda *a: (True, 3))):
            p = mock.patch.object(commands, name, value)
            p.start()
            self.addCleanup(p.stop)

    def git(self, cwd, *args):
        return subprocess.run(["git", "-C", cwd, *args], capture_output=True, text=True, check=True).stdout.strip()

    def commit(self, words):
        self.git(self.wt, "commit", "-q", "--allow-empty", "-m", words)
        return self.git(self.wt, "rev-parse", "HEAD")

    def reviewed(self, commit=None):
        self.commands.cmd_reviewed(self.argparse.Namespace(id="SHEP-1", json=True, folder=None, report="one major, fixed",
                                                           commit=commit))
        return self.task["reviews"][-1]["head"]

    def test_a_round_on_the_commit_it_reviewed(self):
        a = self.commit("reactions on the left")
        b = self.commit("the round's fixes")
        self.assertEqual(self.reviewed(a[:9]), a)
        self.task["head"] = b
        refusal = self.commands.review_refusal(self.store, self.task, self.wt)
        self.assertIn(f"your final commit {b[:7]} (rounds are recorded on {a[:7]})", refusal)
        self.assertEqual(self.reviewed(), b)  # by default HEAD, as before
        self.assertIsNone(self.commands.review_refusal(self.store, self.task, self.wt))

    def test_uncommitted_fixes_still_record_the_named_commit(self):
        a = self.commit("reactions on the left")
        with open(os.path.join(self.wt, "fix.txt"), "w") as f:
            f.write("a fix")
        self.git(self.wt, "add", "fix.txt")
        with self.assertRaises(Exception):
            self.reviewed()
        self.assertEqual(self.reviewed(a), a)

    def test_only_a_commit_on_its_branch(self):
        self.commit("reactions on the left")
        for bad in (self.other, "nope"):
            with self.assertRaises(Exception):
                self.reviewed(bad)
        self.assertNotIn("reviews", self.task)

class GateEndToEnd(GateReview):
    """FAC-91 (field note 20): the gate end to end per reviewer CLI. ready launches the reviewer through the real launch
    code; the fake CLI under test/fake_cli runs as a real process with its real argv and prompt, prints its CLI's screen
    and runs the verdict command it was told; the gate reads that screen and takes the verdict. Only hc is faked."""

    def setUp(self):
        from unittest import mock
        GateReview.setUp(self)
        import herdr_task.launch as launch
        self.fake = os.path.join(ROOT, "test", "fake_cli")
        self.dir, self.procs = tempfile.mkdtemp(dir=self.root), []
        self.go, self.log = os.path.join(self.dir, "go"), os.path.join(self.dir, "log")
        self.mode = "pass"
        self.task["set"]["review_min"] = 1
        self.addCleanup(lambda: [(p.kill(), p.wait()) for p in self.procs])
        for mod, name, value in ((launch, "run", self.hc_run), (self.commands, "run", self.hc_read),
                                 (self.commands, "own_path", lambda: os.path.join(self.fake, "herdr-task"))):
            p = mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)

    def hc_run(self, argv, **k):  # `hc run`: the launch command after `--` runs for real with the fakes first on PATH, its terminal a file
        if argv[:2] == ["hc", "send"]:  # Codex's prompt, typed once its input box shows
            with open(os.path.join(self.dir, argv[2] + ".sent"), "w") as f:
                f.write(argv[3])
        if argv[:2] != ["hc", "run"]:  # launch's own `hc read`/`hc keys`/`hc send` of a terminal
            return self.hc_read(argv)
        self.runs.append(argv)
        addr = f"term-{len(self.runs)}"
        cmd = argv[argv.index("--") + 1:]
        env = {**os.environ, "PATH": self.fake + os.pathsep + os.environ["PATH"], "FAKE_SCREEN": os.path.join(self.dir, addr), "FAKE_LOG": self.log, "FAKE_GO": self.go,
               "FAKE_MODE": self.mode, "FAKE_TAIL": os.path.join(ROOT, "test", "fixtures", "fac78_claude_reviewer_tail.txt")}
        with open(env["FAKE_SCREEN"], "w") as out:
            self.procs.append(subprocess.Popen(cmd, cwd=self.wt, env=env, stdout=out, stderr=subprocess.STDOUT,
                                               stdin=subprocess.DEVNULL))
        return subprocess.CompletedProcess(argv, 0, json.dumps({"address": addr}), "")

    def hc_read(self, argv, **k):
        screen = os.path.join(self.dir, argv[2]) if argv[:2] == ["hc", "read"] else None
        return subprocess.CompletedProcess(argv, 0, self.slurp(screen) if screen and os.path.exists(screen) else "", "")

    def slurp(self, path):
        with open(path) as f:
            return f.read()

    def until(self, what):
        for _ in range(200):
            if what():
                return
            time.sleep(0.05)
        self.fail("the fake CLI never got there: " + self.slurp(os.path.join(self.dir, f"term-{len(self.runs)}"))[-300:])

    def screen_has(self, n, words):
        path = os.path.join(self.dir, f"term-{n}")
        return os.path.exists(path) and words in self.slurp(path)

    def argv_of(self, n):
        self.until(lambda: os.path.exists(os.path.join(self.dir, f"term-{n}.argv")))
        with open(os.path.join(self.dir, f"term-{n}.argv")) as f:
            return json.load(f)

    def take_verdict(self, n):
        """The fake CLI ran the prompt's verdict command (a stand-in herdr-task logging it): apply what it logged, from its terminal."""
        self.procs[n - 1].wait(timeout=10)
        self.assertEqual(self.procs[n - 1].returncode, 0)
        line = self.slurp(self.log).split("\n")[-2]
        self.assertIn("review SHEP-13", line)
        self.assertTrue(line.endswith(" --pass"), line)
        self.verdict(f"term-{n}")

    def test_claude_ends_its_turn_with_a_shell_running_then_records_the_verdict(self):  # FAC-79
        self.task.update(reviewer="claude", reviewer_model="opus")
        self.mode = "shell"
        self.ready()
        argv = self.argv_of(1)
        self.assertEqual(argv[argv.index("--permission-mode") + 1], "auto")  # plan mode would block the verdict command
        self.until(lambda: self.screen_has(1, "1 shell still running"))
        self.reviewer_term = {"state": "live", "agent": "claude", "status": {"state": "done"}, "turn": 3}
        self.commands.gate_reads(self.store, self.task)  # idle, no verdict, its shell running: waits, no failure
        self.assertNotIn("review_failures", self.task)
        self.assertEqual(len(self.runs), 1)
        open(self.go, "w").close()  # the shell ends and it records its verdict
        self.take_verdict(1)
        self.assertEqual([(r["verdict"], r["by"]) for r in self.task["reviews"]], [("pass", "term-1")])
        self.assertNotIn("review_failures", self.task)
        self.valid()

    def test_codex_leaves_an_untracked_file_in_the_worktree_and_the_pass_is_taken(self):
        self.task.update(reviewer="codex", reviewer_model="gpt-5")
        self.ready()
        argv = self.argv_of(1)
        self.assertEqual([argv[argv.index(f) + 1] for f in ("-s", "-a", "--disable")], ["workspace-write", "never", "hooks"])
        self.assertIn("--no-daemon", argv)
        self.take_verdict(1)  # the check that refuses a dirty tree ignores untracked files, as a RECAP.md is
        self.assertTrue(os.path.exists(os.path.join(self.wt, "untracked.txt")))
        self.assertIn("?? untracked.txt", self.git(self.wt, "status", "--porcelain"))
        self.assertEqual([r["verdict"] for r in self.task["reviews"]], ["pass"])
        self.valid()

    def test_agy_hits_a_quota_error_and_the_round_goes_to_claude(self):  # FAC-81
        self.mode = "quota"
        self.ready()
        argv = self.argv_of(1)
        self.assertEqual((argv[argv.index("--mode") + 1], "-i" in argv), ("plan", True))
        self.until(lambda: self.screen_has(1, "Individual quota reached"))
        self.reviewer_term = {"state": "live", "agent": "agy", "status": {"state": "error"}, "turn": 1}
        self.commands.gate_reads(self.store, self.task)
        self.assertEqual(self.task["review_failures"][0]["why"], "quota reached (resets in 10h55m44s)")
        self.mode, self.reviewer_term = "pass", {"state": "live", "agent": "claude", "status": {"state": "working"}}
        self.ready()  # the next reviewer: another CLI, same round
        self.assertEqual([r["cli"] for r in self.task["review_runs"]], ["agy", "claude"])
        self.take_verdict(2)
        self.assertEqual([(r["verdict"], r["by"]) for r in self.task["reviews"]], [("pass", "term-2")])
        self.valid()

    def test_agy_ends_with_the_sandboxed_verdict_block_and_the_gate_reads_it(self):
        from unittest import mock
        import herdr_task.launch as launch
        self.sandbox()  # agy on a Mac: sandboxed, so no herdr-task; the launch's wrap itself (sandbox-exec) is left out
        flag = os.path.join(self.dir, "unsandboxed")
        open(flag, "w").close()
        self.mode = "block"
        with mock.patch.object(launch, "UNSANDBOXED_FLAG", flag):
            self.ready()
        argv = self.argv_of(1)
        self.assertIn("trustedWorkspaces", argv[argv.index("--settings") + 1])
        self.until(lambda: self.screen_has(1, " pass\n"))
        self.reviewer_term = {"state": "live", "agent": "agy", "status": {"state": "idle"}, "turn": 1}
        self.commands.gate_reads(self.store, self.task)  # reads the begin and pass lines off its terminal
        self.assertEqual([(r["verdict"], r["by"], r["must_fix"]) for r in self.task["reviews"]], [("pass", "term-1", 0)])
        self.assertNotIn("review_failures", self.task)
        self.valid()


for _name in [n for n in vars(GateReview) if n.startswith("test_")]:
    setattr(GateEndToEnd, _name, None)  # its setUp and helpers only


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
        out = subprocess.run(argv, env={"PATH": os.environ["PATH"], "HOME": os.environ["HOME"], **self.MARKERS, **self.KEPT}, capture_output=True, text=True, timeout=20)
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
        self.sent, self.sent_env, self.screens, self.sessions = [], [], {}, {}

        def hc(args, env=None):
            out, code = "", 0
            if args[:2] == ["hc", "label"]:
                k, v = args[3].split("=", 1)
                self.terms[args[2]].setdefault("labels", {})[k] = v
            elif args[:2] == ["hc", "send"]:
                # each line first clears a parent Claude Code session's markers (SHEP-86)
                self.assertTrue(args[3].startswith(revive.CLEAN_ENV + " "), args[3])
                self.sent.append((args[2], args[3][len(revive.CLEAN_ENV) + 1:]))
                self.sent_env.append(env)
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
        self.assertEqual(self.sent, [("t1", "cd /home/me/app && DISABLE_AUTOUPDATER=1 command claude --model opus --resume S1")])
        # Once: the next tick types nothing again, even while the agent starts.
        self.tick()
        self.assertEqual(len(self.sent), 1)

    def test_the_line_goes_into_the_shell_with_no_caller(self):
        """SHEP-202 (GitHub #925): the scheduler runs under an agent's HC_TERMINAL_ID, and hc then wraps the typed
        line in <hc from=…> when the shell reports its status, so bash or zsh can't run it. Sent with no caller."""
        with self.mock.patch.dict(os.environ, {"HC_TERMINAL_ID": "t2"}):
            self.assistant()
            self.tick()
            self.crash("t1")
            self.tick()
            # None would mean the inherited environment, which holds HC_TERMINAL_ID here
            env = self.sent_env[0] if self.sent_env and self.sent_env[0] is not None else dict(os.environ)
        self.assertEqual(len(self.sent), 1)
        self.assertIn("command claude --model opus --resume S1", self.sent[0][1])
        self.assertNotIn("HC_TERMINAL_ID", env)
        self.assertIn("PATH", env)

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
        self.assertTrue(line.startswith("cd /home/me/app && DISABLE_AUTOUPDATER=1 command claude --model opus '<restart>Your earlier session is gone"), line)
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
        self.assertTrue(self.sent[0][1].startswith("""cd /wt && DISABLE_AUTOUPDATER=1 HERDR_BROWSER_TASK='dev#6' claude --settings '{"a":1}' --resume S1 'Herdr restarted"""), self.sent[0][1])

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
        self.assertTrue(self.sent[0][1].startswith("cd /home/me && DISABLE_AUTOUPDATER=1 command claude '<restart>"), self.sent[0][1])

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
        self.assertEqual(self.sent, [("t1", "cd /home/me && DISABLE_AUTOUPDATER=1 claude --dangerously-skip-permissions --resume S1")])

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
        self.assertTrue(self.sent[0][1].startswith("cd /wt && DISABLE_AUTOUPDATER=1 HERDR_BROWSER_TASK='dev#6' claude --resume S1 'Herdr restarted"), self.sent)


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
                # on this run's throwaway HOME herdr-task refuses hc (base.not_real): B's hc is a fake, so let it
                b = ("import sys, runpy; sys.dont_write_bytecode = True; sys.path.insert(0, sys.argv[1]);"
                     "import herdr_task.base as base; base.not_real = lambda: None;"
                     "sys.argv = sys.argv[2:]; runpy.run_path(sys.argv[0], run_name='__main__')")
                return real_run([sys.executable, "-c", b, BIN, os.path.join(BIN, "herdr-task"), *rest], env=self.b_env, **kw)
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
            "tick_store", "retry_pass", "archive_pass", "news_pass", "watchdog_pass", "end_done_pass", "memory_guard", "backup_pass",
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
        self.assertEqual(len(note.split("\n")), 14)  # head, 12 lines (Suggested next message included), the Reactions guide
        self.assertIn("- Suggested next message: on (end each reply", note)

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
                         sorted(["review", "merge", "proof", "agent", "base", "worktrees", "max_running", "notes", "autopilot", "reactions", "suggestions", "feedback"]))
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
        if home_guard.NODE is None:
            self.skipTest("no working node (home_guard.NODE)")
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

    def run_allow(self, command, hook, home=None):
        app = tempfile.mkdtemp()
        if hook is not None:
            os.makedirs(os.path.join(app, "mods", "chat"))
            with open(os.path.join(app, "mods", "chat", "ask-hook.sh"), "w") as f:
                f.write(hook)
        code = ("import sys, types, argparse; sys.dont_write_bytecode = True; sys.path.insert(0, sys.argv[1]);"
                "from herdr_task.launch import cmd_allow; cmd_allow(argparse.Namespace(command='/x/herdr-task'))")
        req = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}, "turn_id": "t1"})
        # its own HOME too: else, with no hook in the app home, cmd_allow falls back to ~/.local/share/herdr/ask's
        # copy, and the real one files a real card for this test and waits on it forever (SHEP-147)
        env = {**os.environ, "HERDR_APP_HOME": app, "HOME": home or app}
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

    def test_without_an_app_hook_the_synced_copy_answers(self):
        """SHEP-147: a machine that only runs tasks has no app-home hook; cmd_allow falls back to the copy a sync left in
        ~/.local/share/herdr/ask. It is HOME's own: a test's HOME, never the person's real one."""
        home = tempfile.mkdtemp()
        ask = os.path.join(home, ".local", "share", "herdr", "ask")
        os.makedirs(ask)
        with open(os.path.join(ask, "ask-hook.sh"), "w") as f:
            f.write('[ "$HERDR_ASK_CHAINED" = 1 ] || exit 0; '
                    'echo \'{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"synced"}}}\'')
        out = self.run_allow("rm -rf build", None, home=home)
        self.assertEqual(json.loads(out.stdout)["hookSpecificOutput"]["decision"]["message"], "synced", out.stderr)
        # neither: nothing answers, and no other home's copy is asked
        self.assertEqual(self.run_allow("rm -rf build", None, home=tempfile.mkdtemp()).stdout, "")
        # this run itself: a throwaway HOME, so no test can reach the real copy (test/home_guard.py)
        self.assertNotEqual(os.path.realpath(os.path.expanduser("~")), home_guard.REAL)

    def test_a_claude_worker_asks_on_the_card_too(self):
        # SHEP-139: a Claude worker's question tool (AskUserQuestion) reached the card only where ~/.claude/settings.json
        # had Herdr's hook: a machine that only runs tasks (synced herdr-task, no Herdr app) showed just "Open terminal".
        # Its launch now carries the same hook as Codex's, in its --settings, once, beside what was there.
        sys.path.insert(0, BIN)
        from herdr_task import launch
        argv = launch.own_argv(["claude", "--settings", json.dumps({"fastMode": False, "hooks": {"Stop": [{"hooks": []}]}}), "go"])
        st = json.loads(argv[argv.index("--settings") + 1])
        [entry] = st["hooks"]["PermissionRequest"]
        self.assertIn(" _allow --command ", entry["hooks"][0]["command"])
        self.assertEqual(entry["hooks"][0]["timeout"], launch.ASK_TIMEOUT)
        self.assertFalse(st["fastMode"])
        self.assertIn("Stop", st["hooks"])
        # resumed (its argv given again): still one hook, and its allow rules not repeated
        again = launch.own_argv(argv)
        st2 = json.loads(again[again.index("--settings") + 1])
        self.assertEqual(len(st2["hooks"]["PermissionRequest"]), 1)
        self.assertEqual(st2["permissions"]["allow"], st["permissions"]["allow"])
        # no --settings of its own: one is added
        plain = launch.own_argv(["claude", "go"])
        self.assertIn("PermissionRequest", json.loads(plain[2])["hooks"])

    def test_the_card_hook_where_only_tasks_run(self):
        # no Herdr app home here: the copy a sync left in ~/.local/share/herdr/ask is the card's hook
        home = tempfile.mkdtemp()
        os.makedirs(os.path.join(home, ".local", "share", "herdr", "ask"))
        hook = os.path.join(home, ".local", "share", "herdr", "ask", "ask-hook.sh")
        with open(hook, "w") as f:
            f.write("exit 0\n")
        code = ("import sys; sys.dont_write_bytecode = True; sys.path.insert(0, sys.argv[1]);"
                "from herdr_task.launch import ask_hook; print(ask_hook())")
        env = {**os.environ, "HOME": home, "HERDR_APP_HOME": os.path.join(home, "none")}
        out = subprocess.run([sys.executable, "-c", code, BIN], capture_output=True, text=True, env=env)
        self.assertEqual(out.stdout.strip(), hook, out.stderr)

    def test_a_sync_takes_the_card_hook_along(self):
        sys.path.insert(0, BIN)
        from herdr_task import machines
        app = tempfile.mkdtemp()
        os.makedirs(os.path.join(app, "mods", "chat"))
        for name in ("ask-hook.sh", "ask-end.py"):
            with open(os.path.join(app, "mods", "chat", name), "w") as f:
                f.write(f"# {name}\n")
        from unittest import mock
        with mock.patch.object(machines, "APP_HOME", app):
            files = {f["path"]: f for f in machines.manifest()["files"]}
        self.assertEqual(files[".local/share/herdr/ask/ask-hook.sh"]["data"], b"# ask-hook.sh\n")
        self.assertEqual(files[".local/share/herdr/ask/ask-hook.sh"]["mode"], 0o755)
        self.assertIn(".local/share/herdr/ask/ask-end.py", files)

    def test_codex_waits_as_long_as_the_card(self):
        sys.path.insert(0, BIN)
        from herdr_task import launch
        argv = launch.own_argv(["codex", "--sandbox", "workspace-write"])
        self.assertIn(f"timeout={launch.ASK_TIMEOUT}", argv[2])
        self.assertEqual(launch.ASK_TIMEOUT, 1800)


    def test_a_codex_workers_proof_and_commits_never_ask(self):
        """#866: Codex's sandbox can't write Shepherdr's stores nor git's data, so a Codex worker runs its proof and
        commits escalated: its own hook allows exactly those (not a Claude worker's, nor anything chained or riskier)."""
        sys.path.insert(0, BIN)
        from unittest import mock
        from herdr_task import launch
        with mock.patch.object(launch.shutil, "which", lambda n: None):
            own = lambda c, sb=True: launch.own_command("/x/herdr-task", c, sb)  # noqa: E731
            for c in ("herdr-artifact publish /tmp/p --title 'X · Proof'", "herdr-artifact guide", "herdr-artifact update a-1 /tmp/p",
                      "git add calc.py", "git commit -m 'Fix add'", "/x/herdr-task ready SHEP-1 --summary hi"):
                self.assertTrue(own(c), c)
            for c in ("herdr-artifact publish /tmp/p --custom", "herdr-artifact rm a-1", "git push", "git -c core.hooksPath=/tmp commit",
                      "git -C /elsewhere commit -m x", "git add . && git commit -m x", "git commit -m \"$(id)\"", "rm -rf build"):
                self.assertFalse(own(c), c)
            self.assertFalse(own("git commit -m x", False))
            self.assertFalse(own("herdr-artifact publish /tmp/p", False))
        # its review: the reviewer the brief's Reviewer line gives, read-only, and its own helpers only
        from herdr_task.settings import reviewer_command
        rv = reviewer_command("codex", folder=tempfile.mkdtemp())
        with mock.patch.dict(os.environ, {"HERDR_BROWSER_TASK": "demo#1"}):
            own = lambda c, sb=True: launch.own_command("/x/herdr-task", c, sb)  # noqa: E731
            yes = [f"hc run --bg --name demo-1-review --label \"herdr.task=$HERDR_BROWSER_TASK\" --label herdr.role=reviewer "
                   f"--label herdr.agent=codex -- {rv} 'Review it: \"ok\" & $x'",
                   "hc run --bg --name demo-1-review -- claude --permission-mode plan --model opus 'review'",
                   "hc wait demo-1-review --timeout 600", "hc read demo-1-review --lines 400", "hc kill demo-1-review"]
            no = ["hc run --bg --name demo-1-review -- claude --permission-mode plan --dangerously-skip-permissions x",
                  "hc run --bg --name demo-1-review -- codex --no-daemon --sandbox read-only --ask-for-approval never "
                  "-c sandbox_mode=\"danger-full-access\" x",
                  "hc run --bg --name demo-1-review -- sh -c 'rm -rf /'", "hc run --bg --name x -- claude --permission-mode plan x",
                  "hc kill t15q", "hc send demo-1-review hi"]
            for c in yes:
                self.assertTrue(own(c), c)
            for c in no:
                self.assertFalse(own(c), c)
            self.assertFalse(own(yes[2], False))
        argv = launch.own_argv(["codex", "-s", "workspace-write"])
        self.assertIn(" _allow --command ", argv[2])
        self.assertIn(" --sandboxed", argv[2])
        claude = json.loads(launch.own_argv(["claude", "go"])[2])["hooks"]["PermissionRequest"][0]["hooks"][0]["command"]
        self.assertNotIn("--sandboxed", claude)

    def test_a_codex_worker_never_stops_on_trust_this_folder(self):
        """#866: a Codex worker in a folder Codex hasn't seen sat at "Trust this folder?", nobody at the keys."""
        sys.path.insert(0, BIN)
        from herdr_task import launch
        d = os.path.realpath(tempfile.mkdtemp())
        subprocess.run(["git", "init", "-q", d], check=True)
        argv = launch.own_argv(["codex", "-s", "workspace-write"], d)
        self.assertIn("projects={%s={trust_level=\"trusted\"}}" % json.dumps(d), argv)
        # given already (the person's own argv): left as it is
        mine = ["codex", "-c", "projects={}"]
        self.assertEqual(sum(str(a).startswith("projects") for a in launch.own_argv(mine, d)), 1)
        self.assertFalse(any(str(a).startswith("projects") for a in launch.own_argv(["codex"])))

class TaskFlow(unittest.TestCase):
    """SHEP-170's task flow fixes, in a throwaway git repo and store; hc and gh are never run."""

    def setUp(self):
        from unittest import mock
        import shutil
        sys.path.insert(0, BIN)
        import herdr_task.commands as commands
        self.commands, self.mock = commands, mock
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.main, wt = os.path.join(self.root, "repo"), os.path.join(self.root, "wt")
        self.git(self.root, "init", "-q", "-b", "main", self.main)
        for k, v in (("user.name", "T"), ("user.email", "t@example.com"), ("commit.gpgsign", "false")):
            self.git(self.main, "config", k, v)
        self.commit(self.main, "base")
        self.git(self.main, "worktree", "add", "-q", "-b", "task/1", wt, "HEAD")
        self.live = []
        self.said = []
        self.store = Store([{"id": 1, "title": "Flow", "state": "running", "mode": "worktree", "worktree": wt,
                             "branch": "task/1", "base": "main", "start": self.git(wt, "rev-parse", "HEAD"), "agents": [],
                             "started": 1}])
        self.store.loc["folder"] = self.main
        self.task = self.store.data["tasks"][0]
        self.patch(commands, "open_store", lambda args, write=True: self.store)
        self.patch(commands, "view", lambda s, t, live=None: dict(t))
        self.patch(commands, "say", lambda args, v, text: self.said.append(text))
        self.patch(commands, "live_agents", lambda key: self.live)
        self.patch(commands, "detail", lambda k: k)

    def patch(self, mod, name, value):
        p = self.mock.patch.object(mod, name, value)
        p.start()
        self.addCleanup(p.stop)

    def git(self, cwd, *args):
        return subprocess.run(["git", "-C", cwd, *args], capture_output=True, text=True, check=True).stdout.strip()

    def commit(self, cwd, words, file=None):
        if file:
            with open(os.path.join(cwd, file), "a") as f:
                f.write(words + "\n")
            self.git(cwd, "add", file)
        self.git(cwd, "commit", "-q", "--allow-empty", "-m", words)
        return self.git(cwd, "rev-parse", "HEAD")

    def args(self, **kw):
        import argparse
        return argparse.Namespace(**{"id": "SHEP-1", "json": True, "folder": None, **kw})

    def test_switch_moves_the_task_to_the_given_space(self):
        """#837: an explicit --space wins over the old agent's herdr.task.space label."""
        self.task["space"] = "sp-a"
        self.live = [{"address": "t1", "status": {"state": "working"}, "labels": {"herdr.task.space": "sp-a"}}]
        got = []
        self.patch(self.commands, "agent_running", lambda k: True)
        self.patch(self.commands, "refresh_sessions", lambda *a: None)
        self.patch(self.commands, "run", lambda *a, **k: None)
        self.patch(self.commands, "start_again", lambda s, t, space, *a, **k: (got.append(space), ({"address": "t2", "agent": "claude"}, False))[1])
        self.commands.cmd_switch(self.args(space="sp-b", agent="claude", argv=None, parent="t9", keep=False))
        self.assertEqual(got, ["sp-b"])
        got.clear()
        self.commands.cmd_switch(self.args(space=None, agent="claude", argv=None, parent=None, keep=False))
        self.assertEqual(got, ["sp-a"])  # none given: the old agent's own space

    def switch_events(self, me=None):
        """cmd_switch with the old agent t1 mid-turn on session S1: what it ran, in order."""
        self.live = [{"address": "t1", "status": {"state": "working"}, "labels": {"herdr.task.space": "sp-a"}}]
        self.task["agents"] = [{"address": "t1", "agent": "claude", "argv": ["claude"], "at": 1, "session": "S1"}]
        events = []
        self.patch(self.commands, "agent_running", lambda k: True)
        self.patch(self.commands, "refresh_sessions", lambda *a: None)
        self.patch(self.commands, "stop_note", lambda *a, **k: None)
        self.patch(self.commands, "unarchive", lambda *a: None)
        self.patch(self.commands, "parent_for", lambda parent, space: parent)
        self.patch(self.commands, "run", lambda argv, **k: events.append(("run", argv)))

        def launch(s, t, argv, space, message=None, parent=None, resume=None, role=None):
            events.append(("launch", resume, message))
            return {"address": "t2", "agent": "claude", "argv": argv}
        self.patch(self.commands, "launch", launch)
        env = {k: v for k, v in os.environ.items() if k != "HC_TERMINAL_ID"}
        if me:
            env["HC_TERMINAL_ID"] = me
        with self.mock.patch.dict(os.environ, env, clear=True):
            self.commands.cmd_switch(self.args(space=None, agent=None, argv='["claude", "--model", "opus"]', parent=None,
                                               keep=False))
        return events

    def test_switch_ends_the_old_agent_before_resuming_its_session(self):
        """SHEP-211: switch resumed the old agent's session while the old agent was mid-turn, then killed it: the
        new agent sat on "Interrupted" with no message (a false "needs you"). Now the old one ends first, and the
        resumed session gets a first message to carry on."""
        events = self.switch_events()
        self.assertEqual(events[0], ("run", ["hc", "kill", "t1"]))
        self.assertEqual([e[0] for e in events], ["run", "launch"])
        _, resume, message = events[1]
        self.assertEqual(resume, ["claude", "--model", "opus", "--resume", "S1"])
        self.assertEqual(message, self.commands.SWITCH_NUDGE)
        self.assertEqual(self.said[-1], "SHEP-1: claude t2 resumed its session, t1 ended")

    def test_an_agent_switching_itself_ends_after_the_new_one_starts(self):
        """SHEP-211: ending the caller first would end the switch command with it."""
        events = self.switch_events(me="t1")
        self.assertEqual([e[0] for e in events], ["launch", "run"])
        self.assertEqual(events[1], ("run", ["hc", "kill", "t1"]))

    def settings(self, d):
        import herdr_task.settings as settings
        f = os.path.join(self.root, "shepherdr.json")
        with open(f, "w") as fh:
            json.dump(d, fh)
        self.patch(settings, "SETTINGS", f)
        return settings

    def test_suggested_next_message_in_the_brief(self):
        """SHEP-186: a worker's brief says whether its replies end with a suggested next message: on by default,
        and off when Settings › Agents turns it off (the agent then writes no suggest line)."""
        settings = self.settings({})
        self.assertIn("- Suggested next message: on (your reply ends with the message you'd likely send next",
                      settings.settings_block(self.store, self.task))
        settings = self.settings({"everywhere": {"agents": {"suggestions": False}}})
        self.assertIn("- Suggested next message: off (no suggested next message) (your setting everywhere)",
                      settings.settings_block(self.store, self.task))

    def test_a_running_task_keeps_its_briefs_review(self):
        """#750: Review turned on everywhere while a task runs: its brief said off, so show and ready say off too."""
        settings = self.settings({})
        self.task["agents"] = [{"argv": ["claude"], "agent": "claude"}]
        settings.keep_review(self.store, self.task)  # its brief: Review off
        self.task["review_gate"] = True
        self.commit(self.task["worktree"], "work", "a.txt")
        self.task["head"] = self.git(self.task["worktree"], "rev-parse", "HEAD")
        self.settings({"everywhere": {"tasks": {"review_rounds": 3}}})
        self.assertEqual(settings.review_limits(self.store, self.task)[0], False)
        self.assertIn("- Review: off", settings.settings_block(self.store, self.task))
        self.assertIsNone(self.commands.review_refusal(self.store, self.task, self.task["worktree"]))
        # A fresh brief (a task started now) gets today's: on, three rounds, and ready waits for one.
        settings.keep_review(self.store, self.task)
        self.assertEqual(settings.review_limits(self.store, self.task), (True, 3))
        self.assertIn("isn't reviewed", self.commands.review_refusal(self.store, self.task, self.task["worktree"]))
        # Turned off again meanwhile: still on for this one, as its brief said.
        self.settings({})
        self.assertEqual(settings.review_limits(self.store, self.task), (True, 3))

    def reviewed_task(self):
        self.settings({"everywhere": {"tasks": {"review_rounds": 3}}})
        self.task["review_gate"] = True
        wt = self.task["worktree"]
        head = self.commit(wt, "work", "a.txt")
        self.task["reviews"] = [{"round": 1, "result": "report", "at": 2, "head": head}]
        return wt

    def refusal(self):
        wt = self.task["worktree"]
        self.task["head"] = self.git(wt, "rev-parse", "HEAD")
        return self.commands.review_refusal(self.store, self.task, wt)

    def test_a_clean_merge_from_the_base_needs_no_new_round(self):
        """#741: merging the base into a reviewed branch, cleanly, keeps it reviewed."""
        wt = self.reviewed_task()
        self.assertIsNone(self.refusal())
        self.commit(self.main, "another task", "b.txt")
        self.git(wt, "merge", "-q", "--no-edit", "main")
        self.assertIsNone(self.refusal())
        self.commit(self.main, "and another", "c.txt")
        self.git(wt, "merge", "-q", "--no-edit", "main")
        self.assertIsNone(self.refusal())  # twice over
        self.commit(wt, "a fix of its own", "a.txt")
        self.assertIn("isn't reviewed", self.refusal())

    def test_a_merge_with_more_in_it_needs_a_round(self):
        wt = self.reviewed_task()
        self.commit(self.main, "another task", "b.txt")
        self.git(wt, "merge", "-q", "--no-edit", "main")
        with open(os.path.join(wt, "b.txt"), "a") as f:
            f.write("slipped in\n")
        self.git(wt, "commit", "-q", "-a", "--amend", "--no-edit")
        self.assertIn("isn't reviewed", self.refusal())

    def test_a_merge_with_conflicts_needs_a_round(self):
        wt = self.reviewed_task()
        self.commit(self.main, "the same file", "a.txt")
        subprocess.run(["git", "-C", wt, "merge", "-q", "--no-edit", "main"], capture_output=True)
        with open(os.path.join(wt, "a.txt"), "w") as f:
            f.write("resolved\n")
        self.git(wt, "commit", "-q", "-a", "--no-edit")
        self.assertIn("isn't reviewed", self.refusal())

    def test_out_of_rounds_needs_you_whatever_its_line_says(self):
        """#784: ready refused for rounds, the worker's status line "waiting for a review round" is no work of its own."""
        from herdr_task import agents, base
        t = base.now()
        self.task.update(started=t - 3600000, agents=[{"address": "t1", "at": t - 3600000}],
                         progress={"text": "Out of review rounds: waiting for a review round", "set": t - 60000, "at": t - 60000})
        live = [{"address": "t1", "status": {"state": "idle"}, "status_ms": t - 120000}]
        self.patch(agents, "agent_log", lambda *a, **k: None)
        self.assertEqual(agents.task_status(self.task, live)[0], "working")  # its own line: on its own review
        self.task["review_out"] = t - 90000
        self.assertEqual(agents.task_status(self.task, live)[:2], ("needs", "reply"))

    def test_ready_pushes_only_what_the_remote_lacks(self):
        """#795: the branch already on its remote at its tip: no push through a pre-push hook that fails."""
        import herdr_task.github as github
        wt, remote = self.task["worktree"], os.path.join(self.root, "remote.git")
        self.git(self.root, "init", "-q", "--bare", remote)
        self.git(self.main, "remote", "add", "origin", remote)
        self.commit(wt, "work", "a.txt")
        self.git(wt, "push", "-q", "--no-verify", "origin", "task/1")  # by hand
        hooks = os.path.join(self.main, ".git", "hooks")
        os.makedirs(hooks, exist_ok=True)
        with open(os.path.join(hooks, "pre-push"), "w") as f:
            f.write("#!/bin/sh\necho 'over the time budget' >&2\nexit 1\n")
        os.chmod(os.path.join(hooks, "pre-push"), 0o755)
        asked = []

        def gh(*a, **k):
            asked.append(a[:2])
            return (True, "https://github.com/o/r/pull/7", "") if a[:2] == ("pr", "create") else (True, "[]", "")
        self.patch(github, "gh", gh)
        self.assertEqual(github.pr_open(self.store, self.task, "o/r")["number"], 7)
        self.assertIn(("pr", "create"), asked)
        # A commit the remote lacks: pushed, and the hook's failure said.
        self.task.pop("pr")
        self.commit(wt, "more", "a.txt")
        with self.assertRaises(Exception) as e:
            github.pr_open(self.store, self.task, "o/r")
        self.assertIn("Couldn't push task/1", str(e.exception))

    def test_the_pull_request_title_and_body_are_short(self):
        """SHEP-198 (GitHub #924): the title follows the repo's commit titles ("SHEP-198: ..."), and the
        body is the summary, details and proof's title, never the brief."""
        import herdr_task.github as github
        wt, remote = self.task["worktree"], os.path.join(self.root, "remote.git")
        self.git(self.root, "init", "-q", "--bare", remote)
        self.git(self.main, "remote", "add", "origin", remote)
        self.commit(wt, "work", "a.txt")
        ref = self.store.ref(self.task)
        self.task.update({"title": "Flow", "prompt": "BRIEF-ONLY-LINE do the whole thing", "details": "- Checked the flow",
                          "proof": {"id": "a-1", "title": f"{ref}: Flow · Proof"}})
        asked = []

        def gh(*a, **k):
            asked.append(a)
            return (True, "https://github.com/o/r/pull/9", "") if a[:2] == ("pr", "create") else (True, "[]", "")
        self.patch(github, "gh", gh)
        self.assertEqual(github.pr_open(self.store, self.task, "o/r", "The flow works now")["number"], 9)
        create = next(a for a in asked if a[:2] == ("pr", "create"))
        title, body = create[create.index("--title") + 1], create[create.index("--body") + 1]
        self.assertEqual(title, f"{ref}: Flow")
        self.assertNotIn("BRIEF-ONLY-LINE", body)
        self.assertIn(f"Task: {ref}", body)
        self.assertIn("The flow works now", body)
        self.assertIn("- Checked the flow", body)
        self.assertIn(f"{ref}: Flow · Proof", body)
        self.assertLess(len(body), 400)
        # A title that already names the ref doesn't name it twice.
        self.assertEqual(github.pr_title(self.store, {**self.task, "title": f"{ref} Flow"}), f"{ref}: Flow")

    def test_ready_keeps_the_pull_request_the_task_continues(self):
        """#828: the work pushed to an open pull request's own branch: ready takes that one, never a second."""
        import herdr_task.github as github
        wt, remote = self.task["worktree"], os.path.join(self.root, "remote.git")
        self.git(self.root, "init", "-q", "--bare", remote)
        self.git(self.main, "remote", "add", "origin", remote)
        tip = self.commit(wt, "finish it", "a.txt")
        self.git(wt, "push", "-q", "origin", "task/1:refs/heads/feature")  # by the agent
        asked = []

        def gh(*a, **k):
            asked.append(a)
            if a[:2] == ("pr", "list") and "--search" in a:
                return True, json.dumps([{"number": 2918, "url": "https://github.com/o/r/pull/2918", "isCrossRepository": False,
                                          "baseRefName": "main", "headRefName": "feature", "headRefOid": tip}]), ""
            if a[:2] == ("pr", "view"):
                return True, "OPEN", ""
            return (False, "", "no") if a[:2] == ("pr", "create") else (True, "[]", "")
        self.patch(github, "gh", gh)
        self.assertEqual(github.pr_open(self.store, self.task, "o/r", "Done")["number"], 2918)
        self.assertNotIn("create", [a[1] for a in asked])
        self.assertEqual(self.git(self.root, "--git-dir", remote, "branch", "--list", "task/1"), "")  # no branch of its own
        # A later commit goes to that pull request's branch.
        tip = self.commit(wt, "a fix", "a.txt")
        self.assertEqual(github.pr_open(self.store, self.task, "o/r")["number"], 2918)
        self.assertEqual(self.git(self.root, "--git-dir", remote, "rev-parse", "feature"), tip)

    def test_the_same_red_checks_are_told_once(self):
        """#796: after ready again, the same failing checks of the same commit aren't news again."""
        from herdr_task import ghnews, base
        self.patch(ghnews, "resting", lambda s, t: {"address": "t1"})
        self.patch(ghnews, "pr_comments", lambda repo, n: [])
        self.patch(ghnews, "log_tail", lambda repo, rid: None)
        self.task.update(state="ready", ready_at=base.now() - 1000, pr={"number": 7, "repo": "o/r", "url": "u"})
        v = {"state": "OPEN", "headRefOid": "abc", "statusCheckRollup": [{"name": "test", "conclusion": "FAILURE", "detailsUrl": "x"}]}
        ghnews.pr_news(self.store, self.task, v, {})
        self.assertIn('Check "test" failed', self.task["pr"].pop("news")["text"])  # told, then ready again
        self.task["ready_at"] = base.now()
        ghnews.pr_news(self.store, self.task, v, {})
        self.assertNotIn("news", self.task["pr"])
        # A new commit's runs failing: news.
        self.task["ready_at"] = base.now() + 1
        ghnews.pr_news(self.store, self.task, {**v, "headRefOid": "def"}, {})
        self.assertIn("news", self.task["pr"])

    def test_an_automation_sets_its_tasks_review(self):
        """#744: auto add/edit --set, kept on the automation and given to each task it adds."""
        import argparse
        from herdr_task import automations
        none = dict(title=None, prompt=None, prompt_file=None, every=None, on="ready", match=None, from_=None, agent=None,
                    here=False, worktree=False, space=None, parent=None)
        a = {"id": 1, "title": "Sweep", "prompt": "Read the open pull requests", "space": "sp1"}
        automations.set_auto_fields(self.store, a, argparse.Namespace(**none, set=["review=off", "reviewer.model=opus"]))
        self.assertEqual(a["set"], {"review": "off", "reviewer.model": "opus"})
        with self.assertRaises(Exception):
            automations.set_auto_fields(self.store, a, argparse.Namespace(**none, set=["colour=red"]))
        automations.set_auto_fields(self.store, a, argparse.Namespace(**none, set=["reviewer.model="]))
        self.assertEqual(a["set"], {"review": "off"})
        self.store.new = lambda title, prompt, mode: self.store.data["tasks"].append(
            {"id": 2, "title": title, "prompt": prompt, "mode": mode, "state": "todo", "agents": []}) or self.store.data["tasks"][-1]
        for name, value in (("worker_argv", lambda s, t, space: ["claude"]), ("queue_reason", lambda s, t: None),
                            ("begin", lambda *a, **k: None)):
            self.patch(automations, name, value)
        t = automations.fire(self.store, a, "every")
        self.assertEqual(t["set"], {"review": "off"})

    def test_a_folder_task_that_changed_nothing_needs_no_review(self):
        """#744: a task in the folder itself, still on the commit it started at with nothing changed: nothing to review."""
        self.settings({"everywhere": {"tasks": {"review_rounds": 3}}})
        self.task.update(mode="here", worktree=None, review_gate=True, folder_start=self.git(self.main, "rev-parse", "HEAD"))
        self.assertIsNone(self.commands.review_refusal(self.store, self.task, None))
        with open(os.path.join(self.main, "a.txt"), "w") as f:
            f.write("x\n")
        self.git(self.main, "add", "a.txt")
        self.assertIn("isn't reviewed", self.commands.review_refusal(self.store, self.task, None))  # changed
        self.git(self.main, "commit", "-q", "-m", "a")
        self.assertIn("isn't reviewed", self.commands.review_refusal(self.store, self.task, None))  # committed

    def test_the_folder_trust_dialog_needs_you(self):
        """#755: Claude Code's folder-trust dialog (no status reported yet) is Needs you, not Working."""
        from herdr_task import agents, base
        screen = "Accessing workspace:\\n\\n Quick safety check: Is this a project you created or one you trust?\\n ❯ No, exit\\n   Yes, I trust this folder\\n"
        shown = [screen]
        self.patch(agents, "_trust_seen", {})
        self.patch(base, "run", lambda argv, **k: subprocess.CompletedProcess(argv, 0, shown[0], ""))
        self.task.update(started=base.now() - 10000, agents=[{"address": "t1", "at": base.now() - 10000}])
        live = [{"address": "t1", "status": None, "turn": 0}]
        self.assertEqual(agents.task_status(self.task, live), ("needs", "asks", agents.REASONS["trust"]))
        agents._trust_seen.clear()
        shown[0] = "❯ "
        self.assertEqual(agents.task_status(self.task, live)[0], "working")  # just started, no dialog
        live[0]["status"] = "working"
        shown[0] = screen  # never read once it reports
        self.assertEqual(agents.task_status(self.task, live)[0], "working")

    def test_low_priority_says_nothing_without_proc(self):
        """#755: no "oom_score_adj: No such file" line where /proc isn't (a Mac)."""
        from herdr_task import launch
        low = launch.LOW.replace("/proc/self/oom_score_adj", os.path.join(self.root, "none", "oom_score_adj"))
        p = subprocess.run(["sh", "-c", low, "herdr-low", "true"], capture_output=True, text=True)
        self.assertEqual((p.returncode, p.stderr), (0, ""))

    def test_an_agent_under_fish_is_running(self):
        """#780: under fish the foreground stays `fish -ic "<agent>; exec fish -i"` while its agent works."""
        from herdr_task import agents
        fg = lambda *argv: {"address": "t1", "foreground": {"name": "fish", "argv": list(argv), "pid": 9}}
        self.assertTrue(agents.agent_running(fg("/usr/bin/fish", "-ic", "claude --model x; exec fish -i")))
        self.assertTrue(agents.agent_running(fg("fish", "-l", "--command", "claude; exec fish -i")))
        self.assertFalse(agents.agent_running(fg("fish", "-i")))  # exec'ed its own shell: the agent exited
        self.assertFalse(agents.agent_running(fg("bash")))
        self.assertTrue(agents.agent_running({"foreground": {"name": "claude", "argv": ["claude"]}}))


class ReachProbeSpaces(unittest.TestCase):
    """SHEP-185: a machine's reach map lists its spaces from the window's live list (herdr-app ls) when a window there
    names some, else herdr-core's saved document; the probe runs for real with fake hc and herdr-app on PATH."""

    def probe(self, live=None, saved=None):
        sys.path.insert(0, BIN)
        from herdr_task.machines import PROBE
        root = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(root, ignore_errors=True))
        bin_ = os.path.join(root, "bin")
        os.makedirs(bin_)
        state = os.path.join(root, "state", "herdr-core", "default")
        os.makedirs(state)
        with open(os.path.join(state, "docs.json"), "w") as f:
            json.dump({"herdr-app": {"value": {"spaces": saved or []}, "version": 3}}, f)
        hc = ('#!/bin/sh\ncase "$1 $2" in\n'
              '"server status") echo \'{"core":{"id":"c-b","name":"vm-b"}}\' ;;\n'
              '"machine ls") echo \'{"machines":[]}\' ;;\n'
              '"ls --json") echo \'{"terminals":[]}\' ;;\n'
              '*) echo "{}" ;;\nesac\n')
        for name, body in (("hc", hc), ("herdr-app", None if live is None else '#!/bin/sh\necho \'%s\'\n' % json.dumps({"spaces": live}))):
            if body is None:
                continue
            with open(os.path.join(bin_, name), "w") as f:
                f.write(body)
            os.chmod(os.path.join(bin_, name), 0o755)
        env = {**os.environ, "PATH": bin_, "XDG_STATE_HOME": os.path.join(root, "state")}
        out = subprocess.run([sys.executable, "-c", "import sys; S = sys.stdin.read(); exec(S)", json.dumps({"depth": 0, "seen": [], "timeout": 20})],
                             input=PROBE, capture_output=True, text=True, env=env, timeout=120)
        return json.loads(out.stdout.strip().splitlines()[-1])

    def test_the_window_list_beats_a_saved_document_that_is_empty(self):
        got = self.probe(live=[{"id": "sp1", "name": "", "folder": os.path.expanduser('~')}], saved=[])
        self.assertEqual([(s["id"], s["folder"]) for s in got["spaces"]], [("sp1", os.path.expanduser('~'))])
        self.assertEqual(got["home"], os.path.expanduser("~"))

    def test_without_a_window_the_saved_document_is_read(self):
        got = self.probe(live=None, saved=[{"id": "sp2", "name": "shop", "folder": "/base/shop"}])
        self.assertEqual([(s["id"], s["name"]) for s in got["spaces"]], [("sp2", "shop")])

    def test_a_window_without_spaces_falls_back_to_the_document(self):
        got = self.probe(live=[], saved=[{"id": "sp3", "name": "lab", "folder": "/base/x"}])
        self.assertEqual([s["id"] for s in got["spaces"]], ["sp3"])


class StopLog(unittest.TestCase):
    """SHEP-189: the stop log keeps the last lines as JSON, and never raises into the work it logs."""

    def test_keeps_the_last_lines_as_json(self):
        import herdr_task.base as base
        d = tempfile.mkdtemp()
        from unittest import mock
        with mock.patch.object(base, "STOP_LOG", os.path.join(d, "stops.jsonl")):
            for i in range(base.STOP_LOG_LINES + 5):
                base.stop_note("end-done", n=i)
            with open(base.STOP_LOG) as f:
                lines = f.read().splitlines()
        self.assertEqual(len(lines), base.STOP_LOG_LINES)
        last = json.loads(lines[-1])
        self.assertEqual((last["kind"], last["n"]), ("end-done", base.STOP_LOG_LINES + 4))
        self.assertIn("at", last)

    def test_a_log_that_cannot_be_written_does_not_raise(self):
        import herdr_task.base as base
        from unittest import mock
        with mock.patch.object(base, "STOP_LOG", "/proc/forbidden/stops.jsonl"):
            base.stop_note("gone", task="SHEP-1")  # must not raise

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
        # hc refusing the pane leaves no ask behind (unguarded on home_guard's throwaway HOME so fake hc runs).
        unguarded = ("import sys, runpy; sys.dont_write_bytecode = True; sys.path.insert(0, sys.argv[1]);"
                     "import herdr_task.base as b; b.not_real = lambda: None;"
                     "sys.argv = sys.argv[2:]; runpy.run_path(sys.argv[0], run_name='__main__')")
        p = subprocess.run([sys.executable, "-c", unguarded, BIN, self.entry, "ask", "Ship?", "--option", " Yes ",
                            "--option", "No", "--recommend", " Yes"],
                           capture_output=True, text=True, env={**self.env, "HC_TERMINAL_ID": "t5"})
        self.assertIn("couldn't start the ask pane: hc failed", p.stderr)
        self.assertEqual(os.listdir(os.path.join(self.store, "asks")), [])
        # Outside a terminal there's nobody to tell the answer: refused.
        p = subprocess.run([sys.executable, self.entry, "ask", "Ship?", "--option", "Yes", "--option", "No", "--recommend", "Yes"],
                           capture_output=True, text=True, env=self.env)
        self.assertIn("Run it from an agent's own terminal", p.stderr)

    @unittest.skipUnless(shutil.which("hc") and not CI, "needs herdr-core (hc) on this machine")
    def test_ask_pane_end_to_end(self):
        import time
        session = f"fac9-ask-{os.getpid()}"
        short_home = tempfile.mkdtemp(dir="/tmp", prefix="h-")  # short enough for macOS's 104-byte Unix socket path
        base = {"HOME": short_home, "PATH": f"/usr/bin:/bin:{os.path.dirname(shutil.which('hc'))}"}
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

        state = [short_home]  # the throwaway core's folders, removed after it stops

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

        unguarded = ("import sys, runpy; sys.dont_write_bytecode = True; sys.path.insert(0, sys.argv[1]);"
                     "import herdr_task.base as b; b.not_real = lambda: None;"
                     "sys.argv = sys.argv[2:]; runpy.run_path(sys.argv[0], run_name='__main__')")
        p = subprocess.run([sys.executable, "-c", unguarded, BIN, self.entry, "ask", "Ship it tonight?", "--option", "Wait",
                            "--option", "Ship", "--recommend", "Ship", "--task", "FAC-9", "--json"],
                           capture_output=True, text=True, env=inside)
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
        i = env_idx + 1
        while "=" in hc[i].split("/")[0]:  # after ["env", "HERDR_BROWSER_TASK=...", "DISABLE_AUTOUPDATER=1"]
            i += 1
        return hc[i:]

    def env_words(self, idx=-1):
        hc = self.runs[idx]
        i = hc.index("env") + 1
        out = []
        while "=" in hc[i].split("/")[0]:
            out.append(hc[i])
            i += 1
        return out

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

    def test_claude_launch_and_resume_never_auto_update(self):
        """FAC-87: a self-updated Claude binary makes macOS ask about a removable volume and every new agent hangs."""
        ref = self.started_task({"agent": "claude"})
        self.assertIn("DISABLE_AUTOUPDATER=1", self.env_words())
        self.call(self.commands.cmd_resume, id=ref, space=None, agent=None, argv=None, parent=None)
        self.assertIn("DISABLE_AUTOUPDATER=1", self.env_words())
        self.assertEqual(self.launched_cmd()[0], "claude")

    def test_an_agent_without_an_update_switch_gets_nothing_extra(self):
        self.started_task({"agent": "agy", "perm": "auto"})
        self.assertEqual([w.split("=")[0] for w in self.env_words()], ["HERDR_BROWSER_TASK"])
        self.assertEqual(self.launch.no_update_env(["codex", "exec"]), [])

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
        # switch without --agent keeps prev["argv"] and sends SWITCH_NUDGE (not continue_line).
        out = self.call(self.commands.cmd_switch, id=ref, space=None, agent=None, argv=None, parent=None, keep=False)
        self.assertTrue(out["resumed"])
        cmd = self.launched_cmd()
        self.assertIn("accept-edits", cmd)
        self.assertEqual(cmd[-2:], ["-i", self.commands.SWITCH_NUDGE])
        self.assertNotIn("Herdr resumed you:", cmd[-1])
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

    def test_real_cli_screens_only_hold_on_real_typed_input(self):
        agy_rule, cl_rule = "\x1b[0m\x1b[38;5;8m" + "─" * 90 + "\x1b[0m", "\x1b[0m\x1b[38;2;136;136;136m" + "─" * 90 + "\x1b[0m"
        survey = ("╭" + "─" * 58 + "╮\r\n│ How's the CLI experience so far? Help us improve:        │\r\n"
                  "│ [1] Good  [2] Fine  [3] Bad  [0] Skip                    │\r\n╰" + "─" * 58 + "╯")
        agy_screen = lambda box, above="": "\r\n".join([
            "─" * 24 + " Conversation compacted " + "─" * 24, "\x1b[0m\x1b[38;5;8m" + "─" * 60 + "\x1b[0m",
            "\x1b[0m\x1b[1m\x1b[38;5;4m> From your Assistant: earlier message in scrollback\x1b[0m",
            "  Ready for review.", *([above] if above else []), agy_rule, box, agy_rule,
            "  \x1b[0m\x1b[38;5;3m●\x1b[0m [14:28:06] \x1b[0m\x1b[38;5;12m15m timer\x1b[0m \x1b[0m\x1b[38;5;3mrunning\x1b[0m", agy_rule,
            "\x1b[0m\x1b[38;5;8m? for shortcuts                                  \x1b[0m\x1b[38;5;3mauto\x1b[0m\x1b[2m · Gemini 4 Carbon · high · 1 task(s) · /tasks\x1b[0m\x1b[>4;2m\x1b[5;3H"])
        cl_screen = lambda box: "\r\n".join([
            "\x1b[0m\x1b[38;2;153;153;153m\x1b[48;2;55;55;55m❯ \x1b[0m\x1b[38;2;255;255;255mEarlier message in scrollback\x1b[0m", "", cl_rule, box, cl_rule,
            "  \x1b[0m\x1b[38;2;255;193;7m⏵⏵ auto mode on\x1b[0m\x1b[38;2;153;153;153m (shift+tab to cycle)\x1b[0m\x1b[>4;2m\x1b]7;file://localhost/work\x1b\\\x1b[4;3H"])
        cx_screen = lambda box: "\r\n".join(["› Earlier question in scrollback", "  Done.", "", box, "",
                                             "\x1b[2m  ? for shortcuts                                     100% context left\x1b[0m"])
        cx_picker = "\r\n".join(["› Ask me two things", "", "  Question 1/2 (2 unanswered)", "  Which color?", "",
                                 "  › 1. Red                Choose red.", "    2. Blue               Choose blue.", "",
                                 "  tab to add notes | enter to submit answer | esc to interrupt"])
        idle_cases = [
            agy_screen("\x1b[0m\x1b[38;5;12m>\x1b[0m \x1b[0m\x1b[38;5;8mAuto mode: edits & safe commands auto-approved (shift+tab to cycle)\x1b[0m"),
            agy_screen("\x1b[0m\x1b[38;5;12m>\x1b[0m"), agy_screen("\x1b[0m\x1b[38;5;12m>\x1b[0m", survey),
            agy_screen("  How's the CLI experience so far? Help us improve:  \r\n  [1] Good  [2] Fine  [3] Bad  [0] Skip"),
            "\r\n".join([agy_rule, "\x1b[0m\x1b[1m\x1b[38;5;4m> Earlier in scrollback\x1b[0m", "  Done.", agy_rule, "  Thank you for your feedback!", agy_rule, "? for shortcuts"]),
            "\r\n".join([agy_rule, "> stale draft\r> \x1b[K", agy_rule, "? for shortcuts"]),
            "› stale draft\r\n  second line\x1b[1;3H\x1b[J",
            cl_screen("❯\xa0\x1b[0m\x1b[2mTry \"how does <filepath> work?\"\x1b[0m"), cl_screen("❯\xa0"),
            "─" * 80 + "\r\n Security guide\r\n ❯ No, exit\r\n   Yes, I trust this folder\r\n Enter to confirm · Esc to cancel",
            cx_screen("\x1b[1m› \x1b[0m\x1b[2mAsk Codex to do anything\x1b[0m"), cx_picker,
        ]
        self.term.update(status={"state": "done"}, labels={"herdr.agent": "agy"})
        for raw in idle_cases:
            self.raw = raw
            self.assertEqual((self.news.typed_input(raw), self.news.typing("t1")), ("", None))
        self.raw = idle_cases[0]
        self.assertEqual(self.news.send_or_hold("t1", "Review round 1 found what must be fixed"), "sent")
        self.assertEqual((self.calls, self.held()), ([["hc", "send", "t1", "Review round 1 found what must be fixed"]], {}))
        for raw, want in [
            (agy_screen("\x1b[0m\x1b[38;5;12m>\x1b[0m fix the flaky test\r\n  and rerun checks"), "fix the flaky test\n  and rerun checks"),
            ("\r\n".join([agy_rule, "\x1b[0m\x1b[38;5;12m>\x1b[0m hold on", agy_rule, "  ● [14:28:06] 15m timer running", "  ● [14:29:10] pytest running", agy_rule, "? for shortcuts · 2 task(s) · /tasks"]), "hold on"),
            ("\r\n".join([agy_rule, "\x1b[0m\x1b[38;5;12m>\x1b[0m", agy_rule, "? for shortcuts\x1b[0m\x1b[>4;2m\x1b[2;3Htyped after cursor move"]), "typed after cursor move"),
            ("\r\n".join([agy_rule, "\x1b[38;5;8m> \x1b[m\x1b[2m\x1b[mreset with bare m", agy_rule, "? for shortcuts"]), "reset with bare m"),
            ("\r\n".join([agy_rule, ">", agy_rule, "? for shortcuts"]) + "\x1b[H\x1b[B\x1b[C\x1b[Chome and moves\x1b[D!", "home and move!"),
            (cl_screen("❯\xa0check edge cases\x1b[0m\x1b[2m in news.py\x1b[0m"), "check edge cases"),
            (cx_screen("\x1b[1m› \x1b[0mrefactor the parser\r\n  keep tests green"), "refactor the parser\n  keep tests green"),
        ]:
            self.raw = raw
            self.assertEqual((self.news.typed_input(raw), self.news.typing("t1")), (want, "input"))
        self.calls.clear()
        self.raw = agy_screen("\x1b[0m\x1b[38;5;12m>\x1b[0m hold on, editing")
        self.assertEqual((self.news.send_or_hold("t1", "Deferred findings"), self.calls), ("held", []))
        self.raw = idle_cases[0]
        self.news.held_pass({})
        self.assertEqual((self.calls, self.held()), ([["hc", "send", "t1", "Deferred findings"]], {}))


class ProjectStart(unittest.TestCase):
    """`herdr-task project start` (herdr_task/project.py): repo init, factory settings, check detection,
    requirements import, space & Assistant creation, Autopilot heartbeat, idempotence and --dry-run."""

    def setUp(self):
        import contextlib, io, pathlib
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.ask as ask_mod, herdr_task.autopilot as ap, herdr_task.base as base, herdr_task.cli as cli
        import herdr_task.news as news_mod, herdr_task.project as project, herdr_task.settings as st, herdr_task.store as store
        self.ask_mod, self.ap, self.base, self.cli, self.project, self.st, self.store = ask_mod, ap, base, cli, project, st, store
        self.io, self.redirect, self.exit_stack, self.mock, self.Path = io, contextlib.redirect_stdout, contextlib.ExitStack, mock, pathlib.Path
        self.tmp = os.path.realpath(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.store_dir, self.settings_file = os.path.join(self.tmp, "tasks"), os.path.join(self.tmp, "shepherdr.json")
        self.app_home, self.templates, self.beats = (os.path.join(self.tmp, x) for x in ("app-home", "templates", "beats"))
        self.window, self.terms, self.calls, self.seen_tpl = [], {}, [], {}
        self.gh_repo, self.gh_secrets = None, ""

        def fake_run(argv, cwd=None, check=False, timeout=None):
            if argv[0] in ("git", "asc"):
                return subprocess.run(argv, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True, timeout=timeout)
            self.calls.append(list(argv))
            if argv[0] == "gh":
                if argv[1:3] == ["repo", "view"]:
                    return subprocess.CompletedProcess(argv, 0 if self.gh_repo else 1, json.dumps({"nameWithOwner": self.gh_repo}) if self.gh_repo else "", "")
                if argv[1:3] == ["secret", "list"]:
                    return subprocess.CompletedProcess(argv, 0 if self.gh_secrets is not None else 1, self.gh_secrets or "", "")
            if argv[:2] == ["herdr-app", "ls"]:
                return subprocess.CompletedProcess(argv, 1, "", "no window") if self.window is None else \
                    subprocess.CompletedProcess(argv, 0, json.dumps({"spaces": self.window}), "")
            if argv[:3] == ["herdr-app", "do", "space.new"]:
                sid = f"sp{len(self.window) + 1}"
                self.window.append({"id": sid, "name": argv[argv.index("--name") + 1], "folder": argv[argv.index("--folder") + 1]})
                return subprocess.CompletedProcess(argv, 0, json.dumps({"space": sid}), "")
            if argv[:2] == ["hc", "ls"]:
                return subprocess.CompletedProcess(argv, 0, json.dumps({"terminals": list(self.terms.values())}), "")
            if argv[:2] == ["hc", "get"]:
                t = self.terms.get(argv[2])
                return subprocess.CompletedProcess(argv, 0 if t else 1, json.dumps(t) if t else "", "")
            if argv[:2] == ["hc", "label"]:
                self.terms[argv[2]]["labels"].update(x.split("=", 1) for x in argv[3:])
                return subprocess.CompletedProcess(argv, 0, "", "")
            if argv[:2] == ["hc", "run"]:
                lbs = {k: v for i, a in enumerate(argv) if a == "--label" for k, v in [argv[i + 1].split("=", 1)]}
                tpl, addr = argv[-1], f"t{len(self.terms) + 1}"
                if os.path.isfile(tp := os.path.join(self.templates, f"{tpl}.md")):
                    self.seen_tpl[tpl] = self.Path(tp).read_text()
                self.terms[addr] = {"address": addr, "state": "live", "cwd": argv[argv.index("--cwd") + 1] if "--cwd" in argv else (cwd or ""),
                                    "template": tpl, "labels": lbs, "status": {"state": "idle"}, "turn": 1}
                return subprocess.CompletedProcess(argv, 0, json.dumps({"address": addr}), "")
            raise AssertionError(f"unexpected command: {argv}")

        for obj, k, v in ((base, "run", fake_run), (base, "GH", "gh"), (base, "APP_HOME", self.app_home), (store, "STORE", self.store_dir),
                          (st, "SETTINGS", self.settings_file), (st, "TEMPLATES", self.templates), (news_mod, "send_or_hold", lambda *_, **__: "sent"),
                          (ap, "STORE", self.store_dir), (ap, "STATE", os.path.join(self.store_dir, ".autopilot.json")),
                          (ap, "HEARTBEATS", self.beats), (ap, "run", fake_run), (ap, "typing", lambda *_: None),
                          (ap, "ensure_scheduler", lambda quiet=False: {}), (ap, "news_send", lambda *_, **__: True)):
            p = mock.patch.object(obj, k, v)
            p.start()
            self.addCleanup(p.stop)
        # A real asc on this machine must never run: PATH holds only a scratch dir plus the dirs without asc.
        self.asc_bin = os.path.join(self.tmp, "asc-bin")
        os.makedirs(self.asc_bin)
        path = os.pathsep.join([self.asc_bin] + [d for d in os.environ["PATH"].split(os.pathsep) if not os.access(os.path.join(d, "asc"), os.X_OK)])
        pe = mock.patch.dict(os.environ, {"HC_TERMINAL_ID": "", "PATH": path})
        pe.start()
        self.addCleanup(pe.stop)

    def fake_asc(self, out="", code=0, sleep=0):
        """A fake `asc` first on PATH: prints out, exits with code (after sleep seconds); the arguments it got go to asc-args."""
        f = self.put(os.path.join(self.asc_bin, "asc"), f'#!/bin/sh\necho "$@" >> "{self.tmp}/asc-args"\nsleep {sleep}\ncat <<\'EOF\'\n{out}\nEOF\nexit {code}\n')
        os.chmod(f, 0o755)

    def put(self, path, data):
        os.makedirs(os.path.dirname(path), exist_ok=True)
        self.Path(path).write_text(data if isinstance(data, str) else json.dumps(data))
        return path

    def start(self, *argv):
        buf = self.io.StringIO()
        with self.mock.patch.object(sys, "argv", ["herdr-task", "project", "start", *argv]), self.redirect(buf):
            self.assertEqual(self.cli.main(), 0)
        return buf.getvalue()

    def test_starts_project_autopilot_and_second_run_is_noop(self):
        self.put(os.path.join(self.templates, "assistant.md"), "---\nname: assistant\ncommand: exec claude\n---\nCoordinate.\n")
        self.put(os.path.join(self.app_home, "shared.json"), {"coordinator.slots": {"s1": {"pane": "p9", "space": "sp1", "tab": "tb2"}}})
        self.put(self.settings_file, {"version": 1, "everywhere": {"worker": {"agent": "claude", "model": "opus", "effort": "high"}}})
        repo = os.path.join(self.tmp, "my-app")
        self.put(os.path.join(repo, "package.json"), {"scripts": {"test": "vitest"}})
        spec = self.put(os.path.join(self.tmp, "spec.md"), "# Goal\nShip v1.\n")
        out = json.loads(self.start(repo, "--name", "My App", "--requirements", spec, "--autopilot", "--json"))
        self.assertEqual((out["changed"], out["prefix"], out["space"], out["assistant"], out["check"], out["autopilot"]),
                         (True, "MA", "sp1", "t1", "npm test", True))
        self.assertEqual(self.base.git(repo, "rev-parse", "--abbrev-ref", "HEAD")[1], "main")
        self.assertEqual(self.Path(repo, "docs", "requirements.md").read_text(), "# Goal\nShip v1.\n")
        with self.store.Store(self.store.locate(repo), write=False) as s:
            cfg = self.st.settings_for(s, "sp1")
            self.assertEqual((cfg["tasks"]["merge"], cfg["tasks"]["review_min"], cfg["tasks"]["check"]), ("checks", 2, "npm test"))
            self.assertIs(cfg["tasks"]["reviewer_sandbox"], False)  # sandboxed agy can't sign in today (FAC-76)
            self.assertEqual((cfg["reviewer"]["agent"], cfg["reviewer"]["model"]), ("agy", "gemini-4-argon-high"))
            self.assertEqual(self.st.worker_argv(s, {}, "sp1"), ["agy", "--model", "gemini-4-carbon-high", "--dangerously-skip-permissions"])
        self.assertEqual((self.terms["t1"]["labels"]["herdr.chat"], self.terms["t1"]["labels"]["herdr.coordinator.space"]), ("p9", "sp1"))
        self.assertTrue(any("CLAUDECODE" in v for v in self.seen_tpl.values()) and os.listdir(self.templates) == ["assistant.md"])
        self.assertTrue(any("DISABLE_AUTOUPDATER=1 exec claude" in v for v in self.seen_tpl.values()))  # FAC-87: before exec
        self.assertIn("docs/requirements.md", self.ap.heartbeat_of("sp1")[0])
        self.assertTrue(self.store.read_list(self.ap.STATE)["spaces"]["sp1"]["on"])
        self.assertEqual((out["checklist"]["ready"], len(out["checklist"]["asked"])), (["ci_secrets", "disk", "autopilot"], 4))
        before = len(self.calls)
        text2 = self.start(repo, "--requirements", "https://example.com/other", "--check", "pytest", "--autopilot")
        self.assertTrue(all("found" in line for line in text2.strip().splitlines()[:7]), text2)
        self.assertIn("Checklist for My App (sp1): 3 ready, 5 missing", text2)
        self.assertEqual([c for c in self.calls[before:] if c[:2] in (["hc", "run"], ["herdr-app", "do"])], [])
        self.assertEqual(self.Path(repo, "docs", "requirements.md").read_text(), "# Goal\nShip v1.\n")

    def test_detects_checks_url_requirements_and_no_window_fallback(self):
        m, py, xc = (os.path.join(self.tmp, x) for x in ("mk", "py", "xc"))
        self.put(os.path.join(m, "package.json"), {"scripts": {"test": 'echo "Error: no test specified" && exit 1'}})
        self.put(os.path.join(m, "Makefile"), "build:\n\tcc a.c\ntest:\n\t./a.out\n")
        self.put(os.path.join(py, "test", "test_x.py"), "")
        os.makedirs(os.path.join(xc, "Demo.xcodeproj"))
        self.assertEqual((self.project.detect_check(m), self.project.detect_check(py), self.project.detect_check(xc)),
                         ("make test", "python3 -m unittest discover -s test", "xcodebuild test -project Demo.xcodeproj -scheme Demo"))
        self.window, fresh = None, os.path.join(self.tmp, "headless-app")
        out = json.loads(self.start(fresh, "--requirements", "https://example.com/spec", "--check", "cargo test", "--autopilot", "--json"))
        self.assertTrue("herdr-app do space.new" in out["space_step"] and out["check"] == "cargo test")
        self.assertEqual(self.Path(fresh, "docs", "requirements.md").read_text(), "[https://example.com/spec](https://example.com/spec)\n")
        self.assertFalse(json.loads(self.start(fresh, "--autopilot", "--json"))["changed"])
        self.assertIsNotNone(self.store.read_list(self.ap.STATE)["spaces"]["ha"].get("pending"))
        self.terms["t1"]["foreground"], self.window = {"name": "claude"}, [{"id": "sp2", "name": "headless-app", "folder": fresh}]
        self.put(os.path.join(self.app_home, "shared.json"), {"coordinator.slots": {"s2": {"pane": "p2", "space": "sp2"}}})
        out2 = json.loads(self.start(fresh, "--json"))
        self.assertEqual((out2["space"], out2["assistant"], out2["autopilot"], self.project.coordinator_in("sp2")), ("sp2", "t1", True, "t1"))
        self.assertEqual((self.terms["t1"]["labels"]["herdr.chat"], list(self.store.read_list(self.ap.STATE)["spaces"])), ("p2", ["sp2"]))
        self.assertEqual(self.store.read_list(self.ap.STATE)["spaces"]["sp2"]["inflight"]["to"], "t1")
        self.assertFalse(json.loads(self.start(fresh, "--autopilot", "--json"))["changed"])

    def test_dry_run_prints_steps_and_writes_nothing(self):
        spec = self.put(os.path.join(self.tmp, "spec.md"), "Spec\n")
        sb, target = os.path.join(self.tmp, "untouched"), os.path.join(self.tmp, "untouched", "proj")
        with self.exit_stack() as es:
            for o, k, v in ((self.store, "STORE", f"{sb}/tasks"), (self.st, "SETTINGS", f"{sb}/shepherdr.json"),
                            (self.ap, "STORE", f"{sb}/tasks"), (self.ap, "STATE", f"{sb}/tasks/.autopilot.json"), (self.ap, "HEARTBEATS", f"{sb}/beats")):
                es.enter_context(self.mock.patch.object(o, k, v))
            text = self.start(target, "--requirements", spec, "--autopilot", "--dry-run")
        for w in ("Git repo: would create", "Tasks: would set prefix", "Requirements: would copy",
                  "Space: would create", "Settings: would write", "Assistant: would start", "Autopilot: would set heartbeat"):
            self.assertIn(f"[dry-run] {w}", text)
        self.assertFalse(os.path.exists(sb))
        self.assertEqual([c for c in self.calls if c[:2] in (["hc", "run"], ["herdr-app", "do"])], [])

    def fork(self, name, workflows, commits=2):
        repo = os.path.join(self.tmp, name)
        for f, text in workflows.items():
            self.put(os.path.join(repo, ".github", "workflows", f), text)
        subprocess.run(["git", "init", "-q", "--initial-branch=main", repo], check=True)
        for n in range(commits):
            subprocess.run(["git", "-C", repo, "-c", "user.name=H", "-c", "user.email=h@x", "commit", "-q", "--allow-empty", "-m", f"c{n}"], check=True)
        return repo

    def test_release_workflow_on_push_to_main_gets_a_gate_item_and_base_health_instruction(self):
        """FAC-74 (field notes 28, 29): a fork's release workflow that runs on every push to main is flagged;
        an inherited repo's Assistant is told to measure the base's flaky tests first."""
        rel = "name: Release\non:\n  push:\n    branches: [main]\njobs:\n  ship:\n    steps:\n      - run: fastlane beta\n"
        tags = "name: Release\non:\n  push:\n    tags:\n      - 'v*'\njobs:\n  ship:\n    steps:\n      - run: gh release create v1\n"
        manual = "name: Deploy\non: workflow_dispatch\njobs:\n  ship:\n    steps:\n      - run: npx wrangler deploy\n"
        other = "name: Publish\non:\n  push:\n    branches:\n      - dev\njobs:\n  p:\n    steps:\n      - run: npm publish\n"
        ci = "name: CI\non: [push, pull_request]\njobs:\n  test:\n    steps:\n      - uses: actions/upload-artifact@v4\n      - run: npm test\n"
        for text, hit in ((rel, True), (tags, False), (manual, False), (other, False), (ci, False)):
            self.assertEqual(self.project.releases(text) and self.project.pushes_to(text, "main"), hit, text)
        self.assertTrue(self.project.pushes_to("on:\n  push:\n  pull_request:\n", "main") and self.project.pushes_to("on:\n  push:\n    branches-ignore: [dev]\n", "main"))
        out = lambda *a: json.loads(self.start(*a, "--autopilot", "--dry-run", "--json"))
        o1 = out(self.fork("fork1", {"release.yml": rel, "ci.yml": ci, "tags.yml": tags}))
        keys = [x["key"] for x in o1["checklist"]["items"]]
        self.assertEqual([k for k in keys if k.startswith("gate_")], ["gate_release_yml"])
        self.assertIn("gate_release_yml", keys)
        self.assertIn("gh run list --branch main --repo", o1["base_health"])
        for words in ("base-health task", "docs/flaky-tests.md", "3 times", "test retries in CI", "gate release.yml"):
            self.assertIn(words, o1["base_health"])
        self.assertTrue(any("Base health: Inherited codebase" in x for x in o1["steps"]))
        o2 = out(self.fork("fork2", {"ci.yml": ci, "tags.yml": tags, "manual.yml": manual}))
        self.assertFalse([x for x in o2["checklist"]["items"] if x["key"].startswith("gate_")])
        self.assertIn("base-health task", o2["base_health"])
        self.assertNotIn("gate ", o2["base_health"])
        o3 = out(self.fork("fresh", {}, commits=1))
        self.assertEqual(o3["base_health"], "")

    def checklist(self, *argv):
        buf = self.io.StringIO()
        with self.mock.patch.object(sys, "argv", ["herdr-task", "project", "checklist", *argv]), self.redirect(buf):
            self.assertEqual(self.cli.main(), 0)
        return buf.getvalue()

    def test_checklist_shows_a_stalled_repo_disk_and_notes_an_external_volume(self):
        repo = os.path.join(self.tmp, "web-app")
        os.makedirs(repo)
        with self.mock.patch.object(self.base, "probe_volume", return_value="the disk is stalling"):
            out = self.project.run_checklist(repo, dry=True, ap_hint=True)
        disk = next(x for x in out["items"] if x["key"] == "disk")
        self.assertEqual((disk["ready"], disk["detail"], "disk" in out["missing"]), (False, "the disk is stalling", True))
        self.assertIn("Wake or fix the disk", " ".join(out["steps"]))
        self.assertFalse([ln for ln in out["lines"] if "external volume" in ln])
        with self.mock.patch.object(self.base, "probe_volume", return_value=None):
            ok = self.project.run_checklist(repo, dry=True, ap_hint=True)
            ext = self.project.run_checklist("/Volumes/Ext/web-app", dry=True, ap_hint=True)
        self.assertIn("disk", ok["ready"])
        self.assertIn("Note: factory repo is on an external volume (/Volumes/Ext): a stall or a permission prompt stops the night; the internal disk is safer", ext["lines"])

    def test_a_stalled_disk_stops_checklist_and_start_before_any_repo_read(self):
        repo = os.path.join(self.tmp, "web-app")
        os.makedirs(repo)
        with self.mock.patch.object(self.base, "probe_volume", return_value="the disk is stalling"), \
                self.mock.patch.object(self.project, "locate", side_effect=AssertionError("read the repo")):
            out = json.loads(self.checklist(repo, "--json"))
            self.assertEqual((out["ok"], out["missing"]), (False, ["disk"]))
            self.assertIn("the disk is stalling", self.checklist(repo))
            with self.mock.patch.object(sys, "argv", ["herdr-task", "project", "start", repo]):
                self.assertNotEqual(self.cli.main(), 0)

    def answer_card(self, ask_id, choice, terminal):
        buf = self.io.StringIO()
        with self.mock.patch.object(sys, "argv", ["herdr-task", "_ask-pane", ask_id]), \
                self.mock.patch.object(sys, "stdin", self.io.StringIO(f"{choice}\n")), self.redirect(buf):
            self.assertEqual(self.cli.main(), 0)
        self.terms[terminal]["state"] = "exited"

    def test_autodetects_github_secrets_asks_cards_records_answers_and_turns_on_autopilot(self):
        bare, repo = os.path.join(self.tmp, "remote.git"), os.path.join(self.tmp, "web-app")
        subprocess.run(["git", "init", "-q", "--bare", bare], check=True)
        self.put(os.path.join(repo, "package.json"), {"scripts": {"test": "vitest"}})
        self.put(os.path.join(repo, ".github", "workflows", "ci.yml"),
                 "jobs:\n  test:\n    env:\n      T: ${{ secrets.GITHUB_TOKEN }}\n      A: ${{ secrets.API_KEY }}\n      S: ${{ secrets.SENTRY_DSN }}\n")
        self.start(repo, "--name", "Web App")
        subprocess.run(["git", "-C", repo, "add", "."], check=True)
        subprocess.run(["git", "-C", repo, "-c", "user.name=H", "-c", "user.email=h@x", "commit", "-q", "-m", "ci"], check=True)
        subprocess.run(["git", "-C", repo, "remote", "add", "origin", bare], check=True)
        subprocess.run(["git", "-C", repo, "push", "-q", "-u", "origin", "main"], check=True)
        self.gh_repo, self.gh_secrets = "acme/web-app", "NAME\tUPDATED\nAPI_KEY\t2026-11-12T00:00:00Z\n"
        out1 = json.loads(self.checklist(repo, "--json"))
        self.assertFalse(out1["ios"])
        self.assertEqual(out1["ready"], ["github", "disk"])
        self.assertFalse({"apple_signin", "asc_agreements"} & {x["key"] for x in out1["items"]})
        sec_item = next(x for x in out1["items"] if x["key"] == "ci_secrets")
        self.assertEqual((sec_item["referenced"], sec_item["missing"]), (["API_KEY", "SENTRY_DSN"], ["SENTRY_DSN"]))
        self.assertTrue(any("gh secret set SENTRY_DSN" in s for s in out1["steps"]))
        asks = self.store.read_list(self.settings_file)["spaces"]["sp1"]["project"]["asks"]
        self.assertEqual(list(asks), ["service_account", "milestone_gates", "design_pass", "model_routing", "autopilot"])
        mr_rec = self.ask_mod.load(asks["model_routing"])
        self.assertIn("agy/gemini-4-carbon-high", mr_rec["question"])
        self.assertIn("agy/gemini-4-argon-high", mr_rec["recommend"])
        self.assertFalse(bool((self.store.read_list(self.ap.STATE) or {}).get("spaces", {}).get("sp1", {}).get("on")))
        for k, choice in (("service_account", "1"), ("milestone_gates", "2"), ("design_pass", "2"), ("model_routing", "1"), ("autopilot", "1")):
            self.answer_card(asks[k], choice, self.ask_mod.load(asks[k])["terminal"])
        self.assertTrue(self.store.read_list(self.ap.STATE)["spaces"]["sp1"]["on"])
        self.assertIn("docs/requirements.md", self.ap.heartbeat_of("sp1")[0])
        self.gh_secrets = "API_KEY\t2026-11-12\nSENTRY_DSN\t2026-11-12\n"
        text2 = self.checklist(repo)
        self.assertIn("8 ready, 0 missing — ready for an unattended night.", text2)
        out2 = json.loads(self.checklist(repo, "--json"))
        self.assertEqual((out2["ok"], out2["missing"], out2["asked"], out2["steps"]), (True, [], [], []))
        with self.store.Store(self.store.locate(repo), write=False) as s:
            proj = self.st.settings_for(s, "sp1")["project"]
        self.assertEqual((proj["service_account"], proj["milestone_gates"], proj["design_pass"], proj["autopilot"], "asks" in proj),
                         ("1Password service account", "All milestones except release", "No", "Yes", False))

    def notes(self, repo):
        with self.store.Store(self.store.locate(repo), write=False) as s:
            t = self.st.settings_for(s, "sp1")["tasks"]
        return t.get("start_notes"), t.get("finish_notes")

    def test_project_start_adds_the_simulator_lines_once_and_keeps_notes(self):
        self.put(os.path.join(self.templates, "assistant.md"), "---\nname: assistant\ncommand: exec claude\n---\nCoordinate.\n")
        ios, mac = os.path.join(self.tmp, "ios-app"), os.path.join(self.tmp, "mac-app")
        self.put(os.path.join(ios, "App.xcodeproj", "project.pbxproj"), "IPHONEOS_DEPLOYMENT_TARGET = 17.0;\n")
        self.put(os.path.join(mac, "Mac.xcodeproj", "project.pbxproj"), "SDKROOT = macosx;\n")
        self.start(mac, "--json")
        self.assertEqual(self.notes(mac), ("", ""))
        self.start(ios, "--json")
        start, finish = self.notes(ios)
        self.assertEqual((start, finish), (self.project.IOS_START_NOTE, self.project.IOS_FINISH_NOTE))
        self.assertIn("herdr-task simulator lease <task>", start)
        self.assertIn("herdr-task simulator release <task>", finish)
        cfg = self.store.read_list(self.settings_file)
        t = next(v for v in cfg["spaces"].values() if v.get("folder") == ios)["tasks"]
        t["start_notes"], t["finish_notes"] = "bun install", "tell the person"
        self.put(self.settings_file, cfg)
        self.start(ios, "--json")
        self.start(ios, "--json")
        self.assertEqual(self.notes(ios), (f"bun install\n{self.project.IOS_START_NOTE}", f"tell the person\n{self.project.IOS_FINISH_NOTE}"))
        # Notes set at Everywhere are kept too: the folder's value would replace them.
        ios2 = os.path.join(self.tmp, "ios-two")
        self.put(os.path.join(ios2, "App.xcodeproj", "project.pbxproj"), "IPHONEOS_DEPLOYMENT_TARGET = 17.0;\n")
        cfg = self.store.read_list(self.settings_file)
        cfg["everywhere"] = {"tasks": {"start_notes": "every start"}}
        self.put(self.settings_file, cfg)
        self.start(ios2, "--json")
        self.start(ios2, "--json")
        self.assertEqual(self.notes(ios2), (f"every start\n{self.project.IOS_START_NOTE}", self.project.IOS_FINISH_NOTE))

    def test_ios_only_for_ios_projects_and_lists_person_steps(self):
        mac_pkg, ios_pkg, mac_xc, ios_xc = (os.path.join(self.tmp, x) for x in ("mac-pkg", "ios-pkg", "mac-xc", "ios-xc"))
        self.put(os.path.join(mac_pkg, "Package.swift"), "// .iOS(.v17)\nlet p = Package(platforms: [.macOS(.v14)])\n")
        self.put(os.path.join(ios_pkg, "Package.swift"), "let p = Package(platforms: [.iOS(.v17)])\n")
        self.put(os.path.join(mac_xc, "Mac.xcodeproj", "project.pbxproj"), "SDKROOT = macosx;\n")
        self.put(os.path.join(ios_xc, "ios", "App.xcodeproj", "project.pbxproj"), "IPHONEOS_DEPLOYMENT_TARGET = 17.0;\n")
        self.assertEqual([self.project.detect_ios(p) for p in (mac_pkg, ios_pkg, mac_xc, ios_xc)], [False, True, False, True])
        out = json.loads(self.checklist(ios_xc, "--json"))
        self.assertTrue(out["ios"])
        self.assertEqual([a["item"] for a in out["asked"]][:4], ["apple_signin", "app_record", "app_store_profile", "asc_agreements"])
        self.assertEqual(len(out["asked"]), 9)
        for words in ("gh repo create", "CI workflow", "Apple / App Store Connect", "app record", "provisioning profile"):
            self.assertTrue(any(words in s for s in out["steps"]), out["steps"])
        ap_ask = out["asked"][0]
        self.answer_card(ap_ask["id"], "3", ap_ask["terminal"])  # "Not yet"
        out2 = json.loads(self.checklist(ios_xc, "--json"))
        self.assertIn("apple_signin", out2["missing"])
        self.assertEqual(out2["asked"], [])

    def test_ios_checklist_probes_asc_agreements(self):
        """FAC-77 (field note 36): `asc apps list` saying an agreement expired fails the item before the night; anything else asks the owner."""
        ios = os.path.join(self.tmp, "ios-app")
        self.put(os.path.join(ios, "App.xcodeproj", "project.pbxproj"), "IPHONEOS_DEPLOYMENT_TARGET = 17.0;\n")
        item = lambda: next(x for x in json.loads(self.checklist(ios, "--json", "--dry-run"))["items"] if x["key"] == "asc_agreements")
        gone = "Error: A required agreement is missing or has expired. This request will not succeed until it is fixed."
        self.fake_asc(gone, 1)
        bad = item()
        self.assertEqual((bad["ready"], bad["detail"]), (False, gone))
        out = json.loads(self.checklist(ios, "--json", "--dry-run"))
        self.assertIn("asc_agreements", out["missing"])
        self.assertTrue(any("Business" in s and "HTTP 403" in s for s in out["steps"]))
        self.assertEqual(self.Path(self.tmp, "asc-args").read_text().splitlines()[0], "apps list")
        self.fake_asc("ID  NAME\n1   Demo", 0)
        self.assertTrue(item()["ready"])
        for out_, code in (("Error: network is down", 1), ("", 2)):  # another failure: not checked, the owner is asked
            self.fake_asc(out_, code)
            self.assertEqual((item()["ready"], item()["detail"]), (False, "would ask card"))
        self.fake_asc("agreement is missing or has expired", 0, sleep=5)  # a slow asc: timed out, not checked
        with self.mock.patch.object(self.project, "ASC_TIMEOUT", 1):
            self.assertEqual(item()["detail"], "would ask card")
        os.remove(os.path.join(self.asc_bin, "asc"))  # no asc at all
        self.assertEqual((item()["ready"], item()["detail"]), (False, "would ask card"))
        asked = json.loads(self.checklist(ios, "--json"))["asked"]
        ask = self.ask_mod.load(next(a["id"] for a in asked if a["item"] == "asc_agreements"))
        self.assertEqual((ask["options"][:2], ask["recommend"]), (["Yes, checked today", "Not checked"], "Yes, checked today"))
        self.answer_card(ask["id"], "2", ask["terminal"])  # "Not checked" stays missing
        self.assertIn("asc_agreements", json.loads(self.checklist(ios, "--json"))["missing"])

    def test_claude_ai_requirements_marks_to_export_and_sets_export_instruction(self):
        for good in ("https://claude.ai/artifact/Y769bCQJQT6wuRri9bLurD", "https://claude.ai/doc/abc-123",
                     "https://www.claude.ai/public/artifacts/xyz", "https://claude.ai/design/d1"):
            self.assertTrue(self.project.is_claude_url(good), good)
        for bad in ("https://example.com/spec", "https://example.com/spec?ref=https://claude.ai/artifact/x",
                    "https://notclaude.ai/doc/x", "https://claude.ai.evil.com/doc/x", ""):
            self.assertFalse(self.project.is_claude_url(bad), bad)

        art_url = "https://claude.ai/artifact/Y769bCQJQT6wuRri9bLurD"
        dry_dir = os.path.join(self.tmp, "dry-claude")
        dry_txt = self.start(dry_dir, "--requirements", art_url, "--autopilot", "--dry-run")
        self.assertIn(f"[dry-run] Requirements: would save link to {art_url} into docs/requirements.md (marked to export)", dry_txt)
        self.assertIn("[dry-run] Autopilot: would set heartbeat -> export into docs/requirements.md before planning tasks and turn on", dry_txt)
        self.assertFalse(os.path.exists(dry_dir))

        # claude.ai artifact URL with --autopilot: keeps link line, marks to export, sets export-first heartbeat, idempotent on rerun.
        r1 = os.path.join(self.tmp, "claude-art")
        out1 = json.loads(self.start(r1, "--requirements", art_url, "--autopilot", "--json"))
        req1 = self.Path(r1, "docs", "requirements.md").read_text()
        self.assertTrue(req1.startswith(f"[{art_url}]({art_url})\n"), req1)
        self.assertIn("Status: to export", req1)
        self.assertTrue(self.project.req_needs_export(r1))
        beat1 = self.ap.heartbeat_of(out1["space"])[0]
        self.assertTrue(beat1.startswith(self.project.HEARTBEAT_EXPORT_HEAD))
        self.assertIn("Before planning tasks", beat1)
        self.assertIn("export its content as Markdown into docs/requirements.md", beat1)
        rerun = self.start(r1, "--requirements", art_url, "--autopilot")
        self.assertTrue(all("found" in ln for ln in rerun.strip().splitlines()[:7]), rerun)
        self.assertFalse(json.loads(self.start(r1, "--autopilot", "--json"))["changed"])

        # claude.ai doc URL without --autopilot: still pre-seeds the export heartbeat, stays off until checklist Yes.
        doc_url = "https://claude.ai/doc/req-doc-42"
        r2 = os.path.join(self.tmp, "claude-doc")
        out2 = json.loads(self.start(r2, "--requirements", doc_url, "--json"))
        self.assertFalse(out2["autopilot"])
        self.assertIn("Status: to export", self.Path(r2, "docs", "requirements.md").read_text())
        self.assertTrue(self.ap.heartbeat_of(out2["space"])[0].startswith(self.project.HEARTBEAT_EXPORT_HEAD))
        self.assertFalse(json.loads(self.start(r2, "--json"))["changed"])
        ap_card = next(a for a in out2["checklist"]["asked"] if a["item"] == "autopilot")
        self.answer_card(ap_card["id"], "1", ap_card["terminal"])
        self.assertTrue(self.store.read_list(self.ap.STATE)["spaces"][out2["space"]]["on"])
        self.assertTrue(self.ap.heartbeat_of(out2["space"])[0].startswith(self.project.HEARTBEAT_EXPORT_HEAD))

        # Plain URL (even mentioning claude.ai in query) and local file behave exactly as before.
        plain_url = "https://example.com/spec?from=claude.ai"
        r3, r4 = os.path.join(self.tmp, "plain-url"), os.path.join(self.tmp, "local-file")
        spec_file = self.put(os.path.join(self.tmp, "local.md"), "# Spec\nBuild it.\n")
        out3 = json.loads(self.start(r3, "--requirements", plain_url, "--autopilot", "--json"))
        out4 = json.loads(self.start(r4, "--requirements", spec_file, "--autopilot", "--json"))
        self.assertEqual(self.Path(r3, "docs", "requirements.md").read_text(), f"[{plain_url}]({plain_url})\n")
        self.assertEqual(self.Path(r4, "docs", "requirements.md").read_text(), "# Spec\nBuild it.\n")
        for r, o in ((r3, out3), (r4, out4)):
            self.assertFalse(self.project.req_needs_export(r))
            beat = self.ap.heartbeat_of(o["space"])[0]
            self.assertTrue(beat.startswith(self.project.HEARTBEAT_HEAD))
            self.assertNotIn("to export", beat)


class ProbeVolume(unittest.TestCase):
    """FAC-83: probe_volume names a stalled disk without ever waiting on a stuck child."""

    def probe(self, hung):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.base as base
        made = []
        class Child:
            def __init__(self, argv, **kw):
                self.folder, self.killed = argv[-1], False
                made.append(self)
            def wait(self, timeout=None):
                if self.folder in hung:
                    raise subprocess.TimeoutExpired("sh", timeout)
            def kill(self):
                self.killed = True
        with mock.patch.object(base.subprocess, "Popen", Child), mock.patch.dict(os.environ, {"TMPDIR": "/scratch-tmp"}):
            return base.probe_volume("/Volumes/Ext/repo", timeout=1), made

    def test_answers_in_time(self):
        self.assertEqual(self.probe(set())[0], None)

    def test_repo_stalls_while_tmpdir_answers(self):
        why, made = self.probe({"/Volumes/Ext/repo"})
        self.assertIn("/Volumes/Ext", why)
        self.assertIn("the disk is stalling, or a macOS permission prompt may be waiting for a new process to reach it", why)
        self.assertEqual([c.killed for c in made], [True, False])

    def test_both_stall(self):
        why, made = self.probe({"/Volumes/Ext/repo", "/scratch-tmp"})
        self.assertIn("too busy or stuck", why)
        self.assertNotIn("disk is stalling", why)
        self.assertTrue(all(c.killed for c in made))


class Watchdog(unittest.TestCase):
    """FAC-62: watchdog restarts a stuck local Assistant once with restart.sh and alerts on repeat."""

    def setUp(self):
        from pathlib import Path
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.agents as agents, herdr_task.assistant_settings as aset, herdr_task.autopilot as autopilot
        import herdr_task.base as base, herdr_task.commands as commands, herdr_task.home as home
        import herdr_task.news as news, herdr_task.reactions as reactions, herdr_task.store as store, herdr_task.watchdog as watchdog
        self.news, self.wd, self.Path, real_sub = news, watchdog, Path, subprocess.run
        self.tmp = os.path.realpath(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.store = os.path.join(self.tmp, "tasks")
        os.makedirs(self.store)
        self.slug, self.clock, self.terms, self.screens, self.raw, self.restarts, self.alerts = store.slug_of(self.tmp), 1_800_000_000_000, {}, {}, "", [], []
        self.sent, self.tpl = [], "tpl-new"
        def fake_sub(args, env=None, **kw):
            if args[0] == "git":
                return real_sub(args, env=env, **kw)
            if args[0] == "bash" and os.path.basename(args[1]) == "restart.sh":
                old, nxt, prev = args[2], f"c{len(self.restarts) + 2}", self.terms.pop(args[2])
                self.restarts.append((old, nxt, (env or {}).get("HERDR_RESTART_CHAT")))
                lbs = {k: v for k, v in prev["labels"].items() if not ((env or {}).get("HERDR_RESTART_CHAT") and k == "herdr.space") and k != "herdr.template"}
                self.terms[nxt] = {**prev, "address": nxt, "turn": 0, "status": {"state": "idle"}, "created_ms": self.clock, "labels": lbs}
                return subprocess.CompletedProcess(args, 0, f"{nxt}\n", "")
            if args[:2] == ["hc", "ls"]:
                k, v = args[args.index("--label") + 1].split("=", 1) if "--label" in args else (None, None)
                return subprocess.CompletedProcess(args, 0, json.dumps({"terminals": [t for t in self.terms.values() if not k or (t.get("labels") or {}).get(k) == v]}), "")
            if args[:2] == ["hc", "get"]:
                return subprocess.CompletedProcess(args, 0 if (t := self.terms.get(args[2])) else 1, json.dumps(t) if t else "", "")
            if args[:2] == ["hc", "read"]:
                return subprocess.CompletedProcess(args, 0, self.screens.get(args[2], ""), "")
            if args[:2] == ["hc", "send"]:
                self.sent.append((args[2], args[3]))
            if args[:2] == ["hc", "label"]:
                k, v = args[3].split("=", 1)
                self.terms[args[2]].setdefault("labels", {})[k] = v
                if k == "herdr.alert":
                    self.alerts.append((args[2], json.loads(v)))
            return subprocess.CompletedProcess(args, 0, "", "")

        p_, noop = mock.patch.object, lambda *_: None
        patches = ([p_(m, "now", lambda: self.clock) for m in (base, agents, news, commands, watchdog)]
                   + [p_(m, "STORE", self.store) for m in (store, watchdog, news)]
                   + [p_(subprocess, "run", fake_sub), p_(watchdog, "WATCHDOG", os.path.join(self.store, ".watchdog.json")),
                      p_(watchdog, "template_info", lambda: (self.tpl, self.clock - 5 * 60000)),
                      p_(news, "NEWS", os.path.join(self.store, ".news.json")), p_(news, "HELD", os.path.join(self.store, ".held.json")),
                      p_(autopilot, "STATE", os.path.join(self.store, ".autopilot.json")), p_(news, "POLL_DELAYS", (0,)),
                      p_(news, "screen_raw", lambda *_: self.raw),
                      *(p_(m, fn, noop) for m, fn in ((home, "log_news"), (aset, "settings_pass"), (reactions, "reactions_pass"), (base, "guard"), (news, "guard"), (watchdog, "guard"), (watchdog, "held_ask")))])
        for p in patches:
            p.start()
            self.addCleanup(p.stop)
        self.coord("c1", chat="p1")

    def coord(self, addr="c1", space="shop", status="done", turn=2, chat=None):
        self.terms[addr] = {"address": addr, "state": "live", "status": {"state": status}, "turn": turn, "foreground": {"name": "claude"},
                            "labels": {"herdr.role": "coordinator", "herdr.space": space, **({"herdr.chat": chat} if chat else {})}}

    def worker(self, addr, tid, status="working", **extra):
        self.terms[addr] = {"address": addr, "state": "live", "status": {"state": status}, "foreground": {"name": "claude"}, "labels": {"herdr.task": f"{self.slug}#{tid}"}, **extra}

    def save_tasks(self, *rows, delivered=None, inflight=None):
        put = lambda fn, d: self.Path(self.store, fn).write_text(json.dumps(d))
        put(f"{self.slug}.json", {"folder": self.tmp, "prefix": "SHOP", "tasks": list(rows)})
        if delivered is not None:
            put(".news.json", {"delivered": {f"{self.slug}#{k}": v for k, v in delivered.items()}, "outbox": []})
        if inflight is not None:
            put(".autopilot.json", {"version": 2, "spaces": {"shop": {"on": True, "inflight": inflight}}})

    def tick(self):
        self.wd.watchdog_pass(report := {})
        return report

    def test_idle_with_stale_needs_you_restarts_once_and_second_time_alerts(self):
        t1 = {"id": 1, "title": "Auth", "state": "running", "space": "shop", "attention": "Need API key", "attention_at": self.clock - 10 * 60000}
        self.save_tasks(t1, delivered={1: {"to": "c1", "turn": 2, "at": self.clock - 10 * 60000, "event": "needs-you"}})
        self.tick()
        self.assertEqual(self.restarts, [])
        self.clock += 21 * 60000  # 31m in Needs you with no turn since news reached c1: restart once
        self.tick()
        self.assertEqual((self.restarts, self.alerts, os.path.isfile(os.path.join(self.store, ".watchdog.json"))), ([("c1", "c2", "p1")], [], True))
        self.terms["c2"].update(status={"state": "done"}, turn=1)
        self.clock += 35 * 60000  # c2 handled the handoff turn: pre-restart SHOP-1 doesn't re-trigger
        self.tick()
        self.assertEqual((len(self.restarts), len(self.alerts)), (1, 0))
        # Stuck again within 2 hours (even after HERDR_RESTART_CHAT dropped herdr.space): alerts once, doesn't repeat.
        t2 = {"id": 2, "title": "Pay", "state": "running", "space": "shop", "agents": [{"address": "w2", "at": self.clock - 5000, "ended": self.clock}]}
        self.save_tasks(t1, t2, delivered={2: {"to": "c2", "turn": 1, "at": self.clock, "event": "failed"}})
        self.clock += 31 * 60000
        self.tick()
        self.assertEqual((len(self.restarts), len(self.alerts)), (1, 1))
        self.assertTrue(self.alerts[0][1]["title"].startswith("shop's Assistant is stuck: SHOP-2 failed"), self.alerts[0][1])
        self.clock += 5 * 60000
        self.tick()
        self.assertEqual(len(self.alerts), 1)
        self.terms["c2"]["turn"] = 2  # After a turn and past the 2-hour cooldown, a stalled task restarts again
        self.clock += self.wd.RESTART_COOLDOWN_MS
        self.worker("w3", 3, status_ms=self.clock - 60 * 60000)
        self.save_tasks({"id": 3, "title": "Ship", "state": "running", "space": "shop", "started": self.clock - 60 * 60000, "agents": [{"address": "w3", "at": self.clock - 60 * 60000}]},
                        delivered={3: {"to": "c2", "turn": 2, "at": self.clock - 40 * 60000, "event": "stalled", "stall": f"quiet:{self.clock - 60 * 60000}"}})
        self.tick()
        self.assertEqual(len(self.restarts), 2)

    def test_a_stalled_disk_alerts_once_per_spell_and_blocks_restarts(self):
        from unittest import mock
        t1 = {"id": 1, "title": "Auth", "state": "running", "space": "shop", "attention": "Need API key", "attention_at": self.clock - 40 * 60000}
        self.save_tasks(t1, delivered={1: {"to": "c1", "turn": 2, "at": self.clock - 40 * 60000, "event": "needs-you"}})  # would restart if the disk answered
        answers = ["/Volumes/Ext is stalling"]
        with mock.patch.object(self.wd, "probe_volume", side_effect=lambda *_: answers[0]) as pv:
            rep = self.tick()
            self.assertEqual((len(self.alerts), self.restarts, [x["action"] for x in rep["watchdog"]]), (1, [], ["disk-stalled"]))
            self.assertIn(self.tmp, self.alerts[0][1]["title"])
            self.assertIn("/Volumes/Ext is stalling", self.alerts[0][1]["title"])
            self.clock += 5 * 60000
            self.assertEqual((self.tick(), len(self.alerts), self.restarts), ({}, 1, []))
            self.assertIn("stall", json.loads(self.Path(self.store, ".watchdog.json").read_text())["shop"])
            answers[0] = None  # recovered: the spell clears and the stuck Assistant is restarted
            self.tick()
            self.assertEqual((len(self.restarts), "stall" in json.loads(self.Path(self.store, ".watchdog.json").read_text()).get("shop", {})), (1, False))
            answers[0] = "/Volumes/Ext is stalling"  # a new spell alerts again
            self.tick()
            self.assertEqual(len(self.alerts), 2)
        self.assertEqual(pv.call_count, 4)

    def blank(self, addr, tid, age_ms=3 * 60000, **extra):
        self.worker(addr, tid, status="idle", turn=0, created_ms=self.clock - age_ms, **extra)

    def test_two_blank_starts_alert_once_and_hold_off_restarts_until_a_good_start(self):
        t1 = {"id": 1, "title": "Auth", "state": "running", "space": "shop", "attention": "Need API key", "attention_at": self.clock - 40 * 60000}
        rows = [t1, {"id": 2, "title": "Pay", "state": "running", "space": "other"}, {"id": 3, "title": "Ship", "state": "running", "space": "shop"}]
        self.save_tasks(*rows, delivered={1: {"to": "c1", "turn": 2, "at": self.clock - 40 * 60000, "event": "needs-you"}})  # would restart
        self.blank("w2", 2)
        self.worker("w3", 3, status="working", turn=1, created_ms=self.clock - 3 * 60000)  # a start that went fine is no blank one
        self.tick()
        self.assertEqual((len(self.alerts), len(self.restarts)), (0, 1))  # one blank start is no outage
        self.terms.pop("c2")
        self.terms.pop("w3")
        self.coord("c1", chat="p1")
        self.Path(self.store, ".watchdog.json").unlink()
        self.blank("w3", 3)
        rep = self.tick()
        self.assertEqual((len(self.alerts), [x["action"] for x in rep["watchdog"]]), (1, ["outage"]))
        self.assertRegex(self.alerts[0][1]["title"], r"^New agents start blank: 2 since \d\d:\d\d; likely herdr-core")
        self.assertIn("_outage", json.loads(self.Path(self.store, ".watchdog.json").read_text()))
        self.clock += 5 * 60000
        self.terms["w2"]["created_ms"] += 5 * 60000
        self.terms["w3"]["created_ms"] += 5 * 60000
        self.assertEqual((self.tick(), len(self.alerts), len(self.restarts)), ({}, 1, 1))  # once per spell, no Assistant restart in it
        self.worker("w4", 3, status="working", turn=1, created_ms=self.clock - 1000)  # a new agent starts fine: the spell is over
        self.tick()
        self.assertNotIn("_outage", json.loads(self.Path(self.store, ".watchdog.json").read_text()))

    def test_a_good_start_between_two_blank_ones_is_no_outage(self):
        self.save_tasks(*[{"id": i, "title": "T", "state": "running", "space": "shop"} for i in (1, 2, 3)])
        self.blank("w1", 1, age_ms=5 * 60000)
        self.worker("w2", 2, status="working", turn=1, created_ms=self.clock - 3 * 60000)
        self.blank("w3", 3, age_ms=2 * 60000 + 30000)
        self.assertEqual((self.tick(), self.alerts), ({}, []))
        self.assertNotIn("_outage", json.loads(self.Path(self.store, ".watchdog.json").read_text()) if os.path.exists(os.path.join(self.store, ".watchdog.json")) else {})

    def test_a_restart_ends_the_old_agents_group(self):
        from unittest import mock
        self.terms["c1"]["foreground"]["pid"] = 4242
        self.save_tasks({"id": 1, "title": "Auth", "state": "running", "space": "shop", "attention": "x", "attention_at": self.clock - 40 * 60000},
                        delivered={1: {"to": "c1", "turn": 2, "at": self.clock - 40 * 60000, "event": "needs-you"}})
        with mock.patch.object(self.wd, "end_group") as eg:
            self.tick()
        self.assertEqual((len(self.restarts), eg.call_args_list), (1, [mock.call(4242)]))

    def test_a_stall_spell_survives_the_assistant_being_away(self):
        from unittest import mock
        self.save_tasks({"id": 1, "title": "Auth", "state": "running", "space": "shop"})
        with mock.patch.object(self.wd, "probe_volume", return_value="stalling"):
            self.tick()
            del self.terms["c1"]
            self.tick()
            self.assertIn("stall", json.loads(self.Path(self.store, ".watchdog.json").read_text())["shop"])
            self.coord("c1", chat="p1")
            self.tick()
        self.assertEqual(len(self.alerts), 1)

    def test_ready_behind_and_turn_since_news(self):
        queued = {"id": 2, "title": "Follow-up", "state": "queued", "space": "shop", "after": [1]}
        self.save_tasks({"id": 1, "title": "Base", "state": "todo", "space": "shop"}, queued)
        self.news.news_pass({})
        self.terms["c1"]["status"] = {"state": "working"}  # working on turn 2 when news arrives (queued behind turn 3)
        self.save_tasks({"id": 1, "title": "Base", "state": "ready", "space": "shop", "ready_at": self.clock}, queued)
        self.news.news_pass({})
        self.clock += 10000
        self.news.news_pass({})
        self.terms["c1"].update(status={"state": "done"}, turn=3, status_ms=self.clock + 1000)
        self.clock += 2 * 60000
        self.terms["c1"]["turn"] = 4  # queued turn ran (turn 4 > 3): left alone
        self.tick()
        self.assertEqual(self.restarts, [])
        self.terms["c1"]["turn"] = 3  # queued turn never ran (turn 3 == base_turn): restarted
        self.tick()
        self.assertEqual(self.restarts, [("c1", "c2", "p1")])

    def test_busy_changing_assistant_is_left_alone(self):
        self.terms["c1"]["status"] = {"state": "working"}
        for step in ("Step 1\n", "Step 2\n", "Step 3\n"):
            self.screens["c1"] = step
            self.tick()
            self.clock += 20 * 60000
        self.assertEqual((self.restarts, self.alerts), ([], []))
        self.tick()
        self.clock += 30 * 60000
        self.tick()
        self.assertEqual(len(self.restarts), 1)

    def test_typing_autopilot_and_remote_assistants_hold_it(self):
        self.coord("vm/c9", space="shop")
        stale = {"id": 1, "title": "Fix", "state": "running", "space": "shop", "attention": "Ask", "attention_at": self.clock - 35 * 60000}
        delivered = {1: {"to": "c1", "turn": 2, "at": self.clock - 35 * 60000, "event": "needs-you"}}
        self.save_tasks(stale, delivered=delivered)
        self.terms["c1"]["labels"][self.news.TYPING] = str(self.clock - 1000)
        self.tick()
        self.terms["c1"]["labels"].pop(self.news.TYPING)
        self.raw = "❯ wait a second\n"
        self.tick()
        self.raw = ""
        self.save_tasks(stale, delivered=delivered, inflight={"at": self.clock - 60000, "reason": "schedule", "to": "c1", "turn": 2})
        self.tick()
        self.assertEqual(self.restarts, [])
        self.save_tasks(stale, delivered=delivered, inflight=False)
        self.tick()
        self.assertEqual((self.restarts, "vm/c9" in self.terms), ([("c1", "c2", "p1")], True))

    def test_older_worker_or_attention_timestamps_wait_full_30m(self):
        t0 = self.clock
        for i in (1, 2, 3):
            self.worker(f"w{i}", i)
        self.save_tasks({"id": 1, "title": "Build", "state": "running", "space": "shop", "started": t0, "agents": [{"address": "w1", "at": t0}]},
                        {"id": 2, "title": "Ask", "state": "running", "space": "shop", "started": t0, "attention_at": t0, "agents": [{"address": "w2", "at": t0}]},
                        {"id": 3, "title": "CI", "state": "running", "space": "shop", "started": t0, "attention": "Checks failed", "attention_at": t0, "agents": [{"address": "w3", "at": t0}]})
        self.news.news_pass({})
        self.clock += 35 * 60000  # 35m later: w1 exits, w2 blocks (stale attention_at=t0), w3 ends without fixing attention
        self.terms.pop("w1")
        self.terms["w2"].update(status={"state": "blocked"}, status_ms=self.clock)
        self.terms["w3"].update(status={"state": "done"}, status_ms=self.clock)
        self.news.news_pass({})
        self.clock += 6000
        self.news.news_pass({})
        self.clock += 2 * 60000  # 2m (> grace, < 30m): must NOT restart early from t0 timestamps
        self.tick()
        self.assertEqual(self.restarts, [])
        self.clock += 29 * 60000  # 31m after entering Failed/Needs you: restarts once
        self.tick()
        self.assertEqual(self.restarts, [("c1", "c2", "p1")])

    def test_api_error_gets_one_nudge_then_one_restart_then_an_alert(self):
        err = "● Working on SHOP-1\n  ⎿  API Error: 403 (Cloudflare) · Please run /login\n\n❯ \n"
        self.save_tasks({"id": 1, "title": "Auth", "state": "running", "space": "shop"})
        self.screens["c1"] = "● Done, SHOP-1 is running\n❯ \n"
        self.assertEqual((self.tick(), self.sent), ({}, []))  # no error: left alone
        self.screens["c1"] = err
        rep = self.tick()
        self.assertEqual((self.sent, self.restarts, [x["action"] for x in rep["watchdog"]]), ([("c1", self.wd.NUDGE)], [], ["nudged"]))
        self.tick()  # the nudge has had no time yet: no second nudge, no restart
        self.assertEqual((len(self.sent), self.restarts), (1, []))
        self.clock += 3 * 60000
        rep = self.tick()  # still on the error: restarted with its handoff
        self.assertEqual((self.restarts, len(self.sent)), ([("c1", "c2", "p1")], 1))
        self.terms["c2"].update(turn=1)
        self.screens["c2"] = err
        self.clock += 3 * 60000
        rep = self.tick()  # the new one hits it too: alert, no more restarts
        self.assertEqual((len(self.restarts), len(self.alerts), [x["action"] for x in rep["watchdog"]]), (1, 1, ["alerted"]))
        self.assertIn("stopped on an API error", self.alerts[0][1]["title"])
        self.clock += 10 * 60000
        self.tick()
        self.assertEqual((len(self.restarts), len(self.alerts), len(self.sent)), (1, 1, 1))

    def test_a_nudge_that_worked_is_not_followed_by_a_restart(self):
        err = "  ⎿  API Error: 403 (Cloudflare) · Please run /login\n"
        self.save_tasks({"id": 1, "title": "Auth", "state": "running", "space": "shop"})
        self.screens["c1"] = err + "\n❯ \n"
        self.tick()
        self.assertEqual(len(self.sent), 1)
        self.screens["c1"] = f"{err}\n❯ {self.wd.NUDGE}\n\n● Back on it, SHOP-1 is running.\n\n❯ \n"  # recovered: the old error is still in the tail
        self.clock += 3 * 60000
        self.assertEqual((self.tick(), self.restarts, self.alerts), ({}, [], []))
        self.screens["c1"] += err  # a fresh error after that: a new spell, a new nudge, not a restart
        self.tick()
        self.assertEqual((len(self.sent), self.restarts), (2, []))

    def test_api_error_is_not_acted_on_without_work_waiting_or_when_signed_out(self):
        self.screens["c1"] = "  ⎿  API Error: 529 overloaded_error\n❯ \n"
        self.save_tasks({"id": 1, "title": "Auth", "state": "done", "space": "shop"})
        self.assertEqual((self.tick(), self.sent), ({}, []))
        self.save_tasks({"id": 1, "title": "Auth", "state": "running", "space": "shop"})
        self.terms["c1"]["status"] = {"state": "working"}  # mid-turn: never nudged
        self.assertEqual((self.tick(), self.sent), ({}, []))
        self.terms["c1"]["status"] = {"state": "done"}
        self.screens["c1"] = "  ⎿  API Error: 401 · Invalid API key · Please run /login\n❯ \n"
        self.tick()
        self.clock += 3 * 60000
        rep = self.tick()  # signed out: the nudge failed, so an alert and no restart
        self.assertEqual((len(self.sent), self.restarts, len(self.alerts), [x["action"] for x in rep["watchdog"]]), (1, [], 1, ["alerted"]))

    def test_idle_assistant_on_an_old_template_restarts_once(self):
        self.terms["c1"].update(created_ms=self.clock - 60 * 60000, labels={**self.terms["c1"]["labels"], "herdr.template": "tpl-old"})
        self.terms["c1"]["status"] = {"state": "working"}
        self.screens["c1"] = "busy\n"
        self.assertEqual((self.tick(), self.restarts), ({}, []))  # mid-turn: not restarted
        self.terms["c1"]["status"] = {"state": "done"}
        rep = self.tick()
        self.assertEqual((self.restarts, rep["watchdog"][0]["why"]), ([("c1", "c2", "p1")], "running an old Assistant template"))
        self.assertEqual(self.terms["c2"]["labels"].get("herdr.template"), None)
        self.clock += 60000
        self.tick()  # the new one is labelled with the current template at first sight: left alone
        self.assertEqual((len(self.restarts), self.terms["c2"]["labels"]["herdr.template"]), (1, "tpl-new"))
        self.terms["c2"]["labels"]["herdr.template"] = "tpl-old"  # still old after a restart: not tried again for the same template
        self.clock += self.wd.RESTART_COOLDOWN_MS + 60000
        self.tick()
        self.assertEqual(len(self.restarts), 1)
        self.tpl = "tpl-newer"  # a new change reaches it
        self.tick()
        self.assertEqual(len(self.restarts), 2)

    def test_assistant_started_before_the_template_changed_is_labelled_unknown_and_restarted(self):
        self.terms["c1"]["created_ms"] = self.clock - 60 * 60000  # no label: older than the template file
        rep = self.tick()
        self.assertEqual((self.restarts, rep["watchdog"][0]["why"]), ([("c1", "c2", "p1")], "running an old Assistant template"))


class DesignAndDocReview(unittest.TestCase):
    """FAC-69: design and doc tasks (--set review.kind=design|doc) get 1 review round and review_min 1,
    overriding the space's settings, and review-more refuses and tells the Assistant to hand to the person."""

    git, change, ready, verdict, env = GateReview.git, GateReview.change, GateReview.ready, GateReview.verdict, GateReview.env

    def setUp(self):
        GateReview.setUp(self)
        from herdr_task import settings
        self.settings = settings
        with open(settings.SETTINGS, "w") as f:
            json.dump({"everywhere": {"tasks": {"review_rounds": 5, "review_min": 2, "merge": "checks"}}}, f)
        self.task.pop("set", None)

    def show_out(self, as_json=False):
        import argparse, io
        from unittest import mock
        from herdr_task import output
        buf = io.StringIO()
        with mock.patch.object(self.commands, "view", output.view), mock.patch.object(self.commands, "say", output.say), \
                mock.patch("sys.stdout", buf):
            self.commands.cmd_show(argparse.Namespace(id="SHEP-13", json=as_json, folder=None, evidence=False))
        return json.loads(buf.getvalue()) if as_json else buf.getvalue()

    def review_more(self):
        import argparse
        from unittest import mock
        sent = []
        with mock.patch("herdr_task.news.send_or_hold", lambda to, msg: sent.append(msg) or True), \
                mock.patch.object(self.commands, "workers", lambda s, t: [{"address": "w1"}]), \
                mock.patch.object(self.commands, "caller_labels", lambda: {}):
            self.commands.cmd_review_more(argparse.Namespace(id="SHEP-13", json=True, folder=None))
        return sent

    def test_design_and_doc_kinds_override_space_and_normal_tasks_are_unchanged(self):
        # Normal task: keeps the space's 5 rounds and min 2.
        self.assertEqual((self.settings.gate_min(self.store, self.task), self.settings.review_limits(self.store, self.task)),
                         (2, (True, 5)))
        for raw, canon in (("design", "design"), ("doc", "doc"), ("docs", "doc")):
            self.settings.set_task(self.task, "review.kind", raw)
            self.assertEqual(self.task["set"], {"review_kind": canon})
            self.assertEqual(self.settings.review_kind(self.task), canon)
            self.assertEqual(self.settings.gate_min(self.store, self.task), 1)
            self.assertEqual(self.settings.review_limits(self.store, self.task), (True, 1))
        # Also overrides a space where review is off by default (review_rounds=0, review_min=0).
        with open(self.settings.SETTINGS, "w") as f:
            json.dump({"everywhere": {"tasks": {"review_rounds": 0, "review_min": 0}}}, f)
        self.assertEqual((self.settings.gate_min(self.store, self.task), self.settings.review_limits(self.store, self.task)),
                         (1, (True, 1)))
        with self.assertRaises(self.settings.Fail):
            self.settings.set_task(self.task, "review.kind", "backend")
        self.settings.set_task(self.task, "review.kind", "")
        self.assertNotIn("set", self.task)
        self.assertEqual((self.settings.gate_min(self.store, self.task), self.settings.review_limits(self.store, self.task)),
                         (0, (False, 0)))

    def test_show_and_json_report_kind_and_one_round(self):
        normal_txt, normal_js = self.show_out(False), self.show_out(True)
        self.assertNotIn("review kind:", normal_txt)
        self.assertIn("review rounds: 0 of 5 used", normal_txt)
        self.assertNotIn("review_kind", normal_js)
        self.assertEqual(normal_js["review_rounds"], {"used": 0, "of": 5, "added": 0})

        self.settings.set_task(self.task, "review.kind", "design")
        txt, js = self.show_out(False), self.show_out(True)
        self.assertIn("review kind: design (1 review round)", txt)
        self.assertIn("review rounds: 0 of 1 used", txt)
        self.assertIn("needs 1 passing round;", txt)
        self.assertIn("- Review kind: design (1 review round, then the person reviews it) (set by the assistant for this task)", txt)
        self.assertIn("- Review rounds: at most 1 (set by the assistant for this task)", txt)
        self.assertEqual(js["review_kind"], "design")
        self.assertEqual(js["review_rounds"], {"used": 0, "of": 1, "added": 0, "min": 1, "kind": "design"})

    def test_one_round_gate_and_review_more_refusal_vs_normal_task(self):
        # Design task: 1 passing round puts it straight in Ready (no round 2), and review-more refuses.
        self.settings.set_task(self.task, "review.kind", "design")
        with self.assertRaises(self.commands.Fail) as err:
            self.review_more()
        self.assertNotIn("\n", str(err.exception))
        self.assertIn("design task (1 review round)", str(err.exception))
        self.assertIn("Ready / Accept card", str(err.exception))
        self.assertNotIn("review_rounds_added", self.task)
        self.ready()
        self.assertEqual(len(self.runs), 1)
        self.verdict("term-1", passed=True)
        self.assertEqual((self.task["state"], len(self.runs)), ("ready", 1))

        # Doc task whose 1 round asks for changes: stops after round 1, persists review_out on disk across
        # cmd_ready's two open_store blocks, review-more refuses, and handing to Ready / Accept card
        # (--set review=off) waits for the person instead of auto-merging.
        import argparse
        from unittest import mock
        from herdr_task import store as task_store
        self.task.update(state="running", set={"review_kind": "doc"})
        for k in ("reviews", "review_runs", "verdict_told", "ready_at", "auto_merge"):
            self.task.pop(k, None)
        self.runs.clear()
        self.change("doc draft")
        self.ready()
        self.verdict("term-1", passed=False, changes="tighten intro", must_fix=1)
        self.change("doc tightened")
        sdir = os.path.join(self.root, "store")
        with mock.patch.object(task_store, "STORE", sdir), \
                mock.patch.object(self.commands, "open_store", task_store.open_store):
            with task_store.open_store(argparse.Namespace(folder=self.main)) as s:
                s.data.update(prefix="SHEP", tasks=[json.loads(json.dumps(self.task))])
                s.dirty = True
            self.commands.cmd_ready(argparse.Namespace(id="SHEP-13", json=True, folder=self.main, summary="The gate.",
                                                       details=None, proof=None, agents_md=None))
            with task_store.open_store(argparse.Namespace(folder=self.main), write=False) as s:
                on_disk = s.find("SHEP-13")
        self.assertTrue(on_disk.get("review_out"))
        self.task.update(on_disk)
        self.assertEqual((self.task["state"], len(self.runs), bool(self.task.get("review_out"))), ("running", 1, True))
        with self.assertRaises(self.commands.Fail) as err2:
            self.review_more()
        self.assertNotIn("\n", str(err2.exception))
        self.assertIn("doc task (1 review round)", str(err2.exception))
        self.assertIn("Ready / Accept card", str(err2.exception))
        self.assertNotIn("review_rounds_added", self.task)
        self.settings.set_task(self.task, "review", "off")
        self.ready()
        self.assertEqual(self.task["state"], "ready")
        self.assertNotIn("auto_merge", self.task)

        # Normal task: review-more still adds a round once its rounds are used.
        self.task.update(state="running", set={"review_rounds": 1, "review_min": 2})
        for k in ("reviews", "review_runs", "verdict_told", "ready_at", "review_out"):
            self.task.pop(k, None)
        self.runs.clear()
        self.change("code change")
        self.ready()
        self.verdict("term-1", passed=True)
        sent = self.review_more()
        self.assertEqual(self.task["review_rounds_added"], 1)
        self.assertEqual(len(sent), 1)


class GhAlwaysNamesForkRepo(unittest.TestCase):
    """FAC-68: every repo-targeting gh call names the fork's repo from the push remote, never upstream."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.base as base, herdr_task.checks as checks, herdr_task.commands as commands
        import herdr_task.ghnews as ghnews, herdr_task.github as github, herdr_task.project as project, herdr_task.store as store
        self.base, self.checks, self.commands, self.ghnews, self.github, self.project, self.store = base, checks, commands, ghnews, github, project, store
        self.tmp = os.path.realpath(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.store_dir, self.bin_dir, self.bare, self.main, self.wt = (os.path.join(self.tmp, x) for x in ("tasks", "bin", "fork.git", "main", "wt"))
        for d in (self.store_dir, self.bin_dir):
            os.makedirs(d)
        self.log, fake_gh = os.path.join(self.tmp, "gh.jsonl"), os.path.join(self.bin_dir, "gh")
        with open(fake_gh, "w") as f:
            f.write("#!/usr/bin/env python3\nimport json, os, sys\nargs = sys.argv[1:]\n"
                    f"prior = open({self.log!r}).read() if os.path.exists({self.log!r}) else ''\n"
                    f"with open({self.log!r}, 'a') as lg: lg.write(json.dumps({{'argv': args, 'gh_repo': os.environ.get('GH_REPO')}}) + '\\n')\n"
                    "if args[:2] == ['repo', 'view']:\n"
                    "    slug = args[2] if len(args) > 2 and not args[2].startswith('-') else 'eliasstravik/shepherdr'\n"
                    "    print(slug if '-q' in args else json.dumps({'nameWithOwner': slug}))\n"
                    "elif args[:1] in (['pr'], ['run'], ['secret']):\n"
                    "    r = args[args.index('--repo') + 1] if '--repo' in args else sys.exit('missing --repo')\n"
                    "    if args[:2] == ['pr', 'list']: print('[]')\n"
                    "    elif args[:2] == ['pr', 'create']: print(f'https://github.com/{r}/pull/50')\n"
                    "    elif args[:2] == ['pr', 'view']:\n"
                    "        if os.environ.get('DENY_ROLLUP') and 'statusCheckRollup' in ','.join(args):\n"
                    "            sys.stderr.write('GraphQL: Resource not accessible by integration\\n'); sys.exit(1)\n"
                    "        st = 'MERGED' if '\"merge\"' in prior else 'OPEN'\n"
                    "        print(json.dumps({'state': st, 'mergeable': 'MERGEABLE', 'headRefOid': os.environ.get('FAKE_HEAD', ''), 'mergeCommit': {'oid': 'abc1234'},\n"
                    "                          'mergeStateStatus': 'CLEAN', 'statusCheckRollup': [{'name': 'check', 'status': 'COMPLETED', 'conclusion': 'SUCCESS'}]}))\n"
                    "    elif args[:2] == ['run', 'list']: print(json.dumps([{'headSha': 'deadbeef', 'status': 'completed', 'conclusion': 'failure', 'url': f'https://github.com/{r}/actions/runs/9', 'workflowName': 'Check', 'databaseId': 9}]))\n"
                    "    elif args[:2] == ['run', 'view']: print('step failed')\n"
                    "    elif args[:2] == ['secret', 'list']: print('API_KEY\\t2026-11-12')\n"
                    "elif args[:1] == ['api']:\n"
                    "    p = next((x for x in args[1:] if not x.startswith('-')), '')\n"
                    "    print(json.dumps({'login': 'fork'} if p == 'user' else {'check_runs': [{'name': 'check', 'status': 'completed', 'conclusion': 'success'}]} if 'check-runs' in p\n"
                    "          else {'statuses': []} if p.endswith('/status') else [[]] if '--slurp' in args else ['deadbeef'] if '/commits?' in p else {}))\n"
                    "elif args[:1] == ['auth']: print('Logged in')\n")
        os.chmod(fake_gh, 0o755)
        g = lambda *a, cwd=self.main: subprocess.run(["git", "-C", cwd, *a], check=True)
        subprocess.run(["git", "init", "-q", "--bare", self.bare], check=True)
        subprocess.run(["git", "init", "-q", "-b", "kc-factory", self.main], check=True)
        for k, v in (("user.name", "H"), ("user.email", "h@x"), (f"url.{self.bare}.insteadOf", "git@github-fork:fork/shepherdr.git")):
            g("config", k, v)
        with open(os.path.join(self.main, "a.txt"), "w") as f:
            f.write("base\n")
        g("add", "."); g("commit", "-q", "-m", "init")
        g("remote", "add", "origin", "git@github-fork:fork/shepherdr.git")
        g("remote", "add", "upstream", "https://github.com/eliasstravik/shepherdr.git")
        g("config", "remote.upstream.pushurl", "DISABLED"); g("push", "-q", "-u", "origin", "kc-factory")
        self.start_sha = subprocess.check_output(["git", "-C", self.main, "rev-parse", "HEAD"], text=True).strip()
        g("worktree", "add", "-q", "-b", "task/68-demo", self.wt, "kc-factory")
        with open(os.path.join(self.wt, "a.txt"), "w") as f:
            f.write("change\n")
        g("commit", "-q", "-am", "FAC-68: change", cwd=self.wt)
        self.tip = subprocess.check_output(["git", "-C", self.wt, "rev-parse", "HEAD"], text=True).strip()
        env = {"PATH": f"{self.bin_dir}:{os.environ['PATH']}", "FAKE_HEAD": self.tip, "GH_REPO": "eliasstravik/shepherdr"}
        for p in (mock.patch.dict(os.environ, env), mock.patch.object(base, "GH", "gh"), mock.patch.object(github, "GH", "gh"),
                  mock.patch.object(store, "STORE", self.store_dir), mock.patch.object(github, "STORE", self.store_dir),
                  mock.patch.object(github, "live_agents", lambda *_: []), mock.patch.object(github, "live_helpers", lambda *_: [])):
            p.start(); self.addCleanup(p.stop)

    def calls(self):
        with open(self.log) as f:
            return [json.loads(ln) for ln in f]

    def test_pr_create_view_checks_news_and_merge_carry_fork_repo(self):
        t = {"id": 68, "title": "Demo", "state": "ready", "mode": "worktree", "worktree": self.wt,
             "branch": "task/68-demo", "base": "kc-factory", "start": self.start_sha, "head": self.tip}
        with self.store.Store(self.store.locate(self.main), write=True) as s:
            s.data.update(prefix="FAC", github={"repo": "eliasstravik/shepherdr", "at": self.base.now()}, tasks=[t])
            self.assertEqual((self.github.github(s, t), self.github.github(s, t)), ("fork/shepherdr", "fork/shepherdr"))
            self.assertEqual(self.github.pr_open(s, t, "fork/shepherdr", "Summary"),
                             {"number": 50, "url": "https://github.com/fork/shepherdr/pull/50", "repo": "fork/shepherdr"})
            self.assertEqual(self.github.pr_view(t)["state"], "OPEN")
            with unittest.mock.patch.dict(os.environ, {"DENY_ROLLUP": "1"}):
                self.assertEqual(self.github.pr_view(t)["statusCheckRollup"][0]["conclusion"], "SUCCESS")
            self.assertEqual(self.checks.ci_of(t, self.tip)["state"], "success")
            self.assertEqual((self.ghnews.pr_comments("fork/shepherdr", 50), self.ghnews.log_tail("fork/shepherdr", 9)), ([], "step failed"))
            self.ghnews.main_check(s, "kc-factory", {"space": "s"}, {})
            self.assertIsNone(self.commands.merge_now(s, t))
            self.assertEqual(t["state"], "done")
        argvs = [c["argv"] for c in self.calls()]
        self.assertTrue(all(c["gh_repo"] is None for c in self.calls()))
        self.assertEqual([a for a in argvs if a[:2] == ["repo", "view"]], [["repo", "view", "fork/shepherdr", "--json", "nameWithOwner", "-q", ".nameWithOwner"]])
        for sub in (["pr", "create"], ["pr", "view"], ["pr", "merge"], ["run", "list"], ["run", "view"]):
            matched = [a for a in argvs if a[:2] == sub]
            self.assertTrue(matched and all("--repo" in a and a[a.index("--repo") + 1] == "fork/shepherdr" for a in matched), (sub, matched))
        apis = [a for a in argvs if a[:1] == ["api"]]
        self.assertTrue(apis and all("repos/fork/shepherdr/" in " ".join(a) for a in apis), apis)
        self.assertFalse(any("eliasstravik/shepherdr" in " ".join(a) for a in argvs), argvs)

    def test_push_remote_overrides_and_upstream_never_picked_unless_push_remote(self):
        os.makedirs(wf := os.path.join(self.main, ".github", "workflows"))
        with open(os.path.join(wf, "ci.yml"), "w") as f:
            f.write("env:\n  K: ${{ secrets.API_KEY }}\n")
        (gh_ok, gh_note, _), (sec_ok, _, _, _, miss) = self.project.check_github_secrets(self.main)
        self.assertEqual((gh_ok, sec_ok, miss, "fork/shepherdr" in gh_note), (True, True, [], True))
        self.assertTrue(any(c["argv"][:4] == ["secret", "list", "--repo", "fork/shepherdr"] for c in self.calls()))
        t = {"id": 69, "branch": "task/68-demo", "worktree": self.wt}
        g = lambda *a: subprocess.run(["git", "-C", self.main, *a], check=True)
        g("remote", "add", "myfork", "https://github.com/myfork/shepherdr.git")
        g("config", "branch.task/68-demo.pushRemote", "myfork")
        with self.store.Store(self.store.locate(self.main), write=False) as s:
            self.assertEqual(self.github.github(s, t), "myfork/shepherdr")
        g("config", "--unset", "branch.task/68-demo.pushRemote"); g("remote", "remove", "origin"); g("remote", "remove", "myfork")
        g("config", "--unset", "remote.upstream.pushurl")
        before = len(self.calls() if os.path.exists(self.log) else [])
        with self.store.Store(self.store.locate(self.main), write=False) as s:
            self.assertIsNone(self.github.github(s, t))
            self.assertEqual(len(self.calls()), before)
            g("config", "remote.pushDefault", "upstream")
            self.assertEqual(self.github.github(s, t), "eliasstravik/shepherdr")

    def test_non_repo_gh_calls_stay_unchanged_and_unguarded_repo_calls_refuse(self):
        self.assertEqual((self.github.gh("auth", "status")[:2], self.github.gh("api", "user")[0]), ((True, "Logged in"), True))
        self.assertEqual([c["argv"] for c in self.calls()], [["auth", "status"], ["api", "user"]])
        for bad in (("pr", "view", "50"), ("repo", "view", "--json", "nameWithOwner"),
                    ("api", "repos/{owner}/{repo}/pulls"), ("api", "repos//commits/abc")):
            self.assertFalse(self.github.gh(*bad)[0], bad)
        self.assertEqual(len(self.calls()), 2)


class OpenQuestionSurvivesDone(unittest.TestCase):
    """FAC-70 (field note 31): a worker's open question on a Done task moves into `herdr-task ask`
    before `end_done` closes the worker, or leaves the worker alive with why in the task's log."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.agents as agents, herdr_task.ask as ask, herdr_task.base as base
        import herdr_task.commands as commands, herdr_task.github as github, herdr_task.store as store, herdr_task.tick as tick
        self.agents, self.ask, self.base, self.commands, self.github, self.store, self.tick = agents, ask, base, commands, github, store, tick
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.main, self.wt = os.path.join(self.root, "repo"), os.path.join(self.root, "wt")
        self.store_dir, self.app_home = os.path.join(self.root, "tasks"), os.path.join(self.root, "app")
        g = lambda *a, cwd=self.main: subprocess.run(["git", "-C", cwd, *a], check=True)
        subprocess.run(["git", "init", "-q", "-b", "main", self.main], check=True)
        for k, v in (("user.name", "T"), ("user.email", "t@x"), ("commit.gpgsign", "false")):
            g("config", k, v)
        g("commit", "-q", "--allow-empty", "-m", "base")
        g("worktree", "add", "-q", "-b", "task/11", self.wt, "HEAD")
        g("commit", "-q", "--allow-empty", "-m", "release prep", cwd=self.wt)
        g("merge", "-q", "--ff-only", "task/11")
        self.live, self.panes, self.fail_ask = [], [], False
        live = lambda key: list(self.live)
        for p in (mock.patch.dict(os.environ, {"HERDR_TASK_STORE": self.store_dir, "HERDR_APP_HOME": self.app_home}),
                  mock.patch.object(base, "STORE", self.store_dir), mock.patch.object(store, "STORE", self.store_dir),
                  mock.patch.object(ask, "ASKS", os.path.join(self.store_dir, "asks")),
                  mock.patch.object(ask, "run", self.hc_run),
                  mock.patch.object(github, "live_agents", live), mock.patch.object(github, "live_helpers", lambda *_: []),
                  mock.patch.object(github, "hc_json", lambda *_: {"terminals": self.live + self.panes}),
                  mock.patch.object(tick, "live_agents", live), mock.patch.object(tick, "detail", lambda k: k),
                  mock.patch.object(tick, "refresh_sessions", lambda *_: None), mock.patch.object(tick, "quiet_hc", self.kill_term),
                  mock.patch.object(tick.time, "sleep", lambda *_: None)):
            p.start(); self.addCleanup(p.stop)

    def hc_run(self, cmd, check=False):
        if self.fail_ask:
            raise self.base.Fail("hc server unreachable")
        addr = f"t-ask-{len(self.panes) + 1}"
        self.panes.append({"address": addr, "state": "live", "cmd": cmd})
        return subprocess.CompletedProcess(cmd, 0, json.dumps({"address": addr}), "")

    def kill_term(self, verb, k):
        if verb == "kill":
            self.live = [x for x in self.live if x.get("address") != k.get("address")]
        return subprocess.CompletedProcess(["hc", verb, k["address"]], 0, "", "")

    def put_task(self, **extra):
        log = os.path.join(self.root, "session.jsonl")
        t = {"id": 11, "title": "Ship release", "state": "ready", "mode": "worktree", "worktree": self.wt,
             "branch": "task/11", "base": "main", "merged": True, "space": "sp1",
             "agents": [{"address": "t1", "parent": "t0", "log": log}], **extra}
        with self.store.Store(self.store.locate(self.main), write=True) as s:
            s.data.update(prefix="WUKO", tasks=[t]); s.dirty = True
        return log

    def sweep(self):
        with self.store.Store(self.store.locate(self.main), write=True) as s:
            s.data["tasks"][0]["done_at"] = self.base.now() - self.tick.DONE_GRACE_MS - 1000; s.dirty = True
        report = {}
        self.tick.end_done_pass(report, folder=self.main)
        return report

    def test_open_card_becomes_herdr_task_ask_and_closes_worker_then_worktree(self):
        self.put_task(merged=False, pr={"number": 11, "repo": "o/r"})
        self.live = [{"address": "t1", "parent": "t0", "state": "live", "agent": "claude", "status": "working",
                      "cwd": self.wt, "labels": {"herdr.role": "worker"}}]
        req_dir = os.path.join(self.app_home, "data", "chat-asks", "req")
        os.makedirs(req_dir, exist_ok=True)
        with open(os.path.join(req_dir, "t1.1700000000.99.json"), "w") as f:
            json.dump({"tool_name": "AskUserQuestion", "tool_input": {"questions": [{
                "question": "Push release tag v1.2.0 now?",
                "options": [{"label": "Push tag (v1)"}, {"label": "Hold off (Recommended)"}]}]}}, f)
        with unittest.mock.patch.object(self.commands, "pr_view", lambda t: {"state": "MERGED", "mergeCommit": {"oid": "a1"}}), \
             unittest.mock.patch.object(self.commands, "note_checks", lambda *_: None), \
             unittest.mock.patch.object(self.commands, "watch_main", lambda *_: None), \
             unittest.mock.patch.object(self.commands, "update_main", lambda *_: None):
            with self.store.Store(self.store.locate(self.main), write=True) as s:
                self.assertIsNone(self.commands.merge_now(s, s.data["tasks"][0]))
        self.assertTrue(os.path.isdir(self.wt))
        r1 = self.sweep()
        self.assertEqual((self.live, len(self.panes), len(r1["asked"])), ([], 1, 1))
        rec = self.ask.load(r1["asked"][0]["id"])
        self.assertEqual((rec["task"], rec["to"], rec["recommend"]), ("WUKO-11", "t0", "Hold off"))
        self.assertIn("WUKO-11", rec["question"])
        self.assertIn("Push release tag v1.2.0 now?", rec["question"])
        self.assertEqual(rec["options"], ["Yes — Push tag — v1", "Hold off"])
        self.assertFalse(any("herdr.task=" in str(a) for a in self.panes[0]["cmd"]))
        self.sweep()
        self.assertFalse(os.path.isdir(self.wt))

    def test_terminal_waiting_on_input_with_question_produces_ask(self):
        log = self.put_task()
        with open(log, "w") as f:
            f.write(json.dumps({"type": "assistant", "message": {"content": [{"type": "text", "text": "Should I push the v1.2.0 tag to origin?"}]}}) + "\n")
        self.live = [{"address": "t1", "parent": "t0", "state": "live", "agent": "claude", "status": "idle",
                      "cwd": self.wt, "labels": {"herdr.role": "worker"}}]
        with self.store.Store(self.store.locate(self.main), write=True) as s:
            self.github.finish(s, s.data["tasks"][0], even_if_busy=True)
        self.assertTrue(os.path.isdir(self.wt))
        r = self.sweep()
        rec = self.ask.load(r["asked"][0]["id"])
        self.assertEqual((self.live, rec["task"]), ([], "WUKO-11"))
        self.assertIn("Should I push the v1.2.0 tag to origin?", rec["question"])

    def test_worker_with_no_open_question_closes_as_today(self):
        log = self.put_task(ready_turns={"t1": 1})
        with open(log, "w") as f:
            f.write(json.dumps({"type": "assistant", "message": {"content": [
                {"type": "text", "text": "Fixed 'Why did it fail?' in tick.py.\nAll tests pass.\n<hc suggest=\"Ship it?\"/>"}]}}) + "\n")
        self.live = [{"address": "t1", "parent": "t0", "state": "live", "agent": "claude", "status": "idle", "turn": 1,
                      "cwd": self.wt, "labels": {"herdr.role": "worker"}}]
        with self.store.Store(self.store.locate(self.main), write=True) as s:
            self.github.finish(s, s.data["tasks"][0], even_if_busy=True)
        self.assertFalse(os.path.isdir(self.wt))
        r = self.sweep()
        self.assertEqual((self.live, self.panes, "asked" in r), ([], [], False))

    def test_failed_ask_leaves_worker_alive_and_logs_why(self):
        self.fail_ask = True
        self.put_task(state="done", done_at=1000)
        self.live = [{"address": "t1", "parent": "t0", "state": "live", "agent": "claude",
                      "status": {"state": "blocked", "kind": "question", "msg": "Push release tag v1.2.0?"},
                      "cwd": self.wt, "labels": {"herdr.role": "worker"}}]
        r1, r2 = self.sweep(), self.sweep()
        self.assertEqual((len(self.live), os.path.isdir(self.wt), len(r1["kept_open"])), (1, True, 1))
        with self.store.Store(self.store.locate(self.main), write=False) as s:
            t = s.data["tasks"][0]
            self.assertNotIn("ended_at", t)
            self.assertEqual(len(t["log"]), 1)
            self.assertIn("hc server unreachable", t["log"][0]["text"])
        buf = __import__("io").StringIO()
        with unittest.mock.patch.object(self.commands, "live_agents", lambda *_: list(self.live)), \
             unittest.mock.patch.object(self.commands, "detail", lambda k: k), \
             unittest.mock.patch.object(self.commands, "refresh_sessions", lambda *_: None), \
             __import__("contextlib").redirect_stdout(buf):
            self.commands.cmd_show(__import__("argparse").Namespace(id="WUKO-11", folder=self.main, json=False, evidence=False))
        self.assertIn("log: Kept t1 open", buf.getvalue())


class SimulatorPool(unittest.TestCase):
    """FAC-72: each task leases one iOS simulator from a folder's fixed pool, and freeing shuts it down."""

    U1, U2 = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"

    def setUp(self):
        import contextlib, io
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.cli as cli, herdr_task.github as github, herdr_task.launch as launch
        import herdr_task.settings as settings, herdr_task.simulators as sims, herdr_task.store as store
        self.cli, self.github, self.launch, self.sims, self.store = cli, github, launch, sims, store
        self.io, self.out_r, self.err_r = io, contextlib.redirect_stdout, contextlib.redirect_stderr
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="sim-pool-"))
        self.repo, self.store_dir, self.settings_file, self.bin_dir, self.xc_log = [
            os.path.join(self.tmp, n) for n in ("wukong-mind", "tasks", "settings.json", "bin", "xcrun.jsonl")]
        os.makedirs(self.repo); os.makedirs(self.bin_dir)
        subprocess.run(["git", "init", "-q", "-b", "main", self.repo], check=True)
        devs = {"devices": {"com.apple.CoreSimulator.SimRuntime.iOS-18-2": [
            {"name": "WukongMind 1", "udid": self.U1, "isAvailable": True, "state": "Shutdown"},
            {"name": "WukongMind 2", "udid": self.U2, "isAvailable": True, "state": "Booted"}]}}
        xcrun = os.path.join(self.bin_dir, "xcrun")
        with open(xcrun, "w") as f:
            f.write(f"#!{sys.executable}\nimport json, sys\nopen({self.xc_log!r}, 'a').write(json.dumps(sys.argv[1:]) + '\\n')\n"
                    f"if sys.argv[1:4] == ['simctl', 'list', 'devices']: print(json.dumps({devs!r}))\n")
        os.chmod(xcrun, 0o755)
        for p in (mock.patch.dict(os.environ, {"PATH": f"{self.bin_dir}:{os.environ['PATH']}"}),
                  mock.patch.object(settings, "SETTINGS", self.settings_file), mock.patch.object(store, "STORE", self.store_dir),
                  mock.patch.object(github, "live_agents", lambda *_: []), mock.patch.object(github, "live_helpers", lambda *_: [])):
            p.start(); self.addCleanup(p.stop)

    def set_pool(self, pool):
        with open(self.settings_file, "w") as f:
            json.dump({"spaces": {"sp1": {"folder": self.repo, "tasks": {"simulators": pool}}}}, f)

    def run_cli(self, *argv, env=None):
        from unittest import mock
        out, err = self.io.StringIO(), self.io.StringIO()
        with mock.patch.dict(os.environ, env or {}), mock.patch.object(sys, "argv", ["herdr-task", *argv]), \
                self.out_r(out), self.err_r(err):
            return self.cli.main(), out.getvalue(), err.getvalue()

    def shutdowns(self):
        with open(self.xc_log) as f:
            return [a[2] for a in (json.loads(ln) for ln in f) if a[:2] == ["simctl", "shutdown"]]

    def test_no_pool_says_feature_is_off_and_prompt_adds_only_with_pool(self):
        self.assertEqual(self.run_cli("add", "First", "--folder", self.repo)[0], 0)
        self.assertEqual((r := self.run_cli("simulator", "list", "--folder", self.repo))[0], 0)
        self.assertIn("feature is off", r[1].lower())
        self.assertEqual((r := self.run_cli("simulator", "lease", "--folder", self.repo, env={"HERDR_BROWSER_TASK": "other-repo#99"}))[0], 1)
        self.assertIn("feature is off", r[2].lower())
        with self.store.Store(self.store.locate(self.repo), write=False) as s:
            t = s.find("1")
            self.assertNotIn("simulator lease", self.launch.closing_line(s, t))
            self.set_pool(["WukongMind 1", self.U2])
            for phrase in ("simulator lease", "never create or boot another", "simulator release"):
                self.assertIn(phrase, self.launch.closing_line(s, t))
        self.assertTrue("simulator" in self.launch.OWN and self.launch.own_command("/x/herdr-task", "/x/herdr-task simulator lease WM-1"))

    def test_lease_refusal_release_and_automatic_cleanup_on_end_and_tick(self):
        from unittest import mock
        self.set_pool(["WukongMind 1", self.U2])
        for title in ("One", "Two", "Three", "Four", "Five", "Six", "Seven"):
            self.assertEqual(self.run_cli("add", title, "--folder", self.repo)[0], 0)
        with self.store.Store(self.store.locate(self.repo), write=False) as s:
            slug = s.slug
        rc, out1, _ = self.run_cli("simulator", "lease", "--json", env={"HERDR_BROWSER_TASK": f"{slug}#1"})
        j1 = json.loads(out1)
        self.assertEqual((rc, j1["task"], j1["name"], j1["udid"]), (0, "WM-1", "WukongMind 1", self.U1))
        self.assertEqual(json.loads(self.run_cli("simulator", "lease", "WM-2", "--folder", self.repo, "--json")[1])["udid"], self.U2)
        lst = json.loads(self.run_cli("simulator", "list", "--folder", self.repo, "--json")[1])["simulators"]
        self.assertEqual([(r["udid"], r["holder"]) for r in lst], [(self.U1, "WM-1"), (self.U2, "WM-2")])
        rc, _, err = self.run_cli("simulator", "lease", "WM-3", "--folder", self.repo)
        self.assertEqual((rc, "WM-1" in err and "WM-2" in err), (1, True))
        # Release frees U1 + shuts down so WM-3 can lease it; done & rm free and shut down too.
        self.assertEqual(self.run_cli("simulator", "release", env={"HERDR_BROWSER_TASK": f"{slug}#1"})[0], 0)
        self.assertEqual(self.shutdowns(), [self.U1])
        self.assertEqual(json.loads(self.run_cli("simulator", "lease", "WM-3", "--folder", self.repo, "--json")[1])["udid"], self.U1)
        self.assertEqual(self.run_cli("done", "WM-2", "--folder", self.repo)[0], 0)
        self.assertEqual(self.run_cli("rm", "WM-3", "--folder", self.repo)[0], 0)
        self.assertEqual(self.shutdowns(), [self.U1, self.U2, self.U1])
        # Concurrent child processes serialize on the store lock; merged/stopped/gone tasks & missing xcrun clean up.
        cmd = "import sys; sys.path.insert(0, sys.argv[1]); from herdr_task.cli import main; sys.argv = sys.argv[2:]; raise SystemExit(main())"
        env = {**os.environ, "HERDR_TASK_STORE": self.store_dir, "HERDR_TASK_SETTINGS": self.settings_file, "PATH": f"{self.bin_dir}:{os.environ['PATH']}"}
        ps = [subprocess.Popen([sys.executable, "-c", cmd, BIN, "herdr-task", "simulator", "lease", k, "--folder", self.repo, "--json"],
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env) for k in ("WM-4", "WM-5")]
        u4, u5 = [json.loads(p.communicate(timeout=15)[0])["udid"] for p in ps]
        self.assertEqual({u4, u5}, {self.U1, self.U2})
        with self.store.Store(self.store.locate(self.repo), write=True) as s:
            self.github.finish(s, s.find("4"))
            s.find("5").update(state="running", started=1, agents=[{"address": "t5", "agent": "claude", "at": 1}])
        with mock.patch.object(self.sims, "hc_json", lambda *_: {"terminals": []}):
            rep = {}; self.sims.simulator_pass(rep, self.repo)
            self.assertEqual([x["udid"] for x in rep["simulators_released"]], [u5])
        self.run_cli("simulator", "lease", "WM-6", "--folder", self.repo)
        with self.store.Store(self.store.locate(self.repo), write=True) as s:
            s.data["tasks"] = [t for t in s.data["tasks"] if t["id"] != 6]; s.dirty = True
        rep = {}; self.sims.simulator_pass(rep, self.repo)
        self.assertEqual(([x["udid"] for x in rep["simulators_released"]], self.shutdowns()[-3:]), ([self.U1], [u4, u5, self.U1]))
        self.run_cli("simulator", "lease", "WM-7", "--folder", self.repo)
        with mock.patch.object(self.sims.shutil, "which", lambda c: None if c == "xcrun" else shutil.which(c)):
            self.assertEqual(self.run_cli("done", "WM-7", "--folder", self.repo)[0], 0)

    def test_done_releases_a_lease_the_worker_forgot(self):
        self.set_pool(["WukongMind 1"])
        self.assertEqual(self.run_cli("add", "First", "--folder", self.repo)[0], 0)
        self.assertEqual(self.run_cli("simulator", "lease", "WM-1", "--folder", self.repo)[0], 0)
        self.assertEqual(self.run_cli("done", "WM-1", "--folder", self.repo)[0], 0)
        self.assertEqual(self.shutdowns(), [self.U1])
        lst = json.loads(self.run_cli("simulator", "list", "--folder", self.repo, "--json")[1])["simulators"]
        self.assertEqual([r["holder"] for r in lst], [None])


class EndGroup(unittest.TestCase):
    """FAC-86: ending an agent ends its process group, and only a group its own pid leads."""

    def setUp(self):
        from unittest import mock
        sys.path.insert(0, BIN)
        import herdr_task.base as base
        self.base, self.mock = base, mock

    def test_a_group_led_by_the_agent_gets_term_then_kill(self):
        with self.mock.patch.object(self.base.os, "getpgid", return_value=4242), self.mock.patch.object(self.base.os, "getpgrp", return_value=1), \
                self.mock.patch.object(self.base.os, "killpg") as kp, self.mock.patch.object(self.base.time, "sleep"):
            self.base.end_group(4242)
        sigs = [c.args for c in kp.call_args_list]
        self.assertEqual((sigs[0], sigs[-1]), ((4242, self.base.signal.SIGTERM), (4242, self.base.signal.SIGKILL)))

    def test_another_machines_agent_has_no_local_pid(self):
        with self.mock.patch.object(self.base, "hc_json", return_value={"foreground": {"pid": 4242}}) as hj:
            self.assertEqual((self.base.agent_pid("vm/t5"), hj.call_count, self.base.agent_pid("t5")), (None, 0, 4242))

    def test_our_own_group_and_a_follower_are_left_alone(self):
        with self.mock.patch.object(self.base.os, "getpgid", return_value=4242), self.mock.patch.object(self.base.os, "getpgrp", return_value=4242), \
                self.mock.patch.object(self.base.os, "killpg") as kp:
            self.base.end_group(4242)  # our own group
        with self.mock.patch.object(self.base.os, "getpgid", return_value=77), self.mock.patch.object(self.base.os, "getpgrp", return_value=1), \
                self.mock.patch.object(self.base.os, "killpg") as kp2:
            self.base.end_group(4242)  # a process in somebody else's group
        self.assertEqual((kp.call_count, kp2.call_count), (0, 0))

    def test_kill_agent_kills_the_terminal_then_the_group_and_a_gone_group_is_fine(self):
        calls = []
        runs = lambda a, **k: calls.append(a) or self.mock.Mock(returncode=0, stdout="")
        with self.mock.patch.object(self.base, "hc_json", return_value={"foreground": {"pid": 4242}}), self.mock.patch.object(self.base, "run", runs), \
                self.mock.patch.object(self.base.os, "getpgid", side_effect=ProcessLookupError), self.mock.patch.object(self.base.os, "getpgrp", return_value=1), \
                self.mock.patch.object(self.base.os, "killpg", side_effect=[None, ProcessLookupError]) as kp, self.mock.patch.object(self.base.time, "sleep"):
            self.base.kill_agent("t1")
        self.assertEqual((calls, kp.call_count), ([["hc", "kill", "t1"]], 2))  # the leader's gone, the group answered once then vanished

class ReviewTheCommitGitHubHas(unittest.TestCase):
    """FAC-88 (note 45): a review round and a merge are of the commit the pull request holds. A real throwaway
    repo with a bare origin; only hc and gh are faked (gh answers headRefOid from the bare origin)."""

    setUp_gate, git, change, ready = GateReview.setUp, GateReview.git, GateReview.change, GateReview.ready
    env = GateReview.env

    def setUp(self):
        from unittest import mock
        self.setUp_gate()
        import herdr_task.github as github
        self.github, self.mock = github, mock
        self.origin = os.path.join(self.root, "origin.git")
        self.git(self.root, "init", "-q", "--bare", self.origin)
        self.git(self.main, "remote", "add", "origin", self.origin)
        self.git(self.wt, "push", "-q", "-u", "origin", "task/13")
        self.task["pr"] = {"number": 9, "repo": "o/r", "checked": 5}
        self.merges = []

        def fake_gh(*a, **k):
            if a[:2] == ("pr", "view"):
                return True, self.git(self.origin, "rev-parse", "refs/heads/task/13"), ""
            self.merges.append(a)
            return True, "", ""
        for mod, name, value in ((github, "gh", fake_gh), (self.commands, "gh", fake_gh),
                                 (self.commands, "finish", lambda s, t, even_if_busy=False: t.update(state="done")),
                                 (self.commands, "watch_main", lambda *a: None), (self.commands, "update_main", lambda *a: None)):
            p = mock.patch.object(mod, name, value)
            p.start()
            self.addCleanup(p.stop)

    def pushed(self):
        return self.git(self.origin, "rev-parse", "refs/heads/task/13")

    def test_a_merge_nobody_pushed_is_pushed_before_the_review_starts(self):
        local = self.change("merged main here")
        self.assertNotEqual(self.pushed(), local)
        self.ready()
        self.assertEqual((self.pushed(), len(self.runs)), (local, 1))

    def test_a_failed_push_stops_ready_with_gits_error(self):
        self.git(self.main, "remote", "set-url", "origin", os.path.join(self.root, "gone.git"))
        self.change("never pushed")
        with self.assertRaises(self.commands.Fail) as e:
            self.ready()
        self.assertIn("Couldn't push task/13", str(e.exception))
        self.assertEqual(self.runs, [])

    def merging(self, view_head):
        self.task.update(state="ready", head=self.git(self.wt, "rev-parse", "HEAD"))
        view = {"state": "OPEN", "mergeable": "MERGEABLE", "statusCheckRollup": [], "headRefOid": view_head}
        p = self.mock.patch.object(self.commands, "pr_view", lambda t: {"state": "MERGED"} if self.merges else view)
        p.start()
        self.addCleanup(p.stop)
        return self.commands.merge_now(self.store, self.task)

    def test_a_pass_the_pr_doesnt_contain_is_pushed_then_merged(self):
        local = self.change("reviewed but unpushed")
        old = self.pushed()
        self.assertEqual(self.merging(old), self.commands.CHECKS_RUNNING)  # pushed; its checks haven't run yet
        self.assertEqual((self.pushed(), self.merges, self.task["pr"]["checked"]), (local, [], 0))
        self.assertIsNone(self.merging(local))  # the next tick's fresh view
        self.assertEqual(self.merges[0][-2:], ("--match-head-commit", local))

    def test_a_pass_the_pr_doesnt_contain_and_cant_be_pushed_is_flagged(self):
        other = os.path.join(self.root, "other")
        self.git(self.root, "clone", "-q", self.origin, other)
        for k, v in (("user.name", "T"), ("user.email", "t@example.com"), ("commit.gpgsign", "false")):
            self.git(other, "config", k, v)
        self.git(other, "checkout", "-q", "-b", "x", "origin/task/13")
        self.git(other, "commit", "-q", "--allow-empty", "-m", "someone else's")
        self.git(other, "push", "-q", "origin", "x:task/13")
        theirs, local = self.pushed(), self.change("reviewed, diverged")
        why = self.merging(theirs)
        self.assertEqual(why, f"The PR's head isn't the reviewed commit: GitHub has {theirs[:7]}, reviewed {local[:7]}")
        self.assertEqual((self.task["attention"], self.merges, self.pushed()), (why, [], theirs))

    def test_a_conflict_clears_on_the_next_tick_after_the_push(self):
        import herdr_task.tick as tick
        local = self.change("merged main")
        self.task.update(state="ready", head=local, attention="It conflicts with the base branch.")
        self.assertIsNone(self.github.pr_head_problem(self.store, self.task, "o/r", local))
        self.assertEqual((self.pushed(), self.task["pr"]["checked"]), (local, 0))
        view = {"state": "OPEN", "mergeable": "MERGEABLE", "statusCheckRollup": [], "headRefOid": local}
        for name, value in (("pr_view", lambda t: view), ("live_agents", lambda key: []), ("pr_repo", lambda *a: "o/r")):
            p = self.mock.patch.object(tick, name, value)
            p.start()
            self.addCleanup(p.stop)
        tick.facts(self.store, self.task, {"attention": [], "done": []})
        self.assertNotIn("attention", self.task)


class MergeOnlyReviewRounds(unittest.TestCase):
    """FAC-89 (note 46): merges of the base branch after a passed round whose only extra changes are
    generated files get a delta review without using a review round or tripping the loop-breaker."""

    setUp, git, change, ready = GateReview.setUp, GateReview.git, GateReview.change, GateReview.ready
    verdict, valid, env = GateReview.verdict, GateReview.valid, GateReview.env

    def prompt_of(self, idx=-1):
        cmd = self.runs[idx][self.runs[idx].index("--") + 1:]
        return cmd[cmd.index("-i") + 1]

    def advance_base(self, rel="base_note.txt", content="from main\n"):
        full = os.path.join(self.main, rel)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w") as f:
            f.write(content)
        self.git(self.main, "add", rel)
        self.git(self.main, "commit", "-q", "-m", f"main {rel}")
        return self.git(self.main, "rev-parse", "HEAD")

    def write_wt(self, rel, content):
        full = os.path.join(self.wt, rel)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w") as f:
            f.write(content)
        self.git(self.wt, "add", rel)

    def test_clean_and_pbxproj_merges_use_no_round_nor_fire_out_of_rounds(self):
        import herdr_task.output as output
        self.task["set"] = {"review_min": 2, "review_rounds": 2}
        self.write_wt(".gitattributes", "gen/*.swift linguist-generated\n")
        self.write_wt("App.xcodeproj/project.pbxproj", "// task pbx 1\n")
        self.write_wt("gen/schema.swift", "// task gen 1\n")
        self.git(self.wt, "commit", "-q", "-m", "project files")
        self.ready()
        self.verdict("term-1", passed=True)
        self.verdict("term-2", passed=True)
        self.assertEqual((self.task["state"], self.commands.rounds_used(self.task, True)), ("ready", 2))

        # 1. Clean main merge at 2/2 rounds used: reviewed as a merge-only delta, uses 0 rounds.
        self.advance_base("other.py", "x = 1\n")
        self.git(self.wt, "merge", "--no-ff", "-q", "-m", "merge main cleanly", "main")
        self.ready()
        self.assertNotIn("review_out", self.task)
        self.assertIn("merge of main only; review the conflict resolution", self.prompt_of())
        self.assertEqual(self.commands.rounds_used(self.task, True), 2)
        self.verdict("term-3", passed=True)
        self.assertIn("merge of main only; review the conflict resolution", self.prompt_of())
        self.verdict("term-4", passed=True)
        self.assertEqual((self.task["state"], self.commands.rounds_used(self.task, True)), ("ready", 2))

        # 2. Cascade of two base merges (clean + conflicting *.pbxproj and linguist-generated): still 0 rounds.
        self.advance_base("second.py", "y = 2\n")
        self.git(self.wt, "merge", "--no-ff", "-q", "-m", "merge main again", "main")
        self.advance_base("App.xcodeproj/project.pbxproj", "// main pbx 2\n")
        self.advance_base("gen/schema.swift", "// main gen 2\n")
        subprocess.run(["git", "-C", self.wt, "merge", "--no-commit", "main"], capture_output=True)
        self.write_wt("App.xcodeproj/project.pbxproj", "// regenerated pbx 3\n")
        self.write_wt("gen/schema.swift", "// regenerated gen 3\n")
        self.git(self.wt, "commit", "-q", "-m", "merge main and regenerate project")
        self.ready()
        self.assertNotIn("review_out", self.task)
        self.assertIn("merge of main only; review the conflict resolution", self.prompt_of())
        self.assertIn("regenerated pbx 3", self.prompt_of())
        self.verdict("term-5", passed=True)
        self.verdict("term-6", passed=True)
        live = [{"address": "w1", "agent": "claude", "status": {"state": "idle"}, "status_ms": 1}]
        v = output.view(self.store, self.task, live=live)
        self.assertEqual((v["status"], v.get("why"), self.commands.rounds_used(self.task, True)), ("ready", None, 2))
        self.valid()

    def test_hand_resolved_source_conflict_or_edit_uses_a_round_and_stops_at_limit(self):
        import herdr_task.output as output
        self.task["set"] = {"review_min": 2, "review_rounds": 4}
        self.write_wt("src.py", "v = 'task'\n")
        self.git(self.wt, "commit", "-q", "-m", "add src")
        self.ready()
        self.verdict("term-1", passed=True)
        self.verdict("term-2", passed=True)
        self.assertEqual(self.commands.rounds_used(self.task, True), 2)

        # Real conflict in src.py: counts as normal rounds (3 and 4 of 4).
        self.advance_base("src.py", "v = 'main'\n")
        rc = subprocess.run(["git", "-C", self.wt, "merge", "--no-commit", "main"], capture_output=True).returncode
        self.assertNotEqual(rc, 0)
        self.write_wt("src.py", "v = 'main+task'\n")
        self.git(self.wt, "commit", "-q", "-m", "resolve src.py conflict")
        self.ready()
        self.assertNotIn("merge of main only", self.prompt_of())
        self.assertEqual(self.commands.rounds_used(self.task, True), 3)
        self.verdict("term-3", passed=True)
        self.verdict("term-4", passed=True)
        self.assertEqual((self.task["state"], self.commands.rounds_used(self.task, True)), ("ready", 4))

        # At 4/4 used, a clean merge with a hand edit in a source file hits "Out of review rounds".
        self.advance_base("clean.py", "ok = True\n")
        self.git(self.wt, "merge", "--no-commit", "--no-ff", "-q", "main")
        self.write_wt("src.py", "v = 'sneaked edit'\n")
        self.git(self.wt, "commit", "-q", "-m", "merge with hand edit")
        for r in self.task["review_runs"]:
            r["at"] = 1
        self.ready()
        self.assertEqual(len(self.runs), 4)
        self.assertIn("review_out", self.task)
        live = [{"address": "w1", "agent": "claude", "status": {"state": "idle"}, "status_ms": 1}]
        self.assertEqual(output.view(self.store, self.task, live=live).get("why"), "Out of review rounds")
        self.valid()

    def test_unpassed_round_and_loop_breaker_same_file_flagged(self):
        from herdr_task.budget import triggers
        self.task["set"] = {"review_min": 2, "review_rounds": 6}
        self.write_wt("app.py", "a = 1\n")
        self.git(self.wt, "commit", "-q", "-m", "app v1")
        self.ready()
        self.verdict("term-1", passed=False, changes="[major] app.py broken", must_fix=1)

        # Merging main on top of an unpassed round still counts as a normal round.
        self.advance_base("docs.txt", "hi\n")
        self.git(self.wt, "merge", "--no-ff", "-q", "-m", "merge before fixing", "main")
        self.ready()
        self.assertNotIn("merge of main only", self.prompt_of())
        self.assertEqual(self.commands.rounds_used(self.task, True), 2)
        self.verdict("term-2", passed=True)
        self.verdict("term-3", passed=True)
        self.assertEqual((self.task["state"], self.commands.rounds_used(self.task, True)), ("ready", 3))

        # Now after passing, a merge-only round that flags app.py again does NOT trip "same file flagged in two rounds".
        self.advance_base("more.txt", "yo\n")
        self.git(self.wt, "merge", "--no-ff", "-q", "-m", "clean merge", "main")
        self.ready()
        self.assertEqual(self.commands.rounds_used(self.task, True), 3)
        self.verdict("term-4", passed=False, changes="[major] app.py regression in merge", must_fix=1)
        self.assertNotIn("recap", self.task)
        self.assertEqual(triggers(self.store, self.task), [])

        # A real hand-fix commit after that flagging app.py DOES trip the loop-breaker.
        self.write_wt("app.py", "a = 2\n")
        self.git(self.wt, "commit", "-q", "-m", "fix app")
        self.ready()
        self.assertEqual(self.commands.rounds_used(self.task, True), 4)
        self.verdict("term-5", passed=False, changes="[major] app.py still broken", must_fix=1)
        self.assertIn("recap", self.task)
        self.assertTrue(any("same file flagged in two rounds: app.py" in r for r in self.task["recap"]["reasons"]))
        self.valid()


class BoardSnapshotStore(unittest.TestCase):
    """FAC-92: ready stores counted/generated/estimate line numbers on the task so they survive
    worktree removal, and accept / auto-merge record accepted_by ('person', 'assistant', 'auto')."""

    setUp, git, change, ready = GateReview.setUp, GateReview.git, GateReview.change, GateReview.ready
    verdict, env = GateReview.verdict, GateReview.env
    write_wt = MergeOnlyReviewRounds.write_wt

    def test_ready_stores_size_split_and_estimate_and_reopen_clears_it(self):
        from unittest import mock
        import herdr_task.github as gh_mod
        self.task["prompt"] = "Prep board data.\n\nSize: about 150-250 lines including tests.\n"
        self.task["set"] = {"review_min": 0}
        self.write_wt(".gitattributes", "gen/*.json linguist-generated\n")
        self.write_wt("src.py", "a = 1\nb = 2\nc = 3\n")
        self.write_wt("App.xcodeproj/project.pbxproj", "// p1\n// p2\n")
        self.write_wt("gen/out.json", "{\n  \"x\": 1\n}\n")
        self.git(self.wt, "commit", "-q", "-m", "add code and generated")
        self.ready()
        # GateReview.setUp wrote 1 line to f.txt + 1 line .gitattributes + 3 lines src.py = 5 counted; 2 pbxproj + 3 gen = 5 generated.
        self.assertEqual(self.task["size"], {"changed": 5, "generated": 5, "estimate": 250})
        self.git(self.main, "merge", "--ff-only", "task/13")
        with mock.patch.object(gh_mod, "live_agents", lambda k: []), mock.patch.object(gh_mod, "live_helpers", lambda k: []):
            gh_mod.finish(self.store, self.task, even_if_busy=True)
        self.assertFalse(os.path.exists(self.wt))
        self.assertEqual(self.task["size"], {"changed": 5, "generated": 5, "estimate": 250})
        self.commands.cmd_state(self.commands.argparse.Namespace(id=13, json=False), "reopen")
        self.assertNotIn("size", self.task)

    def test_accepted_by_records_person_assistant_and_auto(self):
        from unittest import mock
        import herdr_task.github as gh_mod
        fine = {"state": "OPEN", "mergeable": "MERGEABLE", "statusCheckRollup": [], "headRefOid": "abc"}
        pend = {"state": "OPEN", "mergeable": "MERGEABLE", "statusCheckRollup": [{"name": "ci", "status": "IN_PROGRESS"}], "headRefOid": "abc"}
        merged = {"state": "MERGED", "mergeCommit": {"oid": "f00"}}
        ns = self.commands.argparse.Namespace(id=13, json=False, space=None, agent=None, argv=None)
        with mock.patch.object(gh_mod, "live_agents", lambda k: []), mock.patch.object(gh_mod, "live_helpers", lambda k: []), \
             mock.patch.object(self.commands, "watch_main", lambda *a: None), mock.patch.object(self.commands, "update_main", lambda *a: None), \
             mock.patch.object(self.commands, "branch_tip", lambda s, t: "abc"):
            # 1. Failed accept leaves accepted_by unset; person accept sets "person".
            self.task.update(state="ready", mode="worktree", worktree=self.wt, head="abc", pr={"number": 8, "repo": "o/r"})
            with mock.patch.object(self.commands, "pr_view", lambda t: None), self.assertRaises(SystemExit):
                self.commands.cmd_accept(self.commands.argparse.Namespace(id=13, json=True, space=None, agent=None, argv=None))
            self.assertNotIn("accepted_by", self.task)
            self.task.update(state="ready", mode="folder", worktree=None)
            self.task.pop("pr", None)
            self.commands.cmd_accept(ns)
            self.assertEqual(self.task.get("accepted_by"), "person")
            # 2. Assistant accept sets "assistant" (locally and across --on via HERDR_TASK_BY).
            self.task.update(state="ready")
            self.task.pop("accepted_by", None)
            with mock.patch.dict(os.environ, {"HC_TERMINAL_ID": "t-coord"}), mock.patch.object(self.commands, "hc_json", lambda *a: {"labels": {"herdr.role": "coordinator"}}):
                self.commands.cmd_accept(ns)
            self.assertEqual(self.task.get("accepted_by"), "assistant")
            self.task.update(state="ready")
            self.commands.back_to_work(self.store, self.task, "fix")
            self.assertNotIn("accepted_by", self.task)
            self.task.update(state="ready")
            with mock.patch.dict(os.environ, {"HERDR_TASK_BY": "assistant"}, clear=False):
                self.commands.cmd_accept(ns)
            self.assertEqual(self.task.get("accepted_by"), "assistant")
            # 3. Person accept waiting on PR checks keeps "person" when merge_now finishes later.
            views = [pend, fine, merged]
            self.task.update(state="ready", mode="worktree", worktree=self.wt, head="abc", pr={"number": 9, "repo": "o/r"})
            self.task.pop("accepted_by", None)
            with mock.patch.object(self.commands, "pr_view", lambda t: views.pop(0)), mock.patch.object(self.commands, "gh", lambda *a, **k: (True, "", "")):
                self.commands.cmd_accept(ns)
                self.assertEqual((self.task["state"], self.task.get("auto_merge"), self.task.get("accepted_by")), ("ready", "checks", "person"))
                self.assertIsNone(self.commands.merge_now(self.store, self.task))
            self.assertEqual((self.task["state"], self.task.get("accepted_by")), ("done", "person"))
            # 4. Merge-after-checks without manual accept records "auto", and reopen clears it.
            views = [fine, merged]
            self.task.update(state="ready", auto_merge="checks", pr={"number": 10, "repo": "o/r"})
            self.task.pop("accepted_by", None)
            with mock.patch.object(self.commands, "pr_view", lambda t: views.pop(0)), mock.patch.object(self.commands, "gh", lambda *a, **k: (True, "", "")):
                self.assertIsNone(self.commands.merge_now(self.store, self.task))
            self.assertEqual((self.task["state"], self.task.get("accepted_by")), ("done", "auto"))
            self.commands.cmd_state(self.commands.argparse.Namespace(id=13, json=False), "reopen")
            self.assertNotIn("accepted_by", self.task)


class BuildOutput(unittest.TestCase):
    """FAC-85: per-folder build-output directory, Settings line, strict cleanup on done, and disk-over news."""

    def setUp(self):
        import contextlib, io, pathlib
        from unittest import mock
        sys.path.insert(0, BIN)
        from herdr_task import base, cli, commands, github, home, launch, settings, store
        self.cli, self.home, self.mock, self.io, self.redirect, self.Path = cli, home, mock, io, contextlib.redirect_stdout, pathlib.Path
        self.root = os.path.realpath(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.root, True)
        self.repo, self.bdir = os.path.join(self.root, "app"), os.path.join(self.root, "builds")
        store_dir, wt_root, cfg_path = (os.path.join(self.root, x) for x in ("store", "wts", "settings.json"))
        subprocess.run(["git", "init", "-q", "-b", "main", self.repo], check=True)
        for k, v in (("user.name", "T"), ("user.email", "t@example.com"), ("commit.gpgsign", "false")):
            subprocess.run(["git", "-C", self.repo, "config", k, v], check=True)
        subprocess.run(["git", "-C", self.repo, "commit", "-q", "--allow-empty", "-m", "init"], check=True)
        self.put(cfg_path, json.dumps({"everywhere": {"tasks": {"worktree_root": wt_root, "proof": False}}}))
        self.briefs = []

        def fake_launch(s, t, argv, space, text, parent=None, resume=None):
            self.briefs.append(text)
            t.setdefault("agents", []).append(a := {"address": f"t{t['id']}", "agent": "claude", "argv": argv, "at": 1})
            return a
        for mod, name, val in ((store, "STORE", store_dir), (base, "STORE", store_dir), (settings, "SETTINGS", cfg_path), (home, "SETTINGS", cfg_path),
                               (commands, "launch", fake_launch), (commands, "ensure_scheduler", lambda **k: {}), (commands, "retag_tabs", lambda s: []),
                               (commands, "live_agents", lambda k: []), (commands, "queue_reason", lambda *a: None), (commands, "parent_for", lambda p, sp: p),
                               (launch, "caller_labels", lambda: {}), (github, "live_agents", lambda k: []), (github, "live_helpers", lambda k: [])):
            self.enterContext(mock.patch.object(mod, name, val))

    def put(self, path, data="x"):
        os.makedirs(os.path.dirname(path), exist_ok=True)
        self.Path(path).write_bytes(data) if isinstance(data, bytes) else self.Path(path).write_text(data)
        return path

    def run_cli(self, *argv):
        with self.mock.patch.object(sys, "argv", ["herdr-task", *argv, "--folder", self.repo]), self.redirect(out := self.io.StringIO()):
            self.assertEqual(self.cli.main(), 0)
        return out.getvalue()

    def test_settings_line_shows_only_with_build_dir_and_start_creates_it(self):
        self.run_cli("config", "--prefix", "APP")
        self.run_cli("add", "Build screen")
        self.assertNotIn("Build output:", self.run_cli("show", "APP-1"))
        self.run_cli("config", "--build-dir", self.bdir)
        line = f"Build output: {self.bdir}/APP-1/ (e.g. xcodebuild -derivedDataPath there); never the default DerivedData."
        self.assertIn(line, self.run_cli("show", "APP-1"))
        self.assertFalse(os.path.exists(os.path.join(self.bdir, "APP-1")))
        self.run_cli("start", "APP-1", "--space", "sp1")
        self.assertTrue(os.path.isdir(os.path.join(self.bdir, "APP-1")) and line in self.briefs[-1])
        self.run_cli("config", "--build-dir", "")
        self.assertNotIn("Build output:", self.run_cli("show", "APP-1"))

    def test_done_removes_exact_task_folder_without_following_symlinks(self):
        self.run_cli("config", "--prefix", "APP", "--build-dir", self.bdir)
        for title in ("First", "Second", "Third"):
            self.run_cli("add", title, "--here")
        self.run_cli("start", "APP-1", "--space", "sp1")
        keep_out = self.put(os.path.join(self.root, "outside", "keep.txt"), "safe")
        outside, sib_link = os.path.dirname(keep_out), os.path.join(self.bdir, "link-out")
        os.chmod(self.put(os.path.join(self.bdir, "APP-1", "DerivedData", "obj.o")), 0o444)
        os.symlink(outside, os.path.join(self.bdir, "APP-1", "escape"))
        sib = self.put(os.path.join(self.bdir, "APP-2", "other.o"), "y")
        os.symlink(outside, sib_link)
        self.run_cli("ready", "APP-1", "--summary", "Built it")
        self.run_cli("accept", "APP-1")
        self.assertFalse(os.path.exists(os.path.join(self.bdir, "APP-1")))
        self.assertTrue(os.path.isdir(self.bdir) and os.path.isfile(sib) and os.path.islink(sib_link) and os.path.isfile(keep_out))
        os.symlink(outside, os.path.join(self.bdir, "APP-3"))
        self.run_cli("done", "APP-3")
        self.assertTrue(os.path.islink(os.path.join(self.bdir, "APP-3")) and os.path.isfile(keep_out))

    def test_no_build_dir_removes_nothing(self):
        self.run_cli("config", "--prefix", "APP")
        self.run_cli("add", "Keep files", "--here")
        f = self.put(os.path.join(self.bdir, "APP-1", "keep.bin"), "stay")
        self.run_cli("done", "APP-1")
        self.assertTrue(os.path.isfile(f))

    def test_disk_over_news_lists_biggest_task_folders(self):
        self.run_cli("config", "--prefix", "APP", "--build-dir", self.bdir)
        for key, kb in (("APP-1", 2), ("APP-2", 12), ("APP-3", 6)):
            self.put(os.path.join(self.bdir, key, "bin"), b"x" * (kb * 1024))
        os.symlink(os.path.dirname(self.put(os.path.join(self.root, "ext", "huge"), b"x" * 50000)), os.path.join(self.bdir, "APP-9"))
        h = {"cpu": 10, "load": 0.4, "cores": 8, "memory": 40, "disk": 95, "disks": [{"path": "home", "pct": 95, "free_gb": 5}]}
        with self.mock.patch.object(self.home, "processes", return_value=[]), self.mock.patch.object(self.home, "leftover_worktrees", return_value=[]):
            with self.mock.patch("herdr_task.machine.sample", return_value={**h, "disk": 50}), self.mock.patch.object(self.home, "task_builds", side_effect=AssertionError):
                self.assertEqual(self.home.local_report()["builds"], [])
            with self.mock.patch("herdr_task.machine.sample", return_value=h):
                line = next(l for l, _ in self.home.health_lines("", self.home.local_report(), {}) if 'event="disk-over"' in l)
                shutil.rmtree(self.bdir)
                empty = next(l for l, _ in self.home.health_lines("", self.home.local_report(), {}) if 'event="disk-over"' in l)
        self.assertTrue(line.index(f"{self.bdir}/APP-2") < line.index(f"{self.bdir}/APP-3") < line.index(f"{self.bdir}/APP-1"))
        self.assertTrue("APP-9" not in line and "Biggest task build folders" not in empty)
