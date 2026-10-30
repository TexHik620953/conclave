<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { api } from '../api'
import hljs from 'highlight.js'
import DOMPurify from 'dompurify'
import { store, toast, refreshBoard, refreshState, conclaveAction } from '../store'

const columns = [
  ['backlog', 'Backlog'],
  ['ready', 'Ready'],
  ['in_progress', 'In Progress'],
  ['needs_planning', 'Needs Planning'],
  ['review', 'Review'],
  ['blocked', 'Blocked'],
  ['done', 'Done'],
]
const search = ref('')
const infoHover = ref(false)
const infoPinned = ref(false)
const showInfo = computed(() => infoHover.value || infoPinned.value)
const formOpen = ref(false)
const editing = ref(null)
const detail = ref(null)
const detailTab = ref('overview')
const logs = ref([])
const selectedAttempt = ref('')
const deleteTaskArmed = ref(false)
const draggedTask = ref(null)
const dragOverStatus = ref('')
let suppressClick = false
let logTimer = null
const comment = ref('')
const form = reactive({ title: '', description: '', context: '', complexity: 3, priority: 3, component: '', labels: '', epic: '', acceptance: '', depends_on: '', write_scope: '', status: 'backlog', requirements: '', constraints: '', verification: '', out_of_scope: '', complexity_reason: '', kind: 'work', parent_id: '' })

