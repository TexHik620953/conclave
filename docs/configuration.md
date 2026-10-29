# Configuration

Conclave reads YAML configuration from two locations and merges them:

1. global: `~/.config/conclave/config.yaml`
2. project: `<workspace>/.conclave/config.yaml`

Project values win. On first load, provider and profile definitions found in a
project config are migrated into the global config so API keys and models are
shared across workspaces.

Use `conclave config path` to print the project path and
`conclave config validate` to validate the merged result.

## Minimal example

```yaml
version: 1
default_profile: default

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

profiles:
  default:
    agent:
      model: openrouter/deepseek/deepseek-chat
      effort: medium
      temperature: 0.2
      max_steps: 120
    master:
      model: openrouter/deepseek/deepseek-chat
      effort: high
      max_steps: 80
    workers:
      - id: coder
        model: openrouter/deepseek/deepseek-chat
        effort: medium
        complexity: [1, 2, 3]
        instances: 4
        max_steps: 120
        tools:
          - read_file
          - write_file
          - edit_file
          - list_dir
          - glob
          - grep
          - shell
          - git
    limits:
      max_parallel: 8
      max_iterations: 6
      providers:
        openrouter: 8
```

## Providers

| Field | Description |
|---|---|
| `base_url` | OpenAI-compatible base URL, without `/chat/completions` |
| `api_key` | API key; `${ENV_VAR}` expansion is supported |
| `max_parallel` | Provider-wide worker concurrency limit |
| `headers` | Optional extra HTTP headers |
| `models` | Models enabled for this provider |

Model IDs may contain slashes, for example `deepseek/deepseek-chat` under the
OpenRouter provider. The first slash in a model reference separates provider and
model:

```yaml
model: openrouter/deepseek/deepseek-chat
```

### Models

| Field | Description |
|---|---|
| `context` | Context window in tokens |
| `max_output` | Maximum output tokens |
| `capabilities.tools` | Model supports tool calls |
| `capabilities.reasoning` | Model exposes reasoning content |
| `capabilities.effort` | Model supports reasoning effort |
| `effort_map` | Optional mapping from Conclave effort names to provider values |

If `max_output` is omitted it defaults to `8192`.

## Profiles

A profile controls model routing.

### Agent

Build-mode agent.

```yaml
agent:
  model: openrouter/deepseek/deepseek-chat
  effort: medium
  temperature: 0.2
  max_steps: 120
```

### Master

Conclave planning and review agent.

```yaml
master:
  model: openrouter/anthropic/claude-sonnet-4
  effort: high
  max_steps: 80
```

### Workers

Workers execute board tasks.

| Field | Description |
|---|---|
| `id` | Worker role, for example `coder`, `senior`, `researcher` |
| `model` | Model reference |
| `effort` | `low`, `medium`, `high` or `ultra` |
| `complexity` | Complexity levels this worker may claim |
| `instances` | Maximum simultaneous tasks for this worker |
| `tools` | Optional tool subset |
| `prompt` | Optional prompt override key |
| `max_steps` | Maximum agent steps per task |

Worker concurrency is limited by:

```text
min(worker.instances, provider.max_parallel, profile.limits.max_parallel)
```

### Limits

```yaml
limits:
  max_parallel: 8
  max_iterations: 6
  providers:
    openrouter: 8
    deepseek: 4
```

`max_iterations` controls how many Conclave task pools a run may execute.

## Permissions

```yaml
permissions:
  shell: allow
  write: allow
  outside_cwd: deny
  network: allow
  yolo: false
```

`allow` and `deny` are enforced. `ask` is reserved for a future interactive
approval flow and currently behaves like the default value. `outside_cwd: deny`
is strongly recommended for untrusted workspaces.

## MCP servers

Conclave can connect to MCP servers:

```yaml
mcp:
  local:
    type: stdio
    command: ["node", "/path/to/server.js"]
    environment:
      TOKEN: ${MCP_TOKEN}
    enabled: true

  remote:
    type: http
    url: https://example.com/mcp
    headers:
      Authorization: Bearer ${MCP_TOKEN}
```

