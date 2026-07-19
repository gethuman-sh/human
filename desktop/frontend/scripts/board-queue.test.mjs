import { test } from "node:test";
import assert from "node:assert/strict";
import { queueOf, forwardDropAllowed, planReady, badgeInfo, sortByHandOrder, insertKeyAt, boardStateFromPayload } from "../build/board-queue.js";

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

// SC-429 regression: "fix complete, review not started" (stage=implementation,
// state=done) is a durable pipeline state, not a sub-second transient. It must
// render a status badge in both the Bugs pane and the Code column — never blank.
test("implementation/done card gets a non-empty badge (SC-429)", () => {
  const info = badgeInfo({ stage: "implementation", state: "done" });
  assert.notEqual(info, null, "implementation/done must classify to a badge, not blank");
  assert.ok(info.text.length > 0, "badge text must be non-empty");
  assert.equal(info.cls, "await");
});

test("badgeInfo preserves prior classifications", () => {
  assert.equal(badgeInfo({ stage: "implementation", state: "running" }).cls, "running");
  assert.equal(badgeInfo({ stage: "verification", state: "failed" }).cls, "failed");
  // SC-405: a no-fix-needed triage outcome is resolved — never red, never blank.
  assert.equal(badgeInfo({ stage: "implementation", state: "resolved" }).cls, "resolved");
  // SC-454: a planning card whose work already shipped is resolved with a
  // positive "already shipped" badge — never red, never blank.
  assert.equal(badgeInfo({ stage: "planning", state: "resolved" }).cls, "resolved");
  assert.equal(badgeInfo({ stage: "planning", state: "resolved" }).text, "already shipped");
  assert.equal(
    badgeInfo({ stage: "verification", state: "done", verdict: "fail", branch: "b" }).cls,
    "warning",
  );
  assert.equal(
    badgeInfo({ stage: "verification", state: "done", verdict: "pass", branch: "b" }),
    null,
    "a resting reviewed card needs no badge — its queue position states completion",
  );
  assert.equal(badgeInfo({ stage: "done", state: "done" }).cls, "done");
});

// An open decision block must surface as its own badge, outranking the
// generic review warning — the actionable statement is "pick one" (SC-534).
test("open options render a decision-needed badge over the review warning", () => {
  const card = {
    stage: "verification",
    state: "done",
    verdict: "fail",
    branch: "b",
    options: [{ id: "1", label: "a" }, { id: "2", label: "b" }],
  };
  const info = badgeInfo(card);
  assert.equal(info.cls, "decision");
  assert.match(info.text, /decision needed/);
  // Without options the same card falls back to the review warning.
  assert.equal(badgeInfo({ ...card, options: [] }).cls, "warning");
});

// SC-624: columns render in the user's hand-sorted order; cards without a
// saved slot keep fetch order after the sorted ones.
test("sortByHandOrder: listed keys first in saved order, rest stable after", () => {
  const cards = [{ key: "A" }, { key: "B" }, { key: "C" }, { key: "D" }];
  sortByHandOrder(cards, ["C", "A"]);
  assert.deepEqual(cards.map((c) => c.key), ["C", "A", "B", "D"]);
});

test("sortByHandOrder: no saved order leaves fetch order untouched", () => {
  const cards = [{ key: "B" }, { key: "A" }];
  sortByHandOrder(cards, undefined);
  assert.deepEqual(cards.map((c) => c.key), ["B", "A"]);
  sortByHandOrder(cards, []);
  assert.deepEqual(cards.map((c) => c.key), ["B", "A"]);
});

// SC-631 regression: the payload-to-state mapping must carry the board-level
// columnOrder the daemon ships. A field-by-field rebuild (cards/dockerAvailable/
// error only) dropped it, collapsing the hand-sort back to fetch order on every
// reload. These pin the mapping so that class of bug cannot ship again.
test("boardStateFromPayload carries columnOrder through the mapping (SC-631)", () => {
  const payload = {
    cards: [{ key: "A" }, { key: "B" }, { key: "C" }],
    dockerAvailable: true,
    columnOrder: { product: ["C", "A", "B"] },
  };
  const state = boardStateFromPayload(payload);
  assert.deepEqual(state.columnOrder, { product: ["C", "A", "B"] });
  assert.deepEqual(state.cards.map((c) => c.key), ["A", "B", "C"]);
  assert.equal(state.dockerAvailable, true);
  assert.equal(state.error, "");
});

test("boardStateFromPayload state feeds sortByHandOrder to the saved order (SC-631)", () => {
  const payload = {
    cards: [{ key: "A" }, { key: "B" }, { key: "C" }],
    dockerAvailable: true,
    columnOrder: { product: ["C", "A", "B"] },
  };
  const state = boardStateFromPayload(payload);
  const sorted = sortByHandOrder([...state.cards], state.columnOrder?.product);
  assert.deepEqual(sorted.map((c) => c.key), ["C", "A", "B"]);
});

test("boardStateFromPayload suppresses error but keeps columnOrder for quick phase (SC-631)", () => {
  const state = boardStateFromPayload({ error: "boom", columnOrder: { product: ["A"] } }, true);
  assert.equal(state.error, "");
  assert.deepEqual(state.columnOrder, { product: ["A"] });
});

test("boardStateFromPayload normalizes an empty payload (SC-631)", () => {
  const state = boardStateFromPayload({});
  assert.deepEqual(state.cards, []);
  assert.equal(state.dockerAvailable, false);
  assert.equal(state.error, "");
  assert.equal(state.columnOrder, undefined);
});

// SC-624: a same-column drop inserts the dragged key at the pointer position.
test("insertKeyAt places dragged key by drop midpoint", () => {
  // Cards A(mid 100), B(mid 200), C(mid 300).
  const resting = ["A", "B", "C"];
  const mids = [100, 200, 300];
  assert.deepEqual(insertKeyAt(resting, mids, "X", 50), ["X", "A", "B", "C"]);
  assert.deepEqual(insertKeyAt(resting, mids, "X", 150), ["A", "X", "B", "C"]);
  assert.deepEqual(insertKeyAt(resting, mids, "X", 999), ["A", "B", "C", "X"]);
  assert.deepEqual(insertKeyAt([], [], "X", 10), ["X"]);
});
