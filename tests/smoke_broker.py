#!/usr/bin/env python3
"""Offline smoke test for the noti broker (NOTI_TEST=1).

Starts the broker in test mode on an ephemeral port with a temp config and a
temp CLAUDE_PLUGIN_DATA, then:
  1. asserts GET /health is ok,
  2. starts a thread doing POST /ask (no options) and, once it is waiting,
     POST /test/inject with text -> asserts /ask returns answered with that text,
  3. asserts POST /notify returns sent:true (test-mode no-op success),
  4. tears everything down (terminate broker, remove lock).

No real network is used. Exit code 0 on success, non-zero on any failure.
"""

import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
BROKER = os.path.join(REPO, "bin", "broker.py")


def free_port():
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def http_get(url, timeout=5):
    with urllib.request.urlopen(url, timeout=timeout) as resp:
        return resp.status, json.loads(resp.read().decode("utf-8"))


def http_post(url, obj, timeout=60):
    data = json.dumps(obj).encode("utf-8")
    req = urllib.request.Request(
        url, data=data, headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.status, json.loads(resp.read().decode("utf-8"))


def wait_for_health(base, deadline):
    while time.time() < deadline:
        try:
            status, body = http_get(base + "/health", timeout=2)
            if status == 200 and body.get("status") == "ok":
                return body
        except (urllib.error.URLError, OSError):
            time.sleep(0.1)
    raise RuntimeError("broker did not become healthy in time")


def main():
    tmp = tempfile.mkdtemp(prefix="noti-smoke-")
    data_dir = os.path.join(tmp, "data")
    os.makedirs(data_dir, exist_ok=True)
    cfg_path = os.path.join(tmp, "config.json")
    port = free_port()

    config = {
        "telegram": {"bot_token": "TESTTOKEN", "default_chat_id": "12345"},
        "channels": {},
        "routing": [],
        "broker": {"host": "127.0.0.1", "port": port},
    }
    with open(cfg_path, "w", encoding="utf-8") as fh:
        json.dump(config, fh)

    env = dict(os.environ)
    env["NOTI_TEST"] = "1"
    env["NOTI_CONFIG"] = cfg_path
    env["CLAUDE_PLUGIN_DATA"] = data_dir

    base = "http://127.0.0.1:%d" % port
    proc = subprocess.Popen(
        [sys.executable, BROKER], env=env,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )

    failures = []
    try:
        health = wait_for_health(base, time.time() + 10)
        assert health.get("version") == "3.0.0", "unexpected version: %r" % health
        print("ok: /health -> %s" % health)

        # --- ask + inject ---
        ask_result = {}

        def do_ask():
            try:
                _, body = http_post(
                    base + "/ask",
                    {"question": "Proceed?", "timeout": 20},
                    timeout=30,
                )
                ask_result.update(body)
            except Exception as exc:  # noqa: BLE001
                ask_result["error"] = str(exc)

        t = threading.Thread(target=do_ask)
        t.start()

        # Wait until the ticket is registered (pending count goes to 1).
        injected = False
        deadline = time.time() + 8
        while time.time() < deadline:
            _, h = http_get(base + "/health", timeout=2)
            if h.get("pending", 0) >= 1:
                _, inj = http_post(
                    base + "/test/inject",
                    {"text": "yes do it"},
                    timeout=5,
                )
                assert inj.get("ok") is True, "inject not ok: %r" % inj
                injected = True
                break
            time.sleep(0.1)
        assert injected, "ticket never became pending; could not inject"

        t.join(timeout=30)
        assert ask_result.get("status") == "answered", \
            "ask did not return answered: %r" % ask_result
        assert ask_result.get("answer") == "yes do it", \
            "unexpected answer: %r" % ask_result
        print("ok: /ask answered via /test/inject -> %r" % ask_result)

        # --- notify ---
        _, notify_body = http_post(
            base + "/notify", {"text": "hello", "level": "info"}, timeout=10
        )
        assert notify_body.get("sent") is True, \
            "notify not sent: %r" % notify_body
        print("ok: /notify -> %r" % notify_body)

    except AssertionError as exc:
        failures.append(str(exc))
    except Exception as exc:  # noqa: BLE001
        failures.append("unexpected: %s" % exc)
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
        # Lockfile should be cleaned up by the broker; remove if it lingers.
        lock = os.path.join(data_dir, "broker.lock")
        if os.path.isfile(lock):
            os.remove(lock)
        shutil.rmtree(tmp, ignore_errors=True)

    if failures:
        for f in failures:
            sys.stderr.write("FAIL: %s\n" % f)
        # Dump broker stderr to aid debugging.
        try:
            err = proc.stderr.read().decode("utf-8", "replace") if proc.stderr else ""
            if err:
                sys.stderr.write("--- broker stderr ---\n%s\n" % err)
        except Exception:
            pass
        return 1

    print("smoke_broker: ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
