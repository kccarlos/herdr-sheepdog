"""Every test runs on a throwaway home, never the person's (SHEP-147). Each test file imports this first.

On import: HOME is a throwaway folder for the whole run (so `~` in a test, a program it runs, or the herdr-task it
starts lands there), and the agent's own terminal and chat (HC_TERMINAL_*, HERDR_BROWSER_TASK and the rest) are
dropped, so nothing reaches the real herdr-core, the real chat's question hook (~/.local/share/herdr/ask/ask-hook.sh)
or its cards. herdr-task itself refuses hc and herdr-app on a changed HOME (base.not_real).

A working `node` is found once, here (NODE; None when there is none): a version manager's shim (mise, asdf) can fail
under the throwaway HOME (mise refuses the person's untrusted config), so a node that fails is replaced by the one
the manager itself names, and its folder goes first on PATH for every program a test starts. Tests that need node
skip on `home_guard.NODE is None`.

Then a Python audit hook fails a test at once, with what it touched, when it opens a file in the real home's Herdr
folders, or starts a program with the real HOME (or none, which falls back to it)."""
import os
import pwd
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def _real_home():
    try:
        return pwd.getpwuid(os.getuid()).pw_dir
    except KeyError:
        return os.path.expanduser("~")


REAL = os.path.realpath(_real_home())
# The person's Herdr and Shepherdr state: data, settings, the synced question hook, chat cards, agents' own configs.
REAL_DIRS = [os.path.join(REAL, p) for p in (
    ".herdr", ".config/herdr-app", ".config/herdr-core", ".config/shepherdr", ".local/share/herdr",
    ".local/share/shepherdr", ".local/state/herdr-core", ".local/state/shepherdr", ".cache/shepherdr", ".claude", ".codex")]
# What the person's environment points at its own terminal, chat and agent with.
DROP = ("HC_TERMINAL_SOCKET", "HC_TERMINAL_ID", "HC_SCOPE", "HC_SOCKET", "HC_SESSION", "HERDR_BROWSER_TASK",
        "HERDR_ASK_CHAINED", "HERDR_APP_HOME", "SHEPHERDR_HOME", "SHEPHERDR_DATA", "HERDR_TASK_STORE",
        "HERDR_TASK_SETTINGS", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
        "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN")


class RealHome(AssertionError):
    pass


def _inside(path, dirs):
    p = os.path.realpath(path)
    return any(p == d or p.startswith(d + os.sep) for d in dirs)


def touches(path):
    """Whether opening this path reaches the person's real Herdr state (this repo's own files don't count: a worktree
    lives under ~/.local/share/shepherdr)."""
    if not isinstance(path, (str, bytes, os.PathLike)):
        return False
    path = os.fsdecode(path)
    if not os.path.isabs(path):
        path = os.path.join(os.getcwd(), path)
    return _inside(path, REAL_DIRS) and not _inside(path, [os.path.realpath(ROOT)])


def _audit(event, args):
    if event == "open":
        if touches(args[0]):
            raise RealHome(f"a test opened {args[0]} in the real home: use a throwaway folder (test/home_guard.py)")
    elif event == "subprocess.Popen":
        env = args[3] if len(args) > 3 and args[3] is not None else os.environ
        home = env.get("HOME") if hasattr(env, "get") else None
        if home is None:
            home = env.get(b"HOME") if hasattr(env, "get") else None
        if not home or os.path.realpath(os.fsdecode(home)) == REAL:
            raise RealHome(f"a test ran {args[1]!r} with {'the real' if home else 'no'} HOME: give it a throwaway "
                           "HOME (test/home_guard.py)")


def _runs(path, env):
    """Whether this node starts and runs a statement under env."""
    try:
        return subprocess.run([path, "-e", "0"], env=env, capture_output=True, timeout=30).returncode == 0
    except (OSError, subprocess.SubprocessError):
        return False


def find_node(real_env):
    """A node that works under the throwaway HOME now set, or None. The first on PATH when it runs; else what mise
    names for `node` (asked with the real HOME, which is where its trust of the person's config lives), when that runs."""
    first = shutil.which("node")
    if first and _runs(first, os.environ):
        return first
    mise = shutil.which("mise")
    if mise:
        try:
            r = subprocess.run([mise, "which", "node"], env=real_env, capture_output=True, text=True, timeout=30)
        except (OSError, subprocess.SubprocessError):
            return None
        path = r.stdout.strip().splitlines()[-1] if r.returncode == 0 and r.stdout.strip() else ""
        if path and os.path.isfile(path) and _runs(path, os.environ):
            return path
    return None


_real_env = dict(os.environ)
for k in DROP:
    os.environ.pop(k, None)
if os.path.realpath(os.environ.get("HOME") or REAL) == REAL:
    os.environ["HOME"] = tempfile.mkdtemp(prefix="shep-test-home-")
_real_env["HOME"] = REAL
NODE = find_node(_real_env)
if NODE and NODE != shutil.which("node"):
    os.environ["PATH"] = os.path.dirname(NODE) + os.pathsep + os.environ.get("PATH", "")
# git commits in throwaway repos need a name; the real ~/.gitconfig is out of reach now
for k, v in (("GIT_AUTHOR_NAME", "Test"), ("GIT_AUTHOR_EMAIL", "test@example.invalid"),
             ("GIT_COMMITTER_NAME", "Test"), ("GIT_COMMITTER_EMAIL", "test@example.invalid")):
    os.environ.setdefault(k, v)
sys.addaudithook(_audit)
