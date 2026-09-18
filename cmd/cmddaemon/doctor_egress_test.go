package cmddaemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
)

// redirectingDevcontainer writes a devcontainer.json into dir that turns the
// human feature's proxy redirect on, so the project's containers are gated on
// the daemon's egress policy.
func redirectingDevcontainer(t *testing.T, dir string) {
	t.Helper()
	dcDir := filepath.Join(dir, ".devcontainer")
	require.NoError(t, os.MkdirAll(dcDir, 0o755))
	body := `{"features":{"ghcr.io/gethuman-sh/treehouse/human:1":{"proxy":true}}}`
	require.NoError(t, os.WriteFile(filepath.Join(dcDir, "devcontainer.json"), []byte(body), 0o600))
}

func TestCheckEgress_allowedByTheProjectPolicy(t *testing.T) {
	dir := writeProjectConfig(t, allowlistConfig)
	redirectingDevcontainer(t, dir)
	t.Chdir(t.TempDir())

	ok, detail := checkEgress(registryFor(t, dir))
	assert.True(t, ok)
	assert.Contains(t, detail, "api.anthropic.com allowed")
}

// The state the daemon was actually in: block-all, containers redirected
// through the proxy, every model call dropped at the SNI stage.
func TestCheckEgress_blockAllWithRedirectedContainersIsRed(t *testing.T) {
	dir := writeProjectConfig(t, "project: noproxy\n")
	redirectingDevcontainer(t, dir)

	ok, detail := checkEgress(registryFor(t, dir))
	assert.False(t, ok)
	assert.Contains(t, detail, "api.anthropic.com")
	assert.Contains(t, detail, dir)
}

// A policy that lists domains but not the model API is the same failure with a
// different cause, and the detail must name the remedy rather than the reason
// for a block-all.
func TestCheckEgress_policyWithoutTheModelHostIsRed(t *testing.T) {
	dir := writeProjectConfig(t, "proxy:\n  mode: allowlist\n  domains: [github.com]\n")
	redirectingDevcontainer(t, dir)

	ok, detail := checkEgress(registryFor(t, dir))
	assert.False(t, ok)
	assert.Contains(t, detail, "proxy.domains")
}

// The check refuses launches, so it must not blame a host whose containers do
// not go through the proxy at all: there the daemon's policy is irrelevant.
func TestCheckEgress_noRedirectMeansThePolicyIsNotEvidence(t *testing.T) {
	dir := writeProjectConfig(t, "project: noproxy\n")

	ok, detail := checkEgress(registryFor(t, dir))
	assert.True(t, ok)
	assert.Contains(t, detail, "no project redirects")
}

func TestCheckEgress_malformedConfigIsRed(t *testing.T) {
	dir := writeProjectConfig(t, "proxy:\n  mode: [broken\n")
	redirectingDevcontainer(t, dir)

	ok, detail := checkEgress(registryFor(t, dir))
	assert.False(t, ok)
	assert.Contains(t, detail, "proxy policy cannot be loaded")
}

// Gating is the decision of record: a daemon that cannot reach the model API
// must leave board work for a healthy one instead of burning a stage's budget.
func TestBuildDoctorChecks_egressIsGating(t *testing.T) {
	assert.True(t, slices.Contains(daemon.LaunchCriticalChecks, "egress"))

	reg, err := daemon.NewProjectRegistry([]string{t.TempDir()})
	require.NoError(t, err)
	egress := findCheck(t, buildDoctorChecks(reg, nil, doctorPersistence{}, nil), "egress")
	assert.True(t, egress.Gating)
	assert.False(t, egress.Transient, "a local policy read cannot blip")
}
