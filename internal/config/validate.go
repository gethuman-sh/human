package config

import "fmt"

// Severity says what a problem costs.
//
// Two levels, not five. An Error means the configuration will not do what it
// says — a command fails, or a backend is silently absent. A Warning means it
// works and costs something the author probably did not intend. Anything
// finer would be a judgement nobody can act on differently.
type Severity string

const (
	// Error: this configuration is wrong and something will fail or vanish.
	Error Severity = "error"
	// Warning: this configuration works, and will do something expensive or
	// surprising that its author is unlikely to have meant.
	Warning Severity = "warning"
)

// Problem is one thing wrong with a configuration, said in the terms the person
// who wrote the file would use: which entry, what will happen, and what to do.
//
// Fix is separate from Message because the two are read at different moments —
// the message says why a command just failed, the fix says what to type next —
// and because a rule with no fix to offer should be forced to admit it rather
// than trailing off into advice-shaped text.
type Problem struct {
	Severity Severity `json:"severity"`
	// Rule is a stable identifier, so a report can be compared across runs and
	// a check can be silenced by name if it ever needs to be.
	Rule     string `json:"rule"`
	Section  string `json:"section,omitempty"`
	Instance string `json:"instance,omitempty"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

func (p Problem) String() string {
	s := fmt.Sprintf("%s: %s", p.Severity, p.Message)
	if p.Fix != "" {
		s += " — " + p.Fix
	}
	return s
}

// RetiredForgeRole is the role a githubs: entry used to declare to say "this is
// a code host, not a tracker". The sections say that now, so the role has
// nothing left to mean ([SC-3876]).
const RetiredForgeRole = "forge"

// RetiredForgeRoleMessage is the one wording for that condition, shared by the
// document's own check and by the GitHub loader that must fail on it — the two
// were separate strings, which is precisely how a rule and its enforcement
// drift ([SC-3889]).
func RetiredForgeRoleMessage(name string) string {
	return "GitHub tracker " + name + " declares role: forge, but a tracker entry is an issue tracker. " +
		"A code host is configured in the forges: section — run `human config migrate` to move it."
}

// Describe names one tracker entry the way its file spells it, so a message
// points at something the reader can find. The same backend can be declared in
// the unified list or a per-vendor section ([SC-3874]), and a message naming the
// wrong one sends someone looking through the wrong part of their config.
func (t Tracker) Describe() string {
	if t.Section == UnifiedTrackerSection {
		return fmt.Sprintf("trackers: entry %q (kind: %s)", t.Name, t.Kind)
	}
	return fmt.Sprintf("%s: entry %q", t.Section, t.Name)
}

// Validate reports everything wrong with this configuration that can be known
// from the file alone.
//
// From the file alone is the boundary, and it is drawn where it is because the
// alternative is a check that lies. Whether a token resolves depends on a
// secret store that may be locked; whether a tracker answers depends on a
// network. Those are diagnosed by the commands that need them, at the moment
// they need them. What lives here is what the file itself says — including, and
// this is the part that had no home before, what two sections say TOGETHER.
func (d *Document) Validate() []Problem {
	trackers := d.Trackers()
	forges := d.Forges()

	var problems []Problem
	problems = append(problems, retiredForgeRoles(trackers)...)
	problems = append(problems, leftoverTrackers(trackers, forges)...)
	problems = append(problems, unscopedGitHubTrackers(trackers)...)
	problems = append(problems, duplicateNames(trackers, forges)...)
	problems = append(problems, missingForge(forges)...)
	return problems
}

// Errors reports whether any problem is fatal, so a caller can exit non-zero
// on a broken config while still printing the warnings.
func Errors(problems []Problem) bool {
	for _, p := range problems {
		if p.Severity == Error {
			return true
		}
	}
	return false
}

// retiredForgeRoles catches an entry still declaring the role that was removed
// with the union. Left alone it does not merely fail — it becomes a tracker
// carrying a role nothing understands, which can join PM resolution and quietly
// become the board's tracker.
func retiredForgeRoles(trackers []Tracker) []Problem {
	var out []Problem
	for _, t := range trackers {
		if t.Kind != "github" || t.Role != RetiredForgeRole {
			continue
		}
		out = append(out, Problem{
			Severity: Error, Rule: "retired-forge-role",
			Section: t.Section, Instance: t.Name,
			Message: RetiredForgeRoleMessage(t.Name),
			Fix:     "human config migrate",
		})
	}
	return out
}

// leftoverTrackers catches a half-done migration: an undeclared GitHub entry
// standing beside a forge of the same name.
//
// This is the cross-section rule — the one that could not be expressed anywhere
// before, because no layer held two sections at once. It had to be smuggled
// into the migration command, where nothing else could consult it ([SC-3887]).
// Its cost is not theoretical: the leftover entry is a tracker, so the board
// asks GitHub for issues, which is a search across every issue the token can
// see on a rate-limited endpoint ([SC-3868]).
func leftoverTrackers(trackers []Tracker, forges []Forge) []Problem {
	byName := make(map[string]bool, len(forges))
	for _, f := range forges {
		byName[f.Name] = true
	}
	var out []Problem
	for _, t := range trackers {
		if t.Kind != "github" || t.Role != "" || !byName[t.Name] {
			continue
		}
		out = append(out, Problem{
			Severity: Error, Rule: "half-migrated-github",
			Section: t.Section, Instance: t.Name,
			Message: fmt.Sprintf("%s sits beside a forges: entry of the same name, so an earlier migration was left half done. "+
				"It declares no role, so it is an issue tracker: the board will ask GitHub for issues and get a rate-limited search across everything the token can see.", t.Describe()),
			Fix: "human config migrate",
		})
	}
	return out
}

// unscopedGitHubTrackers is the trap that survived every fix around it: a
// GitHub tracker with no projects.
//
// "Empty projects means show all work" is documented, and on Shortcut or Linear
// it is a cheap listing. On GitHub it is GET /search/issues across every issue
// the token can see — a different endpoint, its own quota, exhausted in minutes
// by a poll loop. The entry is correct, the role is right, and the behaviour is
// still wrong, which is exactly the class of fault a loader can never catch:
// this is not "can it load" but "what will it do" ([SC-3888]).
func unscopedGitHubTrackers(trackers []Tracker) []Problem {
	var out []Problem
	for _, t := range trackers {
		if t.Kind != "github" || t.Role == RetiredForgeRole || len(t.Projects) > 0 {
			continue
		}
		out = append(out, Problem{
			Severity: Warning, Rule: "unscoped-github-tracker",
			Section: t.Section, Instance: t.Name,
			Message: fmt.Sprintf("%s configures no projects, so listing it searches every issue the token can see "+
				"on GitHub's rate-limited search endpoint, once per board refresh.", t.Describe()),
			Fix: "add projects: [owner/repo], or move the entry to forges: if it only opens pull requests",
		})
	}
	return out
}

// duplicateNames catches two entries of one kind sharing a name, which makes
// --tracker=<name> ambiguous and every by-name resolution a coin toss. A
// tracker and a forge MAY share a name — they are different domains, resolved
// from different lists — so only collisions within one list are reported.
func duplicateNames(trackers []Tracker, forges []Forge) []Problem {
	var out []Problem
	// Keyed by kind rather than by section: the same backend declared twice
	// under one name is ambiguous however the two entries are spelled, and a
	// config part-way through a migration is exactly where that happens.
	seen := map[string]bool{}
	for _, t := range trackers {
		key := t.Kind + "/" + t.Name
		if seen[key] {
			out = append(out, Problem{
				Severity: Error, Rule: "duplicate-name",
				Section: t.Section, Instance: t.Name,
				Message: fmt.Sprintf("%s is configured twice as %q, so --tracker=%s cannot say which one it means.", t.Kind, t.Name, t.Name),
				Fix:     "rename one of them, or remove the entry the migration left behind",
			})
		}
		seen[key] = true
	}
	forgeSeen := map[string]bool{}
	for _, f := range forges {
		if forgeSeen[f.Name] {
			out = append(out, Problem{
				Severity: Error, Rule: "duplicate-name",
				Section: ForgeSection, Instance: f.Name,
				Message: fmt.Sprintf("forges: has two entries named %q.", f.Name),
				Fix:     "rename one of them",
			})
		}
		forgeSeen[f.Name] = true
	}
	return out
}

// missingForge reports a configuration that cannot open a pull request. It is a
// warning rather than an error because plenty of setups never open one — but it
// is reported, because the alternative is finding out when a deploy stops.
func missingForge(forges []Forge) []Problem {
	if len(forges) > 0 {
		return nil
	}
	return []Problem{{
		Severity: Warning, Rule: "no-forge",
		Section: ForgeSection,
		Message: "no forges: entry, so nothing can open a pull request.",
		Fix:     "add a forges: entry, or run `human config migrate` if a githubs: entry used to do it",
	}}
}
