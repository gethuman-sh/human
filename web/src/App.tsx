import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from './api/client'
import type { InstanceDTO, ProjectInfo } from './api/types'
import { AgentPromptDialog } from './components/AgentPromptDialog'
import { ConfirmDialog } from './components/ConfirmDialog'
import { CreateTicketDialog, type TrackerOption } from './components/CreateTicketDialog'
import { Footer } from './components/Footer'
import { Header } from './components/Header'
import { InstancesPanel } from './components/InstancesPanel'
import { NetworkPanel } from './components/NetworkPanel'
import { PipelinePanel } from './components/PipelinePanel'
import { ProjectTabs, type Tab } from './components/ProjectTabs'
import { StatusLine } from './components/StatusLine'
import { ToolStatsPanel } from './components/ToolStatsPanel'
import { TrackersLine } from './components/TrackersLine'
import { useIdleNotify } from './hooks/useIdleNotify'
import { useIssues } from './hooks/useIssues'
import { useKeyboard } from './hooks/useKeyboard'
import { useSnapshot } from './hooks/useSnapshot'
import { flattenIssues, pathMatches, promptForIssue } from './lib/issues'

const LOG_MODES = ['full', 'meta', 'off']
const FLASH_MS = 3000

// buildTabs mirrors the TUI's tabs(): registered projects plus an
// "Other" tab when instances fall outside every project dir.
function buildTabs(projects: ProjectInfo[], instances: InstanceDTO[]): Tab[] {
  if (projects.length === 0) return []
  const tabs: Tab[] = projects.map((p) => ({ name: p.name, dir: p.dir }))
  const unmatched = instances.some(
    (inst) => !projects.some((p) => pathMatches(inst.cwd, p.dir)),
  )
  if (unmatched) tabs.push({ name: 'Other', dir: '' })
  return tabs
}

