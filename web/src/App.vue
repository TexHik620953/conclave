<script setup lang="ts">
import { onMounted } from 'vue'
import { state, loadConfig, loadHistory } from './store'
import { connectWS, closeWS } from './ws'
import RunForm from './components/RunForm.vue'
import RunView from './components/RunView.vue'
import RunHistory from './components/RunHistory.vue'
import RolesPanel from './components/RolesPanel.vue'

onMounted(async () => {
  await loadConfig()
  await loadHistory()
  connectWS()
})

function onStarted(id: string, pipeline: string, task: string) {
  state.currentRunId = id
  state.tab = 'run'
}
</script>

<template>
  <div class="app">
    <header>
      <div class="brand">conclave</div>
      <nav>
        <button :class="{ active: state.tab === 'run' }" @click="state.tab = 'run'">Run</button>
        <button :class="{ active: state.tab === 'history' }" @click="state.tab = 'history'; loadHistory()">History</button>
        <button :class="{ active: state.tab === 'roles' }" @click="state.tab = 'roles'">Roles</button>
      </nav>
      <div class="conn" :class="state.connected ? 'ok' : 'err'">
        {{ state.connected ? 'connected' : 'offline' }}
      </div>
    </header>

    <div class="layout">
      <aside>
        <RunForm :pipelines="state.config.pipelines" @started="onStarted" />
      </aside>

      <main>
        <RunView v-if="state.tab === 'run'" />
        <RunHistory v-else-if="state.tab === 'history'" />
        <RolesPanel v-else />
      </main>
    </div>
  </div>
</template>

<style scoped>
.app { display: flex; flex-direction: column; height: 100vh; }
header {
  display: flex; align-items: center; gap: 16px;
  padding: 10px 16px; border-bottom: 1px solid var(--border); background: var(--panel);
}
.brand { font-weight: 700; color: var(--accent); font-size: 16px; letter-spacing: 0.5px; }
nav { display: flex; gap: 6px; }
nav button.active { border-color: var(--accent); color: var(--accent); }
.conn { margin-left: auto; font-size: 12px; }
.layout { display: flex; flex: 1; min-height: 0; }
aside { width: 320px; border-right: 1px solid var(--border); overflow-y: auto; background: var(--panel); }
main { flex: 1; min-width: 0; overflow: hidden; }
</style>
