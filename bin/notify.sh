#!/usr/bin/env bash
# noti v1 — one-way Telegram notification fired by Claude Code hooks.
#
# Reads the hook event JSON from stdin. $1 is a level label: attention|done|info.
# Sends a Telegram message directly via the Bot API (no daemon, no dependencies
# beyond curl; jq is used for richer text when available). It NEVER fails a Claude
# turn: it always exits 0.
set -uo pipefail

LEVEL="${1:-info}"

# --- read the hook payload from stdin (best effort) ---
INPUT="$(cat 2>/dev/null || true)"

json_field() {  # json_field <key> -> value from $INPUT (needs jq; empty otherwise)
  command -v jq >/dev/null 2>&1 || { printf ''; return; }
  printf '%s' "$INPUT" | jq -r ".$1 // empty" 2>/dev/null
}

CWD="$(json_field cwd)"
MSG="$(json_field message)"
PROJECT="$(basename "${CWD:-$PWD}")"

case "$LEVEL" in
  attention) TEXT="🔔 [$PROJECT] Claude needs you${MSG:+: $MSG}" ;;
  done)      TEXT="✅ [$PROJECT] Claude finished." ;;
  *)         TEXT="ℹ️ [$PROJECT] ${MSG:-notification}" ;;
esac

# --- resolve credentials: plugin userConfig env first, then config file ---
TOKEN="${CLAUDE_PLUGIN_OPTION_BOT_TOKEN:-}"
CHAT="${CLAUDE_PLUGIN_OPTION_CHAT_ID:-}"
CONFIG="${NOTI_CONFIG:-$HOME/.config/noti/config.json}"
if { [ -z "$TOKEN" ] || [ -z "$CHAT" ]; } && [ -f "$CONFIG" ] && command -v jq >/dev/null 2>&1; then
  [ -z "$TOKEN" ] && TOKEN="$(jq -r '.telegram.bot_token // empty' "$CONFIG" 2>/dev/null)"
  [ -z "$CHAT" ]  && CHAT="$(jq -r '.telegram.default_chat_id // empty' "$CONFIG" 2>/dev/null)"
fi

# --- dry-run mode for tests: print intent (token redacted), do not call out ---
if [ "${NOTI_DRY_RUN:-}" = "1" ]; then
  printf 'DRY-RUN sendMessage chat_id=%s text=%s\n' "${CHAT:-<none>}" "$TEXT"
  exit 0
fi

if [ -z "$TOKEN" ] || [ -z "$CHAT" ]; then
  echo "noti: not configured — run /noti:setup" >&2
  exit 0
fi

curl -s -m 10 -X POST "https://api.telegram.org/bot${TOKEN}/sendMessage" \
  --data-urlencode "chat_id=${CHAT}" \
  --data-urlencode "text=${TEXT}" >/dev/null 2>&1 || true

exit 0
