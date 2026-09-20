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

export type EntryKind = 'node' | 'assistant' | 'tool' | 'event' | 'error' | 'context'

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

export const state = reactive({
  config: { pipelines: [] as any[], roles: [] as any[] },
  history: [] as any[],
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

export function startRun(runId: string, pipeline: string, task: string) {
  state.runs[runId] = newRun(runId, pipeline, task)
  state.currentRunId = runId
  state.tab = 'run'
}

function lastAssistant(run: RunState, node: string, role: string): Entry | undefined {
  for (let i = run.entries.length - 1; i >= 0; i--) {
    const e = run.entries[i]
    if (e.kind === 'assistant' && e.node === node && e.role === role) return e
  }
  return undefined
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
      let e = lastAssistant(run, ev.node_id, ev.role)
      if (!e || e.done) {
        e = push(run, { kind: 'assistant', node: ev.node_id, role: ev.role, model: ev.model, text: '', done: false })
      }
      e.text = (e.text || '') + (ev.message || '')
      if (ev.model) e.model = ev.model
      run.finalText = e.text || ''
      break
    }
    case 'role.message': {
      let e = lastAssistant(run, ev.node_id, ev.role)
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

export async function hydrateRun(id: string) {
  const data: any = await api.getRun(id)
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
    const entry: Entry = { id: ++seq, time: t, kind: 'event', node: ev.node_id, role: ev.role, message: ev.message }
    if (ev.type === 'node.started') { entry.kind = 'node'; if (!run.nodes[ev.node_id]) run.nodeOrder.push(ev.node_id); run.nodes[ev.node_id] = 'done' }
    else if (ev.type === 'role.message') { entry.kind = 'assistant'; entry.text = ev.message; entry.done = true; run.finalText = ev.message }
    else if (ev.type === 'error') { entry.kind = 'error' }
    else if (ev.type === 'context.usage') { entry.kind = 'context' }
    run.entries.push(entry)
  }
  return data
}
