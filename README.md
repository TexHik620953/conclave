# Conclave

**Conclave is a local-first, multi-agent coding assistant for real repositories.**

It combines a terminal UI, a browser UI, a persistent project board, isolated
git worktrees and an OpenAI-compatible model gateway. Use it as a normal coding
agent in **Build** mode, or let a read-only **Conclave master** decompose work
and dispatch parallel workers against a board.

Conclave is a single Go binary with an embedded Vue web UI. All workspace state
is stored locally under `.conclave/`.

- License: [MIT](LICENSE)
- Go: 1.26+
- Web UI: Vue 3 + Vite
- Storage: SQLite
- Providers: any OpenAI-compatible API (OpenAI, OpenRouter, DeepSeek, local
  gateways, etc.)

---

## Features

### Two working modes

- **Build** — a regular coding agent with file, shell, git and web tools.
- **Plan only** — a read-only toggle inside Build for inspection and planning.
- **Conclave** — a master model plans a dependency DAG, the plan is approved by
  you, and workers execute tasks in parallel isolated git worktrees.

### Persistent project board

- Jira-like task board stored in SQLite.
- Dependencies, priorities, complexity classes, write scopes and acceptance
  criteria.
- Tasks waiting on unfinished dependencies are **Blocked** with clickable
  blocker links to the blocking task or question.
- Atomic task claims with leases and heartbeats.
- Worker selection by complexity range and provider concurrency limits.
- Task attempts, execution logs, comments, branches and commits.
- Review workflow: `Backlog → Ready → In Progress → Review → Done`.
- Result versions for review: every worker attempt records its final result.
- The Conclave master reviews result versions and calls `approve_result` or
  `reject_result`; humans normally only answer agent questions.
- Workers cannot create tasks. They use `suggest_task`, and the master decides
  whether to create follow-up work.
- Failed/paused work keeps a checkpoint and can continue from the same worktree.
- Workers may request decomposition: child tasks inherit parent constraints,
  acceptance references and a structured handoff, and start from a parent
  snapshot when the parent already changed files.
- Automatic worker retries (3 by default).
- After retries are exhausted the task returns to **Backlog** with a small **FAILED** badge and preserved checkpoint.
- Moving that task back to Ready resumes from the checkpoint.

### Workspaces and sessions

- Open multiple workspaces at the same time.
- One shared active session per workspace; switching is a workspace operation.
- Session history survives reloads and server restarts.
- Workspace registry with active agents, open tasks and pending questions.
- **Forget** removes a workspace from the registry without deleting files.

### Web UI

- Chat, Board, Questions, Usage, Agents and Config views.
- Streaming responses, thinking blocks, tool cards and syntax highlighting.
- Global notifications across workspaces.
- Reconnect and state resynchronization after a reload.
- Per-model/session/task token usage.
- Runtime provider/model configuration.
- Native web search (Tavily, Brave, SearXNG, Exa) and Markdown web fetching.
- MCP server management and tool discovery.

### Questions workflow

- Agents can ask single-choice, multiple-choice or custom-text questions.
- Questions are grouped into board-like columns:
  **Needs answer / Master review / Answered by me / Answered by AI**.
- Answering an autonomous question unblocks the related task.

### Multi-repository workspaces

A workspace may be:

- a single git repository;
- a group directory containing several independent git repositories.

For group workspaces, Conclave selects the repository per task using write
scope, component and task text. Worktrees live under
`.conclave/board/<repository>/`.

### Reliability

- Provider HTTP retries for transient failures.
- Configurable first-token and stream-idle timeouts.
- Dangling tool calls are repaired after a crash.
- Paused/failed tasks resume in the same worktree with their message history.
- Conclave plan approval is persisted and restored after restart.
- Graceful web-server shutdown on `SIGINT`/`SIGTERM`.

---

## Requirements

- Go **1.26+**
- Git
- Node.js **22+** and npm only if you build the web UI from source

Supported platforms: Linux, macOS and other environments where the Go toolchain
and Git are available. The built-in shell tooling is Unix-oriented.

---

## Quick start

### 1. Build

```bash
git clone https://github.com/texhik/conclave.git
cd conclave
make build
```

Or build directly:

```bash
go build -o conclave ./cmd/conclave
```

The repository includes the embedded web assets, so `go build` works without
running npm.

