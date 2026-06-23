#!/usr/bin/env python3
"""Offline smoke test for the MCP stdio server (server/mcp_server.py).

Spins up a tiny stub HTTP broker that always answers /ask with
{"status":"answered","answer":"yes"} and /notify with {"sent":true}. Then it
spawns ``python3 server/mcp_server.py`` with NOTI_BROKER_URL pointing at the
stub and drives a real handshake over the child's stdin/stdout using
line-delimited JSON, matching responses by request id.

Asserts:
  - initialize result has serverInfo.name == "noti"
  - tools/list contains ask_user, wait_for_reply, notify, send_file, send_image
  - tools/call ask_user returns content text "yes" and isError is falsey

Exits 0 on success, non-zero (with a stderr message) on any failure.
Stdlib only.
"""

import json
import os
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import IO, NoReturn, cast

HERE = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.dirname(HERE)
MCP_SERVER = os.path.join(REPO_ROOT, "server", "mcp_server.py")


class StubBrokerHandler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):  # silence default access logging
        pass

    def _read_json(self):
        length = int(self.headers.get("Content-Length", 0) or 0)
        raw = self.rfile.read(length) if length else b""
        try:
            return json.loads(raw.decode("utf-8")) if raw else {}
        except Exception:
            return {}

    def _send(self, obj, code=200):
        body = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        self._read_json()
        if self.path == "/ask":
            self._send({"status": "answered", "answer": "yes"})
        elif self.path == "/notify":
            self._send({"sent": True, "channels": ["telegram"]})
        else:
            self._send({"error": "not found"}, code=404)


def start_stub():
    server = ThreadingHTTPServer(("127.0.0.1", 0), StubBrokerHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    port = server.server_address[1]
    return server, "http://127.0.0.1:%d" % port


def fail(msg) -> NoReturn:
    print("smoke_mcp FAIL: %s" % msg, file=sys.stderr)
    sys.exit(1)


def main():
    if not os.path.exists(MCP_SERVER):
        fail("mcp_server.py not found at %s" % MCP_SERVER)

    server, broker_url = start_stub()
    env = dict(os.environ)
    env["NOTI_BROKER_URL"] = broker_url

    proc = subprocess.Popen(
        [sys.executable, MCP_SERVER],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
        text=True,
        bufsize=1,
    )
    assert proc.stdin is not None and proc.stdout is not None and proc.stderr is not None
    p_in = cast("IO[str]", proc.stdin)
    p_out = cast("IO[str]", proc.stdout)
    p_err = cast("IO[str]", proc.stderr)

    responses = {}

    def send(obj):
        p_in.write(json.dumps(obj) + "\n")
        p_in.flush()

    def read_until_id(want_id, label):
        # Read lines until we see a response with the matching id.
        while True:
            line = p_out.readline()
            if line == "":
                stderr = p_err.read()
                fail("EOF waiting for %s response (id=%s); stderr:\n%s" % (label, want_id, stderr))
            line = line.strip()
            if not line:
                continue
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                fail("non-JSON line on stdout while waiting for %s: %r" % (label, line))
            if isinstance(msg, dict) and msg.get("id") == want_id:
                responses[want_id] = msg
                return msg

    try:
        # 1. initialize
        send({
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "smoke", "version": "0.0.0"},
            },
        })
        init = read_until_id(1, "initialize")
        result = init.get("result", {})
        server_info = result.get("serverInfo", {})
        if server_info.get("name") != "noti":
            fail("serverInfo.name != 'noti' (got %r)" % server_info.get("name"))

        # 2. notifications/initialized (no response expected)
        send({"jsonrpc": "2.0", "method": "notifications/initialized"})

        # 3. tools/list
        send({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
        listed = read_until_id(2, "tools/list")
        tools = listed.get("result", {}).get("tools", [])
        names = {t.get("name") for t in tools}
        expected = {"ask_user", "wait_for_reply", "notify", "send_file", "send_image"}
        missing = expected - names
        if missing:
            fail("tools/list missing tools: %s (got %s)" % (sorted(missing), sorted(names)))

        # 4. tools/call ask_user
        send({
            "jsonrpc": "2.0",
            "id": 3,
            "method": "tools/call",
            "params": {"name": "ask_user", "arguments": {"question": "Proceed?"}},
        })
        called = read_until_id(3, "tools/call ask_user")
        call_result = called.get("result", {})
        if call_result.get("isError"):
            fail("ask_user returned isError=true: %s" % json.dumps(call_result))
        content = call_result.get("content", [])
        text = content[0].get("text") if content else None
        if text != "yes":
            fail("ask_user content text != 'yes' (got %r)" % text)

    finally:
        try:
            proc.stdin.close()
        except Exception:
            pass
        try:
            proc.wait(timeout=10)
        except Exception:
            proc.kill()
        server.shutdown()
        server.server_close()

    print("smoke_mcp OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
