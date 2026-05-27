package apiclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
)

type errDoer struct{ err error }

func (d *errDoer) Do(*http.Request) (*http.Response, error) { return nil, d.err }

type nilDoer struct{}

func (*nilDoer) Do(*http.Request) (*http.Response, error) { return nil, nil }

func TestDo_networkError(t *testing.T) {
	c := New("https://example.com",
		WithProviderName("test"),
		WithHTTPDoer(&errDoer{err: fmt.Errorf("connection refused")}),
	)
	_, err := c.Do(context.Background(), "GET", "/api/v1/test", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requesting test")
}

func TestDo_nilResponse(t *testing.T) {
	c := New("https://example.com",
		WithProviderName("test"),
		WithHTTPDoer(&nilDoer{}),
	)
	_, err := c.Do(context.Background(), "GET", "/api/v1/test", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil response")
}

func TestDo_invalidBaseURL(t *testing.T) {
	c := New("ftp://example.com", WithProviderName("test"))
	_, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheme must be http or https")
}

func TestDo_success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL, WithProviderName("test"))
	resp, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestDo_errorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer srv.Close()

	c := New(srv.URL, WithProviderName("myapi"))
	_, err := c.Do(context.Background(), "GET", "/missing", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "myapi")
	assert.Contains(t, err.Error(), "404")
	assert.Contains(t, err.Error(), "not found")
}

func TestDo_customErrorFormatter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden"))
	}))
	defer srv.Close()

	c := New(srv.URL,
		WithProviderName("custom"),
		WithErrorFormatter(func(provider, method, path string, code int, body []byte) error {
			return fmt.Errorf("CUSTOM: %s %d", provider, code)
		}),
	)
	_, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.Error(t, err)
	assert.Equal(t, "CUSTOM: custom 403", err.Error())
}

