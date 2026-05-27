package dispatch

import (
	"context"
	"fmt"

	"github.com/gethuman-sh/human/internal/claude"
)

// TmuxSender sends prompts to tmux panes via send-keys.
type TmuxSender struct {
	Runner claude.CommandRunner
}

// SendPrompt types the prompt into the target tmux pane and presses Enter.
func (s *TmuxSender) SendPrompt(ctx context.Context, agent Agent, prompt string) error {
	target := fmt.Sprintf("%s:%d.%d", agent.SessionName, agent.WindowIndex, agent.PaneIndex)

	// Send the text literally (no key interpretation).
	if _, err := s.Runner.Run(ctx, "tmux", "send-keys", "-t", target, "-l", prompt); err != nil {
		return err
	}
	// Press Enter to submit.
	if _, err := s.Runner.Run(ctx, "tmux", "send-keys", "-t", target, "Enter"); err != nil {
		return err
	}
	return nil
}
