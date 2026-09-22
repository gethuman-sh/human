package local

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vieweridentity"
)

// Section is the legacy per-vendor YAML key; the unified `trackers:` list with
// `kind: local` is the shape new configs are written in.
const Section = "locals"

// Config is one local tracker entry. No token, no URL: there is nothing to
// authenticate against.
type Config struct {
	Name        string `mapstructure:"name"`
	Description string `mapstructure:"description"`
	Role        string `mapstructure:"role"`
	Safe        bool   `mapstructure:"safe"`
	// Prefix is the key prefix (LOC in LOC-12). Defaults to DefaultPrefix.
	Prefix string `mapstructure:"prefix"`
	// Path is the SQLite file. Relative paths are taken from the project
	// directory. Defaults to a file under ~/.human/local named after the project.
	Path string `mapstructure:"path"`
	// User is the name writes are attributed to. Defaults to the first `me:`
	// name in the same config, then to the OS user.
	User string `mapstructure:"user"`
	// Strict refuses marker posts the pipeline state machine does not allow.
	// Off by default; on for test fixtures and acceptance runs.
	Strict bool `mapstructure:"strict"`
}

// LoadConfigs reads the legacy section only; LoadInstances also reads the
// unified list.
func LoadConfigs(dir string) ([]Config, error) {
	var configs []Config
	if err := config.UnmarshalSection(dir, Section, &configs); err != nil {
		return nil, err
	}
	return configs, nil
}

// specFor builds the loader spec for one project directory. The directory is
// needed at build time — for the default database path and the `me:` name —
// which the generic spec cannot pass, so it is captured here.
func specFor(dir string) config.InstanceSpec[Config, tracker.Instance] {
	return config.InstanceSpec[Config, tracker.Instance]{
		Section:   Section,
		Kind:      Kind,
		EnvPrefix: "LOCAL_",
		GetName:   func(c Config) string { return c.Name },
		Build: func(cfg Config) (tracker.Instance, bool) {
			inst, err := buildInstance(dir, cfg)
			if err != nil {
				// The generic loader reports a skipped entry by name; the reason
				// is logged here because it is the only place that knows it.
				log.Warn().Err(err).Str("section", Section).Str("name", cfg.Name).Msg("local tracker entry skipped")
				return tracker.Instance{}, false
			}
			return inst, true
		},
	}
}

// buildInstance resolves defaults and opens the database. Opening at load time
// is deliberate: a path that cannot be created should fail `human tracker list`,
// not the first ticket write.
func buildInstance(dir string, cfg Config) (tracker.Instance, error) {
	prefix := strings.ToUpper(strings.TrimSpace(cfg.Prefix))
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if err := ValidatePrefix(prefix); err != nil {
		return tracker.Instance{}, err
	}
	path, err := resolvePath(dir, cfg)
	if err != nil {
		return tracker.Instance{}, err
	}
	client, err := sharedClient(path, prefix, resolveUser(dir, cfg), cfg.Strict)
	if err != nil {
		return tracker.Instance{}, err
	}
	return tracker.Instance{
		Name:        cfg.Name,
		Kind:        Kind,
		URL:         path,
		User:        client.user,
		Description: cfg.Description,
		Role:        cfg.Role,
		Safe:        cfg.Safe,
		// The prefix is the tracker's one project: it scopes the board and it
		// is the filing target, so `human bug create` and the board's capture
		// never land a ticket nowhere (SC-1959).
		Projects: []string{prefix},
		CreateIn: prefix,
		Provider: client,
	}, nil
}

// resolvePath picks the database file. An explicit relative path is relative to
// the project, so a config can keep its tickets beside the code if it wants to.
func resolvePath(dir string, cfg Config) (string, error) {
	if p := strings.TrimSpace(cfg.Path); p != "" {
		if filepath.IsAbs(p) {
			return p, nil
		}
		return filepath.Join(dir, p), nil
	}
	return DefaultDBPath(dir, cfg.Name)
}

// DefaultDBPath is ~/.human/local/<project>-<name>-<dirhash>.db. The hash of the
// absolute project directory keeps two checkouts with the same project name
// from sharing tickets; the readable parts are for the person listing the dir.
func DefaultDBPath(dir, name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.WrapWithDetails(err, "resolving home directory for the local tracker")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", errors.WrapWithDetails(err, "resolving project directory for the local tracker", "dir", dir)
	}
	sum := sha256.Sum256([]byte(abs))
	project := config.ReadProjectName(abs)
	if project == "" {
		project = filepath.Base(abs)
	}
	file := slug(project)
	if n := slug(name); n != "" && n != file {
		file += "-" + n
	}
	return filepath.Join(home, ".human", "local", file+"-"+hex.EncodeToString(sum[:4])+".db"), nil
}

// slug keeps a name safe as a file-name fragment.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// resolveUser prefers the config's own `user:`, then the first `me:` name so
// the board's ownership match sees local tickets as the viewer's, then the OS
// account, and finally a fixed word rather than an empty author on every comment.
func resolveUser(dir string, cfg Config) string {
	if u := strings.TrimSpace(cfg.User); u != "" {
		return u
	}
	if id, err := vieweridentity.Load(dir); err == nil && len(id.Names) > 0 {
		return id.Names[0]
	}
	if u, err := user.Current(); err == nil && strings.TrimSpace(u.Username) != "" {
		return u.Username
	}
	return "me"
}

// LoadInstances reads the local tracker entries for a project directory.
func LoadInstances(dir string) ([]tracker.Instance, error) {
	return config.LoadInstances(dir, specFor(dir))
}

// LoadInstancesWithLookup mirrors the other providers' signature; a local
// tracker has no credentials, so the lookup only serves the shared loader.
func LoadInstancesWithLookup(dir string, lookup config.EnvLookup) ([]tracker.Instance, error) {
	spec := specFor(dir)
	spec.Lookup = lookup
	return config.LoadInstances(dir, spec)
}

// LoadInstancesWithResolver mirrors the other providers' signature. There are no
// secrets to resolve; the resolver is accepted so the loader list stays uniform.
func LoadInstancesWithResolver(dir string, lookup config.EnvLookup, resolver config.SecretResolveFunc) ([]tracker.Instance, error) {
	spec := specFor(dir)
	spec.Lookup = lookup
	spec.SecretResolver = resolver
	return config.LoadInstances(dir, spec)
}

// clients holds one open Client per database path. Instances are rebuilt on
// every CLI call and every board refresh; opening a fresh connection each time
// would leak a file handle per refresh for as long as the daemon runs.
var (
	clientsMu sync.Mutex
	clients   = map[string]*Client{}
)

// sharedClient returns the cached client for path, opening it on first use.
// A second entry pointing the same file at a different prefix is refused: the
// keys already in the file are the first prefix's, and rereading them under
// another would silently produce keys nothing else recognises.
func sharedClient(path, prefix, user string, strict bool) (*Client, error) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[path]; ok {
		if c.prefix != prefix {
			return nil, errors.WithDetails("local tracker database is already open under another prefix",
				"path", path, "open", c.prefix, "requested", prefix)
		}
		c.user = user
		c.strict = strict
		return c, nil
	}
	var opts []Option
	if strict {
		opts = append(opts, Strict())
	}
	c, err := Open(path, prefix, user, opts...)
	if err != nil {
		return nil, err
	}
	clients[path] = c
	return c, nil
}

// resetShared drops the cache so tests can reopen a path with fresh state.
func resetShared() {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
	clients = map[string]*Client{}
}
