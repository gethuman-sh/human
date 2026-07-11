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
import { initPermissions, PermissionRequest } from "./permissions.js";

interface Card {
  key: string;
  title: string;
  url: string;
  stage: string;
  state: string;
  engineeringKey?: string;
  branch?: string;
  prURL?: string;
  error?: string;
}

interface BoardData {
  cards: Card[];
  dockerAvailable: boolean;
  error?: string;
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

interface AppBindings {
  Cards(): Promise<BoardData>;
  CardsQuick(): Promise<BoardData>;
  Transition(pmKey: string, pmTitle: string, from: string, to: string): Promise<void>;
  CloseTicket(pmKey: string): Promise<void>;
  DaemonStatus(): Promise<boolean>;
  StartIdeation(seed: string, mode: string, restart: boolean): Promise<IdeationView>;
  ReplyIdeation(sessionId: string, message: string): Promise<IdeationView>;
  ApproveIdeation(sessionId: string, title: string, description: string): Promise<IdeationView>;
  IdeationStatus(): Promise<IdeationView>;
  Instances(): Promise<InstancesData>;
  Features(): Promise<FeatureDoc>;
  GenerateFeatures(): Promise<void>;
  StartProjectStatus(): Promise<StartProjectInfo>;
  StartProject(projectType: string, language: string): Promise<StartProjectResult>;
  PendingPermissions(): Promise<PermissionRequest[]>;
  DecidePermission(id: string, approved: boolean): Promise<void>;
}

// This file is a module (see the trailing `export {}`) so the global
// augmentation below is legal and the local `go()` helper stays module-scoped
// instead of colliding with `window.go`.
declare global {
  interface Window {
    go?: { main?: { App?: AppBindings } };
    runtime?: { EventsOn(name: string, cb: () => void): void };
  }
}

export {};

const STAGES = ["backlog", "planning", "implementation", "verification", "done"] as const;
const STAGE_LABELS: Record<string, string> = {
  backlog: "Backlog",
  planning: "Product planning",
  implementation: "Implementation",
  verification: "Verification",
  done: "Done",
};

const AGENT_STAGES = new Set(["planning", "implementation", "verification"]);

let current: BoardData = { cards: [], dockerAvailable: true, error: "" };
let dragging: { key: string; title: string; stage: string } | null = null;

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

let daemonReachable = false;

function go(): AppBindings {
  const app = window.go?.main?.App;
  if (!app) throw new Error("Wails bindings not available");
  return app;
}

function stageIndex(stage: string): number {
  return (STAGES as readonly string[]).indexOf(stage);
}

function isAdjacentForward(from: string, to: string): boolean {
  return stageIndex(to) === stageIndex(from) + 1;
}

function targetEnabled(to: string): boolean {
  if (AGENT_STAGES.has(to) && !current.dockerAvailable) return false;
  return true;
}

function badge(state: string): string {
  if (state === "done") return `<span class="badge done" title="Stage complete">✓</span>`;
  if (state === "running")
    return `<span class="badge running" title="Agent running"><span class="spinner"></span></span>`;
  if (state === "failed") return `<span class="badge failed" title="Stage failed">✕</span>`;
  return "";
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

  const meta: string[] = [];
  if (stagesLoading) {
    // Titles are shown but this card's real stage is still being derived from
    // comments; a resolving spinner signals it may still move columns.
    meta.push(`<span class="badge resolving" title="Resolving stage…"><span class="spinner"></span></span>`);
  }
  const b = badge(card.state);
  if (b) meta.push(b);
  if (card.engineeringKey) meta.push(`<span>${escapeHtml(card.engineeringKey)}</span>`);
  if (card.prURL) meta.push(`<a href="${escapeAttr(card.prURL)}" target="_blank">PR</a>`);

  el.innerHTML = `
    <div class="card-key">${escapeHtml(card.key)}</div>
    <div class="card-title">${escapeHtml(card.title)}</div>
    <div class="card-meta">${meta.join("")}</div>
    ${card.error ? `<div class="card-error">${escapeHtml(card.error)}</div>` : ""}
  `;

  beginPointerDrag(el, card);
  return el;
}

function renderColumn(stage: string): HTMLElement {
  const col = document.createElement("section");
  col.className = "column";
  col.dataset.stage = stage;

  const cards = current.cards.filter((c) => c.stage === stage);

  const header = document.createElement("div");
  header.className = "column-header";
  if (stage === "backlog") {
    header.innerHTML =
      `<span>${STAGE_LABELS[stage]}</span>` +
      `<button class="add-card" title="New ticket via ideation">+</button>` +
      `<span class="column-count">${cards.length}</span>`;
    header.querySelector(".add-card")!.addEventListener("click", () => void openIdeation());
  } else {
    header.innerHTML = `<span>${STAGE_LABELS[stage]}</span><span class="column-count">${cards.length}</span>`;
  }
  col.appendChild(header);

  const body = document.createElement("div");
  body.className = "column-body";
  for (const card of cards) body.appendChild(renderCard(card));

  if (stage === "done") {
    const zone = document.createElement("div");
    zone.className = "done-zone";
    zone.textContent = "Drop here to open a pull request";
    markStageTarget(zone, stage);
    body.appendChild(zone);

    // A card from ANY column can be dropped here to close its ticket — closing
    // is not an adjacency-gated stage move, so it is its own kind of target.
    const closeZone = document.createElement("div");
    closeZone.className = "close-zone";
    closeZone.textContent = "Close Ticket";
    markCloseTarget(closeZone);
    body.appendChild(closeZone);
  } else if (stage !== "backlog") {
    markStageTarget(body, stage);
  }

  col.appendChild(body);
  return col;
}

// --- Pointer-based drag ------------------------------------------------
//
// The board does NOT use native HTML5 drag-and-drop: WebKitGTK (the Linux
// webview backend) does not fire native drag events, so the board would be
// completely undraggable there. Instead the card tracks pointer events itself
// and hit-tests drop targets with elementFromPoint. Drop targets are plain
// elements tagged with data-drop ("stage" | "close") via the mark* helpers; a
// floating ghost (pointer-events:none) follows the cursor.

const DRAG_THRESHOLD_PX = 5;
let dragGhost: HTMLElement | null = null;
let hoverTarget: HTMLElement | null = null;

function markStageTarget(el: HTMLElement, to: string): void {
  el.dataset.drop = "stage";
  el.dataset.dropStage = to;
}

function markCloseTarget(el: HTMLElement): void {
  el.dataset.drop = "close";
}

// dropTargetAt returns the drop-target element under a viewport point, if any.
// The ghost has pointer-events:none, so it never occludes the hit-test.
function dropTargetAt(x: number, y: number): HTMLElement | null {
  const el = document.elementFromPoint(x, y) as HTMLElement | null;
  return el ? (el.closest("[data-drop]") as HTMLElement | null) : null;
}

// dropAllowed reports whether the dragged card may drop on target. Any card can
// be closed; stage targets keep the forward-adjacency + docker-enabled rules.
function dropAllowed(target: HTMLElement): boolean {
  if (!dragging) return false;
  if (target.dataset.drop === "close") return true;
  const to = target.dataset.dropStage || "";
  return isAdjacentForward(dragging.stage, to) && targetEnabled(to);
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
  // The close zone only shows its own accept state; stage zones also show
  // drop-reject to signal an invalid move.
  target.classList.toggle("drop-ok", ok);
  if (target.dataset.drop !== "close") target.classList.toggle("drop-reject", !ok);
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
      teardown();
      endDrag();
      // `target` may have been replaced by the flushed render, but performDrop
      // only reads its dataset, which a detached node still carries.
      if (target && allowed) performDrop(target, info, { x: ev.clientX, y: ev.clientY });
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
  if (target.dataset.drop === "close") {
    // Closing is destructive and still awaits a confirm dialog — never a
    // moment to celebrate, so the fancy theme stays silent here.
    void requestClose(info.key, info.title);
    return;
  }
  const to = target.dataset.dropStage || "";
  celebrateDrop(pt, { key: info.key, fromStage: info.stage, done: to === "done" });
  void transition(info.key, info.title, info.stage, to);
}

async function transition(key: string, title: string, from: string, to: string): Promise<void> {
  const card = current.cards.find((c) => c.key === key);
  if (card) {
    card.stage = to;
    card.state = "running";
    render();
  }
  try {
    await go().Transition(key, title, from, to);
  } catch (err) {
    showError(errMessage(err));
  }
  await reconcile();
}

// requestClose confirms in-app (never the OS dialog) before closing, so a stray
// drop cannot silently close a ticket.
async function requestClose(key: string, title: string): Promise<void> {
  const ok = await confirmDialog(
    `Close ticket ${key}?`,
    `“${title}” will be marked Done and removed from the board.`,
    "Close ticket",
  );
  if (ok) await closeTicket(key);
}

async function closeTicket(key: string): Promise<void> {
  try {
    await go().CloseTicket(key);
  } catch (err) {
    showError(errMessage(err));
  }
  // The closed ticket is no longer "open", so reconcile drops it from the board.
  await reconcile();
}

// confirmDialog renders a small modal overlay and resolves true/false on the
// user's choice. Overlay-click and Escape count as cancel. Built with the same
// imperative-DOM approach as the rest of the app (no framework).
function confirmDialog(title: string, body: string, confirmLabel: string): Promise<boolean> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "modal-overlay";

