# Conclave architecture and roadmap

This document is an internal roadmap. For the current system design see
[docs/architecture.md](docs/architecture.md).

## Current status

Implemented:

- Build mode with tools, permissions and streaming;
- Plan-only mode;
- Conclave master planning, approval, task pools and review;
- persistent board with dependencies, claims, leases and heartbeats;
- parallel workers in isolated git worktrees;
- multi-repository/group workspaces;
- pause/resume with worktree and conversation checkpoints;
- provider/model configuration UI and global config;
- sessions, fork/revert/regenerate/continue;
- questions workflow and task blocking;
- token usage by model/session/task/agent;
- MCP client;
- background shell jobs;
- Bubble Tea TUI;
- Vue web UI with workspace picker, global notifications, board, questions,
  config and usage;
- embedded production assets;
- crash recovery for plans, sessions and interrupted workers.

## Design constraints

1. Local-first single binary.
2. Server-authoritative state for the web UI.
3. SQLite is the source of truth.
4. Git worktrees isolate parallel workers.
5. Master is read-only.
6. Approval is explicit.
7. No automatic merge/apply.
8. One active session per workspace at a time.

## Next milestones

### Web frontend consolidation

The web UI currently uses Vue 3 JavaScript modules. Planned:

- migrate to TypeScript;
- Pinia stores;
- Vue Router;
- shared UI kit;
- Tailwind/design tokens;
- remove remaining per-component CSS overrides.

### Orchestration

- improve runtime decomposition proposals;
- stronger task dependency validation against board IDs;
- richer integration conflict recovery;
- resumable multi-repository integration for group workspaces.

### Operations

- structured logging with file rotation;
- workspace backup/restore command;
- optional authentication for non-loopback web serving;
- automated release builds for Linux/macOS.

### Testing

- end-to-end web tests;
- multi-workspace concurrency tests;
- repeated pause/resume and crash-recovery tests;
- provider timeout/retry integration tests.

## Non-goals

- microservices;
- cloud-hosted control plane;
- billing/cost tracking;
- automatic deployment to production infrastructure.
