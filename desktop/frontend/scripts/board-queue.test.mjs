import { test } from "node:test";
import assert from "node:assert/strict";
import { queueOf, isReworkable, isReopenable, verdictFailed, forwardDropAllowed, planReady, badgeInfo, sinceText, safetyReconcileError, cardError, sortByHandOrder, insertKeyAt, boardStateFromPayload, isReviewRetryable, STOP_DECISION_LABELS } from "../build/board-queue.js";
import { DAEMON_FORWARDED_STATES } from "../build/board-states.js";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

// SC-355 regression: a running or failed planning card must render in the
// Engineering column where the user dropped it — not snap back to Product.
test("planning card renders in engineering for every state (SC-355)", () => {
  assert.equal(queueOf({ stage: "planning", state: "running" }), "engineering");
  assert.equal(queueOf({ stage: "planning", state: "failed" }), "engineering");
  assert.equal(queueOf({ stage: "planning", state: "done" }), "engineering");
});

test("destination-lane placement stays intact for other stages", () => {
  assert.equal(queueOf({ stage: "backlog", state: "done" }), "product");
  assert.equal(queueOf({ stage: "implementation", state: "running" }), "building");
  assert.equal(queueOf({ stage: "verification", state: "running" }), "building");
  assert.equal(
    queueOf({ stage: "verification", state: "done", verdict: "pass", branch: "b" }),
    "deploy",
  );
});

// SC-355 affordance: only a plan-ready card may advance Engineering -> Code.
test("forward gate: engineering -> code requires plan-ready", () => {
  assert.equal(planReady({ stage: "planning", state: "done" }), true);
  assert.equal(planReady({ stage: "planning", state: "failed" }), false);
  assert.equal(forwardDropAllowed({ stage: "planning", state: "done" }, "building"), true);
  assert.equal(forwardDropAllowed({ stage: "planning", state: "failed" }, "building"), false);
  assert.equal(forwardDropAllowed({ stage: "planning", state: "running" }, "building"), false);
});

test("forward gate: rework re-drop onto code still allowed", () => {
  assert.equal(
    forwardDropAllowed({ stage: "verification", state: "done", verdict: "fail", branch: "b" }, "building"),
    true,
  );
});

// The Engineering column offers "Retry plan" exactly for a failed planning card,
// and NEVER offers the forward Code drop for it (guards SC-355 affordance).
test("failed planning card is retry-eligible but not code-droppable", () => {
  const failed = { stage: "planning", state: "failed" };
  assert.equal(planReady(failed), false, "failed plan must not be plan-ready");
  assert.equal(forwardDropAllowed(failed, "building"), false, "failed plan must not drop into Code");
});

// SC-695: a stage-failed review is retryable in place — the "Retry review"
// affordance mirrors "Retry plan"/"Retry build". Only verification/failed
// qualifies; a done review (rework path), a running review, and a failed build
// must NOT offer it.
test("isReviewRetryable is true only for verification/failed (SC-695)", () => {
  assert.equal(isReviewRetryable({ stage: "verification", state: "failed" }), true);
  assert.equal(isReviewRetryable({ stage: "verification", state: "done", verdict: "fail", branch: "b" }), false);
  assert.equal(isReviewRetryable({ stage: "verification", state: "running" }), false);
  assert.equal(isReviewRetryable({ stage: "implementation", state: "failed" }), false);
});

// SC-429 regression: "fix complete, review not started" (stage=implementation,
// state=done) is a durable pipeline state, not a sub-second transient. It must
// render a status badge in both the Bugs pane and the Code column — never blank.
test("implementation/done card gets a non-empty badge (SC-429)", () => {
  const info = badgeInfo({ stage: "implementation", state: "done" });
  assert.notEqual(info, null, "implementation/done must classify to a badge, not blank");
  assert.ok(info.text.length > 0, "badge text must be non-empty");
  assert.equal(info.cls, "await");
});

// A card mid PR-review→fix-loop (done stage, running, deployPhase pr-review)
// badges distinctly from a plain deploy so the loop is visible while it runs.
test("badge PR review label for a running loop card", () => {
  const info = badgeInfo({ stage: "done", state: "running", deployPhase: "pr-review" });
  assert.equal(info.cls, "running");
  assert.equal(info.text, "PR review…");
});

test("badge deploying label for a plain deploy (no phase)", () => {
  const info = badgeInfo({ stage: "done", state: "running" });
  assert.equal(info.cls, "running");
  assert.equal(info.text, "deploying…");
});

