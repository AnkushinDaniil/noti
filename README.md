# noti — Phone Notifications for Claude Code

Get a Telegram (or Discord/Slack) notification on your phone when Claude Code
pauses for a permission prompt or finishes a task. Answer Claude's questions
directly from your phone via the `ask_user` MCP tool.

## Prerequisites

- **python3** (3.9+) — broker daemon and MCP server
- **curl** — hook notifications and setup
- **jq** (recommended) — JSON parsing in hooks; degrades gracefully without it

## Quick Install

```
/plugin marketplace add AnkushinDaniil/noti
/plugin install noti@noti-marketplace
/noti:setup
```

`/noti:setup` is an interactive runbook that:
1. Creates a Telegram bot via @BotFather
2. Writes `~/.config/noti/config.json` (chmod 600)
3. Auto-detects your chat ID via a one-shot `getUpdates`
4. Sends a test message to confirm delivery
5. Installs the broker daemon (launchd on macOS, systemd --user on Linux)

After setup, **restart Claude Code** (or run `/reload-plugins`) to activate hooks and the MCP server.

---

## Architecture

```
┌──────────────────────────────────────────────────────┐
│  Claude Code session                                   │
│                                                        │
│  hooks/hooks.json ──► bin/notify.sh ──► broker /notify │
│                                               │         │
│  server/mcp_server.py ◄──── stdio ────────── MCP       │
│         │                                              │
│         └──► broker /ask /wait /notify /send_file      │
└───────────────────────────┬──────────────────────────-─┘
                            │ HTTP loopback 127.0.0.1:7432
                    ┌───────▼───────┐
                    │  bin/broker.py │  (long-lived daemon)
                    │               │
                    │  HTTP server  │◄── /health /notify /ask /wait /send_file
                    │  Telegram     │──► getUpdates (single consumer)
                    │  poll thread  │
                    └───────────────┘
                            │
                     Telegram / Discord / Slack
```

### Hooks (one-way alerts)

`hooks/hooks.json` registers two hooks:

| Hook | Matcher | Script arg | When fired |
|------|---------|-----------|------------|
| `Notification` | `permission_prompt` | `attention` | Claude pauses for your permission |
| `Stop` | (any) | `done` | Claude finishes a turn |

`bin/notify.sh` reads the hook JSON from stdin, builds a short message
(`🔔 [project] <message>` or `✅ [project] Claude finished`), then:
1. POSTs to the broker `POST /notify` (5-second timeout)
2. Falls back to a direct Telegram `sendMessage` if the broker is unreachable
3. Always exits 0 — never blocks or breaks a Claude turn

**Note:** `idle_prompt` is intentionally NOT matched (it false-fires after every turn).

### Broker daemon (single getUpdates owner)

`bin/broker.py` is a long-lived Python daemon. It is the **only** process that
polls Telegram's `getUpdates` endpoint. This is required because Telegram allows
exactly one active `getUpdates` consumer per bot token (HTTP 409 otherwise).

The broker:
- Binds `127.0.0.1:7432` (loopback only)
- Runs a Telegram long-poll thread (`timeout=25s`)
- Manages a ticket registry for ask/wait round-trips
- Writes a PID lockfile to prevent duplicate instances
- Persists the `getUpdates` offset across restarts

### MCP server (`server/mcp_server.py`)

A hand-rolled JSON-RPC 2.0 over stdio server (no external SDK). One instance
per Claude Code session. It forwards all tool calls to the broker over HTTP.

### Per-project routing

In `~/.config/noti/config.json` you can route different projects to different
chats or channels:

```json
"routing": [
  { "match": "my-secret-proj", "channel": "telegram", "chat_id": "111222333", "match_type": "project" },
  { "match": "/work/*", "channel": "discord", "match_type": "path_glob" }
]
```

`match_type` options:
- `"project"` — matches `basename(cwd)` exactly
- `"path_glob"` — `fnmatch` on the full `cwd` path

First match wins; if nothing matches, the telegram default is used.

---

## Config file — `~/.config/noti/config.json`

**Permissions: chmod 600. Never commit this file.**

