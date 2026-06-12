import { useMemo } from 'react'
import type { TrackerIssuesResult } from '../api/types'
import {
  effectiveRole,
  flattenIssues,
  isBug,
  pipelineStage,
  stageClass,
} from '../lib/issues'
import { formatElapsed } from '../lib/sparkline'

function RoleLabel({ kind, role }: { kind: string; role?: string }) {
  switch (effectiveRole(kind, role)) {
    case 'pm':
      return <span className="role-pm">PM</span>
    case 'engineering':
      return <span className="role-eng">Eng</span>
    default:
      return <span>{kind}</span>
  }
}

export function PipelinePanel({
  groups,
  fetchedAt,
  cursor,
  onSelect,
  onDispatch,
}: {
  groups: TrackerIssuesResult[]
  fetchedAt: Date | null
  cursor: number
  onSelect: (index: number) => void
  onDispatch: (index: number) => void
}) {
  // Pre-compute flat indices so cursor highlighting matches keyboard nav.
  const flat = useMemo(() => flattenIssues(groups), [groups])
  if (groups.length === 0) return null

  let flatIdx = -1
  return (
    <div className="panel">
      <h2>
        Pipeline
        {fetchedAt && (
          <span className="meta">
            {formatElapsed(Date.now() - fetchedAt.getTime())} ago
          </span>
        )}
      </h2>
      {flat.length === 0 && !groups.some((g) => g.error) && (
        <div className="empty">No open issues</div>
      )}
      {groups.map((g, gi) => {
        if (g.error) {
          return (
            <div className="pipeline-group" key={gi}>
              <div className="fetch-error">
                ! {g.tracker_kind}/{g.project}: fetch failed
              </div>
            </div>
          )
        }
        const issues = g.issues ?? []
        if (issues.length === 0) return null
        return (
          <div className="pipeline-group" key={gi}>
            <div className="group-head">
              ▸ <RoleLabel kind={g.tracker_kind} role={g.tracker_role} />{' '}
              {g.project}
            </div>
            {issues.map((issue) => {
              flatIdx++
              const idx = flatIdx
              const selected = idx === cursor
              const ready = g.ready_for_review?.includes(issue.key) ?? false
              const prUrl = g.ready_for_review_prs?.[issue.key]
              return (
                <div
                  key={issue.key}
                  className={`issue-row${selected ? ' selected' : ''}`}
                  onClick={() => onSelect(idx)}
                  onDoubleClick={() => onDispatch(idx)}
                  title={issue.url ? `${issue.key} — double-click to dispatch` : issue.key}
                >
                  <span className="cursor">{selected ? '▸' : ''}</span>
                  <span className="key">{issue.key}</span>
                  <span className="bug">{isBug(issue) ? '(B)' : ''}</span>
                  {ready ? (
                    prUrl ? (
                      <a
                        className="review"
                        href={prUrl}
                        target="_blank"
                        rel="noreferrer"
                        onClick={(e) => e.stopPropagation()}
                        title="Ready for review — open PR"
                      >
                        (R)
                      </a>
                    ) : (
                      <span className="review" title="Ready for review">
                        (R)
                      </span>
                    )
                  ) : (
                    <span />
                  )}
                  <span className={stageClass(issue.status_type)}>
                    {pipelineStage(
                      g.tracker_kind,
                      g.tracker_role,
                      issue.status,
                      issue.status_type,
                    )}
                  </span>
                  <span className="title">{issue.title}</span>
                </div>
              )
            })}
          </div>
        )
      })}
    </div>
  )
}
