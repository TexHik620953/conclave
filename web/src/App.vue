<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { state, loadConfig, loadHistory, loadSessions, newChat, currentRun } from './store'
import { connectWS } from './ws'
import RunView from './components/RunView.vue'
import RunHistory from './components/RunHistory.vue'
import RolesPanel from './components/RolesPanel.vue'
import SessionsList from './components/SessionsList.vue'
import Icon from './components/Icon.vue'

const sideOpen = ref(false)

onMounted(async () => {
  await loadConfig()
  await loadHistory()
  await loadSessions()
  connectWS()
})

const headerTitle = computed(() => {
  if (state.tab === 'history') return 'Run history'
  if (state.tab === 'roles') return 'Roles'
  const run = currentRun()
  const session = state.sessions.find((s) => s.id === state.currentSessionId)
  return session?.title || run?.pipeline || 'New chat'
})

function startNew() {
  newChat()
  sideOpen.value = false
}
</script>

<template>
  <div class="app">
    <aside class="sidebar" :class="{ open: sideOpen }">
      <div class="brand">
        <div class="logo">C</div>
        <div class="brand-text">
          <b>conclave</b>
          <span class="muted">multi-role agents</span>
        </div>
        <button class="ghost icon mobile-only" title="close" @click="sideOpen = false">
          <Icon name="close" />
        </button>
      </div>

      <button class="new-chat" @click="startNew">
        <Icon name="plus" :size="17" /> New chat
      </button>

      <SessionsList @select="sideOpen = false" />

      <div class="side-foot">
        <button class="ghost nav" :class="{ active: state.tab === 'roles' }" @click="state.tab = 'roles'">
          <Icon name="roles" :size="16" /> Roles
        </button>
        <button class="ghost nav" :class="{ active: state.tab === 'history' }" @click="state.tab = 'history'; loadHistory()">
          <Icon name="history" :size="16" /> History
        </button>
        <span class="conn" :class="state.connected ? 'ok' : 'err'">
          <span class="dot" :class="state.connected ? 'ok live' : 'err'" />
          {{ state.connected ? 'live' : 'offline' }}
        </span>
      </div>
    </aside>

    <div class="scrim" :class="{ show: sideOpen }" @click="sideOpen = false" />

    <main>
      <header class="topbar">
        <button class="ghost icon mobile-only" title="menu" @click="sideOpen = true">
          <Icon name="menu" />
        </button>
        <div class="title">{{ headerTitle }}</div>
      </header>

      <RunView v-if="state.tab === 'run'" />
      <RunHistory v-else-if="state.tab === 'history'" />
      <RolesPanel v-else />
    </main>
  </div>
</template>

<style scoped>
.app { display: flex; height: 100%; min-height: 0; }

.sidebar {
  width: var(--sidebar-w);
  flex: none;
  display: flex;
  flex-direction: column;
  background: linear-gradient(180deg, rgba(255, 255, 255, 0.02), transparent 40%), var(--panel);
  border-right: 1px solid var(--border);
  min-height: 0;
}
.brand { display: flex; align-items: center; gap: 11px; padding: 16px 16px 12px; }
.logo {
  width: 34px; height: 34px; border-radius: 10px; flex: none;
  display: grid; place-items: center; font-weight: 800; color: #0b0d13;
  background: linear-gradient(135deg, var(--accent), var(--accent-2));
  box-shadow: 0 4px 16px rgba(124, 140, 255, 0.35);
}
.brand-text { display: flex; flex-direction: column; line-height: 1.2; }
.brand-text b { font-size: 15px; letter-spacing: 0.02em; }
.brand-text span { font-size: 11px; }
.brand .mobile-only { margin-left: auto; }

.new-chat {
  margin: 4px 12px 12px;
  display: flex; align-items: center; justify-content: center; gap: 8px;
  padding: 10px;
  background: linear-gradient(135deg, rgba(124, 140, 255, 0.16), rgba(167, 139, 250, 0.12));
  border: 1px solid rgba(124, 140, 255, 0.35);
  color: var(--text); font-weight: 600;
}
.new-chat:hover { background: linear-gradient(135deg, rgba(124, 140, 255, 0.26), rgba(167, 139, 250, 0.2)); }

.side-foot {
  margin-top: auto; display: flex; align-items: center; gap: 4px;
  padding: 10px 12px; border-top: 1px solid var(--border);
}
.nav { padding: 7px 10px; font-size: 12.5px; }
.nav.active { color: var(--accent); background: var(--accent-soft); }
.conn { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; }
.conn.ok { color: var(--ok); }
.conn.err { color: var(--err); }

main { flex: 1; min-width: 0; display: flex; flex-direction: column; min-height: 0; }
.topbar {
  display: flex; align-items: center; gap: 10px;
  padding: 12px 20px; border-bottom: 1px solid var(--border);
  background: rgba(10, 12, 17, 0.6); backdrop-filter: blur(8px);
  min-height: 54px;
}
.topbar .title { font-weight: 600; font-size: 14.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.scrim { display: none; }
.mobile-only { display: none; }

@media (max-width: 880px) {
  .mobile-only { display: inline-flex; }
  .sidebar {
    position: fixed; inset: 0 auto 0 0; z-index: 40;
    transform: translateX(-100%); transition: transform 0.25s ease;
    box-shadow: var(--shadow);
  }
  .sidebar.open { transform: translateX(0); }
  .scrim { display: block; position: fixed; inset: 0; z-index: 30; background: rgba(0, 0, 0, 0.5); opacity: 0; pointer-events: none; transition: opacity 0.2s; }
  .scrim.show { opacity: 1; pointer-events: auto; }
}
</style>
