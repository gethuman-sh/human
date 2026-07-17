import { test } from "node:test";
import assert from "node:assert/strict";
import { buildDetailSections } from "../build/board-detail.js";

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
