// Pure column-placement and drop-gate logic for the workflow board, kept free
// of DOM and Wails bindings so it can be unit-tested directly (the rest of
// board.ts bootstraps against document/window.go at import time and cannot).

// Only the fields the placement/gate logic reads. board.ts's Card satisfies it.
export interface QueueCard {
  stage: string;
  state: string;
  verdict?: string;
  // The daemon's own answer to "does this verdict block the card": computed by
  // daemon.VerdictFailed and shipped on the payload. Read this, never `verdict`.
  verdictFailed?: boolean;
  // The phase the run itself last recorded — the only thing on a running card
  // that changes as work advances.
  activity?: string;
  activityAt?: string;
  branch?: string;
  // The failed/outage marker's one-line reason (SC-3024: also read for the
  // paused register, not only "failed" — see cardError).
  error?: string;
  // RFC3339 instant a paused (outage) card's standing marker stated as when
  // the substrate clears, when one was parsed out of the diagnosis. Absent
  // when no time was stated — the badge then reads "paused" with no time.
  resumeAt?: string;
  // Defect ticket: a bug card lives in the Bugs pane, a feature card on the
  // board. The Deploy selectors split on it so each control ships only its own
  // pane's ready cards.
  bug?: boolean;
  // Security ticket: lives in the Security half of the Bugs pane. Mutually
  // exclusive with bug (the classification tokens are disjoint) — the Deploy
  // selectors treat it as a third pane.
  security?: boolean;
  options?: { id: string; label: string }[];
  // RFC3339 time the newest marker of the card's current stage landed; feeds
  // the Engineering-backlog age badge. Absent for cards with no derived stage.
  stageEnteredAt?: string;
  // Done-stage sub-phase: "pr-review" while the machine reviewer runs, "pr-fix"
  // while the fixer runs, absent for a plain deploy.
  deployPhase?: string;
  // What this viewer's machine could see of the agent behind the card, filled by
  // the desktop overlay and never by the daemon (SC-3569): "live" = a board agent
  // is running here; "dead" = the stage is this machine's to run and nothing is
  // running it; "elsewhere" = another machine's daemon owns the stage, so it
  // cannot be seen from here. ABSENT means unknown — render exactly as before,
  // because absence of a signal is never proof.
  agentLiveness?: string;
  // The pre-planning gate's recorded STOP verdict (superseded/escalated/
  // rejected) and the ticket it names. Present only on a decided card; drives
  // the "decided" badge that distinguishes it from a card merely waiting.
  stopDecision?: string;
  stopLinkedKey?: string;
}

export const QUEUES = ["ideas", "product", "engineering", "building", "deploy"] as const;

// Wire stage launched by dropping onto a queue from its predecessor.
export const QUEUE_TRANSITION_TO: Record<string, string> = {
  engineering: "planning",
  building: "implementation",
};

// verdictFailed reports whether the review verdict blocks the card. The answer
// is the DAEMON's — it arrives on the payload as verdictFailed, computed by
// daemon.VerdictFailed. The board must not re-derive it: this function used to
// test `verdict` for a "fail" prefix and nothing else, while the daemon also
// treats "incomplete" as blocking. The reviewer posts "incomplete" for a ticket
// whose acceptance criteria are unmet, so on those cards the board offered
// Deploy and withheld Rework while the daemon refused every Deploy drop — a card
// with no move that could succeed.
//
// The string fallback exists only for a payload from a daemon predating the
// field, and it mirrors the Go rule exactly rather than reinventing it.
export function verdictFailed(card: Pick<QueueCard, "verdict" | "verdictFailed">): boolean {
  if (card.verdictFailed !== undefined) return card.verdictFailed;
  const v = (card.verdict ?? "").trim().toLowerCase();
  return v.startsWith("fail") || v.startsWith("incomplete");
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
      return card.state === "done" && !verdictFailed(card) && !!card.branch ? "deploy" : "building";
    case "done":
      return "deploy";
    default:
      return "product";
  }
}

