package cmdutil

import (
	"context"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/forge"
	forgegithub "github.com/gethuman-sh/human/internal/forge/github"
	"github.com/gethuman-sh/human/internal/index"
	"github.com/gethuman-sh/human/internal/knowledge/notion"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/tracker/azuredevops"
	"github.com/gethuman-sh/human/internal/tracker/clickup"
	"github.com/gethuman-sh/human/internal/tracker/github"
	"github.com/gethuman-sh/human/internal/tracker/gitlab"
	"github.com/gethuman-sh/human/internal/tracker/jira"
	"github.com/gethuman-sh/human/internal/tracker/linear"
	"github.com/gethuman-sh/human/internal/tracker/shortcut"
	"github.com/gethuman-sh/human/internal/vault"
)

// instanceLoaderWithResolver loads tracker instances with a custom env lookup
// and a vault secret resolver.
type instanceLoaderWithResolver func(dir string, lookup config.EnvLookup, resolver config.SecretResolveFunc) ([]tracker.Instance, error)

// allLoadersWithResolver lists every provider's resolver-aware instance loader.
var allLoadersWithResolver = []instanceLoaderWithResolver{
	jira.LoadInstancesWithResolver,
	github.LoadInstancesWithResolver,
	gitlab.LoadInstancesWithResolver,
	linear.LoadInstancesWithResolver,
	azuredevops.LoadInstancesWithResolver,
	shortcut.LoadInstancesWithResolver,
	clickup.LoadInstancesWithResolver,
}

// LoadAllInstances collects tracker instances from all provider configs.
// The dir parameter accepts config.DirProject (resolved via the per-request
// env map in daemon context) or config.DirCwd (".") for direct CLI usage.
// If a vault section is present in .humanconfig, secret references (e.g. 1pw://)
// are resolved automatically.
//
// This is the legacy entry point and uses the process environment to
// resolve config.DirProject. Daemon-served code paths must use
// LoadAllInstancesCtx with a cobra command context that carries the
// per-request env map.
func LoadAllInstances(dir string) ([]tracker.Instance, error) {
	return LoadAllInstancesCtx(context.Background(), dir)
}

// LoadAllInstancesCtx is the context-aware variant of LoadAllInstances.
// The ctx is consulted for env values (HUMAN_PROJECT_DIR) before falling
// back to the process environment, so daemon-served handlers see only
// their own request's env map.
func LoadAllInstancesCtx(ctx context.Context, dir string) ([]tracker.Instance, error) {
	dir = config.ResolveDirCtx(ctx, dir)

	// Prefer a resolver injected on the context (e.g. by the daemon) so
	// per-request commands reuse the session-scoped provider instead of
	// shelling out to op.exe on every call.
	resolver := vault.ResolverFromContext(ctx)
	if resolver == nil {
		// Auto-detect vault config for the direct CLI path.
		vcfg, vcfgErr := vault.ReadConfig(dir)
		if vcfgErr != nil {
			// Surface the parse error but continue without vault resolution so
			// the caller still sees tracker instances get loaded — the tracker
			// client will fail loudly if secrets are unresolved.
			log.Warn().Err(vcfgErr).Str("dir", dir).Msg("vault config parse failed; resolution disabled")
		}
		resolver = vault.NewResolverFromConfig(vcfg)
	}
	var resolveFunc config.SecretResolveFunc
	if resolver != nil {
		resolveFunc = resolver.Resolve
	}

	var all []tracker.Instance
	for _, load := range allLoadersWithResolver {
		instances, err := load(dir, nil, resolveFunc)
		if err != nil {
			return nil, err
		}
		all = append(all, instances...)
	}
	return all, nil
}

// LoadAllInstancesWithResolver collects tracker instances using a custom env
// lookup function and vault secret resolver. This enables both per-project
// token scoping and 1pw:// secret references in .humanconfig.
//
// Daemon callers should pass a concrete dir (not config.DirProject) since
// they already know the project directory from the per-request routing.
func LoadAllInstancesWithResolver(dir string, lookup config.EnvLookup, resolver *vault.Resolver) ([]tracker.Instance, error) {
	dir = config.ResolveDir(dir)
	var resolveFunc config.SecretResolveFunc
	if resolver != nil {
		resolveFunc = resolver.Resolve
	}
	var all []tracker.Instance
	for _, load := range allLoadersWithResolver {
		instances, err := load(dir, lookup, resolveFunc)
		if err != nil {
			return nil, err
		}
		all = append(all, instances...)
	}
	return all, nil
}

