package cmdutil

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/dispatch"
	"github.com/gethuman-sh/human/internal/env"
	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/gitrepo"
	"github.com/gethuman-sh/human/internal/slack"
	"github.com/gethuman-sh/human/internal/telegram"
	"github.com/gethuman-sh/human/internal/tracker"
)

// Deps holds injectable dependencies for command builders that need
// tracker instance loading and resolution.
//
// LoadInstancesCtx is the preferred entry point: it carries the cobra
// command context so daemon-served handlers can resolve project paths
// from per-request env maps without mutating os.Environ. LoadInstances
// is kept for backwards compatibility with legacy tests and direct CLI
// callers; resolve.go prefers LoadInstancesCtx when set.
//
// loadDestructiveNotifierCtx is the context-aware variant of
// DestructiveNotifier — daemon paths set it so per-request notifier
// configuration is read from the request's env, not the daemon process.
type Deps struct {
	LoadInstances       func(dir string) ([]tracker.Instance, error)
	LoadInstancesCtx    func(ctx context.Context, dir string) ([]tracker.Instance, error)
	InstanceFromFlags   func(cmd *cobra.Command) *tracker.Instance
	AuditLogPath        func() string
	DestructiveLogPath  func() string
	DestructiveNotifier func(ctx context.Context) tracker.DestructiveNotifier // returns nil if no notification configured; ctx carries per-request env
}

// DefaultDeps returns a Deps using the real implementations.
func DefaultDeps() Deps {
	return Deps{
		LoadInstances:       LoadAllInstances,
		LoadInstancesCtx:    LoadAllInstancesCtx,
		InstanceFromFlags:   InstanceFromFlags,
		AuditLogPath:        AuditLogPath,
		DestructiveLogPath:  DestructiveLogPath,
		DestructiveNotifier: loadDestructiveNotifier,
	}
}

// AutoResult holds the resolved provider and extracted key from ResolveAutoProvider.
type AutoResult struct {
	Provider tracker.Provider
	Kind     string
	Key      string // resolved key (= input if key, = extracted key if URL)
	Cleanup  func()
}

// ResolveProvider loads instances, applies CLI flag overrides, and resolves
// the provider for the given kind using the tracker name from persistent flags.
func ResolveProvider(cmd *cobra.Command, kind string, deps Deps) (tracker.Provider, func(), error) {
	ctx := cmdContext(cmd)
	instances, err := loadInstancesCtx(ctx, deps)
	if err != nil {
		return nil, nil, err
	}

	if inst := deps.InstanceFromFlags(cmd); inst != nil {
		instances = append(instances, *inst)
	}

	trackerName, _ := cmd.Root().PersistentFlags().GetString("tracker")

	instance, err := tracker.ResolveByKind(kind, instances, trackerName)
	if err != nil {
		return nil, nil, err
	}

	safeFlag, _ := cmd.Root().PersistentFlags().GetBool("safe")
	p := instance.Provider
	if safeFlag || instance.Safe || env.Lookup(ctx, "HUMAN_SAFE_MODE") == "1" {
		p = tracker.NewSafeProvider(p, instance.Name)
	}

	p = applyPolicyWrapper(ctx, p, instance.Name, os.Stderr)

	auditPath := deps.AuditLogPath()
	ap, auditErr := tracker.NewAuditProvider(p, instance.Name, instance.Kind, auditPath)
	auditCleanup := func() {}
	if auditErr != nil {
		fmt.Fprintln(os.Stderr, "warning: audit logging disabled:", auditErr)
	} else {
		p = ap
		auditCleanup = func() { _ = ap.Close() }
	}

	p, dpCleanup := applyDestructiveWrapper(ctx, p, instance.Name, instance.Kind, deps, os.Stderr)
	return p, func() { dpCleanup(); auditCleanup() }, nil
}

