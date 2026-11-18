"""Validators for contracts/gate-evidence.md. Each raises ValueError on a bad record."""
import re

SHA = re.compile(r"[0-9a-f]{40}")


def _check(r, ints=(), strs=(), shas=(), lists=(), enums=None, caps=None, extra=()):
    if not isinstance(r, dict):
        raise ValueError("record is not an object")
    keys = set(ints) | set(strs) | set(shas) | set(lists) | set(enums or {}) | set(caps or {}) | set(extra)
    if set(r) != keys:
        raise ValueError(f"keys differ: {sorted(set(r) ^ keys, key=repr)}")
    for k in ints:
        if type(r[k]) is not int:
            raise ValueError(f"{k} is not an int")
    for k in strs:
        if not isinstance(r[k], str) or not r[k]:
            raise ValueError(f"{k} is not a non-empty string")
    for k in shas:
        if not isinstance(r[k], str) or not SHA.fullmatch(r[k]):
            raise ValueError(f"{k} is not a 40-hex sha")
    for k in lists:
        if not isinstance(r[k], list) or not all(isinstance(x, str) for x in r[k]):
            raise ValueError(f"{k} is not a list of strings")
    for k, ok in (enums or {}).items():
        if r[k] not in ok:
            raise ValueError(f"{k} must be one of {ok}")
    for k, n in (caps or {}).items():
        if not isinstance(r[k], str) or len(r[k]) > n:
            raise ValueError(f"{k} is not a string of at most {n} chars")


def validate_review_run(r):
    r = {"majors_required": False, "before_replan": False, **r}
    _check(r, ints=("round", "at"), strs=("address", "cli"), shas=("head",), extra=("model", "majors_required", "before_replan"))
    if type(r["majors_required"]) is not bool or type(r["before_replan"]) is not bool:
        raise ValueError("majors_required and before_replan are booleans")
    if r["model"] is not None and (not isinstance(r["model"], str) or not r["model"]):
        raise ValueError("model is a non-empty string or null")
    if r["round"] < 1:
        raise ValueError("round starts at 1")


def validate_review(r):
    # only rounds herdr-task recorded; a worker's own `reviewed` round has the old shape and is not validated
    r = {"cli": None, "model": None, "majors": None, "before_replan": False, **r}
    _check(r, ints=("round", "must_fix", "at"), strs=("by", "started_by"), shas=("head",),
           enums={"verdict": ("pass", "changes")}, caps={"report": 2000}, extra=("cli", "model", "majors", "before_replan"))
    if type(r["before_replan"]) is not bool:
        raise ValueError("before_replan is a boolean")
    if r["started_by"] != "herdr-task":
        raise ValueError("started_by must be 'herdr-task'")
    if any(r[k] is not None and (not isinstance(r[k], str) or not r[k]) for k in ("cli", "model")):
        raise ValueError("cli and model are non-empty strings or null")
    if (r["majors"] is not None and (type(r["majors"]) is not int or r["majors"] < 0)) or r["must_fix"] < 0 or r["round"] < 1 or (r["verdict"] == "pass" and r["must_fix"]):
        raise ValueError("round starts at 1; must_fix is not negative and is 0 on a pass")


def validate_check(r):
    _check(r, ints=("exit", "started_at", "finished_at"), strs=("command", "by"), shas=("sha",),
           caps={"output_tail": 4000}, extra=("ci",))
    ci = r["ci"]
    if ci is not None:
        _check(ci, extra=("url",), enums={"state": ("success", "failure", "pending", "none")})
        if ci["url"] is not None and not isinstance(ci["url"], str):
            raise ValueError("ci.url is a string or null")


def validate_verdict(r):
    _check(r, ints=("at",), strs=("by",), shas=("sha",), lists=("commands",), enums={"verdict": ("pass", "fail")},
           caps={"output_tail": 4000})
