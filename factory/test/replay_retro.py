"""SDF-49 replay: the retro template on two real non-converging round histories (SDF-16, SDF-31),
built at round 3, where the retro trigger fires. Without --live it prints each prompt and parses the
recorded answer in replay/retro/<ref>.out.txt; with --live it runs one read-only Fable session per
situation in a detached worktree at that round's head and records its answer there.

    python3 test/replay_retro.py [--live --dir <scratch dir>]
"""
import argparse
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "mods", "tasks", "bin"))
from herdr_task import retro  # noqa: E402

DIR = os.path.join(HERE, "replay", "retro")
SITUATIONS = ("SDF-16", "SDF-31")
READ_ONLY = "Read Grep Glob Bash(git log:*) Bash(git diff:*) Bash(git show:*) Bash(wc:*) Bash(make test-factory:*)"


def prompt(ref, worktree="<the worktree>"):
    with open(os.path.join(DIR, f"{ref}.json"), encoding="utf-8") as f:
        fx = json.load(f)
    reviews = [{**r, "result": "report"} for r in fx["rounds"]]
    values = {k: v for k, v in fx.items() if k not in ("rounds", "brief")}
    values.update(rounds_used=len(reviews), rounds_with_majors=2, worktree=worktree, checks_cmd="make test-factory",
                  rounds_table=retro.rounds_table({"reviews": reviews}))
    return retro.fill(values, fx["brief"])


def recorded(ref):
    try:
        with open(os.path.join(DIR, f"{ref}.out.txt"), encoding="utf-8") as f:
            return f.read()
    except OSError:
        return None


def live(ref, scratch):
    with open(os.path.join(DIR, f"{ref}.json"), encoding="utf-8") as f:
        head = json.load(f)["head_sha"]
    wt = os.path.join(scratch, f"retro-{ref.lower()}")
    if not os.path.isdir(wt):
        subprocess.run(["git", "worktree", "add", "-q", "--detach", wt, head], check=True)
    at = subprocess.run(["git", "-C", wt, "rev-parse", "HEAD"], capture_output=True, text=True, check=True).stdout
    if not at.startswith(head):
        raise SystemExit(f"{wt} is at {at.strip()}, not {head}")
    r = subprocess.run(["claude", "-p", "--model", retro.FABLE["model"], "--permission-mode", "dontAsk", "--allowedTools", READ_ONLY],
                       cwd=wt, input=prompt(ref, wt), capture_output=True, text=True, timeout=1800)  # the prompt on stdin
    if r.returncode:
        raise SystemExit(f"{ref}: claude exited {r.returncode}: {r.stderr[-500:]}")
    with open(os.path.join(DIR, f"{ref}.out.txt"), "w", encoding="utf-8") as f:
        f.write(r.stdout)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--live", action="store_true")
    ap.add_argument("--dir")
    ap.add_argument("--show", action="store_true", help="print each prompt")
    a = ap.parse_args()
    if a.live and not a.dir:
        ap.error("--live needs --dir, a scratch folder for the worktrees")
    for ref in SITUATIONS:
        if a.live:
            live(ref, a.dir)
        if a.show:
            print(prompt(ref), "\n")
        out = recorded(ref)
        rec, why = retro.parse_tail(out) if out else (None, "no recorded answer")
        print(f"{ref}: " + (f"{rec['verdict']}, cause {rec['cause']}, second {rec['second_cause']}, replan {rec['replan']}, "
                            f"confidence {rec['confidence']}" if rec else f"FAILED: {why}"))


if __name__ == "__main__":
    main()