// InstanceFromFlags builds a tracker instance from root persistent flags,
// returning nil when insufficient flags are provided.
func InstanceFromFlags(cmd *cobra.Command) *tracker.Instance {
	getFlag := func(name string) string {
		v, _ := cmd.Root().PersistentFlags().GetString(name)
		return v
	}

	if inst := instanceFromJiraFlags(getFlag); inst != nil {
		return inst
	}
	if inst := instanceFromAzureFlags(getFlag); inst != nil {
		return inst
	}

	// Simple token-based providers: token flag, url flag, default URL, kind,
	// tracker constructor, and an optional forge constructor for backends that
	// also host pull requests (GitHub).
	type simpleProvider struct {
		tokenFlag  string
		urlFlag    string
		defaultURL string
		kind       string
		newClient  func(url, token string) tracker.Provider
		newForge   func(url, token string) forge.Forge
	}
	simpleProviders := []simpleProvider{
		{"github-token", "github-url", "https://api.github.com", "github", func(u, t string) tracker.Provider { return github.New(u, t) }, func(u, t string) forge.Forge { return forgegithub.New(u, t) }},
		{"gitlab-token", "gitlab-url", "https://gitlab.com", "gitlab", func(u, t string) tracker.Provider { return gitlab.New(u, t) }, nil},
		{"linear-token", "linear-url", "https://api.linear.app", "linear", func(u, t string) tracker.Provider { return linear.New(u, t) }, nil},
		{"shortcut-token", "shortcut-url", "https://api.app.shortcut.com", "shortcut", func(u, t string) tracker.Provider { return shortcut.New(u, t) }, nil},
		{"clickup-token", "clickup-url", "https://api.clickup.com", "clickup", func(u, t string) tracker.Provider { return clickup.New(u, t, "") }, nil},
	}
	for _, sp := range simpleProviders {
		token := getFlag(sp.tokenFlag)
		if token == "" {
			continue
		}
		url := getFlag(sp.urlFlag)
		if url == "" {
			url = sp.defaultURL
		}
		inst := &tracker.Instance{
			Kind:     sp.kind,
			URL:      url,
			Provider: sp.newClient(url, token),
		}
		if sp.newForge != nil {
			inst.Forge = sp.newForge(url, token)
		}
		return inst
	}

	return nil
}

// instanceFromJiraFlags builds a Jira instance from flags, or returns nil.
func instanceFromJiraFlags(getFlag func(string) string) *tracker.Instance {
	jiraURL := getFlag("jira-url")
	jiraUser := getFlag("jira-user")
	jiraKey := getFlag("jira-key")
	if jiraURL == "" || jiraUser == "" || jiraKey == "" {
		return nil
	}
	return &tracker.Instance{
		Kind:     "jira",
		URL:      jiraURL,
		User:     jiraUser,
		Provider: jira.New(jiraURL, jiraUser, jiraKey),
	}
}

// instanceFromAzureFlags builds an Azure DevOps instance from flags, or returns nil.
func instanceFromAzureFlags(getFlag func(string) string) *tracker.Instance {
	azureToken := getFlag("azure-token")
	azureOrg := getFlag("azure-org")
	if azureToken == "" || azureOrg == "" {
		return nil
	}
	url := getFlag("azure-url")
	if url == "" {
		url = "https://dev.azure.com"
	}
	return &tracker.Instance{
		Kind:     "azuredevops",
		URL:      url,
		Provider: azuredevops.New(url, azureOrg, azureToken),
	}
}

// LoadNotionIndexInstances loads Notion instances and converts them
// to index.NotionInstance for use by the indexer.
func LoadNotionIndexInstances(dir string) ([]index.NotionInstance, error) {
	notionInsts, err := notion.LoadInstances(dir)
	if err != nil {
		return nil, err
	}
	var result []index.NotionInstance
	for _, ni := range notionInsts {
		result = append(result, index.NotionInstance{
			Name:   ni.Name,
			URL:    ni.URL,
			Client: ni.Client,
		})
	}
	return result, nil
}

// humanFilePath returns the path to a file inside ~/.human/, creating the
// directory if needed. Falls back to ./.human/ if the home dir is unknown.
func humanFilePath(filename string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", filename)
	}
	dir := filepath.Join(home, ".human")
	_ = os.MkdirAll(dir, 0o750)
	return filepath.Join(dir, filename)
}

// AuditLogPath returns the path to the audit log file (~/.human/audit.log),
// creating the directory if needed.
func AuditLogPath() string { return humanFilePath("audit.log") }

// DestructiveLogPath returns the path to the destructive operations log file
// (~/.human/destructive.log), creating the directory if needed.
func DestructiveLogPath() string { return humanFilePath("destructive.log") }
