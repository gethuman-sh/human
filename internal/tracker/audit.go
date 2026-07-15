package tracker

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// AuditEntry represents a single audit log record written as a JSON line.
type AuditEntry struct {
	Timestamp  string `json:"timestamp"`
	Operation  string `json:"operation"`
	Tracker    string `json:"tracker"`
	Kind       string `json:"kind"`
	Key        string `json:"key"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// AuditProvider wraps a Provider and logs every method call to a JSON Lines file.
type AuditProvider struct {
	inner   Provider
	name    string
	kind    string
	logFile *os.File
	mu      sync.Mutex
}

// NewAuditProvider creates an AuditProvider that delegates to inner and appends
// audit entries to the file at logPath. The file is created if it does not exist.
func NewAuditProvider(inner Provider, name, kind, logPath string) (*AuditProvider, error) {
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- logPath is built by auditLogPath(), not user input
	if err != nil {
		return nil, err
	}
	return &AuditProvider{
		inner:   inner,
		name:    name,
		kind:    kind,
		logFile: f,
	}, nil
}

// Close closes the underlying log file if present. Nil defends
// against panics from struct-literal test builds and double-close.
func (a *AuditProvider) Close() error {
	if a.logFile == nil {
		return nil
	}
	return a.logFile.Close()
}

func (a *AuditProvider) log(operation, key string, d time.Duration, err error) {
	entry := AuditEntry{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Operation:  operation,
		Tracker:    a.name,
		Kind:       a.kind,
		Key:        key,
		DurationMs: d.Milliseconds(),
	}
	if err != nil {
		entry.Error = err.Error()
	}

	data, marshalErr := json.Marshal(entry)
	if marshalErr != nil {
		log.Warn().Err(marshalErr).Msg("audit log: marshal failed")
		return
	}
	data = append(data, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.logFile == nil {
		return
	}
	if _, writeErr := a.logFile.Write(data); writeErr != nil {
		log.Warn().Err(writeErr).Msg("audit log: append failed")
	}
}

func (a *AuditProvider) ListIssues(ctx context.Context, opts ListOptions) ([]Issue, error) {
	start := time.Now()
	issues, err := a.inner.ListIssues(ctx, opts)
	a.log("ListIssues", opts.Project, time.Since(start), err)
	return issues, err
}

func (a *AuditProvider) GetIssue(ctx context.Context, key string) (*Issue, error) {
	start := time.Now()
	issue, err := a.inner.GetIssue(ctx, key)
	a.log("GetIssue", key, time.Since(start), err)
	return issue, err
}

func (a *AuditProvider) CreateIssue(ctx context.Context, issue *Issue) (*Issue, error) {
	start := time.Now()
	created, err := a.inner.CreateIssue(ctx, issue)
	a.log("CreateIssue", issue.Project, time.Since(start), err)
	return created, err
}

func (a *AuditProvider) DeleteIssue(ctx context.Context, key string) error {
	start := time.Now()
	err := a.inner.DeleteIssue(ctx, key)
	a.log("DeleteIssue", key, time.Since(start), err)
	return err
}

func (a *AuditProvider) ListComments(ctx context.Context, issueKey string) ([]Comment, error) {
	start := time.Now()
	comments, err := a.inner.ListComments(ctx, issueKey)
	a.log("ListComments", issueKey, time.Since(start), err)
	return comments, err
}

func (a *AuditProvider) AddComment(ctx context.Context, issueKey string, body string) (*Comment, error) {
	start := time.Now()
	comment, err := a.inner.AddComment(ctx, issueKey, body)
	a.log("AddComment", issueKey, time.Since(start), err)
	return comment, err
}

func (a *AuditProvider) LinkIssues(ctx context.Context, key string, otherKey string) error {
	start := time.Now()
	err := a.inner.LinkIssues(ctx, key, otherKey)
	a.log("LinkIssues", key, time.Since(start), err)
	return err
}

func (a *AuditProvider) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	start := time.Now()
	err := a.inner.TransitionIssue(ctx, key, targetStatus)
	a.log("TransitionIssue", key, time.Since(start), err)
	return err
}

func (a *AuditProvider) AssignIssue(ctx context.Context, key string, userID string) error {
	start := time.Now()
	err := a.inner.AssignIssue(ctx, key, userID)
	a.log("AssignIssue", key, time.Since(start), err)
	return err
}

func (a *AuditProvider) GetCurrentUser(ctx context.Context) (string, error) {
	start := time.Now()
	userID, err := a.inner.GetCurrentUser(ctx)
	a.log("GetCurrentUser", "", time.Since(start), err)
	return userID, err
}

func (a *AuditProvider) EditIssue(ctx context.Context, key string, opts EditOptions) (*Issue, error) {
	start := time.Now()
	issue, err := a.inner.EditIssue(ctx, key, opts)
	a.log("EditIssue", key, time.Since(start), err)
	return issue, err
}

func (a *AuditProvider) ListStatuses(ctx context.Context, key string) ([]Status, error) {
	start := time.Now()
	statuses, err := a.inner.ListStatuses(ctx, key)
	a.log("ListStatuses", key, time.Since(start), err)
	return statuses, err
}
