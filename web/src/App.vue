<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import ChatView from './components/ChatView.vue'
import BoardView from './components/BoardView.vue'
import UsageView from './components/UsageView.vue'
import ConfigView from './components/ConfigView.vue'
import QuestionsView from './components/QuestionsView.vue'
import { api, connectGlobalEvents } from './api'
import {
  applyPayload, closeWorkspace, compactSession, deleteSession, newSession, openSession, openWorkspace,
  openLogs, refreshBoard, refreshState, store, switchEffort, switchMode, switchPlanOnly, toast,
} from './store'

const pathInput = ref('')
const pathField = ref(null)
const browser = ref({ path: '', parent: '', dirs: [] })
const recent = ref([])
const workspaceRecords = ref([])
const booting = ref(false)
const modelOpen = ref(false)
const providerList = ref([])
const providerSelected = ref('')
const remoteModels = ref([])
const modelSearch = ref('')
const addProviderOpen = ref(false)
const providerForm = reactive({ name: '', base_url: 'https://openrouter.ai/api/v1', api_key: '' })
const loadingModels = ref(false)
const forgetArmed = ref('')
const sessionDeleteArmed = ref(false)

const session = computed(() => store.state?.session)
const isConclave = computed(() => session.value?.mode === 'conclave')
const effectiveModel = computed(() => isConclave.value ? (store.profile?.master?.model || '') : (session.value?.model || ''))
const modelName = computed(() => {
  const ref = effectiveModel.value || ''
  return ref.split('/').slice(1).join('/') || ref
})
const providerName = computed(() => (effectiveModel.value || '').split('/')[0] || '')
const filteredRemote = computed(() => {
  const q = modelSearch.value.toLowerCase().trim()
  return remoteModels.value.filter(m => !q || m.id.toLowerCase().includes(q)).slice(0, 200)
})

onMounted(async () => {
  recent.value = JSON.parse(localStorage.getItem('conclave.recent') || '[]')
  await loadRecent()
  window.addEventListener('conclave-command', onCommand)
  store.globalDisconnect = connectGlobalEvents(handleGlobalEvent)
  const params = new URLSearchParams(location.search)
  const direct = params.get('workspace')
  if (direct) {
    try {
      await open(direct, false)
      const sid = params.get('session')
      if (sid && sid !== store.state?.session?.id) await openSession(sid)
      const view = params.get('view')
      if (['chat', 'board', 'questions', 'usage', 'config'].includes(view)) store.view = view
      const mode = params.get('mode')
      if (mode && store.state?.session?.mode !== mode) await switchMode(mode)
      syncRoute()
    } catch (e) {
      toast(e.message, 'error')
      await loadDir('')
    }
  } else {
    await loadDir('')
  }
})
onBeforeUnmount(() => {
  window.removeEventListener('conclave-command', onCommand)
  if (store.globalDisconnect) store.globalDisconnect()
  closeWorkspace()
})

watch(() => store.view, (v) => {
  if (v === 'board' && store.workspace) refreshBoard()
})
watch([() => store.workspace?.path, () => store.view, () => store.state?.session?.id, () => store.state?.session?.mode], () => syncRoute())

function syncRoute() {
  if (!store.workspace) {
    history.replaceState(null, '', location.pathname)
    return
  }
  const params = new URLSearchParams()
  params.set('workspace', store.workspace.path)
  params.set('view', store.view)
  if (store.state?.session?.id) params.set('session', store.state.session.id)
  if (store.state?.session?.mode) params.set('mode', store.state.session.mode)
  history.replaceState(null, '', location.pathname + '?' + params.toString())
}
function switchWorkspace() {
  closeWorkspace()
  syncRoute()
}
async function removeSession() {
  if (store.busy) { toast('stop the agent before deleting the session', 'error'); return }
  if (!sessionDeleteArmed.value) {
    sessionDeleteArmed.value = true
    setTimeout(() => { sessionDeleteArmed.value = false }, 4000)
    return
  }
  sessionDeleteArmed.value = false
  const id = session.value?.id
  if (!id) return
  if (await deleteSession(id)) toast('session deleted')
}
function forgetCurrent() {
  if (!store.workspace) return
  if (forgetArmed.value !== 'current') { forgetArmed.value = 'current'; setTimeout(() => forgetArmed.value = '', 4000); return }
  forgetArmed.value = ''
  forgetWorkspace(store.workspace.id, store.workspace.path, true)
}
async function forgetWorkspace(id, path, close) {
  try {
    await api.clearWorkspace(id, 'forget')
    toast('workspace forgotten: ' + (path || id))
    await loadRecent()
    if (close) { closeWorkspace(); syncRoute() }
  } catch (e) { toast(e.message, 'error') }
}

