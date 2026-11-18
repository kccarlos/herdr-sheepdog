"""SDF-11: the role templates replayed against field-note situations (docs/dev/factory/template-replay.md).

Each situation's prompt is built by the engine's own code, on a clean install (an empty templates folder): the worker's
by launch.brief, the reviewer's from instructions("reviewer"), the Assistant's from the bundled assistant.md and its
settings block, as project.py starts it. The fake CLI (test/fake_cli, FAKE_MODE=replay) runs as a real process with that
prompt and acts on the one rule its cue finds. No model is in the loop: this proves what each prompt holds and that what
it names exists in the engine, not how a model behaves.

`python3 test/test_replay.py --score [--files]` prints the scorecard (--files: the bundled worker.md and reviewer.md
installed as the person's templates, instead of the built-in defaults)."""
import home_guard  # noqa: F401  (first: every test on a throwaway home, never the real one)
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.path.join(ROOT, "mods", "tasks", "bin")
FAKE = os.path.join(ROOT, "test", "fake_cli")
sys.path.insert(0, BIN)
sys.path.insert(0, os.path.join(ROOT, "test"))
SITUATIONS = json.load(open(os.path.join(ROOT, "test", "replay", "situations.json")))["situations"]
# What only Shepherdr's window, web app or upstream had: a template naming one points the agent at nothing here.
GONE = ("herdr-app", "herdr-browser", "Tasks panel", "<hc suggest", "herdr-task feedback", "herdr-task home",
        "the switch in your header", "Show things in the chat", "Show it in the chat", "Usage insights", "--space")
HT = re.compile(r"herdr-task(?=\s|$)")
NESTED = ("auto", "autopilot", "machines", "plan", "project", "simulator")


def prompts(files=False, templates=None):
    """{role: (prompt, the template's own words)} as the engine builds them, in a throwaway templates folder."""
    import herdr_task.assistant_settings as aset
    import herdr_task.launch as launch
    import herdr_task.revive as revive
    import herdr_task.settings as settings
    import herdr_task.templates as T
    from test_herdr_task import Store
    tdir = templates or tempfile.mkdtemp()
    if files:
        for role in ("worker", "reviewer"):
            with open(os.path.join(ROOT, "templates", f"{role}.md")) as src, open(os.path.join(tdir, f"{role}.md"), "w") as f:
                f.write(src.read())
    with mock.patch.object(T, "TEMPLATES", tdir), mock.patch.object(revive, "TEMPLATES", tdir), \
            mock.patch.object(settings, "SETTINGS", os.path.join(tdir, "none.json")):
        s = Store([{"id": 4, "title": "Chat panel", "state": "running", "mode": "worktree", "worktree": "/wt/4",
                    "branch": "task/4", "base": "main", "prompt": "Add the chat panel."}])
        worker = launch.brief(s, s.data["tasks"][0])
        reviewer = T.instructions("reviewer")
        _, text = revive.template_of("assistant")
        body = re.sub(r"\A---\n[\s\S]*?\n---\n?", "", text)
        assistant = body.rstrip() + "\n\n" + aset.block(aset.rows("/f", settings.read_settings()))
        return {"worker": (worker, T.instructions("worker")), "reviewer": (reviewer + "\n\nThe task, SHEP-4:\nAdd the chat panel.", reviewer),
                "assistant": (assistant, body)}


def act(prompt, sit):
    """Start the fake CLI on the prompt, the situation's message last, as it would arrive; what it did, or None."""
    d = tempfile.mkdtemp()
    log = os.path.join(d, "log")
    env = {**os.environ, "FAKE_MODE": "replay", "FAKE_CUES": json.dumps(sit["cues"]), "FAKE_LOG": log,
           "FAKE_SCREEN": os.path.join(d, "screen"), "FAKE_GO": os.path.join(d, "go")}
    p = subprocess.run([os.path.join(FAKE, "claude"), prompt + "\n\n" + sit["message"]], env=env, capture_output=True,
                       text=True, timeout=30)
    if p.returncode:
        return None
    with open(log) as f:
        return json.loads(f.read().splitlines()[-1])


def plain(cmd):
    return HT.sub("herdr-task", re.sub(r"\S*/herdr-task(?=\s|$)", "herdr-task", cmd)).strip()


