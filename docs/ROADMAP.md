# Roadmap

## Step 1 — Go parity (current)

Replace the Python implementation with a single static Go binary.

Feature parity with v1.0.0:
- Broker daemon with HTTP API (`/health`, `/notify`, `/ask`, `/wait`, `/cancel`, `/config`)
- MCP stdio server with 5 tools (`ask_user`, `wait_for_reply`, `notify`, `send_file`, `send_image`)
- Hook notifier (`noti notify <level>`) with broker + direct Telegram fallback
- Setup subcommands (`noti detect-chat`, `noti test`, `noti version`)
- Install/uninstall scripts updated to use the Go binary
- CI: test matrix (ubuntu/macos) + cross-compile matrix (darwin/linux × amd64/arm64)

New config schema additions (wired, not yet active):
- `ask` block: `mode`, `idle_timeout_seconds`, `laptop`, `require_laptop`, `permissions`
- `GET /config?project=NAME` endpoint returns resolved ask config
- Client elicitation capability flag stored in MCP server (Step 2 uses it)

## Step 2 — Elicitation race + two modes + permission gate

Activate the features wired in Step 1:

- **Mode `timeout`** (default): wait `idle_timeout_seconds` for a phone reply before
  auto-continuing. Broker POSTs a `/wait` result of `timeout` to the caller.
- **Mode `forward-all`**: block indefinitely until the user replies.
- **Permission gate**: when `permissions.enabled=true`, the `permission_prompt` hook
  blocks Claude from proceeding until the user approves from their phone (or the
  `permissions.timeout_seconds` elapses and the default allow/deny is applied).
- **MCP elicitation**: when the client advertises `elicitation` capability (already
  detected and stored), use the MCP-native elicitation flow rather than
  `ask_user`/`wait_for_reply`. The reader goroutine's response routing is already
  in place.

## Step 3 — Release binaries + download-at-setup

- GitHub Actions release workflow: triggered on version tags, uploads
  `noti-darwin-amd64`, `noti-darwin-arm64`, `noti-linux-amd64`, `noti-linux-arm64`
  as release assets.
- `scripts/build.sh` enhanced: prefer downloading the release binary for the
  current platform from GitHub releases; fall back to `go build` if Go is present.
- Setup skill updated to try the download path first, so Go is not a hard requirement.
