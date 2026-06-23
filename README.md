# noti — Phone Notifications for Claude Code

Get a Telegram notification on your phone when Claude Code pauses for a
permission prompt or finishes a task. Answer Claude's questions directly from
your phone via the `ask_user` MCP tool.

v2.0.0 is a complete rewrite in Go: a single static binary, no Python or
external runtime dependencies.

## Prerequisites

- **Go 1.23+** — to build the binary from source (a pre-built download will be
  available in Step 3 of the roadmap)
- **curl** — optional; used in `notify.sh` fallback path

## Quick Install

```
/plugin marketplace add AnkushinDaniil/noti
/plugin install noti@noti-marketplace
/noti:setup
```

`/noti:setup` is an interactive runbook that:
1. Checks Go is installed
2. Creates a Telegram bot via @BotFather
3. Writes `~/.config/noti/config.json` (chmod 600)
4. Builds the `noti` binary via `scripts/build.sh`
5. Auto-detects your chat ID via `noti detect-chat`
6. Sends a test message via `noti test`
7. Installs the broker daemon (launchd on macOS, systemd --user on Linux)

After setup, **restart Claude Code** (or run `/reload-plugins`) to activate
hooks and the MCP server.

---

## Architecture

```
┌────────────────────────────────────────────────────────┐
│  Claude Code session                                     │
│                                                          │
│  hooks/hooks.json ──► bin/notify.sh ──► broker /notify  │
│                                                          │
│  noti mcp  ◄──── stdio ──── Claude MCP client           │
│      │                                                   │
│      └──► broker /ask /wait /notify /send_file           │
└───────────────────────────┬──────────────────────────-──┘
                            │ HTTP loopback 127.0.0.1:7432
                    ┌───────▼───────┐
                    │  noti broker  │   (long-lived daemon)
                    │               │
                    │  HTTP server  │◄── /health /notify /ask /wait
                    │  Telegram     │──► getUpdates (single consumer)
                    │  poll loop    │
                    └───────────────┘
                            │
                         Telegram
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for a deeper explanation.

### Hooks (one-way alerts)

`hooks/hooks.json` registers two hooks:

| Hook | Matcher | Level | When fired |
|------|---------|-------|------------|
| `Notification` | `permission_prompt` | `attention` | Claude pauses for your permission |
| `Stop` | (any) | `done` | Claude finishes a turn |

`bin/notify.sh` is a thin locator script that finds the `noti` binary and
calls `noti notify <level>`. The binary reads the hook JSON from stdin, builds
a short message, POSTs it to the broker (5-second timeout), and falls back to a
direct Telegram `sendMessage` if the broker is unreachable. Always exits 0.

### Broker daemon (single getUpdates owner)

`noti broker` is the long-lived daemon. It is the **only** process that polls
Telegram's `getUpdates` endpoint — Telegram allows exactly one concurrent
consumer per bot token (HTTP 409 Conflict otherwise).

- Binds `127.0.0.1:7432` (loopback only)
- Long-polls Telegram (`timeout=25s`)
- Manages a ticket registry for ask/wait round-trips
- Writes a PID lockfile to prevent duplicate instances
- Persists the `getUpdates` offset across restarts

### MCP server

`noti mcp` is a JSON-RPC 2.0 over stdio server. One instance per Claude Code
session. Reads `NOTI_BROKER_URL` (default `http://127.0.0.1:7432`) and forwards
all tool calls to the broker over HTTP. **Never touches the bot token.**

---

## Config file — `~/.config/noti/config.json`

**Permissions: chmod 600. Never commit this file.**

```json
{
  "telegram": { "bot_token": "123:AAH...", "default_chat_id": "987654321" },
  "channels": { "discord_webhook": "", "slack_webhook": "" },
  "routing": [],
  "broker": { "host": "127.0.0.1", "port": 7432 },
  "ask": {
    "mode": "timeout",
    "idle_timeout_seconds": 30,
    "laptop": true,
    "require_laptop": true,
    "permissions": { "enabled": true, "timeout_seconds": 30 }
  }
}
```

The `ask` block is wired in now; the two modes (`timeout` / `forward-all`) and
the permission gate become active in v2 Step 2.

