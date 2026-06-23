#!/usr/bin/env python3
"""noti broker daemon.

The single long-lived process that:
  * owns the Telegram bot token and is the ONLY getUpdates consumer,
  * exposes a loopback-only HTTP API for the MCP server / hooks / CLI,
  * maintains a thread-safe ticket registry for human-in-the-loop asks,
  * persists its getUpdates offset and a singleton lockfile under
    CLAUDE_PLUGIN_DATA so it survives plugin updates and refuses double-start.

Pure Python 3.9+ stdlib. Channel I/O lives in noti_channels.py (same dir).

Run directly:  python3 broker.py
Test mode:     NOTI_TEST=1 python3 broker.py   (no real network; /test/inject)
"""

import atexit
import json
import os
import signal
import sys
import threading
import time
import uuid
from fnmatch import fnmatch
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# Make the sibling noti_channels.py importable regardless of cwd. The module is
# named noti_channels (not "channels") to avoid colliding with the popular
# Django "channels" package on sys.path.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import noti_channels as channels  # noqa: E402  # pyright: ignore[reportMissingImports]

VERSION = "1.0.0"

DEFAULT_HOST = "127.0.0.1"
DEFAULT_PORT = 7432

# Long-poll / wait bounds (seconds).
MAX_WAIT = 55          # never exceed Claude's ~60s MCP tool timeout
DEFAULT_WAIT = 50
GETUPDATES_TIMEOUT = 25
TICKET_TTL = 30 * 60   # reap tickets older than 30 minutes
REAPER_INTERVAL = 60


def log(msg):
    sys.stderr.write("[broker %s] %s\n" % (time.strftime("%H:%M:%S"), msg))
    sys.stderr.flush()


def test_mode():
    return os.environ.get("NOTI_TEST") == "1"


# ---------------------------------------------------------------------------
# Data / config locations
# ---------------------------------------------------------------------------

def data_dir():
    """Directory for runtime state (offset, lock, log). Survives updates."""
    d = os.environ.get("CLAUDE_PLUGIN_DATA")
    if not d:
        d = os.path.expanduser("~/.local/state/noti")
    os.makedirs(d, exist_ok=True)
    return d


def config_path():
    p = os.environ.get("NOTI_CONFIG")
    if not p:
        p = os.path.expanduser("~/.config/noti/config.json")
    return p


def load_config():
    """Load config.json. Returns a dict; missing file yields safe defaults."""
    path = config_path()
    if not os.path.isfile(path):
        log("config not found at %s (using defaults)" % path)
        return _default_config()
    try:
        with open(path, "r", encoding="utf-8") as fh:
            cfg = json.load(fh)
    except (OSError, ValueError) as exc:
        log("failed to read config %s: %s (using defaults)" % (path, exc))
        return _default_config()
    if not isinstance(cfg, dict):
        log("config %s is not a JSON object (using defaults)" % path)
        return _default_config()
    # Fill in missing sections so callers can index safely.
    cfg.setdefault("telegram", {})
    cfg.setdefault("channels", {})
    cfg.setdefault("routing", [])
    cfg.setdefault("broker", {})
    return cfg


def _default_config():
    return {
        "telegram": {},
        "channels": {},
        "routing": [],
        "broker": {},
    }


# ---------------------------------------------------------------------------
# Ticket registry (thread-safe)
# ---------------------------------------------------------------------------

class Ticket:
    __slots__ = ("event", "answer", "chat_id", "message_id", "options", "created")

    def __init__(self, chat_id, options):
        self.event = threading.Event()
        self.answer = None
        self.chat_id = str(chat_id) if chat_id is not None else None
        self.message_id = None
        self.options = options or []
        self.created = time.time()


