import type { TrackerStatus } from '../api/types'

export function TrackersLine({ trackers }: { trackers: TrackerStatus[] }) {
  if (trackers.length === 0) return null
  return (
    <div className="panel">
      <h2>Trackers</h2>
      <div className="trackers">
        {trackers.map((t) => (
          <span
            key={t.kind + t.name}
            className={`tracker ${t.working ? 'ok' : 'broken'}`}
            title={
              t.working
                ? `${t.label} (${t.name})`
                : `missing: ${(t.missing ?? []).join(', ') || 'credentials'}`
            }
          >
            {t.working ? '●' : '○'} {t.label} ({t.name})
          </span>
        ))}
      </div>
    </div>
  )
}