export default function App() {
  const feed = useSnapshot()
  const { issues, fetchedAt, refresh } = useIssues()
  const [projects, setProjects] = useState<ProjectInfo[]>([])
  const [activeTab, setActiveTab] = useState(0)
  const [cursor, setCursor] = useState(0)
  const [logMode, setLogMode] = useState('')
  const [flash, setFlash] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [agentOpen, setAgentOpen] = useState(false)
  const [dialogError, setDialogError] = useState('')
  const [dialogBusy, setDialogBusy] = useState(false)

  useIdleNotify(feed.snapshot)

  // Projects and log mode are cheap reads; refresh alongside the
  // snapshot cadence is unnecessary — load once and on reconnect.
  useEffect(() => {
    void api.projects().then((p) => setProjects(p ?? [])).catch(() => {})
    void api.logMode().then((r) => setLogMode(r.mode)).catch(() => {})
  }, [feed.connected])

  // Dispatch-status pushes from the daemon surface in the footer flash.
  useEffect(() => {
    if (feed.dispatchStatus) {
      setFlash(feed.dispatchStatus)
      feed.clearDispatchStatus()
      refresh()
    }
  }, [feed, refresh])

  // Footer flashes auto-clear like the TUI's 3s dispatch status.
  useEffect(() => {
    if (!flash) return
    const timer = window.setTimeout(() => setFlash(''), FLASH_MS)
    return () => clearTimeout(timer)
  }, [flash])

  const instances = feed.snapshot?.instances ?? []
  const tabs = useMemo(() => buildTabs(projects, instances), [projects, instances])

  const visibleInstances = useMemo(() => {
    if (tabs.length < 2) return instances
    const active = tabs[activeTab]
    if (!active) return []
    if (active.dir === '') {
      return instances.filter(
        (inst) => !projects.some((p) => pathMatches(inst.cwd, p.dir)),
      )
    }
    return instances.filter((inst) => pathMatches(inst.cwd, active.dir))
  }, [tabs, activeTab, instances, projects])

  const flat = useMemo(() => flattenIssues(issues), [issues])
  const clampedCursor = Math.min(cursor, Math.max(flat.length - 1, 0))

  const activeProjectDir = useCallback((): string => {
    const active = tabs[activeTab]
    if (active?.dir) return active.dir
    return projects[0]?.dir ?? ''
  }, [tabs, activeTab, projects])

  const dispatchIssue = useCallback(
    (index: number) => {
      const sel = flat[index]
      if (!sel) {
        setFlash('No issues')
        return
      }
      const prompt = promptForIssue(sel)
      setFlash(`Spawning agent for ${sel.issue.key}…`)
      api
        .dispatchAgent(prompt, activeProjectDir())
        .then((r) => setFlash(`Spawning ${r.name} for ${sel.issue.key}…`))
        .catch((err: Error) => setFlash(`Failed: ${err.message}`))
    },
    [flat, activeProjectDir],
  )

  const openIssue = useCallback(() => {
    const sel = flat[clampedCursor]
    if (!sel) return
    if (!sel.issue.url) {
      setFlash(`No URL for ${sel.issue.key}`)
      return
    }
    window.open(sel.issue.url, '_blank', 'noopener')
    setFlash(`Opened ${sel.issue.key}`)
  }, [flat, clampedCursor])

  const cycleLogMode = useCallback(() => {
    const next = LOG_MODES[(LOG_MODES.indexOf(logMode) + 1) % LOG_MODES.length]
    api
      .setLogMode(next)
      .then((r) => setLogMode(r.mode))
      .catch((err: Error) => setFlash(`Log mode failed: ${err.message}`))
  }, [logMode])

  const stopAgent = useCallback((name: string) => {
    api
      .stopAgent(name)
      .then(() => setFlash(`Stopping ${name}…`))
      .catch((err: Error) => setFlash(`Stop failed: ${err.message}`))
  }, [])

  const createTicket = useCallback(
    (opt: TrackerOption, title: string, description: string) => {
      setDialogBusy(true)
      setDialogError('')
      api
        .createTicket({
          tracker_kind: opt.kind,
          project: opt.project,
          title,
          description,
        })
        .then((created) => {
          setCreateOpen(false)
          setFlash(`Created ${created.key}`)
          refresh()
          // TUI parity: a fresh ticket is immediately groomed by
          // /human-ready via a dispatched agent.
          return api.dispatchAgent(
            `/human-ready ${created.tracker_kind} ${created.key}`,
            activeProjectDir(),
          )
        })
        .catch((err: Error) => setDialogError(err.message))
        .finally(() => setDialogBusy(false))
    },
    [refresh, activeProjectDir],
  )

  const spawnAgent = useCallback(
    (prompt: string) => {
      setDialogBusy(true)
      setDialogError('')
      api
        .dispatchAgent(prompt, activeProjectDir())
        .then((r) => {
          setAgentOpen(false)
          setFlash(`Spawning ${r.name}…`)
        })
        .catch((err: Error) => setDialogError(err.message))
        .finally(() => setDialogBusy(false))
    },
    [activeProjectDir],
  )

  const resolveConfirm = useCallback((id: string, approved: boolean) => {
    api
      .resolveConfirm(id, approved)
      .then(() => setFlash(approved ? 'Approved' : 'Aborted'))
      .catch((err: Error) => setFlash(`Confirm failed: ${err.message}`))
  }, [])

  const dialogOpen = createOpen || agentOpen || feed.confirms.length > 0

  useKeyboard({
    enabled: !dialogOpen,
    next: () => setCursor((c) => Math.min(c + 1, Math.max(flat.length - 1, 0))),
    prev: () => setCursor((c) => Math.max(c - 1, 0)),
    dispatch: () => dispatchIssue(clampedCursor),
    open: openIssue,
    newTicket: () => {
      setDialogError('')
      setCreateOpen(true)
    },
    newAgent: () => {
      setDialogError('')
      setAgentOpen(true)
    },
    cycleLogMode,
    nextTab: () => setActiveTab((t) => (tabs.length ? (t + 1) % tabs.length : 0)),
    prevTab: () =>
      setActiveTab((t) => (tabs.length ? (t - 1 + tabs.length) % tabs.length : 0)),
    selectTab: (i) => {
      if (i < tabs.length) setActiveTab(i)
    },
  })

  return (
    <div className="app">
      <Header snapshot={feed.snapshot} />
      <StatusLine snapshot={feed.snapshot} />
      <ProjectTabs tabs={tabs} active={activeTab} onSelect={setActiveTab} />
      <InstancesPanel
        instances={visibleInstances}
        totalUsage={feed.snapshot?.total_usage ?? []}
        onStopAgent={stopAgent}
      />
      <TrackersLine trackers={feed.snapshot?.trackers ?? []} />
      <PipelinePanel
        groups={issues}
        fetchedAt={fetchedAt}
        cursor={clampedCursor}
        onSelect={setCursor}
        onDispatch={dispatchIssue}
      />
      <NetworkPanel events={feed.snapshot?.network_events ?? []} />
      <ToolStatsPanel stats={feed.snapshot?.tool_stats} />
      <Footer
        connected={feed.connected}
        logMode={logMode}
        flash={flash}
        showTabs={tabs.length >= 2}
      />
      {createOpen && (
        <CreateTicketDialog
          groups={issues}
          onSubmit={createTicket}
          onClose={() => setCreateOpen(false)}
          error={dialogError}
          busy={dialogBusy}
        />
      )}
      {agentOpen && (
        <AgentPromptDialog
          onSubmit={spawnAgent}
          onClose={() => setAgentOpen(false)}
          error={dialogError}
          busy={dialogBusy}
        />
      )}
      {feed.confirms.length > 0 && (
        <ConfirmDialog confirm={feed.confirms[0]} onResolve={resolveConfirm} />
      )}
    </div>
  )
}
