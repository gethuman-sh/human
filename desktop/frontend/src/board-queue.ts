// Pure column-placement and drop-gate logic for the workflow board, kept free
// of DOM and Wails bindings so it can be unit-tested directly (the rest of
// board.ts bootstraps against document/window.go at import time and cannot).

// Only the fields the placement/gate logic reads. board.ts's Card satisfies it.
export interface QueueCard {
  stage: string;
  state: string;
  verdict?: string;
  branch?: string;
  // Defect ticket: a bug card lives in the Bugs pane, a feature card on the
  // board. The Deploy selectors split on it so each control ships only its own
  // pane's ready cards.
  bug?: boolean;
  options?: { id: string; label: string }[];
  // RFC3339 time the newest marker of the card's current stage landed; feeds
  // the Engineering-backlog age badge. Absent for cards with no derived stage.
  stageEnteredAt?: string;
}

export const QUEUES = ["ideas", "product", "engineering", "building", "deploy"] as const;

// Wire stage launched by dropping onto a queue from its predecessor.
export const QUEUE_TRANSITION_TO: Record<string, string> = {
  engineering: "planning",
  building: "implementation",
};

export function verdictFailed(verdict?: string): boolean {
  return (verdict ?? "").trim().toLowerCase().startsWith("fail");
}

// queueOf maps (stage, state) onto the column that is true of the card. Running
// and failed cards render in their DESTINATION lane, not their origin queue:
// planning lives in Engineering, implementation/verification in Code — the card
// stays visibly where the user dropped it while its stage runs.
export function queueOf(card: QueueCard): string {
  switch (card.stage) {
    case "ideas":
      return "ideas";
    case "backlog":
      return "product";
    case "planning":
      return "engineering";
    case "implementation":
      return "building";
    case "verification":
      return card.state === "done" && !verdictFailed(card.verdict) && !!card.branch ? "deploy" : "building";
    case "done":
      return "deploy";
    default:
      return "product";
  }
}

export function isReworkable(card: QueueCard): boolean {
  return card.stage === "verification" && card.state === "done" && (verdictFailed(card.verdict) || !card.branch);
}

// ageDays converts a card's stage timestamp into whole days elapsed, or null
// when the timestamp is absent or unparseable.
export function ageDays(stageEnteredAt: string | undefined, now: Date): number | null {
  if (!stageEnteredAt) return null;
  const t = Date.parse(stageEnteredAt);
  if (Number.isNaN(t)) return null;
  const days = Math.floor((now.getTime() - t) / 86_400_000);
  return days >= 0 ? days : null;
}

// Age escalation thresholds: a plan is presumed fresh for a week, suspect for
// a second week, and stale after that — the badge color escalates so rotting
// Engineering-backlog work is visible without reading numbers.
const AGE_WARN_DAYS = 7;
const AGE_HOT_DAYS = 14;

// ageBadge describes the "<n>d" pill for a card sitting planned in the
// Engineering backlog. Only done-state planning cards get one — a running
// plan shows the spinner badge and a failed one its error; under a day the
// pill is suppressed rather than shouting "0d" at fresh plans.
export function ageBadge(card: QueueCard, now: Date): { text: string; cls: string } | null {
  if (card.bug || card.stage !== "planning" || card.state !== "done") return null;
  const days = ageDays(card.stageEnteredAt, now);
  if (days === null || days < 1) return null;
  let cls = "age";
  if (days >= AGE_HOT_DAYS) cls = "age hot";
  else if (days >= AGE_WARN_DAYS) cls = "age warn";
  return { text: `${days}d`, cls };
}

// isReplannable reports a card whose finished plan can be regenerated in
// place: a feature ticket sitting planned in the Engineering backlog. The
// codebase may have moved since the plan landed; replanning posts a fresh
// [human:plan] that supersedes the old one (latest wins).
export function isReplannable(card: QueueCard): boolean {
  return !card.bug && card.stage === "planning" && card.state === "done";
}

