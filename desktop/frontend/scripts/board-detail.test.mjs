import { test } from "node:test";
import assert from "node:assert/strict";
import { buildCostSection, buildDetailSections, buildOptionsSection, buildShippedPartialSection, buildStopDecisionSection } from "../build/board-detail.js";

// SC-365 regression: the board detail panel must render comment-sourced review
// findings, failure reason, and fix summary. buildDetailSections is the pure
// seam that turns those daemon-rendered HTML fields into titled sections. These
// fail before board-detail.ts exists (the import resolves to nothing).

test("all three fields render, failure before review before fix (SC-365)", () => {
  const html = buildDetailSections({
    reviewFindingsHTML: "<p>Nil deref in foo</p>",
    failureReasonHTML: "<p>boom</p>",
    fixSummaryHTML: "<p>fixed it</p>",
  });
  assert.match(html, /Why it failed/);
  assert.match(html, /What the review found/);
  assert.match(html, /Fix summary/);
  // Fixed order: failure, then review, then fix.
  assert.ok(html.indexOf("Why it failed") < html.indexOf("What the review found"));
  assert.ok(html.indexOf("What the review found") < html.indexOf("Fix summary"));
  // The daemon-sanitized HTML is injected verbatim.
  assert.match(html, /Nil deref in foo/);
});

test("absent fields emit nothing (SC-365)", () => {
  assert.equal(buildDetailSections({}), "");
});

test("blank fields emit nothing (SC-365)", () => {
  assert.equal(buildDetailSections({ reviewFindingsHTML: "   " }), "");
});

test("only present field renders, others omitted", () => {
  const html = buildDetailSections({ fixSummaryHTML: "<p>done</p>" });
  assert.match(html, /Fix summary/);
  assert.doesNotMatch(html, /Why it failed/);
  assert.doesNotMatch(html, /What the review found/);
});

