// Workflow-board frontend (typed source). Renders 5 forward-order columns from
// the daemon's derived BoardCards (via the Go App.Cards binding) and lets a card
// be dragged to its single next column to trigger that stage's `human` action
// via App.Transition. Placement, checkmarks and running/error state are all
// derived server-side — this file never re-derives a stage.
//
// The shipped runtime is desktop/frontend/dist/board.js; `npm run build`
// (tsc + bundle.mjs) regenerates dist/ from this source for `wails build`.

// The fancy hooks no-op while the classic theme is active, so they are safe to
// call unconditionally on the hot paths below.
import {
  celebrateDrop,
  ghostTilt,
  initFancy,
  isThemeToggleChord,
  toggleTheme,
  trail,
} from "./fancy.js";
import { applyNotMineOpacity } from "./appearance.js";
import { initPermissions, type PermissionRequest } from "./permissions.js";
import { bugsHeaderHTML, securityHeaderHTML, gardeningHeaderHTML } from "./board-findbugs.js";
import {
  initMockupsView,
  showMockups,
  setPendingMockupSlug,
  setChosenMockup,
  type MockupSet,
} from "./mockupsview.js";
import {
  initSettingsView,
  showSettings,
  settingsIndex,
  saveSetting,
  setPaletteOpener,
  setActiveSection,
  type SettingsData,
} from "./settingsview.js";
import { initPalette, openPalette, isPaletteChord } from "./palette.js";
import {
  initStatsView,
  showStats,
  startStatsPoll,
  stopStatsPoll,
} from "./statsview.js";
import type { StatsOverview } from "./statsview.js";
import {
  QUEUES,
  QUEUE_TRANSITION_TO,
  queueOf,
  isReworkable,
  isReviewRetryable,
  isReopenable,
  ageBadge,
  isReplannable,
  forwardDropAllowed,
  badgeInfo,
  cardError,
  sortByHandOrder,
  insertKeyAt,
  boardStateFromPayload,
  isReadyToDeploy,
  deployableCards,
  deployControlView,
  safetyPollShouldReconcile,
  safetyReconcileError,
} from "./board-queue.js";
import type { DeploySide } from "./board-queue.js";
import { linksWithin, arrowPath, plan, gapsBySide } from "./board-arrows.js";
import type { Box, Drawn, Side } from "./board-arrows.js";
import { buildDeployControl } from "./board-deploy.js";
import { buildCostSection, buildDetailSections, buildOptionsSection, buildShippedPartialSection, buildStopDecisionSection, type TicketCost } from "./board-detail.js";
import { ideationInputEnabled, shouldCloseIdeation } from "./board-ideation.js";
import {
  descEditInputEnabled,
  descEditApplyEnabled,
  buildDescriptionPreview,
  descEditShouldDiscardOnClose,
} from "./board-descedit.js";
import { initProjectsView, showProjectsOverview, type RecentProject } from "./projectsview.js";
import { runGuardedAction } from "./board-actions.js";
import { reconcilePending, dropPending, type Pending } from "./board-pending.js";
import {
  reconcilePendingMoves,
  dropPendingMove,
  pendingMovesForBatch,
  type PendingMove,
} from "./board-pending-move.js";

interface Card {
  key: string;
  title: string;
  url: string;
  stage: string;
  state: string;
  engineeringKey?: string;
  branch?: string;
  prURL?: string;
  labels?: string[];
  description?: string;
  assignee?: string;
  // The ticket's filer. Ownership fallback when there is no assignee (which is
  // nearly every ticket): the board attributes the card to the reporter.
  reporter?: string;
  // Tracker instance name + provider kind the issue was listed from; passed
  // back to GetIssueDetail so the daemon resolves the exact instance (names
  // can repeat across provider sections, keys across kinds).
  tracker?: string;
  trackerKind?: string;
  error?: string;
  verdict?: string;
  // The daemon's decision on whether that verdict blocks the card. Read this
  // via verdictFailed(card) — the board must never re-test the verdict string.
  verdictFailed?: boolean;
  activity?: string;
  activityAt?: string;
  // RFC3339 instant a paused (outage) card's standing marker stated as when
  // the substrate clears, when one was parsed out of the diagnosis (SC-3024).
  // Absent when no time was stated — the badge then reads "paused" with no
  // time and the reconcile pass falls back to its own backoff.
  resumeAt?: string;
  // RFC3339 time the newest marker of the card's current stage landed; feeds
  // the Engineering-backlog age badge.
  stageEnteredAt?: string;
  // Done-stage sub-phase ("pr-review" while the machine review→fix loop runs);
  // badgeInfo reads it to render "PR review…" instead of "deploying…".
  deployPhase?: string;
  // Idea-space sub-column (0 loosest … 4 most concrete) for Ideas-stage cards.
  // Locally persisted preference; absent means leftmost.
  ideaColumn?: number;
  // Defect ticket (bug label or bug issue type): rendered in the Bugs pane
  // instead of the workflow board's columns.
  bug?: boolean;
  // Security ticket (security label or type): rendered in the Security half of
  // the Bugs pane. Disjoint from bug — the classification tokens never overlap.
  security?: boolean;
  // A completed filing-time related-work record is present ([human:related]
  // found/none). Suppresses the on-demand "Find related work" menu item; an
  // incomplete record does not set it, so a died-halfway run stays re-runnable.
  hasRelatedRecord?: boolean;
  // Ticket the user parked off the board (right-click → Hide). Local view
  // preference; filtered out unless revealed via the header's Unhide toggle.
  hidden?: boolean;
  // Tickets this one waits for. A blocker and the card it holds usually sit in
  // different columns, so this renders as a badge naming the key rather than a
  // line drawn between two cards.
  blockers?: string[];
  // Set when the daemon could not read this ticket's markers this scan (a
  // comment-fetch error). Rendered locked: non-draggable, no launch actions,
  // with an "unreadable" badge — never presented as idle Backlog work (1700).
  degraded?: boolean;
  // Set when this card's owner (assignee, or reporter when unassigned) is
  // someone other than the current viewer (SC-3339). Rendered dimmed-but-
  // readable and fully interactive — a hint, never a restriction. Absent/false
  // for the viewer's own cards, ownerless cards, or unknown identity.
  notMine?: boolean;
  mockupSlug?: string;
  mockupState?: string; // "ready" | "creating"
  // The ticket's chosen winner mockup (leaf group slug + option file), set once
  // a design is picked. "View mocks" still opens the root group; the winner is
  // surfaced as the highlighted root→leaf path inside the viewer.
  mockupChosenSlug?: string;
  mockupChosenFile?: string;
  // The card's open decision block: a stage ended in a fork and a human must
  // pick a direction (rendered in the detail panel; badge "decision needed").
  options?: { id: string; label: string }[];
  optionsContext?: string;
  // A pre-planning gate's recorded STOP verdict (superseded/escalated/rejected),
  // the ticket it names, and the recorded reasoning. Rendered as the "decided"
  // badge and a detail-panel Decision section; absent on every other card.
  stopDecision?: string;
  stopLinkedKey?: string;
  stopReasoning?: string;
  // The ticket shipped with acceptance criteria deliberately deferred to a
  // follow-on ticket (SC-2910): shippedPartial marks it, shippedPartialFollowOn
  // names the follow-on key. Rendered as a "partial scope → <key>" card badge and
  // a detail-panel section; absent on every card without the trace.
  shippedPartial?: boolean;
  shippedPartialFollowOn?: string;
}

interface BoardData {
  cards: Card[];
  dockerAvailable: boolean;
  error?: string;
  // Non-error guidance shown in place of the columns when the board has nothing
  // to render for a structural reason — chiefly no PM-role tracker configured
  // (SC-1655), where an empty board would otherwise read as "no work".
  notice?: string;
  // Non-error affordance shown above a populated board when the fetch hit the
  // backend's cap and more tickets exist than are displayed (SC-1693). Distinct
  // from notice, which replaces empty columns; this accompanies real cards.
  truncation?: string;
  // Hand-sorted ticket order per queue column (top first); cards absent from
  // their queue's list render after it in fetch order.
  columnOrder?: Record<string, string[]>;
  // How faint a not-mine card renders, in percent of full opacity, from the
  // project's .humanconfig "ui" section (SC-3409). Absent means the
  // stylesheet's :root default applies.
  dimPercent?: number;
}

interface IdeationMsg {
  role: string;
  text: string;
}

interface IdeationQuestion {
  text: string;
  options: string[];
  kind: string; // "structural" | "content"
}

interface IdeationDraft {
  title: string;
  description: string;
}

interface IdeationView {
  sessionId?: string;
  mode?: string; // "chat" | "guided"
  state: string; // none | thinking | awaiting_reply | awaiting_approval | done | error
  messages: IdeationMsg[];
  question?: IdeationQuestion;
  draft?: IdeationDraft;
  createdKey?: string;
  error?: string;
}

interface DescEditMsg {
  role: string;
  text: string;
}

interface DescEditView {
  sessionId?: string;
  key?: string;
  state: string; // none | thinking | awaiting_reply | applied | error
  messages: DescEditMsg[];
  proposal?: string;
  appliedUrl?: string;
  error?: string;
}

interface ModelUsage {
  name: string;
  inputTokens: number;
  outputTokens: number;
}

interface SubagentInfo {
  description: string;
  type: string;
  done: boolean;
  startedAtUnix: number;
  durationMs: number;
}

interface AgentInstance {
  label: string;
  source: string;
  status: string; // ready | working | blocked | waiting | error | ended | ""
  hasActivity: boolean;
  slug?: string;
  pid: number;
  containerID?: string;
  cwd?: string;
  memory?: string;
  currentTool?: string;
  blockedTool?: string;
  errorType?: string;
  startedAtUnix: number;
  daemonConnected: boolean;
  proxyConfigured: boolean;
  models: ModelUsage[];
  tasksPending: number;
  tasksInProgress: number;
  tasksDone: number;
  subagents: SubagentInfo[];
}

interface InstancesData {
  agents: AgentInstance[];
  error?: string;
}

interface FeatureItem {
  name: string;
  description: string;
  recent?: boolean;
}

interface FeatureGroup {
  group: string;
  features: FeatureItem[];
  groups?: FeatureGroup[];
}

interface FeatureDoc {
  product?: string;
  tagline?: string;
  groups?: FeatureGroup[];
  exists?: boolean;
  error?: string;
}

interface StarterTemplate {
  id: string;
  type: string;
  typeLabel: string;
  language: string;
  languageLabel: string;
}

interface StartProjectInfo {
  emptyProject: boolean;
  templates: StarterTemplate[] | null;
  error?: string;
}

interface StartProjectResult {
  filesCreated: number;
}

interface IssueDetailData {
  title: string;
  assignee?: string;
  description?: string;
  // Rendered and sanitized by the daemon — safe to inject verbatim.
  descriptionHTML?: string;
  // Comment-sourced sections, also daemon-rendered and sanitized.
  reviewFindingsHTML?: string;
  failureReasonHTML?: string;
  fixSummaryHTML?: string;
}

interface DoctorCheck {
  id: string;
  name: string;
  ok: boolean;
  gating?: boolean;
  // Set when this failure holds new work back without being a substrate
  // failure (an unreachable tracker): the rail must say work is waiting rather
  // than that nothing is blocked.
  holding?: boolean;
  // "ok" | "blocking" | "degraded" | "transient" — a failing check's real
  // consequence (SC-1991). Absent on older daemons; treat as unclassified.
  severity?: string;
  detail?: string;
}

interface DoctorData {
  healthy: boolean;
  // blocked is the work-can-start verdict: true only when a gating check is
  // persistently failing. Only this may paint the LED as a hard stop (SC-1991).
  blocked?: boolean;
  summary?: string;
  checkedAt?: string;
  checks: DoctorCheck[];
}

interface ProjectBootstrapResult {
  status: "ready" | "auto" | "overview";
  project?: string;
  error?: string;
  // orphan/orphanProject: see App.checkOrphan (desktop/orphan.go, SC-3015) —
  // true when a prior app session's daemon is still running unsupervised.
  orphan?: boolean;
  orphanProject?: string;
  // conflict/conflictProject: see App.bootstrapFromCwd (desktop/projects.go,
  // SC-3346) — true when the working directory the app was launched from
  // holds a valid project config for a DIFFERENT project than the one the
  // reachable daemon named by `project` is already serving. Nothing was
  // switched; conflictProject names the working directory's project so the
  // launch-time notice can name both.
  conflict?: boolean;
  conflictProject?: string;
}

interface AppBindings {
  Cards(): Promise<BoardData>;
  Doctor(): Promise<DoctorData>;
  GetIssueDetail(trackerKind: string, trackerName: string, key: string): Promise<IssueDetailData>;
  TicketCost(key: string): Promise<TicketCost>;
  CardsQuick(): Promise<BoardData>;
  Transition(pmKey: string, pmTitle: string, from: string, to: string): Promise<void>;
  // Re-running a stage the pipeline resolved. Separate from Transition because
  // the daemon's retry machinery drives the same same-stage request, and only an
  // explicitly human call may re-run a clean terminal.
  Reopen(pmKey: string, pmTitle: string, stage: string): Promise<void>;
  FixBug(pmKey: string, pmTitle: string): Promise<void>;
  FixSecurity(pmKey: string, pmTitle: string): Promise<void>;
  FindRelatedWork(pmKey: string, pmTitle: string): Promise<void>;
  ChooseOption(pmKey: string, optionID: string): Promise<void>;
  CloseTicket(pmKey: string): Promise<void>;
  SetIdeaColumn(pmKey: string, col: number): Promise<void>;
  SetColumnOrder(queue: string, keys: string[]): Promise<void>;
  SetCardHidden(pmKey: string, hidden: boolean): Promise<void>;
  DaemonStatus(): Promise<boolean>;
  StartIdeation(seed: string, mode: string, restart: boolean, evolveKey: string, evolveLabels: string[]): Promise<IdeationView>;
  CreateIdea(title: string): Promise<string>;
  CreateBug(title: string, description: string): Promise<string>;
  CreateSecurity(title: string, description: string): Promise<string>;
  ReplyIdeation(sessionId: string, message: string): Promise<IdeationView>;
  ApproveIdeation(sessionId: string, title: string, description: string): Promise<IdeationView>;
  IdeationStatus(): Promise<IdeationView>;
  StartDescEdit(key: string, currentDescription: string, restart: boolean): Promise<DescEditView>;
  ReplyDescEdit(sessionId: string, message: string): Promise<DescEditView>;
  ApplyDescEdit(sessionId: string): Promise<DescEditView>;
  DiscardDescEdit(sessionId: string): Promise<DescEditView>;
  DescEditStatus(): Promise<DescEditView>;
  Instances(): Promise<InstancesData>;
  Features(): Promise<FeatureDoc>;
  GenerateFeatures(): Promise<void>;
  FindBugs(): Promise<void>;
  FindbugsHunting(): Promise<boolean>;
  FindSecurity(): Promise<void>;
  SecurityHunting(): Promise<boolean>;
  CreateMocks(pmKey: string, pmTitle: string, description: string): Promise<void>;
  CreateVariations(
    pmKey: string,
    feature: string,
    parentSlug: string,
    parentFile: string,
    instructions: string,
  ): Promise<void>;
  ChooseMockup(pmKey: string, slug: string, file: string): Promise<void>;
  PruneMockup(pmKey: string, slug: string): Promise<void>;
  StartProjectStatus(): Promise<StartProjectInfo>;
  StartProject(projectType: string, language: string): Promise<StartProjectResult>;
  PendingPermissions(): Promise<PermissionRequest[]>;
  DecidePermission(id: string, approved: boolean): Promise<void>;
  MockupSets(): Promise<MockupSet[]>;
  Settings(): Promise<SettingsData>;
  SaveSetting(path: string, valueJSON: string): Promise<SettingsData>;
  RecentProjects(): Promise<RecentProject[]>;
  ProjectBootstrap(): Promise<ProjectBootstrapResult>;
  BrowseForProjectDir(): Promise<string>;
  OpenProject(dir: string): Promise<RecentProject>;
  SwitchProject(): Promise<void>;
  Stats(range: string): Promise<StatsOverview>;
  ResolveClose(choice: string): Promise<void>;
  ResolveOrphan(stop: boolean): Promise<void>;
}

// This file is a module (see the trailing `export {}`) so the global
// augmentation below is legal and the local `go()` helper stays module-scoped
// instead of colliding with `window.go`.
declare global {
  interface Window {
    go?: { main?: { App?: AppBindings } };
    runtime?: {
      EventsOn(name: string, cb: () => void): void;
      BrowserOpenURL?(url: string): void;
    };
  }
}

export {};

// openExternal routes a URL to the system browser via the Wails runtime.
// Anchor clicks with target=_blank are NOT reliably forwarded by the Linux
// webview (WebKitGTK swallows the new-window request), so every external
// link must go through BrowserOpenURL; the anchor is only a styling shell.
function openExternal(url: string): void {
  if (!url) return;
  if (window.runtime?.BrowserOpenURL) {
    window.runtime.BrowserOpenURL(url);
    return;
  }
  // Dev fallback (vite in a real browser): no Wails runtime, plain open works.
  window.open(url, "_blank");
}

// Queue columns: each names a state that is TRUE of every card in it, always.
// The agent work happens on the transitions (a drag is the launch), so a card
// being worked stays in its ORIGIN queue with a live badge and only arrives in
// the next queue when the stage completes. State on the column, verb on the
// affordance — the wire stages/markers are untouched; this is pure display.
// Code is the one ACTIVITY column among the queues — deliberately special
// because coding is the board's longest and weightiest phase: the column holds
// the whole build-and-review cycle (the review chains automatically after the
// build, no gesture), and cards can only EARN their way out — a passing review
// releases them into Ready to Deploy, a failing verdict pins them here with a
// warning until a re-drop rebuilds. Deploy is not a column at all: it is a
// terminal drop zone that merges the work into main (after CI passes) and
// closes the ticket, so shipped work simply leaves the board. ("building"
// stays the internal queue id so theme hooks don't churn on a label.)
const QUEUE_LABELS: Record<string, string> = {
  ideas: "Ideas",
  product: "Product backlog",
  engineering: "Engineering backlog",
  building: "Code",
  deploy: "Ready to Deploy",
};

// The Ideas queue renders as an "idea space": five unlabeled lanes spanning a
// loose→concrete axis the PM sorts ideas along by dragging (looser left,
// more concrete right). Placement is a locally persisted preference
// (SetIdeaColumn), never ticket state — the wire stage stays "ideas"
// throughout. The lanes carry no headers: labels would visually compete with
// the real queue headers beside the space.
const IDEA_COL_COUNT = 5;

// ideaColOf resolves a card's idea-space lane: absent means leftmost (a
// fresh idea is loose by definition), out-of-range is clamped so a stale
// file entry can never render a card outside the space.
function ideaColOf(card: Card): number {
  const col = card.ideaColumn ?? 0;
  return Math.min(Math.max(col, 0), IDEA_COL_COUNT - 1);
}

// The verb shown on a drop target while a drag hovers it — the action lives
// on the thing being touched, never in the column title.
const QUEUE_VERB: Record<string, string> = {
  product: "Define it",
  engineering: "Plan it",
  building: "Build it",
};

let current: BoardData = { cards: [], dockerAvailable: true, error: "", notice: "" };
let dragging: { key: string; title: string; stage: string } | null = null;

// showHidden reveals user-hidden cards (marked with an "H" pill) instead of
// filtering them out. Session-local so the board always starts clean.
let showHidden = false;

function cardVisible(card: Card): boolean {
  return !card.hidden || showHidden;
}

// Two-phase load state. boardLoading covers the first fetch before any titles
// exist (the board shows a centered spinner). stagesLoading covers the window
// after titles render but before the comment scan resolves each card's real
// stage (every card shows a small resolving spinner). Both are false in steady
// state and during board:changed reconciles, so those never flash a spinner.
let boardLoading = false;
let stagesLoading = false;

// Matches the daemon subscribe-retry backoff (desktop/main.go backoff(), 2s)
// rounded up slightly so the poll never races the retry loop.
const DAEMON_POLL_MS = 3000;

// Safety net for edits made directly in the tracker's web UI: those produce no
// daemon event, so without a slow re-fetch they stay invisible until an
// unrelated event fires. Event-driven refresh remains the primary path; this
// only bounds the staleness window.
const BOARD_SAFETY_POLL_MS = 90_000;

let daemonReachable = false;

let boardPollTimer: ReturnType<typeof setInterval> | null = null;
let safetyPollTimer: ReturnType<typeof setInterval> | null = null;

function go(): AppBindings {
  const app = window.go?.main?.App;
  if (!app) throw new Error("Wails bindings not available");
  return app;
}

// targetEnabled gates agent-launching drops on Docker availability; every
// queue transition except idea promotion launches a containerized agent.
function targetEnabled(toQueue: string): boolean {
  if (QUEUE_TRANSITION_TO[toQueue] !== undefined && !current.dockerAvailable) return false;
  return true;
}