def score(sit, done):
    """Why the situation failed ([] when it passed)."""
    if done is None:
        return ["no rule covers it"]
    cmds = [plain(c) for c in done["commands"]]
    why = [f"doesn't run {r}" for r in sit.get("run", []) if not any(c.startswith(r) for c in cmds)]
    why += [f"rule lacks \"{w}\"" for w in sit.get("says", []) if w not in done["rule"]]
    why += [f"names a quota tool: {c}" for c in cmds if re.search(r"\bcodexbar\b", c)]
    return why + [f"names {n}" for n in sit.get("never", []) if any(n in c for c in cmds)]


_HELP = {}


def helptext(words):
    key = tuple(words)
    if key not in _HELP:
        p = subprocess.run([sys.executable, os.path.join(BIN, "herdr-task"), *words, "--help"], capture_output=True, text=True)
        _HELP[key] = p.stdout if p.returncode == 0 else None
    return _HELP[key]


def stale(text):
    """Each `herdr-task …` the text names (in backticks or a code block) that the engine's CLI refuses: an unknown
    command or a flag it doesn't take."""
    spans = re.findall(r"`([^`\n]+)`", text) + re.findall(r"(?m)^(?:\S+=\$\()?\s*(herdr-task .*)$", text)
    bad = set()
    for span in spans:
        for m in re.finditer(r"herdr-task((?:\s+[^\s|;)`]+)*)", plain(span)):
            words = m.group(1).split()
            if words[:1] == ["--on"]:
                words = words[2:]
            if not words or not re.fullmatch(r"[a-z_-]+", words[0]):
                continue
            for sub in ([words[0], w] for w in (words[1].split("|") if len(words) > 1 and words[0] in NESTED and
                                                re.fullmatch(r"[a-z|-]+", words[1]) else [None])):
                sub = [w for w in sub if w]
                out = helptext(sub)
                if out is None:
                    bad.add("herdr-task " + " ".join(sub))
                    continue
                for flag in re.findall(r"(?<![\w-])--[a-z][a-z-]*", m.group(1)):
                    if flag not in out and flag not in ("--help", "--on"):
                        bad.add(f"herdr-task {' '.join(sub)} {flag}")
    return sorted(bad)


def scorecard(files=False, templates=None):
    built = prompts(files, templates)
    rows = [(s, score(s, act(built[s["role"]][0], s))) for s in SITUATIONS]
    roles = {r: (stale(p), [w for w in GONE if w in p]) for r, (p, own) in built.items()}
    return rows, roles


class TemplateReplay(unittest.TestCase):
    def test_every_situation_passes_on_a_clean_install(self):
        rows, _ = scorecard()
        self.assertEqual({s["id"]: why for s, why in rows if why}, {})

    def test_the_bundled_files_pass_too(self):
        rows, _ = scorecard(files=True)
        self.assertEqual({s["id"]: why for s, why in rows if why}, {})

    def test_every_command_a_prompt_names_exists_and_nothing_shepherdr_only_is_left(self):
        for files in (False, True):
            _, roles = scorecard(files)
            self.assertEqual({r: v for r, v in roles.items() if v != ([], [])}, {}, f"files={files}")

    def test_a_deleted_rule_fails_its_situation(self):
        """The replay can fail: the Assistant's delete rule taken out of a copy of its template fails A3."""
        tdir = tempfile.mkdtemp()
        with open(os.path.join(ROOT, "templates", "assistant.md")) as f:
            text = f.read()
        cut = re.sub(r" Never approve a delete \(`rm`\)[^\n]*", "", text)
        self.assertNotEqual(cut, text)
        with open(os.path.join(tdir, "assistant.md"), "w") as f:
            f.write(cut)
        rows, _ = scorecard(templates=tdir)
        self.assertEqual([s["id"] for s, why in rows if why], ["A3"])


def report(files):
    rows, roles = scorecard(files)
    print(f"| Situation | Notes | Role | Result |\n|---|---|---|---|")
    for s, why in rows:
        print(f"| {s['id']}: {s['title']} | {s['notes']} | {s['role']} | {'pass' if not why else 'FAIL: ' + '; '.join(why)} |")
    print(f"\nPassed {sum(not w for _, w in rows)} of {len(rows)}.\n\n| Role | Commands the engine refuses | Shepherdr-only words |\n|---|---|---|")
    for r, (bad, gone) in roles.items():
        print(f"| {r} | {', '.join(f'`{b}`' for b in bad) or 'none'} | {', '.join(f'`{g}`' for g in gone) or 'none'} |")


if __name__ == "__main__":
    if "--score" in sys.argv:
        report("--files" in sys.argv)
    else:
        unittest.main()
