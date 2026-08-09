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
// Two shapes predate the separation and both are handled, because both stop
// opening pull requests without it:
//
//   - A githubs: entry with no role used to be tracker AND forge. It stays as
//     the tracker and gains a forge entry beside it carrying the same token —
//     the one identity becomes the two it was always doing the work of.
//   - A githubs: entry with role: forge was never a tracker at all. It moves:
//     the forge entry is written and the githubs: entry is removed, because
//     leaving it behind would leave a tracker declaring a role that no longer
//     exists — and loading it now fails loudly, which would make a completed
//     migration look like a broken one.
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

	// A tracker role (pm, engineering, tracker) never opened pull requests, so
	// the entry has nothing to migrate and stays exactly as it is.
	if role != "" && role != "forge" {
		return entryPlan{name: name, keep: true}
	}
	plan := entryPlan{name: name, keep: role != "forge", moved: role == "forge"}
	if token != "" && !existing[name] {
		plan.forge = forgeEntryNode(name, scalarAt(entry, "url"), token)
	}
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

// forgeEntryNode builds one forges: entry. The URL is carried only when the
// source entry set one, so a migrated config does not acquire a hardcoded
// api.github.com that the loader would have defaulted anyway.
func forgeEntryNode(name, url, token string) *yaml.Node {
	entry := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k, v string) {
		entry.Content = append(entry.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	add("name", name)
	if url != "" {
		add("url", url)
	}
	add("token", token)
	entry.HeadComment = fmt.Sprintf("Written by `human config migrate` from the githubs: entry %q.", name)
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
