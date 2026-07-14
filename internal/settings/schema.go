// Package settings exposes the .humanconfig configuration surface as a
// uniform, addressable set of leaves so UI surfaces (desktop settings view,
// command palette) can render and edit any setting without knowing provider
// packages. The hand-written registry below is the single source of truth:
// it must list exactly the mapstructure keys the provider Config structs
// declare, because reads flatten what viper parses and writes address the
// same keys in the YAML file.
//
// The package deliberately imports no provider packages — sections are read
// generically — so the tracker abstraction layer stays provider-agnostic.
package settings

// FieldType classifies a leaf for form rendering and value validation.
type FieldType string

const (
	TypeString     FieldType = "string"
	TypeSecret     FieldType = "secret" // masked on read, write-only
	TypeBool       FieldType = "bool"
	TypeStringList FieldType = "stringlist"
	TypeIntList    FieldType = "intlist"
	TypeInt        FieldType = "int"
	TypeEnum       FieldType = "enum"
)

// Field describes one editable leaf of a config entry.
type Field struct {
	Key             string // YAML/mapstructure key, e.g. "token"
	Label           string
	Type            FieldType
	Enum            []string // valid values for TypeEnum
	RestartRequired bool     // change only takes effect after a daemon restart
	Description     string   // drives search/palette matching
}

// Group is one YAML section: a named-instance list (linears, jiras, …), a
// singleton mapping (vault, proxy, …), or a top-level scalar (project).
type Group struct {
	Section string // YAML key: "linears", "vault", "project"
	Label   string // "Linear"
	Kind    string // provider kind for display badges; "" for non-providers
	IsList  bool
	Scalar  bool // top-level scalar — Fields has exactly one pseudo-field
	Fields  []Field
}

