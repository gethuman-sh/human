package cmdutil

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/forge"
	forgegithub "github.com/gethuman-sh/human/internal/forge/github"
	"github.com/gethuman-sh/human/internal/vault"
)

// forgeLoader loads one section's forge instances with a per-project env lookup
// and a vault secret resolver.
type forgeLoader func(dir string, lookup config.EnvLookup, resolver config.SecretResolveFunc) ([]forge.Instance, error)

// allForgeLoaders lists every code host's loader. One entry today, because
// GitHub is the only forge — the list exists so adding a second is adding a
// line, exactly as it is for trackers.
var allForgeLoaders = []forgeLoader{
	forgegithub.LoadInstancesWithResolver,
}

// LoadForges collects the configured code hosts, resolving vault references the
// same way the tracker loader does. It is the entry point commands use; the
// dir parameter accepts config.DirProject or config.DirCwd.
func LoadForges(dir string) ([]forge.Instance, error) {
	return LoadForgesCtx(context.Background(), dir)
}

// LoadForgesCtx is the context-aware variant: ctx carries the per-request env
// map and, in the daemon, a session-scoped vault resolver, so a forge lookup
// costs no extra secret-store round trip.
func LoadForgesCtx(ctx context.Context, dir string) ([]forge.Instance, error) {
	dir = config.ResolveDirCtx(ctx, dir)
	resolver := vault.ResolverFromContext(ctx)
	if resolver == nil {
		vcfg, vcfgErr := vault.ReadConfig(dir)
		if vcfgErr != nil {
			// Same judgement as the tracker path: report the parse failure and
			// carry on unresolved, so a broken vault stanza does not make it look
			// as though no forge is configured.
			log.Warn().Err(vcfgErr).Str("dir", dir).Msg("vault config parse failed; resolution disabled")
		}
		resolver = vault.NewResolverFromConfig(vcfg)
	}
	return LoadAllForges(dir, nil, resolver)
}

// LoadAllForges collects the configured code hosts.
//
// It is a separate function from LoadAllInstances, over a separate list, of a
// separate type — which is the whole point. A forge used to arrive inside the
// tracker list with its Provider nil'd out, so every consumer of that list had
// to know it might be holding something that was not a tracker ([SC-3876]).
func LoadAllForges(dir string, lookup config.EnvLookup, resolver *vault.Resolver) ([]forge.Instance, error) {
	dir = config.ResolveDir(dir)
	var resolveFunc config.SecretResolveFunc
	if resolver != nil {
		resolveFunc = resolver.Resolve
	}
	var all []forge.Instance
	for _, load := range allForgeLoaders {
		instances, err := load(dir, lookup, resolveFunc)
		if err != nil {
			return nil, err
		}
		all = append(all, instances...)
	}
	return all, nil
}
