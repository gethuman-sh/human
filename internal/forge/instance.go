package forge

import (
	"fmt"

	"github.com/gethuman-sh/human/errors"
)

// Instance is one configured code host: a name, a kind, and a client that can
// open pull requests.
//
// It exists so the forge domain has a type of its own. A forge used to be
// carried as a tracker instance with its Provider left nil and role: forge
// written on it — which meant every caller had to ask which of the two things a
// value really was, and the codebase grew a predicate, a filter, a credential
// skip and two kind tests to keep asking it. A pull request is a repository
// concept and an issue is a tracker one; the types say so now, so nobody has to
// ask ([SC-3876]).
//
// There is deliberately no Projects, Role or Safe field. A forge holds no
// issues, plays no part in topology, and has nothing destructive to guard —
// every one of those fields would be a tracker concept wearing a forge's name.
type Instance struct {
	Name string
	Kind string
	URL  string
	// Forge is the client. An Instance without one is not constructed: a config
	// entry whose credentials do not resolve is dropped at load time rather than
	// carried as a half-built value.
	Forge Forge
}

// Resolve returns the configured forge of the given kind, optionally narrowed to
// one entry by name.
//
// The error names the section to add rather than the condition that failed:
// reaching this point means someone is trying to open a pull request, and the
// useful answer is the three lines of YAML that would let them.
func Resolve(kind string, instances []Instance, name string) (*Instance, error) {
	var filtered []Instance
	for _, inst := range instances {
		if inst.Kind == kind {
			filtered = append(filtered, inst)
		}
	}
	if len(filtered) == 0 {
		return nil, errors.WithDetails(NoForgeConfigured(kind), "kind", kind)
	}
	if name == "" {
		return &filtered[0], nil
	}
	for i := range filtered {
		if filtered[i].Name == name {
			return &filtered[i], nil
		}
	}
	return nil, errors.WithDetails("forge name not found for kind", "name", name, "kind", kind)
}

// NoForgeConfigured is the message shown when a pull request is attempted with
// no forge configured.
//
// It spells out the entry because of who reads it and when: a `githubs:` entry
// opened pull requests for as long as this tool has existed, and the separation
// of the two domains ends that. Whoever hits this is mid-deploy, on a machine
// whose pipeline has just stopped, and "no forge configured" alone would send
// them looking for a bug in the deploy path rather than at three lines of
// config. `human config migrate` writes them.
func NoForgeConfigured(kind string) string {
	return fmt.Sprintf(`no %s forge configured. A %ss: entry is an issue tracker and no longer opens pull requests — declare the code host separately:

  forges:
    - name: <name>
      token: <token or 1pw:// reference>

Run `+"`human config migrate`"+` to write it from your existing %ss: entry.`, kind, kind, kind)
}