// isReviewRetryable reports a stage-failed review — a [human:review-failed] card
// (verification/failed). It is a dead end on the board otherwise: the rework
// re-drop needs a DONE verification with a failing verdict, so a failed binding
// gate (missing branch, unreachable commits) has no gesture to try again.
// Mirrors isReworkable; surfaced as the "Retry review" context-menu action so a
// failed review is retryable in place, like failed plans and builds (SC-695).
export function isReviewRetryable(card: QueueCard): boolean {
  return card.stage === "verification" && card.state === "failed";
}

// A DOM-free description of a card's live status badge. board.ts renders it to
// HTML; keeping the CLASSIFICATION here (out of the document-bound board.ts)
// lets the badge branches be unit-tested directly. `spinner` requests the
// running spinner glyph before the text.
export interface BadgeInfo {
  cls: string;
  text: string;
  title: string;
  spinner?: boolean;
}

// Live badge text while a stage runs; builds and their chained reviews both
// live in the Code lane, deploys in Ready to Deploy.
export const RUNNING_LABELS: Record<string, string> = {
  planning: "planning…",
  implementation: "building…",
  verification: "reviewing…",
  done: "deploying…",
};

// The verb per chosen stage for a card a recorded decision has (re)queued but
// whose fresh agent has not yet posted its started marker (SC-1320).
export const QUEUED_LABELS: Record<string, string> = {
  planning: "replanning",
  implementation: "rebuild",
  verification: "re-review",
};

// badgeInfo classifies a card's live state into a badge descriptor, or null
// when the card rests and needs none — its queue position IS the statement of
// completion. A review that found problems is a WARNING, not a stage failure:
// the work exists, it just may not advance until a rebuild passes.
export function badgeInfo(card: QueueCard): BadgeInfo | null {
  if (card.state === "running") {
    return {
      cls: "running",
      text: RUNNING_LABELS[card.stage] ?? "working…",
      title: "Agent running",
      spinner: true,
    };
  }
  // A recorded decision has (re)queued the chosen stage but the relaunched
  // agent has not posted its started marker yet — or the launch was deferred to
  // a healthy daemon. An in-progress, non-failing signal: never red, never a
  // blank card, so the user always sees the choice re-queued the work (SC-1320).
  if (card.state === "queued") {
    const verb = QUEUED_LABELS[card.stage] ?? "work";
    return {
      cls: "queued",
      text: `decision recorded — ${verb} picked up`,
      title: "A direction was chosen — a fresh agent will pick up the work",
      spinner: true,
    };
  }
  // An open decision block outranks EVERY other classification, including a
  // stale failed marker: a card parked on a deliberate human fork must never
  // paint red, even if a *-failed marker also landed on it (the daemon's twin
  // guard in reconcileStuckRunning stops new spurious markers going forward,
  // but a marker posted before that fix — or any other race — must still
  // defer to the open decision here). The `?` glyph reads as a question, not
  // an error (ticket 1290).
  if (card.options && card.options.length > 0) {
    return {
      cls: "decision",
      text: `? decision needed`,
      title: `The stage offers ${card.options.length} ways forward — open the card to choose`,
    };
  }
  if (card.state === "failed") return { cls: "failed", text: "✕", title: "Stage failed" };
  if (card.state === "resolved") {
    if (card.stage === "planning") {
      // The planner verified the ticket's work is already merged, so there is
      // nothing left to plan: a successful terminal outcome, never red, never
      // deployable — the right resolution is Done, not re-planning (ticket 454).
      return { cls: "resolved", text: "already shipped", title: "Work already merged — nothing left to plan" };
    }
    // An autofix run whose triage concluded no fix is warranted (not-a-bug or
    // undetermined): a successful terminal outcome, never red, never deployable
    // (ticket 405).
    return { cls: "resolved", text: "no fix needed", title: "Triage concluded no fix is warranted" };
  }
  if (card.stage === "verification" && card.state === "done" && verdictFailed(card.verdict)) {
    return { cls: "warning", text: "⚠ review found problems", title: `Review verdict: ${card.verdict ?? ""}` };
  }
  if (card.stage === "verification" && card.state === "done" && !card.branch) {
    // A passed review with no recorded branch has nothing to ship — deploying
    // it can only fail, so it must read as needing a rebuild, never as ready.
    return {
      cls: "warning",
      text: "⚠ no branch recorded",
      title: "Review passed but no branch was recorded on the handoff — drop it on the build stage to rebuild",
    };
  }
  // SC-429: fix complete, review not started is a durable hand-off state, not a
  // sub-second transient — it must read as a neutral wait, never render blank.
  if (card.stage === "implementation" && card.state === "done") {
    return { cls: "await", text: "awaiting review…", title: "Fix complete — waiting for review to start" };
  }
  if (card.stage === "done" && card.state === "done") {
    return { cls: "done", text: "deployed", title: "Merged and shipped" };
  }
  return null;
}

