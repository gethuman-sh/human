// SC-5033: the description editor's chat box takes more than one line, says
// which gesture sends, stays usable while a turn is in flight, and closes when
// Apply lands. The frontend has no DOM runner (see board-capture.test.mjs), so
// these assert the source wiring and the CSS source rules; the pure halves are
// unit-tested in board-descedit.test.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const ts = readFileSync(resolve(here, "..", "src", "board.ts"), "utf8");
const css = readFileSync(resolve(here, "..", "static", "style.css"), "utf8");

const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "");
function stripTsComments(source) {
  return source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^[ \t]*\/\/.*$/gm, "");
}
const src = stripTsComments(ts);

function functionBody(source, signature) {
  const start = source.indexOf(signature);
  assert.ok(start >= 0, `${signature} must exist`);
  const fn = source.slice(start);
  return fn.slice(0, fn.indexOf("\n}"));
}

function exactRuleBody(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp("(?:^|[}\\n,])\\s*" + escaped + "\\s*\\{([^}]*)\\}", "m");
  const m = stripped.match(re);
  return m ? m[1] : "";
}

const openBody = functionBody(src, "async function openDescEditModal(");
const renderBody = functionBody(src, "function renderDescEdit(): void {");
const submitBody = functionBody(src, "function submitDescEditInput(): void {");
const flushBody = functionBody(src, "function flushDescEditQueue(): void {");
const applyBody = functionBody(src, "async function applyDescEdit(): Promise<void> {");

// --- the input -------------------------------------------------------------

test("the chat input is a textarea that starts at one line", () => {
  assert.match(
    openBody, /<textarea id="descedit-input" rows="1"/,
    "one line to start; the growth is what shows the box takes more",
  );
});

test("Enter sends and Shift+Enter starts a new line", () => {
  assert.match(openBody, /e\.key !== "Enter" \|\| e\.shiftKey/, "Shift+Enter must fall through to the textarea");
  assert.match(openBody, /e\.preventDefault\(\);/, "a sending Enter must not also leave a newline behind");
  assert.match(openBody, /submitDescEditInput\(\);/, "Enter must run the same path the send button runs");
  assert.match(openBody, /e\.isComposing/, "an IME commit is not a send");
});

test("the gesture hint is permanent and in the composer's treatment", () => {
  assert.match(
    openBody, /class="chat-hint modal-body">Enter sends · Shift\+Enter starts a new line</,
    "the hint prevents a silent mistake, so it is not revealed on focus or hidden after first use",
  );
});

test("the placeholder still says what the dialog is for", () => {
  assert.match(openBody, /placeholder="How should this description change\?"/);
  assert.doesNotMatch(
    openBody, /placeholder="[^"]*Shift\+Enter/,
    "the gesture is carried by the hint line and the tooltip, not by a lengthened placeholder",
  );
});

