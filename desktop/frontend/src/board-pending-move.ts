// Pure, DOM/Wails-free logic for holding an optimistic drag-and-drop MOVE
// against a stale board fetch. The create-side counterpart lives in
// board-pending.ts; this is the move-side shield (SC-2521).
//
// A dropped card moves in memory immediately — the person must see their
// action at once (removing that instant move is explicitly not wanted). The
// board then reconciles, and the fetch it gets back can predate the drop
// becoming readable on the tracker (read-after-write lag), so it still shows
// the card at its ORIGIN stage. Applied blindly that stale answer snaps the
// card back; the next poll, now caught up, moves it again — the flicker.
//
// The shield discriminates a stale read from a real change by remembering
// where the card came FROM and where it went TO:
//   - fetch still at fromStage  -> stale read: hold the move and override the
//                                  fetched card back to toStage/running.
//   - fetch at toStage          -> the move is confirmed: drop the shield.
//   - fetch at any THIRD stage  -> the daemon changed its mind (moved it
//                                  elsewhere or the launch failed): let it win,
//                                  drop the shield without overriding.
//   - card absent from fetch    -> incomplete answer: hold, do not override.
//   - now past expiresAt        -> a move that never confirmed must not stick
//                                  forever: drop, yielding to truth.
export interface PendingMove {
  key: string;
  fromStage: string;
  toStage: string;
  // Absolute wall-clock deadline (ms) after which the shield yields to truth.
  expiresAt: number;
}

export interface MoveOverride {
  key: string;
  toStage: string;
}

export interface ReconcileMovesResult {
  moves: PendingMove[];
  overrides: MoveOverride[];
}

export function reconcilePendingMoves(
  moves: PendingMove[],
  fetchedStageByKey: Map<string, string>,
  now: number,
): ReconcileMovesResult {
  const kept: PendingMove[] = [];
  const overrides: MoveOverride[] = [];
  for (const m of moves) {
    // A move that never confirms must not stick forever — bounded so the
    // board returns to the truth rather than showing a comfortable lie.
    if (now >= m.expiresAt) continue;

    const fetched = fetchedStageByKey.get(m.key);

    // An incomplete fetch (card not present) does not undo what the person
    // just did — hold it, but there is nothing to override.
    if (fetched === undefined) {
      kept.push(m);
      continue;
    }

    // Confirmed: the fetch caught up to the target. The real card now carries
    // the move, so the shield is no longer needed.
    if (fetched === m.toStage) continue;

    // Stale read still at the origin: hold the move and pin the fetched card
    // back to the target so the held card keeps its launched appearance.
    if (fetched === m.fromStage) {
      kept.push(m);
      overrides.push({ key: m.key, toStage: m.toStage });
      continue;
    }

    // Any third stage is the daemon changing its mind — it wins, no override.
  }
  return { moves: kept, overrides };
}

export function dropPendingMove(
  moves: PendingMove[],
  key: string,
): PendingMove[] {
  return moves.filter((m) => m.key !== key);
}
