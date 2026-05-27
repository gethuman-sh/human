# Tracker Architecture

## Dependency Graph

```
main.go ──→ tracker     (Provider, Instance, Resolve)
main.go ──→ jira        ──→ tracker  (Instance)
                        ──→ config   (UnmarshalSection)
main.go ──→ github      ──→ tracker  (Instance)
                        ──→ config   (UnmarshalSection)
main.go ──→ gitlab      ──→ tracker  (Instance)
                        ──→ config   (UnmarshalSection)
main.go ──→ linear      ──→ tracker  (Instance)
                        ──→ config   (UnmarshalSection)
main.go ──→ azuredevops ──→ tracker  (Instance)
                        ──→ config   (UnmarshalSection)
main.go ──→ shortcut    ──→ tracker  (Instance)
                        ──→ config   (UnmarshalSection)
```

`config` is a leaf package — no tracker types, no provider knowledge.

## Adding a New Tracker

### 1. Create `internal/<provider>/`

**`client.go`** — implement `tracker.Provider`:

```go
package linear

import "github.com/gethuman-sh/human/internal/tracker"

var _ tracker.Provider = (*Client)(nil)

type Client struct { /* auth fields */ }

func New(url, token string) *Client { return &Client{...} }

func (c *Client) ListIssues(ctx context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) { ... }
func (c *Client) GetIssue(ctx context.Context, key string) (*tracker.Issue, error) { ... }
func (c *Client) CreateIssue(ctx context.Context, issue *tracker.Issue) (*tracker.Issue, error) { ... }
```

**`config.go`** — config type + `LoadInstances`:

```go
type Config struct {
    Name  string `mapstructure:"name"`
    URL   string `mapstructure:"url"`
    Token string `mapstructure:"token"`
}

func LoadConfigs(dir string) ([]Config, error) {
    var configs []Config
    if err := config.UnmarshalSection(dir, "linears", &configs); err != nil {
        return nil, err
    }
    return configs, nil
}

func LoadInstances(dir string) ([]tracker.Instance, error) {
    configs, err := LoadConfigs(dir)
    // for each config: applyEnvOverrides → applyGlobalEnvOverrides → New() → tracker.Instance
}

func applyEnvOverrides(cfg *Config)       { /* LINEAR_<NAME>_TOKEN etc. */ }
func applyGlobalEnvOverrides(cfg *Config)  { /* LINEAR_TOKEN etc. */ }
```

### 2. Wire into `main.go`

Add one call in `loadAllInstances`:

```go
func loadAllInstances(dir string) ([]tracker.Instance, error) {
    // ... existing jira + github calls ...
    li, err := linear.LoadInstances(dir)
    if err != nil { return nil, err }
    return append(all, li...), nil
}
```

Add a CLI-flag branch in `instanceFromCLI` if the provider supports no-config onboarding.

### 3. Config file format

Users add entries under a top-level key (e.g. `linears:`):

```yaml
linears:
  - name: work
    url: https://api.linear.app
    token: lin_abc
```

### 4. Env override convention

- **Per-instance**: `LINEAR_<UPPER(name)>_TOKEN` overrides the config file value
- **Global**: `LINEAR_TOKEN` overrides everything (applied after per-instance)
- Priority: global env > instance env > config file

### 5. Tests

- `config_test.go`: `TestLoadConfigs`, `TestApplyEnvOverrides`, `TestLoadInstances_*` (happy path, multiple entries, missing file, env priority, incomplete config)
- Compile-time check: `var _ tracker.Provider = (*Client)(nil)` in `client.go`
