package cmddaemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	goerrors "errors"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/recall"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

// feedbackModel is the model the launch briefing is distilled by. A constant,
// not config: the briefing is a few lines of advice over a few hundred rows,
// which is small-model work, and a per-project choice would be a knob nobody
// has asked to turn (SC-5959).
const feedbackModel = "haiku"

// launchAdvice is the daemon-wide half of the launch briefing: one handle on
// the findings record, one host runner per project directory, and one cache
// per project so the per-request FeedbackDeps the board constructors build
// share what is expensive and rebuild only what is per request (the
// role-resolved tracker clients).
//
// A nil *launchAdvice disables the briefing everywhere it is threaded, which
// is the state of a daemon whose findings record could not be opened.
type launchAdvice struct {
	record     *recall.SQLiteStore
	refusals   *claudeAuthRefusals
	storeStamp func(context.Context) time.Time
	logger     zerolog.Logger

	mu     sync.Mutex
	caches map[string]*daemon.FeedbackCache
}

func newLaunchAdvice(record *recall.SQLiteStore, refusals *claudeAuthRefusals, logger zerolog.Logger) *launchAdvice {
	if record == nil {
		return nil
	}
	return &launchAdvice{record: record, refusals: refusals, storeStamp: hostClaudeStoreStamp, logger: logger, caches: map[string]*daemon.FeedbackCache{}}
}

// feedbackFor builds the per-request FeedbackDeps for one project entry and
// returns its Advice, or nil when the briefing is disabled.
func (a *launchAdvice) feedbackFor(entry daemon.ProjectEntry, project string, ticket tracker.Getter, comments tracker.Commenter) func(context.Context, string, daemon.BoardStage, string) string {
	deps := a.depsFor(entry, project, ticket, comments)
	if deps == nil {
		return nil
	}
	return deps.Advice
}

// depsFor is the one constructor behind both the launch path (feedbackFor)
// and the feedback route (explainerFor), so `human feedback` answers from
// the same record, runner and per-project cache a launch reads.
func (a *launchAdvice) depsFor(entry daemon.ProjectEntry, project string, ticket tracker.Getter, comments tracker.Commenter) *daemon.FeedbackDeps {
	if a == nil {
		return nil
	}
	return &daemon.FeedbackDeps{
		Record:    a.record,
		Runner:    hostFeedbackRunner{dir: entry.Dir, refusals: a.refusals, storeStamp: a.storeStamp},
		Project:   project,
		Workspace: entry.Dir,
		Ticket:    ticket,
		Comments:  comments,
		Cache:     a.cacheFor(entry.Dir),
		Timeout:   daemon.FeedbackTimeout,
		Logger:    a.logger,
	}
}

// explainerFor answers the feedback route (SC-6016): the project is resolved
// from the ticket key the way every board launch resolves it, the tracker
// clients by PM role, and the briefing is built by the same deps a launch
// builds it with. nil when the briefing is disabled, which the route reports
// as such.
func (a *launchAdvice) explainerFor(reg *daemon.ProjectRegistry, resolver *vault.Resolver) daemon.FeedbackExplainer {
	if a == nil {
		return nil
	}
	return func(req daemon.FeedbackRequest) (daemon.FeedbackReport, error) {
		entry, err := reg.EntryForKey(req.Key)
		if err != nil {
			return daemon.FeedbackReport{}, err
		}
		lookup := entry.EnvLookup()
		// Scope reads are best-effort on the launch path too: a tracker
		// without fetch or comment support leaves the scope to the record.
		getter, err := resolvePMGetter(entry.Dir, lookup, resolver)
		if err != nil {
			a.logger.Debug().Err(err).Str("pm", req.Key).Msg("feedback: no PM getter; scope falls back to the record")
			getter = nil
		}
		commenter, err := resolvePMCommenter(entry.Dir, lookup, resolver)
		if err != nil {
			a.logger.Debug().Err(err).Str("pm", req.Key).Msg("feedback: no PM commenter; scope falls back to the ticket text")
			commenter = nil
		}
		deps := a.depsFor(entry, boardStateProject(reg, req.Key), getter, commenter)
		return deps.Explain(context.Background(), req.Key, req.Stage, req.Branch)
	}
}

