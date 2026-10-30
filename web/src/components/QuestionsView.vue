<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { api } from '../api'
import { store, toast } from '../store'

const sets = ref([])
const loading = ref(false)
const open = ref('')
const answers = reactive({})
const deleteArmed = ref('')

const columns = [
  { key: 'needs_answer', label: 'Needs answer', hint: 'waiting for you' },
  { key: 'master_review', label: 'Master review', hint: 'AI is reviewing' },
  { key: 'answered_me', label: 'Answered by me', hint: 'resolved' },
  { key: 'answered_ai', label: 'Answered by AI', hint: 'resolved automatically' },
]

onMounted(async () => { await load(); focusRequested() })
watch(() => store.counters.questions, load)
watch(() => store.openQuestionId, focusRequested)
function focusRequested() {
  const id = store.openQuestionId
  if (!id) return
  store.openQuestionId = ''
  if (!sets.value.some(s => s.id === id)) return
  if (open.value !== id) toggleSet(id)
}

async function load() {
  loading.value = true
  try { sets.value = (await api.questions(store.workspace.id)).questions || [] }
  catch (e) { toast(e.message, 'error') }
  finally { loading.value = false }
}

function answeredByAI(set) {
  const by = String(set.answered_by || '').toLowerCase()
  return !!by && by !== 'human' && by !== 'user'
}
function answeredByMe(set) {
  return set.status === 'answered' && !answeredByAI(set)
}
function columnOf(set) {
  if (set.status === 'answered') return answeredByAI(set) ? 'answered_ai' : 'answered_me'
  if (set.status === 'pending_master') return 'master_review'
  return 'needs_answer'
}
function setsFor(key) {
  return sets.value.filter(set => columnOf(set) === key)
}
function answeredLabel(set) {
  if (!set.status || set.status !== 'answered') return statusLabel(set.status)
  return answeredByAI(set) ? 'Answered by AI' : 'Answered by me'
}
function toggleSet(id) {
  open.value = open.value === id ? '' : id
  if (open.value === id) {
    const s = sets.value.find(x => x.id === id)
    for (const q of s?.questions || []) {
      if (!answers[q.id]) answers[q.id] = { selected: [], custom_text: '' }
    }
  }
}
function choose(q, opt, checked) {
  const a = answers[q.id] || (answers[q.id] = { selected: [], custom_text: '' })
  if (q.type === 'multiple') {
    a.selected = checked ? [...a.selected, opt.id] : a.selected.filter(x => x !== opt.id)
  } else {
    a.selected = [opt.id]
  }
}
async function removeSet(set) {
  if (deleteArmed.value !== set.id) {
    deleteArmed.value = set.id
    setTimeout(() => { deleteArmed.value = '' }, 4000)
    return
  }
  deleteArmed.value = ''
  try { await api.deleteQuestion(store.workspace.id, set.id); await load(); toast('question deleted') }
  catch (e) { toast(e.message, 'error') }
}
async function submit(set) {
  const payload = {}
  for (const q of set.questions) {
    const a = answers[q.id] || { selected: [], custom_text: '' }
    payload[q.id] = { id: q.id, selected: a.selected || [], custom_text: a.custom_text || '' }
  }
  try {
    await api.answerQuestions(store.workspace.id, set.id, payload)
    toast('answers submitted')
    open.value = ''
    await load()
  } catch (e) { toast(e.message, 'error') }
}
function answerText(q) {
  const selected = (q.options || []).filter(opt => (q.selected || []).includes(opt.id)).map(opt => opt.label)
  if (q.custom_text) selected.push(q.custom_text)
  return selected.filter(Boolean).join(', ') || 'No answer'
}
function statusLabel(s) {
  return ({ pending_human: 'Needs human', pending_answer: 'Needs answer', pending_master: 'Master review', answered: 'Resolved' })[s] || s
}
function dateLabel(value) {
  return value ? new Date(value).toLocaleString() : ''
}
const total = computed(() => sets.value.length)
</script>

<template>
  <section class="questions-shell">
    <header class="page-head">
      <div><h2>Questions</h2><p>Questions from interactive and autonomous agents · {{ total }} total</p></div>
      <button class="btn small ghost" :disabled="loading" @click="load">Refresh</button>
    </header>

    <div class="questions-board">
      <section v-for="col in columns" :key="col.key" class="question-column">
        <header class="question-column-head">
          <span>{{ col.label }}</span>
          <small>{{ col.hint }}</small>
          <em>{{ setsFor(col.key).length }}</em>
        </header>
        <div class="question-column-body">
          <article v-for="s in setsFor(col.key)" :key="s.id" class="question-card" :class="{ open: open === s.id }">
            <div class="question-head" role="button" tabindex="0" @click="toggleSet(s.id)" @keydown.enter="toggleSet(s.id)">
              <span class="question-status" :class="s.status">{{ answeredLabel(s) }}</span>
              <span class="question-title"><b>{{ s.title }}</b><small>{{ s.id }} · {{ s.category }}<template v-if="s.task_id"> · task {{ s.task_id }}</template></small></span>
              <span class="question-chev">{{ open === s.id ? '▾' : '▸' }}</span>
              <button class="question-delete" @click.stop="removeSet(s)">{{ deleteArmed === s.id ? 'Confirm delete' : 'Delete' }}</button>
            </div>
            <div v-if="open === s.id" class="question-body">
              <div v-if="s.task_context" class="question-context"><span>Task context</span><pre>{{ s.task_context }}</pre></div>
              <div v-if="s.agent_context" class="question-context"><span>Agent context</span><pre>{{ s.agent_context }}</pre></div>

              <div v-if="s.status === 'answered'" class="question-resolved">
                <div v-for="q in s.questions" :key="q.id" class="question-resolved-item">
                  <p>{{ q.text }}</p>
                  <b>{{ answerText(q) }}</b>
                </div>
                <small v-if="s.answered_at">Answered {{ dateLabel(s.answered_at) }}</small>
              </div>

              <form v-else @submit.prevent="submit(s)">
                <div v-for="q in s.questions" :key="q.id" class="question-item">
                  <p>{{ q.text }} <em v-if="q.required">*</em></p>
                  <div v-if="q.options?.length" class="question-options">
                    <label v-for="opt in q.options" :key="opt.id">
                      <input :type="q.type === 'multiple' ? 'checkbox' : 'radio'" :name="q.id" :checked="(answers[q.id]?.selected || []).includes(opt.id)" @change="choose(q, opt, $event.target.checked)" />
                      <span><b>{{ opt.label }}</b><small v-if="opt.description">{{ opt.description }}</small></span>
                      <em v-if="opt.recommended">recommended</em>
                    </label>
                  </div>
                  <input v-if="q.allow_custom" v-model="answers[q.id].custom_text" class="custom-answer" placeholder="Your answer…" />
                </div>
                <div class="question-submit"><button class="btn primary" type="submit">Submit answers</button></div>
              </form>
            </div>
          </article>
          <div v-if="!setsFor(col.key).length" class="question-column-empty">Nothing here</div>
        </div>
      </section>
    </div>
  </section>
</template>
