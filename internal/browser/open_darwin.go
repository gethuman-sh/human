//go:build darwin

package browser

import "os/exec"

func startBrowser(url string) error {
	return exec.Command("open", url).Start() // #nosec G204 -- url is passed by the caller; "open" is a macOS system command
}
