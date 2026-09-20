# conclave

Мульти-ролевой оркестратор LLM-агентов на Go. Над задачей работают несколько
редактируемых ролей (менеджер, архитектор, senior/middle/junior, security
reviewer, DBA, frontend, дизайнер и т.д.), каждая может использовать свою
модель. Провайдеры — любой OpenAI-совместимый API: облако (OpenAI, DeepSeek,
OpenRouter, ...) и локальные (Ollama). Кодинг — частный случай; пайплайны
настраиваются под любые задачи.

---

## Содержание

- [Быстрый старт](#быстрый-старт)
- [Концепции](#концепции)
- [Архитектура](#архитектура)
- [Конфигурация](#конфигурация)
- [Пайплайны и типы узлов](#пайплайны-и-типы-узлов)
- [Планировщик](#планировщик)
- [Условия и шаблоны](#условия-и-шаблоны)
- [Инструменты и безопасность](#инструменты-и-безопасность)
- [Контекст, лимиты и суммаризация](#контекст-лимиты-и-суммаризация)
- [Надёжность: retry, бюджет, resume](#надёжность-retry-бюджет-resume)
- [Хранилище](#хранилище)
- [CLI](#cli)
- [MCP](#mcp)
- [Web UI](#web-ui)
- [Интеграция с opencode](#интеграция-с-opencode)
- [Примеры](#примеры)
- [Расширение](#расширение)
- [Разработка](#разработка)

---

## Быстрый старт

```bash
go build -o conclave ./cmd/conclave

# создать конфиг в ./.conclave (провайдеры, 15 ролей, промпты, 8 пайплайнов)
./conclave config init

# проверить
./conclave config validate

# посмотреть, что есть
./conclave pipelines list
./conclave roles list

# запустить пайплайн
./conclave run feature --task "Добавить health-check эндпоинт" --workspace .

# спросить одну роль
./conclave ask architect --prompt "Как разбить эту задачу на шаги?"

# веб-интерфейс (HTTP + WebSocket) на http://127.0.0.1:8080
./conclave tui
```

По умолчанию роли смотрят на `openai/gpt-4o` и т.п. Чтобы работало «из коробки»
без облачных ключей, поставьте Ollama и поменяйте `model` у ролей на
`ollama/qwen2.5-coder:7b` (или задайте `settings.default_model`).

---

## Концепции

**Провайдер (`providers`)** — OpenAI-совместимый HTTP-эндпоинт. Один формат для
всех: `/chat/completions`, стриминг SSE, `tools`/`tool_calls`, `/models`.

**Роль (`roles`)** — агент-персона: системный промпт, модель + fallback-цепочка,
набор инструментов, права, температура. Роли полностью редактируются и не
привязаны к кодингу.

**Пайплайн (`pipelines`)** — граф из узлов. Узлы бывают шести типов (см. ниже),
связи задаются `next` или `edges` с условиями.

**Состояние прогона (blackboard)** — общий `State`: входные данные, выходы узлов,
артефакты, todo-лист, накопленные токены/стоимость, номер итерации. Роли не
общаются напрямую — только через состояние и артефакты.

**Артефакт** — именованный результат шага (`plan.md`, `review.md`, `todos.json`
и т.п.). Хранится в `State`, пишется на диск и в SQLite.

**Прогон (run)** — один запуск пайплайна с уникальным `run-id`, историей событий
и стоимостью.

---

## Архитектура

```
cmd/conclave/                 CLI, web-адаптер, asker, MCP-обвязка
internal/
  config/                     YAML, merge global->project, env-интерполяция, валидация, JSON Schema
  provider/                   OpenAI-совместимый клиент (SSE, tool_calls), registry, типизированные ошибки
  llm/                        высокоуровневый Complete: fallback по моделям + retry/backoff
  role/                       роль + agent-loop, сборка контекста, суммаризация
  tool/                       инструменты, sandbox, права, todo, ask_user
  orchestrator/               движок пайплайна, планировщик, условия, шаблоны, состояние
  store/                      SQLite (runs/nodes/events/artifacts) + файлы артефактов
  event/                      шина событий
  memory/                     обрезка контекста по бюджету
  mcp/                        MCP-сервер (stdio, elicitation) и клиенты (stdio + HTTP/SSE)
```

Поток данных:

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

## Конфигурация

Конфиг читается из двух мест, project перекрывает global (по ключам):

1. Global: `~/.config/conclave/` (или `$XDG_CONFIG_HOME/conclave`).
2. Project: `./.conclave/` (или `--project-dir`).

В каждой директории читается `config.yaml` плюс `roles/*.yaml` и
`pipelines/*.yaml`. Промпты ролей резолвятся относительно директории, где роль
определена (`prompt_file`), поэтому глобальные роли сохраняют свои промпты, даже
если проект переопределяет другие поля.

Поддерживается интерполяция окружения: `${VAR}` и `${VAR:-default}`.
`api_key_env: OPENAI_API_KEY` подставляет ключ из окружения.

Полная схема: `conclave config schema`.

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

Ссылка на модель везде имеет вид `provider/model`, причём `model` может содержать
слэши: `openrouter/anthropic/claude-3.5-sonnet`.

### `settings`

| Ключ | Назначение |
|---|---|
| `default_model` | модель для ролей без своего `model` |
| `controller` | роль для controller-узлов без `role` |
| `max_parallel` | максимум параллельных узлов |
| `max_iterations` | лимит итераций agent-loop и loop-узлов |
| `max_tokens` | дефолтный лимит ответа |
| `max_context_bytes` | обрезка контекстного блока |
| `timeout` | таймаут одного вызова LLM |
| `workspace` | рабочая директория по умолчанию |
| `defaults` | права/sandbox по умолчанию (см. ниже) |
| `model_limits` | размер окна модели для отображения/суммаризации |
| `on_no_user` | поведение `gate.ask` без пользователя: `next`\|`else`\|`stop` |
| `retry` | `max_attempts`, `base_delay`, `max_delay` |
| `model_prices` | цены за 1M токенов (`input_per_1m`, `output_per_1m`) |
| `budget_usd` | лимит стоимости прогона (0 = без лимита) |
| `on_budget` | `warn` (по умолчанию) или `abort` |
| `summarize_threshold` | доля лимита (0..1), при которой включается суммаризация; `0` — выключить |
| `summarizer_model` | модель для суммаризации (по умолчанию модель роли) |
| `web_search` | провайдер `web_search`: `provider` (`exa`/`searxng`), `api_key_env` (напр. `EXA_API_KEY`), `base_url`, `max_results`, `search_type` |

### `roles`

```yaml
# roles/senior.yaml
id: senior
title: "Senior Engineer"
description: "Implements features and fixes."
model: deepseek/deepseek-chat
fallback: ["openai/gpt-4o", "ollama/qwen2.5-coder:7b"]
prompt_file: prompts/senior.md     # либо inline system_prompt
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

`permissions` тристейтные: незаданное значение берётся из `settings.defaults`
(по умолчанию `fs_read: true`, `fs_write: false`, `shell: false`,
`network: true`).

### `mcp`

Внешние MCP-серверы, инструменты которых доступны ролям под именами
`<server>__<tool>`. Чтобы инструмент был доступен роли, он должен быть указан в
её `tools`.

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

## Пайплайны и типы узлов

```yaml
# pipelines/feature.yaml
description: "Implement a feature with planning, coding and review."
inputs: [task]
settings:
  max_parallel: 4
nodes:
  - id: analyze
    type: agent
    role: architect
    prompt: "Analyze the task and produce a plan."
    output: plan.md
    next: [decide]
  - id: decide
    type: controller
    role: controller
    choices: [approve, stop]
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
    next: [revise]
  - id: revise
    type: loop
    until: "review.passed"
    max_iterations: 3
    body: [review, fix]
    next: [summarize]
  - id: fix
    type: agent
    role: senior
    output: fixes.md
  - id: summarize
    type: agent
    role: tech_writer
    output: summary.md
  - id: stop
    type: transform
    template: "Controller stopped before implementation."
```

### Типы узлов

| Тип | Что делает |
|---|---|
| `agent` | Запускает одну роль (agent-loop с инструментами), пишет выход в `output`. |
| `parallel` | Запускает несколько ролей **одновременно** (лимит `max_parallel`), агрегирует их выводы в один артефакт и в `outputs[<role>]`. |
| `controller` | Спрашивает LLM-менеджера, какой из `choices` выбрать; выполняется только выбранная ветка, остальные pruning-ом отсекаются. |
| `loop` | Повторяет `body` (узлы выполняются напрямую, в порядке списка) до выполнения `until` или `max_iterations`. |
| `gate` | Вычисляет `condition`; если `ask: true` — ещё и спрашивает пользователя (approve/reject). При `false` идёт в `else` (или останавливается). |
| `transform` | Рендерит `template` из состояния, без вызова LLM. |

### Связи

- `next: [id1, id2]` — прямые переходы.
- `edges:` с `when:` — условные переходы:

```yaml
edges:
  - {from: decide, to: implement, when: "plan.approved"}
  - {from: decide, to: stop}
```

- Если ни `next`, ни `edges` не заданы — узлы выполняются последовательно в
  порядке объявления.
- Для `controller` и `gate.else` рёбра создаются неявно, чтобы планировщик
  корректно считал join-ы.
- `tools` на узле **переопределяет** набор инструментов роли для этого шага —
  удобно, когда один и тот же шаг в разных пайплайнах должен иметь разные
  права. Обычно достаточно задать инструменты на уровне роли.

### Обмен данными

Каждый узел получает задачу + входы + выходы предыдущих узлов (контекстный
блок). Пишет результат в `outputs[node_id]` и в артефакт (`node.output` или
`<node_id>.md`). Так контекст не «взрывается» при большом числе моделей.

---

## Планировщик

Движок выполняет граф волнами с подсчётом ссылок (reference counting):

1. Для каждого узла считается число входящих рёбер (`pending`).
2. Стартовый узел запускается первым.
3. Готовые узлы волны выполняются **параллельно** (воркер-пул `max_parallel`).
4. После узла для всех его исходящих рёбер уменьшается `pending`; если счётчик
   дошёл до нуля и узел был выбран хотя бы одним предшественником — он готов;
   если не выбран — узел **pruning-уется**, и это каскадно распространяется на
   его потомков (чтобы diamond-join не завис).
5. Пока есть готовые узлы — повторяем.

Это даёт корректные join-семантики (узел с двумя входами ждёт оба) и корректную
работу ветвлений controller/gate. Состояние защищено мьютексом, SQLite работает в
однопоточном режиме.

---

## Условия и шаблоны

### Условия (`when`, `condition`, `until`)

Поддерживается:

- `&&`, `||`, `!`, скобки;
- сравнения `==`, `!=`, `>`, `<`, `>=`, `<=`, оператор `contains`;
- свойства: `review.passed`, `review.failed`, `review.empty`, `review.nonempty`;
- ссылки: `outputs.<node>`, `artifacts.<name>`, `inputs.<name>`, `iterations`;
- `true`/`false`.

Примеры:

```
review.passed
security_reviewer.passed && qa.passed
iterations >= 3
artifacts.plan.md contains rollback
```

`passed` = в тексте есть `PASS`/`APPROV` и нет `FAIL`. Ревьюеры по промпту
завершают ответ строкой `STATUS: PASS` или `STATUS: FAIL`.

### Шаблоны (`transform.template`)

```
{{task}}              задача
{{inputs.NAME}}       вход
{{outputs.NODE}}      выход узла
{{artifacts.NAME}}    артефакт
{{NODE}}              то же, что outputs.NODE
```

---

## Инструменты и безопасность

Инструменты (включаются per-role через `tools`):

| Инструмент | Назначение |
|---|---|
| `read_file`, `list_dir`, `glob`, `grep` | чтение/поиск |
| `write_file`, `edit_file` | запись/точечная правка |
| `apply_patch` | применение unified diff |
| `run_shell` | shell-команда в workspace |
| `run_tests` | авто-детект тест-раннера (go/npm/cargo/pytest) и запуск |
| `git` | git-подкоманды |
| `http_fetch` | HTTP(S) запрос |
| `web_search` | веб-поиск (Exa, как в opencode; либо self-hosted SearXNG) |
| `todo_write`, `todo_read` | todo-лист прогона |
| `ask_user` | вопрос пользователю (single/multi/custom) |
| `<server>__<tool>` | инструменты внешних MCP-серверов |

`web_search` использует Exa (как opencode) и требует ключ: `EXA_API_KEY` (или
`settings.web_search.api_key`). Альтернатива без ключа — self-hosted SearXNG
(`provider: searxng`, `base_url`). Если поиск не настроен, инструмент вернёт
понятную ошибку, и роль перейдёт к `http_fetch`.

### Откуда берутся описания инструментов

Модель видит три вида инструкций:

1. **Системный промпт роли** — `prompts/<role>.md` (или inline `system_prompt`).
2. **Промпт узла** — `prompt:` в YAML пайплайна.
3. **Описание и JSON-схема инструмента** (то, что видно в `tools`).

Для встроенных инструментов (включая `web_search`) описание и схема заданы в
коде: `internal/tool/<name>.go` — методы `Name()`, `Description()`, `Schema()`.
Например `web_search` — `internal/tool/websearch.go`, а его провайдер/ключ —
`settings.web_search`.

Для внешних MCP-серверов описания и схемы приходят **от самого сервера**
(`tools/list`); conclave лишь формирует имя `<server>__<tool>` и добавляет
префикс `[MCP:<server>]` (`internal/mcp/manager.go`). Отдельного «промпта» для
MCP-инструмента в конфиге нет — меняйте описание на стороне сервера либо
инструкцию в системном промпте роли.

Доступность инструмента роли задаётся списком `tools` у роли; `tools` на узле
переопределяет его для конкретного шага. Пустой список = все доступные
инструменты.

Безопасность:

- **Sandbox**: все пути резолвятся относительно `workspace`; выход за его
  пределы запрещён.
- **Права**: `fs_read`, `fs_write`, `shell`, `network` — на роль, с дефолтами.
- **Shell**: allowlist команд (`allowed_commands`), denylist (`denied_commands`),
  таймаут, лимит вывода.
- **Network**: allowlist хостов (`allowed_hosts`), только http/https.
- Все результаты обрезаются по `max_output_bytes`.

---

## Контекст, лимиты и суммаризация

- `model_limits` задаёт окно модели. На каждом вызове публикуется событие
  `context.usage` с `prompt_tokens` и `context_limit`; CLI и Web UI показывают
  `ctx 1.2k/128k`.
- Если оценка размера промпта превышает `summarize_threshold × limit`, старые
  сообщения сжимаются через `summarizer_model` (по умолчанию — модель роли).
  Системный промпт и последние обмены сохраняются; пара `tool_calls`/`tool` не
  разрывается. Публикуется событие `context.summarized`.
- `max_context_bytes` дополнительно обрезает контекстный блок перед подачей роли.

---

## Надёжность: retry, бюджет, resume

**Fallback по моделям.** Роль имеет `model` и `fallback: [...]`; при ошибке
запрос идёт к следующей модели.

**Retry.** Транзиентные ошибки (429, 5xx, сеть, таймаут) повторяются с
экспоненциальным backoff и учётом `Retry-After`. Настройка — `settings.retry`.
4xx (кроме 408/409/429) не повторяются. Если стриминг уже начал отдавать токены,
повтор не делается (чтобы не дублировать вывод).

**Стоимость и бюджет.** Считается `model_prices`; стоимость копится в состоянии и
пишется в БД. При превышении `budget_usd` по умолчанию выводится предупреждение и
прогон продолжается; `on_budget: abort` (или флаг) останавливает прогон.

**Resume.** Каждый узел сохраняется в БД со статусом и выходом. `run --resume
<run-id>` восстанавливает выходы, артефакты и todo, пропускает завершённые узлы и
продолжает с незавершённых.

---

## Хранилище

- SQLite (чистый Go, `modernc.org/sqlite`) в `--data-dir` (по умолчанию
  `./.conclave`): таблицы `runs`, `nodes`, `events`, `artifacts`.
- Файлы артефактов: `<data-dir>/runs/<run-id>/<name>`.
- Статусы прогона: `running`, `completed`, `failed`, `aborted` (отмена),
  `interrupted` (процесс был прерван). При открытии БД «зависшие» `running`
  без времени завершения помечаются как `interrupted`.
- Ctrl+C во время `run`/`ask`/`tui` отменяет прогон через контекст и помечает
  его `aborted`, а не оставляет в `running`.

---

## CLI

Глобальные флаги: `--global-dir`, `--project-dir`, `--no-global`, `--data-dir`.

| Команда | Описание |
|---|---|
| `run [pipeline]` | запустить пайплайн (`--task`, `--workspace`, `--input k=v`, `--resume <id>`) |
| `ask <role>` | разовый вопрос роли (`--prompt`, `--context`, `--workspace`, `--stream`) |
| `serve` | HTTP + WebSocket сервер с веб-интерфейсом (`--addr`, `--token`) |
| `roles list` / `roles show <role>` | список/детали ролей |
| `pipelines list` / `pipelines show <pipeline>` | список/детали пайплайнов |
| `runs list` | последние прогоны |
| `runs show <id>` | узлы, артефакты, стоимость |
| `runs artifacts <id>` | пути артефактов |
| `runs logs <id> [--limit N] [--type T]` | журнал событий |
| `mcp` | MCP-сервер поверх stdio |
| `config init [--global] [--force]` | записать seed-конфиг |
| `config validate` / `config show` / `config schema` | проверка/путь/JSON Schema |
| `version` | версия |

Во время `run` прогресс идёт в **stderr**, итог — в **stdout**, поэтому удобно
перенаправлять.

---

## MCP

**Сервер** (`conclave mcp`, stdio JSON-RPC 2.0) отдаёт инструменты:

- `list_roles`, `list_pipelines`
- `run_pipeline` `{pipeline, task, workspace?, inputs?}`
- `ask_role` `{role, prompt, context?, workspace?}`
- `get_run` `{run_id}`
- `get_artifact` `{run_id, name}`

Также поддерживает **elicitation**: `ask_user` внутри прогона отправляет клиенту
`elicitation/create` с JSON-схемой вопроса (single/multi/custom). Если клиент не
поддерживает — graceful fallback: роль решает сама (`on_no_user`).

**Клиент** умеет подключаться к внешним MCP-серверам:

- stdio (`command`);
- remote Streamable HTTP + SSE (`url`, `headers`), с `Mcp-Session-Id`.

Инструменты внешних серверов видны ролям как `<server>__<tool>`.

---

## Web UI

`conclave serve --addr 127.0.0.1:8080 [--token X]` поднимает HTTP-сервер, который
отдаёт встроенный фронтенд (Vue3) и API. По умолчанию слушает localhost; при
`--token` требуется `Authorization: Bearer X` (для WebSocket — `?token=X`).

- **REST API**: `GET /api/config`, `POST /api/runs`, `GET /api/runs`,
  `GET /api/runs/{id}`, `GET /api/runs/{id}/artifacts/{name}`,
  `POST /api/runs/{id}/cancel`, `POST /api/ask`.
- **WebSocket** `/api/ws` — потоковый протокол событий (наш, не OpenAI):

  ```jsonc
  // server -> client
  {"type":"hello","protocol":1}
  {"type":"run.started","run_id":"...","message":"feature"}
  {"type":"node.started","run_id":"...","node_id":"analyze","role":"architect","model":"openai/gpt-4o"}
  {"type":"role.delta","run_id":"...","node_id":"analyze","role":"architect","model":"openai/gpt-4o","message":"текст токена"}
  {"type":"role.message","run_id":"...","role":"architect","model":"openai/gpt-4o","message":"полный ответ"}
  {"type":"tool.call","run_id":"...","role":"researcher","model":"...","message":"web_search","data":{"id":"call_1","name":"web_search","arguments":"{...}"}}
  {"type":"tool.result","run_id":"...","role":"researcher","model":"...","message":"web_search","data":{"id":"call_1","name":"web_search","is_error":false,"content":"..."}}
  {"type":"context.usage","run_id":"...","data":{"prompt_tokens":1234,"completion_tokens":567,"total_tokens":1801,"context_limit":128000}}
  {"type":"todos.updated","run_id":"...","data":{"todos":[{"id":"t1","content":"...","status":"pending"}]}}
  {"type":"run.finished","run_id":"...","message":"completed"}

  // client -> server
  {"type":"subscribe","run_id":"..."}   // пустой run_id = все
  {"type":"unsubscribe"}
  {"type":"ping"}
  ```

- **Что видно в браузере**:
  - **единый таймлайн** прогона в хронологическом порядке: узлы, сообщения
    ролей (`role · model`), вызовы инструментов (аргументы **и** результат,
    включая MCP-инструменты вида `server__tool`), события, использование
    контекста; шаги разных ролей разделены цветными маркерами и полосами;
  - **кликабельные узлы**: клик по узлу открывает его карточку — начальный
    промпт, контекст, доступные инструменты и все сообщения этого узла;
  - ответы ролей рендерятся как **Markdown** (GFM);
  - переключатель **Timeline / Result**: панель Result показывает только
    итоговый результат — последнее сообщение или выбранный артефакт (`.md`),
    без промежуточных шагов;
  - статус-бар: статус, `ctx текущий/лимит` с полосой, `in`/`out` (сколько
    токенов отдали сети и сколько она сгенерировала), стоимость, время;
  - todo-лист, история прогонов и просмотр ролей.
- Текст стримится токенами (`role.delta`), а не появляется целиком в конце.
- Отмена активного прогона — кнопкой `cancel` (`POST /api/runs/{id}/cancel`),
  статус становится `aborted`.

### Разработка фронтенда

```bash
make web            # npm install + vite build -> internal/web/dist (embed)
cd web && npm run dev   # dev-сервер Vite с прокси /api и /api/ws на :8080
```

Исходники — в `web/` (Vue3 + Vite + TS), сборка встраивается в бинарь через
`//go:embed`. `make build` собирает фронтенд и бинарь; `make build-go` — только Go.

---

## Интеграция с opencode

opencode подключает conclave как локальный MCP-сервер. Пример
`opencode.json`:

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

После этого opencode видит инструменты `conclave_run_pipeline`,
`conclave_ask_role` и т.д. opencode остаётся драйвером/TUI, а тяжёлая
многомодельная работа уходит в conclave.

---

## Примеры

### Не-кодинговые пайплайны (seed)

- `research` — исследование + критика + бриф.
- `write` — план, черновик, ревью, финал.
- `ops` — оценка, план, ревью, резюме.
- `design` — ресёрч, концепт, критика, спецификация.
- `decision` — ресёрч, варианты, controller (с `ask_user`), запись решения.

### Роли (seed)

`controller`, `architect`, `senior`, `middle`, `junior`, `security_reviewer`,
`qa`, `dba`, `frontend`, `backend`, `devops`, `tech_writer`, `designer`,
`researcher`, `critic`.

---

## Расширение

**Новая роль**: создайте `roles/<id>.yaml` (или файл в `.conclave/roles/`) и
промпт в `prompts/<id>.md`. Роль сразу доступна в пайплайнах.

**Новый пайплайн**: добавьте `pipelines/<name>.yaml`. При необходимости
используйте `edges` с `when`, `controller`, `loop`, `gate.ask`.

**Новый провайдер**: добавьте запись в `providers` с `base_url` (и при
необходимости `api_key_env`, `headers`). Ничего в коде менять не нужно.

**Новый инструмент**: реализуйте интерфейс `tool.Tool` и зарегистрируйте его в
`internal/tool/tool.go` (`builtins`), либо подключите MCP-сервер.

**Внешний вызов**: `run_pipeline` / `ask_role` через MCP, либо CLI/Web.

---

## Разработка

```bash
gofmt -l .
go vet ./...
go test ./...
go build ./cmd/conclave
```

Тесты покрывают: загрузку/merge/env конфига, JSON Schema, контекст-лимиты и цены,
SSE-агрегацию tool_calls, retry/backoff, sandbox и права инструментов, todo и
ask_user, парсинг ответов, планировщик (условия, diamond-join, gate/else,
on_no_user), resume, отмену прогона, суммаризацию контекста, MCP-elicitation,
apply_patch, хранилище (NULL-колонки, interrupted), интеграционный прогон
пайплайна с мок-провайдером, REST/WebSocket сервер и web_search.

### Ключевые пакеты

- `internal/orchestrator/engine.go` — планировщик, узлы, gate/controller/loop.
- `internal/orchestrator/condition.go` — вычислитель условий.
- `internal/role/role.go` — agent-loop и суммаризация.
- `internal/provider/client.go` — OpenAI-совместимый клиент.
- `internal/mcp/server.go` / `http.go` — MCP-сервер и HTTP-клиент.
- `internal/web/` — HTTP-сервер, REST API, WebSocket-хаб и протокол.
- `internal/tool/` — инструменты и sandbox (включая `web_search`).
- `web/` — фронтенд Vue3 + Vite (собирается в `internal/web/dist`).
