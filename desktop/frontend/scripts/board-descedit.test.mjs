import { test } from "node:test";
import assert from "node:assert/strict";
import {
  descEditInputEnabled,
  descEditApplyEnabled,
  descEditAllowedFor,
  buildDescriptionPreview,
  descEditShouldDiscardOnClose,
} from "../build/board-descedit.js";

// SC-2873: the Product-Backlog chat-assisted description editor. These pure
// helpers gate the modal's input/Apply controls and resolve which text the
// left pane shows (saved vs. unsaved proposed rewrite).

test("chat input is enabled only while awaiting a reply or after an error", () => {
  assert.equal(descEditInputEnabled("awaiting_reply"), true);
  assert.equal(descEditInputEnabled("error"), true);
});

test("chat input is disabled while thinking, applied, or with no session", () => {
  assert.equal(descEditInputEnabled("thinking"), false);
  assert.equal(descEditInputEnabled("applied"), false);
  assert.equal(descEditInputEnabled("none"), false);
});

test("apply is enabled only with a live proposal, not mid-turn, not already applied", () => {
  assert.equal(descEditApplyEnabled("awaiting_reply", "text"), true);
  assert.equal(descEditApplyEnabled("thinking", "text"), false);
  assert.equal(descEditApplyEnabled("applied", "text"), false);
  assert.equal(descEditApplyEnabled("awaiting_reply", ""), false);
  assert.equal(descEditApplyEnabled("awaiting_reply", undefined), false);
});

test("buildDescriptionPreview shows the saved description with no live proposal", () => {
  assert.deepEqual(buildDescriptionPreview("saved text", undefined, "awaiting_reply"), {
    text: "saved text",
    isPreview: false,
  });
});

test("buildDescriptionPreview shows the live proposal as an unsaved preview", () => {
  assert.deepEqual(buildDescriptionPreview("saved", "new rewrite", "awaiting_reply"), {
    text: "new rewrite",
    isPreview: true,
  });
});

test("buildDescriptionPreview folds the proposal into saved text once applied", () => {
  assert.deepEqual(buildDescriptionPreview("saved", "new rewrite", "applied"), {
    text: "new rewrite",
    isPreview: false,
  });
});

// AC6: closing the modal without Apply/Save must discard the pending session
// so a later reopen never reattaches to a stale proposal or chat history.
test("descEditShouldDiscardOnClose discards a live awaiting_reply or thinking or error session", () => {
  assert.equal(descEditShouldDiscardOnClose("awaiting_reply", "sess-1"), true);
  assert.equal(descEditShouldDiscardOnClose("thinking", "sess-1"), true);
  assert.equal(descEditShouldDiscardOnClose("error", "sess-1"), true);
});

test("descEditShouldDiscardOnClose is a no-op once applied — that lifecycle already ended", () => {
  assert.equal(descEditShouldDiscardOnClose("applied", "sess-1"), false);
});

test("descEditShouldDiscardOnClose is a no-op with no session to discard", () => {
  assert.equal(descEditShouldDiscardOnClose("none", undefined), false);
  assert.equal(descEditShouldDiscardOnClose("awaiting_reply", undefined), false);
});

// SC-4520: promotion opens the editor on a card the board is still rendering in
// Ideas — the labels have come off the ticket, but no refetch has happened yet.
test("the lane gate admits a just-promoted Ideas card", () => {
  assert.equal(descEditAllowedFor("ideas", false, false, true), true);
});

test("a click on an Ideas card opens nothing", () => {
  assert.equal(descEditAllowedFor("ideas"), false);
});

test("a Product-Backlog feature card opens on a plain click", () => {
  assert.equal(descEditAllowedFor("product"), true);
});

test("bugs and security tickets are never description-edited, promoted or not", () => {
  assert.equal(descEditAllowedFor("product", true), false);
  assert.equal(descEditAllowedFor("product", false, true), false);
  assert.equal(descEditAllowedFor("ideas", true, false, true), false);
});