test("badgeInfo preserves prior classifications", () => {
  assert.equal(badgeInfo({ stage: "implementation", state: "running" }).cls, "running");
  assert.equal(badgeInfo({ stage: "verification", state: "failed" }).cls, "failed");
  // SC-405: a no-fix-needed triage outcome is resolved — never red, never blank.
  assert.equal(badgeInfo({ stage: "implementation", state: "resolved" }).cls, "resolved");
  // SC-454: a planning card whose work already shipped is resolved with a
  // positive "already shipped" badge — never red, never blank.
  assert.equal(badgeInfo({ stage: "planning", state: "resolved" }).cls, "resolved");
  assert.equal(badgeInfo({ stage: "planning", state: "resolved" }).text, "already shipped");
  assert.equal(
    badgeInfo({ stage: "verification", state: "done", verdict: "fail", branch: "b" }).cls,
    "fixing",
  );
  assert.equal(
    badgeInfo({ stage: "verification", state: "done", verdict: "pass", branch: "b" }),
    null,
    "a resting reviewed card needs no badge — its queue position states completion",
  );
  assert.equal(badgeInfo({ stage: "done", state: "done" }).cls, "done");
});

// SC-2699: a pre-planning stop decision gives a Backlog/done card a distinct
// "decided" badge that names the decision in human terms and shows the linked
// key — distinguishing it from a card merely waiting its turn.
test("decided badge names the decision and linked key for superseded (SC-2699)", () => {
  const info = badgeInfo({ stage: "backlog", state: "done", stopDecision: "superseded", stopLinkedKey: "SC-100" });
  assert.notEqual(info, null);
  assert.equal(info.cls, "decided");
  assert.match(info.text, /duplicate/);
  assert.match(info.text, /SC-100/);
  assert.notEqual(info.spinner, true, "a settled decision carries no spinner");
});

test("decided badge for escalated names the design ticket (SC-2699)", () => {
  const info = badgeInfo({ stage: "backlog", state: "done", stopDecision: "escalated", stopLinkedKey: "SC-200" });
  assert.equal(info.cls, "decided");
  assert.match(info.text, /needs design decision/);
  assert.match(info.text, /SC-200/);
});

test("decided badge for rejected has no linked key (SC-2699)", () => {
  const info = badgeInfo({ stage: "backlog", state: "done", stopDecision: "rejected" });
  assert.equal(info.cls, "decided");
  assert.equal(info.text, "not a real problem");
});

test("an undecided backlog/done card is unchanged — no badge (SC-2699)", () => {
  assert.equal(badgeInfo({ stage: "backlog", state: "done" }), null);
});

test("a decided card never paints failed (SC-2699)", () => {
  const info = badgeInfo({ stage: "backlog", state: "failed", stopDecision: "superseded", error: "stale" });
  assert.equal(info.cls, "decided", "the stop decision outranks any stale failure");
});

test("STOP_DECISION_LABELS covers the three stop heads (SC-2699)", () => {
  assert.equal(STOP_DECISION_LABELS.superseded.text, "duplicate");
  assert.equal(STOP_DECISION_LABELS.escalated.text, "needs design decision");
  assert.equal(STOP_DECISION_LABELS.rejected.text, "not a real problem");
});

// SC-1830 regression: a failed review verdict is machine work (the daemon
// auto-launches a fixer), NOT a demand on the user. Its badge must read as
// in-flight — a dedicated machine-working class, the running spinner, and copy
// that says what is happening ("fixing…") — never the amber `warning`/`decision`
// register with a ⚠ glyph that means "your turn".
test("failed review verdict badges as in-flight machine work, not a warning (SC-1830)", () => {
  const info = badgeInfo({ stage: "verification", state: "done", verdict: "fail", branch: "b" });
  assert.notEqual(info, null);
  assert.equal(info.cls, "fixing", "failed verdict must use the machine-working class, not amber `warning`");
  assert.notEqual(info.cls, "decision", "must not share the needs-a-human decision class");
  assert.equal(info.spinner, true, "must carry the running spinner so it reads as in-flight");
  assert.match(info.text, /fixing…/, "copy must say what is happening, not hand a verdict to the user");
  assert.doesNotMatch(info.text, /⚠/, "the alarm glyph belongs to needs-a-human states only");
});

// 1290: an open decision block must outrank a `failed` state too, not just the
// review warning — a card carrying a stale *-failed marker AND an open
// same-stage options block is a deliberate human pause (the daemon's twin
// guard in reconcileStuckRunning suppresses the marker going forward, but a
// marker posted before the fix landed must still render as a decision, not a
// red ✕). The badge text carries a `?` glyph so it reads as a question, not an
// error.
test("open options outrank a failed state with a decision-needed badge (1290)", () => {
  const card = { stage: "planning", state: "failed", options: [{ id: "1", label: "a" }, { id: "2", label: "b" }] };
  const info = badgeInfo(card);
  assert.equal(info.cls, "decision");
  assert.match(info.text, /\?/);
  // Without options the same card falls back to the plain failed badge.
  assert.equal(badgeInfo({ ...card, options: [] }).cls, "failed");
});

