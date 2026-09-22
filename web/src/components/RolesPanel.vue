<script setup lang="ts">
import { state } from '../store'

function hue(role: string) {
  let h = 0
  for (const ch of role) h = (h * 31 + ch.charCodeAt(0)) % 360
  return h
}
function color(role: string) {
  return `hsl(${hue(role)}, 68%, 66%)`
}
function initials(id: string) {
  const parts = id.split(/[_\s-]+/).filter(Boolean)
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
  return id.slice(0, 2).toUpperCase()
}
function perms(r: any) {
  return Object.entries(r.permissions).filter(([, v]) => v).map(([k]) => k)
}
</script>

<template>
  <div class="roles">
    <div class="grid">
      <div v-for="r in state.config.roles" :key="r.id" class="card enter">
        <div class="head">
          <span class="avatar" :style="{ background: color(r.id) }">{{ initials(r.id) }}</span>
          <div class="who">
            <b>{{ r.id }}</b>
            <span class="muted">{{ r.title }}</span>
          </div>
          <span class="model mono muted-2">{{ r.model }}</span>
        </div>
        <div v-if="r.description" class="desc muted">{{ r.description }}</div>
        <div class="tags">
          <span v-for="p in perms(r)" :key="p" class="pill accent">{{ p }}</span>
          <span v-if="!perms(r).length" class="pill">no permissions</span>
        </div>
        <div v-if="r.tools?.length" class="tools">
          <span v-for="t in r.tools" :key="t" class="chip">{{ t }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.roles { flex: 1; overflow-y: auto; padding: 20px; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 14px; }
.card { border: 1px solid var(--border); border-radius: var(--radius); padding: 14px; background: var(--panel); box-shadow: var(--shadow-sm); }
.head { display: flex; align-items: center; gap: 10px; }
.avatar { width: 34px; height: 34px; border-radius: 10px; display: grid; place-items: center; font-size: 12px; font-weight: 700; color: #0b0d13; flex: none; }
.who { display: flex; flex-direction: column; line-height: 1.25; min-width: 0; }
.who b { font-size: 14px; }
.who span { font-size: 11.5px; }
.model { margin-left: auto; font-size: 11px; text-align: right; overflow: hidden; text-overflow: ellipsis; max-width: 45%; }
.desc { font-size: 12.5px; margin: 10px 0; line-height: 1.5; }
.tags { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 10px; }
.tools { display: flex; flex-wrap: wrap; gap: 5px; }
</style>
