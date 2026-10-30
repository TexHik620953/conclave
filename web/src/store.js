import { reactive } from 'vue'
import { api, connectEvents, connectGlobalEvents } from './api'

export const store = reactive({
  workspace: null,
  state: null,
  models: [],
  providers: [],
  sessions: [],
  profile: null,
  profileName: '',
  profiles: [],
  counters: { board: 0, questions: 0, agents: 0 },
  board: { tasks: [], project: null, queue: { running: false, active: 0 } },
  conclave: null,
  view: 'chat',
  configTab: 'providers',
  openTaskId: '',
  openQuestionId: '',
  chat: [],
  ledger: { by_model: {}, by_agent: {} },
  busy: false,
  phase: '',
  requestStartedAt: 0,
  lastEventAt: 0,
  lastError: '',
  toasts: [],
  connected: false,
  logs: [],
  logsOpen: false,
  globalDisconnect: null,
  workspaceNames: {},
  _watchdog: null,
  _disconnect: null,
})

export function toast(message, kind = 'info', source = '') {
  const id = Math.random().toString(36).slice(2)
  store.toasts.push({ id, message, kind, source })
  setTimeout(() => { store.toasts = store.toasts.filter(t => t.id !== id) }, 4500)
}

function clearWatchdog() {
  if (store._watchdog) { clearTimeout(store._watchdog); store._watchdog = null }
}
function armWatchdog() {
  clearWatchdog()
  store._watchdog = setTimeout(async () => {
    if (!store.busy) return
    const elapsed = Math.max(1, Math.round((Date.now() - (store.requestStartedAt || Date.now())) / 1000))
    store.busy = false
    store.phase = ''
    store.lastError = `No activity from the provider for 90s (elapsed ${elapsed}s). The request was cancelled.`
    toast('request timed out', 'error')
    try { await api.cancel(store.workspace.id) } catch {}
    try { await refreshState(true) } catch {}
  }, 30 * 60 * 1000) // server-side provider timeouts are authoritative; this is only a last-resort UI guard
}

function messageToChat(m, index) {
  if (!m || !m.role) return null
  if (m.role === 'user') return { kind: 'user', content: m.content, created: m.created_at, messageIndex: index }
  if (m.role === 'assistant' && (m.content || m.reasoning)) return { kind: 'assistant', content: m.content, reasoning: m.reasoning || '', created: m.created_at, streaming: false, messageIndex: index }
  return null
}

export function syncChat() {
  if (!store.state) return
  const messages = store.state.session.mode === 'conclave'
    ? (store.state.session.conclave_messages || [])
    : (store.state.session.messages || [])
  const out = []
  const toolMap = new Map()
  messages.forEach((m, index) => {
    const simple = messageToChat(m, index)
    if (simple) { out.push(simple); return }
    if (m.role === 'assistant') {
      for (const tc of (m.tool_calls || [])) {
        const entry = { kind: 'tool', id: tc.id, name: tc.name, args: tc.arguments || '', content: '', status: 'running' }
        out.push(entry); toolMap.set(tc.id, entry)
      }
    } else if (m.role === 'tool') {
      let entry = toolMap.get(m.tool_call_id)
      if (!entry) { entry = { kind: 'tool', id: m.tool_call_id, name: m.name || 'tool', args: '', content: '', status: 'done' }; out.push(entry); toolMap.set(m.tool_call_id, entry) }
      entry.content = m.content || ''
      entry.status = (m.content || '').startsWith('ERROR:') ? 'failed' : 'done'
    }
  })
  store.chat = out
  store.ledger = store.state.session.ledger || { by_model: {}, by_agent: {} }
}

