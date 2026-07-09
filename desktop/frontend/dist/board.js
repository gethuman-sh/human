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
    const next = STAGES[stageIndex(card.stage) + 1];
    const draggable = !!next && targetEnabled(next);
    el.setAttribute("draggable", draggable ? "true" : "false");
    el.dataset.key = card.key;
    el.dataset.stage = card.stage;
    const meta = [];
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
    el.addEventListener("dragstart", (e) => {
        dragging = { key: card.key, title: card.title, stage: card.stage };
        el.classList.add("dragging");
        if (e.dataTransfer)
            e.dataTransfer.effectAllowed = "move";
    });
    el.addEventListener("dragend", () => {
        dragging = null;
        el.classList.remove("dragging");
    });
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
        wireDropTarget(zone, stage);
        body.appendChild(zone);
    }
    else if (stage !== "backlog") {
        wireDropTarget(body, stage);
    }
    col.appendChild(body);
    return col;
}
function wireDropTarget(el, to) {
    el.addEventListener("dragover", (e) => {
        if (!dragging)
            return;
        const ok = isAdjacentForward(dragging.stage, to) && targetEnabled(to);
        el.classList.toggle("drop-ok", ok);
        el.classList.toggle("drop-reject", !ok);
        if (ok) {
            e.preventDefault();
            if (e.dataTransfer)
                e.dataTransfer.dropEffect = "move";
        }
    });
    el.addEventListener("dragleave", () => {
        el.classList.remove("drop-ok", "drop-reject");
    });
    el.addEventListener("drop", (e) => {
        e.preventDefault();
        el.classList.remove("drop-ok", "drop-reject");
        if (!dragging)
            return;
        const from = dragging.stage;
        if (!isAdjacentForward(from, to) || !targetEnabled(to))
            return;
        void transition(dragging.key, dragging.title, from, to);
        dragging = null;
    });
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
    await refresh();
}
function render() {
    const board = document.getElementById("board");
    board.innerHTML = "";
    for (const stage of STAGES)
        board.appendChild(renderColumn(stage));
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
async function refresh() {
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
            void refresh();
        });
    }
    void refresh();
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
