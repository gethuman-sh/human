package cmdutil

import (
	"context"
	"fmt"

	"github.com/gethuman-sh/human/internal/dispatch"
	"github.com/gethuman-sh/human/internal/tracker"
)

// DispatchDestructiveNotifier adapts dispatch.Notifier to tracker.DestructiveNotifier.
type DispatchDestructiveNotifier struct {
	Notifier dispatch.Notifier
	ChatID   int64 // Telegram chat ID (0 if not applicable)
}

// NotifyDestructive sends a fire-and-forget notification for a destructive operation.
func (d *DispatchDestructiveNotifier) NotifyDestructive(ctx context.Context, entry tracker.DestructiveEntry) {
	msg := FormatDestructiveMessage(entry)
	// Fire-and-forget: ignore error
	_ = d.Notifier.Notify(ctx, d.ChatID, msg)
}

// FormatDestructiveMessage builds a human-readable notification message from a DestructiveEntry.
func FormatDestructiveMessage(entry tracker.DestructiveEntry) string {
	base := fmt.Sprintf("[DESTRUCTIVE] %s on %s (tracker: %s/%s)", entry.Operation, entry.Key, entry.Tracker, entry.Kind)
	if entry.Detail != "" {
		base = fmt.Sprintf("%s %s", base, entry.Detail)
	}
	if entry.Error != "" {
		base = fmt.Sprintf("%s [error: %s]", base, entry.Error)
	}
	return base
}
