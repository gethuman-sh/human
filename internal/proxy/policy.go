package proxy

import (
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// Mode determines whether the domain list is an allowlist or blocklist.
type Mode string

const (
	ModeAllow Mode = "allowlist"
	ModeBlock Mode = "blocklist"
)

// Decider decides whether a given hostname is allowed to pass through the proxy.
type Decider interface {
	Allowed(hostname string) bool
}

// Policy decides whether a given hostname is allowed to pass through the proxy.
type Policy struct {
	mode     Mode
	matchers []domainMatcher
}

type domainMatcher struct {
	wildcard bool   // true for "*.example.com" patterns
	domain   string // lowercase domain (without leading "*.")
}

// NewPolicy creates a policy from a mode and domain list.
func NewPolicy(mode Mode, domains []string) (*Policy, error) {
	if mode != ModeAllow && mode != ModeBlock {
		return nil, errors.WithDetails("unsupported proxy mode %s", "mode", string(mode))
	}

	matchers := make([]domainMatcher, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}

		if strings.HasPrefix(d, "*.") {
			matchers = append(matchers, domainMatcher{wildcard: true, domain: d[2:]})
		} else {
			matchers = append(matchers, domainMatcher{wildcard: false, domain: d})
		}
	}

	return &Policy{mode: mode, matchers: matchers}, nil
}

// BlockAllPolicy returns a policy that blocks every hostname.
func BlockAllPolicy() *Policy {
	return &Policy{mode: ModeAllow, matchers: nil}
}

// Allowed reports whether hostname is permitted by this policy.
func (p *Policy) Allowed(hostname string) bool {
	if hostname == "" {
		return false
	}

	hostname = strings.ToLower(hostname)
	matched := p.matches(hostname)

	switch p.mode {
	case ModeAllow:
		return matched
	case ModeBlock:
		return !matched
	default:
		return false
	}
}

func (p *Policy) matches(hostname string) bool {
	for _, m := range p.matchers {
		if m.wildcard {
			// *.example.com matches sub.example.com but not example.com.
			if strings.HasSuffix(hostname, "."+m.domain) {
				return true
			}
		} else {
			if hostname == m.domain {
				return true
			}
		}
	}
	return false
}
