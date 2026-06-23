# noti — Phone Notifications for Claude Code

Get a **Telegram notification on your phone** when Claude Code pauses for a
permission prompt or finishes a task. No more babysitting the terminal.

> **v1** is one-way (notify only) and intentionally tiny: just `bash` + `curl`,
> no daemon, no dependencies. Two-way "answer from your phone" is on the
> [roadmap](docs/ROADMAP.md) (prototype on the `wip/v3-full` branch).

## Prerequisites

- **curl** — sends the notification
- **jq** *(optional)* — adds the project name and prompt text to messages; without
  it you still get a generic alert

## Install

```
/plugin marketplace add AnkushinDaniil/noti
/plugin install noti@noti-marketplace
/noti:setup
```

`/noti:setup` walks you through creating a Telegram bot via @BotFather, detecting
your chat ID, and sending a test message. After setup, **restart Claude Code**
(or run `/reload-plugins`) to activate the hooks.

## How it works

```
Claude Code event                       hooks/hooks.json
  ├─ Notification (permission_prompt) ──► bin/notify.sh attention ──► Telegram ──► 📱
  └─ Stop (turn finished) ─────────────► bin/notify.sh done       ──► Telegram ──► 📱
```

`bin/notify.sh` reads the hook's JSON from stdin, builds a short message, and
POSTs it to the Telegram Bot API. It always exits 0, so a missing config or a
network blip can never block or break a Claude turn.

| Hook | Matcher | Message |
|------|---------|---------|
| `Notification` | `permission_prompt` | 🔔 Claude needs your permission |
| `Stop` | (any) | ✅ Claude finished |

> `idle_prompt` is intentionally **not** matched — it false-fires after every
> turn. `Stop` already covers "finished".

## Configuration — `~/.config/noti/config.json`

Written by `/noti:setup` with **chmod 600**. Never commit it.

```json
{ "telegram": { "bot_token": "123456789:AAH...", "default_chat_id": "987654321" } }
```

`notify.sh` resolves credentials in this order:

1. `CLAUDE_PLUGIN_OPTION_BOT_TOKEN` / `CLAUDE_PLUGIN_OPTION_CHAT_ID` (from the
   plugin's `userConfig`)
2. `~/.config/noti/config.json` (override the path with `NOTI_CONFIG`)

## Security

- The config file is `chmod 600` (owner-only); the bot token is never logged.
- Messages transit Telegram's servers. For extra privacy, in @BotFather run
  `/setprivacy → Enable`.
- You can revoke or rotate the token any time via @BotFather (`/revoke`).

## Development

```bash
bash tests/run_tests.sh   # JSON validation, shell syntax, notify.sh dry-run — offline
```

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT — see [LICENSE](LICENSE).
