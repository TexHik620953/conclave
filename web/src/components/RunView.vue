<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { currentRun, revertLocal, sendMessage, state } from '../store'
import { api } from '../api'
import TimelineEntry from './TimelineEntry.vue'
import RunResult from './RunResult.vue'
import NodeDetail from './NodeDetail.vue'
import Icon from './Icon.vue'

const now = ref(Date.now())
let timer: number | undefined
onMounted(() => { timer = window.setInterval(() => (now.value = Date.now()), 1000) })
onUnmounted(() => timer && window.clearInterval(timer))

const run = computed(() => currentRun())
const view = ref<'chat' | 'result' | 'node'>('chat')
const selectedNode = ref('')
const scroll = ref<HTMLElement | null>(null)

const draft = ref('')
const busy = ref(false)
const advanced = ref(false)
const pipeline = ref('')
const workspace = ref('')

const elapsed = computed(() => {
  if (!run.value) return ''
  const s = Math.floor((now.value - run.value.startedAt) / 1000)
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
})
const ctxPct = computed(() => {
  const r = run.value
  if (!r || !r.ctxLimit) return 0
  return Math.min(100, Math.round((r.ctxTokens / r.ctxLimit) * 100))
})
const canResume = computed(() => {
  const r = run.value
  if (!r || r.active) return false
  return ['paused', 'aborted', 'failed', 'interrupted'].includes(r.status)
})
const statusKind = computed(() => {
  const s = run.value?.status || ''
  if (s === 'completed') return 'ok'
  if (s === 'failed') return 'err'
  if (s === 'running') return 'warn'
  return ''
})

const suggestions = [
  'Add CSV export of runs in internal/web',
  'Investigate why latency on /api/runs is growing',
  'Review the security of the auth middleware',
  'Write an onboarding guide for this project',
]

function fmt(n: number) {
  if (!n) return '0'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}

async function start(text?: string) {
  const content = (text ?? draft.value).trim()
  if (!content || busy.value) return
  busy.value = true
  try {
    await sendMessage(content, pipeline.value, workspace.value)
    draft.value = ''
  } catch (e: any) {
    console.error(e)
  } finally {
    busy.value = false
  }
}

async function send() {
  if (!run.value) return start()
  const content = draft.value.trim()
  if (!content || busy.value) return
  busy.value = true
  try {
    if (run.value.sessionId) {
      await sendMessage(content, run.value.pipeline)
    } else if (!run.value.active) {
      await api.followup(run.value.id, content)
    }
    draft.value = ''
    view.value = 'chat'
  } finally {
    busy.value = false
  }
}

async function cancel() { if (run.value) await api.cancelRun(run.value.id) }
async function pause() { if (run.value) await api.pauseRun(run.value.id) }
async function resume() { if (run.value) await api.resumeRun(run.value.id) }
function openNode(id: string) { selectedNode.value = id; view.value = 'node' }
async function revertNode(nodeId: string) {
  if (!run.value || run.value.active) return
  await api.revertRun(run.value.id, nodeId)
  revertLocal(run.value.id, nodeId)
  view.value = 'chat'
}
async function answer(payload: { id: string; selected: string[]; custom: string }) {
  if (!run.value) return
  await api.answer(run.value.id, payload.id, payload.selected, payload.custom)
  const e = run.value.entries.find((x) => x.question?.id === payload.id)
  if (e?.question) e.question.answered = true
}

watch(
  () => [run.value?.entries.length, run.value?.finalText, view.value],
  async () => {
    if (view.value !== 'chat') return
    await nextTick()
    if (scroll.value) scroll.value.scrollTop = scroll.value.scrollHeight
  },
)
</script>