// FieldByKey returns the group's field with the given key.
func (g Group) FieldByKey(key string) (Field, bool) {
	for _, f := range g.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// SectionDef is one sidebar section of the settings view.
type SectionDef struct {
	Key    string // "trackers"
	Label  string // "Trackers"
	Groups []Group
}

// Registry returns the full settings schema in UI order. The returned slice
// is shared package state — callers must treat it as read-only.
func Registry() []SectionDef {
	return registry
}

var registry = buildRegistry()

// trackerFields assembles the common tracker instance shape around the
// provider-specific credential/extra fields, keeping the seven tracker
// sections in lockstep with the provider Config structs.
func trackerFields(credentials []Field, extra ...Field) []Field {
	fields := []Field{
		{Key: "name", Label: "Name", Type: TypeString, Description: "Instance name — identity for env vars and settings paths"},
		{Key: "url", Label: "URL", Type: TypeString, Description: "API base URL"},
	}
	fields = append(fields, credentials...)
	fields = append(fields, extra...)
	fields = append(fields,
		Field{Key: "description", Label: "Description", Type: TypeString, Description: "What this tracker is used for"},
		Field{Key: "role", Label: "Role", Type: TypeEnum, Enum: []string{"pm", "engineering"}, Description: "Topology role: pm or engineering — distinct trackers per role keep separate engineering tickets; a single tracker carries the whole ticket lifecycle"},
		Field{Key: "safe", Label: "Safe mode", Type: TypeBool, Description: "Block destructive operations (deletes)"},
		Field{Key: "projects", Label: "Projects", Type: TypeStringList, Description: "Project keys to index"},
	)
	return fields
}

func tokenField() []Field {
	return []Field{{Key: "token", Label: "API token", Type: TypeSecret, Description: "API token — literal or 1pw:// vault reference"}}
}

func buildRegistry() []SectionDef {
	return []SectionDef{
		{
			Key: "project", Label: "Project",
			Groups: []Group{{
				Section: "project", Label: "Project", Scalar: true,
				Fields: []Field{{
					Key: "project", Label: "Project name", Type: TypeString, RestartRequired: true,
					Description: "Project name used by the daemon's project registry",
				}},
			}},
		},
		{
			Key: "trackers", Label: "Trackers",
			Groups: []Group{
				{Section: "jiras", Label: "Jira", Kind: "jira", IsList: true, Fields: trackerFields([]Field{
					{Key: "user", Label: "User", Type: TypeString, Description: "Jira account email"},
					{Key: "key", Label: "API key", Type: TypeSecret, Description: "API key — literal or 1pw:// vault reference"},
				})},
				{Section: "githubs", Label: "GitHub", Kind: "github", IsList: true, Fields: trackerFields(tokenField())},
				{Section: "gitlabs", Label: "GitLab", Kind: "gitlab", IsList: true, Fields: trackerFields(tokenField())},
				{Section: "linears", Label: "Linear", Kind: "linear", IsList: true, Fields: trackerFields(tokenField())},
				{Section: "shortcuts", Label: "Shortcut", Kind: "shortcut", IsList: true, Fields: trackerFields(tokenField())},
				{Section: "azuredevops", Label: "Azure DevOps", Kind: "azuredevops", IsList: true, Fields: trackerFields(tokenField(),
					Field{Key: "org", Label: "Organization", Type: TypeString, Description: "Azure DevOps organization"})},
				{Section: "clickups", Label: "ClickUp", Kind: "clickup", IsList: true, Fields: trackerFields(tokenField(),
					Field{Key: "team_id", Label: "Team ID", Type: TypeString, Description: "ClickUp team (workspace) id"})},
			},
		},
		{
			Key: "knowledge", Label: "Knowledge",
			Groups: []Group{
				{Section: "notions", Label: "Notion", Kind: "notion", IsList: true, Fields: knowledgeFields(tokenField())},
				{Section: "figmas", Label: "Figma", Kind: "figma", IsList: true, Fields: knowledgeFields(tokenField())},
				{Section: "amplitudes", Label: "Amplitude", Kind: "amplitude", IsList: true, Fields: knowledgeFields([]Field{
					{Key: "key", Label: "API key", Type: TypeSecret, Description: "API key — literal or 1pw:// vault reference"},
					{Key: "secret", Label: "API secret", Type: TypeSecret, Description: "API secret — literal or 1pw:// vault reference"},
				})},
			},
		},
		{
			Key: "messaging", Label: "Messaging",
			Groups: []Group{
				{Section: "slacks", Label: "Slack", Kind: "slack", IsList: true, Fields: []Field{
					{Key: "name", Label: "Name", Type: TypeString, Description: "Instance name"},
					{Key: "token", Label: "Bot token", Type: TypeSecret, Description: "Bot token — literal or 1pw:// vault reference"},
					{Key: "channel", Label: "Channel", Type: TypeString, Description: "Default channel"},
					{Key: "description", Label: "Description", Type: TypeString},
				}},
				{Section: "telegrams", Label: "Telegram", Kind: "telegram", IsList: true, Fields: []Field{
					{Key: "name", Label: "Name", Type: TypeString, Description: "Instance name"},
					{Key: "token", Label: "Bot token", Type: TypeSecret, Description: "Bot token — literal or 1pw:// vault reference"},
					{Key: "description", Label: "Description", Type: TypeString},
					{Key: "allowed_users", Label: "Allowed users", Type: TypeIntList, Description: "Telegram user ids allowed to talk to the bot"},
					{Key: "allowed_chats", Label: "Allowed chats", Type: TypeIntList, Description: "Group chat ids allowed to dispatch messages"},
					{Key: "notify_chat_id", Label: "Notify chat", Type: TypeInt, Description: "Chat id for proactive notifications"},
				}},
			},
		},
		{
			Key: "vault", Label: "Vault",
			Groups: []Group{{
				Section: "vault", Label: "Vault",
				Fields: []Field{
					{Key: "provider", Label: "Provider", Type: TypeEnum, Enum: []string{"1password", "1pw"}, RestartRequired: true,
						Description: "Secret vault provider resolving 1pw:// references"},
					{Key: "account", Label: "Account", Type: TypeString, RestartRequired: true,
						Description: "Vault account name (1Password sidebar name)"},
				},
			}},
		},
		{
			Key: "daemon", Label: "Daemon",
			Groups: []Group{
				{Section: "proxy", Label: "Proxy", Fields: []Field{
					{Key: "mode", Label: "Mode", Type: TypeEnum, Enum: []string{"allowlist", "blocklist"}, Description: "Network proxy policy mode"},
					{Key: "domains", Label: "Domains", Type: TypeStringList, Description: "Domains the proxy policy applies to"},
					{Key: "intercept", Label: "Intercept", Type: TypeStringList, Description: "Domains to MITM for traffic logging"},
				}},
				{Section: "policies", Label: "Policies", Fields: []Field{
					{Key: "block", Label: "Block", Type: TypeStringList, Description: "Tracker operations to block"},
					{Key: "confirm", Label: "Confirm", Type: TypeStringList, Description: "Tracker operations requiring confirmation"},
				}},
				{Section: "devcontainer", Label: "Devcontainer", Fields: []Field{
					{Key: "configdir", Label: "Config dir", Type: TypeString, Description: "Devcontainer config directory"},
				}},
			},
		},
	}
}

// knowledgeFields is the shared shape of knowledge connector instances.
func knowledgeFields(credentials []Field) []Field {
	fields := []Field{
		{Key: "name", Label: "Name", Type: TypeString, Description: "Instance name"},
		{Key: "url", Label: "URL", Type: TypeString, Description: "API base URL"},
	}
	fields = append(fields, credentials...)
	fields = append(fields, Field{Key: "description", Label: "Description", Type: TypeString})
	return fields
}
