# conclave-core

Cloud **orchestration core** for multi-role LLM agents. See [PLAN.md](PLAN.md)
for the full design and roadmap.

> Status: **P0–P5 + stateless execution complete**. P0 scaffold; P1 plan graph +
> scheduler + gates + questions; P2 controller hierarchy
> (Interviewer/Planner/Dispatcher/Critic) + re-planning; P3 metrics/tracing +
> expanded eval + timeline; P4 quotas, budgets and rate limiting; P5 playbook
> registry/versions and more playbooks; then durable jobs with leased workers.

## What works today

- PostgreSQL (or in-memory) persistence with versioned migrations.
- Device-token auth (HMAC-SHA256 hashed tokens).
- Sessions, specs, plans, plan nodes/edges, playbook runs, artifacts,
  questions and an append-only event log.
- **Plan scheduler**: executes a DAG of playbooks in parallel up to a quota,
  waits on `dependency`/`data` edges, passes upstream outputs to downstream
  nodes, cascades cancellation on failure, and is resumable.
- **Non-blocking human gates**: a `gate` node opens a question and waits without
  blocking sibling branches; `review`/`dependency` edges gate their own
  successors.
- **Questions with auto-accept**: sessions can auto-answer with the first
  (recommended) option.
- **Controller hierarchy**:
  - **Interviewer**: turns an idea into a Spec by asking clarifying questions
    (resumable across requests).
  - **Planner**: builds a plan graph from the Spec with an LLM.
  - **Dispatcher**: resolves tier `auto` and escalates junior→middle→senior.
  - **Critic**: reviews each playbook result against the Spec and can reopen the
    work with concrete feedback, escalating the minimum tier.
- **Re-planning (Plan v+n)**: the planner can revise a plan graph mid-run,
  preserving the state/output of nodes that are kept, up to a version limit.
- **Reliability**: bounded LLM retries; session pause/resume.
- **Stateless execution**: plan runs are enqueued as durable jobs and executed
  by background workers with leases and heartbeats. Crashed workers are
  detected by lease expiry and their stuck nodes are reconciled and retried.
- **Realtime**: user-scoped tokens and a client WebSocket at `/ws/client`
  (Centrifuge) that streams the event log with history/recovery, plus
  **token-level streaming** of assistant messages (coalesced deltas, persisted
  partial content). A device-scoped agent WebSocket at `/ws/agent` executes
  external tools (`tool.call`/`tool.result`). The broker is in-process by
  default and Redis for cross-replica delivery.
- **Observability**: Prometheus metrics at `/metrics` and OpenTelemetry spans.
- **Quotas & budgets**: per-session budget with cost accounting, plan-version
  limit, configurable parallelism, per-tenant rate limiting.
- One playbook executed by a `supervisor` against an OpenAI-compatible LLM
  (or a fake when no LLM is configured).
- WebSocket endpoint speaking the typed agent protocol (handshake, ping/pong,
  event resume).
- **Eval harness** with golden cases, baselines/regressions and a CLI runner
  (`cmd/eval`).
- **Playbook registry**: built-ins plus loading from a directory, versions.
- Built-in playbooks: `ba`, `general`, `backend`, `frontend`,
  `security_review`, `dba`, `design`, `architecture`.

## Requirements

- Go 1.26+ (only for the local binary path; `docker compose up -d` needs just Docker)
- Docker — to run Postgres/Redis (or the whole platform via compose)

## Quick start

### Docker (everything at once)

```bash
cd conclave-core
CORE_LLM_BASE_URL="https://api.openai.com/v1" CORE_LLM_API_KEY="sk-..." \
  CORE_LLM_MODEL="gpt-4o-mini" CORE_SECRET_KEY="change-me" \
  docker compose up -d
```

This builds and starts **Postgres + Redis + core** (HTTP/WS on
http://127.0.0.1:8090). `make up` does the same. Stop with `make down` (the
Postgres volume is kept). All `CORE_*` variables are read from the host
environment; without an LLM it uses the fake one.

### Local (Go binary)