test("the send button is an icon with an accessible name and a tooltip", () => {
  assert.match(openBody, /class="modal-confirm chat-send"/, "it takes colour, radius and weight from the shared rule");
  assert.match(openBody, /aria-label="Send/, "the only control with no visible text must still have a name");
  assert.match(openBody, /title="Enter sends · Shift\+Enter starts a new line"/);
  assert.match(openBody, /↵/, "the glyph names the gesture the hint describes");
});

// A disabled control cannot take focus, and the session does not exist when
// the dialog opens — so focusing on open alone would silently do nothing.
test("the editor opens focused once the input is live", () => {
  assert.match(openBody, /descEditPendingFocus = true;/, "the focus is owed at open");
  assert.doesNotMatch(openBody, /input\.focus\(\)/, "focusing a disabled input is a no-op");
  assert.match(
    renderBody, /if \(input && inputEnabled && descEditPendingFocus\) \{/,
    "focus lands on the render that first makes the box usable",
  );
});

test("newlines are not flattened on send", () => {
  assert.match(submitBody, /input\.value\.trim\(\)/, "only the ends are trimmed");
  assert.doesNotMatch(
    submitBody, /replace\(\/\\s\+\/g/,
    "idea capture flattens because its wire is a single-line title; a chat instruction's breaks are the user's",
  );
});

test("the transcript keeps the line breaks a multi-line instruction was sent with", () => {
  assert.match(exactRuleBody(".msg"), /white-space:\s*pre-wrap/);
});

// --- the held message ------------------------------------------------------

test("a message typed mid-turn is held, not sent", () => {
  assert.match(submitBody, /descEditSendDefers\(/, "the daemon refuses a reply to a session not awaiting one");
  assert.match(submitBody, /descEditQueued = descEditQueued \?/, "the held text accumulates rather than replacing");
});

test("a held message goes out when the turn lands, and comes back when it fails", () => {
  assert.match(flushBody, /descEdit\.state === "awaiting_reply"/, "a landed turn carries the held message");
  assert.match(flushBody, /sendDescEditMessage\(text\)/);
  assert.match(
    flushBody, /descEdit\.state === "error"/,
    "a failed turn puts the text back in the box rather than into a session that cannot accept it",
  );
});

test("the queue is flushed on success and on failure", () => {
  const pollBody = functionBody(src, "async function pollDescEdit(): Promise<void> {");
  const sendBody = functionBody(src, "async function sendDescEditMessage(text: string): Promise<void> {");
  assert.ok(
    (pollBody.match(/flushDescEditQueue\(\);/g) ?? []).length >= 2,
    "the poll must flush on both its success and its error path — a failed turn must not drop held text",
  );
  assert.ok(
    (sendBody.match(/flushDescEditQueue\(\);/g) ?? []).length >= 1,
    "a send that throws must not drop held text either",
  );
});

// --- growth ----------------------------------------------------------------

test("the input grows and scrolls at the ceiling", () => {
  const rule = exactRuleBody(".chat-form textarea");
  assert.match(rule, /max-height:/, "six lines is a ceiling, not a starting size");
  assert.match(rule, /overflow-y:\s*auto/, "past the ceiling it scrolls");
  assert.match(rule, /resize:\s*none/, "the box sizes itself to the text");
});

test("the growth ceiling is read from CSS, not duplicated", () => {
  const grow = functionBody(src, "function autoGrowChatInput(el: HTMLTextAreaElement): void {");
  assert.match(grow, /getComputedStyle\(el\)/);
  assert.match(grow, /\.maxHeight/, "CSS is the single source of the ceiling");
  assert.match(grow, /chatInputHeight\(/);
  assert.match(
    grow, /borderTopWidth/,
    "scrollHeight excludes the border and everything here is border-box, so it must be added back",
  );
  assert.match(grow, /borderBottomWidth/);
});

test("the CSS ceiling and the JS fallback agree", () => {
  assert.match(exactRuleBody(".chat-form textarea"), /max-height:\s*124px/);
  assert.match(src, /const CHAT_INPUT_MAX_PX = 124;/);
});

test("the chat form carries no copy of the shared button rule", () => {
  assert.ok(!stripped.includes(".chat-form button {"), "the fourth copy of the shared rule must be gone");
  const send = exactRuleBody(".chat-send");
  assert.doesNotMatch(send, /background:/, "colour comes from .modal-confirm");
  assert.doesNotMatch(send, /border-radius:/, "radius comes from the shared rule");
  assert.doesNotMatch(send, /font-weight:/, "weight comes from the shared rule");
});

// --- leaving and applying --------------------------------------------------

test("the description editor is built on the shared shell", () => {
  assert.match(openBody, /buildModal\("modal descedit-modal"/);
  for (const [re, what] of [
    [/modal-overlay/, "the scrim"],
    [/e\.key === "Escape"/, "Escape"],
    [/e\.target === overlay/, "the scrim click"],
    [/descedit-close/, "a private corner ×"],
  ]) {
    assert.doesNotMatch(openBody, re, `${what} comes from the shell, not from this dialog`);
  }
});

test("every way out goes through the same question", () => {
  assert.match(openBody, /onDismiss: \(\) => requestDescEditClose\(\)/, "Escape, the scrim and the × ask too");
  const request = functionBody(src, "function requestDescEditClose(): void {");
  assert.match(request, /descEditHasUnappliedRewrite\(/, "nothing unapplied closes straight away");
  assert.match(request, /confirmDialog\(/, "an unapplied rewrite is worth a question");
  assert.match(request, /if \(ok\) closeDescEditModal\(\);/, "declining returns to the dialog, intact");
});

test("closing still ends the daemon-side chat session", () => {
  assert.match(openBody, /onClose: \(\) => teardownDescEdit\(\)/, "however the dialog goes away");
  const teardown = functionBody(src, "function teardownDescEdit(): void {");
  assert.match(teardown, /discardDescEditSession\(/, "SC-2873 AC6");
  assert.match(teardown, /stopDescEditPoll\(\);/);
});

test("Apply closes the dialog", () => {
  assert.match(applyBody, /closeDescEditModal\(\);/, "the main button closes its dialog, as every other one does");
  assert.doesNotMatch(
    applyBody, /renderDescEdit\(\);/,
    "re-rendering a terminal session was the state in which nothing in the dialog could be used",
  );
});

test("the footer is the shared actions row, named for what it does to the work", () => {
  assert.match(openBody, /class="modal-actions descedit-actions"/);
  assert.match(openBody, />Cancel</, "the button is named for the work, not for the window");
  assert.doesNotMatch(openBody, />Close</);
  assert.doesNotMatch(
    openBody, /class="descedit-apply/,
    "the private accent override is gone — the id stays as the render hook",
  );
});
