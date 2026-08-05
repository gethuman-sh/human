// SC-3409: the config-declared board dimming. Covers the percent->opacity
// conversion and, just as importantly, the REMOVE path: an inline custom
// property outlives the payload that set it, so a value cleared from the config
// has to be actively taken off the root or the old dimming stays stuck for the
// life of the window.
import { test } from "node:test";
import assert from "node:assert/strict";

import { notMineOpacity, applyNotMineOpacity, NOT_MINE_OPACITY_VAR } from "../build/appearance.js";

// Stands in for document.documentElement: the module only ever touches
// style.setProperty/removeProperty, so no DOM is needed to test it.
function makeRoot() {
  const set = new Map();
  const removed = [];
  return {
    set,
    removed,
    style: {
      setProperty: (k, v) => set.set(k, v),
      removeProperty: (k) => {
        removed.push(k);
        set.delete(k);
        return "";
      },
    },
  };
}

test("notMineOpacity converts a declared percent (SC-3409)", () => {
  assert.equal(notMineOpacity(20), "0.2");
  assert.equal(notMineOpacity(35), "0.35");
});

test("notMineOpacity accepts the range bounds (SC-3409)", () => {
  assert.equal(notMineOpacity(5), "0.05");
  assert.equal(notMineOpacity(100), "1");
});

test("notMineOpacity says nothing for an absent value (SC-3409)", () => {
  assert.equal(notMineOpacity(undefined), null);
});

test("notMineOpacity rejects out-of-range and nonsense (SC-3409)", () => {
  for (const bad of [0, -5, 101, NaN, Infinity, 12.5]) {
    assert.equal(notMineOpacity(bad), null, `${bad} must produce no opinion`);
  }
});

// Every integer percent in the usable range must render as a short decimal —
// a floating-point artefact would land verbatim in the stylesheet.
test("notMineOpacity produces clean decimals across the range (SC-3409)", () => {
  for (let n = 5; n <= 100; n++) {
    const out = notMineOpacity(n);
    assert.match(out, /^(0(\.\d{1,2})?|1)$/, `${n}% produced ${out}`);
  }
});

test("applyNotMineOpacity sets the token on the root (SC-3409)", () => {
  const root = makeRoot();
  applyNotMineOpacity(root, 20);
  assert.equal(root.set.get(NOT_MINE_OPACITY_VAR), "0.2");
  assert.deepEqual(root.removed, [], "a usable value must not remove the property");
});

test("applyNotMineOpacity removes a stale token when nothing is declared (SC-3409)", () => {
  const root = makeRoot();
  applyNotMineOpacity(root, 20);
  applyNotMineOpacity(root, undefined);
  assert.deepEqual(root.removed, [NOT_MINE_OPACITY_VAR]);
  assert.equal(root.set.has(NOT_MINE_OPACITY_VAR), false);
});

// 0 is the settings page's cleared-row value. It must return the board to the
// stylesheet default, not paint every not-mine card invisible.
test("applyNotMineOpacity removes rather than writes an unusable value (SC-3409)", () => {
  const root = makeRoot();
  applyNotMineOpacity(root, 20);
  applyNotMineOpacity(root, 0);
  assert.deepEqual(root.removed, [NOT_MINE_OPACITY_VAR]);
  assert.equal(root.set.has(NOT_MINE_OPACITY_VAR), false);
});