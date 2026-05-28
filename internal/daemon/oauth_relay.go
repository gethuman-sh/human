package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/gethuman-sh/human/internal/browser"
	"github.com/gethuman-sh/human/internal/oauth"
)

const oauthAcceptTimeout = 5 * time.Minute

// BrowserOpener opens a URL in the browser. Extracted for testability.
type BrowserOpener interface {
	Open(url string) error
}

// isBrowserWithRedirect checks if args represent a "browser <url>" command
// where the URL contains an OAuth redirect_uri targeting localhost.
func isBrowserWithRedirect(args []string) (*oauth.RedirectInfo, string) {
	// Find "browser" subcommand followed by a URL argument.
	for i, arg := range args {
		if arg == "browser" && i+1 < len(args) {
			url := args[i+1]
			if err := browser.ValidateURL(url); err != nil {
				return nil, ""
			}
			info := oauth.DetectRedirect(url)
			if info != nil {
				return info, url
			}
			return nil, ""
		}
	}
	return nil, ""
}

// handleOAuthRelay intercepts a browser command with an OAuth redirect.
// It binds the original redirect_uri port on the host, opens the browser
// with the unmodified URL, and uses a two-line protocol to relay the
// callback URL back to the client. The client (running inside the
// container) delivers the callback to Claude Code's localhost listener.
func (s *Server) handleOAuthRelay(conn net.Conn, _ *bufio.Reader, info *oauth.RedirectInfo, originalURL string, opener BrowserOpener) {
	// Bind the ORIGINAL redirect_uri port on the host so the browser's
	// OAuth callback lands here. We must NOT rewrite the redirect_uri —
	// the OAuth provider binds the auth code to the exact redirect_uri
	// from the authorization request, and Claude Code will send that
	// same URI when exchanging the code for a token.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", info.Port))
	if err != nil {
		s.writeError(conn, fmt.Sprintf("cannot listen on port %d for OAuth callback: %s", info.Port, err), 1)
		return
	}
	defer func() { _ = ln.Close() }()

	s.Logger.Debug().Int("port", info.Port).Msg("OAuth relay: listening on original redirect port")

	// Open the browser with the ORIGINAL URL (no redirect_uri rewrite).
	if err := opener.Open(originalURL); err != nil {
		s.writeError(conn, fmt.Sprintf("failed to open browser: %s", err), 1)
		return
	}

	s.Logger.Debug().Str("url", originalURL).Msg("browser opened with original URL")

	// Line 1: tell the client to print stdout and keep waiting.
	enc := json.NewEncoder(conn)
	resp1 := Response{
		Stdout:        fmt.Sprintf("Opened %s\n", originalURL),
		AwaitCallback: true,
	}
	if err := enc.Encode(resp1); err != nil {
		s.Logger.Warn().Err(err).Msg("failed to write OAuth line 1")
		return
	}

	// Wait for the browser callback on the original redirect port.
	cbURL, ok := s.awaitCallback(ln, info)
	if !ok {
		return
	}

	// Line 2: send the callback URL to the client for local delivery.
	resp2 := Response{Callback: cbURL}
	if err := enc.Encode(resp2); err != nil {
		s.Logger.Warn().Err(err).Msg("failed to write OAuth line 2")
		return
	}

	s.Logger.Debug().Str("path", info.Path).Msg("OAuth callback URL sent to client")
}

// awaitCallback accepts the OAuth callback on the listener and returns
// the callback URL. Returns ("", false) on timeout or error.
func (s *Server) awaitCallback(ln net.Listener, info *oauth.RedirectInfo) (string, bool) {
	callbackURL := make(chan string, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(info.Path, func(w http.ResponseWriter, r *http.Request) {
		// Only GET callbacks are allowed.
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Exact path match — no prefix-match acceptance.
		if r.URL.Path != info.Path {
			http.NotFound(w, r)
			return
		}
		// If the original authorisation URL carried a state parameter,
		// require the callback to echo the same value.
		if info.State != "" && r.URL.Query().Get("state") != info.State {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		// A valid OAuth2 authorization response always carries either an
		// authorization code or an error (RFC 6749 §4.1.2). Rejecting anything
		// else keeps stray local requests on this port from being mistaken for
		// the callback — the main residual exposure when no state is present.
		if r.URL.Query().Get("code") == "" && r.URL.Query().Get("error") == "" {
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			return
		}

		u := fmt.Sprintf("http://localhost:%d%s?%s", info.Port, r.URL.Path, r.URL.RawQuery)
		paramKeys := make([]string, 0, len(r.URL.Query()))
		for k := range r.URL.Query() {
			paramKeys = append(paramKeys, k)
		}
		sort.Strings(paramKeys)
		s.Logger.Debug().Str("path", r.URL.Path).Strs("param_keys", paramKeys).Msg("OAuth callback received")

		// Ambient network activity: the OAuth callback is exactly the
		// opaque "what is Claude reaching out to" signal the TUI panel
		// is for. Emit with a synthetic host of "oauth:<port><path>" so
		// the panel has a non-empty identifier; the store collapses
		// bursts consecutively.
		if s.NetworkEvents != nil {
			s.NetworkEvents.Emit("oauth", "callback", fmt.Sprintf("oauth:%d%s", info.Port, info.Path))
		}

		// Non-blocking send so a duplicate browser callback (or a favicon
		// retry that survives the dispatcher's path filter) can never
		// block this handler goroutine forever.
		select {
		case callbackURL <- u:
		default:
		}

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "<html><body><h1>Authorization successful</h1><p>You can close this tab.</p></body></html>")
	})

	if info.State == "" {
		s.Logger.Warn().Msg("OAuth relay running without a state parameter; CSRF protection is limited to the loopback callback race")
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: oauthAcceptTimeout} //nolint:gosec // short-lived local server for OAuth callback
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			s.Logger.Warn().Err(serveErr).Msg("OAuth callback server error")
		}
	}()
	defer func() { _ = srv.Close() }()

	select {
	case u := <-callbackURL:
		return u, true
	case <-time.After(oauthAcceptTimeout):
		s.Logger.Warn().Msg("OAuth callback timeout")
		return "", false
	}
}
