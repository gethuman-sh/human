// Workflow-board frontend (typed source). Renders 5 forward-order columns from
// the daemon's derived BoardCards (via the Go App.Cards binding) and lets a card
// be dragged to its single next column to trigger that stage's `human` action
// via App.Transition. Placement, checkmarks and running/error state are all
// derived server-side — this file never re-derives a stage.
//
// The shipped runtime is desktop/frontend/dist/board.js; `npm run build`
// (tsc + bundle.mjs) regenerates dist/ from this source for `wails build`.

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

interface IdeationView {
  sessionId?: string;
  state: string; // none | thinking | awaiting_reply | done | error
  messages: IdeationMsg[];
  createdKey?: string;
  error?: string;
}

interface AppBindings {
  Cards(): Promise<BoardData>;
  Transition(pmKey: string, pmTitle: string, from: string, to: string): Promise<void>;
  DaemonStatus(): Promise<boolean>;
  StartIdeation(seed: string, restart: boolean): Promise<IdeationView>;
  ReplyIdeation(sessionId: string, message: string): Promise<IdeationView>;
  IdeationStatus(): Promise<IdeationView>;
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
  const next = STAGES[stageIndex(card.stage) + 1];
  const draggable = !!next && targetEnabled(next);
  el.setAttribute("draggable", draggable ? "true" : "false");
  el.dataset.key = card.key;
  el.dataset.stage = card.stage;

  const meta: string[] = [];
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

  el.addEventListener("dragstart", (e: DragEvent) => {
    dragging = { key: card.key, title: card.title, stage: card.stage };
    el.classList.add("dragging");
    if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
  });
  el.addEventListener("dragend", () => {
    dragging = null;
    el.classList.remove("dragging");
  });
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
    wireDropTarget(zone, stage);
    body.appendChild(zone);
  } else if (stage !== "backlog") {
    wireDropTarget(body, stage);
  }

  col.appendChild(body);
  return col;
}

function wireDropTarget(el: HTMLElement, to: string): void {
  el.addEventListener("dragover", (e: DragEvent) => {
    if (!dragging) return;
    const ok = isAdjacentForward(dragging.stage, to) && targetEnabled(to);
    el.classList.toggle("drop-ok", ok);
    el.classList.toggle("drop-reject", !ok);
    if (ok) {
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
    }
  });
  el.addEventListener("dragleave", () => {
    el.classList.remove("drop-ok", "drop-reject");
  });
  el.addEventListener("drop", (e: DragEvent) => {
    e.preventDefault();
    el.classList.remove("drop-ok", "drop-reject");
    if (!dragging) return;
    const from = dragging.stage;
    if (!isAdjacentForward(from, to) || !targetEnabled(to)) return;
    void transition(dragging.key, dragging.title, from, to);
    dragging = null;
  });
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
  await refresh();
}

function render(): void {
  const board = document.getElementById("board")!;
  board.innerHTML = "";
  for (const stage of STAGES) board.appendChild(renderColumn(stage));
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
  const dot = document.getElementById("daemon-status")!;
  dot.classList.toggle("reachable", daemonReachable);
  dot.classList.toggle("unreachable", !daemonReachable);
  dot.title = daemonReachable ? "Daemon reachable" : "Daemon unreachable";
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

async function refresh(): Promise<void> {
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
  render();
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

  const input = document.getElementById("ideation-input") as HTMLInputElement | null;
  const send = document.getElementById("ideation-send") as HTMLButtonElement | null;
  const inputEnabled =
    ideation.state === "awaiting_reply" ||
    ideation.state === "none" ||
    ideation.state === "done" ||
    ideation.state === "error";
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
  renderIdeation();
  if (ideation.state === "thinking") startIdeationPoll();
}

function closeIdeation(): void {
  const panel = document.getElementById("ideation-panel");
  if (panel) panel.classList.add("hidden");
  ideationOpen = false;
  stopIdeationPoll();
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

async function submitIdeation(): Promise<void> {
  const input = document.getElementById("ideation-input") as HTMLInputElement | null;
  if (!input) return;
  const text = input.value.trim();
  if (!text) return;

  const isFresh = ideation.state === "none" || ideation.state === "done" || ideation.state === "error";
  const restart = ideation.state === "done" || ideation.state === "error";

  // Optimistic update: show the user's message immediately and disable the
  // input while the turn is in flight.
  ideation = {
    ...ideation,
    state: "thinking",
    messages: [...ideation.messages, { role: "user", text }],
  };
  input.value = "";
  renderIdeation();
  startIdeationPoll();

  try {
    if (isFresh) {
      ideation = await go().StartIdeation(text, restart);
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

function init(): void {
  if (window.runtime?.EventsOn) {
    window.runtime.EventsOn("board:changed", () => {
      void refresh();
    });
  }
  void refresh();
  void pollDaemonStatus();
  setInterval(() => void pollDaemonStatus(), DAEMON_POLL_MS);

  document.getElementById("ideation-close")?.addEventListener("click", () => closeIdeation());
  document.getElementById("ideation-form")?.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    void submitIdeation();
  });
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", init);
} else {
  init();
}
