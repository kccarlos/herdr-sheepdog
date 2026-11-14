"""A fake reviewer CLI (claude, codex or agy) for the gate's end-to-end tests (FAC-91). It keeps its argv, prints a screen
in its CLI's format, and as the prompt told it records its verdict (the prompt's `… review <task> … --pass` command).
Env: FAKE_SCREEN (its screen file), FAKE_LOG, FAKE_GO (a file it waits for), FAKE_TAIL (a real screen tail to print)."""
import json, os, re, shlex, subprocess, sys, time

cli, argv, env = sys.argv[1], sys.argv[2:], os.environ
with open(env["FAKE_SCREEN"] + ".argv", "w") as f:
    json.dump(argv, f)
prompt = max(argv, key=len)  # the prompt is the longest argument
mode = env.get("FAKE_MODE", "pass")


def say(*lines):
    print(*lines, sep="\n", flush=True)


def wait(path=env["FAKE_GO"]):
    for _ in range(300):
        if os.path.exists(path):
            return
        time.sleep(0.05)
    sys.exit(3)


def verdict():
    cmd = next(m for m in re.findall(r"`([^`]+)`", prompt) if m.endswith(" --pass"))
    subprocess.run(shlex.split(cmd), check=True)


if cli == "claude":
    say("● Reading the diff", "● Bash(git diff)", "  ⎿  (no changes)")
    if mode == "shell":  # its turn ended with a shell of its own still running
        with open(env["FAKE_TAIL"]) as f:
            say(*f.read().splitlines()[-8:])
        wait()
    verdict()
    say("● Recorded: pass", "✻ Cooked for 5s · done 3:17 AM")
elif cli == "codex":
    open("untracked.txt", "w").close()  # a scratch file of its own, in the worktree
    say("╭──────────────────────────────╮", "│ >_ OpenAI Codex              │", "╰──────────────────────────────╯",
        "› Ask Codex to do anything", "  ? for shortcuts            100% context left")
    wait(env["FAKE_SCREEN"] + ".sent")  # Herdr types a Codex reviewer's prompt once its input box shows (`hc send`)
    time.sleep(0.2)
    with open(env["FAKE_SCREEN"] + ".sent") as f:
        prompt = f.read()
    verdict()
    say("• Verdict recorded: pass.", "▌ Ask Codex to do anything")
else:  # agy
    if mode == "quota":
        say("⚠ Individual quota reached. Please upgrade your subscription. Resets in 10h55m44s.")
        wait()
        sys.exit(1)
    say("Antigravity CLI", "╭──────────────────────────────╮", "│ > reviewing                  │")
    if mode == "block":  # sandboxed: no herdr-task reachable, the verdict is the last lines of its message
        code = re.search(r"round's code is (R\w+)", prompt).group(1)
        say(f"{code} begin", "No findings at or above Must fix.", f"{code} pass", "Verdict ended the message.")
        sys.exit(0)
    verdict()
    say("Verdict recorded: pass.")
