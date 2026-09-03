// Regression guard for SC-2858, now kept by construction (SC-4608): no board
// surface starts an ideation session at all, so none can create a finished
// ticket outright. Capture is the only way in — the Ideas column's `+` and the
// post-import "Create first ticket" prompt both open the same quick-add — and
// what a captured idea becomes is decided by the background drafter and the
// description editor at promotion.
// The frontend is intentionally dependency-free (no DOM test runner), so this
// asserts the source wiring rather than rendering, like ideation-done.test.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const ts = readFileSync(resolve(here, "..", "src", "board.ts"), "utf8");

function functionBody(source, signature) {
  const start = source.indexOf(signature);
  assert.ok(start >= 0, `${signature} must exist`);
  const fn = source.slice(start);
  return fn.slice(0, fn.indexOf("\n}"));
}

const sendIdeationReplyBody = functionBody(ts, "async function sendIdeationReply");

test("no board surface starts an ideation session (SC-2858, SC-4608)", () => {
  assert.ok(!ts.includes("StartIdeation"), "board.ts must not call or declare the StartIdeation binding");
  assert.doesNotMatch(ts, /openIdeation\(/, "nothing may open the ideation panel");
  assert.doesNotMatch(ts, /function openIdeation/, "and the opener itself is gone");
});

test("sendIdeationReply only replies into a session that already exists (SC-2858)", () => {
  assert.match(
    sendIdeationReplyBody,
    /const sessionId = ideation\.sessionId;\n  if \(!text \|\| !sessionId\) return;/,
    "a reply with no live session must be refused, not turned into a fresh one",
  );
  assert.match(sendIdeationReplyBody, /go\(\)\.ReplyIdeation\(sessionId, text\)/);
  assert.doesNotMatch(sendIdeationReplyBody, /CreateIdea/, "the fresh-session branch is gone");
});

// The post-import prompt is the flow criterion 18b's retirement had to rehome:
// it used to capture an idea and then evolve it in a chat. It now captures an
// idea and stops — the drafter writes the description, promotion opens the
// editor.
test("the post-import prompt captures an idea (SC-4608)", () => {
  const wizardBody = functionBody(ts, "function renderStartWizard(): void {");
  assert.match(wizardBody, /captureFirstIdea\(\);/, "Create-first-ticket must go through idea capture");
  assert.doesNotMatch(wizardBody, /Ideation/, "and must not reach the ideation panel");

  const captureBody = functionBody(ts, "function captureFirstIdea(): void {");
  assert.match(captureBody, /querySelector<HTMLElement>\("\.idea-subcol"\)/);
  assert.match(captureBody, /showIdeaQuickAdd\(col\)/, "it opens the Ideas column's own quick-add");
});

// SC-4608: promotion is a label edit plus the description editor, never an
// agent turn. The drop branch and the function it calls are asserted together —
// a promotion that removed the labels and opened nothing would leave the user
// staring at a card that silently changed columns.
test("dropping an idea on Product Backlog promotes it and opens the editor (SC-4608)", () => {
  const performDropBody = functionBody(ts, "function performDrop(");
  assert.match(
    performDropBody,
    /toQueue === "product" && info\.stage === "ideas"/,
    "the ideas->product drop branch must still exist",
  );
  assert.match(performDropBody, /promoteIdeaToBacklog\(info\.key\)/, "the drop must go through the promotion path");

  const promoteBody = functionBody(ts, "async function promoteIdeaToBacklog");
  assert.match(promoteBody, /go\(\)\.PromoteIdea\(/, "promotion removes the idea labels through the daemon");
  assert.match(
    promoteBody,
    /openDescEditModal\(card,\s*\{\s*promoted:\s*true\s*\}\)/,
    "and then opens the description editor with its remit widened",
  );
});

// The guided interview, its approval park, the old promote path and evolve mode
// are retired (SC-4608). A leftover comment is as much a defect as leftover code
// here: it describes a UI that no longer exists.
test("guided mode, the approval park and evolve mode are gone from board.ts (SC-4608)", () => {
  for (const token of [
    "async function promoteIdea(",
    "ApproveIdeation",
    "approveIdeation",
    "awaiting_approval",
    "renderIdeationOptions",
    "renderIdeationDraft",
    "renderModePicker",
    "ideationMode",
    "interface IdeationQuestion",
    "interface IdeationDraft",
    "ideation-mode-btn",
    '"guided"',
    "evolveKey",
    "evolveLabels",
  ]) {
    assert.ok(!ts.includes(token), `board.ts must no longer contain ${token}`);
  }
});

test("the retired ideation markup is gone from index.html (SC-4608)", () => {
  const html = readFileSync(resolve(here, "..", "static", "index.html"), "utf8");
  for (const id of ["ideation-mode-picker", "ideation-options", "ideation-draft"]) {
    assert.ok(!html.includes(id), `index.html must no longer contain #${id}`);
  }
});

// SC-4485: the Product-lane and sidebar rail "+" buttons are removed. With the
// post-import prompt rehomed onto capture (SC-4608), the Ideas column's own
// capture button is the only button left that creates anything.
test("the Product-lane add-card button is gone from renderColumn (SC-4485)", () => {
  assert.doesNotMatch(ts, /New ticket via ideation/, "the Product-lane button title must not remain");
  const renderColumnBody = functionBody(ts, "function renderColumn(queue: string): HTMLElement {");
  assert.doesNotMatch(renderColumnBody, /queue === "product"/, "the product-only branch must be collapsed");
  assert.doesNotMatch(renderColumnBody, /add-card/, "renderColumn must render no add-card button at all");
});

test("wireRail no longer wires a data-action ideation branch (SC-4485)", () => {
  const wireRailBody = functionBody(ts, "function wireRail(): void {");
  assert.doesNotMatch(wireRailBody, /dataset\.action/, "the dead action-item branch must be removed");
});

// The styling of this control changed in SC-4725; its wiring and its label did
// not. That is what this guards — the surviving entry point still reaches the
// same quick-add and still says what it does.
test("the Ideas column's capture button still wires the quick-add and keeps its label (SC-4485, SC-4725)", () => {
  const renderIdeaSpaceBody = functionBody(ts, "function renderIdeaSpace(): HTMLElement {");
  assert.match(renderIdeaSpaceBody, /Capture an idea/, "the Ideas column's capture control must remain");
  assert.match(renderIdeaSpaceBody, /showIdeaQuickAdd\(subcols\[0\]\)/, "its quick-add wiring must remain");
});
