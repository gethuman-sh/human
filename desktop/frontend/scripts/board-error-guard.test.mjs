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

// Both the source and the committed dist/ transpile are checked. dist/ is not
// a throwaway build artifact here — it is checked in and embedded via
// `//go:embed all:frontend/dist`, so a dist/ that lagged a fixed src/ would
// still ship the clobber to users.
const here = dirname(fileURLToPath(import.meta.url));
const scanned = [
  ["src/board.ts", readFileSync(resolve(here, "..", "src", "board.ts"), "utf8")],
  ["dist/board.js", readFileSync(resolve(here, "..", "dist", "board.js"), "utf8")],
];

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

// Top-level (depth-1) comma-separated argument spans (as [start, end) index
// pairs into `src`) of a call whose "(" is at `open`. Brace/bracket/paren
// nesting inside an argument (e.g. an arrow function's block body) is not a
// separator, only a comma back at the call's own depth is.
function callArgs(src, open) {
  let depth = 0;
  let start = open + 1;
  const args = [];
  for (let i = open; i < src.length; i++) {
    const c = src[i];
    if (c === "(" || c === "{" || c === "[") depth++;
    else if (c === ")" || c === "}" || c === "]") {
      depth--;
      if (depth === 0 && c === ")") {
        args.push([start, i]);
        return { args, close: i };
      }
    } else if (c === "," && depth === 1) {
      args.push([start, i]);
      start = i + 1;
    }
  }
  return null;
}

// Every branch that runs only when an action failed: `catch (e) { … }`
// blocks, `.catch((e) => { … })` handlers, and the onError argument of a
// runGuardedAction(action, onError, onSuccess) call — the helper only
// guarantees onSuccess never follows a throw, but onError's own body can
// still show an error and reconcile together, reintroducing the clobber one
// level in.
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

  const guardRe = /\brunGuardedAction\s*\(/g;
  let gm;
  while ((gm = guardRe.exec(src))) {
    const callOpen = gm.index + gm[0].length - 1;
    const call = callArgs(src, callOpen);
    if (!call || call.args.length < 2) continue;
    const [argStart, argEnd] = call.args[1];
    const onError = src.slice(argStart, argEnd);
    const arrow = onError.match(/^\s*(?:async\s+)?\(?[^()]*\)?\s*=>\s*\{/);
    if (!arrow) continue;
    const open = argStart + arrow[0].length - 1;
    const close = matchBrace(src, open);
    if (close === -1 || close > argEnd) continue;
    found.push({ kind: "guarded-onerror", open, close, body: src.slice(open + 1, close) });
  }

  return found;
}

const lineOf = (src, idx) => src.slice(0, idx).split("\n").length;

function bannerClobberViolations(rawSrc, label = "board.ts") {
  const src = blankComments(rawSrc);
  const violations = [];

  for (const branch of failureBranches(src)) {
    if (!/showError\s*\(/.test(branch.body)) continue;

    // The banner is clobbered by a reconcile inside the branch itself…
    if (/reconcile\s*\(/.test(branch.body)) {
      violations.push(
        `${label}:${lineOf(rawSrc, branch.open)} — ${branch.kind} calls showError() and reconcile() together`,
      );
      continue;
    }

    // …or, for a try/catch, by one on the statement right after it, which the
    // failure path falls straight into unless the branch returns (fixSecurity).
    if (branch.kind !== "catch-block") continue;
    if (/(^|[\s;{])return[\s;]/.test(branch.body)) continue;
    if (/^\s*(await\s+|void\s+)?reconcile\s*\(/.test(src.slice(branch.close + 1))) {
      violations.push(
        `${label}:${lineOf(rawSrc, branch.open)} — catch falls through to the reconcile() after it`,
      );
    }
  }
  return violations;
}

for (const [label, source] of scanned) {
  test(`SC-637: no ${label} failure branch shows an error and reconciles in the same cycle`, () => {
    const violations = bannerClobberViolations(source, label);
    assert.deepEqual(
      violations,
      [],
      "showError() must not be followed by reconcile() in the same failure cycle — route the " +
        "action through runGuardedAction and revert any optimistic mutation instead:\n  " +
        violations.join("\n  "),
    );
  });
}

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

// The helper itself (runGuardedAction) only guarantees onSuccess never runs
// after a throw — it says nothing about onError's own body, so an onError
// callback that shows an error and reconciles together reintroduces the same
// clobber one level in. Guard that shape too, and confirm the ordinary
// onError body (revert + showError, no reconcile) is still accepted.
test("the scanner treats a runGuardedAction onError callback as a failure branch", () => {
  const clobberInOnError = `
    async function act() {
      await runGuardedAction(
        () => go().Thing(),
        (err) => {
          showError(errMessage(err));
          void reconcile();
        },
        reconcile,
      );
    }`;
  const guardedOnError = `
    async function act() {
      await runGuardedAction(
        () => go().Thing(),
        (err) => {
          card.hidden = prevHidden;
          render();
          showError(errMessage(err));
        },
        reconcile,
      );
    }`;

  assert.equal(
    bannerClobberViolations(clobberInOnError).length,
    1,
    "must catch a reconcile inside a runGuardedAction onError callback",
  );
  assert.deepEqual(
    bannerClobberViolations(guardedOnError),
    [],
    "must accept an onError callback that reverts and shows an error without reconciling",
  );
});
