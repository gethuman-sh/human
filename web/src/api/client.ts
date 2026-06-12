import type {
  PendingConfirm,
  ProjectInfo,
  Snapshot,
  TrackerIssuesResult,
} from './types'

// All requests ride the HttpOnly auth cookie set by /auth.
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(path, { credentials: 'same-origin', ...init })
  if (!resp.ok) {
    let message = `${resp.status} ${resp.statusText}`
    try {
      const body = (await resp.json()) as { error?: string }
      if (body.error) message = body.error
    } catch {
      // non-JSON error body — keep the status text
    }
    throw new Error(message)
  }
  return (await resp.json()) as T
}

function post<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export const api = {
  snapshot: () => request<Snapshot>('/api/snapshot'),
  issues: () => request<TrackerIssuesResult[] | null>('/api/issues'),
  projects: () => request<ProjectInfo[] | null>('/api/projects'),
  logMode: () => request<{ mode: string }>('/api/log-mode'),
  setLogMode: (mode: string) =>
    request<{ mode: string }>('/api/log-mode', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode }),
    }),
  confirms: () => request<PendingConfirm[] | null>('/api/confirms'),
  resolveConfirm: (id: string, approved: boolean) =>
    post<{ approved: boolean }>(`/api/confirms/${encodeURIComponent(id)}`, {
      approved,
    }),
  createTicket: (ticket: {
    tracker_kind: string
    project: string
    title: string
    description?: string
  }) => post<{ key: string; tracker_kind: string }>('/api/tickets', ticket),
  dispatchAgent: (prompt: string, projectDir: string) =>
    post<{ name: string }>('/api/agents', {
      prompt,
      project_dir: projectDir,
    }),
  stopAgent: (name: string) =>
    post<{ name: string }>(`/api/agents/${encodeURIComponent(name)}/stop`, {}),
}
