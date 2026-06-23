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
on lifecycle events. noti uses three:

| Hook event | Matcher | Meaning |
|------------|---------|---------|
| `Notification` | `permission_prompt` | Claude needs you to approve a tool |
| `Stop` | (any) | Claude finished its turn |
| `PreToolUse` | gated tools | Intercept tool calls for phone-first Allow/Deny |

> **Why not `idle_prompt`?** The `Notification` event also has an `idle_prompt`
> type, but it false-fires after essentially every turn, so it is a poor "user is
> waiting" signal. We match `permission_prompt` only and let `Stop` cover
> "finished".

Alert hooks (`Notification`, `Stop`) are **fire-and-forget**: they run a command
but cannot inject a reply back into Claude's conversation. `bin/notify.sh` is
the hook command — a thin locator script that finds the `noti` binary and execs
`noti notify <level>`. The binary:
1. Reads the hook JSON from stdin
2. Builds a short message (`🔔 [project] <message>` or `✅ [project] Claude finished`)
3. POSTs to the broker `POST /notify` (5-second timeout)
4. Falls back to a direct Telegram `sendMessage` if the broker is unreachable
5. Always exits 0 — never blocks or breaks a Claude turn

The `PreToolUse` hook (`bin/permission_gate.sh` → `noti permission-gate`) can
return a permission decision (allow/deny/ask), making it the one hook that is
not fire-and-forget. See [Permission gate](#permission-gate) below.

## Answering from the phone: an MCP `ask_user` tool

To get a reply *into* Claude, the reply must arrive as data Claude requested —
i.e. as the result of a tool call. So noti ships an MCP server exposing
`ask_user`. Claude is instructed (via the tool description) to call `ask_user`
whenever it would otherwise guess or stop. The tool sends the question to your
phone and returns your answer as the tool result.

### Why the call is split into `ask_user` + `wait_for_reply`

Claude Code's MCP tool calls time out at ~60 seconds. A human might take minutes
to reply. So a single blocking call is not viable. Instead:

1. `ask_user(question, options?)` sends the question to **both** the laptop and
   the phone (see [Elicitation race](#elicitation-race-ask_user-dual-input) below).
   It waits up to ~50 s. If either source answers in time, it returns the answer.
   If not, it returns a `ticket`.
2. `wait_for_reply(ticket)` waits another ~50 s via the phone broker.
   Claude calls it repeatedly until the answer arrives.

Each individual call stays safely under the 60 s fence, while the overall wait is
unbounded.

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
| `noti permission-gate` | per PreToolUse event | reads stdin, phone-first Allow/Deny gate |
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

## Elicitation race — `ask_user` dual-input

`ask_user` sends the question to **two sources at once** and applies a
first-wins policy. The MCP server issues a server-originated
`elicitation/create` request to Claude Code (laptop) and, depending on the
configured mode, also POSTs to the broker (phone). Whichever source answers
first wins; the loser is cancelled best-effort.

### Two modes

**`timeout` (default)**

```
t=0   MCP server sends elicitation/create to the laptop UI
t=idle_timeout_seconds   broker POST /ask (phone) if no laptop answer yet
      laptop decline → escalates to phone immediately (does not wait for idle)
t=~50s ceiling   give up: cancel laptop prompt, return ticket for wait_for_reply
```

**`forward-all`**

```
t=0   MCP server sends elicitation/create AND broker POST /ask simultaneously
      first answer (laptop or phone) wins; loser cancelled
t=~50s ceiling   give up if neither answered
```

`idle_timeout_seconds` is clamped to the range 1–50.

### Hard-require elicitation

`ask_user` checks whether the Claude Code client advertised the `elicitation`
capability during the `initialize` handshake.

- If `require_laptop: true` (default) and the client lacks the capability,
  `ask_user` returns an `isError` result telling the user to update Claude Code.
  **It does not silently fall back to phone-only.**
- If `require_laptop: false` and the client lacks the capability, the laptop
  leg is skipped and the question goes to the phone only.

The capability requires Claude Code v2.1.76+.

### Server-issued requests + cancellation

The MCP server maintains an atomic request-id counter. Each
`elicitation/create` call is sent as a JSON-RPC 2.0 request with an id of the
form `"noti-req-<n>"`. The response is routed back via the existing `pending`
map (keyed on the request id). When the phone wins:

1. The MCP server sends a `notifications/cancelled` notification with the
   elicitation's `requestId` and `reason: "answered elsewhere"`.
2. The id is recorded in a `cancelled` set. Any late response from the laptop
   with that id is silently dropped (idempotent first-wins).

**Lingering-prompt caveat:** the MCP spec says cancelling a shown elicitation is
`SHOULD`, not `MUST`. The laptop UI prompt may remain visible after the phone
answers. This is a protocol limitation and cannot be avoided.

**~50 s ceiling:** the laptop elicitation only lives during the `ask_user` tool
call. If neither source answers within ~50 s (safely under Claude Code's ~60 s
fence), the laptop prompt is cancelled and `ask_user` returns a ticket so the
caller can continue polling via `wait_for_reply` on the phone only.

### First-wins sequencing

```
MCP server                Claude Code UI        Broker / Phone
     │                         │                      │
     │── elicitation/create ───►│                      │
     │                         │                      │
     │── POST /ask ─────────────────────────────────►  │
     │                         │                      │
     │◄── elicitation response ─│  (laptop wins)       │
     │    or                   │                      │
     │◄────────────── /wait answered ────────────────  │  (phone wins)
     │                         │                      │
     │── notifications/cancelled ►│  (if phone won)    │
     │── POST /cancel ──────────────────────────────►  │  (if laptop won)
     │                         │                      │
     │── toolResult(answer) ──►│                      │
```

## Permission gate

True simultaneous first-wins for permission prompts is not possible with the
Claude Code hook protocol: a `PreToolUse` hook can either block (return a
decision) or pass through, but it cannot race two sources and return the first
answer. The permission gate is therefore **phone-first, then terminal fallback**:

```
PreToolUse hook fires (bin/permission_gate.sh → noti permission-gate)
    │
    ├── permissions.enabled=false OR tool not in gated set OR broker unreachable
    │       → emit pass-through (ask) → normal terminal prompt shown
    │
    └── POST /ask {question:"Allow <tool>?", options:["Allow","Deny"]}
            │
            ├── wait up to permissions.timeout_seconds (5 s polls)
            │       │
            │       ├── answer "Allow" → emit decision: allow
            │       ├── answer "Deny"  → emit decision: deny
            │       └── timeout / no answer → emit pass-through (ask)
            │                                  → normal terminal prompt shown
```

The hook always exits 0 and always emits valid JSON (or nothing for
pass-through). A broken broker or a missed phone notification never stalls
Claude indefinitely.

Default gated tools: `Bash`, `Write`, `Edit`, `NotebookEdit`. Configure via
`ask.permissions.tools`.

## Deployment topology

| Component | Lifetime | Telegram access |
|-----------|----------|-----------------|
| Hook (`noti notify`) | per event | via broker (or direct send fallback) |
| Hook (`noti permission-gate`) | per PreToolUse event | via broker only |
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
