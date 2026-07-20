import { test } from "node:test";
import assert from "node:assert/strict";
import { ideationInputEnabled, shouldCloseIdeation } from "../build/board-ideation.js";

// SC-859 regression: after a ticket is created the ideation session reaches
// "done". The free-text form + send button must NOT be enabled in that terminal
// state, and the panel must auto-close. Both fail before board-ideation.ts exists.

test("done state disables the ideation input/send button (SC-859)", () => {
  assert.equal(ideationInputEnabled("done"), false);
});

test("input stays enabled for the non-terminal interactive states", () => {
  assert.equal(ideationInputEnabled("awaiting_reply"), true);
  assert.equal(ideationInputEnabled("none"), true);
  assert.equal(ideationInputEnabled("error"), true);
});

test("input is disabled while thinking or awaiting approval", () => {
  assert.equal(ideationInputEnabled("thinking"), false);
  assert.equal(ideationInputEnabled("awaiting_approval"), false);
});

test("done + createdKey triggers auto-close (SC-859)", () => {
  assert.equal(shouldCloseIdeation("done", "SC-42"), true);
});

test("done without a created key does not auto-close", () => {
  assert.equal(shouldCloseIdeation("done", undefined), false);
  assert.equal(shouldCloseIdeation("done", "   "), false);
});

test("non-terminal states never auto-close", () => {
  assert.equal(shouldCloseIdeation("awaiting_reply", "SC-42"), false);
  assert.equal(shouldCloseIdeation("thinking", "SC-42"), false);
  assert.equal(shouldCloseIdeation("error", "SC-42"), false);
});
