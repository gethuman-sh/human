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

interface AppBindings {
  Cards(): Promise<BoardData>;
  Transition(pmKey: string, pmTitle: string, from: string, to: string): Promise<void>;
  DaemonStatus(): Promise<boolean>;
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
  header.innerHTML = `<span>${STAGE_LABELS[stage]}</span><span class="column-count">${cards.length}</span>`;
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

function init(): void {
  if (window.runtime?.EventsOn) {
    window.runtime.EventsOn("board:changed", () => {
      void refresh();
    });
  }
  void refresh();
  void pollDaemonStatus();
  setInterval(() => void pollDaemonStatus(), DAEMON_POLL_MS);
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", init);
} else {
  init();
}
