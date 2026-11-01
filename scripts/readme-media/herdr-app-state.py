#!/usr/bin/env python3
"""State-only proxy that presents the recording fixture's voice as ready."""

import http.client
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


DAEMON_PORT = int(sys.argv[1])
LISTEN_PORT = int(sys.argv[2])
HOP_HEADERS = {"connection", "content-length", "host", "keep-alive", "transfer-encoding"}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def log_message(self, _format, *_args):
        pass

    def forward(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length) if length else None
        headers = {
            key: value
            for key, value in self.headers.items()
            if key.lower() not in HOP_HEADERS
        }
        if body is not None:
            headers["Content-Length"] = str(len(body))
        conn = http.client.HTTPConnection("127.0.0.1", DAEMON_PORT, timeout=60)
        conn.request(self.command, self.path, body=body, headers=headers)
        response = conn.getresponse()
        if self.command == "GET" and self.path == "/api/state":
            payload = json.loads(response.read())
            voice = payload.get("voice")
            if isinstance(voice, dict):
                voice["mode"] = "gemini"
                voice["status"] = "ready"
                voice["reason"] = ""
                voice["setup_needed"] = False
                voice["problem"] = ""
            data = json.dumps(payload).encode()
            self.send_response(response.status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        else:
            self.send_response(response.status, response.reason)
            for key, value in response.getheaders():
                if key.lower() not in HOP_HEADERS:
                    self.send_header(key, value)
            self.end_headers()
            try:
                while chunk := response.read(4096):
                    self.wfile.write(chunk)
                    self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                pass
        conn.close()

    do_GET = forward
    do_POST = forward
    do_PUT = forward
    do_DELETE = forward


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", LISTEN_PORT), Handler).serve_forever()
