import { test } from "node:test";
import assert from "node:assert/strict";
import { buildDetailSections, buildOptionsSection, buildStopDecisionSection } from "../build/board-detail.js";

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
