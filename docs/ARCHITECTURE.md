# Architecture

This document explains *why* noti is built the way it is. For usage, see the
[README](../README.md).

## The problem

Claude Code runs in a terminal. When it pauses — to ask for permission, to ask a
question, or because it finished — you have to be watching the terminal to
notice. noti turns those moments into a phone notification, and lets you answer
Claude's questions from your phone so it keeps going.

Two directions, two very different difficulties:

- **One-way (notify):** "Claude paused / finished" → buzz my phone. Easy.
- **Two-way (answer):** Claude asks "Delete these 3 files?" → I tap **No** on my
  phone → Claude receives "No" and continues. Hard, and it drives the whole design.

## Trigger mechanism: hooks

Claude Code fires [hooks](https://docs.anthropic.com/en/docs/claude-code/hooks)
on lifecycle events. noti uses two:

| Hook | Matcher | Meaning |
|------|---------|---------|
| `Notification` | `permission_prompt` | Claude needs you to approve a tool |
| `Stop` | (any) | Claude finished its turn |

> **Why not `idle_prompt`?** The `Notification` event also has an `idle_prompt`
> type, but it false-fires after essentially every turn, so it is a poor "user is
> waiting" signal. We match `permission_prompt` only and let `Stop` cover
> "finished".

Hooks are **fire-and-forget**: a hook can run a command, but it cannot inject a
reply back into Claude's conversation. So hooks are perfect for *notify* and
useless for *answer*.

`bin/notify.sh` is the hook command — a thin locator script that finds the `noti`
binary and execs `noti notify <level>`. The binary:
1. Reads the hook JSON from stdin
2. Builds a short message (`🔔 [project] <message>` or `✅ [project] Claude finished`)
3. POSTs to the broker `POST /notify` (5-second timeout)
4. Falls back to a direct Telegram `sendMessage` if the broker is unreachable
5. Always exits 0 — never blocks or breaks a Claude turn

## Answering from the phone: an MCP `ask_user` tool

To get a reply *into* Claude, the reply must arrive as data Claude requested —
i.e. as the result of a tool call. So noti ships an MCP server exposing
`ask_user`. Claude is instructed (via the tool description) to call `ask_user`
whenever it would otherwise guess or stop. The tool sends the question to your
phone and returns your answer as the tool result.

### Why the call is split into `ask_user` + `wait_for_reply`

Claude Code's MCP tool calls time out at ~60 seconds. A human might take minutes
to reply. So a single blocking call is not viable. Instead:

1. `ask_user(question, options?)` sends the Telegram message and waits up to ~50s.
   If you reply in time, it returns the answer. If not, it returns a `ticket`.
2. `wait_for_reply(ticket)` waits another ~50s. Claude calls it repeatedly until
   the answer arrives.

Each individual call stays safely under the 60s fence, while the overall wait is
unbounded.

> MCP's protocol-native [elicitation](https://modelcontextprotocol.io) is not
> used in Step 1 because Claude Code only surfaces elicitation in the terminal UI.
> The `ask_user`/`wait_for_reply` pair is the workaround. The MCP server already
> stores the client's `elicitation` capability flag — Step 2 will use it.

## The broker daemon: why it must exist and be a singleton

The MCP server does **not** talk to Telegram directly. A separate long-lived
daemon runs `noti broker`, owns the bot token, and is the only process that calls
Telegram's `getUpdates`. Two hard constraints make this mandatory:

1. **Telegram allows exactly one active `getUpdates` consumer per bot token.** A
   second concurrent poller gets `HTTP 409 Conflict`. Two pollers oscillate —
   they kick each other out — rather than one stably winning.
2. **Claude Code spawns one MCP server process per session.** Two terminal tabs =
   two MCP processes with no shared parent. If each polled `getUpdates` they would
   evict each other constantly.

The fix is a single broker that owns the token and the offset cursor. The
per-session MCP servers and the hooks all talk to it over loopback HTTP
(`127.0.0.1:7432`). To make this impossible to get wrong, the MCP server is given
**only the broker URL, never the bot token** — so it cannot poll Telegram even
by accident.

```
Claude session A ── noti mcp ──┐
Claude session B ── noti mcp ──┤
hooks (notify.sh) ─────────────┼──► 127.0.0.1:7432 ──► noti broker ──► Telegram getUpdates
noti CLI ──────────────────────┘       (loopback)        (singleton)    (single consumer)
```

## The binary: one process, multiple subcommands

v2 ships a single static Go binary. The subcommands are:

| Subcommand | Lifetime | Role |
|------------|----------|------|
| `noti broker` | always-on daemon (launchd/systemd) | HTTP server + Telegram poll |
| `noti mcp` | per Claude session | JSON-RPC 2.0 stdio server |
| `noti notify <level>` | per hook event | reads stdin, POSTs to broker |
| `noti detect-chat` | one-shot setup | prints recent private chat ID |
| `noti test` | one-shot | sends a test message |

The binary cross-compiles to darwin/linux × amd64/arm64 with zero CGO.

## Broker internals

- **HTTP API** (`net/http`, loopback only): `/health`, `/notify`, `/ask`, `/wait`,
  `/cancel`, `/config`, plus `/test/inject` when `NOTI_TEST=1`.
- **Ticket registry:** a `sync.Mutex`-guarded map of `ticket → *ticket`. Each
  ticket carries a `done chan string`. `/ask` and `/wait` block via a `select` on
  `<-ticket.done` and `time.After`; the poll goroutine closes the channel when a
  matching reply arrives.
- **`resolve` is idempotent:** a `sync.Once` ensures the first writer wins and
  subsequent calls are no-ops.
- **One Telegram poll goroutine:** long-polls `getUpdates(timeout=25)`, persists
  the offset to `DataDir()/getUpdates.offset`, never exits (panic-recovers inner
  loop), backs off on `ErrConflict` and network errors.
- **Reply matching:** inline-button taps carry `callback_data = noti:<ticket>:<idx>`;
  text replies are matched via Telegram's `reply_to_message` link; the fallback
  is the single pending ticket for that chat.
- **Singleton lockfile:** `DataDir()/broker.lock` contains the PID; startup
  refuses if a live PID already holds it.
- **Reaper goroutine:** removes tickets older than 30 minutes.
- **Security:** updates from chat IDs not in the allow-set (default chat + routing
  chat ids) are ignored; the token is never logged; the socket is loopback-only.

## `/ask`–`/wait`–`/cancel` lifecycle

```
         caller                  broker                  Telegram
           │                       │                        │
           │── POST /ask ──────────►│                        │
           │                       │── sendMessage ─────────►│
           │◄── {ticket, pending} ──│                        │
           │                       │                        │
           │── POST /wait ─────────►│◄── getUpdates reply ───│
           │◄── {status:answered} ──│                        │
           │       or               │                        │
           │◄── {status:pending} ───│  (timeout hit; caller retries)
           │                       │                        │
           │── POST /cancel ───────►│   (if caller gives up)
```

## Deployment topology

| Component | Lifetime | Telegram access |
|-----------|----------|-----------------|
| Hook (`noti notify`) | per event | via broker (or direct send fallback) |
| MCP server (`noti mcp`) | per Claude session | none — broker only |
| Broker daemon (`noti broker`) | always-on | sole `getUpdates` + sends |

The broker is installed as a **launchd LaunchAgent** (macOS) or **systemd
`--user` service** (Linux) so it restarts on failure and starts at login.
Because `CLAUDE_PLUGIN_ROOT` changes on every plugin update, the service path
must be refreshed after an update (`/noti:setup` or `install-broker.sh`).

## Identity model: each user runs their own bot

noti is **single-tenant by design**: every user creates their own Telegram bot via
`@BotFather` and stores their own token locally. There is no central server.

- Zero infrastructure for the plugin author; nothing to operate or scale.
- Each user's messages transit *their own* bot — the author never sees user data.
- The only cost is a ~2-minute BotFather flow during `/noti:setup`.

## Zero runtime dependencies

The binary is a single statically-compiled Go executable. No Python, no pip, no
npm, no dynamic libraries. It cross-compiles to any supported GOOS/GOARCH. The
only external dependency at runtime is outbound HTTPS to `api.telegram.org`.

## Step 2 wiring (not yet active)

The `ask` config block (mode, idle_timeout_seconds, laptop, require_laptop,
permissions) and the broker's `/config` endpoint are already wired. Step 2 will
activate the two modes (`timeout` / `forward-all`) and the permission gate.
The MCP server already records the client's `elicitation` capability flag for
future use.
