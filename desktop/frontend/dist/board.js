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
import { celebrateDrop, ghostTilt, initFancy, isThemeToggleChord, toggleTheme, trail, } from "./fancy.js";
import { initPermissions } from "./permissions.js";
import { initMockupsView, showMockups } from "./mockupsview.js";
import { initSettingsView, showSettings, settingsIndex, saveSetting, setPaletteOpener, setActiveSection, } from "./settingsview.js";
import { initPalette, openPalette, isPaletteChord } from "./palette.js";
// Queue columns: each names a state that is TRUE of every card in it, always.
// The agent work happens on the transitions (a drag is the launch), so a card
// being worked stays in its ORIGIN queue with a live badge and only arrives in
// the next queue when the stage completes. State on the column, verb on the
// affordance — the wire stages/markers are untouched; this is pure display.
// Code is the one ACTIVITY column among the queues — deliberately special
// because coding is the board's longest and weightiest phase: the column
// holds exactly the cards the executor is working on (the machine verifies
// membership, so the name stays true), and its count is the live build gauge.
// Cards leave it on their own when the handoff posts; Ready for review is
// never a drop target — cards can only earn their way in. ("building" stays
// the internal queue id so theme hooks and tests don't churn on a label.)
const QUEUES = ["ideas", "product", "engineering", "building", "review", "deploy"];
const QUEUE_LABELS = {
    ideas: "Ideas",
    product: "Product backlog",
    engineering: "Engineering backlog",
    building: "Code",
    review: "Ready for review",
    deploy: "Ready to deploy",
};
// Wire stage launched by dropping onto a queue from its predecessor. Queues
// missing here (ideas, review) accept no transition drop at all.
const QUEUE_TRANSITION_TO = {
    engineering: "planning",
    building: "implementation",
    deploy: "verification",
};
// The verb shown on a drop target while a drag hovers it — the action lives
// on the thing being touched, never in the column title.
const QUEUE_VERB = {
    product: "Define it",
    engineering: "Plan it",
    building: "Build it",
    deploy: "Review it",
};
// Live badge text while a stage runs; the card sits in its origin queue —
// except implementation, which has its own Code lane.
const RUNNING_LABELS = {
    planning: "planning…",
    implementation: "building…",
    verification: "reviewing…",
    done: "opening PR…",
};
// queueOf maps the wire (stage, state) onto the column whose name is true of
// the card: incomplete stages keep the card where it was pulled from, apart
// from an in-flight (or failed) build, which lives in the Code lane.
function queueOf(card) {
    switch (card.stage) {
        case "ideas":
            return "ideas";
        case "backlog":
            return "product";
        case "planning":
            return card.state === "done" ? "engineering" : "product";
        case "implementation":
            return card.state === "done" ? "review" : "building";
        case "verification":
            return card.state === "done" ? "deploy" : "review";
        case "done":
            return "deploy";
        default:
            return "product";
    }
}
let current = { cards: [], dockerAvailable: true, error: "" };
let dragging = null;
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
function go() {
    const app = window.go?.main?.App;
    if (!app)
        throw new Error("Wails bindings not available");
    return app;
}
function queueIndex(queue) {
    return QUEUES.indexOf(queue);
}
function isNextQueue(fromQueue, toQueue) {
    return queueIndex(toQueue) === queueIndex(fromQueue) + 1;
}
// targetEnabled gates agent-launching drops on Docker availability; every
// queue transition except idea promotion launches a containerized agent.
function targetEnabled(toQueue) {
    if (QUEUE_TRANSITION_TO[toQueue] !== undefined && !current.dockerAvailable)
        return false;
    return true;
}
// badge renders the card's live state. A resting card needs no checkmark —
// its queue position IS the statement of completion — so "done" is only
// badged for the terminal PR stage, as a link when the URL is known.
function badge(card) {
    if (card.state === "running") {
        const label = RUNNING_LABELS[card.stage] ?? "working…";
        return `<span class="badge running" title="Agent running"><span class="spinner"></span> ${escapeHtml(label)}</span>`;
    }
    if (card.state === "failed")
        return `<span class="badge failed" title="Stage failed">✕</span>`;
    if (card.stage === "done" && card.state === "done" && !card.prURL) {
        return `<span class="badge done" title="Pull request opened">PR open</span>`;
    }
    return "";
}
function renderCard(card) {
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
    const meta = [];
    if (stagesLoading) {
        // Titles are shown but this card's real stage is still being derived from
        // comments; a resolving spinner signals it may still move columns.
        meta.push(`<span class="badge resolving" title="Resolving stage…"><span class="spinner"></span></span>`);
    }
    const b = badge(card);
    if (b)
        meta.push(b);
    if (card.engineeringKey)
        meta.push(`<span>${escapeHtml(card.engineeringKey)}</span>`);
    if (card.prURL)
        meta.push(`<a href="${escapeAttr(card.prURL)}" target="_blank">PR</a>`);
    // The terminal action is a button on the card, not a drop zone: a reviewed
    // card already rests in Ready to deploy, so there is nowhere to drag it.
    const showPR = card.stage === "verification" && card.state === "done";
    el.innerHTML = `
    <div class="card-key">${escapeHtml(card.key)}</div>
    <div class="card-title">${escapeHtml(card.title)}</div>
    <div class="card-meta">${meta.join("")}</div>
    ${card.error ? `<div class="card-error">${escapeHtml(card.error)}</div>` : ""}
    ${showPR ? `<button class="card-action" type="button">Open pull request</button>` : ""}
  `;
    el.querySelector(".card-action")?.addEventListener("click", (e) => {
        e.stopPropagation();
        void transition(card.key, card.title, card.stage, "done");
    });
    beginPointerDrag(el, card);
    return el;
}
function renderColumn(queue) {
    const col = document.createElement("section");
    col.className = "column";
    col.dataset.stage = queue;
    const cards = current.cards.filter((c) => queueOf(c) === queue);
    const header = document.createElement("div");
    header.className = "column-header";
    if (queue === "product") {
        header.innerHTML =
            `<span>${QUEUE_LABELS[queue]}</span>` +
                `<button class="add-card" title="New ticket via ideation">+</button>` +
                `<span class="column-count">${cards.length}</span>`;
        header.querySelector(".add-card").addEventListener("click", () => void openIdeation());
    }
    else if (queue === "ideas") {
        // Ideas capture is deliberately dumb: a title becomes a labeled ticket in
        // one keystroke — the thinking happens later, at promotion.
        header.innerHTML =
            `<span>${QUEUE_LABELS[queue]}</span>` +
                `<button class="add-card" title="Capture an idea">+</button>` +
                `<span class="column-count">${cards.length}</span>`;
        header.querySelector(".add-card").addEventListener("click", () => showIdeaQuickAdd(col));
    }
    else {
        header.innerHTML = `<span>${QUEUE_LABELS[queue]}</span><span class="column-count">${cards.length}</span>`;
    }
    col.appendChild(header);
    const body = document.createElement("div");
    body.className = "column-body";
    for (const card of cards)
        body.appendChild(renderCard(card));
    if (queue === "product" || QUEUE_TRANSITION_TO[queue] !== undefined) {
        // Drop targets are the queues a drag can act on: product (idea promotion)
        // and the transition-launching queues. Ready for review is deliberately
        // NOT a target — cards arrive there only by finishing a build.
        markQueueTarget(body, queue);
    }
    if (queue === "deploy") {
        // A card from ANY column can be dropped here to close its ticket — closing
        // is not an adjacency-gated queue move, so it is its own kind of target.
        const closeZone = document.createElement("div");
        closeZone.className = "close-zone";
        closeZone.textContent = "Close Ticket";
        markCloseTarget(closeZone);
        body.appendChild(closeZone);
    }
    col.appendChild(body);
    return col;
}
// showIdeaQuickAdd swaps an inline title input into the Ideas column. Enter
// creates the idea-labeled ticket via CreateIdea; Escape or blur dismisses.
function showIdeaQuickAdd(col) {
    const body = col.querySelector(".column-body");
    if (!body || body.querySelector(".idea-quick-add"))
        return;
    const input = document.createElement("input");
    input.className = "idea-quick-add";
    input.type = "text";
    input.placeholder = "Idea title — Enter to capture";
    body.prepend(input);
    input.focus();
    input.addEventListener("keydown", (e) => {
        if (e.key === "Escape") {
            input.remove();
            return;
        }
        if (e.key !== "Enter")
            return;
        const title = input.value.trim();
        if (!title)
            return;
        input.disabled = true;
        void (async () => {
            try {
                await go().CreateIdea(title);
                input.remove();
                await reconcile();
            }
            catch (err) {
                input.disabled = false;
                showError(errMessage(err));
            }
        })();
    });
    input.addEventListener("blur", () => {
        if (!input.disabled && input.value.trim() === "")
            input.remove();
    });
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
let dragGhost = null;
let hoverTarget = null;
function markQueueTarget(el, queue) {
    el.dataset.drop = "queue";
    el.dataset.dropQueue = queue;
    const verb = QUEUE_VERB[queue];
    if (verb)
        el.dataset.verb = verb;
}
function markCloseTarget(el) {
    el.dataset.drop = "close";
}
// dropTargetAt returns the drop-target element under a viewport point, if any.
// The ghost has pointer-events:none, so it never occludes the hit-test.
function dropTargetAt(x, y) {
    const el = document.elementFromPoint(x, y);
    return el ? el.closest("[data-drop]") : null;
}
// dropAllowed reports whether the dragged card may drop on target. Any card can
// be closed; queue targets keep the forward-adjacency + docker-enabled rules,
// evaluated on the card's RESTING queue so a running card cannot double-launch.
function dropAllowed(target) {
    if (!dragging)
        return false;
    if (target.dataset.drop === "close")
        return true;
    const toQueue = target.dataset.dropQueue || "";
    const card = current.cards.find((c) => c.key === dragging.key);
    if (!card || card.state === "running")
        return false;
    return isNextQueue(queueOf(card), toQueue) && targetEnabled(toQueue);
}
// setHoverTarget moves the highlight to a new target (clearing the previous),
// so exactly one drop zone is lit at a time.
function setHoverTarget(target) {
    if (target !== hoverTarget && hoverTarget) {
        hoverTarget.classList.remove("drop-ok", "drop-reject");
    }
    hoverTarget = target;
    if (!target)
        return;
    const ok = dropAllowed(target);
    // The close zone only shows its own accept state; stage zones also show
    // drop-reject to signal an invalid move.
    target.classList.toggle("drop-ok", ok);
    if (target.dataset.drop !== "close")
        target.classList.toggle("drop-reject", !ok);
}
function makeDragGhost(card) {
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
function beginPointerDrag(el, card) {
    el.addEventListener("pointerdown", (down) => {
        if (down.button !== 0)
            return;
        // Let clicks on interactive children (e.g. the PR link) behave normally.
        if (down.target.closest("a, button"))
            return;
        const info = { key: card.key, title: card.title, stage: card.stage };
        let started = false;
        let lastX = down.clientX;
        const onMove = (ev) => {
            if (!started) {
                if (Math.hypot(ev.clientX - down.clientX, ev.clientY - down.clientY) < DRAG_THRESHOLD_PX)
                    return;
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
        const teardown = () => {
            el.removeEventListener("pointermove", onMove);
            el.removeEventListener("pointerup", onUp);
            el.removeEventListener("pointercancel", onCancel);
            try {
                el.releasePointerCapture(down.pointerId);
            }
            catch {
                // Capture may already be gone; ignore.
            }
            el.classList.remove("dragging");
            if (dragGhost) {
                dragGhost.remove();
                dragGhost = null;
            }
            setHoverTarget(null);
        };
        const onUp = (ev) => {
            const target = started ? dropTargetAt(ev.clientX, ev.clientY) : null;
            const allowed = !!target && dropAllowed(target);
            teardown();
            endDrag();
            // `target` may have been replaced by the flushed render, but performDrop
            // only reads its dataset, which a detached node still carries.
            if (target && allowed)
                performDrop(target, info, { x: ev.clientX, y: ev.clientY });
        };
        const onCancel = () => {
            teardown();
            endDrag();
        };
        try {
            el.setPointerCapture(down.pointerId);
        }
        catch {
            // Best-effort; drag still works via bubbling if capture is unavailable.
        }
        el.addEventListener("pointermove", onMove);
        el.addEventListener("pointerup", onUp);
        el.addEventListener("pointercancel", onCancel);
    });
}
// endDrag closes the drag lifecycle and flushes any board rebuild that was
// deferred while the drag was in flight.
function endDrag() {
    dragging = null;
    if (pendingRender) {
        pendingRender = false;
        render();
    }
}
// performDrop runs the action for a completed drop on an allowed target.
function performDrop(target, info, pt) {
    if (target.dataset.drop === "close") {
        // Closing is destructive and still awaits a confirm dialog — never a
        // moment to celebrate, so the fancy theme stays silent here.
        void requestClose(info.key, info.title);
        return;
    }
    const toQueue = target.dataset.dropQueue || "";
    if (toQueue === "product" && info.stage === "ideas") {
        // Promotion is a conversation, not a stage transition: the evolve-mode
        // ideation session rewrites the ticket in place and removes the idea
        // label; the card moves columns when the board refetches.
        void promoteIdea(info.key);
        return;
    }
    const to = QUEUE_TRANSITION_TO[toQueue] || "";
    if (!to)
        return;
    celebrateDrop(pt, { key: info.key, fromStage: info.stage, done: false });
    void transition(info.key, info.title, info.stage, to);
}
// promoteIdea opens the ideation panel in evolve mode, seeded with the idea
// card's content. An active session must be explicitly replaced — the daemon
// holds a single ideation session, so a silent restart would discard it.
async function promoteIdea(key) {
    const card = current.cards.find((c) => c.key === key);
    if (!card)
        return;
    const active = ideation.state === "thinking" || ideation.state === "awaiting_reply" || ideation.state === "awaiting_approval";
    if (active) {
        const ok = await confirmDialog("Replace the active ideation session?", "Promoting this idea abandons the conversation currently in the ideation panel.", "Replace");
        if (!ok)
            return;
    }
    let seed = card.title;
    if (card.description)
        seed += "\n\n" + card.description;
    const panel = document.getElementById("ideation-panel");
    if (panel)
        panel.classList.remove("hidden");
    ideationOpen = true;
    // Guided mode by default: a parked idea was parked precisely because it
    // wasn't thought through — structured questions fit that moment.
    ideationMode = "guided";
    ideation = { state: "thinking", messages: [{ role: "user", text: seed }] };
    renderIdeation();
    startIdeationPoll();
    try {
        ideation = await go().StartIdeation(seed, "guided", true, card.key, card.labels ?? []);
    }
    catch (err) {
        renderIdeationError(errMessage(err));
        stopIdeationPoll();
        return;
    }
    renderIdeation();
    if (ideation.state !== "thinking")
        stopIdeationPoll();
}
async function transition(key, title, from, to) {
    const card = current.cards.find((c) => c.key === key);
    if (card) {
        card.stage = to;
        card.state = "running";
        render();
    }
    try {
        await go().Transition(key, title, from, to);
    }
    catch (err) {
        showError(errMessage(err));
    }
    await reconcile();
}
// requestClose confirms in-app (never the OS dialog) before closing, so a stray
// drop cannot silently close a ticket.
async function requestClose(key, title) {
    const ok = await confirmDialog(`Close ticket ${key}?`, `“${title}” will be marked Done and removed from the board.`, "Close ticket");
    if (ok)
        await closeTicket(key);
}
async function closeTicket(key) {
    try {
        await go().CloseTicket(key);
    }
    catch (err) {
        showError(errMessage(err));
    }
    // The closed ticket is no longer "open", so reconcile drops it from the board.
    await reconcile();
}
// confirmDialog renders a small modal overlay and resolves true/false on the
// user's choice. Overlay-click and Escape count as cancel. Built with the same
// imperative-DOM approach as the rest of the app (no framework).
function confirmDialog(title, body, confirmLabel) {
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
        const cleanup = (result) => {
            document.removeEventListener("keydown", onKey);
            overlay.remove();
            resolve(result);
        };
        const onKey = (e) => {
            if (e.key === "Escape")
                cleanup(false);
        };
        overlay.addEventListener("click", (e) => {
            if (e.target === overlay)
                cleanup(false);
        });
        modal.querySelector(".modal-cancel").addEventListener("click", () => cleanup(false));
        modal.querySelector(".modal-confirm").addEventListener("click", () => cleanup(true));
        document.addEventListener("keydown", onKey);
        modal.querySelector(".modal-confirm").focus();
    });
}
// captureColumnScroll records each column's current scrollTop keyed by stage, so
// it can be restored after render() rebuilds the DOM from scratch.
function captureColumnScroll(board) {
    const scroll = {};
    board.querySelectorAll(".column").forEach((col) => {
        const body = col.querySelector(".column-body");
        if (body && col.dataset.stage)
            scroll[col.dataset.stage] = body.scrollTop;
    });
    return scroll;
}
// restoreColumnScroll re-applies scroll positions captured before a rebuild.
function restoreColumnScroll(board, scroll) {
    board.querySelectorAll(".column").forEach((col) => {
        const stage = col.dataset.stage;
        const body = col.querySelector(".column-body");
        if (body && stage && scroll[stage])
            body.scrollTop = scroll[stage];
    });
}
// A render mid-drag would replace the dragged card's DOM element, silently
// killing its pointer listeners (frozen ghost, drop never lands). Rebuilds
// requested during a drag are deferred and flushed by endDrag().
let pendingRender = false;
function render() {
    if (dragging) {
        pendingRender = true;
        return;
    }
    const board = document.getElementById("board");
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
    }
    else {
        for (const queue of QUEUES)
            board.appendChild(renderColumn(queue));
        restoreColumnScroll(board, scrollByStage);
    }
    const banner = document.getElementById("banner");
    if (current.error) {
        banner.textContent = current.error;
        banner.classList.remove("hidden");
    }
    else {
        banner.classList.add("hidden");
    }
}
function showError(msg) {
    current.error = msg;
    render();
}
function renderDaemonStatus() {
    // Mirrors the TUI's bottom status line ("● Daemon running"/"stopped").
    const dot = document.getElementById("daemon-indicator");
    dot.classList.toggle("reachable", daemonReachable);
    dot.classList.toggle("unreachable", !daemonReachable);
    const text = document.getElementById("daemon-text");
    text.textContent = daemonReachable ? "Daemon running" : "Daemon stopped";
}
async function pollDaemonStatus() {
    try {
        daemonReachable = await go().DaemonStatus();
    }
    catch {
        // Wails bindings not ready yet or call failed — treat as unreachable.
        daemonReachable = false;
    }
    renderDaemonStatus();
}
// initialLoad renders the board progressively on startup: a spinner first, then
// ticket titles (Backlog) from the fast comment-scan-free fetch, then the full
// reconcile that moves each card into its real stage. Steady-state updates use
// reconcile() directly so they never flash the spinner or re-place cards.
async function initialLoad() {
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
    }
    catch {
        // Quick phase failed (e.g. daemon not up yet): fall through to the full
        // fetch, which surfaces the error via reconcile().
        boardLoading = false;
    }
    await reconcile();
}
// reconcile fetches the full board (including derived stages) and renders it. It
// is the single source of truth after the initial load: board:changed events and
// post-transition refreshes call it directly.
async function reconcile() {
    try {
        const data = await go().Cards();
        current = {
            cards: data.cards || [],
            dockerAvailable: !!data.dockerAvailable,
            error: data.error || "",
        };
    }
    catch (err) {
        current = { cards: [], dockerAvailable: false, error: errMessage(err) };
    }
    boardLoading = false;
    stagesLoading = false;
    render();
    // Offered at most once per session, and only off a confirmed-empty board —
    // see the Start Project wizard section for the guards.
    void maybeOfferStartProject();
}
function errMessage(err) {
    if (err instanceof Error)
        return err.message;
    return String(err);
}
function escapeHtml(s) {
    return String(s == null ? "" : s)
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;");
}
function escapeAttr(s) {
    return escapeHtml(s).replace(/"/g, "&quot;");
}
// --- Ideation chat panel -----------------------------------------------
//
// The panel is a thin client over the daemon's ideation-start/reply/status
// routes: it never derives session state itself, it only renders whatever
// the daemon last reported. Closing the panel does NOT abandon the
// daemon-side session (AD-4) — reopening re-attaches via IdeationStatus().
let ideation = { state: "none", messages: [] };
let ideationOpen = false;
let ideationTimer = null;
// ideationMode is transient frontend-only state: null means the mode picker
// has not been resolved yet for a fresh session. It is not sent to the
// daemon until the user picks a mode and sends the first message/seed.
let ideationMode = null;
const IDEATION_POLL_MS = 1000;
function stopIdeationPoll() {
    if (ideationTimer !== null) {
        clearInterval(ideationTimer);
        ideationTimer = null;
    }
}
// startIdeationPoll only runs while the panel is visible: the daemon-side
// session keeps making progress on its own regardless (AD-4), so there is no
// need to poll for a panel the user cannot see.
function startIdeationPoll() {
    if (!ideationOpen || ideationTimer !== null)
        return;
    ideationTimer = window.setInterval(() => void pollIdeation(), IDEATION_POLL_MS);
}
function renderModePicker() {
    const picker = document.getElementById("ideation-mode-picker");
    if (!picker)
        return;
    const show = ideation.state === "none" && ideationMode === null;
    picker.classList.toggle("hidden", !show);
}
function renderIdeationOptions() {
    const container = document.getElementById("ideation-options");
    const input = document.getElementById("ideation-input");
    if (!container)
        return;
    const question = ideation.state === "awaiting_reply" ? ideation.question : undefined;
    if (!question) {
        container.classList.add("hidden");
        container.innerHTML = "";
        if (input)
            input.classList.remove("hidden");
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
    if (input)
        input.classList.add("hidden");
}
function renderIdeationDraft() {
    const draftEl = document.getElementById("ideation-draft");
    const form = document.getElementById("ideation-form");
    if (!draftEl)
        return;
    if (ideation.state !== "awaiting_approval" || !ideation.draft) {
        draftEl.classList.add("hidden");
        return;
    }
    draftEl.classList.remove("hidden");
    if (form)
        form.classList.add("hidden");
    const titleInput = document.getElementById("ideation-draft-title");
    const descInput = document.getElementById("ideation-draft-description");
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
function renderIdeation() {
    const transcript = document.getElementById("ideation-transcript");
    if (!transcript)
        return;
    transcript.innerHTML = ideation.messages
        .map((m) => `<div class="msg ${m.role === "user" ? "user" : "agent"}">${escapeHtml(m.text)}</div>`)
        .join("");
    transcript.scrollTop = transcript.scrollHeight;
    const statusLine = document.getElementById("ideation-status-line");
    if (statusLine) {
        statusLine.classList.remove("hidden", "error");
        if (ideation.state === "thinking") {
            statusLine.textContent = "Agent is thinking…";
        }
        else if (ideation.state === "error") {
            statusLine.textContent = ideation.error || "Ideation session failed";
            statusLine.classList.add("error");
        }
        else if (ideation.state === "done") {
            statusLine.textContent = "Created " + (ideation.createdKey || "");
        }
        else {
            statusLine.classList.add("hidden");
        }
    }
    renderModePicker();
    renderIdeationOptions();
    renderIdeationDraft();
    const form = document.getElementById("ideation-form");
    const input = document.getElementById("ideation-input");
    const send = document.getElementById("ideation-send");
    const inputEnabled = ideation.state === "awaiting_reply" ||
        ideation.state === "none" ||
        ideation.state === "done" ||
        ideation.state === "error";
    // The draft-review form takes over the panel's bottom area while
    // awaiting_approval; the free-text form must not be reachable there.
    if (form)
        form.classList.toggle("hidden", ideation.state === "awaiting_approval");
    if (input) {
        input.disabled = !inputEnabled;
        input.placeholder = ideation.state === "awaiting_reply" ? "Your answer…" : "Describe the idea…";
    }
    if (send)
        send.disabled = !inputEnabled;
}
function renderIdeationError(msg) {
    ideation = { ...ideation, state: "error", error: msg };
    renderIdeation();
}
async function openIdeation() {
    const panel = document.getElementById("ideation-panel");
    if (panel)
        panel.classList.remove("hidden");
    ideationOpen = true;
    try {
        ideation = await go().IdeationStatus();
    }
    catch (err) {
        renderIdeationError(errMessage(err));
        return;
    }
    // Leave ideationMode as whatever it currently is: it starts null at module
    // load and is only reset by closeIdeation() for terminal/none states, so a
    // panel reopen mid-flow must not re-show a fresh mode picker.
    renderIdeation();
    if (ideation.state === "thinking")
        startIdeationPoll();
}
function closeIdeation() {
    const panel = document.getElementById("ideation-panel");
    if (panel)
        panel.classList.add("hidden");
    ideationOpen = false;
    stopIdeationPoll();
    // Closing does not abandon an active session (AD-4): only reset the mode
    // picker when there is no live session to reattach to on reopen.
    if (ideation.state === "done" || ideation.state === "error" || ideation.state === "none") {
        ideationMode = null;
    }
}
async function pollIdeation() {
    try {
        ideation = await go().IdeationStatus();
    }
    catch (err) {
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
async function sendIdeationReply(text) {
    if (!text || ideation.state === "awaiting_approval")
        return;
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
            ideation = await go().StartIdeation(text, ideationMode ?? "chat", restart, "", []);
        }
        else {
            ideation = await go().ReplyIdeation(ideation.sessionId, text);
        }
    }
    catch (err) {
        renderIdeationError(errMessage(err));
        stopIdeationPoll();
        return;
    }
    renderIdeation();
    if (ideation.state !== "thinking") {
        stopIdeationPoll();
    }
}
async function submitIdeation() {
    const input = document.getElementById("ideation-input");
    if (!input)
        return;
    const text = input.value.trim();
    if (!text)
        return;
    input.value = "";
    await sendIdeationReply(text);
}
async function approveIdeation() {
    const titleInput = document.getElementById("ideation-draft-title");
    const descInput = document.getElementById("ideation-draft-description");
    if (!titleInput || !descInput || !ideation.sessionId)
        return;
    const sessionId = ideation.sessionId;
    ideation = { ...ideation, state: "thinking" };
    renderIdeation();
    startIdeationPoll();
    try {
        ideation = await go().ApproveIdeation(sessionId, titleInput.value.trim(), descInput.value);
    }
    catch (err) {
        renderIdeationError(errMessage(err));
        stopIdeationPoll();
        return;
    }
    renderIdeation();
    if (ideation.state !== "thinking")
        stopIdeationPoll();
}
// wizardChecked is the re-trigger guard: set before any await in
// maybeOfferStartProject so overlapping reconciles (board:changed storms)
// cannot probe or open twice. Dismissal therefore lasts for the session.
let wizardChecked = false;
let wizardOverlay = null;
let wizardTemplates = [];
let wizardStep = "type";
let wizardType = "";
let wizardError = "";
let wizardCreated = 0;
async function maybeOfferStartProject() {
    if (wizardChecked || current.error)
        return;
    // Cards on the board mean a project exists — settle without the FS probe,
    // but leave wizardChecked set: a non-empty board can only gain cards.
    wizardChecked = true;
    if (current.cards.length > 0)
        return;
    let info;
    try {
        info = await go().StartProjectStatus();
    }
    catch {
        return;
    }
    // A failed probe (info.error) means "don't offer", never a broken app.
    if (info.error || !info.emptyProject)
        return;
    wizardTemplates = info.templates || [];
    if (wizardTemplates.length === 0)
        return;
    openStartWizard();
}
function wizardTypeChoices() {
    const seen = new Set();
    const choices = [];
    wizardTemplates.forEach((t) => {
        if (seen.has(t.type))
            return;
        seen.add(t.type);
        choices.push({ type: t.type, label: t.typeLabel });
    });
    return choices;
}
function wizardLanguageChoices(type) {
    return wizardTemplates.filter((t) => t.type === type);
}
function openStartWizard() {
    if (wizardOverlay)
        return;
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
    const onKey = (e) => {
        // No escape while the download runs: the state is not cancellable from
        // here and a hidden in-flight scaffold would be surprising.
        if (e.key === "Escape" && wizardStep !== "creating")
            closeStartWizard();
    };
    overlay.addEventListener("click", (e) => {
        if (e.target === overlay && wizardStep !== "creating")
            closeStartWizard();
    });
    document.addEventListener("keydown", onKey);
    overlay.dataset.bound = "true";
    // Store the handler so closeStartWizard can unbind it.
    overlay._onKey = onKey;
    renderStartWizard();
}
function closeStartWizard() {
    if (!wizardOverlay)
        return;
    const onKey = wizardOverlay._onKey;
    if (onKey)
        document.removeEventListener("keydown", onKey);
    wizardOverlay.remove();
    wizardOverlay = null;
}
function renderStartWizard() {
    if (!wizardOverlay)
        return;
    const modal = wizardOverlay.querySelector(".wizard");
    if (!modal)
        return;
    if (wizardStep === "type") {
        modal.innerHTML = `
      <div class="modal-title">Start a new project</div>
      <div class="modal-body">This folder has no project yet. What do you want to build?</div>
      <div class="wizard-options"></div>
    `;
        const options = modal.querySelector(".wizard-options");
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
        const options = modal.querySelector(".wizard-options");
        wizardLanguageChoices(wizardType).forEach((tpl) => {
            const btn = document.createElement("button");
            btn.type = "button";
            btn.className = "wizard-option";
            btn.textContent = tpl.languageLabel;
            btn.addEventListener("click", () => void createStartProject(tpl));
            options.appendChild(btn);
        });
        modal.querySelector(".wizard-back").addEventListener("click", () => {
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
        modal.querySelector(".modal-cancel").addEventListener("click", () => closeStartWizard());
        modal.querySelector(".modal-confirm").addEventListener("click", () => {
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
    modal.querySelector(".modal-cancel").addEventListener("click", () => closeStartWizard());
    modal.querySelector(".modal-confirm").addEventListener("click", () => {
        wizardStep = "language";
        renderStartWizard();
    });
}
async function createStartProject(tpl) {
    wizardStep = "creating";
    renderStartWizard();
    try {
        const res = await go().StartProject(tpl.type, tpl.language);
        wizardCreated = res.filesCreated;
        wizardStep = "done";
    }
    catch (err) {
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
let agentsData = { agents: [] };
let agentsTimer = null;
const AGENTS_POLL_MS = 2000;
function stopAgentsPoll() {
    if (agentsTimer !== null) {
        clearInterval(agentsTimer);
        agentsTimer = null;
    }
}
function startAgentsPoll() {
    if (agentsTimer !== null)
        return;
    agentsTimer = window.setInterval(() => void pollAgents(), AGENTS_POLL_MS);
}
async function pollAgents() {
    try {
        agentsData = await go().Instances();
    }
    catch (err) {
        agentsData = { agents: [], error: errMessage(err) };
    }
    renderAgents();
}
function formatTokens(n) {
    if (n >= 1000000)
        return (n / 1e6).toFixed(1) + "M";
    if (n >= 1000)
        return (n / 1e3).toFixed(1) + "K";
    return String(n);
}
// formatElapsedUnix mirrors the TUI's formatElapsed: seconds under a minute,
// "Nm Ns" under an hour, "Nh Nm" beyond. startedAtUnix of 0 means "unknown".
function formatElapsedUnix(startedAtUnix) {
    if (!startedAtUnix)
        return "";
    const secs = Math.max(0, Math.floor(Date.now() / 1000) - startedAtUnix);
    if (secs < 60)
        return `${secs}s`;
    if (secs < 3600)
        return `${Math.floor(secs / 60)}m ${secs % 60}s`;
    return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
}
function formatDurationMs(ms) {
    const secs = Math.round(ms / 1000);
    if (secs < 60)
        return `${secs}s`;
    return `${Math.floor(secs / 60)}m ${secs % 60}s`;
}
function agentStatusDot(a) {
    // Mirrors the TUI sessionIcon: a spinner while working, ⚠ on error, and a
    // coloured ● otherwise — with idle splitting on whether the session has seen
    // any activity (● active vs ○ never-active).
    if (a.status === "working")
        return `<span class="agent-dot working"><span class="spinner"></span></span>`;
    if (a.status === "error")
        return `<span class="agent-dot error">⚠</span>`;
    if (a.status === "blocked")
        return `<span class="agent-dot blocked">●</span>`;
    if (a.status === "waiting")
        return `<span class="agent-dot waiting">●</span>`;
    if (a.hasActivity)
        return `<span class="agent-dot active">●</span>`;
    return `<span class="agent-dot idle">○</span>`;
}
function tokenBars(models) {
    const total = models.reduce((sum, m) => sum + m.inputTokens + m.outputTokens, 0);
    if (total === 0)
        return "";
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
function taskLine(a) {
    const parts = [];
    if (a.tasksPending > 0)
        parts.push(`${a.tasksPending} pending`);
    if (a.tasksInProgress > 0)
        parts.push(`${a.tasksInProgress} in progress`);
    if (a.tasksDone > 0)
        parts.push(`${a.tasksDone} done`);
    if (parts.length === 0)
        return "";
    return `<div class="agent-tasks">Tasks: ${escapeHtml(parts.join(" · "))}</div>`;
}
// subagentLines mirrors the TUI renderSubagents: drop agents completed >5s ago,
// show at most the last 5, spinner for running and ✓ for done.
function subagentLines(subs) {
    const now = Date.now();
    const visible = subs.filter((s) => !s.done || now - (s.startedAtUnix * 1000 + s.durationMs) <= 5000);
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
function renderAgentRow(a) {
    const chips = [];
    if (a.daemonConnected)
        chips.push(`<span class="agent-chip proxy">${a.proxyConfigured ? "⚡+proxy" : "⚡"}</span>`);
    else if (a.proxyConfigured)
        chips.push(`<span class="agent-chip proxy">proxy</span>`);
    if (a.memory)
        chips.push(`<span class="agent-chip">${escapeHtml(a.memory)}</span>`);
    const elapsed = formatElapsedUnix(a.startedAtUnix);
    if (elapsed)
        chips.push(`<span class="agent-chip">${elapsed}</span>`);
    if (a.slug)
        chips.push(`<span class="agent-chip slug">${escapeHtml(a.slug)}</span>`);
    const ctx = a.errorType || a.blockedTool || a.currentTool;
    if (ctx)
        chips.push(`<span class="agent-chip ctx">${escapeHtml(a.errorType ? a.errorType : a.blockedTool ? "⚠ " + a.blockedTool : "[" + a.currentTool + "]")}</span>`);
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
function renderAgents() {
    const host = document.getElementById("agents");
    if (!host)
        return;
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
let currentFeatureDoc = {};
let featuresPollTimer;
async function loadFeatures() {
    let doc;
    try {
        doc = await go().Features();
    }
    catch (err) {
        doc = { error: errMessage(err) };
    }
    renderFeatures(doc);
}
// featureSig is a stable fingerprint of the rendered doc: presence plus product,
// tagline, and the recursive group/feature names+descriptions. Two runs that
// produce the same map yield the same signature, so polling only reacts to a
// real change.
function featureSig(doc) {
    if (!doc.exists)
        return "«sent»";
    const walk = (gs = []) => gs
        .map((g) => g.group +
        "|" +
        (g.features ?? []).map((f) => f.name + ":" + f.description + (f.recent ? "*" : "")).join(",") +
        "|" +
        walk(g.groups))
        .join(";");
    return (doc.product ?? "") + "¦" + (doc.tagline ?? "") + "¦" + walk(doc.groups);
}
function stopFeaturesPoll() {
    if (featuresPollTimer !== undefined) {
        clearInterval(featuresPollTimer);
        featuresPollTimer = undefined;
    }
}
// startFeaturesPoll watches for the generation agent's output. It re-reads
// FEATURE.json every 4s and, when the doc's signature differs from the baseline
// captured at click time, stops and re-renders. A 10-minute cap avoids polling
// forever if the agent is slow or fails silently.
function startFeaturesPoll() {
    stopFeaturesPoll();
    const started = Date.now();
    const timeoutMs = 10 * 60 * 1000;
    featuresPollTimer = window.setInterval(() => {
        void (async () => {
            let doc;
            try {
                doc = await go().Features();
            }
            catch {
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
async function onGenerateFeatures() {
    if (featuresGenerating)
        return;
    featuresBaselineSig = featureSig(currentFeatureDoc);
    featuresGenerating = true;
    // Generation runs a coding agent in a container (survey → synthesis), so it
    // is not instant — set expectations up front and keep the note up while the
    // poll waits for FEATURE.json.
    featuresNote = "Running the generation agent — this can take several minutes…";
    renderFeatures(currentFeatureDoc);
    try {
        await go().GenerateFeatures();
    }
    catch (err) {
        featuresGenerating = false;
        featuresNote = "Couldn't start generation: " + errMessage(err);
        renderFeatures(currentFeatureDoc);
        return;
    }
    startFeaturesPoll();
}
function renderFeatureRow(f) {
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
function renderFeatureGroup(g, depth = 0) {
    const rows = (g.features ?? []).map(renderFeatureRow).join("");
    const subgroups = (g.groups ?? []).map((sg) => renderFeatureGroup(sg, depth + 1)).join("");
    return `<div class="feature-group" data-depth="${depth}">
    <div class="feature-group-title">${escapeHtml(g.group)}</div>
    ${rows}
    ${subgroups}
  </div>`;
}
function renderFeatures(doc) {
    currentFeatureDoc = doc;
    const host = document.getElementById("features");
    if (!host)
        return;
    // The action button reads "Generate" when FEATURE.json is absent and "Refresh"
    // when present; while an agent runs it is a disabled "Generating…" spinner.
    const label = featuresGenerating ? "Generating…" : doc.exists ? "Refresh" : "Generate";
    const spinner = featuresGenerating ? `<span class="spinner"></span> ` : "";
    const btn = `<button class="features-btn" ${featuresGenerating ? "disabled" : ""}>${spinner}${escapeHtml(label)}</button>`;
    const header = `<div class="agents-header features-header"><span>Features</span>${btn}</div>`;
    const note = featuresNote ? `<div class="features-note">${escapeHtml(featuresNote)}</div>` : "";
    const attach = () => host.querySelector(".features-btn")?.addEventListener("click", () => void onGenerateFeatures());
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
    const intro = doc.product || doc.tagline
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
function selectView(view) {
    document.querySelectorAll(".rail-item").forEach((item) => {
        const active = item.dataset.view === view;
        item.classList.toggle("active", active);
        if (active)
            item.setAttribute("aria-current", "page");
        else
            item.removeAttribute("aria-current");
    });
    // Toggle main-area containers: exactly one top-level view is visible.
    const board = document.getElementById("board");
    const agents = document.getElementById("agents");
    const features = document.getElementById("features");
    const mockups = document.getElementById("mockups");
    const settings = document.getElementById("settings");
    board?.classList.toggle("hidden", view !== "board");
    agents?.classList.toggle("hidden", view !== "agents");
    features?.classList.toggle("hidden", view !== "features");
    mockups?.classList.toggle("hidden", view !== "mockups");
    settings?.classList.toggle("hidden", view !== "settings");
    if (view === "agents") {
        void pollAgents(); // immediate fetch so the view isn't blank until the first tick
        startAgentsPoll();
    }
    else {
        stopAgentsPoll();
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
function wireRail() {
    document.querySelectorAll(".rail-item").forEach((item) => {
        // Disabled placeholders are inert via the native `disabled` attribute.
        if (item.disabled)
            return;
        item.addEventListener("click", () => {
            // Action items trigger a command (e.g. the ideation chat) rather than
            // switching the active view.
            if (item.dataset.action === "ideation") {
                void openIdeation();
                return;
            }
            const view = item.dataset.view;
            if (view)
                selectView(view);
        });
    });
}
function init() {
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
    initMockupsView(() => go());
    initSettingsView(() => go());
    initPalette({ index: settingsIndex, refresh: showSettings, save: saveSetting });
    setPaletteOpener(() => openPalette());
    // The daemon status line deep-links to its home: Settings → Daemon shows
    // status, registered projects, and the daemon-related config.
    document.getElementById("statusbar")?.addEventListener("click", () => {
        setActiveSection("daemon");
        selectView("settings");
    });
    document.addEventListener("keydown", (e) => {
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
    document.getElementById("ideation-form")?.addEventListener("submit", (e) => {
        e.preventDefault();
        void submitIdeation();
    });
    document.querySelectorAll(".ideation-mode-btn").forEach((btn) => {
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
}
else {
    init();
}
