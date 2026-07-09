// Workflow-board frontend (typed source). Renders 5 forward-order columns from
// the daemon's derived BoardCards (via the Go App.Cards binding) and lets a card
// be dragged to its single next column to trigger that stage's `human` action
// via App.Transition. Placement, checkmarks and running/error state are all
// derived server-side — this file never re-derives a stage.
//
// The shipped runtime is desktop/frontend/dist/board.js; `npm run build`
// (tsc + bundle.mjs) regenerates dist/ from this source for `wails build`.
const STAGES = ["backlog", "planning", "implementation", "verification", "done"];
const STAGE_LABELS = {
    backlog: "Backlog",
    planning: "Product planning",
    implementation: "Implementation",
    verification: "Verification",
    done: "Done",
};
const AGENT_STAGES = new Set(["planning", "implementation", "verification"]);
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
function stageIndex(stage) {
    return STAGES.indexOf(stage);
}
function isAdjacentForward(from, to) {
    return stageIndex(to) === stageIndex(from) + 1;
}
function targetEnabled(to) {
    if (AGENT_STAGES.has(to) && !current.dockerAvailable)
        return false;
    return true;
}
function badge(state) {
    if (state === "done")
        return `<span class="badge done" title="Stage complete">✓</span>`;
    if (state === "running")
        return `<span class="badge running" title="Agent running"><span class="spinner"></span></span>`;
    if (state === "failed")
        return `<span class="badge failed" title="Stage failed">✕</span>`;
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
    const b = badge(card.state);
    if (b)
        meta.push(b);
    if (card.engineeringKey)
        meta.push(`<span>${escapeHtml(card.engineeringKey)}</span>`);
    if (card.prURL)
        meta.push(`<a href="${escapeAttr(card.prURL)}" target="_blank">PR</a>`);
    el.innerHTML = `
    <div class="card-key">${escapeHtml(card.key)}</div>
    <div class="card-title">${escapeHtml(card.title)}</div>
    <div class="card-meta">${meta.join("")}</div>
    ${card.error ? `<div class="card-error">${escapeHtml(card.error)}</div>` : ""}
  `;
    beginPointerDrag(el, card);
    return el;
}
function renderColumn(stage) {
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
        header.querySelector(".add-card").addEventListener("click", () => void openIdeation());
    }
    else {
        header.innerHTML = `<span>${STAGE_LABELS[stage]}</span><span class="column-count">${cards.length}</span>`;
    }
    col.appendChild(header);
    const body = document.createElement("div");
    body.className = "column-body";
    for (const card of cards)
        body.appendChild(renderCard(card));
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
    }
    else if (stage !== "backlog") {
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
let dragGhost = null;
let hoverTarget = null;
function markStageTarget(el, to) {
    el.dataset.drop = "stage";
    el.dataset.dropStage = to;
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
// be closed; stage targets keep the forward-adjacency + docker-enabled rules.
function dropAllowed(target) {
    if (!dragging)
        return false;
    if (target.dataset.drop === "close")
        return true;
    const to = target.dataset.dropStage || "";
    return isAdjacentForward(dragging.stage, to) && targetEnabled(to);
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
            }
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
            dragging = null;
            if (target && allowed)
                performDrop(target, info);
        };
        const onCancel = () => {
            teardown();
            dragging = null;
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
// performDrop runs the action for a completed drop on an allowed target.
function performDrop(target, info) {
    if (target.dataset.drop === "close") {
        void requestClose(info.key, info.title);
        return;
    }
    void transition(info.key, info.title, info.stage, target.dataset.dropStage || "");
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
function render() {
    const board = document.getElementById("board");
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
        for (const stage of STAGES)
            board.appendChild(renderColumn(stage));
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
    const dot = document.getElementById("daemon-status");
    dot.classList.toggle("reachable", daemonReachable);
    dot.classList.toggle("unreachable", !daemonReachable);
    dot.title = daemonReachable ? "Daemon reachable" : "Daemon unreachable";
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
    const input = document.getElementById("ideation-input");
    const send = document.getElementById("ideation-send");
    const inputEnabled = ideation.state === "awaiting_reply" ||
        ideation.state === "none" ||
        ideation.state === "done" ||
        ideation.state === "error";
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
async function submitIdeation() {
    const input = document.getElementById("ideation-input");
    if (!input)
        return;
    const text = input.value.trim();
    if (!text)
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
    input.value = "";
    renderIdeation();
    startIdeationPoll();
    try {
        if (isFresh) {
            ideation = await go().StartIdeation(text, restart);
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
// --- Left activity rail ------------------------------------------------
//
// A scaffold for future top-level views. Today "board" is the only real view
// (it is always mounted); other rail items are disabled placeholders. Adding a
// view later means an enabled `.rail-item` in index.html plus a `case` below —
// no change to the board render/refresh path.
function selectView(view) {
    document.querySelectorAll(".rail-item").forEach((item) => {
        const active = item.dataset.view === view;
        item.classList.toggle("active", active);
        if (active)
            item.setAttribute("aria-current", "page");
        else
            item.removeAttribute("aria-current");
    });
    switch (view) {
        case "board":
            // The board is always mounted; nothing to swap in yet.
            break;
        // Future views mount/unmount their containers here.
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
    document.getElementById("ideation-close")?.addEventListener("click", () => closeIdeation());
    document.getElementById("ideation-form")?.addEventListener("submit", (e) => {
        e.preventDefault();
        void submitIdeation();
    });
}
if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
}
else {
    init();
}
export {};
