#!/usr/bin/env python3
"""Run agy and resume a turn interrupted by a denied command.

Usage: agy_run.py MODEL DIR PROMPT_FILE LOG [TIMEOUT_SECONDS]
The final response is written to LOG.md; diagnostics and stream stay in LOG.
"""
import json
import os
import subprocess
import sys


def main():
    model, cwd, prompt_file, log = sys.argv[1:5]
    timeout = sys.argv[5] if len(sys.argv) > 5 else "2400"
    env = {k: v for k, v in os.environ.items() if k != "GEMINI_API_KEY"}
    prompt = f"Your instructions are in {prompt_file}. Read them and carry them out in this session, without background agents."
    conversation = None
    for attempt in range(1, 9):
        cmd = ["agy", "--model", model, "--mode", "accept-edits", "--output-format", "stream-json",
               "--print-timeout", f"{timeout}s", f"-p={prompt}"]
        if conversation:
            cmd += ["--conversation", conversation]
        try:
            run = subprocess.run(cmd, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                 capture_output=True, text=True)
        except OSError as exc:
            print(f"agy could not start: {exc}", file=sys.stderr)
            return 1
        with open(log, "a") as f:
            f.write(run.stdout)
            f.write(run.stderr)
        active, result = {}, {}
        for line in run.stdout.splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            step = event.get("step_update") or {}
            conversation = (step.get("conversation_id") or (event.get("init") or {}).get("conversation_id")
                            or event.get("conversation_id") or conversation)
            if step.get("tool_name") == "run_command":
                if step.get("state") == "ACTIVE":
                    active = (step.get("tool_info") or {}).get("parameters") or {}
            result = event.get("result") or result
        denied = result.get("denied_actions")
        print(f"attempt {attempt}: status={result.get('status')} denied={bool(denied)}", flush=True)
        if denied:
            if not conversation:
                print("denied action but no conversation id", file=sys.stderr)
                return 1
            command = active.get("CommandLine", "(unknown)")
            prompt = (f"The command `{command[:300]}` was denied. Do not retry it. "
                      "Use file tools or permitted simple commands, and complete the original task.")
            continue
        response = result.get("response") or result.get("error") or ""
        with open(log + ".md", "w") as f:
            f.write(response)
        if run.returncode or result.get("status") != "SUCCESS":
            print(f"agy failed (exit {run.returncode}): {response[:1000]}", file=sys.stderr)
            return 1
        return 0
    print("agy repeatedly encountered denied actions", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
