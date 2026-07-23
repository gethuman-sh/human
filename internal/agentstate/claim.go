package agentstate

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// Claim takes or refreshes an agent's hold on one stage of a scope.
//
// The takeover path is the point of the whole table: when an agent dies
// mid-stage its claim stops being heartbeated, and once the TTL lapses a fresh
// agent may claim the stage. The result then names the agent it displaced and
// the state keys that agent left behind, so the successor resumes from what was
// already learned instead of starting the stage over.
func (s *SQLiteStore) Claim(ctx context.Context, req ClaimRequest) (ClaimResult, error) {
	normScope, err := s.validateClaimRequest(&req)
	if err != nil {
		return ClaimResult{}, err
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = DefaultClaimTTL
	}
	now := s.now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClaimResult{}, errors.WrapWithDetails(err, "begin claim transaction")
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := readClaim(ctx, tx, normScope, req.Stage)
	if err != nil {
		return ClaimResult{}, err
	}
	if found && blocksClaim(existing, req, now) {
		return ClaimResult{Granted: false, Claim: existing}, nil
	}

	granted := Claim{
		Scope:       normScope,
		Stage:       req.Stage,
		Agent:       req.Meta.Agent,
		RunID:       req.Meta.RunID,
		TTL:         ttl,
		ClaimedAt:   claimStart(existing, found, req.Meta.Agent, now),
		HeartbeatAt: now,
	}
	if err := writeClaim(ctx, tx, granted); err != nil {
		return ClaimResult{}, err
	}

	result := ClaimResult{Granted: true, Claim: granted}
	if displaced, ok := displacedBy(existing, found, req.Meta.Agent); ok {
		result.Displaced = &displaced
		keys, err := inheritedKeys(ctx, tx, normScope, displaced.Agent)
		if err != nil {
			return ClaimResult{}, err
		}
		result.InheritedKeys = keys
	}

	if err := tx.Commit(); err != nil {
		return ClaimResult{}, errors.WrapWithDetails(err, "commit claim transaction")
	}
	return result, nil
}

// validateClaimRequest normalises the scope and rejects a claim that could not
// be attributed — an anonymous claim could never be taken over safely.
func (s *SQLiteStore) validateClaimRequest(req *ClaimRequest) (string, error) {
	normScope, err := NormalizeScope(req.Scope)
	if err != nil {
		return "", err
	}
	if err := ValidateStage(req.Stage); err != nil {
		return "", err
	}
	if req.Meta.Agent == "" {
		return "", errors.WithDetails("claim requires an agent name", "scope", normScope, "stage", req.Stage)
	}
	return normScope, nil
}

// blocksClaim reports whether an existing claim stands in the way: only a live
// claim held by a different agent does, and an explicit takeover overrides it.
func blocksClaim(existing Claim, req ClaimRequest, now time.Time) bool {
	if req.Takeover || existing.Agent == req.Meta.Agent {
		return false
	}
	return isLive(existing, now)
}

// isLive reports whether a claim is still held: not released, and heartbeated
// within the TTL the holder itself declared.
func isLive(c Claim, now time.Time) bool {
	if c.ReleasedAt != nil {
		return false
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = DefaultClaimTTL
	}
	return now.Sub(c.HeartbeatAt) <= ttl
}

// claimStart preserves the original claim time when the same agent heartbeats,
// so stage duration measures the work rather than the last heartbeat.
func claimStart(existing Claim, found bool, agent string, now time.Time) time.Time {
	if found && existing.Agent == agent && existing.ReleasedAt == nil {
		return existing.ClaimedAt
	}
	return now
}

// displacedBy reports the previous holder when a different agent takes the
// stage over. A released claim is not a displacement — its agent handed off.
func displacedBy(existing Claim, found bool, agent string) (Claim, bool) {
	if !found || existing.Agent == agent || existing.ReleasedAt != nil {
		return Claim{}, false
	}
	return existing, true
}

