async function request(path, options = {}) {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    ...options,
  })
  const text = await res.text()
  let data = null
  try { data = text ? JSON.parse(text) : null } catch { data = text }
  if (!res.ok) {
    const msg = data && data.error ? data.error : (text || res.statusText)
    throw new Error(msg)
  }
  return data
}

export const api = {
  health: () => request('/api/health'),
  logs: () => request('/api/logs'),
  questions: (id, status = '') => request('/api/questions?id=' + encodeURIComponent(id) + '&status=' + encodeURIComponent(status)),
  deleteQuestion: (id, setId) => request('/api/questions?id=' + encodeURIComponent(id) + '&set_id=' + encodeURIComponent(setId), { method: 'DELETE' }),
  answerQuestions: (id, setId, answers) => request('/api/questions/answer', { method: 'POST', body: JSON.stringify({ id, set_id: setId, answers }) }),
  usage: (id, group = 'models') => request('/api/usage?id=' + encodeURIComponent(id) + '&group=' + group),
  agents: (id) => request('/api/agents?id=' + encodeURIComponent(id)),
  config: () => request('/api/config'),
  saveProvider: (payload) => request('/api/config/providers', { method: 'POST', body: JSON.stringify(payload) }),
  deleteProvider: (name) => request('/api/config/providers?name=' + encodeURIComponent(name), { method: 'DELETE' }),
  setKey: (provider, apiKey) => request('/api/config/key', { method: 'POST', body: JSON.stringify({ provider, api_key: apiKey }) }),
  saveModel: (payload) => request('/api/config/model', { method: 'POST', body: JSON.stringify(payload) }),
  saveProfile: (name, profile) => request('/api/config/profile', { method: 'POST', body: JSON.stringify({ name, profile }) }),
  deleteProfile: (name) => request('/api/config/profile?name=' + encodeURIComponent(name), { method: 'DELETE' }),
  saveMCP: (payload) => request('/api/config/mcp', { method: 'POST', body: JSON.stringify(payload) }),
  deleteMCP: (name) => request('/api/config/mcp?name=' + encodeURIComponent(name), { method: 'DELETE' }),
  saveSearch: (payload) => request('/api/config/search', { method: 'POST', body: JSON.stringify(payload) }),
  deleteModel: (provider, model) => request('/api/config/model?provider=' + encodeURIComponent(provider) + '&model=' + encodeURIComponent(model), { method: 'DELETE' }),
  testProvider: (provider) => request('/api/config/test', { method: 'POST', body: JSON.stringify({ provider }) }),
  fs: (path = '') => request('/api/fs?path=' + encodeURIComponent(path)),
  workspaces: () => request('/api/workspaces'),
  clearWorkspace: (id, mode) => request('/api/workspace?id=' + encodeURIComponent(id) + '&mode=' + mode, { method: 'DELETE' }),
  openWorkspace: (path, create = false) => request('/api/workspace/open', {
    method: 'POST', body: JSON.stringify({ path, create }),
  }),
  state: (id) => request('/api/state?id=' + encodeURIComponent(id)),
  send: (id, text) => request('/api/message', { method: 'POST', body: JSON.stringify({ id, text }) }),
  cancel: (id) => request('/api/cancel', { method: 'POST', body: JSON.stringify({ id }) }),
  revert: (id, index) => request('/api/chat/revert', { method: 'POST', body: JSON.stringify({ id, index }) }),
  fork: (id, index) => request('/api/chat/fork', { method: 'POST', body: JSON.stringify({ id, index }) }),
  regenerate: (id, index) => request('/api/chat/regenerate', { method: 'POST', body: JSON.stringify({ id, index }) }),
  continueChat: (id) => request('/api/chat/continue', { method: 'POST', body: JSON.stringify({ id }) }),
  setMode: (id, mode) => request('/api/mode', { method: 'POST', body: JSON.stringify({ id, mode }) }),
  setModel: (id, model) => request('/api/model', { method: 'POST', body: JSON.stringify({ id, model }) }),
  setProfile: (id, profile) => request('/api/profile', { method: 'POST', body: JSON.stringify({ id, profile }) }),
  setModelRef: (id, provider, value) => request('/api/model', { method: 'POST', body: JSON.stringify({ id, provider, value }) }),
  setEffort: (id, effort) => request('/api/effort', { method: 'POST', body: JSON.stringify({ id, effort }) }),
  setPlanOnly: (id, value) => request('/api/plan-only', { method: 'POST', body: JSON.stringify({ id, plan_only: value }) }),
  newSession: (id) => request('/api/session/new', { method: 'POST', body: JSON.stringify({ id }) }),
  openSession: (id, sessionId) => request('/api/sessions/open', { method: 'POST', body: JSON.stringify({ id, session_id: sessionId }) }),
  deleteSession: (id, sessionId) => request('/api/session/delete', { method: 'POST', body: JSON.stringify({ id, session_id: sessionId }) }),
  compact: (id) => request('/api/compact', { method: 'POST', body: JSON.stringify({ id }) }),
  providers: (id) => request('/api/providers?id=' + encodeURIComponent(id)),
  addProvider: (id, payload) => request('/api/providers', { method: 'POST', body: JSON.stringify({ id, ...payload }) }),
  providerModels: (id, provider) => request('/api/providers/models', { method: 'POST', body: JSON.stringify({ id, provider }) }),
  addModel: (id, provider, value) => request('/api/providers/model', { method: 'POST', body: JSON.stringify({ id, provider, value }) }),
  board: (id) => request('/api/board?id=' + encodeURIComponent(id)),
  createTask: (id, task) => request('/api/board/task', { method: 'POST', body: JSON.stringify({ id, task }) }),
  updateTask: (id, taskId, task) => request('/api/board/task', { method: 'POST', body: JSON.stringify({ id, task_id: taskId, task }) }),
  deleteTask: (id, taskId) => request('/api/board/task?id=' + encodeURIComponent(id) + '&task=' + encodeURIComponent(taskId), { method: 'DELETE' }),
  transition: (id, taskId, status) => request('/api/board/transition', { method: 'POST', body: JSON.stringify({ id, task_id: taskId, status }) }),
  comment: (id, taskId, body) => request('/api/board/comment', { method: 'POST', body: JSON.stringify({ id, task_id: taskId, body }) }),
  taskDetail: (id, taskId) => request('/api/board/task/detail?id=' + encodeURIComponent(id) + '&task=' + encodeURIComponent(taskId)),
  taskLogs: (id, taskId, attemptId) => request('/api/board/task/logs?id=' + encodeURIComponent(id) + '&task=' + encodeURIComponent(taskId) + '&attempt=' + encodeURIComponent(attemptId)),
  queue: (id, action) => request('/api/board/queue', { method: 'POST', body: JSON.stringify({ id, action }) }),
  conclave: (id, action) => request('/api/conclave/action', { method: 'POST', body: JSON.stringify({ id, action }) }),
}

export function connectGlobalEvents(handlers) {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const url = `${proto}://${location.host}/ws/global`
  let closed = false
  let ws
  const connect = () => {
    if (closed) return
    ws = new WebSocket(url)
    ws.onmessage = (e) => { try { handlers(JSON.parse(e.data)) } catch (err) { console.error(err) } }
    ws.onclose = () => { if (!closed) setTimeout(connect, 1200) }
    ws.onerror = () => { try { ws.close() } catch {} }
  }
  connect()
  return () => { closed = true; try { ws.close() } catch {} }
}

export function connectEvents(id, handlers) {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const url = `${proto}://${location.host}/ws?id=${encodeURIComponent(id)}`
  let closed = false
  let ws
  const connect = () => {
    if (closed) return
    ws = new WebSocket(url)
    ws.onmessage = (e) => {
      try { handlers(JSON.parse(e.data)) } catch (err) { console.error(err) }
    }
    ws.onclose = () => { if (!closed) setTimeout(connect, 1200) }
    ws.onerror = () => { try { ws.close() } catch {} }
  }
  connect()
  return () => { closed = true; try { ws.close() } catch {} }
}
