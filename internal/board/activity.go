package board

import (
	"strings"
	"time"

	"github.com/gethuman-sh/human/internal/agentstate"
)

// StagePrefix is the namespace a run writes its own phase records under. Every
// pipeline stage already records one — stage.preflight, stage.triage,
// stage.fix, stage.verify, stage.review, stage.pr-review — with a timestamp,
// because the next stage (or a fresh agent taking over from one that died) reads
// them back instead of re-deriving what it learned.
const StagePrefix = "stage."

// boardStages are the phase names the board's own columns already carry. They
// are excluded from the activity read because they say nothing a card does not
// already show: a card in the Fix column reporting "implementation" is noise,
// while the same card reporting "verify" is the answer to the only question a
// spinner cannot answer.
var boardStages = map[string]bool{
	"planning":       true,
	"implementation": true,
	"verification":   true,
	"done":           true,
	"ticket-review":  true,
}

// LatestActivity returns the most recent phase a run recorded, and when.
//
// This is the whole of what "is it progressing?" needs, and none of it is new
// information: the records are written today, with timestamps, and the board has
// simply never read them. Between [human:implementation-started] and
// [human:ready-for-review] a fix run passes through triage, an adversarial
// challenge, a plan, the fix itself and verification, and every one of those
// collapses into a single unchanging "fixing…" — which reads exactly the same at
// thirty seconds and at fourteen hours, and exactly the same when the agent
// behind it has been dead since yesterday.
//
// Empty name means nothing was recorded, which is honest: a run that wrote no
// phase gets no phase shown rather than an invented one.
func LatestActivity(entries []agentstate.Entry) (string, time.Time) {
	var name string
	var at time.Time
	for _, e := range entries {
		phase, ok := strings.CutPrefix(e.Name, StagePrefix)
		if !ok || phase == "" || boardStages[phase] {
			continue
		}
		// A phase namespace can nest (stage.pr-review.round); the head is the phase.
		if head, _, cut := strings.Cut(phase, "."); cut {
			phase = head
		}
		if e.UpdatedAt.After(at) {
			name, at = phase, e.UpdatedAt
		}
	}
	return name, at
}

// ActivityLabel renders a recorded phase for a card badge: the run's own word for
// what it is doing, in the reader's language rather than the pipeline's.
// An unmapped phase is shown verbatim — a new stage should appear the day it is
// added, not the day someone remembers to extend a table.
func ActivityLabel(phase string) string {
	switch phase {
	case "":
		return ""
	case "preflight":
		return "checking scope"
	case "triage":
		return "reproducing"
	case "challenge":
		return "challenging the verdict"
	case "fix":
		return "writing the fix"
	case "verify":
		return "verifying"
	case "review":
		return "reviewing"
	case "opinion":
		return "second opinion"
	case "pr-review":
		return "reviewing the PR"
	case "pr-fix":
		return "addressing review findings"
	case "deploy-fix":
		return "recovering the deploy"
	default:
		return phase
	}
}