// Release marks a stage claim as handed back. When agent is non-empty the
// release only applies to that agent's claim, so a stale process cannot release
// its successor's hold.
func (s *SQLiteStore) Release(ctx context.Context, scope, stage, agent string) (bool, error) {
	normScope, err := NormalizeScope(scope)
	if err != nil {
		return false, err
	}
	if err := ValidateStage(stage); err != nil {
		return false, err
	}

	q := `UPDATE agent_claims SET released_at = ? WHERE scope = ? AND stage = ? AND released_at IS NULL`
	args := []any{s.now().UTC().Format(TimeFormat), normScope, stage}
	if agent != "" {
		q += ` AND agent = ?`
		args = append(args, agent)
	}

	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, errors.WrapWithDetails(err, "release claim", "scope", normScope, "stage", stage)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Claims lists every claim on a scope, newest heartbeat first, so an operator
// can see who holds what.
func (s *SQLiteStore) Claims(ctx context.Context, scope string) ([]Claim, error) {
	normScope, err := NormalizeScope(scope)
	if err != nil {
		return nil, err
	}
	const q = `
		SELECT scope, stage, agent, run_id, ttl_seconds, claimed_at, heartbeat_at, released_at
		FROM agent_claims WHERE scope = ? ORDER BY heartbeat_at DESC
	`
	rows, err := s.db.QueryContext(ctx, q, normScope)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "list claims", "scope", normScope)
	}
	defer func() { _ = rows.Close() }()

	claims := []Claim{}
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		claims = append(claims, c)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "read claims", "scope", normScope)
	}
	return claims, nil
}

func readClaim(ctx context.Context, tx *sql.Tx, scope, stage string) (Claim, bool, error) {
	const q = `
		SELECT scope, stage, agent, run_id, ttl_seconds, claimed_at, heartbeat_at, released_at
		FROM agent_claims WHERE scope = ? AND stage = ?
	`
	c, err := scanClaim(tx.QueryRowContext(ctx, q, scope, stage))
	if err != nil {
		if stderrors.Is(err, ErrNotFound) {
			return Claim{}, false, nil
		}
		return Claim{}, false, err
	}
	return c, true, nil
}

func writeClaim(ctx context.Context, tx *sql.Tx, c Claim) error {
	const q = `
		INSERT INTO agent_claims (scope, stage, agent, run_id, ttl_seconds, claimed_at, heartbeat_at, released_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(scope, stage) DO UPDATE SET
			agent        = excluded.agent,
			run_id       = excluded.run_id,
			ttl_seconds  = excluded.ttl_seconds,
			claimed_at   = excluded.claimed_at,
			heartbeat_at = excluded.heartbeat_at,
			released_at  = NULL
	`
	_, err := tx.ExecContext(ctx, q, c.Scope, c.Stage, c.Agent, c.RunID, int64(c.TTL/time.Second),
		c.ClaimedAt.Format(TimeFormat), c.HeartbeatAt.Format(TimeFormat))
	if err != nil {
		return errors.WrapWithDetails(err, "write claim", "scope", c.Scope, "stage", c.Stage)
	}
	return nil
}

// inheritedKeys lists the state a displaced agent left behind in the scope —
// the successor's inheritance.
func inheritedKeys(ctx context.Context, tx *sql.Tx, scope, agent string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT name FROM agent_state WHERE scope = ? AND agent = ? ORDER BY name`, scope, agent)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "list inherited keys", "scope", scope, "agent", agent)
	}
	defer func() { _ = rows.Close() }()

	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, errors.WrapWithDetails(err, "scan inherited key")
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "read inherited keys", "scope", scope)
	}
	return names, nil
}

func scanClaim(sc rowScanner) (Claim, error) {
	var c Claim
	var claimedAt, heartbeatAt string
	var releasedAt sql.NullString
	var ttlSeconds int64
	if err := sc.Scan(&c.Scope, &c.Stage, &c.Agent, &c.RunID, &ttlSeconds,
		&claimedAt, &heartbeatAt, &releasedAt); err != nil {
		if isNoRows(err) {
			return Claim{}, ErrNotFound
		}
		return Claim{}, errors.WrapWithDetails(err, "scan claim")
	}
	c.TTL = time.Duration(ttlSeconds) * time.Second

	var err error
	if c.ClaimedAt, err = time.Parse(TimeFormat, claimedAt); err != nil {
		return Claim{}, errors.WrapWithDetails(err, "parse claim timestamp", "value", claimedAt)
	}
	if c.HeartbeatAt, err = time.Parse(TimeFormat, heartbeatAt); err != nil {
		return Claim{}, errors.WrapWithDetails(err, "parse heartbeat timestamp", "value", heartbeatAt)
	}
	if releasedAt.Valid {
		ts, parseErr := time.Parse(TimeFormat, releasedAt.String)
		if parseErr != nil {
			return Claim{}, errors.WrapWithDetails(parseErr, "parse release timestamp", "value", releasedAt.String)
		}
		c.ReleasedAt = &ts
	}
	return c, nil
}

// isNoRows recognises the driver's empty-result error through any wrapping.
func isNoRows(err error) bool {
	return stderrors.Is(err, sql.ErrNoRows)
}
