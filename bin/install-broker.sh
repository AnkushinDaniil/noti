#!/usr/bin/env bash
# install-broker.sh — install + start the noti broker as a background service.
#
# macOS  -> launchd LaunchAgent  ~/Library/LaunchAgents/com.noti.broker.plist
# Linux  -> systemd --user unit  ~/.config/systemd/user/noti-broker.service
#
# The broker must run continuously so the MCP tool ask_user can collect phone
# replies (it is the single Telegram getUpdates owner). Because CLAUDE_PLUGIN_ROOT
# changes on every plugin update, you MUST re-run this script (or /noti:setup)
# after updating the plugin.
set -euo pipefail

LABEL="com.noti.broker"
SERVICE="noti-broker"

err() { printf 'install-broker: %s\n' "$*" >&2; }
info() { printf 'install-broker: %s\n' "$*"; }

# Resolve the plugin root: prefer CLAUDE_PLUGIN_ROOT, else derive from this script.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_ROOT="${CLAUDE_PLUGIN_ROOT:-$(cd "${SCRIPT_DIR}/.." && pwd)}"
BROKER_PY="${PLUGIN_ROOT}/bin/broker.py"

if [ ! -f "${BROKER_PY}" ]; then
  err "broker.py not found at ${BROKER_PY}"
  exit 1
fi

# Resolve a usable python3.
PYTHON_BIN="$(command -v python3 || true)"
if [ -z "${PYTHON_BIN}" ]; then
  err "python3 not found in PATH"
  exit 1
fi

# Resolve the data dir (survives updates). Falls back like the broker does.
DATA_DIR="${CLAUDE_PLUGIN_DATA:-${HOME}/.local/state/noti}"
mkdir -p "${DATA_DIR}"
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
    <string>${PYTHON_BIN}</string>
    <string>${BROKER_PY}</string>
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
  info "broker started. Check: noti status"
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
ExecStart=${PYTHON_BIN} ${BROKER_PY}
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
  info "broker started. Check: noti status   (or: systemctl --user status ${SERVICE})"
}

case "${OS}" in
  Darwin) install_macos ;;
  Linux)  install_linux ;;
  *)
    err "unsupported OS: ${OS}. Run the broker manually: ${PYTHON_BIN} ${BROKER_PY}"
    exit 1
    ;;
esac

info "NOTE: CLAUDE_PLUGIN_ROOT changes on each plugin update."
info "Re-run this script (or /noti:setup) after updating the noti plugin."