```bash
cd conclave-core

# 1. Start Postgres + Redis
make dev-db

# 2. Configure the LLM (any OpenAI-compatible endpoint) and run
export CORE_LLM_BASE_URL="https://api.openai.com/v1"
export CORE_LLM_API_KEY="sk-..."
export CORE_LLM_MODEL="gpt-4o-mini"
export CORE_SECRET_KEY="change-me"          # admin key + device-token secret
export CORE_DB_DSN="postgres://conclave:conclave@localhost:5432/conclave?sslmode=disable"

make run
```

Without `CORE_LLM_BASE_URL` the core uses a fake LLM that finishes immediately —
handy to exercise the API without keys. Without `CORE_DB_DSN` it uses an
in-memory store (nothing is persisted).

### Try the API

```bash
# Bootstrap a tenant, user and device (returns a device token once).
curl -s -H "X-Admin-Key: $CORE_SECRET_KEY" -H 'Content-Type: application/json' \
  -d '{"tenant_name":"Acme","email":"a@b.c","device_name":"laptop"}' \
  http://127.0.0.1:8090/v1/bootstrap

TOKEN=cc_...   # from the response

# Create a session and run the BA playbook.
SID=$(curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"Online shop"}' http://127.0.0.1:8090/v1/sessions | jq -r .id)

curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"playbook_id":"ba","idea":"build an online shop"}' \
  http://127.0.0.1:8090/v1/sessions/$SID/runs | jq

# Inspect artifacts and events.
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8090/v1/sessions/$SID | jq
curl -s -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:8090/v1/sessions/$SID/events?from_seq=0" | jq
```

