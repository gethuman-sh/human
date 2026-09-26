package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/forge"
)

// A red pull-request check and a broken branch used to be the same fact: every
// ChecksFailing verdict was handed to the deploy fixer, which spent one of the
// ticket's two repair rounds looking for a defect that was never in the branch
// (SC-5843 — a contributor-signature job that failed with an internal error).
// This file is the distinction the check model lacked: which failing checks a
// code change could turn green, and what the gate does about the ones it cannot.
//
// The tag below is the deployHeadLagDetail pattern (board_transition.go): a
// failure mode stamped on the error, read by ciFailureFixable and
// ciFailureHeadline so neither has to re-derive it.

// deployExternalChecksDetail tags a deploy error whose cause is a head red ONLY
// on checks no code change can turn green. There is nothing in the branch for a
// code fixer to change, so it must route to a plain deploy-failed naming the
// checks and asking for a re-run of them.
const deployExternalChecksDetail = "deployExternalChecks"

// deployExternalCheckGrace is how long a head red only on such checks is waited
// out before the gate stops. A bot check routinely re-runs (a re-trigger, a
// re-registration after a synchronize), so the first red is not its answer.
// Longer than deployNoChecksGrace because a re-run has to register as well as
// run; far shorter than deployTimeout because the deploy gate is a process-wide
// mutex and a card waiting on someone else's bot must not starve every other
// card's deploy. A package var so tests can run the gate without real time.
var deployExternalCheckGrace = 5 * time.Minute

// externalCheckApps and externalCheckNames are the checks that assert something
// about the CONTRIBUTOR or the pull request's metadata rather than about the
// branch's code. Both are matched EXACTLY (case-folded, trimmed) and never by
// substring: "cla-format-lint" is a lint of the branch and must stay fixable.
//
// Two vocabularies because one signal does not find them. The app slug catches
// the GitHub-App form of these bots whatever their display name; the name
// catches the form this very repository uses, where the CLA gate is a GitHub
// ACTIONS job (.github/workflows/cla.yml) and its app slug is "github-actions"
// — the same slug as build, test and lint, which is why an origin-only rule
// would not have caught the incident this exists for.
//
// Anything not named here is code-fixable. That direction is deliberate: an
// unrecognised bot costs one fixer round, while an over-broad rule would
// silence a real build break, which is the one outcome this must never buy.
var (
	externalCheckApps = map[string]bool{
		"cla-assistant":         true,
		"contributor-assistant": true,
		"dco":                   true,
	}
	externalCheckNames = map[string]bool{
		"cla":           true,
		"cla-assistant": true,
		"cla assistant": true,
		"license/cla":   true,
		"cla/google":    true,
		"dco":           true,
		"dco-check":     true,
	}
)

// checkIsExternal reports whether c asserts something other than the branch's
// code, by either signal. A check with neither a known name nor a known app is
// NOT external: unknown means "cannot attribute", and the safe reading of that
// is a code failure.
func checkIsExternal(c forge.CheckResult) bool {
	if externalCheckApps[strings.ToLower(strings.TrimSpace(c.App))] {
		return true
	}
	return externalCheckNames[strings.ToLower(strings.TrimSpace(c.Name))]
}

// failingChecksByKind reads the pull request's per-check verdicts and splits the
// FAILING ones into the names a code change could turn green and the names it
// could not, each comma-joined for the headline.
//
// Best-effort in exactly the sense checkNames is (the SC-1996 rule): a read
// failure or an empty list yields two empty strings, which the caller reads as
// "cannot attribute this red" and treats as code-fixable. It reads on its own
// short-lived context rather than the gate's, so a deploy window closing at this
// instant cannot degrade an external red into a fixable one — the classification
// must not depend on the clock that is expiring (SC-5395's class).
func (d BoardTransitionDeps) failingChecksByKind(number int) (fixable, external string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := d.Deployer.ReadPullRequest(ctx, d.WorkspaceDir, number)
	if err != nil || state == nil {
		return "", ""
	}
	var fix, ext []string
	for _, c := range state.Checks {
		if c.Conclusion != forge.ChecksFailing || c.Name == "" {
			continue
		}
		if checkIsExternal(c) {
			ext = append(ext, c.Name)
			continue
		}
		fix = append(fix, c.Name)
	}
	return strings.Join(fix, ", "), strings.Join(ext, ", ")
}

// redHeadVerdict says what a red head means for the CI gate.
//
//   - Any failing check a code change could turn green — or a red this cannot
//     attribute at all — is today's fixable "CI checks failed", carrying ONLY
//     the code-fixable names: a fixer told to fix a signature check looks for a
//     defect that is not in the branch.
//   - Every failing check external, within the grace: nil, the caller's signal
//     to keep polling. The check will very likely re-run.
//   - Every failing check external, past the grace: the non-fixable error,
//     tagged so ciFailureFixable declines it and ciFailureHeadline names the
//     checks and the real remedy.
//
// polls and wantHead are for the log only, so the gate's existing "CI checks
// failed" line keeps saying which poll and which head it judged.
func (d BoardTransitionDeps) redHeadVerdict(res PRResult, polls int, wantHead string, graceElapsed bool) error {
	fixable, external := d.failingChecksByKind(res.Number)
	if fixable != "" || external == "" {
		d.Logger.Info().Int("pr", res.Number).Int("polls", polls).Str("head", wantHead).
			Str("failing", fixable).Msg("deploy: CI checks failed")
		return errors.WithDetails("CI checks failed", "pr", res.URL,
			deployFailingChecksDetail, fixable)
	}
	if !graceElapsed {
		d.Logger.Info().Int("pr", res.Number).Str("external", external).
			Dur("grace", deployExternalCheckGrace).
			Msg("deploy: red only on checks no code change can turn green; waiting for them to re-run")
		return nil
	}
	d.Logger.Info().Int("pr", res.Number).Int("polls", polls).Str("head", wantHead).
		Str("external", external).
		Msg("deploy: red only on checks no code change can turn green past the grace; stopping without a fixer")
	return errors.WithDetails("CI is red only on checks no code change can turn green", "pr", res.URL,
		deployExternalChecksDetail, true, deployFailingChecksDetail, external)
}

// externalChecksRed reports whether err is a head red only on checks no code
// change can turn green.
func externalChecksRed(err error) bool {
	external, _ := errors.AllDetails(err)[deployExternalChecksDetail].(bool)
	return external
}

// externalChecksHeadline states the remedy for such a head: the checks by name,
// and a re-run rather than a code fix. It must never say "fix the failing
// checks" — advising a code fix for a check no code fixes is what sent a person
// looking for a defect that was not there.
func externalChecksHeadline(err error) string {
	return "the pull request is red only on checks no code change can turn green" +
		checkSuffix(err, deployFailingChecksDetail, "failing") +
		" — re-run those checks on the PR (or fix the bot that reports them), then re-run Deploy"
}