func TestDo_basicAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "myuser", user)
		assert.Equal(t, "mypass", pass)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL,
		WithAuth(BasicAuth("myuser", "mypass")),
		WithProviderName("test"),
	)
	resp, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestDo_bearerToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok123", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL,
		WithAuth(BearerToken("tok123")),
		WithProviderName("test"),
	)
	resp, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestDo_headerAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "secret", r.Header.Get("PRIVATE-TOKEN"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL,
		WithAuth(HeaderAuth("PRIVATE-TOKEN", "secret")),
		WithProviderName("test"),
	)
	resp, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestDo_extraHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "2022-06-28", r.Header.Get("Notion-Version"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL,
		WithHeader("Accept", "application/json"),
		WithHeader("Notion-Version", "2022-06-28"),
		WithProviderName("test"),
	)
	resp, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestDo_contentType_conditional(t *testing.T) {
	// No Content-Type when no body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if r.Method == "GET" {
			assert.Empty(t, ct, "GET with no body should not set Content-Type")
		} else {
			assert.Equal(t, "application/json", ct)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, WithProviderName("test"))

	resp, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()

	resp, err = c.Do(context.Background(), "POST", "/test", "", strings.NewReader(`{}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestDo_contentType_always(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL,
		WithContentType("application/json"),
		WithProviderName("test"),
	)

	// Even GET with no body should set Content-Type.
	resp, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestDoWithContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, WithProviderName("test"))
	resp, err := c.DoWithContentType(context.Background(), "PATCH", "/test", "", strings.NewReader(`[]`), "application/json-patch+json")
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestDo_queryParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/search", r.URL.Path)
		assert.Equal(t, "q=hello&limit=10", r.URL.RawQuery)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, WithProviderName("test"))
	resp, err := c.Do(context.Background(), "GET", "/api/search", "q=hello&limit=10", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestSetHTTPDoer(t *testing.T) {
	c := New("https://example.com", WithProviderName("test"))
	c.SetHTTPDoer(&errDoer{err: fmt.Errorf("injected")})

	_, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requesting test")
}

func TestDo_noAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, WithAuth(NoAuth()), WithProviderName("test"))
	resp, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func TestDo_displayNameFallback(t *testing.T) {
	c := New("https://example.com",
		WithHTTPDoer(&errDoer{err: fmt.Errorf("fail")}),
	)
	_, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api")
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		// Happy paths.
		{"valid https", "https://example.com", ""},
		{"valid http", "http://example.com", ""},
		{"https with path", "https://example.com/api/v1", ""},
		{"https with port", "https://example.com:8443", ""},

		// Scheme rejections — the primary SSRF guard.
		{"ftp scheme", "ftp://example.com", "scheme must be http or https"},
		{"file scheme", "file:///etc/passwd", "scheme must be http or https"},
		{"gopher scheme", "gopher://example.com", "scheme must be http or https"},
		{"javascript scheme", "javascript:alert(1)", "scheme must be http or https"},
		{"data scheme", "data:text/plain;base64,aGVsbG8=", "scheme must be http or https"},

		// Structure rejections.
		{"no host", "https://", "must have a host"},
		{"empty string", "", "scheme must be http or https"},
		{"malformed", "://no-scheme", "invalid URL"},

		// Current behavior, documented as such. ValidateURL does NOT
		// block loopback, private ranges, or embedded credentials today.
		// Extending it belongs in a dedicated security ticket since the
		// commit log is public.
		{"loopback v4 allowed today", "http://127.0.0.1", ""},
		{"loopback v6 allowed today", "http://[::1]", ""},
		{"private range allowed today", "http://10.0.0.1", ""},
		{"embedded creds allowed today", "https://user:pass@example.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateURL(tt.url)
			if tt.wantErr == "" {
				require.NoError(t, err, "url=%q", tt.url)
			} else {
				require.Error(t, err, "url=%q", tt.url)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestNew_defaultTimeout(t *testing.T) {
	c := New("https://example.com")
	hc, ok := c.http.(*http.Client)
	require.True(t, ok, "default http doer should be *http.Client")
	assert.Equal(t, DefaultTimeout, hc.Timeout)
}

func TestWithTimeout(t *testing.T) {
	c := New("https://example.com", WithTimeout(5*time.Second))
	hc, ok := c.http.(*http.Client)
	require.True(t, ok)
	assert.Equal(t, 5*time.Second, hc.Timeout)
}

func TestWithHTTPDoer_overridesTimeout(t *testing.T) {
	mock := &errDoer{err: fmt.Errorf("mock")}
	c := New("https://example.com", WithTimeout(5*time.Second), WithHTTPDoer(mock))
	assert.Equal(t, mock, c.http, "WithHTTPDoer should take precedence over WithTimeout")
}

func TestDo_timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, WithTimeout(50*time.Millisecond), WithProviderName("slow"))
	_, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requesting slow")
}

func TestDecodeJSON_success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"test","count":42}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	resp, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.NoError(t, err)

	var result struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	require.NoError(t, DecodeJSON(resp, &result))
	assert.Equal(t, "test", result.Name)
	assert.Equal(t, 42, result.Count)
}

func TestDecodeJSON_invalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	resp, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.NoError(t, err)

	var result map[string]string
	err = DecodeJSON(resp, &result, "endpoint", "/test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding response")
}

func TestDecodeJSON_closesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	resp, err := c.Do(context.Background(), "GET", "/test", "", nil)
	require.NoError(t, err)

	var result map[string]bool
	require.NoError(t, DecodeJSON(resp, &result))

	// Body should be closed — reading again should fail or return empty.
	buf := make([]byte, 1)
	_, readErr := resp.Body.Read(buf)
	assert.Error(t, readErr, "body should be closed after DecodeJSON")
}

// TestDoExec_PathSanitizerHidesSecretsOnTransportError ensures that when a
// path sanitizer is installed, sensitive values in the path do not leak
// into error messages or details on transport failure.
func TestDoExec_PathSanitizerHidesSecretsOnTransportError(t *testing.T) {
	c := New("https://example.com",
		WithProviderName("test"),
		WithHTTPDoer(&errDoer{err: fmt.Errorf("connection refused")}),
		WithPathSanitizer(func(p string) string {
			return strings.ReplaceAll(p, "SECRET_VALUE", "***")
		}),
	)

	_, err := c.Do(context.Background(), "GET", "/api/SECRET_VALUE/resource", "", nil)
	require.Error(t, err)

	details := errors.AllDetails(err)
	pathVal, _ := details["path"].(string)
	assert.NotContains(t, pathVal, "SECRET_VALUE", "raw secret must not appear in error path detail")
	assert.Contains(t, pathVal, "***", "sanitized path must appear in error details")
}

// TestDoExec_PathSanitizerHidesSecretsOnErrorStatus ensures sanitisation
// applies to the default non-2xx error path too.
func TestDoExec_PathSanitizerHidesSecretsOnErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL,
		WithProviderName("test"),
		WithPathSanitizer(func(p string) string {
			return strings.ReplaceAll(p, "SECRET_VALUE", "***")
		}),
	)

	_, err := c.Do(context.Background(), "GET", "/api/SECRET_VALUE/resource", "", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET_VALUE")
}

// TestDoExec_DoesNotFollowRedirects ensures the default HTTP client does
// not follow 30x responses, so custom headers are never replayed to a
// redirect target.
func TestDoExec_DoesNotFollowRedirects(t *testing.T) {
	var secondHit int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondHit = 1
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(second.Close)

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", second.URL+"/anything")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(first.Close)

	c := New(first.URL,
		WithProviderName("test"),
		WithHeader("X-Custom-Token", "leaked"),
	)
	_, err := c.Do(context.Background(), "GET", "/api/whatever", "", nil)
	// The 302 response is surfaced as a non-2xx error by Do. What matters is
	// that the second server never received a request with our header.
	require.Error(t, err)
	details := errors.AllDetails(err)
	statusCode, _ := details["statusCode"].(int)
	assert.Equal(t, http.StatusFound, statusCode, "Do must not follow the redirect")
	assert.Equal(t, int32(0), secondHit, "second server must never receive the request")
}

// TestDecodeJSON_RejectsOversizedBody verifies that DecodeJSON refuses
// to read a response body larger than MaxResponseBodyBytes.
func TestDecodeJSON_RejectsOversizedBody(t *testing.T) {
	// Build a response body that is exactly MaxResponseBodyBytes+1 bytes of
	// JSON-ish padding. It doesn't need to be valid JSON — the size check
	// must fire before json.Unmarshal runs.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Stream MaxResponseBodyBytes+1 bytes.
		chunk := strings.Repeat("a", 64*1024)
		written := 0
		for written <= MaxResponseBodyBytes {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			written += len(chunk)
		}
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, WithProviderName("test"))
	resp, err := c.Do(context.Background(), "GET", "/big", "", nil)
	require.NoError(t, err)

	var dest map[string]any
	err = DecodeJSON(resp, &dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds size limit")
}
