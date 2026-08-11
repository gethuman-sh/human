// Regression guard for SC-2858: every fresh ideation session — however the
// panel was opened (today: only the post-import "Create first ticket"
// prompt; SC-4485 removed the Product "+" and sidebar rail "+" button
// triggers, leaving the guided-Q&A flow itself reachable only via
// drag-to-Product/promote-from-Ideas) — must capture an idea first and
// continue in evolve mode, rather than creating a finished ticket outright.
// The frontend is intentionally dependency-free (no DOM test runner), so
// this asserts the source wiring rather than rendering, like
// ideation-done.test.mjs.
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

test("fresh sessions capture an idea before starting the conversation (SC-2858)", () => {
  assert.match(
    sendIdeationReplyBody,
    /const ideaKey = await go\(\)\.CreateIdea\(text\)/,
    "a fresh session must call CreateIdea with the seed text before starting the conversation",
  );
});

test("the captured idea key is passed as StartIdeation's evolveKey (SC-2858)", () => {
  assert.match(
    sendIdeationReplyBody,
    /StartIdeation\(text,\s*ideationMode \?\? "chat",\s*restart,\s*ideaKey,\s*\[\]\)/,
    "StartIdeation must be called in evolve mode against the freshly captured idea",
  );
});

test("no remaining direct-create call passes an empty evolveKey (SC-2858)", () => {
  assert.doesNotMatch(
    sendIdeationReplyBody,
    /StartIdeation\([^)]*,\s*""\s*,\s*\[\]\)/,
    "a future edit must not silently reintroduce the direct-create (empty evolveKey) path",
  );
});

test("promoteIdea (the existing Ideas-column path) is untouched", () => {
  const body = functionBody(ts, "async function promoteIdea");
  assert.match(
    body,
    /StartIdeation\(seed,\s*"guided",\s*true,\s*card\.key,\s*card\.labels\s*\?\?\s*\[\]\)/,
    "the drag-to-promote flow must still call StartIdeation directly with the card's own key/labels",
  );
});

test("CreateIdea failure is caught the same way as a StartIdeation failure", () => {
  const tryStart = sendIdeationReplyBody.indexOf("try {");
  const catchStart = sendIdeationReplyBody.indexOf("} catch (err) {", tryStart);
  assert.ok(tryStart >= 0 && catchStart > tryStart, "sendIdeationReply must have a try/catch block");
  const tryBlock = sendIdeationReplyBody.slice(tryStart, catchStart);
  assert.match(tryBlock, /go\(\)\.CreateIdea\(/, "CreateIdea must be called inside the try block");
  assert.match(tryBlock, /go\(\)\.StartIdeation\(/, "StartIdeation must be called inside the same try block");

  const catchBlock = sendIdeationReplyBody.slice(
    catchStart,
    sendIdeationReplyBody.indexOf("}", sendIdeationReplyBody.indexOf("return;", catchStart)) + 1,
  );
  assert.match(catchBlock, /renderIdeationError\(errMessage\(err\)\)/);
  assert.match(catchBlock, /stopIdeationPoll\(\)/);
  assert.match(catchBlock, /return;/);
});

// SC-4485: the Product-lane and sidebar rail "+" buttons are removed — only
// the Ideas column's own "+" (renderIdeaSpace, untouched) and the post-import
// "Create first ticket" prompt (renderStartWizard, untouched) still open the
// ideation panel.
test("the Product-lane add-card button is gone from renderColumn (SC-4485)", () => {
  assert.doesNotMatch(ts, /New ticket via ideation/, "the Product-lane button title must not remain");
  const renderColumnBody = functionBody(ts, "function renderColumn(queue: string): HTMLElement {");
  assert.doesNotMatch(renderColumnBody, /queue === "product"/, "the product-only branch must be collapsed");
  assert.doesNotMatch(renderColumnBody, /add-card/, "renderColumn must render no add-card button at all");
});

test("wireRail no longer wires a data-action ideation branch (SC-4485)", () => {
  const wireRailBody = functionBody(ts, "function wireRail(): void {");
  assert.doesNotMatch(wireRailBody, /dataset\.action/, "the dead action-item branch must be removed");
  assert.doesNotMatch(wireRailBody, /openIdeation/, "wireRail must no longer call openIdeation directly");
});

test("the Ideas column's own quick-add is unchanged (SC-4485)", () => {
  const renderIdeaSpaceBody = functionBody(ts, "function renderIdeaSpace(): HTMLElement {");
  assert.match(renderIdeaSpaceBody, /Capture an idea/, "the Ideas column's own + must remain");
  assert.match(renderIdeaSpaceBody, /showIdeaQuickAdd\(subcols\[0\]\)/, "its quick-add wiring must remain");
});

test("the post-import prompt still opens ideation directly (SC-4485, out of scope)", () => {
  const wizardBody = functionBody(ts, "function renderStartWizard(): void {");
  assert.match(wizardBody, /void openIdeation\(\);/, "renderStartWizard's Create-first-ticket path must be untouched");
});
