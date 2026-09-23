package daemon

import "context"

// TransitionDepsResolver builds the transition engine's collaborators for one
// ticket: the daemon's own launcher, run registry, signed commenter and forge
// publisher, resolved against the project the key belongs to.
type TransitionDepsResolver func(pmKey string) (BoardTransitionDeps, error)

type transitionDepsKey struct{}

// WithTransitionDeps carries the daemon's deps resolver on the context a
// forwarded command runs under. A forwarded `human deploy` executes inside the
// daemon process but built its own deps from the CLI's wiring, which has no
// launcher — so the one route that starts a deploy from outside the board could
// never start the review the merge depends on (F10). Handing it the daemon's
// resolver is what lets the CLI route enter the machine instead of running
// beside it. Absent (a direct CLI run, no daemon), the command keeps its own
// wiring and the entry point says what that wiring cannot do.
func WithTransitionDeps(ctx context.Context, resolve TransitionDepsResolver) context.Context {
	if resolve == nil {
		return ctx
	}
	return context.WithValue(ctx, transitionDepsKey{}, resolve)
}

// TransitionDepsFromContext returns the resolver a daemon put on ctx, or nil
// when the command is not running inside one.
func TransitionDepsFromContext(ctx context.Context) TransitionDepsResolver {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(transitionDepsKey{}).(TransitionDepsResolver)
	return r
}
