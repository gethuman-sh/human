import { test } from "node:test";
import assert from "node:assert/strict";
import { recreateAllowedFor, recreateConfirmBody } from "../build/board-recreate.js";

// SC-4923: Recreate belongs to Product Backlog alone. The Ideas lane already
// redrafts on a title change, and after planning the description is the input
// to a [human:plan] marker that a silent rewrite would invalidate.
test("recreateAllowedFor: Product Backlog feature cards only", () => {
  assert.equal(recreateAllowedFor("product"), true);
  // The real column set is QUEUES in board-queue.ts.
  for (const queue of ["ideas", "engineering", "building", "deploy", ""]) {
    assert.equal(recreateAllowedFor(queue), false, `queue ${queue} must not offer recreate`);
  }
});

// Bug and security descriptions come from their own triage pipelines — the
// same cards the description editor already excludes.
test("recreateAllowedFor: never on bug or security cards", () => {
  assert.equal(recreateAllowedFor("product", true, false), false);
  assert.equal(recreateAllowedFor("product", false, true), false);
  assert.equal(recreateAllowedFor("product", false, false), true);
});

// The dialog is the whole concurrency story: a concurrent edit in another pane
// is caught by the user reading what is about to be discarded.
test("recreateConfirmBody names the key and says the description is replaced", () => {
  const body = recreateConfirmBody("SC-42");
  assert.ok(body.includes("SC-42"));
  assert.match(body, /replaced/);
  assert.match(body, /discarded/);
});
