#!/usr/bin/env bash
# uninstall-broker.sh — stop + remove the noti broker background service.
#
# macOS  -> unload + remove the launchd LaunchAgent plist.
# Linux  -> disable + stop + remove the systemd --user unit.
set -euo pipefail

LABEL="com.noti.broker"
SERVICE="noti-broker"

err()  { printf 'uninstall-broker: %s\n' "$*" >&2; }
info() { printf 'uninstall-broker: %s\n' "$*"; }

OS="$(uname -s)"

uninstall_macos() {
  local plist="${HOME}/Library/LaunchAgents/${LABEL}.plist"
  if [ -f "${plist}" ]; then
    launchctl unload "${plist}" >/dev/null 2>&1 || true
    rm -f "${plist}"
    info "removed launchd agent: ${plist}"
  else
    info "no launchd agent found at ${plist}"
  fi
}

uninstall_linux() {
  local unit_dir="${HOME}/.config/systemd/user"
  local unit="${unit_dir}/${SERVICE}.service"
  systemctl --user disable --now "${SERVICE}.service" >/dev/null 2>&1 || true
  if [ -f "${unit}" ]; then
    rm -f "${unit}"
    systemctl --user daemon-reload >/dev/null 2>&1 || true
    info "removed systemd user unit: ${unit}"
  else
    info "no systemd unit found at ${unit}"
  fi
}

case "${OS}" in
  Darwin) uninstall_macos ;;
  Linux)  uninstall_linux ;;
  *)
    err "unsupported OS: ${OS}. Stop any manual broker process yourself."
    exit 1
    ;;
esac

# Best-effort: remove the stale lockfile so a future start is clean.
DATA_DIR="${CLAUDE_PLUGIN_DATA:-${HOME}/.local/state/noti}"
if [ -f "${DATA_DIR}/broker.lock" ]; then
  rm -f "${DATA_DIR}/broker.lock"
  info "removed stale lockfile: ${DATA_DIR}/broker.lock"
fi

info "broker uninstalled. (Logs and offset preserved in ${DATA_DIR}.)"
