package cmdutil

import (
	"context"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"

	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/knowledge/notion"
	"github.com/gethuman-sh/human/internal/recall"
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
	// The forges: section builds forge-only instances (nil Provider) so GitHub can
	// serve pull requests without registering as a second tracker ([SC-1671]).
	github.LoadForgeInstancesWithResolver,
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

// LoadAllInstancesTolerant is LoadAllInstancesWithResolver for callers that
// would rather render the trackers that DID load than nothing at all: it
// returns every instance it could build plus the failures it hit, instead of
// abandoning the whole set at the first one.
//
// The strict variant is right where a caller needs one specific tracker and a
// missing credential means it cannot proceed. It is wrong for the board's
// listing, where one provider's momentary credential failure erased every other
// provider's cards too (SC-2005) — secrets are deliberately never cached, so
// that lookup runs on every refresh and any blip took the board down with it.
//
// The failures are returned rather than logged so the caller can surface them:
// a partial board must SAY it is partial, never quietly present fewer trackers
// as if that were all there is.
func LoadAllInstancesTolerant(dir string, lookup config.EnvLookup, resolver *vault.Resolver) ([]tracker.Instance, []error) {
	dir = config.ResolveDir(dir)
	var resolveFunc config.SecretResolveFunc
	if resolver != nil {
		resolveFunc = resolver.Resolve
	}
	var all []tracker.Instance
	var failures []error
	for _, load := range allLoadersWithResolver {
		instances, err := load(dir, lookup, resolveFunc)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		all = append(all, instances...)
	}
	return all, failures
}

// LoadNotionIndexInstances loads Notion instances and converts them
// to recall.NotionInstance for use by the indexer.
func LoadNotionIndexInstances(dir string) ([]recall.NotionInstance, error) {
	notionInsts, err := notion.LoadInstances(dir)
	if err != nil {
		return nil, err
	}
	var result []recall.NotionInstance
	for _, ni := range notionInsts {
		result = append(result, recall.NotionInstance{
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
