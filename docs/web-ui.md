# Web UI

Start the web server:

```bash
./conclave serve --addr 127.0.0.1:8787
```

Open the printed URL. The server binds to loopback by default and has no
authentication layer.

## Workspace picker

The first screen lets you:

- browse the filesystem;
- open an existing workspace;
- create a workspace directory;
- choose from recently opened workspaces;
- see workspace state and counters;
- forget a workspace.

Workspace state values:

| State | Meaning |
|---|---|
| `active` | An agent, queue or Conclave run is active |
| `waiting` | There are pending questions |
| `idle` | No active work |

Counters show running agents, open tasks and pending questions. Active
workspaces are sorted first.

### Forget workspace

**Forget** removes the workspace from the registry and closes its runtime
instance. It does **not** delete the workspace directory or `.conclave/` data.
Deleting `.conclave/` is always a manual operation.

An active workspace cannot be forgotten until its agents and queue stop.

## Top bar

The top bar shows:

- current workspace and project key;
- number of running agents;
- Build/Conclave mode selector;
- Plan-only toggle in Build;
- model selector;
- reasoning effort selector.

The agent counter is updated from queue events and reflects currently claimed
worker tasks.

## Chat

Chat supports:

- streaming model output;
- thinking/reasoning blocks;
- tool cards with parsed arguments and results;
- syntax highlighting;
- message actions;
- recovery actions.

### Message actions

User messages:

- Revert;
- Fork.

Assistant messages:

- Fork;
- Regenerate on the latest assistant message.

### Recovery panel

If the last turn did not complete, chat shows **Last run was interrupted** at the
bottom of the conversation with:

- **Continue** — continue from saved progress;
- **Retry last turn** — rerun the last user turn.

If a request fails, **Request failed** offers Continue, Retry, Logs and Dismiss.

Panels are rendered at the end of the conversation in chronological order.

## Sessions

The sidebar lists sessions for the workspace.

- **New chat** creates a Build session.
- Selecting a session switches the workspace's active session.
- **Delete chat** deletes the selected session after confirmation.
- Deleting the active session creates a new empty Build session.
- Session switching and deletion are blocked while an agent is running.

Mode, session and view are encoded in the URL, so a reload restores them.

## Board

The board is shared across all sessions in the workspace.

Columns:

```text
Backlog · Ready · In Progress · Needs Planning · Review · Blocked · Done
```

Card information includes task ID, priority, complexity, labels, worker and
branch/commit where available.

### Task detail

Task detail contains:

- overview and acceptance criteria;
- dependencies and write scope;
- execution attempts and logs;
- comments and activity;
- branch and commit;
- edit, retry, review and delete actions.

Tasks that are `claimed` or `in_progress` cannot be deleted.

### Plan approval

When Conclave has a pending plan, the board shows a banner:

```text
Conclave plan awaiting approval
```

Click **Approve & start** to move planned backlog tasks to Ready and start the
queue. If the master is still finishing its response, the button is disabled and
shows **Master is finishing…**.

### Queue

- **Start queue** starts worker scheduling.
- **Pause queue** cancels active workers and returns them to Ready.
- When a Conclave run is paused, **Start queue** behaves as **Resume run**.

## Questions

Questions are displayed as board-like columns:

| Column | Meaning |
|---|---|
| **Needs answer** | Waiting for a human or pending answer |
| **Master review** | Pending master review |
| **Answered by me** | Answered by the user |
| **Answered by AI** | Answered by a non-human actor |

Answering is available only for unanswered questions. Resolved questions show
the selected/custom answers and the answer time.

Question sets can be deleted from their card.

## Usage

Usage can be grouped by:

- model;
- session.

The view shows input/output tokens, request counts and context-window usage.
Usage is attributed to sessions, tasks and agents where available.

## Agents

The Agents view is a live list of running model agents. Each row shows:

- agent kind and ID;
- related board task;
- worker/profile role;
- provider and model;
- context usage;
- input/output tokens;
- running time and request count;
- current phase/tool.

Actions:

- **Open task** switches to Board and opens the task detail modal.
- **Profile** switches to Config → Profiles.

The list is refreshed every two seconds while the view is open. Agents
disappear when their run finishes or is cancelled.

## Config

The Config view manages the global configuration:

- providers and base URLs;
- API keys;
- models and capabilities;
- remote model discovery;
- profiles;
- worker routing, complexity ranges and concurrency limits;
- MCP servers (stdio/HTTP, command/url/headers/environment, enabled);
- native web search provider and keys.

See [configuration.md](configuration.md) for the YAML schema.

## Notifications

The server exposes:

- `/ws?id=<workspace-id>` for workspace events;
- `/ws/global` for events from all workspaces.

Toasts show events from other workspaces with the source workspace name.
The workspace list refreshes when global events arrive.

## Reload and reconnect behavior

The frontend treats the server as authoritative:

- state is loaded from `/api/state`;
- chat history is rebuilt from the server snapshot;
- reconnects trigger a full state refresh;
- important events trigger state refresh;
- streamed deltas update the visible chat for responsiveness.

This means a browser reload during an agent run should restore the session,
mode, messages, Conclave plan and running status.

## Developer workflow

Backend:

```bash
go test ./...
go build -o conclave ./cmd/conclave
```

Frontend:

```bash
cd web
npm install
npm run dev
```

The Vite dev server proxies `/api` and `/ws` to `127.0.0.1:8787` by default.
Use a different backend port with:

```bash
CONCLAVE_WEB_TARGET=http://127.0.0.1:8811 npm run dev
```

Production build:

```bash
cd web
npm run build
cd ..
go build -o conclave ./cmd/conclave
```

Vite writes assets to `internal/web/dist/`, which is embedded into the Go
binary.
