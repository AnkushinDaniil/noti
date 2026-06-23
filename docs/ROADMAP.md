# Roadmap

noti ships in stages, smallest useful thing first.

## v1.0 — one-way notify ✅ (current, on `main`)

- Telegram alert when Claude Code needs permission (`Notification` /
  `permission_prompt`) or finishes (`Stop`).
- Pure `bash` + `curl`, zero dependencies, no daemon.

## v2 — answer from your phone (planned)

- An MCP `ask_user` tool so Claude can ask a question and get your reply from your
  phone, then continue.
- Requires a small local **broker daemon** because Telegram allows only one
  `getUpdates` consumer per bot token, while Claude Code spawns one MCP process
  per session. The broker owns the token; the per-session MCP server talks to it
  over loopback.
- Inline Yes/No buttons for quick approvals.

## v3 — breadth (planned)

- Additional channels (Discord, Slack) behind the same broker.
- Per-project routing (different chat/channel per project).
- Richer tools: `notify`, `send_file`, `send_image`.

## Prototype

A complete v2/v3 implementation (broker daemon + hand-rolled MCP stdio server +
multi-channel adapters + per-project routing) already exists on the **`wip/v3-full`**
branch. It passes an offline test suite but has not yet been validated against a
live bot. v1 was extracted as the minimal, ship-now slice.
