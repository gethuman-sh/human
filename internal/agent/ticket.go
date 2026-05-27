package agent

import (
	"context"
	"fmt"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// FetchTicketPrompt loads the ticket identified by key from the configured
// tracker instances and formats its content as a prompt string suitable for
// sending to Claude Code.
func FetchTicketPrompt(ctx context.Context, key string, instances []tracker.Instance) (string, error) {
	result, err := tracker.FindTracker(ctx, key, instances)
	if err != nil {
		return "", errors.WrapWithDetails(err, "finding tracker for ticket", "key", key)
	}

	inst, err := tracker.ResolveByKind(result.Provider, instances, "")
	if err != nil {
		return "", errors.WrapWithDetails(err, "resolving tracker instance", "kind", result.Provider)
	}

	issue, err := inst.Provider.GetIssue(ctx, key)
	if err != nil {
		return "", errors.WrapWithDetails(err, "fetching ticket", "key", key)
	}

	prompt := fmt.Sprintf("Implement ticket %s: %s\n\n%s", issue.Key, issue.Title, issue.Description)
	return prompt, nil
}
