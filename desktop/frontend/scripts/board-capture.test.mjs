// SC-4725: idea capture is the board's primary action and it now looks like
// one. The control the Ideas column renders is deliberately its own class with
// its own accent treatment, not the neutral .add-card circle it used to share
// with the bug and security quick-adds — so what is worth guarding is the pair
// that breaks silently (the class in the markup and the selector that finds
// it), the visible label, and the fact that the two neighbours it was split
// away from did not move.
// The frontend is intentionally dependency-free (no DOM test runner), so this
// asserts the source wiring and the CSS source rules rather than rendering,
// like idea-first-ideation.test.mjs and style.test.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const ts = readFileSync(resolve(here, "..", "src", "board.ts"), "utf8");
const findbugs = readFileSync(resolve(here, "..", "src", "board-findbugs.ts"), "utf8");
const css = readFileSync(resolve(here, "..", "static", "style.css"), "utf8");

// Strip CSS comments so explanatory prose naming a selector never matches.
const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "");

// Strip TS comments so prose explaining why this control is not an .add-card
// never satisfies (or defeats) an assertion about the markup it emits.
function stripTsComments(source) {
  return source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^[ \t]*\/\/.*$/gm, "");
}

function functionBody(source, signature) {
  const start = source.indexOf(signature);
  assert.ok(start >= 0, `${signature} must exist`);
  const fn = source.slice(start);
  return fn.slice(0, fn.indexOf("\n}"));
}

// Extract the body of an exact `selector { ... }` rule, so `.add-card` never
// matches `.add-card:hover` or a longer compound selector.
function exactRuleBody(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp("(?:^|[}\\n,])\\s*" + escaped + "\\s*\\{([^}]*)\\}", "m");
  const m = stripped.match(re);
  return m ? m[1] : "";
}

function pxValue(body, prop) {
  const m = body.match(new RegExp(prop + ":\\s*(\\d+(?:\\.\\d+)?)px"));
  assert.ok(m, `expected a px ${prop} in rule body: ${body}`);
  return Number(m[1]);
}

const src = stripTsComments(ts);
const renderIdeaSpaceBody = functionBody(src, "function renderIdeaSpace(): HTMLElement {");
// SC-4818: idea capture is a centered composer on document.body. The three
// bodies below are the whole surface — the shared dialog shell, the composer
// that opens on it, and the filing that follows a capture.
const buildModalBody = functionBody(
  src,
  "function buildModal(className: string, html: string): { modal: HTMLElement; close: () => void } {",
);
const composerBody = functionBody(src, 'function showIdeaCaptureModal(prefill = ""): void {');
const captureIdeaBody = functionBody(src, "async function captureIdea(title: string): Promise<void> {");

// The class in the markup and the class in the selector are one fact written
// twice: a rename that misses the second throws on the non-null assertion and
// the header dies at render. Assert them together so the pair fails here.
test("the Ideas header renders .capture-idea and wires it to the composer (SC-4725, SC-4818)", () => {
  assert.match(renderIdeaSpaceBody, /class="capture-idea"/, "the header must render the capture-idea button");
  assert.doesNotMatch(
    renderIdeaSpaceBody, /add-card/,
    "the capture control must no longer be an .add-card like the bug and security quick-adds",
  );
  assert.match(
    renderIdeaSpaceBody,
    /querySelector\("\.capture-idea"\)!\.addEventListener\("click", \(\) => showIdeaCaptureModal\(\)\)/,
    "the click must be wired via .capture-idea to the idea composer",
  );
});

// A bare `+` is what users failed to read as "capture an idea", so the label is
// the change, not decoration on it.
test("the capture control carries a visible label, not only a glyph (SC-4725)", () => {
  assert.match(
    renderIdeaSpaceBody, /aria-hidden="true">\+<\/span>Capture an idea/,
    "the button must render the + glyph and the visible text 'Capture an idea'",
  );
});

