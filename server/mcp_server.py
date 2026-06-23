#!/usr/bin/env python3
"""Noti MCP server — hand-rolled JSON-RPC 2.0 over stdio (newline-delimited).

Protocol channel is STDOUT: every protocol message is a single-line JSON object
terminated by ``\\n`` and never contains an embedded newline. STDIN is read line
by line for incoming requests/notifications. ALL logging goes to STDERR only;
nothing but valid MCP messages is ever written to stdout.

The server is intentionally thin: it does NOT talk to Telegram. It forwards tool
calls to the local broker daemon over ``NOTI_BROKER_URL`` using urllib, with a
~55s timeout so each MCP tool call returns before Claude Code's ~60s tool-call
deadline. Connection failures degrade into actionable ``isError`` results.

Stdlib only. No pip dependencies.
"""

import json
import os
import sys
import urllib.error
import urllib.request

PROTOCOL_VERSION_DEFAULT = "2025-06-18"
SERVER_NAME = "noti"
SERVER_VERSION = "1.0.0"

# Each broker HTTP call must finish well under Claude Code's ~60s tool deadline.
BROKER_TIMEOUT = 55
# We ask the broker to block at most this long server-side for a reply.
ASK_WAIT_SECONDS = 50


def log(*args):
    """Write a diagnostic line to stderr (never stdout)."""
    try:
        print("[noti-mcp]", *args, file=sys.stderr, flush=True)
    except Exception:
        # Logging must never break the protocol loop.
        pass


def broker_url():
    return os.environ.get("NOTI_BROKER_URL", "http://127.0.0.1:7432").rstrip("/")


class BrokerDown(Exception):
    """Raised when the broker cannot be reached at all."""


def broker_post(path, payload, timeout=BROKER_TIMEOUT):
    """POST JSON to the broker and return the parsed JSON response.

    Raises BrokerDown when the connection is refused / the broker is absent.
    Raises urllib.error.URLError / json.JSONDecodeError for other failures.
    """
    url = broker_url() + path
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=data,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8")
    except urllib.error.HTTPError as exc:
        # Broker reachable but returned an error status; try to parse its body.
        try:
            body = exc.read().decode("utf-8")
            return json.loads(body)
        except Exception:
            raise
    except urllib.error.URLError as exc:
        reason = getattr(exc, "reason", exc)
        # Connection refused / no route → broker is down.
        if isinstance(reason, ConnectionRefusedError) or isinstance(reason, ConnectionError):
            raise BrokerDown(str(reason))
        if isinstance(reason, OSError) and getattr(reason, "errno", None) in (61, 111):
            raise BrokerDown(str(reason))
        raise BrokerDown(str(reason))
    except (ConnectionRefusedError, ConnectionError) as exc:
        raise BrokerDown(str(exc))
    return json.loads(body)


# --- Tool definitions ------------------------------------------------------

SETUP_HINT = (
    "The noti broker is not running, so I cannot reach the user's phone. "
    "Ask the user to run /noti:setup (or bin/install-broker.sh) to start the broker, "
    "then try again."
)

TOOLS = [
    {
        "name": "ask_user",
        "description": (
            "Ask the human a question and get their answer from their phone. Use this "
            "WHENEVER you need a decision, approval, or information you cannot determine "
            "yourself, INSTEAD of guessing or stopping. Prefer asking over assuming. "
            "Returns the user's answer, or a ticket id you can keep waiting on with "
            "wait_for_reply."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "question": {
                    "type": "string",
                    "description": "The question to ask the human.",
                },
                "options": {
                    "type": "array",
                    "items": {"type": "string"},
                    "description": "Optional list of choices shown as tappable buttons on the phone.",
                },
                "project": {
                    "type": "string",
                    "description": "Optional project name for per-project routing.",
                },
            },
            "required": ["question"],
        },
    },
    {
        "name": "wait_for_reply",
        "description": (
            "Continue waiting for the user's phone reply to a previous ask_user. Call "
            "repeatedly with the ticket id until you get an answer. Use this instead of "
            "giving up when ask_user returned a pending ticket."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "ticket": {
                    "type": "string",
                    "description": "The ticket id returned by a previous ask_user call.",
                },
            },
            "required": ["ticket"],
        },
    },
    {
        "name": "notify",
        "description": (
            "Proactively send a short status message to the user's phone. Use this to "
            "keep the user informed of progress or important events without expecting a reply."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "text": {"type": "string", "description": "The message text to send."},
                "level": {
                    "type": "string",
                    "enum": ["info", "done", "attention"],
                    "description": "Severity / category of the message.",
                },
                "project": {
                    "type": "string",
                    "description": "Optional project name for per-project routing.",
                },
            },
            "required": ["text"],
        },
    },
    {
        "name": "send_file",
        "description": (
            "Send a file from the local filesystem to the user's phone (as a document). "
            "Use for logs, reports, or any artifact the user should see on their phone."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "path": {"type": "string", "description": "Absolute path to the file to send."},
                "caption": {"type": "string", "description": "Optional caption for the file."},
                "project": {
                    "type": "string",
                    "description": "Optional project name for per-project routing.",
                },
            },
            "required": ["path"],
        },
    },
    {
        "name": "send_image",
        "description": (
            "Send an image from the local filesystem to the user's phone (as a photo). "
            "Use for screenshots, charts, or diagrams the user should view on their phone."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "path": {"type": "string", "description": "Absolute path to the image to send."},
                "caption": {"type": "string", "description": "Optional caption for the image."},
                "project": {
                    "type": "string",
                    "description": "Optional project name for per-project routing.",
                },
            },
            "required": ["path"],
        },
    },
]


