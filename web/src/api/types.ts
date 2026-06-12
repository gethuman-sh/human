// TypeScript mirrors of the Go wire types in internal/gui/dto.go and
// internal/daemon. Keep field names in sync with the Go json tags.

export type SessionStatus =
  | 'ready'
  | 'working'
  | 'blocked'
  | 'waiting'
  | 'error'
  | 'ended'

export interface DaemonDTO {
  pid: number
  alive: boolean
  proxy_addr?: string
  proxy_active_conns: number
}

export interface UsageWindow {
  start: string
  end: string
}

export interface TrackerStatus {
  name: string
  kind: string
  label: string
  working: boolean
  vault_ref?: boolean
  missing?: string[]
}

export interface SubagentDTO {
  description: string
  subagent_type?: string
  started_at: string
  completed_at?: string
  duration_ms?: number
}

export interface TaskDTO {
  subject: string
  status: 'pending' | 'in_progress' | 'completed'
}

export interface SessionDTO {
  session_id: string
  slug?: string
  status: SessionStatus
  started_at: string
  last_activity: string
  current_tool?: string
  blocked_tool?: string
  error_type?: string
  subagents?: SubagentDTO[]
  tasks?: TaskDTO[]
}

export interface ModelUsage {
  model: string
  input_tokens: number
  output_tokens: number
  cache_create: number
  cache_read: number
}

export interface InstanceDTO {
  label: string
  source: 'host' | 'container'
  cwd?: string
  pid?: number
  container_id?: string
  memory_usage?: number
  memory_limit?: number
  proxy_configured?: boolean
  daemon_connected?: boolean
  session?: SessionDTO
  models?: ModelUsage[]
}

export interface PaneDTO {
  session_name: string
  window_index: number
  pane_index: number
  cwd?: string
  devcontainer?: boolean
  state?: string
}

export interface NetworkEvent {
  source: string
  status: string
  host: string
  count: number
  last_seen: string
}

export interface ToolCount {
  tool_name: string
  count: number
}

export interface TimeBucket {
  bucket: string
  count: number
}

export interface EventNameCount {
  event_name: string
  count: number
}

export interface ToolStats {
  by_tool: ToolCount[] | null
  by_hour: TimeBucket[] | null
  by_event_name: EventNameCount[] | null
  total_events: number
  since: string
  until: string
}

export interface Snapshot {
  fetched_at: string
  hostname?: string
  error?: string
  daemon: DaemonDTO
  telegram?: string
  slack?: string
  usage_window?: UsageWindow
  trackers?: TrackerStatus[]
  instances?: InstanceDTO[]
  panes?: PaneDTO[]
  total_usage?: ModelUsage[]
  network_events?: NetworkEvent[]
  tool_stats?: ToolStats
}

// Category mirrors internal/tracker.Category.
export type Category = '' | 'unstarted' | 'started' | 'done' | 'closed'

export interface Issue {
  key: string
  project: string
  type: string
  title: string
  status: string
  status_type?: Category
  priority: string
  assignee: string
  reporter: string
  description: string
  url?: string
  updated_at: string
  parent_key?: string
  labels?: string[]
}

export interface TrackerIssuesResult {
  tracker_name: string
  tracker_kind: string
  tracker_role?: string
  project: string
  issues: Issue[] | null
  ready_for_review?: string[]
  ready_for_review_prs?: Record<string, string>
  error?: string
}

export interface ProjectInfo {
  name: string
  dir: string
}

export interface PendingConfirm {
  id: string
  operation: string
  tracker: string
  key: string
  prompt: string
  created_at: string
  client_pid: number
}

export type WsMessage =
  | { type: 'snapshot'; data: Snapshot }
  | { type: 'agent-stopped'; data: { name: string } }
  | { type: 'confirms'; data: PendingConfirm[] | null }
  | { type: 'dispatch-status'; data: { message: string } }
