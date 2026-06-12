import { useEffect, useState } from 'react'

// AgentPromptDialog is the GUI mapping of the TUI's "a" (interactive
// agent) action. Headless agents need a prompt to be useful, so the GUI
// asks for one instead of opening a terminal.
export function AgentPromptDialog({
  onSubmit,
  onClose,
  error,
  busy,
}: {
  onSubmit: (prompt: string) => void
  onClose: () => void
  error: string
  busy: boolean
}) {
  const [prompt, setPrompt] = useState('')

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const canSubmit = prompt.trim() !== '' && !busy

  return (
    <div className="overlay">
      <div className="dialog">
        <h3>New Agent</h3>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (canSubmit) onSubmit(prompt.trim())
          }}
        >
          <label htmlFor="prompt">Prompt</label>
          <input
            id="prompt"
            autoFocus
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            placeholder="/human-execute HUM-42 or any instruction"
          />
          {error && <p className="error">{error}</p>}
          <div className="actions">
            <button type="button" onClick={onClose}>
              Cancel (Esc)
            </button>
            <button type="submit" className="primary" disabled={!canSubmit}>
              {busy ? 'Spawning…' : 'Spawn'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
