import { test } from "node:test";
import assert from "node:assert/strict";
import { reconcilePending, dropPending } from "../build/board-pending.js";

// SC-1691 regression: two placeholders sharing a title used to both be
// cleared when only one of the underlying tickets was created. Reconciling
// by key means only the placeholder whose key was actually fetched hands
// over.
test("SC-1691: duplicate-title placeholders reconcile independently by key", () => {
  const pending = [
    { title: "Fix the login bug", key: "SC-2001" },
    { title: "Fix the login bug", key: "SC-2002" },
  ];
  const fetchedKeys = new Set(["SC-2001"]);
  const fetchedTitles = new Set(["Fix the login bug"]);

  const remaining = reconcilePending(pending, fetchedKeys, fetchedTitles);

  assert.equal(remaining.length, 1);
  assert.equal(remaining[0].key, "SC-2002");
});

test("a keyed placeholder is not cleared by a same-title card with a different key", () => {
  const pending = [{ title: "Same title", key: "SC-3001" }];
  const fetchedKeys = new Set(["SC-3002"]);
  const fetchedTitles = new Set(["Same title"]);

  const remaining = reconcilePending(pending, fetchedKeys, fetchedTitles);

  assert.equal(remaining.length, 1);
});

test("a normalized title still hands over when the key matches", () => {
  const pending = [{ title: "raw title", key: "SC-4001" }];
  const fetchedKeys = new Set(["SC-4001"]);
  const fetchedTitles = new Set(["Raw Title (normalized)"]);

  const remaining = reconcilePending(pending, fetchedKeys, fetchedTitles);

  assert.equal(remaining.length, 0);
});

test("a keyless placeholder falls back to title matching", () => {
  const pending = [{ title: "Lost response ticket" }];
  const fetchedKeys = new Set(["SC-5001"]);
  const fetchedTitles = new Set(["Lost response ticket"]);

  const remaining = reconcilePending(pending, fetchedKeys, fetchedTitles);

  assert.equal(remaining.length, 0);
});

test("dropPending removes only the failed placeholder by identity", () => {
  const a = { title: "Same title" };
  const b = { title: "Same title" };
  const pending = [a, b];

  const remaining = dropPending(pending, a);

  assert.deepEqual(remaining, [b]);
  assert.equal(remaining.length, 1);
});