// badge renders the card's live state. A resting card needs no checkmark —
// its queue position IS the statement of completion. A review that found
// problems is a WARNING, not a stage failure: the work exists, it just may
// not advance until a rebuild passes.
// badge renders an already-normalized card's live state — renderCard normalizes
// the card once (consuming the session's chosen options) and feeds the SAME card
// to both the badge and the error-subtitle gate, so the two paths can never
// disagree during the local session window.
function badge(card: Card): string {
  const info = badgeInfo(card);
  if (!info) return "";
  const spinner = info.spinner ? `<span class="spinner"></span> ` : "";
  return `<span class="badge ${info.cls}" title="${escapeAttr(info.title)}">${spinner}${escapeHtml(info.text)}</span>`;
}

function renderCard(card: Card): HTMLElement {
  const el = document.createElement("div");
  el.className = "card";
  // Native draggable is intentionally OFF. WebKitGTK (the Linux webview) does
  // not fire native HTML5 drag events, so the board drives dragging with
  // pointer events instead (beginPointerDrag), which works in every webview.
  // Disabling native drag also stops it competing with the pointer handler on
  // macOS/Windows.
  el.setAttribute("draggable", "false");
  el.dataset.key = card.key;
  el.dataset.stage = card.stage;
  if (card.degraded) el.classList.add("degraded");
  if (card.notMine) el.classList.add("not-mine");

  const meta: string[] = [];
  if (card.degraded) {
    // Markers could not be read this refresh — badge it distinctly so a
    // transient fetch failure is never mistaken for idle, actionable work
    // (1700). It self-heals on the next successful scan.
    meta.push(
      `<span class="badge degraded" title="Markers could not be read this refresh — the daemon will retry.">unreadable</span>`,
    );
  }
  if (stagesLoading) {
    // Titles are shown but this card's real stage is still being derived from
    // comments; a resolving spinner signals it may still move columns.
    meta.push(`<span class="badge resolving" title="Resolving stage…"><span class="spinner"></span></span>`);
  }
  // Normalize once — consume the session's chosen decision block — and derive
  // both the badge and the red error subtitle from the SAME card, so a card
  // parked on an open decision can never paint the amber badge AND a red failure
  // line at the same time (SC-1301).
  const classCard = chosenOptions.has(card.key) ? { ...card, options: liveOptions(card) } : card;
  const b = badge(classCard);
  if (b) meta.push(b);
  const errText = cardError(classCard);
  // Age pill: how long the finished plan has been sitting in the Engineering
  // backlog — color escalates so rotting plans are visible at a glance.
  const age = ageBadge(card, new Date());
  if (age) {
    const planned = card.stageEnteredAt ? new Date(card.stageEnteredAt).toLocaleDateString() : "";
    meta.push(`<span class="badge ${age.cls}" title="${escapeAttr("planned " + planned)}">${escapeHtml(age.text)}</span>`);
  }
  // A blocked card would otherwise sit in Backlog looking like work nobody
  // picked up. Naming what it waits for is the whole reason the gate can refuse
  // quietly: the card itself carries the explanation.
  if (card.blockers?.length) {
    const keys = card.blockers.join(", ");
    meta.push(
      `<span class="badge blocked" title="${escapeAttr(`Waiting for ${keys} to finish. Remove the link to start this now.`)}">waits for ${escapeHtml(keys)}</span>`,
    );
  }
  // A shipped-partial trace: the card shipped less than its ticket asked, with
  // the rest carried by the named follow-on. Informational (machine register, no
  // spinner, not "your turn") and additive to the stage badge, since it is true
  // at every stage from planning through done (SC-2910).
  if (card.shippedPartial) {
    const follow = card.shippedPartialFollowOn ?? "";
    const text = follow ? `partial scope → ${follow}` : "partial scope";
    meta.push(
      `<span class="badge partial" title="${escapeAttr(
        `This ticket shipped with acceptance criteria deferred${follow ? ` to ${follow}` : ""} — open the card for the deferred list.`,
      )}">${escapeHtml(text)}</span>`,
    );
  }
  if (card.engineeringKey) meta.push(`<span>${escapeHtml(card.engineeringKey)}</span>`);
  if (card.prURL) meta.push(`<a href="${escapeAttr(card.prURL)}" target="_blank">PR</a>`);

  // The H pill marks a hidden card while it is revealed via the header's
  // Unhide toggle — without it, revealed and normal cards would be
  // indistinguishable and re-hiding would feel like cards vanishing at random.
  const hiddenPill = card.hidden
    ? `<span class="hidden-pill" title="Hidden ticket — shown via Unhide">H</span>`
    : "";
  el.innerHTML = `
    <div class="card-key">${escapeHtml(card.key)}</div>
    <div class="card-title" title="${escapeAttr(card.title)}">${hiddenPill}${escapeHtml(card.title)}</div>
    <div class="card-meta">${meta.join("")}</div>
    ${errText ? `<div class="card-error">${escapeHtml(errText)}</div>` : ""}
  `;
  // External links must go through the Wails runtime (see openExternal);
  // the pointerdown filter in beginPointerDrag already exempts anchors.
  el.querySelectorAll<HTMLAnchorElement>(".card-meta a").forEach((a) => {
    a.addEventListener("click", (e: MouseEvent) => {
      e.preventDefault();
      openExternal(a.href);
    });
  });
  el.addEventListener("contextmenu", (e: MouseEvent) => {
    e.preventDefault();
    showCardMenu(card, e.clientX, e.clientY);
  });

  // A degraded card's stage/state may be stale (carried forward from the last
  // known good scan) — never let it be dragged into a transition based on
  // markers we could not actually confirm this refresh (1700).
  if (!card.degraded) beginPointerDrag(el, card);
  return el;
}

// showCardMenu opens the card's right-click menu: the administrative actions
// that are not pipeline transitions. Closing a ticket lives here (not on a
// drop zone) — with deploy auto-closing shipped tickets, a manual close is
// the rare escape hatch for abandoned work.
function showCardMenu(card: Card, x: number, y: number): void {
  document.querySelector(".context-menu")?.remove();

  const menu = document.createElement("div");
  menu.className = "context-menu";

  const openItem = document.createElement("button");
  openItem.type = "button";
  openItem.className = "context-menu-item";
  openItem.textContent = "Open in tracker";
  openItem.disabled = !card.url;
  openItem.addEventListener("click", () => {
    menu.remove();
    openExternal(card.url);
  });
  menu.appendChild(openItem);

  if (card.degraded) {
    // A card whose markers could not be read has no reliable stage/state, so
    // every pipeline/launch action is suppressed until a healthy refresh
    // (1700) — only "Open in tracker" is offered.
    menu.style.left = `${x}px`;
    menu.style.top = `${y}px`;
    document.body.appendChild(menu);
    // Keep the menu on-screen when opened near the window edge.
    const r = menu.getBoundingClientRect();
    if (r.right > window.innerWidth) menu.style.left = `${x - r.width}px`;
    if (r.bottom > window.innerHeight) menu.style.top = `${y - r.height}px`;

    const dismiss = (): void => {
      menu.remove();
      document.removeEventListener("pointerdown", onDown, true);
      document.removeEventListener("keydown", onKey, true);
    };
    const onDown = (e: PointerEvent): void => {
      if (!menu.contains(e.target as Node)) dismiss();
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") dismiss();
    };
    document.addEventListener("pointerdown", onDown, true);
    document.addEventListener("keydown", onKey, true);
    return;
  }

  // A dead fix run leaves a bug card failed with no pipeline gesture to try
  // again — the Fix column only accepts grid and rework drops, and a card
  // cannot be dropped onto the column it already sits in. Retry is therefore
  // a menu action. Relaunching runs an agent — same Docker gate as the drops.
  if ((card.bug || card.security) && card.state === "failed") {
    const retryItem = document.createElement("button");
    retryItem.type = "button";
    retryItem.className = "context-menu-item";
    retryItem.textContent = "Retry fix";
    retryItem.disabled = !current.dockerAvailable;
    if (retryItem.disabled) retryItem.title = "Docker required";
    retryItem.addEventListener("click", () => {
      menu.remove();
      void (card.security ? fixSecurity : fixBug)(card.key, card.title);
    });
    menu.appendChild(retryItem);
  }

  // A freshly filed bug's related-work triage runs automatically, but a
  // sweep-filed bug (or one whose run died halfway) carries no completed record —
  // offer the triage on demand. Suppressed once a completed record exists.
  // Relaunching runs an agent, so it is Docker-gated like its siblings (SC-2405).
  if (card.bug && !card.hasRelatedRecord) {
    const relateItem = document.createElement("button");
    relateItem.type = "button";
    relateItem.className = "context-menu-item";
    relateItem.textContent = "Find related work";
    relateItem.disabled = !current.dockerAvailable;
    if (relateItem.disabled) relateItem.title = "Docker required";
    relateItem.addEventListener("click", () => {
      menu.remove();
      void findRelatedWork(card.key, card.title);
    });
    menu.appendChild(relateItem);
  }

  // A failed planning run leaves the card in Engineering with no pipeline gesture
  // to try again — it cannot be dropped onto the column it already sits in
  // (mirrors the Retry-fix rationale above). Relaunch runs an agent: same Docker
  // gate as the drops. from="backlog" reproduces the original launch semantics;
  // the daemon re-derives the real stage and ignores from except for ideas, so
  // the value is inert for validation.
  if (!card.bug && !card.security && card.stage === "planning" && card.state === "failed") {
    const retryPlan = document.createElement("button");
    retryPlan.type = "button";
    retryPlan.className = "context-menu-item";
    retryPlan.textContent = "Retry plan";
    retryPlan.disabled = !current.dockerAvailable;
    if (retryPlan.disabled) retryPlan.title = "Docker required";
    retryPlan.addEventListener("click", () => {
      menu.remove();
      void transition(card.key, card.title, "backlog", "planning");
    });
    menu.appendChild(retryPlan);
  }

  // A finished plan can rot while the ticket waits in the Engineering
  // backlog — code moves on, the plan doesn't. Replan relaunches /human-plan
  // in place; the fresh plan comment supersedes the old one (latest wins).
  // Same wire shape as Retry plan: from is inert for validation.
  if (isReplannable(card)) {
    const replanItem = document.createElement("button");
    replanItem.type = "button";
    replanItem.className = "context-menu-item";
    replanItem.textContent = "Replan";
    replanItem.disabled = !current.dockerAvailable;
    if (replanItem.disabled) replanItem.title = "Docker required";
    replanItem.addEventListener("click", () => {
      menu.remove();
      void transition(card.key, card.title, "backlog", "planning");
    });
    menu.appendChild(replanItem);
  }

  // A failed build is otherwise a dead end on the workflow board: the rework
  // re-drop requires a failed REVIEW verdict and Retry fix is bug-pane-only
  // (SC-591). Mirrors Retry plan: relaunch runs an agent, same Docker gate;
  // from="planning" is inert for validation (the daemon re-derives the stage).
  if (!card.bug && !card.security && card.stage === "implementation" && card.state === "failed") {
    const retryBuild = document.createElement("button");
    retryBuild.type = "button";
    retryBuild.className = "context-menu-item";
    retryBuild.textContent = "Retry build";
    retryBuild.disabled = !current.dockerAvailable;
    if (retryBuild.disabled) retryBuild.title = "Docker required";
    retryBuild.addEventListener("click", () => {
      menu.remove();
      void transition(card.key, card.title, "planning", "implementation");
    });
    menu.appendChild(retryBuild);
  }

  // A failed review was a dead end: the rework re-drop requires a DONE
  // verification with a failing verdict, so a review that failed its binding
  // gate (missing branch, unreachable commits) had no gesture to try again
  // (SC-695). Mirrors Retry build — relaunch runs an agent in place, same Docker
  // gate; the daemon re-derives the stage and re-binds the handoff.
  if (!card.bug && !card.security && isReviewRetryable(card)) {
    const retryReview = document.createElement("button");
    retryReview.type = "button";
    retryReview.className = "context-menu-item";
    retryReview.textContent = "Retry review";
    retryReview.disabled = !current.dockerAvailable;
    if (retryReview.disabled) retryReview.title = "Docker required";
    retryReview.addEventListener("click", () => {
      menu.remove();
      void transition(card.key, card.title, "verification", "verification");
    });
    menu.appendChild(retryReview);
  }

  // A resolved card — the pipeline concluded there is nothing to do, or no fix
  // is needed — had no gesture at all. The retry paths key on failed or outage
  // and the forward path requires done, so overruling a verdict meant editing
  // the tracker by hand. Re-opening goes through its own daemon entry point
  // rather than a transition, because the automatic relaunch drives the same
  // same-stage request and the machine must never re-run its own clean terminal.
  if (isReopenable(card)) {
    const reopen = document.createElement("button");
    reopen.type = "button";
    reopen.className = "context-menu-item";
    reopen.textContent = "Re-open";
    reopen.title = "The pipeline resolved this without a change — run the stage again";
    reopen.disabled = !current.dockerAvailable;
    if (reopen.disabled) reopen.title = "Docker required";
    reopen.addEventListener("click", () => {
      menu.remove();
      void runGuardedAction(
        () => go().Reopen(card.key, card.title, card.stage),
        (err) => showError(errMessage(err)),
        reconcile,
      );
    });
    menu.appendChild(reopen);
  }

  // A failed deploy is otherwise a dead end: the Deploy zone only accepts
  // stage "verification" (SC-297's isReadyToDeploy), and a deploy failure
  // parks the card at stage "done"/state "failed" with no forward neighbor
  // to re-enter through. Applies to bugs too — they ship through this same
  // transition. Deploying launches no agent, so no Docker gate.
  if (card.stage === "done" && card.state === "failed") {
    const retryDeploy = document.createElement("button");
    retryDeploy.type = "button";
    retryDeploy.className = "context-menu-item";
    retryDeploy.textContent = "Retry deploy";
    retryDeploy.addEventListener("click", () => {
      menu.remove();
      void transition(card.key, card.title, "verification", "done");
    });
    menu.appendChild(retryDeploy);
  }

  // Mockups belong to the product conversation: the item appears only in the
  // Product backlog column, toggling create → creating → view as the local
  // mockup set for this ticket comes into existence. Bug tickets never get
  // one — a defect has no product surface to mock.
  if (queueOf(card) === "product" && !card.bug && !card.security) {
    const mockItem = document.createElement("button");
    mockItem.type = "button";
    mockItem.className = "context-menu-item";
    if (card.mockupState === "ready") {
      mockItem.textContent = "View mocks";
      mockItem.addEventListener("click", () => {
        menu.remove();
        setPendingMockupSlug(card.mockupSlug ?? "");
        setChosenMockup(card.mockupChosenSlug ?? "", card.mockupChosenFile ?? "");
        selectView("mockups");
      });
    } else if (card.mockupState === "creating") {
      mockItem.textContent = "Creating mocks…";
      mockItem.disabled = true;
    } else {
      mockItem.textContent = "Create mocks";
      // Generation launches a containerized agent — same Docker gate as the
      // pipeline drop targets.
      mockItem.disabled = !current.dockerAvailable;
      if (mockItem.disabled) mockItem.title = "Docker required";
      mockItem.addEventListener("click", () => {
        menu.remove();
        void createMocks(card);
      });
    }
    menu.appendChild(mockItem);
  }

  // Hiding is view hygiene, not ticket lifecycle: parked noise disappears
  // from the board while the ticket on the tracker stays untouched.
  const hideItem = document.createElement("button");
  hideItem.type = "button";
  hideItem.className = "context-menu-item";
  hideItem.textContent = card.hidden ? "Unhide ticket" : "Hide ticket";
  hideItem.addEventListener("click", () => {
    menu.remove();
    toggleCardHidden(card);
  });
  menu.appendChild(hideItem);

  const closeItem = document.createElement("button");
  closeItem.type = "button";
  closeItem.className = "context-menu-item danger";
  closeItem.textContent = "Close ticket";
  closeItem.addEventListener("click", () => {
    menu.remove();
    void requestClose(card.key, card.title, card.state === "running");
  });
  menu.appendChild(closeItem);

  menu.style.left = `${x}px`;
  menu.style.top = `${y}px`;
  document.body.appendChild(menu);
  // Keep the menu on-screen when opened near the window edge.
  const r = menu.getBoundingClientRect();
  if (r.right > window.innerWidth) menu.style.left = `${x - r.width}px`;
  if (r.bottom > window.innerHeight) menu.style.top = `${y - r.height}px`;

  const dismiss = (): void => {
    menu.remove();
    document.removeEventListener("pointerdown", onDown, true);
    document.removeEventListener("keydown", onKey, true);
  };
  const onDown = (e: PointerEvent): void => {
    if (!menu.contains(e.target as Node)) dismiss();
  };
  const onKey = (e: KeyboardEvent): void => {
    if (e.key === "Escape") dismiss();
  };
  document.addEventListener("pointerdown", onDown, true);
  document.addEventListener("keydown", onKey, true);
}

function renderColumn(queue: string): HTMLElement {
  const col = document.createElement("section");
  col.className = "column";
  col.dataset.stage = queue;

  // Bug tickets live in the Bugs pane, never in the workflow columns; hidden
  // tickets only render while revealed. The saved hand order sorts what's left.
  const cards = current.cards.filter((c) => queueOf(c) === queue && !c.bug && !c.security && cardVisible(c));
  sortByHandOrder(cards, current.columnOrder?.[queue]);

  const header = document.createElement("div");
  header.className = "column-header";
  if (queue === "product") {
    header.innerHTML =
      `<span>${QUEUE_LABELS[queue]}</span>` +
      `<button class="add-card" title="New ticket via ideation">+</button>` +
      `<span class="column-count">${cards.length}</span>`;
    header.querySelector(".add-card")!.addEventListener("click", () => void openIdeation());
  } else {
    header.innerHTML = `<span>${QUEUE_LABELS[queue]}</span><span class="column-count">${cards.length}</span>`;
  }
  col.appendChild(header);

  const body = document.createElement("div");
  body.className = "column-body";
  for (const card of cards) body.appendChild(renderCard(card));

  // Every column is at least a same-column sort target; product additionally
  // accepts idea promotion and the transition queues launch stages. Dropping
  // INTO Ready to Deploy stays impossible (dropAllowed gates it) — cards
  // arrive there only by passing review, but sorting within it is fine.
  markQueueTarget(body, queue);

  col.appendChild(body);
  return col;
}

// renderIdeaSpace builds the Ideas queue as five gradient sub-columns (the
// idea space). It replaces renderColumn("ideas"): one shared header keeps the
// familiar Ideas title, `+` quick-add and total count, while each sub-column
// is a local-reorder drop target (data-drop="idea") — dropping there saves a
// placement, it never launches an agent, so no Docker gate applies.
function renderIdeaSpace(): HTMLElement {
  const space = document.createElement("section");
  // data-stage="ideas" keeps the theme hooks that key off the Ideas column
  // (fancy tint, clear sweep) anchored to the space as a whole.
  space.className = "idea-space";
  space.dataset.stage = "ideas";

  const ideas = current.cards.filter((c) => queueOf(c) === "ideas" && !c.bug && !c.security && cardVisible(c));

  const grid = document.createElement("div");
  grid.className = "idea-space-grid";
  const subcols: HTMLElement[] = [];
  for (let i = 0; i < IDEA_COL_COUNT; i++) {
    const col = document.createElement("section");
    col.className = "column idea-subcol";
    // Distinct per-sub-column stage keys let scroll capture/restore treat each
    // sub-column independently across rebuilds.
    col.dataset.stage = `ideas:${i}`;

    const colCards = ideas.filter((c) => ideaColOf(c) === i);
    const body = document.createElement("div");
    body.className = "column-body";
    body.dataset.drop = "idea";
    body.dataset.ideaCol = String(i);
    // The drop-ok overlay renders data-verb; without one it would show an
    // empty dashed box. Sorting is the verb of this space.
    body.dataset.verb = "Sort here";
    for (const card of colCards) body.appendChild(renderCard(card));
    if (i === 0) {
      // Quick-add writes into the leftmost sub-column, so captures awaiting
      // their ticket number sit on top of it — where the input just was.
      for (const idea of pendingIdeas) body.prepend(renderPendingCard(idea.title));
    }
    col.appendChild(body);

    subcols.push(col);
    grid.appendChild(col);
  }

  const header = document.createElement("div");
  header.className = "column-header idea-space-header";
  // Ideas capture is deliberately dumb: a title becomes a labeled ticket in
  // one keystroke — the thinking happens later, at promotion. New ideas are
  // loose by definition, so quick-add writes into the leftmost sub-column.
  header.innerHTML =
    `<span>${QUEUE_LABELS["ideas"]}</span>` +
    `<button class="add-card" title="Capture an idea">+</button>` +
    `<span class="column-count">${ideas.length + pendingIdeas.length}</span>`;
  header.querySelector(".add-card")!.addEventListener("click", () => showIdeaQuickAdd(subcols[0]));

  space.appendChild(header);
  space.appendChild(grid);
  return space;
}

// renderDeployControl builds a pane's Deploy control through the shared builder:
// a single widget that both accepts a dropped ready card (ships just that one)
// and, on click, ships every deployable card in the pane. It is deliberately NOT
// a column — no card ever rests "in Deploy"; a shipped card leaves the board.
function renderDeployControl(side: DeploySide): HTMLElement {
  const view = deployControlView(current.cards, side);
  const className = side === "defects" ? "defect-deploy" : "deploy-zone";
  return buildDeployControl(view, { className, onClick: () => void deployReady(side) });
}

