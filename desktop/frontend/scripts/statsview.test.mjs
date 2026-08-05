import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  rangeCoversHistory,
  barPercents,
  initStatsView,
  showStats,
  contextTokens,
  fmtUSD,
  statsFromPayload,
} from "../build/statsview.js";

// Minimal DOM stub: the view only ever reaches for #stats and sets innerHTML on
// it, so a one-element document is enough to exercise render() from node.
function installStatsDOM() {
  const el = { innerHTML: "", querySelectorAll: () => [] };
  globalThis.document = { getElementById: (id) => (id === "stats" ? el : null) };
  return el;
}

// A host that rejects the markup handed to it stands in for ANY fault raised
// while the page is being drawn. Once the payload is normalized no payload
// shape can throw inside render() any more, so a rejecting host is how the
// render boundary — which exists for the faults nobody has thought of yet —
// stays exercised rather than becoming untested dead code.
function installThrowingStatsDOM(shouldThrow) {
  const el = {
    html: "",
    textContent: "",
    querySelectorAll: () => [],
    get innerHTML() {
      return this.html;
    },
    set innerHTML(markup) {
      if (shouldThrow(markup)) throw new Error("host rejected the markup");
      this.html = markup;
    },
  };
  globalThis.document = { getElementById: (id) => (id === "stats" ? el : null) };
  return el;
}

// A complete payload carrying exactly ONE row in every list, each with a
// sentinel value unique to its panel: a panel that vanished is then visible as
// the absence of its own sentinel rather than hiding in an already-empty panel.
// daemonStartedAt is far in the past so the "history still filling" note never
// appears and can never be mistaken for panel content.
function fullPayload() {
  return {
    range: "24h",
    generatedAt: "2026-03-17T09:00:00Z",
    daemonStartedAt: "2020-01-01T00:00:00Z",
    tokens: { input: 11, output: 22, cacheCreate: 33, cacheRead: 44, costUSD: 5.5 },
    toolCalls: { total: 9, success: 8, failure: 1 },
    audit: { total: 7, success: 6, failure: 1 },
    agentRuns: { total: 5, success: 4, failure: 1 },
    ticketCosts: [
      {
        ticket: "SENTINELTICKET",
        costUSD: 1.5,
        contextCostUSD: 1.14,
        answersCostUSD: 0.36,
        outputTokens: 14449,
        contextTokens: 558705,
        durationMs: 226947,
      },
    ],
    tokensByModel: [{ model: "sentinelmodel", input: 100, output: 200, cacheCreate: 50, cacheRead: 30, costUSD: 0.25 }],
    toolsByTool: [{ tool_name: "SentinelTool", count: 42 }],
    auditByDay: [{ day: "2026-09-14", approved: 3, denied: 2, failed: 1 }],
    networkDecisions: [{ source: "proxy", status: "allowed", host: "sentinelhost.example", count: 2, last_seen: "" }],
  };
}

// Which payload field feeds which panel, and the sentinel that proves the panel
// drew its row.
const PANELS = {
  ticketCosts: { title: "Cost by ticket", sentinel: "SENTINELTICKET" },
  tokensByModel: { title: "Tokens by model", sentinel: "sentinelmodel" },
  toolsByTool: { title: "Tool calls by tool", sentinel: "SentinelTool" },
  auditByDay: { title: "Audit outcomes by day", sentinel: "09-14" },
  networkDecisions: { title: "Network decisions", sentinel: "sentinelhost.example" },
};

