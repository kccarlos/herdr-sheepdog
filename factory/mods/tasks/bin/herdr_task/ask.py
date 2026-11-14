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
import codecs
import json
import os
import re
import secrets
import select
import sys
import termios
import tty

from . import base as _base, store as _store
from .base import ENTRY, Fail, now, run, STORE
from .output import say

_DEFAULT_ASKS, _DEFAULT_RUN = os.path.join(STORE, "asks"), run
ASKS = _DEFAULT_ASKS
# Sheepdog's rail refuses an approving answer whose words open with one of these (supervisor/inputs.go negativeWords)
NEGATIVE = {"no", "nope", "nah", "don't", "dont", "stop", "cancel", "never", "deny", "decline", "reject", "wait", "hold", "negative"}
MSG_MAX = 300  # the question in the OSC report, cut: hc refuses an overlong sequence


def asks_dir():
    return ASKS if ASKS != _DEFAULT_ASKS else os.path.join(_store.STORE, "asks")


def path_of(ask_id):
    if not ask_id or not all(c.isalnum() for c in ask_id):
        raise Fail(f"no such ask: {ask_id}")
    return os.path.join(asks_dir(), f"{ask_id}.json")


def load(ask_id):
    try:
        with open(path_of(ask_id)) as f:
            return json.load(f)
    except (OSError, ValueError):
        raise Fail(f"no such ask: {ask_id}")


def save(rec):
    os.makedirs(asks_dir(), exist_ok=True)
    tmp = f"{path_of(rec['id'])}.{os.getpid()}.tmp"
    with open(tmp, "w") as f:
        json.dump(rec, f)
    os.replace(tmp, path_of(rec["id"]))


def open_asks(s, t):
    """Unanswered `herdr-task ask` records for task `t` (by `--task`, `t["asks"]`, or its worker/Assistant)."""
    try:
        names = sorted(n for n in os.listdir(asks_dir()) if n.endswith(".json"))
    except OSError:
        return []
    ref = s.ref(t).upper() if s and hasattr(s, "ref") else ""
    key = s.key(t).lower() if s and hasattr(s, "key") else ""
    folder = os.path.realpath(f) if (f := (getattr(s, "loc", None) or {}).get("folder")) else None
    ids = {a.get("id") for a in t.get("asks") or [] if isinstance(a, dict) and a.get("id")}
    workers = {a.get("address") for a in t.get("agents") or [] if isinstance(a, dict) and a.get("address")}
    terms = workers | {a.get("parent") for a in t.get("agents") or [] if isinstance(a, dict) and a.get("parent")}
    ref_re = re.compile(rf"(?<![A-Z0-9-]){re.escape(ref)}(?![A-Z0-9])", re.I) if ref else None
    out = []
    for name in names:
        try:
            with open(os.path.join(asks_dir(), name)) as f:
                rec = json.load(f)
        except (OSError, ValueError):
            continue
        if not isinstance(rec, dict) or rec.get("answer") is not None:
            continue
        orig_f = (rec.get("origin") or {}).get("folder")
        if orig_f and folder and os.path.realpath(orig_f) != folder and rec.get("id") not in ids:
            continue
        rt = str(rec.get("task") or "").strip()
        bare_n = rt.lstrip("#")
        if ((ref and rt.upper() == ref) or (key and rt.lower() == key) or rec.get("id") in ids
                or (not rt and not rec.get("checklist")
                    and (rec.get("to") in workers or (rec.get("to") in terms and bool(ref_re and ref_re.search(str(rec.get("question") or ""))))))
                or (bare_n.isdigit() and int(bare_n) == t.get("id")
                    and ((orig_f and folder and os.path.realpath(orig_f) == folder) or rec.get("to") in terms))):
            out.append(rec)
    return out


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
    if not question or not 2 <= len(options) <= 9 or not all(options) or len(set(options)) < len(options):
        raise Fail('Say the question and two to nine different options: herdr-task ask "<question>" --option A --option B --recommend A')
    if recommend not in options:
        raise Fail(f"--recommend must be one of the options: {', '.join(options) or 'none given'}")
    words = [w.strip(".!?,;:") for w in recommend.lower().split()]
    if any(re.search(r"\s\([a-z0-9]{1,6}\)$", o, re.I) for o in options):  # sheepdog reads it as the option's key
        raise Fail("An option can't end in a short word in parentheses, like (y) or (v2): sheepdog would press that, not the number.")
    first = " ".join(words[:2]) if words[:2] == ["do", "not"] else (words or [""])[0]
    if first in NEGATIVE | {"do not"} and not any(o.lower().startswith("yes") for o in options):
        raise Fail(f'Sheepdog can\'t tap a recommended option that starts with "{first}" unless another option starts with "Yes": reword it or add one.')
    checklist, origin = getattr(args, "checklist", None), getattr(args, "origin", None)
    to = getattr(args, "to", None) or (None if origin else os.environ.get("HC_TERMINAL_ID"))
    if not to and not checklist and not origin:  # the answer's news goes to the asker's terminal
        raise Fail("Run it from an agent's own terminal (there's no HC_TERMINAL_ID here).")
    rec = {"id": secrets.token_hex(3), "question": question, "options": options, "recommend": recommend,
           "task": args.task, "to": to, "at": now(),
           **({"checklist": checklist} if checklist else {}), **({"origin": origin} if origin else {})}
    save(rec)
    # The pane gets the core's environment, not ours: the store goes along in its command.
    labels = ["--label", "herdr.role=ask", "--label", f"herdr.ask={rec['id']}"]
    env_kv = [f"HERDR_TASK_STORE={os.path.dirname(asks_dir())}"] + (
        [f"HERDR_TASK_SETTINGS={__import__('herdr_task.settings', fromlist=['SETTINGS']).SETTINGS}"] if checklist else [])
    runner, r = run if run is not _DEFAULT_RUN else _base.run, None
    try:
        r = runner(["hc", "run", "--bg", "--json", "--no-notify", "--outlive-parent", "--name", f"ask-{rec['id']}", *labels, "--",
                    "env", *env_kv, sys.executable, ENTRY, "_ask-pane", rec["id"]], check=True)
        rec = {**load(rec["id"]), "terminal": json.loads(r.stdout)["address"]}  # the pane may have saved since
    except (Fail, OSError, ValueError, KeyError, TypeError) as e:
        os.unlink(path_of(rec["id"]))  # no pane, no ask
        raise Fail(f"couldn't start the ask pane: {e if isinstance(e, (Fail, OSError)) or r is None else (r.stderr or r.stdout).strip() or 'no terminal in its answer'}")
    save(rec)
    say(args, {"id": rec["id"], "terminal": rec["terminal"]},
        f"Asked ({rec['id']}): its pane is {rec['terminal']}. The answer comes to you as news; carry on with other work.")
    return rec