// --- Bugs pane ----------------------------------------------------------
//
// Bug tickets get their own view: a wide grid of open bugs (five rows tall,
// cards flowing horizontally — a fill-up tray, not the idea space's sorted
// lanes), a red-bordered Fix activity column, and a Deploy button. The stage
// semantics are the board's own: dropping a bug on Fix launches the
// autonomous fix (autofix triages + plans itself, so no planning gate), the
// fix chains into its review, and Deploy ships every bug whose review passed.

// bugAreaOf places a bug card in the pane: "fix" while the fix cycle owns it
// (build + review, mirroring the Code column), "ready" once its review passed
// or it is deploying, "grid" while it rests unfixed.
function bugAreaOf(card: Card): "grid" | "fix" | "ready" {
  const q = queueOf(card);
  if (q === "building") return "fix";
  if (q === "deploy") return "ready";
  return "grid";
}

// renderBugCard wraps renderCard with the pane-specific wording: the fix
// cycle runs the board's implementation stage, but here the activity is
// "fixing", and a review that passed reads "fixed" — in this pane the
// statement that matters is bug-language, not queue position (the card stays
// in the Fix column until deployed).
function renderBugCard(card: Card): HTMLElement {
  const el = renderCard(card);
  if (card.state === "running" && card.stage === "implementation") {
    const running = el.querySelector(".badge.running");
    if (running) running.innerHTML = `<span class="spinner"></span> fixing…`;
  }
  if (card.state === "failed") {
    // The board's bare ✕ is too quiet for this pane: a dead fix run must say
    // so, with the recorded reason a hover away.
    const failed = el.querySelector<HTMLElement>(".badge.failed");
    if (failed) {
      failed.textContent = "✕ error";
      if (card.error) failed.title = card.error;
    }
  }
  // SC-429: the board's neutral "awaiting review…" reads as bug-language here —
  // the fix is done, it is just waiting for its review to start.
  if (card.stage === "implementation" && card.state === "done") {
    const awaiting = el.querySelector<HTMLElement>(".badge.await");
    if (awaiting) {
      awaiting.textContent = "fixed, awaiting review…";
      awaiting.title = "Fix complete — waiting for review to start";
    }
  }
  if (isReadyToDeploy(card)) {
    const meta = el.querySelector(".card-meta");
    if (meta) {
      const chip = document.createElement("span");
      chip.className = "badge done";
      chip.title = "Fix reviewed and ready to deploy";
      chip.textContent = "fixed ✓";
      meta.prepend(chip);
    }
  }
  return el;
}

// FixSection describes one half of the Bugs & Security view. Both halves share
// the grid + Fix columns (renderBugCard and bugAreaOf are kind-agnostic — they
// speak the fix cycle, not "bug") and a single full-height Deploy control to
// their right; the config captures only what differs: which cards the half owns,
// its header, the "+" dialog, the pending-placeholder list, and the pipeline the
// Fix drop launches.
interface FixSection {
  match: (c: Card) => boolean;
  gridStage: string;
  fixStage: string;
  gridColClass: string;
  fixColClass: string;
  headerHTML: (count: number) => string;
  wireHeader: (gridCol: HTMLElement) => void;
  emptyText: string;
  pending: () => { title: string; description: string }[];
  // dataset.drop value the Fix column carries — the drop/gate logic routes on
  // it so a bug drop launches the autofix and a security drop the security fix.
  dropTarget: string;
}

const bugSection: FixSection = {
  match: (c) => !!c.bug,
  gridStage: "bugs:grid",
  fixStage: "bugs:fix",
  gridColClass: "bug-grid-col",
  fixColClass: "bug-fix-col",
  headerHTML: (count) => bugsHeaderHTML(findbugsHunting, count),
  wireHeader: (gridCol) => {
    gridCol.querySelector(".add-card")!.addEventListener("click", () => showBugModal());
    gridCol.querySelector(".findbugs-btn")?.addEventListener("click", () => void startFindbugs());
  },
  emptyText: "No open bugs",
  pending: () => pendingBugs,
  dropTarget: "fix",
};

const securitySection: FixSection = {
  match: (c) => !!c.security,
  gridStage: "security:grid",
  fixStage: "security:fix",
  gridColClass: "security-grid-col",
  fixColClass: "security-fix-col",
  headerHTML: (count) => securityHeaderHTML(securityHunting, count),
  wireHeader: (gridCol) => {
    gridCol.querySelector(".add-card")!.addEventListener("click", () => showSecurityModal());
    gridCol.querySelector(".findsecurity-btn")?.addEventListener("click", () => void startFindsecurity());
  },
  emptyText: "No open security issues",
  pending: () => pendingSecurity,
  dropTarget: "security-fix",
};

// gardeningSection is the board's third row (SC-1638): same pane + Fix-column
// grammar as Bugs and Security, but deliberately inert. `match` never claims a
// card, so the grid renders empty and the count stays 0; `wireHeader` is a
// no-op (the header has no sweep or quick-add); and `dropTarget` is a value no
// drop gate recognizes, so dropAllowed() rejects every drop on its Fix column.
// Wiring findings, a sweep, and drops into this row is a follow-up.
const gardeningSection: FixSection = {
  match: () => false,
  gridStage: "gardening:grid",
  fixStage: "gardening:fix",
  gridColClass: "gardening-grid-col",
  fixColClass: "gardening-fix-col",
  headerHTML: (count) => gardeningHeaderHTML(count),
  wireHeader: () => {},
  emptyText: "No gardening items",
  pending: () => [],
  dropTarget: "gardening-fix",
};

// renderBugs paints the three rows of the Bugs & Security view top-to-bottom:
// bugs, security, then the inert Gardening row (SC-1638). Every reconcile
// rebuilds all three from the same card list.
function renderBugs(): void {
  const host = document.getElementById("bugs");
  if (!host) return;
  const scrollByStage = captureColumnScroll(host);
  host.innerHTML = "";
  renderFixSection(host, bugSection);
  renderFixSection(host, securitySection);
  renderFixSection(host, gardeningSection);
  // One Deploy control spans all three rows at full height (CSS grid area
  // "deploy"). It ships fixed bugs AND vulnerabilities: the drop is kind-
  // agnostic (a ready card of either kind transitions to done), and the click
  // ships every ready defect at once. The Gardening row is inert, so it never
  // contributes a ready card here.
  host.appendChild(renderDeployControl("defects"));
  restoreColumnScroll(host, scrollByStage);
}

// renderFixSection appends one half's grid + Fix column to the host. The shared
// Deploy control is appended once by renderBugs, spanning both halves; the Fix
// column's drop target is per-kind so a dropped card launches the right skill
// (bug → /human-autofix, security → /human-security-fix).
function renderFixSection(host: HTMLElement, section: FixSection): void {
  const cards = current.cards.filter((c) => section.match(c) && cardVisible(c));
  const gridCards = cards.filter((c) => bugAreaOf(c) === "grid");
  const fixCards = cards.filter((c) => bugAreaOf(c) !== "grid");
  const pending = section.pending();

  const gridCol = document.createElement("section");
  gridCol.className = `column ${section.gridColClass}`;
  gridCol.dataset.stage = section.gridStage;
  gridCol.innerHTML = section.headerHTML(gridCards.length + pending.length);
  section.wireHeader(gridCol);
  const gridBody = document.createElement("div");
  gridBody.className = "column-body bug-grid";
  if (cards.length === 0 && pending.length === 0) {
    gridBody.innerHTML = `<div class="bug-grid-empty">${section.emptyText}</div>`;
  } else {
    for (const b of pending) gridBody.appendChild(renderPendingCard(b.title));
    for (const card of gridCards) gridBody.appendChild(renderBugCard(card));
  }
  gridCol.appendChild(gridBody);

  const fixCol = document.createElement("section");
  fixCol.className = `column ${section.fixColClass}`;
  fixCol.dataset.stage = section.fixStage;
  fixCol.innerHTML =
    `<div class="column-header"><span>Fix</span><span class="column-count">${fixCards.length}</span></div>`;
  const fixBody = document.createElement("div");
  fixBody.className = "column-body";
  fixBody.dataset.drop = section.dropTarget;
  fixBody.dataset.verb = "Fix it";
  for (const card of fixCards) fixBody.appendChild(renderBugCard(card));
  fixCol.appendChild(fixBody);

  host.appendChild(gridCol);
  host.appendChild(fixCol);
}

// fixBug launches the autonomous fix pipeline on one bug. Optimistic move into
// the Fix column, same shape as transition(): the daemon is authoritative and
// the reconcile corrects any lie.
async function fixBug(key: string, title: string): Promise<void> {
  const card = current.cards.find((c) => c.key === key);
  const prevStage = card?.stage;
  const prevState = card?.state;
  if (card) {
    card.stage = "implementation";
    card.state = "running";
    // Shield the optimistic move from a stale reconcile that predates it
    // (SC-2521), same as a drag; replace any prior record for this key.
    if (prevStage !== undefined) {
      pendingMoves = dropPendingMove(pendingMoves, key);
      pendingMoves.push({
        key,
        fromStage: prevStage,
        toStage: "implementation",
        expiresAt: Date.now() + PENDING_MOVE_TTL_MS,
      });
    }
    render();
  }
  await runGuardedAction(
    () => go().FixBug(key, title),
    (err) => {
      // Revert the optimistic move so a failed launch doesn't leave the card
      // looking like it's running when it never started (SC-637), and drop the
      // shield so the next reconcile isn't pinned to the reverted move.
      if (card && prevStage !== undefined && prevState !== undefined) {
        card.stage = prevStage;
        card.state = prevState;
      }
      pendingMoves = dropPendingMove(pendingMoves, key);
      showError(errMessage(err));
    },
    reconcile,
  );
}

// findRelatedWork launches the on-demand related-work triage for one bug — the
// Bugs pane's card action for a bug carrying no completed record. No optimistic
// move: the run is advisory and never changes the card's stage, so there is
// nothing to shield or revert; the daemon is authoritative and the record lands
// on the card on a later reconcile.
async function findRelatedWork(key: string, title: string): Promise<void> {
  await runGuardedAction(
    () => go().FindRelatedWork(key, title),
    (err) => showError(errMessage(err)),
    reconcile,
  );
}

// fixSecurity launches the security-fix pipeline on one security ticket — the
// Security half's counterpart to fixBug, same optimistic move into the Fix
// column and the same daemon-is-authoritative contract.
async function fixSecurity(key: string, title: string): Promise<void> {
  const card = current.cards.find((c) => c.key === key);
  const prevStage = card?.stage;
  const prevState = card?.state;
  if (card) {
    card.stage = "implementation";
    card.state = "running";
    // Shield the optimistic move from a stale reconcile that predates it
    // (SC-2521), same as a drag; replace any prior record for this key.
    if (prevStage !== undefined) {
      pendingMoves = dropPendingMove(pendingMoves, key);
      pendingMoves.push({
        key,
        fromStage: prevStage,
        toStage: "implementation",
        expiresAt: Date.now() + PENDING_MOVE_TTL_MS,
      });
    }
    render();
  }
  await runGuardedAction(
    () => go().FixSecurity(key, title),
    (err) => {
      // Revert the optimistic move so a failed launch doesn't leave the card
      // looking like it's running when it never started (SC-637), and drop the
      // shield so the next reconcile isn't pinned to the reverted move.
      if (card && prevStage !== undefined && prevState !== undefined) {
        card.stage = prevStage;
        card.state = prevState;
      }
      pendingMoves = dropPendingMove(pendingMoves, key);
      showError(errMessage(err));
    },
    reconcile,
  );
}

// Bugs filed from the pane but not yet confirmed by a board fetch — the bug
// grid's counterpart to pendingIdeas, with the same handover rule: the
// placeholder clears when a fetched bug card's key matches the one captured
// from the create response (SC-1691); title is only a fallback for a
// placeholder that never captured a key.
let pendingBugs: (Pending & { description: string })[] = [];

// Security tickets filed from the Security half but not yet confirmed by a
// board fetch — the Security counterpart to pendingBugs, same handover rule.
let pendingSecurity: (Pending & { description: string })[] = [];

// Cards dragged to a new stage but not yet confirmed by a board fetch — the
// move-side counterpart to the pending* placeholders above (SC-2521). A drop
// moves the card in memory at once; the next reconcile can fetch a board that
// predates the move becoming readable and would snap the card back. Each record
// holds the move against exactly that stale read (fetch still at fromStage)
// while yielding to a confirmed target, a daemon-chosen third stage, or expiry.
// See board-pending-move.ts for the discrimination rules.
let pendingMoves: PendingMove[] = [];

// Wall-clock lifetime of a pending-move shield: comfortably above the observed
// ~2s read-after-write lag, yet bounded so a move that never confirms yields to
// the truth rather than showing a comfortable lie.
const PENDING_MOVE_TTL_MS = 20_000;

// True while a findbugs sweep is running for the project — drives the Bugs
// pane's hunt indicator. Refreshed in reconcile() and set optimistically on a
// Findbugs click so the button responds instantly.
let findbugsHunting = false;

// True while a human-security sweep is running for the project — drives the
// Security pane's scan indicator. Refreshed in reconcile() and set
// optimistically on a Find Security click so the button responds instantly.
let securityHunting = false;

// showBugModal opens the file-a-bug dialog: a title and a free-text
// description. Filing is optimistic like the idea quick-add — the placeholder
// card appears immediately; a failed create reopens the dialog with the text
// intact so nothing typed is lost.
function showBugModal(prefillTitle = "", prefillDescription = ""): void {
  const overlay = document.createElement("div");
  overlay.className = "modal-overlay";
  const modal = document.createElement("div");
  modal.className = "modal bug-modal";
  modal.innerHTML = `
    <div class="modal-title">File a bug</div>
    <input class="modal-input" type="text" placeholder="What is broken?" />
    <textarea class="modal-textarea" rows="6" placeholder="What did you see, what did you expect?"></textarea>
    <div class="modal-actions">
      <button class="modal-cancel" type="button">Cancel</button>
      <button class="modal-confirm" type="button">Create bug</button>
    </div>
  `;
  overlay.appendChild(modal);
  document.body.appendChild(overlay);

  const titleInput = modal.querySelector(".modal-input") as HTMLInputElement;
  const descInput = modal.querySelector(".modal-textarea") as HTMLTextAreaElement;
  const confirm = modal.querySelector(".modal-confirm") as HTMLButtonElement;
  titleInput.value = prefillTitle;
  descInput.value = prefillDescription;

  const close = (): void => overlay.remove();
  overlay.addEventListener("click", (e) => {
    if (e.target === overlay) close();
  });
  modal.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Escape") close();
  });
  modal.querySelector(".modal-cancel")!.addEventListener("click", close);
  confirm.addEventListener("click", () => {
    const title = titleInput.value.trim();
    if (!title) {
      titleInput.focus();
      return;
    }
    const description = descInput.value.trim();
    close();
    void createBug(title, description);
  });
  titleInput.focus();
}

// createBug files the ticket and keeps the grid honest: placeholder first,
// rollback + reopened dialog on failure (same contract as CreateIdea).
async function createBug(title: string, description: string): Promise<void> {
  const pending: Pending & { description: string } = { title, description };
  pendingBugs.push(pending);
  render();
  try {
    pending.key = await go().CreateBug(title, description);
  } catch (err) {
    // The ticket does not exist, so the placeholder must not pretend it
    // does — give the text back to the dialog instead.
    pendingBugs = dropPending(pendingBugs, pending);
    showError(errMessage(err));
    showBugModal(title, description);
    return;
  }
  // Invalidate fetches already in flight — their pre-create snapshot would
  // miss the new ticket (same guard as CreateIdea).
  reconcileEpoch++;
  await reconcile();
}

// showSecurityModal opens the file-a-security-issue dialog — the Security
// half's counterpart to showBugModal, same optimistic-filing contract (the
// placeholder appears at once; a failed create reopens the dialog intact).
function showSecurityModal(prefillTitle = "", prefillDescription = ""): void {
  const overlay = document.createElement("div");
  overlay.className = "modal-overlay";
  const modal = document.createElement("div");
  modal.className = "modal bug-modal";
  modal.innerHTML = `
    <div class="modal-title">File a security issue</div>
    <input class="modal-input" type="text" placeholder="What is the vulnerability?" />
    <textarea class="modal-textarea" rows="6" placeholder="What is exposed, and how could it be exploited?"></textarea>
    <div class="modal-actions">
      <button class="modal-cancel" type="button">Cancel</button>
      <button class="modal-confirm" type="button">Create security issue</button>
    </div>
  `;
  overlay.appendChild(modal);
  document.body.appendChild(overlay);

  const titleInput = modal.querySelector(".modal-input") as HTMLInputElement;
  const descInput = modal.querySelector(".modal-textarea") as HTMLTextAreaElement;
  const confirm = modal.querySelector(".modal-confirm") as HTMLButtonElement;
  titleInput.value = prefillTitle;
  descInput.value = prefillDescription;

  const close = (): void => overlay.remove();
  overlay.addEventListener("click", (e) => {
    if (e.target === overlay) close();
  });
  modal.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Escape") close();
  });
  modal.querySelector(".modal-cancel")!.addEventListener("click", close);
  confirm.addEventListener("click", () => {
    const title = titleInput.value.trim();
    if (!title) {
      titleInput.focus();
      return;
    }
    const description = descInput.value.trim();
    close();
    void createSecurity(title, description);
  });
  titleInput.focus();
}

// createSecurity files the security ticket and keeps the grid honest:
// placeholder first, rollback + reopened dialog on failure (mirrors createBug).
async function createSecurity(title: string, description: string): Promise<void> {
  const pending: Pending & { description: string } = { title, description };
  pendingSecurity.push(pending);
  render();
  try {
    pending.key = await go().CreateSecurity(title, description);
  } catch (err) {
    pendingSecurity = dropPending(pendingSecurity, pending);
    showError(errMessage(err));
    showSecurityModal(title, description);
    return;
  }
  reconcileEpoch++;
  await reconcile();
}

// startFindbugs launches the autonomous bug sweep. Optimistic like fixBug: flip
// the hunt indicator on immediately, then let the daemon-backed status poll in
// reconcile() own the truth (it clears when the sweep's pipeline state is gone).
async function startFindbugs(): Promise<void> {
  findbugsHunting = true;
  render();
  try {
    await go().FindBugs();
  } catch (err) {
    findbugsHunting = false;
    showError(errMessage(err));
    render();
    return;
  }
  await reconcile();
}

// startFindsecurity launches the autonomous vulnerability scan — the Security
// counterpart to startFindbugs. Optimistic: flip the scan indicator on
// immediately, then let the status poll in reconcile() own the truth.
async function startFindsecurity(): Promise<void> {
  securityHunting = true;
  render();
  try {
    await go().FindSecurity();
  } catch (err) {
    securityHunting = false;
    showError(errMessage(err));
    render();
    return;
  }
  await reconcile();
}

// deployReady ships every review-passed card in a pane at once — the Deploy
// click for both the board (features) and the Bugs pane. The click is the
// consent — same rule as the Deploy drop — and CI still gates each merge
// server-side. Transitions run sequentially with one reconcile at the end so a
// multi-card ship does not race itself.
async function deployReady(side: DeploySide): Promise<void> {
  const ready = deployableCards(current.cards, side);
  if (ready.length === 0) return;
  const prior = new Map(ready.map((c) => [c.key, { stage: c.stage, state: c.state }]));
  // Shield the whole batch from a stale closing reconcile the same way a single
  // drag is shielded (SC-2521): the ship's one reconcile() races the tracker's
  // read-after-write lag, and without a pending-move record per card that stale
  // fetch snaps every just-shipped card back to its origin. Built from the
  // origin stages BEFORE the mutation loop rewrites them to "done".
  const shipMoves = pendingMovesForBatch(ready, "done", Date.now(), PENDING_MOVE_TTL_MS);
  for (const m of shipMoves) pendingMoves = dropPendingMove(pendingMoves, m.key);
  pendingMoves.push(...shipMoves);
  for (const card of ready) {
    card.stage = "done";
    card.state = "running";
  }
  render();
  let hadError = false;
  for (const card of ready) {
    try {
      await go().Transition(card.key, card.title, "verification", "done");
    } catch (err) {
      hadError = true;
      // Revert only this card's optimistic move — cards that already
      // transitioned successfully keep their optimistic "running" state.
      const prev = prior.get(card.key);
      if (prev) {
        card.stage = prev.stage;
        card.state = prev.state;
      }
      // Drop this card's shield so the next reconcile isn't pinned to a ship
      // that never took; the cards that shipped keep theirs until confirmed.
      pendingMoves = dropPendingMove(pendingMoves, card.key);
      showError(errMessage(err));
    }
  }
  if (hadError) {
    // A reconcile here would overwrite the just-shown failure with the
    // (empty) fetch error, wiping the banner in the same cycle it appeared
    // (SC-637). Successfully-transitioned cards resync on the next natural
    // board:changed event or reconcile instead.
    render();
    return;
  }
  await reconcile();
}

// Ideas captured but not yet confirmed by a board fetch. Each renders as a
// placeholder card the moment Enter is pressed — waiting for the full
// refetch (seconds of comment scanning) would make the capture look lost. An
// entry clears when a fetched Ideas card's key matches the one captured from
// the create response (SC-1691); title is only a fallback for an entry that
// never captured a key, so even a stale in-flight fetch cannot blink the
// capture away.
let pendingIdeas: Pending[] = [];

