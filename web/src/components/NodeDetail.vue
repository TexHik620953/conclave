<script setup lang="ts">
import { computed } from 'vue'
import type { RunState } from '../store'
import MarkdownView from './MarkdownView.vue'
import TimelineEntry from './TimelineEntry.vue'

const props = defineProps<{ run: RunState; nodeId: string }>()

const info = computed(() => props.run.nodeInfo[props.nodeId] || {})
const entries = computed(() => props.run.entries.filter((e) => e.node === props.nodeId && e.kind !== 'node'))

function statusClass(s?: string) {
  if (s === 'done' || s === 'completed') return 'ok'
  if (s === 'running') return 'warn'
  if (s === 'failed') return 'err'
  return 'muted'
}
</script>

<template>
  <div class="node-detail">
    <div class="head">
      <b>{{ nodeId }}</b>
      <span v-if="info.role" class="role">{{ info.role }}</span>
      <span v-if="info.model" class="mono muted">{{ info.model }}</span>
      <span class="badge" :class="statusClass(run.nodes[nodeId])">{{ run.nodes[nodeId] || '—' }}</span>
    </div>

    <div v-if="info.tools?.length" class="tools">
      <span class="muted">tools:</span>
      <span v-for="t in info.tools" :key="t" class="chip mono">{{ t }}</span>
    </div>

    <details v-if="info.prompt" class="block" open>
      <summary>initial prompt</summary>
      <div class="pad"><MarkdownView :text="info.prompt" /></div>
    </details>

    <details v-if="info.context" class="block">
      <summary>context</summary>
      <pre class="pad-pre">{{ info.context }}</pre>
    </details>

    <div class="messages">
      <div class="col-title">messages</div>
      <div v-if="!entries.length" class="muted">no messages yet</div>
      <TimelineEntry v-for="e in entries" :key="e.id" :entry="e" :run="run" />
    </div>
  </div>
</template>

<style scoped>
.node-detail { padding: 12px 16px; overflow-y: auto; height: 100%; }
.head { display: flex; align-items: baseline; gap: 10px; margin-bottom: 8px; }
.head b { font-size: 15px; }
.role { color: var(--accent); }
.badge { font-size: 11px; padding: 1px 6px; border-radius: 4px; border: 1px solid currentColor; }
.tools { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin-bottom: 10px; font-size: 12px; }
.chip { border: 1px solid var(--border); border-radius: 4px; padding: 1px 6px; color: var(--muted); }
.block { border: 1px solid var(--border); border-radius: 8px; margin-bottom: 10px; background: var(--panel); overflow: hidden; }
.block > summary { cursor: pointer; padding: 8px 12px; background: var(--panel-2); color: var(--muted); font-size: 12px; text-transform: uppercase; }
.pad { padding: 10px 12px; }
.pad-pre { padding: 10px 12px; max-height: 360px; overflow: auto; }
.messages { margin-top: 8px; }
.col-title { font-size: 11px; text-transform: uppercase; color: var(--muted); margin: 8px 0; }
</style>