class Registry:
    def __init__(self):
        self._lock = threading.Lock()
        self._tickets = {}

    def create(self, chat_id, options):
        tid = uuid.uuid4().hex[:6]
        with self._lock:
            while tid in self._tickets:
                tid = uuid.uuid4().hex[:6]
            self._tickets[tid] = Ticket(chat_id, options)
        return tid

    def get(self, tid):
        with self._lock:
            return self._tickets.get(tid)

    def set_message_id(self, tid, message_id):
        with self._lock:
            t = self._tickets.get(tid)
            if t is not None:
                t.message_id = message_id

    def resolve(self, tid, answer):
        """Resolve a ticket by id. Returns True if it existed and was unset."""
        with self._lock:
            t = self._tickets.get(tid)
            if t is None or t.answer is not None:
                return False
            t.answer = answer
        t.event.set()
        return True

    def resolve_by_message_id(self, chat_id, message_id, answer):
        with self._lock:
            target = None
            for tid, t in self._tickets.items():
                if (t.message_id == message_id and t.answer is None
                        and str(t.chat_id) == str(chat_id)):
                    target = (tid, t)
                    break
            if target is None:
                return False
            target[1].answer = answer
        target[1].event.set()
        return True

    def pending_for_chat(self, chat_id):
        """Return list of (tid, ticket) that are pending for a given chat."""
        with self._lock:
            return [
                (tid, t) for tid, t in self._tickets.items()
                if t.answer is None and str(t.chat_id) == str(chat_id)
            ]

    def count_pending(self):
        with self._lock:
            return sum(1 for t in self._tickets.values() if t.answer is None)

    def reap(self, ttl):
        now = time.time()
        with self._lock:
            stale = [
                tid for tid, t in self._tickets.items()
                if now - t.created > ttl
            ]
            for tid in stale:
                # Unblock any waiter so it returns 'pending' rather than hang.
                self._tickets[tid].event.set()
                del self._tickets[tid]
        return len(stale)


# ---------------------------------------------------------------------------
# Routing
# ---------------------------------------------------------------------------

def resolve_target(cfg, *, channel=None, chat_id=None, project=None, cwd=None):
    """Resolve a {channel, chat_id} target.

    Precedence:
      1. Explicit channel (+optional chat_id) from the request.
      2. First matching routing rule (by project basename or path glob).
      3. Telegram default chat.
    """
    if channel:
        return {"channel": channel, "chat_id": chat_id}

    cwd = cwd or os.getcwd()
    project = project or os.path.basename(os.path.normpath(cwd))

    for rule in cfg.get("routing", []):
        if not isinstance(rule, dict):
            continue
        match = rule.get("match", "")
        mtype = rule.get("match_type", "project")
        hit = False
        if mtype == "project":
            hit = (match == project)
        elif mtype == "path_glob":
            hit = fnmatch(cwd, match)
        if hit:
            return {
                "channel": rule.get("channel", "telegram"),
                "chat_id": rule.get("chat_id"),
            }

    if chat_id:
        return {"channel": "telegram", "chat_id": chat_id}
    return {
        "channel": "telegram",
        "chat_id": cfg.get("telegram", {}).get("default_chat_id"),
    }


def allowed_chat_ids(cfg):
    """Set of chat ids the broker accepts inbound updates from (security)."""
    ids = set()
    default = cfg.get("telegram", {}).get("default_chat_id")
    if default:
        ids.add(str(default))
    for rule in cfg.get("routing", []):
        if isinstance(rule, dict) and rule.get("chat_id"):
            ids.add(str(rule["chat_id"]))
    return ids


# ---------------------------------------------------------------------------
# The broker state object shared by HTTP handler + poll thread
# ---------------------------------------------------------------------------

