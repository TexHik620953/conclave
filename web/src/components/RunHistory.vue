<script setup lang="ts">
import { state, hydrateRun } from '../store'

async function open(id: string) {
  await hydrateRun(id)
  state.currentRunId = id
  state.tab = 'run'
}

function statusClass(s: string) {
  if (s === 'completed') return 'ok'
  if (s === 'failed') return 'err'
  if (s === 'running') return 'warn'
  return 'muted'
}
</script>

<template>
  <div class="history">
    <div class="col-title">Runs</div>
    <div v-if="!state.history.length" class="muted empty">No runs yet.</div>
    <div v-for="r in state.history" :key="r.id" class="row" @click="open(r.id)">
      <span class="mono id">{{ r.id }}</span>
      <span :class="statusClass(r.status)">{{ r.status }}</span>
      <span class="muted task">{{ r.task }}</span>
      <span v-if="r.cost_usd" class="muted">${{ r.cost_usd.toFixed(4) }}</span>
    </div>
  </div>
</template>

<style scoped>
.history { padding: 16px; overflow-y: auto; height: 100%; }
.col-title { font-size: 11px; text-transform: uppercase; color: var(--muted); margin-bottom: 8px; }
.empty { padding: 20px 0; }
.row {
  display: grid; grid-template-columns: 280px 90px 1fr auto; gap: 12px; align-items: center;
  padding: 8px 10px; border: 1px solid var(--border); border-radius: 6px; margin-bottom: 6px; cursor: pointer;
}
.row:hover { border-color: var(--accent); }
.id { font-size: 12px; }
.task { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