// An open decision block must surface as its own badge, outranking the
// generic review warning — the actionable statement is "pick one" (SC-534).
test("open options render a decision-needed badge over the review warning", () => {
  const card = {
    stage: "verification",
    state: "done",
    verdict: "fail",
    branch: "b",
    options: [{ id: "1", label: "a" }, { id: "2", label: "b" }],
  };
  const info = badgeInfo(card);
  assert.equal(info.cls, "decision");
  assert.match(info.text, /decision needed/);
  // Without options the same card falls back to the fixing badge.
  assert.equal(badgeInfo({ ...card, options: [] }).cls, "fixing");
});

// SC-1669: a card parked on an open decision whose state is still "running"
// (the agent stopped on a fork without posting a terminal marker) must paint
// the decision badge, never the running spinner — the decision branch outranks
// the running early-return, as its own comment claims.
test("open options outrank a running state with a decision-needed badge (SC-1669)", () => {
  const card = {
    stage: "planning",
    state: "running",
    options: [{ id: "1", label: "a" }, { id: "2", label: "b" }],
  };
  const info = badgeInfo(card);
  assert.equal(info.cls, "decision");
  assert.match(info.text, /decision needed/);
  // Without options the same card is a plain running badge.
  assert.equal(badgeInfo({ ...card, options: [] }).cls, "running");
});

// SC-1669: the queued early-return shares the same mis-ordering — a decision
// re-queued a stage but an open block is still present must read as a decision.
test("open options outrank a queued state with a decision-needed badge (SC-1669)", () => {
  const card = {
    stage: "implementation",
    state: "queued",
    options: [{ id: "1", label: "a" }, { id: "2", label: "b" }],
  };
  const info = badgeInfo(card);
  assert.equal(info.cls, "decision");
  // Without options the same card is the queued decision-recorded note.
  assert.equal(badgeInfo({ ...card, options: [] }).cls, "queued");
});

// SC-1301: the red `.card-error` subtitle must track the SAME badge
// classification as the amber decision badge, not be computed independently. A
// card parked on an open [human:options] decision that ALSO carries a stale
// *-failed marker must paint the decision badge and NO red error line — SC-1290
// fixed the badge but left renderCard's error render on raw `card.error`.
test("cardError suppresses the failure subtitle when a decision outranks it (SC-1301)", () => {
  const card = {
    stage: "planning",
    state: "failed",
    error: "Stuck in planning: no terminal marker and no live agent — needs attention",
    options: [{ id: "1", label: "a" }, { id: "2", label: "b" }],
  };
  assert.equal(badgeInfo(card)?.cls, "decision", "an open decision must classify as the decision badge");
  assert.equal(cardError(card), "", "no red error subtitle may accompany the decision badge");
});

test("cardError keeps the failure text for a genuinely failed card (SC-1301)", () => {
  const card = { stage: "planning", state: "failed", error: "boom" };
  assert.equal(badgeInfo(card)?.cls, "failed", "no decision → the failed badge");
  assert.equal(cardError(card), "boom", "a real failure still shows its error text");
});

test("cardError is empty for a running card (SC-1301)", () => {
  assert.equal(cardError({ stage: "implementation", state: "running", error: "boom" }), "");
});

test("cardError is empty when the error field is blank or absent (SC-1301)", () => {
  assert.equal(cardError({ stage: "planning", state: "failed" }), "");
  assert.equal(cardError({ stage: "planning", state: "failed", error: "" }), "");
});