// showIdeaQuickAdd swaps an inline title input into an idea-space sub-column.
// Enter creates the idea-labeled ticket via CreateIdea; Escape or blur
// dismisses. prefill restores the title after a failed create so the text is
// not lost with the error.
function showIdeaQuickAdd(col: HTMLElement, prefill = ""): void {
  const body = col.querySelector(".column-body");
  if (!body || body.querySelector(".idea-quick-add")) return;
  const input = document.createElement("input");
  input.className = "idea-quick-add";
  input.type = "text";
  input.placeholder = "Idea title — Enter to capture";
  input.value = prefill;
  body.prepend(input);
  input.focus();

  input.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      input.remove();
      return;
    }
    if (e.key !== "Enter") return;
    const title = input.value.trim();
    if (!title) return;
    // The capture is visible immediately as a placeholder card; the ticket
    // number arrives with the next fetch. render() rebuilds the board, which
    // also disposes of the input.
    const pending: Pending = { title };
    pendingIdeas.push(pending);
    render();
    void (async () => {
      try {
        pending.key = await go().CreateIdea(title);
      } catch (err) {
        // The ticket does not exist, so the placeholder must not pretend it
        // does — put the title back into a fresh input instead.
        pendingIdeas = dropPending(pendingIdeas, pending);
        showError(errMessage(err));
        const retryCol = document.querySelector<HTMLElement>(".idea-subcol");
        if (retryCol) showIdeaQuickAdd(retryCol, title);
        return;
      }
      // Invalidate fetches already in flight — their pre-create snapshot
      // would miss the new ticket (same guard as closeTicket).
      reconcileEpoch++;
      await reconcile();
    })();
  });
  input.addEventListener("blur", () => {
    if (!input.disabled && input.value.trim() === "") input.remove();
  });
}

// renderPendingCard builds the placeholder card for a ticket (idea or bug)
// still being created: a spinner sits where the ticket number will land. No
// drag, no menu — there is no ticket to act on yet.
function renderPendingCard(title: string): HTMLElement {
  const el = document.createElement("div");
  el.className = "card pending-idea";
  el.setAttribute("draggable", "false");
  el.innerHTML = `
    <div class="card-key"><span class="spinner"></span></div>
    <div class="card-title">${escapeHtml(title)}</div>
    <div class="card-meta"></div>
  `;
  return el;
}

// --- Pointer-based drag ------------------------------------------------
//
// The board does NOT use native HTML5 drag-and-drop: WebKitGTK (the Linux
// webview backend) does not fire native drag events, so the board would be
// completely undraggable there. Instead the card tracks pointer events itself
// and hit-tests drop targets with elementFromPoint. Drop targets are plain
// elements tagged with data-drop ("queue" | "idea" | "close" | "deploy"); a
// floating ghost (pointer-events:none) follows the cursor.

const DRAG_THRESHOLD_PX = 5;
let dragGhost: HTMLElement | null = null;
let hoverTarget: HTMLElement | null = null;

function markQueueTarget(el: HTMLElement, queue: string): void {
  el.dataset.drop = "queue";
  el.dataset.dropQueue = queue;
  const verb = QUEUE_VERB[queue];
  if (verb) el.dataset.verb = verb;
}

// dropTargetAt returns the drop-target element under a viewport point, if any.
// The ghost has pointer-events:none, so it never occludes the hit-test.
function dropTargetAt(x: number, y: number): HTMLElement | null {
  const el = document.elementFromPoint(x, y) as HTMLElement | null;
  return el ? (el.closest("[data-drop]") as HTMLElement | null) : null;
}

// dropAllowed reports whether the dragged card may drop on target. Queue
// targets keep the forward-adjacency + docker-enabled rules, evaluated on the
// card's RESTING queue so a running card cannot double-launch; the one
// exception is the rework re-drop (flagged card back onto Code). The Deploy
// zone accepts only reviewed cards — and needs no Docker, since deploying
// launches no agent.
function dropAllowed(target: HTMLElement): boolean {
  if (!dragging) return false;
  const card = current.cards.find((c) => c.key === dragging!.key);
  if (!card || card.state === "running") return false;
  if (target.dataset.drop === "idea") {
    // Idea-space sub-columns accept only idea cards, and only into a DIFFERENT
    // sub-column — a same-column drop would be a no-op gesture. Local reorder
    // launches nothing, so the Docker gate does not apply.
    return queueOf(card) === "ideas" && Number(target.dataset.ideaCol) !== ideaColOf(card);
  }
  if (target.dataset.drop === "deploy") return isReadyToDeploy(card);
  if (target.dataset.drop === "fix") {
    // The Fix column accepts a resting bug that is not yet being fixed, plus
    // the rework re-drop on a failing verdict — the same two entry points the
    // Code column has, but for bugs the planning gate does not apply
    // (autofix triages and plans itself). Launching an agent needs Docker.
    if (!card.bug || !current.dockerAvailable) return false;
    return bugAreaOf(card) === "grid" || isReworkable(card);
  }
  if (target.dataset.drop === "security-fix") {
    // The Security half's Fix column, gated exactly like the bug one but for
    // security cards — the security-fix pipeline triages and plans itself too.
    if (!card.security || !current.dockerAvailable) return false;
    return bugAreaOf(card) === "grid" || isReworkable(card);
  }
  const toQueue = target.dataset.dropQueue ?? "";
  // A drop back into the card's own column is a local reorder — it launches
  // nothing, so neither the Docker gate nor forward-adjacency applies.
  if (!card.bug && !card.security && toQueue === queueOf(card)) return true;
  // Ready to Deploy is never a transition target — cards earn their way in by
  // passing review; only the same-column sort above may drop here.
  if (toQueue === "deploy") return false;
  // forwardDropAllowed owns forward-adjacency, the Code rework re-drop, and the
  // Engineering->Code plan-ready gate; targetEnabled keeps the local Docker gate.
  return forwardDropAllowed(card, toQueue) && targetEnabled(toQueue);
}

// setHoverTarget moves the highlight to a new target (clearing the previous),
// so exactly one drop zone is lit at a time.
function setHoverTarget(target: HTMLElement | null): void {
  if (target !== hoverTarget && hoverTarget) {
    hoverTarget.classList.remove("drop-ok", "drop-reject");
  }
  hoverTarget = target;
  if (!target) return;
  const ok = dropAllowed(target);
  target.classList.toggle("drop-ok", ok);
  target.classList.toggle("drop-reject", !ok);
  // The overlay verb must state what the drop DOES: a same-column drop sorts,
  // it never launches, so the transition verb would lie ("Build it" on a card
  // already in Code). Recomputed on every hover since the same body serves both.
  const drag = dragging;
  if (ok && target.dataset.drop === "queue" && drag) {
    const card = current.cards.find((c) => c.key === drag.key);
    const sorting = !!card && target.dataset.dropQueue === queueOf(card);
    const verb = sorting ? "Sort here" : QUEUE_VERB[target.dataset.dropQueue ?? ""];
    if (verb) target.dataset.verb = verb;
    else delete target.dataset.verb;
  }
}

function makeDragGhost(card: { key: string; title: string }): HTMLElement {
  const ghost = document.createElement("div");
  ghost.className = "drag-ghost";
  ghost.innerHTML =
    `<div class="card-key">${escapeHtml(card.key)}</div>` +
    `<div class="card-title">${escapeHtml(card.title)}</div>`;
  document.body.appendChild(ghost);
  return ghost;
}

// beginPointerDrag wires one card's pointer-drag lifecycle. Movement past a
// small threshold starts the drag (so a plain click still activates links);
// releasing over a valid target performs the stage move or close.
function beginPointerDrag(el: HTMLElement, card: Card): void {
  el.addEventListener("pointerdown", (down: PointerEvent) => {
    if (down.button !== 0) return;
    // Let clicks on interactive children (e.g. the PR link) behave normally.
    if ((down.target as HTMLElement).closest("a, button")) return;

    const info = { key: card.key, title: card.title, stage: card.stage };
    let started = false;
    let lastX = down.clientX;

    const onMove = (ev: PointerEvent): void => {
      if (!started) {
        if (Math.hypot(ev.clientX - down.clientX, ev.clientY - down.clientY) < DRAG_THRESHOLD_PX) return;
        started = true;
        dragging = info;
        el.classList.add("dragging");
        dragGhost = makeDragGhost(info);
      }
      if (dragGhost) {
        dragGhost.style.left = `${ev.clientX}px`;
        dragGhost.style.top = `${ev.clientY}px`;
        ghostTilt(dragGhost, ev.clientX - lastX);
        trail({ x: ev.clientX, y: ev.clientY });
      }
      lastX = ev.clientX;
      setHoverTarget(dropTargetAt(ev.clientX, ev.clientY));
    };

    const teardown = (): void => {
      el.removeEventListener("pointermove", onMove);
      el.removeEventListener("pointerup", onUp);
      el.removeEventListener("pointercancel", onCancel);
      try {
        el.releasePointerCapture(down.pointerId);
      } catch {
        // Capture may already be gone; ignore.
      }
      el.classList.remove("dragging");
      if (dragGhost) {
        dragGhost.remove();
        dragGhost = null;
      }
      setHoverTarget(null);
    };

    const onUp = (ev: PointerEvent): void => {
      const target = started ? dropTargetAt(ev.clientX, ev.clientY) : null;
      const allowed = !!target && dropAllowed(target);
      const wasClick = !started;
      teardown();
      endDrag();
      // `target` may have been replaced by the flushed render, but performDrop
      // only reads its dataset, which a detached node still carries.
      if (target && allowed) performDrop(target, info, { x: ev.clientX, y: ev.clientY });
      // A press that never crossed the drag threshold is a plain click. The
      // Product Backlog lane opens the chat-assisted description editor
      // (SC-2873); every other lane keeps the existing read-only detail panel
      // (SC-203) unchanged — same lane guard the "Create mocks" context-menu
      // item already uses (queueOf, not the raw wire stage). Links/buttons
      // never get here (pointerdown filters them), and right-clicks go to the
      // contextmenu handler instead.
      else if (wasClick) {
        if (queueOf(card) === "product" && !card.bug && !card.security) {
          void openDescEditModal(card);
        } else {
          toggleTicketDetail(card);
        }
      }
    };

    const onCancel = (): void => {
      teardown();
      endDrag();
    };

    try {
      el.setPointerCapture(down.pointerId);
    } catch {
      // Best-effort; drag still works via bubbling if capture is unavailable.
    }
    el.addEventListener("pointermove", onMove);
    el.addEventListener("pointerup", onUp);
    el.addEventListener("pointercancel", onCancel);
  });
}

// endDrag closes the drag lifecycle and flushes any board rebuild that was
// deferred while the drag was in flight.
function endDrag(): void {
  dragging = null;
  if (pendingRender) {
    pendingRender = false;
    render();
  }
}

// performDrop runs the action for a completed drop on an allowed target.
function performDrop(
  target: HTMLElement,
  info: { key: string; title: string; stage: string },
  pt: { x: number; y: number },
): void {
  if (target.dataset.drop === "idea") {
    // A local reorder, not a stage transition: move the card optimistically so
    // the drop feels instant, then persist. On a write failure we revert the
    // column ourselves and show the error — a reconcile here would overwrite
    // current.error with the (empty) fetch error before it's readable (SC-637).
    const col = Number(target.dataset.ideaCol);
    const card = current.cards.find((c) => c.key === info.key);
    const prevCol = card?.ideaColumn;
    if (card) {
      card.ideaColumn = col;
      render();
    }
    void runGuardedAction(
      () => go().SetIdeaColumn(info.key, col),
      (err) => {
        if (card) {
          card.ideaColumn = prevCol;
          render();
        }
        showError(errMessage(err));
      },
      // No reconcile on success: the daemon issues no board:changed event for
      // a local-only ideaColumn write (unchanged from today's behavior).
      async () => {},
    );
    return;
  }
  if (target.dataset.drop === "deploy") {
    // The drag is the consent: review passed, CI still gates the merge
    // server-side, so no extra dialog stands between the drop and the ship.
    celebrateDrop(pt, { key: info.key, fromStage: info.stage, done: true });
    void transition(info.key, info.title, info.stage, "done");
    return;
  }
  if (target.dataset.drop === "fix") {
    celebrateDrop(pt, { key: info.key, fromStage: info.stage, done: false });
    void fixBug(info.key, info.title);
    return;
  }
  if (target.dataset.drop === "security-fix") {
    celebrateDrop(pt, { key: info.key, fromStage: info.stage, done: false });
    void fixSecurity(info.key, info.title);
    return;
  }
  const toQueue = target.dataset.dropQueue ?? "";
  const dropped = current.cards.find((c) => c.key === info.key);
  if (dropped && !dropped.bug && !dropped.security && toQueue === queueOf(dropped)) {
    // A drop into the card's own column sorts it — mirrors the idea-space
    // local reorder, never a transition.
    reorderWithinQueue(toQueue, info.key, pt.y);
    return;
  }
  if (toQueue === "product" && info.stage === "ideas") {
    // Promotion is a conversation, not a stage transition: the evolve-mode
    // ideation session rewrites the ticket in place and removes the idea
    // label; the card moves columns when the board refetches.
    void promoteIdea(info.key);
    return;
  }
  const to = QUEUE_TRANSITION_TO[toQueue] ?? "";
  if (!to) return;
  celebrateDrop(pt, { key: info.key, fromStage: info.stage, done: false });
  void transition(info.key, info.title, info.stage, to);
}

// reorderWithinQueue persists a same-column drop as the queue's new hand
// order, read from the live DOM so the dragged card lands exactly where the
// pointer released among the cards the user was looking at. Optimistic like
// SetIdeaColumn: render from the new order immediately, snap back via
// reconcile only if the write fails. Hidden cards are absent from the DOM and
// so from the saved list — they simply re-append after it when revealed.
function reorderWithinQueue(queue: string, key: string, dropY: number): void {
  const body = document.querySelector<HTMLElement>(`.column[data-stage="${queue}"] .column-body`);
  if (!body) return;
  const resting: string[] = [];
  const midpoints: number[] = [];
  for (const el of body.querySelectorAll<HTMLElement>(".card")) {
    const k = el.dataset.key ?? "";
    if (!k || k === key) continue;
    const r = el.getBoundingClientRect();
    resting.push(k);
    midpoints.push(r.top + r.height / 2);
  }
  const keys = insertKeyAt(resting, midpoints, key, dropY);
  // Snapshot the whole map rather than this queue's entry alone, so the revert
  // below restores the exact prior state — including a queue that had no
  // hand-order at all, which a per-entry undo cannot express.
  const prevColumnOrder = current.columnOrder ? { ...current.columnOrder } : undefined;
  if (!current.columnOrder) current.columnOrder = {};
  current.columnOrder[queue] = keys;
  render();
  void runGuardedAction(
    () => go().SetColumnOrder(queue, keys),
    (err) => {
      // Undo the optimistic reorder here rather than letting a reconcile do it
      // — a reconcile would overwrite current.error with the (empty) fetch
      // error and the failure would flash away unseen (SC-637).
      current.columnOrder = prevColumnOrder;
      render();
      showError(errMessage(err));
    },
    // No reconcile on success: the daemon issues no board:changed event for a
    // local-only column-order write, same as the ideaColumn drop.
    async () => {},
  );
}

// promoteIdea opens the ideation panel in evolve mode, seeded with the idea
// card's content. An active session must be explicitly replaced — the daemon
// holds a single ideation session, so a silent restart would discard it.
async function promoteIdea(key: string): Promise<void> {
  const card = current.cards.find((c) => c.key === key);
  if (!card) return;

  const active =
    ideation.state === "thinking" || ideation.state === "awaiting_reply" || ideation.state === "awaiting_approval";
  if (active) {
    const ok = await confirmDialog(
      "Replace the active ideation session?",
      "Promoting this idea abandons the conversation currently in the ideation panel.",
      "Replace",
    );
    if (!ok) return;
  }

  let seed = card.title;
  if (card.description) seed += `\n\n${card.description}`;

  const panel = document.getElementById("ideation-panel");
  if (panel) panel.classList.remove("hidden");
  ideationOpen = true;
  // Guided mode by default: a parked idea was parked precisely because it
  // wasn't thought through — structured questions fit that moment.
  ideationMode = "guided";
  ideation = { state: "thinking", messages: [{ role: "user", text: seed }] };
  renderIdeation();
  startIdeationPoll();

  try {
    ideation = await go().StartIdeation(seed, "guided", true, card.key, card.labels ?? []);
  } catch (err) {
    renderIdeationError(errMessage(err));
    stopIdeationPoll();
    return;
  }
  renderIdeation();
  if (ideation.state !== "thinking") stopIdeationPoll();
}

async function transition(key: string, title: string, from: string, to: string): Promise<void> {
  const card = current.cards.find((c) => c.key === key);
  const prevStage = card?.stage;
  const prevState = card?.state;
  if (card) {
    card.stage = to;
    card.state = "running";
    // Shield the optimistic move from a stale reconcile that predates it
    // (SC-2521). Replace any prior record for this key so a re-drag re-anchors
    // the origin and expiry rather than compounding.
    if (prevStage !== undefined) {
      pendingMoves = dropPendingMove(pendingMoves, key);
      pendingMoves.push({
        key,
        fromStage: prevStage,
        toStage: to,
        expiresAt: Date.now() + PENDING_MOVE_TTL_MS,
      });
    }
    render();
  }
  await runGuardedAction(
    () => go().Transition(key, title, from, to),
    (err) => {
      // Revert the optimistic move so a failed launch doesn't leave the card
      // looking like it's running or already moved (SC-637), and drop the
      // shield so the next reconcile isn't pinned to the reverted move.
      if (card && prevStage !== undefined && prevState !== undefined) {
        card.stage = prevStage;
        card.state = prevState;
      }
      pendingMoves = dropPendingMove(pendingMoves, key);
      showError(errMessage(err));
    },
    reconcile,
  );
}

// requestClose confirms in-app (never the OS dialog) before closing, so a stray
// drop cannot silently close a ticket. When a run is live, the dialog says so
// explicitly: closing IS cancellation (1698), so the confirm must not read as a
// harmless "mark Done" when it will actually stop work in progress.
async function requestClose(
  key: string,
  title: string,
  running: boolean,
): Promise<void> {
  const body = running
    ? `“${title}” has a live run. Closing will stop the run — its container is released and any uncommitted work is discarded — and mark the ticket Done.`
    : `“${title}” will be marked Done and removed from the board.`;
  const ok = await confirmDialog(`Close ticket ${key}?`, body, "Close ticket");
  if (ok) await closeTicket(key);
}

async function closeTicket(key: string): Promise<void> {
  await runGuardedAction(
    () => go().CloseTicket(key),
    (err) => {
      // Leave the board untouched so the banner survives — a reconcile here
      // would overwrite current.error with the (empty) fetch error and the
      // failure would flash away unseen (SC-637 guard, now shared via
      // runGuardedAction).
      showError(errMessage(err));
    },
    async () => {
      // The daemon confirmed the close, so the card leaves the board
      // immediately — the full refetch below takes seconds (per-ticket
      // comment scan) and waiting for it reads as "close did nothing".
      // Bumping the epoch invalidates any fetch already in flight, whose
      // pre-close snapshot would resurrect the card.
      reconcileEpoch++;
      current.cards = current.cards.filter((c) => c.key !== key);
      render();
      await reconcile();
    },
  );
}

// applyPermissionDecision optimistically reflects an approved permission
// request on the board — the same instant feedback drag-and-drop already has —
// then reconciles so a change that did not actually land is quietly restored.
// Only DeleteIssue maps to a deterministic board effect (the card leaves);
// EditIssue and others have no card-level change, so they fall through to the
// reconcile alone. A denial makes no board change at all.
function applyPermissionDecision(req: PermissionRequest, approved: boolean): void {
  if (approved && req.operation === "DeleteIssue") {
    // Bump the epoch first so any in-flight pre-delete fetch cannot resurrect
    // the card, mirroring closeTicket's optimistic removal.
    reconcileEpoch++;
    current.cards = current.cards.filter((c) => c.key !== req.key);
    render();
  }
  void reconcile();
}

// createMocks launches mockup generation for one ticket. No confirm dialog —
// the action is additive (files in mockups/, nothing on the tracker). The
// immediate reconcile picks up the daemon-written link so the menu reads
// "Creating mocks…" on the next right-click.
async function createMocks(card: Card): Promise<void> {
  await runGuardedAction(
    () => go().CreateMocks(card.key, card.title, card.description ?? ""),
    (err) => showError(errMessage(err)),
    reconcile,
  );
}

