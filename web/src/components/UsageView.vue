<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { api } from '../api'
import { store, toast } from '../store'

const tab = ref('models')
const loading = ref(false)
const models = ref([])
const sessions = ref([])
const titles = ref({})
const expanded = ref({})

const totals = computed(() => {
  const list = tab.value === 'models' ? models.value : sessions.value
  return list.reduce((a, r) => {
    a.input += r.input_tokens || 0
    a.output += r.output_tokens || 0
    a.requests += r.requests || 0
    return a
  }, { input: 0, output: 0, requests: 0 })
})

onMounted(load)
watch(tab, load)
async function load() {
  if (!store.workspace) return
  loading.value = true
  try {
    const data = await api.usage(store.workspace.id, tab.value)
    if (tab.value === 'models') models.value = data.models || []
    else { sessions.value = data.sessions || []; titles.value = data.titles || {} }
  } catch (e) { toast(e.message, 'error') }
  finally { loading.value = false }
}
function fmt(n) {
  n = n || 0
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k'
  return String(n)
}
function pct(r) { return r.context_window ? Math.min(100, (r.last_context || 0) / r.context_window * 100) : 0 }
function sessionTitle(id) { return titles.value[id] || (id || 'Queue / unassigned') }
function toggle(id) { expanded.value[id] = !expanded.value[id] }
</script>

<template>
  <section class="usage-shell">
    <header class="page-head">
      <div><h2>Usage</h2><p>Workspace totals and usage per session</p></div>
      <div class="usage-head-actions">
        <div class="tabs">
          <button :class="{ active: tab === 'models' }" @click="tab = 'models'">By model</button>
          <button :class="{ active: tab === 'sessions' }" @click="tab = 'sessions'">By session</button>
        </div>
        <button class="btn small ghost" :disabled="loading" @click="load">Refresh</button>
      </div>
    </header>

    <div class="usage-totals">
      <div><span>Input</span><b>{{ fmt(totals.input) }}</b></div>
      <div><span>Output</span><b>{{ fmt(totals.output) }}</b></div>
      <div><span>Requests</span><b>{{ totals.requests }}</b></div>
      <div><span>{{ tab === 'models' ? 'Models' : 'Sessions' }}</span><b>{{ tab === 'models' ? models.length : sessions.length }}</b></div>
    </div>

    <div v-if="tab === 'models'" class="usage-table">
      <div class="usage-row head">
        <span>Provider / model</span><span>Context</span><span>Input</span><span>Output</span><span>Requests</span><span>Sessions</span>
      </div>
      <div v-for="r in models" :key="r.provider + r.model" class="usage-row">
        <span class="model-cell"><b>{{ r.model }}</b><small>{{ r.provider }}</small></span>
        <span class="ctx-cell"><span>{{ fmt(r.last_context) }} / {{ fmt(r.context_window) }}</span><i><em :style="{ width: pct(r) + '%' }"></em></i></span>
        <span>{{ fmt(r.input_tokens) }}</span>
        <span>{{ fmt(r.output_tokens) }}</span>
        <span>{{ r.requests || 0 }}</span>
        <span>{{ r.sessions || 0 }}</span>
      </div>
      <div v-if="!models.length" class="empty-table">No usage recorded yet.</div>
    </div>

    <div v-else class="session-usage-list">
      <article v-for="s in sessions" :key="s.session_id || 'queue'" class="session-usage-card">
        <button class="session-usage-head" @click="toggle(s.session_id || 'queue')">
          <span class="session-chevron">{{ expanded[s.session_id || 'queue'] ? '▾' : '▸' }}</span>
          <span class="session-name"><b>{{ sessionTitle(s.session_id) }}</b><small>{{ s.session_id || 'Tasks not linked to a session' }}</small></span>
          <span class="session-num"><b>{{ fmt(s.input_tokens) }}</b><small>input</small></span>
          <span class="session-num"><b>{{ fmt(s.output_tokens) }}</b><small>output</small></span>
          <span class="session-num"><b>{{ s.requests }}</b><small>requests</small></span>
          <span class="session-ctx">{{ fmt(s.last_context) }} / {{ fmt(s.context_window) }}</span>
        </button>
        <div v-if="expanded[s.session_id || 'queue']" class="session-usage-body">
          <div v-for="m in s.models" :key="m.provider + m.model" class="session-model-row">
            <span class="model-cell"><b>{{ m.model }}</b><small>{{ m.provider }}</small></span>
            <span>{{ fmt(m.input_tokens) }} in</span>
            <span>{{ fmt(m.output_tokens) }} out</span>
            <span>{{ m.requests }} req</span>
          </div>
          <div v-if="s.task_ids?.length" class="session-tasks"><span>Tasks</span><b v-for="id in s.task_ids" :key="id">{{ id }}</b></div>
        </div>
      </article>
      <div v-if="!sessions.length" class="empty-table">No usage recorded yet.</div>
    </div>
  </section>
</template>
