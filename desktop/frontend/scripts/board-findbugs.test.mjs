import { test } from "node:test";
import assert from "node:assert/strict";
import { bugsHeaderHTML, securityHeaderHTML, gardeningHeaderHTML } from "../build/board-findbugs.js";

// 1087: when no sweep is running the header offers the Findbugs button (and the
// neutral quick-add + count still follow), never the hunting spinner.
test("idle header renders the Findbugs button, no hunting spinner", () => {
  const html = bugsHeaderHTML(false, 3);
  assert.match(html, /class="findbugs-btn"/);
  assert.doesNotMatch(html, /class="findbugs-hunting"/);
  assert.match(html, /class="add-card"/);
  assert.match(html, /class="column-count">3</);
});

// 1087: while a sweep runs the button is replaced by a spinner + "hunting…"
// label so the pane shows the hunt is in progress.
test("hunting header renders the spinner label, no Findbugs button", () => {
  const html = bugsHeaderHTML(true, 0);
  assert.match(html, /class="findbugs-hunting"/);
  assert.match(html, /class="spinner"/);
  assert.match(html, /hunting…/);
  assert.doesNotMatch(html, /class="findbugs-btn"/);
  assert.match(html, /class="column-count">0</);
});

// The Security header mirrors the Bugs header: an idle Find Security button that
// launches the human-security scan, swapped for a scanning spinner while a sweep
// runs, with the neutral quick-add and count always following the title.
test("idle security header renders the Find Security button, no spinner", () => {
  const html = securityHeaderHTML(false, 2);
  assert.match(html, /<span>Security<\/span>/);
  assert.match(html, /class="findsecurity-btn"/);
  assert.doesNotMatch(html, /class="findsecurity-hunting"/);
  assert.match(html, /class="add-card"/);
  assert.match(html, /class="column-count">2</);
});

test("scanning security header renders the spinner label, no button", () => {
  const html = securityHeaderHTML(true, 0);
  assert.match(html, /class="findsecurity-hunting"/);
  assert.match(html, /class="spinner"/);
  assert.match(html, /scanning…/);
  assert.doesNotMatch(html, /class="findsecurity-btn"/);
  assert.match(html, /class="column-count">0</);
});

// SC-1638: the Gardening header is intentionally inert — it shows only the
// title and a count, never a sweep control or a quick-add button.
test("gardening header renders title and count, no sweep or quick-add", () => {
  const html = gardeningHeaderHTML(0);
  assert.match(html, /<span>Gardening<\/span>/);
  assert.match(html, /class="column-count">0</);
  assert.doesNotMatch(html, /class="add-card"/);
  assert.doesNotMatch(html, /findbugs-btn/);
  assert.doesNotMatch(html, /findsecurity-btn/);
});
