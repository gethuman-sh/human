package cmdtui

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// stubLooker is an injectable pathLooker: a nil err reports tmux as installed.
type stubLooker struct{ err error }

func (s stubLooker) LookPath(string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return "/usr/bin/tmux", nil
}

func TestDetectTmux_notInstalled_darwin(t *testing.T) {
	st := detectTmux(stubLooker{err: exec.ErrNotFound}, "", "darwin")
	assert.False(t, st.installed)
	assert.False(t, st.inSession)
	assert.Equal(t, "brew install tmux", st.installCmd)
	assert.False(t, st.ok())
}

func TestDetectTmux_installedNoSession(t *testing.T) {
	st := detectTmux(stubLooker{}, "", "darwin")
	assert.True(t, st.installed)
	assert.False(t, st.inSession)
	assert.Empty(t, st.installCmd)
	assert.False(t, st.ok())
	assert.Contains(t, st.guidance(), relaunchCommand)
}

func TestDetectTmux_installedInSession(t *testing.T) {
	st := detectTmux(stubLooker{}, "/tmp/tmux-1000/default,1,0", "linux")
	assert.True(t, st.ok())
	assert.Empty(t, st.guidance())
}

func TestTmuxInstallCommand_linux(t *testing.T) {
	// On darwin/CI /proc/version is absent, so IsWSL() is false.
	assert.Contains(t, tmuxInstallCommand("linux"), "apt-get install tmux")
}

func TestTmuxInstallCommand_unknown(t *testing.T) {
	assert.Contains(t, tmuxInstallCommand("plan9"), "tmux/tmux/wiki/Installing")
}

func TestTmuxState_guidance_notInstalled(t *testing.T) {
	st := tmuxState{installed: false, installCmd: "brew install tmux"}
	assert.Equal(t, "tmux not installed — run: brew install tmux", st.guidance())
}

func TestTmuxState_guidance_noSession(t *testing.T) {
	st := tmuxState{installed: true, inSession: false, relaunchCmd: relaunchCommand}
	assert.Equal(t, "not in a tmux session — relaunch: "+relaunchCommand, st.guidance())
}

func TestTmuxState_guidance_ok(t *testing.T) {
	st := tmuxState{installed: true, inSession: true}
	assert.Empty(t, st.guidance())
}

func TestOSPathLooker_LookPath(t *testing.T) {
	// Exercise the real looker against a binary that is virtually always present
	// so the concrete implementation is covered without asserting a fixed path.
	_, err := osPathLooker{}.LookPath("go")
	assert.NoError(t, err)
	_, err = osPathLooker{}.LookPath("definitely-not-a-real-binary-xyz")
	assert.Error(t, err)
}

func TestRenderTmuxBanner_notInstalled(t *testing.T) {
	out := renderTmuxBanner(tmuxState{installed: false, installCmd: "brew install tmux"})
	assert.Contains(t, out, "not installed")
	assert.Contains(t, out, "brew install tmux")
}

func TestRenderTmuxBanner_noSession(t *testing.T) {
	out := renderTmuxBanner(tmuxState{installed: true, relaunchCmd: relaunchCommand})
	assert.Contains(t, out, "tmux session")
	assert.Contains(t, out, relaunchCommand)
}

func TestRenderTmuxBanner_ok(t *testing.T) {
	assert.Empty(t, renderTmuxBanner(tmuxState{installed: true, inSession: true}))
}

func TestRenderFooter_tmuxAnnotation(t *testing.T) {
	out := renderFooter(80, "", "", false, false)
	assert.Contains(t, out, "need tmux")
}

func TestRenderFooter_tmuxOK_noAnnotation(t *testing.T) {
	out := renderFooter(80, "", "", false, true)
	assert.False(t, strings.Contains(out, "need tmux"))
}
