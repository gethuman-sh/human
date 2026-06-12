import { useEffect, useMemo, useState } from 'react'
import type { TrackerIssuesResult } from '../api/types'
import { effectiveRole } from '../lib/issues'

export interface TrackerOption {
  kind: string
  project: string
}

// CreateTicketDialog mirrors the TUI's "n" form: tracker/project select,
// title, description. Tracker options come from the loaded issue groups,
// defaulting to the first PM tracker.
export function CreateTicketDialog({
  groups,
  onSubmit,
  onClose,
  error,
  busy,
}: {
  groups: TrackerIssuesResult[]
  onSubmit: (opt: TrackerOption, title: string, description: string) => void
  onClose: () => void
  error: string
  busy: boolean
}) {
  const options = useMemo(() => {
    const seen = new Set<string>()
    const opts: TrackerOption[] = []
    for (const g of groups) {
      if (g.error) continue
      const key = `${g.tracker_kind}/${g.project}`
      if (seen.has(key)) continue
      seen.add(key)
      opts.push({ kind: g.tracker_kind, project: g.project })
    }
    // Default to the first PM tracker, like the TUI form.
    opts.sort((a, b) => {
      const pmA = effectiveRole(a.kind) === 'pm' ? 0 : 1
      const pmB = effectiveRole(b.kind) === 'pm' ? 0 : 1
      return pmA - pmB
    })
    return opts
  }, [groups])

  const [selected, setSelected] = useState(0)
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const canSubmit = title.trim() !== '' && options.length > 0 && !busy

  return (
    <div className="overlay">
      <div className="dialog">
        <h3>New Ticket</h3>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (canSubmit) {
              onSubmit(options[selected], title.trim(), description.trim())
            }
          }}
        >
          <label htmlFor="tracker">Tracker / Project</label>
          <select
            id="tracker"
            value={selected}
            onChange={(e) => setSelected(Number(e.target.value))}
          >
            {options.map((opt, i) => (
              <option key={`${opt.kind}/${opt.project}`} value={i}>
                {opt.kind} / {opt.project}
              </option>
            ))}
          </select>
          {options.length === 0 && (
            <p className="warn">No trackers loaded yet — wait for the pipeline.</p>
          )}

          <label htmlFor="title">Title</label>
          <input
            id="title"
            autoFocus
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="What needs to be done?"
          />

          <label htmlFor="description">Description</label>
          <textarea
            id="description"
            rows={5}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />

          {error && <p className="error">{error}</p>}

          <div className="actions">
            <button type="button" onClick={onClose}>
              Cancel (Esc)
            </button>
            <button type="submit" className="primary" disabled={!canSubmit}>
              {busy ? 'Creating…' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