## HTTP API

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/healthz` | — | Liveness. |
| GET | `/metrics` | — | Prometheus metrics. |
| POST | `/v1/bootstrap` | `X-Admin-Key` | Create tenant + user + device, return a device token. |
| POST | `/v1/users/token` | `X-Admin-Key` | Issue a user-scoped token (`user_id`) for the client WebSocket. |
| GET | `/v1/devices/me` | Bearer (device) | Return the device authenticated by the token (id/user/tenant). |
| GET | `/v1/playbooks` | Bearer | List playbooks. |
| GET | `/v1/playbooks/{id}` | Bearer | Get a playbook template. |
| GET | `/v1/roles` | Bearer | List roles, grades and resolved model refs (DB config). |
| GET/POST/PUT/DELETE | `/v1/admin/providers[/{id}]` | Admin key | Manage providers (keys stored in the DB, never returned). |
| GET/POST | `/v1/admin/providers/{id}/models` | Admin key | List/create models of a provider. |
| PUT/DELETE | `/v1/admin/models/{id}` | Admin key | Update/delete a model. |
| GET/POST/PUT/DELETE | `/v1/admin/roles[/{id}]` | Admin key | Manage roles. |
| GET/POST | `/v1/admin/roles/{id}/grades` | Admin key | List/create grades of a role. |
| PUT/DELETE | `/v1/admin/grades/{id}` | Admin key | Update/delete a grade. |
| GET/PUT/DELETE | `/v1/admin/brain-configs[/{name}]` | Admin key | Manage top-level brain models. |
| POST | `/v1/sessions` | Bearer | Create a session. |
| GET | `/v1/sessions` | Bearer | List sessions for the device's tenant. |
| GET | `/v1/sessions/{id}` | Bearer | Session, latest spec, artifacts, questions. |
| POST | `/v1/sessions/{id}/spec` | Bearer | Add a spec version. |
| POST | `/v1/sessions/{id}/runs` | Bearer | Run a single playbook (`playbook_id`, `idea`). |
| GET | `/v1/sessions/{id}/events?from_seq=N` | Bearer | Event log / resume. |
| GET | `/v1/sessions/{id}/timeline?from_seq=N` | Bearer | Combined timeline read model (session, events, plan, artifacts, questions). |
| GET | `/v1/sessions/{id}/messages` | Bearer | Assistant messages, including streaming partial content. |
| POST | `/v1/sessions/{id}/plans` | Bearer | Submit a plan graph (`nodes`, `edges`) or `{"auto":true}`; enqueues a job (202). |
| GET | `/v1/sessions/{id}/plan` | Bearer | Read model: active plan, nodes, edges, artifacts, questions. |
| POST | `/v1/sessions/{id}/plans/{planID}/resume` | Bearer | Enqueue the plan to continue (202). |
| POST | `/v1/sessions/{id}/plans/{planID}/replan` | Bearer | Revise the plan (`reason`) and enqueue the new version (202). |
| POST | `/v1/sessions/{id}/interrupt` | Bearer | Request cancellation of the session's active jobs. |
| POST | `/v1/sessions/{id}/cancel` | Bearer | Cancel all jobs and mark the session cancelled. |
| DELETE | `/v1/sessions/{id}` | Bearer | Delete the session and all of its data. |
| POST | `/v1/sessions/{id}/message` | Bearer | Send a message (`content`) to the session controller. Idle: enqueues a `session` job (202 `job`). Busy: queues it for injection (202 `{"injected":true}`). |
| GET | `/v1/sessions/{id}/jobs` | Bearer | List execution jobs for a session. |
| GET | `/v1/jobs/{id}` | Bearer | Get a job's state, attempts and error. |
| GET | `/v1/sessions/{id}/questions` | Bearer | List questions. |
| POST | `/v1/questions/{id}/answer` | Bearer | Answer a question (`selected`, `custom`); resumes a gate plan, an interview or the session controller (all async, 202). |
| POST | `/v1/sessions/{id}/interview` | Bearer | Start a requirements interview (`idea`) as a background job (202). |
| POST | `/v1/sessions/{id}/pause` | Bearer | Pause a session. |
| POST | `/v1/sessions/{id}/resume` | Bearer | Resume a session and its active plan. |
| GET | `/ws` | Bearer (`?token=`) | Legacy agent protocol WebSocket (handshake + event replay). |
| GET | `/ws/client` | User token (Centrifuge connect token) | Client realtime WebSocket: subscribe to `client:session:<id>` and receive live events. |
| GET | `/ws/agent` | Device token (Centrifuge connect token) | Agent WebSocket: receives `tool.call`, replies `tool.result`, binds sessions via `session.open`. |

`POST /v1/sessions/{id}/plans` accepts `{"auto": true}` to let the Planner build
the graph from the session's spec.

### Session controller

`POST /v1/sessions/{id}/message` is the single top-level entry point. The
**session controller** — one LLM loop — reads the session state (spec, plan and
node states, dialogue, new messages) and calls exactly one tool: `ask`,
`write_spec`, `set_plan`, `run_plan`, `replan` or `finish`. Each decision runs as
a durable `session` job.

- When the session is idle, `message` enqueues a `session` job.
- When work is already running, the text is stored as a **pending user message**
  (`kind=user`, `state=pending`) and returned as `{"injected":true}`. The
  controller and running roles **drain the inbox between iterations** and inject
  those messages into the live context, then mark them `injected`. Messages
  survive restarts.
- Every user message emits a `user.message` event.
- When a `run_plan` job finishes, the worker hands control back to the
  controller, which reviews the node states and continues, replans or finishes.
- Controller-asked questions have `kind=controller`; answering one re-enqueues
  the controller.

The lower-level endpoints (`interview`, `plans`, `runs`, `resume`, `replan`)
remain for scripting and advanced use.

### Plan graph

```jsonc
// POST /v1/sessions/{id}/plans
{
  "nodes": [
    {"id": "a", "playbook_id": "ba", "grade": "senior"},
    {"id": "g", "kind": "gate", "title": "Approve the requirements?"},
    {"id": "b", "playbook_id": "backend", "grade": "junior"}
  ],
  "edges": [
    {"from": "a", "to": "g"},            // dependency (default)
    {"from": "a", "to": "b", "kind": "data"}  // b receives a's output
  ]
}
```

`playbook_id` is the role key and `grade` pins the grade for that node (the
controller sets both when it builds the graph).

Edge kinds: `dependency` (wait for completion), `data` (also pass the
predecessor's output as context), `review` (gate-oriented). The response
reports `status: done | waiting | failed`; a `waiting` plan is resumed after its
questions are answered.

## Configuration

| Env | Default | Purpose |
|---|---|---|
| `CORE_LISTEN_ADDR` | `127.0.0.1:8090` | HTTP/WS bind address. |
| `CORE_DB_DSN` | *(empty)* | Postgres DSN; empty = in-memory. |
| `CORE_SECRET_KEY` | *(empty)* | Admin key and device-token HMAC secret. |
| `CORE_LLM_BASE_URL` | *(empty)* | OpenAI-compatible base URL; empty = fake LLM. |
| `CORE_LLM_API_KEY` | *(empty)* | LLM API key. |
| `CORE_LLM_MODEL` | `gpt-4o-mini` | Bootstrap default model (seed only; DB is source of truth). |
| `CORE_PROVIDERS` | *(empty)* | Bootstrap providers (seed only, see below). |
| `CORE_DEFAULT_PROVIDER` | `default` | Bootstrap default provider (seed only). |
| `CORE_CONFIG_POLL` | `15s` | How often a replica checks `config_version` for hot reload. |
| `CORE_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error`. |
| `CORE_PLAYBOOK_DIR` | *(empty)* | Import extra YAML templates when seeding roles. |
| `CORE_MAX_PARALLEL` | `4` | Max parallel plan nodes. |
| `CORE_MAX_PLAN_VERSIONS` | `5` | Re-planning version limit per session. |
| `CORE_BUDGET_USD` | `0` | Default per-session budget (0 = unlimited). |
| `CORE_PRICE_INPUT_PER_1M` | `0` | Default input price for cost accounting. |
| `CORE_PRICE_OUTPUT_PER_1M` | `0` | Default output price for cost accounting. |
| `CORE_RATE_LIMIT_RPS` | `0` | Per-tenant request rate (0 = disabled). |
| `CORE_RATE_LIMIT_BURST` | `0` | Per-tenant burst (0 = rate). |
| `CORE_FANOUT` | `memory` | Realtime broker: `memory` (single node) or `redis` (cross-replica). |
| `CORE_STREAM_HISTORY_SIZE` | `1000` | Publications retained per channel for recovery. |
| `CORE_STREAM_HISTORY_TTL` | `1h` | Channel history retention. |
| `CORE_DELTA_FLUSH_MS` | `75ms` | Coalescing interval for token deltas. |
| `CORE_TOOL_TIMEOUT` | `60s` | Timeout for a tool call to a local agent. |
| `CORE_WORKERS` | `2` | Number of background execution workers. |
| `CORE_JOB_LEASE` | `60s` | Job lease duration without a heartbeat. |
| `CORE_JOB_POLL` | `500ms` | Worker idle poll interval. |

### Providers, models, roles and grades

Configuration lives in the database and is hot-reloaded by every replica — no
restart needed. The env vars below are only a **bootstrap seed**, applied when the
`providers`/`roles` tables are empty; afterwards the DB is the source of truth.

Data model (migration `0012`):
- `providers` — OpenAI-compatible endpoint + API key (stored as plain text).
- `provider_models` — models per provider, with per-million prices.
- `roles` — the functions (backend, frontend, dba, ...).
- `role_grades` — a role's grades (junior/middle/senior/...), each with a
  `model_id` and tools.
- `brain_configs` — models for the top-level brains `controller`, `planner`,
  `interviewer`, `critic`, `supervisor` (the **controller** is the main role that
  builds the plan graph and assigns a role+grade to each node).
- `config_version` — bumped by triggers on every config write; each replica polls
  it (`CORE_CONFIG_POLL`) and atomically swaps its snapshot when it changes.

Model references are `provider/model`, resolved against the current snapshot.
Role grades always carry an explicit model (models are configured manually).

Manage configuration over the admin API (`X-Admin-Key: <CORE_SECRET_KEY>`):

```bash
# provider (+ key, stored in the DB)
curl -sX POST localhost:8090/v1/admin/providers -H 'X-Admin-Key: change-me' \
  -d '{"name":"deepseek","base_url":"https://api.deepseek.com/v1","api_key":"sk-..."}'