// ResolveForge resolves the configured instance for the given kind and returns
// it as a forge.Forge (code-host) capability. Forge operations (opening a PR)
// are intentionally not wrapped by the tracker decorator chain: they do not
// mutate tracker data and have no audit/policy semantics. A clear error is
// returned when the resolved backend is not a forge (e.g. a pure issue tracker).
func ResolveForge(cmd *cobra.Command, kind string, deps Deps) (forge.Forge, error) {
	ctx := cmdContext(cmd)
	instances, err := loadInstancesCtx(ctx, deps)
	if err != nil {
		return nil, err
	}

	if inst := deps.InstanceFromFlags(cmd); inst != nil {
		instances = append(instances, *inst)
	}

	trackerName, _ := cmd.Root().PersistentFlags().GetString("tracker")

	instance, err := tracker.ResolveByKind(kind, instances, trackerName)
	if err != nil {
		return nil, err
	}

	f, ok := instance.Provider.(forge.Forge)
	if !ok {
		return nil, errors.WithDetails("pull requests not supported by this tracker", "kind", kind)
	}
	return f, nil
}

// OriginForge derives the code forge from the local git "origin" remote and
// returns it together with the "owner/repo" parsed from that remote. The forge
// kind comes from the remote host (e.g. github.com → github); the configured
// instance is then resolved by kind via ResolveForge. Use it for commands that
// should default to "the repository I'm standing in".
func OriginForge(cmd *cobra.Command, deps Deps) (forge.Forge, string, error) {
	ctx := cmdContext(cmd)
	dir := config.ResolveDirCtx(ctx, config.DirProject)
	raw, err := gitrepo.OriginURL(ctx, dir)
	if err != nil {
		return nil, "", err
	}
	host, repo, ok := forge.ParseRemoteURL(raw)
	if !ok {
		return nil, "", errors.WithDetails("could not parse git origin remote", "remote", raw)
	}
	kind := forge.KindForHost(host)
	if kind == "" {
		return nil, "", errors.WithDetails("git origin host is not a supported forge", "host", host)
	}
	f, err := ResolveForge(cmd, kind, deps)
	if err != nil {
		return nil, "", err
	}
	return f, repo, nil
}

// OriginRepo returns the "owner/repo" parsed from the local git "origin"
// remote. Used by the per-kind `pr create` command to default --repo.
func OriginRepo(cmd *cobra.Command) (string, error) {
	ctx := cmdContext(cmd)
	dir := config.ResolveDirCtx(ctx, config.DirProject)
	raw, err := gitrepo.OriginURL(ctx, dir)
	if err != nil {
		return "", err
	}
	_, repo, ok := forge.ParseRemoteURL(raw)
	if !ok {
		return "", errors.WithDetails("could not parse git origin remote", "remote", raw)
	}
	return repo, nil
}

// ResolveAutoProvider loads all instances, applies flag overrides, and resolves
// the provider without requiring a fixed kind. It uses tracker.Resolve for
// auto-detection and falls back to FindTracker + ResolveByKind for ambiguous
// get commands.
//
// When input is a URL, it parses the URL to extract kind, base URL, and key,
// then tries to match an existing config instance or build one from env vars.
func ResolveAutoProvider(ctx context.Context, cmd *cobra.Command, input string, allowFindFallback bool, deps Deps) (*AutoResult, error) {
	if tracker.IsURL(input) {
		return resolveFromURL(ctx, cmd, input, deps)
	}
	return resolveFromKey(ctx, cmd, input, allowFindFallback, deps)
}

// resolveFromURL handles URL-based resolution.
func resolveFromURL(ctx context.Context, cmd *cobra.Command, rawURL string, deps Deps) (*AutoResult, error) {
	parsed, ok := tracker.ParseURL(rawURL)
	if !ok {
		return nil, errors.WithDetails("unrecognized tracker URL format", "url", rawURL)
	}

	// 1. Check existing config for a matching instance.
	if ctx == nil {
		ctx = cmdContext(cmd)
	}
	instances, err := loadInstancesCtx(ctx, deps)
	if err != nil {
		return nil, err
	}

	if inst := deps.InstanceFromFlags(cmd); inst != nil {
		instances = append(instances, *inst)
	}

	if instance := matchInstanceByKindAndURL(instances, parsed.Kind, parsed.BaseURL); instance != nil {
		return wrapInstance(cmd, instance, parsed.Key, deps)
	}

	// 2. Try building from env vars.
	inst, ok := InstanceFromURL(parsed)
	if ok {
		// Auto-save config for future use (best-effort).
		if saveErr := AutoSaveTrackerConfig(parsed, "."); saveErr != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not save config: %v\n", saveErr)
		} else {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Saved tracker configuration to .humanconfig.yaml")
		}
		return wrapInstance(cmd, inst, parsed.Key, deps)
	}

	// 3. No creds — return actionable error.
	spec, _ := tracker.CredSpecForKind(parsed.Kind)
	result := tracker.CheckCreds(spec)
	return nil, errors.WithDetails(tracker.FormatMissingCreds(result, parsed))
}

