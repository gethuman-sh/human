//go:build wailsapp

package main

import (
	"sort"

	"github.com/gethuman-sh/human/internal/daemon"
)

// PermissionRequest is the frontend view of one pending destructive-operation
// permission request from the daemon's confirm queue (see HUM-160). The agent
// name is absent by design — the queue entry only carries the operation, the
// ticket it targets, and the requester PID.
type PermissionRequest struct {
	ID        string `json:"id"`
	Operation string `json:"operation"` // "DeleteIssue", "EditIssue", ...
	Tracker   string `json:"tracker"`   // tracker kind, e.g. "jira"
	Key       string `json:"key"`       // issue key, e.g. "KAN-1"
	Prompt    string `json:"prompt"`    // daemon fallback prompt
	CreatedAt string `json:"createdAt"` // RFC3339
}

// PendingPermissions returns the queue oldest-first so the command strip can
// drain it in FIFO order. An unreachable daemon yields an empty list rather
// than an error: the strip simply stays hidden, and daemon health is already
// surfaced separately via DaemonStatus.
func (a *App) PendingPermissions() ([]PermissionRequest, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return []PermissionRequest{}, nil
	}
	confirms, err := daemon.GetPendingConfirms(info.Addr, info.Token)
	if err != nil {
		return []PermissionRequest{}, nil
	}

	out := make([]PermissionRequest, 0, len(confirms))
	for _, c := range confirms {
		out = append(out, PermissionRequest{
			ID:        c.ID,
			Operation: c.Operation,
			Tracker:   c.Tracker,
			Key:       c.Key,
			Prompt:    c.Prompt,
			CreatedAt: c.CreatedAt,
		})
	}
	// RFC3339 timestamps sort correctly as strings.
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// DecidePermission records the user's decision for one request. Approval only
// grants — the requesting agent redeems the grant and executes on its own.
// Unlike the polling read above, a failure here must surface: silently
// dropping a decision the user just made would be worse than an error banner.
func (a *App) DecidePermission(id string, approved bool) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemon.SendConfirmDecision(info.Addr, info.Token, id, approved)
}
