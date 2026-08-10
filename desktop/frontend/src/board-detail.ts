// Pure builder for the board detail panel's comment-sourced sections, kept
// free of DOM and Wails bindings so it can be unit-tested directly (mirrors
// board-queue.ts). The daemon renders and sanitizes each field's HTML; this
// module only wraps present, non-blank fields in titled sections and drops
// absent ones so the panel never shows an empty heading.

export interface DetailSections {
  reviewFindingsHTML?: string;
  failureReasonHTML?: string;
  fixSummaryHTML?: string;
  // paused (SC-3024) is set by the caller from card.state === "outage": the
  // evidence section then titles itself "Why it's paused" rather than "Why it
  // failed" — the stop was the machine being unavailable, not the work being
  // wrong, and the heading must say so rather than reading as a stack trace.
  paused?: boolean;
}

export interface DetailOption {
  id: string;
  label: string;
}

// escapeText is a minimal HTML escaper for the option labels/context, which
// come from ticket comments (agent-written, but still untrusted text).
function escapeText(s: string): string {
  return s.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;");
}

// buildOptionsSection renders a card's open decision block: the one-line
// context and one button per option. The buttons carry data-option-id; the
// caller wires the click to ChooseOption. Empty options emit nothing.
export function buildOptionsSection(context: string | undefined, options: DetailOption[] | undefined): string {
  if (!options || options.length === 0) return "";
  const ctx = context && context.trim() ? `<div class="detail-options-context">${escapeText(context)}</div>` : "";
  const buttons = options
    .map(
      (o) =>
        `<button type="button" class="detail-option-btn" data-option-id="${escapeText(o.id)}">` +
        `<span class="detail-option-id">${escapeText(o.id)}</span>${escapeText(o.label)}</button>`,
    )
    .join("");
  return (
    `<section class="detail-section detail-options"><h3 class="detail-section-title">Decision needed</h3>` +
    `${ctx}<div class="detail-options-list">${buttons}</div></section>`
  );
}

// Human phrasing for the detail-panel heading of each pre-planning stop verdict,
// paired with board-queue's STOP_DECISION_LABELS (badge copy). Kept here so the
// pure builder needs no cross-module import in tests.
const STOP_DECISION_HEADINGS: Record<string, string> = {
  superseded: "Duplicate of another ticket",
  escalated: "Blocked on a design decision",
  rejected: "Not a real problem",
};

// buildStopDecisionSection renders a pre-planning gate's recorded STOP verdict:
// the decision in human terms, the ticket it names (as a button the caller wires
// to open that card), and the recorded reasoning. Empty decision emits nothing so
// an undecided card's panel is unchanged (SC-2699).
export function buildStopDecisionSection(
  decision: string | undefined,
  linkedKey: string | undefined,
  reasoning: string | undefined,
): string {
  if (!decision) return "";
  const heading = STOP_DECISION_HEADINGS[decision] ?? "Stopped before planning";
  const linked =
    linkedKey && linkedKey.trim()
      ? `<div class="detail-stop-linked">See <button type="button" class="detail-linked-btn" ` +
        `data-linked-key="${escapeText(linkedKey)}">${escapeText(linkedKey)}</button></div>`
      : "";
  const body =
    reasoning && reasoning.trim()
      ? `<div class="detail-stop-reasoning">${escapeText(reasoning).replaceAll("\n", "<br>")}</div>`
      : "";
  return (
    `<section class="detail-section detail-stop"><h3 class="detail-section-title">Decision: ${escapeText(heading)}</h3>` +
    `${linked}${body}</section>`
  );
}

// buildShippedPartialSection renders the durable shipped-partial trace: a heading
// stating the card shipped less than its ticket asked, and the follow-on ticket
// it deferred the rest to (as a button the caller wires to open that card, reusing
// the detail-linked-btn / data-linked-key convention). Empty when the card carries
// no such trace, so an ordinary card's panel is unchanged (SC-2910).
export function buildShippedPartialSection(
  shippedPartial: boolean | undefined,
  followOnKey: string | undefined,
): string {
  if (!shippedPartial) return "";
  const linked =
    followOnKey && followOnKey.trim()
      ? `<div class="detail-partial-followon">The deferred work now lives on ` +
        `<button type="button" class="detail-linked-btn" data-linked-key="${escapeText(followOnKey)}">${escapeText(followOnKey)}</button>.</div>`
      : "";
  return (
    `<section class="detail-section detail-partial"><h3 class="detail-section-title">Shipped partial</h3>` +
    `<div class="detail-partial-note">This ticket shipped with one or more acceptance criteria deliberately deferred.</div>` +
    `${linked}</section>`
  );
}

