package daemon

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func doctorWithChecks(checks ...DoctorCheckDef) *DoctorRunner {
	return NewDoctorRunner(checks)
}

func passing(id string) DoctorCheckDef {
	return DoctorCheckDef{
		ID:   id,
		Name: id,
		Run:  func(context.Context) (bool, string) { return true, "fine" },
	}
}

func failing(id, detail string) DoctorCheckDef {
	return DoctorCheckDef{
		ID:   id,
		Name: id,
		Run:  func(context.Context) (bool, string) { return false, detail },
	}
}

func TestDoctorRunner_AggregatesHealth(t *testing.T) {
	d := doctorWithChecks(passing("a"), failing("b", "broken thing"))

	res := d.Results(context.Background(), 0)
	require.Len(t, res.Checks, 2)
	assert.False(t, res.Healthy)
	assert.Equal(t, "a", res.Checks[0].ID)
	assert.True(t, res.Checks[0].OK)
	assert.False(t, res.Checks[1].OK)
	assert.Equal(t, "broken thing", res.Checks[1].Detail)
	assert.NotEmpty(t, res.CheckedAt)

	allGreen := doctorWithChecks(passing("a"), passing("b"))
	assert.True(t, allGreen.Results(context.Background(), 0).Healthy)
}

// The LED polls every few seconds; checks must not re-run on every poll.
func TestDoctorRunner_CachesWithinMaxAge(t *testing.T) {
	var runs atomic.Int32
	d := doctorWithChecks(DoctorCheckDef{
		ID: "counted", Name: "counted",
		Run: func(context.Context) (bool, string) {
			runs.Add(1)
			return true, ""
		},
	})

	for range 5 {
		d.Results(context.Background(), time.Minute)
	}
	assert.Equal(t, int32(1), runs.Load(), "fresh results must be served from cache")

	// maxAge zero forces a live run regardless of cache freshness.
	d.Results(context.Background(), 0)
	assert.Equal(t, int32(2), runs.Load())
}

// A wedged check must not hang the runner: each check gets a bounded slice
// of time and reports failure when it exceeds it.
func TestDoctorRunner_CheckTimeoutFails(t *testing.T) {
	slow := DoctorCheckDef{
		ID: "slow", Name: "slow", Timeout: 30 * time.Millisecond,
		Run: func(ctx context.Context) (bool, string) {
			<-ctx.Done()
			return false, "context expired"
		},
	}
	d := doctorWithChecks(slow)

	start := time.Now()
	res := d.Results(context.Background(), 0)
	require.Less(t, time.Since(start), 5*time.Second)
	assert.False(t, res.Healthy)
	assert.False(t, res.Checks[0].OK)
}

// Blockers filters the failing subset of launch-critical checks so board
// launches can be refused with the real reason.
func TestDoctorRunner_Blockers(t *testing.T) {
	d := doctorWithChecks(
		passing("docker"),
		failing("agent-skills", "no /human-autofix in /proj/.claude"),
		failing("ca-cert", "PEM parse failed"),
	)

	blockers := d.Blockers(context.Background(), []string{"docker", "agent-skills"})
	require.Len(t, blockers, 1)
	assert.Equal(t, "agent-skills", blockers[0].ID)
	assert.Equal(t, "no /human-autofix in /proj/.claude", blockers[0].Detail)

	healthy := doctorWithChecks(passing("docker"))
	assert.Empty(t, healthy.Blockers(context.Background(), []string{"docker"}))
}

// A nil runner is a disabled feature, mirroring the Server's other nil-guards.
func TestDoctorRunner_NilSafe(t *testing.T) {
	var d *DoctorRunner
	res := d.Results(context.Background(), 0)
	assert.True(t, res.Healthy, "a disabled doctor must not block anything")
	assert.Empty(t, res.Checks)
	assert.Empty(t, d.Blockers(context.Background(), []string{"docker"}))
}