# --- Tool handlers ---------------------------------------------------------

def tool_ask_user(args):
    question = args.get("question")
    if not isinstance(question, str) or not question.strip():
        return _err("ask_user requires a non-empty 'question' string.")
    payload = {"question": question, "timeout": ASK_WAIT_SECONDS}
    options = args.get("options")
    if isinstance(options, list) and options:
        payload["options"] = [str(o) for o in options]
    if args.get("project"):
        payload["project"] = str(args["project"])
    try:
        result = broker_post("/ask", payload)
    except BrokerDown:
        return _err(SETUP_HINT)
    except Exception as exc:  # noqa: BLE001 - surface broker errors to Claude
        log("ask_user broker error:", repr(exc))
        return _err("Failed to reach the noti broker: %s" % exc)

    status = result.get("status")
    if status == "answered":
        return _ok(str(result.get("answer", "")))
    if status == "pending":
        ticket = result.get("ticket", "")
        return _ok(
            'No reply yet. Call wait_for_reply with ticket="%s" to keep waiting '
            "(repeat until answered)." % ticket
        )
    return _err("Unexpected broker response to ask_user: %s" % json.dumps(result))


def tool_wait_for_reply(args):
    ticket = args.get("ticket")
    if not isinstance(ticket, str) or not ticket.strip():
        return _err("wait_for_reply requires a non-empty 'ticket' string.")
    try:
        result = broker_post("/wait", {"ticket": ticket, "timeout": ASK_WAIT_SECONDS})
    except BrokerDown:
        return _err(SETUP_HINT)
    except Exception as exc:  # noqa: BLE001
        log("wait_for_reply broker error:", repr(exc))
        return _err("Failed to reach the noti broker: %s" % exc)

    status = result.get("status")
    if status == "answered":
        return _ok(str(result.get("answer", "")))
    if status == "pending":
        return _ok("Still waiting, call wait_for_reply again with the same ticket.")
    if status == "unknown_ticket":
        return _err("Unknown ticket; the question may have expired. Ask again with ask_user.")
    return _err("Unexpected broker response to wait_for_reply: %s" % json.dumps(result))


def tool_notify(args):
    text = args.get("text")
    if not isinstance(text, str) or not text.strip():
        return _err("notify requires a non-empty 'text' string.")
    payload = {"text": text}
    if args.get("level"):
        payload["level"] = str(args["level"])
    if args.get("project"):
        payload["project"] = str(args["project"])
    try:
        result = broker_post("/notify", payload)
    except BrokerDown:
        return _err(SETUP_HINT)
    except Exception as exc:  # noqa: BLE001
        log("notify broker error:", repr(exc))
        return _err("Failed to reach the noti broker: %s" % exc)
    if result.get("sent"):
        channels = ", ".join(result.get("channels", [])) or "phone"
        return _ok("Notification sent via %s." % channels)
    return _err("Broker did not send the notification: %s" % json.dumps(result))


