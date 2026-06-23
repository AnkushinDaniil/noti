# Contributing

Thanks for your interest in noti! The codebase is a single static Go binary —
easy to build and hack on.

## Principles

- **Go stdlib only.** No third-party `require`s in `go.mod`. Please keep it that way.
- **Never break a Claude turn.** Hook scripts (`bin/notify.sh`) must always `exit 0`.
  HTTP handlers must never crash the server goroutine.
- **Secrets never touch the repo or the logs.** The bot token lives only in
  `~/.config/noti/config.json` (chmod 600) and in process memory. Never log it.
- **Logs go to stderr.** The `noti mcp` subcommand uses stdout as the JSON-RPC
  channel — never write anything to stdout from MCP-related code.
- **Idiomatic Go.** `gofmt`-formatted, `go vet` clean, table-driven tests.

## Repository layout

```
go.mod                   module github.com/AnkushinDaniil/noti
main.go                  subcommand dispatch (package main)
internal/
  version/               const Version
  config/                config schema, Load, ResolveTarget, ResolveAsk
  telegram/              stdlib Telegram Bot API client
  broker/                broker daemon (HTTP + poll goroutine + registry)
  mcp/                   MCP stdio server
  notify/                hook notifier (noti notify <level>)
.claude-plugin/          plugin.json, marketplace.json
hooks/                   hooks.json (Notification + Stop)
bin/                     notify.sh, install-broker.sh, uninstall-broker.sh
scripts/                 build.sh
skills/setup/            SKILL.md — /noti:setup onboarding runbook
docs/                    ARCHITECTURE.md, ROADMAP.md
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the design rationale (why a
broker daemon, why the `ask_user`/`wait_for_reply` split, the single-`getUpdates`
constraint, etc.). Read it before changing the broker or MCP server.

## Building

```bash
# Build to bin/noti (for development):
bash scripts/build.sh

# Build to the persistent data directory (survives plugin updates):
OUT="${CLAUDE_PLUGIN_DATA:-${HOME}/.local/state/noti}/bin/noti" bash scripts/build.sh

# Cross-compile (example):
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o noti-linux-arm64 .
```

## Running the tests

```bash
go test -race ./...
```

All tests run offline — no Telegram network, no installed services. The broker
tests spin up on an ephemeral port with `NOTI_TEST=1`, which disables real
Telegram I/O and enables `POST /test/inject` so tests can simulate phone replies.

```bash
# Format check:
gofmt -l .

# Static analysis:
go vet ./...
```

## Manual testing against a real bot

Some paths can only be verified with a live Telegram bot (real `getUpdates`
long-poll, 409 handling, the launchd/systemd service):

1. Create a bot via `@BotFather`, write `~/.config/noti/config.json`.
2. `bash scripts/build.sh && bin/install-broker.sh` to build and start the daemon.
3. `bin/noti test "hello"` — confirm it lands on your phone.
4. `NOTI_TEST=1 bin/noti broker &` for an isolated offline broker.

## Pull requests

- Run `go test -race ./...` and `go vet ./...` — all must pass.
- `gofmt -l .` must emit no files.
- Add or update a test for behavior changes in the broker, MCP server, or notify path.
- Keep the README and `docs/ARCHITECTURE.md` in sync with any contract change
  (routes, env vars, config schema, tool list).
- Conventional-commit style messages (`feat:`, `fix:`, `docs:`, …) are appreciated.