const filteredTasks = computed(() => {
  const tasks = store.board.tasks || []
  const q = search.value.toLowerCase().trim()
  if (!q) return tasks
  return tasks.filter(t => (t.title + ' ' + t.description + ' ' + t.context + ' ' + (t.labels || []).join(' ')).toLowerCase().includes(q))
})
function tasksFor(status) {
  if (status === 'in_progress') {
    return filteredTasks.value.filter(t => t.status === 'in_progress' || t.status === 'claimed')
  }
  return filteredTasks.value.filter(t => t.status === status)
}
onMounted(focusRequestedTask)
watch(() => store.openTaskId, focusRequestedTask)
async function focusRequestedTask() {
  const id = store.openTaskId
  if (!id) return
  await refreshBoard()
  const task = (store.board.tasks || []).find(t => t.id === id)
  store.openTaskId = ''
  if (task) await openDetail(task)
}
function openNew(status = 'backlog') {
  editing.value = null
  Object.assign(form, { title: '', description: '', context: '', complexity: 3, priority: 3, component: '', labels: '', epic: '', acceptance: '', depends_on: '', write_scope: '', status, requirements: '', constraints: '', verification: '', out_of_scope: '', complexity_reason: '', kind: 'work', parent_id: '' })
  formOpen.value = true
}
function openEdit(task) {
  editing.value = task
  Object.assign(form, {
    title: task.title, description: task.description, context: task.context,
    complexity: task.complexity, priority: task.priority,
    component: task.component || '', labels: (task.labels || []).join(', '), epic: task.epic || '',
    acceptance: (task.acceptance || []).join('\n'), depends_on: (task.depends_on || []).join(', '),
    write_scope: (task.write_scope || []).join(', '), status: task.status,
    requirements: (task.requirements || []).join('\n'), constraints: (task.constraints || []).join('\n'),
    verification: (task.verification || []).join('\n'), out_of_scope: (task.out_of_scope || []).join('\n'),
    complexity_reason: task.complexity_reason || '', kind: task.kind || 'work', parent_id: task.parent_id || '',
  })
  formOpen.value = true
}
async function save() {
  if (!form.title.trim()) { toast('title is required', 'error'); return }
  const payload = {
    title: form.title, description: form.description, context: form.context,
    complexity: Number(form.complexity), priority: Number(form.priority),
    component: form.component, epic: form.epic, status: form.status,
    labels: form.labels.split(',').map(x => x.trim()).filter(Boolean),
    depends_on: form.depends_on.split(',').map(x => x.trim()).filter(Boolean),
    write_scope: form.write_scope.split(',').map(x => x.trim()).filter(Boolean),
    acceptance: form.acceptance.split('\n').map(x => x.trim()).filter(Boolean),
    requirements: form.requirements.split('\n').map(x => x.trim()).filter(Boolean),
    constraints: form.constraints.split('\n').map(x => x.trim()).filter(Boolean),
    verification: form.verification.split('\n').map(x => x.trim()).filter(Boolean),
    out_of_scope: form.out_of_scope.split('\n').map(x => x.trim()).filter(Boolean),
    complexity_reason: form.complexity_reason, kind: form.kind, parent_id: form.parent_id,
  }
  try {
    if (editing.value) await api.updateTask(store.workspace.id, editing.value.id, payload)
    else await api.createTask(store.workspace.id, payload)
    formOpen.value = false
    await refreshBoard()
    toast(editing.value ? 'task updated' : 'task created')
  } catch (e) { toast(e.message, 'error') }
}
async function move(task, status) {
  if (!task || task.status === status) return
  if (status === 'in_progress' && task.status === 'ready') {
    toast('Use Start queue to let a worker claim this task', 'error')
    return
  }
  try {
    await api.transition(store.workspace.id, task.id, status)
    await refreshBoard()
    if (detail.value?.task.id === task.id) await openDetail(task)
    toast(`${task.id} → ${status.replace('_', ' ')}`)
  } catch (e) { toast(e.message, 'error') }
}
function dragStart(task, e) {
  draggedTask.value = task
  suppressClick = true
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', task.id)
  }
}
function dragEnd() {
  draggedTask.value = null
  dragOverStatus.value = ''
  setTimeout(() => { suppressClick = false }, 180)
}
function dragOver(status, e) {
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'move'
  dragOverStatus.value = status
}
function drop(status, e) {
  e.preventDefault()
  const task = draggedTask.value
  dragOverStatus.value = ''
  draggedTask.value = null
  if (task) move(task, status)
  setTimeout(() => { suppressClick = false }, 180)
}
async function approvePlan() {
  await conclaveAction('approve')
  await refreshBoard()
}
async function removeTask(task) {
  if (!deleteTaskArmed.value) {
    deleteTaskArmed.value = true
    setTimeout(() => { deleteTaskArmed.value = false }, 4000)
    return
  }
  deleteTaskArmed.value = false
  try {
    await api.deleteTask(store.workspace.id, task.id)
    toast(`${task.id} deleted`)
    closeDetail()
    await refreshBoard()
  } catch (e) { toast(e.message, 'error') }
}
function openBlocker(blocker) {
  if (!blocker) return
  if (blocker.kind === 'question') {
    store.openQuestionId = blocker.ref_id
    store.view = 'questions'
    return
  }
  store.openTaskId = blocker.ref_id
  store.view = 'board'
}
async function openDetail(task) {
  if (suppressClick) return
  try {
    detail.value = await api.taskDetail(store.workspace.id, task.id)
    detailTab.value = 'overview'
    logs.value = []
    selectedAttempt.value = detail.value.attempts?.at(-1)?.id || ''
    stopLogPolling()
  } catch (e) { toast(e.message, 'error') }
}
async function loadLogs() {
  if (!detail.value?.task?.id || !selectedAttempt.value) { logs.value = []; return }
  try { logs.value = (await api.taskLogs(store.workspace.id, detail.value.task.id, selectedAttempt.value)).logs || [] }
  catch (e) { toast(e.message, 'error') }
}
function setDetailTab(tab) {
  detailTab.value = tab
  if (tab === 'logs') { loadLogs(); startLogPolling() } else stopLogPolling()
}
function startLogPolling() {
  stopLogPolling()
  logTimer = setInterval(loadLogs, 2000)
}
function stopLogPolling() { if (logTimer) { clearInterval(logTimer); logTimer = null } }
function closeDetail() { stopLogPolling(); detail.value = null }
onBeforeUnmount(stopLogPolling)
async function sendComment() {
  if (!comment.value.trim()) return
  try {
    await api.comment(store.workspace.id, detail.value.task.id, comment.value)
    comment.value = ''
    await openDetail(detail.value.task)
  } catch (e) { toast(e.message, 'error') }
}
async function queue(action) {
  try {
    await api.queue(store.workspace.id, action)
    await refreshBoard()
    await refreshState(false)
    toast(action === 'start' ? 'queue started' : 'queue paused')
  } catch (e) { toast(e.message, 'error') }
}
const detailTask = computed(() => detail.value?.task)
const results = computed(() => detail.value?.results || [])
const pendingResult = computed(() => results.value.find(r => r.verdict === 'pending') || null)

