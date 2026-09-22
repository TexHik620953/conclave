<script setup lang="ts">
import { computed, ref } from 'vue'
import type { RunState } from '../store'
import { revertLocal } from '../store'
import { api } from '../api'
import MarkdownView from './MarkdownView.vue'
import TimelineEntry from './TimelineEntry.vue'
import Icon from './Icon.vue'

const props = defineProps<{ run: RunState; nodeId: string }>()
const emit = defineEmits<{ (e: 'reverted'): void }>()

const info = computed(() => props.run.nodeInfo[props.nodeId] || {})
const entries = computed(() => props.run.entries.filter((e) => e.node === props.nodeId && e.kind !== 'node'))
const reverting = ref(false)

function statusClass(s?: string) {
  if (s === 'done' || s === 'completed') return 'ok'
  if (s === 'running') return 'warn'
  if (s === 'failed') return 'err'
  return ''
}

async function revert() {
  if (props.run.active) return
  reverting.value = true
  try {
    await api.revertRun(props.run.id, props.nodeId)
    revertLocal(props.run.id, props.nodeId)
    emit('reverted')
  } finally {
    reverting.value = false
  }
}
</script>

<template>
  <div class="node-detail">
    <div class="head">
      <b>{{ nodeId }}</b>
      <span v-if="info.role" class="role">{{ info.role }}</span>
      <span v-if="info.model" class="mono muted-2">{{ info.model }}</span>
      <span class="pill" :class="statusClass(run.nodes[nodeId])">{{ run.nodes[nodeId] || '—' }}</span>
      <div class="spacer" />
      <button
        class="ghost"
        :disabled="run.active || reverting"
        :title="run.active ? 'pause the run first' : 'delete this node and everything after, then resume'"
        @click="revert"
      >
        <Icon name="refresh" :size="15" /> {{ reverting ? 'reverting…' : 'Revert from here' }}
      </button>
    </div>

    <div v-if="info.tools?.length" class="tools">
      <span class="muted">tools</span>
      <span v-for="t in info.tools" :key="t" class="chip">{{ t }}</span>
    </div>

    <details v-if="info.prompt" class="block" open>
      <summary>Initial prompt</summary>
      <div class="pad"><MarkdownView :text="info.prompt" /></div>
    </details>

    <details v-if="info.context" class="block">
      <summary>Context</summary>
      <pre class="pad-pre">{{ info.context }}</pre>
    </details>

    <div class="messages">
      <div class="col-title">Messages</div>
      <div v-if="!entries.length" class="muted">No messages yet.</div>
      <TimelineEntry v-for="e in entries" :key="e.id" :entry="e" :run="run" />
    </div>
  </div>
</template>

<style scoped>
.node-detail { flex: 1; padding: 16px 20px; overflow-y: auto; }
.head { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; flex-wrap: wrap; }
.head b { font-size: 15px; }
.role { color: var(--accent); font-weight: 600; }
.tools { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin-bottom: 14px; font-size: 12px; }
.block { border: 1px solid var(--border); border-radius: 12px; margin-bottom: 12px; background: var(--panel); overflow: hidden; }
.block > summary { cursor: pointer; padding: 10px 14px; background: var(--panel-2); color: var(--muted); font-size: 11.5px; text-transform: uppercase; letter-spacing: 0.05em; }
.pad { padding: 12px 14px; }
.pad-pre { padding: 12px 14px; max-height: 360px; overflow: auto; color: var(--muted); }
.col-title { font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; color: var(--muted); margin: 14px 0 8px; }
</style>