// cardError derives the red `.card-error` subtitle from the SAME classifier the
// badge uses, so the two render paths can never disagree: a card only shows its
// failure text when badgeInfo classifies it as an actual stage failure. A card
// parked on an open decision (which outranks a stale *-failed marker in
// badgeInfo) therefore paints the amber decision badge and NO red line — SC-1290
// fixed the badge but left renderCard rendering on raw `card.error` (SC-1301).
export function cardError(card: QueueCard & { error?: string }): string {
  if (!card.error) return "";
  return badgeInfo(card)?.cls === "failed" ? card.error : "";
}

// planReady reports a planning card whose plan is complete — the only planning
// state permitted to advance forward into Code. A running or failed planning
// card in Engineering must NOT launch implementation on an unplanned ticket.
export function planReady(card: QueueCard): boolean {
  return card.stage === "planning" && card.state === "done";
}

// forwardDropAllowed is the queue-transition predicate: forward-adjacency, plus
// the Code rework re-drop, plus the plan-ready gate on advancing OUT of
// Engineering. DOM/Docker gating stays in board.ts's dropAllowed.
export function forwardDropAllowed(card: QueueCard, toQueue: string): boolean {
  if (toQueue === "building" && isReworkable(card)) return true;
  const from = queueOf(card);
  if (!isNextQueue(from, toQueue)) return false;
  // Engineering -> Code may only launch implementation once the plan is ready.
  if (from === "engineering" && toQueue === "building") return planReady(card);
  return true;
}

// sortByHandOrder sorts cards by a saved hand-sorted key list: listed cards
// first in list order, unlisted cards after in their existing (fetch) order.
// In place, relying on Array.prototype.sort's stability for the tail.
export function sortByHandOrder<T extends { key: string }>(cards: T[], order: string[] | undefined): T[] {
  if (!order || order.length === 0) return cards;
  const pos = new Map(order.map((k, i) => [k, i]));
  return cards.sort(
    (a, b) => (pos.get(a.key) ?? Number.MAX_SAFE_INTEGER) - (pos.get(b.key) ?? Number.MAX_SAFE_INTEGER),
  );
}

export interface BoardPayload<C> {
  cards?: C[];
  dockerAvailable?: boolean;
  error?: string;
  columnOrder?: Record<string, string[]>;
}

export interface BoardState<C> {
  cards: C[];
  dockerAvailable: boolean;
  error: string;
  columnOrder?: Record<string, string[]>;
}

// boardStateFromPayload normalizes a BoardData fetch into the runtime `current`
// state, so every reload site rebuilds `current` through ONE path: bug 631 was a
// field-by-field rebuild that silently dropped the board-level columnOrder the
// daemon ships, collapsing the hand-sort back to fetch order. suppressError blanks
// the payload error for the startup quick phase (avoids a flickering banner).
export function boardStateFromPayload<C>(payload: BoardPayload<C>, suppressError = false): BoardState<C> {
  return {
    cards: payload.cards || [],
    dockerAvailable: !!payload.dockerAvailable,
    error: suppressError ? "" : payload.error || "",
    columnOrder: payload.columnOrder,
  };
}

