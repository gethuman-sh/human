package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// DestructiveEntry represents a destructive operation log record.
type DestructiveEntry struct {
	Timestamp string `json:"timestamp"`
	Operation string `json:"operation"`        // "DeleteIssue", "EditIssue", "TransitionIssue"
	Tracker   string `json:"tracker"`          // instance name
	Kind      string `json:"kind"`             // "jira", "linear", etc.
	Key       string `json:"key"`              // issue key
	Detail    string `json:"detail,omitempty"` // e.g. target status for transition, changed fields for edit
	Error     string `json:"error,omitempty"`
}

// DestructiveNotifier sends fire-and-forget notifications for destructive ops.
type DestructiveNotifier interface {
	NotifyDestructive(ctx context.Context, entry DestructiveEntry) // no error return -- fire-and-forget
}

// DestructiveProvider wraps a Provider and logs destructive operations
// (DeleteIssue, EditIssue, TransitionIssue) to a dedicated log file.
// Optionally sends notifications via a DestructiveNotifier.
type DestructiveProvider struct {
	inner    Provider
	name     string
	kind     string
	logFile  *os.File
	mu       sync.Mutex
	notifier DestructiveNotifier // nil means no notifications
}

// NewDestructiveProvider creates a DestructiveProvider that delegates to inner
// and logs destructive operations to the file at logPath. The notifier may be
// nil, in which case only logging occurs.
func NewDestructiveProvider(inner Provider, name, kind, logPath string, notifier DestructiveNotifier) (*DestructiveProvider, error) {
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- logPath is built by DestructiveLogPath(), not user input
	if err != nil {
		return nil, err
	}
	return &DestructiveProvider{
		inner:    inner,
		name:     name,
		kind:     kind,
		logFile:  f,
		notifier: notifier,
	}, nil
}

// Close closes the underlying log file if present. Struct-literal
// construction in tests and composite wrappers can leave logFile nil,
// so the nil check defends against panics in those paths.
func (d *DestructiveProvider) Close() error {
	if d.logFile == nil {
		return nil
	}
	return d.logFile.Close()
}

func (d *DestructiveProvider) logEntry(ctx context.Context, entry DestructiveEntry) {
	data, marshalErr := json.Marshal(entry)
	if marshalErr != nil {
		log.Warn().Err(marshalErr).Msg("destructive log: marshal failed")
		return
	}
	data = append(data, '\n')

	// Capture the notifier under the lock so the field is read safely, then
	// release the lock before calling out to the network. Holding d.mu across
	// a notifier round-trip would serialise every destructive op behind a
	// single slow upstream and risks recursive deadlock with self-notifying
	// composite notifiers.
	d.mu.Lock()
	if d.logFile != nil {
		if _, writeErr := d.logFile.Write(data); writeErr != nil {
			log.Warn().Err(writeErr).Msg("destructive log: append failed")
		}
	}
	notifier := d.notifier
	d.mu.Unlock()

	if notifier != nil {
		notifier.NotifyDestructive(ctx, entry)
	}
}

func (d *DestructiveProvider) buildEntry(operation, key, detail string, err error) DestructiveEntry {
	entry := DestructiveEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Operation: operation,
		Tracker:   d.name,
		Kind:      d.kind,
		Key:       key,
		Detail:    detail,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	return entry
}

// editDetail builds a human-readable summary of which fields were changed.
func editDetail(opts EditOptions) string {
	var parts []string
	if opts.Title != nil {
		parts = append(parts, "title")
	}
	if opts.Description != nil {
		parts = append(parts, "description")
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("changed: %s", strings.Join(parts, ", "))
}

// --- Destructive operations (logged + notified) ---

func (d *DestructiveProvider) DeleteIssue(ctx context.Context, key string) error {
	err := d.inner.DeleteIssue(ctx, key)
	entry := d.buildEntry("DeleteIssue", key, "", err)
	d.logEntry(ctx, entry)
	return err
}

func (d *DestructiveProvider) EditIssue(ctx context.Context, key string, opts EditOptions) (*Issue, error) {
	detail := editDetail(opts)
	issue, err := d.inner.EditIssue(ctx, key, opts)
	entry := d.buildEntry("EditIssue", key, detail, err)
	d.logEntry(ctx, entry)
	return issue, err
}

func (d *DestructiveProvider) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	err := d.inner.TransitionIssue(ctx, key, targetStatus)
	detail := fmt.Sprintf("-> %s", targetStatus)
	entry := d.buildEntry("TransitionIssue", key, detail, err)
	d.logEntry(ctx, entry)
	return err
}

// --- Non-destructive operations (pass-through) ---

func (d *DestructiveProvider) ListIssues(ctx context.Context, opts ListOptions) ([]Issue, error) {
	return d.inner.ListIssues(ctx, opts)
}

func (d *DestructiveProvider) GetIssue(ctx context.Context, key string) (*Issue, error) {
	return d.inner.GetIssue(ctx, key)
}

func (d *DestructiveProvider) CreateIssue(ctx context.Context, issue *Issue) (*Issue, error) {
	return d.inner.CreateIssue(ctx, issue)
}

func (d *DestructiveProvider) ListComments(ctx context.Context, issueKey string) ([]Comment, error) {
	return d.inner.ListComments(ctx, issueKey)
}

func (d *DestructiveProvider) AddComment(ctx context.Context, issueKey string, body string) (*Comment, error) {
	return d.inner.AddComment(ctx, issueKey, body)
}

func (d *DestructiveProvider) LinkIssues(ctx context.Context, key string, otherKey string) error {
	// Additive like AddComment: linking never needs a destructive confirm.
	return d.inner.LinkIssues(ctx, key, otherKey)
}

func (d *DestructiveProvider) AssignIssue(ctx context.Context, key string, userID string) error {
	// Pair with TransitionIssue logging: RunStartIssue transitions and
	// assigns in one operation and the audit log should capture both
	// halves so operators can reconstruct who took ownership.
	err := d.inner.AssignIssue(ctx, key, userID)
	entry := d.buildEntry("AssignIssue", key, "user="+userID, err)
	d.logEntry(ctx, entry)
	return err
}

func (d *DestructiveProvider) GetCurrentUser(ctx context.Context) (string, error) {
	return d.inner.GetCurrentUser(ctx)
}

func (d *DestructiveProvider) ListStatuses(ctx context.Context, key string) ([]Status, error) {
	return d.inner.ListStatuses(ctx, key)
}
