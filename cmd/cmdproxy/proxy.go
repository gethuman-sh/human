package cmdproxy

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/proxy"
)

// BuildProxyCmd creates the "proxy" command tree.
func BuildProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Manage the HTTPS proxy",
	}

	cmd.AddCommand(buildProxyTrustCmd())
	cmd.AddCommand(buildProxyCACertCmd())
	cmd.AddCommand(buildProxyModelOutcomesCmd())
	return cmd
}

// buildProxyModelOutcomesCmd creates the "proxy model-outcomes" command, the
// read surface over the content-free model-call outcomes the proxy boundary
// records: what each call cost in time, its outcome class, and the ticket+stage
// that made it (SC-2555). Runs on the host, forwarded via the daemon.
func buildProxyModelOutcomesCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "model-outcomes",
		Short: "Show recorded model-call outcomes (class, timing, ticket/stage)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProxyModelOutcomes(cmd, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the raw outcome records as JSON")
	return cmd
}

func runProxyModelOutcomes(cmd *cobra.Command, asJSON bool) error {
	client, err := connectDaemon()
	if err != nil {
		return err
	}
	outcomes, err := client.GetModelOutcomes()
	if err != nil {
		return errors.WrapWithDetails(err, "fetching model outcomes from daemon")
	}
	return writeModelOutcomes(cmd.OutOrStdout(), outcomes, asJSON)
}

// writeModelOutcomes renders outcomes as JSON when asked, otherwise as a compact
// human-readable table. Kept separate from the command so it is unit-testable
// without a daemon.
func writeModelOutcomes(w io.Writer, outcomes []proxy.ModelCallOutcome, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(outcomes)
	}
	if len(outcomes) == 0 {
		_, _ = fmt.Fprintln(w, "No model-call outcomes recorded.")
		return nil
	}
	for _, o := range outcomes {
		ticket := o.Ticket
		if ticket == "" {
			ticket = "(unattributed)"
		}
		stage := o.Stage
		if stage == "" {
			stage = "-"
		}
		_, _ = fmt.Fprintf(w, "%-8s %-14s %-16s status=%-3d %s %s\n",
			o.Class, o.Duration.Round(time.Millisecond), ticket+"/"+stage, o.StatusCode, o.Host,
			o.StartedAt.Format(time.RFC3339))
	}
	return nil
}

// connectDaemon returns a client for the daemon named by the environment,
// falling back to daemon.json.
//
// It does not use daemon.Connect: `proxy trust` runs under sudo, where ~
// resolves to /root, so the info file has to be read from the invoking user's
// home instead. That is the whole difference, and it is why this resolution
// stays local.
func connectDaemon() (*daemon.Client, error) {
	envToken := os.Getenv("HUMAN_DAEMON_TOKEN")
	info := daemon.DaemonInfo{Addr: os.Getenv("HUMAN_DAEMON_ADDR"), Token: envToken}
	if info.Addr == "" {
		file, err := readDaemonInfo()
		if err != nil {
			return nil, errors.WrapWithDetails(err, "daemon not reachable")
		}
		info = file
		if envToken != "" {
			info.Token = envToken
		}
	}
	if info.Addr == "" {
		return nil, errors.WithDetails("daemon address not configured")
	}
	return daemon.NewClient(info)
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
	client, err := connectDaemon()
	if err != nil {
		return nil, err
	}

	resp, err := client.RunRemoteCapture([]string{"proxy", "ca-cert"})
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
