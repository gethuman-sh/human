// SC-637 regression, call-site half.
//
// board-actions.test.mjs proves the *helper* (runGuardedAction) sequences
// correctly, but the original bug was never in a helper — it was the same two
// lines repeated across call sites, and fixSecurity() later reintroduced it
// verbatim by cloning the pre-guard fixBug(). A helper-only test leaves that
// whole class of regression uncovered, so this asserts the property directly on
// the source: no failure branch that shows an error may also reconcile in the
// same cycle, whatever route it takes there.
//
// Source-level rather than behavioural because the frontend is intentionally
// dependency-free (no DOM test runner), the same reason style.test.mjs asserts
// CSS rules instead of rendering. It also needs no tsc build, so it runs even
// where the toolchain isn't installed.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const boardSrc = readFileSync(resolve(here, "..", "src", "board.ts"), "utf8");

// Blank comments to spaces (preserving offsets and newlines) so prose
// mentioning reconcile()/showError() can't register as code. `//` is only
// treated as a line comment when it isn't a URL's `://`; blanking can only ever
// hide a violation, never invent one.
function blankComments(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, " "))
    .replace(/(^|[^:])\/\/[^\n]*/g, (m, lead) => lead + " ".repeat(m.length - lead.length));
}

// Index of the `}` closing the `{` at `open`.
function matchBrace(src, open) {
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return i;
  }
  return -1;
}

// Every branch that runs only when an action failed: `catch (e) { … }` blocks
// and `.catch((e) => { … })` handlers.
function failureBranches(src) {
  const found = [];
  for (const [re, kind] of [
    [/(?:^|[^.\w])catch\s*\([^)]*\)\s*\{/g, "catch-block"],
    [/\.catch\(\s*\(?[^)]*\)?\s*=>\s*\{/g, "catch-handler"],
  ]) {
    let m;
    while ((m = re.exec(src))) {
      const open = src.indexOf("{", m.index + m[0].length - 1);
      const close = matchBrace(src, open);
      if (close === -1) continue;
      found.push({ kind, open, close, body: src.slice(open + 1, close) });
    }
  }
  return found;
}

const lineOf = (src, idx) => src.slice(0, idx).split("\n").length;

function bannerClobberViolations(rawSrc) {
  const src = blankComments(rawSrc);
  const violations = [];

  for (const branch of failureBranches(src)) {
    if (!/showError\s*\(/.test(branch.body)) continue;

    // The banner is clobbered by a reconcile inside the branch itself…
    if (/reconcile\s*\(/.test(branch.body)) {
      violations.push(
        `board.ts:${lineOf(rawSrc, branch.open)} — ${branch.kind} calls showError() and reconcile() together`,
      );
      continue;
    }

    // …or, for a try/catch, by one on the statement right after it, which the
    // failure path falls straight into unless the branch returns (fixSecurity).
    if (branch.kind !== "catch-block") continue;
    if (/(^|[\s;{])return[\s;]/.test(branch.body)) continue;
    if (/^\s*(await\s+|void\s+)?reconcile\s*\(/.test(src.slice(branch.close + 1))) {
      violations.push(
        `board.ts:${lineOf(rawSrc, branch.open)} — catch falls through to the reconcile() after it`,
      );
    }
  }
  return violations;
}

test("SC-637: no board.ts failure branch shows an error and reconciles in the same cycle", () => {
  const violations = bannerClobberViolations(boardSrc);
  assert.deepEqual(
    violations,
    [],
    "showError() must not be followed by reconcile() in the same failure cycle — route the " +
      "action through runGuardedAction and revert any optimistic mutation instead:\n  " +
      violations.join("\n  "),
  );
});

// Guard the guard: a scanner that silently matches nothing would pass forever.
test("the scanner detects both clobber shapes", () => {
  const insideBranch = `
    async function act() {
      try {
        await go().Thing();
      } catch (err) {
        showError(errMessage(err));
        void reconcile();
      }
    }`;
  const fallsThrough = `
    async function act() {
      try {
        await go().Thing();
      } catch (err) {
        showError(errMessage(err));
      }
      await reconcile();
    }`;
  const guarded = `
    async function act() {
      try {
        await go().Thing();
      } catch (err) {
        showError(errMessage(err));
        return;
      }
      await reconcile();
    }`;

  assert.equal(bannerClobberViolations(insideBranch).length, 1, "must catch a reconcile inside the branch");
  assert.equal(bannerClobberViolations(fallsThrough).length, 1, "must catch a fall-through reconcile");
  assert.deepEqual(bannerClobberViolations(guarded), [], "must accept a branch that returns first");
});
