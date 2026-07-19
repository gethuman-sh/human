import { test } from "node:test";
import assert from "node:assert/strict";
import { rangeCoversHistory, barPercents, initStatsView, showStats } from "../build/statsview.js";

// Minimal DOM stub: the view only ever reaches for #stats and sets innerHTML on
// it, so a one-element document is enough to exercise render() from node.
function installStatsDOM() {
  const el = { innerHTML: "", querySelectorAll: () => [] };
  globalThis.document = { getElementById: (id) => (id === "stats" ? el : null) };
  return el;
}

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

// SC-671: the view must paint its header and a Loading state the instant it
// activates — synchronously, before the Stats() fetch resolves. A never-resolving
// Stats() proves render() ran before the await, not after it.
test("showStats paints Loading synchronously before Stats() resolves (SC-671)", () => {
  const el = installStatsDOM();
  initStatsView(() => ({ Stats: () => new Promise(() => {}) }));

  void showStats();

  assert.notEqual(el.innerHTML, "", "#stats is painted synchronously");
  assert.match(el.innerHTML, /Loading/, "the Loading state shows before data arrives");
});

// SC-671: overlapping showStats calls (a poll tick landing while the previous
// fetch is still in flight, or a range switch mid-fetch) must not stack Stats()
// calls. The second call coalesces into a single follow-up fetch once the first
// resolves — never a second concurrent walk on the daemon.
test("in-flight guard: overlapping showStats calls do not stack Stats() calls (SC-671)", async () => {
  installStatsDOM();

  let calls = 0;
  let release;
  const gate = new Promise((r) => (release = r));
  initStatsView(() => ({
    Stats: () => {
      calls += 1;
      return gate.then(() => ({
        range: "24h",
        generatedAt: "",
        daemonStartedAt: "",
        tokens: { fresh: 0, cacheRead: 0 },
        toolCalls: { total: 0, success: 0, failure: 0 },
        audit: { total: 0, success: 0, failure: 0 },
        agentRuns: { total: 0, success: 0, failure: 0 },
        tokensPerHour: [],
        toolsByTool: [],
        auditByDay: [],
        networkDecisions: [],
      }));
    },
  }));

  const first = showStats();
  const second = showStats();
  assert.equal(calls, 1, "the second call does not start a second concurrent fetch");

  release();
  await Promise.all([first, second]);
  assert.equal(calls, 2, "the overlapping call coalesces into exactly one follow-up fetch");
});
