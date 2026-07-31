package tracker_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/tracker/azuredevops"
	"github.com/gethuman-sh/human/internal/tracker/clickup"
	"github.com/gethuman-sh/human/internal/tracker/github"
	"github.com/gethuman-sh/human/internal/tracker/gitlab"
	"github.com/gethuman-sh/human/internal/tracker/jira"
	"github.com/gethuman-sh/human/internal/tracker/linear"
)

// linkers is every backend that records relationships but implements only the
// symmetric one.
func linkers() map[string]tracker.Linker {
	return map[string]tracker.Linker{
		"linear":      linear.New("http://unused", "tok"),
		"jira":        jira.New("http://unused", "user", "tok"),
		"gitlab":      gitlab.New("http://unused", "tok"),
		"github":      github.New("http://unused", "tok"),
		"clickup":     clickup.New("http://unused", "tok", ""),
		"azuredevops": azuredevops.New("http://unused", "org", "tok"),
	}
}

// The contract that keeps a dependency meaningful: a backend that cannot record
// direction must SAY so. Silently storing "blocks" as "related" would produce a
// link that gates nothing while looking exactly like one that does — and no
// caller could tell the difference, so the gate would quietly stop working.
//
// No network is configured on these clients: refusing before any request is
// itself the assertion.
func TestLinkIssues_BackendsWithoutDirectionRefuseBlocks(t *testing.T) {
	for name, l := range linkers() {
		t.Run(name, func(t *testing.T) {
			err := l.LinkIssues(context.Background(), "A-1", "A-2", tracker.LinkBlocks)

			assert.Error(t, err, "a backend that cannot express a dependency must refuse it")
			assert.Contains(t, err.Error(), "directional",
				"the refusal must name the limitation, not fail obscurely")
		})
	}
}

// Removing a link is the release valve for work held behind a blocker. A
// backend that cannot do it must refuse rather than report success: a silent
// no-op would leave the caller believing a dependency was gone when it was not,
// and the work would stay stuck with no visible reason.
func TestUnlinkIssues_UnsupportedBackendsRefuseRatherThanNoOp(t *testing.T) {
	for name, l := range linkers() {
		t.Run(name, func(t *testing.T) {
			err := l.UnlinkIssues(context.Background(), "A-1", "A-2")

			assert.Error(t, err, "a no-op that reports success would hide a dependency that still exists")
			assert.Contains(t, err.Error(), "not implemented")
		})
	}
}
