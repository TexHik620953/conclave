<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { api } from '../api'
import { store, toast } from '../store'

const cfg = ref(null)
const tab = ref(store.configTab || 'providers')
watch(() => store.configTab, value => { if (value) tab.value = value })
const profileName = ref('')
const defaultProfile = ref('')
const profileForm = ref(null)
const newProfileName = ref('')
const workerRoleOptions = ['coder', 'senior', 'verifier', 'reviewer', 'tester', 'integrator', 'researcher', 'docs']
const workerIDs = computed(() => {
  const set = new Set(workerRoleOptions)
  for (const w of (profileForm.value?.workers || [])) if (w.id) set.add(w.id)
  return [...set]
})
const selected = ref('')
const providerForm = reactive({ name: '', base_url: '', api_key: '', max_parallel: 8 })
const keyInput = ref('')
const remote = ref([])
const loading = ref(false)
const testing = ref(false)
const addingProvider = ref(false)
const deleteArmed = ref(false)
const deleteModelArmed = ref('')
const configError = ref('')
let deleteTimer = null
const manualModel = reactive({ id: '', context: 128000, max_output: 8192 })
const remoteSearch = ref('')
const mcpEditing = ref('')
const mcpDeleteArmed = ref('')
const mcpForm = reactive({ name: '', type: 'stdio', url: '', command: '', headers: '', environment: '', enabled: true })
const searchForm = reactive({ provider: 'tavily', api_key: '', base_url: '', max_results: 5 })

const mcpServers = computed(() => Object.values(cfg.value?.mcp || {}).sort((a, b) => a.name.localeCompare(b.name)))
const providers = computed(() => {
  const p = cfg.value?.providers || {}
  return Object.values(p).sort((a, b) => a.name.localeCompare(b.name))
})
const current = computed(() => cfg.value?.providers?.[selected.value] || null)
const profileNames = computed(() => Object.keys(cfg.value?.profiles || {}).sort())
const allModels = computed(() => {
  const out = []
  for (const p of Object.values(cfg.value?.providers || {})) {
    for (const id of Object.keys(p.models || {})) out.push(p.name + '/' + id)
  }
  return out.sort()
})
const models = computed(() => {
  if (!current.value?.models) return []
  return Object.entries(current.value.models).map(([id, m]) => ({ id, ...m })).sort((a, b) => a.id.localeCompare(b.id))
})
const availableRemote = computed(() => {
  const q = remoteSearch.value.toLowerCase().trim()
  return remote.value.filter(r => !current.value?.models?.[r.id] && (!q || (r.id || '').toLowerCase().includes(q))).slice(0, 400)
})

