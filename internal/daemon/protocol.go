package daemon

import client "github.com/gethuman-sh/human-daemon-client"

// TrackerIssuesResult is the wire type for a single tracker/project's issues.
//
// ReadyForReview carries the engineering ticket keys that a PM tracker has
// currently flagged for review via a [human:ready-for-review] comment. It is
// populated on engineering-tracker results (where the keys actually live) so
// the TUI can join it against Issues without a separate lookup. See
// cli/CLAUDE.md "Review handoff" for the comment convention.
//
// The wire types live in the public human-daemon-client contract module so the
// daemon and every client serialize literally the same structs; the daemon
// aliases them here so existing core callers compile unchanged.
type TrackerIssuesResult = client.TrackerIssuesResult

// Request is sent from the client to the daemon (one JSON line per connection).
type Request = client.Request

// Response is sent from the daemon back to the client (one or more JSON lines per connection).
type Response = client.Response

// SubscribeEvent is a notification sent over a persistent subscribe connection.
type SubscribeEvent = client.SubscribeEvent

// PendingConfirm is the wire type for a single pending destructive operation
// awaiting user confirmation via the TUI.
type PendingConfirm = client.PendingConfirm
