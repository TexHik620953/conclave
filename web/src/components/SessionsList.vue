<script setup lang="ts">
import { computed, ref } from 'vue'
import { state, openSession, deleteSession } from '../store'
import Icon from './Icon.vue'

const emit = defineEmits<{ (e: 'select'): void }>()
const query = ref('')

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return state.sessions
  return state.sessions.filter(
    (s) => (s.title || '').toLowerCase().includes(q) || (s.pipeline || '').toLowerCase().includes(q),
  )
})

function select(id: string) {
  openSession(id)
  emit('select')
}
async function remove(id: string, ev: Event) {
  ev.stopPropagation()
  await deleteSession(id)
}
function when(ts: string) {
  if (!ts) return ''
  const d = new Date(ts)
  const diff = (Date.now() - d.getTime()) / 1000
  if (diff < 60) return 'now'
  if (diff < 3600) return `${Math.floor(diff / 60)}m`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`
  return d.toLocaleDateString()
}
</script>

<template>
  <div class="sessions">
    <div class="search">
      <Icon name="search" :size="14" />
      <input v-model="query" placeholder="Search chats" />
    </div>

    <div class="list">
      <div v-if="!filtered.length" class="muted empty">
        {{ state.sessions.length ? 'No matches.' : 'No chats yet. Start one above.' }}
      </div>
      <button
        v-for="s in filtered"
        :key="s.id"
        class="item"
        :class="{ active: s.id === state.currentSessionId }"
        @click="select(s.id)"
      >
        <span class="glyph"><Icon name="chat" :size="15" /></span>
        <span class="text">
          <span class="title">{{ s.title || 'Untitled chat' }}</span>
          <span class="sub muted">{{ s.pipeline || 'auto' }}</span>
        </span>
        <span class="when muted-2">{{ when(s.updated_at) }}</span>
        <span class="del" title="delete" @click="remove(s.id, $event)"><Icon name="trash" :size="13" /></span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.sessions { flex: 1; min-height: 0; display: flex; flex-direction: column; padding: 0 10px; }
.search { position: relative; display: flex; align-items: center; padding: 0 4px 8px; }
.search :deep(svg) { position: absolute; left: 14px; color: var(--muted-2); pointer-events: none; }
.search input { padding-left: 32px; background: var(--panel-2); }

.list { overflow-y: auto; display: flex; flex-direction: column; gap: 2px; padding-bottom: 8px; }
.empty { padding: 16px 8px; font-size: 12.5px; }

.item {
  display: flex; align-items: center; gap: 10px; width: 100%; text-align: left;
  background: transparent; border: 1px solid transparent; border-radius: 10px;
  padding: 9px 10px; color: var(--text);
}
.item:hover { background: var(--panel-2); border-color: var(--border); }
.item.active { background: var(--accent-soft); border-color: rgba(124, 140, 255, 0.35); }
.glyph { color: var(--muted); flex: none; }
.item.active .glyph { color: var(--accent); }
.text { display: flex; flex-direction: column; min-width: 0; flex: 1; line-height: 1.25; }
.title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; font-weight: 500; }
.sub { font-size: 11px; }
.when { font-size: 10.5px; flex: none; }
.del { display: none; color: var(--muted); flex: none; padding: 2px; border-radius: 6px; }
.item:hover .del { display: inline-flex; }
.del:hover { color: var(--err); background: rgba(255, 107, 107, 0.12); }
</style>
