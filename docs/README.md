# Conclave documentation

- [Architecture](architecture.md) — packages, state machines, concurrency,
  persistence, multi-repository workspaces and checkpoints.
- [Configuration](configuration.md) — providers, models, profiles, workers,
  permissions, MCP, prompts and timeouts.
- [Web UI](web-ui.md) — workspace picker, chat, sessions, board, questions,
  notifications and developer workflow.
- [Troubleshooting](troubleshooting.md) — provider errors, git worktree issues,
  approval, pause/resume and recovery.
- [Project README](../README.md) — quick start and feature overview.

## Common commands

```bash
go build -o conclave ./cmd/conclave
./conclave config init
./conclave
./conclave serve --addr 127.0.0.1:8787
./conclave run "fix the failing test"
```

## Verification

```bash
make test
make vet
make web
make build
```
