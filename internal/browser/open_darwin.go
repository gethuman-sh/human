//go:build darwin

package browser

import "os/exec"

func startBrowser(url string) error {
	return exec.Command("open", url).Start() // #nosec G204 -- "open" is a fixed macOS system command; url is caller-controlled but this is intentional browser-open behaviour
}
