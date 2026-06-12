import type { Snapshot } from '../api/types'

export function StatusLine({ snapshot }: { snapshot: Snapshot | null }) {
  if (!snapshot) {
    return (
      <div className="statusline">
        <span>● Loading…</span>
      </div>
    )
  }
  const d = snapshot.daemon
  const proxy =
    d.proxy_active_conns > 0
      ? `proxy: ${d.proxy_active_conns} active`
      : 'proxy: idle'
  return (
    <div className="statusline">
      <span>
        <span className={d.alive ? 'ok' : 'bad'}>
          ● Daemon {d.alive ? `running (PID ${d.pid})` : 'stopped'}
        </span>
        {d.alive && <span> · {proxy}</span>}
      </span>
      <span>
        {[snapshot.telegram, snapshot.slack].filter(Boolean).join(' · ')}
      </span>
    </div>
  )
}
