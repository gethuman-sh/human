// Pure builder for the board detail panel's comment-sourced sections, kept
// free of DOM and Wails bindings so it can be unit-tested directly (mirrors
// board-queue.ts). The daemon renders and sanitizes each field's HTML; this
// module only wraps present, non-blank fields in titled sections and drops
// absent ones so the panel never shows an empty heading.
// escapeText is a minimal HTML escaper for the option labels/context, which
// come from ticket comments (agent-written, but still untrusted text).
function escapeText(s) {
    return s.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;");
}
// buildOptionsSection renders a card's open decision block: the one-line
// context and one button per option. The buttons carry data-option-id; the
// caller wires the click to ChooseOption. Empty options emit nothing.
export function buildOptionsSection(context, options) {
    if (!options || options.length === 0)
        return "";
    const ctx = context && context.trim() ? `<div class="detail-options-context">${escapeText(context)}</div>` : "";
    const buttons = options
        .map((o) => `<button type="button" class="detail-option-btn" data-option-id="${escapeText(o.id)}">` +
        `<span class="detail-option-id">${escapeText(o.id)}</span>${escapeText(o.label)}</button>`)
        .join("");
    return (`<section class="detail-section detail-options"><h3 class="detail-section-title">Decision needed</h3>` +
        `${ctx}<div class="detail-options-list">${buttons}</div></section>`);
}
// Human phrasing for the detail-panel heading of each pre-planning stop verdict,
// paired with board-queue's STOP_DECISION_LABELS (badge copy). Kept here so the
// pure builder needs no cross-module import in tests.
const STOP_DECISION_HEADINGS = {
    superseded: "Duplicate of another ticket",
    escalated: "Blocked on a design decision",
    rejected: "Not a real problem",
};
// buildStopDecisionSection renders a pre-planning gate's recorded STOP verdict:
// the decision in human terms, the ticket it names (as a button the caller wires
// to open that card), and the recorded reasoning. Empty decision emits nothing so
// an undecided card's panel is unchanged (SC-2699).
export function buildStopDecisionSection(decision, linkedKey, reasoning) {
    if (!decision)
        return "";
    const heading = STOP_DECISION_HEADINGS[decision] ?? "Stopped before planning";
    const linked = linkedKey && linkedKey.trim()
        ? `<div class="detail-stop-linked">See <button type="button" class="detail-linked-btn" ` +
            `data-linked-key="${escapeText(linkedKey)}">${escapeText(linkedKey)}</button></div>`
        : "";
    const body = reasoning && reasoning.trim()
        ? `<div class="detail-stop-reasoning">${escapeText(reasoning).replaceAll("\n", "<br>")}</div>`
        : "";
    return (`<section class="detail-section detail-stop"><h3 class="detail-section-title">Decision: ${escapeText(heading)}</h3>` +
        `${linked}${body}</section>`);
}
// buildShippedPartialSection renders the durable shipped-partial trace: a heading
// stating the card shipped less than its ticket asked, and the follow-on ticket
// it deferred the rest to (as a button the caller wires to open that card, reusing
// the detail-linked-btn / data-linked-key convention). Empty when the card carries
// no such trace, so an ordinary card's panel is unchanged (SC-2910).
export function buildShippedPartialSection(shippedPartial, followOnKey) {
    if (!shippedPartial)
        return "";
    const linked = followOnKey && followOnKey.trim()
        ? `<div class="detail-partial-followon">The deferred work now lives on ` +
            `<button type="button" class="detail-linked-btn" data-linked-key="${escapeText(followOnKey)}">${escapeText(followOnKey)}</button>.</div>`
        : "";
    return (`<section class="detail-section detail-partial"><h3 class="detail-section-title">Shipped partial</h3>` +
        `<div class="detail-partial-note">This ticket shipped with one or more acceptance criteria deliberately deferred.</div>` +
        `${linked}</section>`);
}
// buildDetailSections returns the HTML for the comment-sourced detail sections,
// in fixed order: failure reason, review findings, fix summary. Each present,
// non-blank field becomes a titled <section>; absent or blank fields emit
// nothing. Section titles are static literals; only the daemon-sanitized *HTML
// values are injected, and the caller injects the result verbatim.
export function buildDetailSections(d) {
    const sections = [];
    const add = (title, html, cls) => {
        if (html && html.trim()) {
            sections.push(`<section class="detail-section ${cls}"><h3 class="detail-section-title">${title}</h3><div class="detail-section-body rendered">${html}</div></section>`);
        }
    };
    add(d.paused ? "Why it's paused" : "Why it failed", d.failureReasonHTML, "detail-failure");
    add("What the review found", d.reviewFindingsHTML, "detail-review");
    add("Fix summary", d.fixSummaryHTML, "detail-fixsummary");
    return sections.join("");
}
// fmtUSD/fmtDuration are local so board-detail has no dependency on statsview.
// Sub-dollar figures get four decimals so a few cents never rounds to "$0.00".
function fmtUSD(n) {
    return "$" + n.toFixed(n < 1 ? 4 : 2);
}
function fmtDuration(ms) {
    const s = Math.round(ms / 1000);
    if (s < 60)
        return `${s}s`;
    const m = Math.floor(s / 60);
    if (m < 60)
        return `${m}m ${s % 60}s`;
    const h = Math.floor(m / 60);
    return `${h}h ${m % 60}m`;
}
// elapsedLabel names what the current-stage clock actually measures. "running"
// is a claim about a live process: when the viewer can see there is none — the
// agent died, or the stage belongs to another machine — the same number must be
// labelled honestly, or a card that has been dead for fourteen hours reads as
// the busiest one on the board (SC-3569). Unknown liveness keeps today's word,
// because absence of a signal is not proof.
function elapsedLabel(agentLiveness) {
    if (agentLiveness === "dead")
        return "since last activity";
    if (agentLiveness === "elsewhere")
        return "— on another machine";
    return "running";
}
// buildCostSection renders the ticket's whole-life cost (with the answers/context
// split) and elapsed time (per-stage plus the live current-stage clock). A ticket
// with no recorded spend says so plainly rather than showing $0.00 (SC-2847
// criterion 5). currentStage/stageEnteredAt come from the open card; nowMs is
// injected for tests. agentLiveness comes from the open card's viewer-side
// overlay; absent means unknown and keeps the pre-SC-3569 wording.
export function buildCostSection(c, currentStage, stageEnteredAt, nowMs, agentLiveness) {
    if (!c || !c.hasSpend) {
        return `<section class="detail-section detail-cost"><h3 class="detail-section-title">Cost &amp; time</h3>` +
            `<div class="detail-cost-empty">No spend recorded for this ticket yet.</div></section>`;
    }
    const curElapsed = stageEnteredAt
        ? `<div class="detail-cost-current">Current stage (${escapeText(currentStage ?? "")}): ` +
            `${escapeText(fmtDuration(Math.max(0, nowMs - Date.parse(stageEnteredAt))))} ${escapeText(elapsedLabel(agentLiveness))}</div>`
        : "";
    const stageRows = c.stages.map((s) => `<div class="detail-cost-stage"><span>${escapeText(s.stage || "—")}</span>` +
        `<span>${escapeText(fmtUSD(s.costUSD))} · ${escapeText(fmtDuration(s.durationMs))}</span></div>`).join("");
    return `<section class="detail-section detail-cost"><h3 class="detail-section-title">Cost &amp; time</h3>` +
        `<div class="detail-cost-total">${escapeText(fmtUSD(c.totalCostUSD))} · ${escapeText(fmtDuration(c.totalDurationMs))}</div>` +
        `<div class="detail-cost-split">answers ${escapeText(fmtUSD(c.answersCostUSD))} · context ${escapeText(fmtUSD(c.contextCostUSD))}</div>` +
        curElapsed +
        `<div class="detail-cost-stages">${stageRows}</div></section>`;
}
