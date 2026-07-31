// Dependency arrows between cards, kept free of DOM and Wails bindings so the
// geometry can be unit-tested directly (the rest of board.ts bootstraps against
// document/window.go at import time and cannot).
//
// An arrow says the same thing as the "waits for SC-1234" badge, but says it
// without being read. It can only be drawn when both ends are on screen
// together, so the badge stays the statement of record and the arrow is what
// you get when the layout happens to allow it.

// Box is a card's position within its scrolling container, in that container's
// own content coordinates (offsetLeft/offsetTop), so an arrow drawn from these
// scrolls with the cards instead of sliding across them.
export interface Box {
  left: number;
  top: number;
  width: number;
  height: number;
}

// Link is one dependency to draw: the blocker points at the work it holds.
export interface Link {
  from: string; // the blocker — it must finish first
  to: string; // the card waiting for it
}

// linksWithin returns the dependencies whose BOTH ends are present.
//
// Board columns are pipeline stages, so a blocker and the card it holds are
// usually in different ones. Drawing an arrow to a card that is not there would
// mean drawing to the edge of the container and hoping the user infers the
// rest, so those are left to the badge. A chain that IS all present draws
// arrow-per-hop and comes out as the tree it is, with no code that knows about
// chains.
//
// `blockers` maps a card key to the keys it waits for. The result is ordered by
// (to, from) so a re-render produces byte-identical SVG and the browser has
// nothing to repaint.
export function linksWithin(blockers: Map<string, string[]>, present: Set<string>): Link[] {
  const links: Link[] = [];
  for (const [to, keys] of blockers) {
    if (!present.has(to)) continue;
    for (const from of keys) {
      if (present.has(from) && from !== to) links.push({ from, to });
    }
  }
  return links.sort((a, b) => a.to.localeCompare(b.to) || a.from.localeCompare(b.from));
}

// Side names which edge of a card an arrow leaves from or arrives at.
type Side = "left" | "right" | "top" | "bottom";

// sides picks the pair of edges to connect: the ones that face each other.
//
// A vertical offset of more than half a card decides it, because the two
// layouts this runs in differ: the bug grid flows cards left-to-right, the
// workflow columns stack them top-to-bottom. Reading the actual offsets means
// neither layout has to announce itself — cards sharing a row connect
// sideways, cards on different rows connect up or down. That also handles a
// grid that wrapped: the blocker ends a row and the card it holds starts the
// next one, far to the LEFT of it, and connecting sideways there would drag an
// arrow back across the whole row to arrive pointing the wrong way.
export function sides(from: Box, to: Box): { exit: Side; enter: Side } {
  const dx = to.left + to.width / 2 - (from.left + from.width / 2);
  const dy = to.top + to.height / 2 - (from.top + from.height / 2);
  if (Math.abs(dy) > from.height / 2) {
    return dy >= 0 ? { exit: "bottom", enter: "top" } : { exit: "top", enter: "bottom" };
  }
  return dx >= 0 ? { exit: "right", enter: "left" } : { exit: "left", enter: "right" };
}

// anchor returns the point on one edge of a card, at the middle of that edge.
export function anchor(box: Box, side: Side): { x: number; y: number } {
  const midX = box.left + box.width / 2;
  const midY = box.top + box.height / 2;
  switch (side) {
    case "left":
      return { x: box.left, y: midY };
    case "right":
      return { x: box.left + box.width, y: midY };
    case "top":
      return { x: midX, y: box.top };
    default:
      return { x: midX, y: box.top + box.height };
  }
}

// GAP is how far short of the target the arrow stops, leaving the head clear of
// the card's border instead of touching it.
const GAP = 4;

// arrowPath builds the SVG path for one dependency.
//
// A cubic curve rather than a straight line: adjacent cards sit edge to edge,
// and a straight segment between them is short enough to read as a border
// artifact. The control points push out along the exit and entry axes, so the
// curve leaves and arrives perpendicular to the cards and the direction is
// legible even when the two are only a few pixels apart.
export function arrowPath(from: Box, to: Box): string {
  const { exit, enter } = sides(from, to);
  const start = anchor(from, exit);
  const raw = anchor(to, enter);
  const end = shortenBy(raw, enter, GAP);
  const bend = Math.max(12, Math.min(48, distance(start, end) / 2));
  const c1 = push(start, exit, bend);
  const c2 = push(end, enter, bend);
  return `M ${r(start.x)} ${r(start.y)} C ${r(c1.x)} ${r(c1.y)}, ${r(c2.x)} ${r(c2.y)}, ${r(end.x)} ${r(end.y)}`;
}

// push moves a point outward along the axis of the edge it sits on — away from
// its own card for the start, back toward the arrow's approach for the end.
function push(p: { x: number; y: number }, side: Side, by: number): { x: number; y: number } {
  switch (side) {
    case "left":
      return { x: p.x - by, y: p.y };
    case "right":
      return { x: p.x + by, y: p.y };
    case "top":
      return { x: p.x, y: p.y - by };
    default:
      return { x: p.x, y: p.y + by };
  }
}

// shortenBy pulls the endpoint out of the target card by `by` pixels, along the
// edge it would have landed on.
function shortenBy(p: { x: number; y: number }, side: Side, by: number): { x: number; y: number } {
  return push(p, side, by);
}

function distance(a: { x: number; y: number }, b: { x: number; y: number }): number {
  return Math.hypot(b.x - a.x, b.y - a.y);
}

// r rounds to whole pixels: sub-pixel coordinates buy nothing at this scale and
// keep a re-render from emitting a different string for an identical layout.
function r(n: number): number {
  return Math.round(n);
}
