<script setup lang="ts">
import type { Entry, RunState } from '../store'
import { ref } from 'vue'
import MarkdownView from './MarkdownView.vue'
import Icon from './Icon.vue'

const props = defineProps<{ entry: Entry; run: RunState; canRevert?: boolean }>()
const emit = defineEmits<{
  (e: 'revert', node: string): void
  (e: 'answer', payload: { id: string; selected: string[]; custom: string }): void
  (e: 'open-node', node: string): void
}>()

const picked = ref<string[]>([])
const custom = ref('')

function isPicked(label: string) {
  return picked.value.includes(label)
}
function toggle(label: string) {
  const q = props.entry.question
  if (!q) return
  if (q.multiple) {
    const i = picked.value.indexOf(label)
    if (i >= 0) picked.value.splice(i, 1)
    else picked.value.push(label)
  } else {
    picked.value = [label]
  }
}
function submitAnswer() {
  const q = props.entry.question
  if (!q || q.answered) return
  emit('answer', { id: q.id, selected: picked.value, custom: custom.value })
}

function t(ms: number) {
  return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}
function pretty(s: string) {
  try { return JSON.stringify(JSON.parse(s), null, 2) } catch { return s }
}
function oneLine(s?: string) {
  if (!s) return ''
  const x = s.replace(/\s+/g, ' ').trim()
  return x.length > 80 ? x.slice(0, 80) + '…' : x
}
function fmt(n: number) {
  if (!n) return '0'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}
function hue(role?: string) {
  if (!role) return 220
  let h = 0
  for (const ch of role) h = (h * 31 + ch.charCodeAt(0)) % 360
  return h
}
function roleColor(role?: string) {
  return `hsl(${hue(role)}, 68%, 66%)`
}
function initials(role?: string) {
  if (!role) return '?'
  const parts = role.split(/[_\s-]+/).filter(Boolean)
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
  return role.slice(0, 2).toUpperCase()
}
</script>

