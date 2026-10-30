<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import hljs from 'highlight.js'
import { store, sendMessage, conclaveAction, cancelRequest, openLogs, revertTo, forkAt, regenerate, continueChat } from '../store'

const input = ref('')
const scroller = ref(null)
const liveSeconds = ref(0)
const atBottom = ref(true)
const unread = ref(false)
const activeActions = ref(-1)
const thinkingOpen = ref({})
const thinkingEls = ref({})
const thinkingAtBottom = ref({})
let clock = null
watch(() => store.busy, (busy) => {
  if (clock) { clearInterval(clock); clock = null }
  if (busy) {
    liveSeconds.value = 0
    clock = setInterval(() => { liveSeconds.value++ }, 1000)
  }
}, { immediate: true })
onMounted(async () => { await nextTick(); jumpBottom() })
onBeforeUnmount(() => { if (clock) clearInterval(clock) })
const elapsed = computed(() => store.busy ? liveSeconds.value : 0)
const waitingLong = computed(() => store.busy && elapsed.value > 45)
const liveInAssistant = computed(() => store.busy && store.chat.at(-1)?.kind === 'assistant' && !store.chat.at(-1)?.content && !store.chat.at(-1)?.reasoning)
const showCommands = computed(() => input.value.startsWith('/') && !input.value.includes('\n'))
const commands = [
  ['/models', 'choose provider and model'],
  ['/board', 'open project board'],
  ['/usage', 'token usage by model'],
  ['/new', 'new session'],
  ['/compact', 'compact context'],
  ['/help', 'keyboard and commands'],
]
const filtered = computed(() => {
  const q = input.value.slice(1).split(/\s/)[0].toLowerCase()
  return commands.filter(c => c[0].slice(1).startsWith(q))
})

function renderMarkdown(text) {
  const root = document.createElement('div')
  root.innerHTML = marked.parse(text || '')
  root.querySelectorAll('pre code').forEach(el => {
    const cls = [...el.classList].find(c => c.startsWith('language-'))
    const lang = cls ? cls.slice(9) : ''
    try {
      const res = lang && hljs.getLanguage(lang) ? hljs.highlight(el.textContent, { language: lang }) : hljs.highlightAuto(el.textContent)
      el.innerHTML = res.value
      el.classList.add('hljs')
    } catch {}
  })
  return DOMPurify.sanitize(root.innerHTML)
}

function submit() {
  const text = input.value.trim()
  if (!text) return
  if (text.startsWith('/')) {
    input.value = ''
    window.dispatchEvent(new CustomEvent('conclave-command', { detail: text }))
    return
  }
  input.value = ''
  sendMessage(text)
}

function onKey(e) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    submit()
  }
}

function pickCommand(c) {
  input.value = c[0] + ' '
}

watch(() => store.state?.session?.id, async () => {
  await nextTick()
  jumpBottom()
})
watch(() => [store.chat.length, store.chat.at(-1)?.content, store.chat.at(-1)?.reasoning, store.phase], async () => {
  await nextTick()
  stickThinkingToBottom()
  if (!scroller.value) return
  if (atBottom.value) {
    scroller.value.scrollTop = scroller.value.scrollHeight
    unread.value = false
  } else {
    unread.value = true
  }
}, { deep: true })

function onScroll() {
  if (!scroller.value) return
  const el = scroller.value
  atBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 80
  if (atBottom.value) unread.value = false
}
function jumpBottom() {
  if (!scroller.value) return
  scroller.value.scrollTo({ top: scroller.value.scrollHeight, behavior: 'smooth' })
  atBottom.value = true
  unread.value = false
}
function navigateMessage(dir) {
  if (!scroller.value) return
  const nodes = [...scroller.value.querySelectorAll('.msg')]
  if (!nodes.length) return
  const top = scroller.value.scrollTop + 20
  if (dir < 0) {
    const prev = [...nodes].reverse().find(n => n.offsetTop < top - 10)
    if (prev) scroller.value.scrollTo({ top: prev.offsetTop - 12, behavior: 'smooth' })
    else scroller.value.scrollTo({ top: 0, behavior: 'smooth' })
  } else {
    const next = nodes.find(n => n.offsetTop > top + 10)
    if (next) scroller.value.scrollTo({ top: next.offsetTop - 12, behavior: 'smooth' })
    else jumpBottom()
  }
}
function toggleActions(i) { activeActions.value = activeActions.value === i ? -1 : i }
function toggleThinking(i) {
  thinkingOpen.value[i] = !thinkingOpen.value[i]
  if (thinkingOpen.value[i]) {
    thinkingAtBottom.value[i] = true
    nextTick(() => stickThinkingToBottom())
  }
}
function setThinkingRef(i, el) {
  if (el) thinkingEls.value[i] = el
  else delete thinkingEls.value[i]
}
function onThinkingScroll(i, event) {
  const el = event.target
  thinkingAtBottom.value[i] = el.scrollHeight - el.scrollTop - el.clientHeight < 16
}
function stickThinkingToBottom() {
  for (const [key, el] of Object.entries(thinkingEls.value)) {
    const i = Number(key)
    if (!thinkingOpen.value[i] || thinkingAtBottom.value[i] === false) continue
    el.scrollTop = el.scrollHeight
  }
}
function toolSummary(name, raw) {
  let args = {}
  try { args = JSON.parse(raw || '{}') } catch {}
  if (name === 'read_file' || name === 'write_file' || name === 'edit_file' || name === 'list_dir') return args.path || ''
  if (name === 'shell' || name === 'shell_background') return args.command || ''
  if (name === 'grep') return [args.pattern, args.path].filter(Boolean).join(' · ')
  if (name === 'glob') return args.pattern || ''
  if (name === 'git') return args.args || ''
  if (args.path) return args.path
  if (args.command) return args.command
  const first = Object.entries(args).find(([, v]) => typeof v === 'string' && v)
  return first ? String(first[1]) : ''
}
function isLastAssistant(i) {
  for (let j = store.chat.length - 1; j >= 0; j--) if (store.chat[j].kind === 'assistant') return j === i
  return false
}

