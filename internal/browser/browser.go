package browser

import (
	"fmt"
	"io"
	"net/url"

	"github.com/gethuman-sh/human/errors"
)

// Opener opens a URL in the default browser.
type Opener interface {
	Open(url string) error
}

// DefaultOpener uses platform-specific commands to open URLs without blocking.
type DefaultOpener struct{}

func (DefaultOpener) Open(rawURL string) error {
	// Validate at the opener boundary so every caller (including the
	// TUI's direct openBrowserCmd) gets the scheme whitelist, not just
	// those routing through RunOpen.
	if err := ValidateURL(rawURL); err != nil {
		return err
	}
	return startBrowser(rawURL)
}

// ValidateURL checks that rawURL is non-empty, parseable, and uses http or https.
func ValidateURL(rawURL string) error {
	if rawURL == "" {
		return errors.WithDetails("URL must not be empty")
	}
	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return errors.WithDetails("invalid URL", "url", rawURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.WithDetails("URL must use http or https scheme", "scheme", u.Scheme, "url", rawURL)
	}
	return nil
}

// RunOpen validates the URL, opens it, and prints a confirmation.
func RunOpen(opener Opener, out io.Writer, rawURL string) error {
	if err := ValidateURL(rawURL); err != nil {
		return err
	}
	if err := opener.Open(rawURL); err != nil {
		return errors.WithDetails("opening browser", "url", rawURL)
	}
	_, _ = fmt.Fprintf(out, "Opened %s\n", rawURL)
	return nil
}