class Broker:
    def __init__(self):
        self.cfg = load_config()
        self.registry = Registry()
        self.data_dir = data_dir()
        self.offset_path = os.path.join(self.data_dir, "getUpdates.offset")
        self.telegram_connected = False
        self._stop = threading.Event()

    # ---- offset persistence ----
    def load_offset(self):
        try:
            with open(self.offset_path, "r", encoding="utf-8") as fh:
                return int(fh.read().strip())
        except (OSError, ValueError):
            return None

    def save_offset(self, offset):
        try:
            tmp = self.offset_path + ".tmp"
            with open(tmp, "w", encoding="utf-8") as fh:
                fh.write(str(offset))
            os.replace(tmp, self.offset_path)
        except OSError as exc:
            log("could not persist offset: %s" % exc)

    def token(self):
        return self.cfg.get("telegram", {}).get("bot_token", "")

    def stop(self):
        self._stop.set()

    def stopped(self):
        return self._stop.is_set()

    # ---- sending ----
    def send_notify(self, text, channel=None, chat_id=None,
                    project=None):
        target = resolve_target(
            self.cfg, channel=channel, chat_id=chat_id, project=project
        )
        sent_channels = []
        try:
            sent = channels.send_text(self.cfg, target, text)
            sent_channels.append(sent)
        except channels.ChannelError as exc:
            log("notify send failed (%s): %s" % (target.get("channel"), exc))
        return {"sent": bool(sent_channels), "channels": sent_channels}

    def send_file(self, path, caption=None, channel=None, chat_id=None,
                  project=None):
        if not path or not os.path.isfile(path):
            return {"sent": False, "channels": [], "error": "file not found"}
        if not os.access(path, os.R_OK):
            return {"sent": False, "channels": [], "error": "file not readable"}
        target = resolve_target(
            self.cfg, channel=channel, chat_id=chat_id, project=project
        )
        sent_channels = []
        try:
            sent = channels.send_file(self.cfg, target, path, caption)
            sent_channels.append(sent)
        except channels.ChannelError as exc:
            log("send_file failed (%s): %s" % (target.get("channel"), exc))
            return {"sent": False, "channels": [], "error": str(exc)}
        return {"sent": bool(sent_channels), "channels": sent_channels}

    # ---- ask ----
    def create_ask(self, question, options, project=None, chat_id=None):
        target = resolve_target(self.cfg, chat_id=chat_id, project=project)
        resolved_chat = target.get("chat_id")
        tid = self.registry.create(resolved_chat, options)
        text = "#%s %s" % (tid, question)
        reply_markup = None
        if options:
            keyboard = [
                [{"text": opt, "callback_data": "noti:%s:%d" % (tid, idx)}]
                for idx, opt in enumerate(options)
            ]
            reply_markup = {"inline_keyboard": keyboard}
        else:
            text += "\n\n(Reply to this message with your answer.)"

        # Only Telegram supports interactive replies; for other channels we
        # still send the question but cannot collect a typed reply.
        try:
            if target.get("channel", "telegram") == "telegram":
                result = channels.tg_send_message(
                    self.token(), resolved_chat, text, reply_markup
                )
                msg = result.get("result") or {}
                mid = msg.get("message_id")
                if mid is not None:
                    self.registry.set_message_id(tid, mid)
            else:
                channels.send_text(self.cfg, target, text)
        except channels.ChannelError as exc:
            log("ask send failed: %s" % exc)
            # Ticket still exists; caller may inject via test or it will time out.
        return tid

    def wait_ticket(self, tid, timeout):
        t = self.registry.get(tid)
        if t is None:
            return {"status": "unknown_ticket"}
        bounded = max(0, min(timeout, MAX_WAIT))
        t.event.wait(bounded)
        if t.answer is not None:
            return {"status": "answered", "answer": t.answer}
        return {"status": "pending"}


# ---------------------------------------------------------------------------
# HTTP handler
# ---------------------------------------------------------------------------

