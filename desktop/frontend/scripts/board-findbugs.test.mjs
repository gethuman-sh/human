import { test } from "node:test";
import assert from "node:assert/strict";
import { bugsHeaderHTML, securityHeaderHTML } from "../build/board-findbugs.js";

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

// The Security header is filed-by-hand only: the neutral quick-add and count
// follow the "Security" title, but there is no Findbugs sweep control (the
// human-security scanner is a separate report, not a board-filing hunt).
test("security header renders the quick-add and count, no Findbugs control", () => {
  const html = securityHeaderHTML(2);
  assert.match(html, /<span>Security<\/span>/);
  assert.match(html, /class="add-card"/);
  assert.match(html, /class="column-count">2</);
  assert.doesNotMatch(html, /class="findbugs-btn"/);
  assert.doesNotMatch(html, /class="findbugs-hunting"/);
});
