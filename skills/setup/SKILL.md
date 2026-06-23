---
name: setup
description: >
  Interactive onboarding runbook for noti. Guides the user through creating
  a Telegram bot, writing ~/.config/noti/config.json (chmod 600), detecting
  their chat id, sending a test message, and installing the broker daemon.
---

# /noti:setup — Interactive Onboarding

Walk the user through these steps in order, pausing at each step that requires
their input or action. Do not skip any step.

---

## Step 1 — Check prerequisites

Run the following commands in bash and check each result:

```bash
command -v python3 && python3 --version
command -v curl && curl --version | head -1
command -v jq && jq --version
```

- If **python3** is missing: tell the user to install it (macOS: `brew install python3`; Linux: `sudo apt install python3` or `sudo dnf install python3`). Stop and ask them to re-run `/noti:setup` after installing.
- If **curl** is missing: tell the user to install it (macOS: `brew install curl`; Linux: `sudo apt install curl`). Stop.
- If **jq** is missing: warn the user that jq is strongly recommended (`brew install jq` / `sudo apt install jq`) but notify.sh will degrade gracefully without it. Do not stop — continue.

---

## Step 2 — Create a Telegram bot

Tell the user:

> To create your bot:
> 1. Open Telegram and search for **@BotFather**.
> 2. Send the command `/newbot`.
> 3. BotFather will ask for a **display name** (e.g. "My Noti Bot") — type anything you like.
> 4. BotFather will then ask for a **username** (must end in `bot`, e.g. `my_noti_bot`).
> 5. BotFather will reply with your **bot token** — a string like `123456789:AAH...`.
>
> **Please paste your bot token here.**

Wait for the user to paste the token. Validate that it matches the pattern `^\d+:[A-Za-z0-9_-]{35,}$`. If it does not look like a token, tell the user and ask again.

Store the token as `BOT_TOKEN` for the rest of this skill.

---

## Step 3 — Write config.json

Run the following bash to create the config directory and write the config file:

```bash
mkdir -p ~/.config/noti
python3 - <<'PYEOF'
import json, os, sys

config_path = os.path.expanduser("~/.config/noti/config.json")
bot_token = os.environ.get("NOTI_SETUP_TOKEN", "")

# Load existing config if present, to preserve any existing settings
try:
    with open(config_path) as f:
        cfg = json.load(f)
except Exception:
    cfg = {}

cfg.setdefault("telegram", {})
cfg["telegram"]["bot_token"] = bot_token
cfg.setdefault("channels", {"discord_webhook": "", "slack_webhook": ""})
cfg.setdefault("routing", [])
cfg.setdefault("broker", {"host": "127.0.0.1", "port": 7432})

# Write with permissions 0600
fd = os.open(config_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as f:
    json.dump(cfg, f, indent=2)
    f.write("\n")

print("Config written to", config_path)
PYEOF
```

Set the environment variable `NOTI_SETUP_TOKEN` to the token the user pasted before running, so Python can read it. Confirm that the file was created.

---

## Step 4 — Detect chat ID

Tell the user:

> Now open Telegram and **send any message** (e.g. `/start`) to your new bot: `t.me/<your_bot_username>`.
>
> After you have sent that message, press Enter here.

Once they confirm, run this one-shot getUpdates call to find the chat ID:

```bash
RESULT=$(curl -s "https://api.telegram.org/bot${BOT_TOKEN}/getUpdates")
echo "$RESULT"
```

Parse the result:
- Extract `.result[0].message.chat.id` (or `.result[-1].message.chat.id` if multiple results).
- If found, store as `CHAT_ID` and tell the user: "Your chat ID is `<CHAT_ID>`."
- If the result array is empty, tell the user they may not have sent a message to the bot yet, and ask them to do so, then confirm again. Retry the curl once more.
- If still empty, ask the user to enter their chat ID manually (they can use `@userinfobot` to find it).

Once you have the chat ID, update the config:

```bash
python3 - <<'PYEOF'
import json, os

config_path = os.path.expanduser("~/.config/noti/config.json")
chat_id = os.environ.get("NOTI_SETUP_CHAT_ID", "")

with open(config_path) as f:
    cfg = json.load(f)

cfg["telegram"]["default_chat_id"] = chat_id

fd = os.open(config_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as f:
    json.dump(cfg, f, indent=2)
    f.write("\n")

print("Chat ID saved.")
PYEOF
```

Set `NOTI_SETUP_CHAT_ID` to the detected/entered chat ID before running.

---

## Step 5 — Send a test message

Run:

```bash
curl -s -X POST "https://api.telegram.org/bot${BOT_TOKEN}/sendMessage" \
  -H 'Content-Type: application/json' \
  -d "{\"chat_id\":\"${CHAT_ID}\",\"text\":\"✅ noti is working! You will receive alerts here.\"}"
```

Ask the user: "Did you receive the test message on your phone?" If yes, continue. If no, tell them to double-check the bot token and chat ID, and offer to restart from Step 2.

---

## Step 6 — Install the broker daemon

Tell the user:

> The broker daemon is required for the `ask_user` MCP tool (answer from phone). It runs in the background and manages Telegram polling.

Run:

```bash
"${CLAUDE_PLUGIN_ROOT}/bin/install-broker.sh"
```

If the script succeeds, tell the user the broker is now installed and will start automatically on login.

If it fails (non-zero exit), show the error output and tell the user they can run the broker manually for now:

```bash
python3 "${CLAUDE_PLUGIN_ROOT}/bin/broker.py" &
```

---

## Step 7 — Final steps

Tell the user:

> **Setup complete!**
>
> To activate the hooks and MCP server:
> - **Restart Claude Code** (quit and relaunch), or run `/reload-plugins` if available.
>
> **What happens next:**
> - When Claude needs your permission, you will get a 🔔 notification on your phone.
> - When Claude finishes a task, you will get a ✅ message.
> - You can answer Claude's questions from your phone using the `ask_user` MCP tool.
>
> **Privacy note:** Your messages transit Telegram's servers. The bot token and chat ID are
> stored locally in `~/.config/noti/config.json` (chmod 600). The broker only listens on
> loopback (127.0.0.1). For extra privacy, open @BotFather → `/setprivacy` → Enable
> (prevents the bot from seeing group messages it isn't addressed to).
>
> **To uninstall:** run `"${CLAUDE_PLUGIN_ROOT}/bin/uninstall-broker.sh"`, then remove the plugin.
>
> **Re-running setup** after a plugin update is required to refresh the broker service path.
