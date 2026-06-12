package gui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testServer() *Server {
	return &Server{
		Addr:   "127.0.0.1:0",
		Token:  "secret-token",
		Logger: zerolog.Nop(),
	}
}

// doReq runs one request through the full handler chain.
func doReq(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// loopbackReq builds a request whose Host passes the loopback guard
// (httptest.NewRequest defaults to example.com, which must be rejected).
func loopbackReq(method, target string, body string) *http.Request {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, "http://127.0.0.1:19288"+target, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, "http://127.0.0.1:19288"+target, nil)
	}
	return req
}

func authedReq(method, target string, body string) *http.Request {
	req := loopbackReq(method, target, body)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "secret-token"})
	return req
}

func TestAuth_ValidTokenSetsCookieAndRedirects(t *testing.T) {
	s := testServer()
	rec := doReq(t, s, loopbackReq(http.MethodGet, "/auth?token=secret-token", ""))

	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/", rec.Header().Get("Location"))

	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, cookieName, cookies[0].Name)
	assert.Equal(t, "secret-token", cookies[0].Value)
	assert.True(t, cookies[0].HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)
}

func TestAuth_InvalidTokenRejected(t *testing.T) {
	s := testServer()
	rec := doReq(t, s, loopbackReq(http.MethodGet, "/auth?token=wrong", ""))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = doReq(t, s, loopbackReq(http.MethodGet, "/auth", ""))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAPI_RequiresAuth(t *testing.T) {
	s := testServer()

	rec := doReq(t, s, loopbackReq(http.MethodGet, "/api/snapshot", ""))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	req := loopbackReq(http.MethodGet, "/api/snapshot", "")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "wrong"})
	rec = doReq(t, s, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAPI_CookieAuthAccepted(t *testing.T) {
	s := testServer()
	rec := doReq(t, s, authedReq(http.MethodGet, "/api/projects", ""))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAPI_BearerAuthAccepted(t *testing.T) {
	s := testServer()
	req := loopbackReq(http.MethodGet, "/api/projects", "")
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := doReq(t, s, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGuardOrigin_RejectsNonLoopbackHost(t *testing.T) {
	s := testServer()
	req := authedReq(http.MethodGet, "/api/projects", "")
	req.Host = "evil.example.com:19288"
	rec := doReq(t, s, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestGuardOrigin_RejectsNonLoopbackOrigin(t *testing.T) {
	s := testServer()
	req := authedReq(http.MethodGet, "/api/projects", "")
	req.Header.Set("Origin", "https://evil.example.com")
	rec := doReq(t, s, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestGuardOrigin_AcceptsLoopbackVariants(t *testing.T) {
	s := testServer()
	for _, host := range []string{"127.0.0.1:19288", "localhost:19288", "[::1]:19288"} {
		req := authedReq(http.MethodGet, "/api/projects", "")
		req.Host = host
		req.Header.Set("Origin", "http://"+host)
		rec := doReq(t, s, req)
		assert.Equal(t, http.StatusOK, rec.Code, "host %s", host)
	}
}

func TestStatic_ServesPlaceholderIndex(t *testing.T) {
	s := testServer()

	rec := doReq(t, s, loopbackReq(http.MethodGet, "/", ""))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "human gui")

	// SPA fallback: unknown paths serve the index, not a 404, so
	// client-side routes survive a refresh.
	rec = doReq(t, s, loopbackReq(http.MethodGet, "/some/client/route", ""))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "human gui")
}

func TestPlaceholderOnly(t *testing.T) {
	rec := httptest.NewRecorder()
	placeholderOnly().ServeHTTP(rec, loopbackReq(http.MethodGet, "/", ""))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "make web")
}

func TestIsLoopbackOrigin(t *testing.T) {
	assert.True(t, isLoopbackOrigin("http://localhost:3000"))
	assert.True(t, isLoopbackOrigin("http://127.0.0.1"))
	assert.False(t, isLoopbackOrigin("http://example.com"))
	assert.False(t, isLoopbackOrigin("::not a url"))
}

func TestAuthURLEscaping(t *testing.T) {
	// The /auth flow must survive tokens with URL-significant bytes.
	s := testServer()
	s.Token = "a+b/c=="
	target := "/auth?token=" + url.QueryEscape(s.Token)
	rec := doReq(t, s, loopbackReq(http.MethodGet, target, ""))
	assert.Equal(t, http.StatusFound, rec.Code)
}
