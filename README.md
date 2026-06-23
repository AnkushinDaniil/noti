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
┌──────────────────────────────────────────────────────────────┐
│  Claude Code session                                          │
│                                                               │
│  hooks/hooks.json ──► bin/notify.sh       ──► broker /notify │
│                   ──► bin/permission_gate.sh ► broker /ask   │
│                                                               │
│  noti mcp  ◄──── stdio ──── Claude MCP client                │
│      │               elicitation/create ◄──┘                  │
│      └──► broker /ask /wait /cancel /notify /send_file        │
└──────────────────────────────┬───────────────────────────────┘
                               │ HTTP loopback 127.0.0.1:7432
                       ┌───────▼───────┐
                       │  noti broker  │   (long-lived daemon)
                       │               │
                       │  HTTP server  │◄── /health /notify /ask /wait /cancel /config
                       │  Telegram     │──► getUpdates (single consumer)
                       │  poll loop    │
                       └───────────────┘
                               │
                            Telegram
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for a deeper explanation.

### Hooks

`hooks/hooks.json` registers three hooks:

| Hook event | Matcher | Purpose |
|------------|---------|---------|
| `Notification` | `permission_prompt` | Sends a Telegram alert when Claude pauses for permission |
| `Stop` | (any) | Sends a Telegram alert when Claude finishes a turn |
| `PreToolUse` | gated tools (`Bash`, `Write`, `Edit`, `NotebookEdit`) | Phone-first permission gate |

**Alert hooks** (`Notification`, `Stop`): `bin/notify.sh` is a thin locator script that
finds the `noti` binary and calls `noti notify <level>`. The binary reads the hook JSON
from stdin, builds a short message, POSTs it to the broker (5-second timeout), and falls
back to a direct Telegram `sendMessage` if the broker is unreachable. Always exits 0.

