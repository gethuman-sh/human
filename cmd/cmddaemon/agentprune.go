package cmddaemon

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/devcontainer"
)

// agentPruneInterval paces the prune. The debris it removes accrues over
// days, so an hour is plenty; the first pass runs at start so a daemon that
// was down for weeks cleans up before anyone looks.
const agentPruneInterval = time.Hour

// runAgentPrune retires what the stop paths leave behind: agent records that
// ended before StoppedMetaRetention, and agent containers that exited with
// no running record — a run killed with the daemon, or torn down halfway.
// Neither is reaped by anything else: the zombie sweep lists running records
// only, and every stop path is best-effort about the container (SC-5248).
func runAgentPrune(ctx context.Context, logger zerolog.Logger) {
	prune := func() {
		removed, err := agent.PruneStoppedMetas(time.Now(), agent.StoppedMetaRetention)
		if err != nil {
			logger.Warn().Err(err).Msg("agent prune: retiring stopped records")
		}
		if len(removed) > 0 {
			logger.Info().Strs("agents", removed).Msg("agent prune: retired records of agents that ended over a week ago")
		}
		n, err := pruneExitedAgentContainers(ctx)
		if err != nil {
			logger.Warn().Err(err).Msg("agent prune: removing exited containers")
		}
		if n > 0 {
			logger.Info().Int("containers", n).Msg("agent prune: removed exited agent containers with no running record")
		}
	}
	prune()
	ticker := time.NewTicker(agentPruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

func pruneExitedAgentContainers(ctx context.Context) (int, error) {
	docker, err := devcontainer.NewDockerClient()
	if err != nil {
		return 0, err
	}
	defer func() { _ = docker.Close() }()
	containers, err := docker.ContainerList(ctx, devcontainer.ContainerListOptions{All: true, NameFilter: agent.ContainerPrefix})
	if err != nil {
		return 0, err
	}
	metas, err := agent.ListMetas()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, c := range exitedAgentDebris(containers, metas) {
		// A container the normal stop path is removing at this moment is gone
		// by the time we get to it; that is the outcome wanted, not an error.
		if err := docker.ContainerRemove(ctx, c.ID, devcontainer.ContainerRemoveOptions{}); err != nil {
			continue
		}
		removed++
	}
	return removed, nil
}

// exitedAgentDebris is the pure half of the decision: an agent container that
// has exited and whose agent is not recorded as running. A running record
// means the stop path has not run yet and owns the container; a stopped
// record or none at all means every path that could remove it already ran,
// and did not.
func exitedAgentDebris(containers []devcontainer.ContainerSummary, metas []agent.Meta) []devcontainer.ContainerSummary {
	running := map[string]bool{}
	for _, m := range metas {
		if m.Status == agent.StatusRunning {
			running[m.Name] = true
		}
	}
	var debris []devcontainer.ContainerSummary
	for _, c := range containers {
		if c.State != "exited" {
			continue
		}
		name, ok := containerAgentName(c)
		if !ok || running[name] {
			continue
		}
		debris = append(debris, c)
	}
	return debris
}

// containerAgentName maps a container back to its agent name, or reports that
// the container is not one of ours — the name filter is a substring match.
func containerAgentName(c devcontainer.ContainerSummary) (string, bool) {
	for _, n := range c.Names {
		n = strings.TrimPrefix(n, "/")
		if rest, ok := strings.CutPrefix(n, agent.ContainerPrefix); ok && rest != "" {
			return rest, true
		}
	}
	return "", false
}