### 2. Create a workspace configuration

```bash
./conclave config init
```

This seeds `.conclave/config.yaml`. On first load, provider and profile
definitions are migrated into the global configuration:

```text
~/.config/conclave/config.yaml
```

Project-specific permissions and prompt overrides may remain in
`.conclave/config.yaml`.

### 3. Provide API keys

```bash
export OPENROUTER_API_KEY=sk-or-...
# or
export DEEPSEEK_API_KEY=sk-...
```

Keys can also be entered in **Web UI → Config**.

### 4. Run

Terminal UI:

```bash
./conclave
```

Browser UI:

```bash
./conclave serve --addr 127.0.0.1:8787
```

The address can also be stored globally:

```yaml
server:
  address: 0.0.0.0:8811
```

and configured in **Global config → Server**. CLI `--addr` overrides it.

Then open <http://127.0.0.1:8787>.

Headless one-shot prompt:

```bash
./conclave run "find and fix the failing test"
./conclave run -p cheap "explain this repository"
```

---

## Configuration

Conclave merges two YAML files:

1. global: `~/.config/conclave/config.yaml`
2. project: `<workspace>/.conclave/config.yaml`

Project values override global values. API keys support environment expansion:

```yaml
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    api_key: ${OPENROUTER_API_KEY}
    max_parallel: 8
    models:
      deepseek/deepseek-chat:
        context: 128000
        max_output: 8192
        capabilities:
          tools: true
          reasoning: false
          effort: false
```

Profiles define the agent, master and worker pool:

```yaml
profiles:
  default:
    agent:
      model: openrouter/deepseek/deepseek-chat
      effort: medium
    master:
      model: openrouter/anthropic/claude-sonnet-4
      effort: high
    workers:
      - id: coder
        model: openrouter/deepseek/deepseek-chat
        effort: medium
        complexity: [1, 2, 3]
        instances: 4
    limits:
      max_parallel: 8
      max_iterations: 6
    timeouts:
      first_token: 180s
      idle: 180s
```

Each workspace can select its active profile from the top bar. The choice is
stored as `workspace_profile` in `<workspace>/.conclave/config.yaml` and is used
by Build, Conclave and Board tasks in that workspace.

Worker concurrency is the minimum of:

```text
worker.instances,
provider.max_parallel,
profile.limits.max_parallel
```

See [docs/configuration.md](docs/configuration.md) for the full schema,
permissions, MCP servers, prompt overrides and timeouts.

---

## Web UI

```bash
./conclave serve --addr 127.0.0.1:8787
```

Open the URL printed by the server.

Main views:

| View | Purpose |
|---|---|
| **Chat** | Build or Conclave conversation, streaming output, tool activity |
| **Board** | Tasks, queue, attempts, logs, comments, review/apply |
| **Questions** | Questions grouped by state and author |
| **Usage** | Tokens by model or session |
| **Agents** | Live agents, tasks, models, context and token usage |

**Global config** is opened from the gear button in the top bar or from the
workspace picker. It manages providers, models, API keys, profiles, MCP servers,
search and timeouts independently of the active workspace.

The top bar shows the workspace and the number of currently running agents.

For frontend development:

```bash
./conclave serve --addr 127.0.0.1:8787
cd web
npm install
npm run dev
```

See [docs/web-ui.md](docs/web-ui.md).

---

## Conclave workflow

1. Switch the session to **Conclave**.
2. Describe the goal to the master.
3. The master inspects the repository and calls `submit_plan`.
4. Planned tasks appear in **Backlog** as a preview.
5. Review the plan and click **Approve plan**.
6. Tasks move to **Ready**, workers claim them and run in isolated worktrees.
7. Successful tasks move to **Review** with a branch and commit.
8. The master reviews the batch and either asks for another pool or finishes.
9. Apply/merge work only when you explicitly choose to.

While the master is still finishing its response, approval is disabled. This
prevents workers from starting while the planning turn is still active.

---

## Board and queue

The board belongs to the workspace and is shared by all sessions.

Typical statuses:

```text
backlog → ready → claimed → in_progress → review → done
                   ↘ blocked
                   ↘ backlog + FAILED badge (retries exhausted)
                   ↘ cancelled
```

