#!/usr/bin/env bash
# install-broker.sh — install + start the noti broker as a background service.
#
# macOS  -> launchd LaunchAgent  ~/Library/LaunchAgents/com.noti.broker.plist
# Linux  -> systemd --user unit  ~/.config/systemd/user/noti-broker.service
#
# The broker must run continuously so the MCP tool ask_user can collect phone
# replies (it is the single Telegram getUpdates owner). Re-run this script (or
# /noti:setup) after updating the plugin so the service path stays current.
set -euo pipefail

LABEL="com.noti.broker"
SERVICE="noti-broker"

err()  { printf 'install-broker: %s\n' "$*" >&2; }
info() { printf 'install-broker: %s\n' "$*"; }

# Resolve the plugin root: prefer CLAUDE_PLUGIN_ROOT, else derive from this script.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_ROOT="${CLAUDE_PLUGIN_ROOT:-$(cd "${SCRIPT_DIR}/.." && pwd)}"

# Resolve the installed noti binary.
DATA_DIR="${CLAUDE_PLUGIN_DATA:-${HOME}/.local/state/noti}"
mkdir -p "${DATA_DIR}"

NOTI_BIN="${DATA_DIR}/bin/noti"
if [ ! -x "${NOTI_BIN}" ]; then
  # Fall back to a binary built inside the repo checkout.
  NOTI_BIN="${PLUGIN_ROOT}/bin/noti"
fi
if [ ! -x "${NOTI_BIN}" ]; then
  info "noti binary not found — fetching it…"
  if [ -x "${PLUGIN_ROOT}/scripts/fetch-binary.sh" ]; then
    OUT="${DATA_DIR}/bin/noti" bash "${PLUGIN_ROOT}/scripts/fetch-binary.sh" || true
  fi
  NOTI_BIN="${DATA_DIR}/bin/noti"
fi
if [ ! -x "${NOTI_BIN}" ]; then
  err "noti binary not found and could not be fetched."
  err "Run ${PLUGIN_ROOT}/scripts/fetch-binary.sh (or scripts/build.sh with Go installed)."
  exit 1
fi

LOG_FILE="${DATA_DIR}/broker.log"
OS="$(uname -s)"

install_macos() {
  local plist="${HOME}/Library/LaunchAgents/${LABEL}.plist"
  mkdir -p "${HOME}/Library/LaunchAgents"

  # Unload any existing instance first (ignore errors).
  launchctl unload "${plist}" >/dev/null 2>&1 || true

  cat > "${plist}" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${NOTI_BIN}</string>
    <string>broker</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>CLAUDE_PLUGIN_ROOT</key>
    <string>${PLUGIN_ROOT}</string>
    <key>CLAUDE_PLUGIN_DATA</key>
    <string>${DATA_DIR}</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>${LOG_FILE}</string>
  <key>StandardErrorPath</key>
  <string>${LOG_FILE}</string>
</dict>
</plist>
PLIST

  launchctl load -w "${plist}"
  info "installed launchd agent: ${plist}"
  info "logs: ${LOG_FILE}"
  info "broker started. Verify: noti version"
}

install_linux() {
  local unit_dir="${HOME}/.config/systemd/user"
  local unit="${unit_dir}/${SERVICE}.service"
  mkdir -p "${unit_dir}"

  cat > "${unit}" <<UNIT
[Unit]
Description=noti broker (Telegram getUpdates owner + loopback API)
After=network-online.target

[Service]
Type=simple
ExecStart=${NOTI_BIN} broker
Environment=CLAUDE_PLUGIN_ROOT=${PLUGIN_ROOT}
Environment=CLAUDE_PLUGIN_DATA=${DATA_DIR}
Restart=always
RestartSec=3
StandardOutput=append:${LOG_FILE}
StandardError=append:${LOG_FILE}

[Install]
WantedBy=default.target
UNIT

  systemctl --user daemon-reload
  systemctl --user enable --now "${SERVICE}.service"
  info "installed systemd user unit: ${unit}"
  info "logs: ${LOG_FILE}"
  info "broker started. Verify: systemctl --user status ${SERVICE}"
}

case "${OS}" in
  Darwin) install_macos ;;
  Linux)  install_linux ;;
  *)
    err "unsupported OS: ${OS}. Run the broker manually: ${NOTI_BIN} broker &"
    exit 1
    ;;
esac

info "NOTE: Re-run this script after updating the noti plugin."
