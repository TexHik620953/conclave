<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import type { RunState } from '../store'
import { api } from '../api'
import MarkdownView from './MarkdownView.vue'
import Icon from './Icon.vue'

const props = defineProps<{ run: RunState }>()
const selected = ref('')
const content = ref('')
const loading = ref(false)

const preferred = ['brief.md', 'summary.md', 'final.md', 'result.md', 'review-summary.md', 'implementation.md']
const names = computed(() => props.run.artifacts.map((a) => a.name))

async function load() {
  if (!selected.value) {
    content.value = props.run.finalText || ''
    return
  }
  loading.value = true
  try {
    content.value = (await api.artifact(props.run.id, selected.value)) as string
  } catch (e: any) {
    content.value = 'error: ' + (e?.message || e)
  } finally {
    loading.value = false
  }
}

function pickDefault() {
  const list = names.value
  if (!list.length) {
    selected.value = ''
    return
  }
  const pref = preferred.find((p) => list.includes(p))
  selected.value = pref || list[list.length - 1]
}

function download() {
  const text = content.value || ''
  if (!text) return
  const name = selected.value || `${props.run.id}-result.md`
  const blob = new Blob([text], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

watch(() => props.run.id, () => { pickDefault(); load() })
watch(names, () => { if (!selected.value) pickDefault(); load() }, { deep: true })
watch(selected, load)
watch(() => props.run.finalText, () => { if (!selected.value) content.value = props.run.finalText })
onMounted(() => { pickDefault(); load() })
</script>

<template>
  <div class="result">
    <div class="head">
      <span class="muted">Result</span>
      <select v-model="selected">
        <option value="">last message</option>
        <option v-for="n in names" :key="n" :value="n">{{ n }}</option>
      </select>
      <div class="spacer" />
      <button class="ghost icon" title="reload" @click="load"><Icon name="refresh" :size="15" /></button>
      <button class="ghost" :disabled="!content" @click="download"><Icon name="download" :size="15" /> .md</button>
    </div>
    <div class="body">
      <div v-if="loading" class="muted">loading…</div>
      <MarkdownView v-else-if="content" :text="content" />
      <div v-else class="muted empty">No result yet.</div>
    </div>
  </div>
</template>

<style scoped>
.result { flex: 1; display: flex; flex-direction: column; min-height: 0; }
.head { display: flex; align-items: center; gap: 10px; padding: 10px 20px; border-bottom: 1px solid var(--border); background: var(--bg-2); }
.head select { width: auto; min-width: 190px; }
.body { flex: 1; overflow-y: auto; padding: 22px; }
.empty { text-align: center; padding: 40px; }
</style>
