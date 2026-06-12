import { useEffect, useRef } from 'react'
import type { Snapshot } from '../api/types'

// statesNotifyOn mirrors the TUI's checkIdleTransitions: a chime when an
// instance that was working/blocked/waiting becomes ready.
const BUSY_STATES = new Set(['working', 'blocked', 'waiting'])

function instanceKey(label: string, sessionId?: string): string {
  return sessionId || label
}

// playChime emits a short two-tone beep via WebAudio — the browser
// equivalent of the TUI's notification sound.
function playChime() {
  try {
    const ctx = new AudioContext()
    const gain = ctx.createGain()
    gain.gain.value = 0.06
    gain.connect(ctx.destination)
    const tone = (freq: number, start: number, duration: number) => {
      const osc = ctx.createOscillator()
      osc.type = 'sine'
      osc.frequency.value = freq
      osc.connect(gain)
      osc.start(ctx.currentTime + start)
      osc.stop(ctx.currentTime + start + duration)
    }
    tone(880, 0, 0.12)
    tone(1320, 0.14, 0.18)
    window.setTimeout(() => void ctx.close(), 600)
  } catch {
    // no audio available — notification alone has to do
  }
}

// useIdleNotify watches snapshot updates for working→ready transitions
// and raises a sound + browser notification, like the TUI's idle chime.
export function useIdleNotify(snapshot: Snapshot | null) {
  const prevStatuses = useRef<Map<string, string>>(new Map())

  useEffect(() => {
    if (!snapshot) return
    const next = new Map<string, string>()
    for (const inst of snapshot.instances ?? []) {
      if (!inst.session) continue
      const key = instanceKey(inst.label, inst.session.session_id)
      const status = inst.session.status
      next.set(key, status)

      const prev = prevStatuses.current.get(key)
      if (prev && BUSY_STATES.has(prev) && status === 'ready') {
        playChime()
        if ('Notification' in window && Notification.permission === 'granted') {
          new Notification('human', { body: `${inst.label} is ready` })
        }
      }
    }
    prevStatuses.current = next
  }, [snapshot])

  // Ask for notification permission on the first user interaction —
  // browsers reject permission prompts that aren't user-initiated.
  useEffect(() => {
    if (!('Notification' in window) || Notification.permission !== 'default') {
      return
    }
    const request = () => void Notification.requestPermission()
    window.addEventListener('pointerdown', request, { once: true })
    return () => window.removeEventListener('pointerdown', request)
  }, [])
}
