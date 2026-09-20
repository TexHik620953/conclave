<script setup lang="ts">
import { ref, computed } from 'vue'
import { api } from '../api'
import { startRun } from '../store'

const props = defineProps<{ pipelines: any[] }>()
const emit = defineEmits<{ (e: 'started', id: string, pipeline: string, task: string): void }>()

const pipeline = ref('')
const task = ref('')
const workspace = ref('')
const inputs = ref('')
const busy = ref(false)
const error = ref('')

const selected = computed(() => props.pipelines.find((p) => p.name === pipeline.value))

async function run() {
  error.value = ''
  if (!pipeline.value) {
    pipeline.value = props.pipelines[0]?.name || ''
  }
  if (!pipeline.value) {
    error.value = 'no pipeline selected'
    return
  }
  if (!task.value.trim()) {
    error.value = 'task is required'
    return
  }
  const inputMap: Record<string, string> = {}
  for (const line of inputs.value.split('\n')) {
    const [k, ...rest] = line.split('=')
    if (k && rest.length) inputMap[k.trim()] = rest.join('=').trim()
  }
  busy.value = true
  try {
    const res: any = await api.createRun({
      pipeline: pipeline.value,
      task: task.value,
      workspace: workspace.value,
      inputs: inputMap,
    })
    startRun(res.run_id, pipeline.value, task.value)
    emit('started', res.run_id, pipeline.value, task.value)
  } catch (e: any) {
    error.value = String(e.message || e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="form">
    <h3>New run</h3>
    <label>Pipeline</label>
    <select v-model="pipeline">
      <option value="" disabled>select a pipeline</option>
      <option v-for="p in props.pipelines" :key="p.name" :value="p.name">{{ p.name }}</option>
    </select>
    <p v-if="selected?.description" class="muted desc">{{ selected.description }}</p>

    <label>Task</label>
    <textarea v-model="task" rows="5" placeholder="what should the pipeline do?" />

    <label>Workspace <span class="muted">(optional)</span></label>
    <input v-model="workspace" placeholder="path or leave empty" />

    <label>Inputs <span class="muted">(key=value per line, optional)</span></label>
    <textarea v-model="inputs" rows="2" placeholder="e.g. language=go" />

    <button class="primary" :disabled="busy" @click="run">{{ busy ? 'starting…' : 'Run' }}</button>
    <p v-if="error" class="err">{{ error }}</p>
  </div>
</template>

<style scoped>
.form { padding: 16px; display: flex; flex-direction: column; gap: 6px; }
h3 { margin: 0 0 8px; color: var(--accent); }
label { font-size: 12px; color: var(--muted); margin-top: 6px; }
.desc { font-size: 12px; margin: 2px 0 4px; }
button { margin-top: 12px; }
</style>
