// Regression guard for SC-204 / bug 204: a long bug-card title must be
// clamped (ellipsis) and the card must contain its overflow so the title
// never paints over the card's rounded bottom border in the Bugs pane.
// The frontend is intentionally dependency-free (no DOM test runner), so this
// asserts the CSS source rules that prevent the overflow rather than rendering.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(resolve(here, "..", "static", "style.css"), "utf8");

// Strip CSS comments so commented-out or explanatory text never matches.
const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "");

// Extract the body of a `selector { ... }` rule (first match). Returns "" when
// the selector is absent, so a missing rule fails the containment assertions.
function ruleBody(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp(escaped + "\\s*\\{([^}]*)\\}");
  const m = stripped.match(re);
  return m ? m[1] : "";
}

test("bug-grid card title is line-clamped with an ellipsis", () => {
  const body = ruleBody(".column-body.bug-grid .card-title");
  assert.match(body, /-webkit-line-clamp:\s*\S+/, "title must set -webkit-line-clamp");
  assert.match(body, /-webkit-box-orient:\s*vertical/, "clamp needs vertical box orient");
  assert.match(body, /display:\s*-webkit-box/, "clamp needs display:-webkit-box");
  assert.match(body, /overflow:\s*hidden/, "clamped title must hide its overflow");
});

// SC-1656 regression: a fixed 1/N grid-auto-rows pins every card to the same
// height regardless of card count, leaving a large void when the tray is
// sparse. The row height must instead be content-adaptive.
test("bug-grid rows are content-adaptive, not a fixed 1/N fraction of the tray (SC-1656)", () => {
  const body = exactRuleBody(".column-body.bug-grid");
  const rows = body.match(/grid-auto-rows:\s*([^;]+);/);
  assert.ok(rows, ".column-body.bug-grid must set grid-auto-rows");
  assert.doesNotMatch(
    rows[1], /\/\s*\d+\s*\)/,
    "grid-auto-rows must not be a fixed 1/N fraction of the tray height (SC-1656)",
  );
  assert.match(
    rows[1], /minmax\([^;]*,\s*1fr\s*\)/,
    "grid-auto-rows must use minmax(min, 1fr) so rows expand to fill a sparse tray (SC-1656)",
  );
});

