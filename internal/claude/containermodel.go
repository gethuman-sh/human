package claude

import (
	"strings"

	"github.com/gethuman-sh/human/internal/config"
)

// ContainerModel returns the top-level model an agent container for the project
// at dir should run, or "" to leave the account default in force.
//
// An unrecognised value yields "" rather than being passed through: a typo in
// one config line would otherwise break EVERY container launch for the project,
// and "unchanged behaviour" is the failure mode this setting is specified to
// have when it is absent. AgentModelProblem reports the typo where a person
// looks for it (SC-5474).
func ContainerModel(dir string) string {
	model := strings.ToLower(strings.TrimSpace(config.AgentModel(dir)))
	if model == "" || !IsTaskModelAlias(model) {
		return ""
	}
	return model
}

// AgentModelProblem reports an agent.model value this binary does not recognise.
// ok is false when the setting is absent or valid.
//
// It lives here rather than in Document.Validate because the rule needs the
// model card's vocabulary and internal/config may not import it — config is
// imported by botidentity, which this package imports.
func AgentModelProblem(dir string) (config.Problem, bool) {
	raw := strings.TrimSpace(config.AgentModel(dir))
	if raw == "" || IsTaskModelAlias(raw) {
		return config.Problem{}, false
	}
	return config.Problem{
		Severity: config.Warning,
		Rule:     "unknown-agent-model",
		Section:  "agent",
		Message: "agent.model is " + raw + ", which is not a model this binary knows, so every container runs the account default instead — " +
			"the setting has no effect and nothing else reports that.",
		Fix: "set agent.model to one of " + strings.Join(taskModelAliases(), ", "),
	}, true
}
