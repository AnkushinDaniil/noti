# Changelog

All notable changes to this project are documented here. This project adheres to
[Semantic Versioning](https://semver.org/).

## [2.0.0] - unreleased

Rewrote noti in Go as a single static binary (was Python+bash). Feature parity
with 1.0.0. Adds the `ask` config block (modes wired in a later step).

### Added

- **Single static Go binary** (`noti`) with subcommands: `broker`, `mcp`,
  `notify <level>`, `detect-chat`, `test`, `version`, `help`.
- **`internal/broker`**: Go broker daemon replacing `bin/broker.py`. Same HTTP
  API (`/health`, `/notify`, `/ask`, `/wait`, `/cancel`, `/config`), same
  loopback singleton with PID lockfile and offset persistence.
- **`internal/mcp`**: Go MCP stdio server replacing `server/mcp_server.py`.
  Same 5 tools (`ask_user`, `wait_for_reply`, `notify`, `send_file`,
  `send_image`). Stores client elicitation capability flag for Step 2.
- **`internal/notify`**: `noti notify <level>` replaces the inline logic in
  `bin/notify.sh`. Reads hook JSON from stdin, POSTs to broker, falls back to
  direct Telegram send. Always exits 0.
- **`internal/config`**: typed config with `ResolveTarget`, `ResolveAsk`, and
  full per-project routing.
- **`internal/telegram`**: stdlib-only Telegram Bot API client with test mode
  (no network; records sends to `Outbox`).
- **`internal/version`**: `const Version = "2.0.0"`.
- **`ask` config block**: `mode` (`timeout` / `forward-all`), `idle_timeout_seconds`,
  `laptop`, `require_laptop`, `permissions`. Broker exposes `/config` to read
  per-project resolved ask config. Modes and permission gate activate in Step 2.
- **`scripts/build.sh`**: cross-platform build script (`go build -o bin/noti .`).
- **CI**: GitHub Actions test matrix (ubuntu/macos, go vet + gofmt + go test)
  and cross-compile matrix (darwin/linux × amd64/arm64, uploaded as artifacts).
- **`bin/notify.sh`**: simplified to a thin binary locator (`exec noti notify <level>`).
- **`bin/install-broker.sh`** and **`bin/uninstall-broker.sh`**: updated to run
  `<binary> broker` instead of `python3 broker.py`.

### Removed

- `bin/broker.py`, `bin/noti_channels.py`, `bin/noti` (Python entrypoint),
  `server/mcp_server.py`, `tests/smoke_broker.py`, `tests/smoke_mcp.py`,
  `tests/run_tests.sh`, `pyrightconfig.json`.

### Notes

- Zero runtime dependencies at runtime: a single Go binary + outbound HTTPS to
  `api.telegram.org`. No Python, pip, npm, or external libraries.
- Build requires Go 1.23+; pre-built release binaries arrive in Step 3.
- Single-tenant by design: each user runs their own Telegram bot.

---

## [1.0.0] — 2026-06-23

Initial public release.

### Added

- **One-way notifications** via Claude Code hooks: `Notification`
  (`permission_prompt`) and `Stop`, delivered to the phone through `bin/notify.sh`.
- **Two-way "answer from phone"** via an MCP server (`server/mcp_server.py`,
  hand-rolled JSON-RPC 2.0 over stdio) exposing `ask_user`, `wait_for_reply`,
  `notify`, `send_file`, and `send_image`.
- **Broker daemon** (`bin/broker.py`): the single `getUpdates` consumer, loopback
  HTTP API, thread-safe ticket registry, offset persistence, PID-lockfile singleton.
- **Per-project routing** in `~/.config/noti/config.json` by project name or path glob.
- **Onboarding**: `/noti:setup` skill, `bin/install-broker.sh`, `bin/uninstall-broker.sh`.
- Documentation: README, `docs/ARCHITECTURE.md`, `CONTRIBUTING.md`.

### Notes

- Zero runtime dependencies — Python 3.9+ standard library and `bash`/`curl` only.
- Single-tenant by design: each user runs their own Telegram bot.
