// SC-5033: the board's dialogs are one shell and one button vocabulary.
//
// Two things are guarded here. The pure helpers in board-modal.ts are tested
// directly. Everything else is a property of source text — that the scrim,
// Escape, Cancel and the corner × exist in buildModal and nowhere else, and
// that the constructive confirm is stated once in CSS rather than once per
// dialog — because the frontend is deliberately dependency-free with no DOM
// runner (see board-capture.test.mjs).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { confirmButtonClass, isTopmostOverlay } from "../build/board-modal.js";

const here = dirname(fileURLToPath(import.meta.url));
const ts = readFileSync(resolve(here, "..", "src", "board.ts"), "utf8");
const css = readFileSync(resolve(here, "..", "static", "style.css"), "utf8");

// Strip comments so prose naming a selector or a retired pattern can never
// satisfy (or defeat) an assertion about the code that ships.
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

// Extract the body of an exact `selector { ... }` rule, so `.modal-confirm`
// never matches `.modal-confirm:hover` or a longer compound selector.
function exactRuleBody(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp("(?:^|[}\\n,])\\s*" + escaped + "\\s*\\{([^}]*)\\}", "m");
  const m = stripped.match(re);
  return m ? m[1] : "";
}

const buildModalBody = functionBody(
  src,
  "function buildModal(\n  className: string,\n  html: string,\n  opts: ModalOptions = {},\n): { modal: HTMLElement; close: () => void; setContent: (html: string) => void } {",
);

// --- the pure helpers ------------------------------------------------------

test("a confirm is constructive unless it says otherwise", () => {
  assert.equal(confirmButtonClass(), "modal-confirm");
  assert.equal(confirmButtonClass("constructive"), "modal-confirm");
});

test("a destructive confirm names itself", () => {
  assert.equal(confirmButtonClass("destructive"), "modal-confirm modal-destructive");
});

// Dialogs stack — the description editor's discard confirmation sits over the
// editor — and Escape is bound on document, so without this one Escape would
// dismiss both.
test("only the topmost overlay answers Escape", () => {
  assert.equal(isTopmostOverlay(["a", "b"], "b"), true);
  assert.equal(isTopmostOverlay(["a", "b"], "a"), false);
  assert.equal(isTopmostOverlay([], "a"), false);
});

// --- the shell -------------------------------------------------------------

test("the shell tolerates a dialog with no Cancel", () => {
  assert.match(
    buildModalBody, /querySelector\("\.modal-cancel"\)\?\.addEventListener/,
    "the launch notice has one button; a non-null assertion would throw on it",
  );
});

test("the shell owns the corner ×", () => {
  assert.match(buildModalBody, /MODAL_CLOSE_HTML/, "the × markup must come from the shell");
  assert.match(
    buildModalBody, /querySelector\("\.modal-close"\)\?\.addEventListener\("click", dismiss\)/,
    "the × must do exactly what Escape and the scrim do",
  );
  assert.match(src, /class="modal-close"/, "the shared × markup must exist");
});