<template>
  <!-- node marker -->
  <div v-if="entry.kind === 'node'" class="node-mark" @click="entry.node && emit('open-node', entry.node)">
    <span class="avatar" :style="{ background: roleColor(entry.role) }">{{ initials(entry.role) }}</span>
    <span class="node-label">
      <b>{{ entry.node }}</b>
      <span v-if="entry.role" :style="{ color: roleColor(entry.role) }">{{ entry.role }}</span>
      <span v-if="entry.model" class="mono muted-2">{{ entry.model }}</span>
    </span>
    <span class="rule" />
    <Icon name="chevron" :size="14" class="muted-2" />
  </div>

  <!-- assistant message -->
  <div
    v-else-if="entry.kind === 'assistant'"
    class="msg assistant enter"
    :style="{ '--role': roleColor(entry.role) }"
  >
    <span class="avatar" :style="{ background: roleColor(entry.role) }">{{ initials(entry.role) }}</span>
    <div class="bubble">
      <div class="meta">
        <span class="role">{{ entry.role }}</span>
        <span v-if="entry.model" class="mono muted-2 model">{{ entry.model }}</span>
        <span v-if="entry.done" class="muted-2">·</span>
        <span v-if="entry.done" class="muted-2 time">{{ t(entry.time) }}</span>
        <div class="spacer" />
        <button
          v-if="canRevert && entry.node"
          class="ghost tiny"
          title="delete this node and everything after, then resume"
          @click="emit('revert', entry.node!)"
        >
          revert
        </button>
      </div>
      <div class="content">
        <MarkdownView :text="entry.text || ''" />
        <span v-if="!entry.done" class="cursor">▋</span>
      </div>
    </div>
  </div>

  <!-- user message -->
  <div v-else-if="entry.kind === 'user'" class="msg user enter">
    <div class="bubble">
      <div class="meta"><span class="you">You</span><span class="muted-2 time">{{ t(entry.time) }}</span></div>
      <div class="content plain">{{ entry.message }}</div>
    </div>
    <span class="avatar me"><Icon name="user" :size="15" /></span>
  </div>

  <!-- tool call -->
  <details v-else-if="entry.kind === 'tool'" class="tool enter" :class="{ error: entry.tool?.isError }">
    <summary class="tool-head">
      <span class="tool-icon"><Icon name="tool" :size="14" /></span>
      <span class="tool-name mono">{{ entry.tool?.name }}</span>
      <span v-if="entry.tool?.pending" class="pill warn">running</span>
      <span v-else-if="entry.tool?.isError" class="pill err">error</span>
      <span v-else class="pill ok">ok</span>
      <span class="hint muted-2">{{ oneLine(entry.tool?.args) }}</span>
      <span class="muted-2 time">{{ t(entry.time) }}</span>
      <Icon name="down" :size="14" class="muted-2 caret" />
    </summary>
    <div class="tool-body">
      <div class="lbl muted">arguments</div>
      <pre>{{ pretty(entry.tool?.args || '') }}</pre>
      <template v-if="entry.tool?.result">
        <div class="lbl muted">result</div>
        <pre class="result">{{ entry.tool.result }}</pre>
      </template>
    </div>
  </details>

  <!-- question -->
  <div v-else-if="entry.kind === 'question'" class="question enter" :class="{ answered: entry.question?.answered }">
    <div class="q-head">
      <span class="q-icon"><Icon name="spark" :size="15" /></span>
      <b>{{ entry.question?.header || 'Question' }}</b>
      <span v-if="entry.role" class="muted-2 mono">{{ entry.role }}</span>
      <div class="spacer" />
      <span v-if="entry.question?.answered" class="pill ok">answered</span>
      <span class="muted-2 time">{{ t(entry.time) }}</span>
    </div>
    <div class="q-text">{{ entry.question?.question }}</div>
    <label v-for="opt in entry.question?.options" :key="opt.label" class="q-opt">
      <input
        :type="entry.question?.multiple ? 'checkbox' : 'radio'"
        :checked="isPicked(opt.label)"
        :disabled="entry.question?.answered"
        @change="toggle(opt.label)"
      />
      <span>{{ opt.label }}</span>
      <span v-if="opt.description" class="muted"> — {{ opt.description }}</span>
    </label>
    <input
      v-if="entry.question?.allowCustom"
      v-model="custom"
      class="q-custom"
      :disabled="entry.question?.answered"
      placeholder="your own answer…"
    />
    <button class="primary q-submit" :disabled="entry.question?.answered" @click="submitAnswer">
      {{ entry.question?.answered ? 'answered' : 'Submit' }}
    </button>
  </div>

  <div v-else-if="entry.kind === 'context'" class="context">
    <span class="dot" /> ctx {{ fmt(entry.ctxTokens || 0) }}/{{ fmt(entry.ctxLimit || 0) }}
    <span v-if="entry.role"> · {{ entry.role }}</span>
  </div>
  <div v-else-if="entry.kind === 'error'" class="error-line enter"><Icon name="alert" :size="14" /> {{ entry.message }}</div>
  <div v-else class="event-line">{{ entry.message }}</div>
</template>

<style scoped>
.avatar {
  width: 30px; height: 30px; border-radius: 9px; flex: none;
  display: grid; place-items: center;
  font-size: 11px; font-weight: 700; color: #0b0d13;
}
.avatar.me { background: var(--panel-3); color: var(--muted); border: 1px solid var(--border); }

/* node marker */
.node-mark {
  display: flex; align-items: center; gap: 10px; margin: 22px 0 12px;
  cursor: pointer; opacity: 0.9;
}
.node-mark:hover { opacity: 1; }
.node-mark .avatar { width: 24px; height: 24px; border-radius: 7px; font-size: 9px; }
.node-label { display: flex; align-items: baseline; gap: 8px; font-size: 12.5px; white-space: nowrap; }
.node-label b { color: var(--text); }
.node-label .mono { font-size: 11px; }
.rule { flex: 1; height: 1px; background: var(--border); }

