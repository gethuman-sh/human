package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// PMCreatorResolver resolves the single PM-role tracker Creator and its first
// configured project, mirroring the role-based resolution of resolvePMCommenter.
type PMCreatorResolver func() (creator tracker.Creator, project string, err error)

// IdeaCreator quick-captures ideas on the PM tracker. It holds no session and
// no conversation: capture is a single write, and the thinking that used to
// precede it now happens after it, in the background drafter and the
// description editor.
type IdeaCreator struct {
	ResolveCreator PMCreatorResolver
	Notify         func() // pokes the subscribe loop after creation; nil ok
}

// createOwnedIssue creates an issue and makes its creator the owner, so a ticket
// the board produces is never ownerless (SC-3345). Ownership is applied
// best-effort, so a refused claim still yields the created ticket.
func createOwnedIssue(creator tracker.Creator, issue *tracker.Issue) (*tracker.Issue, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	created, err := creator.CreateIssue(ctx, issue)
	if err != nil || created == nil {
		return created, err
	}
	_, _ = tracker.AssignToCurrentUserBestEffort(ctx, creator, created.Key)
	return created, nil
}

// CreateIdea quick-captures a title-only ticket carrying the idea label — the
// Ideas column's `+` button. No agent involved: a background drafter writes the
// description while the idea waits, and promotion opens the description editor
// on it.
func (c *IdeaCreator) CreateIdea(title string) (key, url string, err error) {
	if strings.TrimSpace(title) == "" {
		return "", "", errors.WithDetails("idea title must not be empty")
	}
	if c.ResolveCreator == nil {
		return "", "", errors.WithDetails("no PM ticket creator configured")
	}
	creator, project, err := c.ResolveCreator()
	if err != nil {
		return "", "", err
	}
	// An idea is owned by whoever raised it, from the moment it exists.
	created, err := createOwnedIssue(creator, &tracker.Issue{
		Project: project,
		Title:   title,
		Labels:  []string{tracker.IdeaLabel},
	})
	if err != nil {
		return "", "", errors.WrapWithDetails(err, "creating idea ticket", "project", project)
	}
	if created == nil {
		// A provider must return the created issue on success; a broken one
		// failing that contract must surface as an error, not a panic.
		return "", "", errors.WithDetails("tracker returned no issue for the created idea", "project", project)
	}
	if c.Notify != nil {
		c.Notify()
	}
	return created.Key, created.URL, nil
}