test("Escape is document-bound and topmost-guarded", () => {
  assert.match(
    buildModalBody, /document\.addEventListener\("keydown"/,
    "the wizard's first screens focus nothing, so a modal-scoped listener would never fire",
  );
  assert.match(buildModalBody, /isTopmostOverlay\(/, "a stacked dialog must not dismiss its parent too");
});

test("a dialog may name what dismissal means", () => {
  assert.match(buildModalBody, /opts\.onDismiss \?\? close/, "dismissal defaults to close and is overridable");
});

test("the shell runs close-time cleanup exactly once", () => {
  assert.match(buildModalBody, /if \(closed\) return;/, "close must be idempotent — the listener unbind depends on it");
  assert.match(buildModalBody, /opts\.onClose\?\.\(\)/, "the description editor ends its chat session here");
});

// --- the dialogs that used to build their own shell ------------------------

test("the generic confirm is on the shell", () => {
  const body = functionBody(src, "function confirmDialog(");
  assert.match(body, /buildModal\(/, "confirmDialog must use the shared shell");
  assert.doesNotMatch(body, /document\.createElement\("div"\)/, "no second overlay may be hand-built");
  assert.doesNotMatch(body, /modal-overlay/, "the scrim comes from the shell");
});

test("the busy-close × means Cancel", () => {
  const body = functionBody(src, "function busyCloseDialog(");
  assert.match(body, /buildModal\(/, "busyCloseDialog must use the shared shell");
  assert.doesNotMatch(body, /modal-overlay/, "the scrim comes from the shell");
  assert.match(
    body, /onClose: \(\) => resolve\(settled \? choice : "cancel"\)/,
    "a dismissal must resolve cancel — the only choice that changes nothing while work is in flight",
  );
});

test("the launch notice is on the shell and not red", () => {
  const body = functionBody(src, "function noticeDialog(");
  assert.match(body, /buildModal\(/, "noticeDialog must use the shared shell");
  assert.doesNotMatch(body, /modal-overlay/, "the scrim comes from the shell");
  assert.doesNotMatch(body, /modal-destructive/, "acknowledging a notice destroys nothing");
});

test("the wizard is on the shell and refuses the × while creating", () => {
  const body = functionBody(src, "function openStartWizard(): void {");
  assert.match(body, /buildModal\("modal wizard"/, "the wizard must use the shared shell");
  assert.match(
    body, /canDismiss: \(\) => wizardStep !== "creating"/,
    "an in-flight scaffold is not cancellable from here, so it gets no × either",
  );
  assert.match(
    body, /onClose: \(\) => \{\s*wizardModal = null;/,
    "every way out must drop the handles, or the re-entry guard blocks every reopen",
  );
  assert.doesNotMatch(src, /_onKey/, "the hand-stashed keydown handler must be gone");
});

test("the wizard re-renders through the shell", () => {
  const body = functionBody(src, "function renderStartWizard(): void {");
  assert.match(body, /wizardSetContent\(/, "each step must re-render through the shell");
  assert.doesNotMatch(body, /modal\.innerHTML =/, "a direct innerHTML write loses the ×");
  assert.doesNotMatch(
    body, /modal-cancel"\)!\.addEventListener/,
    "setContent wires .modal-cancel on every render — a per-step wiring binds it twice",
  );
});

// --- the button vocabulary in CSS -----------------------------------------

test("the shared confirm is constructive and destructive is the opt-in", () => {
  assert.match(exactRuleBody(".modal-confirm"), /background:\s*var\(--accent\)/);
  assert.match(exactRuleBody(".modal-confirm.modal-destructive"), /background:\s*var\(--failed\)/);
});

test("no dialog re-states the constructive confirm", () => {
  for (const selector of [
    ".bug-modal .modal-confirm",
    ".idea-modal .modal-confirm",
    ".wizard .modal-confirm",
    ".descedit-apply",
  ]) {
    assert.ok(
      !stripped.includes(`${selector} {`),
      `${selector} must be gone — a fifth dialog would otherwise need a fifth override`,
    );
  }
});

test("the shared button treatment is on the classes, not the container", () => {
  assert.ok(
    !stripped.includes(".modal-actions button {"),
    "the send button lives in the chat form, not in .modal-actions",
  );
  const shared = exactRuleBody(".modal-cancel,\n.modal-confirm,\n.modal-secondary");
  assert.match(shared, /border-radius:/, "the shared rule must set the radius");
  assert.match(shared, /font-weight:/, "the shared rule must set the weight");
});

test("the three destructive confirms are explicit", () => {
  assert.match(
    functionBody(src, "function busyCloseDialog("), /modal-confirm modal-destructive/,
    '"Stop anyway" must stay red',
  );
  const requestClose = functionBody(src, "async function requestClose(");
  assert.match(requestClose, /confirmDialog\(`Close ticket \$\{key\}\?`, body, "Close ticket"\)/);
  assert.doesNotMatch(requestClose, /constructive/, "close-ticket keeps the destructive default");
  assert.doesNotMatch(
    functionBody(src, "async function offerOrphanCleanup("), /constructive/,
    '"Stop it" keeps the destructive default',
  );
});

test("recreate-description is constructive", () => {
  assert.match(
    functionBody(src, "async function recreateDescription("), /"constructive"/,
    "recreating a description creates, it does not destroy",
  );
});
