"""Asks (docs/factory.md section 3): the Assistant asks the person without blocking its own pane.

`herdr-task ask "<question>" --option A --option B --recommend A` keeps the ask in the store
(asks/<id>.json) and starts its ask pane: a tiny hc terminal that prints the question as a
numbered prompt and reports itself blocked on a question over OSC 7501, as agents report to
herdr-core, so whatever watches terminals (sheepdog, hc get) sees a question waiting. The answer
(an option's number, or words of one's own) goes into the record, the pane exits, and the answer
reaches the asker as news: sent at once, or held for the scheduler while it can't take it (the
person is writing to it, hc refused). `ask --show <id>` reads a record: held news goes when its
Assistant has ended, so a restarted one can still find the answer.
"""

import base64
import json
import os
import secrets
import sys

from .base import ENTRY, Fail, now, run, STORE
from .output import say

ASKS = os.path.join(STORE, "asks")
MSG_MAX = 300  # the question in the OSC report, cut: hc refuses an overlong sequence


def path_of(ask_id):
    if not ask_id or not all(c.isalnum() for c in ask_id):
        raise Fail(f"no such ask: {ask_id}")
    return os.path.join(ASKS, f"{ask_id}.json")


def load(ask_id):
    try:
        with open(path_of(ask_id)) as f:
            return json.load(f)
    except (OSError, ValueError):
        raise Fail(f"no such ask: {ask_id}")


def save(rec):
    os.makedirs(ASKS, exist_ok=True)
    tmp = f"{path_of(rec['id'])}.{os.getpid()}.tmp"
    with open(tmp, "w") as f:
        json.dump(rec, f)
    os.replace(tmp, path_of(rec["id"]))


def news_line(rec):
    attr = lambda x: str(x).replace("&", "&amp;").replace('"', "&quot;").replace("<", "&lt;").replace(">", "&gt;")
    text = f"Ask {rec['id']} answered: {rec['answer']} (asked: {rec['question']})"
    return (f'<herdr-news task="{attr(rec.get("task") or "ask")}" event="answer" key="{rec["id"]}">'
            f'{text.replace("<", "‹").replace(">", "›")}</herdr-news>')


def cmd_ask(args):
    if args.show is not None:
        rec = load(args.show)
        return say(args, rec, f"{rec['question']}\n" + (f"Answer: {rec['answer']}" if "answer" in rec else "Not answered yet."))
    question = " ".join((args.question or "").split())
    options = [" ".join(o.split()) for o in args.option or []]
    recommend = None if args.recommend is None else " ".join(args.recommend.split())
    if not question or len(options) < 2 or not all(options) or len(set(options)) < len(options):
        raise Fail('Say the question and at least two different options: herdr-task ask "<question>" --option A --option B --recommend A')
    if recommend not in options:
        raise Fail(f"--recommend must be one of the options: {', '.join(options) or 'none given'}")
    if not os.environ.get("HC_TERMINAL_ID"):  # the answer's news goes to the asker's terminal
        raise Fail("Run it from an agent's own terminal (there's no HC_TERMINAL_ID here).")
    rec = {"id": secrets.token_hex(3), "question": question, "options": options, "recommend": recommend,
           "task": args.task, "to": os.environ.get("HC_TERMINAL_ID"), "at": now()}
    save(rec)
    # The pane gets the core's environment, not ours: the store goes along in its command.
    labels = ["--label", "herdr.role=ask", "--label", f"herdr.ask={rec['id']}"]
    try:
        r = run(["hc", "run", "--bg", "--json", "--no-notify", "--outlive-parent", "--name", f"ask-{rec['id']}", *labels, "--",
                 "env", f"HERDR_TASK_STORE={STORE}", sys.executable, ENTRY, "_ask-pane", rec["id"]], check=True)
        rec = {**load(rec["id"]), "terminal": json.loads(r.stdout)["address"]}  # the pane may have saved since
    except (Fail, OSError, ValueError, KeyError, TypeError) as e:
        os.unlink(path_of(rec["id"]))  # no pane, no ask
        raise Fail(f"couldn't start the ask pane: {e if isinstance(e, (Fail, OSError)) else (r.stderr or r.stdout).strip() or 'no terminal in its answer'}")
    save(rec)
    say(args, {"id": rec["id"], "terminal": rec["terminal"]},
        f"Asked ({rec['id']}): its pane is {rec['terminal']}. The answer comes to you as news; carry on with other work.")


def report(state, kind=None, msg=None):
    """Our own status to herdr-core (OSC 7501), as agents report it: colon-separated, msg in base64."""
    fields = [f"state={state}"] + ([f"kind={kind}"] if kind else [])
    if msg:
        fields.append("msg=" + base64.b64encode(msg[:MSG_MAX].encode()).decode())
    sys.stdout.write(f"\x1b]7501;{':'.join(fields)}\x07")
    sys.stdout.flush()


def cmd_ask_pane(args):
    """The ask pane's program: the prompt, one answer into the record, news to the asker."""
    from .news import hold, send_or_hold
    rec = load(args.id)
    print(rec["question"])
    for i, o in enumerate(rec["options"], 1):
        print(f"  {i}. {o}" + ("  (recommended)" if o == rec.get("recommend") else ""))
    print("Answer with a number or your own words, then Enter:")
    report("blocked", "question", rec["question"])
    answer = ""
    while not answer:
        try:
            answer = " ".join(input("> ").split())
        except EOFError:
            raise Fail("no answer: the ask stays open")
        if answer.isascii() and answer.isdigit() and 1 <= int(answer) <= len(rec["options"]):
            answer = rec["options"][int(answer) - 1]
    report("working")
    rec = {**load(args.id), "answer": answer, "answered_at": now()}  # as saved now (ask adds its terminal)
    save(rec)
    if rec.get("to"):
        try:
            sent = send_or_hold(rec["to"], news_line(rec), "news")
        except (Fail, OSError):  # no hc here, or the guard refuses it (another core): held all the same
            sent = None
        if not sent:  # held for the scheduler's next pass (news.held_pass)
            hold(rec["to"], news_line(rec), "news")
    print(f"Answered: {answer}")
