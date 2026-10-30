# Architecture

Conclave is a modular monolith. One process owns the workspace state, SQLite
database, model clients and worker scheduling. The TUI and web UI are clients of
the same application layer.

## Principles

1. One binary; no separate control plane or database server.
2. Domain types do not perform I/O.
3. Infrastructure adapters are constructed in `internal/app` / `cmd`.
4. State changes are published as events for the TUI and web clients.
5. A Conclave run is coordinated by one service; workers return results rather
   than mutating run state directly.
6. Git worktrees are the isolation boundary for parallel workers.
7. SQLite is the source of truth for sessions, board state, questions and usage.

## Package map

| Package | Responsibility |
|---|---|
| `cmd/conclave` | CLI parsing, TUI/headless/web entry points |
| `internal/app` | Composition root, sessions, build-mode send/regenerate/fork |
| `internal/agent` | One model tool loop, tool batches, cancellation |
| `internal/board` | Queue scheduling, worktrees, attempts, checkpoints |
| `internal/conclave` | Master planning, approval, run state machine, review |
| `internal/config` | YAML schema, merge, environment expansion, validation |
| `internal/domain` | Sessions, messages, runs, tasks, plans, events |
| `internal/event` | In-process non-blocking event bus |
| `internal/gitx` | Git CLI adapter and multi-repository discovery |
| `internal/jobs` | Background shell jobs |
| `internal/llm` | OpenAI-compatible streaming transport and retries |
| `internal/mcp` | MCP server connections and tool adaptation |
| `internal/prompt` | System, master, worker and reviewer prompts |
| `internal/questions` | Question sets, answers, task blockers |
| `internal/store` | SQLite schema, sessions, runs, cleanup |
| `internal/tools` | Built-in tools, path policy, shell/network permissions |
| `internal/tracker` | Board project/task persistence and claims |
| `internal/tui` | Bubble Tea terminal client |
| `internal/usage` | Per-model/session/task token aggregation |
| `internal/web` | HTTP API, WebSockets, embedded Vue bundle |

## Dependency direction

```text
cmd
 └── app
      ├── agent ──► llm, tools, prompt, domain
      ├── conclave ──► board, tracker, gitx, questions, agent
      ├── board ──► tracker, gitx, agent, questions
      ├── store ──► SQLite
      └── event ──► domain
```

UI packages (`internal/tui`, `internal/web`) talk to `app` and infrastructure
through the application layer. They do not open SQLite or call model providers
directly.

## Workspace model

A workspace is a directory registered in the global workspace registry.

```text
<workspace>/.conclave/
  config.yaml       project/workspace overrides
  conclave.db       sessions, runs, board, questions, usage
  runs/             run artifacts
  board/            isolated worker worktrees and checkpoints
```

One workspace has:

- one board;
- one execution queue;
- one runtime application instance;
- one active session;
- any number of saved sessions.

The active session can be changed only while no build/Conclave agent is running.
Multiple browser tabs share the same server-side workspace state.

The global registry lives under the user config/data directory and is separate
from the workspace files. **Forget** removes a registry entry; it never deletes
`.conclave/`.

## Sessions

A session stores:

- mode: `build` or `conclave`;
- plan-only flag;
- model and reasoning effort;
- messages for Build;
- Conclave messages and usage ledger;
- context token count;
- timestamps.

Session deletion removes its runs, events and usage records. Deleting the active
session creates a new empty Build session.

## Run state machine

```text
planning
  → awaiting_approval
  → running
  → reviewing
  → running (next task pool)
  → completed

running → paused → running
any active state → cancelled / failed
```

Pending plans are persisted in the run record. If a server restarts while a plan
is awaiting approval, Conclave restores the plan. If the plan record is missing,
it reconstructs the approval preview from backlog tasks associated with the run.

## Task state machine

```text
backlog
  → ready
  → claimed
  → in_progress
  → review
  → done

claimed/in_progress → blocked (needs human/external input)
claimed/in_progress → backlog + FAILED marker (automatic retries exhausted)
any non-terminal state → cancelled
```

Only `ready` tasks are eligible for the queue. Claims use leases and heartbeats;
interrupted claims return to the queue through recovery.

Dependencies are represented as unresolved `task_blockers` with
`kind=dependency`. A task with unfinished dependencies is moved to **Blocked**.
Finishing the dependency resolves the blocker and moves the dependent task back
to **Ready**. Question blockers use the same table with `kind=question`.

## Conclave planning

The master is a normal agent with a restricted set of tools:

```text
read_file, list_dir, glob, grep, git, web_fetch, web_search
submit_plan, finish_run, create_task, list_tasks, comment_task,
ask_user, list_workers
```

It cannot write files or run shell commands.

Workers may also call `request_decomposition`. Child tasks:

