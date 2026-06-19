package devcontainer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

// RunHook executes a lifecycle command inside a container.
// The cmd parameter follows the devcontainer.json spec:
//   - string: run via /bin/sh -c "<string>"
//   - []interface{}: run as a direct command (each element is a string arg)
//   - map[string]interface{}: run each value in parallel (keys are labels)
//
// Returns nil if cmd is nil (hook not defined).
func RunHook(ctx context.Context, docker DockerClient, containerID, user string, cmd interface{}, logger zerolog.Logger) error {
	if cmd == nil {
		return nil
	}

	switch v := cmd.(type) {
	case string:
		if v == "" {
			return nil
		}
		return execInContainer(ctx, docker, containerID, user, []string{"/bin/sh", "-c", v}, nil, logger)

	case []interface{}:
		args := make([]string, 0, len(v))
		for _, a := range v {
			s, ok := a.(string)
			if !ok {
				return errors.WithDetails("lifecycle command array must contain strings",
					"got", fmt.Sprintf("%T", a))
			}
			args = append(args, s)
		}
		if len(args) == 0 {
			return nil
		}
		return execInContainer(ctx, docker, containerID, user, args, nil, logger)

	case map[string]interface{}:
		return runParallelHooks(ctx, docker, containerID, user, v, logger)

	default:
		return errors.WithDetails("unsupported lifecycle command type",
			"type", fmt.Sprintf("%T", cmd))
	}
}

// runParallelHooks executes multiple named commands in parallel, as specified
// by the devcontainer.json object-form lifecycle commands.
func runParallelHooks(ctx context.Context, docker DockerClient, containerID, user string, cmds map[string]interface{}, logger zerolog.Logger) error {
	var mu sync.Mutex
	var errs []error
	var wg sync.WaitGroup

	for name, cmd := range cmds {
		wg.Add(1)
		go func(name string, cmd interface{}) {
			defer wg.Done()
			logger.Info().Str("hook", name).Msg("running parallel hook")
			if err := RunHook(ctx, docker, containerID, user, cmd, logger); err != nil {
				mu.Lock()
				errs = append(errs, errors.WrapWithDetails(err, "hook failed", "name", name))
				mu.Unlock()
			}
		}(name, cmd)
	}

	wg.Wait()

	if len(errs) > 0 {
		// Return first error with count.
		return errors.WrapWithDetails(errs[0], "parallel hooks failed",
			"total_errors", len(errs))
	}
	return nil
}

// execInContainer runs a command inside a container and waits for completion.
func execInContainer(ctx context.Context, docker DockerClient, containerID, user string, cmd, env []string, logger zerolog.Logger) error {
	_, err := execCapture(ctx, docker, containerID, user, cmd, env, logger)
	return err
}

// execCapture runs cmd in the container and returns its stdout. It is the core
// used by execInContainer; callers that need the command's output (e.g. reading
// the container environment) use it directly. On a non-zero exit it returns an
// error carrying the exit code plus both streams — feature install.sh scripts
// frequently print their fatal reason to stdout, so stdout must be surfaced too.
func execCapture(ctx context.Context, docker DockerClient, containerID, user string, cmd, env []string, logger zerolog.Logger) (string, error) {
	logger.Info().Strs("cmd", cmd).Str("user", user).Msg("exec in container")

	execID, err := docker.ExecCreate(ctx, containerID, cmd, ExecOptions{
		User:         user,
		Env:          env,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", errors.WrapWithDetails(err, "creating exec", "cmd", fmt.Sprint(cmd))
	}

	attach, err := docker.ExecAttach(ctx, execID)
	if err != nil {
		return "", errors.WrapWithDetails(err, "attaching to exec")
	}
	defer func() { _ = attach.Close() }()

	// Drain output to avoid blocking the exec.
	var stdout, stderr bytes.Buffer
	_, _ = StdCopy(&stdout, &stderr, attach.Reader)

	if stdout.Len() > 0 {
		logger.Debug().Str("stdout", stdout.String()).Msg("exec output")
	}
	if stderr.Len() > 0 {
		logger.Debug().Str("stderr", stderr.String()).Msg("exec stderr")
	}

	inspect, err := docker.ExecInspect(ctx, execID)
	if err != nil {
		return stdout.String(), errors.WrapWithDetails(err, "inspecting exec result")
	}
	if inspect.ExitCode != 0 {
		return stdout.String(), errors.WithDetails("exec failed",
			"exit_code", inspect.ExitCode,
			"cmd", fmt.Sprint(cmd),
			"stdout", lastLines(stdout.String(), 4000),
			"stderr", lastLines(stderr.String(), 4000))
	}
	return stdout.String(), nil
}

// lastLines returns at most the final maxBytes of s, trimmed to a line boundary,
// so a failing command's tail (where the actual error usually is) is preserved
// without bloating the error with the entire build log.
func lastLines(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	tail := s[len(s)-maxBytes:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	}
	return "...\n" + tail
}

// RunLifecycleHooks executes the devcontainer lifecycle hooks in order inside a container.
// Sequence: onCreateCommand -> updateContentCommand -> postCreateCommand -> postStartCommand
func RunLifecycleHooks(ctx context.Context, docker DockerClient, containerID, user string, cfg *DevcontainerConfig, logger zerolog.Logger, out io.Writer) error {
	hooks := []struct {
		name string
		cmd  interface{}
	}{
		{"onCreateCommand", cfg.OnCreateCommand},
		{"updateContentCommand", cfg.UpdateContentCommand},
		{"postCreateCommand", cfg.PostCreateCommand},
		{"postStartCommand", cfg.PostStartCommand},
	}

	for _, h := range hooks {
		if h.cmd == nil {
			continue
		}
		_, _ = fmt.Fprintf(out, "Running %s...\n", h.name)
		if err := RunHook(ctx, docker, containerID, user, h.cmd, logger); err != nil {
			return errors.WrapWithDetails(err, "lifecycle hook failed", "hook", h.name)
		}
	}
	return nil
}
