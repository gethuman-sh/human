// Ports of the TUI sparkline helpers (cmd/cmdtui/tui.go).

import type { TimeBucket } from '../api/types'

const BLOCKS = ['▁', '▂', '▃', '▄', '▅', '▆', '▇', '█']

// sparkline renders values as unicode block characters, down-sampling
// by averaging when there are more values than width.
export function sparkline(values: number[], width: number): string {
  if (values.length === 0 || width < 1) return ''

  let display = values
  if (values.length > width) {
    display = new Array<number>(width).fill(0)
    for (let i = 0; i < width; i++) {
      const start = Math.floor((i * values.length) / width)
      const end = Math.min(
        Math.floor(((i + 1) * values.length) / width),
        values.length,
      )
      const count = end - start
      if (count > 0) {
        let sum = 0
        for (let j = start; j < end; j++) sum += values[j]
        display[i] = Math.floor(sum / count)
      }
    }
  }

  const maxVal = Math.max(...display, 0)
  return display
    .map((v) => {
      if (maxVal === 0) return BLOCKS[0]
      const idx = Math.min(
        Math.floor((v * (BLOCKS.length - 1)) / maxVal),
        BLOCKS.length - 1,
      )
      return BLOCKS[idx]
    })
    .join('')
}

// hourLabel formats a Date as the daemon's UTC bucket label
// "2006-01-02 15:00".
function hourLabel(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:00`
}

// byHourToValues expands sparse TimeBucket data into a dense hourly
// series across [since, until]; missing hours are zero.
export function byHourToValues(
  buckets: TimeBucket[],
  since: string,
  until: string,
): number[] {
  const hourMs = 3600_000
  const sinceHour = Math.floor(Date.parse(since) / hourMs) * hourMs
  const untilHour = Math.floor(Date.parse(until) / hourMs) * hourMs
  let hours = Math.floor((untilHour - sinceHour) / hourMs) + 1
  if (!Number.isFinite(hours) || hours < 1) hours = 1
  if (hours > 168) hours = 168 // safety cap at one week

  const lookup = new Map(buckets.map((b) => [b.bucket, b.count]))
  const values: number[] = []
  for (let i = 0; i < hours; i++) {
    const label = hourLabel(new Date(sinceHour + i * hourMs))
    values.push(lookup.get(label) ?? 0)
  }
  return values
}

// formatTokens mirrors claude.FormatTokens' compact formatting.
export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

// formatElapsed mirrors the TUI's formatElapsed ("12s", "3m 5s", "2h 4m").
export function formatElapsed(ms: number): string {
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  return `${Math.floor(m / 60)}h ${m % 60}m`
}
