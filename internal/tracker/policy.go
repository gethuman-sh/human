package tracker

import (
	"context"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// Action represents the result of a policy evaluation.
type Action int

const (
	// ActionAllow permits the operation (default).
	ActionAllow Action = iota
	// ActionBlock prevents the operation entirely.
	ActionBlock
	// ActionConfirm logs a warning before proceeding.
	ActionConfirm
)

// PolicyConfig holds the policies section from .humanconfig.
type PolicyConfig struct {
	Block   []string `mapstructure:"block"`
	Confirm []string `mapstructure:"confirm"`
}

// policyRule represents a parsed policy rule with optional argument.
type policyRule struct {
	operation string // lowercased operation name
	argument  string // lowercased argument (empty for bare rules)
}

// Policy evaluates operations against configured block/confirm rules.
type Policy struct {
	blockRules   []policyRule
	confirmRules []policyRule
}

// NewPolicy creates a Policy from the given config, parsing rule strings
// into structured rules for fast lookup.
func NewPolicy(cfg PolicyConfig) *Policy {
	return &Policy{
		blockRules:   parseRules(cfg.Block),
		confirmRules: parseRules(cfg.Confirm),
	}
}

// parseRules converts string rules like "delete" or "transition:Done"
// into structured policyRule values.
func parseRules(rules []string) []policyRule {
	parsed := make([]policyRule, 0, len(rules))
	for _, r := range rules {
		if r == "" {
			continue
		}
		idx := strings.IndexByte(r, ':')
		var pr policyRule
		if idx >= 0 {
			pr.operation = strings.ToLower(r[:idx])
			pr.argument = strings.ToLower(r[idx+1:])
		} else {
			pr.operation = strings.ToLower(r)
		}
		parsed = append(parsed, pr)
	}
	return parsed
}

// Evaluate checks an operation (and optional argument) against the policy
// rules. Block takes precedence over confirm when both match.
func (p *Policy) Evaluate(operation, arg string) Action {
	op := strings.ToLower(operation)
	a := strings.ToLower(arg)

	if matchesRules(p.blockRules, op, a) {
		return ActionBlock
	}
	if matchesRules(p.confirmRules, op, a) {
		return ActionConfirm
	}
	return ActionAllow
}

// matchesRules checks whether any rule in the list matches the given
// operation and argument. A bare rule (no argument) matches all invocations
// of that operation. A parameterized rule matches only when the argument
// also matches.
func matchesRules(rules []policyRule, op, arg string) bool {
	for _, r := range rules {
		if r.operation != op {
			continue
		}
		// Bare rule matches all invocations of this operation.
		if r.argument == "" {
			return true
		}
		// Parameterized rule matches only when the argument matches.
		if r.argument == arg {
			return true
		}
	}
	return false
}

// PolicyProvider wraps a Provider and evaluates policy rules before
// delegating to the inner provider. Read-only methods always pass through.
type PolicyProvider struct {
	inner        Provider
	instanceName string
	policy       *Policy
	warnFn       func(string)
}

// NewPolicyProvider creates a PolicyProvider that evaluates policy rules
// before delegating write operations to the inner provider.
func NewPolicyProvider(inner Provider, instanceName string, policy *Policy, warnFn func(string)) *PolicyProvider {
	return &PolicyProvider{
		inner:        inner,
		instanceName: instanceName,
		policy:       policy,
		warnFn:       warnFn,
	}
}

// checkPolicy evaluates the policy for the given operation and argument.
// Returns an error for blocked operations, calls warnFn for confirm, or
// returns nil for allow.
func (pp *PolicyProvider) checkPolicy(operation, arg string) error {
	action := pp.policy.Evaluate(operation, arg)
	switch action {
	case ActionBlock:
		return errors.WithDetails("operation blocked by policy: %s on %s",
			"operation", operation,
			"instance", pp.instanceName)
	case ActionConfirm:
		if pp.warnFn != nil {
			msg := operation + " on " + pp.instanceName
			if arg != "" {
				msg = operation + ":" + arg + " on " + pp.instanceName
			}
			pp.warnFn(msg)
		}
	}
	return nil
}

// Read-only methods always pass through without policy check.

func (pp *PolicyProvider) ListIssues(ctx context.Context, opts ListOptions) ([]Issue, error) {
	return pp.inner.ListIssues(ctx, opts)
}

func (pp *PolicyProvider) GetIssue(ctx context.Context, key string) (*Issue, error) {
	return pp.inner.GetIssue(ctx, key)
}

func (pp *PolicyProvider) ListComments(ctx context.Context, issueKey string) ([]Comment, error) {
	return pp.inner.ListComments(ctx, issueKey)
}

func (pp *PolicyProvider) GetCurrentUser(ctx context.Context) (string, error) {
	return pp.inner.GetCurrentUser(ctx)
}

func (pp *PolicyProvider) ListStatuses(ctx context.Context, key string) ([]Status, error) {
	return pp.inner.ListStatuses(ctx, key)
}

// Write methods evaluate the policy before delegating.

func (pp *PolicyProvider) CreateIssue(ctx context.Context, issue *Issue) (*Issue, error) {
	if err := pp.checkPolicy("create", ""); err != nil {
		return nil, err
	}
	return pp.inner.CreateIssue(ctx, issue)
}

func (pp *PolicyProvider) DeleteIssue(ctx context.Context, key string) error {
	if err := pp.checkPolicy("delete", ""); err != nil {
		return err
	}
	return pp.inner.DeleteIssue(ctx, key)
}

func (pp *PolicyProvider) AddComment(ctx context.Context, issueKey string, body string) (*Comment, error) {
	if err := pp.checkPolicy("comment", ""); err != nil {
		return nil, err
	}
	return pp.inner.AddComment(ctx, issueKey, body)
}

func (pp *PolicyProvider) LinkIssues(ctx context.Context, key string, otherKey string) error {
	if err := pp.checkPolicy("link", ""); err != nil {
		return err
	}
	return pp.inner.LinkIssues(ctx, key, otherKey)
}

func (pp *PolicyProvider) TransitionIssue(ctx context.Context, key string, targetStatus string) error {
	if err := pp.checkPolicy("transition", targetStatus); err != nil {
		return err
	}
	return pp.inner.TransitionIssue(ctx, key, targetStatus)
}

func (pp *PolicyProvider) AssignIssue(ctx context.Context, key string, userID string) error {
	if err := pp.checkPolicy("assign", ""); err != nil {
		return err
	}
	return pp.inner.AssignIssue(ctx, key, userID)
}

func (pp *PolicyProvider) EditIssue(ctx context.Context, key string, opts EditOptions) (*Issue, error) {
	if err := pp.checkPolicy("edit", ""); err != nil {
		return nil, err
	}
	return pp.inner.EditIssue(ctx, key, opts)
}