const conclave = computed(() => store.conclave)
const interrupted = computed(() => {
  const session = store.state?.session
  if (!session || store.busy) return false
  const messages = session.mode === 'conclave' ? (session.conclave_messages || []) : (session.messages || [])
  const last = messages[messages.length - 1]
  return !!last && last.role !== 'assistant'
})
const plan = computed(() => store.conclave?.pending_plan)
const activeModel = computed(() => {
  const s = store.state?.session
  if (!s) return ''
  return s.model?.split('/').slice(1).join('/') || s.model
})
</script>

<template>
  <section class="chat-shell">
    <div ref="scroller" class="chat-scroll" @scroll="onScroll">
      <div v-if="!store.chat.length" class="empty-state">
        <div class="empty-mark">C</div>
        <h2>What are we building?</h2>
        <p>Ask for a change, a review, or open the board to pick a task.</p>
        <div class="empty-hints">
          <span><b>/</b> commands</span>
          <span><b>Enter</b> send</span>
          <span><b>Shift+Enter</b> newline</span>
        </div>
      </div>

      <article v-for="(m, i) in store.chat" :key="i" :class="['msg', m.kind]">
        <template v-if="m.kind === 'user'">
          <div class="prompt-block" @click="toggleActions(i)">{{ m.content }}</div>
          <div v-if="m.messageIndex !== undefined" :class="['msg-actions', { visible: activeActions === i }]">
            <button @click.stop="revertTo(m.messageIndex)">Revert</button>
            <button @click.stop="forkAt(m.messageIndex)">Fork</button>
          </div>
        </template>
        <template v-else-if="m.kind === 'assistant'">
          <div class="assistant-avatar" @click="toggleActions(i)">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round"><path d="M18.2 7.8A7.2 7.2 0 1 0 18.2 16.2"/></svg>
          </div>
          <div v-if="m.reasoning" class="thinking-block">
            <button class="thinking-head" @click="toggleThinking(i)">
              <span class="thinking-label">Thinking</span>
              <span>{{ thinkingOpen[i] ? 'Collapse' : 'Expand' }}</span>
            </button>
            <pre :ref="el => setThinkingRef(i, el)" :class="['thinking-body', { collapsed: !thinkingOpen[i] }]" @scroll="onThinkingScroll(i, $event)">{{ m.reasoning }}</pre>
          </div>
          <div v-if="m.streaming && !m.content && !m.reasoning" class="assistant-pending">
            <span class="spinner"></span>
            <span>{{ store.phase || 'thinking…' }}</span>
            <span class="elapsed">{{ elapsed }}s</span>
            <button v-if="waitingLong" class="cancel-request" @click="cancelRequest">Cancel</button>
          </div>
          <div v-else-if="m.content" class="markdown" v-html="renderMarkdown(m.content)"></div>
          <span v-if="m.streaming && m.content" class="caret"></span>
          <div v-if="m.messageIndex !== undefined" :class="['msg-actions', 'assistant-actions', { visible: activeActions === i }]">
            <button @click.stop="forkAt(m.messageIndex)">Fork</button>
            <button v-if="isLastAssistant(i)" @click.stop="regenerate(m.messageIndex)">Regenerate</button>
          </div>
        </template>
        <template v-else-if="m.kind === 'tool'">
          <details class="tool-card" :open="m.status === 'running'">
            <summary>
              <span class="tool-dot" :class="m.status"></span>
              <span class="tool-name">{{ m.name }}</span>
              <span v-if="m.batchId" class="tool-batch">batch</span>
              <span class="tool-args">{{ toolSummary(m.name, m.args) }}</span>
              <span class="tool-status">{{ m.status }}</span>
            </summary>
            <pre>{{ m.content }}</pre>
          </details>
        </template>
      </article>

      <div v-if="store.lastError" class="error-panel">
        <div><b>Request failed</b><p>{{ store.lastError }}</p></div>
        <div><button class="btn" @click="continueChat">Continue</button><button class="btn" @click="regenerate()">Retry</button><button class="btn ghost" @click="openLogs">Logs</button><button class="btn ghost" @click="store.lastError = ''">Dismiss</button></div>
      </div>
      <div v-else-if="interrupted" class="error-panel recovery-panel">
        <div><b>Last run was interrupted</b><p>The previous request did not finish. Continue from the saved progress, or retry the last turn from scratch.</p></div>
        <div><button class="btn" @click="continueChat">Continue</button><button class="btn ghost" @click="regenerate()">Retry last turn</button></div>
      </div>
      <div v-if="conclave" class="conclave-banner">
        <div class="conclave-status">
          <span class="conclave-orb">◆</span>
          <div class="conclave-status-text">
            <b>Conclave master</b>
            <small>{{ conclave.run?.goal || 'Discuss the goal, then approve the plan to dispatch workers.' }}</small>
          </div>
          <span class="conclave-phase">{{ conclave.run?.phase || 'planning' }}</span>
        </div>
        <div v-if="plan" class="plan-box">
          <p>{{ plan.goal }}</p>
          <div v-for="t in plan.tasks" :key="t.id" class="plan-task">
            <span>{{ t.id }}</span><b>{{ t.title }}</b><em>C{{ t.complexity || 3 }} · {{ t.worker }}</em>
          </div>
        </div>
        <div v-if="!plan && !conclave.running && conclave.run?.phase !== 'paused'" class="conclave-hint">
          Ask the master below. It will create Board tasks and a plan; nothing is dispatched until you approve it.
        </div>
        <div class="banner-actions">
          <button v-if="plan" class="btn primary" :disabled="store.busy || conclave.busy" @click="conclaveAction('approve')">{{ (store.busy || conclave.busy) ? 'Master is finishing…' : 'Approve plan' }}</button>
          <button v-if="conclave.running" class="btn ghost" @click="conclaveAction('pause')">Pause run</button>
          <button v-if="conclave.run?.phase === 'paused'" class="btn" @click="conclaveAction('resume')">Resume run</button>
          <button v-if="conclave.run?.phase === 'failed'" class="btn" :disabled="conclave.busy" @click="conclaveAction('retry_review')">Retry master review</button>
          <button v-if="conclave.running || conclave.run?.phase === 'paused'" class="btn ghost" @click="conclaveAction('cancel')">Cancel run</button>
        </div>
      </div>
      <div v-if="store.busy && !liveInAssistant" class="thinking">
        <span class="spinner"></span>
        <span>{{ store.phase || 'thinking…' }}</span>
        <span class="elapsed">{{ elapsed }}s</span>
        <span v-if="waitingLong" class="slow-note">provider is taking longer than usual</span>
        <button class="cancel-request" @click="cancelRequest">Cancel</button>
      </div>
    </div>

    <div class="chat-nav">
      <button title="Previous message" @click="navigateMessage(-1)">↑</button>
      <button title="Next message" @click="navigateMessage(1)">↓</button>
    </div>
    <button v-if="!atBottom" class="jump-bottom" @click="jumpBottom">↓ <span v-if="unread"></span></button>

    <div class="composer-wrap">
      <div v-if="showCommands" class="command-menu">
        <button v-for="c in filtered" :key="c[0]" type="button" @click="pickCommand(c)">
          <b>{{ c[0] }}</b><span>{{ c[1] }}</span>
        </button>
      </div>
      <div class="composer">
        <textarea
          v-model="input"
          rows="1"
          placeholder="Ask Conclave anything…  / for commands"
          @keydown="onKey"
        ></textarea>
        <button v-if="!store.busy" class="send-btn" :disabled="!input.trim()" @click="submit" title="Send">
          ↑
        </button>
        <button v-else class="send-btn stop-btn" @click="cancelRequest" title="Stop request" aria-label="Stop request">
          <span class="stop-glyph"></span>
        </button>
      </div>
      <div class="composer-meta">
        <span>{{ activeModel }}</span>
        <span>{{ store.busy ? (store.phase || 'working…') : 'ready' }}</span>
      </div>
    </div>
  </section>
</template>
