import { test } from "node:test";
import assert from "node:assert/strict";
import { buildDetailSections, buildOptionsSection } from "../build/board-detail.js";

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