export function applyPayload(payload, resetChat = true) {
  store.workspace = payload.workspace
  store.state = payload.state
  store.models = payload.models || []
  store.providers = payload.providers || []
  store.sessions = payload.sessions || []
  store.profile = payload.profile || null
  store.profileName = payload.profile_name || payload.state?.profile_name || ''
  store.profiles = payload.profiles || []
  store.counters = payload.counters || store.counters
  store.conclave = payload.conclave || null
  store.ledger = payload.state?.session?.ledger || store.ledger
  if (resetChat) syncChat()
  store.busy = !!payload.state?.busy
  if (store.busy && !store.requestStartedAt) store.requestStartedAt = Date.now()
}

export async function openWorkspace(path, create) {
  const payload = await api.openWorkspace(path, create)
  applyPayload(payload, true)
  const recent = JSON.parse(localStorage.getItem('conclave.recent') || '[]')
  localStorage.setItem('conclave.recent', JSON.stringify([path, ...recent.filter(p => p !== path)].slice(0, 8)))
  if (store._disconnect) store._disconnect()
  store.connected = true
  store._disconnect = connectEvents(store.workspace.id, handleEvent)
  return payload
}

export function closeWorkspace() {
  if (store._disconnect) store._disconnect()
  store._disconnect = null
  store.workspace = null
  store.state = null
  store.chat = []
  store.busy = false
  store.phase = ''
  store.connected = false
}

export async function refreshState(resetChat = false) {
  if (!store.workspace) return
  applyPayload(await api.state(store.workspace.id), resetChat)
}

export async function refreshBoard() {
  if (!store.workspace) return
  const data = await api.board(store.workspace.id)
  data.tasks = data.tasks || []
  store.board = data
}

export async function sendMessage(text) {
  if (!text.trim() || store.busy) return
  store.busy = true
  store.phase = 'waiting for model…'
  store.lastError = ''
  store.requestStartedAt = Date.now()
  store.lastEventAt = Date.now()
  armWatchdog()
  try { await api.send(store.workspace.id, text) }
  catch (e) { clearWatchdog(); store.busy = false; store.phase = ''; store.lastError = e.message; toast(e.message, 'error') }
}

function streamEntry(agentId) {
  return store.chat.findIndex(e => e.kind === 'assistant' && e.agentId === agentId && e.streaming)
}

function isCancellation(text = '') {
  const t = String(text).toLowerCase()
  return t.includes('context canceled') || t.includes('context cancelled') || t.includes('context deadline exceeded')
}

