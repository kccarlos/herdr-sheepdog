#!/usr/bin/env python3
"""Validate the audit's JSONL protocol and produce a safe report and fix input."""
import datetime
import json
import sys
from pathlib import Path

SCOPES = ["daemon-security", "concurrency-lifecycle", "approvals-held-questions",
          "docs-against-code", "iphone-app", "herdr-app-panel", "test-gaps"]


def lines(path):
    text = Path(path).read_text().strip()
    if text == "":
        raise ValueError("empty response; expected JSONL or the literal []")
    if text == "[]":
        return []
    return [json.loads(line) for line in text.splitlines() if line.strip()]


def render(findings_path, verdicts_path, report, selected):
    findings = lines(findings_path)
    for item in findings:
        if not isinstance(item, dict) or any(not isinstance(item.get(k), str) or not item[k].strip()
                for k in ("file", "scenario", "fix", "severity")) or type(item.get("line")) is not int or item["line"] < 1:
            raise ValueError("invalid finding")
        if item["severity"].lower() not in ("high", "medium", "low"):
            raise ValueError("invalid severity")
    verdicts = lines(verdicts_path)
    by_id = {}
    for v in verdicts:
        if (not isinstance(v, dict) or type(v.get("id")) is not int or v["id"] in by_id
                or v.get("verdict") not in ("confirmed", "rejected")
                or not isinstance(v.get("reason"), str) or not v["reason"].strip()):
            raise ValueError("invalid verdict")
        by_id[v["id"]] = v
    if set(by_id) != set(range(1, len(findings) + 1)):
        raise ValueError("verdicts must cover each finding exactly once")
    chosen = []
    with open(report, "a") as out:
        if not findings:
            out.write("\nNo findings.\n")
        for i, item in enumerate(findings, 1):
            v = by_id[i]
            # JSON strings keep model-supplied Markdown and newlines inert.
            out.write(f"\n## Finding {i}: {v['verdict']}\n\n```json\n")
            out.write(json.dumps({**item, **v}, ensure_ascii=True, indent=2).replace('```', '` ` `'))
            out.write("\n```\n")
            if v["verdict"] == "confirmed" and item["severity"].lower() == "high" and len(chosen) < 3:
                chosen.append({"id": i, **item, **v})
    Path(selected).write_text(json.dumps(chosen, indent=2))


if __name__ == "__main__":
    try:
        if sys.argv[1] == "scope":
            day = datetime.date.fromisoformat(sys.argv[2])
            print(SCOPES[(day - datetime.date(1970, 1, 1)).days % len(SCOPES)])
        elif sys.argv[1] == "render":
            render(*sys.argv[2:6])
        else:
            raise ValueError("unknown command")
    except (ValueError, OSError, json.JSONDecodeError) as exc:
        print(f"audit data: {exc}", file=sys.stderr)
        sys.exit(1)