function splitTool(content) {
  const text = content || ''
  const idx = text.indexOf(' ')
  if (idx < 0) return { name: text || 'tool', args: '' }
  return { name: text.slice(0, idx), args: text.slice(idx + 1) }
}
function detectLanguage(name, args, content) {
  if (name === 'shell') return 'bash'
  if ((content || '').includes('diff --git') || (content || '').trimStart().startsWith('@@')) return 'diff'
  const m = (args || '').match(/"path"\s*:\s*"([^"]+)"/)
  if (m) {
    const ext = m[1].split('.').pop().toLowerCase()
    const map = { go: 'go', ts: 'typescript', tsx: 'typescript', js: 'javascript', jsx: 'javascript', rs: 'rust', py: 'python', json: 'json', md: 'markdown', yaml: 'yaml', yml: 'yaml', sh: 'bash', sql: 'sql', css: 'css', html: 'xml' }
    if (map[ext]) return map[ext]
  }
  const t = (content || '').trimStart()
  if (t.startsWith('{') || t.startsWith('[')) return 'json'
  return ''
}
function highlight(content, lang) {
  const code = content || ''
  try {
    const out = lang && hljs.getLanguage(lang)
      ? hljs.highlight(code, { language: lang }).value
      : hljs.highlightAuto(code).value
    return DOMPurify.sanitize(out)
  } catch { return DOMPurify.sanitize(code) }
}
const groupedLogs = computed(() => {
  const out = []
  for (let i = 0; i < logs.value.length; i++) {
    const l = logs.value[i]
    if (l.kind === 'tool_call') {
      const t = splitTool(l.content)
      const next = logs.value[i + 1]
      let result = null
      if (next && (next.kind === 'tool_result' || next.level === 'error')) {
        result = next
        i++
      }
      out.push({ type: 'tool', call: l, name: t.name, args: t.args, result, lang: detectLanguage(t.name, t.args, result?.content) })
    } else {
      out.push({ type: 'single', log: l })
    }
  }
  return out
})
const stats = computed(() => {
  const tasks = store.board.tasks || []
  const out = { total: tasks.length, ready: 0, active: 0, planning: 0, review: 0, blocked: 0, done: 0 }
  for (const t of tasks) {
    if (t.status === 'ready') out.ready++
    else if (t.status === 'claimed' || t.status === 'in_progress') out.active++
    else if (t.status === 'needs_planning') out.planning++
    else if (t.status === 'review') out.review++
    else if (t.status === 'blocked') out.blocked++
    else if (t.status === 'done') out.done++
  }
  return out
})
</script>