Config path can be overridden with the `NOTI_CONFIG` environment variable.

### Per-project routing

Route different projects to different chats:

```json
"routing": [
  { "match": "my-secret-proj", "channel": "telegram", "chat_id": "111222333", "match_type": "project" },
  { "match": "/work/client-*", "channel": "telegram", "chat_id": "444555666", "match_type": "path_glob" }
]
```

`match_type` options:
- `"project"` — matches `basename(cwd)` exactly (default)
- `"path_glob"` — `filepath.Match` on the full `cwd` path

First match wins; if nothing matches, the telegram default is used.

---

## MCP Tools

| Tool | Description |
|------|-------------|
| `ask_user` | Ask the human a question; returns the answer or a ticket. Use INSTEAD of guessing or stopping. |
| `wait_for_reply` | Continue waiting for a phone reply. Call with the ticket until answered. |
| `notify` | Proactively send a short status update to the user's phone. |
| `send_file` | Send a file or document to the user's phone. |
| `send_image` | Send an image to the user's phone. |

---

## Broker HTTP API

Loopback only (`127.0.0.1:7432`). All endpoints accept/return JSON.

| Method | Route | Description |
|--------|-------|-------------|
| GET | `/health` | Status — `{"status":"ok","version":"2.0.0",...}` |
| POST | `/notify` | Send a notification — `{text, level?, channel?, chat_id?, project?}` |
| POST | `/ask` | Create a question ticket — `{question, options?[], project?, chat_id?}` |
| POST | `/wait` | Poll for an answer — `{ticket, timeout?}` (blocks up to 55s) |
| POST | `/cancel` | Cancel a ticket — `{ticket}` |
| GET | `/config` | Resolved ask config — `?project=NAME` |

---

## Build & Install

```bash
# Build the binary into bin/noti:
bash scripts/build.sh

# Or build to the persistent data directory (survives plugin updates):
OUT="${CLAUDE_PLUGIN_DATA:-${HOME}/.local/state/noti}/bin/noti" bash scripts/build.sh

# Install broker as a background service:
"${CLAUDE_PLUGIN_ROOT}/bin/install-broker.sh"

# Uninstall:
"${CLAUDE_PLUGIN_ROOT}/bin/uninstall-broker.sh"
```

---

## Subcommands

```
noti broker              Start the background broker daemon
noti mcp                 Start the MCP stdio server
noti notify <level>      Send a hook notification (stdin: hook JSON)
noti detect-chat         Print the most recent Telegram chat ID (setup helper)
noti test [text]         Send a test notification
noti version             Print version
noti help                Print usage
```

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CLAUDE_PLUGIN_ROOT` | set by Claude Code | Absolute path to plugin install dir |
| `CLAUDE_PLUGIN_DATA` | `~/.local/state/noti/` | Persistent state: offsets, lockfile, logs |
| `NOTI_BROKER_URL` | `http://127.0.0.1:7432` | Broker endpoint (read by `noti mcp`) |
| `NOTI_CONFIG` | `~/.config/noti/config.json` | Config file path override |
| `NOTI_TEST` | — | Set to `1` for offline test mode (no real Telegram I/O) |

---

## Security

- `~/.config/noti/config.json` is written with **chmod 600** (owner read/write only).
- The broker binds **loopback only** (`127.0.0.1`) — not accessible over the network.
- Incoming Telegram updates are validated against allowed chat IDs — unknown senders are ignored.
- The bot token is **never logged**.
- The MCP server never receives the bot token — it only knows the broker URL.
- Messages transit Telegram's servers. For maximum privacy: BotFather → `/setprivacy` → Enable.

---

## Known Limitations

- The broker only runs while you are **logged in**. A sleeping machine queues
  Telegram notifications but `ask_user` calls will time out if the machine is asleep.
- **One bot per user.** Two concurrent broker instances with the same token cause
  Telegram HTTP 409. Use one bot and route different projects via `routing`.
- After a plugin update, re-run `/noti:setup` or `install-broker.sh` to refresh
  the service path (`CLAUDE_PLUGIN_ROOT` changes with each update).
- Pre-built release binaries (no Go required) land in v2 Step 3.

---

## License

MIT — see [LICENSE](LICENSE).
