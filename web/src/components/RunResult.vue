<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import type { RunState } from '../store'
import { api } from '../api'
import MarkdownView from './MarkdownView.vue'

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

watch(() => props.run.id, () => { pickDefault(); load() })
watch(names, () => { if (!selected.value) pickDefault(); load() }, { deep: true })
watch(selected, load)
watch(() => props.run.finalText, () => { if (!selected.value) content.value = props.run.finalText })
onMounted(() => { pickDefault(); load() })
</script>

<template>
  <div class="result">
    <div class="head">
      <span class="muted">result</span>
      <select v-model="selected">
        <option value="">last message</option>
        <option v-for="n in names" :key="n" :value="n">{{ n }}</option>
      </select>
      <button @click="load">reload</button>
    </div>
    <div class="body">
      <div v-if="loading" class="muted">loading…</div>
      <MarkdownView v-else-if="content" :text="content" />
      <div v-else class="muted">No result yet.</div>
    </div>
  </div>
</template>

<style scoped>
.result { display: flex; flex-direction: column; height: 100%; }
.head { display: flex; align-items: center; gap: 10px; padding: 8px 16px; border-bottom: 1px solid var(--border); }
.head select { width: auto; min-width: 180px; }
.body { flex: 1; overflow-y: auto; padding: 16px; }
</style>
