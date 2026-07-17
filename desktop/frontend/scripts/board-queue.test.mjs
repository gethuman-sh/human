import { test } from "node:test";
import assert from "node:assert/strict";
import { queueOf, forwardDropAllowed, planReady } from "../build/board-queue.js";

// SC-355 regression: a running or failed planning card must render in the
// Engineering column where the user dropped it — not snap back to Product.
test("planning card renders in engineering for every state (SC-355)", () => {
  assert.equal(queueOf({ stage: "planning", state: "running" }), "engineering");
  assert.equal(queueOf({ stage: "planning", state: "failed" }), "engineering");
  assert.equal(queueOf({ stage: "planning", state: "done" }), "engineering");
});

test("destination-lane placement stays intact for other stages", () => {
  assert.equal(queueOf({ stage: "backlog", state: "done" }), "product");
  assert.equal(queueOf({ stage: "implementation", state: "running" }), "building");
  assert.equal(queueOf({ stage: "verification", state: "running" }), "building");
  assert.equal(
    queueOf({ stage: "verification", state: "done", verdict: "pass", branch: "b" }),
    "deploy",
  );
});

// SC-355 affordance: only a plan-ready card may advance Engineering -> Code.
test("forward gate: engineering -> code requires plan-ready", () => {
  assert.equal(planReady({ stage: "planning", state: "done" }), true);
  assert.equal(planReady({ stage: "planning", state: "failed" }), false);
  assert.equal(forwardDropAllowed({ stage: "planning", state: "done" }, "building"), true);
  assert.equal(forwardDropAllowed({ stage: "planning", state: "failed" }, "building"), false);
  assert.equal(forwardDropAllowed({ stage: "planning", state: "running" }, "building"), false);
});

test("forward gate: rework re-drop onto code still allowed", () => {
  assert.equal(
    forwardDropAllowed({ stage: "verification", state: "done", verdict: "fail", branch: "b" }, "building"),
    true,
  );
});

// The Engineering column offers "Retry plan" exactly for a failed planning card,
// and NEVER offers the forward Code drop for it (guards SC-355 affordance).
test("failed planning card is retry-eligible but not code-droppable", () => {
  const failed = { stage: "planning", state: "failed" };
  assert.equal(planReady(failed), false, "failed plan must not be plan-ready");
  assert.equal(forwardDropAllowed(failed, "building"), false, "failed plan must not drop into Code");
});
