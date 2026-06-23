# Contributing

noti v1 is deliberately tiny — `bash` + `curl`, no daemon, no dependencies.

## Principles

- **Keep it dependency-free.** `bash` + `curl` (optional `jq`). No Python runtime,
  no `pip`/`npm`, no build step.
- **Never break a Claude turn.** `bin/notify.sh` must always `exit 0`.
- **Secrets stay out of the repo and the logs.** The bot token lives only in
  `~/.config/noti/config.json` (chmod 600). It is never printed (the dry-run path
  redacts it).

## Layout

```
.claude-plugin/   plugin.json (manifest) + marketplace.json
hooks/            hooks.json — Notification + Stop hooks
bin/notify.sh     the notifier (reads hook JSON from stdin, sends to Telegram)
skills/setup/     SKILL.md — /noti:setup onboarding
tests/            run_tests.sh — offline suite
docs/             ROADMAP.md
```

## Testing

```bash
bash tests/run_tests.sh
```

Runs: JSON validation of the manifests, `bash -n` on the shell scripts, and a
dry-run of `notify.sh` (`NOTI_DRY_RUN=1`) that checks routing, the message label,
and that the token never leaks into output. Fully offline.

To test against a real bot, run `/noti:setup` and trigger a permission prompt or
let Claude finish a turn.

## Pull requests

- Run `bash tests/run_tests.sh` (CI runs it on Linux + macOS).
- Keep the README in sync with behavior changes.
- Conventional-commit messages (`feat:`, `fix:`, `docs:`) appreciated.

## Roadmap

Two-way "answer from your phone" (an MCP `ask_user` tool backed by a local broker
daemon) is prototyped on the `wip/v3-full` branch. See
[docs/ROADMAP.md](docs/ROADMAP.md).
