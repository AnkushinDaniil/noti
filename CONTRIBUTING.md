# Contributing

Thanks for your interest in noti! This is a small, dependency-free project — easy
to hack on.

## Principles

- **Zero runtime dependencies.** Python 3.9+ standard library and `bash`/`curl`
  only. No `pip`, no `npm`, no build step. Please keep it that way.
- **Never break a Claude turn.** Hook scripts (`bin/notify.sh`) must always
  `exit 0`. HTTP handlers must never crash the server thread.
- **Secrets never touch the repo or the logs.** The bot token lives only in
  `~/.config/noti/config.json` (chmod 600) and in process memory.
- **Small, focused files.** Prefer extracting a module over growing a giant one.

## Repository layout

```
.claude-plugin/   plugin.json (manifest) + marketplace.json
hooks/            hooks.json — Notification + Stop hooks
bin/              notify.sh, broker.py, noti_channels.py, noti (CLI),
                  install-broker.sh, uninstall-broker.sh
server/           mcp_server.py — hand-rolled MCP stdio server
skills/setup/     SKILL.md — the /noti:setup onboarding runbook
tests/            run_tests.sh + offline smoke tests
docs/             ARCHITECTURE.md
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the design rationale (why a
broker daemon, why the `ask_user`/`wait_for_reply` split, the single-`getUpdates`
constraint, etc.). Read it before changing the broker or MCP server.

## Running the tests

The whole suite runs offline — no Telegram network, no installed services:

```bash
bash tests/run_tests.sh
```

It runs:

1. `py_compile` on every Python file
2. JSON validation of all plugin manifests/configs
3. `bash -n` syntax check on every shell script
4. `tests/smoke_broker.py` — boots the broker in `NOTI_TEST=1` mode on an
   ephemeral port and drives `/health`, `/ask` (resolved via `/test/inject`), and
   `/notify`
5. `tests/smoke_mcp.py` — spawns the MCP server against a stub broker and drives
   the full `initialize → tools/list → tools/call` handshake over stdio

`NOTI_TEST=1` makes the broker skip the real Telegram network: channel sends
become recorded no-ops and a `/test/inject` route lets tests simulate inbound
replies. Use it for any new test that would otherwise need a live bot.

## Manual testing against a real bot

Some paths can only be verified with a live Telegram bot (real `getUpdates`
long-poll, 409 handling, the launchd/systemd service):

1. Create a bot via `@BotFather`, write `~/.config/noti/config.json`.
2. `bin/install-broker.sh` to start the daemon, then `bin/noti status`.
3. `bin/noti test "hello"` — confirm it lands on your phone.

## Type checking

A `pyrightconfig.json` is included (`extraPaths: ["bin", "server"]` so the
sibling-module imports resolve). Run your editor's Pyright/Pylance or
`pyright` if you have it. The only expected hints are unused-parameter notices on
framework-mandated override signatures (`log_message`, signal handlers).

## Pull requests

- Run `bash tests/run_tests.sh` and make sure it passes.
- Add or update a smoke test for behavior changes in the broker or MCP server.
- Keep the README and `docs/ARCHITECTURE.md` in sync with any contract change
  (routes, env vars, config schema, tool list).
- Conventional-commit style messages (`feat:`, `fix:`, `docs:`, …) are
  appreciated.
