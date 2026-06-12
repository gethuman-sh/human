import { useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import type { PendingConfirm, Snapshot, WsMessage } from '../api/types'

export interface SnapshotFeed {
  snapshot: Snapshot | null
  confirms: PendingConfirm[]
  dispatchStatus: string
  connected: boolean
  clearDispatchStatus: () => void
  setDispatchStatus: (message: string) => void
}

const RECONNECT_MIN_MS = 500
const RECONNECT_MAX_MS = 10_000
const FALLBACK_POLL_MS = 5_000

// useSnapshot owns the WebSocket connection to the daemon: snapshots,
// pending confirmations, and dispatch status flashes all arrive here.
// While the socket is down it falls back to polling GET /api/snapshot
// so the dashboard degrades instead of freezing.
export function useSnapshot(): SnapshotFeed {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [confirms, setConfirms] = useState<PendingConfirm[]>([])
  const [dispatchStatus, setDispatchStatus] = useState('')
  const [connected, setConnected] = useState(false)
  const backoff = useRef(RECONNECT_MIN_MS)

  useEffect(() => {
    let disposed = false
    let ws: WebSocket | null = null
    let reconnectTimer: number | undefined
    let pollTimer: number | undefined

    const poll = async () => {
      try {
        setSnapshot(await api.snapshot())
        setConfirms((await api.confirms()) ?? [])
      } catch {
        // daemon unreachable — keep the last snapshot on screen
      }
    }

    const connect = () => {
      if (disposed) return
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${location.host}/ws`)

      ws.onopen = () => {
        backoff.current = RECONNECT_MIN_MS
        setConnected(true)
        if (pollTimer !== undefined) {
          clearInterval(pollTimer)
          pollTimer = undefined
        }
        // Confirms are pushed on change only; fetch the current set once.
        void api.confirms().then((c) => setConfirms(c ?? [])).catch(() => {})
      }

      ws.onmessage = (event: MessageEvent<string>) => {
        let msg: WsMessage
        try {
          msg = JSON.parse(event.data) as WsMessage
        } catch {
          return
        }
        switch (msg.type) {
          case 'snapshot':
            setSnapshot(msg.data)
            break
          case 'confirms':
            setConfirms(msg.data ?? [])
            break
          case 'dispatch-status':
            setDispatchStatus(msg.data.message)
            break
          case 'agent-stopped':
            // Drop the stopped agent's instance immediately instead of
            // waiting for the next discovery cycle (TUI parity).
            setSnapshot((prev) =>
              prev
                ? {
                    ...prev,
                    instances: prev.instances?.filter(
                      (inst) => !inst.label.includes(msg.data.name),
                    ),
                  }
                : prev,
            )
            break
        }
      }

      ws.onclose = () => {
        setConnected(false)
        if (disposed) return
        if (pollTimer === undefined) {
          void poll()
          pollTimer = window.setInterval(() => void poll(), FALLBACK_POLL_MS)
        }
        reconnectTimer = window.setTimeout(connect, backoff.current)
        backoff.current = Math.min(backoff.current * 2, RECONNECT_MAX_MS)
      }

      ws.onerror = () => ws?.close()
    }

    connect()

    return () => {
      disposed = true
      if (reconnectTimer !== undefined) clearTimeout(reconnectTimer)
      if (pollTimer !== undefined) clearInterval(pollTimer)
      ws?.close()
    }
  }, [])

  return {
    snapshot,
    confirms,
    dispatchStatus,
    connected,
    clearDispatchStatus: () => setDispatchStatus(''),
    setDispatchStatus,
  }
}
