package apiclient

import (
	"net/url"

	"github.com/gethuman-sh/human/errors"
)

// URLBuilder constructs a full URL from a parsed base URL, path, and raw query string.
type URLBuilder func(base *url.URL, path, rawQuery string) (string, error)

// StandardURL returns a URLBuilder that sets u.Path and u.RawQuery directly.
// Used by Jira, GitHub, Azure DevOps, Shortcut, and Amplitude.
func StandardURL() URLBuilder {
	return func(base *url.URL, path, rawQuery string) (string, error) {
		u := *base
		u.Path = path
		u.RawQuery = rawQuery
		return u.String(), nil
	}
}

// RawPathURL returns a URLBuilder that also sets u.RawPath to preserve
// percent-encoding in paths. Used by GitLab where project paths contain
// encoded slashes (e.g. "mygroup%2Fmyproject").
func RawPathURL() URLBuilder {
	return func(base *url.URL, path, rawQuery string) (string, error) {
		u := *base
		decodedPath, _ := url.PathUnescape(path)
		u.Path = decodedPath
		u.RawPath = path
		u.RawQuery = rawQuery
		return u.String(), nil
	}
}

// ParsePathURL returns a URLBuilder that parses the path as a URL to extract
// embedded query parameters. Used by Notion, Figma, and Telegram where the
// path may include query strings (e.g. "/v1/files/key?depth=1").
func ParsePathURL() URLBuilder {
	return func(base *url.URL, path, rawQuery string) (string, error) {
		u := *base
		parsedPath, err := url.Parse(path)
		if err != nil {
			return "", errors.WrapWithDetails(err, "parsing path", "path", path)
		}
		u.Path = parsedPath.Path
		// Use rawQuery if provided, otherwise use what was embedded in the path.
		if rawQuery != "" {
			u.RawQuery = rawQuery
		} else {
			u.RawQuery = parsedPath.RawQuery
		}
		return u.String(), nil
	}
}
