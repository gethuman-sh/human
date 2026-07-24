package agent

import (
	"context"
	"testing"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/botidentity"
)

// hasEnv reports whether the recorded ExecCreate env contains want.
func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func TestExecClaudeDetached_injectsBotGitEnv(t *testing.T) {
	withLogRoot(t)
	restore := botidentity.Load
	botidentity.Load = func(string) (botidentity.Identity, error) {
		return botidentity.Identity{Name: "acmebot", Email: "bot@acme.dev"}, nil
	}
	t.Cleanup(func() { botidentity.Load = restore })

	docker := &mockDockerClient{}
	mgr := &Manager{Docker: docker}
	t.Cleanup(mgr.teeWG.Wait)

	if _, err := mgr.execClaudeDetached(context.Background(), "cid", "vscode", "", "", StartOpts{Name: "a", Prompt: "P", DaemonID: "d1"}); err != nil {
		t.Fatalf("execClaudeDetached: %v", err)
	}

	if len(docker.execEnvs) != 1 {
		t.Fatalf("expected 1 ExecCreate call, got %d", len(docker.execEnvs))
	}
	env := docker.execEnvs[0]
	for _, want := range []string{
		"HUMAN_AGENT_NAME=a",
		"HUMAN_DAEMON_ID=d1",
		"GIT_AUTHOR_NAME=acmebot",
		"GIT_AUTHOR_EMAIL=bot@acme.dev",
		"GIT_COMMITTER_NAME=acmebot",
		"GIT_COMMITTER_EMAIL=bot@acme.dev",
	} {
		if !hasEnv(env, want) {
			t.Errorf("env missing %q; got %v", want, env)
		}
	}
}

func TestExecClaudeDetached_loadErrorFallsBackToDefault(t *testing.T) {
	withLogRoot(t)
	restore := botidentity.Load
	botidentity.Load = func(string) (botidentity.Identity, error) {
		return botidentity.Identity{}, errors.WithDetails("boom")
	}
	t.Cleanup(func() { botidentity.Load = restore })

	docker := &mockDockerClient{}
	mgr := &Manager{Docker: docker}
	t.Cleanup(mgr.teeWG.Wait)

	exe, err := mgr.execClaudeDetached(context.Background(), "cid", "vscode", "", "", StartOpts{Name: "a", Prompt: "P"})
	if err != nil {
		t.Fatalf("launch must not fail on load error: %v", err)
	}
	if exe == nil {
		t.Fatal("expected non-nil execution despite load error")
	}
	if len(docker.execEnvs) != 1 {
		t.Fatalf("expected 1 ExecCreate call, got %d", len(docker.execEnvs))
	}
	if !hasEnv(docker.execEnvs[0], "GIT_AUTHOR_NAME="+botidentity.DefaultName) {
		t.Errorf("expected default bot author on load error; got %v", docker.execEnvs[0])
	}
}
