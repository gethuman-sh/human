// Ports of the TUI pipeline logic (cmd/cmdtui/tui.go) so both surfaces
// label issues identically.

import type { Category, Issue, TrackerIssuesResult } from '../api/types'

export interface FlatIssue {
  trackerKind: string
  trackerRole: string
  project: string
  issue: Issue
  readyForReview: boolean
  prUrl?: string
}

// flattenIssues mirrors the TUI's flattenIssues: the keyboard cursor
// walks this flat list in render order, skipping errored groups.
export function flattenIssues(groups: TrackerIssuesResult[]): FlatIssue[] {
  const out: FlatIssue[] = []
  for (const g of groups) {
    if (g.error) continue
    for (const issue of g.issues ?? []) {
      out.push({
        trackerKind: g.tracker_kind,
        trackerRole: g.tracker_role ?? '',
        project: g.project,
        issue,
        readyForReview: g.ready_for_review?.includes(issue.key) ?? false,
        prUrl: g.ready_for_review_prs?.[issue.key],
      })
    }
  }
  return out
}

// inferRole mirrors the TUI fallback when no explicit role is configured.
export function inferRole(trackerKind: string): string {
  switch (trackerKind) {
    case 'shortcut':
      return 'pm'
    case 'linear':
      return 'engineering'
    default:
      return ''
  }
}

export function effectiveRole(trackerKind: string, trackerRole?: string): string {
  return trackerRole || inferRole(trackerKind)
}

// pipelineStage maps a tracker role and status type to the pipeline
// stage label: PM = Ready for Plan -> Planning -> Planned,
// Eng = Backlog -> In Dev -> Done -> Closed.
export function pipelineStage(
  trackerKind: string,
  trackerRole: string | undefined,
  statusName: string,
  statusType: Category | undefined,
): string {
  switch (effectiveRole(trackerKind, trackerRole)) {
    case 'pm':
      switch (statusType) {
        case 'unstarted':
          return 'Ready for Plan'
        case 'started':
          return 'Planning'
        case 'done':
          return 'Planned'
        default:
          return statusName
      }
    case 'engineering':
      switch (statusType) {
        case 'unstarted':
          return 'Backlog'
        case 'started':
          return 'In Dev'
        case 'done':
          return 'Done'
        case 'closed':
          return 'Closed'
        default:
          return statusName
      }
    default:
      return statusName
  }
}

// stageClass mirrors pipelineStageStyle: started = warning (yellow),
// done = special (teal), everything else subtle.
export function stageClass(statusType: Category | undefined): string {
  switch (statusType) {
    case 'started':
      return 'stage-started'
    case 'done':
      return 'stage-done'
    default:
      return 'stage-subtle'
  }
}

// isBug ports tracker.Issue.IsBug: any '/'- or ':'-separated segment of
// the type or a label equal to "bug", case-insensitive.
export function isBug(issue: Issue): boolean {
  const hasBugSegment = (value: string): boolean =>
    value
      .split(/[/:]/)
      .some((segment) => segment.trim().toLowerCase() === 'bug')
  if (hasBugSegment(issue.type)) return true
  return (issue.labels ?? []).some(hasBugSegment)
}

// promptForIssue mirrors agent.PromptForIssue: bugs get root-cause
// analysis, PM tickets get planning, engineering tickets get execution.
export function promptForIssue(flat: FlatIssue): string {
  if (isBug(flat.issue)) return `/human-bug-plan ${flat.issue.key}`
  if (flat.trackerKind === 'shortcut') return `/human-plan ${flat.issue.key}`
  return `/human-execute ${flat.issue.key}`
}

// pathMatches mirrors the TUI's project-tab path matching: exact dir or
// a proper descendant (boundary-aware, so /home/alice-project is not
// under /home/alice).
export function pathMatches(cwd: string | undefined, dir: string): boolean {
  if (!dir || !cwd) return false
  return cwd === dir || cwd.startsWith(dir + '/')
}
