// Dependency arrows between cards, kept free of DOM and Wails bindings so the
// geometry can be unit-tested directly (the rest of board.ts bootstraps against
// document/window.go at import time and cannot).
//
// An arrow says the same thing as the "waits for SC-1234" badge, but says it
// without being read. It can only be drawn when both ends are on screen
// together, so the badge stays the statement of record and the arrow is what
// you get when the layout happens to allow it.
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
export function linksWithin(blockers, present) {
    const links = [];
    for (const [to, keys] of blockers) {
        if (!present.has(to))
            continue;
        for (const from of keys) {
            if (present.has(from) && from !== to)
                links.push({ from, to });
        }
    }
    return links.sort((a, b) => a.to.localeCompare(b.to) || a.from.localeCompare(b.from));
}
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
export function sides(from, to) {
    const dx = to.left + to.width / 2 - (from.left + from.width / 2);
    const dy = to.top + to.height / 2 - (from.top + from.height / 2);
    if (Math.abs(dy) > from.height / 2) {
        return dy >= 0 ? { exit: "bottom", enter: "top" } : { exit: "top", enter: "bottom" };
    }
    return dx >= 0 ? { exit: "right", enter: "left" } : { exit: "left", enter: "right" };
}
// anchor returns the point on one edge of a card, at the middle of that edge.
export function anchor(box, side) {
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
export function arrowPath(from, to, edges = sides(from, to)) {
    const { exit, enter } = edges;
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
function push(p, side, by) {
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
function shortenBy(p, side, by) {
    return push(p, side, by);
}
function distance(a, b) {
    return Math.hypot(b.x - a.x, b.y - a.y);
}
// r rounds to whole pixels: sub-pixel coordinates buy nothing at this scale and
// keep a re-render from emitting a different string for an identical layout.
function r(n) {
    return Math.round(n);
}
// plan settles which dependencies are actually drawable, and how.
//
// A pair whose corridor is blocked is dropped rather than drawn across whatever
// sits between them: a line over a third card's face states a relationship
// between the wrong two tickets. Those keep the badge, exactly like a pair split
// across two columns — the rule is the same one, that an arrow is drawn only
// where it reads.
export function plan(links, boxes) {
    const drawn = [];
    for (const link of links) {
        const from = boxes.get(link.from);
        const to = boxes.get(link.to);
        if (!from || !to)
            continue;
        const others = [...boxes.entries()]
            .filter(([key]) => key !== link.from && key !== link.to)
            .map(([, box]) => box);
        if (!corridorClear(from, to, others))
            continue;
        drawn.push({ ...link, ...sides(from, to) });
    }
    return drawn;
}
// corridorClear reports whether the space an arrow would cross holds no other
// card. The corridor is the box spanning the two facing edges — for neighbours
// that is the gap between them, and for a pair with a card in between it is a
// box that card sits inside.
export function corridorClear(from, to, others) {
    const { exit, enter } = sides(from, to);
    const a = anchor(from, exit);
    const b = anchor(to, enter);
    const corridor = {
        left: Math.min(a.x, b.x),
        top: Math.min(a.y, b.y),
        width: Math.abs(b.x - a.x),
        height: Math.abs(b.y - a.y),
    };
    return !others.some((o) => overlaps(corridor, o));
}
// overlaps is a strict rectangle intersection: two boxes that merely touch
// edges do not overlap, so a neighbour flush against the corridor's boundary
// does not count as standing in it.
function overlaps(a, b) {
    return (a.left < b.left + b.width &&
        b.left < a.left + a.width &&
        a.top < b.top + b.height &&
        b.top < a.top + a.height);
}
// gapsBySide reports, per card, which of its edges an arrow attaches to — the
// sides that must be narrowed to make room.
//
// A card in the middle of a chain is waited on from one side and waits on the
// other, so it narrows on both; that is not a case in the code, it is what
// collecting sides per card rather than per arrow produces.
export function gapsBySide(drawn) {
    const gaps = new Map();
    const add = (key, side) => {
        const sides = gaps.get(key) ?? new Set();
        sides.add(side);
        gaps.set(key, sides);
    };
    for (const d of drawn) {
        add(d.from, d.exit);
        add(d.to, d.enter);
    }
    return new Map([...gaps].map(([key, sides]) => [key, [...sides].sort()]));
}