// insertKeyAt rebuilds a column's hand-sorted key list after a same-column
// drop: the dragged key lands before the first resting card whose vertical
// midpoint is below the drop point, or last when the drop was below them all.
export function insertKeyAt(restingKeys: string[], midpoints: number[], dragged: string, dropY: number): string[] {
  const keys: string[] = [];
  let inserted = false;
  restingKeys.forEach((k, i) => {
    if (!inserted && dropY < midpoints[i]) {
      keys.push(dragged);
      inserted = true;
    }
    keys.push(k);
  });
  if (!inserted) keys.push(dragged);
  return keys;
}

export function queueIndex(queue: string): number {
  return (QUEUES as readonly string[]).indexOf(queue);
}

export function isNextQueue(fromQueue: string, toQueue: string): boolean {
  return queueIndex(toQueue) === queueIndex(fromQueue) + 1;
}

// --- Deploy controls (shared by the board's Deploy zone and the Bugs Deploy
// button) -----------------------------------------------------------------
//
// The two Deploy controls are one abstraction with two panes: the same
// readiness gate, the same count/disabled affordance, and (via buildDeployControl
// in board-deploy.ts) the same drop-and-click wiring. Keeping the DOM-free half
// here lets it be unit-tested directly and gives isReadyToDeploy a single home.

// isReadyToDeploy reports a card resting in Ready to Deploy on a passed review
// of a recorded branch — the only cards a Deploy control accepts. Without a
// branch there is nothing to ship: deploying can only fail, so the card must
// never be offered (SC-297).
export function isReadyToDeploy(card: QueueCard): boolean {
  return card.stage === "verification" && card.state === "done" && !verdictFailed(card.verdict) && !!card.branch;
}

// DeploySide names the pane a Deploy control belongs to: the board's feature
// workflow, or the Bugs pane. It selects which ready cards the control ships.
export type DeploySide = "features" | "bugs";

// deployableCards is the click's payload: every ready card in the control's pane
// — feature cards on the board, bug cards in the Bugs pane. The same predicate
// gates the single-card drop, so click and drop can never disagree on what is
// shippable.
export function deployableCards<C extends QueueCard>(cards: C[], side: DeploySide): C[] {
  const wantBug = side === "bugs";
  return cards.filter((c) => !!c.bug === wantBug && isReadyToDeploy(c));
}

// DeployControlView is the DOM-free description a Deploy control renders: how
// many cards a click would ship, whether it is disabled, its caption, and the
// tooltip explaining the disabled state.
export interface DeployControlView {
  count: number;
  disabled: boolean;
  label: string;
  tooltip: string;
}

// deployControlView derives the affordance both controls show from the live card
// list: a count-labelled Deploy caption, disabled with a pane-specific tooltip
// when nothing is ready, enabled with a "ship every…" tooltip otherwise.
export function deployControlView(cards: QueueCard[], side: DeploySide): DeployControlView {
  const count = deployableCards(cards, side).length;
  const noun = side === "bugs" ? "fixed bug" : "ready-to-deploy card";
  return {
    count,
    disabled: count === 0,
    label: `Deploy${count ? ` (${count})` : ""}`,
    tooltip: count === 0 ? `No ${noun}s to deploy yet` : `Ship every ${noun}`,
  };
}

// initialLoadPhase decides the startup render path from whether a cached
// snapshot was available. A hit paints the last-known board instantly and takes
// the "cache" path, SKIPPING the titles-only quick pass — running it after a
// cache paint would regress every card to Backlog and flicker. A miss takes the
// "quick" path (spinner + titles). Either way the full reconcile runs afterward
// and silently swaps in fresh data (stale-while-revalidate).
export function initialLoadPhase(cacheHit: boolean): "cache" | "quick" {
  return cacheHit ? "cache" : "quick";
}
