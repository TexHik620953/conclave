import { reactive } from 'vue'
import { api } from './api'

export interface ToolState {
  id: string
  name: string
  args: string
  result?: string
  isError?: boolean
  pending: boolean
}

export interface Todo {
  id: string
  content: string
  status: string
  priority?: string
}

export type EntryKind = 'node' | 'assistant' | 'tool' | 'event' | 'error' | 'context' | 'question' | 'user'

export interface QuestionState {
  id: string
  header?: string
  question: string
  options: { label: string; description?: string }[]
  multiple: boolean
  allowCustom: boolean
  answered: boolean
}

export interface Entry {
  id: number
  kind: EntryKind
  time: number
  node?: string
  role?: string
  model?: string
  text?: string
  done?: boolean
  tool?: ToolState
  question?: QuestionState
  message?: string
  ctxTokens?: number
  ctxLimit?: number
}

export interface NodeInfo {
  role?: string
  model?: string
  prompt?: string
  context?: string
  tools?: string[]
  status?: string
}

export interface RunState {
  id: string
  sessionId?: string
  pipeline: string
  task: string
  status: string
  active: boolean
  nodes: Record<string, string>
  nodeOrder: string[]
  nodeInfo: Record<string, NodeInfo>
  entries: Entry[]
  todos: Todo[]
  ctxTokens: number
  ctxLimit: number
  inputTokens: number
  outputTokens: number
  cost: number
  artifacts: { name: string; path: string }[]
  finalText: string
  startedAt: number
}

let seq = 0

function newRun(id: string, pipeline = '', task = ''): RunState {
  return {
    id, pipeline, task, status: 'running', active: true,
    nodes: {}, nodeOrder: [], nodeInfo: {}, entries: [], todos: [],
    ctxTokens: 0, ctxLimit: 0, inputTokens: 0, outputTokens: 0, cost: 0,
    artifacts: [], finalText: '', startedAt: Date.now(),
  }
}

export interface Session {
  id: string
  title: string
  pipeline: string
  workspace: string
  created_at: string
  updated_at: string
}

export const state = reactive({
  config: { pipelines: [] as any[], roles: [] as any[] },
  history: [] as any[],
  sessions: [] as Session[],
  currentSessionId: '',
  runs: {} as Record<string, RunState>,
  currentRunId: '',
  connected: false,
  tab: 'run' as 'run' | 'history' | 'roles',
})

function push(run: RunState, entry: Omit<Entry, 'id' | 'time'>): Entry {
  const e: Entry = { id: ++seq, time: Date.now(), ...entry }
  run.entries.push(e)
  return e
}

export function ensureRun(id: string, pipeline = '', task = ''): RunState {
  if (!state.runs[id]) state.runs[id] = newRun(id, pipeline, task)
  return state.runs[id]
}

export function currentRun(): RunState | null {
  return state.currentRunId ? state.runs[state.currentRunId] || null : null
}

export async function loadConfig() {
  state.config = await api.config()
}

export async function loadHistory() {
  state.history = await api.listRuns()
}

export function startRun(runId: string, pipeline: string, task: string, sessionId = '') {
  const run = newRun(runId, pipeline, task)
  run.sessionId = sessionId
  state.runs[runId] = run
  state.currentRunId = runId
  if (sessionId) state.currentSessionId = sessionId
  state.tab = 'run'
}

// newChat returns to the landing state so the user can start a fresh session.
export function newChat() {
  state.currentSessionId = ''
  state.currentRunId = ''
  state.tab = 'run'
}

export async function loadSessions() {
  try {
    state.sessions = (await api.listSessions()) as Session[]
  } catch {
    /* ignore */
  }
}

// openSession loads a session's latest run so the chat continues where it left off.
export async function openSession(sessionId: string) {
  state.currentSessionId = sessionId
  const data: any = await api.getSession(sessionId)
  const runs: any[] = data.runs || []
  if (runs.length === 0) {
    state.currentRunId = ''
    state.tab = 'run'
    return
  }
  const last = runs[runs.length - 1]
  await hydrateRun(last.id)
  const run = state.runs[last.id]
  if (run) run.sessionId = sessionId
  state.currentRunId = last.id
  state.tab = 'run'
}

// sendMessage posts a chat message to the current session, starting a run when
// none is active and queuing it when one is.
export async function sendMessage(content: string, pipeline = '', workspace = '') {
  if (!state.currentSessionId) {
    const created: any = await api.createSession({ pipeline, workspace })
    state.currentSessionId = created.id
    state.sessions.unshift(created)
  }
  const res: any = await api.postMessage(state.currentSessionId, { content, pipeline, workspace })
  if (res.run_id) {
    if (res.queued) {
      // The running run will pick it up; the server echoes a user.message event.
      return res.run_id
    }
    startRun(res.run_id, pipeline, content, state.currentSessionId)
    return res.run_id
  }
  return ''
}

export async function deleteSession(sessionId: string) {
  await api.deleteSession(sessionId)
  state.sessions = state.sessions.filter((s) => s.id !== sessionId)
  if (state.currentSessionId === sessionId) {
    state.currentSessionId = ''
    state.currentRunId = ''
  }
}

