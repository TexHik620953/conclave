# conclave-agent

Local agent for [conclave-core](../conclave-core). It runs on the user's
machine, connects to the core over two WebSockets, executes tools the core
cannot run in the cloud (filesystem, shell, git), and serves a built-in Web UI
for working with and managing sessions.

```
conclave-core (cloud)
   ▲  /ws/agent  (device token)  — tool.call / tool.result
   │
conclave-agent ── /ws/client (user token) — live events, plan, questions, deltas
   │
   └── local Web UI (http://127.0.0.1:7070) + local tool execution
```

## How it works

- **Agent connection** (`/ws/agent`, device token): the agent registers its
  tools (`tools.register`), binds sessions (`session.open`), receives
  `tool.call` messages, executes them locally and replies with `tool.result`.
- **Client connection** (`/ws/client`, user token): the agent subscribes to the
  open session's channels and forwards events (`event.append`), the plan
  snapshot, questions and token deltas to the Web UI over SSE.
- **Web UI**: lists sessions, shows the live timeline/graph, and has a single
  composer — **Send** drives the session controller (which decides whether to
  ask, plan, run or finish), **Stop** cancels active work, and answering a
  question continues automatically. `Auto-plan` and `Interview` live under an
  *Advanced* disclosure. All commands are proxied to the core with the device
  token.

## Tools

`read_file`, `write_file`, `edit_file`, `list_dir`, `glob` (with `**`), `grep`,
`run_shell`, `git` (dangerous options/subcommands blocked), `apply_patch`. All
paths are sandboxed to the workspace (symlink-safe).

## Build & run

```bash
make build

# from the core:
#   POST /v1/bootstrap            → device token (cc_...)
#   POST /v1/users/token          → user token (cu_...)
make run \
  CORE=http://127.0.0.1:8090 \
  DEVICE=cc_... USER=cu_... \
  WORKSPACE=$PWD
```

Open http://127.0.0.1:7070.

Environment variables (flags override):

| Env | Flag | Default | Purpose |
|---|---|---|---|
| `CONCLAVE_CORE_URL` | `--core-url` | `http://127.0.0.1:8090` | Core base URL. |
| `CONCLAVE_DEVICE_TOKEN` | `--device-token` | — | Device token for `/ws/agent`. |
| `CONCLAVE_USER_TOKEN` | `--user-token` | — | User token for `/ws/client`. |
| `CONCLAVE_WORKSPACE` | `--workspace` | cwd | Directory tools operate in. |
| `CONCLAVE_AGENT_ADDR` | `--addr` | `127.0.0.1:7070` | Local UI/API address. |
| `CONCLAVE_LOG_LEVEL` | — | `info` | `debug`/`info`/`warn`/`error`. |
| `CONCLAVE_COMMAND_TIMEOUT` | — | `2m` | Shell/tool timeout. |

## Layout

```
cmd/agent/               entrypoint
internal/
  config/                env/flags configuration
  conn/                  the two Centrifuge connections + tool-call handling
  corehttp/              HTTP client for the core REST API
  hub/                   SSE fanout to the browser
  tools/                 sandboxed local tools
  api/                   local HTTP API + embedded UI (api/ui/index.html)
```

## Development

```bash
make fmt && make vet && make test
```
