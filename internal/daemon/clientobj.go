package daemon

import (
	stderrors "errors"
	"fmt"
	"os"

	"github.com/gethuman-sh/human/errors"
)

// Client is a handle on one running daemon. It exists so the endpoint travels
// as the single value it is: Addr and ChromeAddr are both host:port strings and
// a token belongs to exactly one of them, so passing the parts separately lets a
// caller pair one daemon's address with another's token and still compile.
//
// It is immutable and dials per call, so one Client is safe to share across
// goroutines.
type Client struct {
	info DaemonInfo
	// version is stamped into every Request so the daemon's version gate can
	// refuse a protocol-stale client up front.
	version string
}

// NewClient wraps an already-resolved DaemonInfo.
//
// It deliberately does NOT probe the address: callers that gate on
// IsReachable() before deciding to talk to the daemon keep making that decision
// themselves, and the ones that do not should not start paying a dial timeout.
//
// The protocol gate lives here because this is the only way to obtain a client:
// before it existed, DaemonProtocolError had one caller in main.go and every
// other path to the daemon — the whole desktop app, every cmd/ package — reached
// it ungated.
func NewClient(info DaemonInfo) (*Client, error) {
	if err := DaemonProtocolError(info); err != nil {
		return nil, err
	}
	return &Client{info: info, version: ClientVersion}, nil
}

// Connect locates the running daemon and returns a client for it: env first,
// then the info file, then the host.docker.internal fallback so a command works
// from inside a devcontainer.
//
// This is the one discovery path. A second copy in a command package is how the
// order drifts, and a command that looks in a different order finds a different
// daemon.
func Connect() (*Client, error) {
	info, err := resolveInfo()
	if err != nil {
		return nil, err
	}
	return NewClient(info)
}

// Info returns the endpoint this client speaks to, including the chrome and
// proxy addresses that ride along unused by the RPC path.
func (c *Client) Info() DaemonInfo { return c.info }

// resolveInfo runs the discovery order. It returns the whole DaemonInfo rather
// than an address and a token because the caller of the fallback branch needs
// ChromeAddr and ProxyAddr too — dropping them is what leaves a containerized
// agent with no HUMAN_PROXY_ADDR.
func resolveInfo() (DaemonInfo, error) {
	file, readErr := ReadInfo()
	token := os.Getenv("HUMAN_DAEMON_TOKEN")
	if token == "" && readErr == nil {
		token = file.Token
	}

	// An explicit address is the caller stating where the daemon is, so it is
	// taken without a reachability probe. The rest of the file's fields still
	// come along: the version-skew warning and the protocol gate read them, and
	// they describe the same daemon whenever the file is present at all.
	if addr := os.Getenv("HUMAN_DAEMON_ADDR"); addr != "" {
		info := file
		if readErr != nil {
			info = DaemonInfo{}
		}
		info.Addr = addr
		info.Token = token
		return info, nil
	}

	if readErr == nil && file.IsReachable() {
		file.Token = token
		return file, nil
	}

	// Inside a container the info file is often absent, or names a host address
	// that does not resolve there. All three addresses are synthesized, not just
	// Addr: the chrome bridge and the agent proxy redirect read the other two.
	fallback := DaemonInfo{
		Addr:       fmt.Sprintf("%s:%d", DockerHost, DefaultPort),
		ChromeAddr: fmt.Sprintf("%s:%d", DockerHost, DefaultChromePort),
		ProxyAddr:  fmt.Sprintf("%s:%d", DockerHost, DefaultProxyPort),
		Token:      token,
	}
	if fallback.IsReachable() {
		return fallback, nil
	}

	return DaemonInfo{}, errors.WithDetails("human daemon not reachable — start it with `human daemon start` (it holds the tracker credentials and resolves the configured PM group)")
}

// errProtocolTooOld marks the refusal so a caller can tell it apart from "no
// daemon reachable". The two need opposite responses: a missing daemon means
// run the command locally, a stale one means stop and say so.
var errProtocolTooOld = stderrors.New("daemon protocol too old")

// IsProtocolError reports whether err is the too-old-daemon refusal.
func IsProtocolError(err error) bool { return stderrors.Is(err, errProtocolTooOld) }