<template>
  <section class="board-shell">
    <header class="board-head">
      <div class="board-title-block">
        <div>
          <h2>Project board</h2>
          <p>{{ store.board.project?.key }} · {{ store.board.project?.name }}</p>
        </div>
        <div class="board-stats">
          <span><b>{{ stats.total }}</b> total</span>
          <span class="ready"><b>{{ stats.ready }}</b> ready</span>
          <span class="active"><b>{{ stats.active }}</b> active</span>
          <span class="review"><b>{{ stats.review }}</b> review</span>
          <span class="planning"><b>{{ stats.planning }}</b> planning</span>
          <span class="blocked"><b>{{ stats.blocked }}</b> blocked</span>
          <span class="done"><b>{{ stats.done }}</b> done</span>
        </div>
      </div>
      <div class="board-actions">
        <button class="info-btn" title="How statuses work" aria-label="How statuses work" @mouseenter="infoHover = true" @mouseleave="infoHover = false" @click="infoPinned = !infoPinned">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <circle cx="12" cy="12" r="9"></circle>
            <path d="M12 11v5"></path>
            <path d="M12 7.8h.01"></path>
          </svg>
        </button>
        <input v-model="search" class="search" placeholder="Search tasks…" />
        <button class="btn ghost" @click="queue(store.board.queue?.running ? 'pause' : 'start')">
          {{ store.board.queue?.running ? '⏸ Pause queue' : (store.conclave?.run?.phase === 'paused' ? '▶ Resume run' : '▶ Start queue') }}
        </button>
        <button class="btn ghost" @click="openNew('backlog')">+ Backlog</button>
        <button class="btn primary" @click="openNew('ready')">+ Ready for AI</button>
      </div>
    </header>

    <aside v-if="showInfo" class="status-help" @mouseenter="infoHover = true" @mouseleave="infoHover = false">
      <header>
        <b>How task statuses work</b>
        <button @click="infoPinned = false; infoHover = false">×</button>
      </header>
      <div class="status-help-list">
        <div><span class="status-dot backlog"></span><b>Backlog</b><p>Идея или задача, созданная вручную. Нейросеть её не видит и не берёт в работу.</p></div>
        <div><span class="status-dot ready"></span><b>Ready</b><p>Задача разрешена к исполнению. Очередь может забрать её, если совпадают complexity, labels и есть свободный worker slot.</p></div>
        <div><span class="status-dot active"></span><b>In Progress</b><p>Воркер уже забрал задачу (claimed) или работает над ней в отдельном git worktree. Здесь видны branch и commit.</p></div>
        <div><span class="status-dot review"></span><b>Review</b><p>Воркер закончил, сделал commit и оставил отчёт. Нужно проверить diff и тесты: принять в Done или вернуть в Ready.</p></div>
        <div><span class="status-dot blocked"></span><b>Blocked</b><p>Задача ждёт человека или внешнего решения: вопрос, зависимость, конфликт. Очередь её не трогает.</p></div>
        <div><span class="status-dot done"></span><b>Done</b><p>Результат принят. Ветку задачи можно применить/смержить в рабочее дерево.</p></div>
      </div>
      <footer>
        <span><b>Complexity</b> выбирает класс модели.</span>
        <span><b>Priority</b> задаёт порядок в очереди.</span>
        <span>Очередь забирает только <b>Ready</b>.</span>
      </footer>
    </aside>

    <div v-if="store.conclave?.pending_plan && store.conclave?.run?.phase === 'awaiting_approval'" class="plan-approval-banner">
      <div>
        <b>Conclave plan awaiting approval</b>
        <p>{{ store.conclave.pending_plan.tasks?.length || 0 }} tasks are in Backlog as a draft. Approve the plan to move them to Ready and start workers.</p>
      </div>
      <button class="btn primary" :disabled="store.busy || store.conclave?.busy" @click="approvePlan">{{ (store.busy || store.conclave?.busy) ? 'Master is finishing…' : 'Approve & start' }}</button>
    </div>

    <div class="board-columns">
      <div v-for="[key, label] in columns" :key="key" :class="['board-column', key, { 'drag-over': dragOverStatus === key }]" @dragover="dragOver(key, $event)" @dragleave="dragOverStatus = ''" @drop="drop(key, $event)">
        <div class="column-head">
          <span>{{ label }}</span><span class="count">{{ tasksFor(key).length }}</span>
        </div>
        <div class="column-body">
          <article v-for="task in tasksFor(key)" :key="task.id" :class="['task-card', 'p' + task.priority, { dragging: draggedTask?.id === task.id }]" draggable="true" @dragstart="dragStart(task, $event)" @dragend="dragEnd" @click="openDetail(task)">
            <div class="task-top">
              <span class="task-id">{{ task.id }}</span>
              <span class="chip p">P{{ task.priority }}</span>
              <span class="chip c">C{{ task.complexity }}</span>
              <span v-if="task.kind === 'research'" class="chip research">research</span>
              <span v-if="task.failure_at || task.failure_reason" class="task-failed-badge">FAILED</span>
            </div>
            <h3>{{ task.title }}</h3>
            <div v-if="task.blockers?.length" class="task-blockers">
              <button v-for="b in task.blockers" :key="b.kind + b.ref_id" :title="b.reason" @click.stop="openBlocker(b)">
                Blocked by {{ b.kind === 'question' ? 'question' : b.ref_id }}
              </button>
            </div>
            <div class="task-meta">
              <span v-if="task.component" class="tag">{{ task.component }}</span>
              <span v-for="l in task.labels" :key="l" class="tag">{{ l }}</span>
            </div>
            <div class="task-bottom">
              <span>{{ task.assignee || task.created_by }}</span>
              <span v-if="task.branch" class="branch">branch</span>
            </div>
          </article>
          <button v-if="!tasksFor(key).length && key !== 'blocked' && key !== 'done'" class="column-empty" @click="openNew(key === 'ready' ? 'ready' : 'backlog')">
            <span>＋</span><span>Create {{ label.toLowerCase() }} task</span>
          </button>
          <div v-else-if="!tasksFor(key).length" class="column-empty">No tasks</div>
        </div>
      </div>
    </div>

    <button class="task-fab" title="Create a new task" @click="openNew('backlog')">＋</button>

    <div v-if="formOpen" class="modal-backdrop" @click.self="formOpen = false">
      <div class="modal wide task-modal">
        <header class="modal-head">
          <div>
            <span class="modal-eyebrow">Board task</span>
            <h3>{{ editing ? 'Edit ' + editing.id : 'New task' }}</h3>
            <p>{{ editing ? 'Update task details and scope.' : 'Create a work item for the board or the AI queue.' }}</p>
          </div>
          <button class="modal-close" @click="formOpen = false">×</button>
        </header>

        <div class="task-form">
          <section class="form-section">
            <h4>Basics</h4>
            <label class="field full">
              <span>Title <em>*</em></span>
              <input v-model="form.title" autofocus placeholder="Short, actionable task title" />
            </label>
            <label class="field full">
              <span>Description</span>
              <textarea v-model="form.description" rows="3" placeholder="What needs to be done?"></textarea>
            </label>
            <label class="field full">
              <span>Background / context</span>
              <textarea v-model="form.context" rows="3" placeholder="Why this task exists, links, relevant files and decisions…"></textarea>
            </label>
            <label class="field full">
              <span>Requirements</span>
              <textarea v-model="form.requirements" rows="3" placeholder="One requirement per line"></textarea>
            </label>
            <label class="field full">
              <span>Constraints</span>
              <textarea v-model="form.constraints" rows="2" placeholder="One constraint per line"></textarea>
            </label>
            <label class="field full">
              <span>Verification</span>
              <textarea v-model="form.verification" rows="2" placeholder="Commands or checks, one per line"></textarea>
            </label>
          </section>

          <section class="form-section">
            <h4>Execution</h4>
            <div class="form-row">
              <div class="field">
                <span>Complexity <em>*</em></span>
                <div class="choice-row">
                  <button v-for="n in 5" :key="n" type="button" :class="{ active: form.complexity === n }" @click="form.complexity = n">{{ n }}</button>
                </div>
                <small>1 — trivial · 5 — expert. Determines which worker tier can take it.</small>
              </div>
              <div class="field">
                <span>Priority <em>*</em></span>
                <div class="choice-row priority-row">
                  <button v-for="n in 5" :key="n" type="button" :class="{ active: form.priority === n }" @click="form.priority = n">{{ n }}</button>
                </div>
                <small>5 runs first. Lower priority tasks age in the queue.</small>
              </div>
            </div>
            <div class="form-row">
              <label class="field">
                <span>Component</span>
                <input v-model="form.component" placeholder="backend, frontend, infra…" />
              </label>
              <label class="field">
                <span>Epic</span>
                <input v-model="form.epic" placeholder="Parent epic or initiative" />
              </label>
            </div>
            <div class="form-row">
              <label class="field">
                <span>Status</span>
                <select v-model="form.status">
                  <option value="backlog">Backlog — triage later</option>
                  <option value="ready">Ready — available to AI queue</option>
                </select>
              </label>
              <label class="field">
                <span>Kind</span>
                <select v-model="form.kind">
                  <option value="work">Work — implementation</option>
                  <option value="research">Research — read-only analysis</option>
                  <option value="epic">Epic — container</option>
                </select>
              </label>
            </div>
            <div class="form-row">
              <label class="field">
                <span>Depends on</span>
                <input v-model="form.depends_on" placeholder="CNC-1, CNC-2" />
              </label>
              <label class="field">
                <span>Parent task</span>
                <input v-model="form.parent_id" placeholder="CNC-1" />
              </label>
            </div>
          </section>

          <section class="form-section">
            <h4>Scope & acceptance</h4>
            <label class="field full">
              <span>Labels</span>
              <input v-model="form.labels" placeholder="go, backend, websocket" />
            </label>
            <label class="field full">
              <span>Write scope</span>
              <input v-model="form.write_scope" placeholder="internal/api, web/src, docs" />
              <small>Directories or files the worker is expected to change.</small>
            </label>
            <label class="field full">
              <span>Acceptance criteria</span>
              <textarea v-model="form.acceptance" rows="3" placeholder="One criterion per line&#10;e.g. go test ./... passes&#10;e.g. reconnect works after sleep"></textarea>
            </label>
            <label class="field full">
              <span>Out of scope</span>
              <textarea v-model="form.out_of_scope" rows="2" placeholder="What this task must not touch"></textarea>
            </label>
            <label v-if="form.complexity >= 4" class="field full">
              <span>Complexity reason</span>
              <textarea v-model="form.complexity_reason" rows="2" placeholder="Why is this C4/C5? What makes it risky or cross-cutting?"></textarea>
            </label>
          </section>
        </div>

        <footer class="modal-footer">
          <span class="form-tip">
            {{ form.status === 'ready' ? 'Task will be available to the AI queue immediately.' : 'Task will wait in Backlog until moved to Ready.' }}
          </span>
          <div>
            <button class="btn ghost" @click="formOpen = false">Cancel</button>
            <button class="btn primary" @click="save">{{ editing ? 'Save changes' : 'Create task' }}</button>
          </div>
        </footer>
      </div>
    </div>

    <div v-if="detailTask" class="modal-backdrop task-detail-backdrop" @click.self="closeDetail">
      <div class="modal task-detail-modal">
        <header class="task-detail-head">
          <div class="task-title-wrap">
            <div class="task-title-top"><span class="task-id">{{ detailTask.id }}</span><span class="chip p">P{{ detailTask.priority }}</span><span class="chip c">C{{ detailTask.complexity }}</span><span class="status-pill">{{ detailTask.status.replace('_',' ') }}</span></div>
            <h2>{{ detailTask.title }}</h2>
            <p v-if="detailTask.component || detailTask.epic">{{ detailTask.component }}<span v-if="detailTask.epic"> · {{ detailTask.epic }}</span></p>
          </div>
          <button class="modal-close" @click="closeDetail">×</button>
        </header>

        <div class="task-detail-tabs">
          <button :class="{ active: detailTab === 'overview' }" @click="setDetailTab('overview')">Overview</button>
          <button :class="{ active: detailTab === 'result' }" @click="setDetailTab('result')">Result <span v-if="results.length">{{ results.length }}</span></button>
          <button :class="{ active: detailTab === 'logs' }" @click="setDetailTab('logs')">Execution logs <span v-if="detail.attempts?.length">{{ detail.attempts.length }}</span></button>
          <button :class="{ active: detailTab === 'comments' }" @click="setDetailTab('comments')">Comments & activity <span v-if="detail.comments?.length">{{ detail.comments.length }}</span></button>
        </div>

        <div class="task-detail-body">
          <aside class="task-side">
            <div class="side-block">
              <span class="side-label">Status</span>
              <b class="status-value">{{ detailTask.status.replace('_',' ') }}</b>
            </div>
            <div class="side-block">
              <span class="side-label">Priority / Complexity</span>
              <div class="side-chips"><span class="chip p">P{{ detailTask.priority }}</span><span class="chip c">C{{ detailTask.complexity }}</span></div>
            </div>
            <div class="side-block">
              <span class="side-label">Worker</span>
              <b>{{ detailTask.assignee || detailTask.created_by }}</b>
              <small>{{ detailTask.attempts }} attempt(s)</small>
            </div>
            <div v-if="detailTask.blockers?.length" class="side-block">
              <span class="side-label">Blocked by</span>
              <div class="blocker-list">
                <button v-for="b in detailTask.blockers" :key="b.kind + b.ref_id" :title="b.reason" @click="openBlocker(b)">
                  {{ b.kind === 'question' ? 'Question: ' + b.ref_id : b.ref_id }}
                </button>
              </div>
            </div>
            <div v-if="detailTask.branch || detailTask.commit" class="side-block">
              <span class="side-label">Execution</span>
              <code v-if="detailTask.branch">{{ detailTask.branch }}</code>
              <code v-if="detailTask.commit">{{ detailTask.commit.slice(0,12) }}</code>
            </div>
            <div class="side-block" v-if="detailTask.labels?.length">
              <span class="side-label">Labels</span>
              <div class="task-meta"><span v-for="l in detailTask.labels" :key="l" class="tag">{{ l }}</span></div>
            </div>
            <div class="side-actions">
              <button class="btn ghost" @click="openEdit(detailTask)">Edit task</button>
              <button v-if="detailTask.status === 'backlog'" class="btn" @click="move(detailTask, 'ready')">Move to Ready</button>
              <button v-if="detailTask.status === 'review'" class="btn" @click="move(detailTask, 'done')">Approve result</button>
              <button v-if="detailTask.status === 'review'" class="btn ghost" @click="move(detailTask, 'ready')">Reject result</button>
              <button v-if="detailTask.status === 'blocked' || detailTask.failure_at" class="btn ghost" @click="move(detailTask, 'ready')">Retry</button>
              <button class="btn ghost danger delete-task-btn" :disabled="detailTask.status === 'in_progress' || detailTask.status === 'claimed'" @click="removeTask(detailTask)">
                {{ deleteTaskArmed ? 'Confirm delete' : 'Delete task' }}
              </button>
            </div>
          </aside>

          <main class="task-main">
            <div v-if="detailTab === 'overview'" class="overview-pane">
              <section>
                <h4>Description</h4>
                <p class="task-description">{{ detailTask.description || 'No description.' }}</p>
              </section>
              <section v-if="detailTask.context">
                <h4>Background / context</h4>
                <p class="pre">{{ detailTask.context }}</p>
              </section>
              <section v-if="detailTask.requirements?.length">
                <h4>Requirements</h4><ul class="acceptance-list"><li v-for="a in detailTask.requirements" :key="a">{{ a }}</li></ul>
              </section>
              <section v-if="detailTask.constraints?.length">
                <h4>Constraints</h4><ul class="acceptance-list"><li v-for="a in detailTask.constraints" :key="a">{{ a }}</li></ul>
              </section>
              <section v-if="detailTask.acceptance?.length">
                <h4>Acceptance criteria</h4>
                <ul class="acceptance-list"><li v-for="a in detailTask.acceptance" :key="a">{{ a }}</li></ul>
              </section>
              <section v-if="detailTask.verification?.length">
                <h4>Verification</h4><ul class="acceptance-list"><li v-for="a in detailTask.verification" :key="a"><code>{{ a }}</code></li></ul>
              </section>
              <section v-if="detailTask.out_of_scope?.length">
                <h4>Out of scope</h4><ul class="acceptance-list"><li v-for="a in detailTask.out_of_scope" :key="a">{{ a }}</li></ul>
              </section>
              <section v-if="detailTask.complexity_reason">
                <h4>Complexity reason · C{{ detailTask.complexity }}</h4><p class="pre">{{ detailTask.complexity_reason }}</p>
              </section>
              <section v-if="detailTask.depends_on?.length || detailTask.write_scope?.length">
                <h4>Scope & dependencies</h4>
                <div v-if="detailTask.depends_on?.length" class="kv"><span>Depends on</span><b>{{ detailTask.depends_on.join(', ') }}</b></div>
                <div v-if="detailTask.write_scope?.length" class="kv"><span>Write scope</span><b>{{ detailTask.write_scope.join(', ') }}</b></div>
              </section>
            </div>

            <div v-else-if="detailTab === 'result'" class="result-pane">
              <div v-if="pendingResult && detailTask.status === 'review'" class="result-actions">
                <div><b>Review result</b><p>Approve the latest result or reject it and return the task to Ready.</p></div>
                <div><button class="btn primary" @click="move(detailTask, 'done')">Approve</button><button class="btn ghost danger" @click="move(detailTask, 'ready')">Reject</button></div>
              </div>
              <div v-if="!results.length" class="empty-log">No result recorded yet.</div>
              <div v-else class="result-list">
                <article v-for="(r, idx) in results" :key="r.id" class="result-version">
                  <header>
                    <b>Result v{{ results.length - idx }}</b>
                    <span>Attempt #{{ r.attempt_no }}</span>
                    <span :class="['result-verdict', r.verdict]">{{ r.verdict }}</span>
                    <time>{{ new Date(r.created_at).toLocaleString() }}</time>
                  </header>
                  <pre>{{ r.content }}</pre>
                  <small v-if="r.decided_at">Decided {{ new Date(r.decided_at).toLocaleString() }}<template v-if="r.decided_by"> by {{ r.decided_by }}</template></small>
                </article>
              </div>
            </div>

            <div v-else-if="detailTab === 'logs'" class="logs-pane">
              <div class="log-toolbar">
                <select v-model="selectedAttempt" @change="loadLogs">
                  <option v-for="a in detail.attempts" :key="a.id" :value="a.id">Attempt #{{ a.attempt_no }} · {{ a.worker }} · {{ a.status }}</option>
                </select>
                <span class="log-count">{{ logs.length }} entries</span>
                <button class="btn small ghost" @click="loadLogs">Refresh</button>
              </div>
              <div v-if="!detail.attempts?.length" class="empty-log">No attempts yet. Start the queue to see execution logs.</div>
              <div v-else-if="!logs.length" class="empty-log">No log entries yet.</div>
              <div v-else class="log-list">
                <template v-for="(g, gi) in groupedLogs" :key="gi">
                  <div v-if="g.type === 'tool'" :class="['log-tool-group', { failed: g.result?.level === 'error' }]">
                    <div class="log-tool-head">
                      <span class="log-dot"></span>
                      <b>{{ g.name }}</b>
                      <code v-if="g.args" :title="g.args">{{ g.args }}</code>
                      <span class="log-tool-status">{{ g.result ? (g.result.level === 'error' ? 'failed' : 'done') : 'running' }}</span>
                      <span class="log-tool-time">{{ new Date(g.call.created_at).toLocaleTimeString() }}</span>
                    </div>
                    <div v-if="g.result" class="log-tool-result">
                      <pre><code class="hljs" v-html="highlight(g.result.content, g.lang)"></code></pre>
                    </div>
                  </div>
                  <div v-else :class="['log-entry', g.log.level, g.log.kind]">
                    <div class="log-head"><span>{{ new Date(g.log.created_at).toLocaleTimeString() }}</span><b>{{ g.log.kind.replace('_',' ') }}</b></div>
                    <pre><code class="hljs" v-html="highlight(g.log.content, detectLanguage('', '', g.log.content))"></code></pre>
                  </div>
                </template>
              </div>
            </div>

            <div v-else class="comments-pane">
              <section>
                <h4>Comments</h4>
                <div class="comments">
                  <div v-for="c in detail.comments" :key="c.id" class="comment"><b>{{ c.author }}</b><p>{{ c.body }}</p></div>
                  <div v-if="!detail.comments?.length" class="muted">No comments yet.</div>
                </div>
                <div class="comment-box">
                  <textarea v-model="comment" rows="2" placeholder="Write a comment…"></textarea>
                  <button class="btn" @click="sendComment">Comment</button>
                </div>
              </section>
              <section>
                <h4>Activity</h4>
                <div class="activity">
                  <div v-for="e in detail.events" :key="e.id"><span>{{ e.actor }}</span> {{ e.type }} <em>{{ e.data }}</em></div>
                  <div v-if="!detail.events?.length" class="muted">No activity yet.</div>
                </div>
              </section>
            </div>
          </main>
        </div>
      </div>
    </div>
  </section>
</template>
