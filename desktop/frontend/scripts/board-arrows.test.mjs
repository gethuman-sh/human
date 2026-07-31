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
