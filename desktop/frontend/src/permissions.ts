// Command strip for destructive-operation permission requests (HUM-161).
//
// A slim amber strip under the header that exists only while the daemon's
// confirm queue (HUM-160) has pending requests. It always shows the OLDEST
// request so the queue drains FIFO; the counter on the right drops down the
// rest. Approval only records a grant on the daemon — the requesting agent
// redeems it and executes on its own, so nothing here waits for execution.

export interface PermissionRequest {
  id: string;
  operation: string;
  tracker: string;
  key: string;
  prompt: string;
  createdAt: string;
}

export interface PermissionBindings {
  PendingPermissions(): Promise<PermissionRequest[]>;
  DecidePermission(id: string, approved: boolean): Promise<void>;
}

const POLL_MS = 2000;
// How long the decision flash tints the strip before the next request shows.
const FLASH_MS = 450;

let bindings: (() => PermissionBindings) | null = null;
let queue: PermissionRequest[] = [];
// IDs decided locally but possibly still pending in the next poll response —
// filtered out so a just-decided request cannot flash back for one tick.
const decided = new Set<string>();
let dropdownOpen = false;
let flashing = false;

function els() {
  return {
    strip: document.getElementById("perm-strip"),
    question: document.getElementById("perm-question"),
    meta: document.getElementById("perm-meta"),
    approve: document.getElementById("perm-approve") as HTMLButtonElement | null,
    deny: document.getElementById("perm-deny") as HTMLButtonElement | null,
    more: document.getElementById("perm-more") as HTMLButtonElement | null,
    dropdown: document.getElementById("perm-queue"),
  };
}

// The daemon prompt is "DeleteIssue KAN-1?" — reword per operation so the
// strip reads like a question about the ticket, not an RPC name.
export function humanizeRequest(r: PermissionRequest): string {
  switch (r.operation) {
    case "DeleteIssue":
      return `Delete ${r.key}?`;
    case "EditIssue":
      return `Edit ${r.key}?`;
    case "TransitionIssue":
      return `Change status of ${r.key}?`;
    case "StartIssue":
      return `Start ${r.key}?`;
    default:
      return r.prompt || `${r.operation} ${r.key}?`;
  }
}

export function formatAge(createdAt: string, now: Date = new Date()): string {
  const t = Date.parse(createdAt);
  if (Number.isNaN(t)) return "";
  const s = Math.max(0, Math.floor((now.getTime() - t) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  return `${Math.floor(s / 3600)}h`;
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function render(): void {
  const e = els();
  if (!e.strip || !e.question || !e.meta || !e.more || !e.dropdown) return;

  if (queue.length === 0) {
    e.strip.classList.add("hidden");
    e.dropdown.classList.add("hidden");
    dropdownOpen = false;
    return;
  }

  const head = queue[0];
  e.strip.classList.remove("hidden");
  e.question.textContent = humanizeRequest(head);
  e.meta.textContent = `${head.tracker} · ${formatAge(head.createdAt)}`;

  const rest = queue.length - 1;
  if (rest > 0) {
    e.more.classList.remove("hidden");
    e.more.textContent = `${dropdownOpen ? "▴" : "▾"} ${rest} more waiting`;
  } else {
    e.more.classList.add("hidden");
    dropdownOpen = false;
  }

  if (dropdownOpen && rest > 0) {
    e.dropdown.classList.remove("hidden");
    e.dropdown.innerHTML =
      `<div class="perm-queue-hd">Waiting queue</div>` +
      queue
        .slice(1)
        .map(
          (r) =>
            `<div class="perm-row" data-id="${escapeHtml(r.id)}">` +
            `<div class="perm-row-main"><span class="perm-row-q">${escapeHtml(humanizeRequest(r))}</span>` +
            `<span class="perm-row-meta">${escapeHtml(r.tracker)} · ${formatAge(r.createdAt)}</span></div>` +
            `<button class="perm-mini perm-mini-no" data-id="${escapeHtml(r.id)}" data-approve="no" title="Deny">✕</button>` +
            `<button class="perm-mini perm-mini-ok" data-id="${escapeHtml(r.id)}" data-approve="yes" title="Approve">✓</button>` +
            `</div>`,
        )
        .join("");
    e.dropdown.querySelectorAll<HTMLButtonElement>(".perm-mini").forEach((btn) => {
      btn.addEventListener("click", () => {
        void decide(btn.dataset.id ?? "", btn.dataset.approve === "yes");
      });
    });
  } else {
    e.dropdown.classList.add("hidden");
  }
}

async function poll(): Promise<void> {
  if (!bindings) return;
  try {
    const list = (await bindings().PendingPermissions()) ?? [];
    queue = list.filter((r) => !decided.has(r.id));
    // Locally-decided IDs that no longer appear are settled on the daemon;
    // stop tracking them so the set cannot grow unbounded.
    const present = new Set(list.map((r) => r.id));
    for (const id of decided) if (!present.has(id)) decided.delete(id);
  } catch {
    queue = [];
  }
  if (!flashing) render();
}

async function decide(id: string, approved: boolean): Promise<void> {
  if (!bindings || !id) return;
  const e = els();
  decided.add(id);
  queue = queue.filter((r) => r.id !== id);
  try {
    await bindings().DecidePermission(id, approved);
  } catch {
    // The decision did not reach the daemon — undo the local removal so the
    // request reappears instead of silently vanishing undecided.
    decided.delete(id);
    void poll();
    return;
  }
  // Brief tint as feedback, then advance to the next request. The CSS side
  // is a no-op under prefers-reduced-motion.
  if (e.strip) {
    flashing = true;
    e.strip.classList.add(approved ? "perm-flash-ok" : "perm-flash-no");
    window.setTimeout(() => {
      e.strip?.classList.remove("perm-flash-ok", "perm-flash-no");
      flashing = false;
      render();
    }, FLASH_MS);
  }
  render();
}

function isEditableTarget(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el) return false;
  return el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable;
}

function onKeydown(e: KeyboardEvent): void {
  if (e.key === "Escape" && dropdownOpen) {
    dropdownOpen = false;
    render();
    return;
  }
  if (!(e.ctrlKey || e.metaKey) || !e.shiftKey || queue.length === 0) return;
  if (isEditableTarget(e.target)) return;
  const k = e.key.toLowerCase();
  if (k === "y") {
    e.preventDefault();
    void decide(queue[0].id, true);
  } else if (k === "n") {
    e.preventDefault();
    void decide(queue[0].id, false);
  }
}

export function initPermissions(getBindings: () => PermissionBindings): void {
  bindings = getBindings;
  const e = els();
  e.approve?.addEventListener("click", () => {
    if (queue.length > 0) void decide(queue[0].id, true);
  });
  e.deny?.addEventListener("click", () => {
    if (queue.length > 0) void decide(queue[0].id, false);
  });
  e.more?.addEventListener("click", () => {
    dropdownOpen = !dropdownOpen;
    render();
  });
  document.addEventListener("keydown", onKeydown);
  void poll();
  window.setInterval(() => void poll(), POLL_MS);
}