func (a *launchAdvice) cacheFor(dir string) *daemon.FeedbackCache {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.caches[dir]
	if !ok {
		c = &daemon.FeedbackCache{}
		a.caches[dir] = c
	}
	return c
}

// hostFeedbackRunner is one headless `claude -p` turn on the daemon host with
// no tools at all: the briefing is written from the rows in the prompt and
// nothing else, so the model has nothing to inspect and nothing to change. It
// feeds the same refusal record as the description editor's turns, since a
// rejected host login is visible only to a turn that tries it.
type hostFeedbackRunner struct {
	dir        string
	refusals   *claudeAuthRefusals
	storeStamp func(context.Context) time.Time
}

// Run implements daemon.FeedbackRunner.
func (r hostFeedbackRunner) Run(ctx context.Context, prompt string) (string, error) {
	args := []string{"-p", prompt, "--output-format", "json", "--tools", "", "--model", feedbackModel}
	turn, err := hostClaudeTurn(ctx, r.dir, args, r.refusals, r.storeStamp)
	if err != nil {
		return "", err
	}
	return turn.Result, nil
}

// hostClaudeTurn runs one `claude -p` invocation in dir and returns its parsed
// result. Shared by the description editor's turns and the launch briefing so
// the two read the CLI's failure shapes the same way.
//
// Live-verified (CLI 2.1.193): on turn failure claude exits non-zero, writes
// the result JSON with is_error:true and the cause in `result` to STDOUT, and
// leaves stderr empty. So the JSON parse must run on both the success and the
// ExitError path; stderr is only meaningful for true exec failures (binary
// missing, process killed).
func hostClaudeTurn(ctx context.Context, dir string, args []string, refusals *claudeAuthRefusals, storeStamp func(context.Context) time.Time) (claudeTurnOutput, error) {
	cmd := exec.CommandContext(ctx, "claude", args...) // #nosec G204 -- fixed binary, prompt is a discrete argv element
	cmd.Dir = dir
	out, err := cmd.Output()
	var parsed claudeTurnOutput
	parseErr := json.Unmarshal(out, &parsed)
	if parseErr == nil {
		noteHostClaudeAuth(ctx, parsed, refusals, storeStamp)
	}
	if parseErr == nil && parsed.IsError {
		return claudeTurnOutput{}, errors.WithDetails(turnFailureMessage(parsed), "result", parsed.Result)
	}
	if err != nil {
		if ctx.Err() != nil {
			return claudeTurnOutput{}, errors.WrapWithDetails(ctx.Err(), "agent turn timed out")
		}
		detail := ""
		if ee, ok := goerrors.AsType[*exec.ExitError](err); ok {
			detail = strings.TrimSpace(string(ee.Stderr))
		}
		return claudeTurnOutput{}, errors.WrapWithDetails(err, "running agent turn"+withCause(detail), "stderr", detail)
	}
	if parseErr != nil {
		return claudeTurnOutput{}, errors.WrapWithDetails(parseErr, "parsing agent turn output")
	}
	return parsed, nil
}

// noteHostClaudeAuth feeds the host-claude-auth doctor check: a 401 records a
// refusal against the host store, any other completed turn clears it.
func noteHostClaudeAuth(ctx context.Context, turn claudeTurnOutput, refusals *claudeAuthRefusals, storeStamp func(context.Context) time.Time) {
	if refusals == nil {
		return
	}
	switch {
	case turn.IsError && turn.APIErrorStatus == http.StatusUnauthorized:
		stamp := time.Time{}
		if storeStamp != nil {
			stamp = storeStamp(ctx)
		}
		refusals.record(hostClaudeStore, stamp)
	case !turn.IsError:
		refusals.clear(hostClaudeStore)
	}
}