// confirmDialog renders a small modal overlay and resolves true/false on the
// user's choice. Overlay-click and Escape count as cancel. Built with the same
// imperative-DOM approach as the rest of the app (no framework).
function confirmDialog(
  title: string,
  body: string,
  confirmLabel: string,
  cancelLabel = "Cancel",
): Promise<boolean> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "modal-overlay";

    const modal = document.createElement("div");
    modal.className = "modal";
    modal.innerHTML = `
      <div class="modal-title">${escapeHtml(title)}</div>
      <div class="modal-body">${escapeHtml(body)}</div>
      <div class="modal-actions">
        <button class="modal-cancel" type="button">${escapeHtml(cancelLabel)}</button>
        <button class="modal-confirm" type="button">${escapeHtml(confirmLabel)}</button>
      </div>
    `;
    overlay.appendChild(modal);
    document.body.appendChild(overlay);

    const cleanup = (result: boolean): void => {
      document.removeEventListener("keydown", onKey);
      overlay.remove();
      resolve(result);
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") cleanup(false);
    };

    overlay.addEventListener("click", (e: MouseEvent) => {
      if (e.target === overlay) cleanup(false);
    });
    modal.querySelector(".modal-cancel")!.addEventListener("click", () => cleanup(false));
    modal.querySelector(".modal-confirm")!.addEventListener("click", () => cleanup(true));
    document.addEventListener("keydown", onKey);
    (modal.querySelector(".modal-confirm") as HTMLButtonElement).focus();
  });
}

// busyCloseDialog is confirmDialog's three-way counterpart for the app-level
// close flow: unlike a plain confirm, closing while the daemon is busy has a
// genuine third option (wait it out) that is neither "cancel" nor "destroy
// the work now" (SC-3015). Overlay-click and Escape both count as "cancel" —
// the safest default when work is in flight.
function busyCloseDialog(): Promise<"cancel" | "stop" | "wait"> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "modal-overlay";

    const modal = document.createElement("div");
    modal.className = "modal";
    modal.innerHTML = `
      <div class="modal-title">human is still working</div>
      <div class="modal-body">An agent is actively running, or a stage is still leased. Closing now would leave that work unsupervised — choose what to do.</div>
      <div class="modal-actions">
        <button class="modal-cancel" type="button">Cancel</button>
        <button class="modal-secondary" type="button">Wait and close</button>
        <button class="modal-confirm" type="button">Stop anyway</button>
      </div>
    `;
    overlay.appendChild(modal);
    document.body.appendChild(overlay);

    const cleanup = (result: "cancel" | "stop" | "wait"): void => {
      document.removeEventListener("keydown", onKey);
      overlay.remove();
      resolve(result);
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") cleanup("cancel");
    };

    overlay.addEventListener("click", (e: MouseEvent) => {
      if (e.target === overlay) cleanup("cancel");
    });
    modal.querySelector(".modal-cancel")!.addEventListener("click", () => cleanup("cancel"));
    modal.querySelector(".modal-secondary")!.addEventListener("click", () => cleanup("wait"));
    modal.querySelector(".modal-confirm")!.addEventListener("click", () => cleanup("stop"));
    document.addEventListener("keydown", onKey);
    (modal.querySelector(".modal-cancel") as HTMLButtonElement).focus();
  });
}

// captureColumnScroll records each column's current scrollTop keyed by stage, so
// it can be restored after render() rebuilds the DOM from scratch.
function captureColumnScroll(board: HTMLElement): Record<string, number> {
  const scroll: Record<string, number> = {};
  board.querySelectorAll<HTMLElement>(".column").forEach((col) => {
    const body = col.querySelector<HTMLElement>(".column-body");
    if (body && col.dataset.stage) scroll[col.dataset.stage] = body.scrollTop;
  });
  return scroll;
}

// restoreColumnScroll re-applies scroll positions captured before a rebuild.
function restoreColumnScroll(board: HTMLElement, scroll: Record<string, number>): void {
  board.querySelectorAll<HTMLElement>(".column").forEach((col) => {
    const stage = col.dataset.stage;
    const body = col.querySelector<HTMLElement>(".column-body");
    if (body && stage && scroll[stage]) body.scrollTop = scroll[stage];
  });
}

// A render mid-drag would replace the dragged card's DOM element, silently
// killing its pointer listeners (frozen ghost, drop never lands). Rebuilds
// requested during a drag are deferred and flushed by endDrag().
let pendingRender = false;

function render(): void {
  // Appearance rides the board payload, so every render re-asserts what the
  // current config says — editing dim_percent and refreshing shows the new
  // dimming with no rebuild. Set before the drag guard: it is a cheap,
  // idempotent property write and must not wait for a drag to end.
  applyNotMineOpacity(document.documentElement, current.dimPercent);
  if (dragging) {
    pendingRender = true;
    return;
  }
  const board = document.getElementById("board")!;
  // Capture each column's scroll position before the full rebuild below wipes
  // it. A reconcile (board:changed / post-transition) must not snap a column the
  // user scrolled down back to the top.
  const scrollByStage = captureColumnScroll(board);
  board.innerHTML = "";
  if (boardLoading && current.cards.length === 0) {
    // First fetch in flight with nothing to show yet: a centered spinner gives
    // immediate feedback instead of five empty columns that read as "no work".
    const loading = document.createElement("div");
    loading.className = "board-loading";
    loading.innerHTML = `<span class="spinner"></span><span>Loading board…</span>`;
    board.appendChild(loading);
  } else if (current.notice && current.cards.length === 0) {
    // No PM-role tracker resolved (SC-1655): show the explicit reason in place
    // of five empty columns that would read as "no work". Only when there are
    // genuinely no cards — a configured PM tracker with zero issues still shows
    // its columns.
    const notice = document.createElement("div");
    notice.className = "board-notice";
    notice.textContent = current.notice;
    board.appendChild(notice);
  } else {
    if (current.truncation) {
      // The fetch was capped and the board below omits the overflow — a bar
      // above the columns tells the user the list is partial rather than
      // letting a full-looking board read as the whole backlog (SC-1693).
      const truncation = document.createElement("div");
      truncation.className = "board-truncation";
      truncation.textContent = current.truncation;
      board.appendChild(truncation);
    }
    for (const queue of QUEUES) {
      board.appendChild(queue === "ideas" ? renderIdeaSpace() : renderColumn(queue));
    }
    board.appendChild(renderDeployControl("features"));
    restoreColumnScroll(board, scrollByStage);
  }
  // The Bugs pane renders from the same card list, so every reconcile keeps
  // both views fresh regardless of which one is visible.
  renderBugs();
  const banner = document.getElementById("banner")!;
  if (current.error) {
    banner.textContent = current.error;
    banner.classList.remove("hidden");
  } else {
    banner.classList.add("hidden");
  }
  // The detail panel lives outside #board, so the rebuild above never touches
  // it — it only needs its card data refreshed from the new board state.
  refreshTicketDetail();
  updateHideToggle();
  // Arrows are measured from laid-out cards, so they wait for the frame the
  // browser draws this rebuild in — reading offsets now would measure the DOM
  // as it was before any of the above landed.
  requestAnimationFrame(drawBlockerArrows);
}

// drawBlockerArrows connects each card to the work it waits for, wherever both
// ends happen to share a column.
//
// Every column is swept on every render rather than tracking which cards moved:
// a card changes column by being rebuilt somewhere else, so there is no move to
// observe, and a stale arrow pointing at a card that left would be worse than
// no arrow at all.
//
// Three passes, all inside one frame so the user sees a single painted result:
// decide what is drawable from the untouched layout, narrow the cards those
// arrows attach to, then measure the settled layout and draw. The order is
// forced — which edge an arrow uses is read from the cards' positions, and
// narrowing moves them. Deciding first is safe because narrowing only ever
// opens space, so a corridor that was clear stays clear.
function drawBlockerArrows(): void {
  const blockers = new Map<string, string[]>();
  for (const card of current.cards) {
    if (card.blockers?.length) blockers.set(card.key, card.blockers);
  }
  // Cleared unconditionally: a resize redraws without rebuilding the DOM, so
  // last layout's gaps would otherwise linger beside arrows that have moved.
  document.querySelectorAll<HTMLElement>(".card").forEach(clearArrowGaps);
  const all = [...document.querySelectorAll<HTMLElement>(".column-body")];
  all.forEach((body) => body.querySelector(".blocker-arrows")?.remove());
  observeForSize(all);
  if (blockers.size === 0) return;
  // A hidden column measures zero on every card, so an arrow computed here
  // would be drawn at the origin and never seen. The Bugs pane is rebuilt on
  // every render even while the workflow board is showing, which is exactly
  // that case: the gaps land (they are CSS, and correct the moment the pane
  // appears) while the arrow does not, and it only shows up whenever the next
  // render happens to run with the pane open. Skipping here and redrawing when
  // the column gains size is what keeps the two in step.
  const bodies = all.filter(hasLayout);

  const work = bodies.map((body) => {
    const cards = cardsIn(body);
    const boxes = new Map([...cards].map(([key, el]) => [key, boxOf(el)]));
    return { body, cards, drawn: plan(linksWithin(blockers, new Set(cards.keys())), boxes) };
  });

  for (const { cards, drawn } of work) {
    for (const [key, sides] of gapsBySide(drawn)) {
      const el = cards.get(key);
      if (el) for (const side of sides) el.classList.add(`arrow-gap-${side}`);
    }
  }

  for (const { body, cards, drawn } of work) {
    if (drawn.length === 0) continue;
    // Re-measured AFTER the gaps land: these are the positions the arrows are
    // drawn between, and they are not the ones the plan was made from.
    const boxes = new Map([...cards].map(([key, el]) => [key, boxOf(el)]));
    body.appendChild(arrowLayer(body, bodies.indexOf(body), drawn, boxes));
  }
}

// hasLayout reports whether an element is actually laid out — false inside a
// `display: none` subtree, where every offset reads zero.
function hasLayout(el: HTMLElement): boolean {
  return el.offsetParent !== null && el.offsetWidth > 0 && el.offsetHeight > 0;
}

// sizeWatcher redraws when a column's size changes: when a hidden pane is
// opened (zero → real), when a web font finishes loading and reflows the cards,
// and when the window resizes. Watching the elements rather than the window
// covers all three with one mechanism, including the pane switch, which fires
// no resize event at all.
const sizeWatcher =
  typeof ResizeObserver === "undefined"
    ? null
    : new ResizeObserver(() => requestAnimationFrame(drawBlockerArrows));

// observeForSize points the watcher at the current columns. render() rebuilds
// them, so the previous observations are dropped first — an observer holding
// detached nodes would keep firing for elements nobody can see.
function observeForSize(bodies: HTMLElement[]): void {
  if (!sizeWatcher) return;
  sizeWatcher.disconnect();
  for (const body of bodies) sizeWatcher.observe(body);
}

const ARROW_GAP_SIDES: Side[] = ["left", "right", "top", "bottom"];

function clearArrowGaps(el: HTMLElement): void {
  for (const side of ARROW_GAP_SIDES) el.classList.remove(`arrow-gap-${side}`);
}

// cardsIn indexes one column's cards by ticket key.
function cardsIn(body: HTMLElement): Map<string, HTMLElement> {
  const cards = new Map<string, HTMLElement>();
  body.querySelectorAll<HTMLElement>(".card[data-key]").forEach((el) => {
    if (el.dataset.key) cards.set(el.dataset.key, el);
  });
  return cards;
}

// arrowLayer builds one column's finished SVG, paths and all.
function arrowLayer(body: HTMLElement, index: number, drawn: Drawn[], boxes: Map<string, Box>): SVGSVGElement {
  const overlay = arrowOverlay(body, index);
  for (const d of drawn) {
    const from = boxes.get(d.from);
    const to = boxes.get(d.to);
    if (!from || !to) continue;
    const path = document.createElementNS(SVG_NS, "path");
    // The sides come from the plan, not from a fresh reading of the narrowed
    // boxes: the gap was opened on those edges, and an arrow that picked
    // different ones would leave through a side with no room for it.
    path.setAttribute("d", arrowPath(from, to, { exit: d.exit, enter: d.enter }));
    path.setAttribute("class", "blocker-arrow");
    path.setAttribute("marker-end", `url(#${overlay.dataset.head})`);
    overlay.appendChild(path);
  }
  return overlay;
}

const SVG_NS = "http://www.w3.org/2000/svg";

// boxOf measures a card in its column's own content coordinates. The column is
// positioned, so offsetLeft/offsetTop are already relative to it and already
// account for how far it is scrolled — which is what makes the overlay scroll
// with the cards instead of sliding across them.
function boxOf(el: HTMLElement): Box {
  return { left: el.offsetLeft, top: el.offsetTop, width: el.offsetWidth, height: el.offsetHeight };
}

// arrowOverlay builds the SVG layer for one column, sized to the column's whole
// scrollable content so an arrow to a card below the fold is not clipped away.
// It never takes pointer events: dragging a card is how the board works, and a
// layer over every card would swallow it.
function arrowOverlay(body: HTMLElement, index: number): SVGSVGElement {
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("class", "blocker-arrows");
  svg.setAttribute("width", String(body.scrollWidth));
  svg.setAttribute("height", String(body.scrollHeight));
  // Marker ids are document-global, so each column defines its own — two
  // columns sharing one id would leave every arrow pointing at whichever
  // definition happened to render last.
  const headID = `blocker-arrowhead-${index}`;
  svg.dataset.head = headID;
  const defs = document.createElementNS(SVG_NS, "defs");
  const marker = document.createElementNS(SVG_NS, "marker");
  marker.setAttribute("id", headID);
  marker.setAttribute("viewBox", "0 0 8 8");
  marker.setAttribute("refX", "7");
  marker.setAttribute("refY", "4");
  marker.setAttribute("markerWidth", "6");
  marker.setAttribute("markerHeight", "6");
  marker.setAttribute("orient", "auto-start-reverse");
  const head = document.createElementNS(SVG_NS, "path");
  head.setAttribute("d", "M 0 1 L 7 4 L 0 7 z");
  head.setAttribute("class", "blocker-arrowhead");
  marker.appendChild(head);
  defs.appendChild(marker);
  svg.appendChild(defs);
  return svg;
}

function showError(msg: string): void {
  current.error = msg;
  render();
}

// toggleCardHidden parks a ticket off the board or restores it. Optimistic
// like SetIdeaColumn: flip and render immediately, snapping the flip back
// locally if the write fails.
function toggleCardHidden(card: Card): void {
  const prevHidden = card.hidden;
  const nextHidden = !card.hidden;
  card.hidden = nextHidden;
  render();
  void runGuardedAction(
    () => go().SetCardHidden(card.key, nextHidden),
    (err) => {
      // Snap back here rather than via reconcile — a reconcile would overwrite
      // current.error with the (empty) fetch error and the failure would flash
      // away unseen (SC-637).
      card.hidden = prevHidden;
      render();
      showError(errMessage(err));
    },
    // No reconcile on success: the daemon issues no board:changed event for a
    // local-only hidden write, same as the ideaColumn drop.
    async () => {},
  );
}

// updateHideToggle keeps the header's Unhide/Hide button in sync: present only
// while hidden tickets exist (labeled with the count), toggling whether they
// render with their H pill or stay filtered out. When the last hidden ticket
// is unhidden the reveal state resets, so the button never sticks around dead.
function updateHideToggle(): void {
  const header = document.getElementById("app-header");
  if (!header) return;
  let btn = document.getElementById("hide-toggle") as HTMLButtonElement | null;
  const hiddenCount = current.cards.filter((c) => c.hidden).length;
  if (hiddenCount === 0) {
    showHidden = false;
    btn?.remove();
    return;
  }
  if (!btn) {
    btn = document.createElement("button");
    btn.id = "hide-toggle";
    btn.type = "button";
    btn.className = "hide-toggle";
    btn.addEventListener("click", () => {
      showHidden = !showHidden;
      render();
    });
    header.appendChild(btn);
  }
  btn.textContent = showHidden ? `Hide hidden (${hiddenCount})` : `Unhide (${hiddenCount})`;
  btn.title = showHidden
    ? "Conceal the revealed hidden tickets again"
    : "Reveal hidden tickets (marked with an H pill)";
}

function renderDaemonStatus(): void {
  // Mirrors the TUI's bottom status line ("● Daemon running"/"stopped").
  const dot = document.getElementById("daemon-indicator")!;
  dot.classList.toggle("reachable", daemonReachable);
  dot.classList.toggle("unreachable", !daemonReachable);
  const text = document.getElementById("daemon-text")!;
  text.textContent = daemonReachable ? "Daemon running" : "Daemon stopped";
}

async function pollDaemonStatus(): Promise<void> {
  try {
    daemonReachable = await go().DaemonStatus();
  } catch {
    // Wails bindings not ready yet or call failed — treat as unreachable.
    daemonReachable = false;
  }
  renderDaemonStatus();
  void pollDoctor();
}

// pollDoctor drives the rail LED in three states, so a failure is shown with
// its real weight rather than every failure reading as an outage (SC-1991):
//   - green  ("ok")   every check passes.
//   - amber  ("warn") something is failing but nothing blocks new work — an
//     advisory or a momentary blip; visible, never an alarm.
//   - red    ("fail") a gating check is persistently failing — work genuinely
//     cannot start. This is the only hard stop.
// The daemon caches check results, so polling at the daemon-status cadence is
// cheap and the LED reflects reality within seconds.
async function pollDoctor(): Promise<void> {
  const led = document.getElementById("doctor-led");
  if (!led) return;
  let doctor: DoctorData;
  try {
    doctor = await go().Doctor();
  } catch {
    doctor = { healthy: false, blocked: true, checks: [{ id: "daemon", name: "daemon", ok: false, severity: "blocking", detail: "not reachable" }] };
  }
  const failing = (doctor.checks ?? []).filter((c) => !c.ok);
  // Prefer the daemon's own verdict; fall back to deriving it from severities
  // so an older daemon (no `blocked`/`severity`) still degrades sensibly:
  // unclassified failures are treated as blocking, preserving prior behaviour.
  const blocked =
    doctor.blocked ?? failing.some((c) => !c.severity || c.severity === "blocking");
  const healthy = doctor.healthy && failing.length === 0;
  led.classList.toggle("ok", healthy);
  led.classList.toggle("warn", !healthy && !blocked);
  led.classList.toggle("fail", blocked);
  if (healthy) {
    led.title = "All systems go";
  } else {
    const lines = failing.map((c) => `${c.name}: ${c.detail || "failing"}`);
    // Three states, not two. A held check stops work from starting without the
    // substrate being broken — saying "nothing is blocked" there would be
    // false, and it is the false half that leaves someone hunting a ticket
    // failure whose real cause was an approval nobody answered.
    const holding = !blocked && failing.some((c) => c.holding);
    const header = blocked
      ? "New work is blocked:"
      : holding
        ? "New work is waiting — this needs you:"
        : "Advisory — work can start, nothing is blocked:";
    led.title = [header, ...lines].join("\n");
  }
}

// initialLoad renders the board on startup: spinner → quick titles → full
// reconcile.
//
// The board no longer keeps its own snapshot to paint from. The daemon owns the
// last-known board and serves it (stale-marked) when a refresh fails, so
// instant-paint is answered by the machine that actually knows what the board
// last looked like. Without a daemon there is nothing to show and the fetch
// error says so — an empty board would read as "there is no work" (SC-2132).
async function initialLoad(): Promise<void> {
  {
    boardLoading = true;
    render();
    try {
      const quick = await go().CardsQuick();
      // Suppress the quick-phase error: the full reconcile surfaces it, and
      // clearing it here avoids a banner that flickers away a moment later.
      current = boardStateFromPayload(quick, true);
      boardLoading = false;
      stagesLoading = true;
      render();
    } catch {
      // Quick phase failed (e.g. daemon not up yet): fall through to the full
      // fetch, which surfaces the error via reconcile().
      boardLoading = false;
    }
  }
  await reconcile();
}

// reconcile fetches can overlap: a board:changed event may land while a
// post-close refresh is still scanning comments. Only the newest fetch may
// write `current` — a slower stale response would otherwise overwrite fresh
// state and resurrect cards that already left the board. closeTicket also
// bumps the epoch when it mutates `current` directly, for the same reason.
let reconcileEpoch = 0;

// reconcile fetches the full board (including derived stages) and renders it. It
// is the single source of truth after the initial load: board:changed events and
// post-transition refreshes call it directly.
async function reconcile(opts: { safety?: boolean } = {}): Promise<void> {
  const epoch = ++reconcileEpoch;
  try {
    const data = await go().Cards();
    if (epoch !== reconcileEpoch) return;
    current = boardStateFromPayload(data);
    findbugsHunting = await go()
      .FindbugsHunting()
      .catch(() => false);
    securityHunting = await go()
      .SecurityHunting()
      .catch(() => false);
  } catch (err) {
    if (epoch !== reconcileEpoch) return;
    current = opts.safety
      ? safetyReconcileError(current, errMessage(err))
      : { cards: [], dockerAvailable: false, error: errMessage(err) };
  }
  if (pendingIdeas.length) {
    // A fetched Ideas card whose key matches the pending capture's key IS
    // that capture — the placeholder hands over to the real card. A
    // placeholder that never captured a key falls back to title matching
    // (SC-1691). Unconfirmed captures stay, whatever this fetch contained.
    const ideaCards = current.cards.filter((c) => queueOf(c) === "ideas");
    const keys = new Set(ideaCards.map((c) => c.key));
    const titles = new Set(ideaCards.map((c) => c.title));
    pendingIdeas = reconcilePending(pendingIdeas, keys, titles);
  }
  if (pendingBugs.length) {
    // Same handover rule for bugs filed from the pane's + dialog.
    const bugCards = current.cards.filter((c) => c.bug);
    const keys = new Set(bugCards.map((c) => c.key));
    const titles = new Set(bugCards.map((c) => c.title));
    pendingBugs = reconcilePending(pendingBugs, keys, titles);
  }
  if (pendingSecurity.length) {
    // Same handover rule for security tickets filed from the Security half.
    const securityCards = current.cards.filter((c) => c.security);
    const keys = new Set(securityCards.map((c) => c.key));
    const titles = new Set(securityCards.map((c) => c.title));
    pendingSecurity = reconcilePending(pendingSecurity, keys, titles);
  }
  if (pendingMoves.length) {
    // Hold each optimistic drag against a fetch that predates the move
    // becoming readable (SC-2521): a fetch still showing the origin stage is
    // stale and its card is pinned back to the target, while a confirmed
    // target, a daemon-chosen third stage, or expiry retires the shield.
    const stageByKey = new Map(current.cards.map((c) => [c.key, c.stage]));
    const { moves, overrides } = reconcilePendingMoves(
      pendingMoves,
      stageByKey,
      Date.now(),
    );
    pendingMoves = moves;
    for (const o of overrides) {
      const c = current.cards.find((card) => card.key === o.key);
      if (c) {
        c.stage = o.toStage;
        c.state = "running";
      }
    }
  }
  boardLoading = false;
  stagesLoading = false;
  render();
  // Offered at most once per session, and only off a confirmed-empty board —
  // see the Start Project wizard section for the guards.
  void maybeOfferStartProject();
}

