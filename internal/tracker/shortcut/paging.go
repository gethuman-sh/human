package shortcut

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// maxSearchPages bounds how far a paged search will follow next-links, so a
// malformed or looping cursor cannot spin forever.
const maxSearchPages = 50

// scStoryPage is one page of stories.
//
// Shortcut's story endpoints answer in two shapes: a bare JSON array, and an
// envelope carrying a cursor. Both are decoded here rather than assuming one,
// because guessing wrong is silent — a bare array decoded as an envelope yields
// zero stories and reads as an empty backlog.
type scStoryPage struct {
	Stories []scStory
	// Next is the cursor URL for the following page, empty at the end. Only the
	// envelope form has one; a bare array is complete by construction of the
	// response, which is not the same as complete by construction of the DATA —
	// see decodeStoryPage.
	Next string
	// Enveloped records which shape came back. A bare array carries no cursor,
	// so it cannot tell us whether the server truncated it, and callers must not
	// read its lack of a Next as proof there is nothing more.
	Enveloped bool
}

// decodeStoryPage reads either response shape.
func decodeStoryPage(raw []byte) (scStoryPage, error) {
	trimmed := strings.TrimLeft(string(raw), " \t\r\n")
	if strings.HasPrefix(trimmed, "[") {
		var stories []scStory
		if err := json.Unmarshal(raw, &stories); err != nil {
			return scStoryPage{}, errors.WrapWithDetails(err, "decoding story array")
		}
		return scStoryPage{Stories: stories}, nil
	}
	var envelope struct {
		Data []scStory `json:"data"`
		Next string    `json:"next"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return scStoryPage{}, errors.WrapWithDetails(err, "decoding story page")
	}
	return scStoryPage{Stories: envelope.Data, Next: envelope.Next, Enveloped: true}, nil
}

// nextPathAndQuery splits a cursor URL into the path and query the API client
// takes. Shortcut returns next as a site-relative URL; an absolute one is
// accepted too so a changed representation does not silently end pagination.
func nextPathAndQuery(next string) (path, rawQuery string, ok bool) {
	next = strings.TrimSpace(next)
	if next == "" {
		return "", "", false
	}
	u, err := url.Parse(next)
	if err != nil || u.Path == "" {
		return "", "", false
	}
	return u.Path, u.RawQuery, true
}