onMounted(load)
async function load() {
  try {
    cfg.value = await api.config()
    if (!selected.value) selected.value = providers.value[0]?.name || ''
    if (selected.value && cfg.value.providers?.[selected.value]) selectProvider(selected.value)
    defaultProfile.value = cfg.value.default_profile || ''
    if (!profileName.value) profileName.value = cfg.value.default_profile || profileNames.value[0] || ''
    if (profileName.value && cfg.value.profiles?.[profileName.value]) loadProfile(profileName.value)
  } catch (e) { toast(e.message, 'error') }
}
async function refresh() {
  cfg.value = await api.config()
}
function selectProvider(name) {
  selected.value = name
  deleteArmed.value = false
  deleteModelArmed.value = ''
  configError.value = '' 
  keyInput.value = ''
  remote.value = []
  const p = cfg.value.providers[name]
  Object.assign(providerForm, { name, base_url: p.base_url, api_key: '', max_parallel: p.max_parallel || 8 })
  addingProvider.value = false
}
function startAdd() {
  addingProvider.value = true
  selected.value = ''
  Object.assign(providerForm, { name: '', base_url: 'https://openrouter.ai/api/v1', api_key: '', max_parallel: 8 })
  keyInput.value = ''
  remote.value = []
}
async function saveProvider() {
  if (!providerForm.name.trim() || !providerForm.base_url.trim()) { toast('name and base URL are required', 'error'); return }
  try {
    await api.saveProvider(providerForm)
    await refresh()
    selected.value = providerForm.name.toLowerCase().trim()
    addingProvider.value = false
    toast('provider saved')
  } catch (e) { toast(e.message, 'error') }
}
async function removeProvider() {
  if (!selected.value) return
  if (!deleteArmed.value) {
    deleteArmed.value = true
    clearTimeout(deleteTimer)
    deleteTimer = setTimeout(() => { deleteArmed.value = false }, 4000)
    return
  }
  deleteArmed.value = false
  configError.value = ''
  try {
    await api.deleteProvider(selected.value)
    selected.value = ''
    await refresh()
    toast('provider deleted')
  } catch (e) {
    configError.value = e.message
    toast(e.message, 'error')
  }
}
async function saveKey() {
  if (!selected.value || !keyInput.value.trim()) return
  try { await api.setKey(selected.value, keyInput.value.trim()); keyInput.value = ''; await refresh(); toast('API key saved') }
  catch (e) { toast(e.message, 'error') }
}
async function clearKey() {
  if (!selected.value) return
  try { await api.setKey(selected.value, ''); await refresh(); toast('API key cleared') }
  catch (e) { toast(e.message, 'error') }
}
async function testProvider() {
  if (!selected.value) return
  testing.value = true
  try {
    const data = await api.testProvider(selected.value)
    remote.value = data.models || []
    toast(`connection OK · ${remote.value.length} models`)
  } catch (e) { toast(e.message, 'error') }
  finally { testing.value = false }
}
async function addModel(m) {
  try {
    await api.saveModel({
      provider: selected.value, model: m.id,
      context: m.context || 128000, max_output: m.max_output || 8192,
      tools: true, reasoning: false, effort_cap: false,
    })
    await refresh()
    toast('model added: ' + m.id)
  } catch (e) { toast(e.message, 'error') }
}
async function addManual() {
  if (!manualModel.id.trim()) return
  await addModel({ id: manualModel.id.trim(), context: manualModel.context, max_output: manualModel.max_output })
  manualModel.id = ''
}
async function saveModel(m) {
  try {
    await api.saveModel({ provider: selected.value, model: m.id, context: m.context, max_output: m.max_output, tools: m.tools, reasoning: m.reasoning, effort_cap: m.effort })
    await refresh(); toast('model saved: ' + m.id)
  } catch (e) { toast(e.message, 'error') }
}
function loadProfile(name) {
  profileName.value = name
  const src = cfg.value?.profiles?.[name]
  if (!src) { profileForm.value = null; return }
  const p = JSON.parse(JSON.stringify(src))
  p.agent ||= { model: allModels.value[0] || '', effort: 'medium' }
  p.master ||= { model: allModels.value[0] || '', effort: 'high' }
  p.workers ||= []
  p.limits ||= { max_parallel: 8, max_iterations: 6, providers: {} }
  p.workers.forEach(w => {
    w._min = w.complexity?.[0] || 1
    w._max = w.complexity?.[w.complexity.length - 1] || 5
    w.instances ||= 1
  })
  profileForm.value = p
}
function addWorker() {
  if (!profileForm.value) return
  const used = new Set(profileForm.value.workers.map(w => w.id))
  const id = workerRoleOptions.find(x => !used.has(x)) || ('worker-' + (profileForm.value.workers.length + 1))
  profileForm.value.workers.push({ id, model: allModels.value[0] || '', effort: 'medium', instances: 1, _min: 1, _max: 3 })
}
function removeWorker(i) { profileForm.value.workers.splice(i, 1) }
function createProfile() {
  const name = newProfileName.value.trim()
  if (!name) { configError.value = 'Profile name is required'; return }
  const src = profileForm.value ? JSON.parse(JSON.stringify(profileForm.value)) : { agent: { model: allModels.value[0] || '', effort: 'medium' }, master: { model: allModels.value[0] || '', effort: 'high' }, workers: [], limits: { max_parallel: 8, max_iterations: 6, providers: {} } }
  cfg.value.profiles[name] = src
  newProfileName.value = ''
  loadProfile(name)
}
async function saveDefaultProfile() {
  try {
    await api.setDefaultProfile(defaultProfile.value)
    await refresh()
    toast('global default profile: ' + defaultProfile.value)
  } catch (e) { toast(e.message, 'error') }
}
async function saveProfile() {
  if (!profileForm.value || !profileName.value) return
  configError.value = ''
  const profile = JSON.parse(JSON.stringify(profileForm.value))
  profile.workers = (profile.workers || []).map(w => {
    const min = Number(w._min || 1), max = Number(w._max || 5)
    const complexity = []
    for (let i = min; i <= max; i++) complexity.push(i)
    delete w._min; delete w._max
    return { ...w, complexity, instances: Number(w.instances || 1) }
  })
  try {
    await api.saveProfile(profileName.value, profile)
    await refresh()
    loadProfile(profileName.value)
    toast('profile saved')
  } catch (e) { configError.value = e.message; toast(e.message, 'error') }
}
async function removeProfile() {
  if (!profileName.value || profileName.value === cfg.value.default_profile) { configError.value = 'Default profile cannot be deleted'; return }
  if (!deleteArmed.value) { deleteArmed.value = true; setTimeout(() => deleteArmed.value = false, 4000); return }
  try { await api.deleteProfile(profileName.value); profileName.value = ''; await refresh(); toast('profile deleted') }
  catch (e) { configError.value = e.message; toast(e.message, 'error') }
}

