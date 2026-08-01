import { test } from "node:test";
import assert from "node:assert/strict";
import { applyPendingMoves } from "../build/board-pending.js";

const card = (key, stage, state = "idle") => ({ key, stage, state });
const move = (key, from, to, expiresAt = 1000) => ({ key, from, to, state: "running", expiresAt });

// The reported failure: the fetch answers from before the drop, still showing
// the card where it came from, and replacing the card with that answer undoes
// what the person just did.
test("a fetch that has not caught up does not undo the move", () => {
  const cards = [card("SC-1", "bugs:grid")];

  const out = applyPendingMoves(cards, [move("SC-1", "bugs:grid", "implementation")], 0);

  assert.equal(out.cards[0].stage, "implementation");
  assert.equal(out.cards[0].state, "running");
  assert.equal(out.moves.length, 1, "still unconfirmed, so still held");
});

// Confirmation ends the hold — nothing is suppressed after that.
test("the destination confirms the move and releases the hold", () => {
  const cards = [card("SC-1", "implementation", "running")];

  const out = applyPendingMoves(cards, [move("SC-1", "bugs:grid", "implementation")], 0);

  assert.equal(out.cards[0].stage, "implementation");
  assert.deepEqual(out.moves, [], "confirmed moves are not held");
});

// The machine changing its mind must win. A failure, or any other stage, is a
// real answer and not a stale one.
test("the machine moving the card elsewhere ends the hold", () => {
  const cards = [card("SC-1", "verification", "failed")];

  const out = applyPendingMoves(cards, [move("SC-1", "bugs:grid", "implementation")], 0);

  assert.equal(out.cards[0].stage, "verification", "the machine's answer stands");
  assert.equal(out.cards[0].state, "failed");
  assert.deepEqual(out.moves, []);
});

// A move nothing ever confirms must not persist as a comfortable lie.
test("an unconfirmed move expires back to the truth", () => {
  const cards = [card("SC-1", "bugs:grid")];

  const out = applyPendingMoves(cards, [move("SC-1", "bugs:grid", "implementation", 1000)], 5000);

  assert.equal(out.cards[0].stage, "bugs:grid", "expired: the fetch wins");
  assert.deepEqual(out.moves, []);
});

// A card that left the board entirely is not resurrected by a held move.
test("a card absent from the fetch ends the hold", () => {
  const out = applyPendingMoves([card("SC-9", "backlog")], [move("SC-1", "bugs:grid", "implementation")], 0);

  assert.equal(out.cards.length, 1);
  assert.deepEqual(out.moves, []);
});

// Only the moved card is touched.
test("other cards are left exactly as fetched", () => {
  const cards = [card("SC-1", "bugs:grid"), card("SC-2", "bugs:grid")];

  const out = applyPendingMoves(cards, [move("SC-1", "bugs:grid", "implementation")], 0);

  assert.equal(out.cards[1].stage, "bugs:grid");
  assert.equal(out.cards[1].state, "idle");
});

// No moves in flight is the common case and must cost nothing.
test("no pending moves returns the fetch untouched", () => {
  const cards = [card("SC-1", "bugs:grid")];

  const out = applyPendingMoves(cards, [], 0);

  assert.equal(out.cards[0].stage, "bugs:grid");
  assert.deepEqual(out.moves, []);
});
