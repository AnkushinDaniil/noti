---
name: setup
description: >
  Interactive onboarding runbook for noti. Guides the user through creating
  a Telegram bot, writing ~/.config/noti/config.json (chmod 600), detecting
  their chat id, sending a test message, building the Go binary, and
  installing the broker daemon.
---

# /noti:setup — Interactive Onboarding

Walk the user through these steps in order, pausing at each step that requires
their input or action. Do not skip any step.

---

## Step 1 — Check prerequisites

Run the following commands in bash and check each result:

```bash
command -v go && go version
command -v curl && curl --version | head -1
```

- If **go** is missing: tell the user to install Go 1.23+ from https://go.dev/dl/
  (macOS: `brew install go`; Linux: follow the official installer). Stop and ask
  them to re-run `/noti:setup` after installing.
- If **curl** is missing: tell the user to install it (macOS: `brew install curl`;
  Linux: `sudo apt install curl`). Stop.

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

Wait for the user to paste the token. Validate that it matches the pattern
`^\d+:[A-Za-z0-9_-]{35,}$`. If it does not look like a token, tell the user and ask again.

Store the token as `BOT_TOKEN` for the rest of this skill.

---

## Step 3 — Write config.json

Create the config directory and write a minimal config file:

```bash
mkdir -p ~/.config/noti
CONFIG_PATH="${NOTI_CONFIG:-${HOME}/.config/noti/config.json}"

# Build JSON with the token (using printf to avoid jq/python dependency).
cat > /tmp/noti_cfg_tmp.json <<CFGJSON
{
  "telegram": {
    "bot_token": "${BOT_TOKEN}",
    "default_chat_id": ""
  },
  "channels": { "discord_webhook": "", "slack_webhook": "" },
  "routing": [],
  "broker": { "host": "127.0.0.1", "port": 7432 }
}
CFGJSON

cp /tmp/noti_cfg_tmp.json "${CONFIG_PATH}"
chmod 600 "${CONFIG_PATH}"
rm /tmp/noti_cfg_tmp.json
echo "Config written to ${CONFIG_PATH}"
```

---

## Step 4 — Build the noti binary

Build the static Go binary and install it into the data directory:

```bash
DATA_DIR="${CLAUDE_PLUGIN_DATA:-${HOME}/.local/state/noti}"
export OUT="${DATA_DIR}/bin/noti"
bash "${CLAUDE_PLUGIN_ROOT}/scripts/build.sh"
```

If the build fails, show the error and tell the user to check that Go 1.23+ is installed and `go env GOROOT` is set.

---

## Step 5 — Detect chat ID

Tell the user:

> Now open Telegram and **send any message** (e.g. `/start`) to your new bot: `t.me/<your_bot_username>`.
>
> After you have sent that message, press Enter here.

Once they confirm, run:

```bash
"${CLAUDE_PLUGIN_DATA:-${HOME}/.local/state/noti}/bin/noti" detect-chat
```

- If a chat ID is printed, store it as `CHAT_ID` and tell the user: "Your chat ID is `<CHAT_ID>`."
- If nothing is printed, tell the user they may not have sent a message to the bot yet, ask them to do so, and try again.
- If still empty, ask the user to enter their chat ID manually (they can use `@userinfobot`).

Once you have the chat ID, update the config:

```bash
CONFIG_PATH="${NOTI_CONFIG:-${HOME}/.config/noti/config.json}"
# Use noti itself to avoid external dependencies — write config manually.
# Read existing config and update default_chat_id using bash/sed fallback.
sed -i.bak "s/\"default_chat_id\": \"\"/\"default_chat_id\": \"${CHAT_ID}\"/" "${CONFIG_PATH}"
rm -f "${CONFIG_PATH}.bak"
echo "Chat ID saved."
```

---

## Step 6 — Send a test message

```bash
"${CLAUDE_PLUGIN_DATA:-${HOME}/.local/state/noti}/bin/noti" test "✅ noti is working!"
```

Ask the user: "Did you receive the test message on your phone?" If yes, continue.
If no, tell them to double-check the bot token and chat ID, and offer to restart from Step 2.

---

## Step 7 — Install the broker daemon

Tell the user:

> The broker daemon is required for the `ask_user` MCP tool (answer from phone). It runs in the
> background and manages Telegram polling.

Run:

```bash
"${CLAUDE_PLUGIN_ROOT}/bin/install-broker.sh"
```

If the script succeeds, tell the user the broker is now installed and will start automatically on login.

If it fails (non-zero exit), show the error output and tell the user they can run the broker manually:

```bash
"${CLAUDE_PLUGIN_DATA:-${HOME}/.local/state/noti}/bin/noti" broker &
```

---

## Step 8 — Final steps

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
> **Re-running setup** after a plugin update rebuilds the binary and refreshes the broker service path.
