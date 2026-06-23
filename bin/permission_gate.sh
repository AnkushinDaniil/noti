#!/usr/bin/env bash
# permission_gate.sh — thin locator wrapper for the noti permission-gate.
#
# Wired to Claude Code's PreToolUse hook. Reads the hook JSON from stdin and
# writes a permission decision to stdout. Always exits 0: if the binary is
# missing, it emits a pass-through ("ask") decision so the tool is never
# blocked and the normal terminal permission prompt applies.
#
# Binary search order:
#   1. $CLAUDE_PLUGIN_DATA/bin/noti   (installed by scripts/build.sh)
#   2. $CLAUDE_PLUGIN_ROOT/bin/noti   (dev checkout)
#   3. noti  on $PATH
set -uo pipefail

NOTI_BIN=""

if [ -n "${CLAUDE_PLUGIN_DATA:-}" ] && [ -x "${CLAUDE_PLUGIN_DATA}/bin/noti" ]; then
  NOTI_BIN="${CLAUDE_PLUGIN_DATA}/bin/noti"
elif [ -n "${CLAUDE_PLUGIN_ROOT:-}" ] && [ -x "${CLAUDE_PLUGIN_ROOT}/bin/noti" ]; then
  NOTI_BIN="${CLAUDE_PLUGIN_ROOT}/bin/noti"
elif command -v noti >/dev/null 2>&1; then
  NOTI_BIN="noti"
fi

if [ -z "${NOTI_BIN}" ]; then
  # Binary missing: pass through to the normal permission flow.
  printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask"}}\n'
  exit 0
fi

exec "${NOTI_BIN}" permission-gate
