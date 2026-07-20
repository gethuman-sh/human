import { test } from "node:test";
import assert from "node:assert/strict";
import { deployableCards, deployControlView, isReadyToDeploy } from "../build/board-queue.js";
import { buildDeployControl } from "../build/board-deploy.js";

// Minimal element/document stub: buildDeployControl only ever
// createElement("button"), sets a handful of properties and binds one click
// listener. The stub records bound listeners so the test can assert BOTH
// interaction halves are wired at once (precedent: statsview.test.mjs).
function installDeployDOM() {
  function makeEl() {
    return {
      dataset: {},
      listeners: {},
      style: {},
      addEventListener(type, fn) {
        (this.listeners[type] ??= []).push(fn);
      },
    };
  }
  globalThis.document = { createElement: () => makeEl() };
}

// A card resting in Ready to Deploy: passed review on a recorded branch.
const ready = (over = {}) => ({ stage: "verification", state: "done", verdict: "pass", branch: "b", ...over });

// 849 wiring invariant: the single Deploy control must support BOTH halves of
// the interaction at once — a drop target (dataset.drop) AND a click action.
// The Features control used to have only the drop half, the Bugs control only
// the click half; the unified builder gives every control both.
test("849: the Deploy control is SIMULTANEOUSLY a drop target and a click action", () => {
  installDeployDOM();
  let clicked = 0;
  const view = deployControlView([ready()], "features");
  const el = buildDeployControl(view, { className: "deploy-zone", onClick: () => clicked++ });
  // drop half — the drag hit-test finds it via data-drop="deploy"
  assert.equal(el.dataset.drop, "deploy", "control is a drop target");
  // click half — a bound click listener ships every ready card
  assert.ok(el.listeners.click?.length, "control has a bound click listener");
  el.listeners.click[0]();
  assert.equal(clicked, 1, "the click listener invokes onClick");
});

// Even with nothing to ship the control stays a drop target and explains itself:
// disabled blocks the bulk click, but a single ready card dragged onto it is
// still gated by isReadyToDeploy at drop time, not by hiding the target.
test("849: an empty Deploy control is disabled with a tooltip yet still accepts a drop", () => {
  installDeployDOM();
  const view = deployControlView([], "features");
  const el = buildDeployControl(view, { className: "deploy-zone", onClick: () => {} });
  assert.equal(el.disabled, true, "disabled when nothing is ready");
  assert.ok(el.title && el.title.length > 0, "carries an explanatory tooltip");
  assert.equal(el.dataset.drop, "deploy", "still a drop target even when disabled");
});

// The Features click ships every ready-to-deploy card ON THE BOARD — the feature
// workflow cards, never the Bugs pane's cards, and never an unready card.
test("deployableCards(features) selects ready non-bug cards only", () => {
  const cards = [
    ready({ key: "a" }),
    ready({ key: "b", bug: true }), // a bug -> belongs to the Bugs pane
    ready({ key: "c", branch: undefined }), // no branch -> not ready
  ];
  assert.deepEqual(
    deployableCards(cards, "features").map((c) => c.key),
    ["a"],
  );
});

// The Bugs click ships every ready fixed bug — bug cards only, and never one
// whose review failed.
test("deployableCards(bugs) selects ready bug cards only", () => {
  const cards = [
    ready({ key: "a" }), // not a bug
    ready({ key: "b", bug: true }),
    ready({ key: "c", bug: true, verdict: "fail" }), // failed review
  ];
  assert.deepEqual(
    deployableCards(cards, "bugs").map((c) => c.key),
    ["b"],
  );
});

// The shared readiness gate (SC-297): a passed review with no recorded branch
// has nothing to ship and must be rejected by both controls, in both
// interactions.
test("isReadyToDeploy rejects a passed review with no recorded branch (SC-297 gate)", () => {
  assert.equal(isReadyToDeploy(ready({ branch: undefined })), false);
  assert.equal(isReadyToDeploy(ready({ verdict: "fail" })), false);
  assert.equal(isReadyToDeploy(ready()), true);
});

// The view carries the deployable count, an enabled state, and a count-labelled
// caption when cards are ready — the affordance both controls render.
test("deployControlView reports a count, an enabled state and a labelled count when cards are ready", () => {
  const view = deployControlView([ready({ bug: true }), ready({ bug: true })], "bugs");
  assert.equal(view.count, 2);
  assert.equal(view.disabled, false);
  assert.match(view.label, /2/);
});

// With nothing ready the view is disabled and offers an explanatory tooltip —
// the shared disabled-state affordance the acceptance criteria require.
test("deployControlView reports disabled with an explanatory tooltip when nothing is ready", () => {
  const view = deployControlView([], "bugs");
  assert.equal(view.count, 0);
  assert.equal(view.disabled, true);
  assert.ok(view.tooltip && view.tooltip.length > 0);
});