function lastAssistant(run: RunState, node: string, role: string): Entry | undefined {
  for (let i = run.entries.length - 1; i >= 0; i--) {
    const e = run.entries[i]
    if (e.kind === 'assistant' && e.node === node && e.role === role) return e
  }
  return undefined
}

function lastOpenAssistant(run: RunState, node: string, role: string): Entry | undefined {
  const e = lastAssistant(run, node, role)
  return e && !e.done ? e : undefined
}

export function applyEvent(ev: any) {
  if (ev.type === 'hello' || ev.type === 'pong') return
  const run = ensureRun(ev.run_id)
  switch (ev.type) {
    case 'run.started':
      run.pipeline = ev.message || run.pipeline
      run.status = 'running'
      run.active = true
      push(run, { kind: 'event', message: `run started · ${ev.message || run.pipeline}` })
      break
    case 'run.finished':
      run.status = ev.message || 'completed'
      run.active = false
      push(run, { kind: 'event', message: `run finished · ${run.status}` })
      loadHistory()
      refreshArtifacts(run)
      break
    case 'node.started':
      if (!run.nodes[ev.node_id]) run.nodeOrder.push(ev.node_id)
      run.nodes[ev.node_id] = 'running'
      run.nodeInfo[ev.node_id] = {
        role: ev.role || ev.data?.role,
        model: ev.model || ev.data?.model,
        prompt: ev.data?.prompt,
        context: ev.data?.context,
        tools: ev.data?.tools,
        status: 'running',
      }
      push(run, { kind: 'node', node: ev.node_id, role: ev.role, model: ev.model, message: ev.message })
      break
    case 'node.finished':
      run.nodes[ev.node_id] = 'done'
      if (run.nodeInfo[ev.node_id]) run.nodeInfo[ev.node_id].status = 'done'
      push(run, { kind: 'event', node: ev.node_id, message: `${ev.node_id}: ${ev.message}` })
      break
    case 'role.delta': {
      let e = lastOpenAssistant(run, ev.node_id, ev.role)
      if (!e) {
        e = push(run, { kind: 'assistant', node: ev.node_id, role: ev.role, model: ev.model, text: '', done: false })
      }
      e.text = (e.text || '') + (ev.message || '')
      if (ev.model) e.model = ev.model
      run.finalText = e.text || ''
      break
    }
    case 'role.message': {
      let e = lastOpenAssistant(run, ev.node_id, ev.role)
      if (!e) {
        e = push(run, { kind: 'assistant', node: ev.node_id, role: ev.role, model: ev.model, text: '', done: false })
      }
      e.text = ev.message || e.text
      e.done = true
      if (ev.model) e.model = ev.model
      run.finalText = e.text || ''
      break
    }
    case 'tool.call': {
      const tool: ToolState = {
        id: ev.data?.id || `${ev.data?.name || ev.message}-${seq}`,
        name: ev.data?.name || ev.message,
        args: ev.data?.arguments || '',
        pending: true,
      }
      push(run, { kind: 'tool', node: ev.node_id, role: ev.role, model: ev.model, tool })
      break
    }
    case 'tool.result': {
      let found = false
      for (let i = run.entries.length - 1; i >= 0; i--) {
        const t = run.entries[i].tool
        if (t && t.pending && (t.id === ev.data?.id || t.name === (ev.data?.name || ev.message))) {
          t.result = ev.data?.content || ''
          t.isError = !!ev.data?.is_error
          t.pending = false
          found = true
          break
        }
      }
      if (!found) {
        push(run, {
          kind: 'tool', node: ev.node_id, role: ev.role, model: ev.model,
          tool: { id: ev.data?.id || 'x', name: ev.data?.name || ev.message, args: '', result: ev.data?.content || '', isError: !!ev.data?.is_error, pending: false },
        })
      }
      break
    }
    case 'context.usage':
      run.ctxTokens = ev.data?.prompt_tokens || 0
      run.ctxLimit = ev.data?.context_limit || 0
      run.inputTokens += ev.data?.prompt_tokens || 0
      run.outputTokens += ev.data?.completion_tokens || 0
      push(run, {
        kind: 'context', role: ev.role, model: ev.model,
        ctxTokens: run.ctxTokens, ctxLimit: run.ctxLimit,
      })
      break
    case 'context.summarized':
      push(run, { kind: 'event', message: `context summarized: ${ev.message}` })
      break
    case 'todos.updated':
      run.todos = ev.data?.todos || []
      break
    case 'user.message':
      push(run, { kind: 'user', role: 'user', message: ev.message })
      break
    case 'user.question':
      push(run, {
        kind: 'question',
        node: ev.node_id,
        role: ev.role,
        question: {
          id: ev.data?.id,
          header: ev.data?.header,
          question: ev.data?.question || ev.message,
          options: ev.data?.options || [],
          multiple: !!ev.data?.multiple,
          allowCustom: !!ev.data?.allow_custom,
          answered: false,
        },
      })
      break
    case 'error':
      push(run, { kind: 'error', message: ev.message })
      break
  }
}

