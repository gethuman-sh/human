package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// DefaultTimeout is the HTTP client timeout applied when no custom HTTPDoer is provided.
const DefaultTimeout = 30 * time.Second

// ValidateURL checks that rawURL is a valid HTTP(S) URL.
// This guards against SSRF by rejecting non-HTTP schemes.
// HTTP URLs are accepted but logged as insecure; prefer HTTPS.
func ValidateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return errors.WrapWithDetails(err, "invalid URL", "url", rawURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.WithDetails("URL scheme must be http or https", "url", rawURL, "scheme", u.Scheme)
	}
	if u.Host == "" {
		return errors.WithDetails("URL must have a host", "url", rawURL)
	}
	if u.Scheme == "http" {
		fmt.Fprintf(os.Stderr, "warning: using insecure HTTP for %s — credentials may be transmitted in plaintext\n", u.Host)
	}
	return nil
}

// ErrorFormatter formats an HTTP error response into an error value.
type ErrorFormatter func(providerName, method, path string, statusCode int, body []byte) error

// PathSanitizer transforms a request path before it is included in error
// details. Use it when the path may contain secrets that would otherwise
// appear verbatim in log output on transport or decode failures.
type PathSanitizer func(path string) string

// MaxResponseBodyBytes bounds the number of bytes DecodeJSON and
// DoGraphQL will read from a response body before returning an error.
// It's large enough for realistic JSON payloads but prevents an
// adversarial or misbehaving upstream from exhausting memory.
const MaxResponseBodyBytes = 50 * 1024 * 1024 // 50 MiB

// Client is a shared HTTP API client that handles URL construction,
// authentication, headers, and error handling.
// Client is not safe for concurrent modification. All configuration (including
// SetHTTPDoer) must be done before the first call to Do.
type Client struct {
	baseURL        string
	auth           AuthFunc
	urlBuilder     URLBuilder
	headers        map[string]string
	contentType    string // if set, always use this Content-Type; if empty, set "application/json" only when body != nil
	providerName   string
	errorFormatter ErrorFormatter
	pathSanitizer  PathSanitizer
	http           HTTPDoer
	timeout        time.Duration
}

// Option configures a Client.
type Option func(*Client)

// New creates a new API client with the given base URL and options.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:    baseURL,
		auth:       NoAuth(),
		urlBuilder: StandardURL(),
		headers:    make(map[string]string),
		timeout:    DefaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.http == nil {
		// Do not follow redirects by default. Custom headers (including
		// auth tokens) would otherwise be replayed to the redirect target,
		// and tracker APIs do not need redirect following for normal use.
		c.http = &http.Client{
			Timeout: c.timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return c
}

// WithAuth sets the authentication strategy.
func WithAuth(auth AuthFunc) Option {
	return func(c *Client) { c.auth = auth }
}

// WithURLBuilder sets the URL construction strategy.
func WithURLBuilder(ub URLBuilder) Option {
	return func(c *Client) { c.urlBuilder = ub }
}

// WithHeader adds a header to every request.
func WithHeader(name, value string) Option {
	return func(c *Client) { c.headers[name] = value }
}

// WithContentType sets a Content-Type header on every request, regardless of
// whether a body is present. When empty (the default), Content-Type is set to
// "application/json" only when a body is provided.
func WithContentType(ct string) Option {
	return func(c *Client) { c.contentType = ct }
}

// WithProviderName sets the provider name used in error messages.
func WithProviderName(name string) Option {
	return func(c *Client) { c.providerName = name }
}

// WithErrorFormatter sets a custom error formatter.
func WithErrorFormatter(ef ErrorFormatter) Option {
	return func(c *Client) { c.errorFormatter = ef }
}

// WithPathSanitizer installs a function that transforms the request path
// before it is included in error details. Providers that embed sensitive
// values into the URL path should supply a sanitizer so failure paths
// don't surface the raw value in log output.
func WithPathSanitizer(ps PathSanitizer) Option {
	return func(c *Client) { c.pathSanitizer = ps }
}

// WithTimeout sets the HTTP client timeout. Only effective when no custom
// HTTPDoer is provided via WithHTTPDoer.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithHTTPDoer sets the HTTP client used for requests.
func WithHTTPDoer(doer HTTPDoer) Option {
	return func(c *Client) { c.http = doer }
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer HTTPDoer) {
	c.http = doer
}

// Do executes an HTTP request with the client's configuration.
func (c *Client) Do(ctx context.Context, method, path, rawQuery string, body io.Reader) (*http.Response, error) {
	return c.doExec(ctx, method, path, rawQuery, body, "")
}

// DoWithContentType executes an HTTP request with an explicit Content-Type,
// overriding the client's default for this single request.
func (c *Client) DoWithContentType(ctx context.Context, method, path, rawQuery string, body io.Reader, contentType string) (*http.Response, error) {
	return c.doExec(ctx, method, path, rawQuery, body, contentType)
}

func (c *Client) doExec(ctx context.Context, method, path, rawQuery string, body io.Reader, contentTypeOverride string) (*http.Response, error) {
	if err := ValidateURL(c.baseURL); err != nil {
		return nil, err
	}

	base, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "parsing base URL", "baseURL", c.baseURL)
	}

	fullURL, err := c.urlBuilder(base, path, rawQuery)
	if err != nil {
		return nil, err
	}

	// safePath is the path value included in any error details. Error
	// formatters still receive the raw path so they can compose request-
	// specific messages; sanitizer-using callers must also sanitize within
	// their own formatter if they install one.
	safePath := path
	if c.pathSanitizer != nil {
		safePath = c.pathSanitizer(path)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "creating request",
			"method", method, "path", safePath)
	}

	c.auth(req)

	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	// Determine Content-Type.
	ct := contentTypeOverride
	if ct == "" {
		ct = c.contentType
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.WrapWithDetails(err, fmt.Sprintf("requesting %s", c.displayName()),
			"method", method, "path", safePath)
	}
	if resp == nil {
		return nil, errors.WithDetails(fmt.Sprintf("requesting %s: nil response", c.displayName()),
			"method", method, "path", safePath)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		if c.errorFormatter != nil {
			return nil, c.errorFormatter(c.providerName, method, path, resp.StatusCode, respBody)
		}
		return nil, errors.WithDetails(
			fmt.Sprintf("%s %s %s returned %d: %s", c.displayName(), method, safePath, resp.StatusCode, string(respBody)),
			"statusCode", resp.StatusCode, "method", method, "path", safePath)
	}
	return resp, nil
}

func (c *Client) displayName() string {
	if c.providerName != "" {
		return c.providerName
	}
	return "api"
}

// DecodeJSON reads and decodes a JSON response body into dest, then closes the
// body. Response bodies are capped at MaxResponseBodyBytes so an oversized or
// adversarial upstream cannot exhaust memory. The context args are passed to
// errors.WrapWithDetails on decode failure.
func DecodeJSON(resp *http.Response, dest interface{}, contextArgs ...interface{}) error {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBodyBytes+1))
	if err != nil {
		return errors.WrapWithDetails(err, "reading response body", contextArgs...)
	}
	if int64(len(body)) > MaxResponseBodyBytes {
		return errors.WithDetails(
			fmt.Sprintf("response body exceeds size limit of %d bytes", MaxResponseBodyBytes),
			contextArgs...)
	}
	if err := json.Unmarshal(body, dest); err != nil {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return errors.WithDetails(
			fmt.Sprintf("decoding response: %s (body: %s)", err, snippet),
			contextArgs...)
	}
	return nil
}
