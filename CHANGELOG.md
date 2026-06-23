# Changelog

All notable changes to this project are documented here. This project adheres to
[Semantic Versioning](https://semver.org/).

## [1.0.0] — 2026-06-23

Initial release: **one-way phone notifications**.

### Added

- Claude Code hooks that fire a phone notification:
  - `Notification` (matcher `permission_prompt`) — Claude is waiting for your approval.
  - `Stop` — Claude finished a turn.
- `bin/notify.sh` — sends the alert to Telegram via the Bot API. Reads credentials
  from the plugin's `userConfig` (env) or `~/.config/noti/config.json`. Degrades
  gracefully without `jq` and always exits 0 so it can never break a Claude turn.
- `/noti:setup` — interactive onboarding (create a bot, detect chat ID, send a
  test message).
- Offline test suite (`tests/run_tests.sh`) and CI on Linux + macOS.

### Notes

- Zero dependencies: `bash` + `curl` (plus optional `jq`). No daemon, no Python.
- One-way only. Answering Claude's questions from your phone is planned — see
  [docs/ROADMAP.md](docs/ROADMAP.md). A working prototype lives on the
  `wip/v3-full` branch.
