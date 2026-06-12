import { describe, expect, it } from 'vitest'
import {
  byHourToValues,
  formatElapsed,
  formatTokens,
  sparkline,
} from './sparkline'

describe('sparkline', () => {
  it('returns empty for no data', () => {
    expect(sparkline([], 10)).toBe('')
    expect(sparkline([1, 2], 0)).toBe('')
  })

  it('scales to the block range', () => {
    const line = sparkline([0, 7], 2)
    expect(line).toHaveLength(2)
    expect(line[0]).toBe('▁')
    expect(line[1]).toBe('█')
  })

  it('renders flat zero series as the lowest block', () => {
    expect(sparkline([0, 0, 0], 3)).toBe('▁▁▁')
  })

  it('downsamples by averaging when values exceed width', () => {
    const line = sparkline([0, 0, 8, 8], 2)
    expect(line).toHaveLength(2)
    expect(line[0]).toBe('▁')
    expect(line[1]).toBe('█')
  })
})

describe('byHourToValues', () => {
  it('expands sparse buckets into a dense hourly series', () => {
    const values = byHourToValues(
      [
        { bucket: '2026-06-12 10:00', count: 5 },
        { bucket: '2026-06-12 12:00', count: 2 },
      ],
      '2026-06-12T10:00:00Z',
      '2026-06-12T12:59:00Z',
    )
    expect(values).toEqual([5, 0, 2])
  })

  it('caps the window at one week', () => {
    const values = byHourToValues([], '2026-01-01T00:00:00Z', '2026-03-01T00:00:00Z')
    expect(values).toHaveLength(168)
  })

  it('returns at least one slot for inverted windows', () => {
    const values = byHourToValues([], '2026-06-12T12:00:00Z', '2026-06-12T10:00:00Z')
    expect(values).toHaveLength(1)
  })
})

describe('formatTokens', () => {
  it('mirrors claude.FormatTokens', () => {
    expect(formatTokens(950)).toBe('950')
    expect(formatTokens(1500)).toBe('1.5K')
    expect(formatTokens(2_300_000)).toBe('2.3M')
  })
})

describe('formatElapsed', () => {
  it('mirrors the TUI formatting', () => {
    expect(formatElapsed(12_000)).toBe('12s')
    expect(formatElapsed(185_000)).toBe('3m 5s')
    expect(formatElapsed(2 * 3600_000 + 4 * 60_000)).toBe('2h 4m')
  })
})
