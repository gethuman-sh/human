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
		docker, err := devcontainer.NewDockerClient()
		if err != nil {
			logger.Warn().Err(err).Msg("agent prune: connecting to docker")
			return
		}
		defer func() { _ = docker.Close() }()
		n, removed, err := pruneAgentDebrisWith(ctx, docker, time.Now(), agent.StoppedMetaRetention)
		if err != nil {
			logger.Warn().Err(err).Msg("agent prune")
		}
		if n > 0 {
			logger.Info().Int("containers", n).Msg("agent prune: removed exited agent containers with no running record")
		}
		if len(removed) > 0 {
			logger.Info().Strs("agents", removed).Msg("agent prune: retired records of agents that ended over a week ago")
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

// pruneAgentDebrisWith runs both halves of the prune in the order that keeps
// preservation possible: containers before metas. pruneExitedAgentContainersWith
// reads an agent's meta to find what to preserve, and PruneStoppedMetas is
// what makes that meta disappear once it is old enough. Running the meta
// prune first retired the record of aged debris (no prune pass for longer
// than retention) before the container prune ever saw it, so
// agent.ReadMeta failed, PreserveExecutionArtifacts was skipped, and the
// container was destroyed with no copy of its transcript ever made (SC-5248
// review finding). It takes the DockerClient directly so a test can assert
// the same ordering against a fake.
func pruneAgentDebrisWith(ctx context.Context, docker devcontainer.DockerClient, now time.Time, retention time.Duration) (int, []string, error) {
	containersRemoved, containerErr := pruneExitedAgentContainersWith(ctx, docker)
	metasRemoved, metaErr := agent.PruneStoppedMetas(now, retention)
	if containerErr != nil {
		return containersRemoved, metasRemoved, containerErr
	}
	return containersRemoved, metasRemoved, metaErr
}

// pruneExitedAgentContainersWith takes the DockerClient rather than building
// one, so a fake can assert both the removal and the preservation call.
func pruneExitedAgentContainersWith(ctx context.Context, docker devcontainer.DockerClient) (int, error) {
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
		// The stop paths all persist the transcript and outcome before they
		// remove the container (agent.PreserveExecutionArtifacts, the one
		// choke point every remove path funnels through). This is debris
		// precisely because no stop path reached it, so it is this prune's
		// job to go through the same choke point rather than destroy the
		// only copy of a crashed run's transcript. A meta that is already
		// gone (deleted, or never written) has nothing left to preserve.
		if name, ok := containerAgentName(c); ok {
			if meta, err := agent.ReadMeta(name); err == nil {
				agent.PreserveExecutionArtifacts(ctx, docker, meta)
			}
		}
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
