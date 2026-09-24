package daemon

import "github.com/rs/zerolog"

// doneStageAgentStages are every agent the done stage can legitimately run
// under. The done stage is the ONE stage whose agent name is not derivable from
// the stage: three agents run under it and a plain deploy runs in-process under
// no agent at all, so agentNameFor(key, done) composes board-<key>-done — a
// name nothing has ever launched, which every liveness probe then reads as
// "nobody is there" (SC-5396).
var doneStageAgentStages = []BoardStage{prReviewAgentStage, prFixAgentStage, deployFixAgentStage}

// stageAgentNames names every board agent that could legitimately own
// (pmKey, stage) on this machine.
//
// This is the DAEMON's join and it deliberately differs from the viewer's
// (AgentNamesForCard, board_liveness.go): the viewer answers "paint this card
// live or dead" from a placement alone, so naming the deploy fixer there could
// only ever match a container left over from an earlier round and report a
// false "live" (SC-3569). Here the name is consulted only to SPARE a card from
// being redded, and a stale container that spares one is examined a moment
// later by hungLiveAgent, which stops it and reds the card on the uncharged
// silence path. Sparing wrongly costs a reconcile tick; redding wrongly kills a
// working fixer and charges a retry against the ticket.
func stageAgentNames(pmKey string, stage BoardStage) []string {
	if stage != BoardDoneStage {
		return []string{agentNameFor(pmKey, stage)}
	}
	names := make([]string, 0, len(doneStageAgentStages))
	for _, s := range doneStageAgentStages {
		names = append(names, agentNameFor(pmKey, s))
	}
	return names
}

// liveStageAgent names the agent that is ACTUALLY alive for (pmKey, stage), so
// a caller can probe that container's progress rather than a composed name no
// launcher used.
func liveStageAgent(alive map[string]struct{}, pmKey string, stage BoardStage) (string, bool) {
	for _, name := range stageAgentNames(pmKey, stage) {
		if _, ok := alive[name]; ok {
			return name, true
		}
	}
	return "", false
}

// aliveAgentSet reads the board agents running on this machine into a set. The
// bool reports whether the answer is usable at all: a nil lister or a failed
// lookup cannot establish liveness, and a caller that cannot establish liveness
// must decide on something other than a guess. `what` names the caller in the
// warning, so a probe failure says which pass lost its footing.
func aliveAgentSet(lister LiveAgentLister, logger zerolog.Logger, what string) (map[string]struct{}, bool) {
	if lister == nil {
		return nil, false
	}
	names, err := lister()
	if err != nil {
		logger.Warn().Err(err).Msg("board: cannot list live agents for " + what)
		return nil, false
	}
	alive := make(map[string]struct{}, len(names))
	for _, n := range names {
		alive[n] = struct{}{}
	}
	return alive, true
}