**Permission gate** (`PreToolUse`): `bin/permission_gate.sh` calls `noti permission-gate`.
The binary reads the hook JSON from stdin, sends an Allow/Deny question to the phone, and
returns a permission decision on stdout. If the broker is unreachable or the timeout
elapses, it emits a pass-through so Claude falls back to the normal terminal prompt.
Always exits 0.

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
    "permissions": {
      "enabled": true,
      "timeout_seconds": 30,
      "tools": ["Bash", "Write", "Edit", "NotebookEdit"]
    }
  }
}
```

Config path can be overridden with the `NOTI_CONFIG` environment variable.

### The `ask` config block

The `ask` block controls how `ask_user` delivers questions and how permission
prompts are gated. All fields have defaults and can be overridden per-project
(see [Per-project routing](#per-project-routing) below).

| Field | Default | Description |
|-------|---------|-------------|
| `mode` | `"timeout"` | `"timeout"` or `"forward-all"` — see [Two modes](#two-modes) |
| `idle_timeout_seconds` | `30` | Seconds before escalating to phone in `timeout` mode (clamped 1–50) |
| `laptop` | `true` | Whether to show the question in the Claude Code UI via MCP elicitation |
| `require_laptop` | `true` | If `true` and the client does not support elicitation, `ask_user` returns an error instead of falling back silently |
| `permissions.enabled` | `true` | Enable the phone-first permission gate for tool-approval prompts |
| `permissions.timeout_seconds` | `30` | How long the gate waits for a phone response before falling back to the normal terminal prompt |
| `permissions.tools` | `["Bash","Write","Edit","NotebookEdit"]` | Tools whose permission prompts are sent to the phone |

### Two modes

`ask_user` sends the question to **both** the laptop (via MCP elicitation) and
the phone (via the broker), subject to the configured mode. The first answer
wins; the other input is cancelled (best-effort).

**`timeout` (default)**

The question appears in the Claude Code terminal UI immediately. If you do not
answer within `idle_timeout_seconds`, the question is also sent to your phone.
If you then answer on the phone, that wins. If you later answer on the laptop,
the late answer is silently dropped.

**`forward-all`**

The question is sent to both the laptop and the phone at the same time. Whichever
you answer first wins; the other is cancelled.

### Laptop elicitation and the ~50 s window

The laptop prompt is delivered via MCP's `elicitation/create` and lives only
for the duration of the `ask_user` tool call — roughly 50 seconds (safely under
Claude Code's ~60 s tool-call ceiling). If neither source answers within 50 s,
the laptop prompt is cancelled and `ask_user` returns a ticket so you can
continue waiting on the phone via `wait_for_reply`.

**Lingering-laptop caveat:** the MCP specification says cancelling a shown
elicitation is `SHOULD`, not `MUST`. When the phone wins, the laptop prompt
*may* remain visible until you dismiss it. Any late answer from the laptop is
silently dropped — the first-wins outcome is already committed.

### Hard-require elicitation

When `require_laptop` is `true` (the default) and the connected Claude Code
client does **not** advertise the `elicitation` capability (requires Claude Code
v2.1.76+), `ask_user` returns an error:

> noti needs Claude Code with MCP elicitation (v2.1.76+). Update Claude Code,
> or set ask.require_laptop=false for phone-only.

This is intentional — noti will not silently downgrade to phone-only when you
expect the dual-input race. Set `require_laptop: false` to explicitly opt in to
phone-only operation on older clients.

### Phone-first permission gate

When `permissions.enabled` is `true`, a `PreToolUse` hook intercepts calls to
the tools listed in `permissions.tools`. The flow is sequential (not
first-wins — a protocol limit of the hook system):

1. The hook POSTs the tool name and a short summary of its input to the broker.
2. The broker sends an **Allow / Deny** question to your phone.
3. If you answer **Allow** within `permissions.timeout_seconds`, the tool
   proceeds. If you answer **Deny**, the tool is blocked with a reason. If the
   timeout elapses or the broker is unreachable, the normal terminal prompt is
   shown (pass-through — Claude is never blocked indefinitely).

The gate always exits 0 and never crashes a Claude session.

### Per-project routing

Route different projects to different chats, and optionally override `ask`
settings per project:

```json
"routing": [
  {
    "match": "my-secret-proj",
    "channel": "telegram",
    "chat_id": "111222333",
    "match_type": "project"
  },
  {
    "match": "/work/client-*",
    "channel": "telegram",
    "chat_id": "444555666",
    "match_type": "path_glob",
    "ask": {
      "mode": "forward-all",
      "permissions": { "enabled": false }
    }
  }
]
```

`match_type` options:
- `"project"` — matches `basename(cwd)` exactly (default)
- `"path_glob"` — `filepath.Match` on the full `cwd` path

First match wins; if nothing matches, the telegram default is used. The `ask`
override in a matching route is merged on top of the global `ask` block —
only fields you set are changed.

---

## MCP Tools

| Tool | Description |
|------|-------------|
| `ask_user` | Ask the human a question. Shows the question on the laptop (MCP elicitation) AND the phone; the first answer wins. Returns the answer directly, or a ticket if neither source replied within the ~50 s window. |
| `wait_for_reply` | Continue waiting for a phone reply. Call with the ticket returned by `ask_user` until answered. |
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
noti permission-gate     PreToolUse hook: phone-first permission gate (stdin: hook JSON)
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
- **Lingering laptop prompt:** when the phone answers first, the MCP elicitation
  prompt in the Claude Code UI may linger until you dismiss it. Any subsequent
  laptop answer is silently dropped.
- **Laptop window is ~50 s:** if neither input source answers within 50 s,
  `ask_user` returns a ticket and you must call `wait_for_reply` to keep
  waiting on the phone. The laptop prompt is cancelled at that point.
- **Permission gate is sequential, not first-wins:** true simultaneous
  first-wins for permission prompts is not possible with the Claude Code hook
  protocol (a hook is block-or-pass-through, not a race). Permissions are
  phone-first, then fall through to the normal terminal prompt on timeout.
- **Elicitation requires Claude Code v2.1.76+.** With `require_laptop: true`
  (the default), `ask_user` returns an error on older clients. Set
  `require_laptop: false` to use phone-only mode instead.

---

## License

MIT — see [LICENSE](LICENSE).
