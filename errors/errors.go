package errors

import (
	stderrors "errors"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	goerrors "gitlab.com/tozd/go/errors"
)

func LogError(err error) *zerolog.Event {
	// tozd's Error() and %+v only expose the outermost wrap message, so the root
	// cause (e.g. a Docker connection failure) is invisible to users unless we
	// render the full chain ourselves.
	return log.Error().Str("error", CauseChain(err)).Fields(AllDetails(err))
}

// CauseChain renders the full unwrapped error chain as "outer: inner: root".
// Wrappers like WrapWithDetails report the same message at multiple chain
// levels; those consecutive duplicates are collapsed so the output stays
// readable. Returns "" for a nil error.
func CauseChain(err error) string {
	if err == nil {
		return ""
	}
	var b strings.Builder
	var last string
	for e := err; e != nil; e = stderrors.Unwrap(e) {
		msg := e.Error()
		if msg == "" || msg == last {
			continue
		}
		last = msg
		if b.Len() == 0 {
			b.WriteString(msg)
			continue
		}
		// A leaf error built with fmt.Errorf("%w") already embeds its cause as a
		// suffix; don't append it twice.
		if strings.HasSuffix(b.String(), msg) {
			continue
		}
		b.WriteString(": ")
		b.WriteString(msg)
	}
	return b.String()
}

func WithDetails(message string, details ...interface{}) error {
	args := extractArgs(message, details)
	return goerrors.WithDetails(
		goerrors.Errorf(message, args...),
		details...,
	)
}

func WrapWithDetails(err error, message string, details ...interface{}) error {
	args := extractArgs(message, details)
	return goerrors.WithDetails(
		goerrors.Wrapf(err, message, args...),
		details...,
	)
}

// AllDetails collects structured details from every level of the error chain,
// walking past causer wraps that tozd's own AllDetails halts at. First writer
// wins so an outer wrap's value shadows an inner one on key collision, matching
// tozd's within-scope merge semantics.
func AllDetails(err error) map[string]interface{} {
	res := make(map[string]interface{})
	type detailer interface{ Details() map[string]interface{} }
	for e := err; e != nil; e = stderrors.Unwrap(e) {
		d, ok := e.(detailer)
		if !ok {
			continue
		}
		for key, value := range d.Details() {
			if _, exists := res[key]; !exists {
				res[key] = value
			}
		}
	}
	return res
}

func isFormatVerb(c byte) bool {
	switch c {
	case 'b', 'c', 'd', 'e', 'E', 'f', 'F', 'g', 'G', 'o', 'O', 'p', 'q', 's', 't', 'T', 'U', 'v', 'w', 'x', 'X':
		return true
	}
	return false
}

func extractArgs(message string, details []interface{}) []interface{} {
	var args []interface{}
	for i := 1; i < len(details); i += 2 {
		args = append(args, details[i])
	}

	placeholderCount := 0
	for i := 0; i < len(message); i++ {
		if message[i] == '%' && i+1 < len(message) {
			if message[i+1] == '%' {
				i++ // skip escaped %%
			} else if isFormatVerb(message[i+1]) {
				placeholderCount++
			}
		}
	}

	if len(args) > placeholderCount {
		args = args[:placeholderCount]
	}
	return args
}
