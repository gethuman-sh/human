import type { NetworkEvent } from '../api/types'
import { formatElapsed } from '../lib/sparkline'

const MAX_ROWS = 8

function tagClass(e: NetworkEvent): string {
  if (e.source === 'oauth') return 'net-tag-oauth'
  if (e.source === 'fail') return 'net-tag-fail'
  switch (e.status) {
    case 'block':
      return 'net-tag-proxy-block'
    case 'intercept':
      return 'net-tag-proxy-intercept'
    default:
      return 'net-tag-proxy-forward'
  }
}

export function NetworkPanel({ events }: { events: NetworkEvent[] }) {
  if (events.length === 0) return null
  // Snapshot is oldest-first; show newest on top like the TUI.
  const rows = [...events].reverse().slice(0, MAX_ROWS)
  return (
    <div className="panel">
      <h2>Network</h2>
      {rows.map((e, i) => (
        <div className="net-row" key={i}>
          <span className={tagClass(e)}>[{e.source}]</span>
          <span>
            {e.host || '(no host)'}
            {e.count > 1 ? ` ×${e.count}` : ''}
          </span>
          <span>{formatElapsed(Date.now() - Date.parse(e.last_seen))} ago</span>
        </div>
      ))}
    </div>
  )
}
