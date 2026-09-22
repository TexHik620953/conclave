<script setup lang="ts">
import { state, hydrateRun } from '../store'
import Icon from './Icon.vue'

async function open(id: string) {
  await hydrateRun(id)
  state.currentRunId = id
  state.tab = 'run'
}

function kind(s: string) {
  if (s === 'completed') return 'ok'
  if (s === 'failed') return 'err'
  if (s === 'running') return 'warn'
  return ''
}
function when(ts?: string) {
  if (!ts) return ''
  return new Date(ts).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}
</script>

<template>
  <div class="history">
    <div v-if="!state.history.length" class="muted empty">No runs yet.</div>
    <div v-for="r in state.history" :key="r.id" class="row enter" @click="open(r.id)">
      <span class="pill" :class="kind(r.status)"><span class="dot" :class="kind(r.status)" /> {{ r.status }}</span>
      <span class="pipe">{{ r.pipeline }}</span>
      <span class="task">{{ r.task }}</span>
      <span class="muted-2 when">{{ when(r.started_at) }}</span>
      <span v-if="r.cost_usd" class="mono muted-2 cost">${{ r.cost_usd.toFixed(4) }}</span>
      <Icon name="chevron" :size="15" class="muted-2" />
    </div>
  </div>
</template>

<style scoped>
.history { flex: 1; overflow-y: auto; padding: 20px; display: flex; flex-direction: column; gap: 8px; }
.empty { text-align: center; padding: 40px; }
.row {
  display: grid; grid-template-columns: 110px 110px 1fr auto auto 18px; gap: 12px; align-items: center;
  padding: 12px 14px; border: 1px solid var(--border); border-radius: 12px;
  background: var(--panel); cursor: pointer; transition: border-color 0.15s, background 0.15s;
}
.row:hover { border-color: var(--border-strong); background: var(--panel-2); }
.pipe { font-weight: 600; color: var(--accent); font-size: 13px; }
.task { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
.when, .cost { font-size: 11.5px; white-space: nowrap; }
@media (max-width: 760px) {
  .row { grid-template-columns: 1fr auto; grid-auto-flow: row; }
  .pipe, .when, .cost { display: none; }
}
</style>