function startBoardPolling(): void {
  void initialLoad();
  void pollDaemonStatus();
  if (boardPollTimer === null) boardPollTimer = setInterval(() => void pollDaemonStatus(), DAEMON_POLL_MS);
  if (safetyPollTimer === null) {
    safetyPollTimer = setInterval(() => {
      // Runs every tick regardless of the daemonReachable flag — see
      // safetyPollShouldReconcile / SC-1677. Failures surface via the banner.
      if (safetyPollShouldReconcile(daemonReachable)) void reconcile({ safety: true });
    }, BOARD_SAFETY_POLL_MS);
  }
}

function stopBoardPolling(): void {
  if (boardPollTimer !== null) {
    clearInterval(boardPollTimer);
    boardPollTimer = null;
  }
  if (safetyPollTimer !== null) {
    clearInterval(safetyPollTimer);
    safetyPollTimer = null;
  }
}

function setActiveProjectName(name: string): void {
  const title = document.querySelector(".app-title");
  if (title) title.textContent = name ? `human — workflow board · ${name}` : "human — workflow board";
}

function showAppShell(projectName?: string): void {
  document.querySelector(".app-shell")?.classList.remove("hidden");
  document.getElementById("projects-overview")?.classList.add("hidden");
  if (projectName !== undefined) setActiveProjectName(projectName);
}

function showOverviewScreen(errText?: string): void {
  document.querySelector(".app-shell")?.classList.add("hidden");
  document.getElementById("projects-overview")?.classList.remove("hidden");
  stopBoardPolling();
  setActiveProjectName("");
  void showProjectsOverview(errText);
}

async function bootstrapProject(): Promise<void> {
  let result: ProjectBootstrapResult;
  try {
    result = await go().ProjectBootstrap();
  } catch (err) {
    result = { status: "overview", error: errMessage(err) };
  }
  if (result.status === "overview") {
    showOverviewScreen(result.error);
    return;
  }
  showAppShell(result.project);
  startBoardPolling();
  if (result.orphan) {
    void offerOrphanCleanup(result.orphanProject);
  }
  if (result.conflict) {
    void offerProjectConflict(result.project, result.conflictProject);
  }
}

// offerOrphanCleanup runs once at launch when ProjectBootstrap detects a
// daemon left running by a crashed/force-quit prior session (no attached
// app) rather than one a user intentionally runs standalone (SC-3015).
// Fire-and-forget: the board is already usable behind it, matching the rest
// of the app's non-blocking dialog pattern.
async function offerOrphanCleanup(project?: string): Promise<void> {
  const label = project ? `"${project}"'s` : "This project's";
  const stop = await confirmDialog(
    "Clean up a leftover daemon?",
    `${label} daemon is still running from a previous session that never closed cleanly (a crash, force-quit, or shutdown), and nobody is attached to it right now.`,
    "Stop it",
    "Leave it running",
  );
  try {
    await go().ResolveOrphan(stop);
  } catch (err) {
    showError(errMessage(err));
  }
}

// noticeDialog is confirmDialog's one-button counterpart: a plain
// acknowledgement with no choice to make, for launch-time notices where
// offering a choice would be scope this ticket deliberately defers
// (SC-3346's conflict signal — see offerProjectConflict below).
function noticeDialog(title: string, body: string, buttonLabel = "OK"): Promise<void> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "modal-overlay";

    const modal = document.createElement("div");
    modal.className = "modal";
    modal.innerHTML = `
      <div class="modal-title">${escapeHtml(title)}</div>
      <div class="modal-body">${escapeHtml(body)}</div>
      <div class="modal-actions">
        <button class="modal-confirm" type="button">${escapeHtml(buttonLabel)}</button>
      </div>
    `;
    overlay.appendChild(modal);
    document.body.appendChild(overlay);

    const cleanup = (): void => {
      document.removeEventListener("keydown", onKey);
      overlay.remove();
      resolve();
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") cleanup();
    };

    overlay.addEventListener("click", (e: MouseEvent) => {
      if (e.target === overlay) cleanup();
    });
    modal.querySelector(".modal-confirm")!.addEventListener("click", () => cleanup());
    document.addEventListener("keydown", onKey);
    (modal.querySelector(".modal-confirm") as HTMLButtonElement).focus();
  });
}

// offerProjectConflict runs once at launch when ProjectBootstrap detects that
// the working directory the app was launched from holds a different project
// than the one a reachable daemon is already serving (SC-3346). Fire-and-
// forget and purely informational: the running project's board is already
// showing behind it, and nothing was switched — the interactive choice
// between the two is a follow-up ticket, so this only names them.
async function offerProjectConflict(runningProject?: string, cwdProject?: string): Promise<void> {
  const running = runningProject || "the running project";
  const here = cwdProject || "this directory's project";
  await noticeDialog(
    "A different project is already running",
    `This directory holds "${here}", but "${running}" is the project currently running here. Nothing has changed — use Switch Project if you want to open "${here}" instead.`,
  );
}

function errMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}

function escapeHtml(s: unknown): string {
  return String(s ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;");
}

function escapeAttr(s: unknown): string {
  return escapeHtml(s).replaceAll('"', "&quot;");
}

// --- Ideation chat panel -----------------------------------------------
//
// The panel is a thin client over the daemon's ideation-start/reply/status
// routes: it never derives session state itself, it only renders whatever
// the daemon last reported. Closing the panel does NOT abandon the
// daemon-side session (AD-4) — reopening re-attaches via IdeationStatus().

let ideation: IdeationView = { state: "none", messages: [] };
let ideationOpen = false;
let ideationTimer: number | null = null;
// ideationMode is transient frontend-only state: null means the mode picker
// has not been resolved yet for a fresh session. It is not sent to the
// daemon until the user picks a mode and sends the first message/seed.
let ideationMode: "chat" | "guided" | null = null;
const IDEATION_POLL_MS = 1000;

function stopIdeationPoll(): void {
  if (ideationTimer !== null) {
    clearInterval(ideationTimer);
    ideationTimer = null;
  }
}

// startIdeationPoll only runs while the panel is visible: the daemon-side
// session keeps making progress on its own regardless (AD-4), so there is no
// need to poll for a panel the user cannot see.
function startIdeationPoll(): void {
  if (!ideationOpen || ideationTimer !== null) return;
  ideationTimer = window.setInterval(() => void pollIdeation(), IDEATION_POLL_MS);
}

function renderModePicker(): void {
  const picker = document.getElementById("ideation-mode-picker");
  if (!picker) return;
  const show = ideation.state === "none" && ideationMode === null;
  picker.classList.toggle("hidden", !show);
}

function renderIdeationOptions(): void {
  const container = document.getElementById("ideation-options");
  const input = document.getElementById("ideation-input") as HTMLInputElement | null;
  if (!container) return;

  const question = ideation.state === "awaiting_reply" ? ideation.question : undefined;
  if (!question) {
    container.classList.add("hidden");
    container.innerHTML = "";
    if (input) input.classList.remove("hidden");
    return;
  }

  container.classList.remove("hidden");
  container.innerHTML = "";
  question.options.forEach((opt) => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "ideation-option";
    btn.textContent = opt;
    btn.addEventListener("click", () => void sendIdeationReply(opt));
    container.appendChild(btn);
  });
  const other = document.createElement("button");
  other.type = "button";
  other.className = "ideation-option ideation-option-other";
  other.textContent = "Other…";
  other.addEventListener("click", () => {
    if (input) {
      input.classList.remove("hidden");
      input.focus();
    }
  });
  container.appendChild(other);
  // The freeform escape hatch stays hidden behind "Other…" until clicked, but
  // remains functionally enabled/usable for every question regardless of Kind.
  if (input) input.classList.add("hidden");
}

function renderIdeationDraft(): void {
  const draftEl = document.getElementById("ideation-draft");
  const form = document.getElementById("ideation-form");
  if (!draftEl) return;

  if (ideation.state !== "awaiting_approval" || !ideation.draft) {
    draftEl.classList.add("hidden");
    return;
  }

  draftEl.classList.remove("hidden");
  if (form) form.classList.add("hidden");

  const titleInput = document.getElementById("ideation-draft-title") as HTMLInputElement | null;
  const descInput = document.getElementById("ideation-draft-description") as HTMLTextAreaElement | null;
  // Only pre-fill on first render of a draft (avoid clobbering in-progress
  // user edits on every poll tick).
  if (titleInput && titleInput.dataset.sessionId !== ideation.sessionId) {
    titleInput.value = ideation.draft.title;
    titleInput.dataset.sessionId = ideation.sessionId ?? "";
  }
  if (descInput && descInput.dataset.sessionId !== ideation.sessionId) {
    descInput.value = ideation.draft.description;
    descInput.dataset.sessionId = ideation.sessionId ?? "";
  }
}

// Keys whose "Move to feature" was already triggered this session: the board
// snapshot can lag the transition for a poll tick or two, and the guard keeps
// the button from re-arming (and double-launching an agent) in that window.
const ideationMovedKeys = new Set<string>();

// renderIdeationDone fills the done-state status line. The created ticket is a
// Product-backlog card, but the panel used to dead-end at "Created SC-XXX"
// with no way to act on it (SC-881) — so the line carries a right-aligned
// "Move to feature" action that launches the same backlog→planning transition
// a drag onto the Engineering backlog would, for both chat and guided modes.
function renderIdeationDone(statusLine: HTMLElement): void {
  const key = ideation.createdKey ?? "";
  statusLine.textContent = `Created ${key}`;
  if (!key) return;

  const card = current.cards.find((c) => c.key === key);
  // Once the ticket has left the backlog — via this button or a board drag —
  // the move is spent; showing it armed again would offer a second launch.
  const moved = ideationMovedKeys.has(key) || (card !== undefined && card.stage !== "backlog");
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "ideation-move-feature";
  btn.textContent = moved ? "Moved to feature" : "Move to feature";
  btn.disabled = moved || !current.dockerAvailable;
  if (!moved && !current.dockerAvailable) {
    btn.title = "Docker is required to launch the planning agent";
  }
  btn.onclick = () => {
    if (ideationMovedKeys.has(key)) return;
    ideationMovedKeys.add(key);
    renderIdeation();
    void transition(key, card?.title ?? "", card?.stage ?? "backlog", "planning");
  };
  statusLine.appendChild(btn);
}

function renderIdeation(): void {
  const transcript = document.getElementById("ideation-transcript");
  if (!transcript) return;
  transcript.innerHTML = ideation.messages
    .map((m) => `<div class="msg ${m.role === "user" ? "user" : "agent"}">${escapeHtml(m.text)}</div>`)
    .join("");
  transcript.scrollTop = transcript.scrollHeight;

  const statusLine = document.getElementById("ideation-status-line");
  if (statusLine) {
    statusLine.classList.remove("hidden", "error");
    if (ideation.state === "thinking") {
      statusLine.textContent = "Agent is thinking…";
    } else if (ideation.state === "error") {
      statusLine.textContent = ideation.error || "Ideation session failed";
      statusLine.classList.add("error");
    } else if (ideation.state === "done") {
      renderIdeationDone(statusLine);
    } else {
      statusLine.classList.add("hidden");
    }
  }

  renderModePicker();
  renderIdeationOptions();
  renderIdeationDraft();

  const form = document.getElementById("ideation-form");
  const input = document.getElementById("ideation-input") as HTMLInputElement | null;
  const send = document.getElementById("ideation-send") as HTMLButtonElement | null;
  const inputEnabled = ideationInputEnabled(ideation.state);
  // The draft-review form takes over the panel's bottom area while
  // awaiting_approval; the free-text form must not be reachable there.
  if (form) form.classList.toggle("hidden", ideation.state === "awaiting_approval");
  if (input) {
    input.disabled = !inputEnabled;
    input.placeholder = ideation.state === "awaiting_reply" ? "Your answer…" : "Describe the idea…";
  }
  if (send) send.disabled = !inputEnabled;
}

function renderIdeationError(msg: string): void {
  ideation = { ...ideation, state: "error", error: msg };
  renderIdeation();
}

// --- Product-Backlog description-edit modal (SC-2873) ----------------------
//
// A dynamically-built centered modal (like showBugModal), deliberately NOT
// the fixed-edge slide-out pattern .detail-panel/.ideation-panel use — the
// AC requires this click target to be visibly distinct. Unlike ideation's
// AD-4 (chat survives a close), AC6 requires the opposite here: closing the
// modal without Apply/Save discards the pending proposal and chat, so
// closeDescEditModal ends the daemon-side session via DiscardDescEdit —
// reopening the SAME ticket after a close always starts genuinely fresh, not
// re-attached. Nothing is ever written to the tracker before the user clicks
// Apply.

let descEdit: DescEditView = { state: "none", messages: [] };
let descEditCard: Card | null = null;
let descEditSavedDescription = "";
let descEditSavedHTML: string | null = null;
let descEditTimer: number | null = null;
const DESCEDIT_POLL_MS = 1000;

function stopDescEditPoll(): void {
  if (descEditTimer !== null) {
    clearInterval(descEditTimer);
    descEditTimer = null;
  }
}

function startDescEditPoll(): void {
  if (descEditTimer !== null) return;
  descEditTimer = window.setInterval(() => void pollDescEdit(), DESCEDIT_POLL_MS);
}

async function openDescEditModal(card: Card): Promise<void> {
  descEditCard = card;
  descEdit = { state: "none", messages: [] };
  descEditSavedDescription = card.description ?? "";
  descEditSavedHTML = null;

  const overlay = document.createElement("div");
  overlay.className = "modal-overlay";
  overlay.id = "descedit-overlay";
  const modal = document.createElement("div");
  modal.className = "modal descedit-modal";
  modal.innerHTML = `
    <div class="descedit-header">
      <span class="descedit-key">${escapeHtml(card.key)}</span>
      <button type="button" class="descedit-close" title="Close">×</button>
    </div>
    <div class="descedit-body">
      <div class="descedit-desc-pane">
        <div class="descedit-desc-label">Description</div>
        <div id="descedit-desc" class="detail-description">Loading…</div>
      </div>
      <div class="descedit-chat-pane">
        <div id="descedit-transcript" class="descedit-transcript ideation-transcript"></div>
        <div id="descedit-status-line" class="descedit-status ideation-status hidden"></div>
        <form id="descedit-form" class="descedit-form ideation-form">
          <input id="descedit-input" type="text" autocomplete="off" placeholder="How should this description change?" />
          <button id="descedit-send" type="submit">Send</button>
        </form>
      </div>
    </div>
    <div class="descedit-actions">
      <button type="button" class="descedit-cancel modal-cancel">Close</button>
      <button type="button" id="descedit-apply" class="descedit-apply modal-confirm" disabled>Apply</button>
    </div>
  `;
  overlay.appendChild(modal);
  document.body.appendChild(overlay);

  const close = (): void => closeDescEditModal();
  overlay.addEventListener("click", (e) => {
    if (e.target === overlay) close();
  });
  modal.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Escape") close();
  });
  modal.querySelector(".descedit-close")!.addEventListener("click", close);
  modal.querySelector(".descedit-cancel")!.addEventListener("click", close);
  modal.querySelector("#descedit-apply")!.addEventListener("click", () => void applyDescEdit());
  modal.querySelector("#descedit-form")!.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    const input = document.getElementById("descedit-input") as HTMLInputElement;
    const text = input.value.trim();
    if (!text) return;
    input.value = "";
    void sendDescEditMessage(text);
  });

  renderDescEdit();

  try {
    const detail = await go().GetIssueDetail(card.trackerKind ?? "", card.tracker ?? "", card.key);
    if (descEditCard?.key !== card.key) return; // modal closed/reopened for a different ticket meanwhile
    descEditSavedDescription = detail.description || descEditSavedDescription;
    descEditSavedHTML = detail.descriptionHTML || null;
    renderDescEdit();
    descEdit = await go().StartDescEdit(card.key, descEditSavedDescription, false);
  } catch (err) {
    if (descEditCard?.key !== card.key) return;
    descEdit = { state: "error", messages: [], error: errMessage(err) };
  }
  renderDescEdit();
  if (descEdit.state === "thinking") startDescEditPoll();
}

function closeDescEditModal(): void {
  stopDescEditPoll();
  const { state, sessionId } = descEdit;
  document.getElementById("descedit-overlay")?.remove();
  descEditCard = null;
  // AC6: discard the daemon-side session so a later reopen of the same
  // ticket never reattaches to this proposal/chat history. Fire-and-forget —
  // the modal is already gone either way, and a failed discard only risks a
  // stale session reattaching once rather than corrupting anything (Discard
  // never touches the tracker).
  if (descEditShouldDiscardOnClose(state, sessionId)) {
    void go()
      .DiscardDescEdit(sessionId!)
      .catch(() => {});
  }
}

function renderDescEdit(): void {
  if (!descEditCard) return;
  const descEl = document.getElementById("descedit-desc");
  if (descEl) {
    const preview = buildDescriptionPreview(descEditSavedDescription, descEdit.proposal, descEdit.state);
    descEl.classList.toggle("descedit-desc-preview", preview.isPreview);
    if (preview.isPreview) {
      descEl.classList.remove("rendered");
      descEl.innerHTML =
        `<div class="descedit-preview-badge">Proposed rewrite (unsaved)</div>` +
        `<div>${escapeHtml(preview.text).replaceAll("\n", "<br>")}</div>`;
    } else if (descEditSavedHTML && preview.text === descEditSavedDescription) {
      descEl.classList.add("rendered");
      descEl.innerHTML = descEditSavedHTML;
    } else {
      descEl.classList.remove("rendered");
      descEl.textContent = preview.text || "No description";
    }
  }

  const transcript = document.getElementById("descedit-transcript");
  if (transcript) {
    transcript.innerHTML = descEdit.messages
      .map((m) => `<div class="msg ${m.role === "user" ? "user" : "agent"}">${escapeHtml(m.text)}</div>`)
      .join("");
    transcript.scrollTop = transcript.scrollHeight;
  }

  const statusLine = document.getElementById("descedit-status-line");
  if (statusLine) {
    statusLine.classList.remove("hidden", "error");
    if (descEdit.state === "thinking") {
      statusLine.textContent = "Thinking…";
    } else if (descEdit.state === "error") {
      statusLine.textContent = descEdit.error || "Description chat failed";
      statusLine.classList.add("error");
    } else if (descEdit.state === "applied") {
      statusLine.textContent = "Saved.";
    } else {
      statusLine.classList.add("hidden");
    }
  }

  const input = document.getElementById("descedit-input") as HTMLInputElement | null;
  const send = document.getElementById("descedit-send") as HTMLButtonElement | null;
  const inputEnabled = descEditInputEnabled(descEdit.state);
  if (input) input.disabled = !inputEnabled;
  if (send) send.disabled = !inputEnabled;

  const apply = document.getElementById("descedit-apply") as HTMLButtonElement | null;
  if (apply) apply.disabled = !descEditApplyEnabled(descEdit.state, descEdit.proposal);
}

async function pollDescEdit(): Promise<void> {
  if (!descEditCard) {
    stopDescEditPoll();
    return;
  }
  try {
    descEdit = await go().DescEditStatus();
  } catch (err) {
    descEdit = { ...descEdit, state: "error", error: errMessage(err) };
    stopDescEditPoll();
    renderDescEdit();
    return;
  }
  renderDescEdit();
  if (descEdit.state !== "thinking") stopDescEditPoll();
}

async function sendDescEditMessage(text: string): Promise<void> {
  if (!descEditCard || !text || descEdit.state === "thinking") return;
  descEdit = { ...descEdit, state: "thinking", messages: [...descEdit.messages, { role: "user", text }] };
  renderDescEdit();
  startDescEditPoll();
  try {
    descEdit = await go().ReplyDescEdit(descEdit.sessionId!, text);
  } catch (err) {
    descEdit = { ...descEdit, state: "error", error: errMessage(err) };
    stopDescEditPoll();
  }
  renderDescEdit();
}

