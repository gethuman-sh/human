// Package cmdgui launches the browser dashboard: it makes sure a daemon
// (which hosts the GUI listener) is running, then opens the authenticated
// GUI URL in the default browser.
package cmdgui

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/browser"
	"github.com/gethuman-sh/human/internal/daemon"
)

// BuildGuiCmd creates the "gui" command.
func BuildGuiCmd() *cobra.Command {
	var projectDirs []string
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "gui",
		Short: "Open the browser dashboard",
		Long:  "Start the daemon if needed and open the GUI dashboard in the default browser.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGui(cmd, projectDirs, noBrowser, browser.DefaultOpener{})
		},
	}
	cmd.Flags().StringArrayVar(&projectDirs, "project", nil, "Project directory to register (repeatable; forwarded to daemon)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the GUI URL instead of opening a browser")
	return cmd
}

// URLOpener abstracts browser launching for testability.
type URLOpener interface {
	Open(url string) error
}

func runGui(cmd *cobra.Command, projectDirs []string, noBrowser bool, opener URLOpener) error {
	ensureDaemon(projectDirs)

	info, err := daemon.ReadInfo()
	if err != nil {
		return errors.WrapWithDetails(err, "daemon is not running and could not be started")
	}
	guiAddr := info.GuiAddr
	if guiAddr == "" {
		// Older daemon without a GUI listener: a restart picks up the new
		// default. Don't restart automatically — the daemon may be serving
		// other clients.
		return errors.WithDetails("the running daemon has no GUI listener — restart it with 'human daemon stop && human daemon start'")
	}

	authURL := fmt.Sprintf("http://%s/auth?token=%s", guiAddr, url.QueryEscape(info.Token))
	if noBrowser {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), authURL)
		return nil
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Opening GUI at http://%s\n", guiAddr)
	if err := opener.Open(authURL); err != nil {
		return errors.WrapWithDetails(err, "opening browser", "url", "http://"+guiAddr)
	}
	return nil
}

// ensureDaemon starts the daemon if it is not already running, mirroring
// the TUI's bootstrap (0.0.0.0 listeners for container access; the GUI
// listener itself stays on its loopback default).
func ensureDaemon(projectDirs []string) {
	if _, alive := daemon.ReadAlivePid(); alive {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	args := []string{"daemon", "start",
		"--addr", "0.0.0.0:19285",
		"--chrome-addr", "0.0.0.0:19286",
		"--proxy-addr", "0.0.0.0:19287",
	}
	for _, dir := range projectDirs {
		args = append(args, "--project", dir)
	}
	child := exec.Command(exe, args...) // #nosec G204 -- re-exec of own binary via os.Executable()
	_ = child.Start()
	if child.Process != nil {
		_ = child.Process.Release()
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", "localhost:19285", 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