// SC-3024: a paused (outage) card's evidence section must say "Why it's
// paused" — never "Why it failed", which reads as a stack trace for a
// substrate that is merely unavailable and will resume on its own.
test("paused card titles its evidence section 'Why it's paused' (SC-3024)", () => {
  const html = buildDetailSections({ failureReasonHTML: "<p>model usage limit</p>", paused: true });
  assert.match(html, /Why it's paused/);
  assert.doesNotMatch(html, /Why it failed/);
});

test("a non-paused failure keeps 'Why it failed' (SC-3024)", () => {
  const html = buildDetailSections({ failureReasonHTML: "<p>boom</p>", paused: false });
  assert.match(html, /Why it failed/);
});

// --- Decision options (ticket 372/534) ---

test("options render as buttons with context and escaped labels", () => {
  const html = buildOptionsSection("review found a design gap", [
    { id: "1", label: "Add a re-run path <b>now</b>" },
    { id: "2", label: "Defer criterion 3" },
  ]);
  assert.match(html, /Decision needed/);
  assert.match(html, /review found a design gap/);
  assert.match(html, /data-option-id="1"/);
  assert.match(html, /data-option-id="2"/);
  // Labels are untrusted comment text — must arrive escaped, never as markup.
  assert.match(html, /&lt;b&gt;now&lt;\/b&gt;/);
  assert.doesNotMatch(html, /<b>now<\/b>/);
});

test("no options emit nothing", () => {
  assert.equal(buildOptionsSection("context alone", []), "");
  assert.equal(buildOptionsSection(undefined, undefined), "");
});

// --- Pre-planning stop decision (SC-2699) ---

test("stop decision renders heading, linked key button, and reasoning", () => {
  const html = buildStopDecisionSection("superseded", "SC-100", "Same surface as SC-100");
  assert.match(html, /Duplicate of another ticket/);
  assert.match(html, /data-linked-key="SC-100"/);
  assert.match(html, /SC-100/);
  assert.match(html, /Same surface as SC-100/);
});

test("escalated stop decision uses the design-decision heading", () => {
  const html = buildStopDecisionSection("escalated", "SC-200", "why");
  assert.match(html, /Blocked on a design decision/);
  assert.match(html, /data-linked-key="SC-200"/);
});

test("rejected stop decision has no linked button", () => {
  const html = buildStopDecisionSection("rejected", undefined, "not a bug because it works as designed");
  assert.match(html, /Not a real problem/);
  assert.doesNotMatch(html, /detail-linked-btn/);
  assert.match(html, /not a bug because/);
});

test("empty stop decision emits nothing", () => {
  assert.equal(buildStopDecisionSection("", undefined, undefined), "");
});

test("multi-line stop reasoning preserves line breaks", () => {
  const html = buildStopDecisionSection("rejected", undefined, "line1\nline2");
  assert.match(html, /line1<br>line2/);
});

// --- Shipped-partial trace (SC-2910) ---

test("shipped-partial section renders with follow-on button", () => {
  const html = buildShippedPartialSection(true, "SC-3001");
  assert.match(html, /detail-partial/);
  assert.match(html, /Shipped partial/);
  assert.match(html, /data-linked-key="SC-3001"/);
  assert.match(html, /detail-linked-btn/);
});

test("shipped-partial section empty when not partial", () => {
  assert.equal(buildShippedPartialSection(false, undefined), "");
});

test("shipped-partial section without follow-on has no linked button", () => {
  const html = buildShippedPartialSection(true, undefined);
  assert.notEqual(html, "");
  assert.doesNotMatch(html, /detail-linked-btn/);
});

// --- Per-ticket cost & time (SC-2847) ---

test("no spend says so plainly, never $0.00 (SC-2847 criterion 5)", () => {
  const html = buildCostSection(null, undefined, undefined, Date.now());
  assert.match(html, /No spend recorded/);
  assert.doesNotMatch(html, /\$0\.00/);
});

test("empty hasSpend=false rollup also renders the plain empty state", () => {
  const html = buildCostSection(
    { ticket: "SC-1", hasSpend: false, totalCostUSD: 0, contextCostUSD: 0, answersCostUSD: 0, totalDurationMs: 0, stages: [] },
    "implementation",
    undefined,
    Date.now(),
  );
  assert.match(html, /No spend recorded/);
});

test("populated rollup shows total, split, per-stage rows, and live current-stage clock", () => {
  const now = Date.now();
  const enteredAt = new Date(now - 90_000).toISOString(); // 90s ago
  const html = buildCostSection(
    {
      ticket: "SC-1",
      hasSpend: true,
      totalCostUSD: 1.23,
      contextCostUSD: 0.8,
      answersCostUSD: 0.43,
      totalDurationMs: 5000,
      stages: [
        { stage: "implementation", costUSD: 1.0, contextCostUSD: 0.7, answersCostUSD: 0.3, durationMs: 3000 },
        { stage: "planning", costUSD: 0.23, contextCostUSD: 0.1, answersCostUSD: 0.13, durationMs: 2000 },
      ],
    },
    "implementation",
    enteredAt,
    now,
  );
  assert.match(html, /\$1\.23/); // total in dollars (>= $1 => two decimals)
  assert.match(html, /answers/);
  assert.match(html, /context/);
  assert.match(html, /implementation/);
  assert.match(html, /planning/);
  assert.match(html, /Current stage \(implementation\): 1m 30s running/);
});

test("sub-dollar figures keep four decimals so a few cents never rounds to $0.00", () => {
  const html = buildCostSection(
    { ticket: "SC-1", hasSpend: true, totalCostUSD: 0.0123, contextCostUSD: 0.01, answersCostUSD: 0.0023, totalDurationMs: 1000, stages: [] },
    "planning",
    undefined,
    Date.now(),
  );
  assert.match(html, /\$0\.0123/);
});

test("no stageEnteredAt omits the live current-stage clock", () => {
  const html = buildCostSection(
    { ticket: "SC-1", hasSpend: true, totalCostUSD: 1.0, contextCostUSD: 0.5, answersCostUSD: 0.5, totalDurationMs: 1000, stages: [] },
    "planning",
    undefined,
    Date.now(),
  );
  assert.doesNotMatch(html, /Current stage/);
});

// --- Cost the ledger could not measure or could not read (SC-4151 C7/C8) ---

test("a roll-up whose every call recorded no tokens says the cost is unknown, not $0.0000", () => {
  // The shape of SC-1542 (85 calls, 8m40s) and SC-3339 (222 calls, 80m37s): real
  // duration, real calls, no token counts on any of them, so nothing to price.
  const html = buildCostSection(
    {
      ticket: "SC-3339",
      ledgerRead: true,
      hasSpend: true,
      totalCostUSD: 0,
      contextCostUSD: 0,
      answersCostUSD: 0,
      totalDurationMs: 4_836_948,
      calls: 222,
      unmeasuredCalls: 222,
      stages: [{ stage: "implementation", costUSD: 0, contextCostUSD: 0, answersCostUSD: 0, durationMs: 4_836_948 }],
    },
    "implementation",
    undefined,
    Date.now(),
  );
  assert.match(html, /cost not measured/);
  assert.match(html, /222 calls recorded no tokens/);
  assert.doesNotMatch(html, /\$0\.0000/);
  // The duration is real and still shown — only the price is unknown.
  assert.match(html, /1h 20m/);
});

test("a partial measurement gap keeps the figure and qualifies it", () => {
  const html = buildCostSection(
    {
      ticket: "SC-1",
      ledgerRead: true,
      hasSpend: true,
      totalCostUSD: 1.23,
      contextCostUSD: 0.8,
      answersCostUSD: 0.43,
      totalDurationMs: 5000,
      calls: 10,
      unmeasuredCalls: 4,
      stages: [],
    },
    "implementation",
    undefined,
    Date.now(),
  );
  assert.match(html, /\$1\.23/);
  assert.match(html, /4 of 10 calls recorded no tokens/);
});

test("a ledger that could not be read says so, instead of claiming the ticket is unspent", () => {
  const html = buildCostSection(
    { ticket: "SC-1", ledgerRead: false, hasSpend: false, totalCostUSD: 0, contextCostUSD: 0, answersCostUSD: 0, totalDurationMs: 0, stages: [] },
    "implementation",
    undefined,
    Date.now(),
  );
  assert.match(html, /ledger was not available/);
  assert.doesNotMatch(html, /No spend recorded/);
});

test("a payload from a daemon predating ledgerRead still reads as no spend", () => {
  const html = buildCostSection(
    { ticket: "SC-1", hasSpend: false, totalCostUSD: 0, contextCostUSD: 0, answersCostUSD: 0, totalDurationMs: 0, stages: [] },
    "implementation",
    undefined,
    Date.now(),
  );
  assert.match(html, /No spend recorded/);
});

test("a fully measured roll-up is unchanged — no note, full split", () => {
  const html = buildCostSection(
    {
      ticket: "SC-1",
      ledgerRead: true,
      hasSpend: true,
      totalCostUSD: 2.5,
      contextCostUSD: 1.5,
      answersCostUSD: 1.0,
      totalDurationMs: 1000,
      calls: 7,
      unmeasuredCalls: 0,
      stages: [],
    },
    "planning",
    undefined,
    Date.now(),
  );
  assert.match(html, /\$2\.50/);
  assert.match(html, /answers/);
  assert.doesNotMatch(html, /recorded no tokens/);
});