async function applyDescEdit(): Promise<void> {
  if (!descEditCard || !descEditApplyEnabled(descEdit.state, descEdit.proposal)) return;
  const applyBtn = document.getElementById("descedit-apply") as HTMLButtonElement | null;
  // Disable-on-click before the round-trip: same guard as the detail panel's
  // decision buttons (prevents a slow daemon response from double-writing).
  if (applyBtn) applyBtn.disabled = true;
  try {
    descEdit = await go().ApplyDescEdit(descEdit.sessionId!);
    descEditSavedDescription = descEdit.proposal || descEditSavedDescription;
    descEditSavedHTML = null;
    renderDescEdit();
    reconcileEpoch++;
    await reconcile();
  } catch (err) {
    showError(errMessage(err));
    if (applyBtn) applyBtn.disabled = false;
  }
}

// --- Ticket detail panel ---------------------------------------------------
//
// A plain click on any card (board or Bugs pane) opens a read-only slide-out
// with the ticket's key, title, owner and description. It renders a snapshot
// of the clicked card, re-resolved by key after each render() so the full
// fetch backfills a description the quick titles-only pass left empty.

// chosenOptions tracks decisions made this session, keyed by ticket with the
// consumed block's signature. The board's comment-scan cache can lag the
// consumption by a full cycle, so fetched cards keep re-offering a block the
// user already chose — this is the optimistic local consumption that bridges
// the gap. A DIFFERENT signature on a later fetch is a NEW decision block and
// must show, so the entry clears itself (ticket 579).
const chosenOptions = new Map<string, { signature: string; optionID: string }>();

// optionsSignature identifies one decision block by its content, so stale
// re-offers of a consumed block are distinguishable from a genuinely new one.
function optionsSignature(options: { id: string; label: string }[] | undefined): string {
  return (options ?? []).map((o) => `${o.id}:${o.label}`).join("|");
}

// liveOptions returns the card's options with the session's consumed block
// suppressed — and retires the suppression once the server catches up or a
// new block appears.
function liveOptions(card: Card): Card["options"] {
  const chosen = chosenOptions.get(card.key);
  if (!chosen) return card.options;
  if (!card.options || card.options.length === 0) {
    // Server caught up: the consumed block is gone for real.
    chosenOptions.delete(card.key);
    return undefined;
  }
  if (optionsSignature(card.options) !== chosen.signature) {
    // A new decision block — the old choice must not mask it.
    chosenOptions.delete(card.key);
    return card.options;
  }
  return undefined;
}

let detailCard: Card | null = null;
// detailError surfaces a failed per-ticket backfill in the panel. A silent
// failure is indistinguishable from "the ticket has no description", which
// is exactly the confusion it must prevent.
let detailError: string | null = null;
// detailHTML is the daemon-rendered markdown of the open ticket's description.
// Caching lives in the daemon (stale-while-revalidate on the tracker-issue
// route), so the panel just shows whatever the last fetch returned.
let detailHTML: string | null = null;
// detailSections is the daemon-rendered HTML for the open ticket's
// comment-sourced sections (failure reason, review findings, fix summary),
// pre-built by buildDetailSections. Empty until fetchTicketDetail lands them.
let detailSections = "";

// detailCost holds the open ticket's durable cost/time rollup, fetched
// independently of the description so a cost-endpoint failure never blanks the
// rest of the panel. Null renders the plain "no spend recorded" empty state.
let detailCost: TicketCost | null = null;

// toggleTicketDetail is the card-click entry point: a second click on the
// ticket that is already open closes the panel instead of re-opening it.
function toggleTicketDetail(card: Card): void {
  if (detailCard && detailCard.key === card.key) {
    closeTicketDetail();
    return;
  }
  openTicketDetail(card);
}

function openTicketDetail(card: Card): void {
  // The detail panel and the ideation panel share the fixed right edge; only
  // one may be visible. Closing ideation keeps its session running (AD-4).
  closeIdeation();
  detailCard = card;
  detailError = null;
  detailHTML = null;
  detailSections = "";
  detailCost = null;
  renderTicketDetail();
  document.getElementById("detail-panel")?.classList.remove("hidden");
  void fetchTicketDetail(card);
}

// fetchTicketDetail backfills the panel from a per-ticket fetch: the board's
// list fetch is slim on some trackers (Shortcut returns stories without
// descriptions), so the card's own description can be empty even for a ticket
// that has one. The snapshot renders first; this fills in what the list missed.
async function fetchTicketDetail(card: Card): Promise<void> {
  try {
    const detail = await go().GetIssueDetail(card.trackerKind ?? "", card.tracker ?? "", card.key);
    // A slow fetch for a previously clicked card must never overwrite the
    // currently open one.
    if (!detailCard || detailCard.key !== card.key) return;
    detailError = null;
    detailHTML = detail.descriptionHTML || null;
    detailSections = buildDetailSections({
      reviewFindingsHTML: detail.reviewFindingsHTML,
      failureReasonHTML: detail.failureReasonHTML,
      fixSummaryHTML: detail.fixSummaryHTML,
      paused: card.state === "outage",
    });
    detailCard = {
      ...detailCard,
      title: detail.title || detailCard.title,
      assignee: detail.assignee || detailCard.assignee,
      description: detail.description || detailCard.description,
    };
    // The durable cost/time rollup is fetched independently: a cost-endpoint
    // failure must never blank the description panel it sits beside.
    try {
      const cost = await go().TicketCost(card.key);
      if (detailCard && detailCard.key === card.key) {
        detailCost = cost;
      }
    } catch {
      detailCost = null;
    }
    renderTicketDetail();
  } catch (err) {
    if (!detailCard || detailCard.key !== card.key) return;
    detailError = errMessage(err);
    renderTicketDetail();
  }
}

function closeTicketDetail(): void {
  detailCard = null;
  detailHTML = null;
  detailSections = "";
  detailCost = null;
  document.getElementById("detail-panel")?.classList.add("hidden");
}

// refreshTicketDetail re-renders the open panel from the freshest card with
// the same key. A card that left the board (e.g. closed elsewhere) keeps its
// last snapshot — stale-but-readable beats a panel that vanishes mid-read.
function refreshTicketDetail(): void {
  if (!detailCard) return;
  const key = detailCard.key;
  const fresh = current.cards.find((c) => c.key === key);
  if (fresh) {
    // Merge, don't replace: the fresh card comes from a slim list fetch whose
    // empty description/assignee must not wipe what fetchTicketDetail filled in.
    detailCard = {
      ...fresh,
      assignee: fresh.assignee || detailCard.assignee,
      description: fresh.description || detailCard.description,
    };
  }
  renderTicketDetail();
}

function renderTicketDetail(): void {
  if (!detailCard) return;
  const keyEl = document.getElementById("detail-key");
  if (keyEl) keyEl.textContent = detailCard.key;
  const body = document.getElementById("detail-body");
  if (!body) return;
  const owner = detailCard.assignee
    ? `<span class="detail-owner-name">${escapeHtml(detailCard.assignee)}</span>`
    : "Unassigned";
  // Prefer the daemon-rendered (and sanitized) HTML; fall back to escaped
  // plain text while it hasn't arrived, so the panel is never empty-handed.
  let desc: string;
  if (detailHTML) {
    desc = `<div class="detail-description rendered">${detailHTML}</div>`;
  } else if (detailCard.description) {
    desc = `<div class="detail-description">${escapeHtml(detailCard.description)}</div>`;
  } else {
    desc = `<div class="detail-description empty">No description</div>`;
  }
  const link = detailCard.url
    ? `<button type="button" class="detail-tracker-btn">Open in tracker</button>`
    : "";
  const error = detailError
    ? `<div class="detail-error">Couldn't load the full ticket: ${escapeHtml(detailError)}</div>`
    : "";
  // The open decision renders FIRST: when the pipeline is waiting on the
  // human, the choice is the panel's most actionable content. A decision made
  // this session renders as its confirmation instead — the comment-scan cache
  // may re-offer the consumed block for a full cycle (ticket 579).
  const chosen = chosenOptions.get(detailCard.key);
  const visibleOptions = liveOptions(detailCard);
  let options: string;
  if (chosen && !visibleOptions) {
    options =
      `<section class="detail-section detail-options"><h3 class="detail-section-title">Decision made</h3>` +
      `<div class="detail-options-context">Direction ${escapeHtml(chosen.optionID)} chosen — a fresh agent is pursuing it. ` +
      `The choice is recorded on the ticket.</div></section>`;
  } else {
    options = buildOptionsSection(detailCard.optionsContext, visibleOptions);
  }
  const stopDecision = buildStopDecisionSection(
    detailCard.stopDecision,
    detailCard.stopLinkedKey,
    detailCard.stopReasoning,
  );
  const shippedPartial = buildShippedPartialSection(detailCard.shippedPartial, detailCard.shippedPartialFollowOn);
  body.innerHTML = `
    <div class="detail-title">${escapeHtml(detailCard.title)}</div>
    <div class="detail-owner">Owner: ${owner}</div>
    ${error}
    ${options}
    ${stopDecision}
    ${shippedPartial}
    ${desc}
    ${detailSections}
    ${buildCostSection(detailCost, detailCard.stage, detailCard.stageEnteredAt, Date.now())}
    ${link}
  `;
  const url = detailCard.url;
  body.querySelector<HTMLButtonElement>(".detail-tracker-btn")?.addEventListener("click", () => openExternal(url));
  // The linked ticket named by a stop decision opens that card's detail when it
  // is on the board — the AC's reachability requirement (SC-2699).
  body.querySelector<HTMLButtonElement>(".detail-linked-btn")?.addEventListener("click", () => {
    const key = detailCard?.stopLinkedKey ?? detailCard?.shippedPartialFollowOn;
    if (!key) return;
    const linked = current.cards.find((c) => c.key === key);
    if (linked) openTicketDetail(linked);
  });
  const optionKey = detailCard.key;
  const optionSig = optionsSignature(visibleOptions);
  body.querySelectorAll<HTMLButtonElement>(".detail-option-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      // The click is the consent: disable all choices immediately so a slow
      // daemon round-trip can never dispatch two directions.
      body.querySelectorAll<HTMLButtonElement>(".detail-option-btn").forEach((b) => (b.disabled = true));
      const optionID = btn.dataset.optionId ?? "";
      // Optimistic consumption: confirm in place instead of waiting a full
      // comment-scan cycle for the server-derived card to catch up.
      const confirmChoice = (): void => {
        chosenOptions.set(optionKey, { signature: optionSig, optionID });
        renderTicketDetail();
        render();
      };
      void runGuardedAction(
        () => go().ChooseOption(optionKey, optionID),
        (err) => {
          const msg = errMessage(err);
          if (msg.includes("no open decision")) {
            // The guard refusing a double-dispatch is the feature working —
            // the decision is already made, which is a state, not a failure.
            confirmChoice();
            return;
          }
          // The dispatch never landed, so give the choices back rather than
          // leaving a dead panel, and leave the board alone — a reconcile here
          // would overwrite current.error with the (empty) fetch error and the
          // failure would flash away unseen (SC-637).
          body
            .querySelectorAll<HTMLButtonElement>(".detail-option-btn")
            .forEach((b) => (b.disabled = false));
          showError(msg);
        },
        async () => {
          confirmChoice();
          await reconcile();
        },
      );
    });
  });
  // Links inside the rendered description must leave via the system browser,
  // never navigate the webview away from the board.
  body.querySelectorAll<HTMLAnchorElement>("a").forEach((a) => {
    a.addEventListener("click", (e: MouseEvent) => {
      e.preventDefault();
      openExternal(a.href);
    });
  });
}

async function openIdeation(): Promise<void> {
  // Mirror of the exclusivity in openTicketDetail: both panels occupy the
  // fixed right edge, so opening one always closes the other.
  closeTicketDetail();
  const panel = document.getElementById("ideation-panel");
  if (panel) panel.classList.remove("hidden");
  ideationOpen = true;

  try {
    ideation = await go().IdeationStatus();
  } catch (err) {
    renderIdeationError(errMessage(err));
    return;
  }
  // Leave ideationMode as whatever it currently is: it starts null at module
  // load and is only reset by closeIdeation() for terminal/none states, so a
  // panel reopen mid-flow must not re-show a fresh mode picker.
  renderIdeation();
  if (ideation.state === "thinking") startIdeationPoll();
}

function closeIdeation(): void {
  const panel = document.getElementById("ideation-panel");
  if (panel) panel.classList.add("hidden");
  ideationOpen = false;
  stopIdeationPoll();
  // Closing does not abandon an active session (AD-4): only reset the mode
  // picker when there is no live session to reattach to on reopen.
  if (ideation.state === "done" || ideation.state === "error" || ideation.state === "none") {
    ideationMode = null;
  }
}

async function pollIdeation(): Promise<void> {
  try {
    ideation = await go().IdeationStatus();
  } catch (err) {
    renderIdeationError(errMessage(err));
    stopIdeationPoll();
    return;
  }
  if (shouldCloseIdeation(ideation.state, ideation.createdKey)) {
    // Terminal transition: the PM ticket was created — closeIdeation() hides the
    // panel, stops the poll, and resets the mode picker. The daemon's board:changed
    // event surfaces the new card (SC-859).
    closeIdeation();
    return;
  }
  renderIdeation();
  if (ideation.state !== "thinking") {
    stopIdeationPoll();
  }
}

// sendIdeationReply carries either the freeform input text or a clicked
// option's text into the running session — both are just `message: string`
// to ReplyIdeation, and `seed: string` to StartIdeation on a fresh session.
// awaiting_approval is never routed through here: the draft-review form
// (see renderIdeationDraft/approveIdeation) replaces the free-text form
// entirely while a session is in that state, so this function should not be
// invoked with a stale awaiting_approval state during a poll/input race.
async function sendIdeationReply(text: string): Promise<void> {
  if (!text || ideation.state === "awaiting_approval") return;

  const isFresh = ideation.state === "none" || ideation.state === "done" || ideation.state === "error";
  const restart = ideation.state === "done" || ideation.state === "error";

  // Optimistic update: show the user's message immediately and disable the
  // input while the turn is in flight.
  ideation = {
    ...ideation,
    state: "thinking",
    messages: [...ideation.messages, { role: "user", text }],
  };
  renderIdeation();
  startIdeationPoll();

  try {
    if (isFresh) {
      // SC-2858: every fresh session — however the panel was opened (Product
      // "+", post-import "Create first ticket", rail nav "new ticket") — must
      // start from a captured idea, never a ticket created outright. The
      // seed text becomes the idea's title, then the same conversation
      // continues in evolve mode against that freshly captured ticket,
      // mirroring the Ideas-column promotion path (promoteIdea) rather than
      // duplicating it. The idea marker stays on the ticket until the
      // conversation's terminal action (evolveTicket) removes it.
      const ideaKey = await go().CreateIdea(text);
      ideation = await go().StartIdeation(text, ideationMode ?? "chat", restart, ideaKey, []);
    } else {
      ideation = await go().ReplyIdeation(ideation.sessionId!, text);
    }
  } catch (err) {
    renderIdeationError(errMessage(err));
    stopIdeationPoll();
    return;
  }
  if (shouldCloseIdeation(ideation.state, ideation.createdKey)) {
    // Terminal transition: the PM ticket was created — closeIdeation() hides the
    // panel, stops the poll, and resets the mode picker. The daemon's board:changed
    // event surfaces the new card (SC-859).
    closeIdeation();
    return;
  }
  renderIdeation();
  if (ideation.state !== "thinking") {
    stopIdeationPoll();
  }
}

async function submitIdeation(): Promise<void> {
  const input = document.getElementById("ideation-input") as HTMLInputElement | null;
  if (!input) return;
  const text = input.value.trim();
  if (!text) return;
  input.value = "";
  await sendIdeationReply(text);
}

async function approveIdeation(): Promise<void> {
  const titleInput = document.getElementById("ideation-draft-title") as HTMLInputElement | null;
  const descInput = document.getElementById("ideation-draft-description") as HTMLTextAreaElement | null;
  if (!titleInput || !descInput || !ideation.sessionId) return;
  const sessionId = ideation.sessionId;

  ideation = { ...ideation, state: "thinking" };
  renderIdeation();
  startIdeationPoll();

  try {
    ideation = await go().ApproveIdeation(sessionId, titleInput.value.trim(), descInput.value);
  } catch (err) {
    renderIdeationError(errMessage(err));
    stopIdeationPoll();
    return;
  }
  if (shouldCloseIdeation(ideation.state, ideation.createdKey)) {
    // Terminal transition: the PM ticket was created — closeIdeation() hides the
    // panel, stops the poll, and resets the mode picker. The daemon's board:changed
    // event surfaces the new card (SC-859).
    closeIdeation();
    return;
  }
  renderIdeation();
  if (ideation.state !== "thinking") stopIdeationPoll();
}

// --- Start Project wizard ------------------------------------------------
//
// Offered exactly once per session, and only when the board is a *confirmed*
// empty board (successful fetch, zero cards) AND the project directory holds
// no source files — i.e. there is genuinely no project yet, just tool config.
// The steps are static local choices derived from the Go-side template
// registry, so unlike ideation there is no per-step backend round-trip: only
// the final scaffold call goes to Go.

type WizardStep = "type" | "language" | "creating" | "done" | "error";

// wizardChecked is the re-trigger guard: set before any await in
// maybeOfferStartProject so overlapping reconciles (board:changed storms)
// cannot probe or open twice. Dismissal therefore lasts for the session.
let wizardChecked = false;
let wizardOverlay: HTMLElement | null = null;
let wizardTemplates: StarterTemplate[] = [];
let wizardStep: WizardStep = "type";
let wizardType = "";
let wizardError = "";
let wizardCreated = 0;

async function maybeOfferStartProject(): Promise<void> {
  if (wizardChecked || current.error) return;
  // Cards on the board mean a project exists — settle without the FS probe,
  // but leave wizardChecked set: a non-empty board can only gain cards.
  wizardChecked = true;
  if (current.cards.length > 0) return;

  let info: StartProjectInfo;
  try {
    info = await go().StartProjectStatus();
  } catch {
    return;
  }
  // A failed probe (info.error) means "don't offer", never a broken app.
  if (info.error || !info.emptyProject) return;
  wizardTemplates = info.templates ?? [];
  if (wizardTemplates.length === 0) return;
  openStartWizard();
}

function wizardTypeChoices(): { type: string; label: string }[] {
  const seen = new Set<string>();
  const choices: { type: string; label: string }[] = [];
  wizardTemplates.forEach((t) => {
    if (seen.has(t.type)) return;
    seen.add(t.type);
    choices.push({ type: t.type, label: t.typeLabel });
  });
  return choices;
}

function wizardLanguageChoices(type: string): StarterTemplate[] {
  return wizardTemplates.filter((t) => t.type === type);
}

function openStartWizard(): void {
  if (wizardOverlay) return;
  wizardStep = "type";
  wizardType = "";
  wizardError = "";
  wizardCreated = 0;

  const overlay = document.createElement("div");
  overlay.className = "modal-overlay";
  const modal = document.createElement("div");
  modal.className = "modal wizard";
  overlay.appendChild(modal);
  document.body.appendChild(overlay);
  wizardOverlay = overlay;

  const onKey = (e: KeyboardEvent): void => {
    // No escape while the download runs: the state is not cancellable from
    // here and a hidden in-flight scaffold would be surprising.
    if (e.key === "Escape" && wizardStep !== "creating") closeStartWizard();
  };
  overlay.addEventListener("click", (e: MouseEvent) => {
    if (e.target === overlay && wizardStep !== "creating") closeStartWizard();
  });
  document.addEventListener("keydown", onKey);
  overlay.dataset.bound = "true";
  // Store the handler so closeStartWizard can unbind it.
  (overlay as HTMLElement & { _onKey?: (e: KeyboardEvent) => void })._onKey = onKey;

  renderStartWizard();
}

function closeStartWizard(): void {
  if (!wizardOverlay) return;
  const onKey = (wizardOverlay as HTMLElement & { _onKey?: (e: KeyboardEvent) => void })._onKey;
  if (onKey) document.removeEventListener("keydown", onKey);
  wizardOverlay.remove();
  wizardOverlay = null;
}

