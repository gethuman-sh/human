package daemon

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/afero"
)

const (
	// DefaultPort is the well-known daemon listening port.
	DefaultPort = 19285
	// DefaultChromePort is the well-known Chrome proxy port.
	DefaultChromePort = 19286
	// DefaultProxyPort is the well-known HTTPS proxy port.
	DefaultProxyPort = 19287

	// DockerHost is the hostname Docker provides for reaching the host machine
	// from inside a container. Enabled by --add-host=host.docker.internal:host-gateway.
	DockerHost = "host.docker.internal"
)

// ProjectInfo describes a registered project in a running daemon.
type ProjectInfo struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// DaemonInfo holds the runtime details of a running daemon instance.
type DaemonInfo struct {
	Addr       string        `json:"addr"`
	ChromeAddr string        `json:"chrome_addr,omitempty"`
	ProxyAddr  string        `json:"proxy_addr,omitempty"`
	Token      string        `json:"token"`
	PID        int           `json:"pid"`
	Projects   []ProjectInfo `json:"projects,omitempty"`
}

// InfoPath returns the default path for the daemon info file (~/.human/daemon.json).
func InfoPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "daemon.json")
	}
	return filepath.Join(home, ".human", "daemon.json")
}

// WriteInfo writes the daemon info as JSON to InfoPath with restricted permissions.
func WriteInfo(info DaemonInfo) error {
	path := InfoPath()
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return afero.WriteFile(fs, path, data, 0o600)
}

// ReadInfo reads and unmarshals the daemon info from InfoPath.
func ReadInfo() (DaemonInfo, error) {
	data, err := afero.ReadFile(fs, InfoPath())
	if err != nil {
		return DaemonInfo{}, err
	}
	var info DaemonInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return DaemonInfo{}, err
	}
	return info, nil
}

// IsReachable checks whether the daemon is accepting TCP connections at its
// advertised address. This works across process namespaces (e.g. host ↔
// devcontainer) where PID-based checks fail.
func (d DaemonInfo) IsReachable() bool {
	if d.Addr == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", d.Addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// RemoveInfo removes the daemon info file (best-effort).
func RemoveInfo() {
	_ = fs.Remove(InfoPath())
}