MCP tools are registered into Build, Conclave workers and board workers when the
server is available.

### Example presets

```yaml
mcp:
  playwright:
    type: stdio
    command: ["npx", "-y", "@playwright/mcp@latest"]

  github:
    type: stdio
    command: ["npx", "-y", "@modelcontextprotocol/server-github"]
    environment:
      GITHUB_PERSONAL_ACCESS_TOKEN: ${GITHUB_TOKEN}

  sqlite:
    type: stdio
    command: ["uvx", "mcp-server-sqlite", "--db-path", "/path/to/db.sqlite"]

  remote:
    type: http
    url: https://example.com/mcp
    headers:
      Authorization: Bearer ${MCP_TOKEN}
```

Install the corresponding Node/Python package first. The Web UI shows the tool
names exposed by each server (`mcp__<server>__<tool>`).

## Prompt overrides

Prompt text can be overridden by key:

```yaml
prompts:
  worker: |
    You are a careful implementation worker...
  worker.coder: |
    You are the coder worker...
  reviewer: |
    Review the proposed changes...
```

Supported keys include `worker`, `worker.<worker-id>` and mode-specific prompt
keys used by `internal/prompt`.

## Web search

Native `web_search` supports four providers:

```yaml
search:
  provider: tavily          # tavily | brave | searxng | exa
  api_key: ${TAVILY_API_KEY}
  base_url: ""              # required for searxng
  max_results: 5
```

Provider keys:

| Provider | Key | Base URL |
|---|---|---|
| `tavily` | `TAVILY_API_KEY` | fixed |
| `brave` | `BRAVE_API_KEY` | fixed |
| `exa` | `EXA_API_KEY` | fixed |
| `searxng` | none | required |

The tool accepts `query`, `max_results`, `recency`, `include_domains` and
`exclude_domains`. Configure it in **Web UI → Config → MCP & search**.

## Web fetch

`web_fetch` supports HTML → Markdown conversion, JSON pretty-printing and plain
text. Private/loopback network access is blocked by default.

Allow private addresses only for trusted local setups:

```yaml
permissions:
  allow_private_network: true
```

## MCP management

MCP servers can be added in **Web UI → Config → MCP & search**. Environment
variables in `command`, `environment`, `headers` and `url` are expanded from the
process environment.

## Environment variables

| Variable | Purpose |
|---|---|
| `DEEPSEEK_API_KEY` | Default DeepSeek key |
| `OPENROUTER_API_KEY` | Default OpenRouter key |
| `CONCLAVE_DB` | Override the global SQLite path used by CLI helpers |
| `CONCLAVE_LLM_FIRST_TOKEN_TIMEOUT` | Time to first token, default `180s` |
| `CONCLAVE_LLM_IDLE_TIMEOUT` | Max stream idle time, default `180s` |
| `TAVILY_API_KEY` | Tavily web search key |
| `BRAVE_API_KEY` | Brave Search key |
| `EXA_API_KEY` | Exa search key |
| `XDG_CONFIG_HOME` | Override the global config directory |

Workspace databases are normally stored under:

```text
<workspace>/.conclave/conclave.db
```

The standalone `conclave sessions` command uses `CONCLAVE_DB` or the default
global data directory.

## Timeouts and long-running models

A provider may take longer than expected to emit the first token, especially
with a large context or reasoning models. Increase limits without rebuilding:

```bash
export CONCLAVE_LLM_FIRST_TOKEN_TIMEOUT=300s
export CONCLAVE_LLM_IDLE_TIMEOUT=300s
./conclave serve --addr 127.0.0.1:8787
```

Timeout failures are retried once before the task is reported as failed. The
worker checkpoint remains available for retry.

## Updating configuration at runtime

The web UI **Config** view can:

- add/delete providers;
- save/clear API keys;
- fetch remote model lists;
- add/edit/delete models;
- edit profiles and worker settings.

Changes are written to the global config and reloaded into open workspaces.
