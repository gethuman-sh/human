import { useEffect } from 'react'
import type { PendingConfirm } from '../api/types'

// ConfirmDialog mirrors the TUI's destructive-operation overlay:
// y approves, n/Esc aborts. Only the oldest pending confirmation is
// shown; the next one appears once this is resolved.
export function ConfirmDialog({
  confirm,
  onResolve,
}: {
  confirm: PendingConfirm
  onResolve: (id: string, approved: boolean) => void
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'y') onResolve(confirm.id, true)
      if (e.key === 'n' || e.key === 'Escape') onResolve(confirm.id, false)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [confirm.id, onResolve])

  return (
    <div className="overlay">
      <div className="dialog">
        <h3>⚠ Confirm destructive operation</h3>
        <p className="warn">{confirm.prompt}</p>
        <p>
          {confirm.operation} on {confirm.tracker} (requested by PID{' '}
          {confirm.client_pid})
        </p>
        <div className="actions">
          <button onClick={() => onResolve(confirm.id, false)}>
            Abort (n)
          </button>
          <button className="danger" onClick={() => onResolve(confirm.id, true)}>
            Approve (y)
          </button>
        </div>
      </div>
    </div>
  )
}
