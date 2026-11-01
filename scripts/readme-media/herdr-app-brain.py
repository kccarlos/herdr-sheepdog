#!/usr/bin/env python3
"""Loopback OpenAI-compatible brain fixture for the README recording."""

import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


REPLY = "shop-tests is waiting on you: it asks to run the end-to-end tests."


def completion(content: str) -> dict:
    return {
        "id": "chatcmpl-readme-fixture",
        "object": "chat.completion",
        "created": int(time.time()),
        "model": "scripted",
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": content},
                "finish_reason": "stop",
            }
        ],
        "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
    }


class Handler(BaseHTTPRequestHandler):
    def log_message(self, _format, *_args):
        pass

    def send_json(self, value: dict):
        body = json.dumps(value).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.endswith("/models"):
            self.send_json(
                {
                    "object": "list",
                    "data": [{"id": "scripted", "object": "model", "owned_by": "readme-fixture"}],
                }
            )
            return
        self.send_error(404)

    def do_POST(self):
        if not self.path.endswith("/chat/completions"):
            self.send_error(404)
            return
        length = int(self.headers.get("Content-Length", "0"))
        request = json.loads(self.rfile.read(length) or b"{}")
        text = "\n".join(
            str(message.get("content", "")) for message in request.get("messages", [])
        )
        if "How are my agents doing?" in text:
            answer = {
                "spoken": REPLY,
                "detail": "The recording fixture reports the open end-to-end test prompt.",
                "needs_user": False,
            }
        elif "Event from the supervisor" in text:
            answer = {
                "spoken": "",
                "detail": "Nothing to add.",
                "needs_user": False,
                "urgency": "skip",
            }
        else:
            answer = {
                "spoken": "I am here.",
                "detail": "The recording fixture has no other scripted answer.",
                "needs_user": False,
            }
        self.send_json(completion(json.dumps(answer)))


if __name__ == "__main__":
    port = int(sys.argv[1])
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
