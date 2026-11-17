#!/usr/bin/env python3
"""Unit tests for scripts/factory/baseline.py on synthetic fixtures."""

from __future__ import annotations

import datetime
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import baseline  # noqa: E402


def local_iso_and_ms(date_str: str, h: int, m: int, s: int = 0) -> tuple[str, int]:
    """Build a local-timezone ISO timestamp and epoch ms so date bucketing is timezone-independent."""
    y, mo, d = (int(x) for x in date_str.split("-"))
    dt = datetime.datetime(y, mo, d, h, m, s).astimezone()
    return dt.isoformat(), int(dt.timestamp() * 1000)


def write_jsonl(path: str, rows: list[dict]) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r) + "\n")


def write_json(path: str, obj: object) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(obj, f)


class BaselineClassifierTests(unittest.TestCase):
    def test_missing_directories_returns_zeroes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            missing = os.path.join(tmp, "does-not-exist")
            rep = baseline.measure(
                since="2026-11-10",
                until="2026-11-11",
                usage_dirs=[missing],
                tasks_dir=missing,
                claude_dir=missing,
                codex_dir=missing,
            )
            self.assertEqual(rep["days_count"], 2)
            self.assertEqual(rep["totals"]["owner_touches"], 0)
            self.assertEqual(rep["totals"]["outside_interventions"], 0)
            self.assertEqual(rep["median_per_day"]["owner_touches"], 0.0)

    def test_end_to_end_synthetic_classification_and_dedup(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            alpha_repo = os.path.join(tmp, "repos", "alpha-app")
            os.makedirs(alpha_repo, exist_ok=True)

            tasks_dir = os.path.join(tmp, "tasks")
            service_dir = os.path.join(tmp, "service")
            legacy_service_link = os.path.join(tmp, "legacy-service")
            claude_dir = os.path.join(tmp, "claude")
            codex_dir = os.path.join(tmp, "codex")
            os.makedirs(service_dir, exist_ok=True)
            os.symlink(service_dir, legacy_service_link)

            _, ms_d1_accept_outside = local_iso_and_ms("2026-11-10", 3, 15, 20)
            _, ms_d1_accept_auto = local_iso_and_ms("2026-11-10", 4, 0, 0)
            _, ms_d2_accept_owner = local_iso_and_ms("2026-11-11", 10, 5, 0)

            # Task store with 3 tasks:
            # ALP-1 accepted by outside babysitter CLI -> should count as outside intervention, NOT owner UI accept
            # ALP-2 auto-accepted by engine -> neither touch nor intervention
            # ALP-3 accepted by owner in UI -> counts as 1 owner UI accept ('other')
            write_json(
                os.path.join(tasks_dir, "repos-alpha-app.json"),
                {
                    "folder": alpha_repo,
                    "prefix": "ALP",
                    "tasks": [
                        {
                            "id": 1,
                            "title": "Add parser",
                            "state": "done",
                            "space": "sp_alpha01",
                            "accepted_at": ms_d1_accept_outside,
                            "accepted_by": "person",  # forged by CLI caller outside coordinator
                            "agents": [{"address": "t2w", "parent": "t1a"}],
                            "review_runs": [{"address": "t3r"}],
                        },
                        {
                            "id": 2,
                            "title": "Add unit tests",
                            "state": "done",
                            "space": "sp_alpha01",
                            "accepted_at": ms_d1_accept_auto,
                            "accepted_by": "auto",
                            "auto_accepted": True,
                            "agents": [{"address": "t4w", "parent": "t1a"}],
                        },
                        {
                            "id": 3,
                            "title": "Polish empty state",
                            "state": "done",
                            "space": "sp_alpha01",
                            "accepted_at": ms_d2_accept_owner,
                            "accepted_by": "person",
                            "agents": [{"address": "t5w", "parent": "t1a"}],
                        },
                    ],
                },
            )

            # Asks: one answered by outside `hc keys t9k` (not owner!), one release ask answered by owner
            iso_keys, ms_keys = local_iso_and_ms("2026-11-10", 2, 0, 0)
            _, ms_ask_rel = local_iso_and_ms("2026-11-11", 11, 0, 0)
            write_json(
                os.path.join(tasks_dir, "asks", "a11111.json"),
                {
                    "id": "a11111",
                    "question": "Continue ALP-1?",
                    "options": ["Yes", "No"],
                    "recommend": "Yes",
                    "task": "ALP-1",
                    "to": "t2w",
                    "terminal": "t9k",
                    "at": ms_keys - 30_000,
                    "answer": "Yes",
                    "answered_at": ms_keys + 2_000,
                },
            )
            write_json(
                os.path.join(tasks_dir, "asks", "b22222.json"),
                {
                    "id": "b22222",
                    "question": "Tag v1.0.0 release for TestFlight now?",
                    "options": ["Yes, tag v1.0.0", "Wait"],
                    "recommend": "Yes, tag v1.0.0",
                    "kind": "release",
                    "task": "ALP-3",
                    "to": "t1a",
                    "terminal": "t8k",
                    "at": ms_ask_rel - 60_000,
                    "answer": "Yes, tag v1.0.0",
                    "answered_at": ms_ask_rel,
                },
            )

            # Usage log (symlinked into legacy_service_link to verify realpath dedup):
            # 1 setting change on day 2 + duplicate accept event within 30s of ALP-3's accepted_at
            _, ms_setting = local_iso_and_ms("2026-11-11", 9, 0, 0)
            write_jsonl(
                os.path.join(service_dir, "usage.jsonl"),
                [
                    {"at": ms_setting, "kind": "act", "from": "window", "space": "sp_alpha01", "key": "tasks.merge"},
                    {"at": ms_d2_accept_owner + 1000, "kind": "act", "from": "window", "task": "ALP-3", "type": "accept"},
                ],
            )

            # 1. Project Assistant session for alpha-app
            t_open, _ = local_iso_and_ms("2026-11-10", 1, 0, 0)
            t_outside_send, _ = local_iso_and_ms("2026-11-10", 2, 10, 0)
            t_outside_arrive, _ = local_iso_and_ms("2026-11-10", 2, 10, 3)
            t_pilot, _ = local_iso_and_ms("2026-11-10", 5, 0, 0)
            t_news, _ = local_iso_and_ms("2026-11-10", 5, 10, 0)
            t_digest_q, _ = local_iso_and_ms("2026-11-11", 9, 30, 0)
            t_digest_a, _ = local_iso_and_ms("2026-11-11", 9, 30, 5)
            t_ask_prompt, _ = local_iso_and_ms("2026-11-11", 9, 40, 0)
            t_owner_ans, _ = local_iso_and_ms("2026-11-11", 9, 41, 0)

            write_jsonl(
                os.path.join(claude_dir, "proj-alpha", "asst0001.jsonl"),
                [
                    # Turn 1: Assistant template + owner's opening goal
                    {
                        "type": "user",
                        "cwd": alpha_repo,
                        "timestamp": t_open,
                        "message": {
                            "content": (
                                "You are the coordinator of one space in Herdr.\n"
                                "Otherwise say in one short line that you are ready, then wait for the person.\n\n"
                                "Build milestone 1 parser and tests."
                            )
                        },
                    },
                    {
                        "type": "assistant",
                        "timestamp": t_open,
                        "message": {
                            "content": [
                                {"type": "tool_use", "id": "tu1", "name": "Bash", "input": {"command": "herdr-task add 'Add parser'"}},
                                {"type": "text", "text": "Started ALP-1."},
                            ]
                        },
                    },
                    # Message arriving during outside orchestrator's `hc send` window -> NOT an owner touch
                    {
                        "type": "user",
                        "timestamp": t_outside_arrive,
                        "message": {"content": "Unblock ALP-1 now."},
                    },
                    # Autopilot wake -> counted as autopilot_wake, NOT an owner touch
                    {
                        "type": "user",
                        "timestamp": t_pilot,
                        "message": {"content": '<herdr-autopilot reason="schedule" space="sp_alpha01">\nbody\n</herdr-autopilot>'},
                    },
                    # Herdr news -> NOT an owner touch
                    {
                        "type": "user",
                        "timestamp": t_news,
                        "message": {"content": '<herdr-news task="ALP-1" event="ready">ALP-1 is ready</herdr-news>'},
                    },
                    # Day 2: Owner asks read-only status question -> 'digest'
                    {
                        "type": "user",
                        "timestamp": t_digest_q,
                        "message": {"content": "How is alpha-app progressing?"},
                    },
                    {
                        "type": "assistant",
                        "timestamp": t_digest_a,
                        "message": {
                            "content": [
                                {"type": "tool_use", "id": "tu2", "name": "Bash", "input": {"command": "herdr-task list --json"}},
                                {"type": "text", "text": "ALP-3 is ready."},
                            ]
                        },
                    },
                    # Assistant asks a question, owner replies shortly -> 'ask_answer'
                    {
                        "type": "assistant",
                        "timestamp": t_ask_prompt,
                        "message": {"content": [{"type": "text", "text": "Should we skip the optional benchmark for M1?"}]},
                    },
                    {
                        "type": "user",
                        "timestamp": t_owner_ans,
                        "message": {"content": '<hc reply-to="Should we skip"/>\nYes, skip it.'},
                    },
                ],
            )

            # 2. Outside orchestrator ("babysitter") session
            t_b_owner, _ = local_iso_and_ms("2026-11-10", 1, 55, 0)
            t_b_sched, _ = local_iso_and_ms("2026-11-10", 3, 14, 0)
            t_b_act2, _ = local_iso_and_ms("2026-11-10", 3, 15, 0)
            write_jsonl(
                os.path.join(claude_dir, "proj-babysitter", "baby0001.jsonl"),
                [
                    # Owner tells babysitter a standing rule -> 1 owner touch ('rule')
                    {
                        "type": "user",
                        "cwd": os.path.join(tmp, "workspace"),
                        "timestamp": t_b_owner,
                        "origin": {"kind": "human"},
                        "turnOrigin": "human",
                        "message": {"content": "From now on always use codex for reviews in alpha-app."},
                    },
                    # Episode 1 on Day 1: 2 actions in one turn (`hc keys t9k` + `hc send t1a`)
                    {
                        "type": "assistant",
                        "timestamp": iso_keys,
                        "message": {
                            "content": [
                                {"type": "tool_use", "id": "b1", "name": "Bash", "input": {"command": "hc keys t9k 1"}},
                            ]
                        },
                    },
                    {
                        "type": "assistant",
                        "timestamp": t_outside_send,
                        "message": {
                            "content": [
                                {"type": "tool_use", "id": "b2", "name": "Bash", "input": {"command": "hc send t1a 'Unblock ALP-1 now.'"}},
                            ]
                        },
                    },
                    # Scheduled /loop wake -> NOT an owner touch!
                    {
                        "type": "user",
                        "timestamp": t_b_sched,
                        "turnOrigin": "scheduled",
                        "message": {"content": "Check factory tasks"},
                    },
                    # Episode 2 on Day 1: wrapper accept + edit + read-only list (list ignored)
                    {
                        "type": "assistant",
                        "timestamp": t_b_act2,
                        "message": {
                            "content": [
                                {
                                    "type": "tool_use",
                                    "id": "b3",
                                    "name": "Bash",
                                    "input": {
                                        "command": (
                                            "herdr-task list --json && "
                                            "bash verify.sh ALP-1 1-add-parser accept && "
                                            "herdr-task edit ALP-3 --model sonnet"
                                        )
                                    },
                                }
                            ]
                        },
                    },
                ],
            )

            # 3. Codex gate reviewer session -> recording its own `herdr-task review --pass` is NOT an intervention
            t_rev, _ = local_iso_and_ms("2026-11-10", 3, 0, 0)
            write_jsonl(
                os.path.join(codex_dir, "2026", "11", "10", "rollout-2026-11-10-rev00001.jsonl"),
                [
                    {"type": "session_meta", "timestamp": t_rev, "payload": {"cwd": os.path.join(tmp, "shepherdr")}},
                    {
                        "type": "response_item",
                        "timestamp": t_rev,
                        "payload": {
                            "role": "user",
                            "content": [{"type": "input_text", "text": "You review one Herdr task's work before it can merge."}],
                        },
                    },
                    {
                        "type": "response_item",
                        "timestamp": t_rev,
                        "payload": {
                            "type": "function_call",
                            "name": "exec_command",
                            "arguments": json.dumps({"cmd": f"herdr-task review ALP-1 --folder {alpha_repo} --pass"}),
                        },
                    },
                ],
            )

            # 4. Unrelated regular dev session in alpha-app that runs `pkill` -> ignored completely
            write_jsonl(
                os.path.join(claude_dir, "proj-dev", "dev00001.jsonl"),
                [
                    {
                        "type": "user",
                        "cwd": alpha_repo,
                        "timestamp": t_open,
                        "message": {"content": "Fix a typo in README"},
                    },
                    {
                        "type": "assistant",
                        "timestamp": t_open,
                        "message": {
                            "content": [{"type": "tool_use", "id": "d1", "name": "Bash", "input": {"command": "pkill -f myserver"}}]
                        },
                    },
                ],
            )

            rep = baseline.measure(
                since="2026-11-10",
                until="2026-11-11",
                usage_dirs=[service_dir, legacy_service_link],
                tasks_dir=tasks_dir,
                claude_dir=claude_dir,
                codex_dir=codex_dir,
            )

            d1, d2 = rep["per_day"]
            # Day 1: 2 owner touches (1 opening goal in assistant + 1 rule in babysitter; scheduled /loop & outside send excluded)
            self.assertEqual(d1["date"], "2026-11-10")
            self.assertEqual(d1["owner_touches"], 2)
            self.assertEqual(d1["touches_by_touchpoint"], {"goal": 1, "rule": 1})
            # Day 1 outside interventions: hc_keys (1) + hc_send (1) + herdr_task_accept (1) + herdr_task_edit (1) = 4 across 2 turns
            self.assertEqual(d1["outside_interventions"], 4)
            self.assertEqual(d1["intervention_episodes"], 2)
            self.assertEqual(
                d1["interventions_by_type"],
                {"hc_send": 1, "hc_keys": 1, "herdr_task_accept": 1, "herdr_task_edit": 1},
            )
            self.assertEqual(d1["autopilot_wakes"], 1)

            # Day 2: 5 owner touches:
            # - 1 setting change (rule, settings)
            # - 1 digest question (digest, assistant_chat)
            # - 1 reply to assistant question (ask_answer, assistant_chat)
            # - 1 UI accept of ALP-3 (other, ui_accept; deduplicated against usage.jsonl accept)
            # - 1 release ask b22222 answered (release_approval, ask_card)
            self.assertEqual(d2["date"], "2026-11-11")
            self.assertEqual(d2["owner_touches"], 5)
            self.assertEqual(
                d2["touches_by_touchpoint"],
                {"rule": 1, "ask_answer": 1, "release_approval": 1, "digest": 1, "other": 1},
            )
            self.assertEqual(d2["outside_interventions"], 0)

            # Project attribution: everything maps cleanly to alpha-app
            self.assertEqual(rep["per_project"]["alpha-app"]["owner_touches"], 7)
            self.assertEqual(rep["per_project"]["alpha-app"]["outside_interventions"], 4)

    def test_shell_parser_ignores_heredoc_bodies(self) -> None:
        cmd = (
            "python3 - <<'EOF'\n"
            "hc send t1a 'inside heredoc should not count'\n"
            "herdr-task accept FAC-99\n"
            "EOF\n"
            "herdr-task resume FAC-12"
        )
        acts = baseline.parse_bash_actions(cmd, {})
        self.assertEqual(len(acts), 1)
        self.assertEqual(acts[0]["kind"], "herdr_task")
        self.assertEqual(acts[0]["verb"], "resume")


if __name__ == "__main__":
    unittest.main()