- Only **Ready** tasks are visible to the queue.
- The queue is off until you start it.
- Pausing interrupts active workers and returns them to **Ready**.
- Resuming reuses their worktree, branch and conversation checkpoint.
- A failed task remains **Blocked** with its logs and checkpoint for retry.

---

## Pause, crash recovery and continuation

When a worker is interrupted:

- its worktree and branch are preserved;
- the assistant/tool message history is checkpointed;
- incomplete tool calls are repaired before continuation;
- the next attempt resumes in the same worktree instead of starting from zero.

The same fallback works for worktrees created by older versions that did not
have checkpoint metadata.

Conclave run state, pending plans and messages are also restored when the web
server restarts.

---

## Multi-repository workspaces

For a group workspace:

```text
my-platform/
  service-api/       # git repository
  service-worker/    # git repository
  infrastructure/    # git repository
```

Conclave detects immediate child repositories. For each task it chooses one
repository by matching:

1. `write_scope`
2. `component`
3. task title
4. task description

Repository short names also work: `node-go` matches `liberator-node-go`.

If no hint matches and several repositories exist, the first repository is used
as a deterministic fallback. Prefer explicit `write_scope` or `component` values
for multi-repository tasks.

---

## CLI reference

```text
conclave                       start the TUI
conclave tui [-p profile]      start the TUI with a profile
conclave serve [--addr HOST]   start the web UI
conclave run [-p profile] ...  run one prompt headlessly
conclave config init           create .conclave/config.yaml
conclave config validate       validate merged configuration
conclave config show           print a configuration summary
conclave config path           print the project config path
conclave models                list configured models
conclave sessions              list recent sessions
conclave version               print version
conclave help                  show help
```

### Environment variables

| Variable | Purpose |
|---|---|
| `DEEPSEEK_API_KEY` | API key for the default DeepSeek provider |
| `OPENROUTER_API_KEY` | API key for the default OpenRouter provider |
| `CONCLAVE_DB` | Override the SQLite database path |
| `CONCLAVE_LLM_FIRST_TOKEN_TIMEOUT` | Fallback first-token timeout |
| `CONCLAVE_LLM_IDLE_TIMEOUT` | Fallback stream idle timeout |
| `XDG_CONFIG_HOME` | Override the global config location |

---

## Repository layout

```text
cmd/conclave/       CLI entry point
internal/agent/     model tool loop
internal/app/       application composition root and sessions
internal/board/     task queue, worktrees, checkpoints
internal/conclave/  master coordinator and run state
internal/config/    YAML configuration
internal/domain/    shared domain types
internal/gitx/      git CLI adapter
internal/llm/       OpenAI-compatible client and retry logic
internal/mcp/       MCP client integration
internal/prompt/    system/master/worker/reviewer prompts
internal/questions/ agent questions and answer workflow
internal/store/     SQLite persistence
internal/tools/     built-in tools and permissions
internal/tracker/   board/task persistence
internal/tui/       Bubble Tea UI
internal/usage/     token usage aggregation
internal/web/       HTTP/WebSocket API and embedded web bundle
web/                Vue 3 frontend sources
docs/               architecture, configuration and troubleshooting
```

---

## Development

```bash
make test          # go test ./...
make race          # go test -race ./...
make vet           # go vet ./...
make fmt           # gofmt
make web           # install and build the Vue UI
make build         # build the Go binary
make check         # tests, vet and web build
```

After changing frontend sources:

```bash
cd web
npm run build
cd ..
go build -o conclave ./cmd/conclave
```

The Vite output is written to `internal/web/dist/` and embedded into the Go
binary.

More:

- [Architecture](docs/architecture.md)
- [Configuration](docs/configuration.md)
- [Web UI](docs/web-ui.md)
- [Troubleshooting](docs/troubleshooting.md)

---

## Security notes

- The web server binds to `127.0.0.1` by default. It has no authentication layer;
  do not expose it to untrusted networks without a reverse proxy and access
  controls.
- API keys are stored in the global config or supplied through environment
  variables. Do not commit `.conclave/` or `~/.config/conclave/config.yaml`.
- Worker tools are sandboxed by workspace permissions, but shell access is
  powerful by design. Review `permissions` before running untrusted prompts.
- `.conclave/` contains the database, logs and worktrees. Back it up if the
  workspace state matters.

---

## License

MIT. See [LICENSE](LICENSE).