```json
{
  "telegram": { "bot_token": "123:AAH...", "default_chat_id": "987654321" },
  "channels": { "discord_webhook": "", "slack_webhook": "" },
  "routing": [],
  "broker": { "host": "127.0.0.1", "port": 7432 }
}
```

Config path can be overridden with the `NOTI_CONFIG` environment variable.

---

## MCP Tools

All tools are available to Claude via the `noti` MCP server.

| Tool | Description |
|------|-------------|
| `ask_user` | Ask the human a question; returns the answer or a ticket for `wait_for_reply`. Use INSTEAD of guessing or stopping. |
| `wait_for_reply` | Continue waiting for a phone reply. Call repeatedly with the ticket id until answered. |
| `notify` | Proactively send a short status update to the user's phone. |
| `send_file` | Send a file or document to the user's phone. |
| `send_image` | Send an image to the user's phone (same as send_file, clearly named). |

### `ask_user` flow

Claude calls `ask_user` with a question and optional buttons. The broker
sends a Telegram message with inline keyboard buttons (if options provided).
The user taps a button or replies; the answer is returned to Claude.

Because the MCP tool-call timeout is ~60s, the broker returns `pending` after
~50s if no reply, and Claude should call `wait_for_reply` repeatedly until answered.

---

## Broker HTTP API

Loopback only. All endpoints accept/return JSON.

| Method | Route | Description |
|--------|-------|-------------|
| GET | `/health` | Status check |
| POST | `/notify` | Send a notification |
| POST | `/ask` | Ask a question, wait for reply |
| POST | `/wait` | Continue waiting on a ticket |
| POST | `/send_file` | Send a file/image |

---

## Broker Install / Uninstall

```bash
# Install (run via /noti:setup or manually):
"${CLAUDE_PLUGIN_ROOT}/bin/install-broker.sh"

# Uninstall:
"${CLAUDE_PLUGIN_ROOT}/bin/uninstall-broker.sh"

# Status / manual test:
"${CLAUDE_PLUGIN_ROOT}/bin/noti" status
"${CLAUDE_PLUGIN_ROOT}/bin/noti" test "hello from CLI"
```

**macOS:** installs as a LaunchAgent (`~/Library/LaunchAgents/com.noti.broker.plist`) that starts on login.

**Linux:** installs as a systemd user service (`~/.config/systemd/user/noti-broker.service`) with `Restart=always`.

After a **plugin update**, re-run `/noti:setup` or `install-broker.sh` to refresh the service path (because `CLAUDE_PLUGIN_ROOT` changes with each update).

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CLAUDE_PLUGIN_ROOT` | set by Claude Code | Absolute path to plugin install dir |
| `CLAUDE_PLUGIN_DATA` | `~/.local/state/noti/` | Persistent state: offsets, lockfile, logs |
| `CLAUDE_PLUGIN_OPTION_BOT_TOKEN` | — | Token fallback for notify.sh if broker down |
| `CLAUDE_PLUGIN_OPTION_CHAT_ID` | — | Chat ID fallback for notify.sh |
| `NOTI_BROKER_URL` | `http://127.0.0.1:7432` | Broker endpoint |
| `NOTI_CONFIG` | `~/.config/noti/config.json` | Config file path override |
| `NOTI_TEST` | — | Set to `1` for offline test mode (no real Telegram) |

---

## Security

- `~/.config/noti/config.json` is written with **chmod 600** (owner read/write only).
- The broker binds **loopback only** (`127.0.0.1`) — not accessible over the network.
- Incoming Telegram updates are validated against allowed chat IDs — unknown senders are ignored.
- The bot token is **never logged**.
- Messages transit Telegram's servers. For maximum privacy, use BotFather → `/setprivacy` → Enable.

---

## Known Limitations

- The broker only runs while you are **logged in**. A sleeping machine queues webhook notifications
  but `ask_user` calls will time out if the machine is asleep.
- **One bot per user.** You cannot run two separate broker instances with the same token
  (Telegram HTTP 409). Use one bot and route different projects via `routing` config.
- Slack file sending requires a bot token (not just an incoming webhook).
- After a plugin update, the broker service path must be refreshed via `/noti:setup`.

---

## License

MIT — see [LICENSE](LICENSE).