function handleGlobalEvent(ev) {
  if (!ev || !ev.workspace_id) return
  if (store.workspace && ev.workspace_id === store.workspace.id) return
  const source = store.workspaceNames[ev.workspace_id] || ev.workspace_id
  const notify = (msg) => toast(msg, 'info', source)
  if (ev.type === 'question.created' || ev.type === 'question.escalated') notify('new question: ' + (ev.text || 'agent needs input'))
  else if (ev.type === 'board.changed' || ev.type === 'task.finished') notify(ev.text || 'task updated')
  else if (ev.type === 'run.phase_changed') { const p = ev.payload?.phase; if (p === 'completed' || p === 'failed') notify('run ' + p) }
  if (!store.workspace) loadRecent()
}

async function loadRecent() {
  try {
    const data = await api.workspaces()
    workspaceRecords.value = data.workspaces || []
    store.workspaceNames = Object.fromEntries(workspaceRecords.value.map(w => [w.id || w.path, w.name]))
    for (const item of workspaceRecords.value) {
      if (!recent.value.some(p => p === item.path)) recent.value.push(item.path)
    }
  } catch {}
}

async function loadDir(path) {
  try {
    browser.value = await api.fs(path)
    pathInput.value = browser.value.path
  } catch (e) { toast(e.message, 'error') }
}

async function enterDir(name) {
  const p = browser.value.path.endsWith('/') ? browser.value.path + name : browser.value.path + '/' + name
  await loadDir(p)
}

async function open(path, create) {
  if (!path.trim()) { toast('workspace path is required', 'error'); return }
  booting.value = true
  try {
    await openWorkspace(path, create)
    recent.value = JSON.parse(localStorage.getItem('conclave.recent') || '[]')
    store.view = 'chat'
  } catch (e) { toast(e.message, 'error') }
  finally { booting.value = false }
}

function onCommand(ev) {
  const text = (ev.detail || '').trim()
  if (text.startsWith('/models')) openModels()
  else if (text.startsWith('/board')) store.view = 'board'
  else if (text.startsWith('/usage')) store.view = 'usage'
  else if (text.startsWith('/new')) newSession()
  else if (text.startsWith('/compact')) compactSession()
  else if (text.startsWith('/help')) toast('Commands: /models /board /usage /new /compact')
  else toast('Unknown command. Try /models, /board, /usage, /new, /compact')
}

async function openModels() {
  if (isConclave.value) {
    store.view = 'config'
    toast('Conclave master model is configured in Config → Profiles')
    return
  }
  modelOpen.value = true
  providerSelected.value = ''
  remoteModels.value = []
  modelSearch.value = ''
  addProviderOpen.value = false
  await loadProviders()
}
async function loadProviders() {
  try {
    const data = await api.providers(store.workspace.id)
    providerList.value = data.providers || []
  } catch (e) { toast(e.message, 'error') }
}
async function chooseProvider(name) {
  providerSelected.value = name
  loadingModels.value = true
  remoteModels.value = []
  try {
    const data = await api.providerModels(store.workspace.id, name)
    remoteModels.value = data.models || []
  } catch (e) {
    toast(e.message, 'error')
    const p = providerList.value.find(x => x.name === name)
    remoteModels.value = (p?.models || []).map(id => ({ id, context: 128000 }))
  } finally { loadingModels.value = false }
}
async function chooseModel(m) {
  try {
    const payload = await api.setModelRef(store.workspace.id, providerSelected.value, m.id)
    applyPayload(payload, false)
    modelOpen.value = false
    toast('model: ' + m.id)
  } catch (e) { toast(e.message, 'error') }
}
async function addProvider() {
  try {
    const payload = await api.addProvider(store.workspace.id, {
      provider: providerForm.name, base_url: providerForm.base_url, api_key: providerForm.api_key,
    })
    applyPayload(payload, false)
    addProviderOpen.value = false
    await loadProviders()
    await chooseProvider(providerForm.name.toLowerCase().trim())
    providerForm.name = ''
    providerForm.api_key = ''
  } catch (e) { toast(e.message, 'error') }
}
async function changeMode(mode) { await switchMode(mode) }
async function changeEffort(e) { await switchEffort(e.target.value) }
async function openBoard() { store.view = 'board'; await refreshBoard() }
</script>

