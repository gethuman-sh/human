// Package cliflags centralises CLI flag metadata shared between client-side
// argument forwarding and the daemon's destructive-command detection. Keeping a
// single source of truth prevents the two from drifting — a drift there is a
// security gap, since a value flag the detector doesn't recognise lets its
// value shift the positional indices and slip a delete/edit past confirmation.
package cliflags

// ValueFlags lists global persistent flags that take a separate value token
// (e.g. "--tracker work"). Consumers scanning positional subcommands must skip
// the token that follows one of these so it is not mistaken for a subcommand,
// verb, or key. The "--flag=value" form is a single token and needs no skip.
//
// Keep this in sync with PersistentFlags() in newRootCmd.
var ValueFlags = map[string]bool{
	"--tracker":        true,
	"--jira-key":       true,
	"--jira-url":       true,
	"--jira-user":      true,
	"--github-token":   true,
	"--github-url":     true,
	"--gitlab-token":   true,
	"--gitlab-url":     true,
	"--linear-token":   true,
	"--linear-url":     true,
	"--azure-token":    true,
	"--azure-url":      true,
	"--azure-org":      true,
	"--shortcut-token": true,
	"--shortcut-url":   true,
	"--clickup-token":  true,
	"--clickup-url":    true,
}