// startDoctorTestServer mirrors startTestServerWithOpts but lets the test
// configure the doctor and board hooks the gating rides on.
func startDoctorTestServer(t *testing.T, configure func(*Server)) (addr string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	srv := &Server{Addr: "127.0.0.1:0", Token: "tok", CmdFactory: echoCmd, Logger: zerolog.Nop()}
	configure(srv)

	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr = ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr

	go func() { _ = srv.ListenAndServe(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(cancel)
	return addr
}

func TestServer_DoctorRoute(t *testing.T) {
	addr := startDoctorTestServer(t, func(s *Server) {
		s.Doctor = doctorWithChecks(passing("docker"), failing("ca-cert", "PEM parse failed"))
	})

	resp := sendRequest(t, addr, Request{Token: "tok", Args: []string{"doctor"}})
	require.Equal(t, 0, resp.ExitCode, resp.Stderr)

	var data DoctorData
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &data))
	assert.False(t, data.Healthy)
	require.Len(t, data.Checks, 2)
	assert.Equal(t, "ca-cert", data.Checks[1].ID)
	assert.Equal(t, "PEM parse failed", data.Checks[1].Detail)
}

// A disabled doctor reports healthy: the route must never invent problems.
func TestServer_DoctorRouteNilRunner(t *testing.T) {
	addr := startDoctorTestServer(t, func(*Server) {})

	resp := sendRequest(t, addr, Request{Token: "tok", Args: []string{"doctor"}})
	require.Equal(t, 0, resp.ExitCode, resp.Stderr)

	var data DoctorData
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &data))
	assert.True(t, data.Healthy)
}

// A board fix launch on a substrate the doctor knows is broken is refused
// with the check's own diagnosis — the agent never launches to rediscover it.
func TestServer_BoardFixBlockedByDoctor(t *testing.T) {
	var launched bool
	addr := startDoctorTestServer(t, func(s *Server) {
		s.Doctor = doctorWithChecks(failing("agent-skills", "no /human-autofix under /proj/.claude — run human install"))
		s.BoardFixer = func(BoardFixRequest) error { launched = true; return nil }
	})

	resp := sendRequest(t, addr, Request{Token: "tok", Args: []string{"board-fix", `{"pm_key":"1","pm_title":"t"}`}})
	require.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "launch blocked")
	assert.Contains(t, resp.Stderr, "run human install")
	assert.False(t, launched, "the fixer must not launch on a blocked substrate")
}

func TestServer_BoardFixProceedsWhenHealthy(t *testing.T) {
	var launched bool
	addr := startDoctorTestServer(t, func(s *Server) {
		s.Doctor = doctorWithChecks(passing("docker"), passing("agent-skills"))
		s.BoardFixer = func(BoardFixRequest) error { launched = true; return nil }
	})

	resp := sendRequest(t, addr, Request{Token: "tok", Args: []string{"board-fix", `{"pm_key":"1","pm_title":"t"}`}})
	require.Equal(t, 0, resp.ExitCode, resp.Stderr)
	assert.True(t, launched)
}

// Deploy (the done stage) merges and closes without an agent — a red docker
// LED must not stop shipping already-reviewed work.
func TestServer_DeployTransitionNotGated(t *testing.T) {
	var applied bool
	addr := startDoctorTestServer(t, func(s *Server) {
		s.Doctor = doctorWithChecks(failing("docker", "engine unreachable"))
		s.BoardTransitioner = func(BoardTransitionRequest) error { applied = true; return nil }
	})

	resp := sendRequest(t, addr, Request{Token: "tok", Args: []string{"board-transition", `{"pm_key":"1","pm_title":"t","from":"verification","to":"done"}`}})
	require.Equal(t, 0, resp.ExitCode, resp.Stderr)
	assert.True(t, applied, "deploy launches no agent and must not be doctor-gated")

	// The same failing docker check DOES gate an agent-launching transition.
	resp = sendRequest(t, addr, Request{Token: "tok", Args: []string{"board-transition", `{"pm_key":"1","pm_title":"t","from":"backlog","to":"planning"}`}})
	require.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "engine unreachable")
}
