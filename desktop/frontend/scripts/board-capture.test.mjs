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

const renderIdeaSpaceBody = functionBody(stripTsComments(ts), "function renderIdeaSpace(): HTMLElement {");

// The class in the markup and the class in the selector are one fact written
// twice: a rename that misses the second throws on the non-null assertion and
// the header dies at render. Assert them together so the pair fails here.
test("the Ideas header renders .capture-idea and wires it to the quick-add (SC-4725)", () => {
  assert.match(renderIdeaSpaceBody, /class="capture-idea"/, "the header must render the capture-idea button");
  assert.doesNotMatch(
    renderIdeaSpaceBody, /add-card/,
    "the capture control must no longer be an .add-card like the bug and security quick-adds",
  );
  assert.match(
    renderIdeaSpaceBody,
    /querySelector\("\.capture-idea"\)!\.addEventListener\("click", \(\) => showIdeaQuickAdd\(subcols\[0\]\)\)/,
    "the click must be wired via .capture-idea to the leftmost sub-column's quick-add",
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
  const squish = stripped.match(/((?:\[data-theme="fancy"\][^{]*,\s*)*\[data-theme="fancy"\][^{]*:active\s*)\{\s*transform:\s*scale\(0\.92\)/);
  assert.ok(squish, 'the fancy scale(0.92) :active rule must exist');
  assert.match(squish[1], /\.capture-idea:active/, "the fancy :active squish must list .capture-idea:active");
});
