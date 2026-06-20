package cmdutil

import (
	forgegithub "github.com/gethuman-sh/human/internal/forge/github"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/tracker/azuredevops"
	"github.com/gethuman-sh/human/internal/tracker/github"
	"github.com/gethuman-sh/human/internal/tracker/gitlab"
	"github.com/gethuman-sh/human/internal/tracker/jira"
	"github.com/gethuman-sh/human/internal/tracker/linear"
	"github.com/gethuman-sh/human/internal/tracker/shortcut"
)

// InstanceFromURL attempts to build a tracker Instance from a parsed URL
// using environment variable credentials.
// Returns (instance, true) if credentials are available and the instance was built.
// Returns (zero, false) if credentials are missing.
func InstanceFromURL(parsed *tracker.ParsedURL) (*tracker.Instance, bool) {
	return instanceFromURLEnv(parsed, tracker.CheckCreds)
}

// instanceFromURLEnv is like InstanceFromURL but accepts a custom credential checker.
func instanceFromURLEnv(parsed *tracker.ParsedURL, checkCreds func(tracker.CredSpec) tracker.CredResult) (*tracker.Instance, bool) {
	spec, ok := tracker.CredSpecForKind(parsed.Kind)
	if !ok {
		return nil, false
	}

	result := checkCreds(spec)
	if !result.Complete {
		return nil, false
	}

	inst := buildInstanceFromCreds(parsed, result)
	if inst == nil {
		return nil, false
	}

	return inst, true
}

// buildInstanceFromCreds constructs a tracker.Instance from a parsed URL and resolved credentials.
func buildInstanceFromCreds(parsed *tracker.ParsedURL, creds tracker.CredResult) *tracker.Instance {
	switch parsed.Kind {
	case "jira":
		return &tracker.Instance{
			Kind:     "jira",
			URL:      parsed.BaseURL,
			User:     creds.Available["USER"],
			Provider: jira.New(parsed.BaseURL, creds.Available["USER"], creds.Available["KEY"]),
		}
	case "github":
		return &tracker.Instance{
			Kind:     "github",
			URL:      parsed.BaseURL,
			Provider: github.New(parsed.BaseURL, creds.Available["TOKEN"]),
			// GitHub is also a code forge — expose PR creation.
			Forge: forgegithub.New(parsed.BaseURL, creds.Available["TOKEN"]),
		}
	case "gitlab":
		return &tracker.Instance{
			Kind:     "gitlab",
			URL:      parsed.BaseURL,
			Provider: gitlab.New(parsed.BaseURL, creds.Available["TOKEN"]),
		}
	case "linear":
		return &tracker.Instance{
			Kind:     "linear",
			URL:      parsed.BaseURL,
			Provider: linear.New(parsed.BaseURL, creds.Available["TOKEN"]),
		}
	case "azuredevops":
		return &tracker.Instance{
			Kind:     "azuredevops",
			URL:      parsed.BaseURL,
			Provider: azuredevops.New(parsed.BaseURL, parsed.Org, creds.Available["TOKEN"]),
		}
	case "shortcut":
		return &tracker.Instance{
			Kind:     "shortcut",
			URL:      parsed.BaseURL,
			Provider: shortcut.New(parsed.BaseURL, creds.Available["TOKEN"]),
		}
	default:
		return nil
	}
}