// SC-1301 wiring guard: renderCard's error subtitle must be gated on the shared
// cardError classifier, never on raw `card.error`. The frontend is dependency-
// free (no DOM test runner), so this asserts the source wiring like
// ideation-done.test.mjs — so the unconditional render cannot be reintroduced.
test("renderCard gates the error subtitle on cardError, not raw card.error (SC-1301)", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const ts = readFileSync(resolve(here, "..", "src", "board.ts"), "utf8");
  assert.match(ts, /cardError\(/, "renderCard must derive the error text through the shared cardError classifier");
  assert.match(ts, /class="card-error"/, "the error subtitle keeps its style hook");
  assert.doesNotMatch(
    ts,
    /card\.error \? `<div class="card-error"/,
    "the unconditional card.error render must not be reintroduced",
  );
});

test("queued state renders the decision-recorded note, not a failure (SC-1320)", () => {
  const info = badgeInfo({ stage: "planning", state: "queued" });
  assert.equal(info.cls, "queued");
  assert.match(info.text, /decision recorded — replanning picked up/);
});

test("queued verb is stage-aware (SC-1320)", () => {
  assert.match(badgeInfo({ stage: "implementation", state: "queued" }).text, /rebuild picked up/);
});

test("a queued card shows no red error subtitle (SC-1320)", () => {
  assert.equal(cardError({ stage: "planning", state: "queued", error: "Stuck in planning" }), "");
});

// SC-624: columns render in the user's hand-sorted order; cards without a
// saved slot keep fetch order after the sorted ones.
test("sortByHandOrder: listed keys first in saved order, rest stable after", () => {
  const cards = [{ key: "A" }, { key: "B" }, { key: "C" }, { key: "D" }];
  sortByHandOrder(cards, ["C", "A"]);
  assert.deepEqual(cards.map((c) => c.key), ["C", "A", "B", "D"]);
});

test("sortByHandOrder: no saved order leaves fetch order untouched", () => {
  const cards = [{ key: "B" }, { key: "A" }];
  sortByHandOrder(cards, undefined);
  assert.deepEqual(cards.map((c) => c.key), ["B", "A"]);
  sortByHandOrder(cards, []);
  assert.deepEqual(cards.map((c) => c.key), ["B", "A"]);
});

// SC-631 regression: the payload-to-state mapping must carry the board-level
// columnOrder the daemon ships. A field-by-field rebuild (cards/dockerAvailable/
// error only) dropped it, collapsing the hand-sort back to fetch order on every
// reload. These pin the mapping so that class of bug cannot ship again.
test("boardStateFromPayload carries columnOrder through the mapping (SC-631)", () => {
  const payload = {
    cards: [{ key: "A" }, { key: "B" }, { key: "C" }],
    dockerAvailable: true,
    columnOrder: { product: ["C", "A", "B"] },
  };
  const state = boardStateFromPayload(payload);
  assert.deepEqual(state.columnOrder, { product: ["C", "A", "B"] });
  assert.deepEqual(state.cards.map((c) => c.key), ["A", "B", "C"]);
  assert.equal(state.dockerAvailable, true);
  assert.equal(state.error, "");
});

test("boardStateFromPayload state feeds sortByHandOrder to the saved order (SC-631)", () => {
  const payload = {
    cards: [{ key: "A" }, { key: "B" }, { key: "C" }],
    dockerAvailable: true,
    columnOrder: { product: ["C", "A", "B"] },
  };
  const state = boardStateFromPayload(payload);
  const sorted = sortByHandOrder([...state.cards], state.columnOrder?.product);
  assert.deepEqual(sorted.map((c) => c.key), ["C", "A", "B"]);
});

test("boardStateFromPayload suppresses error but keeps columnOrder for quick phase (SC-631)", () => {
  const state = boardStateFromPayload({ error: "boom", columnOrder: { product: ["A"] } }, true);
  assert.equal(state.error, "");
  assert.deepEqual(state.columnOrder, { product: ["A"] });
});

test("boardStateFromPayload normalizes an empty payload (SC-631)", () => {
  const state = boardStateFromPayload({});
  assert.deepEqual(state.cards, []);
  assert.equal(state.dockerAvailable, false);
  assert.equal(state.error, "");
  assert.equal(state.notice, "");
  assert.equal(state.truncation, "");
  assert.equal(state.columnOrder, undefined);
});

test("boardStateFromPayload carries the truncation affordance through the mapping (SC-1693)", () => {
  const state = boardStateFromPayload({ truncation: "Showing the first 200 tickets — more exist." });
  assert.equal(state.truncation, "Showing the first 200 tickets — more exist.");
});

test("boardStateFromPayload carries the no-PM-tracker notice through the mapping (SC-1655)", () => {
  const state = boardStateFromPayload({ notice: "No PM-role tracker configured." });
  assert.equal(state.notice, "No PM-role tracker configured.");
  assert.deepEqual(state.cards, []);
});

// SC-624: a same-column drop inserts the dragged key at the pointer position.
test("insertKeyAt places dragged key by drop midpoint", () => {
  // Cards A(mid 100), B(mid 200), C(mid 300).
  const resting = ["A", "B", "C"];
  const mids = [100, 200, 300];
  assert.deepEqual(insertKeyAt(resting, mids, "X", 50), ["X", "A", "B", "C"]);
  assert.deepEqual(insertKeyAt(resting, mids, "X", 150), ["A", "X", "B", "C"]);
  assert.deepEqual(insertKeyAt(resting, mids, "X", 999), ["A", "B", "C", "X"]);
  assert.deepEqual(insertKeyAt([], [], "X", 10), ["X"]);
});

// --- Engineering-backlog age badge + Replan gating ---

test("ageDays: absent, unparseable, negative, and whole-day conversion", async () => {
  const { ageDays } = await import("../build/board-queue.js");
  const now = new Date("2026-07-21T12:00:00Z");
  assert.equal(ageDays(undefined, now), null);
  assert.equal(ageDays("not-a-date", now), null);
  assert.equal(ageDays("2026-07-22T12:00:00Z", now), null, "future timestamps yield null, not negative days");
  assert.equal(ageDays("2026-07-21T02:00:00Z", now), 0);
  assert.equal(ageDays("2026-07-14T12:00:00Z", now), 7);
});

test("ageBadge: only done-state planning feature cards, from 1 day up", async () => {
  const { ageBadge } = await import("../build/board-queue.js");
  const now = new Date("2026-07-21T12:00:00Z");
  const daysAgo = (n) => new Date(now.getTime() - n * 86_400_000).toISOString();

  assert.equal(ageBadge({ stage: "planning", state: "done", stageEnteredAt: daysAgo(0) }, now), null, "under a day: no 0d noise");
  assert.deepEqual(ageBadge({ stage: "planning", state: "done", stageEnteredAt: daysAgo(3) }, now), { text: "3d", cls: "age" });
  assert.deepEqual(ageBadge({ stage: "planning", state: "done", stageEnteredAt: daysAgo(7) }, now), { text: "7d", cls: "age warn" });
  assert.deepEqual(ageBadge({ stage: "planning", state: "done", stageEnteredAt: daysAgo(14) }, now), { text: "14d", cls: "age hot" });

  assert.equal(ageBadge({ stage: "planning", state: "running", stageEnteredAt: daysAgo(3) }, now), null, "running plans show the spinner instead");
  assert.equal(ageBadge({ stage: "implementation", state: "done", stageEnteredAt: daysAgo(3) }, now), null, "only the Engineering backlog ages");
  assert.equal(ageBadge({ stage: "planning", state: "done", bug: true, stageEnteredAt: daysAgo(3) }, now), null, "bug cards live in the Bugs pane");
  assert.equal(ageBadge({ stage: "planning", state: "done" }, now), null, "no timestamp, no badge");
});

test("isReplannable: planned feature cards only", async () => {
  const { isReplannable } = await import("../build/board-queue.js");
  assert.equal(isReplannable({ stage: "planning", state: "done" }), true);
  assert.equal(isReplannable({ stage: "planning", state: "failed" }), false, "failed plans get Retry plan, not Replan");
  assert.equal(isReplannable({ stage: "planning", state: "running" }), false);
  assert.equal(isReplannable({ stage: "implementation", state: "done" }), false);
  assert.equal(isReplannable({ stage: "planning", state: "done", bug: true }), false);
});

import { deploySideOf, deployControlView, deployableCards } from "../build/board-queue.js";

// deploySideOf is the one place the three-way pane split is decided: security
// wins over bug (they are disjoint, but the guard makes the precedence explicit),
// bug over feature, and a plain card is a feature.
test("deploySideOf maps each kind to its own pane", () => {
  assert.equal(deploySideOf({ security: true }), "security");
  assert.equal(deploySideOf({ bug: true }), "bugs");
  assert.equal(deploySideOf({}), "features");
  // Disjoint in practice; if both flags ever appear, security takes precedence.
  assert.equal(deploySideOf({ bug: true, security: true }), "security");
});

// The Security Deploy control names vulnerabilities, not bugs or ready cards, so
// its empty-state tooltip reads in security language.
test("security deploy control uses vulnerability wording", () => {
  const view = deployControlView([], "security");
  assert.equal(view.count, 0);
  assert.ok(view.disabled);
  assert.match(view.tooltip, /vulnerability/);
});

// The single "defects" Deploy control ships BOTH kinds: deployableCards for it
// returns every ready bug AND security card, but never a feature (the board has
// its own Deploy) nor an unready one.
test("defects deploy collects ready bugs and security, excludes features and unready", () => {
  const ready = { stage: "verification", state: "done", branch: "b" };
  const cards = [
    { ...ready, key: "bug", bug: true },
    { ...ready, key: "sec", security: true },
    { ...ready, key: "feat" },
    { key: "unready-bug", bug: true, stage: "implementation", state: "running" },
  ];
  const got = deployableCards(cards, "defects").map((c) => c.key).sort();
  assert.deepEqual(got, ["bug", "sec"]);
});

// The shared control names both kinds in its wording.
test("defects deploy control names bugs and vulnerabilities", () => {
  const view = deployControlView([], "defects");
  assert.equal(view.count, 0);
  assert.ok(view.disabled);
  assert.match(view.tooltip, /bugs or vulnerabilities/);
});

// SC-3024: an outage card is the do-nothing "paused" register — a substrate
// the run depends on is unavailable, the work is safe, and it resumes itself.
// It must draw a visible badge (never the blank `null` a missing case falls
// through to) and never the spinner (nothing is actively running).
test("outage card renders a visible paused badge (SC-3024)", () => {
  const info = badgeInfo({
    stage: "implementation",
    state: "outage",
    error: "paused — model usage limit",
    resumeAt: "2026-08-03T08:50:00Z",
  });
  assert.notEqual(info, null, "an outage card must draw a badge, not render blank");
  assert.equal(info.cls, "paused");
  assert.match(info.text, /paused/);
  assert.notEqual(info.spinner, true, "nothing is actively running while paused");
});

// cardError's suppression rule only anticipated "failed"; an outage card's
// explanation must not be silently dropped just because it is drawn in a
// state the rule did not name.
test("outage explanation is not suppressed (SC-3024)", () => {
  const card = { stage: "implementation", state: "outage", error: "paused — model usage limit" };
  assert.equal(cardError(card), "paused — model usage limit");
});

// The two-way drift lock (board_states_contract_test.go is the Go half):
// every state the daemon forwards must classify to a real badge here, so a
// state added on one side without teaching the other fails a test instead of
// rendering blank on the board.
function representativeCardFor(state) {
  switch (state) {
    case "running":
      return { stage: "implementation", state: "running" };
    case "queued":
      return { stage: "planning", state: "queued" };
    case "done":
      return { stage: "done", state: "done" };
    case "failed":
      return { stage: "implementation", state: "failed" };
    case "resolved":
      return { stage: "implementation", state: "resolved" };
    case "outage":
      return { stage: "implementation", state: "outage", error: "paused — model usage limit" };
    default:
      throw new Error(`representativeCardFor: no fixture for state ${state}`);
  }
}

test("badgeInfo covers every daemon-forwarded state (SC-3024 contract)", () => {
  for (const state of DAEMON_FORWARDED_STATES) {
    const info = badgeInfo(representativeCardFor(state));
    assert.notEqual(info, null, `badgeInfo must classify a "${state}" card, not return null`);
  }
});

// SC-3409: the same bug-631 rule as columnOrder — every board-level field must
// survive the single payload->state mapper, or the call sites silently drop it.
test("boardStateFromPayload carries dimPercent through the mapping (SC-3409)", () => {
  assert.equal(boardStateFromPayload({ dimPercent: 20 }).dimPercent, 20);
});

// Absent stays absent rather than becoming 0: "nothing declared" has to reach
// the renderer as a distinct answer so it leaves the stylesheet alone.
test("boardStateFromPayload leaves dimPercent undefined when the payload omits it (SC-3409)", () => {
  assert.equal(boardStateFromPayload({}).dimPercent, undefined);
});

// The board reads the daemon's decision, never the verdict string. These two
// used to be separate implementations one word apart: Go treated "incomplete"
// as blocking, the board did not. An incomplete review therefore rendered in
// Ready to Deploy with no Rework gesture, while the daemon refused every drop —
// a card whose only offered move could not succeed.
test("verdictFailed prefers the daemon's decision over the verdict text", () => {
  // The flag wins even when the text would say otherwise, in both directions.
  assert.equal(verdictFailed({ verdict: "pass", verdictFailed: true }), true);
  assert.equal(verdictFailed({ verdict: "fail", verdictFailed: false }), false);
});

test("the legacy fallback matches the Go rule, including incomplete", () => {
  // Only reached for a payload from a daemon predating the field.
  assert.equal(verdictFailed({ verdict: "fail — three findings" }), true);
  assert.equal(verdictFailed({ verdict: "incomplete" }), true);
  assert.equal(verdictFailed({ verdict: "Incomplete: one criterion unmet" }), true);
  assert.equal(verdictFailed({ verdict: "pass" }), false);
  assert.equal(verdictFailed({ verdict: "pass with notes" }), false);
  assert.equal(verdictFailed({}), false, "absence of a verdict is not failure");
});

test("an incomplete verdict keeps the card in Code and offers Rework", () => {
  const card = { stage: "verification", state: "done", verdict: "incomplete", verdictFailed: true, branch: "b" };
  assert.equal(queueOf(card), "building", "must not sit in Ready to Deploy — the daemon refuses that drop");
  assert.equal(isReworkable(card), true, "the one gesture that can move it must be offered");
});

// A resolved card is a clean terminal — never red, never retried — which also
// meant no gesture could move it. Re-opening is the human override; every other
// state keeps its existing gestures and must not offer this one.
test("only a resolved card offers Re-open", () => {
  assert.equal(isReopenable({ stage: "planning", state: "resolved" }), true);
  assert.equal(isReopenable({ stage: "implementation", state: "resolved" }), true);
  assert.equal(isReopenable({ stage: "implementation", state: "failed" }), false, "a failed card already retries");
  assert.equal(isReopenable({ stage: "implementation", state: "running" }), false);
  assert.equal(isReopenable({ stage: "verification", state: "done" }), false);
});

// The badge said one word for the whole of a run — identical at thirty seconds,
// at fourteen hours, and when the agent behind it was already dead. The phase
// the run records is the only thing on a running card that changes as work moves.
test("a running badge shows the recorded phase when there is one", () => {
  const badge = badgeInfo({ stage: "implementation", state: "running", activity: "verifying" });
  assert.equal(badge.text, "verifying…");
  assert.equal(badge.spinner, true);
  assert.match(badge.title, /verifying/);
});

test("a run that recorded no phase keeps the stage word", () => {
  const badge = badgeInfo({ stage: "implementation", state: "running" });
  assert.equal(badge.text, "building…", "no invented phase — absence stays absent");
});

// --- The phase badge carries its own age (SC-4151 B4) ---

test("a fresh phase reads as it always did, with no age", () => {
  const now = Date.parse("2026-08-10T12:00:00Z");
  const info = badgeInfo(
    { stage: "implementation", state: "running", activity: "triaging", activityAt: "2026-08-10T11:58:00Z" },
    now,
  );
  assert.equal(info.text, "triaging…");
  assert.match(info.title, /Agent running — triaging, last recorded 2m ago/);
});

test("a phase that has stood too long says how long", () => {
  const now = Date.parse("2026-08-10T12:00:00Z");
  const info = badgeInfo(
    { stage: "implementation", state: "running", activity: "triaging", activityAt: "2026-08-10T06:00:00Z" },
    now,
  );
  assert.equal(info.text, "triaging… (6h ago)");
  assert.equal(info.spinner, true, "still running — the age is information, not a verdict");
});

test("an unreadable or absent activity time prints no age", () => {
  const now = Date.parse("2026-08-10T12:00:00Z");
  assert.equal(badgeInfo({ stage: "implementation", state: "running", activity: "fixing" }, now).text, "fixing…");
  assert.equal(
    badgeInfo({ stage: "implementation", state: "running", activity: "fixing", activityAt: "not-a-time" }, now).text,
    "fixing…",
  );
});

test("a running card with no recorded phase is unchanged", () => {
  const now = Date.parse("2026-08-10T12:00:00Z");
  const info = badgeInfo({ stage: "implementation", state: "running", activityAt: "2026-08-10T06:00:00Z" }, now);
  assert.equal(info.text, "building…");
  assert.equal(info.title, "Agent running");
});

test("sinceText refuses to render an age it could not read", () => {
  const now = Date.parse("2026-08-10T12:00:00Z");
  assert.equal(sinceText(undefined, now), "");
  assert.equal(sinceText("nonsense", now), "");
  assert.equal(sinceText("2026-08-10T11:59:30Z", now), "30s ago");
  assert.equal(sinceText("2026-08-08T12:00:00Z", now), "2d ago");
});

// --- The badge names the half of the loop that is running (SC-4151 F15) ---

test("a pr-fix card says the fixer is working, not the reviewer", () => {
  const info = badgeInfo({ stage: "done", state: "running", deployPhase: "pr-fix" });
  assert.equal(info.text, "fixing PR findings…");
});

test("a pr-review card is unchanged", () => {
  assert.equal(badgeInfo({ stage: "done", state: "running", deployPhase: "pr-review" }).text, "PR review…");
});

test("an unknown deploy phase falls back to the review wording rather than blanking", () => {
  assert.equal(badgeInfo({ stage: "done", state: "running", deployPhase: "something-new" }).text, "PR review…");
});

// --- The Deploy control and its lane agree (SC-4151 F16) ---

test("an empty deploy lane still says there is nothing to deploy yet", () => {
  const view = deployControlView([{ stage: "planning", state: "done" }], "features");
  assert.ok(view.disabled);
  assert.match(view.tooltip, /No ready-to-deploy cards to deploy yet/);
});

test("cards in the lane that the control refuses are counted, not denied", () => {
  // A done-stage card sits in the deploy lane whatever its state, but
  // isReadyToDeploy takes only a reviewed verification/done card with a branch.
  const cards = [
    { stage: "done", state: "running" },
    { stage: "done", state: "failed" },
  ];
  const view = deployControlView(cards, "features");
  assert.equal(view.count, 0);
  assert.ok(view.disabled);
  assert.match(view.tooltip, /2 cards here are still finishing or stopped/);
  assert.doesNotMatch(view.tooltip, /No ready-to-deploy cards to deploy yet/);
});

test("one waiting card reads in the singular", () => {
  const view = deployControlView([{ stage: "done", state: "running" }], "features");
  assert.match(view.tooltip, /1 card here is still finishing or stopped/);
});

test("a ready card still ships and says so", () => {
  const view = deployControlView(
    [{ stage: "verification", state: "done", branch: "b", verdictFailed: false }, { stage: "done", state: "failed" }],
    "features",
  );
  assert.equal(view.count, 1);
  assert.ok(!view.disabled);
  assert.match(view.tooltip, /Ship every ready-to-deploy card/);
});

// --- A failed fetch is not a Docker verdict (SC-4151 G17) ---

test("an empty board after a fetch failure keeps the last known Docker answer", () => {
  const got = safetyReconcileError({ cards: [], dockerAvailable: true }, "daemon unreachable");
  assert.equal(got.dockerAvailable, true, "Docker was never probed by this failure");
  assert.equal(got.error, "daemon unreachable");
});

test("a populated board on a hiccup is unchanged", () => {
  const got = safetyReconcileError({ cards: [{ key: "SC-1" }], dockerAvailable: false }, "hiccup");
  assert.equal(got.dockerAvailable, false);
  assert.match(got.error, /Board may be stale/);
});

// SC-3569 reproduction 1: liveness was not an input to the badge at all, so a
// card whose agent died hours ago rendered the same spinner and the same word
// as one burning tokens right now.
test("a dead agent drops the spinner and stops claiming work is happening (SC-3569)", () => {
  const live = badgeInfo({ stage: "implementation", state: "running", agentLiveness: "live" });
  const dead = badgeInfo({ stage: "implementation", state: "running", agentLiveness: "dead" });
  assert.notDeepEqual(live, dead, "a dead card must not render identically to a live one");
  assert.equal(dead.spinner, false, "the spinner is the claim that a process is alive");
  assert.equal(live.spinner, true);
  assert.match(dead.text, /not running/);
});

// The multi-daemon rule: another machine's agent is invisible from here, so the
// card must show neither a false spinner nor a false death.
test("a stage owned by another machine says so, with no spinner and no death (SC-3569)", () => {
  const info = badgeInfo({ stage: "implementation", state: "running", agentLiveness: "elsewhere" });
  assert.equal(info.spinner, false);
  assert.match(info.text, /another machine/);
  assert.doesNotMatch(info.text, /not running/);
});

// Absence of a signal is never proof: an unknown liveness renders exactly as before.
test("unknown liveness renders exactly as today (SC-3569)", () => {
  assert.deepEqual(
    badgeInfo({ stage: "implementation", state: "running" }),
    { cls: "running", text: "building…", title: "Agent running", spinner: true },
  );
});

// SC-3569 PR review finding: "dead" and "recovering" must NOT render the
// same. A running card between agentLaunchGrace and StuckRunningGrace is
// dead-but-machine-owed — the daemon's own relaunch is still due — so it must
// never carry the "stalled" class (the person register) or the "Retry it"
// wording, only "recovering" (the machine register).
test("a recovering agent stays in the machine register, never the person's (SC-3569)", () => {
  const dead = badgeInfo({ stage: "implementation", state: "running", agentLiveness: "dead" });
  const recovering = badgeInfo({ stage: "implementation", state: "running", agentLiveness: "recovering" });
  assert.equal(dead.cls, "stalled");
  assert.equal(recovering.cls, "recovering", "must not share the person-facing stalled class");
  assert.notDeepEqual(dead, recovering);
  assert.equal(recovering.spinner, false, "no live process is asserted either");
  assert.doesNotMatch(recovering.title, /Retry it/, "the machine, not the person, is due to act next");
});

// The queued and fixing badges spin on the same unchecked assumption.
test("queued and fixing badges also drop the spinner for a dead agent (SC-3569)", () => {
  const queued = badgeInfo({ stage: "implementation", state: "queued", agentLiveness: "dead" });
  assert.equal(queued.spinner, false);
  const fixing = badgeInfo({
    stage: "verification", state: "done", verdict: "fail", branch: "b", agentLiveness: "dead",
  });
  assert.equal(fixing.spinner, false);
  assert.match(fixing.text, /no fixer running/);
});
