# Troubleshooting

## `config not found`

Create a workspace config:

```bash
./conclave config init
```

Then provide a provider key:

```bash
export OPENROUTER_API_KEY=...
# or edit ~/.config/conclave/config.yaml / Web UI → Config
```

## `provider authentication failed`

Check:

- the API key is set before starting Conclave;
- the key belongs to the selected provider;
- the provider base URL is correct;
- the model is enabled under that provider.

The Web UI Config view masks stored keys but can replace or clear them.

## `provider timeout: no response from model`

The provider did not emit a first token within the configured limit or the stream
was idle for too long.

Default limits are now 180 seconds for both cases. Increase them if needed:

```bash
export CONCLAVE_LLM_FIRST_TOKEN_TIMEOUT=300s
export CONCLAVE_LLM_IDLE_TIMEOUT=300s
./conclave serve --addr 127.0.0.1:8787
```

Timeout failures are retried once. The worker checkpoint remains on disk, so a
retry can continue from the existing worktree and conversation.

## `web_search` returns key/configuration errors

Configure a search provider in **Config → MCP & search** or in YAML:

```yaml
search:
  provider: tavily
  api_key: ${TAVILY_API_KEY}
```

Supported providers: `tavily`, `brave`, `searxng`, `exa`. SearXNG requires
`base_url` instead of an API key.

If `web_fetch` refuses a local/private URL, that is intentional SSRF
protection. Enable `permissions.allow_private_network: true` only for trusted
local environments.

## MCP server does not appear

- Check the server is enabled.
- For `stdio`, verify the command exists and starts manually.
- For `http`, verify the URL and headers.
- Use **Config → MCP & search** to re-save; open workspaces reload MCP after
  configuration changes.
- `${ENV_VAR}` placeholders in command, environment, headers and URL are
  expanded before launch.

## `workspace is not a git repository`

Older Conclave versions required the workspace root itself to be a repository.

Current behavior supports group workspaces. For each task, Conclave searches:

1. the workspace root;
2. immediate child directories.

For reliable repository selection, set task `component` or `write_scope`:

```text
component: service-api
write_scope:
  - service-api/internal/**
```

Repository short names work; for example `node-go` matches
`liberator-node-go`.

## Tasks stay in Backlog

Backlog tasks created by Conclave are a plan preview. They move to Ready only
after plan approval.

- Open Chat or Board.
- Find **Conclave plan awaiting approval**.
- Click **Approve plan** / **Approve & start**.

If the master is still streaming, approval is disabled and shows
**Master is finishing…**.

## Plan approval is missing after restart

Conclave persists the pending plan in the run record and restores it on startup.
If the plan record is missing, it reconstructs the approval preview from the
backlog tasks belonging to that run.

If the plan still cannot be restored, ask the master to submit the plan again,
but first avoid creating duplicate backlog tasks manually.

## Pause produces `context canceled`

Expected cancellation events are now treated as `agent.cancelled` and should not
appear as **Request failed**.

If an old error remains visible, dismiss it or reload the page. The server clears
the error on a paused/cancelled run phase.

## Paused tasks restart from scratch

A paused worker should preserve:

```text
.conclave/board/<repo>/<task>-a<N>/
.conclave/board/<repo>/<task>.resume.json
```

Check that these exist. The checkpoint stores the branch, base commit and full
message history.

If a task was paused before checkpoint support was added, Conclave falls back to
the newest existing worktree and asks the model to continue from its current
state.

If both the worktree and checkpoint are removed, the task cannot resume and will
start a new attempt.

## Queue does not claim a task

Only **Ready** tasks are claimable. Check:

- task status is Ready;
- complexity matches a worker range;
- worker has a free instance;
- profile/provider concurrency limits are not exhausted;
- dependencies are Done;
- the queue is running.

## Cannot delete a task

Tasks in `claimed` or `in_progress` cannot be deleted. Stop the queue or wait for
the attempt to finish/cancel, then delete the task.

## Cannot switch or delete a session

Session operations are blocked while an agent or Conclave run is active. Stop or
pause the run first.

Deleting the active session creates a new empty session.

## Workspace Forget returns `409`

The workspace still has an active agent or queue. Pause the queue and stop
running tasks before forgetting it.

## Web UI disconnects

The frontend reconnects its workspace and global WebSockets automatically.
After reconnect it reloads authoritative state from the server.

If the server process was terminated:

1. restart it;
2. reload the browser;
3. if a run was active, use **Resume run** or retry the interrupted task.

## Server was terminated externally

`zsh: terminated ./conclave serve` means the process received `SIGTERM`; it is
not a Go panic. This commonly happens when a build/deploy script kills the
process. Conclave now shuts down gracefully on `SIGINT`/`SIGTERM`, but active
agents are still interrupted.

For a persistent server, run it detached:

```bash
setsid nohup ./conclave serve --addr 127.0.0.1:8787 \
  >/tmp/conclave.log 2>&1 < /dev/null &
```

## Application data

Workspace data:

```text
<workspace>/.conclave/
```

Global config:

```text
~/.config/conclave/config.yaml
```

Global CLI session database (used by `conclave sessions`):

```text
~/.local/share/conclave/conclave.db
```

Remove a workspace's `.conclave/` manually if you want to reset it. Forget only
removes the registry entry.
