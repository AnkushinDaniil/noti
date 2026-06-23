#!/usr/bin/env bash
# mcp.sh — thin locator wrapper that launches the noti MCP stdio server.
#
# Declared as the MCP server command in .mcp.json. Resolves the noti binary the
# same way notify.sh does, then exec's `noti mcp` (stdin/stdout are the JSON-RPC
# channel, so we exec to stay transparent).
#
# Binary search order:
#   1. $CLAUDE_PLUGIN_DATA/bin/noti   (downloaded by scripts/fetch-binary.sh)
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
  echo "[noti] mcp.sh: noti binary not found — run /noti:setup (downloads the binary)" >&2
  exit 1
fi

exec "${NOTI_BIN}" mcp
