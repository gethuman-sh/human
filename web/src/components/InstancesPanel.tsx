import type { InstanceDTO, ModelUsage } from '../api/types'
import { formatElapsed, formatTokens } from '../lib/sparkline'

const STATUS_ICON: Record<string, string> = {
  ready: '●',
  working: '◐',
  blocked: '⚠',
  waiting: '⚠',
  error: '⚠',
  ended: '○',
}

function modelClass(model: string): string {
  if (model.includes('opus')) return 'fill-opus'
  if (model.includes('sonnet')) return 'fill-sonnet'
  if (model.includes('haiku')) return 'fill-haiku'
  return 'fill-other'
}

function shortModel(model: string): string {
  return model.replace(/^claude-/, '').replace(/-\d{8}$/, '')
}

function ModelBars({ models }: { models: ModelUsage[] }) {
  const totals = models.map(
    (m) => m.input_tokens + m.output_tokens + m.cache_create + m.cache_read,
  )
  const max = Math.max(...totals, 1)
  return (
    <>
      {models.map((m, i) => (
        <div className="modelbar" key={m.model}>
          <span className="name">{shortModel(m.model)}</span>
          <span className="track">
            <span
              className={`fill ${modelClass(m.model)}`}
              style={{ width: `${Math.max((totals[i] / max) * 100, 2)}%` }}
            />
          </span>
          <span>{formatTokens(totals[i])}</span>
        </div>
      ))}
    </>
  )
}

function agentNameFromLabel(label: string): string | null {
  // Container labels look like `Container "human-agent-agent-3" (...)`.
  const match = label.match(/human-agent-([A-Za-z0-9_-]+)/)
  return match ? match[1] : null
}

function Instance({
  inst,
  onStopAgent,
}: {
  inst: InstanceDTO
  onStopAgent: (name: string) => void
}) {
  const status = inst.session?.status ?? 'ready'
  const starting = inst.label.includes('starting...')
  const icon = starting ? '◌' : (STATUS_ICON[status] ?? '○')
  const statusClass = starting ? 'status-starting' : `status-${status}`
  const agentName = inst.source === 'container' ? agentNameFromLabel(inst.label) : null

  const running = (inst.session?.subagents ?? []).filter((s) => !s.completed_at)
  const completed = (inst.session?.subagents ?? []).filter((s) => s.completed_at)
  const tasks = inst.session?.tasks ?? []
  const taskCounts = {
    pending: tasks.filter((t) => t.status === 'pending').length,
    inProgress: tasks.filter((t) => t.status === 'in_progress').length,
    completed: tasks.filter((t) => t.status === 'completed').length,
  }

  return (
    <div className="instance">
      <div className="row">
        <span className={statusClass}>{icon}</span>
        <span className="label">{inst.label}</span>
        {inst.daemon_connected && <span className="marker" title="daemon-connected">⚡</span>}
        {inst.proxy_configured && <span className="marker">proxy</span>}
        {inst.memory_usage !== undefined && inst.memory_usage > 0 && (
          <span className="marker">
            {(inst.memory_usage / 1024 / 1024).toFixed(0)}MB
          </span>
        )}
        {inst.session?.started_at && !starting && (
          <span className="marker">
            {formatElapsed(Date.now() - Date.parse(inst.session.started_at))}
          </span>
        )}
        {inst.session?.slug && <span className="slug">{inst.session.slug}</span>}
        {status === 'working' && inst.session?.current_tool && (
          <span className="status-working">{inst.session.current_tool}</span>
        )}
        {status === 'blocked' && inst.session?.blocked_tool && (
          <span className="status-blocked">blocked: {inst.session.blocked_tool}</span>
        )}
        {status === 'error' && inst.session?.error_type && (
          <span className="status-error">{inst.session.error_type}</span>
        )}
        {agentName && !starting && (
          <button
            className="stop-btn"
            title={`Stop ${agentName}`}
            onClick={() => onStopAgent(agentName)}
          >
            stop
          </button>
        )}
      </div>
      {inst.models && inst.models.length > 0 && <ModelBars models={inst.models} />}
      {running.length > 0 && (
        <div className="subagents">
          {running.slice(0, 5).map((s, i) => (
            <div key={i} className="running">
              ↳ {s.subagent_type ? `[${s.subagent_type}] ` : ''}
              {s.description}
            </div>
          ))}
        </div>
      )}
      {completed.length > 0 && (
        <div className="subagents">
          <span className="done">
            {completed.length} agent{completed.length > 1 ? 's' : ''} completed
          </span>
        </div>
      )}
      {tasks.length > 0 && (
        <div className="tasks">
          tasks: {taskCounts.completed}/{tasks.length} done
          {taskCounts.inProgress > 0 && `, ${taskCounts.inProgress} in progress`}
          {taskCounts.pending > 0 && `, ${taskCounts.pending} pending`}
        </div>
      )}
    </div>
  )
}

export function InstancesPanel({
  instances,
  totalUsage,
  onStopAgent,
}: {
  instances: InstanceDTO[]
  totalUsage: ModelUsage[]
  onStopAgent: (name: string) => void
}) {
  return (
    <div className="panel">
      <h2>
        Instances
        {totalUsage.length > 0 && (
          <span className="meta">
            total{' '}
            {formatTokens(
              totalUsage.reduce(
                (acc, m) =>
                  acc + m.input_tokens + m.output_tokens + m.cache_create + m.cache_read,
                0,
              ),
            )}{' '}
            tokens
          </span>
        )}
      </h2>
      {instances.length === 0 ? (
        <div className="empty">No Claude instances found</div>
      ) : (
        instances.map((inst, i) => (
          <Instance key={inst.label + i} inst={inst} onStopAgent={onStopAgent} />
        ))
      )}
    </div>
  )
}
