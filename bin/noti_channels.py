#!/usr/bin/env python3
"""Channel adapters for noti (Telegram / Discord / Slack).

Pure stdlib (urllib). No third-party deps. Telegram messages are sent as PLAIN
TEXT (no parse_mode) because MarkdownV2 is fragile. All network functions honor
the NOTI_TEST=1 environment variable: when set, they record the call and return
a synthetic success instead of hitting the network.

Errors are surfaced as ChannelError (never silently swallowed). Callers are
responsible for logging / degrading gracefully.
"""

import io
import json
import os
import urllib.error
import urllib.request
import uuid

TELEGRAM_API = "https://api.telegram.org"

# Timeouts (seconds)
SEND_TIMEOUT = 15
LONGPOLL_TIMEOUT_PAD = 10  # added on top of the long-poll's own timeout

IMAGE_EXTS = {".png", ".jpg", ".jpeg", ".gif", ".webp"}

# When NOTI_TEST=1, every send is appended here so tests can inspect what would
# have been transmitted.
TEST_OUTBOX = []


class ChannelError(Exception):
    """Typed error raised when a channel operation fails.

    ``http_status`` carries the originating HTTP status (e.g. 409) so the poll
    thread can distinguish a Telegram getUpdates conflict from other failures.
    """

    def __init__(self, message, http_status=None):
        super().__init__(message)
        self.http_status = http_status


def _test_mode():
    return os.environ.get("NOTI_TEST") == "1"


def _record(kind, payload):
    """Record a no-op send in test mode and return a synthetic success dict."""
    entry = {"kind": kind, "payload": payload}
    TEST_OUTBOX.append(entry)
    return {"ok": True, "test_noop": True, "recorded": entry}


# ---------------------------------------------------------------------------
# Low-level HTTP helpers
# ---------------------------------------------------------------------------

def _http_post_json(url, obj, timeout):
    data = json.dumps(obj).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=data,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read()
            return resp.status, body
    except urllib.error.HTTPError as exc:
        body = b""
        try:
            body = exc.read()
        except Exception:
            pass
        return exc.code, body
    except urllib.error.URLError as exc:
        raise ChannelError("network error POSTing to %s: %s" % (url, exc.reason))
    except OSError as exc:
        raise ChannelError("socket error POSTing to %s: %s" % (url, exc))


