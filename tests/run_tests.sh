#!/usr/bin/env bash
# noti v1 offline test suite: JSON validation, shell syntax, and a dry-run of
# notify.sh. No network, no Telegram bot required.
set -uo pipefail
cd "$(dirname "$0")/.."

FAILED=0
ok()   { echo "ok: $*"; }
fail() { echo "FAIL: $*" >&2; FAILED=1; }

# Pick a working python for JSON validation (some machines shim bare `python3`).
PY=""
for c in "${PYTHON:-}" python3 /usr/bin/python3 /usr/local/bin/python3 python; do
  [ -n "$c" ] || continue
  command -v "$c" >/dev/null 2>&1 || continue
  "$c" -c 'import json' >/dev/null 2>&1 && { PY="$c"; break; }
done

echo "== json validation =="
for f in .claude-plugin/plugin.json .claude-plugin/marketplace.json hooks/hooks.json; do
  if [ -n "$PY" ]; then
    "$PY" -m json.tool "$f" >/dev/null 2>&1 && ok "json $f" || fail "json $f"
  elif command -v jq >/dev/null 2>&1; then
    jq . "$f" >/dev/null 2>&1 && ok "json $f" || fail "json $f"
  else
    echo "skip: json $f (no python or jq)"
  fi
done

echo "== bash -n =="
for f in bin/notify.sh tests/run_tests.sh; do
  bash -n "$f" && ok "bash -n $f" || fail "bash -n $f"
done

echo "== notify.sh dry-run =="
OUT="$(printf '%s' '{"hook_event_name":"Stop","cwd":"/tmp/myproj","message":""}' \
  | NOTI_DRY_RUN=1 CLAUDE_PLUGIN_OPTION_BOT_TOKEN=SECRET123 CLAUDE_PLUGIN_OPTION_CHAT_ID=42 \
    bash bin/notify.sh done 2>&1)"
echo "  $OUT"
case "$OUT" in
  *"chat_id=42"*) ok "dry-run routes to chat_id" ;;
  *)              fail "dry-run missing chat_id: $OUT" ;;
esac
case "$OUT" in
  *"✅"*) ok "dry-run uses the 'done' label" ;;
  *)      fail "dry-run wrong label: $OUT" ;;
esac
case "$OUT" in
  *SECRET123*) fail "bot token leaked into dry-run output" ;;
  *)           ok "bot token not leaked" ;;
esac

if [ "$FAILED" = "0" ]; then
  echo "ALL TESTS PASSED"
  exit 0
fi
echo "TESTS FAILED"
exit 1