- inherit parent component, labels, epic, requirements, constraints,
  verification and out-of-scope fields;
- receive parent description, acceptance references, decomposition reason and
  handoff in their context;
- inherit the parent run/batch and are included in Conclave review;
- start from a snapshot commit of the parent worktree when files have already
  changed;
- cannot request nested decomposition.

`submit_plan` validates:

- unique task IDs;
- dependency references;
- no dependency cycles;
- known worker profiles.

Planned tasks are created as **Backlog** records. Approval assigns them to the
run batch and moves them to **Ready**.

Approval is rejected while the master is still streaming. This prevents workers
from starting before planning has completely finished.

## Board queue

`internal/board.Runner` is a workspace-level scheduler.

For each worker profile:

1. Check worker instance limit.
2. Check profile and provider concurrency limits.
3. Atomically claim an eligible ready task.
4. Create or resume an isolated git worktree.
5. Run the worker agent.
6. Commit changes, attach branch/commit and move the task to review.

Worker eligibility is based on complexity, labels and configured worker ranges.

## Multi-repository workspaces

`gitx.ResolveRepo` supports two layouts:

- workspace root is a git repository;
- workspace contains immediate child directories that are git repositories.

For group workspaces it scores repository names against:

1. task `write_scope`;
2. task `component`;
3. task title;
4. task description.

Both full names and suffixes are accepted (`node-go` matches
`liberator-node-go`). Worktrees are created under
`.conclave/board/<repository>/`.

## Checkpoints and pause/resume

Every worker writes a checkpoint alongside its worktree:

```text
.conclave/board/<repo>/<task>.resume.json
```

The checkpoint contains:

- worktree path;
- branch;
- base commit;
- full message history.

Checkpoints are updated after each assistant/tool message. A worker error also
preserves the checkpoint. The board retries up to three times automatically;
after that the task returns to **Backlog** with a `FAILED` badge. Moving it back
to Ready resumes from the same worktree and conversation instead of rebuilding
context. A resumed task:

1. loads the checkpoint;
2. repairs dangling tool calls;
3. reuses the same worktree and branch;
4. continues the conversation instead of starting a new attempt.

If no checkpoint exists but an older worktree does, Conclave reuses that
worktree and asks the model to continue from its current state. Checkpoints are
deleted after a task reaches review successfully.

## Model transport

`internal/llm` implements an OpenAI-compatible streaming client.

- SSE and `[DONE]` handling;
- tool-call assembly across chunks;
- batching of independent read-only tool calls;
- parallel execution for read-only shell commands (`git log`, `grep`, `find`,
  `sed -n`, etc.);
- parallel web search/fetch calls;
- reasoning content;
- usage accounting;
- retry for transient HTTP failures;
- configurable first-token and idle timeouts.

Defaults:

```text
first token: 180s
stream idle: 180s
```

Override:

```bash
export CONCLAVE_LLM_FIRST_TOKEN_TIMEOUT=300s
export CONCLAVE_LLM_IDLE_TIMEOUT=300s
```

## Tool batching

The model can request several independent tools in one assistant turn. Conclave
partitions the batch into read-only and side-effecting calls:

- read-only calls run concurrently with up to 8 workers;
- side-effecting calls run serially in the original order;
- mixed batches run read-only calls first, then side effects.

Read-only tools include file/list/glob/grep/git/web tools and shell commands
classified as non-mutating. Shell redirection, in-place edits, commits, package
builds and unknown commands force serial execution.

If the model emits one tool call per turn, Conclave cannot batch it
retroactively. The worker prompt explicitly asks the model to group independent
reads, searches and fetches into a single turn.

## Events and web synchronization

`internal/event.Bus` is a non-blocking fan-out bus. Slow subscribers drop events
instead of blocking agents; clients can re-read authoritative state.

Each workspace application publishes events. The web server:

- forwards workspace events to `/ws?id=<workspace-id>`;
- forwards all workspace events to `/ws/global`;
- tags global events with the workspace ID.

The frontend reloads full state after reconnects and important events. This keeps
the server authoritative and avoids client-side optimistic state drift.

## Persistence

SQLite tables cover:

- sessions;
- runs;
- events;
- board projects and tasks;
- task comments, events, attempts, logs, blockers and claims;
- task result versions and their approval/rejection verdicts;
- question sets;
- usage records.

Schema initialization is idempotent. Lightweight migrations (for example adding
`answered_by` to question sets) run at startup.

## Security boundaries

- Model tools validate paths against the workspace root by default.
- `outside_cwd`, `write`, `shell` and `network` permissions are configurable.
- The web server has no authentication and binds to loopback by default.
- Worker worktrees are isolated from the main working tree until explicitly
  merged/applied.

See [configuration.md](configuration.md) and [web-ui.md](web-ui.md).
