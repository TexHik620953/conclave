<script setup lang="ts">
import type { Entry, RunState } from '../store'
import MarkdownView from './MarkdownView.vue'

const props = defineProps<{ entry: Entry; run: RunState }>()

function t(ms: number) {
  return new Date(ms).toLocaleTimeString()
}
function pretty(s: string) {
  try { return JSON.stringify(JSON.parse(s), null, 2) } catch { return s }
}
function oneLine(s?: string) {
  if (!s) return ''
  const t = s.replace(/\s+/g, ' ').trim()
  return t.length > 90 ? t.slice(0, 90) + '…' : t
}
function fmt(n: number) {
  if (!n) return '0'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}
function roleColor(role?: string) {
  if (!role) return 'var(--muted)'
  let h = 0
  for (const ch of role) h = (h * 31 + ch.charCodeAt(0)) % 360
  return `hsl(${h}, 70%, 62%)`
}
</script>

<template>
  <div v-if="entry.kind === 'node'" class="tl-divider">
    <span class="line" :style="{ background: roleColor(entry.role) }" />
    <span class="label">
      <b>{{ entry.node }}</b>
      <span v-if="entry.role" :style="{ color: roleColor(entry.role) }"> · {{ entry.role }}</span>
      <span v-if="entry.model" class="model mono"> · {{ entry.model }}</span>
    </span>
    <span class="line" :style="{ background: roleColor(entry.role) }" />
  </div>

  <div
    v-else-if="entry.kind === 'assistant'"
    class="tl-card"
    :style="{ borderLeftColor: roleColor(entry.role) }"
  >
    <div class="tl-head">
      <span class="role" :style="{ color: roleColor(entry.role) }">{{ entry.role }}</span>
      <span class="model mono">{{ entry.model }}</span>
      <span v-if="entry.done" class="ok badge">done</span>
      <span class="time muted">{{ t(entry.time) }}</span>
    </div>
    <div class="tl-body">
      <MarkdownView :text="entry.text || ''" />
      <span v-if="!entry.done" class="cursor">▋</span>
    </div>
  </div>

  <details v-else-if="entry.kind === 'tool'" class="tl-tool" :class="{ error: entry.tool?.isError }">
    <summary class="tl-head">
      <span class="tool-name mono">⚙ {{ entry.tool?.name }}</span>
      <span v-if="entry.tool?.pending" class="warn badge">running…</span>
      <span v-else-if="entry.tool?.isError" class="err badge">error</span>
      <span v-else class="ok badge">ok</span>
      <span class="hint muted">{{ oneLine(entry.tool?.args) }}</span>
      <span class="time muted">{{ t(entry.time) }}</span>
    </summary>
    <div class="lbl muted">arguments</div>
    <pre>{{ pretty(entry.tool?.args || '') }}</pre>
    <template v-if="entry.tool?.result">
      <div class="lbl muted">result</div>
      <pre class="tool-result">{{ entry.tool.result }}</pre>
    </template>
  </details>

  <div v-else-if="entry.kind === 'context'" class="tl-line muted">
    ctx {{ fmt(entry.ctxTokens || 0) }}/{{ fmt(entry.ctxLimit || 0) }}
    <span v-if="entry.role"> · {{ entry.role }}</span><span v-if="entry.model" class="mono"> {{ entry.model }}</span>
  </div>
  <div v-else-if="entry.kind === 'error'" class="tl-line err">! {{ entry.message }}</div>
  <div v-else class="tl-line muted">{{ entry.message }}</div>
</template>

<style scoped>
.tl-divider { display: flex; align-items: center; gap: 10px; margin: 18px 0 10px; }
.tl-divider .line { flex: 1; height: 2px; opacity: 0.35; border-radius: 2px; }
.tl-divider .label { font-size: 13px; white-space: nowrap; }
.tl-divider .label b { color: var(--text); }
.model { font-size: 12px; color: var(--muted); }

.tl-card, .tl-tool {
  border: 1px solid var(--border); border-left: 3px solid var(--muted);
  border-radius: 8px; margin-bottom: 10px; background: var(--panel); overflow: hidden;
}
.tl-tool { border-left-width: 1px; }
.tl-tool > summary { list-style: none; cursor: pointer; }
.tl-tool > summary::-webkit-details-marker { display: none; }
.tl-tool > summary::before { content: '▸'; color: var(--muted); margin-right: 2px; }
.tl-tool[open] > summary::before { content: '▾'; }
.tl-tool > summary:hover { background: var(--border); }
.hint { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.tl-tool.error { border-color: var(--err); }
.tl-head {
  display: flex; align-items: center; gap: 10px; padding: 7px 12px;
  background: var(--panel-2); border-bottom: 1px solid var(--border);
}
.tl-tool > summary.tl-head { border-bottom: none; }
.tl-tool[open] > summary.tl-head { border-bottom: 1px solid var(--border); }
.role { font-weight: 600; }
.time { margin-left: auto; font-size: 11px; }
.badge { font-size: 11px; padding: 1px 6px; border-radius: 4px; border: 1px solid currentColor; }
.tl-body { padding: 10px 12px; }
.cursor { color: var(--accent); animation: blink 1s steps(2) infinite; }
@keyframes blink { 50% { opacity: 0; } }
.tl-tool .lbl { font-size: 11px; text-transform: uppercase; padding: 6px 12px 0; }
.tl-tool pre { padding: 4px 12px 8px; max-height: 320px; overflow: auto; }
.tool-result { border-top: 1px dashed var(--border); }
.tl-line { font-size: 12.5px; padding: 2px 0; }
</style>
