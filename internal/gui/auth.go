package gui

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// cookieName carries the daemon token in the browser after /auth.
const cookieName = "human_gui"

// handleAuth exchanges a one-shot token query parameter for an HttpOnly
// cookie and redirects to the app root, so the token does not linger in
// the address bar or browser history.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !s.tokenValid(token) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// requireAuth accepts the auth cookie or an Authorization: Bearer header.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenValid(requestToken(r)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestToken extracts the token from the cookie or the bearer header.
func requestToken(r *http.Request) string {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		return c.Value
	}
	auth := r.Header.Get("Authorization")
	if rest, ok := strings.CutPrefix(auth, "Bearer "); ok {
		return rest
	}
	return ""
}

func (s *Server) tokenValid(token string) bool {
	if token == "" || s.Token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.Token)) == 1
}

// guardOrigin rejects requests whose Host or Origin is not loopback. The
// GUI is a same-origin localhost app; this blocks DNS-rebinding attacks
// where a hostile page resolves its own domain to 127.0.0.1 and rides the
// browser's cookie jar. Applied to every route, including /auth and static
// assets — there is no legitimate cross-origin use of any of them.
func (s *Server) guardOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHostport(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !isLoopbackOrigin(origin) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackOrigin reports whether an Origin header value points at a
// loopback host.
func isLoopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

// isLoopbackHostport reports whether a host[:port] string is loopback.
func isLoopbackHostport(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return isLoopbackHost(host)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
