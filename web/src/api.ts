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
  ask: (body: unknown) => req('/api/ask', { method: 'POST', body: JSON.stringify(body) }),
}
