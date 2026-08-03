import { test } from "node:test";
import assert from "node:assert/strict";
import { buildCostSection, buildDetailSections, buildOptionsSection, buildStopDecisionSection } from "../build/board-detail.js";

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
