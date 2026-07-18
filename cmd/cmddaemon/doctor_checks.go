package cmddaemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/devcontainer"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

// doctorPersistence captures which durable stores opened at startup. The
// values cannot change while the daemon runs, so the check closes over them.
type doctorPersistence struct {
	stats    bool
	audit    bool
	confirms bool
}

// buildDoctorChecks assembles the substrate probes for the preflight doctor.
// Every check is cheap and side-effect free; failure details name the fix,
// because they become the board LED tooltip and launch-refusal messages —
// infrastructure failures must be attributed to infrastructure, never to a
// ticket (SC-514; regressions 428/478 are the motivating incidents).
func buildDoctorChecks(reg *daemon.ProjectRegistry, resolver *vault.Resolver, persist doctorPersistence) []daemon.DoctorCheckDef {
	diagnose := trackerDiagnoserFunc(reg, resolver)
	return []daemon.DoctorCheckDef{
		{ID: "trackers", Name: "tracker credentials", Run: func(context.Context) (bool, string) {
			return checkTrackers(reg, diagnose)
		}},
		{ID: "docker", Name: "docker engine", Run: checkDocker},
		{ID: "ca-cert", Name: "proxy CA certificate", Run: func(context.Context) (bool, string) {
			return checkCACert(caCertPath())
		}},
		{ID: "agent-skills", Name: "agent skills", Run: func(context.Context) (bool, string) {
			return checkAgentSkills(reg)
		}},
		{ID: "persistence", Name: "daemon persistence", Run: func(context.Context) (bool, string) {
			return checkPersistence(persist)
		}},
	}
}

// caCertPath is the well-known proxy CA location the containers mount
// (devcontainer/manager.go binds the same path).
func caCertPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "ca.crt")
	}
	return filepath.Join(home, ".human", "ca.crt")
}

// checkTrackers fails when a configured tracker instance cannot load its
// credentials — the exact condition that silently drops the instance and
// leaves the board empty or PM-less (ticket 172's class).
func checkTrackers(reg *daemon.ProjectRegistry, diagnose func(dir string) []tracker.TrackerStatus) (bool, string) {
	var broken []string
	var total int
	for _, entry := range reg.Entries() {
		for _, st := range diagnose(entry.Dir) {
			total++
			if st.Working || st.VaultRef {
				continue
			}
			broken = append(broken, fmt.Sprintf("%s/%s (set %s)", st.Kind, st.Name, strings.Join(st.Missing, ", ")))
		}
	}
	if len(broken) > 0 {
		return false, "credentials unresolved for " + strings.Join(broken, "; ")
	}
	return true, fmt.Sprintf("%d instance(s) working", total)
}

// checkDocker mirrors the desktop's dockerAvailable probe: a cheap engine
// round-trip that fails fast when the socket is absent or the engine stopped.
func checkDocker(ctx context.Context) (bool, string) {
	dc, err := devcontainer.NewDockerClient()
	if err != nil {
		return false, "docker client unavailable: " + err.Error() + " — start Docker"
	}
	if _, err := dc.ImageList(ctx, devcontainer.ImageListOptions{}); err != nil {
		return false, "docker engine unreachable: " + err.Error() + " — start Docker"
	}
	return true, "engine reachable"
}

// checkCACert catches ticket 428's failure mode: a present-but-unparseable
// CA silently breaks in-container TLS and hook delivery for every agent. An
// absent file is fine — the daemon generates it on first proxy use.
func checkCACert(path string) (bool, string) {
	if _, err := os.Stat(path); err != nil {
		return true, "not yet generated"
	}
	if !devcontainer.IsValidCACertFile(path) {
		return false, path + " exists but is not a valid PEM certificate — delete it and restart the daemon to regenerate"
	}
	return true, "valid"
}

// checkAgentSkills catches ticket 478's failure mode: worktree provisioning
// copies the project's .claude, so a project missing its skills kills every
// board run at launch with an unknown slash command.
func checkAgentSkills(reg *daemon.ProjectRegistry) (bool, string) {
	var missing []string
	for _, entry := range reg.Entries() {
		skill := filepath.Join(entry.Dir, ".claude", "skills", "human-autofix", "SKILL.md")
		if _, err := os.Stat(skill); err != nil {
			missing = append(missing, entry.Dir)
		}
	}
	if len(missing) > 0 {
		return false, "no agent skills under " + strings.Join(missing, ", ") + " — run 'human install --agent claude' there"
	}
	return true, "skills present"
}

// checkPersistence reports durable stores that failed to open: the daemon
// runs, but stats/audit/approvals silently stopped surviving restarts.
func checkPersistence(p doctorPersistence) (bool, string) {
	var down []string
	if !p.stats {
		down = append(down, "stats")
	}
	if !p.audit {
		down = append(down, "audit")
	}
	if !p.confirms {
		down = append(down, "approvals")
	}
	if len(down) > 0 {
		return false, strings.Join(down, ", ") + " persistence disabled — check ~/.human/*.db permissions and the daemon start log"
	}
	return true, "all stores open"
}
