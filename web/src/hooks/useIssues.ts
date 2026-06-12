import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import type { TrackerIssuesResult } from '../api/types'

// Matches the TUI's issueTickCmd cadence.
const ISSUE_POLL_MS = 30_000

export interface IssuesFeed {
  issues: TrackerIssuesResult[]
  fetchedAt: Date | null
  refresh: () => void
}

// useIssues polls the tracker pipeline on the TUI's 30s cadence and
// exposes refresh() for after-write refetches (ticket creation).
export function useIssues(): IssuesFeed {
  const [issues, setIssues] = useState<TrackerIssuesResult[]>([])
  const [fetchedAt, setFetchedAt] = useState<Date | null>(null)
  const inFlight = useRef(false)

  const refresh = useCallback(() => {
    if (inFlight.current) return
    inFlight.current = true
    api
      .issues()
      .then((results) => {
        setIssues(results ?? [])
        setFetchedAt(new Date())
      })
      .catch(() => {
        // tracker fetch failed — keep showing the previous pipeline
      })
      .finally(() => {
        inFlight.current = false
      })
  }, [])

  useEffect(() => {
    refresh()
    const timer = window.setInterval(refresh, ISSUE_POLL_MS)
    return () => clearInterval(timer)
  }, [refresh])

  return { issues, fetchedAt, refresh }
}