async function removeModel(m) {
  if (deleteModelArmed.value !== m.id) {
    deleteModelArmed.value = m.id
    clearTimeout(deleteTimer)
    deleteTimer = setTimeout(() => { deleteModelArmed.value = '' }, 4000)
    return
  }
  deleteModelArmed.value = ''
  configError.value = ''
  try {
    await api.deleteModel(selected.value, m.id)
    await refresh()
    toast('model removed')
  } catch (e) {
    configError.value = e.message
    toast(e.message, 'error')
  }
}

function selectMCP(name) {
  const m = cfg.value?.mcp?.[name]
  if (!m) return
  mcpEditing.value = name
  mcpDeleteArmed.value = ''
  Object.assign(mcpForm, {
    name, type: m.type || 'stdio', url: m.url || '',
    command: (m.command || []).join(' '),
    headers: Object.entries(m.headers || {}).map(([k, v]) => `${k}=${v}`).join('\n'),
    environment: Object.entries(m.environment || {}).map(([k, v]) => `${k}=${v}`).join('\n'),
    enabled: m.enabled !== false,
  })
}
function newMCP() {
  mcpEditing.value = '__new__'
  Object.assign(mcpForm, { name: '', type: 'stdio', url: '', command: '', headers: '', environment: '', enabled: true })
}
function linesToMap(raw) {
  const out = {}
  for (const line of String(raw || '').split('\n')) {
    const i = line.indexOf('=')
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1)
  }
  return out
}
async function saveMCP() {
  if (!mcpForm.name.trim()) { toast('MCP server name is required', 'error'); return }
  const command = mcpForm.type === 'stdio' ? mcpForm.command.trim().split(/\s+/).filter(Boolean) : []
  try {
    await api.saveMCP({
      name: mcpForm.name.trim(), type: mcpForm.type, url: mcpForm.url.trim(), command,
      headers: linesToMap(mcpForm.headers), environment: linesToMap(mcpForm.environment), enabled: mcpForm.enabled,
    })
    await refresh()
    mcpEditing.value = mcpForm.name.trim()
    toast('MCP server saved')
  } catch (e) { toast(e.message, 'error') }
}
async function removeMCP() {
  if (!mcpEditing.value || mcpEditing.value === '__new__') return
  if (mcpDeleteArmed.value !== mcpEditing.value) {
    mcpDeleteArmed.value = mcpEditing.value
    setTimeout(() => { mcpDeleteArmed.value = '' }, 4000)
    return
  }
  try {
    await api.deleteMCP(mcpEditing.value)
    mcpEditing.value = ''
    await refresh()
    toast('MCP server deleted')
  } catch (e) { toast(e.message, 'error') }
}
async function saveSearchConfig() {
  try {
    await api.saveSearch({ ...searchForm, max_results: Number(searchForm.max_results || 5) })
    await refresh()
    toast('search settings saved')
  } catch (e) { toast(e.message, 'error') }
}
</script>