<template>
  <!-- Landing / new chat -->
  <div v-if="!run" class="landing">
    <div class="hero enter">
      <div class="hero-badge"><Icon name="spark" :size="14" /> Controller-driven</div>
      <h1>What should the team work on?</h1>
      <p class="muted">
        The lead model plans, delegates to specialist roles and iterates until the task is done.
        You don't have to design a pipeline.
      </p>

      <div class="composer big">
        <textarea
          v-model="draft"
          rows="3"
          placeholder="Describe the task…  (Enter to start, Shift+Enter for a new line)"
          @keydown.enter.exact.prevent="start()"
        />
        <div class="composer-bar">
          <button class="ghost" @click="advanced = !advanced">
            <Icon name="chevron" :size="15" :style="{ transform: advanced ? 'rotate(90deg)' : 'none' }" />
            Advanced
          </button>
          <div class="spacer" />
          <button class="primary" :disabled="!draft.trim() || busy" @click="start()">
            <Icon name="send" :size="16" /> Start
          </button>
        </div>
      </div>

      <div v-if="advanced" class="advanced enter">
        <div class="field">
          <label>Pipeline</label>
          <select v-model="pipeline">
            <option value="">auto — controller decides (recommended)</option>
            <option v-for="p in state.config.pipelines" :key="p.name" :value="p.name">{{ p.name }} — {{ p.description }}</option>
          </select>
        </div>
        <div class="field">
          <label>Workspace</label>
          <input v-model="workspace" placeholder="path to the repository (optional)" />
        </div>
      </div>

      <div class="suggestions">
        <button v-for="s in suggestions" :key="s" class="suggestion" @click="start(s)">{{ s }}</button>
      </div>
    </div>
  </div>

  <!-- Chat -->
  <div v-else class="chat">
    <header class="chat-head">
      <span class="pill" :class="statusKind">
        <span class="dot" :class="[statusKind, run.active ? 'live' : '']" />
        {{ run.status }}
      </span>
      <span class="pill accent">{{ run.pipeline || 'auto' }}</span>
      <span v-if="run.active" class="muted elapsed">{{ elapsed }}</span>

      <div class="spacer" />

      <span v-if="run.ctxLimit" class="stat" :title="`context ${run.ctxTokens}/${run.ctxLimit}`">
        <span class="muted">ctx</span>
        <span class="bar"><i :style="{ width: ctxPct + '%' }" /></span>
        <span class="mono">{{ fmt(run.ctxTokens) }}/{{ fmt(run.ctxLimit) }}</span>
      </span>
      <span class="stat muted" title="tokens in / out">↑{{ fmt(run.inputTokens) }} ↓{{ fmt(run.outputTokens) }}</span>
      <span v-if="run.cost > 0" class="stat mono muted" title="cost">${{ run.cost.toFixed(4) }}</span>

      <div class="seg">
        <button :class="{ on: view === 'chat' }" @click="view = 'chat'">Chat</button>
        <button :class="{ on: view === 'result' }" @click="view = 'result'">Result</button>
      </div>

      <button v-if="run.active" class="ghost icon" title="pause" @click="pause"><Icon name="pause" :size="16" /></button>
      <button v-if="run.active" class="ghost icon" title="cancel" @click="cancel"><Icon name="stop" :size="15" /></button>
      <button v-if="canResume" class="ghost icon" title="resume" @click="resume"><Icon name="play" :size="16" /></button>
    </header>

    <div class="body">
      <NodeDetail v-if="view === 'node'" :run="run" :node-id="selectedNode" @reverted="view = 'chat'" />
      <RunResult v-else-if="view === 'result'" :run="run" />
      <div v-else ref="scroll" class="timeline">
        <TimelineEntry
          v-for="e in run.entries"
          :key="e.id"
          :entry="e"
          :run="run"
          :can-revert="!run.active"
          @revert="revertNode"
          @answer="answer"
          @open-node="openNode"
        />
        <div v-if="!run.entries.length" class="muted empty">Waiting for the team to start…</div>
      </div>
    </div>

    <div class="composer sticky">
      <textarea
        v-model="draft"
        rows="1"
        :placeholder="run.active ? 'Message the team… (queued and injected at the next step)' : 'Follow-up… (continues in the same context)'"
        @keydown.enter.exact.prevent="send()"
      />
      <button class="primary icon" :disabled="!draft.trim() || busy" title="send" @click="send()">
        <Icon name="send" :size="17" />
      </button>
    </div>
  </div>
</template>

<style scoped>
/* Landing */
.landing { flex: 1; overflow-y: auto; display: grid; place-items: center; padding: 40px 20px; }
.hero { width: min(760px, 100%); text-align: center; }
.hero-badge {
  display: inline-flex; align-items: center; gap: 7px; margin-bottom: 18px;
  font-size: 12px; font-weight: 600; color: var(--accent);
  border: 1px solid rgba(124, 140, 255, 0.35); background: var(--accent-soft);
  padding: 5px 12px; border-radius: 999px;
}
.hero h1 { font-size: clamp(26px, 4vw, 38px); margin: 0 0 10px; letter-spacing: -0.02em; }
.hero p { margin: 0 auto 26px; max-width: 560px; line-height: 1.6; }

.advanced { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-top: 14px; text-align: left; }
.field { display: flex; flex-direction: column; gap: 5px; }
.field label { font-size: 11.5px; color: var(--muted); text-transform: uppercase; letter-spacing: 0.04em; }

.suggestions { display: flex; flex-wrap: wrap; gap: 8px; justify-content: center; margin-top: 26px; }
.suggestion {
  font-size: 12.5px; color: var(--muted); border-radius: 999px; padding: 7px 14px;
  background: transparent; border: 1px solid var(--border);
}
.suggestion:hover { color: var(--text); border-color: var(--border-strong); background: var(--panel); }

/* Chat */
.chat { flex: 1; display: flex; flex-direction: column; min-height: 0; }
.chat-head {
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
  padding: 10px 20px; border-bottom: 1px solid var(--border);
  background: var(--bg-2);
}
.elapsed { font-size: 12px; }
.stat { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; }
.bar { display: inline-block; width: 60px; height: 5px; background: var(--panel-3); border-radius: 3px; overflow: hidden; }
.bar i { display: block; height: 100%; background: linear-gradient(90deg, var(--accent), var(--accent-2)); transition: width 0.3s; }

.seg { display: inline-flex; border: 1px solid var(--border); border-radius: 9px; overflow: hidden; }
.seg button { border: none; border-radius: 0; padding: 6px 12px; font-size: 12.5px; background: transparent; color: var(--muted); }
.seg button.on { background: var(--panel-3); color: var(--text); }

.body { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.timeline { flex: 1; overflow-y: auto; padding: 20px 20px 8px; }
.empty { text-align: center; padding: 40px; }

/* Composer */
.composer {
  display: flex; align-items: flex-end; gap: 10px;
  background: var(--panel); border: 1px solid var(--border); border-radius: var(--radius);
  padding: 8px 8px 8px 14px; box-shadow: var(--shadow-sm);
}
.composer textarea {
  border: none; background: transparent; padding: 8px 0; resize: none;
  max-height: 180px; font-size: 14px;
}
.composer textarea:focus { box-shadow: none; }
.composer.big { text-align: left; padding: 10px 10px 10px 16px; }
.composer.big textarea { font-size: 15px; }
.composer-bar { display: flex; align-items: center; gap: 8px; padding: 6px 2px 2px; }
.composer.sticky { margin: 8px 20px 18px; }
.composer.sticky .icon { width: 38px; height: 38px; flex: none; }

@media (max-width: 700px) {
  .advanced { grid-template-columns: 1fr; }
  .timeline { padding: 14px 12px 4px; }
  .composer.sticky { margin: 8px 12px 12px; }
}
</style>