    const modal = document.createElement("div");
    modal.className = "modal";
    modal.innerHTML = `
      <div class="modal-title">${escapeHtml(title)}</div>
      <div class="modal-body">${escapeHtml(body)}</div>
      <div class="modal-actions">
        <button class="modal-cancel" type="button">Cancel</button>
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
  } else {
    for (const stage of STAGES) board.appendChild(renderColumn(stage));
    restoreColumnScroll(board, scrollByStage);
  }
  const banner = document.getElementById("banner")!;
  if (current.error) {
    banner.textContent = current.error;
    banner.classList.remove("hidden");
  } else {
    banner.classList.add("hidden");
  }
}

function showError(msg: string): void {
  current.error = msg;
  render();
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
}

// initialLoad renders the board progressively on startup: a spinner first, then
// ticket titles (Backlog) from the fast comment-scan-free fetch, then the full
// reconcile that moves each card into its real stage. Steady-state updates use
// reconcile() directly so they never flash the spinner or re-place cards.
async function initialLoad(): Promise<void> {
  boardLoading = true;
  render();
  try {
    const quick = await go().CardsQuick();
    current = {
      cards: quick.cards || [],
      dockerAvailable: !!quick.dockerAvailable,
      // Suppress the quick-phase error: the full reconcile surfaces it, and
      // clearing it here avoids a banner that flickers away a moment later.
      error: "",
    };
    boardLoading = false;
    stagesLoading = true;
    render();
  } catch {
    // Quick phase failed (e.g. daemon not up yet): fall through to the full
    // fetch, which surfaces the error via reconcile().
    boardLoading = false;
  }
  await reconcile();
}

// reconcile fetches the full board (including derived stages) and renders it. It
// is the single source of truth after the initial load: board:changed events and
// post-transition refreshes call it directly.
async function reconcile(): Promise<void> {
  try {
    const data = await go().Cards();
    current = {
      cards: data.cards || [],
      dockerAvailable: !!data.dockerAvailable,
      error: data.error || "",
    };
  } catch (err) {
    current = { cards: [], dockerAvailable: false, error: errMessage(err) };
  }
  boardLoading = false;
  stagesLoading = false;
  render();
  // Offered at most once per session, and only off a confirmed-empty board —
  // see the Start Project wizard section for the guards.
  void maybeOfferStartProject();
}

function errMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}

function escapeHtml(s: unknown): string {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

function escapeAttr(s: unknown): string {
  return escapeHtml(s).replace(/"/g, "&quot;");
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
    titleInput.dataset.sessionId = ideation.sessionId || "";
  }
  if (descInput && descInput.dataset.sessionId !== ideation.sessionId) {
    descInput.value = ideation.draft.description;
    descInput.dataset.sessionId = ideation.sessionId || "";
  }
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
      statusLine.textContent = "Created " + (ideation.createdKey || "");
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
  const inputEnabled =
    ideation.state === "awaiting_reply" ||
    ideation.state === "none" ||
    ideation.state === "done" ||
    ideation.state === "error";
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

async function openIdeation(): Promise<void> {
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
      ideation = await go().StartIdeation(text, ideationMode ?? "chat", restart);
    } else {
      ideation = await go().ReplyIdeation(ideation.sessionId!, text);
    }
  } catch (err) {
    renderIdeationError(errMessage(err));
    stopIdeationPoll();
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
  wizardTemplates = info.templates || [];
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
  if (n >= 1_000_000) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1_000) return (n / 1e3).toFixed(1) + "K";
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
  if (ctx) chips.push(`<span class="agent-chip ctx">${escapeHtml(a.errorType ? a.errorType : a.blockedTool ? "⚠ " + a.blockedTool : "[" + a.currentTool + "]")}</span>`);

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
          g.group +
          "|" +
          (g.features ?? []).map((f) => f.name + ":" + f.description + (f.recent ? "*" : "")).join(",") +
          "|" +
          walk(g.groups),
      )
      .join(";");
  return (doc.product ?? "") + "¦" + (doc.tagline ?? "") + "¦" + walk(doc.groups);
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
    featuresNote = "Couldn't start generation: " + errMessage(err);
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
  const agents = document.getElementById("agents");
  const features = document.getElementById("features");
  board?.classList.toggle("hidden", view !== "board");
  agents?.classList.toggle("hidden", view !== "agents");
  features?.classList.toggle("hidden", view !== "features");

  if (view === "agents") {
    void pollAgents(); // immediate fetch so the view isn't blank until the first tick
    startAgentsPoll();
  } else {
    stopAgentsPoll();
  }

  // The features doc is static — load it once on first activation, then leave
  // the rendered pane in place (no poll, unlike agents).
  if (view === "features" && !featuresLoaded) {
    featuresLoaded = true;
    void loadFeatures();
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
  }
  void initialLoad();
  void pollDaemonStatus();
  setInterval(() => void pollDaemonStatus(), DAEMON_POLL_MS);

  wireRail();
  initFancy();
  initPermissions(() => go());
  document.addEventListener("keydown", (e: KeyboardEvent) => {
    if (isThemeToggleChord(e)) {
      e.preventDefault();
      toggleTheme();
    }
  });
  document.getElementById("ideation-close")?.addEventListener("click", () => closeIdeation());
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

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", init);
} else {
  init();
}
