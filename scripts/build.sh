#!/usr/bin/env bash
# build.sh — build the noti binary.
#
# Usage:
#   scripts/build.sh              # builds to bin/noti (dev)
#   OUT=/path/to/noti scripts/build.sh   # builds to custom path
#   OUT="$CLAUDE_PLUGIN_DATA/bin/noti" scripts/build.sh   # setup target
#
# Requires: go 1.23+
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

OUT="${OUT:-${REPO_ROOT}/bin/noti}"
OUT_DIR="$(dirname "${OUT}")"
mkdir -p "${OUT_DIR}"

printf 'building noti -> %s\n' "${OUT}"
cd "${REPO_ROOT}"
go build -o "${OUT}" .
chmod +x "${OUT}"
printf 'done: %s\n' "${OUT}"
