# Roadmap

## Step 1 — Go parity (done)

Replace the Python implementation with a single static Go binary.

Feature parity with v1.0.0:
- Broker daemon with HTTP API (`/health`, `/notify`, `/ask`, `/wait`, `/cancel`, `/config`)
- MCP stdio server with 5 tools (`ask_user`, `wait_for_reply`, `notify`, `send_file`, `send_image`)
- Hook notifier (`noti notify <level>`) with broker + direct Telegram fallback
- Setup subcommands (`noti detect-chat`, `noti test`, `noti version`)
- Install/uninstall scripts updated to use the Go binary
- CI: test matrix (ubuntu/macos) + cross-compile matrix (darwin/linux × amd64/arm64)

Config schema additions (now fully active):
- `ask` block: `mode`, `idle_timeout_seconds`, `laptop`, `require_laptop`, `permissions`
- `GET /config?project=NAME` endpoint returns resolved ask config
- Client elicitation capability flag stored in MCP server

## Step 2 — Elicitation race + two modes + permission gate (done)

- **Mode `timeout`** (default): the question appears in the Claude Code UI
  immediately via MCP elicitation. After `idle_timeout_seconds`, the question is
  also sent to the phone. The first answer (laptop or phone) wins. If neither
  answers within ~50 s, `ask_user` returns a ticket for `wait_for_reply`.
- **Mode `forward-all`**: question sent to both laptop and phone simultaneously;
  first answer wins.
- **Hard-require elicitation** (`require_laptop: true`, default): if the client
  does not advertise the `elicitation` capability, `ask_user` returns an error
  instead of silently falling back. Set `require_laptop: false` to opt in to
  phone-only on older clients.
- **Lingering-prompt caveat**: cancelling a shown elicitation is `SHOULD`, not
  `MUST` in the MCP spec. When the phone wins, the laptop prompt may linger;
  any late laptop answer is silently dropped.
- **~50 s laptop window**: the elicitation lives only for the duration of the
  `ask_user` tool call. After ~50 s the laptop prompt is cancelled and only the
  phone path (via `wait_for_reply`) continues.
- **Permission gate** (`noti permission-gate`, `bin/permission_gate.sh`): a
  `PreToolUse` hook sends Allow/Deny to the phone for the gated tools (`Bash`,
  `Write`, `Edit`, `NotebookEdit`). Sequential (phone-first, then terminal
  fallback on timeout) — not first-wins, which is a protocol limit of the hook
  system. Always exits 0; never blocks Claude indefinitely.
- **`permissions.tools`**: configurable list of gated tool names.

## Step 3 — Release binaries + download-at-setup (done)

- GoReleaser (`.goreleaser.yaml`) + a tag-triggered GitHub Actions release
  workflow (`.github/workflows/release.yml`) build and publish per-platform
  archives (`noti_darwin_amd64`, `noti_darwin_arm64`, `noti_linux_amd64`,
  `noti_linux_arm64`) plus `checksums.txt`. The release tag is injected into the
  binary version via `-ldflags`.
- `scripts/fetch-binary.sh` downloads the right archive for the current platform,
  verifies the checksum, and installs it to `$CLAUDE_PLUGIN_DATA/bin/noti`,
  falling back to `go build` only if the download fails and Go is present.
- `/noti:setup` downloads the binary first, so **Go is not required** for normal use.
