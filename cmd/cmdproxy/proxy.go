package cmdproxy

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
)

// BuildProxyCmd creates the "proxy" command tree.
func BuildProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Manage the HTTPS proxy",
	}

	cmd.AddCommand(buildProxyTrustCmd())
	cmd.AddCommand(buildProxyCACertCmd())
	return cmd
}

func buildProxyTrustCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "trust",
		Short: "Install the daemon CA certificate into the container trust store",
		Long:  "Fetches the CA certificate from the daemon host and installs it into the system trust store. Requires sudo.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProxyTrust(cmd)
		},
	}
}

// buildProxyCACertCmd creates the "proxy ca-cert" command that prints the CA
// certificate PEM. This runs on the host (forwarded via daemon) so containers
// can fetch the cert.
func buildProxyCACertCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "ca-cert",
		Short:  "Print the CA certificate PEM",
		Hidden: true, // internal command used by proxy trust
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProxyCACert(cmd)
		},
	}
}

func runProxyTrust(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()

	// First try to find the cert locally (running on the host).
	certPEM, err := findCACertLocal()
	if err != nil {
		// Not found locally — fetch from daemon (running inside container).
		certPEM, err = fetchCACertFromDaemon()
		if err != nil {
			return errors.WrapWithDetails(err, "cannot find CA certificate locally or from daemon")
		}
	}

	destDir := "/usr/local/share/ca-certificates"
	destPath := filepath.Join(destDir, "human-proxy.crt")

	_, _ = fmt.Fprintf(out, "Installing CA cert → %s\n", destPath)

	if err := os.MkdirAll(destDir, 0o755); err != nil { // #nosec G301 -- system ca-certificates dir must be world-readable
		return errors.WrapWithDetails(err, "failed to create ca-certificates directory")
	}

	if err := os.WriteFile(destPath, certPEM, 0o644); err != nil { // #nosec G306 -- CA cert must be world-readable
		return errors.WrapWithDetails(err, "failed to write CA cert",
			"dest", destPath)
	}

	// Update trust store.
	updateCmd := exec.Command("update-ca-certificates") // #nosec G204 -- no user input
	updateCmd.Stdout = out
	updateCmd.Stderr = cmd.ErrOrStderr()
	if err := updateCmd.Run(); err != nil {
		return errors.WrapWithDetails(err, "update-ca-certificates failed")
	}

	// Configure Node.js CA trust (Node ignores system CA store).
	profileScript := "export NODE_EXTRA_CA_CERTS=" + destPath + "\n"
	profilePath := "/etc/profile.d/human-proxy-ca.sh"
	if err := os.WriteFile(profilePath, []byte(profileScript), 0o644); err != nil { // #nosec G306 -- profile.d scripts must be world-readable
		_, _ = fmt.Fprintf(out, "Warning: could not write %s: %v\n", profilePath, err)
	}

	_, _ = fmt.Fprintln(out, "CA certificate installed and trust store updated")
	_, _ = fmt.Fprintf(out, "Node.js CA trust: %s\n", profilePath)
	return nil
}

// runProxyCACert prints the CA cert PEM to stdout. This command runs on the
// host (forwarded via daemon), allowing containers to fetch the cert.
func runProxyCACert(cmd *cobra.Command) error {
	certPEM, err := findCACertLocal()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprint(out, string(certPEM))
	return nil
}

// findCACertLocal looks for the CA cert in ~/.human/ca.crt.
func findCACertLocal() ([]byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.WrapWithDetails(err, "resolving home directory")
	}

	certPath := filepath.Join(home, ".human", "ca.crt")
	data, err := os.ReadFile(certPath) // #nosec G304 -- path built from home dir
	if err != nil {
		return nil, errors.WithDetails("CA certificate not found; start the daemon with intercept: configured first",
			"path", certPath)
	}
	return data, nil
}

// fetchCACertFromDaemon asks the daemon to run "proxy ca-cert" and returns
// the PEM output.
func fetchCACertFromDaemon() ([]byte, error) {
	addr := os.Getenv("HUMAN_DAEMON_ADDR")
	token := os.Getenv("HUMAN_DAEMON_TOKEN")

	if addr == "" {
		info, err := readDaemonInfo()
		if err != nil {
			return nil, errors.WrapWithDetails(err, "daemon not reachable")
		}
		addr = info.Addr
		if token == "" {
			token = info.Token
		}
	}

	if addr == "" {
		return nil, errors.WithDetails("daemon address not configured")
	}

	resp, err := daemon.RunRemoteCapture(addr, token, []string{"proxy", "ca-cert"})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "failed to fetch CA cert from daemon")
	}

	if len(resp) == 0 {
		return nil, errors.WithDetails("daemon returned empty CA cert")
	}

	// Validate the response is a proper CA certificate to guard against
	// MITM injection of rogue certificates over the plaintext TCP channel.
	block, _ := pem.Decode(resp)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.WithDetails("daemon returned invalid PEM data")
	}
	cert, parseErr := x509.ParseCertificate(block.Bytes)
	if parseErr != nil {
		return nil, errors.WrapWithDetails(parseErr, "daemon returned unparseable certificate")
	}
	if !cert.IsCA {
		return nil, errors.WithDetails("daemon returned a non-CA certificate")
	}

	return resp, nil
}

// readDaemonInfo reads daemon.json, falling back to the original user's home
// when running under sudo (where ~ resolves to /root instead of the real user).
func readDaemonInfo() (daemon.DaemonInfo, error) {
	info, err := daemon.ReadInfo()
	if err == nil {
		return info, nil
	}

	// Under sudo, HOME is /root but daemon.json is in the real user's home.
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		return daemon.DaemonInfo{}, err
	}

	// Validate SUDO_USER to prevent path traversal.
	if strings.Contains(sudoUser, "/") || strings.Contains(sudoUser, "..") {
		return daemon.DaemonInfo{}, errors.WithDetails("invalid SUDO_USER value")
	}

	// Try /home/<SUDO_USER>/.human/daemon.json
	altPath := filepath.Join("/home", sudoUser, ".human", "daemon.json")
	data, readErr := os.ReadFile(altPath) // #nosec G304 G703 -- SUDO_USER is trusted OS-provided env
	if readErr != nil {
		return daemon.DaemonInfo{}, err // return original error
	}

	var altInfo daemon.DaemonInfo
	if jsonErr := json.Unmarshal(data, &altInfo); jsonErr != nil {
		return daemon.DaemonInfo{}, err
	}

	return altInfo, nil
}
