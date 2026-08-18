// Package agentname owns the name the daemon gives a board stage agent, and
// the grammar that name is written in.
//
// It is a package of its own because two packages that cannot import each
// other both have to agree on it. internal/daemon composes the name when it
// launches a stage and parses it back when the stage exits; internal/capabilities
// reads the same prefix to decide what a run inside that container may do, and
// cannot import internal/daemon. Before this, each held its own "board-"
// literal and a comment in one pointed at the other.
//
// The grammar is also read outside Go: the autofix and security-fix skill
// prompts treat a HUMAN_AGENT_NAME starting with the prefix as a fallback
// board-context signal. A prompt cannot import anything, so those readers stay
// separate — but the Go side now has one place for them to be true about.
package agentname

import (
	"regexp"
	"strings"
)

// BoardPrefix marks a container as a board stage agent. It is the whole of the
// board signal: a run whose name carries it holds no push credentials, because
// the board's Deploy stage is what ships the work.
const BoardPrefix = "board-"

// IsBoard reports whether name is a board stage agent's name.
func IsBoard(name string) bool {
	return strings.HasPrefix(name, BoardPrefix)
}

// sanitizeRe drops characters that are invalid in an agent name (alphanumeric,
// hyphen, underscore only) so a PM key like "SC-105" maps to a valid,
// reversible name.
var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// SanitizeKey renders a ticket key the way it appears inside an agent name.
// Callers that compare a live agent against a ticket need it too: matching a
// raw key against a name would miss every key carrying a character the name
// encoding replaced.
func SanitizeKey(key string) string {
	return sanitizeRe.ReplaceAllString(key, "-")
}

// Board builds the name for a stage agent working key. It is reversible by
// ParseBoard, which is what lets an exit event be traced back to the work it
// was for.
//
// The stage token must carry no hyphen: ParseBoard splits on the LAST one, so
// a hyphenated token would take part of itself for the key. Callers keep their
// public, hyphenated spellings elsewhere and pass the hyphen-free token here.
func Board(key, stage string) string {
	return BoardPrefix + SanitizeKey(key) + "-" + stage
}

// ParseBoard recovers the key and stage from a board agent name. The key comes
// back sanitized — the form embedded in the name — which is what a caller
// needs to match it against tickets it already fetched.
func ParseBoard(name string) (key, stage string, ok bool) {
	rest, found := strings.CutPrefix(name, BoardPrefix)
	if !found {
		return "", "", false
	}
	idx := strings.LastIndex(rest, "-")
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// AuxPrefixes are the daemon-launched runs that work on ONE ticket without
// being a board stage: they carry no stage of their own, so the prefix IS the
// stage as far as accounting is concerned. Listed longest-first so a prefix
// that is a prefix of another cannot win the wrong match.
var AuxPrefixes = []string{"idea-draft", "relate"}

// Aux builds the name for an auxiliary per-ticket run.
func Aux(prefix, key string) string { return prefix + "-" + SanitizeKey(key) }

// ParseAux recovers the key and the prefix-as-stage from an auxiliary run's
// name. It deliberately answers for a CLOSED list rather than any hyphenated
// name: an unknown name must stay unattributed, not be decoded into a ticket
// key that does not exist.
func ParseAux(name string) (key, stage string, ok bool) {
	for _, p := range AuxPrefixes {
		if rest, found := strings.CutPrefix(name, p+"-"); found && rest != "" {
			return rest, p, true
		}
	}
	return "", "", false
}