def report(state, kind=None, msg=None):
    """Our own status to herdr-core (OSC 7501), as agents report it: colon-separated, msg in base64."""
    fields = [f"state={state}"] + ([f"kind={kind}"] if kind else [])
    if msg:
        fields.append("msg=" + base64.b64encode(msg[:MSG_MAX].encode()).decode())
    sys.stdout.write(f"\x1b]7501;{':'.join(fields)}\x07")
    sys.stdout.flush()


def read_answer(count):
    """One answer, read key by key: a digit alone (sheepdog's tap sends no Enter) answers once nothing follows it for a
    moment; anything else is a line of words, as typed (Backspace edits, arrow keys are ignored). Without a terminal,
    a plain line."""
    if not sys.stdin.isatty():
        return input("> ")
    fd, old = sys.stdin.fileno(), termios.tcgetattr(sys.stdin.fileno())
    dec, buf, esc = codecs.getincrementaldecoder("utf-8")("replace"), "", None
    try:
        tty.setcbreak(fd, termios.TCSADRAIN)  # keeps what's already typed; before the prompt shows: switching later can drop a key typed in between
        sys.stdout.write("> ")
        sys.stdout.flush()
        while True:
            if len(buf) == 1 and buf.isascii() and buf.isdigit() and 1 <= int(buf) <= count \
                    and not select.select([fd], [], [], 0.3)[0]:
                return buf
            data = os.read(fd, 1)  # one byte at a time: nothing past the line's end is lost
            if not data:
                raise EOFError
            for ch in dec.decode(data):
                if esc == "esc":  # an escape sequence (an arrow key) is swallowed up to its last character
                    esc = "seq" if ch in "[O" else None
                elif esc == "seq":
                    esc = None if "@" <= ch <= "~" else esc
                elif ch == "\x1b":  # a lone Esc is no sequence: it must not swallow the next key
                    esc = "esc" if select.select([fd], [], [], 0.05)[0] else None
                elif ch in "\r\n":
                    return buf
                elif ch in "\x7f\b":
                    if buf:
                        buf = buf[:-1]
                        sys.stdout.write("\b \b")
                elif ch == "\x04" and not buf:
                    raise EOFError
                elif ch.isprintable():
                    buf += ch
                    sys.stdout.write(ch)
                sys.stdout.flush()
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old)
        sys.stdout.write("\r\n")


def cmd_ask_pane(args):
    """The ask pane's program: the prompt, one answer into the record, news to the asker."""
    from .news import hold, send_or_hold
    rec = load(args.id)
    print(rec["question"])
    for i, o in enumerate(rec["options"], 1):  # the ❯ marks the recommended option: sheepdog's prompt parser needs one
        rec_here = o == rec.get("recommend")
        print(f"{'❯' if rec_here else ' '} {i}. {o}" + ("  (recommended)" if rec_here else ""))
    print("Answer with a number or your own words, then Enter:")
    report("blocked", "question", rec["question"])
    answer = ""
    while not answer:
        try:
            answer = " ".join(read_answer(len(rec["options"])).split())
        except EOFError:
            raise Fail("no answer: the ask stays open")
        if answer.isascii() and answer.isdigit() and 1 <= int(answer) <= len(rec["options"]):
            answer = rec["options"][int(answer) - 1]
    report("working")
    rec = {**load(args.id), "answer": answer, "answered_at": now()}  # as saved now (ask adds its terminal)
    save(rec)
    if rec.get("checklist"):
        try:
            __import__("herdr_task.project", fromlist=["apply_checklist_answer"]).apply_checklist_answer(rec)
        except Fail:
            pass
    if not rec.get("to") and (rec.get("origin") or {}).get("space"):
        try:
            rec["to"] = __import__("herdr_task.launch", fromlist=["coordinator_in"]).coordinator_in(rec["origin"]["space"])
        except Fail:
            pass
    if rec.get("to"):
        try:
            sent = send_or_hold(rec["to"], news_line(rec), "news")
        except (Fail, OSError):  # no hc here, or the guard refuses it (another core): held all the same
            sent = None
        if not sent:  # held for the scheduler's next pass (news.held_pass)
            hold(rec["to"], news_line(rec), "news")
    print(f"Answered: {answer}")
