import { test } from "node:test";
import assert from "node:assert/strict";
import { queueOf, forwardDropAllowed, planReady, badgeInfo, cardError, sortByHandOrder, insertKeyAt, boardStateFromPayload, isReviewRetryable } from "../build/board-queue.js";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

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

// SC-695: a stage-failed review is retryable in place — the "Retry review"
// affordance mirrors "Retry plan"/"Retry build". Only verification/failed
// qualifies; a done review (rework path), a running review, and a failed build
// must NOT offer it.
test("isReviewRetryable is true only for verification/failed (SC-695)", () => {
  assert.equal(isReviewRetryable({ stage: "verification", state: "failed" }), true);
  assert.equal(isReviewRetryable({ stage: "verification", state: "done", verdict: "fail", branch: "b" }), false);
  assert.equal(isReviewRetryable({ stage: "verification", state: "running" }), false);
  assert.equal(isReviewRetryable({ stage: "implementation", state: "failed" }), false);
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

// 1290: an open decision block must outrank a `failed` state too, not just the
// review warning — a card carrying a stale *-failed marker AND an open
// same-stage options block is a deliberate human pause (the daemon's twin
// guard in reconcileStuckRunning suppresses the marker going forward, but a
// marker posted before the fix landed must still render as a decision, not a
// red ✕). The badge text carries a `?` glyph so it reads as a question, not an
// error.
test("open options outrank a failed state with a decision-needed badge (1290)", () => {
  const card = { stage: "planning", state: "failed", options: [{ id: "1", label: "a" }, { id: "2", label: "b" }] };
  const info = badgeInfo(card);
  assert.equal(info.cls, "decision");
  assert.match(info.text, /\?/);
  // Without options the same card falls back to the plain failed badge.
  assert.equal(badgeInfo({ ...card, options: [] }).cls, "failed");
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

// SC-1301: the red `.card-error` subtitle must track the SAME badge
// classification as the amber decision badge, not be computed independently. A
// card parked on an open [human:options] decision that ALSO carries a stale
// *-failed marker must paint the decision badge and NO red error line — SC-1290
// fixed the badge but left renderCard's error render on raw `card.error`.
test("cardError suppresses the failure subtitle when a decision outranks it (SC-1301)", () => {
  const card = {
    stage: "planning",
    state: "failed",
    error: "Stuck in planning: no terminal marker and no live agent — needs attention",
    options: [{ id: "1", label: "a" }, { id: "2", label: "b" }],
  };
  assert.equal(badgeInfo(card)?.cls, "decision", "an open decision must classify as the decision badge");
  assert.equal(cardError(card), "", "no red error subtitle may accompany the decision badge");
});

test("cardError keeps the failure text for a genuinely failed card (SC-1301)", () => {
  const card = { stage: "planning", state: "failed", error: "boom" };
  assert.equal(badgeInfo(card)?.cls, "failed", "no decision → the failed badge");
  assert.equal(cardError(card), "boom", "a real failure still shows its error text");
});

test("cardError is empty for a running card (SC-1301)", () => {
  assert.equal(cardError({ stage: "implementation", state: "running", error: "boom" }), "");
});

test("cardError is empty when the error field is blank or absent (SC-1301)", () => {
  assert.equal(cardError({ stage: "planning", state: "failed" }), "");
  assert.equal(cardError({ stage: "planning", state: "failed", error: "" }), "");
});

// SC-1301 wiring guard: renderCard's error subtitle must be gated on the shared
// cardError classifier, never on raw `card.error`. The frontend is dependency-
// free (no DOM test runner), so this asserts the source wiring like
// ideation-done.test.mjs — so the unconditional render cannot be reintroduced.
test("renderCard gates the error subtitle on cardError, not raw card.error (SC-1301)", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const ts = readFileSync(resolve(here, "..", "src", "board.ts"), "utf8");
  assert.match(ts, /cardError\(/, "renderCard must derive the error text through the shared cardError classifier");
  assert.match(ts, /class="card-error"/, "the error subtitle keeps its style hook");
  assert.doesNotMatch(
    ts,
    /card\.error \? `<div class="card-error"/,
    "the unconditional card.error render must not be reintroduced",
  );
});

test("queued state renders the decision-recorded note, not a failure (SC-1320)", () => {
  const info = badgeInfo({ stage: "planning", state: "queued" });
  assert.equal(info.cls, "queued");
  assert.match(info.text, /decision recorded — replanning picked up/);
});

test("queued verb is stage-aware (SC-1320)", () => {
  assert.match(badgeInfo({ stage: "implementation", state: "queued" }).text, /rebuild picked up/);
});

test("a queued card shows no red error subtitle (SC-1320)", () => {
  assert.equal(cardError({ stage: "planning", state: "queued", error: "Stuck in planning" }), "");
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

// --- Engineering-backlog age badge + Replan gating ---

test("ageDays: absent, unparseable, negative, and whole-day conversion", async () => {
  const { ageDays } = await import("../build/board-queue.js");
  const now = new Date("2026-07-21T12:00:00Z");
  assert.equal(ageDays(undefined, now), null);
  assert.equal(ageDays("not-a-date", now), null);
  assert.equal(ageDays("2026-07-22T12:00:00Z", now), null, "future timestamps yield null, not negative days");
  assert.equal(ageDays("2026-07-21T02:00:00Z", now), 0);
  assert.equal(ageDays("2026-07-14T12:00:00Z", now), 7);
});

test("ageBadge: only done-state planning feature cards, from 1 day up", async () => {
  const { ageBadge } = await import("../build/board-queue.js");
  const now = new Date("2026-07-21T12:00:00Z");
  const daysAgo = (n) => new Date(now.getTime() - n * 86_400_000).toISOString();

  assert.equal(ageBadge({ stage: "planning", state: "done", stageEnteredAt: daysAgo(0) }, now), null, "under a day: no 0d noise");
  assert.deepEqual(ageBadge({ stage: "planning", state: "done", stageEnteredAt: daysAgo(3) }, now), { text: "3d", cls: "age" });
  assert.deepEqual(ageBadge({ stage: "planning", state: "done", stageEnteredAt: daysAgo(7) }, now), { text: "7d", cls: "age warn" });
  assert.deepEqual(ageBadge({ stage: "planning", state: "done", stageEnteredAt: daysAgo(14) }, now), { text: "14d", cls: "age hot" });

  assert.equal(ageBadge({ stage: "planning", state: "running", stageEnteredAt: daysAgo(3) }, now), null, "running plans show the spinner instead");
  assert.equal(ageBadge({ stage: "implementation", state: "done", stageEnteredAt: daysAgo(3) }, now), null, "only the Engineering backlog ages");
  assert.equal(ageBadge({ stage: "planning", state: "done", bug: true, stageEnteredAt: daysAgo(3) }, now), null, "bug cards live in the Bugs pane");
  assert.equal(ageBadge({ stage: "planning", state: "done" }, now), null, "no timestamp, no badge");
});

test("isReplannable: planned feature cards only", async () => {
  const { isReplannable } = await import("../build/board-queue.js");
  assert.equal(isReplannable({ stage: "planning", state: "done" }), true);
  assert.equal(isReplannable({ stage: "planning", state: "failed" }), false, "failed plans get Retry plan, not Replan");
  assert.equal(isReplannable({ stage: "planning", state: "running" }), false);
  assert.equal(isReplannable({ stage: "implementation", state: "done" }), false);
  assert.equal(isReplannable({ stage: "planning", state: "done", bug: true }), false);
});
