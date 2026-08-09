package settings

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/gethuman-sh/human/errors"
)

// ForgeMigration is what MigrateForges did, or would do.
type ForgeMigration struct {
	// File is the config that was (or would be) rewritten.
	File string
	// Added names the forges: entries written.
	Added []string
	// Moved names githubs: entries that declared role: forge and were removed
	// from the tracker section, having become forge entries.
	Moved []string
}

// Empty reports that there was nothing to migrate.
func (m ForgeMigration) Empty() bool { return len(m.Added) == 0 && len(m.Moved) == 0 }

// MigrateForges writes the forges: section a config needs now that a githubs:
// entry is an issue tracker and nothing more.
//
// Two shapes predate the separation, and both MOVE — the githubs: entry becomes
// a forges: entry and is removed:
//
//   - role: forge was never a tracker at all. Leaving it behind would leave a
//     tracker declaring a role that no longer exists, and loading it now fails,
//     which would make a completed migration look like a broken one.
//   - No role at all is the same case wearing no label. It is what a GitHub
//     entry looked like when one entry meant both things, and what it held was
//     credentials — the evidence is three bugs deep ([SC-1671], [SC-2132],
//     [SC-3868]), every one of them an unattended pass asking such an entry for
//     tickets and getting a rate-limited search across every issue the token
//     could see. Copying it instead of moving it would leave that entry standing
//     as a tracker and hand the board the same 403 banner the separation was
//     meant to end.
//
// So a migration reports what it moved, and says how to declare a GitHub issue
// tracker if that is genuinely what the entry was: role: pm, which the board has
// required from a GitHub tracker all along. Guessing the other way is the
// costlier mistake — a config that quietly resumes burning a search quota looks
// like a tool bug, where a tracker that has to be re-declared is a line of YAML
// and a message that names it.
//
// An entry whose token is a vault reference migrates as the reference, never as
// a resolved secret: this writes a config file, and a resolved token in one is
// a credential leak with a long half-life.
//
// dryRun reports what would happen without touching the file. Migration is
// idempotent: a name already present in forges: is left alone, so running it
// twice is not an error and never duplicates an entry.
func MigrateForges(dir string, dryRun bool) (ForgeMigration, error) {
	file, exists := LocateConfigFile(dir)
	if !exists {
		return ForgeMigration{}, errors.WithDetails("no .humanconfig file found", "dir", dir)
	}
	root, err := loadRoot(file, exists)
	if err != nil {
		return ForgeMigration{}, err
	}
	result := ForgeMigration{File: file}

	mapping := root.Content[0]
	githubs := mapValue(mapping, "githubs")
	if githubs == nil || githubs.Kind != yaml.SequenceNode {
		return result, nil
	}

	existing := existingForgeNames(mapping)
	var keep []*yaml.Node
	var newForges []*yaml.Node
	for _, entry := range githubs.Content {
		plan := planEntry(entry, existing)
		if plan.keep {
			keep = append(keep, entry)
		}
		if plan.moved {
			result.Moved = append(result.Moved, plan.name)
		}
		if plan.forge != nil {
			existing[plan.name] = true
			result.Added = append(result.Added, plan.name)
			newForges = append(newForges, plan.forge)
		}
	}

	if result.Empty() {
		return result, nil
	}
	if dryRun {
		return result, nil
	}

	if len(result.Moved) > 0 {
		githubs.Content = keep
		// A githubs: section emptied by the move is removed rather than left as
		// an empty list: an empty section reads as "configured, but broken".
		if len(keep) == 0 {
			removeKey(mapping, "githubs")
		}
	}
	forges := ensureMapValue(mapping, "forges", yaml.SequenceNode)
	forges.Content = append(forges.Content, newForges...)

	if err := writeAtomically(file, exists, root); err != nil {
		return ForgeMigration{}, err
	}
	return result, nil
}

// entryPlan is what one githubs: entry contributes to the migration: whether it
// stays a tracker, whether it counts as moved, and the forge entry it yields.
type entryPlan struct {
	name  string
	keep  bool
	moved bool
	forge *yaml.Node
}

// planEntry decides one githubs: entry's fate, so the rewrite above stays a
// walk rather than a nest of conditions.
func planEntry(entry *yaml.Node, existing map[string]bool) entryPlan {
	if entry.Kind != yaml.MappingNode {
		return entryPlan{keep: true}
	}
	name := scalarAt(entry, "name")
	role := scalarAt(entry, "role")
	token := scalarAt(entry, "token")

	// A declared tracker role (pm, engineering, tracker) says the entry is an
	// issue tracker, so it never opened pull requests and stays exactly as it is.
	if role != "" && role != "forge" {
		return entryPlan{name: name, keep: true}
	}
	// An undeclared entry, or one declaring the retired forge role, is credentials
	// for the code host: it moves.
	plan := entryPlan{name: name}
	if token != "" && !existing[name] {
		plan.forge = forgeEntryFrom(entry, name)
	}
	// Removal is earned three ways, and the third is the one this missed at
	// first. A forge entry written here obviously replaces it. The retired
	// role: forge must go whether or not anything replaced it, since leaving it
	// fails the load. And an undeclared entry whose name is ALREADY in forges:
	// was carried across by an earlier run — the replacement is right there in
	// the file, so keeping the tracker only leaves the board searching GitHub
	// for issues again ([SC-3868]). That state is not hypothetical: the first
	// version of this migration copied instead of moving, so every config
	// migrated before [SC-3884] looks exactly like it.
	//
	// Everything else stays. Deleting configuration with no replacement in sight
	// is the one outcome a migration must never produce.
	alreadyCarried := role == "" && token != "" && existing[name]
	plan.moved = plan.forge != nil || role == "forge" || alreadyCarried
	plan.keep = !plan.moved
	return plan
}

// existingForgeNames is the set of forge entries already declared, so a rerun
// adds nothing twice.
func existingForgeNames(mapping *yaml.Node) map[string]bool {
	names := map[string]bool{}
	forges := mapValue(mapping, "forges")
	if forges == nil || forges.Kind != yaml.SequenceNode {
		return names
	}
	for _, entry := range forges.Content {
		if entry.Kind == yaml.MappingNode {
			names[scalarAt(entry, "name")] = true
		}
	}
	return names
}

// forgeEntryFrom turns a githubs: entry into a forges: entry by moving the node
// itself and dropping the keys a forge has no use for.
//
// Moving rather than rebuilding is what keeps the comments: someone wrote
// "# the token lives in 1Password" next to that line, and a migration that
// silently eats their notes has damaged the file it was asked to repair. Only
// name, kind, url and token survive — projects, role, create_in, safe and
// description are issue-tracker concepts, and carrying them across would rebuild
// the union in the config file.
func forgeEntryFrom(entry *yaml.Node, name string) *yaml.Node {
	keep := map[string]bool{"name": true, "kind": true, "url": true, "token": true}
	var content []*yaml.Node
	for i := 0; i+1 < len(entry.Content); i += 2 {
		if keep[entry.Content[i].Value] {
			content = append(content, entry.Content[i], entry.Content[i+1])
		}
	}
	entry.Content = content
	head := fmt.Sprintf("Moved here from githubs: by `human config migrate` — %q opens pull requests.", name)
	if entry.HeadComment != "" {
		head += "\n" + entry.HeadComment
	}
	entry.HeadComment = head
	return entry
}

// scalarAt reads one scalar field from a mapping node, empty when absent.
func scalarAt(mapping *yaml.Node, key string) string {
	if v := mapValue(mapping, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

// removeKey drops a key and its value from a mapping node.
func removeKey(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}
