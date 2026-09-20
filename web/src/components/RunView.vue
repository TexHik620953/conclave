<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { currentRun } from '../store'
import { api } from '../api'
import TimelineEntry from './TimelineEntry.vue'
import RunResult from './RunResult.vue'
import NodeDetail from './NodeDetail.vue'

const now = ref(Date.now())
let timer: number | undefined
onMounted(() => { timer = window.setInterval(() => (now.value = Date.now()), 1000) })
onUnmounted(() => timer && window.clearInterval(timer))

const run = computed(() => currentRun())
const view = ref<'timeline' | 'result' | 'node'>('timeline')
const selectedNode = ref('')
const tl = ref<HTMLElement | null>(null)

const elapsed = computed(() => {
  if (!run.value) return ''
  const s = Math.floor((now.value - run.value.startedAt) / 1000)
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${s % 60}s`
})
const ctxPct = computed(() => {
  const r = run.value
  if (!r || !r.ctxLimit) return 0
  return Math.min(100, Math.round((r.ctxTokens / r.ctxLimit) * 100))
})

function fmt(n: number) {
  if (!n) return '0'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}
function statusClass(s: string) {
  if (s === 'completed') return 'ok'
  if (s === 'failed') return 'err'
  if (s === 'running') return 'warn'
  return 'muted'
}
async function cancel() {
  if (run.value) await api.cancelRun(run.value.id)
}
function openNode(id: string) {
  selectedNode.value = id
  view.value = 'node'
}

watch(
  () => [run.value?.entries.length, run.value?.finalText, view.value],
  async () => {
    if (view.value !== 'timeline') return
    await nextTick()
    if (tl.value) tl.value.scrollTop = tl.value.scrollHeight
  },
)
</script>

<template>
  <div v-if="!run" class="empty muted">Start a run to see live output.</div>
  <div v-else class="run">
    <div class="statusbar">
      <span class="pipe">{{ run.pipeline }}</span>
      <span :class="statusClass(run.status)">{{ run.status }}</span>
      <span class="muted" v-if="run.active">{{ elapsed }}</span>
      <span class="stat" v-if="run.ctxLimit">
        <span class="muted">ctx</span> {{ fmt(run.ctxTokens) }}/{{ fmt(run.ctxLimit) }}
        <span class="bar"><i :style="{ width: ctxPct + '%' }" /></span>
      </span>
      <span v-else-if="run.ctxTokens" class="stat muted">ctx {{ fmt(run.ctxTokens) }}</span>
      <span class="stat muted">in {{ fmt(run.inputTokens) }}</span>
      <span class="stat muted">out {{ fmt(run.outputTokens) }}</span>
      <span class="stat muted" v-if="run.cost > 0">${{ run.cost.toFixed(4) }}</span>

      <div class="toggle">
        <button :class="{ active: view === 'timeline' }" @click="view = 'timeline'">Timeline</button>
        <button :class="{ active: view === 'result' }" @click="view = 'result'">Result</button>
        <button v-if="view === 'node'" class="active" @click="view = 'timeline'">← {{ selectedNode }}</button>
      </div>
      <button v-if="run.active" class="cancel" @click="cancel">cancel</button>
    </div>

    <div class="body">
      <div class="col nodes">
        <div class="col-title">Nodes</div>
        <div
          v-for="id in run.nodeOrder"
          :key="id"
          class="node"
          :class="[run.nodes[id], { selected: view === 'node' && selectedNode === id }]"
          @click="openNode(id)"
        >
          <span class="dot" />
          <span class="mono">{{ id }}</span>
        </div>
        <div v-if="run.todos.length" class="todos">
          <div class="col-title">Todos</div>
          <div v-for="td in run.todos" :key="td.id" class="todo" :class="td.status">
            <span>{{ td.status === 'completed' ? '☑' : td.status === 'in_progress' ? '◐' : td.status === 'cancelled' ? '☒' : '☐' }}</span>
            <span>{{ td.content }}</span>
          </div>
        </div>
      </div>

      <div class="col main">
        <NodeDetail v-if="view === 'node'" :run="run" :node-id="selectedNode" />
        <RunResult v-else-if="view === 'result'" :run="run" />
        <div v-else ref="tl" class="timeline">
          <TimelineEntry v-for="e in run.entries" :key="e.id" :entry="e" :run="run" />
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.empty { padding: 40px; text-align: center; }
.run { display: flex; flex-direction: column; height: 100%; }
.statusbar {
  display: flex; align-items: center; gap: 12px; padding: 10px 16px; flex-wrap: wrap;
  border-bottom: 1px solid var(--border); background: var(--panel);
}
.pipe { font-weight: 600; color: var(--accent); }
.stat { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; }
.bar { display: inline-block; width: 70px; height: 6px; background: var(--panel-2); border-radius: 3px; overflow: hidden; }
.bar i { display: block; height: 100%; background: var(--accent); }
.toggle { margin-left: auto; display: flex; gap: 4px; }
.toggle button.active { border-color: var(--accent); color: var(--accent); }
.body { display: flex; flex: 1; min-height: 0; }
.col { overflow-y: auto; }
.nodes { width: 200px; border-right: 1px solid var(--border); background: var(--panel); padding: 12px 16px; }
.main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.col-title { font-size: 11px; text-transform: uppercase; color: var(--muted); margin: 8px 0 6px; }
.node { display: flex; align-items: center; gap: 8px; padding: 4px 6px; border-radius: 5px; color: var(--muted); cursor: pointer; }
.node:hover { background: var(--panel-2); }
.node.selected { background: var(--panel-2); color: var(--accent); }
.node .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--muted); }
.node.running { color: var(--warn); } .node.running .dot { background: var(--warn); }
.node.done { color: var(--text); } .node.done .dot { background: var(--ok); }
.todo { display: flex; gap: 6px; font-size: 12.5px; padding: 2px 0; }
.todo.completed { color: var(--muted); text-decoration: line-through; }
.timeline { flex: 1; overflow-y: auto; padding: 12px 16px; }
</style>