test(".capture-idea takes the accent fill and outweighs .add-card (SC-4725)", () => {
  const capture = exactRuleBody(".capture-idea");
  assert.match(capture, /background:\s*var\(--accent\)/, ".capture-idea must take the stylesheet's primary fill");
  // The size delta is the acceptance criterion, so assert the relation rather
  // than a literal that any later restyle would have to chase.
  assert.ok(
    pxValue(capture, "height") > pxValue(exactRuleBody(".add-card"), "width"),
    ".capture-idea must be visibly larger than the neutral .add-card circle",
  );
});

// The bug and security quick-adds are explicitly out of scope: they keep the
// neutral circle and become secondary only as a side effect.
test("the bug and security quick-adds are untouched (SC-4725)", () => {
  const addCard = exactRuleBody(".add-card");
  assert.match(addCard, /width:\s*20px/, ".add-card must keep its 20px circle");
  assert.match(addCard, /background:\s*var\(--card-bg\)/, ".add-card must stay neutral, not accent-filled");
  for (const fn of ["bugsHeaderHTML", "securityHeaderHTML"]) {
    const body = functionBody(stripTsComments(findbugs), `export function ${fn}(`);
    assert.match(body, /class="add-card"/, `${fn} must still render the neutral quick-add`);
    assert.doesNotMatch(body, /capture-idea/, `${fn} must not adopt the idea-capture control`);
  }
});

