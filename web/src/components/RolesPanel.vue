<script setup lang="ts">
import { state } from '../store'
</script>

<template>
  <div class="roles">
    <div class="col-title">Roles</div>
    <div v-for="r in state.config.roles" :key="r.id" class="card">
      <div class="head">
        <span class="name">{{ r.id }}</span>
        <span class="muted">{{ r.title }}</span>
        <span class="model mono">{{ r.model }}</span>
      </div>
      <div class="meta">
        <span v-if="r.tools?.length"><b>tools:</b> {{ r.tools.join(', ') }}</span>
        <span><b>perms:</b>
          {{ Object.entries(r.permissions).filter(([, v]) => v).map(([k]) => k).join(', ') || 'none' }}
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.roles { padding: 16px; overflow-y: auto; height: 100%; }
.col-title { font-size: 11px; text-transform: uppercase; color: var(--muted); margin-bottom: 8px; }
.card { border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; margin-bottom: 8px; background: var(--panel); }
.head { display: flex; gap: 10px; align-items: baseline; }
.name { font-weight: 600; color: var(--accent); }
.model { margin-left: auto; font-size: 12px; color: var(--muted); }
.meta { margin-top: 6px; display: flex; flex-direction: column; gap: 2px; font-size: 12.5px; color: var(--muted); }
</style>
