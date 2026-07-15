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
  assert.match(body, /-webkit-line-clamp:\s*\d+/, "title must set -webkit-line-clamp");
  assert.match(body, /-webkit-box-orient:\s*vertical/, "clamp needs vertical box orient");
  assert.match(body, /display:\s*-webkit-box/, "clamp needs display:-webkit-box");
  assert.match(body, /overflow:\s*hidden/, "clamped title must hide its overflow");
});

test("default-theme .card contains its overflow", () => {
  const body = ruleBody(".card");
  assert.match(body, /overflow:\s*hidden/, ".card must clip content so text never crosses the border");
});
