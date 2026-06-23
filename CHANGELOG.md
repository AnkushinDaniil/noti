# Changelog

All notable changes to this project are documented here. This project adheres to
[Semantic Versioning](https://semver.org/).

## [1.0.0] — 2026-06-23

Initial public release.

### Added

- **One-way notifications** via Claude Code hooks: `Notification`
  (`permission_prompt`) and `Stop`, delivered to the phone through `bin/notify.sh`.
- **Two-way "answer from phone"** via an MCP server (`server/mcp_server.py`,
  hand-rolled JSON-RPC 2.0 over stdio) exposing `ask_user`, `wait_for_reply`,
  `notify`, `send_file`, and `send_image`.
- **Broker daemon** (`bin/broker.py`): the single `getUpdates` consumer, loopback
  HTTP API (`/health`, `/notify`, `/ask`, `/wait`, `/send_file`), thread-safe
  ticket registry, offset persistence, and a PID-lockfile singleton guard.
- **Multi-channel delivery**: Telegram (two-way, inline buttons), Discord and
  Slack (one-way webhooks).
- **Per-project routing** in `~/.config/noti/config.json` by project name or path
  glob.
- **Onboarding**: `/noti:setup` skill and `bin/install-broker.sh` /
  `bin/uninstall-broker.sh` for launchd (macOS) and systemd `--user` (Linux).
- **Tests**: offline smoke tests for the broker and the MCP handshake
  (`tests/run_tests.sh`).
- Documentation: README, `docs/ARCHITECTURE.md`, `CONTRIBUTING.md`.

### Notes

- Zero runtime dependencies — Python 3.9+ standard library and `bash`/`curl` only.
- Single-tenant by design: each user runs their own Telegram bot.