<template>
  <section class="config-shell">
    <header class="page-head">
      <div><h2>Configuration</h2><p>Global providers, API keys, models and profiles · ~/.config/conclave/config.yaml</p></div>
      <div class="config-tabs">
        <button :class="{ active: tab === 'providers' }" @click="tab = 'providers'">Providers & models</button>
        <button :class="{ active: tab === 'profiles' }" @click="tab = 'profiles'">Profiles</button>
        <button :class="{ active: tab === 'mcp' }" @click="tab = 'mcp'">MCP & search</button>
      </div>
      <button v-if="tab === 'providers'" class="btn primary" @click="startAdd">＋ Add provider</button>
    </header>

    <div v-if="configError" class="config-error"><b>Configuration error</b><span>{{ configError }}</span><button @click="configError = ''">×</button></div>

    <div v-if="tab === 'providers'" class="config-grid">
      <aside class="provider-side">
        <button v-for="p in providers" :key="p.name" :class="['provider-item', { active: selected === p.name }]" @click="selectProvider(p.name)">
          <span class="provider-name">{{ p.name }}</span>
          <small>{{ Object.keys(p.models || {}).length }} models</small>
          <span :class="['key-state', p.has_key ? 'ok' : 'missing']">{{ p.has_key ? 'key set' : 'no key' }}</span>
        </button>
        <button :class="['provider-item', 'add', { active: addingProvider }]" @click="startAdd">＋ Add provider</button>
      </aside>

      <div class="config-editor">
        <template v-if="addingProvider || current">
          <div class="editor-card">
            <div class="editor-head">
              <div><h3>{{ addingProvider ? 'New provider' : current.name }}</h3><p class="muted">{{ addingProvider ? 'Add an OpenAI-compatible provider' : current.base_url }}</p></div>
              <button v-if="!addingProvider" class="btn ghost danger" @click="removeProvider">{{ deleteArmed ? 'Confirm delete' : 'Delete' }}</button>
            </div>
            <div class="form-row">
              <label class="field"><span>Provider name</span><input v-model="providerForm.name" :disabled="!addingProvider" placeholder="openrouter" /></label>
              <label class="field"><span>Max parallel</span><input v-model.number="providerForm.max_parallel" type="number" min="1" /></label>
            </div>
            <label class="field"><span>Base URL</span><input v-model="providerForm.base_url" placeholder="https://api.openai.com/v1" /></label>
            <div class="key-box">
              <div>
                <span class="field-label">API key</span>
                <p v-if="!addingProvider" class="key-current">{{ current.has_key ? current.masked_key : 'not configured' }}</p>
                <p v-else class="muted">Stored in ~/.config/conclave/config.yaml (mode 0600)</p>
              </div>
              <div class="key-actions">
                <input v-model="keyInput" type="password" :placeholder="current?.has_key ? 'Replace API key…' : 'sk-…'" />
                <button class="btn small" @click="saveKey">Save key</button>
                <button v-if="current?.has_key" class="btn small ghost" @click="clearKey">Clear</button>
              </div>
            </div>
            <div class="editor-actions">
              <button class="btn primary" @click="saveProvider">{{ addingProvider ? 'Create provider' : 'Save provider' }}</button>
              <button v-if="!addingProvider" class="btn ghost" :disabled="testing" @click="testProvider">{{ testing ? 'Testing…' : 'Test connection · fetch models' }}</button>
            </div>
          </div>

          <div v-if="!addingProvider" class="editor-card">
            <div class="editor-head"><div><h3>Models</h3><p class="muted">Enabled models are available in the model picker.</p></div><span class="count-pill">{{ models.length }}</span></div>
            <div class="model-table">
              <div class="model-row head"><span>Model</span><span>Context</span><span>Max out</span><span>Capabilities</span><span></span></div>
              <div v-for="m in models" :key="m.id" class="model-row">
                <span class="model-id">{{ m.id }}</span>
                <input v-model.number="m.context" type="number" />
                <input v-model.number="m.max_output" type="number" />
                <span class="caps"><label><input v-model="m.tools" type="checkbox" /> tools</label><label><input v-model="m.reasoning" type="checkbox" /> reasoning</label><label><input v-model="m.effort" type="checkbox" /> effort</label></span>
                <span class="row-actions"><button class="btn small" @click="saveModel(m)">Save</button><button class="btn small ghost" :class="{ danger: deleteModelArmed === m.id }" @click="removeModel(m)">{{ deleteModelArmed === m.id ? 'Confirm' : '×' }}</button></span>
              </div>
              <div v-if="!models.length" class="muted pad">No models yet. Test connection and add from the provider list.</div>
            </div>

            <div v-if="remote.length" class="remote-models">
              <div class="remote-head"><h4>Available from provider</h4><span class="muted">{{ availableRemote.length }} models</span></div>
              <div class="remote-search"><input v-model="remoteSearch" placeholder="Filter models by name…" /></div>
              <div class="remote-list">
                <button v-for="m in availableRemote" :key="m.id" class="remote-model" @click="addModel(m)">
                  <span class="remote-model-name">{{ m.id }}</span>
                  <span class="remote-model-meta">{{ Math.round((m.context || 128000)/1000) }}k context <b>Add ＋</b></span>
                </button>
              </div>
              <div v-if="!availableRemote.length" class="muted pad-sm">No matching models.</div>
            </div>
            <div class="manual-model">
              <h4>Add model manually</h4>
              <div class="form-row three">
                <input v-model="manualModel.id" placeholder="model id" />
                <input v-model.number="manualModel.context" type="number" placeholder="context" />
                <input v-model.number="manualModel.max_output" type="number" placeholder="max output" />
                <button class="btn" @click="addManual">Add</button>
              </div>
            </div>
          </div>
        </template>
        <div v-else class="config-empty">
          <span>◇</span><h3>Select a provider</h3><p>Add a provider to configure models and API keys.</p>
        </div>
      </div>
    </div>

    <div v-if="tab === 'profiles'" class="profiles-layout">
      <aside class="profile-side">
        <label class="field">
          <span>Global default profile</span>
          <select v-model="defaultProfile" @change="saveDefaultProfile">
            <option v-for="p in profileNames" :key="p" :value="p">{{ p }}</option>
          </select>
        </label>
        <button v-for="p in profileNames" :key="p" :class="['provider-item', { active: profileName === p }]" @click="loadProfile(p)">
          <span class="provider-name">{{ p }}</span>
          <small>{{ p === cfg.default_profile ? 'default profile' : 'profile' }}</small>
        </button>
        <div class="new-profile">
          <input v-model="newProfileName" placeholder="new profile" @keyup.enter="createProfile" />
          <button class="btn small" @click="createProfile">Add</button>
        </div>
      </aside>

      <div v-if="profileForm" class="config-editor">
        <div class="editor-card">
          <div class="editor-head">
            <div><h3>{{ profileName }}</h3><p class="muted">Model routing and worker limits</p></div>
            <button v-if="profileName !== cfg.default_profile" class="btn ghost danger" @click="removeProfile">{{ deleteArmed ? 'Confirm delete' : 'Delete profile' }}</button>
          </div>
          <div class="form-row">
            <label class="field"><span>Agent model</span><select v-model="profileForm.agent.model"><option v-for="m in allModels" :key="m" :value="m">{{ m }}</option></select></label>
            <label class="field"><span>Agent effort</span><select v-model="profileForm.agent.effort"><option>low</option><option>medium</option><option>high</option><option>ultra</option></select></label>
          </div>
          <div class="form-row">
            <label class="field"><span>Master model</span><select v-model="profileForm.master.model"><option v-for="m in allModels" :key="m" :value="m">{{ m }}</option></select></label>
            <label class="field"><span>Master effort</span><select v-model="profileForm.master.effort"><option>low</option><option>medium</option><option>high</option><option>ultra</option></select></label>
          </div>
          <div class="form-row">
            <label class="field"><span>Max parallel</span><input v-model.number="profileForm.limits.max_parallel" type="number" min="1" /></label>
            <label class="field"><span>Max iterations</span><input v-model.number="profileForm.limits.max_iterations" type="number" min="1" /></label>
          </div>
          <div class="field full">
            <span>Provider concurrency caps</span>
            <small class="muted">Effective provider limit = min(provider max_parallel, this value, worker instances).</small>
            <div class="provider-limit-grid">
              <label v-for="p in providers" :key="p.name" class="field">
                <span>{{ p.name }}</span>
                <input v-model.number="profileForm.limits.providers[p.name]" type="number" min="1" :placeholder="'provider: ' + (p.max_parallel || 8)" />
              </label>
            </div>
          </div>
          <div class="form-row">
            <label class="field"><span>Agent max steps</span><input v-model.number="profileForm.agent.max_steps" type="number" min="1" placeholder="120" /></label>
            <label class="field"><span>Master max steps</span><input v-model.number="profileForm.master.max_steps" type="number" min="1" placeholder="120" /></label>
          </div>
          <div class="form-row">
            <label class="field"><span>First token timeout</span><input v-model="profileForm.timeouts.first_token" placeholder="180s" /></label>
            <label class="field"><span>Stream idle timeout</span><input v-model="profileForm.timeouts.idle" placeholder="180s" /></label>
          </div>
        </div>

        <div class="editor-card">
          <div class="editor-head"><div><h3>Workers</h3><p class="muted">Complexity ranges decide which tasks each worker can claim.</p></div><button class="btn small" @click="addWorker">+ Add worker</button></div>
          <div class="worker-list">
            <div v-for="(w, i) in profileForm.workers" :key="i" class="worker-row">
              <label class="field"><span>Worker role</span><select v-model="w.id"><option v-for="role in workerIDs" :key="role" :value="role">{{ role }}</option></select></label>
              <label class="field wide"><span>Model</span><select v-model="w.model"><option v-for="m in allModels" :key="m" :value="m">{{ m }}</option></select></label>
              <label class="field"><span>Min C</span><select v-model.number="w._min"><option v-for="n in 5" :key="n" :value="n">{{ n }}</option></select></label>
              <label class="field"><span>Max C</span><select v-model.number="w._max"><option v-for="n in 5" :key="n" :value="n">{{ n }}</option></select></label>
              <label class="field"><span>Instances</span><input v-model.number="w.instances" type="number" min="1" /></label>
              <button class="btn small ghost danger" @click="removeWorker(i)">×</button>
            </div>
          </div>
          <div class="editor-actions"><button class="btn primary" @click="saveProfile">Save profile</button></div>
        </div>
      </div>
      <div v-else class="config-empty"><span>◇</span><h3>No profile selected</h3><p>Create a profile to configure models and workers.</p></div>
    </div>

    <div v-if="tab === 'mcp'" class="config-grid">
      <aside class="provider-side">
        <button v-for="m in mcpServers" :key="m.name" :class="['provider-item', { active: mcpEditing === m.name }]" @click="selectMCP(m.name)">
          <span class="provider-name">{{ m.name }}</span>
          <small>{{ m.type }} · {{ m.enabled === false ? 'disabled' : 'enabled' }}</small>
        </button>
        <button class="provider-item add" @click="newMCP">＋ Add MCP server</button>
      </aside>
      <div v-if="mcpEditing" class="config-editor">
        <div class="editor-card">
          <div class="editor-head">
            <div><h3>{{ mcpEditing === '__new__' ? 'New MCP server' : mcpForm.name }}</h3><p class="muted">stdio or HTTP MCP server</p></div>
            <button v-if="mcpEditing !== '__new__'" class="btn ghost danger" @click="removeMCP">{{ mcpDeleteArmed === mcpEditing ? 'Confirm delete' : 'Delete' }}</button>
          </div>
          <div class="form-row">
            <label class="field"><span>Name</span><input v-model="mcpForm.name" placeholder="playwright" /></label>
            <label class="field"><span>Transport</span><select v-model="mcpForm.type"><option value="stdio">stdio</option><option value="http">http</option></select></label>
          </div>
          <label class="field"><span>Command (stdio, space-separated)</span><input v-model="mcpForm.command" placeholder="npx -y @playwright/mcp@latest" /></label>
          <label class="field"><span>URL (http)</span><input v-model="mcpForm.url" placeholder="https://example.com/mcp" /></label>
          <div class="form-row">
            <label class="field"><span>Headers, KEY=VALUE per line</span><textarea v-model="mcpForm.headers" rows="3"></textarea></label>
            <label class="field"><span>Environment, KEY=VALUE per line</span><textarea v-model="mcpForm.environment" rows="3"></textarea></label>
          </div>
          <label class="field"><span><input v-model="mcpForm.enabled" type="checkbox" /> Enabled</span></label>
          <div class="editor-actions"><button class="btn primary" @click="saveMCP">Save MCP server</button></div>
        </div>

        <div class="editor-card">
          <div class="editor-head"><div><h3>Web search</h3><p class="muted">Native web_search provider</p></div></div>
          <div class="form-row">
            <label class="field"><span>Provider</span><select v-model="searchForm.provider"><option value="tavily">Tavily</option><option value="brave">Brave</option><option value="searxng">SearXNG</option><option value="exa">Exa</option></select></label>
            <label class="field"><span>Max results</span><input v-model.number="searchForm.max_results" type="number" min="1" max="20" /></label>
          </div>
          <label class="field"><span>API key</span><input v-model="searchForm.api_key" type="password" :placeholder="cfg.search?.has_key ? 'Configured: ' + cfg.search.masked_key : 'API key'" /></label>
          <label class="field"><span>Base URL (SearXNG)</span><input v-model="searchForm.base_url" placeholder="http://127.0.0.1:8080" /></label>
          <div class="editor-actions"><button class="btn primary" @click="saveSearchConfig">Save search settings</button></div>
        </div>
      </div>
      <div v-else class="config-empty"><span>◇</span><h3>MCP & search</h3><p>Add an MCP server or configure a web search provider.</p></div>
    </div>
  </section>
</template>
