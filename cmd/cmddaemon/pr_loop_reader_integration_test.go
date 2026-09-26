package cmddaemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
)

// fakeLoopCommenter is a minimal tracker.Commenter for the reader->arm
// integration tests below: a fixed comment thread, plus what got posted back.
type fakeLoopCommenter struct {
	comments []tracker.Comment
	added    []string
}

func (f *fakeLoopCommenter) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return f.comments, nil
}

func (f *fakeLoopCommenter) AddComment(_ context.Context, _, body string) (*tracker.Comment, error) {
	f.added = append(f.added, body)
	return &tracker.Comment{Body: body}, nil
}

func postedBody(bodies []string, header string) (string, bool) {
	for _, b := range bodies {
		if strings.HasPrefix(b, header) {
			return b, true
		}
	}
	return "", false
}

// SC-5554 verify-gap 1: readPRFixReport must zero every field it exposes on an
// undecodable record, exactly like readDeployFixReport does — proven end to
// end through AdvancePRLoop -> escalatePRLoop, the actual site that consumed
// the half-decoded FixOptions. EvaluatePRLoop's own stepUnreadable-first check
// (pr_review_loop.go) never protected this: escalatePRLoop re-reads
// outcome.FixExit/FixOptions independently, before ever calling
// prEscalationReason, to choose between posting a [human:options] block and
// the failed marker.
//
// {"options":[...2 options...],"exit":123,"summary":"hi"}: "options" precedes
// "exit" in the JSON, so json.Unmarshal decodes it before rejecting the
// numeric "exit". A reader that did not zero every field on read.unreadable
// would hand escalatePRLoop two real-looking directions with an empty
// FixExit, which reads as "the fixer stopped short of done, naming two or
// more directions" and posts a genuine decision block nobody actually
// recorded.
func TestAdvancePRLoop_readerToEscalate_undecodableFixRecordNeverPostsFabricatedOptions(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.pr-fix",
		`{"options":[{"id":"1","label":"Do X"},{"id":"2","label":"Do Y"}],"exit":123,"summary":"hi"}`)

	exit, options, summary, head, read := readPRFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	require.True(t, read.unreadable, "the malformed exit makes this an undecodable record")

	c := &fakeLoopCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", Created: time.Unix(1, 0)},
		{Body: "[human:pr-review-started]\npr: https://example/pr/7\nnumber: 7\nbranch: feat/x", Created: time.Unix(2, 0)},
		{Body: "[human:pr-fix-started]", Created: time.Unix(3, 0)},
	}}
	deps := daemon.BoardTransitionDeps{Commenter: c}

	err := deps.AdvancePRLoop(context.Background(), "SC-1", daemon.PRLoopOutcome{
		FixRecorded:   read.recorded,
		FixUnreadable: read.unreadable,
		FixExit:       exit,
		FixOptions:    options,
		FixSummary:    summary,
		FixHead:       head,
	})
	require.NoError(t, err)

	_, asked := postedBody(c.added, daemon.OptionsHeader)
	assert.False(t, asked, "an undecodable record must never post a decision block built from a half-decoded options list")
	failed, ok := postedBody(c.added, daemon.PRReviewFailedHeader)
	require.True(t, ok, "an undecodable fixer record must red the card")
	assert.Contains(t, failed, "could not read", "the card must say the record was unreadable, not invent a question")
}

// SC-5554 verify-gap 2: TestAdvanceDeployFix_UndecodableRecordNeverReachesTheDoneArm
// used to construct DeployFixReport{} directly — never calling
// readDeployFixReport at all — which made it an exact duplicate of
// TestAdvanceDeployFix_UnrecordedExit_Reds and unable to fail against
// e3d6f197's parent for the actual product reason. This exercises the real
// path: the reader's own undecodable-record contract
// (TestReadDeployFixReport_undecodableRecordExposesNoHalfDecodedExit) feeding
// straight into AdvanceDeployFix, proving the half-decoded "done" the raw
// record leads with never reaches the publish arm.
func TestAdvanceDeployFix_readerToArm_undecodableRecordNeverReachesTheDoneArm(t *testing.T) {
	isolateState(t)
	writeRawReport(t, "SC-1", "stage.deploy-fix", `{"exit":"done","blocker":"oops"}`)

	report := readDeployFixReport(context.Background(), "", "SC-1", time.Time{}, zerolog.Nop())
	require.Empty(t, report.Exit, "the reader must not hand a half-decoded exit through")

	c := &fakeLoopCommenter{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: feat/x", Created: time.Unix(1, 0)},
		{Body: "[human:review-complete]", Created: time.Unix(2, 0)},
	}}
	deps := daemon.BoardTransitionDeps{Commenter: c}

	err := deps.AdvanceDeployFix(context.Background(), "SC-1", report)
	require.NoError(t, err)

	_, ok := postedBody(c.added, daemon.DeployFailedHeader)
	require.True(t, ok, "the undecodable record must red the deploy rather than silently publish")
}
