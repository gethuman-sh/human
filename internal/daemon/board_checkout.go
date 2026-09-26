package daemon

import (
	"context"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// Checkout-interlock pacing. Package vars so a test can decide in milliseconds.
//
// DeployCheckoutWaitBound is exported because the pipeline document names it as
// a budget (invariants.constants) and `human fsm constants` serves it: a bound a
// reader can only find by reading Go is a bound a plan guesses at.
var (
	checkoutFreeCheckInterval = 15 * time.Second
	// DeployCheckoutWaitBound is how long a starting deploy waits for the
	// implementation container to release the checkout before abandoning the
	// launch. Generous on purpose: the container's own end is what frees the
	// tree, and a run that is merely slow to write its summary must not cost the
	// ticket its deploy.
	DeployCheckoutWaitBound = 15 * time.Minute
)

// checkoutHolder names the board container that still holds pmKey's checkout, if
// any. Only the implementation and verification stages are asked about: they are
// the two that run in the bind-mounted tree the done stage then pushes from. The
// done stage's own agents are excluded deliberately — a second loop step is
// interlocked by the launcher (ErrAgentAlreadyRunning), and counting them here
// would make the deploy wait for itself.
//
// A liveness answer that cannot be established is not "held": a nil lister (the
// bare CLI) or a failed listing (a docker hiccup) would otherwise stall every
// deploy for the whole bound and then abandon it, which costs more than the
// overlap window it is guarding. The package's "nil disables" convention, and
// the same degradation verificationAgentAlive takes.
func (d BoardTransitionDeps) checkoutHolder(pmKey string) (string, bool) {
	alive, known := aliveAgentSet(d.LiveAgents, d.Logger, "the deploy's checkout interlock")
	if !known {
		return "", false
	}
	for _, stage := range []BoardStage{BoardImplementation, BoardVerification} {
		if name, ok := liveStageAgent(alive, pmKey, stage); ok {
			return name, true
		}
	}
	return "", false
}

// awaitCheckoutFree blocks until no implementation or verification container for
// pmKey is alive on this machine, so the deploy never works a tree another stage
// is still writing to.
//
// It WAITS rather than refusing because nothing would re-drive a refusal: no
// reconcile pass moves a plain `reviewed` card into the done stage (the only
// done-stage re-drive is redeploy-after-outage, for a card already at
// substrate-down), so a marker-less refusal of the Deploy gesture would strand
// it.
//
// onQueued, when non-nil, is called ONCE with the holding agent's name at the
// moment the wait begins — never when the checkout is already free. It is how the
// BOARD's routes record the queued deploy on the ticket: the board derives a card
// from markers alone, so a wait held for minutes and recorded nowhere is
// indistinguishable from a refused drop (SC-5878). Nil for the CLI route, whose
// caller is told ErrDeployCheckoutBusy directly and which records nothing before a
// start by design ("nil disables", this package's convention).
//
// Past the bound the launch is abandoned and ErrDeployCheckoutBusy is returned to
// the caller, which decides what that means for the ticket. The done stage is
// still never REDDED for it: a container alive fifteen minutes after its verdict
// is hung and the stuck-running sweep answers for it — redding the done stage
// over another stage's hang would blame the wrong one (SC-5691).
//
// A cancelled wait records no withdrawal: the daemon is going away, and the
// stranded record is retired by the stuck-running pass (abandonStrandedQueuedDeploy).
func (d BoardTransitionDeps) awaitCheckoutFree(ctx context.Context, pmKey string, onQueued func(holder string)) error {
	holder, held := d.checkoutHolder(pmKey)
	if !held {
		return nil
	}
	d.Logger.Info().Str("pm", pmKey).Str("agent", holder).Str("bound", DeployCheckoutWaitBound.String()).
		Msg("deploy: waiting for the implementation container to release the checkout")
	if onQueued != nil {
		onQueued(holder)
	}
	deadline := time.Now().Add(DeployCheckoutWaitBound)
	for {
		select {
		case <-ctx.Done():
			// Classified, never bare: a raw context error reaching the CI
			// classifier reads as "the checks failed" (SC-5395).
			return errors.WrapWithDetails(ctx.Err(),
				"cancelled while waiting for the implementation container to release the checkout",
				"pm", pmKey, "agent", holder)
		case <-time.After(checkoutFreeCheckInterval):
		}
		if holder, held = d.checkoutHolder(pmKey); !held {
			d.Logger.Info().Str("pm", pmKey).Msg("deploy: the checkout is free; continuing")
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.WrapWithDetails(ErrDeployCheckoutBusy,
				"deploy refused: the implementation container for this ticket still holds the checkout — it is finishing its run; re-run Deploy once it has ended",
				"pm", pmKey, "agent", holder, "waited", DeployCheckoutWaitBound.String())
		}
	}
}
