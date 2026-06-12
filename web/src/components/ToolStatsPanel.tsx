import type { ToolStats } from '../api/types'
import { byHourToValues, sparkline } from '../lib/sparkline'

const MAX_TOOLS = 8
const SPARK_WIDTH = 72

export function ToolStatsPanel({ stats }: { stats?: ToolStats }) {
  if (!stats || stats.total_events === 0) return null

  const byTool = (stats.by_tool ?? []).slice(0, MAX_TOOLS)
  const maxCount = Math.max(...byTool.map((t) => t.count), 1)

  let successes = 0
  let failures = 0
  for (const enc of stats.by_event_name ?? []) {
    if (enc.event_name === 'PostToolUse') successes += enc.count
    if (enc.event_name === 'PostToolUseFailure') failures += enc.count
  }
  const outcomesTotal = successes + failures

  const spark = stats.by_hour?.length
    ? sparkline(byHourToValues(stats.by_hour, stats.since, stats.until), SPARK_WIDTH)
    : ''

  return (
    <div className="panel">
      <h2>
        Tools (24h)
        <span className="meta">{stats.total_events} events</span>
      </h2>
      {spark && <div className="spark">{spark}</div>}
      {byTool.map((tc) => (
        <div className="tool-row" key={tc.tool_name}>
          <span className="name">{tc.tool_name}</span>
          <span className="track">
            <span
              className="fill"
              style={{ width: `${Math.max((tc.count / maxCount) * 100, 2)}%` }}
            />
          </span>
          <span className="count">
            {tc.count} ({Math.floor((tc.count * 100) / stats.total_events)}%)
          </span>
        </div>
      ))}
      {outcomesTotal > 0 && (
        <div className="outcomes">
          Outcomes: {successes} ok, {failures} failed (
          {((failures * 100) / outcomesTotal).toFixed(1)}% error rate)
        </div>
      )}
    </div>
  )
}
