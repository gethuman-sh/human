package cmddaemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// rotateDaemonLogs wires the four ~/.human log paths to their policies. Pointing
// HOME at a temp dir lets us drive the whole wiring: a diagnostic log over the
// cap rotates, and the accountability trails are the ones that never lose a
// generation (SC-2611).
func TestRotateDaemonLogsRotatesOversizedDiagnosticLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	humanDir := filepath.Join(home, ".human")
	if err := os.MkdirAll(humanDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	daemonLog := filepath.Join(humanDir, "daemon.log")
	big := strings.Repeat("x", diagnosticLogMaxBytes+1)
	if err := os.WriteFile(daemonLog, []byte(big), 0o600); err != nil {
		t.Fatalf("write daemon.log: %v", err)
	}

	rotateDaemonLogs(zerolog.Nop())

	if got := fileSize(t, daemonLog); got != 0 {
		t.Fatalf("daemon.log should be truncated after rotation, size=%d", got)
	}
	if got := fileSize(t, daemonLog+".1"); int(got) != len(big) {
		t.Fatalf("daemon.log.1 should hold the prior contents, size=%d want %d", got, len(big))
	}
}

func TestRotateDaemonLogsLeavesSmallLogsAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	humanDir := filepath.Join(home, ".human")
	if err := os.MkdirAll(humanDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	auditLog := filepath.Join(humanDir, "audit.log")
	if err := os.WriteFile(auditLog, []byte("small\n"), 0o600); err != nil {
		t.Fatalf("write audit.log: %v", err)
	}

	rotateDaemonLogs(zerolog.Nop())

	if _, err := os.Stat(auditLog + ".1"); !os.IsNotExist(err) {
		t.Fatalf("small audit.log must not rotate, stat err=%v", err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}
