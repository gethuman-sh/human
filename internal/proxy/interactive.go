package proxy

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"
)

// PromptFunc asks the user whether a hostname should be allowed.
// It returns true to allow, false to deny.
type PromptFunc func(hostname string) (bool, error)

// InteractiveDecider wraps a base Decider and prompts the user for hostnames
// that the base does not allow. Decisions are cached for the session.
type InteractiveDecider struct {
	base    Decider
	mu      sync.Mutex
	cache   map[string]bool
	pending map[string]*sync.Cond
	prompt  PromptFunc
}

// NewInteractiveDecider creates an InteractiveDecider that falls through to
// prompt for hostnames not allowed by base.
func NewInteractiveDecider(base Decider, prompt PromptFunc) *InteractiveDecider {
	return &InteractiveDecider{
		base:    base,
		cache:   make(map[string]bool),
		pending: make(map[string]*sync.Cond),
		prompt:  prompt,
	}
}

// Allowed returns true if the hostname is permitted. Hostnames allowed by the
// base decider pass through immediately. Unknown hostnames trigger a prompt;
// the result is cached for subsequent calls.
func (d *InteractiveDecider) Allowed(hostname string) bool {
	if d.base.Allowed(hostname) {
		return true
	}

	host := strings.ToLower(hostname)

	d.mu.Lock()

	if result, ok := d.cache[host]; ok {
		d.mu.Unlock()
		return result
	}

	if cond, ok := d.pending[host]; ok {
		// Another goroutine is prompting. Wait in a loop per the sync.Cond
		// contract: a wake-up does not by itself prove the decision is ready,
		// so re-check the cache and only return once it is populated. The
		// stillPending guard avoids blocking forever if the prompter ever
		// disappears without recording a result.
		for {
			cond.Wait()
			if result, cached := d.cache[host]; cached {
				d.mu.Unlock()
				return result
			}
			if _, stillPending := d.pending[host]; !stillPending {
				d.mu.Unlock()
				return false
			}
		}
	}

	cond := sync.NewCond(&d.mu)
	d.pending[host] = cond
	d.mu.Unlock()

	allowed, err := d.prompt(host)
	if err != nil {
		allowed = false
	}

	d.mu.Lock()
	d.cache[host] = allowed
	delete(d.pending, host)
	cond.Broadcast()
	d.mu.Unlock()

	return allowed
}

// NewTerminalPrompt returns a PromptFunc that asks the user via the terminal.
// It serialises I/O with its own mutex so that concurrent prompts don't
// interleave on the terminal.
func NewTerminalPrompt(in io.Reader, out io.Writer) PromptFunc {
	var mu sync.Mutex
	scanner := bufio.NewScanner(in)

	return func(hostname string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()

		_, err := fmt.Fprintf(out, "Allow connections to %s? [y/N] ", hostname)
		if err != nil {
			return false, err
		}

		if !scanner.Scan() {
			if scanErr := scanner.Err(); scanErr != nil {
				return false, scanErr
			}
			return false, io.EOF
		}

		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		return answer == "y" || answer == "yes", nil
	}
}
