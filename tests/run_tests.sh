#!/usr/bin/env bash
#
# Noti test runner. Runs static checks and offline smoke tests against the
# whole plugin. Exits non-zero on the first failure category encountered, but
# attempts all checks so the output is informative. Requires only python3 and
# bash (stdlib + coreutils); no pip/npm.
#
set -uo pipefail

# Resolve repo root from this script's location.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${HERE}/.." && pwd)"
cd "${ROOT}"

FAIL=0
fail() {
  echo "FAIL: $*" >&2
  FAIL=1
}
ok() {
  echo "ok: $*"
}

# Resolve a working python3. Prefer $PYTHON, then a bare python3, but if that
# bare name is a non-functional wrapper/shim (cannot run `-m py_compile`), fall
# back to common real interpreter locations. Stdlib-only; no pip/uv required.
pick_python() {
  local cand
  for cand in "${PYTHON:-}" python3 /usr/bin/python3 /usr/local/bin/python3 python; do
    [ -z "${cand}" ] && continue
    command -v "${cand}" >/dev/null 2>&1 || continue
    if "${cand}" -c 'import py_compile, json' >/dev/null 2>&1; then
      echo "${cand}"
      return 0
    fi
  done
  # Last resort: return the bare name so the failure is visible.
  echo "${PYTHON:-python3}"
}
PY="$(pick_python)"
echo "using python: ${PY}"

echo "== py_compile =="
PY_FILES=(
  "bin/broker.py"
  "bin/noti_channels.py"
  "bin/noti"
  "server/mcp_server.py"
  "tests/smoke_broker.py"
  "tests/smoke_mcp.py"
)
for f in "${PY_FILES[@]}"; do
  if [ ! -f "${f}" ]; then
    fail "missing python file: ${f}"
    continue
  fi
  if "${PY}" -m py_compile "${f}"; then
    ok "py_compile ${f}"
  else
    fail "py_compile ${f}"
  fi
done

echo "== json validation =="
# Validate every tracked JSON file in the repo.
while IFS= read -r jf; do
  if "${PY}" -m json.tool "${jf}" >/dev/null; then
    ok "json ${jf}"
  else
    fail "json ${jf}"
  fi
done < <(find . -type f -name '*.json' -not -path './.git/*' -not -path './.omc/*' -not -path '*/__pycache__/*')

echo "== bash -n =="
while IFS= read -r sf; do
  if bash -n "${sf}"; then
    ok "bash -n ${sf}"
  else
    fail "bash -n ${sf}"
  fi
done < <(find . -type f -name '*.sh' -not -path './.git/*')

echo "== smoke_broker.py =="
if [ -f "tests/smoke_broker.py" ]; then
  if "${PY}" tests/smoke_broker.py; then
    ok "smoke_broker"
  else
    fail "smoke_broker"
  fi
else
  fail "missing tests/smoke_broker.py"
fi

echo "== smoke_mcp.py =="
if [ -f "tests/smoke_mcp.py" ]; then
  if "${PY}" tests/smoke_mcp.py; then
    ok "smoke_mcp"
  else
    fail "smoke_mcp"
  fi
else
  fail "missing tests/smoke_mcp.py"
fi

if [ "${FAIL}" -ne 0 ]; then
  echo "TESTS FAILED" >&2
  exit 1
fi
echo "ALL TESTS PASSED"