// SC-1656 regression: the clamp constant and the cell height must be coupled
// by layout (a shared --bug-title-lines token), not by assumption, so a cell
// can never be shorter than the clamped lines and slice a title mid-glyph.
test("bug-grid title clamp is coupled to the card min-height, not a bare constant (SC-1656)", () => {
  const clampBody = ruleBody(".column-body.bug-grid .card-title");
  const clamp = clampBody.match(/-webkit-line-clamp:\s*([^;]+);/);
  assert.ok(clamp, "title must set -webkit-line-clamp");
  assert.doesNotMatch(
    clamp[1].trim(), /^\d+$/,
    "-webkit-line-clamp must not be a bare integer disconnected from the cell height (SC-1656)",
  );
  assert.match(
    clamp[1], /var\(\s*--bug-title-lines\s*\)/,
    "-webkit-line-clamp must reference the shared --bug-title-lines token (SC-1656)",
  );

  const gridBody = exactRuleBody(".column-body.bug-grid");
  assert.match(
    gridBody, /--bug-title-lines:\s*\d+/,
    ".column-body.bug-grid must declare --bug-title-lines (SC-1656)",
  );
  assert.match(
    gridBody, /--bug-card-min-h:\s*calc\([^;]*var\(\s*--bug-title-lines\s*\)/,
    ".column-body.bug-grid must derive --bug-card-min-h from --bug-title-lines (SC-1656)",
  );
});

test("default-theme .card contains its overflow and resists shrinking", () => {
  const body = ruleBody(".card");
  assert.match(body, /overflow:\s*hidden/, ".card must clip content so text never crosses the border");
  // SC-155 guard: cards must not shrink below their content in an overflowing
  // flex column, otherwise the fancy theme's overflow:hidden squishes them.
  assert.match(body, /flex-shrink:\s*0/, ".card must set flex-shrink:0 so it keeps full height when the column overflows (SC-155)");
});

// SC-1451 regression: the board grid's fixed track minimums must fit the
// default window's board area so the rightmost (Ready to Deploy) column is
// never clipped off-screen on cold open. There is no DOM test runner here, so
// sum the CSS grid track floors (the board's intrinsic min-width) and assert it
// fits: default window 1280px (desktop/main.go App.Width) minus the 60px rail.
const WINDOW_WIDTH = 1280; // desktop/main.go: options.App.Width
const RAIL_WIDTH = 60;     // .rail width in style.css
const BOARD_AREA = WINDOW_WIDTH - RAIL_WIDTH; // 1220
const BOARD_PADDING = 12 * 2; // .board padding: 12px (both sides)
const GAP = 10;               // .board gap

// ruleBody() matches the selector as a substring, so a bare ".board" would
// wrongly match the compound ".app-main .board" rule that appears earlier in
// the file. Require the selector to start a rule (preceded by the start of
// the file, a prior rule's closing brace, or a selector-list comma) so it
// targets the exact standalone rule.
function exactRuleBody(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp("(?:^|[}\\n,])\\s*" + escaped + "\\s*\\{([^}]*)\\}", "m");
  const m = stripped.match(re);
  return m ? m[1] : "";
}

// Extract the grid-template-columns value from a rule body.
function gridTemplateColumns(selector) {
  const m = exactRuleBody(selector).match(/grid-template-columns:\s*([^;]+);/);
  return m ? m[1].trim() : "";
}

// Sum the px floors of a grid-template-columns value, expanding
// repeat(N, minmax(...)). A minmax() min in px is a hard floor; 0/fr/auto mins
// can shrink to nothing and count as 0 — exactly how an overflow:auto grid
// computes its intrinsic min-width. Returns { sum, count }.
function boardMinWidth(value) {
  const expanded = value.replace(
    /repeat\(\s*(\d+)\s*,\s*(minmax\([^)]*\))\s*\)/g,
    (_, n, track) => (track + " ").repeat(Number(n)),
  );
  const tracks = expanded.match(/minmax\([^)]*\)/g) || [];
  let sum = 0;
  for (const t of tracks) {
    const min = t.match(/minmax\(\s*([^,]+),/)[1].trim();
    const px = min.match(/^(\d+)px$/);
    sum += px ? Number(px[1]) : 0;
  }
  return { sum, count: tracks.length };
}

test("board grid fits the default window so the last column is not clipped (SC-1451)", () => {
  const value = gridTemplateColumns(".board");
  assert.ok(value, ".board must set grid-template-columns");
  const { sum, count } = boardMinWidth(value);
  const minWidth = sum + (count - 1) * GAP + BOARD_PADDING;
  assert.ok(
    minWidth <= BOARD_AREA,
    `board intrinsic min-width ${minWidth}px must fit the ${BOARD_AREA}px board ` +
      `area (window ${WINDOW_WIDTH} − ${RAIL_WIDTH}px rail) so 'Ready to Deploy' ` +
      `is not clipped on cold open (SC-1451)`,
  );
});

test("idea-space lanes are compressible so the Ideas track never clips inside (SC-1451)", () => {
  const value = gridTemplateColumns(".idea-space-grid");
  assert.ok(value, ".idea-space-grid must set grid-template-columns");
  const { sum } = boardMinWidth(value);
  assert.equal(
    sum, 0,
    "idea-space lanes must use minmax(0, …) floors so the five lanes shrink " +
      "with the Ideas track instead of forcing a ~500px inner min-width (SC-1451)",
  );
});

test("board exposes a persistent horizontal scroll affordance (SC-1451)", () => {
  assert.match(
    exactRuleBody(".board"), /scrollbar-gutter:\s*stable/,
    ".board must reserve a stable scrollbar gutter so scroll is discoverable",
  );
  assert.match(
    stripped, /\.board::-webkit-scrollbar\s*\{[^}]*height:\s*\d+px/,
    ".board must style ::-webkit-scrollbar so WebKitGTK shows a visible bar",
  );
});

