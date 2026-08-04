package tracker

import (
	"context"
	stderrors "errors"

	"github.com/gethuman-sh/human/errors"
)

// ErrOwnershipUnsupported reports that a provider cannot express ownership —
// it implements neither Assigner nor CurrentUserGetter. Callers treat this as
// "nothing to do", never as a failure: a backend without an assignee concept
// must not stop a ticket being created or a stage being launched.
var ErrOwnershipUnsupported = stderrors.New("tracker does not support assignment")

// AssignToCurrentUser makes the authenticated identity the owner of key.
//
// It is the single primitive behind both halves of ticket ownership (SC-3345),
// because both reduce to the same call:
//
//   - On creation, the owner should be the reporter. Every tracker stamps the
//     reporter from the credential that created the issue, so for anything this
//     tool creates the reporter IS the authenticated user — no name lookup is
//     needed, and none is possible: Issue.Reporter carries a DISPLAY NAME while
//     Assigner.AssignIssue takes a user ID, and the two are not interchangeable.
//   - When a stage starts, the owner should be whoever picked the work up, which
//     is again the identity whose credential is running it.
//
// A ticket created outside this tool keeps whatever reporter the backend
// recorded; ownership only moves when this tool acts on it.
func AssignToCurrentUser(ctx context.Context, p any, key string) error {
	assigner, canAssign := p.(Assigner)
	getter, canIdentify := p.(CurrentUserGetter)
	if !canAssign || !canIdentify {
		return ErrOwnershipUnsupported
	}
	userID, err := getter.GetCurrentUser(ctx)
	if err != nil {
		return errors.WrapWithDetails(err, "resolving the current user to own the ticket", "key", key)
	}
	if userID == "" {
		return errors.WithDetails("tracker reported no current user to own the ticket", "key", key)
	}
	if err := assigner.AssignIssue(ctx, key, userID); err != nil {
		return errors.WrapWithDetails(err, "assigning the ticket to the current user", "key", key, "user", userID)
	}
	return nil
}

// AssignToCurrentUserBestEffort sets ownership and swallows every failure,
// reporting only whether it landed.
//
// Ownership is metadata about work, never a precondition for it. A ticket that
// was created but could not be assigned is still a created ticket, and a stage
// whose claim did not stick still needs to run — failing either one over an
// assignment would trade a real outcome for a cosmetic one. Callers that want
// the reason log it from the returned error; callers that do not, ignore it.
func AssignToCurrentUserBestEffort(ctx context.Context, p any, key string) (ok bool, err error) {
	if key == "" {
		return false, nil
	}
	if err := AssignToCurrentUser(ctx, p, key); err != nil {
		if stderrors.Is(err, ErrOwnershipUnsupported) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