// buildDetailSections returns the HTML for the comment-sourced detail sections,
// in fixed order: failure reason, review findings, fix summary. Each present,
// non-blank field becomes a titled <section>; absent or blank fields emit
// nothing. Section titles are static literals; only the daemon-sanitized *HTML
// values are injected, and the caller injects the result verbatim.
export function buildDetailSections(d: DetailSections): string {
  const sections: string[] = [];
  const add = (title: string, html: string | undefined, cls: string): void => {
    if (html && html.trim()) {
      sections.push(
        `<section class="detail-section ${cls}"><h3 class="detail-section-title">${title}</h3><div class="detail-section-body rendered">${html}</div></section>`,
      );
    }
  };
  add(d.paused ? "Why it's paused" : "Why it failed", d.failureReasonHTML, "detail-failure");
  add("What the review found", d.reviewFindingsHTML, "detail-review");
  add("Fix summary", d.fixSummaryHTML, "detail-fixsummary");
  return sections.join("");
}

// TicketCost mirrors internal/costledger.TicketCost: a ticket's whole-life
// priced roll-up with the answers/context split and per-stage breakdown.
export interface TicketCost {
  ticket: string;
  // Whether the ledger was consulted at all. False means the answer says
  // nothing about the ticket — see buildCostSection.
  ledgerRead?: boolean;
  hasSpend: boolean;
  totalCostUSD: number;
  contextCostUSD: number;
  answersCostUSD: number;
  totalDurationMs: number;
  // How many calls the roll-up covers, and how many of those carried no token
  // counts so could not be priced.
  calls?: number;
  unmeasuredCalls?: number;
  stages: { stage: string; costUSD: number; contextCostUSD: number; answersCostUSD: number; durationMs: number }[];
}

