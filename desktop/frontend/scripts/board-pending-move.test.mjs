import { test } from "node:test";
import assert from "node:assert/strict";
import {
  reconcilePendingMoves,
  dropPendingMove,
  pendingMovesForBatch,
} from "../build/board-pending-move.js";

// SC-2521 regression: a card dragged into a new stage lands optimistically,
// but the next reconcile fetches a board that predates the drop becoming
// readable (tracker read-after-write lag). Without a shield that stale fetch —
// which still shows the card at its ORIGIN stage — overwrites the optimistic
// move, snapping the card back before a later poll restores it. The move/
// jump-back/move flicker. reconcilePendingMoves holds the move against exactly
// that stale read while still yielding to any real change.

const move = (over) => ({
  key: "SC-9001",
  fromStage: "backlog",
  toStage: "implementation",
  expiresAt: 100_000,
  ...over,
});

test("SC-2521: a stale fetch still at the origin is held and overridden to the target", () => {
  const moves = [move()];
  const fetched = new Map([["SC-9001", "backlog"]]);

  const { moves: kept, overrides } = reconcilePendingMoves(moves, fetched, 0);

  assert.equal(kept.length, 1, "the move is held past the stale read");
  assert.equal(overrides.length, 1);
  assert.deepEqual(overrides[0], { key: "SC-9001", toStage: "implementation" });
});

test("a fetch confirming the target clears the move with no override", () => {
  const moves = [move()];
  const fetched = new Map([["SC-9001", "implementation"]]);

  const { moves: kept, overrides } = reconcilePendingMoves(moves, fetched, 0);

  assert.equal(kept.length, 0, "confirmed move needs no further shielding");
  assert.equal(overrides.length, 0);
});

test("a fetch showing a third stage lets the daemon win — cleared, not overridden", () => {
  const moves = [move()];
  // Daemon moved it somewhere else entirely (e.g. review) — that must show.
  const fetched = new Map([["SC-9001", "review"]]);

  const { moves: kept, overrides } = reconcilePendingMoves(moves, fetched, 0);

  assert.equal(kept.length, 0, "a real change wins over the shield");
  assert.equal(overrides.length, 0);
});

test("an expired move yields to truth even while the fetch still shows the origin", () => {
  const moves = [move({ expiresAt: 100 })];
  const fetched = new Map([["SC-9001", "backlog"]]);

  const { moves: kept, overrides } = reconcilePendingMoves(moves, fetched, 200);

  assert.equal(kept.length, 0, "a move that never confirms must not stick forever");
  assert.equal(overrides.length, 0);
});

test("a card absent from the fetch is held without override", () => {
  const moves = [move()];
  const fetched = new Map();

  const { moves: kept, overrides } = reconcilePendingMoves(moves, fetched, 0);

  assert.equal(kept.length, 1, "an incomplete fetch does not undo the move");
  assert.equal(overrides.length, 0, "nothing to override when the card is absent");
});

// SC-2521 follow-up: the bulk Deploy button ships every review-passed card in a
// pane at once, moving them all to "done" optimistically, then runs ONE closing
// reconcile — the same unshielded optimistic-move-then-reconcile shape as a
// single drag, flickering identically. pendingMovesForBatch captures each
// card's ORIGIN stage so a stale post-ship fetch is held against the right
// origin, and the whole batch survives the closing reconcile.

test("SC-2521: pendingMovesForBatch captures each card's origin stage against one target", () => {
  const cards = [
    { key: "SC-1", stage: "verification" },
    { key: "SC-2", stage: "review" },
  ];

  const moves = pendingMovesForBatch(cards, "done", 1_000, 20_000);

  assert.deepEqual(moves, [
    { key: "SC-1", fromStage: "verification", toStage: "done", expiresAt: 21_000 },
    { key: "SC-2", fromStage: "review", toStage: "done", expiresAt: 21_000 },
  ]);
});

test("pendingMovesForBatch on an empty batch yields no shields", () => {
  assert.deepEqual(pendingMovesForBatch([], "done", 0, 20_000), []);
});

test("SC-2521: a bulk-shipped batch survives a stale post-ship reconcile", () => {
  // Two cards shipped to done; the closing reconcile fetches a board that
  // predates the ships becoming readable, so both still read at their origin.
  const moves = pendingMovesForBatch(
    [
      { key: "SC-1", stage: "verification" },
      { key: "SC-2", stage: "verification" },
    ],
    "done",
    0,
    20_000,
  );
  const stale = new Map([
    ["SC-1", "verification"],
    ["SC-2", "verification"],
  ]);

  const { moves: kept, overrides } = reconcilePendingMoves(moves, stale, 0);

  assert.equal(kept.length, 2, "the whole batch is held past the stale read");
  assert.deepEqual(
    overrides,
    [
      { key: "SC-1", toStage: "done" },
      { key: "SC-2", toStage: "done" },
    ],
    "each held card is pinned back to done",
  );
});

test("SC-2521: a card whose bulk ship failed is dropped and not re-pinned", () => {
  // The Deploy loop drops a card's shield when its transition throws; the next
  // reconcile must then let that card follow the fetched truth, not hold it.
  let moves = pendingMovesForBatch(
    [
      { key: "SC-1", stage: "verification" },
      { key: "SC-2", stage: "verification" },
    ],
    "done",
    0,
    20_000,
  );
  moves = dropPendingMove(moves, "SC-2"); // SC-2's Transition threw
  const fetched = new Map([
    ["SC-1", "verification"], // stale — still held
    ["SC-2", "verification"], // reverted — must NOT be re-pinned to done
  ]);

  const { moves: kept, overrides } = reconcilePendingMoves(moves, fetched, 0);

  assert.deepEqual(kept.map((m) => m.key), ["SC-1"]);
  assert.deepEqual(overrides, [{ key: "SC-1", toStage: "done" }]);
});

test("dropPendingMove removes only the given key", () => {
  const moves = [
    move({ key: "SC-9001" }),
    move({ key: "SC-9002" }),
  ];

  const remaining = dropPendingMove(moves, "SC-9001");

  assert.equal(remaining.length, 1);
  assert.equal(remaining[0].key, "SC-9002");
});
