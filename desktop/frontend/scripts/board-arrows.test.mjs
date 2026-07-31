import { test } from "node:test";
import assert from "node:assert/strict";
import { linksWithin, sides, anchor, arrowPath } from "../build/board-arrows.js";

const box = (left, top, width = 100, height = 60) => ({ left, top, width, height });

// An arrow can only be drawn between two cards that are both on screen. Drawing
// to a card that is not in this column would mean drawing at the container's
// edge and hoping the user infers the rest — those are left to the badge.
test("only dependencies with both ends present are drawn", () => {
  const blockers = new Map([
    ["SC-2", ["SC-1"]],
    ["SC-9", ["SC-8"]], // SC-8 sits in another column
  ]);

  const links = linksWithin(blockers, new Set(["SC-1", "SC-2", "SC-9"]));

  assert.deepEqual(links, [{ from: "SC-1", to: "SC-2" }]);
});

// A chain draws one arrow per hop and comes out as the tree it is — no code
// knows about chains.
test("a chain becomes one arrow per hop", () => {
  const blockers = new Map([
    ["SC-2", ["SC-1"]],
    ["SC-3", ["SC-2"]],
  ]);

  assert.deepEqual(linksWithin(blockers, new Set(["SC-1", "SC-2", "SC-3"])), [
    { from: "SC-1", to: "SC-2" },
    { from: "SC-2", to: "SC-3" },
  ]);
});

// A card waiting for several tickets gets an arrow from each.
test("several blockers each get their own arrow", () => {
  const links = linksWithin(new Map([["SC-3", ["SC-2", "SC-1"]]]), new Set(["SC-1", "SC-2", "SC-3"]));

  assert.deepEqual(links, [
    { from: "SC-1", to: "SC-3" },
    { from: "SC-2", to: "SC-3" },
  ]);
});

// Ordering is fixed so an unchanged layout re-renders to identical SVG and the
// browser has nothing to repaint.
test("output order does not depend on map insertion order", () => {
  const a = linksWithin(new Map([["SC-3", ["SC-1"]], ["SC-2", ["SC-1"]]]), new Set(["SC-1", "SC-2", "SC-3"]));
  const b = linksWithin(new Map([["SC-2", ["SC-1"]], ["SC-3", ["SC-1"]]]), new Set(["SC-1", "SC-2", "SC-3"]));

  assert.deepEqual(a, b);
});

// A ticket cannot wait for itself; a self-arrow would be a loop drawn on one
// card, which says nothing.
test("a card is never linked to itself", () => {
  assert.deepEqual(linksWithin(new Map([["SC-1", ["SC-1"]]]), new Set(["SC-1"])), []);
});

// The two layouts this runs in differ — the bug grid flows cards across, the
// workflow columns stack them down — and reading the offsets means neither has
// to announce itself.
test("the connected edges are the ones facing each other", () => {
  assert.deepEqual(sides(box(0, 0), box(200, 0)), { exit: "right", enter: "left" });
  assert.deepEqual(sides(box(200, 0), box(0, 0)), { exit: "left", enter: "right" });
  assert.deepEqual(sides(box(0, 0), box(0, 200)), { exit: "bottom", enter: "top" });
  assert.deepEqual(sides(box(0, 200), box(0, 0)), { exit: "top", enter: "bottom" });
});

// A grid that wraps puts the blocker above and to the right of what it blocks;
// the dominant axis decides, so the wrapped pair connects vertically instead of
// drawing an arrow that points backwards.
test("a wrapped grid row connects vertically", () => {
  assert.deepEqual(sides(box(600, 0), box(0, 300)), { exit: "bottom", enter: "top" });
});

test("anchors sit at the middle of the named edge", () => {
  assert.deepEqual(anchor(box(10, 20), "right"), { x: 110, y: 50 });
  assert.deepEqual(anchor(box(10, 20), "top"), { x: 60, y: 20 });
});

// The head has to stop short of the card, or it renders as part of the border.
test("the arrow stops short of the card it points at", () => {
  const d = arrowPath(box(0, 0), box(200, 0));
  const end = d.split(",").pop().trim().split(" ");

  assert.equal(Number(end[0]), 196, "ends before the target's left edge at x=200");
});

// Adjacent cards sit edge to edge: a straight segment between them would read
// as a border artifact, so the path is always a curve with room to bend.
test("touching cards still produce a curve, not a dot", () => {
  const d = arrowPath(box(0, 0), box(108, 0));

  assert.match(d, /^M \d+ \d+ C /);
  assert.notEqual(d.split("C")[1].trim(), "");
});

import { plan, corridorClear, gapsBySide } from "../build/board-arrows.js";

// A row of three: SC-1 and SC-3 are linked, SC-2 sits between them. A straight
// line would cross SC-2's face and state a relationship between the wrong two
// tickets, so that pair keeps the badge — the same rule as a pair split across
// two columns.
test("a card in the way means no arrow, not an arrow through it", () => {
  const boxes = new Map([
    ["SC-1", box(0, 0)],
    ["SC-2", box(140, 0)],
    ["SC-3", box(280, 0)],
  ]);

  assert.deepEqual(plan([{ from: "SC-1", to: "SC-3" }], boxes), []);
  assert.equal(plan([{ from: "SC-1", to: "SC-2" }], boxes).length, 1, "neighbours still draw");
});

// The corridor between neighbours is just the gap, and a card flush against it
// is beside the arrow, not in its way.
test("touching the corridor is not standing in it", () => {
  assert.equal(corridorClear(box(0, 0), box(140, 0), [box(140, 0)]), true);
});

// A wrapped grid row: the blocker ends one row, the card it holds starts the
// next. The corridor is the space between the rows, which no card occupies.
test("a wrapped row still draws", () => {
  const boxes = new Map([
    ["SC-1", box(500, 0)],
    ["SC-2", box(0, 100)],
    ["SC-9", box(140, 0)],
  ]);

  const drawn = plan([{ from: "SC-1", to: "SC-2" }], boxes);
  assert.deepEqual(drawn, [{ from: "SC-1", to: "SC-2", exit: "bottom", enter: "top" }]);
});

// The sides are settled before any card is narrowed, so the gap that gets
// opened is always the one the arrow uses.
test("the plan records which edges the arrow will use", () => {
  const drawn = plan([{ from: "SC-1", to: "SC-2" }], new Map([["SC-1", box(0, 0)], ["SC-2", box(140, 0)]]));

  assert.deepEqual(drawn, [{ from: "SC-1", to: "SC-2", exit: "right", enter: "left" }]);
});

// Only the edge an arrow attaches to gives up space.
test("each card narrows only on the side an arrow uses", () => {
  const drawn = plan([{ from: "SC-1", to: "SC-2" }], new Map([["SC-1", box(0, 0)], ["SC-2", box(140, 0)]]));

  assert.deepEqual([...gapsBySide(drawn)], [
    ["SC-1", ["right"]],
    ["SC-2", ["left"]],
  ]);
});

// A card in the middle of a chain is waited on from one side and waits on the
// other, so it narrows on both — which is what collecting sides per card rather
// than per arrow produces, with no case for it in the code.
test("a card mid-chain narrows on both sides", () => {
  const boxes = new Map([["SC-1", box(0, 0)], ["SC-2", box(140, 0)], ["SC-3", box(280, 0)]]);
  const drawn = plan(
    [
      { from: "SC-1", to: "SC-2" },
      { from: "SC-2", to: "SC-3" },
    ],
    boxes,
  );

  assert.deepEqual(gapsBySide(drawn).get("SC-2"), ["left", "right"]);
});
