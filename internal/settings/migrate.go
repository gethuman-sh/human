package settings

import (
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
)

// errNoConfig is the one place this command refuses to act: there is nothing to
// migrate if there is nothing to read.
func errNoConfig(dir string) error {
	return errors.WithDetails("no .humanconfig file found", "dir", dir)
}

// ForgeMigration is what MigrateForges did, or would do.
type ForgeMigration struct {
	// File is the config that was (or would be) rewritten.
	File string
	// Moved names the githubs: entries that became forges: entries.
	Moved []string
	// Unified names the kinds folded from their per-vendor sections into the
	// single trackers: list ([SC-3874]).
	Unified []string
}

// Empty reports that there was nothing to migrate.
func (m ForgeMigration) Empty() bool { return len(m.Moved) == 0 && len(m.Unified) == 0 }

// MigrateForges moves the code-host credentials into the forges: section a
// config needs now that a githubs: entry is an issue tracker and nothing more.
//
// The rules it applies are the document's, not its own ([SC-3889]). This used
// to be several hundred lines of yaml node surgery with the only statement of a
// cross-section invariant in the codebase buried inside it — which meant no
// other caller could consult that rule, and the check that enforced a sibling
// rule lived in a provider's loader with its own copy of the wording. What is
// left here is the decision of WHICH entries to move; the document knows how.
//
// Two shapes move, and both were credentials rather than a backlog:
//
//   - An entry declaring the retired role: forge, which was never a tracker.
//   - An entry declaring no role at all — the same case wearing no label. Three
//     bugs say what those hold ([SC-1671], [SC-2132], [SC-3868]), every one of
//     them an unattended pass asking such an entry for tickets and getting a
//     rate-limited search across every issue the token could see.
//
// A declared tracker role is left alone: it says the entry holds issues.
//
// dryRun reports what would happen without touching the file. Migration is
// idempotent, and a rerun finishes a half-done one: an entry whose credentials
// already reached forges: is a leftover, and the document removes it rather
// than seeing a name it recognises and stopping ([SC-3887]).
func MigrateForges(dir string, dryRun bool) (ForgeMigration, error) {
	return migrate(dir, dryRun, false)
}

// MigrateAll is MigrateForges plus the shape change: the per-vendor sections
// fold into one trackers: list, with each entry stamped with the kind its
// section used to imply.
//
// Separate from MigrateForges because the two are different kinds of change. The
// forge move repairs a config that has stopped opening pull requests; the
// grouping is a preference about how a working file is written, and nobody
// should have their file restructured while asking for a repair ([SC-3874]).
func MigrateAll(dir string, dryRun bool) (ForgeMigration, error) {
	return migrate(dir, dryRun, true)
}

func migrate(dir string, dryRun, unify bool) (ForgeMigration, error) {
	if _, exists := config.LocateFile(dir); !exists {
		return ForgeMigration{}, errNoConfig(dir)
	}
	doc, err := config.Load(dir)
	if err != nil {
		return ForgeMigration{}, err
	}
	result := ForgeMigration{File: doc.Path()}

	for _, t := range doc.Trackers() {
		if !movesToForge(t) {
			continue
		}
		moved, err := doc.MoveTrackerToForge(t.Kind, t.Name)
		if err != nil {
			return ForgeMigration{}, err
		}
		if moved {
			result.Moved = append(result.Moved, t.Name)
		}
	}

	// Grouping runs after the forge move, so credentials that are leaving the
	// tracker list are not folded into it on the way out.
	if unify {
		result.Unified = doc.UnifyTrackers()
	}

	if result.Empty() || dryRun {
		return result, nil
	}
	if err := doc.Write(); err != nil {
		return ForgeMigration{}, err
	}
	return result, nil
}

// movesToForge decides whether one tracker entry is really code-host
// credentials. Confined to GitHub: it is the only backend that was ever both,
// so it is the only one whose undeclared entries are ambiguous.
func movesToForge(t config.Tracker) bool {
	if t.Kind != "github" {
		return false
	}
	// An entry with no credentials cannot be carried anywhere, and a migration
	// must never delete what it cannot replace.
	if t.Token == "" {
		return false
	}
	return t.Role == "" || t.Role == config.RetiredForgeRole
}