// The markup of one panel, so "this panel is empty" can be asserted without the
// neighbouring panels' content answering for it.
function panelHtml(html, title) {
  const chunk = html.split(`<div class="stats-panel">`).find((c) => c.includes(`<span>${title}</span>`));
  assert.ok(chunk, `panel "${title}" is present in the page`);
  return chunk;
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

// SC-3508: the daemon and the board app are separate builds, so the app can
// outlive a field the daemon stopped sending (or vice versa). A field the page
// no longer receives must cost exactly its own panel — not the whole page. The
// loop runs over EVERY top-level field because the guarantee is about the
// payload class, not about the one field (ticketCosts) that exposed it.
for (const field of Object.keys(fullPayload())) {
  test(`SC-3508: a payload missing ${field} still renders the rest of the page`, async () => {
    const el = installStatsDOM();
    const payload = fullPayload();
    delete payload[field];
    initStatsView(() => ({ Stats: () => Promise.resolve(payload) }));

    await showStats();

    for (const [panelField, panel] of Object.entries(PANELS)) {
      const body = panelHtml(el.innerHTML, panel.title);
      if (panelField === field) {
        assert.ok(!body.includes(panel.sentinel), `${panel.title} has no row to draw without ${field}`);
        assert.ok(body.includes("No data yet"), `${panel.title} degrades to an empty panel, not a blank page`);
        continue;
      }
      assert.ok(body.includes(panel.sentinel), `${panel.title} still renders its row without ${field}`);
    }
    assert.doesNotMatch(el.innerHTML, /Loading/, "the page is not left on the first paint");
    assert.doesNotMatch(el.innerHTML, /class="banner"/, "a missing field is not an error, it is an empty panel");
  });
}

// SC-3508: normalization is what makes the guarantee hold for a field nobody
// has added yet — the payload comes through one door and leaves it fully shaped.
test("SC-3508: statsFromPayload zeroes an empty payload rather than passing undefined through", () => {
  const s = statsFromPayload({});

  assert.deepEqual(s.ticketCosts, []);
  assert.deepEqual(s.tokensByModel, []);
  assert.deepEqual(s.toolsByTool, []);
  assert.deepEqual(s.auditByDay, []);
  assert.deepEqual(s.networkDecisions, []);
  assert.deepEqual(s.tokens, { input: 0, output: 0, cacheCreate: 0, cacheRead: 0, costUSD: 0 });
  assert.deepEqual(s.toolCalls, { total: 0, success: 0, failure: 0 });
  assert.deepEqual(s.audit, { total: 0, success: 0, failure: 0 });
  assert.deepEqual(s.agentRuns, { total: 0, success: 0, failure: 0 });
  assert.equal(s.daemonStartedAt, "");
  assert.equal(s.range, "");
  assert.equal(s.generatedAt, "");
});

// A field whose TYPE changed across builds is the same mismatch as a field that
// vanished: `?? []` would pass a string straight through to rows.length.
test("SC-3508: statsFromPayload coerces a field whose type changed", () => {
  const s = statsFromPayload({ ticketCosts: "nope", tokens: 7, toolsByTool: [null, { tool_name: "Bash" }] });

  assert.deepEqual(s.ticketCosts, [], "a list that became a string is not a list");
  assert.equal(s.tokens.costUSD, 0, "an object that became a number reads as zero");
  assert.equal(s.toolsByTool.length, 2, "rows survive individually");
  assert.deepEqual(s.toolsByTool[0], { tool_name: "", count: 0 }, "a null row is filled in, never dereferenced");
  assert.deepEqual(s.toolsByTool[1], { tool_name: "Bash", count: 0 }, "a row missing a field keeps what it has");
});

test("SC-3508: statsFromPayload survives a null or undefined payload", () => {
  for (const raw of [null, undefined, "nonsense", 42]) {
    const s = statsFromPayload(raw);
    assert.deepEqual(s.ticketCosts, []);
    assert.deepEqual(s.networkDecisions, []);
    assert.equal(s.tokens.output, 0);
  }
});

// An unknown extra field is the other direction of the same build mismatch: the
// daemon ships something this app has never heard of and the page is unmoved.
test("SC-3508: statsFromPayload ignores a field the app has never heard of", () => {
  const raw = fullPayload();
  raw.somethingNew = { nested: [1, 2, 3] };

  const s = statsFromPayload(raw);

  assert.equal(s.somethingNew, undefined, "unknown fields do not reach the render path");
  assert.equal(s.ticketCosts[0].ticket, "SENTINELTICKET", "known fields are untouched by the stranger");
});

// SC-3508: the second half of the fix. Normalization removes the payload faults
// we know about; the boundary is what keeps ANY remaining draw-time fault a
// banner instead of a page frozen on its first paint.
test("SC-3508: a fault while drawing shows the banner instead of a blank page", async () => {
  const el = installThrowingStatsDOM((markup) => markup.includes("stats-panels"));
  initStatsView(() => ({ Stats: () => Promise.resolve(fullPayload()) }));

  await showStats();

  assert.match(el.innerHTML, /class="banner"/, "the fault surfaces as the same banner a failed fetch produces");
  assert.match(el.innerHTML, /out of date/, "the banner names the remedy: the app is stale, rebuild it");
  assert.match(el.innerHTML, /stats-range-btn/, "the range switch stays live under the banner");
  assert.doesNotMatch(el.innerHTML, /Loading/, "the page never stays on the first paint");
});

test("SC-3508: a host that rejects every write still gets a plain-text message", async () => {
  const el = installThrowingStatsDOM(() => true);
  initStatsView(() => ({ Stats: () => Promise.resolve(fullPayload()) }));

  await showStats();

  assert.match(el.textContent, /out of date/, "the last resort is text, never an empty page");
});

// A fault that escaped showStats() as a rejected promise is what killed the
// poll loop's later ticks; every tick must stand on its own.
test("SC-3508: three consecutive ticks on a faulting draw never wedge the poll loop", async () => {
  const el = installThrowingStatsDOM((markup) => markup.includes("stats-panels"));
  initStatsView(() => ({ Stats: () => Promise.resolve(fullPayload()) }));

  for (let tick = 0; tick < 3; tick++) {
    await showStats();
    assert.match(el.innerHTML, /class="banner"/, `tick ${tick + 1} still paints the banner`);
  }
});

// Class-level guard, the board-error-guard.test.mjs shape: the behavioural
// tests above prove one door is normalized, this proves there is only one door.
// dist/ is scanned as well because it is checked in and embedded via
// `//go:embed all:frontend/dist`, so a dist/ lagging a fixed src/ still ships
// the blank page to users.
const here = dirname(fileURLToPath(import.meta.url));
for (const [label, path] of [
  ["src/statsview.ts", resolve(here, "..", "src", "statsview.ts")],
  ["dist/statsview.js", resolve(here, "..", "dist", "statsview.js")],
]) {
  test(`SC-3508: the fetched payload is normalized before anything renders it (${label})`, () => {
    const source = readFileSync(path, "utf8");
    assert.ok(
      source.includes("latest = statsFromPayload("),
      `${label} must assign the payload through statsFromPayload, not raw`,
    );
    assert.ok(!/latest!/.test(source), `${label} must not re-assert a payload the render path can no longer trust`);
  });
}
