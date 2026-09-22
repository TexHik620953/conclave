# conclave

A multi-role LLM agent orchestrator written in Go. Several editable roles
(manager, architect, senior/middle/junior, security reviewer, DBA, frontend,
designer, …) work on a task, and each role can use its own model. Providers are
any OpenAI-compatible API: cloud (OpenAI, DeepSeek, OpenRouter, …) or local
(Ollama). Coding is just one case; pipelines can be configured for any task.

> Русская версия документации: [README.md](README.md).

## Contents

- [Quick start](#quick-start)
- [Use cases](#use-cases)
- [Concepts](#concepts)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [Pipelines and node types](#pipelines-and-node-types)
- [Scheduler](#scheduler)
- [Conditions and templates](#conditions-and-templates)
- [Tools and security](#tools-and-security)
- [Context, limits and summarization](#context-limits-and-summarization)
- [Reliability: retry, budget, resume](#reliability-retry-budget-resume)
- [Storage](#storage)
- [CLI](#cli)
- [MCP](#mcp)
- [Web UI](#web-ui)
- [opencode integration](#opencode-integration)
- [Examples](#examples)
- [Extending](#extending)
- [Development](#development)

---

## Quick start

```bash
go build -o conclave ./cmd/conclave

# create config in ./.conclave (providers, 15 roles, prompts, 9 pipelines)
./conclave config init

# validate it
./conclave config validate

# see what is available
./conclave pipelines list
./conclave roles list

# start the web UI (HTTP + WebSocket) on http://127.0.0.1:8080
./conclave serve

# or expose conclave as an MCP server for opencode
./conclave mcp
```

**You don't have to write a pipeline.** By default the `auto` pipeline runs a
`supervisor` node where the LLM manager decides which roles to run, in what
order, iterating until the task is done. The user just describes the task — the
controller plans and delegates. Ready-made declarative pipelines (`feature`,
`research`, `solve`, …) remain as examples for repeatable processes.

Pipelines are started through the web UI (`serve`). There are no separate
`run`/`ask` CLI commands — only web, MCP and debugging commands (role/pipeline/
run listings, `config`, `version`).

### Docker

```bash
docker build -t conclave .
docker run --rm -p 8080:8080 \
  -v "$PWD/.conclave:/data/.conclave" \
  -v "$PWD/workspace:/workspace" \
  -e OPENAI_API_KEY conclave serve --addr 0.0.0.0:8080
```

The image embeds the built frontend; `--data-dir` defaults to `/data/.conclave`.
When publishing on `0.0.0.0`, **always** pass `--token` (see the Web UI section):
without a token the API and WebSocket are open to anyone who can reach the port.

By default roles point at `openai/gpt-4o` and the like. To work out of the box
without cloud keys, install Ollama and change role `model` to
`ollama/qwen2.5-coder:7b` (or set `settings.default_model`).

---

## Use cases

The scenarios below assume the config has been created (`conclave config init`)
and `conclave serve` is running; after that you act in the browser
(`http://127.0.0.1:8080`) or over MCP.

### 1. Feature with planning, code and multi-role review

Pipeline `feature`: the architect builds a plan → the controller decides whether
it is ready → a gate asks for approval → a senior implements → `security_reviewer`,
`qa` and `devops` review in parallel → a review/fix loop until `PASS` → the
tech writer summarizes.

- Pipeline: `feature`, Task: "Add CSV export of runs in `internal/web`".
- Workspace: path to the repository.
- Watch the timeline: the plan, diffs via `apply_patch`/`edit_file`, `run_tests`
  calls, `STATUS: PASS/FAIL` verdicts.
- Artifacts `plan.md`, `implementation.md`, `review.md`, `summary.md` appear in
  the **Result** panel (`download .md`).

### 2. Quick bug fix

Pipeline `solve`: plan → execute → parallel verification (`qa`,
`security_reviewer`) → summary. Handy when you don't need gates and a long loop.

- Pipeline: `solve`, Task: "`TestFoo` fails on empty input — find and fix it,
  add a regression test".

### 3. Dynamic mode: the controller picks the roles itself

Pipeline `auto` uses a `supervisor` node: the LLM manager decides whom to call
(`delegate`), can run several in parallel (`delegate_parallel`) and ends the
step via `finish`. Nothing needs to be hardcoded in the graph.

- Pipeline: `auto`, Task: "Figure out why latency on `/api/runs` is growing and
  propose a fix".
- Limits: available roles — the node's `roles`, number of steps — `max_steps`,
  cost — `budget_usd`.
- When to use: tasks of unknown shape where you can't tell in advance which
  specialists are needed. For repeatable processes a fixed pipeline is better.

### 4. Research and brief

Pipeline `research`: a researcher and a critic iterate, then a brief is written.
Roles can be given `web_search` (Exa/SearXNG) and `http_fetch`.

- Pipeline: `research`, Task: "Compare approaches to session storage in Go and
  recommend one".

### 5. Decision with a user question

Pipeline `decision`: research → options → `controller` with `ask_user` (the
question appears in the browser) → record the decision. A role can ask you to
choose (`single`/`multi`/custom answer).

- Pipeline: `decision`, Task: "Pick a database for a queue service".

### 6. Document / article

Pipeline `write`: outline → draft → review → final.

- Pipeline: `write`, Task: "Write a 2-page onboarding guide for the project".

### 7. Ops change

Pipeline `ops`: assessment → plan → review → summary. Useful for migrations, CI
changes and infrastructure edits.

- Pipeline: `ops`, Task: "Plan the move from GitHub Actions to self-hosted
  runners".

### 8. Design concept

Pipeline `design`: research → concept → critique → specification.

- Pipeline: `design`, Task: "Concept for an onboarding screen in the mobile
  client".

### 9. Chat: clarify while work is in progress

During an active run the input box at the bottom stays enabled. Send a message —
it is queued and injected into the roles/controller between iterations (a "you"
card appears in the timeline). This lets you steer a task on the fly without
stopping the run:

- "Don't touch the public API, only internal functions".
- "Ignore the linter warnings, finish the functionality first".

If the run has already finished, the message starts a new turn in the same
session, keeping the chat history.

### 10. Re-run a specific step (revert)

A step failed or you want a different result: in the node card click **revert
from here** — that node and everything after it are deleted, then **resume**
re-runs them. Handy for retrying one tool call or step without redoing the whole
pipeline.

### 11. Local models (Ollama)

```yaml
# .conclave/config.yaml
providers:
  ollama:
    type: openai
    base_url: "http://localhost:11434/v1"
settings:
  default_model: ollama/qwen2.5-coder:7b
```

Verify with `ollama pull qwen2.5-coder:7b && conclave config validate`. Cheap and
private; for hard steps you can keep a cloud model only for the architect and
reviewers.

### 12. Different models for different roles

Each role has its own `model` and `fallback`:

```yaml
# roles/architect.yaml — a strong model for planning
model: openrouter/anthropic/claude-3.5-sonnet
fallback: ["openai/gpt-4o"]

# roles/junior.yaml — a cheap local model for routine work
model: ollama/qwen2.5-coder:7b
```

### 13. MCP from opencode (no browser)

opencode calls conclave as a tool (see the
[opencode integration](#opencode-integration) section):

- "Run `conclave_run_pipeline` with pipeline `feature` and task …".
- "Ask the `security_reviewer` role about this diff" (`conclave_ask_role`).

opencode stays the driver; the heavy multi-model work goes to conclave.

### 14. Read-only analysis (safe mode)

To let roles only analyze, set permissions on the role/node:

```yaml
permissions:
  fs_read: true
  fs_write: false
  shell: false
  network: false
tools: [read_file, list_dir, glob, grep]
```

Such a reviewer cannot modify the workspace. Useful for code audits and research
on untrusted repositories.

### 15. Your own pipeline for your process

Add `.conclave/pipelines/<name>.yaml` (see
[Pipelines and node types](#pipelines-and-node-types)) and use `edges` with
`when`, `controller`, `gate.ask` and `supervisor`. The pipeline is
immediately available in the web UI and over MCP.

---

## Concepts

**Provider (`providers`)** — an OpenAI-compatible HTTP endpoint. One format for
all: `/chat/completions`, SSE streaming, `tools`/`tool_calls`, `/models`.

**Role (`roles`)** — an agent persona: system prompt, model + fallback chain,
tool set, permissions, temperature. Roles are fully editable and not tied to
coding.

**Pipeline (`pipelines`)** — a graph of nodes. There are seven node types (see
below); edges are set with `next` or `edges` with conditions.

**Run state (blackboard)** — a shared `State`: inputs, node outputs, artifacts,
todo list, accumulated tokens/cost, iteration number. Roles never talk directly —
only through state and artifacts.

**Artifact** — a named result of a step (`plan.md`, `review.md`, `todos.json`,
…). Stored in `State`, written to disk and to SQLite.

**Run** — one execution of a pipeline with a unique `run-id`, an event history
and a cost.

---

## Architecture

```
cmd/conclave/                 CLI, web adapter, asker, MCP wiring
internal/
  config/                     YAML, global->project merge, env interpolation, validation, JSON Schema
  provider/                   OpenAI-compatible client (SSE, tool_calls), registry, typed errors
  llm/                        high-level Complete: model fallback + retry/backoff
  role/                       role + agent loop, context assembly, summarization
  tool/                       tools, sandbox, permissions, todo, ask_user
  orchestrator/               pipeline engine, scheduler, conditions, templates, state
  store/                      SQLite (sessions/messages/runs/nodes/events/artifacts) + artifact files
  event/                      event bus
  memory/                     context trimming by budget
  mcp/                        MCP server (stdio, elicitation) and clients (stdio + HTTP/SSE)
```

Data flow:

```
config ──▶ provider.Registry ──▶ llm.Client ──┐
                                              ▼
pipeline ──▶ orchestrator.Engine ──▶ role.Runtime ──▶ tool.Registry
                    │                     │
                    ▼                     ▼
                event.Bus            State (blackboard)
                    │
                    ▼
              store (SQLite + files) / CLI / Web / MCP
```

---

## Configuration

Config is read from two places; project overrides global (by key):

1. Global: `~/.config/conclave/` (or `$XDG_CONFIG_HOME/conclave`).
2. Project: `./.conclave/` (or `--project-dir`).

Each directory is read for `config.yaml` plus `roles/*.yaml` and
`pipelines/*.yaml`. Role prompts resolve relative to the directory where the role
is defined (`prompt_file`), so global roles keep their prompts even if a project
overrides other fields.

Environment interpolation is supported: `${VAR}` and `${VAR:-default}`.
`api_key_env: OPENAI_API_KEY` pulls the key from the environment.

Full schema: `conclave config schema`.

### `providers`

```yaml
providers:
  ollama:
    type: openai
    base_url: "http://localhost:11434/v1"
    models: ["qwen2.5-coder:7b"]
  openai:
    type: openai
    base_url: "https://api.openai.com/v1"
    api_key_env: OPENAI_API_KEY
  openrouter:
    type: openai
    base_url: "https://openrouter.ai/api/v1"
    api_key_env: OPENROUTER_API_KEY
    headers:
      HTTP-Referer: "https://example.com"
```

A model reference is always `provider/model`, and `model` may contain slashes:
`openrouter/anthropic/claude-3.5-sonnet`.

### `settings`

| Key | Purpose |
|---|---|
| `default_model` | model for roles without their own `model` |
| `default_pipeline` | pipeline when a run doesn't name one (default `auto` — the controller decides) |
| `controller` | role for controller/supervisor nodes without `role` |
| `max_parallel` | maximum parallel nodes |
| `max_iterations` | agent-loop iteration limit |
| `max_tokens` | default response limit |
| `max_context_bytes` | trim the context block |
| `timeout` | timeout of a single LLM call |
| `workspace` | default working directory |
| `defaults` | default permissions/sandbox (see below) |
| `model_limits` | model context window for display/summarization |
| `on_no_user` | `gate.ask` behavior with no user: `next`\|`else`\|`stop` |
| `retry` | `max_attempts`, `base_delay`, `max_delay` |
| `model_prices` | prices per 1M tokens (`input_per_1m`, `output_per_1m`) |
| `budget_usd` | run cost limit (0 = unlimited) |
| `on_budget` | `warn` (default) or `abort` |
| `summarize_threshold` | fraction of the limit (0..1) that triggers summarization; `0` disables |
| `summarizer_model` | model for summarization (defaults to the role's model) |
| `web_search` | `web_search` provider: `provider` (`exa`/`searxng`), `api_key_env` (e.g. `EXA_API_KEY`), `base_url`, `max_results`, `search_type` |

### `roles`

```yaml
# roles/senior.yaml
id: senior
title: "Senior Engineer"
description: "Implements features and fixes."
model: deepseek/deepseek-chat
fallback: ["openai/gpt-4o", "ollama/qwen2.5-coder:7b"]
prompt_file: prompts/senior.md     # or inline system_prompt
temperature: 0.2
max_tokens: 8192
tools: [read_file, write_file, edit_file, apply_patch, list_dir, glob, grep, run_shell, run_tests, git]
permissions:
  fs_read: true
  fs_write: true
  shell: true
  network: true
outputs: [implementation.md]
```

`permissions` are tri-state: an unset value falls back to `settings.defaults`
(defaults: `fs_read: true`, `fs_write: false`, `shell: false`, `network: true`).

### `mcp`

External MCP servers whose tools are exposed to roles as `<server>__<tool>`. For
a tool to be available to a role it must be listed in the role's `tools`.

```yaml
mcp:
  github:
    command: ["npx", "-y", "@modelcontextprotocol/server-github"]
    env:
      GITHUB_PERSONAL_ACCESS_TOKEN: "${GITHUB_TOKEN}"
  remote:
    url: "https://mcp.example.com/mcp"
    headers:
      Authorization: "Bearer ${MCP_TOKEN}"
```

---

## Pipelines and node types

The simplest pipeline is a single `supervisor` node that decides everything:

```yaml
# pipelines/auto.yaml (used by default)
description: "Controller-driven: the manager decides which roles to run, iteratively, until it finishes."
inputs: [task]
settings:
  max_parallel: 4
nodes:
  - id: lead
    type: supervisor
    role: controller
    prompt: >-
      Deliver the task. Delegate to the specialist roles with `delegate` /
      `delegate_parallel`, review the results and iterate until done, then call
      `finish` with a summary.
    max_steps: 12
    roles: [architect, senior, middle, junior, security_reviewer, qa, dba, frontend, backend, devops, tech_writer, researcher, critic]
    output: result.md
```

For a repeatable process, describe the graph explicitly with `agent`, `parallel`,
`controller`, `gate` and `transform`:

```yaml
# pipelines/feature.yaml
description: "Implement a feature with planning, coding and multi-role review."
inputs: [task]
settings:
  max_parallel: 4
nodes:
  - id: analyze
    type: agent
    role: architect
    prompt: "Analyze the task and produce a plan."
    output: plan.md
    next: [approve]
  - id: approve
    type: gate
    ask: true
    prompt: "Approve the plan before implementation?"
    else: stop
    next: [implement]
  - id: implement
    type: agent
    role: senior
    output: implementation.md
    next: [review]
  - id: review
    type: parallel
    roles: [security_reviewer, qa, devops]
    output: review.md
    next: [summarize]
  - id: summarize
    type: agent
    role: tech_writer
    output: summary.md
  - id: stop
    type: transform
    template: "Plan was not approved."
```

### Node types

| Type | What it does |
|---|---|
| `agent` | Runs one role (agent loop with tools), writes output to `output`. |
| `parallel` | Runs several roles **concurrently** (limit `max_parallel`), aggregates their outputs into one artifact and into `outputs[<role>]`. |
| `controller` | Asks the LLM manager which of `choices` to pick; only the chosen branch runs, the rest are pruned. |
| `supervisor` | Dynamic mode: the controller LLM itself decides which roles to run via the `delegate` / `delegate_parallel` tools and ends the step with `finish`. Available roles — `roles`, step limit — `max_steps`. This is the default mode (`auto`). |
| `gate` | Evaluates `condition`; with `ask: true` it also asks the user (approve/reject). On `false` it goes to `else` (or stops). |
| `transform` | Renders `template` from state, no LLM call. |

### Edges

- `next: [id1, id2]` — direct transitions.
- `edges:` with `when:` — conditional transitions:

```yaml
edges:
  - {from: decide, to: implement, when: "plan.approved"}
  - {from: decide, to: stop}
```

- If neither `next` nor `edges` are set, nodes run sequentially in declaration
  order.
- For `controller` and `gate.else`, edges are created implicitly so the scheduler
  counts joins correctly.
- Node-level `tools` **override** the role's tool set for that step — useful when
  the same step needs different permissions in different pipelines. Usually
  role-level tools are enough.

### Data exchange

Each node receives the task + inputs + outputs of previous nodes (the context
block). It writes its result to `outputs[node_id]` and to an artifact
(`node.output` or `<node_id>.md`). This keeps the context from exploding with
many models.

---

## Scheduler

The engine executes the graph in waves using reference counting:

1. Each node's incoming edge count is computed (`pending`).
2. The start node runs first.
3. Ready nodes in a wave run **in parallel** (worker pool of `max_parallel`).
4. After a node, `pending` is decremented for all outgoing edges; if the counter
   reaches zero and the node was selected by at least one predecessor, it becomes
   ready; if not selected, the node is **pruned**, cascading to its descendants
   (so diamond joins don't deadlock).
5. Repeat while there are ready nodes.

This yields correct join semantics (a node with two inputs waits for both) and
correct controller/gate branching. State is mutex-protected and SQLite runs in
single-threaded mode.

---

## Conditions and templates

### Conditions (`when`, `condition`)

Supported:

- `&&`, `||`, `!`, parentheses;
- comparisons `==`, `!=`, `>`, `<`, `>=`, `<=`, the `contains` operator;
- properties: `review.passed`, `review.failed`, `review.empty`, `review.nonempty`;
- references: `outputs.<node>`, `artifacts.<name>`, `inputs.<name>`, `iterations`;
- `true`/`false`.

Examples:

```
review.passed
security_reviewer.passed && qa.passed
iterations >= 3
artifacts.plan.md contains rollback
```

`passed` = the text contains `PASS`/`APPROV` and no `FAIL`. Reviewers end their
answer with a `STATUS: PASS` or `STATUS: FAIL` line by prompt.

### Templates (`transform.template`)

```
{{task}}              task
{{inputs.NAME}}       input
{{outputs.NODE}}      node output
{{artifacts.NAME}}    artifact
{{NODE}}              same as outputs.NODE
```

---

## Tools and security

Tools (enabled per role via `tools`):

| Tool | Purpose |
|---|---|
| `read_file`, `list_dir`, `glob`, `grep` | read/search |
| `write_file`, `edit_file` | write/point edit |
| `apply_patch` | apply a unified diff |
| `run_shell` | shell command in the workspace |
| `run_tests` | auto-detect the test runner (go/npm/cargo/pytest) and run it |
| `git` | git subcommands |
| `http_fetch` | HTTP(S) request |
| `web_search` | web search (Exa, as in opencode; or self-hosted SearXNG) |
| `todo_write`, `todo_read` | run todo list |
| `ask_user` | ask the user (single/multi/custom); works in CLI, MCP (elicitation) and Web |
| `<server>__<tool>` | tools of external MCP servers |

`web_search` uses Exa (like opencode) and needs a key: `EXA_API_KEY` (or
`settings.web_search.api_key`). A keyless alternative is a self-hosted SearXNG
(`provider: searxng`, `base_url`). If search is not configured the tool returns a
clear error and the role falls back to `http_fetch`.

### Where tool descriptions come from

The model sees three kinds of instructions:

1. **Role system prompt** — `prompts/<role>.md` (or inline `system_prompt`).
2. **Node prompt** — `prompt:` in the pipeline YAML.
3. **Tool description and JSON schema** (what appears in `tools`).

For built-in tools (including `web_search`) the description and schema live in
code: `internal/tool/<name>.go` — methods `Name()`, `Description()`, `Schema()`.
For example `web_search` — `internal/tool/websearch.go`, and its provider/key —
`settings.web_search`.

For external MCP servers descriptions and schemas come **from the server itself**
(`tools/list`); conclave only forms the `<server>__<tool>` name and adds the
`[MCP:<server>]` prefix (`internal/mcp/manager.go`). There is no separate
"prompt" for an MCP tool in the config — change the description on the server
side or the instruction in the role's system prompt.

Tool availability is set by a role's `tools` list; node-level `tools` overrides
it for a specific step. An empty list = all available tools.

Security:

- **Sandbox**: all paths resolve relative to `workspace`; escaping it is
  forbidden (symlinks are resolved and rejected if they point outside).
- **Permissions**: `fs_read`, `fs_write`, `shell`, `network` — per role, with
  defaults.
- **Shell**: command allowlist (`allowed_commands`), denylist (`denied_commands`),
  timeout, output limit. When an allowlist is set, shell metacharacters (`;`,
  `&&`, `|`, redirects, `$()`, backticks) are rejected and the command runs
  directly without `sh -c` — an allowed command cannot be bypassed. Without an
  allowlist the command goes through `sh -c` (full shell, only with explicit role
  permission).
- **Git**: dangerous options (`-C`, `--git-dir`, `-c`, `--exec-path`, …) and
  network/global-state subcommands (`clone`, `fetch`, `push`, `remote`, `config`,
  `submodule`, …) are rejected.
- **Network**: host allowlist (`allowed_hosts`); http/https only. `http_fetch`
  blocks loopback/private/link-local/metadata IPs and re-validates every redirect
  (SSRF protection) unless the host is explicitly allowed.
- All results are truncated by `max_output_bytes`.

---

## Context, limits and summarization

- `model_limits` sets the model window. On every call a `context.usage` event is
  published with `prompt_tokens` and `context_limit`; the CLI and Web UI show
  `ctx 1.2k/128k`.
- If the estimated prompt size exceeds `summarize_threshold × limit`, older
  messages are compressed via `summarizer_model` (default: the role's model). The
  system prompt and the most recent exchanges are kept; a `tool_calls`/`tool`
  pair is never split. A `context.summarized` event is published.
- `max_context_bytes` additionally trims the context block before it is given to
  a role.

---

## Reliability: retry, budget, resume

**Model fallback.** A role has `model` and `fallback: [...]`; on error the
request goes to the next model.

**Retry.** Transient errors (429, 5xx, network, timeout) are retried with
exponential backoff and respect for `Retry-After`. Configured via
`settings.retry`. 4xx (except 408/409/429) are not retried. If streaming has
already emitted tokens, no retry happens (to avoid duplicate output), and
fallback to another model is also skipped.

**Cost and budget.** Computed from `model_prices`; cost accumulates in state and
is written to the DB. When `budget_usd` is exceeded a warning is printed and the
run continues by default; `on_budget: abort` (or the flag) stops the run.
Summarization calls are also accounted for.

**Pause / Resume / Revert.** Each node is saved to the DB with status, prompt and
output. `POST /api/runs/{id}/pause` gently stops a run (status `paused`),
`POST /api/runs/{id}/resume` continues from unfinished nodes (restores outputs,
artifacts and todos, skips completed ones). `POST /api/runs/{id}/revert` with a
`{"node_id": "..."}` body deletes that node and everything that ran after it —
then `resume` re-runs them (retrying a step or a specific tool call). The
equivalent of resume via creation: `POST /api/runs {"resume_run_id": "<run-id>"}`.

---

## Storage

- SQLite (pure Go, `modernc.org/sqlite`) in `--data-dir` (default `./.conclave`):
  tables `sessions`, `messages`, `runs`, `nodes`, `events`, `artifacts`, `meta`.
- **Sessions** are chats: one session holds several runs (`runs.session_id`) and a
  canonical message log (`messages`), so a conversation is restored after a
  reload. All events (`role.message`, `tool.call`, `tool.result`,
  `user.question`, `context.usage`, `user.message`) are persisted with their
  payload and replayed in the timeline.
- The schema is versioned (`meta.schema_version`); migrations are applied in
  order.
- Artifact files: `<data-dir>/runs/<run-id>/<name>`.
- Runs send heartbeats; only runs whose owner stopped heartbeating for more than
  90 seconds are marked `interrupted`, so a concurrent `runs list` does not break
  active runs.
- Run statuses: `running`, `completed`, `failed`, `aborted` (cancelled),
  `interrupted` (process was killed).
- Ctrl+C during `serve` (or cancelling a run in the web UI) cancels the run via
  context and marks it `aborted` instead of leaving it `running`.

---

## CLI

Global flags: `--global-dir`, `--project-dir`, `--no-global`, `--data-dir`.

| Command | Description |
|---|---|
| `serve` | HTTP + WebSocket server with the web UI (`--addr`, `--token`) — the main way to run |
| `roles list` / `roles show <role>` | list/show roles |
| `pipelines list` / `pipelines show <pipeline>` | list/show pipelines |
| `runs list` | recent runs |
| `runs show <id>` | nodes, artifacts, cost |
| `runs artifacts <id>` | artifact paths |
| `runs logs <id> [--limit N] [--type T]` | event log |
| `mcp` | MCP server over stdio (for opencode and others) |
| `config init [--global] [--force]` | write the seed config |
| `config validate` / `config show` / `config schema` | validate/path/JSON Schema |
| `version` | version |

Pipelines and roles are started via `serve` (web) or `mcp` (external client);
there are no separate `run`/`ask` commands.

---

## MCP

**Server** (`conclave mcp`, stdio JSON-RPC 2.0) exposes tools:

- `list_roles`, `list_pipelines`
- `run_pipeline` `{pipeline, task, workspace?, inputs?}`
- `ask_role` `{role, prompt, context?, workspace?}`
- `get_run` `{run_id}`
- `get_artifact` `{run_id, name}`

It also supports **elicitation**: `ask_user` inside a run sends the client an
`elicitation/create` with the question's JSON schema (single/multi/custom). If the
client does not support it, it degrades gracefully: the role decides itself
(`on_no_user`).

**Client** can connect to external MCP servers:

- stdio (`command`);
- remote Streamable HTTP + SSE (`url`, `headers`), with `Mcp-Session-Id`.

Tools of external servers appear to roles as `<server>__<tool>`.

---

## Web UI

`conclave serve --addr 127.0.0.1:8080 [--token X]` starts an HTTP server that
serves the embedded frontend (Vue3) and the API. It listens on localhost by
default; with `--token`, `Authorization: Bearer X` is required (for WebSocket —
`?token=X`).

> **Security.** The token is optional, but with an `--addr` other than loopback
> set `--token`: without it the REST API and WebSocket are open to anyone who can
> see the port. The WebSocket accepts only same-origin (plus localhost for the
> Vite dev server), token comparison is constant-time, and request bodies are
> limited to 4 MiB.

- **REST API**: `GET /api/config`, `POST /api/runs`, `GET /api/runs`,
  `GET /api/runs/{id}`, `GET /api/runs/{id}/artifacts/{name}`,
  `POST /api/runs/{id}/cancel`, `POST /api/runs/{id}/pause`,
  `POST /api/runs/{id}/resume`, `POST /api/runs/{id}/revert`,
  `POST /api/runs/{id}/followup`, `POST /api/runs/{id}/answer`, `POST /api/ask`;
  chats: `GET/POST /api/sessions`, `GET/DELETE /api/sessions/{id}`,
  `GET/POST /api/sessions/{id}/messages`.
- **WebSocket** `/api/ws` — streaming event protocol (ours, not OpenAI):

  ```jsonc
  // server -> client
  {"type":"hello","protocol":1}
  {"type":"run.started","run_id":"...","message":"feature"}
  {"type":"node.started","run_id":"...","node_id":"analyze","role":"architect","model":"openai/gpt-4o"}
  {"type":"role.delta","run_id":"...","node_id":"analyze","role":"architect","model":"openai/gpt-4o","message":"token text"}
  {"type":"role.message","run_id":"...","role":"architect","model":"openai/gpt-4o","message":"full answer"}
  {"type":"tool.call","run_id":"...","role":"researcher","model":"...","message":"web_search","data":{"id":"call_1","name":"web_search","arguments":"{...}"}}
  {"type":"tool.result","run_id":"...","role":"researcher","model":"...","message":"web_search","data":{"id":"call_1","name":"web_search","is_error":false,"content":"..."}}
  {"type":"context.usage","run_id":"...","data":{"prompt_tokens":1234,"completion_tokens":567,"total_tokens":1801,"context_limit":128000}}
  {"type":"todos.updated","run_id":"...","data":{"todos":[{"id":"t1","content":"...","status":"pending"}]}}
  {"type":"user.question","run_id":"...","message":"Which DB?","data":{"id":"q1","question":"Which DB?","options":[{"label":"Postgres"}],"multiple":false,"allow_custom":true}}
  {"type":"run.finished","run_id":"...","message":"completed"}

  // client -> server
  {"type":"subscribe","run_id":"..."}   // empty run_id = all
  {"type":"unsubscribe"}
  {"type":"ping"}
  ```

  An answer to a question is sent via `POST /api/runs/{id}/answer`
  `{"id":"q1","selected":["Postgres"],"custom":""}`.

- **What you see in the browser**:
  - **landing screen**: a big "What should the team work on?" box — just describe
    the task and hit Start. No pipeline to pick: by default the `auto` pipeline
    runs, where the controller decides which roles to call. Pipeline and
    workspace are under "Advanced". Example task chips are provided;
  - **chats/sessions**: a list of sessions in the sidebar; clicking opens a chat
    with the timeline and the latest run. A session survives a page reload and a
    server restart;
  - **unified timeline** of a run in chronological order: nodes, role messages
    (`role · model`), tool calls (arguments **and** result, including MCP tools
    like `server__tool`), events, context usage; steps of different roles are
    separated by colored markers and bars;
  - **clickable nodes**: clicking a node opens its card — initial prompt, context,
    available tools and all messages of that node;
  - role answers render as **Markdown** (GFM);
  - **Timeline / Result** toggle: the Result panel shows only the final result —
    the last message or a selected artifact (`.md`) — without intermediate steps;
  - status bar: status, `ctx current/limit` with a bar, `in`/`out` (tokens sent
    to the network and generated by it), cost, time;
  - todo list, run history and role viewer;
  - **run control**: `pause` (soft stop, status `paused`), `resume` (continue
    from unfinished nodes), `cancel` (status `aborted`);
  - **revert**: in a node card — "revert from here": deletes that node and the
    following ones, then `resume` re-runs them (handy for retrying a specific
    tool call/step);
  - **questions from models**: if a role has `ask_user` in `tools` it can ask a
    question right in the browser — a card with options (single/multi) and a
    custom answer field appears in the timeline; the run waits for the answer;
  - **download**: in the Result panel a `download .md` button saves the selected
    artifact or the last message;
  - **messaging during work**: the input box is always enabled. If a run is
    active, the message is queued and injected into the roles/controller between
    iterations (a "you" card appears in the timeline); if the run has finished, a
    new turn starts in the same session;
  - **follow-up**: an extra instruction with which the run **continues in the
    same context**: previous outputs and the timeline are kept, the instruction is
    appended to the task (`## Follow-up`), and all nodes re-run with it. It is not
    a reset but a new iteration on top of the previous one.
- Text streams token by token (`role.delta`), not all at once at the end.
- Cancelling an active run — the `cancel` button (`POST /api/runs/{id}/cancel`),
  status becomes `aborted`.

### Frontend development

```bash
make web            # npm install + vite build -> internal/web/dist (embed)
cd web && npm run dev   # Vite dev server proxying /api and /api/ws to :8080
```

Sources are in `web/` (Vue3 + Vite + TS); the build is embedded into the binary
via `//go:embed`. `make build` builds the frontend and the binary; `make build-go`
is Go-only.

---

## opencode integration

opencode connects conclave as a local MCP server. Example `opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "conclave": {
      "type": "local",
      "command": ["conclave", "mcp"],
      "enabled": true
    }
  }
}
```

After that opencode sees the tools `conclave_run_pipeline`, `conclave_ask_role`,
etc. opencode stays the driver/TUI, while the heavy multi-model work goes to
conclave.

---

## Examples

### Pipelines (seed)

`auto` is the default; the rest are examples of declarative processes.

- `auto` — the controller-supervisor picks the roles itself and iterates until
  `finish` (**default**, `default_pipeline: auto`).
- `feature` — the controller-supervisor drives a feature: plan → code → review →
  fixes.
- `research` — the controller-supervisor: research + critique + brief.
- `write` — outline, draft, review, final.
- `ops` — assessment, plan, review, summary.
- `design` — research, concept, critique, specification.
- `decision` — research, options, controller (with `ask_user`), record the
  decision.
- `solve`, `review` — general tasks and review.

### Roles (seed)

`controller`, `architect`, `senior`, `middle`, `junior`, `security_reviewer`,
`qa`, `dba`, `frontend`, `backend`, `devops`, `tech_writer`, `designer`,
`researcher`, `critic`.

---

## Extending

**New role**: create `roles/<id>.yaml` (or a file in `.conclave/roles/`) and a
prompt in `prompts/<id>.md`. The role is immediately available to pipelines.

**New pipeline**: add `pipelines/<name>.yaml`. Use `edges` with `when`,
`controller`, `gate.ask` and `supervisor` as needed.

**New provider**: add an entry to `providers` with `base_url` (and `api_key_env`,
`headers` if needed). No code changes required.

**New tool**: implement the `tool.Tool` interface and register it in
`internal/tool/tool.go` (`builtins`), or connect an MCP server.

**External call**: `run_pipeline` / `ask_role` over MCP, or CLI/Web.

---

## Development

```bash
gofmt -l .
go vet ./...
go test ./...
go build ./cmd/conclave
```

Tests cover: config load/merge/env, JSON Schema, context limits and prices, SSE
tool_call aggregation, retry/backoff, tool sandbox and permissions (shell/git/fs/
symlink/SSRF), todo and ask_user, answer parsing, the scheduler (conditions,
diamond join, gate/else, on_no_user), resume, run cancellation, context
summarization, MCP elicitation, apply_patch, the store (id validation, sessions
and messages, heartbeat/interrupted, migrations), an integration pipeline run
with a mock provider, the dynamic `supervisor`, in-flight message injection, the
REST/WebSocket server and web_search.

### Key packages

- `internal/orchestrator/engine.go` — scheduler, nodes, gate/controller/supervisor.
- `internal/orchestrator/supervisor.go` — dynamic controller-driven delegation.
- `internal/orchestrator/condition.go` — condition evaluator.
- `internal/role/role.go` — agent loop and summarization.
- `internal/provider/client.go` — OpenAI-compatible client.
- `internal/mcp/server.go` / `http.go` — MCP server and HTTP client.
- `internal/web/` — HTTP server, REST API, WebSocket hub and protocol.
- `internal/tool/` — tools and sandbox (including `web_search`).
- `web/` — Vue3 + Vite frontend (built into `internal/web/dist`).
