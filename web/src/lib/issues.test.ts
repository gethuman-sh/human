import { describe, expect, it } from 'vitest'
import type { Issue, TrackerIssuesResult } from '../api/types'
import {
  flattenIssues,
  inferRole,
  isBug,
  pathMatches,
  pipelineStage,
  promptForIssue,
  stageClass,
} from './issues'

function issue(overrides: Partial<Issue>): Issue {
  return {
    key: 'KEY-1',
    project: 'P',
    type: 'Task',
    title: 't',
    status: 'Todo',
    priority: '',
    assignee: '',
    reporter: '',
    description: '',
    updated_at: '2026-06-12T00:00:00Z',
    ...overrides,
  }
}

describe('flattenIssues', () => {
  it('flattens groups in order and skips errored groups', () => {
    const groups: TrackerIssuesResult[] = [
      {
        tracker_name: 'a',
        tracker_kind: 'linear',
        project: 'HUM',
        issues: [issue({ key: 'HUM-1' }), issue({ key: 'HUM-2' })],
        ready_for_review: ['HUM-2'],
        ready_for_review_prs: { 'HUM-2': 'https://pr' },
      },
      {
        tracker_name: 'b',
        tracker_kind: 'shortcut',
        project: 'S',
        issues: [issue({ key: '7' })],
        error: 'boom',
      },
    ]
    const flat = flattenIssues(groups)
    expect(flat.map((f) => f.issue.key)).toEqual(['HUM-1', 'HUM-2'])
    expect(flat[1].readyForReview).toBe(true)
    expect(flat[1].prUrl).toBe('https://pr')
    expect(flat[0].readyForReview).toBe(false)
  })

  it('tolerates null issue lists', () => {
    expect(
      flattenIssues([
        { tracker_name: 'a', tracker_kind: 'linear', project: 'P', issues: null },
      ]),
    ).toEqual([])
  })
})

describe('inferRole / pipelineStage', () => {
  it('infers pm for shortcut and engineering for linear', () => {
    expect(inferRole('shortcut')).toBe('pm')
    expect(inferRole('linear')).toBe('engineering')
    expect(inferRole('jira')).toBe('')
  })

  it('maps pm stages', () => {
    expect(pipelineStage('shortcut', '', 'Todo', 'unstarted')).toBe('Ready for Plan')
    expect(pipelineStage('shortcut', '', 'Doing', 'started')).toBe('Planning')
    expect(pipelineStage('shortcut', '', 'Done', 'done')).toBe('Planned')
    expect(pipelineStage('shortcut', '', 'Weird', '')).toBe('Weird')
  })

  it('maps engineering stages', () => {
    expect(pipelineStage('linear', '', 'Todo', 'unstarted')).toBe('Backlog')
    expect(pipelineStage('linear', '', 'Doing', 'started')).toBe('In Dev')
    expect(pipelineStage('linear', '', 'Done', 'done')).toBe('Done')
    expect(pipelineStage('linear', '', 'Canceled', 'closed')).toBe('Closed')
  })

  it('explicit role beats kind inference', () => {
    expect(pipelineStage('jira', 'pm', 'Todo', 'unstarted')).toBe('Ready for Plan')
  })

  it('unknown role falls back to the raw status name', () => {
    expect(pipelineStage('jira', '', 'In Review', 'started')).toBe('In Review')
  })
})

describe('stageClass', () => {
  it('mirrors the TUI stage styling', () => {
    expect(stageClass('started')).toBe('stage-started')
    expect(stageClass('done')).toBe('stage-done')
    expect(stageClass('unstarted')).toBe('stage-subtle')
    expect(stageClass('closed')).toBe('stage-subtle')
    expect(stageClass(undefined)).toBe('stage-subtle')
  })
})

describe('isBug', () => {
  it('detects bug type segments case-insensitively', () => {
    expect(isBug(issue({ type: 'Bug' }))).toBe(true)
    expect(isBug(issue({ type: 'defect/bug' }))).toBe(true)
    expect(isBug(issue({ type: 'type:BUG' }))).toBe(true)
    expect(isBug(issue({ type: 'Task' }))).toBe(false)
    expect(isBug(issue({ type: 'Bugfix' }))).toBe(false)
  })

  it('detects bug labels', () => {
    expect(isBug(issue({ labels: ['bug'] }))).toBe(true)
    expect(isBug(issue({ labels: ['kind/bug'] }))).toBe(true)
    expect(isBug(issue({ labels: ['feature'] }))).toBe(false)
  })
})

describe('promptForIssue', () => {
  it('routes bugs to bug-plan regardless of tracker', () => {
    expect(
      promptForIssue({
        trackerKind: 'shortcut',
        trackerRole: 'pm',
        project: 'S',
        issue: issue({ key: '9', type: 'bug' }),
        readyForReview: false,
      }),
    ).toBe('/human-bug-plan 9')
  })

  it('routes pm tickets to plan and engineering to execute', () => {
    expect(
      promptForIssue({
        trackerKind: 'shortcut',
        trackerRole: '',
        project: 'S',
        issue: issue({ key: '9' }),
        readyForReview: false,
      }),
    ).toBe('/human-plan 9')
    expect(
      promptForIssue({
        trackerKind: 'linear',
        trackerRole: '',
        project: 'HUM',
        issue: issue({ key: 'HUM-3' }),
        readyForReview: false,
      }),
    ).toBe('/human-execute HUM-3')
  })
})

describe('pathMatches', () => {
  it('matches exact dirs and proper descendants only', () => {
    expect(pathMatches('/home/u/proj', '/home/u/proj')).toBe(true)
    expect(pathMatches('/home/u/proj/sub', '/home/u/proj')).toBe(true)
    expect(pathMatches('/home/u/proj-other', '/home/u/proj')).toBe(false)
    expect(pathMatches(undefined, '/home/u/proj')).toBe(false)
    expect(pathMatches('/home/u/proj', '')).toBe(false)
  })
})
