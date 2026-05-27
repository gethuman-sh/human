package dispatch

import (
	"context"

	"github.com/gethuman-sh/human/internal/claude"
)

// TmuxAgentFinder discovers Claude tmux panes that are available for dispatch.
type TmuxAgentFinder struct {
	InstanceFinder claude.InstanceFinder
	TmuxClient     claude.TmuxClient
	ProcessLister  claude.ProcessLister
}

// FindIdleAgents returns Claude tmux panes available for dispatch.
// Activity detection is not yet implemented — always returns nil.
func (f *TmuxAgentFinder) FindIdleAgents(_ context.Context) ([]Agent, error) {
	return nil, nil
}