/* messages */
.msg { display: flex; gap: 11px; margin-bottom: 14px; align-items: flex-start; }
.msg.user { justify-content: flex-end; }
.bubble {
  max-width: min(760px, 86%);
  background: var(--panel); border: 1px solid var(--border); border-radius: 13px;
  padding: 10px 14px; box-shadow: var(--shadow-sm);
}
.msg.assistant .bubble { border-left: 2px solid var(--role, var(--accent)); }
.msg.user .bubble {
  background: linear-gradient(135deg, rgba(124, 140, 255, 0.18), rgba(167, 139, 250, 0.14));
  border-color: rgba(124, 140, 255, 0.3);
}
.meta { display: flex; align-items: center; gap: 7px; font-size: 11.5px; margin-bottom: 5px; }
.role { font-weight: 650; color: var(--role, var(--accent)); }
.model { font-size: 11px; }
.you { font-weight: 650; color: var(--accent); }
.time { font-size: 10.5px; }
.content { line-height: 1.6; }
.content.plain { white-space: pre-wrap; }
.cursor { color: var(--accent); animation: blink 1s steps(2) infinite; }
@keyframes blink { 50% { opacity: 0; } }
.tiny { padding: 2px 8px; font-size: 11px; }

/* tool */
.tool { border: 1px solid var(--border); border-radius: 11px; margin: 0 0 12px 41px; background: var(--panel); overflow: hidden; }
.tool.error { border-color: rgba(255, 107, 107, 0.4); }
.tool-head {
  display: flex; align-items: center; gap: 9px; padding: 8px 12px; cursor: pointer;
  list-style: none; font-size: 12.5px;
}
.tool-head::-webkit-details-marker { display: none; }
.tool-head:hover { background: var(--panel-2); }
.tool-icon { color: var(--muted); display: inline-flex; }
.tool-name { font-weight: 600; }
.hint { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11.5px; }
.caret { transition: transform 0.2s; }
.tool[open] .caret { transform: rotate(180deg); }
.tool-body { border-top: 1px solid var(--border); padding: 4px 0 8px; }
.tool-body .lbl { font-size: 10.5px; text-transform: uppercase; letter-spacing: 0.05em; padding: 8px 12px 2px; }
.tool-body pre { padding: 0 12px; max-height: 320px; overflow: auto; color: var(--muted); }
.tool-body .result { color: var(--text); }

/* question */
.question {
  border: 1px solid rgba(124, 140, 255, 0.45); border-radius: 13px;
  background: linear-gradient(135deg, rgba(124, 140, 255, 0.08), transparent);
  padding: 14px; margin: 0 0 14px 41px;
}
.question.answered { opacity: 0.75; border-color: var(--border); }
.q-head { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.q-icon { color: var(--accent); display: inline-flex; }
.q-text { margin: 8px 0 10px; line-height: 1.5; }
.q-opt { display: flex; align-items: center; gap: 9px; padding: 4px 0; cursor: pointer; }
.q-opt input { width: auto; }
.q-custom { margin-top: 8px; }
.q-submit { margin-top: 12px; }

/* misc */
.context { display: flex; align-items: center; gap: 7px; font-size: 11.5px; color: var(--muted-2); padding: 2px 0 2px 41px; }
.error-line {
  display: flex; align-items: center; gap: 8px; color: var(--err); font-size: 12.5px;
  border: 1px solid rgba(255, 107, 107, 0.3); background: rgba(255, 107, 107, 0.07);
  border-radius: 10px; padding: 8px 12px; margin: 0 0 12px 41px;
}
.event-line { font-size: 12px; color: var(--muted-2); padding: 2px 0 2px 41px; }
</style>
