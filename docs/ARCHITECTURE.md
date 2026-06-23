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
> "finished". This was a deliberate correction, not an oversight.

Hooks are **fire-and-forget**: a hook can run a command, but it cannot inject a
reply back into Claude's conversation. That is the key limitation that forces the
two-way path onto a different mechanism (below). So hooks are perfect for
*notify* and useless for *answer*.

`bin/notify.sh` is the hook command. It reads the hook JSON from stdin, formats a
short message, and POSTs it to the broker. If the broker is down it falls back to
calling Telegram directly. It always exits 0 so it can never break a Claude turn.

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

> MCP's protocol-native [elicitation](https://modelcontextprotocol.io) is not used
> because Claude Code only surfaces elicitation in the terminal UI — there is no
> hook to route it to Telegram. The async `ask_user`/`wait_for_reply` pair is the
> workaround.

## The broker daemon: why it must exist and be a singleton

The MCP server does **not** talk to Telegram directly. A separate long-lived
daemon, `bin/broker.py`, owns the bot token and is the only process that calls
Telegram's `getUpdates`. Two hard constraints make this mandatory:

1. **Telegram allows exactly one active `getUpdates` consumer per bot token.** A
   second concurrent poller gets `HTTP 409 Conflict`. (Telegram hands the
   connection to the newest caller and terminates the previous one, so two
   pollers *oscillate* — they kick each other out — rather than one stably
   winning.)
2. **Claude Code spawns one MCP server process per session.** Two terminal tabs =
   two MCP processes, with no shared parent. If each polled `getUpdates` they
   would evict each other constantly.

The fix is a single broker that owns the token and the offset cursor. The
per-session MCP servers and the hooks all talk to it over loopback HTTP
(`127.0.0.1:7432`). To make this impossible to get wrong, the MCP server is given
**only the broker URL, never the bot token** — so it *cannot* poll Telegram even
by accident.

```
Claude session A ─ MCP ─┐
Claude session B ─ MCP ─┤
hooks (notify.sh) ──────┼──► 127.0.0.1:7432  ──►  broker  ──►  Telegram getUpdates
noti CLI ───────────────┘        (loopback)      (singleton)   (single consumer)
```

### Broker internals

- **HTTP API** (`ThreadingHTTPServer`, loopback only): `/health`, `/notify`,
  `/ask`, `/wait`, `/send_file`, plus `/test/inject` in test mode.
- **Ticket registry:** thread-safe map of `ticket → threading.Event`. `/ask` and
  `/wait` block on the event; the poll thread resolves it when a matching reply
  arrives.
- **One Telegram poll thread:** long-polls `getUpdates(timeout=25)`, persists the
  offset to disk, never dies (loop body is exception-wrapped), backs off on 409
  and network errors.
- **Reply matching:** inline-button taps carry `callback_data = noti:<ticket>:<idx>`;
  text replies are matched via Telegram's reply-to-message link; the fallback
  is the single pending ticket for that chat. This keeps concurrent sessions from
  stealing each other's answers.
- **Singleton lockfile:** a PID lockfile under `CLAUDE_PLUGIN_DATA` refuses a
  second broker start.
- **Security:** updates from chat IDs not on the allow-list are ignored; the
  token is never logged; the socket is loopback-only.

## Deployment topology

| Component | Lifetime | Telegram access |
|-----------|----------|-----------------|
| Hooks (`notify.sh`) | per event | direct send, or via broker |
| MCP server | per Claude session | none — broker only |
| Broker daemon | always-on (launchd/systemd) | sole `getUpdates` + sends |

The broker is installed as a **launchd LaunchAgent** (macOS) or **systemd `--user`
service** (Linux) so it restarts on failure and starts at login. Because the
plugin install path (`CLAUDE_PLUGIN_ROOT`) changes on every update, the service
must be refreshed after a plugin update (`/noti:setup` or `install-broker.sh`).

## Identity model: each user runs their own bot

noti is **single-tenant by design**: every user creates their own Telegram bot via
`@BotFather` and stores their own token locally. There is no central server and no
shared bot.

- Zero infrastructure for the plugin author; nothing to operate or scale.
- Each user's messages transit *their own* bot — the author never sees user data.
- The only cost is a ~2-minute BotFather flow during `/noti:setup`.

A shared-central-bot model (one bot, many users, deep-link pairing) would improve
onboarding but would make the author a data controller and turn one token leak
into a fleet-wide compromise. It is intentionally **out of scope** for this
version.

## Secrets

The source of truth for the bot token is `~/.config/noti/config.json` (chmod
600), read by the broker. The plugin also captures the token via Claude Code's
`userConfig` (keychain), but because that storage has had persistence quirks, the
broker and hooks fall back to the config file / environment. The token is never
committed, never logged, and only ever leaves the machine as an HTTPS call to
Telegram.

## Zero dependencies

Everything is Python 3.9+ standard library plus `bash`/`curl`. The MCP server is a
hand-rolled JSON-RPC 2.0 stdio implementation rather than an SDK. This keeps
installation friction at zero: any machine with `python3` (every macOS/Linux) can
run noti without `pip`/`npm`/build steps.