// SC-1830 regression: badge colour answers "whose turn is it?", not severity.
// The machine-working failed-verdict badge must not wear the same amber as the
// needs-a-human decision badge.
test("failed-verdict badge uses the machine-working colour, not the decision amber (SC-1830)", () => {
  const fixing = ruleBody(".badge.fixing");
  const decision = ruleBody(".badge.decision");
  assert.match(fixing, /color:\s*var\(--turn-machine\)/, ".badge.fixing must use the gray machine-working token");
  assert.match(decision, /color:\s*var\(--turn-person\)/, ".badge.decision must use the needs-a-person token");
  assert.doesNotMatch(fixing, /var\(--turn-person\)/, "the fixing badge must not borrow the needs-a-person colour");
});

// SC-1830 inverse-sibling fix: terminal successes are green, not muted gray.
test("resolved (terminal-success) badge is green, not muted (SC-1830)", () => {
  const resolved = ruleBody(".badge.resolved");
  assert.match(resolved, /color:\s*var\(--turn-done\)/, ".badge.resolved is a terminal success — it must be green");
  assert.doesNotMatch(resolved, /var\(--muted\)/, "the muted-gray mistone must not be reintroduced");
});

// SC-2699: a pre-planning stop decision is a settled, informational outcome —
// never the amber "your turn" register and never a red failure. It shares the
// neutral machine-working tone, so a decided card reads as decided, not as a
// demand or a crash.
test("decided badge uses the neutral machine-working colour, not amber or red (SC-2699)", () => {
  const decided = ruleBody(".badge.decided");
  assert.match(decided, /color:\s*var\(--turn-machine\)/, ".badge.decided must use the neutral machine-working token");
  assert.doesNotMatch(decided, /var\(--turn-person\)/, "a settled decision must not wear the needs-a-person amber");
  assert.doesNotMatch(decided, /var\(--turn-error\)/, "a settled decision must not wear the failure red");
});

test("partial badge uses the machine register, not the needs-a-person amber (SC-2910)", () => {
  const partial = ruleBody(".badge.partial");
  assert.match(partial, /color:\s*var\(--turn-machine\)/, ".badge.partial must use the neutral machine-working token");
  assert.doesNotMatch(partial, /var\(--turn-person\)/, "a shipped-partial trace records scope, it asks nothing of a person");
});

// SC-3339: a not-mine card is dimmed but must stay fully interactive — the
// dimming is a hint, never a restriction. Unlike .degraded (which also locks
// the card with cursor:not-allowed), .card.not-mine must touch opacity only.
test("not-mine card is dimmed via opacity only, never locked like a degraded card (SC-3339)", () => {
  const body = ruleBody(".card.not-mine");
  assert.match(body, /opacity:\s*[\d.]+/, ".card.not-mine must set opacity");
  assert.doesNotMatch(body, /cursor:\s*not-allowed/, "not-mine must not disable the pointer like .degraded does");
  assert.doesNotMatch(body, /pointer-events:\s*none/, "not-mine must not block interaction");
});

// SC-3339: a card that is both degraded (fetch failure) and not-mine must
// keep the more restrictive .degraded dimming, so a degraded card never
// reads as LESS urgent than a healthy not-mine one.
test("a degraded not-mine card keeps the degraded (more restrictive) opacity, not the lighter not-mine one (SC-3339)", () => {
  const notMine = ruleBody(".card.not-mine").match(/opacity:\s*([\d.]+)/);
  const degraded = ruleBody(".card.degraded").match(/opacity:\s*([\d.]+)/);
  assert.ok(notMine, ".card.not-mine must set opacity");
  assert.ok(degraded, ".card.degraded must set opacity");

  const combined = ruleBody(".card.degraded.not-mine").match(/opacity:\s*([\d.]+)/);
  assert.ok(combined, ".card.degraded.not-mine must set an explicit opacity to break the cascade tie");
  assert.equal(
    Number(combined[1]), Number(degraded[1]),
    ".card.degraded.not-mine must resolve to .degraded's opacity, not .not-mine's lighter one",
  );
});

// SC-1830: the four "whose turn" semantic tokens must exist and alias the
// existing palette (gray=machine, amber=person, red=error, green=done).
test("whose-turn colour tokens are defined (SC-1830)", () => {
  for (const decl of [
    /--turn-machine:\s*var\(--muted\)/,
    /--turn-person:\s*var\(--running\)/,
    /--turn-error:\s*var\(--failed\)/,
    /--turn-done:\s*var\(--done\)/,
  ]) {
    assert.match(css, decl, `:root must define ${decl}`);
  }
});
