import { test } from "node:test";
import assert from "node:assert/strict";
import {
  rangeCoversHistory,
  barPercents,
  initStatsView,
  showStats,
  contextTokens,
  fmtUSD,
} from "../build/statsview.js";

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
        tokens: { input: 0, output: 0, cacheCreate: 0, cacheRead: 0, costUSD: 0 },
        toolCalls: { total: 0, success: 0, failure: 0 },
        audit: { total: 0, success: 0, failure: 0 },
        agentRuns: { total: 0, success: 0, failure: 0 },
        ticketCosts: [],
        tokensByModel: [],
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

// SC-1316: the stats page must render a "Tokens by model" panel from the
// tokensByModel payload so the tier split (opus/sonnet/haiku) is visible.
test("showStats renders the Tokens by model panel from tokensByModel (SC-1316)", async () => {
  const el = installStatsDOM();
  initStatsView(() => ({
    Stats: () =>
      Promise.resolve({
        range: "24h",
        generatedAt: "",
        daemonStartedAt: "",
        tokens: { input: 0, output: 0, cacheCreate: 0, cacheRead: 0, costUSD: 0 },
        toolCalls: { total: 0, success: 0, failure: 0 },
        audit: { total: 0, success: 0, failure: 0 },
        agentRuns: { total: 0, success: 0, failure: 0 },
        ticketCosts: [],
        tokensByModel: [{ model: "opus 4.8", input: 100, output: 200, cacheCreate: 50, cacheRead: 30, costUSD: 0 }],
        toolsByTool: [],
        auditByDay: [],
        networkDecisions: [],
      }),
  }));

  await showStats();

  assert.match(el.innerHTML, /Tokens by model/, "the panel title renders");
  assert.match(el.innerHTML, /opus 4\.8/, "the model row renders");
});

// SC-3497: the stats page shows which WORK spent the range's tokens, ranked by
// cost — the question the money is attached to. It replaced the per-hour burn,
// which showed only when tokens were spent, and that panel must be gone rather
// than merely unlinked.
test("showStats renders the Cost by ticket panel from ticketCosts (SC-3497)", async () => {
  const el = installStatsDOM();
  initStatsView(() => ({
    Stats: () =>
      Promise.resolve({
        range: "24h",
        generatedAt: "",
        daemonStartedAt: "",
        tokens: { input: 0, output: 0, cacheCreate: 0, cacheRead: 0, costUSD: 0 },
        toolCalls: { total: 0, success: 0, failure: 0 },
        audit: { total: 0, success: 0, failure: 0 },
        agentRuns: { total: 0, success: 0, failure: 0 },
        ticketCosts: [
          { ticket: "SC-637", costUSD: 1.5, contextCostUSD: 1.14, answersCostUSD: 0.36, outputTokens: 14449, contextTokens: 558705, durationMs: 226947 },
          { ticket: "SC-3321", costUSD: 0.05, contextCostUSD: 0.04, answersCostUSD: 0.01, outputTokens: 158, contextTokens: 18317, durationMs: 4000 },
        ],
        tokensByModel: [],
        toolsByTool: [],
        auditByDay: [],
        networkDecisions: [],
      }),
  }));

  await showStats();

  assert.match(el.innerHTML, /Cost by ticket/, "the panel title renders");
  assert.match(el.innerHTML, /SC-637/, "the most expensive ticket renders");
  assert.match(el.innerHTML, /\$1\.50/, "the ticket's cost renders as money");
  assert.doesNotMatch(el.innerHTML, /Tokens per hour/, "the per-hour burn panel it replaced must be gone");
});

// A ticket whose rows carry duration but no priced tokens still lists, at
// $0.00: hiding it would claim the work never ran (SC-3440 left 520 such rows).
test("showStats lists an unpriced ticket rather than dropping it (SC-3497)", async () => {
  const el = installStatsDOM();
  initStatsView(() => ({
    Stats: () =>
      Promise.resolve({
        range: "24h",
        generatedAt: "",
        daemonStartedAt: "",
        tokens: { input: 0, output: 0, cacheCreate: 0, cacheRead: 0, costUSD: 0 },
        toolCalls: { total: 0, success: 0, failure: 0 },
        audit: { total: 0, success: 0, failure: 0 },
        agentRuns: { total: 0, success: 0, failure: 0 },
        ticketCosts: [
          { ticket: "SC-BLIND", costUSD: 0, contextCostUSD: 0, answersCostUSD: 0, outputTokens: 0, contextTokens: 0, durationMs: 6387000 },
        ],
        tokensByModel: [],
        toolsByTool: [],
        auditByDay: [],
        networkDecisions: [],
      }),
  }));

  await showStats();

  assert.match(el.innerHTML, /SC-BLIND/, "an unpriced ticket still appears");
  assert.match(el.innerHTML, /\$0\.00/, "priced at zero rather than hidden");
});

// SC-2549: context is everything spent establishing/re-reading context
// (input + cache-create + cache-read) — output is deliberately excluded, since
// separating the two is the point of the split.
test("contextTokens sums input + cacheCreate + cacheRead (not output)", () => {
  assert.equal(contextTokens({ input: 278, cacheCreate: 614446, cacheRead: 7849469 }), 8464193);
});

test("fmtUSD formats two decimals with a $ prefix", () => {
  assert.equal(fmtUSD(6.14), "$6.14");
  assert.equal(fmtUSD(0), "$0.00");
});
