import { api } from './api'
import { applyEvent, state } from './store'

let socket: WebSocket | null = null
let timer: number | undefined

export function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const q = api.token ? `?token=${encodeURIComponent(api.token)}` : ''
  socket = new WebSocket(`${proto}://${location.host}/api/ws${q}`)

  socket.onopen = () => {
    state.connected = true
    socket?.send(JSON.stringify({ type: 'subscribe', run_id: '' }))
  }
  socket.onclose = () => {
    state.connected = false
    timer = window.setTimeout(connectWS, 1500)
  }
  socket.onerror = () => socket?.close()
  socket.onmessage = (e) => {
    try {
      applyEvent(JSON.parse(e.data))
    } catch {
      /* ignore malformed frames */
    }
  }
}

export function closeWS() {
  if (timer) window.clearTimeout(timer)
  socket?.close()
}