class Handler(BaseHTTPRequestHandler):
    # Injected by the server factory (make_server) before serving; declared as a
    # non-optional annotation so callers/type-checkers treat it as a live Broker.
    broker: "Broker"

    protocol_version = "HTTP/1.1"

    def log_message(self, format, *args):  # silence default stderr access log
        return

    # ---- helpers ----
    def _send_json(self, status, obj):
        body = json.dumps(obj).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def _read_json(self):
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0:
            return {}
        raw = self.rfile.read(length)
        try:
            obj = json.loads(raw.decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            raise _BadRequest("invalid JSON body")
        if not isinstance(obj, dict):
            raise _BadRequest("JSON body must be an object")
        return obj

    # ---- routing ----
    def do_GET(self):
        try:
            if self.path == "/health":
                self._handle_health()
            else:
                self._send_json(404, {"error": "not found"})
        except _BadRequest as exc:
            self._send_json(400, {"error": str(exc)})
        except Exception as exc:  # never crash the thread
            log("GET %s handler error: %s" % (self.path, exc))
            self._send_json(500, {"error": "internal error"})

    def do_POST(self):
        try:
            if self.path == "/notify":
                self._handle_notify()
            elif self.path == "/ask":
                self._handle_ask()
            elif self.path == "/wait":
                self._handle_wait()
            elif self.path == "/send_file":
                self._handle_send_file()
            elif self.path == "/test/inject" and test_mode():
                self._handle_inject()
            else:
                self._send_json(404, {"error": "not found"})
        except _BadRequest as exc:
            self._send_json(400, {"error": str(exc)})
        except Exception as exc:  # never crash the thread
            log("POST %s handler error: %s" % (self.path, exc))
            self._send_json(500, {"error": "internal error"})

    # ---- handlers ----
    def _handle_health(self):
        self._send_json(200, {
            "status": "ok",
            "version": VERSION,
            "telegram_connected": bool(self.broker.telegram_connected),
            "pending": self.broker.registry.count_pending(),
        })

    def _handle_notify(self):
        body = self._read_json()
        text = body.get("text")
        if not isinstance(text, str) or not text:
            raise _BadRequest("'text' is required")
        # 'level' is accepted in the request body for forward-compat but the
        # broker does not format on it (notify.sh builds the display text).
        result = self.broker.send_notify(
            text,
            channel=body.get("channel"),
            chat_id=body.get("chat_id"),
            project=body.get("project"),
        )
        self._send_json(200, result)

    def _handle_ask(self):
        body = self._read_json()
        question = body.get("question")
        if not isinstance(question, str) or not question:
            raise _BadRequest("'question' is required")
        options = body.get("options") or []
        if options and not isinstance(options, list):
            raise _BadRequest("'options' must be a list")
        timeout = body.get("timeout")
        try:
            timeout = int(timeout) if timeout is not None else DEFAULT_WAIT
        except (TypeError, ValueError):
            timeout = DEFAULT_WAIT
        tid = self.broker.create_ask(
            question, options,
            project=body.get("project"),
            chat_id=body.get("chat_id"),
        )
        result = self.broker.wait_ticket(tid, timeout)
        if result.get("status") == "answered":
            self._send_json(200, {
                "status": "answered", "ticket": tid, "answer": result["answer"],
            })
        else:
            self._send_json(200, {"status": "pending", "ticket": tid})

    def _handle_wait(self):
        body = self._read_json()
        tid = body.get("ticket")
        if not isinstance(tid, str) or not tid:
            raise _BadRequest("'ticket' is required")
        timeout = body.get("timeout")
        try:
            timeout = int(timeout) if timeout is not None else DEFAULT_WAIT
        except (TypeError, ValueError):
            timeout = DEFAULT_WAIT
        self._send_json(200, self.broker.wait_ticket(tid, timeout))

    def _handle_send_file(self):
        body = self._read_json()
        path = body.get("path")
        if not isinstance(path, str) or not path:
            raise _BadRequest("'path' is required")
        result = self.broker.send_file(
            path,
            caption=body.get("caption"),
            channel=body.get("channel"),
            chat_id=body.get("chat_id"),
            project=body.get("project"),
        )
        self._send_json(200, result)

    def _handle_inject(self):
        body = self._read_json()
        text = body.get("text")
        if not isinstance(text, str):
            raise _BadRequest("'text' is required")
        tid = body.get("ticket")
        chat_id = body.get("chat_id")
        reg = self.broker.registry
        if tid:
            ok = reg.resolve(tid, text)
            self._send_json(200, {"ok": bool(ok)})
            return
        if chat_id:
            pending = reg.pending_for_chat(chat_id)
            if len(pending) == 1:
                ok = reg.resolve(pending[0][0], text)
                self._send_json(200, {"ok": bool(ok)})
                return
            self._send_json(200, {"ok": False, "error":
                                  "expected exactly one pending ticket"})
            return
        # No ticket/chat given: resolve the single global pending ticket.
        pending = []
        with reg._lock:  # noqa: SLF001 (test helper only)
            pending = [tid for tid, t in reg._tickets.items()
                       if t.answer is None]
        if len(pending) == 1:
            ok = reg.resolve(pending[0], text)
            self._send_json(200, {"ok": bool(ok)})
            return
        self._send_json(200, {"ok": False,
                              "error": "expected exactly one pending ticket"})


class _BadRequest(Exception):
    pass


# ---------------------------------------------------------------------------
# Telegram poll thread
# ---------------------------------------------------------------------------

def poll_loop(broker):
    """Single getUpdates consumer. Never dies; backs off on errors."""
    if test_mode():
        log("NOTI_TEST=1: telegram poll thread disabled")
        return
    token = broker.token()
    if not token:
        log("no telegram bot_token configured; poll thread idle")
        return

    offset = broker.load_offset()
    backoff = 1
    allowed = allowed_chat_ids(broker.cfg)

    while not broker.stopped():
        try:
            result = channels.tg_get_updates(token, offset, GETUPDATES_TIMEOUT)
            broker.telegram_connected = True
            backoff = 1
            for update in result.get("result", []):
                try:
                    _process_update(broker, update, allowed)
                except Exception as exc:  # one bad update must not kill loop
                    log("error processing update: %s" % exc)
                upd_id = update.get("update_id")
                if upd_id is not None:
                    offset = upd_id + 1
                    broker.save_offset(offset)
        except channels.ChannelError as exc:
            broker.telegram_connected = False
            status = getattr(exc, "http_status", None)
            if status == 409:
                log("409 Conflict: another getUpdates consumer for this token "
                    "— is a second broker running? Backing off.")
            else:
                log("getUpdates error: %s" % exc)
            _sleep_backoff(broker, backoff)
            backoff = min(backoff * 2, 60)
        except Exception as exc:  # truly unexpected; keep the thread alive
            broker.telegram_connected = False
            log("unexpected poll error: %s" % exc)
            _sleep_backoff(broker, backoff)
            backoff = min(backoff * 2, 60)


def _sleep_backoff(broker, seconds):
    # Interruptible sleep so shutdown is prompt.
    broker._stop.wait(seconds)


def _process_update(broker, update, allowed):
    reg = broker.registry
    token = broker.token()

    cb = update.get("callback_query")
    if cb:
        from_chat = (cb.get("message") or {}).get("chat", {}).get("id")
        if allowed and from_chat is not None and str(from_chat) not in allowed:
            log("ignoring callback from non-allowed chat %s" % from_chat)
            return
        data = cb.get("data", "")
        cb_id = cb.get("id")
        if data.startswith("noti:"):
            parts = data.split(":")
            if len(parts) == 3:
                tid, idx_s = parts[1], parts[2]
                t = reg.get(tid)
                if t is not None:
                    try:
                        idx = int(idx_s)
                        if 0 <= idx < len(t.options):
                            answer = t.options[idx]
                            reg.resolve(tid, answer)
                            _safe(lambda: channels.tg_answer_callback(token, cb_id))
                            msg = cb.get("message") or {}
                            mid = msg.get("message_id")
                            chat = msg.get("chat", {}).get("id")
                            if mid is not None and chat is not None:
                                _safe(lambda: channels.tg_edit_message_text(
                                    token, chat, mid,
                                    "#%s -> %s" % (tid, answer)))
                            return
                    except (ValueError, IndexError):
                        pass
        # Unknown callback: still clear the spinner.
        if cb_id:
            _safe(lambda: channels.tg_answer_callback(token, cb_id))
        return

    message = update.get("message")
    if not message:
        return
    chat_id = message.get("chat", {}).get("id")
    if allowed and chat_id is not None and str(chat_id) not in allowed:
        log("ignoring message from non-allowed chat %s" % chat_id)
        return
    text = message.get("text")
    if text is None:
        return

    reply_to = message.get("reply_to_message")
    if reply_to:
        ref_mid = reply_to.get("message_id")
        if ref_mid is not None and reg.resolve_by_message_id(
                chat_id, ref_mid, text):
            return

    pending = reg.pending_for_chat(chat_id)
    if len(pending) == 1:
        reg.resolve(pending[0][0], text)
        return
    if len(pending) > 1:
        log("multiple pending tickets for chat %s; ignoring untargeted reply"
            % chat_id)


def _safe(fn):
    try:
        return fn()
    except channels.ChannelError as exc:
        log("telegram call failed: %s" % exc)
        return None


# ---------------------------------------------------------------------------
# Reaper thread
# ---------------------------------------------------------------------------

def reaper_loop(broker):
    while not broker.stopped():
        broker._stop.wait(REAPER_INTERVAL)
        if broker.stopped():
            break
        try:
            n = broker.registry.reap(TICKET_TTL)
            if n:
                log("reaped %d stale ticket(s)" % n)
        except Exception as exc:
            log("reaper error: %s" % exc)


# ---------------------------------------------------------------------------
# Lockfile / singleton
# ---------------------------------------------------------------------------

def lock_path():
    return os.path.join(data_dir(), "broker.lock")


def _pid_alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True  # exists, owned by someone else
    except OSError:
        return False
    return True


def acquire_lock():
    """Acquire the singleton lock. Returns True on success, False if a live
    broker already holds it."""
    path = lock_path()
    if os.path.isfile(path):
        try:
            with open(path, "r", encoding="utf-8") as fh:
                existing = int(fh.read().strip())
            if _pid_alive(existing):
                log("another broker is already running (pid %d); refusing to "
                    "start. Lock: %s" % (existing, path))
                return False
            log("stale lock from pid %d; reclaiming" % existing)
        except (OSError, ValueError):
            log("unreadable lock file; reclaiming")
    try:
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(str(os.getpid()))
    except OSError as exc:
        log("could not write lock file %s: %s" % (path, exc))
        return False
    return True


def release_lock():
    path = lock_path()
    try:
        if os.path.isfile(path):
            with open(path, "r", encoding="utf-8") as fh:
                pid = int(fh.read().strip())
            if pid == os.getpid():
                os.remove(path)
    except (OSError, ValueError):
        pass


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def make_server(broker):
    host = broker.cfg.get("broker", {}).get("host", DEFAULT_HOST)
    port = int(broker.cfg.get("broker", {}).get("port", DEFAULT_PORT))

    # Bind to an ephemeral port when requested (tests set port 0).
    handler_cls = type("BoundHandler", (Handler,), {"broker": broker})
    httpd = ThreadingHTTPServer((host, port), handler_cls)
    httpd.daemon_threads = True
    return httpd


def run():
    broker = Broker()

    if not acquire_lock():
        sys.exit(1)
    atexit.register(release_lock)

    def _term(signum, _frame):
        log("received signal %d; shutting down" % signum)
        broker.stop()
        release_lock()
        # Raising SystemExit lets atexit/finally run.
        raise SystemExit(0)

    signal.signal(signal.SIGTERM, _term)
    try:
        signal.signal(signal.SIGINT, _term)
    except ValueError:
        pass  # not on main thread (shouldn't happen here)

    try:
        httpd = make_server(broker)
    except OSError as exc:
        log("could not bind broker socket: %s" % exc)
        release_lock()
        sys.exit(1)

    host, port = httpd.server_address[0], httpd.server_address[1]
    log("broker %s listening on %s:%d (test_mode=%s)"
        % (VERSION, host, port, test_mode()))

    poll_thread = threading.Thread(
        target=poll_loop, args=(broker,), name="tg-poll", daemon=True
    )
    reaper_thread = threading.Thread(
        target=reaper_loop, args=(broker,), name="reaper", daemon=True
    )
    poll_thread.start()
    reaper_thread.start()

    try:
        httpd.serve_forever(poll_interval=0.5)
    except (KeyboardInterrupt, SystemExit):
        pass
    finally:
        broker.stop()
        try:
            httpd.shutdown()
        except Exception:
            pass
        try:
            httpd.server_close()
        except Exception:
            pass
        release_lock()
        log("broker stopped")


if __name__ == "__main__":
    run()
