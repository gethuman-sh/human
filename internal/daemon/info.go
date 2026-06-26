package daemon

import (
	"encoding/json"
	"path/filepath"

	"github.com/spf13/afero"

	client "github.com/gethuman-sh/human-daemon-client"
)

const (
	// DefaultPort is the well-known daemon listening port.
	DefaultPort = client.DefaultPort
	// DefaultChromePort is the well-known Chrome proxy port.
	DefaultChromePort = client.DefaultChromePort
	// DefaultProxyPort is the well-known HTTPS proxy port.
	DefaultProxyPort = client.DefaultProxyPort

	// DockerHost is the hostname Docker provides for reaching the host machine
	// from inside a container. Enabled by --add-host=host.docker.internal:host-gateway.
	DockerHost = client.DockerHost
)

// ProjectInfo describes a registered project in a running daemon. It is defined
// by the public human-daemon-client contract (clients read it off ReadInfo);
// the daemon aliases it.
type ProjectInfo = client.ProjectInfo

// DaemonInfo holds the runtime details of a running daemon instance. The struct
// (and its IsReachable method) live in the public human-daemon-client contract
// so the wire format and the reachability probe have a single source of truth;
// the daemon aliases the type here.
type DaemonInfo = client.DaemonInfo

// InfoPath returns the default path for the daemon info file (~/.human/daemon.json).
func InfoPath() string { return client.InfoPath() }

// ReadInfo reads and unmarshals the daemon info from InfoPath.
func ReadInfo() (DaemonInfo, error) { return client.ReadInfo() }

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

// RemoveInfo removes the daemon info file (best-effort).
func RemoveInfo() {
	_ = fs.Remove(InfoPath())
}