// resolveFromKey is the original key-based resolution logic.
func resolveFromKey(ctx context.Context, cmd *cobra.Command, keyHint string, allowFindFallback bool, deps Deps) (*AutoResult, error) {
	if ctx == nil {
		ctx = cmdContext(cmd)
	}
	instances, err := loadInstancesCtx(ctx, deps)
	if err != nil {
		return nil, err
	}

	if inst := deps.InstanceFromFlags(cmd); inst != nil {
		instances = append(instances, *inst)
	}

	trackerName, _ := cmd.Root().PersistentFlags().GetString("tracker")

	// Try Resolve first (name-based or auto-detect).
	instance, err := tracker.Resolve(trackerName, instances, keyHint)
	if err != nil && allowFindFallback && trackerName == "" {
		// Ambiguous — fall back to FindTracker for get commands.
		result, findErr := tracker.FindTracker(ctx, keyHint, instances)
		if findErr != nil {
			// Return the original Resolve error — it's more informative.
			return nil, err
		}
		instance, err = tracker.ResolveByKind(result.Provider, instances, "")
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	return wrapInstance(cmd, instance, keyHint, deps)
}

// wrapInstance applies safe/audit wrappers and returns an AutoResult.
func wrapInstance(cmd *cobra.Command, instance *tracker.Instance, key string, deps Deps) (*AutoResult, error) {
	ctx := cmdContext(cmd)
	safeFlag, _ := cmd.Root().PersistentFlags().GetBool("safe")
	p := instance.Provider
	if safeFlag || instance.Safe || env.Lookup(ctx, "HUMAN_SAFE_MODE") == "1" {
		p = tracker.NewSafeProvider(p, instance.Name)
	}

	var errW io.Writer = os.Stderr
	if cmd != nil {
		errW = cmd.ErrOrStderr()
	}

	p = applyPolicyWrapper(ctx, p, instance.Name, errW)

	auditPath := deps.AuditLogPath()
	ap, auditErr := tracker.NewAuditProvider(p, instance.Name, instance.Kind, auditPath)
	auditCleanup := func() {}
	if auditErr != nil {
		_, _ = fmt.Fprintln(errW, "warning: audit logging disabled:", auditErr)
	} else {
		p = ap
		auditCleanup = func() { _ = ap.Close() }
	}

	p, dpCleanup := applyDestructiveWrapper(ctx, p, instance.Name, instance.Kind, deps, errW)
	return &AutoResult{Provider: p, Kind: instance.Kind, Key: key, Cleanup: func() { dpCleanup(); auditCleanup() }}, nil
}

// matchInstanceByKindAndURL finds a configured instance matching the tracker kind
// whose URL is compatible with the given base URL.
func matchInstanceByKindAndURL(instances []tracker.Instance, kind, baseURL string) *tracker.Instance {
	for i := range instances {
		if instances[i].Kind != kind {
			continue
		}
		if urlsCompatible(instances[i].URL, baseURL) {
			return &instances[i]
		}
	}
	return nil
}

// urlsCompatible returns true if two tracker URLs refer to the same instance.
// It normalizes trailing slashes and compares case-insensitively.
func urlsCompatible(a, b string) bool {
	a = strings.TrimRight(strings.ToLower(a), "/")
	b = strings.TrimRight(strings.ToLower(b), "/")
	return a == b
}

// applyPolicyWrapper loads policy config and wraps the provider with a
// PolicyProvider if policies are defined. Errors are reported as warnings
// to errW; the original provider is returned unchanged on error. The ctx
// is consulted for the per-request project directory.
func applyPolicyWrapper(ctx context.Context, p tracker.Provider, instanceName string, errW io.Writer) tracker.Provider {
	cfg, err := tracker.LoadPolicyConfig(config.ResolveDirCtx(ctx, config.DirProject))
	if err != nil {
		_, _ = fmt.Fprintln(errW, "warning: policy config error:", err)
		return p
	}
	if cfg == nil {
		return p
	}
	policy := tracker.NewPolicy(*cfg)
	return tracker.NewPolicyProvider(p, instanceName, policy, func(msg string) {
		_, _ = fmt.Fprintln(errW, "policy: confirm:", msg)
	})
}

// cmdContext returns the cobra command's context, falling back to
// context.Background() when cmd is nil or has no context (legacy tests).
func cmdContext(cmd *cobra.Command) context.Context {
	if cmd == nil {
		return context.Background()
	}
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// loadInstancesCtx invokes deps.LoadInstances under the given context so
// that env-driven path resolution (config.DirProject) sees the
// per-request env map. Existing Deps users keep working unchanged: when
// the daemon installs a context-aware loader the env flows through;
// otherwise the legacy LoadAllInstances falls through to os.Getenv.
func loadInstancesCtx(ctx context.Context, deps Deps) ([]tracker.Instance, error) {
	if deps.LoadInstancesCtx != nil {
		return deps.LoadInstancesCtx(ctx, config.DirProject)
	}
	return deps.LoadInstances(config.DirProject)
}

// applyDestructiveWrapper wraps the provider with a DestructiveProvider for
// logging and notifying on destructive operations. If wiring fails, a warning
// is printed and the original provider is returned. The ctx is passed to the
// notifier loader so per-request env values reach the underlying config.
func applyDestructiveWrapper(ctx context.Context, p tracker.Provider, name, kind string, deps Deps, errW io.Writer) (tracker.Provider, func()) {
	if deps.DestructiveLogPath == nil {
		return p, func() {}
	}
	logPath := deps.DestructiveLogPath()

	var notifier tracker.DestructiveNotifier
	if deps.DestructiveNotifier != nil {
		notifier = deps.DestructiveNotifier(ctx)
	}

	dp, dpErr := tracker.NewDestructiveProvider(p, name, kind, logPath, notifier)
	if dpErr != nil {
		_, _ = fmt.Fprintln(errW, "warning: destructive logging disabled:", dpErr)
		return p, func() {}
	}
	return dp, func() { _ = dp.Close() }
}

// loadDestructiveNotifier builds a DestructiveNotifier from configured
// Telegram and Slack instances using the per-request project directory
// resolved from ctx. Returns nil if neither is configured.
func loadDestructiveNotifier(ctx context.Context) tracker.DestructiveNotifier {
	var notifiers []dispatch.Notifier
	var chatID int64

	dir := config.ResolveDirCtx(ctx, config.DirProject)

	// Load Telegram
	telegramInstances, _ := telegram.LoadInstances(dir)
	if len(telegramInstances) > 0 {
		configs, _ := telegram.LoadConfigs(dir)
		for _, cfg := range configs {
			if cfg.NotifyChatID != 0 {
				chatID = cfg.NotifyChatID
				// Find the matching instance
				for _, inst := range telegramInstances {
					if inst.Name == cfg.Name {
						notifiers = append(notifiers, &dispatch.TelegramNotifier{Client: inst.Client})
						break
					}
				}
				break
			}
		}
	}

	// Load Slack
	slackInstances, _ := slack.LoadInstances(dir)
	if len(slackInstances) > 0 {
		notifiers = append(notifiers, &dispatch.SlackNotifier{Client: slackInstances[0].Client})
	}

	if len(notifiers) == 0 {
		return nil
	}

	combined := &dispatch.CompositeNotifier{Notifiers: notifiers}
	return &DispatchDestructiveNotifier{Notifier: combined, ChatID: chatID}
}
