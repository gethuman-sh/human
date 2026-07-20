// Regression guard for SC-881: the ideation panel's done state must not
// dead-end at "Created SC-XXX" — it has to offer the created ticket's forward
// move ("Move to feature", the backlog→planning transition a drag onto the
// Engineering backlog would launch). The frontend is intentionally
// dependency-free (no DOM test runner), so this asserts the source wiring
// rather than rendering, like style.test.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const ts = readFileSync(resolve(here, "..", "src", "board.ts"), "utf8");
const css = readFileSync(resolve(here, "..", "static", "style.css"), "utf8");

test("done state renders an action, not a bare created line", () => {
  assert.match(
    ts,
    /state === "done"\)\s*\{\s*renderIdeationDone\(/,
    "the done branch must delegate to renderIdeationDone so the state carries an affordance",
  );
});

test("Move to feature launches the backlog→planning transition", () => {
  const fn = ts.slice(ts.indexOf("function renderIdeationDone"));
  assert.ok(fn.length > 0, "renderIdeationDone must exist");
  const body = fn.slice(0, fn.indexOf("\n}"));
  assert.match(body, /ideation-move-feature/, "the action button must carry its style hook");
  assert.match(body, /transition\([^)]*"planning"\)/, "the button must launch the planning transition");
  assert.match(body, /ideationMovedKeys/, "a spent move must not re-arm (double agent launch)");
  assert.match(body, /dockerAvailable/, "the launch must gate on Docker like every queue drop");
});

test("the action is styled as a right-aligned status-line button", () => {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const m = stripped.match(/\.ideation-move-feature\s*\{([^}]*)\}/);
  assert.ok(m, ".ideation-move-feature rule must exist");
  assert.match(m[1], /margin-left:\s*auto/, "button must right-align inside the status line");
  assert.match(stripped, /\.ideation-move-feature:disabled\s*\{[^}]*opacity/, "spent/blocked state needs a visible disabled style");
});
