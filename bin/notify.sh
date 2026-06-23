#!/usr/bin/env bash
# notify.sh — thin locator wrapper for the noti binary hook notifier.
#
# Usage: notify.sh <level>   (level = attention | done | info)
# Reads hook JSON from stdin. Always exits 0.
#
# Binary search order:
#   1. $CLAUDE_PLUGIN_DATA/bin/noti   (installed by scripts/build.sh)
#   2. $CLAUDE_PLUGIN_ROOT/bin/noti   (dev checkout)
#   3. noti  on $PATH
set -uo pipefail

LEVEL="${1:-info}"

NOTI_BIN=""

if [ -n "${CLAUDE_PLUGIN_DATA:-}" ] && [ -x "${CLAUDE_PLUGIN_DATA}/bin/noti" ]; then
  NOTI_BIN="${CLAUDE_PLUGIN_DATA}/bin/noti"
elif [ -n "${CLAUDE_PLUGIN_ROOT:-}" ] && [ -x "${CLAUDE_PLUGIN_ROOT}/bin/noti" ]; then
  NOTI_BIN="${CLAUDE_PLUGIN_ROOT}/bin/noti"
elif command -v noti >/dev/null 2>&1; then
  NOTI_BIN="noti"
fi

if [ -z "${NOTI_BIN}" ]; then
  printf '[noti] notify.sh: noti binary not found — run scripts/build.sh and bin/install-broker.sh\n' >&2
  exit 0
fi

exec "${NOTI_BIN}" notify "${LEVEL}"