def _multipart_post(url, fields, file_field, filename, file_bytes, timeout):
    """POST a multipart/form-data body containing one file plus text fields."""
    boundary = "----notiboundary%s" % uuid.uuid4().hex
    crlf = b"\r\n"
    buf = io.BytesIO()
    for name, value in fields.items():
        if value is None:
            continue
        buf.write(b"--" + boundary.encode() + crlf)
        buf.write(
            ('Content-Disposition: form-data; name="%s"' % name).encode("utf-8")
            + crlf + crlf
        )
        buf.write(str(value).encode("utf-8") + crlf)
    buf.write(b"--" + boundary.encode() + crlf)
    buf.write(
        ('Content-Disposition: form-data; name="%s"; filename="%s"'
         % (file_field, filename)).encode("utf-8") + crlf
    )
    buf.write(b"Content-Type: application/octet-stream" + crlf + crlf)
    buf.write(file_bytes + crlf)
    buf.write(b"--" + boundary.encode() + b"--" + crlf)
    body = buf.getvalue()
    req = urllib.request.Request(
        url,
        data=body,
        headers={
            "Content-Type": "multipart/form-data; boundary=%s" % boundary,
            "Content-Length": str(len(body)),
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as exc:
        b = b""
        try:
            b = exc.read()
        except Exception:
            pass
        return exc.code, b
    except urllib.error.URLError as exc:
        raise ChannelError("network error (multipart) to %s: %s" % (url, exc.reason))
    except OSError as exc:
        raise ChannelError("socket error (multipart) to %s: %s" % (url, exc))


def _parse_tg(status, body):
    """Parse a Telegram API response, raising ChannelError on failure."""
    try:
        parsed = json.loads(body.decode("utf-8")) if body else {}
    except (ValueError, UnicodeDecodeError):
        raise ChannelError("telegram returned non-JSON (HTTP %s)" % status)
    if status == 409:
        # Surface 409 distinctly so the poll thread can back off.
        err = ChannelError(
            "telegram 409 Conflict: %s" % parsed.get("description", "conflict")
        )
        err.http_status = 409
        raise err
    if not parsed.get("ok", False):
        err = ChannelError(
            "telegram API error (HTTP %s): %s"
            % (status, parsed.get("description", "unknown"))
        )
        err.http_status = status
        raise err
    return parsed


# ---------------------------------------------------------------------------
# Telegram-specific
# ---------------------------------------------------------------------------

def tg_send_message(token, chat_id, text, reply_markup=None):
    """Send a plain-text Telegram message. Returns the parsed result dict."""
    if not token:
        raise ChannelError("telegram bot_token is empty")
    if not chat_id:
        raise ChannelError("telegram chat_id is empty")
    payload = {"chat_id": str(chat_id), "text": text}
    if reply_markup is not None:
        payload["reply_markup"] = reply_markup
    if _test_mode():
        return _record("tg_send_message", payload)
    url = "%s/bot%s/sendMessage" % (TELEGRAM_API, token)
    status, body = _http_post_json(url, payload, SEND_TIMEOUT)
    return _parse_tg(status, body)


def tg_get_updates(token, offset, timeout):
    """Long-poll getUpdates. Returns the parsed result dict (with 'result' list).

    Note: in NOTI_TEST mode this is never called (the broker disables the poll
    thread), but we still guard it for safety.
    """
    if not token:
        raise ChannelError("telegram bot_token is empty")
    if _test_mode():
        return {"ok": True, "result": []}
    payload = {
        "timeout": timeout,
        "allowed_updates": ["message", "callback_query"],
    }
    if offset is not None:
        payload["offset"] = offset
    url = "%s/bot%s/getUpdates" % (TELEGRAM_API, token)
    # The socket timeout must exceed the long-poll timeout, or urlopen aborts
    # the connection before Telegram replies.
    status, body = _http_post_json(url, payload, timeout + LONGPOLL_TIMEOUT_PAD)
    return _parse_tg(status, body)


def tg_answer_callback(token, cb_id, text=None):
    """Answer a callback_query to clear the inline-button spinner."""
    if not token:
        raise ChannelError("telegram bot_token is empty")
    payload = {"callback_query_id": cb_id}
    if text:
        payload["text"] = text
    if _test_mode():
        return _record("tg_answer_callback", payload)
    url = "%s/bot%s/answerCallbackQuery" % (TELEGRAM_API, token)
    status, body = _http_post_json(url, payload, SEND_TIMEOUT)
    return _parse_tg(status, body)


def tg_edit_message_text(token, chat_id, message_id, text):
    """Best-effort edit of a previously-sent message (e.g. show chosen answer)."""
    if not token:
        raise ChannelError("telegram bot_token is empty")
    payload = {"chat_id": str(chat_id), "message_id": message_id, "text": text}
    if _test_mode():
        return _record("tg_edit_message_text", payload)
    url = "%s/bot%s/editMessageText" % (TELEGRAM_API, token)
    status, body = _http_post_json(url, payload, SEND_TIMEOUT)
    return _parse_tg(status, body)


def tg_send_document(token, chat_id, path, caption=None):
    """Send a file as a document via multipart sendDocument."""
    if not token:
        raise ChannelError("telegram bot_token is empty")
    if _test_mode():
        return _record("tg_send_document",
                       {"chat_id": str(chat_id), "path": path, "caption": caption})
    file_bytes, filename = _read_file(path)
    fields = {"chat_id": str(chat_id)}
    if caption:
        fields["caption"] = caption
    url = "%s/bot%s/sendDocument" % (TELEGRAM_API, token)
    status, body = _multipart_post(
        url, fields, "document", filename, file_bytes, SEND_TIMEOUT
    )
    return _parse_tg(status, body)


def tg_send_photo(token, chat_id, path, caption=None):
    """Send an image via multipart sendPhoto."""
    if not token:
        raise ChannelError("telegram bot_token is empty")
    if _test_mode():
        return _record("tg_send_photo",
                       {"chat_id": str(chat_id), "path": path, "caption": caption})
    file_bytes, filename = _read_file(path)
    fields = {"chat_id": str(chat_id)}
    if caption:
        fields["caption"] = caption
    url = "%s/bot%s/sendPhoto" % (TELEGRAM_API, token)
    status, body = _multipart_post(
        url, fields, "photo", filename, file_bytes, SEND_TIMEOUT
    )
    return _parse_tg(status, body)


def _read_file(path):
    if not path or not os.path.isfile(path):
        raise ChannelError("file does not exist: %s" % path)
    if not os.access(path, os.R_OK):
        raise ChannelError("file is not readable: %s" % path)
    try:
        with open(path, "rb") as fh:
            return fh.read(), os.path.basename(path)
    except OSError as exc:
        raise ChannelError("could not read file %s: %s" % (path, exc))


# ---------------------------------------------------------------------------
# Discord / Slack incoming webhooks
# ---------------------------------------------------------------------------

def discord_send_text(webhook, text):
    if not webhook:
        raise ChannelError("discord webhook is empty")
    payload = {"content": text}
    if _test_mode():
        return _record("discord_send_text", {"webhook": webhook, **payload})
    status, _ = _http_post_json(webhook, payload, SEND_TIMEOUT)
    if status not in (200, 204):
        raise ChannelError("discord webhook failed (HTTP %s)" % status)
    return {"ok": True}


def discord_send_file(webhook, path, caption=None):
    if not webhook:
        raise ChannelError("discord webhook is empty")
    if _test_mode():
        return _record("discord_send_file",
                       {"webhook": webhook, "path": path, "caption": caption})
    file_bytes, filename = _read_file(path)
    fields = {}
    if caption:
        fields["content"] = caption
    status, _ = _multipart_post(
        webhook, fields, "file", filename, file_bytes, SEND_TIMEOUT
    )
    if status not in (200, 204):
        raise ChannelError("discord file upload failed (HTTP %s)" % status)
    return {"ok": True}


def slack_send_text(webhook, text):
    if not webhook:
        raise ChannelError("slack webhook is empty")
    payload = {"text": text}
    if _test_mode():
        return _record("slack_send_text", {"webhook": webhook, **payload})
    status, _ = _http_post_json(webhook, payload, SEND_TIMEOUT)
    if status != 200:
        raise ChannelError("slack webhook failed (HTTP %s)" % status)
    return {"ok": True}


# ---------------------------------------------------------------------------
# High-level dispatch (config + resolved target)
# ---------------------------------------------------------------------------
# A "target" dict resolved by the broker's routing logic looks like:
#   {"channel": "telegram"|"discord"|"slack", "chat_id": str|None}

def send_text(cfg, target, text):
    """Send text to a single resolved target. Returns the channel name sent on.

    Raises ChannelError on failure so the caller can decide how to degrade.
    """
    channel = target.get("channel", "telegram")
    if channel == "telegram":
        token = _tg_token(cfg)
        chat_id = target.get("chat_id") or _tg_default_chat(cfg)
        tg_send_message(token, chat_id, text)
        return "telegram"
    if channel == "discord":
        webhook = cfg.get("channels", {}).get("discord_webhook", "")
        discord_send_text(webhook, text)
        return "discord"
    if channel == "slack":
        webhook = cfg.get("channels", {}).get("slack_webhook", "")
        slack_send_text(webhook, text)
        return "slack"
    raise ChannelError("unknown channel: %s" % channel)


def send_file(cfg, target, path, caption=None):
    """Send a file to a single resolved target. Returns the channel name."""
    if not path or not os.path.isfile(path):
        raise ChannelError("file does not exist: %s" % path)
    channel = target.get("channel", "telegram")
    if channel == "telegram":
        token = _tg_token(cfg)
        chat_id = target.get("chat_id") or _tg_default_chat(cfg)
        ext = os.path.splitext(path)[1].lower()
        if ext in IMAGE_EXTS:
            tg_send_photo(token, chat_id, path, caption)
        else:
            tg_send_document(token, chat_id, path, caption)
        return "telegram"
    if channel == "discord":
        webhook = cfg.get("channels", {}).get("discord_webhook", "")
        discord_send_file(webhook, path, caption)
        return "discord"
    if channel == "slack":
        # Slack incoming webhooks cannot upload files; needs a bot token + the
        # files.upload API. Per contract: skip with a note.
        raise ChannelError(
            "slack file upload requires a bot token (not configured); skipped"
        )
    raise ChannelError("unknown channel: %s" % channel)


def _tg_token(cfg):
    return cfg.get("telegram", {}).get("bot_token", "")


def _tg_default_chat(cfg):
    return cfg.get("telegram", {}).get("default_chat_id", "")
