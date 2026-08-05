// Stats view: a glanceable page of what the factory is doing with its
// resources, all from data human already records locally. A headline row (token
// window, tool calls, audit outcomes, agent runs) sits above four panels
// (tokens per hour, tool calls by tool, audit outcomes by day, network
// decisions) plus a 24h/7d/30d range switch that drives every panel except the
// live network snapshot.
//
// The whole payload comes from one daemon route (App.Stats), so the range
// switch is atomic. This view only reads already-recorded local data — it adds
// no collection and nothing leaves the machine. Follows settingsview.ts
// conventions: an init(getBindings) + a show() entry point, with pure helpers
// pulled out for node-test units.
let bindings = null;
let latest = null;
let error = "";
let range = "24h";
let timer = null;
// inFlight guards against a second fetch stacking on the first: a poll tick or a
// range-switch arriving mid-fetch sets pendingFetch instead of walking again.
let inFlight = false;
let pendingFetch = false;
// polling tracks whether the self-rescheduling poll loop should keep arming its
// next tick; stopStatsPoll clears it so an in-flight fetch's .finally does not
// resurrect the timer after the view is left.
let polling = false;
// The token walk is the daemon's only expensive stats source and is cached with
// a short TTL, so the live network panel — the only truly live source — can
// refresh on a relaxed cadence that leaves the daemon headroom.
const POLL_MS = 15000;
export function initStatsView(getBindings) {
    bindings = getBindings;
    // A fresh view starts blank with no fetch outstanding, so re-init clears any
    // prior render/fetch state rather than inheriting a stale payload or a
    // dangling in-flight flag.
    latest = null;
    error = "";
    inFlight = false;
    pendingFetch = false;
}
export function setStatsRange(r) {
    range = r;
    void showStats();
}
function isObj(v) {
    return typeof v === "object" && v !== null;
}
function num(v) {
    return typeof v === "number" && isFinite(v) ? v : 0;
}
function str(v) {
    return typeof v === "string" ? v : "";
}
// rowList coerces an arbitrary payload field into a list of normalized rows:
// anything other than an array becomes empty (a field whose type changed —
// e.g. a list that became an object — degrades exactly like a missing field),
// and each element is normalized individually so one bad row cannot cost its
// neighbours.
function rowList(v, normalizeRow) {
    if (!Array.isArray(v))
        return [];
    return v.map(normalizeRow);
}
function headline(v) {
    const o = isObj(v) ? v : {};
    return { total: num(o.total), success: num(o.success), failure: num(o.failure) };
}
function tokenTotals(v) {
    const o = isObj(v) ? v : {};
    return {
        input: num(o.input),
        output: num(o.output),
        cacheCreate: num(o.cacheCreate),
        cacheRead: num(o.cacheRead),
        costUSD: num(o.costUSD),
    };
}
function ticketCostRow(v) {
    const o = isObj(v) ? v : {};
    return {
        ticket: str(o.ticket),
        costUSD: num(o.costUSD),
        contextCostUSD: num(o.contextCostUSD),
        answersCostUSD: num(o.answersCostUSD),
        outputTokens: num(o.outputTokens),
        contextTokens: num(o.contextTokens),
        durationMs: num(o.durationMs),
    };
}
function modelTokensRow(v) {
    const o = isObj(v) ? v : {};
    return {
        model: str(o.model),
        input: num(o.input),
        output: num(o.output),
        cacheCreate: num(o.cacheCreate),
        cacheRead: num(o.cacheRead),
        costUSD: num(o.costUSD),
    };
}
function toolCountRow(v) {
    const o = isObj(v) ? v : {};
    return { tool_name: str(o.tool_name), count: num(o.count) };
}
function auditDayRow(v) {
    const o = isObj(v) ? v : {};
    return { day: str(o.day), approved: num(o.approved), denied: num(o.denied), failed: num(o.failed) };
}
function networkDecisionRow(v) {
    const o = isObj(v) ? v : {};
    return {
        source: str(o.source),
        status: str(o.status),
        host: str(o.host),
        count: num(o.count),
        last_seen: str(o.last_seen),
    };
}
// statsFromPayload is the single door the fetched payload comes through: the
// daemon and the board app are separate builds, so a field this app expects
// may be missing, or of a different shape, from what StatsOverview declares.
// Every field is coerced by runtime type rather than passed through with
// `?? []`, so a field whose TYPE changed (not just one that vanished)
// degrades the same way — to an empty/zeroed value — instead of throwing
// deep inside a panel (SC-3508).
export function statsFromPayload(raw) {
    const o = isObj(raw) ? raw : {};
    return {
        range: str(o.range),
        generatedAt: str(o.generatedAt),
        daemonStartedAt: str(o.daemonStartedAt),
        tokens: tokenTotals(o.tokens),
        toolCalls: headline(o.toolCalls),
        audit: headline(o.audit),
        agentRuns: headline(o.agentRuns),
        ticketCosts: rowList(o.ticketCosts, ticketCostRow),
        tokensByModel: rowList(o.tokensByModel, modelTokensRow),
        toolsByTool: rowList(o.toolsByTool, toolCountRow),
        auditByDay: rowList(o.auditByDay, auditDayRow),
        networkDecisions: rowList(o.networkDecisions, networkDecisionRow),
    };
}
// showStats paints first, then fetches. The synchronous render() shows the
// header and a Loading state (or the last data) the instant the view activates,
// so the section is never blank while the fetch is in flight. A fetch already
// running is not doubled: pendingFetch flags a single follow-up that picks up
// the current range, coalescing a mid-fetch range-switch or poll tick into one
// refetch. Errors land in a banner rather than blanking the page (mirrors the
// agents view error path).
export async function showStats() {
    if (!bindings)
        return;
    render(); // first paint: header + Loading / last data, before any await
    if (inFlight) {
        pendingFetch = true;
        return;
    }
    inFlight = true;
    try {
        do {
            pendingFetch = false;
            try {
                latest = statsFromPayload(await bindings().Stats(range));
                error = "";
            }
            catch (err) {
                error = err instanceof Error ? err.message : String(err);
            }
            render();
        } while (pendingFetch); // a range-switch mid-fetch resolves to the last-clicked range
    }
    finally {
        inFlight = false;
    }
}
function stopStatsPollTimer() {
    if (timer !== null) {
        clearTimeout(timer);
        timer = null;
    }
}
// scheduleStatsPoll arms the next poll only after the current fetch resolves, so
// ticks never overlap (a slow fetch simply delays the next tick rather than
// stacking a second one). It re-checks polling each time so a stop that lands
// mid-fetch is honored.
function scheduleStatsPoll() {
    if (!polling)
        return;
    timer = window.setTimeout(() => {
        timer = null;
        if (!polling)
            return;
        void showStats().finally(scheduleStatsPoll);
    }, POLL_MS);
}
export function startStatsPoll() {
    if (polling)
        return;
    polling = true;
    scheduleStatsPoll();
}
export function stopStatsPoll() {
    polling = false;
    stopStatsPollTimer();
}
// rangeCoversHistory reports whether the daemon has been up at least as long as
// the selected range. When false the view shows a "history still filling" note,
// because a panel that looks empty may simply predate the daemon's start.
export function rangeCoversHistory(daemonStartedAtISO, r, now) {
    const started = Date.parse(daemonStartedAtISO);
    if (Number.isNaN(started))
        return true; // unknown start: don't cry wolf
    const spanMs = rangeSpanMs(r);
    return now - started >= spanMs;
}
function rangeSpanMs(r) {
    const day = 24 * 60 * 60 * 1000;
    if (r === "7d")
        return 7 * day;
    if (r === "30d")
        return 30 * day;
    return day;
}
// contextTokens is everything spent establishing and re-reading context —
// fresh input plus cache writes plus cache reads — as opposed to producing
// output. Separating this from output is the point of the split: it is what
// lets the page show that most of the spend is context, not answers.
export function contextTokens(r) {
    return r.input + r.cacheCreate + r.cacheRead;
}
// fmtUSD formats a cost as a dollar figure so token counts can be read as
// money without the reader knowing the rate card.
export function fmtUSD(n) {
    return "$" + n.toFixed(2);
}
// barPercents normalizes values to 0..100 against the max, so a set of bars
// reads as relative magnitude. An all-zero (or empty) input yields all zeros
// rather than dividing by zero.
export function barPercents(values) {
    const max = values.reduce((m, v) => (v > m ? v : m), 0);
    if (max <= 0)
        return values.map(() => 0);
    return values.map((v) => Math.round((v / max) * 100));
}
function escapeHtml(s) {
    return String(s == null ? "" : s)
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;");
}
function fmtNum(n) {
    if (n >= 1_000_000)
        return (n / 1e6).toFixed(1) + "M";
    if (n >= 1_000)
        return (n / 1e3).toFixed(1) + "K";
    return String(n);
}
function host() {
    return document.getElementById("stats");
}
// render resolves the host and hands drawing to paint(), routing any throw —
// from a panel nobody has thought to guard yet — to paintFault() instead of
// leaving #stats on whatever it last painted (SC-3508). Normalization removes
// the payload faults we already know about; this is the backstop for the rest.
function render() {
    const h = host();
    if (!h)
        return;
    try {
        paint(h);
    }
    catch (err) {
        paintFault(h, err);
    }
}
function paint(h) {
    if (error) {
        h.innerHTML = renderHeader() + `<div class="banner">${escapeHtml(error)}</div>`;
        wireRange(h);
        return;
    }
    if (!latest) {
        h.innerHTML = `<div class="stats-header">Stats</div><div class="stats-empty">Loading…</div>`;
        return;
    }
    h.innerHTML = renderHeader() + renderFillingNote(latest) + renderHeadlines(latest) + renderPanels(latest);
    wireRange(h);
}
// paintFault paints the same banner shape a fetch error uses, naming the
// remedy directly (a stale build, not a data problem the user can fix). It
// does not touch the sticky `error` state — a render fault that set it would
// keep bannering after the payload recovered until the next successful
// fetch, coupling the state machine to which of two render() calls faulted.
// If the banner render itself throws (a host that rejects every write), fall
// back to plain text rather than leaving the page exactly as broken as before.
function paintFault(h, err) {
    const msg = err instanceof Error ? err.message : String(err);
    const banner = `The board app is out of date relative to the daemon — rebuild it (make desktop). Details: ${escapeHtml(msg)}`;
    try {
        h.innerHTML = renderHeader() + `<div class="banner">${banner}</div>`;
        wireRange(h);
    }
    catch {
        try {
            h.textContent = `The board app is out of date relative to the daemon — rebuild it (make desktop). Details: ${msg}`;
        }
        catch {
            // nothing left to try: the host rejects every write we know how to make
        }
    }
}
function renderHeader() {
    const btn = (r, label) => `<button class="stats-range-btn${r === range ? " active" : ""}" data-range="${r}" type="button">${label}</button>`;
    return (`<div class="stats-header">` +
        `<span>Stats</span>` +
        `<span class="stats-range">${btn("24h", "24h")}${btn("7d", "7d")}${btn("30d", "30d")}</span>` +
        `</div>`);
}
// The note appears only while the daemon's uptime is shorter than the selected
// range, so an empty panel there reads as "no history yet" rather than "no data".
function renderFillingNote(s) {
    if (rangeCoversHistory(s.daemonStartedAt, range, Date.now()))
        return "";
    return `<div class="stats-note">Daemon started recently — history is still filling.</div>`;
}
function headlineCard(label, big, sub) {
    return (`<div class="stats-card">` +
        `<div class="stats-card-label">${escapeHtml(label)}</div>` +
        `<div class="stats-card-big">${escapeHtml(big)}</div>` +
        `<div class="stats-card-sub">${escapeHtml(sub)}</div>` +
        `</div>`);
}
function renderHeadlines(s) {
    const cards = [
        headlineCard("Tokens (5h window)", fmtUSD(s.tokens.costUSD), `${fmtNum(s.tokens.output)} output · ${fmtNum(contextTokens(s.tokens))} context`),
        headlineCard("Tool calls", fmtNum(s.toolCalls.total), `${fmtNum(s.toolCalls.success)} ok · ${fmtNum(s.toolCalls.failure)} err`),
        headlineCard("Audit outcomes", fmtNum(s.audit.total), `${fmtNum(s.audit.success)} approved · ${fmtNum(s.audit.failure)} denied/failed`),
        headlineCard("Agent runs", fmtNum(s.agentRuns.total), `${fmtNum(s.agentRuns.success)} ok · ${fmtNum(s.agentRuns.failure)} failed`),
    ];
    return `<div class="stats-headline-row">${cards.join("")}</div>`;
}
function renderPanels(s) {
    return (`<div class="stats-panels">` +
        panelTicketCosts(s) +
        panelTokensByModel(s) +
        panelTools(s) +
        panelAudit(s) +
        panelNetwork(s) +
        `</div>`);
}
function panelShell(title, badge, body) {
    const b = badge ? `<span class="stats-badge">${escapeHtml(badge)}</span>` : "";
    return (`<div class="stats-panel">` +
        `<div class="stats-panel-head"><span>${escapeHtml(title)}</span>${b}</div>` +
        body +
        `</div>`);
}
function emptyBody() {
    return `<div class="stats-empty">No data yet</div>`;
}
// Cost by ticket: one row per ticket over the range, most expensive first, with
// the same two bars every other token panel uses (answers, context). It
// replaces the per-hour burn, which showed WHEN tokens were spent — a question
// nobody acts on — where this one shows WHICH WORK spent them, which is the
// question the money is actually attached to (SC-3497).
//
// A ticket with no priced spend still lists, at $0.00: rows written before the
// proxy could read usage off a compressed body carry duration only, and hiding
// them would claim the work never ran.
function panelTicketCosts(s) {
    const rows = s.ticketCosts;
    if (rows.length === 0)
        return panelShell("Cost by ticket", "", emptyBody());
    const all = rows.flatMap((r) => [r.outputTokens, r.contextTokens]);
    const pcts = barPercents(all);
    const body = rows
        .map((r, i) => {
        const op = pcts[i * 2];
        const cp = pcts[i * 2 + 1];
        return (`<div class="stats-hour-row">` +
            `<span class="stats-hour-label">${escapeHtml(r.ticket)}</span>` +
            `<span class="stats-hour-bars">` +
            `<span class="token-bar"><span class="token-bar-fill fresh" style="width:${op}%"></span></span>` +
            `<span class="token-bar"><span class="token-bar-fill cache" style="width:${cp}%"></span></span>` +
            `</span>` +
            `<span class="stats-hour-val">${escapeHtml(fmtUSD(r.costUSD))} · ${escapeHtml(fmtNum(r.outputTokens))}/${escapeHtml(fmtNum(r.contextTokens))}</span>` +
            `</div>`);
    })
        .join("");
    return panelShell("Cost by ticket", "answers / context", body);
}
// Tokens-by-model: one row per model with two bars (output, context)
// normalized against the overall max, so the tier split (opus/sonnet/haiku)
// reads at a glance over the selected range.
function panelTokensByModel(s) {
    const rows = s.tokensByModel;
    if (rows.length === 0)
        return panelShell("Tokens by model", "", emptyBody());
    const all = rows.flatMap((r) => [r.output, contextTokens(r)]);
    const pcts = barPercents(all);
    const body = rows
        .map((r, i) => {
        const op = pcts[i * 2];
        const cp = pcts[i * 2 + 1];
        return (`<div class="stats-hour-row">` +
            `<span class="stats-hour-label">${escapeHtml(r.model)}</span>` +
            `<span class="stats-hour-bars">` +
            `<span class="token-bar"><span class="token-bar-fill fresh" style="width:${op}%"></span></span>` +
            `<span class="token-bar"><span class="token-bar-fill cache" style="width:${cp}%"></span></span>` +
            `</span>` +
            `<span class="stats-hour-val">${escapeHtml(fmtNum(r.output))}/${escapeHtml(fmtNum(contextTokens(r)))} · ${escapeHtml(fmtUSD(r.costUSD))}</span>` +
            `</div>`);
    })
        .join("");
    return panelShell("Tokens by model", "output / context", body);
}
function panelTools(s) {
    const rows = s.toolsByTool;
    if (rows.length === 0)
        return panelShell("Tool calls by tool", "", emptyBody());
    const pcts = barPercents(rows.map((r) => r.count));
    const body = rows
        .map((r, i) => {
        return (`<div class="stats-bar-row">` +
            `<span class="stats-bar-label">${escapeHtml(r.tool_name)}</span>` +
            `<span class="token-bar"><span class="token-bar-fill" style="width:${pcts[i]}%"></span></span>` +
            `<span class="stats-bar-val">${escapeHtml(fmtNum(r.count))}</span>` +
            `</div>`);
    })
        .join("");
    return panelShell("Tool calls by tool", "", body);
}
// Audit-by-day: a stacked bar per day (approved/denied/failed) normalized
// against the busiest day so relative volume reads across the week.
function panelAudit(s) {
    const rows = s.auditByDay;
    if (rows.length === 0)
        return panelShell("Audit outcomes by day", "", emptyBody());
    const totals = rows.map((r) => r.approved + r.denied + r.failed);
    const pcts = barPercents(totals);
    const body = rows
        .map((r, i) => {
        const total = totals[i] || 1;
        const seg = (n, cls) => n > 0 ? `<span class="stats-seg ${cls}" style="width:${Math.round((n / total) * 100)}%"></span>` : "";
        return (`<div class="stats-bar-row">` +
            `<span class="stats-bar-label">${escapeHtml(r.day.slice(5))}</span>` +
            `<span class="token-bar" style="max-width:${pcts[i]}%">` +
            seg(r.approved, "approved") +
            seg(r.denied, "denied") +
            seg(r.failed, "failed") +
            `</span>` +
            `<span class="stats-bar-val">${escapeHtml(String(totals[i]))}</span>` +
            `</div>`);
    })
        .join("");
    return panelShell("Audit outcomes by day", "", body);
}
// Network decisions is the one range-exempt panel: the daemon's buffer is a live
// in-memory snapshot with no historical timestamps, so it carries a "live" badge
// and is unaffected by the range switch.
function panelNetwork(s) {
    const rows = s.networkDecisions;
    if (rows.length === 0)
        return panelShell("Network decisions", "live", emptyBody());
    const body = rows
        .slice()
        .reverse() // newest first (store keeps insertion order)
        .map((r) => {
        const count = r.count > 1 ? ` ×${r.count}` : "";
        return (`<div class="stats-net-row">` +
            `<span class="stats-net-status ${escapeHtml(r.status)}">${escapeHtml(r.status)}</span>` +
            `<span class="stats-net-host">${escapeHtml(r.host || "—")}</span>` +
            `<span class="stats-net-meta">${escapeHtml(r.source)}${escapeHtml(count)}</span>` +
            `</div>`);
    })
        .join("");
    return panelShell("Network decisions", "live", body);
}
function wireRange(h) {
    h.querySelectorAll(".stats-range-btn").forEach((btn) => {
        btn.addEventListener("click", () => {
            const r = btn.dataset.range;
            if (r)
                setStatsRange(r);
        });
    });
}