// A new class silently loses the fancy theme's press feedback, which the old
// control had — the acceptance criterion is that the change holds in every
// theme.
test("the fancy press-squish covers the new control (SC-4725)", () => {
  // [^,{]* (not [^{]*) so a selector segment can never itself absorb the
  // comma that separates it from the next one — with [^{]* the split between
  // repetitions of the (?:...)* group and the trailing segment is ambiguous,
  // which is what let this backtrack exponentially on a non-matching input
  // (CodeQL js/redos, SC-4725 deploy fix).
  const squish = stripped.match(/((?:\[data-theme="fancy"\][^,{]*,\s*)*\[data-theme="fancy"\][^,{]*:active\s*)\{\s*transform:\s*scale\(0\.92\)/);
  assert.ok(squish, 'the fancy scale(0.92) :active rule must exist');
  assert.match(squish[1], /\.capture-idea:active/, "the fancy :active squish must list .capture-idea:active");
});

// The bug this ticket fixes is structural, not cosmetic: capture was a child of
// a narrow column, which is what made the typing area one line wide. Assert the
// structure (an overlay on document.body) rather than any pixel value.
test("idea capture is an overlay on document.body, not a column child (SC-4818)", () => {
  assert.doesNotMatch(src, /showIdeaQuickAdd/, "the inline quick-add function must be gone");
  assert.doesNotMatch(src, /idea-quick-add/, "the inline quick-add markup class must be gone");
  assert.match(
    composerBody, /buildModal\("modal idea-modal"/,
    "the composer must be built on the shared .modal-overlay family, not a new one",
  );
});

test("the composer is a multi-line text area (SC-4818)", () => {
  assert.match(composerBody, /class="modal-textarea" rows="6"/, "a title too long for one line must wrap in a textarea");
  assert.match(composerBody, /modal-title">Capture an idea/, "the composer must say what it captures");
});

// Enter is the capture gesture the old quick-add had; a textarea would
// otherwise swallow it as a newline, so the preventDefault is the fix, not
// tidiness.
test("Enter captures and Shift+Enter breaks a line (SC-4818)", () => {
  assert.match(composerBody, /e\.key !== "Enter" \|\| e\.shiftKey/, "Shift+Enter must fall through to the textarea");
  assert.match(composerBody, /e\.preventDefault\(\)/, "a capturing Enter must not also leave a newline behind");
  assert.match(composerBody, /submit\(\)/, "Enter must run the same submit the Capture button runs");
});

test("the composer opens focused (SC-4818)", () => {
  assert.match(composerBody, /input\.focus\(\);/, "the caret must be in the text area on open");
});

// The three ways out live in buildModal exactly once. A copy in the composer
// would be the third paste this extraction exists to prevent.
test("Escape, the scrim and Cancel all close, once, in the shared builder (SC-4818)", () => {
  assert.match(buildModalBody, /e\.key === "Escape"/, "Escape must close the dialog");
  assert.match(buildModalBody, /e\.target === overlay/, "a click on the scrim, not inside the box, must close it");
  assert.match(
    buildModalBody, /querySelector\("\.modal-cancel"\)!\.addEventListener\("click", close\)/,
    "Cancel must close the dialog",
  );
  for (const [re, what] of [
    [/e\.key === "Escape"/, "Escape"],
    [/e\.target === overlay/, "the scrim click"],
    [/\.modal-cancel"\)!\.addEventListener/, "Cancel"],
  ]) {
    assert.doesNotMatch(composerBody, re, `the composer must not re-implement ${what} — buildModal owns it`);
  }
});

test("a second click focuses the open composer instead of stacking one (SC-4818)", () => {
  assert.match(
    composerBody,
    /querySelector<HTMLTextAreaElement>\("\.idea-modal \.modal-textarea"\)[\s\S]*?if \(open\) \{[\s\S]*?open\.focus\(\);\s*return;/,
    "an already-open composer must take the caret back, not be covered by a second overlay",
  );
});

// A typed description would permanently stand the background drafter down
// (VerdictStandDown, ReasonUnknownProvenance), so the wire stays title-only.
test("the wire stays title-only (SC-4818)", () => {
  assert.match(captureIdeaBody, /go\(\)\.CreateIdea\(title\)/, "capture must file through CreateIdea with the title");
  assert.doesNotMatch(src, /CreateIdea\([^)]*,/, "CreateIdea must never be handed a second argument");
});

// The textarea can hold newlines; a tracker title cannot, and nothing
// downstream normalizes one — the placeholder card must carry the same string.
test("a multi-line entry becomes a single-line title (SC-4818)", () => {
  assert.match(composerBody, /replace\(\/\\s\+\/g, " "\)\.trim\(\)/, "whitespace runs must collapse before the title is sent");
});

test("an empty composer captures nothing (SC-4818)", () => {
  assert.match(
    composerBody,
    /if \(!title\) \{[\s\S]*?input\.focus\(\);\s*return;\s*\}[\s\S]*?captureIdea\(title\)/,
    "an empty or all-whitespace composer must keep the caret and file nothing",
  );
});

// SC-1691: the placeholder must never outlive a create that failed, and the
// text must come back with the error rather than disappear with it.
test("a failed create rolls back and reopens the composer with the title (SC-4818, SC-1691)", () => {
  assert.match(captureIdeaBody, /pendingIdeas = dropPending\(pendingIdeas, pending\)/, "the placeholder must be dropped");
  assert.match(captureIdeaBody, /showError\(errMessage\(err\)\)/, "the failure must be shown");
  assert.match(captureIdeaBody, /showIdeaCaptureModal\(title\)/, "the typed title must come back in a fresh composer");
});

test("a successful create invalidates in-flight fetches (SC-4818)", () => {
  assert.match(
    captureIdeaBody, /reconcileEpoch\+\+[\s\S]*?await reconcile\(\)/,
    "a pre-create snapshot must not be allowed to blink the new ticket away",
  );
});

test("the inline quick-add styles are gone (SC-4818)", () => {
  assert.doesNotMatch(stripped, /\.idea-quick-add/, "the removed input must not leave its rules behind");
});

// One overlay family, not two: the composer takes the sibling dialog's field
// rules by joining its selector lists rather than copying the declarations.
test("the composer shares the bug dialog's field rules rather than copying them (SC-4818)", () => {
  assert.match(
    stripped, /\.bug-modal \.modal-textarea,\s*\.idea-modal \.modal-textarea \{/,
    "the textarea rule must name both dialogs instead of being pasted",
  );
  assert.match(exactRuleBody(".idea-modal"), /width:/, ".idea-modal must set its own width");
});
