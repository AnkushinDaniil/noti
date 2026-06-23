---
name: setup
description: Set up noti — create a Telegram bot, detect your chat ID, and send a test notification.
---

# /noti:setup

Guide the user through connecting noti to their phone. Run the steps interactively;
do not skip the test message at the end.

## 1. Check prerequisites

Confirm `curl` is available (`command -v curl`). `jq` is recommended (richer
messages) but optional.

## 2. Create a Telegram bot

Tell the user, verbatim:

1. Open Telegram and message **@BotFather**.
2. Send `/newbot`.
3. Answer the two prompts: a display name, then a username that ends in `bot`.
4. BotFather replies with a **token** like `123456789:AAH...`. Copy it.

Ask the user to paste the token.

## 3. Save the token

Write `~/.config/noti/config.json` with mode `600` (owner-only). This file is the
source of truth the hook reads:

```bash
mkdir -p ~/.config/noti
umask 177
cat > ~/.config/noti/config.json <<JSON
{ "telegram": { "bot_token": "<TOKEN>", "default_chat_id": "" } }
JSON
chmod 600 ~/.config/noti/config.json
```

## 4. Detect the chat ID

Tell the user to open `t.me/<their_bot_username>` and send it any message (e.g.
`/start`) — Telegram won't reveal the chat ID until they message the bot first.
Then run a one-shot lookup (token read from the file, never echoed):

```bash
TOKEN=$(jq -r '.telegram.bot_token' ~/.config/noti/config.json)
curl -s "https://api.telegram.org/bot${TOKEN}/getUpdates" \
  | jq -r '.result[-1].message.chat.id'
```

Write the printed numeric ID into `default_chat_id` in the config file. If the
result is empty, ask the user to message the bot and retry.

## 5. Send a test message

```bash
TOKEN=$(jq -r '.telegram.bot_token' ~/.config/noti/config.json)
CHAT=$(jq -r '.telegram.default_chat_id' ~/.config/noti/config.json)
curl -s -X POST "https://api.telegram.org/bot${TOKEN}/sendMessage" \
  --data-urlencode "chat_id=${CHAT}" \
  --data-urlencode "text=✅ noti is connected."
```

Ask the user to confirm the message arrived on their phone.

## 6. Activate

Tell the user to **restart Claude Code** (or run `/reload-plugins`) so the
`Notification` and `Stop` hooks take effect. Mention that notification text
transits Telegram's servers.