def _send_file_like(args, kind):
    path = args.get("path")
    if not isinstance(path, str) or not path.strip():
        return _err("%s requires a non-empty 'path' string." % kind)
    payload = {"path": path}
    if args.get("caption"):
        payload["caption"] = str(args["caption"])
    if args.get("project"):
        payload["project"] = str(args["project"])
    try:
        result = broker_post("/send_file", payload)
    except BrokerDown:
        return _err(SETUP_HINT)
    except Exception as exc:  # noqa: BLE001
        log("%s broker error:" % kind, repr(exc))
        return _err("Failed to reach the noti broker: %s" % exc)
    if result.get("sent"):
        channels = ", ".join(result.get("channels", [])) or "phone"
        return _ok("Sent %s via %s." % (kind, channels))
    return _err("Broker did not send the %s: %s" % (kind, json.dumps(result)))


def tool_send_file(args):
    return _send_file_like(args, "send_file")


def tool_send_image(args):
    return _send_file_like(args, "send_image")


TOOL_HANDLERS = {
    "ask_user": tool_ask_user,
    "wait_for_reply": tool_wait_for_reply,
    "notify": tool_notify,
    "send_file": tool_send_file,
    "send_image": tool_send_image,
}


def _ok(text):
    return {"content": [{"type": "text", "text": text}], "isError": False}


def _err(text):
    return {"content": [{"type": "text", "text": text}], "isError": True}


# --- JSON-RPC plumbing -----------------------------------------------------

def write_message(obj):
    """Write a single JSON-RPC message to stdout as one newline-terminated line."""
    line = json.dumps(obj, separators=(",", ":"))
    sys.stdout.write(line + "\n")
    sys.stdout.flush()


def make_result(req_id, result):
    return {"jsonrpc": "2.0", "id": req_id, "result": result}


def make_error(req_id, code, message):
    return {"jsonrpc": "2.0", "id": req_id, "error": {"code": code, "message": message}}


def handle_initialize(params):
    client_version = None
    if isinstance(params, dict):
        client_version = params.get("protocolVersion")
    protocol_version = client_version if isinstance(client_version, str) and client_version else PROTOCOL_VERSION_DEFAULT
    return {
        "protocolVersion": protocol_version,
        "capabilities": {"tools": {}},
        "serverInfo": {"name": SERVER_NAME, "version": SERVER_VERSION},
    }


def handle_tools_call(params):
    if not isinstance(params, dict):
        return _err("tools/call requires params with 'name' and 'arguments'.")
    name = params.get("name")
    if not isinstance(name, str):
        return _err("tools/call requires a string 'name'.")
    arguments = params.get("arguments") or {}
    if not isinstance(arguments, dict):
        arguments = {}
    handler = TOOL_HANDLERS.get(name)
    if handler is None:
        return _err("Unknown tool: %s" % name)
    try:
        return handler(arguments)
    except Exception as exc:  # noqa: BLE001 - never crash the loop on a tool bug
        log("tool handler crashed:", name, repr(exc))
        return _err("Internal error running tool %s: %s" % (name, exc))


def dispatch(message):
    """Process one parsed JSON-RPC message; return a response dict or None.

    Notifications (no ``id``) return None (no response is written).
    """
    if not isinstance(message, dict):
        return make_error(None, -32600, "Invalid Request")

    method = message.get("method")
    req_id = message.get("id")
    params = message.get("params")
    is_notification = "id" not in message

    if method == "initialize":
        return make_result(req_id, handle_initialize(params))

    if method == "notifications/initialized":
        return None  # notification → no response

    if method == "tools/list":
        return make_result(req_id, {"tools": TOOLS})

    if method == "tools/call":
        return make_result(req_id, handle_tools_call(params))

    if method == "ping":
        return make_result(req_id, {})

    # Other notifications we don't handle: ignore silently.
    if is_notification:
        log("ignoring unknown notification:", method)
        return None

    return make_error(req_id, -32601, "Method not found")


def main():
    log("starting; broker =", broker_url())
    stdin = sys.stdin
    while True:
        line = stdin.readline()
        if line == "":
            break  # EOF
        line = line.strip()
        if not line:
            continue
        try:
            message = json.loads(line)
        except json.JSONDecodeError as exc:
            log("parse error:", repr(exc))
            write_message(make_error(None, -32700, "Parse error"))
            continue
        try:
            response = dispatch(message)
        except Exception as exc:  # noqa: BLE001 - keep the loop alive
            log("dispatch crashed:", repr(exc))
            req_id = message.get("id") if isinstance(message, dict) else None
            write_message(make_error(req_id, -32603, "Internal error"))
            continue
        if response is not None:
            write_message(response)
    log("stdin EOF; exiting")
    return 0


if __name__ == "__main__":
    sys.exit(main())