function handleEvent(ev) {
  store.lastEventAt = Date.now()
  if (store.busy) armWatchdog()
  const chatAgent = !ev.agent_id || ['main', 'master', 'master-review'].includes(ev.agent_id)
  switch (ev.type) {
    case 'assistant.started':
      if (!chatAgent) return
      store.phase = 'thinking…'
      store.chat.push({ kind: 'assistant', agentId: ev.agent_id, content: '', reasoning: '', streaming: true })
      break
    case 'assistant.reasoning':
      if (!chatAgent) return
      store.phase = 'thinking…'
      let ri = streamEntry(ev.agent_id || 'main')
      if (ri < 0) { store.chat.push({ kind: 'assistant', agentId: ev.agent_id, content: '', reasoning: '', streaming: true }); ri = store.chat.length - 1 }
      store.chat[ri].reasoning += ev.text || ''
      break
    case 'assistant.delta':
      if (!chatAgent) return
      store.phase = 'streaming…'
      let si = streamEntry(ev.agent_id || 'main')
      if (si < 0) { store.chat.push({ kind: 'assistant', agentId: ev.agent_id, content: '', reasoning: '', streaming: true }); si = store.chat.length - 1 }
      store.chat[si].content += ev.text || ''
      break
    case 'assistant.completed':
      if (!chatAgent) return
      {
        const p = ev.payload || {}
        const i = streamEntry(ev.agent_id || 'main')
        if (i >= 0) { store.chat[i].content = p.content || store.chat[i].content; store.chat[i].reasoning = p.reasoning || store.chat[i].reasoning; store.chat[i].streaming = false }
      }
      break
    case 'tool.started': {
      if (!chatAgent) return
      const call = ev.payload?.call
      if (!call) return
      store.phase = 'running ' + call.name + '…'
      store.chat.push({ kind: 'tool', id: call.id, name: call.name, args: call.arguments || '', content: '', status: 'running', batchId: ev.payload?.batch_id || '' })
      break
    }
    case 'tools.batch.started':
      if (chatAgent) store.phase = `running ${ev.payload?.calls?.length || 0} tools…`
      break
    case 'tool.running':
      if (chatAgent) store.phase = `running ${ev.payload?.name || 'tool'}… ${ev.payload?.elapsed_seconds || 0}s`
      break
    case 'tool.completed':
    case 'tool.failed': {
      if (!chatAgent) return
      const id = ev.payload?.call_id
      const entry = store.chat.find(e => e.kind === 'tool' && e.id === id)
      if (entry) { entry.content = ev.text || ''; entry.status = ev.type === 'tool.failed' ? 'failed' : 'done' }
      break
    }
    case 'usage.updated': {
      const r = ev.payload
      if (r && r.provider) {
        const key = r.provider + '/' + r.model
        const m = store.ledger.by_model[key] || { provider: r.provider, model: r.model, input_tokens: 0, output_tokens: 0, requests: 0 }
        m.input_tokens += r.input_tokens || 0; m.output_tokens += r.output_tokens || 0; m.requests += r.requests || 0
        m.last_context = r.last_context || m.last_context; m.context_window = r.context_window || m.context_window
        store.ledger.by_model[key] = m
      }
      break
    }
    case 'agent.started':
      if (ev.agent_id === 'master' || ev.agent_id === 'master-review') { store.busy = true; store.phase = 'master is thinking…'; store.requestStartedAt = Date.now(); armWatchdog() }
      break
    case 'agent.finished':
      if (!ev.agent_id || ['main', 'master', 'master-review'].includes(ev.agent_id)) { clearWatchdog(); store.busy = false; store.phase = ''; setTimeout(() => refreshState(true), 150) }
      break
    case 'agent.error':
      if (!ev.agent_id || ['main', 'master', 'master-review'].includes(ev.agent_id)) {
        clearWatchdog(); store.busy = false; store.phase = ''; store.lastError = ev.text || 'agent error'; toast(ev.text || 'agent error', 'error'); setTimeout(() => refreshState(true), 400)
      }
      break
    case 'agent.cancelled':
      break
    case 'session.error':
    case 'run.error':
      if (isCancellation(ev.text)) { clearWatchdog(); store.busy = false; store.phase = ''; break }
      clearWatchdog(); store.busy = false; store.phase = ''; store.lastError = ev.text || 'agent error'; toast(ev.text || 'agent error', 'error'); setTimeout(() => refreshState(true), 400)
      break
    case 'queue.changed':
      refreshState(false).catch(() => {})
      if (store.view === 'board') refreshBoard()
      break
    case 'board.changed':
    case 'task.finished':
      toast(ev.text || 'board updated')
      if (store.view === 'board') refreshBoard()
      refreshState(false).catch(() => {})
      break
    case 'question.created':
    case 'question.escalated':
    case 'question.answered':
    case 'question.deleted':
      refreshState(false).catch(() => {})
      break
    case 'plan.ready':
      toast('Conclave plan is ready for approval')
      if (store.state) refreshState(true).catch(() => {})
      break
    case 'run.phase_changed':
      if (ev.payload?.phase === 'paused' || ev.payload?.phase === 'cancelled') store.lastError = ''
      if (store.state) refreshState(!store.busy).catch(() => {})
      break
    case 'job.finished':
      toast(ev.text || 'background job finished')
      break
  }
}

export async function cancelRequest() {
  if (!store.workspace) return
  clearWatchdog()
  try { await api.cancel(store.workspace.id) } catch (e) { toast(e.message, 'error') }
  store.busy = false; store.phase = ''; toast('request cancelled')
}

