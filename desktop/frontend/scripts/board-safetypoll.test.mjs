import { test } from "node:test";
import assert from "node:assert/strict";
import { safetyPollShouldReconcile, safetyReconcileError } from "../build/board-queue.js";

// SC-1677 regression: the 90s safety poll bounds staleness for tracker writes
// that emit no daemon event (e.g. `human bug create`). It MUST run regardless of
// the daemonReachable flag — that flag can read false while the daemon is alive
// and Cards() succeeds, and gating on it silently removed the only staleness
// bound, leaving CLI-created tickets invisible for minutes.
test("safety poll reconciles even when the daemon reads unreachable (SC-1677)", () => {
  assert.equal(safetyPollShouldReconcile(false), true);
  assert.equal(safetyPollShouldReconcile(true), true);
});

// SC-1677: a safety-poll fetch failure must surface a staleness cue, not blank a
// populated board (which would itself look "not current"). Keep the last-known
// cards and set a "may be stale" banner; an already-empty board shows the plain
// error.
test("safety-poll fetch failure keeps existing cards with a staleness banner (SC-1677)", () => {
  const populated = { cards: [{ key: "SC-1" }, { key: "SC-2" }], dockerAvailable: true };
  const next = safetyReconcileError(populated, "daemon not reachable");
  assert.equal(next.cards.length, 2, "populated board must not be blanked");
  assert.match(next.error, /stale/i);
  assert.match(next.error, /daemon not reachable/);

  const empty = { cards: [], dockerAvailable: false };
  const emptyNext = safetyReconcileError(empty, "daemon not reachable");
  assert.equal(emptyNext.cards.length, 0);
  assert.equal(emptyNext.error, "daemon not reachable");
});

// SC-3577: the cards are the last thing known to be true, so they stay; the flow
// claim was a statement about a moment that has now passed unverified, so it goes.
test("safetyReconcileError drops the flow claim but keeps the cards", () => {
  const out = safetyReconcileError({ cards: [{}], dockerAvailable: true, flow: { state: "flowing" } }, "boom");
  assert.equal(out.cards.length, 1);
  assert.equal(out.flow, undefined);
  assert.match(out.error, /^Board may be stale — /);
});

test("safetyReconcileError makes no flow claim when it drops the cards", () => {
  const out = safetyReconcileError({ cards: [], dockerAvailable: true, flow: { state: "flowing" } }, "boom");
  assert.equal(out.cards.length, 0);
  assert.equal(out.flow, undefined);
});
