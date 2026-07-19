import { test } from "node:test";
import assert from "node:assert/strict";
import { rangeCoversHistory, barPercents } from "../build/statsview.js";

// rangeCoversHistory decides whether the "history still filling" note shows: it
// is FALSE only while the daemon's uptime is shorter than the selected range.
test("rangeCoversHistory: fresh daemon under the range is not covered", () => {
  const now = Date.UTC(2026, 6, 19, 12, 0, 0);
  const twoHoursAgo = new Date(now - 2 * 60 * 60 * 1000).toISOString();
  assert.equal(rangeCoversHistory(twoHoursAgo, "24h", now), false);
});

test("rangeCoversHistory: long-running daemon covers the range", () => {
  const now = Date.UTC(2026, 6, 19, 12, 0, 0);
  const fortyDaysAgo = new Date(now - 40 * 24 * 60 * 60 * 1000).toISOString();
  assert.equal(rangeCoversHistory(fortyDaysAgo, "30d", now), true);
});

test("rangeCoversHistory: exactly at the boundary counts as covered", () => {
  const now = Date.UTC(2026, 6, 19, 12, 0, 0);
  const oneDayAgo = new Date(now - 24 * 60 * 60 * 1000).toISOString();
  assert.equal(rangeCoversHistory(oneDayAgo, "24h", now), true);
});

test("rangeCoversHistory: unknown start does not cry wolf", () => {
  assert.equal(rangeCoversHistory("not-a-date", "7d", Date.now()), true);
});

// barPercents normalizes to 0..100 against the max; an all-zero input stays all
// zero rather than dividing by zero.
test("barPercents normalizes against the max value", () => {
  assert.deepEqual(barPercents([1, 3, 0]), [33, 100, 0]);
});

test("barPercents on all-zero input is all zero", () => {
  assert.deepEqual(barPercents([0, 0]), [0, 0]);
});

test("barPercents on empty input is empty", () => {
  assert.deepEqual(barPercents([]), []);
});
