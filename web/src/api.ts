const token = new URLSearchParams(location.search).get('token') || ''
const headers: Record<string, string> = { 'Content-Type': 'application/json' }
if (token) headers['Authorization'] = `Bearer ${token}`

async function req(path: string, init?: RequestInit) {
  const res = await fetch(path, { ...init, headers: { ...headers, ...(init?.headers || {}) } })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || res.statusText)
  }
  const ct = res.headers.get('Content-Type') || ''
  return ct.includes('application/json') ? res.json() : res.text()
}

export const api = {
  token,
  config: () => req('/api/config'),
  listRuns: () => req('/api/runs?limit=50'),
  getRun: (id: string) => req(`/api/runs/${id}`),
  artifact: (id: string, name: string) => req(`/api/runs/${id}/artifacts/${encodeURIComponent(name)}`),
  createRun: (body: unknown) => req('/api/runs', { method: 'POST', body: JSON.stringify(body) }),
  cancelRun: (id: string) => req(`/api/runs/${id}/cancel`, { method: 'POST' }),
  pauseRun: (id: string) => req(`/api/runs/${id}/pause`, { method: 'POST' }),
  resumeRun: (id: string) => req(`/api/runs/${id}/resume`, { method: 'POST' }),
  revertRun: (id: string, nodeId: string) =>
    req(`/api/runs/${id}/revert`, { method: 'POST', body: JSON.stringify({ node_id: nodeId }) }),
  followup: (id: string, prompt: string, nodeId = '') =>
    req(`/api/runs/${id}/followup`, { method: 'POST', body: JSON.stringify({ prompt, node_id: nodeId }) }),
  answer: (id: string, questionId: string, selected: string[], custom: string) =>
    req(`/api/runs/${id}/answer`, { method: 'POST', body: JSON.stringify({ id: questionId, selected, custom }) }),
  ask: (body: unknown) => req('/api/ask', { method: 'POST', body: JSON.stringify(body) }),

  listSessions: () => req('/api/sessions?limit=100'),
  createSession: (body: unknown) => req('/api/sessions', { method: 'POST', body: JSON.stringify(body) }),
  getSession: (id: string) => req(`/api/sessions/${id}`),
  deleteSession: (id: string) => req(`/api/sessions/${id}`, { method: 'DELETE' }),
  listMessages: (id: string) => req(`/api/sessions/${id}/messages`),
  postMessage: (id: string, body: unknown) =>
    req(`/api/sessions/${id}/messages`, { method: 'POST', body: JSON.stringify(body) }),
}
