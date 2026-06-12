import type { Snapshot } from '../api/types'

function windowLabel(snapshot: Snapshot | null): string {
  if (!snapshot?.usage_window) return ''
  const fmt = (iso: string) => {
    const d = new Date(iso)
    return `${String(d.getHours()).padStart(2, '0')}:00`
  }
  return `${fmt(snapshot.usage_window.start)} – ${fmt(snapshot.usage_window.end)}`
}

export function Header({ snapshot }: { snapshot: Snapshot | null }) {
  return (
    <div className="header">
      <span>
        <span className="title">human gui</span>{' '}
        {snapshot?.hostname && (
          <span className="hostname">{snapshot.hostname}</span>
        )}
      </span>
      <span className="window">{windowLabel(snapshot)}</span>
    </div>
  )
}
