#!/usr/bin/env bash
# notify.sh — fire-and-forget hook notifier for noti
# Usage: notify.sh <level>   (level = attention | done | info)
# Reads hook JSON from stdin. Always exits 0.
set -uo pipefail

LEVEL="${1:-info}"
BROKER_URL="${NOTI_BROKER_URL:-http://127.0.0.1:7432}"

# Read all of stdin (hook JSON payload)
STDIN_DATA="$(cat)"

# ---------------------------------------------------------------------------
# Parse stdin with jq if available; degrade gracefully if not.
# ---------------------------------------------------------------------------
PROJECT=""
MESSAGE=""

if command -v jq >/dev/null 2>&1; then
    PROJECT="$(printf '%s' "$STDIN_DATA" | jq -r '(.cwd // "") | split("/") | last' 2>/dev/null || true)"
    MESSAGE="$(printf '%s' "$STDIN_DATA" | jq -r '.message // ""' 2>/dev/null || true)"
    HOOK_EVENT="$(printf '%s' "$STDIN_DATA" | jq -r '.hook_event_name // ""' 2>/dev/null || true)"
else
    # No jq: extract cwd/message with basic shell tools (best-effort)
    PROJECT="$(printf '%s' "$STDIN_DATA" | grep -o '"cwd"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/.*: *"//' | sed 's/".*//' | awk -F/ '{print $NF}' 2>/dev/null || true)"
    MESSAGE="$(printf '%s' "$STDIN_DATA" | grep -o '"message"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/.*: *"//' | sed 's/".*//' 2>/dev/null || true)"
    HOOK_EVENT=""
fi

# Build a human-friendly text
if [ "$LEVEL" = "done" ]; then
    if [ -n "$PROJECT" ]; then
        TEXT="✅ [$PROJECT] Claude finished"
    else
        TEXT="✅ Claude finished"
    fi
elif [ "$LEVEL" = "attention" ]; then
    if [ -n "$MESSAGE" ] && [ -n "$PROJECT" ]; then
        TEXT="🔔 [$PROJECT] $MESSAGE"
    elif [ -n "$MESSAGE" ]; then
        TEXT="🔔 $MESSAGE"
    elif [ -n "$PROJECT" ]; then
        TEXT="🔔 [$PROJECT] Claude needs your attention"
    else
        TEXT="🔔 Claude needs your attention"
    fi
else
    if [ -n "$MESSAGE" ] && [ -n "$PROJECT" ]; then
        TEXT="ℹ️ [$PROJECT] $MESSAGE"
    elif [ -n "$MESSAGE" ]; then
        TEXT="ℹ️ $MESSAGE"
    else
        TEXT="ℹ️ Claude notification"
    fi
fi

# Escape text for JSON (replace backslash then double-quote then newline/tab)
escape_json() {
    printf '%s' "$1" \
        | sed 's/\\/\\\\/g' \
        | sed 's/"/\\"/g' \
        | sed 's/'"$(printf '\t')"'/\\t/g' \
        | tr -d '\n\r'
}

TEXT_ESC="$(escape_json "$TEXT")"
LEVEL_ESC="$(escape_json "$LEVEL")"
PROJECT_ESC="$(escape_json "$PROJECT")"

NOTIFY_JSON="{\"text\":\"${TEXT_ESC}\",\"level\":\"${LEVEL_ESC}\",\"project\":\"${PROJECT_ESC}\"}"

# ---------------------------------------------------------------------------
# Step 1: Try broker /notify (5-second timeout)
# ---------------------------------------------------------------------------
BROKER_OK=0
if command -v curl >/dev/null 2>&1; then
    HTTP_STATUS="$(curl -s -o /dev/null -w "%{http_code}" -m 5 \
        -X POST "${BROKER_URL}/notify" \
        -H 'Content-Type: application/json' \
        -d "$NOTIFY_JSON" 2>/dev/null || true)"
    if [ "$HTTP_STATUS" = "200" ]; then
        BROKER_OK=1
    fi
fi

# ---------------------------------------------------------------------------
# Step 2: Fallback — direct Telegram sendMessage if broker unreachable
# ---------------------------------------------------------------------------
if [ "$BROKER_OK" = "0" ] && command -v curl >/dev/null 2>&1; then
    # Token resolution order: env → config.json
    BOT_TOKEN="${CLAUDE_PLUGIN_OPTION_BOT_TOKEN:-}"
    CHAT_ID="${CLAUDE_PLUGIN_OPTION_CHAT_ID:-}"

    # If env is incomplete, read config.json. Prefer jq (no Python needed); fall
    # back to python3 only if jq is unavailable. Using bare `python3` as the sole
    # parser is fragile — on some setups it is a wrapper/shim that refuses to run.
    if [ -z "$BOT_TOKEN" ] || [ -z "$CHAT_ID" ]; then
        CONFIG_PATH="${NOTI_CONFIG:-${HOME}/.config/noti/config.json}"
        if [ -r "$CONFIG_PATH" ]; then
            if command -v jq >/dev/null 2>&1; then
                [ -z "$BOT_TOKEN" ] && BOT_TOKEN="$(jq -r '.telegram.bot_token // empty' "$CONFIG_PATH" 2>/dev/null || true)"
                [ -z "$CHAT_ID" ]  && CHAT_ID="$(jq -r '.telegram.default_chat_id // empty' "$CONFIG_PATH" 2>/dev/null || true)"
            elif command -v python3 >/dev/null 2>&1; then
                [ -z "$BOT_TOKEN" ] && BOT_TOKEN="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("telegram",{}).get("bot_token",""))' "$CONFIG_PATH" 2>/dev/null || true)"
                [ -z "$CHAT_ID" ]  && CHAT_ID="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("telegram",{}).get("default_chat_id",""))' "$CONFIG_PATH" 2>/dev/null || true)"
            fi
        fi
    fi

    if [ -n "$BOT_TOKEN" ] && [ -n "$CHAT_ID" ]; then
        CHAT_ID_ESC="$(escape_json "$CHAT_ID")"
        TG_JSON="{\"chat_id\":\"${CHAT_ID_ESC}\",\"text\":\"${TEXT_ESC}\"}"
        curl -s -o /dev/null -m 10 \
            -X POST "https://api.telegram.org/bot${BOT_TOKEN}/sendMessage" \
            -H 'Content-Type: application/json' \
            -d "$TG_JSON" 2>/dev/null || true
    else
        printf '[noti] notify.sh: broker unreachable and no credentials available for fallback\n' >&2
    fi
fi

# Always exit 0 — never break a Claude turn
exit 0