export function isReworkable(card: QueueCard): boolean {
  return card.stage === "verification" && card.state === "done" && (verdictFailed(card) || !card.branch);
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
  if (card.bug || card.security || card.stage !== "planning" || card.state !== "done") return null;
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
  return !card.bug && !card.security && card.stage === "planning" && card.state === "done";
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

// The badge word per half of the pre-merge review→fix loop. Both halves used to
// read "PR review…", so a card whose live container was -prfix running the PR
// fixer said the reviewer was working (SC-4151 F15, SC-3569). The daemon
// carries the datum (BoardViewCard.deployPhase); the copy lives here, as with
// STOP_DECISION_LABELS.
export const DEPLOY_PHASE_LABELS: Record<string, string> = {
  "pr-review": "PR review…",
  "pr-fix": "fixing PR findings…",
};

// The verb per chosen stage for a card a recorded decision has (re)queued but
// whose fresh agent has not yet posted its started marker (SC-1320).
export const QUEUED_LABELS: Record<string, string> = {
  planning: "replanning",
  implementation: "rebuild",
  verification: "re-review",
};

// Human phrasing for each pre-planning stop verdict — the card must say WHICH
// decision was reached in these terms, never the internal head token (SC-2699).
export const STOP_DECISION_LABELS: Record<string, { text: string; title: string }> = {
  superseded: {
    text: "duplicate",
    title: "The pre-planning gate found this is a symptom of another ticket, which carries the work",
  },
  escalated: {
    text: "needs design decision",
    title: "The pre-planning gate created a design ticket that must be decided before this can proceed",
  },
  rejected: {
    text: "not a real problem",
    title: "The pre-planning gate concluded this is not a real problem, with the evidence on the card",
  },
};

// livenessBadge folds the viewer's liveness overlay into a badge that would
// otherwise assert work is happening.
//
// The spinner IS the claim "a process is alive right now", so it survives only
// for a card an agent was actually found behind. A dead card moves to the
// needs-a-person register and says plainly that nothing is running; a card
// another machine owns says that instead — neither a false spinner nor a false
// death, because this machine genuinely cannot see a peer's containers.
// Unknown liveness returns the base badge untouched (SC-3569).
function livenessBadge(base: BadgeInfo, liveness: string | undefined, deadText: string, deadTitle: string): BadgeInfo {
  if (liveness === "dead") {
    return { cls: "stalled", text: deadText, title: deadTitle, spinner: false };
  }
  if (liveness === "elsewhere") {
    return {
      cls: "elsewhere",
      text: `${base.text} on another machine`,
      title: "Another machine's daemon owns this stage — its agent cannot be seen from here.",
      spinner: false,
    };
  }
  return base;
}

// badgeInfo classifies a card's live state into a badge descriptor, or null
// when the card rests and needs none — its queue position IS the statement of
// completion. A review that found problems is machine-fixing work, not a demand
// on the user: the daemon auto-launches a fixer, so it reads as in-flight
// (gray + spinner), never the amber "your turn" register (SC-1830).
// activityStaleAfterMs is how long a recorded phase may stand before the badge
// starts saying how old it is. Short enough that a stall shows within a coffee
// break; long enough that an ordinary phase — a plan, a build, a review — is
// not decorated with an age for merely taking its normal time.
const activityStaleAfterMs = 10 * 60 * 1000;

// sinceText renders how long ago an RFC3339 instant was, in the board's compact
// house form. An absent or unparseable timestamp yields "" — an age the board
// could not read must never be printed as an age of zero.
export function sinceText(at: string | undefined, nowMs: number): string {
  if (!at) return "";
  const t = Date.parse(at);
  if (Number.isNaN(t)) return "";
  const secs = Math.max(0, Math.floor((nowMs - t) / 1000));
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

// activityAge is the parenthesised age the badge carries, and only once the
// phase is old enough to be worth reading.
function activityAge(at: string | undefined, nowMs: number): string {
  if (!at) return "";
  const t = Date.parse(at);
  if (Number.isNaN(t) || nowMs - t < activityStaleAfterMs) return "";
  return `(${sinceText(at, nowMs)})`;
}

export function badgeInfo(card: QueueCard, nowMs: number = Date.now()): BadgeInfo | null {
  // An open decision block outranks EVERY other classification, including a
  // stale failed marker: a card parked on a deliberate human fork must never
  // paint red, even if a *-failed marker also landed on it (the daemon's twin
  // guard in reconcileStuckRunning stops new spurious markers going forward,
  // but a marker posted before that fix — or any other race — must still
  // defer to the open decision here). The `?` glyph reads as a question, not
  // an error (ticket 1290). This must come before the running/queued
  // early-returns below too — a card can still read as running or queued
  // server-side while an open decision sits on it (SC-1669).
  if (card.options && card.options.length > 0) {
    return {
      cls: "decision",
      text: `? decision needed`,
      title: `The stage offers ${card.options.length} ways forward — open the card to choose`,
    };
  }
  // A pre-planning stop decision distinguishes a decided card from one merely
  // waiting. It is a settled outcome, not a demand — no spinner, no red — and it
  // outranks the plain state rendering (a decided card is stage=backlog/done,
  // which otherwise badges as nothing). The linked ticket, when named, rides the
  // badge text so the key is visible on the card face (SC-2699).
  if (card.stopDecision) {
    const label = STOP_DECISION_LABELS[card.stopDecision] ?? {
      text: card.stopDecision,
      title: "The pre-planning gate stopped this ticket",
    };
    const text = card.stopLinkedKey ? `${label.text} → ${card.stopLinkedKey}` : label.text;
    return { cls: "decided", text, title: label.title };
  }
  if (card.state === "running") {
    const stageText =
      card.stage === "done" && card.deployPhase
        ? (DEPLOY_PHASE_LABELS[card.deployPhase] ?? "PR review…")
        : (RUNNING_LABELS[card.stage] ?? "working…");
    // The run's own phase, when it recorded one. Without it the badge says the
    // same word for the whole of a fix run — triage, the challenge, the plan,
    // the fix, verification — so it reads identically at thirty seconds and at
    // fourteen hours, and identically again when the agent behind it is dead.
    // The phase changing is the only thing on the card that shows movement.
    //
    // Its changing was still the only signal: a phase that STOPPED changing read
    // exactly like one that never had to. The daemon has always sent the
    // timestamp beside the phase and the card dropped it on the floor
    // (SC-4151 B4), so a badge could say "triaging…" over a record six hours
    // old. The age now rides the badge once it is old enough to mean something.
    const age = activityAge(card.activityAt, nowMs);
    const text = card.activity ? `${card.activity}…${age !== "" ? ` ${age}` : ""}` : stageText;
    const title = card.activity
      ? `Agent running — ${card.activity}${card.activityAt ? `, last recorded ${sinceText(card.activityAt, nowMs)}` : ""}`
      : "Agent running";
    return livenessBadge(
      { cls: "running", text, title, spinner: true },
      card.agentLiveness,
      `${text} — agent not running`,
      "No agent is running this stage on this machine — the run died or was stopped. Retry it, or drop the card on its stage again.",
    );
  }
  // A recorded decision has (re)queued the chosen stage but the relaunched
  // agent has not posted its started marker yet — or the launch was deferred to
  // a healthy daemon. An in-progress, non-failing signal: never red, never a
  // blank card, so the user always sees the choice re-queued the work (SC-1320).
  if (card.state === "queued") {
    const verb = QUEUED_LABELS[card.stage] ?? "work";
    return livenessBadge(
      {
        cls: "queued",
        text: `decision recorded — ${verb} picked up`,
        title: "A direction was chosen — a fresh agent will pick up the work",
        spinner: true,
      },
      card.agentLiveness,
      `decision recorded — ${verb} never started`,
      "A direction was chosen but no agent picked the work up on this machine. Retry the stage.",
    );
  }
  // A paused (outage) card is the do-nothing register: a substrate the run
  // depends on is unavailable, the work stays written and safe on the ticket,
  // and it resumes on its own — never the amber "your turn" register and
  // never the spinner (nothing is actively running while paused). This is the
  // one case that renders EVERY unavailable-substrate card, not only a model
  // usage limit (SC-3024).
  if (card.state === "outage") {
    const resumes = formatResume(card.resumeAt);
    const substrate = card.error || "a substrate it depends on is unavailable";
    return {
      cls: "paused",
      text: resumes ? `paused — ${substrate}, resumes ${resumes}` : `paused — ${substrate}`,
      title: "Paused — the work is safe and continues automatically. Nothing to do.",
      spinner: false,
    };
  }
  if (card.state === "failed") return { cls: "failed", text: "✕", title: card.error || "Stage failed" };
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
  // A failed review verdict is machine work, not a demand on the user: the
  // daemon auto-launches a fixer (internal/daemon/board_failure.go →
  // pr_review_loop.go). It must read as in-flight — the gray machine-working
  // `fixing` badge with the running spinner and copy that says what is
  // happening — never the amber `warning`/`decision` "your turn" register with
  // a ⚠ glyph (SC-1830).
  if (card.stage === "verification" && card.state === "done" && verdictFailed(card)) {
    return livenessBadge(
      {
        cls: "fixing",
        text: "review found problems — fixing…",
        title: `Review found problems — a fixer is reworking the code automatically (verdict: ${card.verdict ?? ""})`,
        spinner: true,
      },
      card.agentLiveness,
      "review found problems — no fixer running",
      `Review found problems and no fixer is running on this machine (verdict: ${card.verdict ?? ""}). Drop the card on the build stage to rework it.`,
    );
  }
  // A passed review with no recorded branch is a BROKEN HANDOFF that genuinely
  // needs a person: nothing can ship, so it keeps the needs-a-human `warning`
  // register (SC-1830 split the two cases that used to share this class).
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

// formatResume renders a paused card's stated resume instant in the reader's
// own local timezone as "HH:MM", or "" when absent/unparseable — the badge
// then falls back to plain "paused — <substrate>" wording with no time.
export function formatResume(iso?: string): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  return new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

// cardError derives the red/amber `.card-error` subtitle from the SAME
// classifier the badge uses, so the two render paths can never disagree: a
// card only shows its explanation when badgeInfo classifies it as an actual
// failure OR a paused substrate — a card parked on an open decision (which
// outranks a stale *-failed marker in badgeInfo) therefore paints the amber
// decision badge and NO explanation line (SC-1301). The paused register is
// included so its explanation is never suppressed for being drawn in a state
// the original suppression rule (bare "failed") did not anticipate (SC-3024).
export function cardError(card: QueueCard): string {
  if (!card.error) return "";
  const cls = badgeInfo(card)?.cls;
  return cls === "failed" || cls === "paused" ? card.error : "";
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
  notice?: string;
  truncation?: string;
  columnOrder?: Record<string, string[]>;
  // Declared not-mine dimming, in percent of full opacity; absent means the
  // stylesheet default applies (SC-3409).
  dimPercent?: number;
}

export interface BoardState<C> {
  cards: C[];
  dockerAvailable: boolean;
  error: string;
  notice: string;
  truncation: string;
  columnOrder?: Record<string, string[]>;
  dimPercent?: number;
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
    notice: payload.notice || "",
    truncation: payload.truncation || "",
    columnOrder: payload.columnOrder,
    dimPercent: payload.dimPercent,
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
  return card.stage === "verification" && card.state === "done" && !verdictFailed(card) && !!card.branch;
}

// DeploySide names the pane a Deploy control belongs to: the board's feature
// workflow, or "defects" — the single Deploy control shared by the Bugs and
// Security halves, which ships fixed cards of both kinds. It selects which ready
// cards the control ships. ("bugs"/"security" name a card's own kind via
// deploySideOf; only "features" and "defects" are ever passed to a control.)
export type DeploySide = "features" | "bugs" | "security" | "defects";

// deploySideOf maps a card to its own kind — bug and security are disjoint,
// everything else is a feature. The one place the split is decided, so the
// selectors below cannot drift apart.
export function deploySideOf(c: QueueCard): "features" | "bugs" | "security" {
  if (c.security) return "security";
  if (c.bug) return "bugs";
  return "features";
}

// deployMatches reports whether a card of the given kind belongs to a control's
// side. "defects" is the union of bug and security — the shared Deploy button;
// every other side matches its own kind exactly.
function deployMatches(kind: "features" | "bugs" | "security", side: DeploySide): boolean {
  if (side === "defects") return kind === "bugs" || kind === "security";
  return kind === side;
}

// deployableCards is the click's payload: every ready card the control ships —
// feature cards on the board, both bug and security cards for the shared
// "defects" control. The same predicate gates the single-card drop, so click and
// drop can never disagree on what is shippable.
export function deployableCards<C extends QueueCard>(cards: C[], side: DeploySide): C[] {
  return cards.filter((c) => deployMatches(deploySideOf(c), side) && isReadyToDeploy(c));
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
  // queueOf sends every done-stage card to the deploy lane whatever its state,
  // while isReadyToDeploy takes only a reviewed verification/done card with a
  // branch. So the lane can hold cards the control refuses, and it said "none to
  // deploy yet" with those cards visible beside it — a flat contradiction the
  // reader has to resolve by guessing (SC-4151 F16). Counting them lets the
  // empty state say which of the two things is true.
  const waiting = cards.filter((c) => deployMatches(deploySideOf(c), side) && inDeployLaneNotReady(c)).length;
  if (side === "defects") {
    return {
      count,
      disabled: count === 0,
      label: `Deploy${count ? ` (${count})` : ""}`,
      tooltip: count === 0 ? emptyTooltip(waiting, "fixed bugs or vulnerabilities") : "Ship every fixed bug and vulnerability",
    };
  }
  const noun =
    side === "bugs" ? "fixed bug" : side === "security" ? "fixed vulnerability" : "ready-to-deploy card";
  return {
    count,
    disabled: count === 0,
    label: `Deploy${count ? ` (${count})` : ""}`,
    tooltip: count === 0 ? emptyTooltip(waiting, `${noun}s`) : `Ship every ${noun}`,
  };
}

// inDeployLaneNotReady reports a card sitting in the deploy lane that the Deploy
// control will not take — still shipping, or stopped on the way.
function inDeployLaneNotReady(card: QueueCard): boolean {
  return queueOf(card) === "deploy" && !isReadyToDeploy(card);
}

// emptyTooltip explains a disabled Deploy control. "Nothing to deploy yet" is
// true only when the lane is empty; with cards in it the control is disabled
// because those cards are not ready, which is a different sentence. plural is
// the already-pluralised noun phrase, so each caller keeps its own wording.
function emptyTooltip(waiting: number, plural: string): string {
  if (waiting === 0) return `No ${plural} to deploy yet`;
  return `${waiting} card${waiting === 1 ? "" : "s"} here ${waiting === 1 ? "is" : "are"} still finishing or stopped — none is ready to ship`;
}

// safetyPollShouldReconcile decides whether the 90s safety poll runs its
// reconcile on a given tick. It intentionally does NOT gate on daemon
// reachability: the poll exists to bound staleness for tracker writes that emit
// no daemon event, and the reachability flag can read false while the daemon is
// alive and Cards() succeeds — gating on it silently removed the only bound
// (SC-1677). reconcile() has its own error path, so an actually-dead daemon is
// surfaced by the fetch failure, not pre-empted by this flag.
export function safetyPollShouldReconcile(_daemonReachable: boolean): boolean {
  return true;
}

// safetyReconcileError builds the board state after a safety-poll fetch failure.
// Blanking a populated board on a transient hiccup would itself present a board
// that "is not current" (SC-1677), so keep the last-known cards and surface the
// failure as an explicit staleness banner. An already-empty board shows the
// plain error unchanged.
export function safetyReconcileError<C>(
  prev: { cards: C[]; dockerAvailable: boolean; columnOrder?: Record<string, string[]> },
  message: string,
): { cards: C[]; dockerAvailable: boolean; error: string; columnOrder?: Record<string, string[]> } {
  if (prev.cards.length > 0) {
    return { ...prev, error: `Board may be stale — ${message}` };
  }
  // Docker was not what failed and was not probed, so the last known answer
  // stands. Reporting it unavailable here disabled every agent-launching
  // gesture with the tooltip "Docker required" (SC-4151 G17).
  return { cards: [], dockerAvailable: prev.dockerAvailable, error: message };
}

// isReopenable reports a card the pipeline RESOLVED: it concluded there is
// nothing to plan ([human:nothing-to-do]) or no fix is needed
// ([human:no-fix-needed]). Both are clean terminals — never red, never retried —
// which also meant no gesture could move them, so a verdict a person judged
// wrong could only be undone by editing the tracker by hand. Re-opening is the
// human override; the machine still never retries a terminal of its own accord.
export function isReopenable(card: QueueCard): boolean {
  return card.state === "resolved";
}