<template>
  <div v-if="!store.workspace" class="workspace-screen">
    <div class="workspace-glow"></div>
    <main class="workspace-card">
      <div class="ws-hero">
        <div class="brand big"><span class="brand-mark">C</span><span>Conclave</span></div>
        <span class="eyebrow">LOCAL MULTI-AGENT WORKSPACE</span>
        <h1>Your work, in one place.</h1>
        <p class="lead">Open an existing project or create a new workspace. Config, sessions, tasks and agent worktrees stay inside <code>.conclave/</code>.</p>
      </div>

      <div class="path-panel">
        <label class="workspace-path-label">Workspace directory
          <div class="path-row">
            <input ref="pathField" v-model="pathInput" placeholder="/path/to/workspace" spellcheck="false" @keyup.enter="open(pathInput, false)" />
            <button class="btn primary" :disabled="booting" @click="open(pathInput, false)">Open path</button>
            <button class="btn" :disabled="booting" @click="open(pathInput, true)">Create workspace</button>
          </div>
        </label>
        <div class="browse-toggle" @click="loadDir(browser.path || pathInput)">Browse folders <span>⌄</span></div>
        <div class="browser">
          <div class="browser-head">
            <button class="icon-btn" @click="loadDir(browser.parent)">↑</button>
            <span>{{ browser.path }}</span>
            <button class="icon-btn" @click="loadDir('')">⌂</button>
          </div>
          <div class="dir-list">
            <button v-for="d in browser.dirs" :key="d" class="dir-row" @click="enterDir(d)">
              <span class="folder">▸</span><span>{{ d }}</span>
            </button>
            <div v-if="!browser.dirs?.length" class="muted">No subdirectories</div>
          </div>
        </div>
      </div>

      <section class="workspaces-section">
        <div class="section-head">
          <h3>Workspaces</h3>
          <span>{{ workspaceRecords.length }}</span>
        </div>
        <div v-if="workspaceRecords.length" class="workspace-grid">
          <div v-for="ws in workspaceRecords" :key="ws.path" :class="['workspace-card-item', { active: ws.active }]" @click="open(ws.path, false)">
            <div class="ws-card-top"><span class="ws-key">{{ ws.key }}</span><span :class="['ws-state', ws.state]">{{ ws.state }}</span></div>
            <b>{{ ws.name }}</b>
            <small>{{ ws.path }}</small>
            <div v-if="ws.active || ws.pending_questions || ws.open_tasks" class="ws-counters">
              <span v-if="ws.running_agents">{{ ws.running_agents }} agents</span>
              <span v-if="ws.open_tasks">{{ ws.open_tasks }} tasks</span>
              <span v-if="ws.pending_questions">{{ ws.pending_questions }} questions</span>
            </div>
            <span class="ws-date">{{ new Date(ws.last_opened).toLocaleString() }}</span>
            <button class="ws-forget" @click.stop="forgetArmed === ws.path ? forgetWorkspace(ws.id || ws.path, ws.path, false) : forgetArmed = ws.path">{{ forgetArmed === ws.path ? 'Confirm forget' : 'Forget' }}</button>
          </div>
        </div>
        <div v-else class="workspace-empty">
          <span class="empty-icon">◇</span>
          <b>No workspaces yet</b>
          <p>Choose a folder above and create your first workspace.</p>
        </div>
      </section>
    </main>
  </div>

  <div v-else class="app-shell" :data-mode="session?.mode || 'build'">
    <aside class="sidebar">
      <div class="brand"><span class="brand-mark">C</span><span>Conclave</span><button class="sidebar-switch" title="All workspaces" @click="switchWorkspace">⌂</button></div>
      <div class="project-pill">
        <b>{{ store.workspace.project?.name }}</b>
        <span>{{ store.workspace.project?.key }} · {{ store.workspace.path }}</span>
      </div>

      <nav class="nav">
        <button :class="{ active: store.view === 'chat' }" @click="store.view='chat'"><span>◈</span> Chat</button>
        <button :class="{ active: store.view === 'board' }" @click="openBoard"><span>▦</span> Board <em class="nav-count">{{ store.counters.board }}</em><em v-if="store.board.queue?.running" class="live"></em></button>
        <button :class="{ active: store.view === 'questions' }" @click="store.view='questions'"><span>?</span> Questions <em class="nav-count">{{ store.counters.questions }}</em></button>
        <button :class="{ active: store.view === 'usage' }" @click="store.view='usage'"><span>◔</span> Usage</button>
        <button :class="{ active: store.view === 'config' }" @click="store.view='config'"><span>⚙</span> Config</button>
      </nav>

      <div class="side-section">
        <span class="side-label">Sessions</span>
        <select class="session-select" :value="session?.id" @change="openSession($event.target.value)">
          <option v-for="s in store.sessions" :key="s.id" :value="s.id">{{ s.title }} · {{ s.mode }}</option>
        </select>
        <span class="muted">{{ session?.messages?.length || 0 }} messages · {{ session?.mode }}</span>
        <div class="side-actions">
          <button class="btn small" @click="newSession">+ New chat</button>
          <button class="btn small ghost" @click="compactSession">Compact</button>
          <button class="btn small ghost danger" :disabled="store.busy" @click="removeSession">{{ sessionDeleteArmed ? 'Confirm delete' : 'Delete chat' }}</button>
        </div>
      </div>

      <div class="side-section">
        <span class="side-label">Workspace</span>
        <span class="path-text" :title="store.workspace.path">{{ store.workspace.path }}</span>
        <button class="link-btn" @click="switchWorkspace">Switch workspace</button>
        <div class="danger-actions">
          <button class="link-btn danger" @click="forgetCurrent">{{ forgetArmed === 'current' ? 'Confirm forget' : 'Forget workspace' }}</button>
        </div>
      </div>

      <div class="side-bottom">
        <div class="connection"><span :class="{ dot: true, on: store.connected }"></span>{{ store.connected ? 'connected' : 'offline' }}</div>
        <button class="link-btn logs-link" @click="openLogs">View server logs</button>
        <span class="muted">Conclave web · v0.1</span>
      </div>
    </aside>

    <main class="main">
      <header class="topbar">
        <div class="topbar-title">
          <div class="topbar-crumb">
            <button class="workspace-back" @click="switchWorkspace">← Workspaces</button>
            <span class="slash">/</span>
            <span class="project-name" :title="store.workspace.path">{{ store.workspace.project?.name }}</span>
            <span v-if="store.workspace.project?.key" class="project-key" :title="'Project key: ' + store.workspace.project.key">{{ store.workspace.project.key }}</span>
            <span :class="['running-agents', { active: (store.counters.agents || 0) > 0 }]" :title="(store.counters.agents || 0) + ' running agent(s)'">
              <span class="agent-pulse"></span>{{ store.counters.agents || 0 }} {{ (store.counters.agents || 0) === 1 ? 'agent' : 'agents' }}
            </span>
          </div>
          <div class="title-line">
            <h1>{{ store.view === 'chat' ? 'Chat' : store.view === 'board' ? 'Board' : store.view === 'questions' ? 'Questions' : store.view === 'usage' ? 'Usage' : 'Config' }}</h1>
            <span class="crumb" :title="store.workspace.path">{{ store.workspace.path }}</span>
          </div>
        </div>
        <div class="topbar-controls">
          <div class="segmented">
            <button v-for="m in ['build','conclave']" :key="m" :class="{ active: session?.mode === m }" @click="changeMode(m)">{{ m }}</button>
          </div>
          <button v-if="session?.mode === 'build'" :class="['plan-toggle', { active: session?.plan_only }]" @click="switchPlanOnly(!session?.plan_only)">
            {{ session?.plan_only ? 'Plan only' : 'Act' }}
          </button>
          <button class="model-btn" :title="isConclave ? 'Master model comes from the active profile' : 'Model for the current Build session'" @click="openModels">
            <span class="model-dot"></span>
            <span><b>{{ modelName }}</b><small>{{ isConclave ? 'master · profile' : 'session · ' + providerName }}</small></span>
            <span class="chev">{{ isConclave ? '⚙' : '⌄' }}</span>
          </button>
          <select class="effort" :value="session?.effort" @change="changeEffort">
            <option value="low">low</option><option value="medium">medium</option><option value="high">high</option><option value="ultra">ultra</option>
          </select>
        </div>
      </header>

      <div class="content">
        <ChatView v-if="store.view === 'chat'" />
        <BoardView v-else-if="store.view === 'board'" />
        <QuestionsView v-else-if="store.view === 'questions'" />
        <UsageView v-else-if="store.view === 'usage'" />
        <ConfigView v-else />
      </div>
    </main>

    <div class="toasts">
      <div v-for="t in store.toasts" :key="t.id" :class="['toast', t.kind]"><small v-if="t.source">{{ t.source }}</small><span>{{ t.message }}</span></div>
    </div>

    <div v-if="store.logsOpen" class="modal-backdrop" @click.self="store.logsOpen=false">
      <div class="modal logs-modal">
        <header><div><h3>Server logs</h3><p class="muted">In-memory log of API and agent requests</p></div><button @click="store.logsOpen=false">×</button></header>
        <pre class="logs-content">{{ store.logs.length ? store.logs.join('\n') : 'No log entries yet.' }}</pre>
        <footer><button class="btn ghost" @click="store.logsOpen=false">Close</button></footer>
      </div>
    </div>

    <div v-if="modelOpen" class="modal-backdrop" @click.self="modelOpen=false">
      <div class="modal models-modal">
        <header><div><h3>Models</h3><p class="muted">Choose a provider, then a model</p></div><button @click="modelOpen=false">×</button></header>
        <div class="models-layout">
          <div class="provider-list">
            <button class="provider-row add" @click="addProviderOpen = !addProviderOpen">＋ Add provider</button>
            <button v-for="p in providerList" :key="p.name" :class="['provider-row', { active: providerSelected === p.name }]" @click="chooseProvider(p.name)">
              <span>{{ p.name }}</span><small>{{ p.models.length }} configured</small>
            </button>
          </div>
          <div class="model-list-panel">
            <div v-if="addProviderOpen" class="add-provider">
              <label>Name<input v-model="providerForm.name" placeholder="local" /></label>
              <label>Base URL<input v-model="providerForm.base_url" /></label>
              <label>API key<input v-model="providerForm.api_key" type="password" placeholder="optional for local" /></label>
              <button class="btn primary" @click="addProvider">Add provider</button>
            </div>
            <template v-else-if="providerSelected">
              <div class="model-search"><input v-model="modelSearch" placeholder="Search models…" /><span v-if="loadingModels" class="spinner"></span></div>
              <button v-for="m in filteredRemote" :key="m.id" class="model-row" @click="chooseModel(m)">
                <span><b>{{ m.id }}</b><small>{{ m.context ? Math.round(m.context/1000) + 'k context' : 'context unknown' }}</small></span>
                <span>→</span>
              </button>
              <div v-if="!loadingModels && !filteredRemote.length" class="muted pad">No models. Add a provider or check the API key.</div>
            </template>
            <div v-else class="muted pad">Select a provider on the left.</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