// fmtUSD/fmtDuration are local so board-detail has no dependency on statsview.
// Sub-dollar figures get four decimals so a few cents never rounds to "$0.00".
function fmtUSD(n: number): string {
  return "$" + n.toFixed(n < 1 ? 4 : 2);
}
function fmtDuration(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

// elapsedLabel names what the current-stage clock actually measures once the
// card's lifecycle state says it is running. "running" is a claim about a
// live process: when the viewer can see there is none — the agent died, or
// the stage belongs to another machine — the same number must be labelled
// honestly, or a card whose agent has been dead for fourteen hours reads as
// the busiest one on the board (SC-3569). "recovering" reads the same as
// "dead" here: no process is visible either way, and the elapsed clock is
// honest about that regardless of whether the daemon's own relaunch has come
// due yet — that distinction belongs to the badge, not this label. Unknown
// liveness keeps today's word, because absence of a signal is not proof.
function elapsedLabel(agentLiveness: string | undefined): string {
  if (agentLiveness === "dead" || agentLiveness === "recovering") return "since last activity";
  if (agentLiveness === "elsewhere") return "— on another machine";
  return "running";
}

// stageClockLine phrases the current-stage clock for the state the card is
// actually in. Only a running card is running; every other state gets the same
// measurement stated as what it is — when the stage was entered — so the number
// stops asserting work is in progress behind it (SC-4151 B3). A card whose
// lifecycle state says "running" may still have no live agent behind it —
// agentLiveness refines the word for that case (SC-3569). An absent state (a
// payload from a daemon predating the field) keeps the original wording.
function stageClockLine(
  stage: string | undefined,
  state: string | undefined,
  agentLiveness: string | undefined,
  elapsedMs: number,
): string {
  const elapsed = escapeText(fmtDuration(Math.max(0, elapsedMs)));
  const named = escapeText(stage ?? "");
  if (state === undefined || state === "running") {
    return `Current stage (${named}): ${elapsed} ${elapsedLabel(agentLiveness)}`;
  }
  return `Stage (${named}): entered ${elapsed} ago`;
}

// buildCostSection renders the ticket's whole-life cost (with the answers/context
// split) and elapsed time (per-stage plus the live current-stage clock). A ticket
// with no recorded spend says so plainly rather than showing $0.00 (SC-2847
// criterion 5). currentStage/stageEnteredAt come from the open card; nowMs is
// injected for tests. agentLiveness comes from the open card's viewer-side
// overlay; absent means unknown and keeps the pre-SC-3569 wording.
export function buildCostSection(
  c: TicketCost | null,
  currentStage: string | undefined,
  stageEnteredAt: string | undefined,
  nowMs: number,
  currentState?: string,
  agentLiveness?: string,
): string {
  if (!c || !c.hasSpend) {
    // "No spend" is a claim about the TICKET and may only be made when the
    // ledger was actually read. An older daemon (no ledgerRead field) still
    // reads as before; a daemon that says it could not consult the ledger says
    // so instead of attributing its own gap to the work (SC-4151 C8).
    const unread = c !== null && c.ledgerRead === false;
    const text = unread
      ? "Cost could not be read for this ticket — the ledger was not available."
      : "No spend recorded for this ticket yet.";
    return `<section class="detail-section detail-cost"><h3 class="detail-section-title">Cost &amp; time</h3>` +
      `<div class="detail-cost-empty">${text}</div></section>`;
  }
  // Calls that carried no token counts cannot be priced. Pricing them at zero
  // and printing the sum states that the run was free; when EVERY call is
  // unmeasured the figure is not a cost at all, so it is not shown as one
  // (SC-4151 C7). A partial gap keeps the figure and qualifies it.
  const calls = c.calls ?? 0;
  const unmeasured = c.unmeasuredCalls ?? 0;
  const allUnmeasured = calls > 0 && unmeasured === calls;
  const totalLine = allUnmeasured
    ? `cost not measured · ${escapeText(fmtDuration(c.totalDurationMs))}`
    : `${escapeText(fmtUSD(c.totalCostUSD))} · ${escapeText(fmtDuration(c.totalDurationMs))}`;
  const unmeasuredNote =
    unmeasured > 0 && !allUnmeasured
      ? `<div class="detail-cost-unmeasured">${unmeasured} of ${calls} calls recorded no tokens — not included above.</div>`
      : allUnmeasured
        ? `<div class="detail-cost-unmeasured">${calls} call${calls === 1 ? "" : "s"} recorded no tokens, so what this cost is not known.</div>`
        : "";
  // The elapsed figure measures the same thing whatever the card is doing — the
  // time since its newest stage marker landed — but "running" is a claim about
  // the work, and this section was making it for every state. A card that failed
  // three days ago read "72h 4m running", and the longer it had been dead the
  // more impressive its number (SC-4151 B3). The clock stays; only the state
  // that earns the word "running" keeps it.
  const curElapsed = stageEnteredAt
    ? `<div class="detail-cost-current">${stageClockLine(currentStage, currentState, agentLiveness, nowMs - Date.parse(stageEnteredAt))}</div>`
    : "";
  // The per-stage rows carry the same honesty as the total: with nothing
  // measured anywhere, a stage's "$0.0000" is the same false claim in smaller
  // type, so the row shows the duration alone.
  const stageRows = c.stages.map((s) =>
    `<div class="detail-cost-stage"><span>${escapeText(s.stage || "—")}</span>` +
    `<span>${allUnmeasured ? "" : escapeText(fmtUSD(s.costUSD)) + " · "}${escapeText(fmtDuration(s.durationMs))}</span></div>`,
  ).join("");
  const split = allUnmeasured
    ? ""
    : `<div class="detail-cost-split">answers ${escapeText(fmtUSD(c.answersCostUSD))} · context ${escapeText(fmtUSD(c.contextCostUSD))}</div>`;
  return `<section class="detail-section detail-cost"><h3 class="detail-section-title">Cost &amp; time</h3>` +
    `<div class="detail-cost-total">${totalLine}</div>` +
    split +
    unmeasuredNote +
    curElapsed +
    `<div class="detail-cost-stages">${stageRows}</div></section>`;
}