# model under that provider
curl -sX POST localhost:8090/v1/admin/providers/<pid>/models -H 'X-Admin-Key: change-me' \
  -d '{"name":"deepseek-chat","input_price_per_1m":0.2,"output_price_per_1m":0.5}'

# role + a grade bound to the model
curl -sX POST localhost:8090/v1/admin/roles -H 'X-Admin-Key: change-me' \
  -d '{"key":"backend","title":"Backend"}'
curl -sX POST localhost:8090/v1/admin/roles/<rid>/grades -H 'X-Admin-Key: change-me' \
  -d '{"grade":"senior","rank":2,"model_id":"<mid>"}'

# main brain model
curl -sX PUT localhost:8090/v1/admin/brain-configs/controller -H 'X-Admin-Key: change-me' \
  -d '{"model_id":"<mid>"}'
```

`GET /v1/roles` (device token) returns roles, grades and resolved model refs.

Bootstrap seed (only used while the DB tables are empty):

```bash
CORE_PROVIDERS='{
  "openai":   {"base_url":"https://api.openai.com/v1","api_key":"sk-..."},
  "deepseek": {"base_url":"https://api.deepseek.com/v1","api_key":"sk-..."},
  "ollama":   {"base_url":"http://host.docker.internal:11434/v1","models":["qwen2.5-coder:7b"]}
}'
CORE_DEFAULT_PROVIDER=deepseek
CORE_LLM_MODEL=deepseek-chat
```

The built-in playbooks (`internal/playbook/builtin/`) seed the `roles`/`role_grades`
tables with every grade bound to the default provider/model. `CORE_PLAYBOOK_DIR`
additionally imports YAML templates at seed time. The legacy single-endpoint vars
(`CORE_LLM_BASE_URL`/`CORE_LLM_API_KEY`) define provider `default`.

## Layout

```
cmd/core/                 server entrypoint
internal/
  api/                    HTTP control plane + WebSocket endpoint
  auth/                   device-token registration/verification
  config/                 environment configuration
  conf/                   DB-backed config snapshot, hot reload, LLM gateway, seed
  domain/                 entities, state machines, event types
  eval/                   evaluation harness + golden cases
  events/                 append-only log + in-process fanout
  llmgw/                  LLM gateway (interface, fake, openai client)
  orchestrator/           supervisor + brain/role-runner interfaces + fakes
  playbook/               playbook templates, registry, builtins
  protocol/               legacy agent protocol frames + codec + resume tracker
  realtime/               client WebSocket (Centrifuge) + event relay
  stream/                 token-delta envelope + context sink
  scheduler/              plan DAG execution, quotas, gates, resume
  session/                session lifecycle + plan service
  store/                  Store contract + memory + postgres implementations
  toolgw/                 tool gateway (interface + fake)
  worker/                 durable job workers (lease, heartbeat, reconcile)
  obs/                    metrics + OpenTelemetry tracing
