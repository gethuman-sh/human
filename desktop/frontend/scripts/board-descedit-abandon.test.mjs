// SC-2873 AC6, the in-flight half.
//
// board-descedit.test.mjs proves the predicate (descEditShouldDiscardOnClose),
// and the close handler calls it — but the leak this covers happens where no
// close handler can see the session at all. Closing the modal while the opening
// StartDescEdit is still awaiting leaves the daemon holding a session whose id
// the UI only learns after the modal is gone, so "closing discards" silently
// stops being true inside that window. The property is therefore asserted on
// the source: every awaited StartDescEdit must be followed by an ownership
// re-check that discards, and must not assign straight into the shared
// `descEdit` state a closed modal no longer owns.
//
// Source-level rather than behavioural for the reason board-error-guard.test.mjs
// gives: the frontend is intentionally dependency-free, with no DOM test runner.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

// Both src/ and the committed dist/ transpile are checked: dist/ is embedded
// via `//go:embed all:frontend/dist`, so a dist/ lagging a fixed src/ would
// still ship the leak to users.
const here = dirname(fileURLToPath(import.meta.url));
const raw = [
  ["src/board.ts", readFileSync(resolve(here, "..", "src", "board.ts"), "utf8")],
  ["dist/board.js", readFileSync(resolve(here, "..", "dist", "board.js"), "utf8")],
];

// Blank comments to spaces (preserving offsets and newlines) so the prose
// explaining the fix cannot itself satisfy the assertions, and so the window
// below measures code rather than commentary. `//` is only a line comment when
// it isn't a URL's `://`; blanking can only ever hide a violation, never invent
// one. Same helper, same reason, as board-error-guard.test.mjs.
function blankComments(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, " "))
    .replace(/(^|[^:])\/\/[^\n]*/g, (m, lead) => lead + " ".repeat(m.length - lead.length));
}

// Blanking preserves offsets, so a blanked comment would fill the window with
// spaces; collapse runs of whitespace afterwards so WINDOW counts code.
const scanned = raw.map(([name, src]) => [name, blankComments(src).replace(/\s+/g, " ")]);

// The 300 code characters after the awaited call: enough to hold the re-check
// and its discard, short enough that a match cannot come from unrelated code
// further down the function.
const WINDOW = 300;

// The span stops at the enclosing try's `} catch`: the catch block has always
// carried its own `descEditCard?.key !== card.key` guard, and letting the
// window run into it would let that pre-existing guard satisfy the assertion
// the success path is supposed to answer.
function afterStart(src) {
  const spans = [];
  const re = /await\s+go\(\)\.StartDescEdit\(/g;
  let m;
  while ((m = re.exec(src))) {
    const span = src.slice(m.index, m.index + WINDOW);
    const catchAt = span.search(/\}\s*catch\b/);
    spans.push(catchAt === -1 ? span : span.slice(0, catchAt));
  }
  return spans;
}

test("every awaited StartDescEdit is followed by an ownership re-check", () => {
  for (const [name, src] of scanned) {
    const spans = afterStart(src);
    assert.ok(spans.length > 0, `${name}: expected an awaited StartDescEdit call`);
    for (const span of spans) {
      assert.match(span, /descEditCard\?\.key\s*!==\s*card\.key/, `${name}: no ownership re-check after StartDescEdit`);
    }
  }
});

test("an abandoned StartDescEdit discards the session it just created", () => {
  for (const [name, src] of scanned) {
    for (const span of afterStart(src)) {
      assert.match(span, /discardDescEditSession\(/, `${name}: abandoned StartDescEdit does not discard its session`);
    }
  }
});

test("StartDescEdit's result is not assigned straight into the shared descEdit state", () => {
  for (const [name, src] of scanned) {
    assert.doesNotMatch(
      src,
      /descEdit\s*=\s*await\s+go\(\)\.StartDescEdit\(/,
      `${name}: StartDescEdit adopted into shared state before the ownership re-check`,
    );
  }
});