async function refreshArtifacts(run: RunState) {
  try {
    const data: any = await api.getRun(run.id)
    run.artifacts = data.artifacts || []
    if (data.run?.cost_usd) run.cost = data.run.cost_usd
  } catch {
    /* ignore */
  }
}

// revertLocal drops timeline entries from a reverted node onward so a resumed
// run does not mix with the old output.
export function revertLocal(runId: string, nodeId: string) {
  const run = state.runs[runId]
  if (!run) return
  const idx = run.entries.findIndex((e) => e.node === nodeId)
  if (idx >= 0) run.entries.splice(idx)
  const oi = run.nodeOrder.indexOf(nodeId)
  if (oi >= 0) {
    for (let i = oi; i < run.nodeOrder.length; i++) run.nodes[run.nodeOrder[i]] = 'pending'
  }
  run.finalText = ''
  run.status = 'paused'
  run.active = false
}

export async function hydrateRun(id: string) {  const data: any = await api.getRun(id)
  const run = ensureRun(id, data.run?.pipeline, data.run?.task)
  run.pipeline = data.run?.pipeline || run.pipeline
  run.task = data.run?.task || run.task
  run.status = data.run?.status || run.status
  run.active = !!data.active
  run.cost = data.run?.cost_usd || 0
  run.inputTokens = data.run?.prompt_tokens || 0
  run.outputTokens = data.run?.completion_tokens || 0
  run.todos = data.todos || []
  run.artifacts = data.artifacts || []
  run.nodes = {}
  run.nodeOrder = []
  run.nodeInfo = {}
  run.entries = []
  for (const n of data.nodes || []) {
    run.nodeOrder.push(n.node_id)
    run.nodes[n.node_id] = n.status === 'completed' ? 'done' : n.status
    run.nodeInfo[n.node_id] = { role: n.role, prompt: n.prompt, status: n.status }
  }
  for (const ev of data.events || []) {
    const t = Date.parse(ev.time) || Date.now()
    const entry: Entry = { id: ++seq, time: t, kind: 'event', node: ev.node_id, role: ev.role, model: ev.model, message: ev.message }
    if (ev.type === 'node.started') {
      entry.kind = 'node'
      if (!run.nodes[ev.node_id]) run.nodeOrder.push(ev.node_id)
      const info = run.nodeInfo[ev.node_id] || {}
      if (ev.data?.role || ev.role) info.role = ev.data?.role || ev.role
      if (ev.data?.model || ev.model) info.model = ev.data?.model || ev.model
      if (ev.data?.prompt) info.prompt = ev.data.prompt
      if (ev.data?.context) info.context = ev.data.context
      if (ev.data?.tools) info.tools = ev.data.tools
      run.nodeInfo[ev.node_id] = info
    } else if (ev.type === 'node.finished') {
      if (run.nodeInfo[ev.node_id]) run.nodeInfo[ev.node_id].status = 'done'
      if (run.nodes[ev.node_id] !== 'running') run.nodes[ev.node_id] = 'done'
    } else if (ev.type === 'role.message') {
      entry.kind = 'assistant'; entry.text = ev.message; entry.done = true; run.finalText = ev.message
    } else if (ev.type === 'tool.call') {
      entry.kind = 'tool'
      entry.tool = { id: ev.data?.id || `x${entry.id}`, name: ev.data?.name || ev.message, args: ev.data?.arguments || '', pending: true }
    } else if (ev.type === 'tool.result') {
      const id = ev.data?.id
      let matched = false
      for (let i = run.entries.length - 1; i >= 0; i--) {
        const tl = run.entries[i].tool
        if (tl && tl.pending && (tl.id === id || tl.name === (ev.data?.name || ev.message))) {
          tl.result = ev.data?.content || ''
          tl.isError = !!ev.data?.is_error
          tl.pending = false
          matched = true
          break
        }
      }
      if (matched) continue
      entry.kind = 'tool'
      entry.tool = { id: id || `x${entry.id}`, name: ev.data?.name || ev.message, args: '', result: ev.data?.content || '', isError: !!ev.data?.is_error, pending: false }
    } else if (ev.type === 'user.message') {
      entry.kind = 'user'
    } else if (ev.type === 'user.question') {
      entry.kind = 'question'
      entry.question = {
        id: ev.data?.id, header: ev.data?.header, question: ev.data?.question || ev.message,
        options: ev.data?.options || [], multiple: !!ev.data?.multiple,
        allowCustom: !!ev.data?.allow_custom, answered: false,
      }
    } else if (ev.type === 'error') {
      entry.kind = 'error'
    } else if (ev.type === 'context.usage') {
      entry.kind = 'context'
      entry.ctxTokens = ev.data?.prompt_tokens
      entry.ctxLimit = ev.data?.context_limit
      run.ctxTokens = ev.data?.prompt_tokens || run.ctxTokens
      run.ctxLimit = ev.data?.context_limit || run.ctxLimit
    } else if (ev.type === 'role.delta') {
      continue
    }
    run.entries.push(entry)
  }
  return data
}
