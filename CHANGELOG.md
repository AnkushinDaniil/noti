# Changelog

All notable changes to this project are documented here. This project adheres to
[Semantic Versioning](https://semver.org/).

## [2.0.1] - 2026-06-23

### Changed
- **Permission gate is now opt-in and OFF by default.** The `PreToolUse` hook
  was removed from the plugin's default hooks (it fired on every tool call and
  asked the phone even in auto/acceptEdits modes where the tool proceeds without
  a prompt). The `noti permission-gate` subcommand remains for opt-in use, and
  now contacts the phone **only in the `default` permission mode**.
- **Richer hook notifications.** Notifications now show which project/session
  they came from (project name, cwd, short session id) and a context line — for
  `finished`, the tail of Claude's last message read from the transcript; for
  `needs you`, the permission/prompt text.

## [2.0.0] - 2026-06-23

Rewrote noti in Go as a single static binary (was Python+bash). Feature parity
with 1.0.0. Adds dual-input `ask_user` (laptop elicitation + phone, first-wins),
two configurable modes, hard-require elicitation enforcement, and a phone-first
permission gate for tool-approval prompts. Distributed as prebuilt binaries via
GitHub Releases (GoReleaser); `/noti:setup` downloads the right one for your
platform, so Go is no longer required to run noti.

### Added

- **Single static Go binary** (`noti`) with subcommands: `broker`, `mcp`,
  `notify <level>`, `permission-gate`, `detect-chat`, `test`, `version`, `help`.
- **`internal/broker`**: Go broker daemon replacing `bin/broker.py`. Same HTTP
  API (`/health`, `/notify`, `/ask`, `/wait`, `/cancel`, `/config`), same
  loopback singleton with PID lockfile and offset persistence.
- **`internal/mcp`**: Go MCP stdio server replacing `server/mcp_server.py`.
  Same 5 tools (`ask_user`, `wait_for_reply`, `notify`, `send_file`,
  `send_image`).
- **Dual-input `ask_user`**: sends the question to both the laptop (via MCP
  `elicitation/create`) and the phone (via the broker); the first answer wins.
  The loser is cancelled best-effort. Modes control when each source is started
  (see below).
- **Mode `timeout`** (default): question appears in Claude Code UI immediately;
  after `idle_timeout_seconds` the phone is also notified. First answer wins.
  A laptop decline escalates to the phone before the idle timer fires.
- **Mode `forward-all`**: question sent to laptop and phone simultaneously.
  First answer wins.
- **Hard-require elicitation** (`require_laptop: true`, default): if the
  connected Claude Code client does not advertise the `elicitation` capability,
  `ask_user` returns an `isError` result asking the user to update Claude Code
  (v2.1.76+). Set `require_laptop: false` to allow phone-only fallback instead.
- **~50 s laptop window**: the laptop elicitation lives only during the
  `ask_user` call. If neither source answers within ~50 s, the laptop prompt is
  cancelled and `ask_user` returns a ticket for continued polling via
  `wait_for_reply`.
- **Lingering-laptop-prompt caveat**: the MCP spec says cancelling a shown
  elicitation is `SHOULD`, not `MUST`. After the phone wins, the laptop prompt
  may remain visible; any late laptop answer is silently dropped.
- **Server-issued requests + cancellation**: the MCP server sends
  `elicitation/create` as a JSON-RPC 2.0 server-originated request (id
  `"noti-req-<n>"`); the response is routed via the `pending` map. A
  `notifications/cancelled` notification is sent when the other source wins; a
  `cancelled` id set prevents late responses from being acted on.
- **Permission gate** (`noti permission-gate`, `bin/permission_gate.sh`): a
  `PreToolUse` hook that sends an Allow/Deny question to the phone for the
  configured gated tools. Sequential (phone-first, then terminal fallback on
  timeout) — not first-wins (a protocol limit of the hook system). Always exits
  0. Decision is emitted as
  `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"|"deny"|"ask",...}}`.
- **`permissions.tools`** config field: list of tool names whose
  `PreToolUse` events are gated. Default: `["Bash","Write","Edit","NotebookEdit"]`.
- **Per-project `routing[].ask` override**: a matching route's `ask` block is
  merged on top of the global `ask` block, allowing per-project mode, timeout,
  and permission settings.
- **`internal/notify`**: `noti notify <level>` replaces the inline logic in
  `bin/notify.sh`. Reads hook JSON from stdin, POSTs to broker, falls back to
  direct Telegram send. Always exits 0.
- **`internal/config`**: typed config with `ResolveTarget`, `ResolveAsk`, and
  full per-project routing.
- **`internal/telegram`**: stdlib-only Telegram Bot API client with test mode
  (no network; records sends to `Outbox`).
- **`internal/version`**: `const Version = "2.0.0"`.
- **`scripts/build.sh`**: cross-platform build script (`go build -o bin/noti .`).
- **CI**: GitHub Actions test matrix (ubuntu/macos, go vet + gofmt + go test)
  and cross-compile matrix (darwin/linux × amd64/arm64, uploaded as artifacts).
- **`bin/notify.sh`**: simplified to a thin binary locator (`exec noti notify <level>`).
- **`bin/permission_gate.sh`**: thin binary locator for the permission gate
  (`exec noti permission-gate`); exits 0 (pass-through) if the binary is missing.
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
