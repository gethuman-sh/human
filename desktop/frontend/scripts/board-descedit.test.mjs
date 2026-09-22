import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  descEditInputEnabled,
  descEditApplyEnabled,
  descEditAllowedFor,
  descEditSendDefers,
  descEditHasUnappliedRewrite,
  descEditStatusLine,
  chatInputHeight,
  buildDescriptionPreview,
  descEditShouldDiscardOnClose,
  draftNotice,
} from "../build/board-descedit.js";

// SC-2873: the Product-Backlog chat-assisted description editor. These pure
// helpers gate the modal's input/Apply controls and resolve which text the
// left pane shows (saved vs. unsaved proposed rewrite).

test("chat input is live whenever there is a session to type into", () => {
  assert.equal(descEditInputEnabled("awaiting_reply"), true);
  assert.equal(descEditInputEnabled("error"), true);
  // SC-5033: a correction that occurs to you mid-answer must be typeable; the
  // message is held by descEditSendDefers rather than refused at the keyboard.
  assert.equal(descEditInputEnabled("thinking"), true);
});

test("chat input is disabled with no session and after applying", () => {
  assert.equal(descEditInputEnabled("applied"), false);
  assert.equal(descEditInputEnabled("none"), false);
});

// SC-5033: the input stays live through a turn, so the rules that used to be
// one predicate are now two — may you type (descEditInputEnabled), and does
// what you typed go out now or wait (descEditSendDefers). The daemon refuses a
// reply to a session that is not awaiting one, which is why waiting exists.
test("a message typed mid-turn is held, not sent", () => {
  assert.equal(descEditSendDefers("thinking"), true);
  assert.equal(descEditSendDefers("awaiting_reply"), false);
  assert.equal(descEditSendDefers("error"), false);
});

// The dismiss path asks before destroying work the dialog itself labels
// "Proposed rewrite (unsaved)" — a stray click on the backdrop used to cost the
// whole session.
test("an unapplied rewrite is what makes leaving ask first", () => {
  assert.equal(descEditHasUnappliedRewrite("awaiting_reply", "new text"), true);
  assert.equal(descEditHasUnappliedRewrite("thinking", "new text"), true);
  assert.equal(descEditHasUnappliedRewrite("applied", "new text"), false);
  assert.equal(descEditHasUnappliedRewrite("awaiting_reply", "   "), false);
  assert.equal(descEditHasUnappliedRewrite("awaiting_reply", undefined), false);
});

// The status line is the only place a held message is visible: the transcript
// cannot show it, because it has not been sent.
test("descEditStatusLine names a held message while thinking", () => {
  const line = descEditStatusLine("thinking", undefined, true);
  assert.equal(line.kind, "info");
  assert.match(line.text, /sends when this answer lands/);
});

test("descEditStatusLine is plain while thinking with nothing held", () => {
  assert.deepEqual(descEditStatusLine("thinking", undefined, false), { text: "Thinking…", kind: "info" });
});

test("descEditStatusLine prefers the daemon's error text and falls back", () => {
  assert.deepEqual(descEditStatusLine("error", "boom", false), { text: "boom", kind: "error" });
  assert.deepEqual(descEditStatusLine("error", undefined, false), {
    text: "Description chat failed",
    kind: "error",
  });
});

test("descEditStatusLine says Saved after applying and is silent when idle", () => {
  assert.deepEqual(descEditStatusLine("applied", undefined, false), { text: "Saved.", kind: "info" });
  assert.deepEqual(descEditStatusLine("awaiting_reply", undefined, false), { text: "", kind: "none" });
});

// The ceiling comes from CSS (.chat-form textarea max-height) and is passed in,
// so the growth rule has exactly one copy of the number and this helper has
// none. A missing/unparseable max-height must not collapse the box to zero.
test("chatInputHeight grows to the content and caps at the ceiling", () => {
  assert.equal(chatInputHeight(48, 124), 48);
  assert.equal(chatInputHeight(400, 124), 124);
});

test("chatInputHeight survives a missing ceiling", () => {
  assert.equal(chatInputHeight(400, Number.NaN), 400);
  assert.equal(chatInputHeight(400, 0), 400);
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

// SC-4608: promotion opens the editor on a card the board is still rendering in
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

// SC-4521 regression guard. The chat pane has never had .descedit-* rules of
// its own: it renders against the shared chat rules, which until SC-4521 were
// named .ideation-*. Retiring the panel therefore had to rename them rather
// than delete them, and a future "tidy up the chat CSS" must not undo that —
// the failure mode is an unstyled editor nobody notices until they open it.
test("the descedit chat pane renders against chat rules style.css actually defines (SC-4521)", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const ts = readFileSync(resolve(here, "..", "src", "board.ts"), "utf8");
  const css = readFileSync(resolve(here, "..", "static", "style.css"), "utf8");

  for (const [hook, shared] of [
    ["descedit-transcript", "chat-transcript"],
    ["descedit-status", "chat-status"],
    ["descedit-form", "chat-form"],
  ]) {
    assert.match(
      ts,
      new RegExp(`class="${hook} ${shared}`),
      `the ${hook} element must carry the shared .${shared} class`,
    );
    assert.match(css, new RegExp(`\\.${shared}\\s*\\{`), `style.css must define .${shared}`);
    assert.ok(!css.includes(`.${hook} {`), `.${hook} is a JS hook only — styling lives on .${shared}`);
  }
});

// SC-4820: an empty description pane used to cover three unrelated cases — no
// draft attempted, one running now, one that died — indistinguishable to the
// user. draftNotice resolves which of the three the pane is looking at.
test("draftNotice reports a failed background draft", () => {
  const n = draftNotice("failed", false);
  assert.equal(n.kind, "failed");
  assert.ok(n.text.length > 0);
});

test("draftNotice reports a draft still in flight", () => {
  const n = draftNotice("drafting", false);
  assert.equal(n.kind, "drafting");
  assert.ok(n.text.length > 0);
});

test("draftNotice with no state and no description says none was ever attempted", () => {
  const n = draftNotice(undefined, false);
  assert.equal(n.kind, "none");
  assert.ok(n.text.length > 0);
});

test("draftNotice with no state and a description says nothing — nothing to explain", () => {
  const n = draftNotice(undefined, true);
  assert.equal(n.kind, "none");
  assert.equal(n.text, "");
});