migrations live in internal/store/postgres/migrations/
```

## Development

```bash
make fmt
make vet
make test
```

Run the store contract tests against Postgres and the cross-replica test against
Redis:

```bash
make dev-db
CORE_TEST_DB_DSN="postgres://conclave:conclave@localhost:5432/conclave?sslmode=disable" go test ./internal/store/ -v
CORE_TEST_REDIS_ADDR="redis://127.0.0.1:6379" go test ./internal/realtime/ -run CrossReplica -v
```

## Observability

- **Metrics**: `GET /metrics` (Prometheus text). Includes HTTP requests/latency,
  plan and node outcomes, playbook/role runs, critic verdicts, LLM requests,
  tokens and latency.
- **Tracing**: OpenTelemetry spans for `scheduler.run` and `playbook.run`, written
  to the logger by default. Swap in an OTLP exporter in `internal/obs/tracing.go`.

## Quotas and budgets

- Per-session `budget_usd` (default from `CORE_BUDGET_USD`). Token usage and cost
  are recorded per session and an LLM call is aborted once the budget is
  exceeded.
- `CORE_MAX_PARALLEL` bounds scheduler parallelism.
- `CORE_MAX_PLAN_VERSIONS` bounds re-planning churn.
- `CORE_RATE_LIMIT_RPS`/`CORE_RATE_LIMIT_BURST` rate-limit each tenant.

## Playbooks

Built-in templates live in `internal/playbook/builtin/`. Add your own by putting
`*.yaml` files in `CORE_PLAYBOOK_DIR`; higher `version` wins. Each template
declares tiered roles, control mode (`supervisor` or `explicit-graph`),
guidelines and a question policy. Roles with an empty `model` use the gateway
default model.

## Eval

```bash
go test ./internal/eval/ -v      # run the golden suite
go run ./cmd/eval                 # run and print metrics
go run ./cmd/eval -out report.json -write-baseline baseline.json
go run ./cmd/eval -baseline baseline.json   # fail on regressions
```

Golden cases live in `internal/eval/cases.go`. They run playbooks/plans with
deterministic fakes and assert artifacts, events, plan status and open
questions. A baseline lets CI fail on regressions.

## Realtime (client WebSocket)

`/ws/client` is a [Centrifuge](https://centrifugal.dev) endpoint for user-scoped
clients. Authenticate the connection with a user token (from
`POST /v1/users/token`), then subscribe to channels:

- `client:session:<session_id>` — the session event stream (`event.append`):
  node/plan changes, `question.asked`/`answered`, `job.*`, `message.created`;
- `client:session:<session_id>:delta` — ephemeral token deltas (`message.delta`)
  with the accumulated text of the in-progress assistant message;
- `client:user:<user_id>` — user-level notifications.

`/ws/agent` is a device-scoped Centrifuge endpoint. An agent authenticates with
its device token, subscribes to `agent:device:<device_id>`, advertises its tools
via the `tools.register` RPC, binds sessions with the `session.open` RPC, and
answers `tool.call` messages with a `tool.result` RPC. The core routes external
tool calls from roles to the agent bound to the session, exposing the agent's
tool schemas to the model, correlating by `call_id` with a timeout
(`CORE_TOOL_TIMEOUT`).

Token deltas are coalesced (every `CORE_DELTA_FLUSH_MS`) before being persisted
and published, so a long generation does not flood the database. The partial
content is upserted into `messages` (exposed at `/v1/sessions/{id}/messages`)
and finalized with a `message.created` event when the role finishes.

The event log is relayed to these channels, so a client sees a run's progress in
real time. Channel history is enabled, so a reconnecting client recovers missed
events automatically; the HTTP read models (`/plan`, `/timeline`,
`/events?from_seq`) remain available as a fallback. With `CORE_FANOUT=memory`
the broker is in-process; set `CORE_FANOUT=redis` (and `CORE_REDIS_ADDR`) for
cross-replica delivery.

## Statefulness

All durable state lives in Postgres, so **API and worker replicas are
interchangeable** behind a load balancer. The stateful aspects to be aware of:

- **Durable state**: sessions, specs, plans, nodes/edges, playbook runs, tasks,
  artifacts, questions, answers, interviews, jobs, the append-only event log,
  usage and the hot-reloadable configuration (providers, models, roles, grades,
  brains).
- **Durable execution**: plan runs are enqueued as `jobs` and claimed by workers
  with a **lease** (`CORE_JOB_LEASE`) and periodic heartbeats. Claiming uses
  `FOR UPDATE SKIP LOCKED`, so any number of workers can run safely. If a worker
  dies, its lease expires, another worker reclaims the job, **resets nodes stuck
  in `running`** and retries the plan. Node execution is resumable because the
  scheduler recomputes readiness from persisted node states.
- **In-memory store**: when `CORE_DB_DSN` is empty, state lives in the process and
  is lost on restart (dev/tests only).
- **Event fanout**: WebSocket subscribers live in-process; events themselves are
  persisted, so resume-by-seq works across reconnects and replicas.
- **Everything long-running is a job**: plan execution, interviews, LLM
  planning and session-controller decisions all run in background workers. The
  HTTP API only enqueues and reads.
- Redis is configured (`CORE_REDIS_ADDR`) but not used yet.

## Next phases

All planned phases (P0–P5, stateless execution, realtime S1–S5) are
implemented. The local agent with its Web UI lives in
[`../conclave-agent`](../conclave-agent). See [PLAN.md](PLAN.md) §18 for the
phase log and remaining optional items (Centrifuge metrics, richer reconcile).