export async function continueChat() {
  if (!store.workspace || store.busy) return
  store.busy = true; store.phase = 'continuing…'; store.lastError = ''; store.requestStartedAt = Date.now(); armWatchdog()
  try { await api.continueChat(store.workspace.id) }
  catch (e) { clearWatchdog(); store.busy = false; store.phase = ''; store.lastError = e.message; toast(e.message, 'error') }
}

export async function regenerate(index = -1) {
  if (!store.workspace || store.busy) return
  if (index < 0) {
    let lastUser = -1
    let lastAssistant = -1
    store.chat.forEach((e, i) => { if (e.kind === 'user') lastUser = i; if (e.kind === 'assistant') lastAssistant = i })
    if (lastAssistant > lastUser) store.chat = store.chat.slice(0, lastAssistant)
  } else {
    const i = store.chat.findIndex(e => e.messageIndex === index)
    if (i >= 0) store.chat = store.chat.slice(0, i)
  }
  store.busy = true; store.phase = 'waiting for model…'; store.lastError = ''; store.requestStartedAt = Date.now(); armWatchdog()
  try { await api.regenerate(store.workspace.id, index) }
  catch (e) { clearWatchdog(); store.busy = false; store.phase = ''; store.lastError = e.message; toast(e.message, 'error') }
}

export async function revertTo(index) {
  try { applyPayload(await api.revert(store.workspace.id, index + 1), true); toast('reverted') } catch (e) { toast(e.message, 'error') }
}
export async function forkAt(index) {
  try { applyPayload(await api.fork(store.workspace.id, index + 1), true); toast('forked') } catch (e) { toast(e.message, 'error') }
}

export async function switchMode(mode) {
  try { applyPayload(await api.setMode(store.workspace.id, mode), false); toast('mode: ' + mode) } catch (e) { toast(e.message, 'error') }
}
export async function switchModel(model) {
  try { applyPayload(await api.setModel(store.workspace.id, model), false); toast('model: ' + model) } catch (e) { toast(e.message, 'error') }
}
export async function switchProfile(profile) {
  try { applyPayload(await api.setProfile(store.workspace.id, profile), false); toast('profile: ' + profile); return true } catch (e) { toast(e.message, 'error'); return false }
}
export async function switchEffort(effort) {
  try { applyPayload(await api.setEffort(store.workspace.id, effort), false) } catch (e) { toast(e.message, 'error') }
}
export async function switchPlanOnly(value) {
  try { applyPayload(await api.setPlanOnly(store.workspace.id, value), false) } catch (e) { toast(e.message, 'error') }
}
export async function newSession() {
  try { applyPayload(await api.newSession(store.workspace.id), true); store.view = 'chat'; toast('new session') } catch (e) { toast(e.message, 'error') }
}
export async function openSession(id) {
  try { applyPayload(await api.openSession(store.workspace.id, id), true); toast('session opened') } catch (e) { toast(e.message, 'error') }
}
export async function deleteSession(id) {
  if (!id) return false
  try { applyPayload(await api.deleteSession(store.workspace.id, id), true); return true }
  catch (e) { toast(e.message, 'error'); return false }
}
export async function compactSession() {
  try { applyPayload(await api.compact(store.workspace.id), true); toast('context compacted') } catch (e) { toast(e.message, 'error') }
}
export async function clearWorkspace(mode) {
  try {
    const data = await api.clearWorkspace(store.workspace.id, mode)
    if (mode === 'all') { closeWorkspace(); return }
    applyPayload(data, true); toast('workspace data cleared')
  } catch (e) { toast(e.message, 'error') }
}
export async function openLogs() {
  try { store.logs = (await api.logs()).logs || []; store.logsOpen = true } catch (e) { toast(e.message, 'error') }
}
export async function conclaveAction(action) {
  try { applyPayload(await api.conclave(store.workspace.id, action), false); toast('conclave: ' + action) } catch (e) { toast(e.message, 'error') }
}
export { connectGlobalEvents }
