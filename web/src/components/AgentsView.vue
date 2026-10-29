<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api } from '../api'
import { store, toast } from '../store'

const agents = ref([])
const loading = ref(false)
const now = ref(Date.now())
let timer = null

onMounted(() => {
  load()
  timer = setInterval(load, 2000)
})
onBeforeUnmount(() => { if (timer) clearInterval(timer) })

async function load() {
  if (!store.workspace) return
  loading.value = true
  try {
    agents.value = (await api.agents(store.workspace.id)).agents || []
    now.value = Date.now()
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    loading.value = false
  }
}

function duration(agent) {
  const start = new Date(agent.started_at).getTime()
  if (!Number.isFinite(start)) return '—'
  const seconds = Math.max(0, Math.round((now.value - start) / 1000))
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = seconds % 60
  return h ? `${h}h ${m}m` : m ? `${m}m ${s}s` : `${s}s`
}
function tokens(value) {
  value = value || 0
  if (value >= 1_000_000) return (value / 1_000_000).toFixed(1) + 'M'
  if (value >= 1_000) return (value / 1_000).toFixed(1) + 'k'
  return String(value)
}
function contextPct(agent) {
  if (!agent.context_window) return 0
  return Math.min(100, Math.round((agent.last_context || 0) / agent.context_window * 100))
}
function modelName(agent) {
  if (!agent.model) return '—'
  return agent.model.split('/').slice(1).join('/') || agent.model
}
function phaseLabel(agent) {
  if (agent.current_tool) return `${agent.phase} · ${agent.current_tool}`
  return agent.phase || 'running'
}
function openTask(agent) {
  if (!agent.task_id) return
  store.openTaskId = agent.task_id
  store.view = 'board'
}
function openProfile() {
  store.configTab = 'profiles'
  store.view = 'config'
}
const count = computed(() => agents.value.length)
</script>

<template>
  <section class="agents-shell">
    <header class="page-head">
      <div><h2>Agents</h2><p>Live model agents in this workspace · {{ count }} running</p></div>
      <button class="btn small ghost" :disabled="loading" @click="load">Refresh</button>
    </header>

    <div v-if="agents.length" class="agent-list">
      <article v-for="agent in agents" :key="agent.id" class="agent-row">
        <div class="agent-main">
          <div class="agent-ident">
            <span class="agent-dot"></span>
            <div>
              <b>{{ agent.kind === 'worker' ? (agent.worker_id || 'worker') : agent.kind }}</b>
              <small>{{ agent.id }}</small>
            </div>
          </div>

          <div class="agent-task">
            <span class="agent-label">Task</span>
            <button v-if="agent.task_id" class="agent-link" @click="openTask(agent)">{{ agent.task_id }}</button>
            <span v-else class="muted">workspace chat</span>
            <small v-if="agent.task_title">{{ agent.task_title }}</small>
          </div>

          <div class="agent-model">
            <span class="agent-label">Model</span>
            <b>{{ modelName(agent) }}</b>
            <small>{{ agent.provider || 'provider' }}</small>
          </div>

          <div class="agent-context">
            <span class="agent-label">Context</span>
            <span>{{ tokens(agent.last_context) }} / {{ tokens(agent.context_window) }}</span>
            <i><em :style="{ width: contextPct(agent) + '%' }"></em></i>
          </div>

          <div class="agent-tokens">
            <span class="agent-label">Tokens</span>
            <b>{{ tokens(agent.input_tokens) }} in</b>
            <b>{{ tokens(agent.output_tokens) }} out</b>
          </div>

          <div class="agent-time">
            <span class="agent-label">Running</span>
            <b>{{ duration(agent) }}</b>
            <small>{{ agent.requests }} req</small>
          </div>
        </div>

        <footer class="agent-footer">
          <span :class="['agent-phase', { tool: agent.current_tool }]">{{ phaseLabel(agent) }}</span>
          <div class="agent-actions">
            <button v-if="agent.task_id" class="btn small ghost" @click="openTask(agent)">Open task</button>
            <button class="btn small ghost" @click="openProfile">Profile</button>
          </div>
        </footer>
      </article>
    </div>

    <div v-else class="agents-empty">
      <div class="empty-mark">◎</div>
      <h3>No agents running</h3>
      <p>Start the board queue or send a prompt to see live agents here.</p>
    </div>
  </section>
</template>