function renderStartWizard(): void {
  if (!wizardOverlay) return;
  const modal = wizardOverlay.querySelector<HTMLElement>(".wizard");
  if (!modal) return;

  if (wizardStep === "type") {
    modal.innerHTML = `
      <div class="modal-title">Start a new project</div>
      <div class="modal-body">This folder has no project yet. What do you want to build?</div>
      <div class="wizard-options"></div>
    `;
    const options = modal.querySelector<HTMLElement>(".wizard-options")!;
    wizardTypeChoices().forEach((choice) => {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "wizard-option";
      btn.textContent = choice.label;
      btn.addEventListener("click", () => {
        wizardType = choice.type;
        wizardStep = "language";
        renderStartWizard();
      });
      options.appendChild(btn);
    });
    return;
  }

  if (wizardStep === "language") {
    modal.innerHTML = `
      <div class="modal-title">Choose a language</div>
      <div class="modal-body">The project is set up ready to run.</div>
      <div class="wizard-options"></div>
      <div class="wizard-nav"><button class="wizard-back" type="button">Back</button></div>
    `;
    const options = modal.querySelector<HTMLElement>(".wizard-options")!;
    wizardLanguageChoices(wizardType).forEach((tpl) => {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "wizard-option";
      btn.textContent = tpl.languageLabel;
      btn.addEventListener("click", () => void createStartProject(tpl));
      options.appendChild(btn);
    });
    modal.querySelector(".wizard-back")!.addEventListener("click", () => {
      wizardStep = "type";
      renderStartWizard();
    });
    return;
  }

  if (wizardStep === "creating") {
    modal.innerHTML = `
      <div class="modal-title">Creating project…</div>
      <div class="wizard-status"><span class="spinner"></span><span>Downloading starter template</span></div>
    `;
    return;
  }

  if (wizardStep === "done") {
    modal.innerHTML = `
      <div class="modal-title">Project created</div>
      <div class="modal-body">${escapeHtml(`${wizardCreated} files added. Create a first ticket to start working on it.`)}</div>
      <div class="modal-actions">
        <button class="modal-cancel" type="button">Close</button>
        <button class="modal-confirm" type="button">Create first ticket</button>
      </div>
    `;
    modal.querySelector(".modal-cancel")!.addEventListener("click", () => closeStartWizard());
    modal.querySelector(".modal-confirm")!.addEventListener("click", () => {
      closeStartWizard();
      void openIdeation();
    });
    return;
  }

  // error
  modal.innerHTML = `
    <div class="modal-title">Could not create project</div>
    <div class="modal-body wizard-error">${escapeHtml(wizardError)}</div>
    <div class="modal-actions">
      <button class="modal-cancel" type="button">Close</button>
      <button class="modal-confirm" type="button">Try again</button>
    </div>
  `;
  modal.querySelector(".modal-cancel")!.addEventListener("click", () => closeStartWizard());
  modal.querySelector(".modal-confirm")!.addEventListener("click", () => {
    wizardStep = "language";
    renderStartWizard();
  });
}

async function createStartProject(tpl: StarterTemplate): Promise<void> {
  wizardStep = "creating";
  renderStartWizard();
  try {
    const res = await go().StartProject(tpl.type, tpl.language);
    wizardCreated = res.filesCreated;
    wizardStep = "done";
  } catch (err) {
    wizardError = errMessage(err);
    wizardStep = "error";
  }
  renderStartWizard();
}

// --- Running agents view -----------------------------------------------
//
// The desktop equivalent of the TUI's instances panel. Data comes from the Go
// App.Instances() binding, which runs the monitor in-process (not via the
// daemon). The view only polls while it is the active view — the discovery scan
// is cheap but pointless for a hidden panel, mirroring the ideation poll.

let agentsData: InstancesData = { agents: [] };
let agentsTimer: number | null = null;
const AGENTS_POLL_MS = 2000;

function stopAgentsPoll(): void {
  if (agentsTimer !== null) {
    clearInterval(agentsTimer);
    agentsTimer = null;
  }
}

function startAgentsPoll(): void {
  if (agentsTimer !== null) return;
  agentsTimer = window.setInterval(() => void pollAgents(), AGENTS_POLL_MS);
}

async function pollAgents(): Promise<void> {
  try {
    agentsData = await go().Instances();
  } catch (err) {
    agentsData = { agents: [], error: errMessage(err) };
  }
  renderAgents();
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1e3).toFixed(1)}K`;
  return String(n);
}

// formatElapsedUnix mirrors the TUI's formatElapsed: seconds under a minute,
// "Nm Ns" under an hour, "Nh Nm" beyond. startedAtUnix of 0 means "unknown".
function formatElapsedUnix(startedAtUnix: number): string {
  if (!startedAtUnix) return "";
  const secs = Math.max(0, Math.floor(Date.now() / 1000) - startedAtUnix);
  if (secs < 60) return `${secs}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ${secs % 60}s`;
  return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
}

function formatDurationMs(ms: number): string {
  const secs = Math.round(ms / 1000);
  if (secs < 60) return `${secs}s`;
  return `${Math.floor(secs / 60)}m ${secs % 60}s`;
}

function agentStatusDot(a: AgentInstance): string {
  // Mirrors the TUI sessionIcon: a spinner while working, ⚠ on error, and a
  // coloured ● otherwise — with idle splitting on whether the session has seen
  // any activity (● active vs ○ never-active).
  if (a.status === "working") return `<span class="agent-dot working"><span class="spinner"></span></span>`;
  if (a.status === "error") return `<span class="agent-dot error">⚠</span>`;
  if (a.status === "blocked") return `<span class="agent-dot blocked">●</span>`;
  if (a.status === "waiting") return `<span class="agent-dot waiting">●</span>`;
  if (a.hasActivity) return `<span class="agent-dot active">●</span>`;
  return `<span class="agent-dot idle">○</span>`;
}

function tokenBars(models: ModelUsage[]): string {
  const total = models.reduce((sum, m) => sum + m.inputTokens + m.outputTokens, 0);
  if (total === 0) return "";
  return [...models]
    .sort((x, y) => x.name.localeCompare(y.name))
    .map((m) => {
      const pct = ((m.inputTokens + m.outputTokens) / total) * 100;
      return `<div class="token-row">
        <span class="token-model">${escapeHtml(m.name)}</span>
        <span class="token-bar"><span class="token-bar-fill" style="width:${pct.toFixed(0)}%"></span></span>
        <span class="token-stats">${pct.toFixed(0)}%  ${formatTokens(m.inputTokens)} in  ${formatTokens(m.outputTokens)} out</span>
      </div>`;
    })
    .join("");
}

function taskLine(a: AgentInstance): string {
  const parts: string[] = [];
  if (a.tasksPending > 0) parts.push(`${a.tasksPending} pending`);
  if (a.tasksInProgress > 0) parts.push(`${a.tasksInProgress} in progress`);
  if (a.tasksDone > 0) parts.push(`${a.tasksDone} done`);
  if (parts.length === 0) return "";
  return `<div class="agent-tasks">Tasks: ${escapeHtml(parts.join(" · "))}</div>`;
}

// subagentLines mirrors the TUI renderSubagents: drop agents completed >5s ago,
// show at most the last 5, spinner for running and ✓ for done.
function subagentLines(subs: SubagentInfo[]): string {
  const now = Date.now();
  const visible = subs.filter(
    (s) => !s.done || now - (s.startedAtUnix * 1000 + s.durationMs) <= 5000,
  );
  const shown = visible.slice(Math.max(0, visible.length - 5));
  return shown
    .map((s) => {
      const type = s.type || "agent";
      const desc = escapeHtml(s.description);
      if (s.done) {
        const dur = s.durationMs > 0 ? formatDurationMs(s.durationMs) : "";
        return `<div class="agent-subagent done">✓ ${desc} <span class="subagent-meta">(${escapeHtml(type)}${dur ? ", " + dur : ""})</span></div>`;
      }
      const elapsed = formatElapsedUnix(s.startedAtUnix);
      return `<div class="agent-subagent"><span class="spinner"></span> ${desc} <span class="subagent-meta">(${escapeHtml(type)}${elapsed ? ", " + elapsed : ""})</span></div>`;
    })
    .join("");
}

function renderAgentRow(a: AgentInstance): string {
  const chips: string[] = [];
  if (a.daemonConnected) chips.push(`<span class="agent-chip proxy">${a.proxyConfigured ? "⚡+proxy" : "⚡"}</span>`);
  else if (a.proxyConfigured) chips.push(`<span class="agent-chip proxy">proxy</span>`);
  if (a.memory) chips.push(`<span class="agent-chip">${escapeHtml(a.memory)}</span>`);
  const elapsed = formatElapsedUnix(a.startedAtUnix);
  if (elapsed) chips.push(`<span class="agent-chip">${elapsed}</span>`);
  if (a.slug) chips.push(`<span class="agent-chip slug">${escapeHtml(a.slug)}</span>`);
  const ctx = a.errorType || a.blockedTool || a.currentTool;
  if (ctx) chips.push(`<span class="agent-chip ctx">${escapeHtml(a.errorType ? a.errorType : a.blockedTool ? `⚠ ${a.blockedTool}` : `[${a.currentTool}]`)}</span>`);

  const rowClass = a.status === "blocked" ? "agent-row blocked" : "agent-row";
  return `<div class="${rowClass}">
    <div class="agent-head">
      ${agentStatusDot(a)}
      <span class="agent-label">${escapeHtml(a.label)}</span>
      ${chips.join("")}
    </div>
    ${tokenBars(a.models)}
    ${taskLine(a)}
    ${subagentLines(a.subagents)}
  </div>`;
}

function renderAgents(): void {
  const host = document.getElementById("agents");
  if (!host) return;
  if (agentsData.error) {
    host.innerHTML = `<div class="agents-header">Running agents</div><div class="banner">${escapeHtml(agentsData.error)}</div>`;
    return;
  }
  if (agentsData.agents.length === 0) {
    host.innerHTML = `<div class="agents-header">Running agents</div><div class="agents-empty">No active instances</div>`;
    return;
  }
  host.innerHTML =
    `<div class="agents-header">Running agents</div>` + agentsData.agents.map(renderAgentRow).join("");
}

// --- Features view -----------------------------------------------------
//
// Renders the project's FEATURE.json (grouped product features) from the Go
// App.Features() binding — a plain file read, no daemon. Unlike the agents view
// this is a static document, so it loads once on activation with no poll.

let featuresLoaded = false;
// Generation runs as a detached agent (like a kanban stage), so the button
// can't block on completion. We capture the currently-shown doc's signature
// when generation starts, then poll Features() until it changes — the file
// appearing (Generate) or its content shifting (Refresh) both flip the button
// back and re-render. currentFeatureDoc is the last doc rendered; featuresNote
// carries a transient status/error line without wiping the rendered map.
let featuresGenerating = false;
let featuresBaselineSig = "";
let featuresNote = "";
let currentFeatureDoc: FeatureDoc = {};
let featuresPollTimer: number | undefined;

async function loadFeatures(): Promise<void> {
  let doc: FeatureDoc;
  try {
    doc = await go().Features();
  } catch (err) {
    doc = { error: errMessage(err) };
  }
  renderFeatures(doc);
}

// featureSig is a stable fingerprint of the rendered doc: presence plus product,
// tagline, and the recursive group/feature names+descriptions. Two runs that
// produce the same map yield the same signature, so polling only reacts to a
// real change.
function featureSig(doc: FeatureDoc): string {
  if (!doc.exists) return "«sent»";
  const walk = (gs: FeatureGroup[] = []): string =>
    gs
      .map(
        (g) =>
          `${g.group}|${(g.features ?? [])
            .map((f) => `${f.name}:${f.description}${f.recent ? "*" : ""}`)
            .join(",")}|${walk(g.groups)}`,
      )
      .join(";");
  return `${doc.product ?? ""}¦${doc.tagline ?? ""}¦${walk(doc.groups)}`;
}

function stopFeaturesPoll(): void {
  if (featuresPollTimer !== undefined) {
    clearInterval(featuresPollTimer);
    featuresPollTimer = undefined;
  }
}

// startFeaturesPoll watches for the generation agent's output. It re-reads
// FEATURE.json every 4s and, when the doc's signature differs from the baseline
// captured at click time, stops and re-renders. A 10-minute cap avoids polling
// forever if the agent is slow or fails silently.
function startFeaturesPoll(): void {
  stopFeaturesPoll();
  const started = Date.now();
  const timeoutMs = 10 * 60 * 1000;
  featuresPollTimer = window.setInterval(() => {
    void (async () => {
      let doc: FeatureDoc;
      try {
        doc = await go().Features();
      } catch {
        return; // transient; keep polling
      }
      if (featureSig(doc) !== featuresBaselineSig) {
        stopFeaturesPoll();
        featuresGenerating = false;
        featuresNote = "";
        renderFeatures(doc);
        return;
      }
      if (Date.now() - started > timeoutMs) {
        stopFeaturesPoll();
        featuresGenerating = false;
        featuresNote = "Agent still running — click Refresh when it finishes.";
        renderFeatures(currentFeatureDoc);
      }
    })();
  }, 4000);
}

// onGenerateFeatures drives both Generate and Refresh: it launches the
// human-features skill through the daemon (the same containerized agent path as
// a kanban drag-and-drop), flips the button to a disabled "Generating…" state,
// and starts polling for the result.
async function onGenerateFeatures(): Promise<void> {
  if (featuresGenerating) return;
  featuresBaselineSig = featureSig(currentFeatureDoc);
  featuresGenerating = true;
  // Generation runs a coding agent in a container (survey → synthesis), so it
  // is not instant — set expectations up front and keep the note up while the
  // poll waits for FEATURE.json.
  featuresNote = "Running the generation agent — this can take several minutes…";
  renderFeatures(currentFeatureDoc);
  try {
    await go().GenerateFeatures();
  } catch (err) {
    featuresGenerating = false;
    featuresNote = `Couldn't start generation: ${errMessage(err)}`;
    renderFeatures(currentFeatureDoc);
    return;
  }
  startFeaturesPoll();
}

function renderFeatureRow(f: FeatureItem): string {
  // A "recent" badge flags a capability changed since the last release. Ticket
  // keys in FEATURE.json are deliberately not surfaced here — the desktop pane
  // presents features from a user's point of view, not their engineering trail.
  const badge = f.recent ? `<span class="feature-badge">recent</span>` : "";
  return `<div class="feature-row">
    <span class="feature-name">${escapeHtml(f.name)}${badge}</span>
    <span class="feature-desc">${escapeHtml(f.description)}</span>
  </div>`;
}

// Recursive: a group renders its own features, then any nested sub-groups. depth
// drives indentation so a deeper tree (larger projects) reads as a shallow
// hierarchy rather than a flat wall.
function renderFeatureGroup(g: FeatureGroup, depth = 0): string {
  const rows = (g.features ?? []).map(renderFeatureRow).join("");
  const subgroups = (g.groups ?? []).map((sg) => renderFeatureGroup(sg, depth + 1)).join("");
  return `<div class="feature-group" data-depth="${depth}">
    <div class="feature-group-title">${escapeHtml(g.group)}</div>
    ${rows}
    ${subgroups}
  </div>`;
}

function renderFeatures(doc: FeatureDoc): void {
  currentFeatureDoc = doc;
  const host = document.getElementById("features");
  if (!host) return;

  // The action button reads "Generate" when FEATURE.json is absent and "Refresh"
  // when present; while an agent runs it is a disabled "Generating…" spinner.
  const label = featuresGenerating ? "Generating…" : doc.exists ? "Refresh" : "Generate";
  const spinner = featuresGenerating ? `<span class="spinner"></span> ` : "";
  const btn = `<button class="features-btn" ${featuresGenerating ? "disabled" : ""}>${spinner}${escapeHtml(label)}</button>`;
  const header = `<div class="agents-header features-header"><span>Features</span>${btn}</div>`;
  const note = featuresNote ? `<div class="features-note">${escapeHtml(featuresNote)}</div>` : "";

  const attach = () =>
    host.querySelector(".features-btn")?.addEventListener("click", () => void onGenerateFeatures());

  if (doc.error) {
    host.innerHTML = header + note + `<div class="banner">${escapeHtml(doc.error)}</div>`;
    attach();
    return;
  }
  if (!doc.exists) {
    host.innerHTML =
      header + note + `<div class="features-empty">No FEATURE.json yet — click Generate to build it.</div>`;
    attach();
    return;
  }
  const groups = doc.groups ?? [];
  if (groups.length === 0) {
    host.innerHTML = header + note + `<div class="features-empty">No features to show</div>`;
    attach();
    return;
  }
  const intro =
    doc.product || doc.tagline
      ? `<div class="features-intro">
          ${doc.product ? `<div class="features-product">${escapeHtml(doc.product)}</div>` : ""}
          ${doc.tagline ? `<div class="features-tagline">${escapeHtml(doc.tagline)}</div>` : ""}
        </div>`
      : "";
  host.innerHTML = header + note + intro + groups.map(renderFeatureGroup).join("");
  attach();
}

// --- Left activity rail ------------------------------------------------
//
// "board" and "agents" are real views swapped in the main area; other rail
// items are disabled placeholders. Adding a view means an enabled `.rail-item`
// in index.html plus a `case` in selectView — the agents view is the reference.

function selectView(view: string): void {
  document.querySelectorAll<HTMLElement>(".rail-item").forEach((item) => {
    const active = item.dataset.view === view;
    item.classList.toggle("active", active);
    if (active) item.setAttribute("aria-current", "page");
    else item.removeAttribute("aria-current");
  });

  // Toggle main-area containers: exactly one top-level view is visible.
  const board = document.getElementById("board");
  const bugs = document.getElementById("bugs");
  const agents = document.getElementById("agents");
  const features = document.getElementById("features");
  const mockups = document.getElementById("mockups");
  const settings = document.getElementById("settings");
  const stats = document.getElementById("stats");
  board?.classList.toggle("hidden", view !== "board");
  bugs?.classList.toggle("hidden", view !== "bugs");
  agents?.classList.toggle("hidden", view !== "agents");
  features?.classList.toggle("hidden", view !== "features");
  mockups?.classList.toggle("hidden", view !== "mockups");
  settings?.classList.toggle("hidden", view !== "settings");
  stats?.classList.toggle("hidden", view !== "stats");

  if (view === "agents") {
    void pollAgents(); // immediate fetch so the view isn't blank until the first tick
    startAgentsPoll();
  } else {
    stopAgentsPoll();
  }

  // Stats polls only while active (like agents): the network panel is live, so a
  // slow poll keeps it fresh; leaving the view stops the poll.
  if (view === "stats") {
    void showStats();
    startStatsPoll();
  } else {
    stopStatsPoll();
  }

  // The features doc is static — load it once on first activation, then leave
  // the rendered pane in place (no poll, unlike agents).
  if (view === "features" && !featuresLoaded) {
    featuresLoaded = true;
    void loadFeatures();
  }

  // Mockups rescan on every activation so a set generated while the app was
  // open appears without a restart (no poll: disk only changes via the skill).
  if (view === "mockups") {
    void showMockups();
  }

  // Settings refresh on every activation — .humanconfig can change on disk at
  // any time (CLI, agents, editors), so a stale form must never be shown.
  if (view === "settings") {
    void showSettings();
  }
}

function wireRail(): void {
  document.querySelectorAll<HTMLButtonElement>(".rail-item").forEach((item) => {
    // Disabled placeholders are inert via the native `disabled` attribute.
    if (item.disabled) return;
    item.addEventListener("click", () => {
      // Action items trigger a command (e.g. the ideation chat) rather than
      // switching the active view.
      if (item.dataset.action === "ideation") {
        void openIdeation();
        return;
      }
      const view = item.dataset.view;
      if (view) selectView(view);
    });
  });
}

function init(): void {
  if (window.runtime?.EventsOn) {
    window.runtime.EventsOn("board:changed", () => {
      void reconcile();
    });
    // Fired from closeflow.go's runCloseFlow when the daemon is busy — the
    // window close is already held open (OnBeforeClose returned true); this
    // dialog's choice is the only thing that can let it proceed (SC-3015).
    window.runtime.EventsOn("app:close-busy", () => {
      void (async () => {
        const choice = await busyCloseDialog();
        try {
          await go().ResolveClose(choice);
        } catch {
          // Best-effort: a failed round-trip must not strand the app — the
          // user can simply try the window's close button again.
        }
      })();
    });
  }
  void bootstrapProject();

  wireRail();
  initFancy();
  initPermissions(() => go(), applyPermissionDecision);
  initMockupsView(() => go());
  initSettingsView(() => go());
  initProjectsView(
    () => go(),
    (project) => {
      showAppShell(project.name);
      startBoardPolling();
    },
  );
  document.getElementById("switch-project-btn")?.addEventListener("click", () => {
    void (async () => {
      try {
        await go().SwitchProject();
      } catch {
        // Best-effort: even if the stop call fails, still show the
        // picker — OpenProject's own StopIfRunning covers a stale daemon.
      }
      showOverviewScreen();
    })();
  });
  initStatsView(() => go());
  initPalette({ index: settingsIndex, refresh: showSettings, save: saveSetting });
  setPaletteOpener(() => openPalette());
  // The daemon status line deep-links to its home: Settings → Daemon shows
  // status, registered projects, and the daemon-related config.
  document.getElementById("statusbar")?.addEventListener("click", () => {
    setActiveSection("daemon");
    selectView("settings");
  });
  document.addEventListener("keydown", (e: KeyboardEvent) => {
    // Palette chord first: Ctrl+, must win even while an input has focus.
    if (isPaletteChord(e)) {
      e.preventDefault();
      openPalette();
      return;
    }
    if (isThemeToggleChord(e)) {
      e.preventDefault();
      toggleTheme();
    }
  });
  document.getElementById("ideation-close")?.addEventListener("click", () => closeIdeation());
  document.getElementById("detail-close")?.addEventListener("click", () => closeTicketDetail());
  document.getElementById("ideation-form")?.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    void submitIdeation();
  });
  document.querySelectorAll<HTMLButtonElement>(".ideation-mode-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      const mode = btn.dataset.mode === "guided" ? "guided" : "chat";
      ideationMode = mode;
      renderIdeation();
    });
  });
  document.getElementById("ideation-draft-submit")?.addEventListener("click", () => void approveIdeation());
}

// Window resizes are covered by sizeWatcher (the columns resize with the
// window), so no separate resize listener is needed.

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", init);
} else {
  init();
}
